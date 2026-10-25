package tests

// The connectors for services besides GitHub: Slack, Telegram, Home
// Assistant and any HTTP API, installed from the directory, each checking
// the token an admin sets, and carried out by connector processes.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type servicesDirectoryEntry struct {
	Name       string `json:"name"`
	Category   string `json:"category"`
	Connector  bool   `json:"connector"`
	URL        string `json:"url"`
	URLExample string `json:"urlExample"`
	Multiple   bool   `json:"multiple"`
	Installed  *int64 `json:"installed"`
}

func TestServiceConnectors(t *testing.T) {
	forgetMCPServers(t, "sc_slack", "sc_home", "sc_telegram", "sc_api_one", "sc_api_two")
	a := newApp(t)

	var dir []servicesDirectoryEntry
	a.call("GET", "/api/connectors", nil, http.StatusOK, &dir)
	byName := map[string]servicesDirectoryEntry{}
	for _, e := range dir {
		byName[e.Name] = e
	}
	for _, name := range []string{"github", "slack", "telegram", "google", "homeassistant", "http"} {
		if !byName[name].Connector {
			t.Errorf("the directory has no connector %s: %+v", name, byName[name])
		}
	}
	if ha := byName["homeassistant"]; ha.URL != "" || ha.URLExample == "" || ha.Category != "Home" {
		t.Errorf("Home Assistant, which is wherever it is installed: %+v", ha)
	}
	if !byName["http"].Multiple || byName["slack"].Multiple || byName["slack"].Category != "Communication" {
		t.Errorf("multiple: %+v %+v", byName["http"], byName["slack"])
	}

	// Home Assistant has no URL but the one its admin gives.
	status, body := a.do("POST", "/api/mcp-servers", map[string]any{"name": "sc_home", "connector": "homeassistant"})
	if status != http.StatusBadRequest || !strings.Contains(string(body), "Home Assistant needs the URL") {
		t.Errorf("Home Assistant without a URL: %d %s", status, body)
	}

	// A fake Slack, which answers a call that failed with a success.
	var mu sync.Mutex
	var posted []string
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		posted = append(posted, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		mu.Unlock()
		var msg map[string]any
		json.Unmarshal(b, &msg)
		if msg["channel"] != "C0123456789" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "channel_not_found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": msg["channel"], "ts": "1730000000.000100", "message": map[string]any{"text": msg["text"]}})
	}))
	defer slack.Close()

	res := setupImportOK(a, `spoletta: 1
mcpServers:
  - name: sc_slack
    connector: slack
    url: `+slack.URL+`
flows:
  - name: SC Announce
    start: {next: Write}
    processes:
      - name: Write
        yields: [Note]
        prompt: Write the note.
        next: [Announce]
      - name: Announce
        kind: connector
        connector:
          server: sc_slack
          operation: post_message
          args: {channel: "{{Note.channel}}", text: "Shipped: {{Note.text}}"}
        takes: [Note]
        yields: [Posted]
    objects:
      - name: Note
        attributes:
          - name: channel
          - name: text
      - name: Posted
        attributes:
          - name: key
          - name: ts
        end: true
`)
	announce := res.Flows[0].ID
	server := connectorsServers(a)["sc_slack"]

	status, body = a.do("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", server.ID), map[string]any{"token": "ghp-not-slack"})
	if status != http.StatusBadRequest || !strings.Contains(string(body), "xoxb-") {
		t.Errorf("a token that isn't Slack's: %d %s", status, body)
	}
	a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", server.ID), map[string]any{"token": "xoxb-sc-secret"}, http.StatusNoContent, nil)

	a.worker.onAgent("Write", handsIn(map[string]any{"channel": "C0123456789", "text": "the search"}))
	d := a.finish(a.startRun(announce).ID)
	if d.Status != runDone {
		t.Fatalf("announcing: %s %s", d.Status, describeSteps(d))
	}
	if cs := contentsOf(d, "Posted"); len(cs) != 1 || !strings.Contains(cs[0], `"C0123456789/1730000000.000100"`) {
		t.Errorf("Posted: %v", cs)
	}
	mu.Lock()
	last := posted[len(posted)-1]
	mu.Unlock()
	if last != `/chat.postMessage Bearer xoxb-sc-secret {"channel":"C0123456789","text":"Shipped: the search"}` {
		t.Errorf("Slack got %s", last)
	}

	a.worker.onAgent("Write", handsIn(map[string]any{"channel": "C0000000000", "text": "x"}))
	d = a.finish(a.startRun(announce).ID)
	if s := onlyStep(t, d, "Announce"); s.Status != stepError || !strings.Contains(s.Error, "channel_not_found") {
		t.Errorf("Slack saying ok: false: %s %s", s.Status, s.Error)
	}

	// Telegram's token is checked as BotFather gives it.
	tg := map[string]any{}
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "sc_telegram", "connector": "telegram"}, http.StatusCreated, &tg)
	status, _ = a.do("PUT", fmt.Sprintf("/api/mcp-servers/%v/token", tg["id"]), map[string]any{"token": "xoxb-1"})
	if status != http.StatusBadRequest {
		t.Errorf("a token that isn't a bot's: %d", status)
	}

	// The HTTP API connector is installed once for each API.
	for _, name := range []string{"sc_api_one", "sc_api_two"} {
		a.call("POST", "/api/mcp-servers", map[string]any{"name": name, "connector": "http", "url": "https://api.example.com/v1"}, http.StatusCreated, nil)
	}
	servers := connectorsServers(a)
	if servers["sc_api_one"].Connector != "http" || servers["sc_api_two"].Connector != "http" {
		t.Errorf("the HTTP APIs: %+v %+v", servers["sc_api_one"], servers["sc_api_two"])
	}
}

// The board offers Sentry processes once the webapp has Sentry's token.
func TestSentryConfigured(t *testing.T) {
	for _, c := range []struct {
		env  []string
		want bool
	}{{nil, false}, {[]string{"SENTRY_AUTH_TOKEN=sntrys_abc"}, true}} {
		a := startApp(t, newFakeWorker(t).url, c.env...)
		var got struct {
			Configured bool `json:"configured"`
		}
		a.call("GET", "/api/sentry", nil, http.StatusOK, &got)
		if got.Configured != c.want {
			t.Errorf("with %v, Sentry configured: %v", c.env, got.Configured)
		}
	}
}
