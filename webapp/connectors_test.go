package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fake GitHub answering what the tests ask of it, recording each request.
type fakeGitHub struct {
	srv      *httptest.Server
	requests []string
	bodies   []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/app/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"number": 7, "title": "Crash on start", "state": "open", "user": map[string]any{"login": "ada"},
				"labels": []map[string]any{{"name": "bug"}}, "repository_url": "https://api.github.com/repos/acme/app", "html_url": "https://github.com/acme/app/issues/7"},
			{"number": 8, "title": "A pull request", "state": "open", "pull_request": map[string]any{}, "repository_url": "https://api.github.com/repos/acme/app"},
		})
	})
	mux.HandleFunc("POST /repos/acme/app/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{"id": 1, "body": "seen", "user": map[string]any{"login": "bot"}})
	})
	mux.HandleFunc("POST /repos/acme/app/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{"number": 9, "title": "New", "state": "open", "repository_url": "https://api.github.com/repos/acme/app"})
	})
	mux.HandleFunc("GET /repos/acme/app/contents/docs/a b.md", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"type": "file", "path": "docs/a b.md", "size": 5, "encoding": "base64", "content": "aGVs\nbG8="})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	})
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.requests = append(g.requests, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization"))
		g.bodies = append(g.bodies, string(body))
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) client() connectorClient {
	return githubConnector.client(g.srv.URL, "gh-token")
}

// Sends one JSON-RPC request to the connector as an agent would.
func callConnector(t *testing.T, client connectorClient, filter toolFilter, method, body string) (int, rpcMessage) {
	t.Helper()
	w := httptest.NewRecorder()
	serveConnector(w, httptest.NewRequest(method, "/mcp/1", strings.NewReader(body)), githubConnector, client, filter, []byte(body))
	var m rpcMessage
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("%s: %v", w.Body.String(), err)
		}
	}
	return w.Code, m
}

// The connector answers as an MCP server, as pi's client talks to one:
// initialize, the initialized notification, then tools.
func TestConnectorMCP(t *testing.T) {
	g := newFakeGitHub(t)
	filter := filterFor(mcpServer{Connector: "github"}, nil)

	code, m := callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`)
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
	}
	json.Unmarshal(m.Result, &init)
	if code != http.StatusOK || init.ProtocolVersion != "2025-11-25" || init.Capabilities["tools"] == nil {
		t.Errorf("initialize: %d %s", code, m.Result)
	}
	_, m = callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if json.Unmarshal(m.Result, &init); init.ProtocolVersion != "2025-06-18" {
		t.Errorf("an unknown version got %s", init.ProtocolVersion)
	}
	if code, _ := callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Errorf("a notification got %d", code)
	}
	if code, _ := callConnector(t, g.client(), filter, "GET", ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET got %d", code)
	}

	// Without picking, an agent has only what reads.
	_, m = callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	json.Unmarshal(m.Result, &list)
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
		if tool.InputSchema["type"] != "object" || tool.Annotations["readOnlyHint"] != true {
			t.Errorf("%s: schema %v, annotations %v", tool.Name, tool.InputSchema, tool.Annotations)
		}
	}
	if !names["list_issues"] || !names["get_file"] || names["add_comment"] || names["merge_pull_request"] {
		t.Errorf("tools without picking: %v", names)
	}

	_, m = callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_issues","arguments":{"owner":"acme","repo":"app","labels":["bug"]}}}`)
	var call struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	json.Unmarshal(m.Result, &call)
	if call.IsError || len(call.Content) != 1 || !strings.Contains(call.Content[0].Text, `"key": "acme/app#7"`) || strings.Contains(call.Content[0].Text, "A pull request") {
		t.Errorf("list_issues: %s", m.Result)
	}
	if want := "GET /repos/acme/app/issues?direction=desc&labels=bug&per_page=20&sort=updated Bearer gh-token"; g.requests[len(g.requests)-1] != want {
		t.Errorf("GitHub got %s, want %s", g.requests[len(g.requests)-1], want)
	}

	_, m = callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"add_comment","arguments":{"owner":"acme","repo":"app","number":7,"body":"seen"}}}`)
	if m.Error == nil || !strings.Contains(m.Error.Message, "isn't one this process uses") {
		t.Errorf("an unpicked write: %+v", m)
	}
	picked := filterFor(mcpServer{Connector: "github"}, []string{"add_comment"})
	_, m = callConnector(t, g.client(), picked, "POST", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"add_comment","arguments":{"owner":"acme","repo":"app","number":7,"body":"seen"}}}`)
	if m.Error != nil || !strings.Contains(string(m.Result), "seen") {
		t.Errorf("a picked write: %+v %s", m.Error, m.Result)
	}
	if !strings.Contains(g.bodies[len(g.bodies)-1], `"body":"seen"`) {
		t.Errorf("GitHub got the comment %s", g.bodies[len(g.bodies)-1])
	}

	// Arguments that would reach elsewhere in the API are turned away
	// before anything is sent.
	sent := len(g.requests)
	for _, args := range []string{
		`{"owner":"acme","repo":".."}`,
		`{"owner":"../x","repo":"app"}`,
		`{"owner":"acme","repo":"app","limit":500}`,
		`{"owner":"acme","repo":"app","state":"merged"}`,
		`{"owner":"acme","repo":"app","color":"red"}`,
		`{"owner":"acme"}`,
	} {
		_, m = callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"list_issues","arguments":`+args+`}}`)
		if json.Unmarshal(m.Result, &call); !call.IsError {
			t.Errorf("list_issues %s: %s", args, m.Result)
		}
	}
	if len(g.requests) != sent {
		t.Errorf("bad arguments reached GitHub: %v", g.requests[sent:])
	}

	// GitHub's errors are the tool's.
	_, m = callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_issue","arguments":{"owner":"acme","repo":"app","number":404}}}`)
	if json.Unmarshal(m.Result, &call); !call.IsError || !strings.Contains(call.Content[0].Text, "Not Found") {
		t.Errorf("a missing issue: %s", m.Result)
	}
	_, m = callConnector(t, g.client(), filter, "POST", `{"jsonrpc":"2.0","id":8,"method":"resources/list"}`)
	if m.Error == nil || m.Error.Code != rpcMethodNotFound {
		t.Errorf("resources/list: %+v", m)
	}
}

