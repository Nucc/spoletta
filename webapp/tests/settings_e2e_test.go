package tests

// Settings each person keeps for themselves: their model keys, and their
// connections to MCP servers, which agents of runs for them use.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

type settingsKey struct {
	Env       string   `json:"env"`
	Providers []string `json:"providers"`
	Set       bool     `json:"set"`
	Saved     bool     `json:"saved"`
	Hint      string   `json:"hint"`
}

type settingsKeys struct {
	Keys        []settingsKey `json:"keys"`
	WorkerError string        `json:"workerError"`
}

// The keys are the variables the worker's models read; only an admin saves
// one, which every agent gets, whoever started its run, over the worker's
// own. Keys aren't shown again.
func TestSettingsModelKeys(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	var keys settingsKeys
	a.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)
	if len(keys.Keys) != 2 || keys.Keys[0].Env != "GATEWAY_KEY" || !keys.Keys[0].Set || keys.Keys[0].Saved || keys.Keys[1].Set {
		t.Errorf("before any is saved: %+v", keys)
	}
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-instance-openai"}, http.StatusOK, &keys)
	if k := keys.Keys[1]; !k.Saved || k.Hint != "enai" || !slices.Equal(k.Providers, []string{"openai"}) {
		t.Errorf("saved: %+v", keys)
	}
	a.call("PUT", "/api/model-keys/GATEWAY_KEY", map[string]any{"key": "gw-saved"}, http.StatusOK, nil)
	a.refused("PUT", "/api/model-keys/ANTHROPIC_API_KEY", map[string]any{"key": "x"}, http.StatusBadRequest, "no model the worker offers reads")
	a.refused("PUT", "/api/model-keys/lower", map[string]any{"key": "x"}, http.StatusBadRequest, "variable's name")
	a.refused("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": " "}, http.StatusBadRequest, "key is required")
	f := a.newFlow("Keys")
	usersShare(a, f.ID)
	a.startAt(f.ID, a.agent(f.ID, "Write", "Write.").ID)
	a.worker.onAgent("Write", handsIn(data("done")))

	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	a.refused("GET", "/api/model-keys", nil, http.StatusForbidden, "only an admin")
	a.refused("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "mine"}, http.StatusForbidden, "only an admin")
	a.refused("DELETE", "/api/model-keys/OPENAI_API_KEY", nil, http.StatusForbidden, "only an admin")
	a.finish(a.startRun(f.ID).ID)
	runs := a.worker.runsOf("Write")
	got := runs[len(runs)-1].Keys
	if got["OPENAI_API_KEY"] != "sk-instance-openai" || got["GATEWAY_KEY"] != "gw-saved" {
		t.Errorf("bob's agent got keys %v", got)
	}

	usersSignIn(a, p, usersAda)
	a.call("DELETE", "/api/model-keys/OPENAI_API_KEY", nil, http.StatusNoContent, nil)
	a.finish(a.startRun(f.ID).ID)
	runs = a.worker.runsOf("Write")
	if got := runs[len(runs)-1].Keys; got["OPENAI_API_KEY"] != "" || got["GATEWAY_KEY"] == "" {
		t.Errorf("after removing OpenAI's key, the agent got %v", got)
	}
}

// An MCP server that signs in whoever comes, handing out the token the test
// says is next, and answers with the token a request came with.
type settingsMCP struct {
	srv *httptest.Server

	mu   sync.Mutex
	next string
	// The token each code the sign-in sent back stands for.
	codes map[string]string
}

func newSettingsMCP(t *testing.T) *settingsMCP {
	m := &settingsMCP{codes: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                 m.srv.URL,
			"authorization_endpoint": m.srv.URL + "/authorize",
			"token_endpoint":         m.srv.URL + "/token",
			"registration_endpoint":  m.srv.URL + "/register",
		})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{"client_id": "spoletta"})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		m.mu.Lock()
		code := fmt.Sprint("code-", len(m.codes))
		m.codes[code] = m.next
		m.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		m.mu.Lock()
		token := m.codes[r.Form.Get("code")]
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer"})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"authorization": r.Header.Get("Authorization")})
	})
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

