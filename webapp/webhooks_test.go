package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testHookSecret = "whsec_0123456789abcdef"

func signedHex(secret string, parts ...string) string {
	b := make([][]byte, len(parts))
	for i, p := range parts {
		b[i] = []byte(p)
	}
	return hex.EncodeToString(hmacSHA256(secret, b...))
}

// A GitHub delivery holds the secret when its X-Hub-Signature-256 is the
// HMAC of its body; nothing else will do.
func TestWebhookGitHubSignatures(t *testing.T) {
	s := webhookSigner{kind: webhookGitHub, secret: testHookSecret}
	body := []byte(`{"action":"labeled"}`)
	now := time.Now()
	for sig, want := range map[string]string{
		"sha256=" + signedHex(testHookSecret, string(body)): "",
		"":                                      "no X-Hub-Signature-256",
		signedHex(testHookSecret, string(body)): "doesn't match",
		"sha256=" + signedHex("another secret entirely", string(body)): "doesn't match",
		"sha256=" + signedHex(testHookSecret, string(body)+" "):        "doesn't match",
		"sha256=zz": "doesn't match",
	} {
		h := http.Header{}
		if sig != "" {
			h.Set("X-Hub-Signature-256", sig)
		}
		got := s.refuse(h, body, now)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("signature %q: %q, want %q", sig, got, want)
		}
		if strings.Contains(got, testHookSecret) {
			t.Errorf("the refusal %q holds the secret", got)
		}
	}
}

// A Slack request holds the signing secret when its signature is the HMAC
// of its timestamp and body, and is refused when that timestamp is more
// than five minutes off, either way.
func TestWebhookSlackSignatures(t *testing.T) {
	s := webhookSigner{kind: webhookSlack, secret: testHookSecret}
	body := `{"type":"event_callback"}`
	now := time.Now()
	sign := func(at time.Time) http.Header {
		ts := strconv.FormatInt(at.Unix(), 10)
		h := http.Header{}
		h.Set("X-Slack-Request-Timestamp", ts)
		h.Set("X-Slack-Signature", "v0="+signedHex(testHookSecret, "v0:"+ts+":", body))
		return h
	}
	if got := s.refuse(sign(now), []byte(body), now); got != "" {
		t.Errorf("a signed request is refused: %s", got)
	}
	for _, at := range []time.Time{now.Add(-6 * time.Minute), now.Add(6 * time.Minute)} {
		if got := s.refuse(sign(at), []byte(body), now); !strings.Contains(got, "5 minutes off") {
			t.Errorf("one signed at %s: %q", at.Sub(now), got)
		}
	}
	h := sign(now)
	// The timestamp is signed too.
	h.Set("X-Slack-Request-Timestamp", strconv.FormatInt(now.Unix()+1, 10))
	if got := s.refuse(h, []byte(body), now); !strings.Contains(got, "doesn't match") {
		t.Errorf("another timestamp: %q", got)
	}
	if got := s.refuse(http.Header{}, []byte(body), now); !strings.Contains(got, "doesn't come from Slack") {
		t.Errorf("an unsigned one: %q", got)
	}
}

// A generic webhook takes an HMAC of the body in its header, as hex or
// base64 with or without sha256=, or the secret itself, bare or as a
// bearer token.
func TestWebhookGenericSignatures(t *testing.T) {
	body := []byte("alert: disk full")
	mac := hmacSHA256(testHookSecret, body)
	now := time.Now()
	hmacs := webhookSigner{kind: webhookGeneric, verify: verifyHMAC, header: "X-Signature", secret: testHookSecret}
	for v, ok := range map[string]bool{
		hex.EncodeToString(mac):                   true,
		"sha256=" + hex.EncodeToString(mac):       true,
		base64.StdEncoding.EncodeToString(mac):    true,
		base64.RawURLEncoding.EncodeToString(mac): true,
		hex.EncodeToString(mac[:16]):              false,
		testHookSecret:                            false,
		"":                                        false,
	} {
		h := http.Header{}
		if v != "" {
			h.Set("X-Signature", v)
		}
		if got := hmacs.refuse(h, body, now); (got == "") != ok {
			t.Errorf("an HMAC of %q: %q", v, got)
		}
	}
	tokens := webhookSigner{kind: webhookGeneric, verify: verifyToken, header: "Authorization", secret: testHookSecret}
	for v, ok := range map[string]bool{
		testHookSecret:             true,
		"Bearer " + testHookSecret: true,
		testHookSecret + "x":       false,
		testHookSecret[:10]:        false,
		"":                         false,
	} {
		h := http.Header{}
		if v != "" {
			h.Set("Authorization", v)
		}
		got := tokens.refuse(h, body, now)
		if (got == "") != ok {
			t.Errorf("the token %q: %q", v, got)
		}
		if strings.Contains(got, testHookSecret) {
			t.Errorf("the refusal %q holds the secret", got)
		}
	}
}