func TestGitHubGetFile(t *testing.T) {
	g := newFakeGitHub(t)
	op := githubConnector.operation("get_file")
	v, err := op.run(t.Context(), g.client(), opArgs{"owner": "acme", "repo": "app", "path": "docs/a b.md", "ref": "main"})
	if err != nil {
		t.Fatal(err)
	}
	if f := v.(map[string]any); f["content"] != "hello" {
		t.Errorf("content %v", f["content"])
	}
	if want := "GET /repos/acme/app/contents/docs/a%20b.md?ref=main Bearer gh-token"; g.requests[0] != want {
		t.Errorf("GitHub got %s, want %s", g.requests[0], want)
	}
	for _, path := range []string{"../secrets", "docs/../../x", "a//b"} {
		if _, err := op.run(t.Context(), g.client(), opArgs{"owner": "acme", "repo": "app", "path": path}); err == nil {
			t.Errorf("get_file %s went through", path)
		}
	}
}

// A connector process's arguments are text, made of what its step takes.
func TestFillArgs(t *testing.T) {
	inputs := []runInput{
		{Name: "Bug", Content: `{"title":"Crash","number":7,"meta":{"repo":"app"},"labels":["bug","p1"]}`},
		{Name: "Note", Content: `{"data":"looked at it"}`},
		{Name: "Bugs", Content: `[{"title":"a"}]`},
	}
	filled, err := fillArgs(map[string]string{
		"title":  "Fix {{Bug.title}}",
		"number": "{{ Bug.number }}",
		"repo":   "{{Bug.meta.repo}}",
		"labels": "{{Bug.labels}}",
		"body":   "{{Note}}",
	}, inputs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"title": "Fix Crash", "number": "7", "repo": "app", "labels": `["bug","p1"]`, "body": "looked at it"}
	for k, v := range want {
		if filled[k] != v {
			t.Errorf("%s = %q, want %q", k, filled[k], v)
		}
	}
	for _, bad := range []string{"{{Issue.title}}", "{{Bug.color}}", "{{Bugs.title}}"} {
		if _, err := fillArgs(map[string]string{"title": bad}, inputs); err == nil {
			t.Errorf("%s filled in", bad)
		}
	}

	a, err := githubConnector.operation("create_issue").coerce(map[string]string{"owner": "acme", "repo": "app", "title": "t", "labels": `["bug","p1"]`, "assignees": "ada, bob"})
	if err != nil {
		t.Fatal(err)
	}
	if l := a.strings("labels"); len(l) != 2 || l[1] != "p1" {
		t.Errorf("labels %v", l)
	}
	if l := a.strings("assignees"); len(l) != 2 || l[1] != "bob" {
		t.Errorf("assignees %v", l)
	}
	if _, err := githubConnector.operation("get_issue").coerce(map[string]string{"owner": "acme", "repo": "app", "number": "seven"}); err == nil {
		t.Errorf("a number that isn't one went through")
	}
}

// What an operation answers becomes the object a connector process
// generates, its attributes named after the answer's fields.
func TestConnectorObject(t *testing.T) {
	answer := json.RawMessage(`[{"key":"acme/app#7","title":"Crash","user":{"login":"ada"}},{"key":"acme/app#8","title":"Slow","user":{"login":"bob"}}]`)
	list := shapedObject{name: "Issues", many: true, fields: []field{{name: "key"}, {name: "user.login"}}}
	out, err := connectorObject(list, answer)
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]string
	json.Unmarshal([]byte(out), &items)
	if len(items) != 2 || items[1]["key"] != "acme/app#8" || items[1]["user.login"] != "bob" {
		t.Errorf("list: %s", out)
	}
	if _, err := connectorObject(shapedObject{name: "Issues", many: true, fields: []field{{name: "colour"}}}, answer); err == nil || !strings.Contains(err.Error(), "such as key, title, user") {
		t.Errorf("a misnamed attribute: %v", err)
	}
	out, err = connectorObject(shapedObject{name: "Answer", data: true}, json.RawMessage(`{"merged":true}`))
	if err != nil || !strings.Contains(out, `"data"`) {
		t.Errorf("an object without attributes: %s %v", out, err)
	}
	out, err = connectorObject(shapedObject{name: "First", fields: []field{{name: "title"}}}, json.RawMessage(`{"title":"Crash","state":"open"}`))
	if err != nil || strings.Join(strings.Fields(out), "") != `{"title":"Crash"}` {
		t.Errorf("one object: %s %v", out, err)
	}
}
