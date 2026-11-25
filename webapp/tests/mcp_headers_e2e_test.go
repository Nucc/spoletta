package tests

// Headers an MCP server's owners give it, such as API keys in headers of
// the server's own: the proxy puts them on every request an agent makes to
// it, and the webapp on its own asking for the server's tools; a server may
// take nothing else. Only owners set them, nobody sees a value again, and
// changing the server's URL drops them.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type headersServer struct {
	ID        int64    `json:"id"`
	Auth      string   `json:"auth"`
	Connected bool     `json:"connected"`
	Headers   []string `json:"headers"`
}

// What the fake server saw of a request: the keys it came with and its
// Authorization.
type headersSeen struct {
	APIKey        string `json:"apiKey"`
	AppKey        string `json:"appKey"`
	Authorization string `json:"authorization"`
}

// An MCP server taking its keys in headers of its own: it lists its tools
// only to a request with an API key, naming its one tool after it, and
// answers anything else with what it saw.
func newHeadersMCP(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		seen := headersSeen{APIKey: r.Header.Get("X-Api-Key"), AppKey: r.Header.Get("X-App-Key"), Authorization: r.Header.Get("Authorization")}
		switch m.Method {
		case "initialize":
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if seen.APIKey == "" {
				http.Error(w, "no API key", http.StatusUnauthorized)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"tools": []any{map[string]any{"name": "tool-for-" + seen.APIKey}}}})
		default:
			writeJSON(w, http.StatusOK, seen)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func headersOf(a *app, id int64) headersServer {
	a.t.Helper()
	var servers []headersServer
	a.call("GET", "/api/mcp-servers", nil, http.StatusOK, &servers)
	i := slices.IndexFunc(servers, func(s headersServer) bool { return s.ID == id })
	if i < 0 {
		a.t.Fatalf("no server %d among %+v", id, servers)
	}
	return servers[i]
}

