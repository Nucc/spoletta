package main

// Claude Code itself, the version the images are built with, run as a step
// runs it, against a fake of the model's API: what the worker gives it is
// what that version takes, and what it prints is what claude_stream.go
// reads, on Anthropic's API and on Bedrock's. It runs only where
// CLAUDE_CODE_TEST=real says the claude on PATH is the one to test, as CI's
// claude-code job runs it, in the steps' image with no network; never just
// because a claude is found, which on a developer's machine is their own.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// What reached the fake model API.
type modelCall struct {
	method, path, apiKey, auth string
	// Whether it offered the agent's tools, and the texts of its messages.
	tools bool
	texts []string
}

// A fake of Anthropic's Messages API, or Bedrock's InvokeModel, answering
// the agent's turns from a script and anything else, such as Claude Code's
// own calls to a small model, with a line of text.
type fakeModelAPI struct {
	*httptest.Server
	bedrock bool
	// The agent's working directory, where the script's files are.
	cwd string

	mu    sync.Mutex
	calls []modelCall
	// Each message's ID is its own, as Claude Code puts the blocks of one
	// ID together into one message.
	answered int
}

func newFakeModelAPI(t *testing.T, bedrock bool) *fakeModelAPI {
	f := &fakeModelAPI{bedrock: bedrock}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeModelAPI) seen() []modelCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

var bedrockInvoke = regexp.MustCompile(`^/llm/[^/]+/model/([^/]+)/(invoke-with-response-stream|invoke)$`)

func (f *fakeModelAPI) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	json.Unmarshal(raw, &body)
	call := modelCall{method: r.Method, path: r.URL.Path, apiKey: r.Header.Get("X-Api-Key"), auth: r.Header.Get("Authorization")}
	var outTool string
	for _, t := range body.Tools {
		call.tools = call.tools || t.Name == "Bash"
		if strings.HasSuffix(t.Name, "__submit_output") {
			outTool = t.Name
		}
	}
	assistants := 0
	for _, m := range body.Messages {
		call.texts = append(call.texts, contentText(m.Content))
		if m.Role == "assistant" {
			assistants++
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.answered++
	id := fmt.Sprintf("msg_fake%04d", f.answered)
	f.mu.Unlock()

	invoke := bedrockInvoke.FindStringSubmatch(r.URL.Path)
	switch {
	case r.Method != http.MethodPost:
		writeJSONBody(w, map[string]any{})
		return
	case f.bedrock && invoke == nil, !f.bedrock && !strings.HasSuffix(r.URL.Path, "/v1/messages"):
		writeJSONBody(w, map[string]any{"input_tokens": 100})
		return
	}
	model := body.Model
	if invoke != nil {
		model = invoke[1]
	}
	blocks, stop := []map[string]any{{"type": "text", "text": "ok"}}, "end_turn"
	last := ""
	if n := len(call.texts); n > 0 {
		last = call.texts[n-1]
	}
	switch {
	case !call.tools:
	case strings.Contains(last, "Push it."):
		blocks = []map[string]any{{"type": "text", "text": "Pushed."}}
	default:
		blocks, stop = f.turn(assistants, outTool)
	}
	events := messageEvents(id, model, blocks, stop)
	switch {
	case invoke != nil && invoke[2] == "invoke-with-response-stream":
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, e := range events {
			inner, _ := json.Marshal(e)
			payload, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString(inner)})
			w.Write(eventFrame(map[string]string{":event-type": "chunk", ":content-type": "application/json", ":message-type": "event"}, payload))
		}
	case body.Stream:
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			data, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], data)
		}
	default:
		writeJSONBody(w, map[string]any{"id": id, "type": "message", "role": "assistant", "model": model, "content": blocks,
			"stop_reason": stop, "usage": map[string]int{"input_tokens": 10, "output_tokens": 5}})
	}
}

// The agent's turns: it writes a file with bash, reads it, edits it, writes
// another, hands in its output wrongly, then rightly, and says it is done.
func (f *fakeModelAPI) turn(k int, outTool string) ([]map[string]any, string) {
	use := func(name string, input map[string]any) map[string]any {
		return map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%02d", k), "name": name, "input": input}
	}
	note := filepath.Join(f.cwd, "note.txt")
	switch k {
	case 0:
		return []map[string]any{{"type": "thinking", "thinking": "Look around first."}, {"type": "text", "text": "Writing a note."},
			use("Bash", map[string]any{"command": "echo hi > note.txt && ls", "description": "Write a note"})}, "tool_use"
	case 1:
		return []map[string]any{use("Read", map[string]any{"file_path": note})}, "tool_use"
	case 2:
		return []map[string]any{use("Edit", map[string]any{"file_path": note, "old_string": "hi", "new_string": "hello"})}, "tool_use"
	case 3:
		return []map[string]any{use("Write", map[string]any{"file_path": filepath.Join(f.cwd, "other.txt"), "content": "other\n"})}, "tool_use"
	case 4:
		return []map[string]any{use(outTool, map[string]any{"text": "all done", "count": "two"})}, "tool_use"
	case 5:
		return []map[string]any{use(outTool, map[string]any{"text": "all done", "count": 2})}, "tool_use"
	}
	return []map[string]any{{"type": "text", "text": "Handed in the summary."}}, "end_turn"
}

