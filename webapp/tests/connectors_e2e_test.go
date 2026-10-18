package tests

// Connectors built into the webapp, such as GitHub's: installed as MCP
// servers taking a token an admin sets, carried out by connector processes
// and used by agents through the MCP proxy, which holds each agent to the
// tools its process picks.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A fake GitHub API, recording every request it gets with its token.
type connectorsGitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []string
}

func newConnectorsGitHub(t *testing.T) *connectorsGitHub {
	g := &connectorsGitHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/app/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"number": 7, "title": "Crash on start", "state": "open", "labels": []map[string]any{{"name": "bug"}},
				"repository_url": "https://api.github.com/repos/acme/app"},
			{"number": 8, "title": "Slow search", "state": "open", "labels": []map[string]any{{"name": "bug"}},
				"repository_url": "https://api.github.com/repos/acme/app"},
		})
	})
	mux.HandleFunc("GET /repos/acme/app/issues/7", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"number": 7, "title": "Crash on start", "state": "open", "repository_url": "https://api.github.com/repos/acme/app"})
	})
	mux.HandleFunc("POST /repos/acme/app/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		writeJSON(w, http.StatusCreated, map[string]any{"id": 99, "body": body.Body, "user": map[string]any{"login": "spoletta-bot"}})
	})
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.requests = append(g.requests, strings.TrimSpace(fmt.Sprintf("%s %s %s %s", r.Method, r.URL.Path, r.Header.Get("Authorization"), body)))
		g.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		// Every call has the connector's headers, whether a connector
		// process or an agent makes it.
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" || r.Header.Get("User-Agent") != "Spoletta" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "the call is missing the connector's headers"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *connectorsGitHub) got() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.requests)
}

type connectorsServer struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Auth      string `json:"auth"`
	Connector string `json:"connector"`
	Connected bool   `json:"connected"`
}

func connectorsServers(a *app) map[string]connectorsServer {
	a.t.Helper()
	var servers []connectorsServer
	a.call("GET", "/api/mcp-servers", nil, http.StatusOK, &servers)
	byName := map[string]connectorsServer{}
	for _, s := range servers {
		byName[s.Name] = s
	}
	return byName
}

