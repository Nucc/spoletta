package tests

// An agent process runs with pi or with Claude Code, which the worker offers
// when an operator built it with Claude Code. The webapp picks, carries and
// checks the choice: the worker is told which runs a step and its
// follow-ups, a run keeps what its version says, Claude Code takes only
// models of a provider speaking Anthropic's API, and its calls to them pass
// through the model proxy as pi's do.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A model gateway speaking Anthropic's API, as Claude Code reaches it
// through the proxy: a message streamed with its usage, tokens counted,
// models listed and the start-up probe answered. It keeps what reached it.
type claudeGateway struct {
	*httptest.Server
	mu      sync.Mutex
	reached []*http.Request
}

const claudeStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus\",\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":3000,\"cache_creation_input_tokens\":400,\"output_tokens\":1}}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Fixed.\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":57}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func newClaudeGateway(t *testing.T) *claudeGateway {
	g := &claudeGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.reached = append(g.reached, r.Clone(context.Background()))
		g.mu.Unlock()
		switch r.URL.Path {
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, claudeStream)
		case "/v1/messages/count_tokens":
			writeJSON(w, http.StatusOK, map[string]int{"input_tokens": 2048})
		case "/v1/models":
			writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "claude-opus"}}, "has_more": false})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *claudeGateway) requests() []*http.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reached)
}

// Adds the gateway as a provider speaking Anthropic's API, Acme gateway,
// whose one model, acme-gateway/claude-opus, Claude Code may use; it and
// the default it becomes go once the test is over.
func addClaudeGateway(t *testing.T, a *app, g *claudeGateway) {
	t.Helper()
	db := secretsDB(t)
	t.Cleanup(func() {
		db.Exec("DELETE FROM model_providers")
		db.Exec("DELETE FROM model_default")
	})
	a.call("POST", "/api/model-providers", map[string]any{
		"name": "Acme gateway", "api": "anthropic-messages", "baseUrl": g.URL,
		"keySource": "saved", "key": "acme-key", "models": []string{"claude-opus"},
	}, http.StatusOK, nil)
}

// Sends what Claude Code sends as a step goes, with the step's token: a
// message streamed, its tokens counted, the models listed and the probe.
// Returns the status of each.
func callAsClaudeCode(llm *llmAccess, provider string) []int {
	var statuses []int
	for _, c := range []struct{ method, path, body string }{
		{"HEAD", "/api/hello", ""},
		{"GET", "/v1/models?limit=1000", ""},
		{"POST", "/v1/messages/count_tokens?beta=true", `{"model":"claude-opus","messages":[{"role":"user","content":"hi"}]}`},
		{"POST", "/v1/messages?beta=true", `{"model":"claude-opus","stream":true,"max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`},
	} {
		var body io.Reader
		if c.body != "" {
			body = strings.NewReader(c.body)
		}
		req, _ := http.NewRequest(c.method, llm.URL+"/"+provider+c.path, body)
		req.Header.Set("X-Api-Key", llm.Token)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14")
		req.Header.Set("User-Agent", "claude-cli/2.1.325 (external, sdk-cli)")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			statuses = append(statuses, 0)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		statuses = append(statuses, resp.StatusCode)
	}
	return statuses
}

// The models offered say which coding agents the worker offers, pi alone
// for one that says none, and which models Claude Code may use: those of a
// provider speaking Anthropic's API.
func TestClaudeCodeModelsOffered(t *testing.T) {
	a := newApp(t)
	var models agentModels
	a.call("GET", "/api/models", nil, http.StatusOK, &models)
	if !slices.Equal(models.Harnesses, []string{"pi"}) || len(models.Claude) != 0 {
		t.Errorf("from a worker saying no harnesses: %+v", models)
	}
	addClaudeGateway(t, a, newClaudeGateway(t))
	a.worker.mu.Lock()
	a.worker.harnesses = []string{"pi", "claude"}
	a.worker.mu.Unlock()
	models = agentModels{}
	a.call("GET", "/api/models", nil, http.StatusOK, &models)
	if !slices.Equal(models.Harnesses, []string{"pi", "claude"}) || !slices.Equal(models.Claude, []string{"acme-gateway/claude-opus"}) ||
		!slices.Contains(models.Models, "fake/model") {
		t.Errorf("with the gateway: %+v", models)
	}
}

