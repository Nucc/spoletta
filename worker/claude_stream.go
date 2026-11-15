package main

// Reads Claude Code's stream-json output and gives the conversation in pi's
// message shape, which the webapp stores and its Terminal shows, so both
// harnesses' conversations read the same: the prompt, each reply of the
// model with its thinking and tool calls, and each tool's result.
//
// Claude Code prints a JSON event per line. Each content block of a reply
// comes as an assistant event of its own, the blocks of one reply sharing
// its id, and a tool runs as soon as its call is written, so its result can
// come before the reply's next block: each event is passed on as a message
// of its own, as it comes, so a call shows while it runs. A tool's name and
// arguments are put as pi's tools have them (Bash as bash{command}, Read as
// read{path}, Edit as edit{path, oldText, newText}, Write as write{path,
// content}), so the Terminal shows their calls and patches. The prompt
// isn't echoed, so it is sent first. What a subagent says (an event with a
// parent tool call) is left out. A request that failed comes as a reply of
// the model "<synthetic>" holding the error, given as a failed request.
//
// The run ends with a result event, the outcome, or several when something
// it started in the background ends after it answered and it goes on; the
// last is the outcome. Events this doesn't know are passed by.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type claudeStream struct {
	emit   func(json.RawMessage)
	log    *stepLogger
	prompt string
	buf    []byte
	// Whether the prompt went out, and the first init event was read.
	started, inited bool
	// Each tool call's name as pi's tools are named, by its id.
	names map[string]string
	// The last result, if any came.
	result *claudeResult
	// Why the step fails though Claude Code may end well: it signed in with
	// something else than the step's key, which init says as signIn.
	refused string
	signIn  string
}

type claudeResult struct {
	Subtype           string   `json:"subtype"`
	IsError           bool     `json:"is_error"`
	Result            string   `json:"result"`
	Errors            []string `json:"errors"`
	NumTurns          int      `json:"num_turns"`
	DurationMS        int64    `json:"duration_ms"`
	TotalCostUSD      float64  `json:"total_cost_usd"`
	PermissionDenials []struct {
		ToolName string `json:"tool_name"`
	} `json:"permission_denials"`
	APIErrorType string `json:"api_error_type"`
}

// A content block of Claude Code's, as the Messages API has it.
type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   *bool           `json:"is_error"`
	Source    struct {
		MediaType string `json:"media_type"`
	} `json:"source"`
}

func (s *claudeStream) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		s.record(bytes.TrimSuffix(s.buf[:i], []byte("\r")))
		s.buf = s.buf[i+1:]
	}
}

func (s *claudeStream) send(m map[string]any) {
	if s.emit == nil {
		return
	}
	if b, err := json.Marshal(m); err == nil {
		s.emit(b)
	}
}

func (s *claudeStream) record(line []byte) {
	var ev struct {
		Type            string          `json:"type"`
		Subtype         string          `json:"subtype"`
		ParentToolUseID *string         `json:"parent_tool_use_id"`
		Message         json.RawMessage `json:"message"`
		IsAPIError      bool            `json:"is_api_error_message"`
		ToolUseResult   json.RawMessage `json:"tool_use_result"`
	}
	if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &ev) != nil {
		return
	}
	if !s.started {
		s.started = true
		s.send(map[string]any{"role": "user", "content": s.prompt})
	}
	if ev.ParentToolUseID != nil && *ev.ParentToolUseID != "" {
		return
	}
	switch ev.Type {
	case "system":
		s.system(ev.Subtype, line)
	case "assistant":
		s.assistant(ev.Message, ev.IsAPIError)
	case "user":
		s.toolResults(ev.Message, ev.ToolUseResult)
	case "result":
		var r claudeResult
		if json.Unmarshal(line, &r) == nil {
			s.result = &r
			s.ended(r)
		}
	}
}