// A connector process carries out its operation with the token an admin
// set, with arguments made of what its step takes, and generates what the
// service answers as its object's attributes.
func TestConnectorProcesses(t *testing.T) {
	a := newApp(t)
	g := newConnectorsGitHub(t)
	res := setupImportOK(a, `spoletta: 1
mcpServers:
  - name: cp_github
    connector: github
    url: `+g.srv.URL+`
flows:
  - name: CP Fetch bugs
    start: {next: Fetch}
    processes:
      - name: Fetch
        kind: connector
        connector:
          server: cp_github
          operation: list_issues
          args: {owner: acme, repo: app, labels: bug}
        yields: [Issues]
    objects:
      - name: Issues
        many: true
        attributes:
          - name: key
          - name: title
          - name: labels
        end: true
  - name: CP Answer a bug
    start: {next: Find}
    processes:
      - name: Find
        yields: [Bug]
        prompt: Find the bug.
        next: [Comment]
      - name: Comment
        kind: connector
        connector:
          server: cp_github
          operation: add_comment
          args:
            owner: acme
            repo: app
            number: "{{Bug.number}}"
            body: "Looking at {{Bug.title}}"
        takes: [Bug]
        yields: [Reply]
    objects:
      - name: Bug
        attributes:
          - name: number
          - name: title
      - name: Reply
        attributes:
          - name: id
          - name: author
        end: true
`)
	fetch, answer := res.Flows[0].ID, res.Flows[1].ID
	github := connectorsServers(a)["cp_github"]
	if github.Auth != "token" || github.Connector != "github" || github.Connected {
		t.Fatalf("the installed connector: %+v", github)
	}

	d := a.finish(a.startRun(fetch).ID)
	if s := onlyStep(t, d, "Fetch"); s.Status != stepError || !strings.Contains(s.Error, "has no token") {
		t.Errorf("without a token: %s %s", s.Status, s.Error)
	}
	if len(g.got()) != 0 {
		t.Errorf("GitHub was called without a token: %v", g.got())
	}

	a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", github.ID), map[string]any{"token": "ghp-secret"}, http.StatusNoContent, nil)
	if !connectorsServers(a)["cp_github"].Connected {
		t.Errorf("the connector isn't connected once its token is set")
	}
	d = a.finish(a.startRun(fetch).ID)
	if d.Status != runDone {
		t.Fatalf("fetching: %s %s", d.Status, describeSteps(d))
	}
	var issues []map[string]any
	if cs := contentsOf(d, "Issues"); len(cs) != 1 || json.Unmarshal([]byte(cs[0]), &issues) != nil {
		t.Fatalf("Issues: %v", contentsOf(d, "Issues"))
	}
	if len(issues) != 2 || issues[0]["key"] != "acme/app#7" || issues[1]["title"] != "Slow search" || fmt.Sprint(issues[0]["labels"]) != "[bug]" {
		t.Errorf("Issues: %v", issues)
	}
	if got := g.got(); len(got) != 1 || !strings.HasPrefix(got[0], "GET /repos/acme/app/issues Bearer ghp-secret") {
		t.Errorf("GitHub got %v", got)
	}
	if s := onlyStep(t, d, "Fetch"); !strings.Contains(s.Prompt, "cp_github list_issues") || strings.Contains(s.Prompt, "ghp-secret") {
		t.Errorf("the step's prompt: %s", s.Prompt)
	}

	a.worker.onAgent("Find", handsIn(map[string]any{"number": 7, "title": "Crash on start"}))
	d = a.finish(a.startRun(answer).ID)
	if d.Status != runDone {
		t.Fatalf("answering: %s %s", d.Status, describeSteps(d))
	}
	got := g.got()
	if last := got[len(got)-1]; last != `POST /repos/acme/app/issues/7/comments Bearer ghp-secret {"body":"Looking at Crash on start"}` {
		t.Errorf("GitHub got %s", last)
	}
	if cs := contentsOf(d, "Reply"); len(cs) != 1 || !strings.Contains(cs[0], "spoletta-bot") {
		t.Errorf("Reply: %v", cs)
	}

	// An argument naming what the step doesn't take fails the step
	// before anything reaches GitHub.
	var comment process
	for _, p := range verProcesses(a, answer) {
		if p.Name == "Comment" {
			comment = p
		}
	}
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", comment.ID), map[string]any{"settings": map[string]any{"connector": map[string]any{
		"server": github.ID, "operation": "add_comment", "args": map[string]string{"owner": "acme", "repo": "app", "number": "{{Issue.number}}", "body": "x"},
	}}}, http.StatusOK, nil)
	sent := len(g.got())
	d = a.finish(a.startRun(answer).ID)
	if s := onlyStep(t, d, "Comment"); s.Status != stepError || !strings.Contains(s.Error, `takes no object "Issue"`) {
		t.Errorf("a missing object: %s %s", s.Status, s.Error)
	}
	if len(g.got()) != sent {
		t.Errorf("GitHub was called: %v", g.got()[sent:])
	}

	// The flow's setup, as its assistant reads it, names the connector its
	// process uses; the token never leaves the webapp.
	status, body := a.do("GET", fmt.Sprintf("/api/flows/%d/setup", answer), nil)
	if status != http.StatusOK || !strings.Contains(string(body), "server: cp_github") || strings.Contains(string(body), "ghp-secret") || !strings.Contains(string(body), "operation: add_comment") {
		t.Errorf("the setup: %d %s", status, body)
	}
}

