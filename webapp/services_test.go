package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A fake service: what it was asked, and the handler answering.
type fakeService struct {
	srv      *httptest.Server
	requests []string
	bodies   []string
	headers  []http.Header
}

func newFakeService(t *testing.T, handler http.HandlerFunc) *fakeService {
	f := &fakeService{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.bodies = append(f.bodies, string(body))
		f.headers = append(f.headers, r.Header.Clone())
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handler(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// Carries out an operation as a connector process does, from text.
func runOp(t *testing.T, c *connector, client connectorClient, name string, text map[string]string) (any, error) {
	t.Helper()
	op := c.operation(name)
	if op == nil {
		t.Fatalf("%s has no operation %s", c.Name, name)
	}
	a, err := op.coerce(text)
	if err != nil {
		return nil, err
	}
	return op.run(context.Background(), client, a)
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Slack answers a call that failed with a success; the connector takes it
// as the failure it is.
func TestSlackConnector(t *testing.T) {
	f := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["channel"] == "C0NOTFOUND" {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "channel_not_found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": body["channel"], "ts": "1730000000.000100",
				"message": map[string]any{"text": body["text"], "bot_id": "B1"}})
		case "/conversations.history":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": []map[string]any{{"ts": "1730000000.000200", "user": "U1", "text": "hi", "reply_count": 2}}})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "unknown_method"})
		}
	})
	client := slackConnector.client(f.srv.URL, "xoxb-1-2")

	v, err := runOp(t, slackConnector, client, "post_message", map[string]string{"channel": "C0123456789", "text": "Deployed", "thread_ts": "1730000000.000001"})
	if err != nil || !strings.Contains(asJSON(v), `"key":"C0123456789/1730000000.000100"`) || !strings.Contains(asJSON(v), `"text":"Deployed"`) {
		t.Errorf("post: %s %v", asJSON(v), err)
	}
	if f.headers[0].Get("Authorization") != "Bearer xoxb-1-2" || !strings.Contains(f.bodies[0], `"thread_ts":"1730000000.000001"`) {
		t.Errorf("request: %v %s", f.headers[0], f.bodies[0])
	}
	if _, err := runOp(t, slackConnector, client, "post_message", map[string]string{"channel": "C0NOTFOUND", "text": "x"}); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("ok: false: %v", err)
	}
	v, err = runOp(t, slackConnector, client, "list_messages", map[string]string{"channel": "C0123456789", "limit": "5"})
	if err != nil || !strings.Contains(asJSON(v), `"replies":2`) || !strings.Contains(f.requests[len(f.requests)-1], "limit=5") {
		t.Errorf("history: %s %v %v", asJSON(v), err, f.requests)
	}
	if _, err := runOp(t, slackConnector, client, "post_message", map[string]string{"channel": "general", "text": "x"}); err == nil {
		t.Errorf("a channel's name instead of its id was taken")
	}
	if slackConnector.checkToken("abc") == nil || slackConnector.checkToken("xoxb-123") != nil {
		t.Errorf("token check")
	}
}

// Telegram's token goes in the path, and so never in what a failed call
// says, whether Telegram answered or not.
func TestTelegramConnector(t *testing.T) {
	const token = "123456789:AAsecretsecretsecretsecret"
	f := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot"+token+"/") {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "description": "Unauthorized"})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch strings.TrimPrefix(r.URL.Path, "/bot"+token) {
		case "/sendMessage":
			if body["chat_id"] == "-1" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": map[string]any{"message_id": 5, "date": 1730000000, "text": body["text"],
				"chat": map[string]any{"id": -100123, "type": "group", "title": "Ops"}}})
		case "/getUpdates":
			if body["offset"] != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": []any{}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": []map[string]any{
				{"update_id": 10, "message": map[string]any{"message_id": 3, "date": 1730000000, "text": "status?",
					"chat": map[string]any{"id": 42, "type": "private"}, "from": map[string]any{"id": 42, "username": "ada"}}}}})
		}
	})
	client := telegramConnector.client(f.srv.URL, token)

	v, err := runOp(t, telegramConnector, client, "send_message", map[string]string{"chat_id": "-100123", "text": "Deployed", "reply_to": "3"})
	if err != nil || !strings.Contains(asJSON(v), `"key":"-100123/5"`) || !strings.Contains(f.bodies[0], `"reply_parameters":{"message_id":3}`) {
		t.Errorf("send: %s %v %s", asJSON(v), err, f.bodies)
	}
	v, err = runOp(t, telegramConnector, client, "get_updates", nil)
	if err != nil || !strings.Contains(asJSON(v), `"from":"@ada"`) || !strings.Contains(asJSON(v), `"updateId":10`) || strings.Contains(f.bodies[1], "offset") {
		t.Errorf("updates: %s %v %s", asJSON(v), err, f.bodies)
	}
	// Reading leaves the updates with Telegram; confirming them, which
	// changes something, is what makes it forget them.
	if _, err := runOp(t, telegramConnector, client, "confirm_updates", map[string]string{"through": "10"}); err != nil || !strings.Contains(f.bodies[2], `"offset":11`) {
		t.Errorf("confirm: %v %s", err, f.bodies)
	}
	if f := filterFor(mcpServer{Connector: "telegram"}, nil); !f.allows("get_updates") || f.allows("confirm_updates") || f.allows("send_message") {
		t.Errorf("without picking: %v", f)
	}
	_, err = runOp(t, telegramConnector, client, "send_message", map[string]string{"chat_id": "-1", "text": "x"})
	if err == nil || !strings.Contains(err.Error(), "chat not found") || strings.Contains(err.Error(), "secret") {
		t.Errorf("failure: %v", err)
	}
	f.srv.Close()
	_, err = runOp(t, telegramConnector, client, "send_message", map[string]string{"chat_id": "42", "text": "x"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("unreachable: %v", err)
	}
	if telegramConnector.checkToken("xoxb-1") == nil || telegramConnector.checkToken(token) != nil {
		t.Errorf("token check")
	}
}