// Logs what Claude Code started with, once, and each request it tries again.
func (s *claudeStream) system(subtype string, line []byte) {
	switch subtype {
	case "init":
		if s.inited {
			return
		}
		s.inited = true
		var init struct {
			Model        string `json:"model"`
			Version      string `json:"claude_code_version"`
			APIKeySource string `json:"apiKeySource"`
			MCPServers   []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"mcp_servers"`
		}
		if json.Unmarshal(line, &init) != nil {
			return
		}
		if init.Version != "" {
			s.log.infof("Claude Code %s, with the model %s", init.Version, init.Model)
		}
		// Only the step's token, standing in for the instance's key, may
		// sign it in.
		if init.APIKeySource != s.signIn {
			s.refused = fmt.Sprintf("Claude Code signed in with %q rather than the step's key, which isn't allowed", init.APIKeySource)
			s.log.errorf("%s", s.refused)
		}
		var connected []string
		for _, m := range init.MCPServers {
			if m.Status == "connected" {
				connected = append(connected, m.Name)
			} else {
				s.log.errorf("the MCP server %s didn't connect: %s", m.Name, m.Status)
			}
		}
		if len(connected) > 0 {
			s.log.infof("MCP servers connected: %s", strings.Join(connected, ", "))
		}
	case "api_retry":
		var r struct {
			Attempt     int    `json:"attempt"`
			MaxRetries  int    `json:"max_retries"`
			ErrorStatus *int   `json:"error_status"`
			Error       string `json:"error"`
		}
		if json.Unmarshal(line, &r) != nil {
			return
		}
		status := "no answer"
		if r.ErrorStatus != nil {
			status = fmt.Sprintf("status %d", *r.ErrorStatus)
		}
		s.log.infof("the model's request failed (%s, %s); trying again, attempt %d of %d", status, r.Error, r.Attempt, r.MaxRetries)
	}
}

func (s *claudeStream) assistant(raw json.RawMessage, apiError bool) {
	var m struct {
		Model   string        `json:"model"`
		Content []claudeBlock `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	if apiError {
		var texts []string
		for _, b := range m.Content {
			if b.Type == "text" {
				texts = append(texts, b.Text)
			}
		}
		s.send(map[string]any{"role": "assistant", "model": m.Model, "content": []any{}, "stopReason": "error", "errorMessage": strings.Join(texts, "\n")})
		return
	}
	content := []any{}
	stop := "stop"
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": b.Text})
		case "thinking":
			content = append(content, map[string]any{"type": "thinking", "thinking": b.Thinking})
		case "redacted_thinking":
			content = append(content, map[string]any{"type": "thinking", "thinking": "", "redacted": true})
		case "tool_use":
			name, args := piTool(b.Name, b.Input)
			s.names[b.ID] = name
			content = append(content, map[string]any{"type": "toolCall", "id": b.ID, "name": name, "arguments": args})
			stop = "toolUse"
		}
	}
	if len(content) == 0 {
		return
	}
	s.send(map[string]any{"role": "assistant", "model": m.Model, "stopReason": stop, "content": content})
}

func (s *claudeStream) toolResults(raw json.RawMessage, toolUseResult json.RawMessage) {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &m) != nil || json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	var results []claudeBlock
	for _, b := range blocks {
		if b.Type == "tool_result" {
			results = append(results, b)
		}
	}
	for _, b := range results {
		msg := map[string]any{
			"role": "toolResult", "toolCallId": b.ToolUseID, "toolName": s.names[b.ToolUseID],
			"isError": b.IsError != nil && *b.IsError, "content": resultContent(b.Content),
		}
		// An edit's patch, as pi's edit tool gives it, which the Terminal
		// shows under the call; it is said for the event's one result.
		if len(results) == 1 && s.names[b.ToolUseID] == "edit" {
			if patch := editPatch(toolUseResult); patch != "" {
				msg["details"] = map[string]any{"patch": patch}
			}
		}
		s.send(msg)
	}
}