// A message's text, its blocks' texts and tool results' joined.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	json.Unmarshal(raw, &blocks)
	var texts []string
	for _, b := range blocks {
		texts = append(texts, b.Text)
		if len(b.Content) > 0 {
			texts = append(texts, contentText(b.Content))
		}
	}
	return strings.Join(texts, " ")
}

// A message streamed as Anthropic's API streams it, a block at a time.
func messageEvents(id, model string, blocks []map[string]any, stop string) []map[string]any {
	events := []map[string]any{{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model, "content": []any{}, "stop_reason": nil,
		"usage": map[string]int{"input_tokens": 100, "output_tokens": 1}}}}
	for i, b := range blocks {
		switch b["type"] {
		case "text":
			events = append(events,
				map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text", "text": ""}},
				map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": b["text"]}})
		case "thinking":
			events = append(events,
				map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "thinking", "thinking": "", "signature": ""}},
				map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "thinking_delta", "thinking": b["thinking"]}},
				map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "signature_delta", "signature": "c2ln"}})
		case "tool_use":
			input, _ := json.Marshal(b["input"])
			events = append(events,
				map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "tool_use", "id": b["id"], "name": b["name"], "input": map[string]any{}}},
				map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		}
		events = append(events, map[string]any{"type": "content_block_stop", "index": i})
	}
	return append(events,
		map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop}, "usage": map[string]int{"output_tokens": 42}},
		map[string]any{"type": "message_stop"})
}

// A frame of AWS's event stream: its prelude, headers and payload, each
// checked by a CRC32.
func eventFrame(headers map[string]string, payload []byte) []byte {
	var hs bytes.Buffer
	for _, k := range []string{":event-type", ":content-type", ":message-type"} {
		hs.WriteByte(byte(len(k)))
		hs.WriteString(k)
		hs.WriteByte(7)
		binary.Write(&hs, binary.BigEndian, uint16(len(headers[k])))
		hs.WriteString(headers[k])
	}
	var frame bytes.Buffer
	binary.Write(&frame, binary.BigEndian, uint32(12+hs.Len()+len(payload)+4))
	binary.Write(&frame, binary.BigEndian, uint32(hs.Len()))
	binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(frame.Bytes()))
	frame.Write(hs.Bytes())
	frame.Write(payload)
	binary.Write(&frame, binary.BigEndian, crc32.ChecksumIEEE(frame.Bytes()))
	return frame.Bytes()
}