// A path goes into objects by key and into arrays by index; the empty path
// is the whole payload.
func TestWebhookPayloadPaths(t *testing.T) {
	payload, err := decodePayload([]byte(`{"issue": {"title": "Jam", "number": 42, "labels": [{"name": "bug"}], "body": null}}`))
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"issue.title":         `"Jam"`,
		"issue.number":        `42`,
		"issue.labels.0.name": `"bug"`,
		"issue.body":          `null`,
		"issue.labels.1":      "",
		"issue.labels.x":      "",
		"issue.title.more":    "",
		"pull_request":        "",
	} {
		v, ok := payloadAt(payload, path)
		got := ""
		if ok {
			b, _ := json.Marshal(v)
			got = string(b)
		}
		if got != want {
			t.Errorf("%s is %s, want %s", path, got, want)
		}
	}
	if v, ok := payloadAt(payload, ""); !ok || v.(map[string]any)["issue"] == nil {
		t.Errorf("the whole payload is %v", v)
	}
	for p, ok := range map[string]bool{"": true, "issue.title": true, "a..b": false, ".a": false, "a.": false, "a\nb": false} {
		if got := checkPayloadPath(p); (got == "") != ok {
			t.Errorf("the path %q: %q", p, got)
		}
	}
}

// The mapping makes a request of what the flow starts from: a path, the
// whole payload, or a path for each attribute; what it leaves out, or a
// path the payload lacks, refuses it, and what the flow doesn't start from
// goes unused.
func TestWebhookMappingRequests(t *testing.T) {
	payload, _ := decodePayload([]byte(`{"action": "labeled", "issue": {"title": "Jam", "number": 42}}`))
	in := []givenObject{
		{id: 1, name: "Ticket", fields: []field{{name: "id"}, {name: "subject"}}},
		{id: 2, name: "Number"},
		{id: 3, name: "Event"},
	}
	m := webhookMapping{
		"Ticket": {Attributes: map[string]string{"id": "issue.number", "subject": "issue.title"}},
		"Number": {Path: "issue.number"},
		"Event":  {},
		"Gone":   {Path: "issue"},
	}
	req, err := m.request(in, payload)
	if err != nil {
		t.Fatal(err)
	}
	given, err := givenValues(in, req)
	if err != nil {
		t.Fatalf("%v, of %v", err, req.Inputs)
	}
	if got := given[1].content; !strings.Contains(got, `"id": 42`) || !strings.Contains(got, `"subject": "Jam"`) {
		t.Errorf("the ticket holds %s", got)
	}
	if got := given[2].content; got != "42" {
		t.Errorf("the number holds %s", got)
	}
	if got := given[3].content; !strings.Contains(got, `"action":"labeled"`) {
		t.Errorf("the whole payload is %s", got)
	}
	if ps := m.problems(in); len(ps) != 1 || !strings.Contains(ps[0], `"Gone"`) {
		t.Errorf("the problems are %q", ps)
	}

	missing := webhookMapping{"Ticket": {Attributes: map[string]string{"id": "issue.number", "subject": "issue.label"}}, "Number": {}, "Event": {}}
	if _, err := missing.request(in, payload); err == nil || !strings.Contains(err.Error(), "no issue.label") {
		t.Errorf("a path the payload lacks: %v", err)
	}
	if _, err := (webhookMapping{"Ticket": {}, "Number": {}}).request(in, payload); err == nil || !strings.Contains(err.Error(), `nothing for "Event"`) {
		t.Errorf("an object left out: %v", err)
	}
	short := webhookMapping{"Ticket": {Attributes: map[string]string{"id": "issue.number", "extra": "issue.title"}}, "Number": {Attributes: map[string]string{"x": "issue"}}, "Event": {}}
	ps := strings.Join(short.problems(in), "\n")
	for _, want := range []string{`gives "Ticket" no "subject"`, `"Ticket" has no attribute "extra"`, `"Number" is given a path for each attribute`} {
		if !strings.Contains(ps, want) {
			t.Errorf("the problems %q lack %q", ps, want)
		}
	}
	if (webhookMapping{}).problems(in)[0] == "" {
		t.Errorf("an empty mapping has no problems")
	}
}