// A service account signs in to Google with a JWT signed by its key, once
// an hour, and reads rows as objects named by the first row.
func TestGoogleConnector(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	keyJSON, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "flows@acme.iam.gserviceaccount.com",
		"private_key_id": "k1", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	if err := googleConnector.checkToken(string(keyJSON)); err != nil {
		t.Fatal(err)
	}
	if googleConnector.checkToken(`{"type":"authorized_user"}`) == nil || googleConnector.checkToken("abc") == nil {
		t.Errorf("token check took what isn't a service account's key")
	}

	var signIns atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signIns.Add(1)
		r.ParseForm()
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig) != nil || !strings.Contains(string(claims), `"iss":"flows@acme.iam.gserviceaccount.com"`) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "Invalid JWT Signature."})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "ya29.test", "expires_in": 3600})
	}))
	defer tokens.Close()
	defer func(u string) { googleTokenURL = u }(googleTokenURL)
	googleTokenURL = tokens.URL

	f := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ya29.test" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": 401, "message": "Request had invalid authentication credentials."}})
			return
		}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/values/Bugs!1:1"):
			writeJSON(w, http.StatusOK, map[string]any{"values": [][]any{{"Title", "Status"}}})
		case r.Method == "GET":
			writeJSON(w, http.StatusOK, map[string]any{"values": [][]any{{"Title", "Status"}, {"Crash", "open"}, {"Slow"}}})
		case r.Method == "POST":
			writeJSON(w, http.StatusOK, map[string]any{"updates": map[string]any{"updatedRange": "Bugs!A4:B4", "updatedRows": 1, "updatedCells": 2}})
		}
	})
	client := googleConnector.client(f.srv.URL, string(keyJSON))
	const sheet = "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"

	v, err := runOp(t, googleConnector, client, "read_rows", map[string]string{"spreadsheet_id": sheet, "range": "Bugs!A1:B"})
	if err != nil || asJSON(v) != `[{"Status":"open","Title":"Crash"},{"Status":"","Title":"Slow"}]` {
		t.Errorf("rows: %s %v", asJSON(v), err)
	}
	if f.requests[0] != "GET /sheets/v4/spreadsheets/"+sheet+"/values/Bugs%21A1:B" {
		t.Errorf("request: %v", f.requests)
	}
	v, err = runOp(t, googleConnector, client, "append_rows", map[string]string{"spreadsheet_id": sheet, "range": "Bugs", "rows": `{"Status":"open","Title":"New"}`})
	if err != nil || !strings.Contains(asJSON(v), `"rows":1`) || !strings.Contains(f.bodies[len(f.bodies)-1], `{"values":[["New","open"]]}`) {
		t.Errorf("append: %s %v %v", asJSON(v), err, f.bodies)
	}
	if n := signIns.Load(); n != 1 {
		t.Errorf("signed in %d times, want once an hour", n)
	}
	if _, err := runOp(t, googleConnector, client, "read_rows", map[string]string{"spreadsheet_id": "../x", "range": "A1"}); err == nil {
		t.Errorf("a path for a spreadsheet's id was taken")
	}
	if got := googleEndpoint(googleAPIs, "/sheets/v4/spreadsheets/x"); got != "https://sheets.googleapis.com/v4/spreadsheets/x" {
		t.Errorf("sheets endpoint %s", got)
	}
	if got := googleEndpoint(googleAPIs, "/drive/v3/files"); got != "https://www.googleapis.com/drive/v3/files" {
		t.Errorf("drive endpoint %s", got)
	}
}

