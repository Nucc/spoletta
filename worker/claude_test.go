package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Feeds a recording of Claude Code's stream-json output to the translator,
// in uneven pieces, returning the messages it gave and its outcome.
func translate(t *testing.T, file, prompt string) ([]map[string]any, string, string, []logLine) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	var lines []logLine
	l := newStepLogger("test", func(line logLine) { lines = append(lines, line) })
	var messages []map[string]any
	s := claudeHarness{}.stream(&agentStep{prompt: prompt, log: l}, func(m json.RawMessage) {
		var v map[string]any
		if err := json.Unmarshal(m, &v); err != nil {
			t.Fatalf("not a JSON object: %s", m)
		}
		messages = append(messages, v)
	})
	for len(b) > 0 {
		n := min(len(b), 777)
		s.Write(b[:n])
		b = b[n:]
	}
	output, failure := s.outcome()
	return messages, output, failure, lines
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Claude Code's conversation comes out in pi's message shape, its tools
// named and their arguments put as pi's are, so the Terminal shows it as a
// pi step's.
func TestClaudeStreamGivesPiMessages(t *testing.T) {
	messages, output, failure, lines := translate(t, "claude_stream.jsonl", "Do the task.")
	if output != "Handed in the summary." || failure != "" {
		t.Errorf("outcome %q, %q", output, failure)
	}
	var got []string
	for _, m := range messages {
		got = append(got, asJSON(m))
	}
	ws := "/data/workspaces/run-7/repo"
	want := []string{
		`{"content":"Do the task.","role":"user"}`,
		`{"content":[{"thinking":"I should look around.","type":"thinking"}],"model":"claude-test-1","role":"assistant","stopReason":"stop"}`,
		`{"content":[{"text":"Let me look.","type":"text"}],"model":"claude-test-1","role":"assistant","stopReason":"stop"}`,
		`{"content":[{"arguments":{"command":"echo hi \u003e a.txt \u0026\u0026 ls","description":"Write a file"},"id":"toolu_0001_Bash","name":"bash","type":"toolCall"}],"model":"claude-test-1","role":"assistant","stopReason":"toolUse"}`,
		`{"content":[{"text":"a.txt","type":"text"}],"isError":false,"role":"toolResult","toolCallId":"toolu_0001_Bash","toolName":"bash"}`,
		`{"content":[{"arguments":{"path":"` + ws + `/a.txt"},"id":"toolu_0002_Read","name":"read","type":"toolCall"}],"model":"claude-test-1","role":"assistant","stopReason":"toolUse"}`,
		`{"content":[{"text":"1\thi\n2\t","type":"text"}],"isError":false,"role":"toolResult","toolCallId":"toolu_0002_Read","toolName":"read"}`,
		`{"content":[{"redacted":true,"thinking":"","type":"thinking"}],"model":"claude-test-1","role":"assistant","stopReason":"stop"}`,
		`{"content":[{"arguments":{"newText":"hello","oldText":"hi","path":"` + ws + `/a.txt","replace_all":false},"id":"toolu_0003_Edit","name":"edit","type":"toolCall"}],"model":"claude-test-1","role":"assistant","stopReason":"toolUse"}`,
		`{"content":[{"text":"The file ` + ws + `/a.txt has been updated successfully. (file state is current in your context — no need to Read it back)","type":"text"}],"details":{"patch":"@@ -1,1 +1,1 @@\n-hi\n+hello"},"isError":false,"role":"toolResult","toolCallId":"toolu_0003_Edit","toolName":"edit"}`,
		`{"content":[{"arguments":{"content":"b\n","path":"` + ws + `/b.txt"},"id":"toolu_0004_Write","name":"write","type":"toolCall"}],"model":"claude-test-1","role":"assistant","stopReason":"toolUse"}`,
	}
	if len(got) < len(want) {
		t.Fatalf("got %d messages:\n%s", len(got), strings.Join(got, "\n"))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("message %d:\n got %s\nwant %s", i, got[i], w)
		}
	}
	// The rest: the write's result, the searches this version hasn't
	// (failing, with pi's names), submit_output refused then taken, the end.
	rest := strings.Join(got[len(want):], "\n")
	for _, w := range []string{
		`"name":"grep"`, `"name":"find"`, `"isError":true,"role":"toolResult","toolCallId":"toolu_0005_Grep","toolName":"grep"`,
		`"name":"mcp__flow_output__submit_output"`,
		`{"content":[{"text":"\"summary\" must be a string","type":"text"}],"isError":true,"role":"toolResult"`,
		`{"content":[{"text":"Output submitted. You are done; reply with one short line and stop.","type":"text"}],"isError":false,"role":"toolResult"`,
		`{"content":[{"text":"Handed in the summary.","type":"text"}],"model":"claude-test-1","role":"assistant","stopReason":"stop"}`,
	} {
		if !strings.Contains(rest, w) {
			t.Errorf("no %s in\n%s", w, rest)
		}
	}
	for _, m := range messages {
		if r := m["role"]; r != "user" && r != "assistant" && r != "toolResult" {
			t.Errorf("a message of role %v", r)
		}
	}
	log := asJSON(lines)
	for _, w := range []string{"Claude Code 2.1.325, with the model claude-test-1", "MCP servers connected: flow_output", "estimating it cost $0.0099"} {
		if !strings.Contains(log, w) {
			t.Errorf("the log says nothing of %q: %s", w, log)
		}
	}
}