// GitHub's deliveries say their event in a header and their action in the
// payload, which may come as a form's field; a webhook takes the events it
// names, alone or with an action, and answers a ping.
func TestWebhookGitHubEvents(t *testing.T) {
	events := []string{"issues.labeled", "pull_request"}
	read := func(event, ct, body string) (webhookPayload, string) {
		h := http.Header{}
		h.Set("X-GitHub-Event", event)
		h.Set("X-GitHub-Delivery", "d-1")
		if ct != "" {
			h.Set("Content-Type", ct)
		}
		return readPayload(webhookGitHub, "", events, h, []byte(body))
	}
	for _, c := range []struct {
		event, body, name string
		ignored           bool
	}{
		{"issues", `{"action": "labeled"}`, "issues.labeled", false},
		{"issues", `{"action": "opened"}`, "issues.opened", true},
		{"pull_request", `{"action": "closed"}`, "pull_request.closed", false},
		{"push", `{"ref": "main"}`, "push", true},
	} {
		p, msg := read(c.event, "application/json", c.body)
		if msg != "" || p.eventName() != c.name || (p.ignored != "") != c.ignored || p.delivery != "d-1" {
			t.Errorf("%s %s: %+v, %q", c.event, c.body, p, msg)
		}
	}
	if p, _ := read("ping", "", `{"zen": "Keep it simple."}`); p.answer == nil {
		t.Errorf("a ping isn't answered: %+v", p)
	}
	p, msg := read("issues", "application/x-www-form-urlencoded", "payload="+`%7B%22action%22%3A%22labeled%22%7D`)
	if msg != "" || p.eventName() != "issues.labeled" || p.text != `{"action":"labeled"}` {
		t.Errorf("a form's payload: %+v, %q", p, msg)
	}
	if _, msg := read("issues", "application/x-www-form-urlencoded", "x=1"); !strings.Contains(msg, "application/json") {
		t.Errorf("a form without its payload: %q", msg)
	}
	if _, msg := read("issues", "", "not json"); !strings.Contains(msg, "isn't JSON") {
		t.Errorf("a payload not JSON: %q", msg)
	}
	if _, msg := read("", "", "{}"); !strings.Contains(msg, "X-GitHub-Event") {
		t.Errorf("no event: %q", msg)
	}
}

// Slack's URL verification is answered with its challenge; its event
// callbacks are taken by the events named, if any, by their event id.
func TestWebhookSlackEvents(t *testing.T) {
	p, msg := readPayload(webhookSlack, "", nil, http.Header{}, []byte(`{"type": "url_verification", "challenge": "abc"}`))
	if msg != "" || p.answer.(map[string]string)["challenge"] != "abc" {
		t.Errorf("the URL verification: %+v, %q", p, msg)
	}
	callback := []byte(`{"type": "event_callback", "event_id": "Ev1", "event": {"type": "app_mention", "text": "hi"}}`)
	p, msg = readPayload(webhookSlack, "", nil, http.Header{}, callback)
	if msg != "" || p.event != "app_mention" || p.delivery != "Ev1" || p.ignored != "" || p.answer != nil {
		t.Errorf("an event: %+v, %q", p, msg)
	}
	if p, _ := readPayload(webhookSlack, "", []string{"message"}, http.Header{}, callback); p.ignored == "" {
		t.Errorf("an event not named isn't ignored: %+v", p)
	}
	if p, _ := readPayload(webhookSlack, "", nil, http.Header{}, []byte(`{"type": "app_rate_limited"}`)); p.ignored == "" {
		t.Errorf("what isn't an event isn't ignored: %+v", p)
	}
}