// A process running with Claude Code has the worker run it with Claude
// Code, on its model of a provider speaking Anthropic's API, whose calls
// pass through the proxy and are recorded as pi's are; its follow-up
// carries the session on with Claude Code even once the process runs with
// pi again, and the next run is pi's.
func TestClaudeCodeStepAndFollowUp(t *testing.T) {
	a := newApp(t)
	a.worker.quiet = true
	g := newClaudeGateway(t)
	addClaudeGateway(t, a, g)
	f := a.newFlow("Claude fixes")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
	}}, http.StatusOK, nil)
	fix := a.newProcess(f.ID, map[string]any{"name": "Fix", "kind": "agent", "prompt": "Fix it.", "model": "acme-gateway/claude-opus",
		"settings": map[string]any{"harness": "claude"}})
	if fix.Settings.Harness != "claude" {
		t.Fatalf("Fix was made running with %q", fix.Settings.Harness)
	}
	a.output(fix.ID, a.newObject(f.ID, "Diff").ID)
	a.startAt(f.ID, fix.ID)
	var mu sync.Mutex
	var statuses []int
	a.worker.onAgent("Fix", func(_ context.Context, run agentRun) runResult {
		if run.Resume {
			return runResult{OK: true, Output: "pushed"}
		}
		s := callAsClaudeCode(run.LLM, "acme-gateway")
		mu.Lock()
		statuses = s
		mu.Unlock()
		return submitted(data("fixed"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	run := a.worker.runsOf("Fix")[0]
	if run.Harness != "claude" || run.Model != "acme-gateway/claude-opus" || run.SessionID == "" || run.Resume ||
		run.LLM == nil || !slices.Contains(run.LLM.Providers, "acme-gateway") {
		t.Errorf("the worker was asked to run %+v", run)
	}
	step := onlyStep(t, d, "Fix")
	if step.Harness != "claude" || step.Session == "" {
		t.Errorf("the step ran with %q, session %q", step.Harness, step.Session)
	}

	// Claude Code's calls passed as it sent them, its token swapped for the
	// gateway's key, and only the message it streamed is recorded.
	mu.Lock()
	if fmt.Sprint(statuses) != "[200 200 200 200]" {
		t.Errorf("Claude Code's calls answered %v", statuses)
	}
	mu.Unlock()
	reached := g.requests()
	if len(reached) != 4 {
		t.Fatalf("the gateway was reached %d times", len(reached))
	}
	for _, r := range reached {
		if r.Header.Get("X-Api-Key") != "acme-key" || r.Header.Get("Anthropic-Version") != "2023-06-01" ||
			r.Header.Get("Anthropic-Beta") != "claude-code-20250219,interleaved-thinking-2025-05-14" {
			t.Errorf("%s %s reached the gateway with %v", r.Method, r.URL, r.Header)
		}
	}
	if m := reached[3]; m.Method != "POST" || m.URL.Path != "/v1/messages" || m.URL.RawQuery != "beta=true" {
		t.Errorf("the message reached %s %s", m.Method, m.URL)
	}
	if m := reached[0]; m.Method != "HEAD" || m.URL.Path != "/api/hello" {
		t.Errorf("the probe reached %s %s", m.Method, m.URL)
	}
	db := secretsDB(t)
	var calls, in, out, read, written int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_write_tokens), 0) FROM llm_usage WHERE step_id = ?`, step.ID).Scan(&calls, &in, &out, &read, &written); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || in != 12 || out != 57 || read != 3000 || written != 400 {
		t.Errorf("recorded %d calls taking %d, giving %d, reading %d and writing %d; want the message alone", calls, in, out, read, written)
	}

	// Back to pi, said as pi, which the process keeps as none.
	var back process
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", fix.ID), map[string]any{"settings": map[string]any{"harness": "pi"}}, http.StatusOK, &back)
	if back.Settings.Harness != "" {
		t.Errorf("pi was kept as %q", back.Settings.Harness)
	}
	waitReleased(t, a, d.ID, 1)
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, step.ID), map[string]any{"message": "Push it."}, http.StatusCreated, nil)
	a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
		s := onlyStep(t, d, "Fix")
		return len(s.FollowUps) == 1 && s.FollowUps[0].Status == stepDone
	})
	runs := a.worker.runsOf("Fix")
	if last := runs[len(runs)-1]; !last.Resume || last.Harness != "claude" || last.SessionID != run.SessionID || last.Model != "acme-gateway/claude-opus" {
		t.Errorf("the follow-up was run as %+v", last)
	}

	// The next run is pi's, which the worker isn't told of at all.
	d = a.finish(a.startRun(f.ID).ID)
	runs = a.worker.runsOf("Fix")
	if last := runs[len(runs)-1]; last.Harness != "" || strings.Contains(last.Raw, "harness") {
		t.Errorf("pi's run was sent as %s", last.Raw)
	}
	if s := onlyStep(t, d, "Fix"); s.Harness != "" {
		t.Errorf("pi's step says it ran with %q", s.Harness)
	}
}

// A run of the version published runs its processes with the harness they
// were published with, after the board switched back to pi; a run of the
// board as it is runs with pi. Neither step keeps a session, so each says
// what its run's version set.
func TestClaudeCodeKeepsTheVersionPublished(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Claude published")
	write := a.newProcess(f.ID, map[string]any{"name": "Write", "kind": "agent", "prompt": "Write.", "model": "anthropic/claude-sonnet-4-6",
		"settings": map[string]any{"harness": "claude"}})
	a.startAt(f.ID, write.ID)
	a.worker.onAgent("Write", handsIn(data("written")))
	pubPublish(a, f.ID, "with Claude Code")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", write.ID), map[string]any{"settings": map[string]any{}}, http.StatusOK, nil)

	var published run
	a.call("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), map[string]any{"published": true}, http.StatusCreated, &published)
	d := a.finish(published.ID)
	runs := a.worker.runsOf("Write")
	if d.Status != runDone || len(runs) != 1 || runs[0].Harness != "claude" || runs[0].Model != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("run %s; the worker was asked %+v", d.Status, runs)
	}
	if s := onlyStep(t, d, "Write"); s.Harness != "claude" || s.Session != "" {
		t.Errorf("the published run's step ran with %q, session %q", s.Harness, s.Session)
	}

	d = a.finish(a.startRun(f.ID).ID)
	runs = a.worker.runsOf("Write")
	if last := runs[len(runs)-1]; d.Status != runDone || last.Harness != "" {
		t.Errorf("run %s; the board's run was sent as %s", d.Status, last.Raw)
	}
	if s := onlyStep(t, d, "Write"); s.Harness != "" {
		t.Errorf("the board's step says it ran with %q", s.Harness)
	}
}

// A process running with Claude Code on a model it can't use fails before
// the worker sees it: without a model of its own, on the default, said in
// words of its own; with one, naming it.
func TestClaudeCodeFailsOnModelsItCantUse(t *testing.T) {
	a := newApp(t)
	var models agentModels
	a.call("GET", "/api/models", nil, http.StatusOK, &models)
	if models.Default != "fake/model" {
		t.Fatalf("the default is %q; a provider another test added is still there", models.Default)
	}
	for _, c := range []struct{ name, model, says string }{
		{"Default", "", "the default model isn't one Claude Code can use; pick one for the process"},
		{"Own", "openai/gpt-x", "Claude Code can't use the model openai/gpt-x: it takes only models of a provider speaking Anthropic's API, or Claude's on Bedrock; pick another for the process"},
	} {
		f := a.newFlow("Claude on " + c.name)
		p := a.newProcess(f.ID, map[string]any{"name": c.name, "kind": "agent", "prompt": "Go.", "model": c.model, "settings": map[string]any{"harness": "claude"}})
		a.startAt(f.ID, p.ID)
		a.worker.onAgent(c.name, handsIn(data("never")))
		d := a.finish(a.startRun(f.ID).ID)
		if s := onlyStep(t, d, c.name); d.Status != runFailed || s.Status != stepError || s.Error != c.says {
			t.Errorf("%s: run %s, step %s: %q", c.name, d.Status, s.Status, s.Error)
		}
		if n := len(a.worker.runsOf(c.name)); n != 0 {
			t.Errorf("%s reached the worker %d times", c.name, n)
		}
	}
}

// Only an agent process runs with Claude Code, judged by the kind it has
// after a change; pi is kept as none, and nothing else is a harness.
func TestClaudeCodeOnlyOnAgents(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Harnesses")
	a.refused("POST", "/api/processes", map[string]any{"flow": f.ID, "name": "Lint", "kind": "script", "settings": map[string]any{"harness": "claude"}},
		http.StatusBadRequest, "only an agent process runs with Claude Code")
	a.refused("POST", "/api/processes", map[string]any{"flow": f.ID, "name": "Odd", "kind": "agent", "settings": map[string]any{"harness": "codex"}},
		http.StatusBadRequest, "harness must be pi or claude")
	pi := a.newProcess(f.ID, map[string]any{"name": "Plain", "kind": "agent", "settings": map[string]any{"harness": "pi"}})
	if pi.Settings.Harness != "" {
		t.Errorf("pi was kept as %q", pi.Settings.Harness)
	}

	script := a.newProcess(f.ID, map[string]any{"name": "Lint", "kind": "script", "script": "make lint"})
	path := fmt.Sprintf("/api/processes/%d", script.ID)
	a.refused("PATCH", path, map[string]any{"settings": map[string]any{"harness": "claude"}}, http.StatusBadRequest, "only an agent process runs with Claude Code")
	a.refused("PATCH", path, map[string]any{"settings": map[string]any{"harness": "codex"}}, http.StatusBadRequest, "harness must be pi or claude")
	var p process
	a.call("PATCH", path, map[string]any{"kind": "agent", "settings": map[string]any{"harness": "claude", "timeout": 60}}, http.StatusOK, &p)
	if p.Kind != "agent" || p.Settings.Harness != "claude" {
		t.Errorf("became %s running with %q", p.Kind, p.Settings.Harness)
	}
	// Becoming another kind with its settings left as they are goes back to
	// pi, keeping the rest of them; settings saying Claude Code for another
	// kind are refused.
	a.refused("PATCH", path, map[string]any{"kind": "human", "settings": map[string]any{"harness": "claude"}}, http.StatusBadRequest, "only an agent process")
	var back process
	a.call("PATCH", path, map[string]any{"kind": "script"}, http.StatusOK, &back)
	if back.Kind != "script" || back.Settings.Harness != "" || back.Settings.Timeout == nil || *back.Settings.Timeout != 60 {
		t.Errorf("became %s running with %q, settings %+v", back.Kind, back.Settings.Harness, back.Settings)
	}

	// The setup file says so as the API does.
	status, body := setupImport(a, "spoletta: 1\nflows:\n  - name: Harnessed\n    processes:\n      - name: Lint\n        kind: script\n        harness: claude\n")
	if status != http.StatusBadRequest || !strings.Contains(string(body), "only an agent process runs with Claude Code") {
		t.Errorf("a script with harness: claude imported: %d %s", status, body)
	}
}

// The setup file has harness: claude on an agent process running with
// Claude Code, and brings it back in.
func TestClaudeCodeInTheSetupFile(t *testing.T) {
	a := setupApp(t)
	imported := setupImportOK(a, "spoletta: 1\nflows:\n  - name: Claude setup\n    processes:\n      - name: Fix\n        model: anthropic/claude-sonnet-4-6\n        harness: claude\n      - name: Review\n        harness: pi\n")
	var ps []process
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", imported.Flows[0].ID), nil, http.StatusOK, &ps)
	harnesses := map[string]string{}
	for _, p := range ps {
		harnesses[p.Name] = p.Settings.Harness
	}
	if harnesses["Fix"] != "claude" || harnesses["Review"] != "" {
		t.Errorf("imported running with %v", harnesses)
	}
	file := setupExport(a, "Claude setup")
	if strings.Count(file, "harness: claude") != 1 || strings.Contains(file, "harness: pi") {
		t.Errorf("the flow went out as:\n%s", file)
	}
	// Brought back in as it went out, it is as it was.
	setupImportOK(a, file)
	var again []process
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", imported.Flows[0].ID), nil, http.StatusOK, &again)
	if len(again) != 2 {
		t.Fatalf("came back as %+v", again)
	}
	for _, p := range again {
		if p.Settings.Harness != harnesses[p.Name] {
			t.Errorf("%s came back running with %q", p.Name, p.Settings.Harness)
		}
	}
}

// The board warns of a process running with Claude Code when the worker
// doesn't offer it, or on a model it can't use, and of neither once the
// worker offers it and the model is Anthropic's.
func TestClaudeCodeProblems(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Claude problems")
	p := a.newProcess(f.ID, map[string]any{"name": "Fix", "kind": "agent", "prompt": "Fix.", "model": "fake/model", "settings": map[string]any{"harness": "claude"}})
	a.startAt(f.ID, p.ID)
	ps := prbGet(a, f.ID)
	if !prbHas(ps.Problems, "warning", "harness", p.ID, `"Fix" runs with Claude Code, which the worker doesn't offer`) ||
		!prbHas(ps.Problems, "warning", "model", p.ID, `Claude Code can't use the model fake/model, so "Fix" fails once it runs`) {
		t.Errorf("problems: %s", prbDescribe(ps.Problems))
	}
	a.worker.mu.Lock()
	a.worker.harnesses = []string{"pi", "claude"}
	a.worker.mu.Unlock()
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"model": ""}, http.StatusOK, nil)
	ps = prbGet(a, f.ID)
	if prbHas(ps.Problems, "warning", "harness", p.ID, "") || !prbHas(ps.Problems, "warning", "model", p.ID, `The default model fake/model isn't one Claude Code can use, so "Fix" fails once it runs`) {
		t.Errorf("problems once the worker offers Claude Code: %s", prbDescribe(ps.Problems))
	}
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"model": "anthropic/claude-sonnet-4-6"}, http.StatusOK, nil)
	if ps = prbGet(a, f.ID); len(ps.Problems) != 0 {
		t.Errorf("problems on Anthropic's model: %s", prbDescribe(ps.Problems))
	}
}