// Sends a JSON-RPC request to an MCP server as the step's agent, through the
// proxy, and returns what came back.
func connectorsMCP(ctx context.Context, run agentRun, server, body string) string {
	i := slices.IndexFunc(run.MCPServers, func(s mcpRunServer) bool { return s.Name == server })
	if i < 0 {
		return "no server " + server
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", run.MCPServers[i].URL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+run.MCPToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("%d %s", resp.StatusCode, out)
}

// Only an admin installs a connector and sets its token, which is then
// everyone's. An agent has a connector's tools that read unless its
// process picks others, and of any MCP server only those it picks, if it
// picks some.
func TestConnectorsForAgents(t *testing.T) {
	a, p := usersApp(t)
	g := newConnectorsGitHub(t)
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		w.Header().Set("Content-Type", "text/event-stream")
		result := map[string]any{"authorization": r.Header.Get("Authorization")}
		if m.Method == "tools/list" {
			result["tools"] = []map[string]any{{"name": "search"}, {"name": "delete_everything"}}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", out)
	}))
	t.Cleanup(ext.Close)

	usersSignIn(a, p, usersAda)
	var dir []struct {
		Name      string `json:"name"`
		Connector bool   `json:"connector"`
		Installed *int64 `json:"installed"`
	}
	a.call("GET", "/api/connectors", nil, http.StatusOK, &dir)
	if i := slices.IndexFunc(dir, func(e struct {
		Name      string `json:"name"`
		Connector bool   `json:"connector"`
		Installed *int64 `json:"installed"`
	}) bool {
		return e.Name == "github"
	}); i < 0 || !dir[i].Connector {
		t.Fatalf("the directory: %+v", dir)
	}
	var github, tracker connectorsServer
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "ca_gh", "connector": "github", "url": g.srv.URL}, http.StatusCreated, &github)
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "ca_tracker", "url": ext.URL, "auth": "token"}, http.StatusCreated, &tracker)
	a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", github.ID), map[string]any{"token": "ghp-shared"}, http.StatusNoContent, nil)
	a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", tracker.ID), map[string]any{"token": "tracker-key"}, http.StatusNoContent, nil)
	a.call("GET", "/api/connectors", nil, http.StatusOK, &dir)
	if i := slices.IndexFunc(dir, func(e struct {
		Name      string `json:"name"`
		Connector bool   `json:"connector"`
		Installed *int64 `json:"installed"`
	}) bool {
		return e.Name == "github"
	}); dir[i].Installed == nil {
		t.Errorf("installed: %+v", dir[i])
	}

	f := a.newFlow("Agents on GitHub")
	usersShare(a, f.ID)
	use := a.agent(f.ID, "Use", "Use GitHub.")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", use.ID), map[string]any{"settings": map[string]any{"mcpServers": []int64{github.ID, tracker.ID}}}, http.StatusOK, nil)
	a.startAt(f.ID, use.ID)
	seen := make(chan map[string]string, 4)
	a.worker.onAgent("Use", func(ctx context.Context, run agentRun) runResult {
		seen <- map[string]string{
			"init":    connectorsMCP(ctx, run, "ca_gh", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"pi","version":"1"}}}`),
			"notice":  connectorsMCP(ctx, run, "ca_gh", `{"jsonrpc":"2.0","method":"notifications/initialized"}`),
			"tools":   connectorsMCP(ctx, run, "ca_gh", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`),
			"read":    connectorsMCP(ctx, run, "ca_gh", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_issue","arguments":{"owner":"acme","repo":"app","number":7}}}`),
			"write":   connectorsMCP(ctx, run, "ca_gh", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"add_comment","arguments":{"owner":"acme","repo":"app","number":7,"body":"hi"}}}`),
			"listed":  connectorsMCP(ctx, run, "ca_tracker", `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`),
			"removed": connectorsMCP(ctx, run, "ca_tracker", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"delete_everything"}}`),
		}
		return submitted(data("used"))
	})

	// Bob isn't an admin, so he can't set tokens, but he has the ones set
	// and nothing to sign in to.
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	a.refused("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", github.ID), map[string]any{"token": "mine"}, http.StatusForbidden, "only an admin")
	a.refused("DELETE", fmt.Sprintf("/api/mcp-servers/%d/token", github.ID), nil, http.StatusForbidden, "only an admin")
	if s := connectorsServers(a)["ca_gh"]; !s.Connected {
		t.Errorf("the connector isn't connected for bob: %+v", s)
	}
	resp, err := a.client.Get(fmt.Sprintf("%s/api/mcp-servers/%d/connect", a.url, github.ID))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "nobody signs in") {
		t.Errorf("signing in to a connector: %d %s", resp.StatusCode, page)
	}

	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("bob's run: %s %s", d.Status, describeSteps(d))
	}
	got := <-seen
	if !strings.HasPrefix(got["init"], "200 ") || !strings.Contains(got["init"], `"protocolVersion":"2025-06-18"`) {
		t.Errorf("initialize: %s", got["init"])
	}
	if !strings.HasPrefix(got["notice"], "202") {
		t.Errorf("the initialized notification: %s", got["notice"])
	}
	if !strings.Contains(got["tools"], `"get_issue"`) || strings.Contains(got["tools"], `"add_comment"`) {
		t.Errorf("tools without picking: %s", got["tools"])
	}
	if !strings.Contains(got["read"], `acme/app#7`) {
		t.Errorf("get_issue: %s", got["read"])
	}
	if !strings.Contains(got["write"], "isn't one this process uses") {
		t.Errorf("an unpicked write: %s", got["write"])
	}
	if !strings.Contains(got["listed"], "delete_everything") || !strings.Contains(got["listed"], "Bearer tracker-key") {
		t.Errorf("an MCP server without picking: %s", got["listed"])
	}
	for _, r := range g.got() {
		if strings.HasPrefix(r, "POST") || !strings.Contains(r, "Bearer ghp-shared") {
			t.Errorf("GitHub got %s", r)
		}
	}

	// Picking tools gives the agent those and no others.
	usersSignIn(a, p, usersAda)
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", use.ID), map[string]any{"settings": map[string]any{
		"mcpServers": []int64{github.ID, tracker.ID},
		"mcpTools":   map[string][]string{fmt.Sprint(github.ID): {"add_comment"}, fmt.Sprint(tracker.ID): {"search"}},
	}}, http.StatusOK, nil)
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("ada's run: %s %s", d.Status, describeSteps(d))
	}
	got = <-seen
	if strings.Contains(got["tools"], `"get_issue"`) || !strings.Contains(got["tools"], `"add_comment"`) {
		t.Errorf("picked tools: %s", got["tools"])
	}
	if !strings.Contains(got["read"], "isn't one this process uses") || !strings.Contains(got["write"], "spoletta-bot") {
		t.Errorf("picked: read %s, write %s", got["read"], got["write"])
	}
	if strings.Contains(got["listed"], "delete_everything") || !strings.Contains(got["listed"], `"search"`) || !strings.Contains(got["listed"], "data: ") {
		t.Errorf("an MCP server's picked tools: %s", got["listed"])
	}
	if !strings.Contains(got["removed"], "isn't one this process uses") || strings.Contains(got["removed"], "Bearer") {
		t.Errorf("an unpicked tool of an MCP server: %s", got["removed"])
	}

	// The tools to pick from, as the process's panel lists them.
	var tools []struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"readOnly"`
	}
	a.call("GET", fmt.Sprintf("/api/mcp-servers/%d/tools", github.ID), nil, http.StatusOK, &tools)
	if i := slices.IndexFunc(tools, func(t struct {
		Name     string `json:"name"`
		ReadOnly bool   `json:"readOnly"`
	}) bool {
		return t.Name == "merge_pull_request"
	}); i < 0 || tools[i].ReadOnly {
		t.Errorf("the connector's tools: %+v", tools)
	}

	// An MCP server's tools are asked of it, as an agent would.
	a.call("GET", fmt.Sprintf("/api/mcp-servers/%d/tools", tracker.ID), nil, http.StatusOK, &tools)
	if len(tools) != 2 || tools[0].Name != "search" || tools[1].Name != "delete_everything" {
		t.Errorf("the MCP server's tools: %+v", tools)
	}

	a.call("DELETE", fmt.Sprintf("/api/mcp-servers/%d/token", github.ID), nil, http.StatusNoContent, nil)
	d := a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Use"); s.Status != stepError || !strings.Contains(s.Error, "has no token") {
		t.Errorf("without its token: %s %s", s.Status, s.Error)
	}
}