// A tool result's content, a text or a list of blocks, as pi's blocks.
func resultContent(raw json.RawMessage) []any {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	var blocks []claudeBlock
	_ = json.Unmarshal(raw, &blocks)
	content := []any{}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": b.Text})
		case "image":
			content = append(content, map[string]any{"type": "image", "mimeType": b.Source.MediaType})
		}
	}
	return content
}

// An edit's hunks as a unified diff's.
func editPatch(raw json.RawMessage) string {
	var r struct {
		StructuredPatch []struct {
			OldStart int      `json:"oldStart"`
			OldLines int      `json:"oldLines"`
			NewStart int      `json:"newStart"`
			NewLines int      `json:"newLines"`
			Lines    []string `json:"lines"`
		} `json:"structuredPatch"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	var out []string
	for _, h := range r.StructuredPatch {
		out = append(out, fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines))
		out = append(out, h.Lines...)
	}
	return strings.Join(out, "\n")
}

// Claude Code's tools as pi's are named, with their arguments' names; any
// other tool is as it is.
var piTools = map[string]struct {
	name string
	args map[string]string
}{
	"Bash":  {"bash", nil},
	"Read":  {"read", map[string]string{"file_path": "path"}},
	"Edit":  {"edit", map[string]string{"file_path": "path", "old_string": "oldText", "new_string": "newText"}},
	"Write": {"write", map[string]string{"file_path": "path"}},
	"Grep":  {"grep", nil},
	"Glob":  {"find", nil},
}

func piTool(name string, input json.RawMessage) (string, any) {
	var args map[string]any
	if json.Unmarshal(input, &args) != nil || args == nil {
		var v any
		_ = json.Unmarshal(input, &v)
		if t, ok := piTools[name]; ok {
			return t.name, v
		}
		return name, v
	}
	t, ok := piTools[name]
	if !ok {
		return name, args
	}
	for from, to := range t.args {
		if v, ok := args[from]; ok {
			delete(args, from)
			args[to] = v
		}
	}
	return t.name, args
}

// Logs how a turn ended: what Claude Code estimates it cost is never what
// counts, which is what the model proxy recorded of each call.
func (s *claudeStream) ended(r claudeResult) {
	s.log.infof("Claude Code answered after %d turns in %s, estimating it cost $%.4f; what counts is what the model proxy recorded",
		r.NumTurns, (time.Duration(r.DurationMS) * time.Millisecond).Round(time.Millisecond), r.TotalCostUSD)
	for _, d := range r.PermissionDenials {
		s.log.errorf("Claude Code was refused the tool %s", d.ToolName)
	}
}

// What the run gave, from its last result: its answer, or why it failed.
func (s *claudeStream) outcome() (output, failure string) {
	if s.refused != "" {
		return "", s.refused
	}
	r := s.result
	if r == nil {
		return "", "Claude Code ended without a result"
	}
	if r.Subtype == "success" && !r.IsError {
		return strings.TrimSpace(r.Result), ""
	}
	switch {
	case len(r.Errors) > 0:
		return "", strings.Join(r.Errors, "\n")
	case r.Result != "":
		return "", apiErrorText(r)
	case r.Subtype == "error_max_turns":
		return "", "Claude Code ran out of turns"
	case r.Subtype == "error_during_execution":
		return "", "Claude Code failed while running"
	case r.Subtype == "error_max_budget_usd":
		return "", "Claude Code reached its budget"
	}
	return "", "Claude Code failed (" + r.Subtype + ")"
}

// A refused request's error as the proxy said it: Claude Code puts a 403
// as failing to sign in, which a run or person past their spending limit
// isn't.
func apiErrorText(r *claudeResult) string {
	const prefix = "API Error: 403 "
	if r.APIErrorType == "out_of_budget" {
		if _, after, ok := strings.Cut(r.Result, prefix); ok {
			return strings.TrimSpace(after)
		}
	}
	return r.Result
}