func writeJSONBody(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func TestRealClaudeCode(t *testing.T) {
	if os.Getenv("CLAUDE_CODE_TEST") != "real" {
		t.Skip("CLAUDE_CODE_TEST isn't real: it runs the claude on PATH, which CI's claude-code job has in the steps' image")
	}
	for _, c := range []struct {
		name, model, provider string
		bedrock               bool
	}{
		{"Anthropic's API", "anthropic/claude-sonnet-4-6", "anthropic", false},
		{"Bedrock", "amazon-bedrock/global.anthropic.claude-sonnet-5-5", "amazon-bedrock", true},
	} {
		t.Run(c.name, func(t *testing.T) { realClaudeStep(t, c.model, c.provider, c.bedrock) })
	}
}

// A step running with Claude Code and its follow-up, on the provider.
func realClaudeStep(t *testing.T, model, provider string, bedrock bool) {
	steps, workspaces := stepsRoot, workspacesRoot
	stepsRoot, workspacesRoot = t.TempDir(), t.TempDir()
	t.Cleanup(func() { stepsRoot, workspacesRoot = steps, workspaces })
	api := newFakeModelAPI(t, bedrock)
	api.cwd = filepath.Join(workspacesRoot, "run-1")

	run := runRequest{
		Harness: "claude", Prompt: "Write a note, then hand in a summary.", Model: model, Thinking: "high",
		LLM:       &llmAccess{URL: api.URL + "/llm/", Token: stepToken, Providers: []string{provider}},
		SessionID: "0123abcd456789ef0123456789abcdef", Workspace: &workspaceSpec{ID: "run-1", Branch: "flow/run-1", Run: 1, Step: 1},
		Output: &outputSpec{Shape: "object", Objects: json.RawMessage(`[{"name":"Summary","many":false,"attributes":[` +
			`{"name":"text","type":"text","description":"","file":false},{"name":"count","type":"integer","description":"","file":false}]}]`)},
		Skills: []skill{{Name: "notes", Description: "How notes are written", Content: "Short."}},
	}
	var lines []logLine
	l := newStepLogger("test", func(line logLine) { lines = append(lines, line) }).hiding(run.tokens())
	var messages []map[string]any
	emit := func(m json.RawMessage) {
		var msg map[string]any
		json.Unmarshal(m, &msg)
		messages = append(messages, msg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res := runAgent(ctx, claudeHarness{}, run, 90*time.Second, l, emit)
	log := describeLog(lines)
	if !res.OK || !res.Submitted {
		t.Fatalf("the step: %+v\nits log:\n%s\nthe model API was called:\n%s\nthe conversation: %v", res, log, describeCalls(api.seen()), messages)
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(res.Output), &output); err != nil || output["text"] != "all done" || output["count"] != float64(2) {
		t.Errorf("handed in %q", res.Output)
	}

	// The conversation, in pi's shape and tool names: each call, the bad
	// hand-in refused, and the edit's patch.
	var calls []string
	refused, patched := false, false
	for _, m := range messages {
		switch m["role"] {
		case "assistant":
			content, _ := m["content"].([]any)
			for _, b := range content {
				if b, _ := b.(map[string]any); b["type"] == "toolCall" {
					calls = append(calls, fmt.Sprint(b["name"]))
				}
			}
		case "toolResult":
			if m["toolName"] == "mcp__flow_output__submit_output" && m["isError"] == true {
				refused = true
			}
			if d, _ := m["details"].(map[string]any); m["toolName"] == "edit" && strings.Contains(fmt.Sprint(d["patch"]), "+hello") {
				patched = true
			}
		}
	}
	if want := []string{"bash", "read", "edit", "write", "mcp__flow_output__submit_output", "mcp__flow_output__submit_output"}; !slices.Equal(calls, want) {
		t.Errorf("the calls were %v, want %v", calls, want)
	}
	if !refused || !patched {
		t.Errorf("the bad hand-in refused: %v; the edit's patch: %v; messages %v", refused, patched, messages)
	}
	if b, err := os.ReadFile(filepath.Join(api.cwd, "note.txt")); err != nil || string(b) != "hello\n" {
		t.Errorf("the note holds %q, %v", b, err)
	}
	for _, w := range []string{"Claude Code 2.", "MCP servers connected: flow_output", "Claude Code answered after"} {
		if !strings.Contains(log, w) {
			t.Errorf("no %q in the log:\n%s", w, log)
		}
	}
	for _, line := range lines {
		if line.Level == "error" {
			t.Errorf("the log has an error: %s", line.Message)
		}
	}

	// It reached its model only with the step's token, as its API key or as
	// a Bedrock API key.
	calls1 := api.seen()
	invoked := false
	for _, c := range calls1 {
		if bedrock {
			invoked = invoked || bedrockInvoke.MatchString(c.path)
			if c.auth != "Bearer "+stepToken || c.apiKey != "" || !strings.HasPrefix(c.path, "/llm/amazon-bedrock/") {
				t.Errorf("%s %s with the key %q and authorization %q", c.method, c.path, c.apiKey, c.auth)
			}
		} else {
			invoked = invoked || c.path == "/llm/anthropic/v1/messages"
			if c.apiKey != stepToken || c.auth != "" || !strings.HasPrefix(c.path, "/llm/anthropic/") {
				t.Errorf("%s %s with the key %q and authorization %q", c.method, c.path, c.apiKey, c.auth)
			}
		}
	}
	if !invoked {
		t.Errorf("the model was never called: %+v", calls1)
	}

	// Its follow-up carries the session on, sending the conversation so far.
	run.Prompt, run.Resume, run.Output = "Push it.", true, nil
	messages, lines = nil, nil
	res = runAgent(ctx, claudeHarness{}, run, 90*time.Second, l, emit)
	if !res.OK || res.Output != "Pushed." {
		t.Fatalf("the follow-up: %+v\nits log:\n%s", res, describeLog(lines))
	}
	carried := false
	for _, c := range api.seen()[len(calls1):] {
		if c.tools && len(c.texts) > 2 && strings.Contains(c.texts[0], "Write a note") && strings.Contains(c.texts[len(c.texts)-1], "Push it.") {
			carried = true
		}
	}
	if !carried {
		t.Errorf("the follow-up didn't send the conversation so far: %+v", api.seen()[len(calls1):])
	}
}

func describeCalls(calls []modelCall) string {
	var b strings.Builder
	for _, c := range calls {
		last := ""
		if len(c.texts) > 0 {
			last = c.texts[len(c.texts)-1]
		}
		fmt.Fprintf(&b, "%s %s tools=%v messages=%d last=%.120q\n", c.method, c.path, c.tools, len(c.texts), last)
	}
	return b.String()
}

func describeLog(lines []logLine) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s %s\n", l.Level, l.Message)
	}
	return b.String()
}