// No MCP server may be called as the one a Claude Code step hands in its
// output through, in any case, nor be renamed so or brought in so.
func TestClaudeCodeOutputServerName(t *testing.T) {
	a := newApp(t)
	forgetMCPServers(t, "docs")
	const says = "flow_output is kept for the server agents hand in their output through."
	for _, name := range []string{"flow_output", "Flow-Output"} {
		a.refused("POST", "/api/mcp-servers", map[string]any{"name": name, "url": "https://mcp.example.com/mcp"}, http.StatusBadRequest, says)
	}
	a.refused("POST", "/api/mcp-servers", map[string]any{"name": "FLOW_OUTPUT", "connector": "github"}, http.StatusBadRequest, says)
	var s struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "docs", "url": "https://mcp.example.com/mcp"}, http.StatusCreated, &s)
	a.refused("PATCH", fmt.Sprintf("/api/mcp-servers/%d", s.ID), map[string]any{"name": "flow_output"}, http.StatusBadRequest, says)
	status, body := setupImport(a, "spoletta: 1\nmcpServers:\n  - name: flow_output\n    url: https://mcp.example.com/mcp\n")
	if status != http.StatusBadRequest || !strings.Contains(string(body), says) {
		t.Errorf("a server named flow_output imported: %d %s", status, body)
	}
}