// Connects the server for whoever is signed in, as the settings' window does.
func (m *settingsMCP) connect(a *app, server int64, token string) {
	a.t.Helper()
	m.mu.Lock()
	m.next = token
	m.mu.Unlock()
	resp, err := a.client.Get(fmt.Sprintf("%s/api/mcp-servers/%d/connect", a.url, server))
	if err != nil {
		a.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "is connected") {
		a.t.Fatalf("connecting ended with %d: %s", resp.StatusCode, body)
	}
}

type settingsServer struct {
	ID        int64 `json:"id"`
	Connected bool  `json:"connected"`
}

func settingsConnected(a *app, id int64) bool {
	a.t.Helper()
	var servers []settingsServer
	a.call("GET", "/api/mcp-servers", nil, http.StatusOK, &servers)
	i := slices.IndexFunc(servers, func(s settingsServer) bool { return s.ID == id })
	return i >= 0 && servers[i].Connected
}

// Only an admin configures MCP servers; each person connects them for
// themselves, and an agent reaches a server as whoever its run is for.
func TestSettingsMCPConnections(t *testing.T) {
	a, p := usersApp(t)
	m := newSettingsMCP(t)
	usersSignIn(a, p, usersAda)
	var server settingsServer
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "tracker", "url": m.srv.URL + "/mcp"}, http.StatusCreated, &server)
	f := a.newFlow("MCP")
	usersShare(a, f.ID)
	use := a.agent(f.ID, "Use", "Use the tracker.")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", use.ID), map[string]any{"settings": map[string]any{"mcpServers": []int64{server.ID}}}, http.StatusOK, nil)
	a.startAt(f.ID, use.ID)
	reached := make(chan string, 4)
	a.worker.onAgent("Use", func(ctx context.Context, run agentRun) runResult {
		req, _ := http.NewRequestWithContext(ctx, "POST", run.MCPServers[0].URL, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+run.MCPToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			reached <- err.Error()
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			reached <- string(body)
		}
		return submitted(data("used"))
	})

	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email)
	usersAdd(a, carl.email)
	usersSignIn(a, p, bob)
	a.refused("POST", "/api/mcp-servers", map[string]any{"name": "mine", "url": m.srv.URL}, http.StatusForbidden, "only an admin")
	a.refused("DELETE", fmt.Sprintf("/api/mcp-servers/%d", server.ID), nil, http.StatusForbidden, "only an admin")
	m.connect(a, server.ID, "bob-token")
	if !settingsConnected(a, server.ID) {
		t.Errorf("bob's server isn't connected for him")
	}
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("bob's run %s: %s", d.Status, describeSteps(d))
	}
	if body := <-reached; !strings.Contains(body, "Bearer bob-token") {
		t.Errorf("bob's agent reached the server with %s", body)
	}

	// Carl hasn't connected it, so his run's agent can't use it.
	usersSignIn(a, p, carl)
	if settingsConnected(a, server.ID) {
		t.Errorf("bob's connection shows as carl's")
	}
	d := a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Use"); s.Status != stepError || !strings.Contains(s.Error, "isn't connected for Carl") {
		t.Errorf("carl's step %s: %s", s.Status, s.Error)
	}
	m.connect(a, server.ID, "carl-token")
	a.finish(a.startRun(f.ID).ID)
	if body := <-reached; !strings.Contains(body, "Bearer carl-token") {
		t.Errorf("carl's agent reached the server with %s", body)
	}
	a.call("POST", fmt.Sprintf("/api/mcp-servers/%d/disconnect", server.ID), nil, http.StatusNoContent, nil)
	if settingsConnected(a, server.ID) {
		t.Errorf("carl's server is still connected")
	}
	usersSignIn(a, p, bob)
	if !settingsConnected(a, server.ID) {
		t.Errorf("carl disconnecting disconnected bob")
	}
}