// A request that failed shows as one, and fails the step with what the
// model proxy said.
func TestClaudeStreamGivesAFailedRequest(t *testing.T) {
	messages, output, failure, _ := translate(t, "claude_stream_error.jsonl", "Do it")
	if output != "" || failure != "the run has spent its limit of $1.00" {
		t.Errorf("outcome %q, %q", output, failure)
	}
	last := asJSON(messages[len(messages)-1])
	if want := `{"content":[],"errorMessage":"Failed to authenticate. API Error: 403 the run has spent its limit of $1.00","model":"\u003csynthetic\u003e","role":"assistant","stopReason":"error"}`; last != want {
		t.Errorf("got %s\nwant %s", last, want)
	}
}

func TestClaudeStreamOutcomes(t *testing.T) {
	init := `{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY","mcp_servers":[{"name":"jira","status":"failed"}]}`
	for _, c := range []struct {
		name, stream, output, failure string
	}{
		{"success", init + "\n" + `{"type":"result","subtype":"success","is_error":false,"result":" done \n"}`, "done", ""},
		{"the last result counts", init + "\n" + `{"type":"result","subtype":"success","result":"first"}` + "\n" + `{"type":"result","subtype":"success","result":"second"}`, "second", ""},
		{"an error result", init + "\n" + `{"type":"result","subtype":"success","is_error":true,"result":"API Error: 500 boom"}`, "", "API Error: 500 boom"},
		{"errors", init + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["one","two"]}`, "", "one\ntwo"},
		{"out of turns", init + "\n" + `{"type":"result","subtype":"error_max_turns","is_error":true}`, "", "Claude Code ran out of turns"},
		{"failed running", init + "\n" + `{"type":"result","subtype":"error_during_execution","is_error":true}`, "", "Claude Code failed while running"},
		{"no result", init, "", "Claude Code ended without a result"},
		{"signed in otherwise", `{"type":"system","subtype":"init","apiKeySource":"/login managed key"}` + "\n" + `{"type":"result","subtype":"success","result":"done"}`, "", `Claude Code signed in with "/login managed key" rather than the step's key, which isn't allowed`},
	} {
		var lines []logLine
		s := claudeHarness{}.stream(&agentStep{prompt: "p", log: newStepLogger("test", func(l logLine) { lines = append(lines, l) })}, nil)
		s.Write([]byte(c.stream + "\n"))
		if output, failure := s.outcome(); output != c.output || failure != c.failure {
			t.Errorf("%s: %q, %q; want %q, %q", c.name, output, failure, c.output, c.failure)
		}
		if !strings.Contains(asJSON(lines), "the MCP server jira didn't connect: failed") && c.name == "success" {
			t.Errorf("a server that didn't connect isn't logged: %v", lines)
		}
	}
}

// What a subagent says isn't the step's conversation.
func TestClaudeStreamLeavesSubagentsOut(t *testing.T) {
	var n int
	s := claudeHarness{}.stream(&agentStep{prompt: "p", log: newStepLogger("test", nil)}, func(json.RawMessage) { n++ })
	s.Write([]byte(`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"model":"m","content":[{"type":"text","text":"inside"}]}}` + "\n"))
	s.Write([]byte(`{"type":"user","parent_tool_use_id":"toolu_1","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"y"}]}}` + "\n"))
	s.Write([]byte(`{"type":"something_new","x":1}` + "\n"))
	if n != 1 {
		t.Errorf("%d messages, want only the prompt", n)
	}
}