// A generic webhook takes any body, JSON or its text, with its id from the
// header it names.
func TestWebhookGenericPayloads(t *testing.T) {
	h := http.Header{}
	h.Set("X-Request-Id", "r-7")
	p, msg := readPayload(webhookGeneric, "X-Request-Id", nil, h, []byte("disk full"))
	if msg != "" || p.value != "disk full" || p.delivery != "r-7" {
		t.Errorf("text: %+v, %q", p, msg)
	}
	h.Set("X-Request-Id", strings.Repeat("x", 256))
	if _, msg := readPayload(webhookGeneric, "X-Request-Id", nil, h, []byte("{}")); !strings.Contains(msg, "255") {
		t.Errorf("a long id: %q", msg)
	}
}

// What a webhook is made of is checked and tidied as it is saved.
func TestWebhookFields(t *testing.T) {
	ok := func(f webhookFields) webhookFields {
		t.Helper()
		if msg := f.check(true); msg != "" {
			t.Errorf("%+v is refused: %s", f, msg)
		}
		return f
	}
	f := ok(webhookFields{Name: " Issues ", Kind: webhookGitHub, Events: []string{" issues.labeled", "issues.labeled", "", "pull_request"}, Header: "X-Ignored"})
	if f.Name != "Issues" || strings.Join(f.Events, ",") != "issues.labeled,pull_request" || f.Header != "" || f.Mapping == nil {
		t.Errorf("tidied to %+v", f)
	}
	ok(webhookFields{Name: "Alerts", Kind: webhookGeneric, Verify: verifyHMAC, Header: "X-Signature", IDHeader: "X-Request-Id"})
	ok(webhookFields{Name: "Slack", Kind: webhookSlack, Secret: "0123456789abcdef0123456789abcdef"})
	for _, c := range []struct {
		f    webhookFields
		want string
	}{
		{webhookFields{Kind: webhookGitHub, Events: []string{"issues"}}, "needs a name"},
		{webhookFields{Name: "x", Kind: "jira"}, "github, slack or generic"},
		{webhookFields{Name: "x", Kind: webhookGitHub}, "name the events"},
		{webhookFields{Name: "x", Kind: webhookGitHub, Events: []string{"issues labeled"}}, "isn't an event"},
		{webhookFields{Name: "x", Kind: webhookSlack}, "signing secret"},
		{webhookFields{Name: "x", Kind: webhookGitHub, Events: []string{"issues"}, Secret: "short"}, "one line of"},
		{webhookFields{Name: "x", Kind: webhookGitHub, Events: []string{"issues"}, Secret: testHookSecret, NewSecret: true}, "not both"},
		{webhookFields{Name: "x", Kind: webhookGeneric, Verify: verifyHMAC}, "name the header"},
		{webhookFields{Name: "x", Kind: webhookGeneric, Verify: verifyHMAC, Header: "Host"}, "name the header"},
		{webhookFields{Name: "x", Kind: webhookGeneric, Verify: "basic", Header: "X-Signature"}, "hmac"},
		{webhookFields{Name: "x", Kind: webhookGeneric, Verify: verifyToken, Header: "X-Token", Events: []string{"a"}}, "names no events"},
		{webhookFields{Name: "x", Kind: webhookGitHub, Events: []string{"issues"}, Mapping: webhookMapping{"Ticket": {Path: "a..b"}}}, "isn't a path"},
		{webhookFields{Name: "x", Kind: webhookGitHub, Events: []string{"issues"}, Mapping: webhookMapping{"Ticket": {Path: "a", Attributes: map[string]string{"id": "b"}}}}, "not both"},
		{webhookFields{Name: "x", Kind: webhookGitHub, Events: []string{"issues"}, Mapping: webhookMapping{"Ticket": {Attributes: map[string]string{"id": ""}}}}, "each attribute"},
	} {
		if got := c.f.check(true); !strings.Contains(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.f, got, c.want)
		}
	}
}