func TestHomeAssistantConnector(t *testing.T) {
	f := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/states":
			writeJSON(w, http.StatusOK, []map[string]any{
				{"entity_id": "sensor.temp", "state": "21.5", "attributes": map[string]any{"friendly_name": "Temperature", "unit_of_measurement": "°C"}},
				{"entity_id": "light.kitchen", "state": "off", "attributes": map[string]any{"friendly_name": "Kitchen"}},
			})
		case "/api/services/light/turn_on":
			writeJSON(w, http.StatusOK, []map[string]any{{"entity_id": "light.kitchen", "state": "on"}})
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Entity not found."})
		}
	})
	client := homeAssistantConnector.client(f.srv.URL, "ha-token")
	v, err := runOp(t, homeAssistantConnector, client, "list_states", map[string]string{"domain": "sensor"})
	if err != nil || !strings.Contains(asJSON(v), `"name":"Temperature"`) || strings.Contains(asJSON(v), "kitchen") {
		t.Errorf("states: %s %v", asJSON(v), err)
	}
	v, err = runOp(t, homeAssistantConnector, client, "call_service", map[string]string{"domain": "light", "service": "turn_on", "entity_id": "light.kitchen", "data": `{"brightness_pct": 50}`})
	if err != nil || !strings.Contains(asJSON(v), `"state":"on"`) || f.bodies[1] != `{"brightness_pct":50,"entity_id":["light.kitchen"]}` {
		t.Errorf("service: %s %v %v", asJSON(v), err, f.bodies)
	}
	if _, err := runOp(t, homeAssistantConnector, client, "get_state", map[string]string{"entity_id": "sensor.gone"}); err == nil || !strings.Contains(err.Error(), "Entity not found") {
		t.Errorf("missing: %v", err)
	}
	if _, err := runOp(t, homeAssistantConnector, client, "call_service", map[string]string{"domain": "../config", "service": "x"}); err == nil {
		t.Errorf("a path for a domain was taken")
	}
	s := mcpServer{Name: "home", Connector: "homeassistant"}
	s.defaults()
	if p := s.problem(); !strings.Contains(p, "Home Assistant needs the URL") {
		t.Errorf("installed without a URL: %q", p)
	}
}

func TestHTTPConnector(t *testing.T) {
	f := newFakeService(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/notes" {
			io.WriteString(w, "plain text")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": r.URL.Path, "query": r.URL.RawQuery})
	})
	client := httpConnector.client(f.srv.URL+"/v1", "X-API-Key: abc123")
	v, err := runOp(t, httpConnector, client, "get", map[string]string{"path": "/tickets/42", "query": "status=open"})
	if err != nil || asJSON(v) != `{"path":"/v1/tickets/42","query":"status=open"}` || f.headers[0].Get("X-API-Key") != "abc123" || f.headers[0].Get("Authorization") != "" {
		t.Errorf("get: %s %v %v", asJSON(v), err, f.headers[0])
	}
	v, err = runOp(t, httpConnector, client, "get", map[string]string{"path": "/notes"})
	if err != nil || !strings.Contains(asJSON(v), `"text":"plain text"`) {
		t.Errorf("text: %s %v", asJSON(v), err)
	}
	if _, err := runOp(t, httpConnector, httpConnector.client(f.srv.URL, "tok"), "post", map[string]string{"path": "/t", "body": `{"a":1}`}); err != nil || f.headers[2].Get("Authorization") != "Bearer tok" || f.bodies[2] != `{"a":1}` {
		t.Errorf("post: %v %v %s", err, f.headers[2], f.bodies[2])
	}
	for _, path := range []string{"/../admin", "//evil.example/x", "/a/%2e%2e/b", "relative", "/a?b=c", "/x/./y"} {
		if _, err := runOp(t, httpConnector, client, "get", map[string]string{"path": path}); err == nil {
			t.Errorf("%s was called", path)
		}
	}
	if f := filterFor(mcpServer{Connector: "http"}, nil); !f.allows("get") || f.allows("post") || f.allows("delete") {
		t.Errorf("without picking: %v", f)
	}
}