func TestClaudeSessionID(t *testing.T) {
	if id, ok := claudeSessionID("0123abcd456789ef0123456789abcdef"); !ok || id != "0123abcd-4567-89ef-0123-456789abcdef" {
		t.Errorf("%q, %v", id, ok)
	}
	for _, bad := range []string{"", "0123", "0123abcd456789ef0123456789abcdeg", "0123abcd-456789ef0123456789abcde"} {
		if _, ok := claudeSessionID(bad); ok {
			t.Errorf("%q taken", bad)
		}
	}
}

// A step's first run starts its session in a config directory of the
// session's own; carrying it on finds its transcript there, and fails when
// it is gone.
func TestClaudeSessionArgs(t *testing.T) {
	workspacesRoot = t.TempDir()
	const hex, uuid = "0123abcd456789ef0123456789abcdef", "0123abcd-4567-89ef-0123-456789abcdef"
	ws := &workspaceSpec{ID: "run-7"}
	h := claudeHarness{}
	if args, err := h.session(runRequest{}); err != nil || !slices.Equal(args, []string{"--no-session-persistence"}) {
		t.Errorf("no session: %v, %v", args, err)
	}
	if _, err := h.session(runRequest{Resume: true}); err == nil {
		t.Error("carried on nothing")
	}
	if _, err := h.session(runRequest{SessionID: hex}); err == nil {
		t.Error("a session without a workspace")
	}
	if _, err := h.session(runRequest{SessionID: "not-hex", Workspace: ws}); err == nil {
		t.Error("a session id that isn't one")
	}
	args, err := h.session(runRequest{SessionID: hex, Workspace: ws})
	if err != nil || !slices.Equal(args, []string{"--session-id", uuid}) {
		t.Fatalf("first run: %v, %v", args, err)
	}
	dir := filepath.Join(workspacesRoot, ".sessions", "run-7", "claude", uuid)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("no config directory: %v", err)
	}
	if _, err := h.session(runRequest{SessionID: hex, Workspace: ws, Resume: true}); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Errorf("carried on a session with no transcript: %v", err)
	}
	project := filepath.Join(dir, "projects", "-data-workspaces-run-7")
	os.MkdirAll(project, 0o700)
	os.WriteFile(filepath.Join(project, uuid+".jsonl"), []byte("{}\n"), 0o600)
	if args, err := h.session(runRequest{SessionID: hex, Workspace: ws, Resume: true}); err != nil || !slices.Equal(args, []string{"--resume", uuid}) {
		t.Errorf("resume: %v, %v", args, err)
	}
}

func TestClaudeHarnessIsOfferedOnlyWhenInstalled(t *testing.T) {
	claudeInstalled = false
	t.Cleanup(func() { claudeInstalled = false })
	if _, err := harnessFor(runRequest{Harness: "claude"}); err == nil {
		t.Error("Claude Code without the binary")
	}
	claudeInstalled = true
	if h, err := harnessFor(runRequest{Harness: "claude"}); err != nil || h.name() != "Claude Code" {
		t.Errorf("%v, %v", h, err)
	}
	if _, err := harnessFor(runRequest{Harness: "claude", NoTools: true}); err == nil {
		t.Error("Claude Code without tools")
	}
	if _, err := harnessFor(runRequest{Harness: "other"}); err == nil {
		t.Error("an unknown harness")
	}
	if h, err := harnessFor(runRequest{}); err != nil || h.name() != "pi" {
		t.Errorf("default: %v, %v", h, err)
	}
}

// A claude that notes how it was started and what it was given, hands in
// FAKE_RESULT as submit_output would, and prints the recording.
const fakeClaude = `#!/bin/sh
out="$FAKE_RECORD"
printf '%s\n' "$@" > "$out/args"
env > "$out/env"
cat > "$out/stdin"
prev=""
for a in "$@"; do
  case "$prev" in
    --mcp-config) cp "$a" "$out/mcp.json"; priv=$(dirname "$(dirname "$a")");;
    --settings) cp "$a" "$out/settings.json";;
    --plugin-dir) cp -R "$a" "$out/plugin";;
  esac
  prev="$a"
done
if [ -n "$FAKE_RESULT" ]; then printf '%s' "$FAKE_RESULT" > "$priv/out/result.json"; fi
cat "$FAKE_STREAM"
`