func TestMCPServerHeaders(t *testing.T) {
	parallel(t)
	forgetMCPServers(t, "keyed", "signed_keyed")
	a, p := usersApp(t)
	db := secretsDB(t)
	srv := newHeadersMCP(t)
	usersSignIn(a, p, usersAda)

	var keyed headersServer
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "keyed", "url": srv.URL + "/mcp", "auth": "headers"}, http.StatusCreated, &keyed)
	if keyed.Auth != "headers" || keyed.Connected || keyed.Headers == nil || len(keyed.Headers) != 0 {
		t.Errorf("a new server taking only headers: %+v", keyed)
	}
	headers := fmt.Sprintf("/api/mcp-servers/%d/headers", keyed.ID)
	a.refused("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", keyed.ID), map[string]any{"token": "t"}, http.StatusBadRequest, "headers alone")
	a.refused("GET", fmt.Sprintf("/api/mcp-servers/%d/tools", keyed.ID), nil, http.StatusConflict, "set the server's headers")
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{"Host": "elsewhere.example.com"}}, http.StatusBadRequest, "sets Host itself")
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{"X_Forwarded_For": "10.0.0.1"}}, http.StatusBadRequest, "sets X_Forwarded_For itself")
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{"Mcp-Session-Id": "s"}}, http.StatusBadRequest, "itself")
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{"X Api Key": "k"}}, http.StatusBadRequest, "isn't a header's name")
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{"X-Api-Key": "k\r\nHost: elsewhere"}}, http.StatusBadRequest, "line break")
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{"X-Api-Key": "a", "x-api-key": "b"}}, http.StatusBadRequest, "the same header")
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{}}, http.StatusBadRequest, "name the headers")
	tooMany := map[string]any{}
	for i := range 21 {
		tooMany[fmt.Sprintf("X-Key-%d", i)] = "k"
	}
	a.refused("PUT", headers, map[string]any{"headers": tooMany}, http.StatusBadRequest, "up to 20 headers")

	a.call("PUT", headers, map[string]any{"headers": map[string]any{"X-Api-Key": "api-secret-1", "X-App-Key": "app-secret-1"}}, http.StatusNoContent, nil)
	if s := headersOf(a, keyed.ID); !s.Connected || !slices.Equal(s.Headers, []string{"X-Api-Key", "X-App-Key"}) {
		t.Errorf("with its headers set: %+v", s)
	}
	if _, body := a.do("GET", "/api/mcp-servers", nil); strings.Contains(string(body), "secret-1") {
		t.Errorf("the servers' list shows a header's value: %s", body)
	}
	if v := secretsStored(t, db, "SELECT value FROM mcp_server_headers WHERE server_id = ? AND name = 'X-Api-Key'", keyed.ID); !strings.HasPrefix(v, "sealed:v1:") || strings.Contains(v, "api-secret-1") {
		t.Errorf("the header is kept as %q", v)
	}
	var tools []struct {
		Name string `json:"name"`
	}
	a.call("GET", fmt.Sprintf("/api/mcp-servers/%d/tools", keyed.ID), nil, http.StatusOK, &tools)
	if len(tools) != 1 || tools[0].Name != "tool-for-api-secret-1" {
		t.Errorf("the server's tools, asked with its headers: %+v", tools)
	}
	a.auditEvent("kind:mcp.headers.set keyed", func(who, detail string) bool {
		return detail == "the headers X-Api-Key, X-App-Key of the MCP server keyed"
	})

	// An agent reaches it through the proxy with its headers, and no bearer
	// token, as it takes none.
	f := a.newFlow("Keyed MCP")
	usersShare(a, f.ID)
	use := a.agent(f.ID, "Use", "Use the keyed server.")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", use.ID), map[string]any{"settings": map[string]any{"mcpServers": []int64{keyed.ID}}}, http.StatusOK, nil)
	a.startAt(f.ID, use.ID)
	reached := make(chan headersSeen, 4)
	a.worker.onAgent("Use", func(ctx context.Context, run agentRun) runResult {
		req, _ := http.NewRequestWithContext(ctx, "POST", run.MCPServers[0].URL, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+run.MCPToken)
		// The agent's own headers of the same name stay behind.
		req.Header.Set("X-Api-Key", "the agent's own")
		var seen headersSeen
		if resp, err := http.DefaultClient.Do(req); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			json.Unmarshal(body, &seen)
		}
		reached <- seen
		return submitted(data("used"))
	})
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("the run %s: %s", d.Status, describeSteps(d))
	}
	if seen := receive(t, reached, "the agent reaching the server"); seen != (headersSeen{APIKey: "api-secret-1", AppKey: "app-secret-1"}) {
		t.Errorf("the server saw %+v", seen)
	}

	// Setting one again in another case replaces it, and null removes one.
	a.call("PUT", headers, map[string]any{"headers": map[string]any{"x-api-key": "api-secret-2", "X-App-Key": nil}}, http.StatusNoContent, nil)
	if s := headersOf(a, keyed.ID); !slices.Equal(s.Headers, []string{"x-api-key"}) {
		t.Errorf("after replacing one and removing the other: %+v", s)
	}
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("the second run %s: %s", d.Status, describeSteps(d))
	}
	if seen := receive(t, reached, "the agent reaching the server again"); seen != (headersSeen{APIKey: "api-secret-2"}) {
		t.Errorf("the server then saw %+v", seen)
	}
	// A server taking only headers may be given an Authorization of its own.
	a.call("PUT", headers, map[string]any{"headers": map[string]any{"Authorization": "Token api-secret-3"}}, http.StatusNoContent, nil)
	a.finish(a.startRun(f.ID).ID)
	if seen := receive(t, reached, "the agent reaching the server with its Authorization"); seen.Authorization != "Token api-secret-3" {
		t.Errorf("the server then saw %+v", seen)
	}

	// A server signed in to takes headers beside its token, but not
	// Authorization, which is its token's.
	var signed headersServer
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "signed_keyed", "url": srv.URL + "/mcp"}, http.StatusCreated, &signed)
	a.refused("PUT", fmt.Sprintf("/api/mcp-servers/%d/headers", signed.ID), map[string]any{"headers": map[string]any{"Authorization": "Token x"}}, http.StatusBadRequest, "with the server's token")
	a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/headers", signed.ID), map[string]any{"headers": map[string]any{"X-App-Key": "app"}}, http.StatusNoContent, nil)
	if s := headersOf(a, signed.ID); s.Connected || !slices.Equal(s.Headers, []string{"X-App-Key"}) {
		t.Errorf("a server signed in to, with a header: %+v", s)
	}

	// Only an owner sets them; anyone in the project sees their names.
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	a.refused("PUT", headers, map[string]any{"headers": map[string]any{"X-Api-Key": "bob's"}}, http.StatusForbidden, "only an admin")
	if s := headersOf(a, keyed.ID); len(s.Headers) != 2 {
		t.Errorf("bob sees the server as %+v", s)
	}

	// Its URL changing drops them, so they don't go to another host.
	usersSignIn(a, p, usersAda)
	a.call("PATCH", fmt.Sprintf("/api/mcp-servers/%d", keyed.ID), map[string]any{"url": srv.URL + "/mcp?moved=1"}, http.StatusOK, nil)
	if s := headersOf(a, keyed.ID); s.Connected || len(s.Headers) != 0 {
		t.Errorf("after its URL changed: %+v", s)
	}
	d := a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Use"); s.Status != stepError || !strings.Contains(s.Error, "has no headers") {
		t.Errorf("a step using it without headers %s: %s", s.Status, s.Error)
	}
}