func withFakeClaude(t *testing.T) string {
	t.Helper()
	bin, record := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	stream, _ := filepath.Abs(filepath.Join("testdata", "claude_stream.jsonl"))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_RECORD", record)
	t.Setenv("FAKE_STREAM", stream)
	t.Setenv("CHILD_ENV", "FAKE_RECORD,FAKE_STREAM,FAKE_RESULT,ANTHROPIC_AUTH_TOKEN,CLAUDE_CODE_OAUTH_TOKEN")
	return record
}

func readRecord(t *testing.T, record, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(record, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	stepToken = "tok_abcdefghijklmnop0123"
	mcpToken  = "mcp_abcdefghijklmnop0123"
)

func claudeRun() runRequest {
	return runRequest{
		Harness: "claude", Prompt: "/review the change", Model: "anthropic/claude-sonnet-4-6", Thinking: "minimal",
		LLM:        &llmAccess{URL: "http://webapp:8080/llm/", Token: stepToken, Providers: []string{"openai", "anthropic"}},
		MCPServers: []mcpServer{{Name: "jira", URL: "http://webapp:8080/mcp/3"}}, MCPToken: mcpToken,
		Output: &outputSpec{Shape: "object", Objects: json.RawMessage(`[{"name":"Summary","many":false,"attributes":[]}]`)},
		Skills: []skill{{Name: "review", Description: "Reviews: carefully", Content: "Look."}},
	}
}

// A Claude Code step reaches its model only through the model proxy, with
// its token as the API key, reads none of its own setup but what the
// worker writes, and hands in its output through submit_output's server.
func TestClaudeStepRunsWithTheStepsToken(t *testing.T) {
	record := withFakeClaude(t)
	t.Setenv("FAKE_RESULT", `{"summary":"all done"}`)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "a-key-of-the-workers")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "a-sign-in")
	var lines []logLine
	l := newStepLogger("test", func(line logLine) { lines = append(lines, line) }).hiding(claudeRun().tokens())
	var messages []string
	res := runAgent(context.Background(), claudeHarness{}, claudeRun(), time.Minute, l, func(m json.RawMessage) { messages = append(messages, string(m)) })
	if !res.OK || !res.Submitted || res.Output != `{"summary":"all done"}` {
		t.Fatalf("%+v", res)
	}
	if len(messages) == 0 || messages[0] != `{"content":"/review the change","role":"user"}` {
		t.Errorf("the prompt went out as %v", messages[:min(1, len(messages))])
	}

	args := strings.Split(strings.TrimSuffix(readRecord(t, record, "args"), "\n"), "\n")
	joined := "\n" + strings.Join(args, "\n") + "\n"
	for _, w := range []string{
		"\n-p\n--output-format\nstream-json\n--verbose\n",
		"\n--setting-sources\n\n",
		"\n--permission-mode\nbypassPermissions\n",
		"\n--model\nclaude-sonnet-4-6\n",
		"\n--effort\nlow\n",
		"\n--tools\nBash,Read,Edit,Write,WebFetch,NotebookEdit,Skill\n",
		"\n--disallowedTools\nWebSearch\n",
		"\n--strict-mcp-config\n--mcp-config\n",
		"\n--no-session-persistence\n",
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("no %q in %q", w, args)
		}
	}
	if slices.Contains(args, "--bare") {
		t.Error("--bare leaves the agent only Bash, Read and Edit")
	}
	if i := slices.Index(args, "--append-system-prompt"); i < 0 || !strings.Contains(args[i+1], "mcp__flow_output__submit_output") || !strings.Contains(args[i+1], "headless") {
		t.Errorf("system prompt: %q", args)
	}
	// A prompt starting with / would be read as one of Claude Code's commands.
	if stdin := readRecord(t, record, "stdin"); stdin != "\n/review the change" {
		t.Errorf("stdin %q", stdin)
	}

	env := "\n" + readRecord(t, record, "env")
	for _, w := range []string{
		"\nANTHROPIC_BASE_URL=http://webapp:8080/llm/anthropic\n",
		"\nANTHROPIC_API_KEY=" + stepToken + "\n",
		"\nANTHROPIC_DEFAULT_HAIKU_MODEL=claude-sonnet-4-6\n",
		"\nENABLE_TOOL_SEARCH=false\n",
		"\nCLAUDE_CODE_DISABLE_AUTO_MEMORY=1\n",
		"\nCLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1\n",
		"\nDISABLE_TELEMETRY=1\n",
		"\nCLAUDE_CONFIG_DIR=" + stepsRoot,
	} {
		if !strings.Contains(env, w) {
			t.Errorf("no %q in the environment", strings.TrimSpace(w))
		}
	}
	for _, bad := range []string{"ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "a-key-of-the-workers", "a-sign-in", "PI_CODING_AGENT_DIR"} {
		if strings.Contains(env, bad) {
			t.Errorf("the environment holds %s", bad)
		}
	}

	var mcp struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(readRecord(t, record, "mcp.json")), &mcp); err != nil {
		t.Fatal(err)
	}
	if j := mcp.MCPServers["jira"]; j.Type != "http" || j.URL != "http://webapp:8080/mcp/3" || j.Headers["Authorization"] != "Bearer "+mcpToken {
		t.Errorf("jira: %+v", j)
	}
	if o := mcp.MCPServers["flow_output"]; o.Type != "stdio" || o.Command != "node" || !slices.Equal(o.Args, []string{"/opt/flow/submit-output-mcp.ts"}) || !strings.HasSuffix(o.Env["FLOW_OUTPUT_SPEC"], "/spec.json") {
		t.Errorf("flow_output: %+v", o)
	}
	var settings map[string]any
	json.Unmarshal([]byte(readRecord(t, record, "settings.json")), &settings)
	if settings["cleanupPeriodDays"] != float64(36500) || asJSON(settings["attribution"]) != `{"commit":"","pr":""}` {
		t.Errorf("settings: %v", settings)
	}
	if skill := readRecord(t, record, "plugin/skills/review/SKILL.md"); !strings.Contains(skill, `description: "Reviews: carefully"`) {
		t.Errorf("SKILL.md: %s", skill)
	}
	if p := readRecord(t, record, "plugin/.claude-plugin/plugin.json"); !strings.Contains(p, `"name": "flow"`) {
		t.Errorf("plugin.json: %s", p)
	}

	var texts []string
	for _, line := range lines {
		texts = append(texts, line.Message)
	}
	log := strings.Join(texts, "\n")
	for _, secret := range []string{stepToken, mcpToken} {
		if strings.Contains(log, secret) {
			t.Errorf("the log holds a token: %s", log)
		}
	}
	if !strings.Contains(log, `claude -p --output-format stream-json --verbose --setting-sources "" --permission-mode bypassPermissions --model claude-sonnet-4-6`) ||
		!strings.Contains(log, "--append-system-prompt <…>") || strings.Contains(log, "headless") {
		t.Errorf("the command as logged: %s", log)
	}
}

// Claude Code reaches only models of a provider the model proxy reaches
// for the step, and its MCP servers can't take submit_output's name.
func TestClaudeStepNeedsAProxiedModel(t *testing.T) {
	withFakeClaude(t)
	for _, c := range []struct {
		name string
		edit func(*runRequest)
		want string
	}{
		{"no model", func(r *runRequest) { r.Model = "" }, "Claude Code needs a model: pick one for the process"},
		{"no proxy", func(r *runRequest) { r.LLM = nil }, "Claude Code needs a model of a provider the model proxy reaches"},
		{"no provider", func(r *runRequest) { r.Model = "claude-sonnet-4-6" }, "Claude Code needs a model of a provider the model proxy reaches"},
		{"another provider", func(r *runRequest) { r.Model = "acme/claude-sonnet-4-6" }, "Claude Code needs a model of a provider the model proxy reaches"},
		{"submit_output's server", func(r *runRequest) { r.MCPServers = []mcpServer{{Name: "Flow_Output", URL: "http://webapp:8080/mcp/4"}} }, `the MCP server "Flow_Output" can't be given to Claude Code by that name`},
	} {
		run := claudeRun()
		c.edit(&run)
		res := runAgent(context.Background(), claudeHarness{}, run, time.Minute, newStepLogger("test", nil), nil)
		if res.OK || res.Error != c.want {
			t.Errorf("%s: %+v", c.name, res)
		}
	}
}
