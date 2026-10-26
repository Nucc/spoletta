package tests

// Other systems' events starting flows through webhooks: deliveries signed
// with the webhook's secret, each taken once, mapped onto what the flow
// starts from, run as the API's requests are and as whoever added the
// webhook; and what each delivery came to, listed and audited, never the
// secret.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type webhook struct {
	ID        int64            `json:"id"`
	Flow      int64            `json:"flow"`
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	URL       string           `json:"url"`
	Events    []string         `json:"events"`
	Mapping   map[string]any   `json:"mapping"`
	Enabled   bool             `json:"enabled"`
	CreatedBy string           `json:"createdBy"`
	Hint      string           `json:"hint"`
	Problems  []string         `json:"problems"`
	Secret    string           `json:"secret"`
	Last      *webhookDelivery `json:"lastDelivery"`
}

type webhookDelivery struct {
	Delivery string `json:"delivery"`
	Event    string `json:"event"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason"`
	Run      *int64 `json:"run"`
}

type webhookDeliveries struct {
	Deliveries []webhookDelivery `json:"deliveries"`
	Payload    *struct {
		Event   string `json:"event"`
		Payload string `json:"payload"`
	} `json:"payload"`
}

type webhookList struct {
	Webhooks []webhook `json:"webhooks"`
	Inputs   []struct {
		Name       string   `json:"name"`
		Many       bool     `json:"many"`
		Attributes []string `json:"attributes"`
	} `json:"inputs"`
	Problem string `json:"problem"`
}

// Adds a webhook to the flow f as fields say.
func hooksAdd(a *app, f int64, fields map[string]any) webhook {
	a.t.Helper()
	body := map[string]any{"flow": f}
	for k, v := range fields {
		body[k] = v
	}
	var h webhook
	a.call("POST", "/api/webhooks", body, http.StatusCreated, &h)
	if !strings.Contains(h.URL, "/hooks/") {
		a.t.Fatalf("added %+v", h)
	}
	return h
}

// The path of a webhook's URL, on the test's webapp.
func hookPath(h webhook) string {
	return h.URL[strings.Index(h.URL, "/hooks/"):]
}

// Delivers body to the webhook with the headers, as its sender does,
// without a session, and returns the status and reply.
func hookDeliver(a *app, h webhook, headers map[string]string, body []byte) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest("POST", a.url+hookPath(h), bytes.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, reply
}

func hookMAC(secret string, parts ...string) string {
	m := hmac.New(sha256.New, []byte(secret))
	for _, p := range parts {
		m.Write([]byte(p))
	}
	return hex.EncodeToString(m.Sum(nil))
}

// Delivers a GitHub event, signed with secret.
func hookGitHub(a *app, h webhook, secret, event, delivery string, payload any) (int, []byte) {
	a.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		a.t.Fatal(err)
	}
	return hookDeliver(a, h, map[string]string{"X-GitHub-Event": event, "X-GitHub-Delivery": delivery,
		"X-Hub-Signature-256": "sha256=" + hookMAC(secret, string(body))}, body)
}

// The run a delivery's reply names; the reply must have status want.
func hookRun(a *app, status int, reply []byte, want int) int64 {
	a.t.Helper()
	if status != want {
		a.t.Fatalf("the delivery = %d %s, want %d", status, bytes.TrimSpace(reply), want)
	}
	var out struct {
		Run int64  `json:"run"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(reply, &out); err != nil || out.Run == 0 || !strings.Contains(out.URL, fmt.Sprintf("/runs/%d", out.Run)) {
		a.t.Fatalf("the delivery's reply %s: %v", reply, err)
	}
	return out.Run
}

func hookDeliveriesOf(a *app, h webhook) webhookDeliveries {
	a.t.Helper()
	var d webhookDeliveries
	a.call("GET", fmt.Sprintf("/api/webhooks/%d/deliveries", h.ID), nil, http.StatusOK, &d)
	return d
}

// The GitHub issue a test delivers, labeled.
func hookIssue(number int, title, action string) map[string]any {
	return map[string]any{"action": action, "issue": map[string]any{"number": number, "title": title}, "label": map[string]any{"name": "urgent"}}
}

// A GitHub webhook signs its deliveries with the secret it was made with,
// shown once; each event it takes starts a run given what the mapping
// takes from the payload, recorded first as a step, once however often it
// comes. What it doesn't take, a ping, and what isn't signed with the
// secret start nothing; every delivery is listed and audited, and the
// secret is neither, nor in the webapp's log.
func TestWebhooksStartRunsFromGitHub(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	db := secretsDB(t)
	mapping := map[string]any{
		"Ticket": map[string]any{"attributes": map[string]any{"id": "issue.number", "subject": "issue.title"}},
		"Note":   map[string]any{"path": "label.name"},
	}
	h := hooksAdd(a, tf.flow.ID, map[string]any{"name": "Issues", "kind": "github", "events": []string{"issues.labeled", "pull_request"}, "mapping": mapping})
	secret := h.Secret
	if !strings.HasPrefix(secret, "whsec_") || h.Hint != secret[len(secret)-4:] || !h.Enabled || len(h.Problems) != 0 {
		t.Fatalf("made %+v", h)
	}
	if v := secretsStored(t, db, "SELECT secret FROM webhooks WHERE id = ?", h.ID); !strings.HasPrefix(v, "sealed:v1:") || strings.Contains(v, secret) {
		t.Errorf("the secret was kept as %q", v)
	}
	var list webhookList
	a.call("GET", fmt.Sprintf("/api/webhooks?flow=%d", tf.flow.ID), nil, http.StatusOK, &list)
	if _, body := a.do("GET", fmt.Sprintf("/api/webhooks?flow=%d", tf.flow.ID), nil); strings.Contains(string(body), secret) || strings.Contains(string(body), "sealed:") {
		t.Errorf("the list holds the secret: %s", body)
	}
	if len(list.Webhooks) != 1 || list.Webhooks[0].Secret != "" || len(list.Inputs) != 2 || list.Inputs[0].Name != "Ticket" ||
		strings.Join(list.Inputs[0].Attributes, ",") != "id,subject" || list.Problem != "" {
		t.Errorf("the flow's webhooks are %+v", list)
	}

	delivery := fmt.Sprint("delivery-", h.ID)
	status, reply := hookGitHub(a, h, secret, "issues", delivery, hookIssue(7, "Paper jam", "labeled"))
	rn := hookRun(a, status, reply, http.StatusAccepted)
	d := a.finish(rn)
	if d.Status != runDone || d.Webhook == nil || *d.Webhook != "Issues" {
		t.Fatalf("the run ended %s, by webhook %v: %s", d.Status, d.Webhook, describeSteps(d))
	}
	if given := onlyStep(t, d, "Given by the webhook Issues"); given.Kind != "input" {
		t.Errorf("what it was given is a step of kind %s", given.Kind)
	}
	if ticket := contentsOf(d, "Ticket"); len(ticket) != 1 || !strings.Contains(ticket[0], `"id": 7`) || !strings.Contains(ticket[0], `"subject": "Paper jam"`) {
		t.Errorf("the ticket held %q", ticket)
	}
	if note := contentsOf(d, "Note"); len(note) != 1 || note[0] != "urgent" {
		t.Errorf("the note held %q", note)
	}

	// Sent again, it gets the run it started.
	status, reply = hookGitHub(a, h, secret, "issues", delivery, hookIssue(7, "Paper jam", "labeled"))
	if again := hookRun(a, status, reply, http.StatusOK); again != rn {
		t.Errorf("sending it again started run %d after %d", again, rn)
	}
	// Unsigned, or signed with another secret, it starts nothing.
	body, _ := json.Marshal(hookIssue(8, "Toner", "labeled"))
	status, reply = hookDeliver(a, h, map[string]string{"X-GitHub-Event": "issues", "X-GitHub-Delivery": "unsigned"}, body)
	if status != http.StatusUnauthorized || !strings.Contains(string(reply), "no X-Hub-Signature-256") {
		t.Errorf("unsigned = %d %s", status, reply)
	}
	status, reply = hookGitHub(a, h, "whsec_another-secret-entirely", "issues", "forged", hookIssue(8, "Toner", "labeled"))
	if status != http.StatusUnauthorized || !strings.Contains(string(reply), "doesn't match") {
		t.Errorf("signed with another secret = %d %s", status, reply)
	}
	// What it doesn't take, and a ping, are answered and start nothing.
	status, reply = hookGitHub(a, h, secret, "issues", "opened-1", hookIssue(9, "Fuser", "opened"))
	if status != http.StatusOK || !strings.Contains(string(reply), "not issues.opened") {
		t.Errorf("an event it doesn't take = %d %s", status, reply)
	}
	status, reply = hookGitHub(a, h, secret, "ping", "ping-1", map[string]any{"zen": "Keep it logically awesome."})
	if status != http.StatusOK || !strings.Contains(string(reply), "pong") {
		t.Errorf("a ping = %d %s", status, reply)
	}
	big := append([]byte(`{"action": "labeled", "pad": "`), bytes.Repeat([]byte("x"), 1<<20)...)
	big = append(big, []byte(`"}`)...)
	status, _ = hookDeliver(a, h, map[string]string{"X-GitHub-Event": "issues", "X-Hub-Signature-256": "sha256=" + hookMAC(secret, string(big))}, big)
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized delivery = %d", status)
	}

	ds := hookDeliveriesOf(a, h)
	var outcomes []string
	for _, d := range ds.Deliveries {
		outcomes = append(outcomes, d.Outcome)
	}
	if strings.Join(outcomes, ",") != "refused,ignored,ignored,refused,refused,repeat,started" {
		t.Errorf("the deliveries came to %v: %+v", outcomes, ds.Deliveries)
	}
	if first := ds.Deliveries[len(ds.Deliveries)-1]; first.Run == nil || *first.Run != rn || first.Event != "issues.labeled" || first.Delivery != delivery {
		t.Errorf("the first delivery is listed as %+v", first)
	}
	// The payload shown is the latest signed delivery's, which wasn't a ping.
	if ds.Payload == nil || ds.Payload.Event != "issues.opened" || !strings.Contains(ds.Payload.Payload, "Fuser") {
		t.Errorf("the payload shown is %+v", ds.Payload)
	}
	if raw, _ := json.Marshal(ds); strings.Contains(string(raw), secret) {
		t.Errorf("the deliveries hold the secret")
	}
	a.auditEvent("kind:webhook.delivery run:"+strconv.FormatInt(rn, 10), func(_, detail string) bool {
		return strings.Contains(detail, `"Issues"`) && strings.Contains(detail, "started")
	})
	a.auditEvent("kind:webhook.delivery", func(_, detail string) bool {
		return strings.Contains(detail, "forged") && strings.Contains(detail, "doesn't match")
	})
	a.auditEvent("kind:webhook.add", func(_, detail string) bool { return strings.Contains(detail, `"Issues"`) })
	for _, e := range a.audit("webhook", 1).Events {
		if strings.Contains(e.Detail, secret) || strings.Contains(e.Detail, "Paper jam") {
			t.Errorf("an event holds the secret or the payload: %s", e.Detail)
		}
	}

	// The run list tells the webhook's runs apart.
	var runs []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d&started=webhook", tf.flow.ID), nil, http.StatusOK, &runs)
	if len(runs) != 1 || runs[0].ID != rn {
		t.Errorf("the webhook's runs are %+v", runs)
	}
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d&started=api,person", tf.flow.ID), nil, http.StatusOK, &runs)
	if len(runs) != 0 {
		t.Errorf("the webhook's run counts as another's: %+v", runs)
	}
	var logs []struct {
		Message string `json:"message"`
	}
	a.call("GET", fmt.Sprintf("/api/runs/%d/logs", rn), nil, http.StatusOK, &logs)
	if !slices.ContainsFunc(logs, func(l struct {
		Message string `json:"message"`
	}) bool {
		return strings.Contains(l.Message, `for a delivery to the webhook "Issues"`)
	}) {
		t.Errorf("the run's log says %+v", logs)
	}

	// Removed, it takes nothing; its run stays, as a webhook's.
	a.call("DELETE", fmt.Sprintf("/api/webhooks/%d", h.ID), nil, http.StatusNoContent, nil)
	status, _ = hookGitHub(a, h, secret, "issues", "after", hookIssue(10, "Drum", "labeled"))
	if status != http.StatusNotFound {
		t.Errorf("a delivery to a removed webhook = %d", status)
	}
	if d := a.getRun(rn); d.Webhook == nil || *d.Webhook != "" {
		t.Errorf("the run of a removed webhook says %v", d.Webhook)
	}
	if strings.Contains(a.proc.log.String(), secret) {
		t.Errorf("the webapp logged the secret")
	}
}

// A delivery refused takes nothing: once what refused it is put right, the
// same delivery sent again starts a run. What the flow starts from is
// checked as the API checks a request.
func TestWebhooksRefusedDeliveriesTakeNothing(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	fields := map[string]any{"name": "Issues", "kind": "github", "events": []string{"issues"},
		"mapping": map[string]any{"Ticket": map[string]any{"attributes": map[string]any{"id": "issue.number", "subject": "issue.title"}}}}
	h := hooksAdd(a, tf.flow.ID, fields)
	if len(h.Problems) != 1 || !strings.Contains(h.Problems[0], `nothing for "Note"`) {
		t.Errorf("the webhook's problems are %q", h.Problems)
	}
	delivery := fmt.Sprint("refused-", h.ID)
	status, reply := hookGitHub(a, h, h.Secret, "issues", delivery, hookIssue(3, "Jam", "opened"))
	if status != http.StatusBadRequest || !strings.Contains(string(reply), `gives nothing for "Note"`) {
		t.Errorf("without the note = %d %s", status, reply)
	}
	// A path the payload doesn't have refuses it too.
	fields["mapping"] = map[string]any{"Ticket": map[string]any{"attributes": map[string]any{"id": "issue.number", "subject": "issue.title"}},
		"Note": map[string]any{"path": "comment.body"}}
	a.call("PUT", fmt.Sprintf("/api/webhooks/%d", h.ID), fields, http.StatusOK, nil)
	status, reply = hookGitHub(a, h, h.Secret, "issues", delivery, hookIssue(3, "Jam", "opened"))
	if status != http.StatusBadRequest || !strings.Contains(string(reply), "no comment.body") {
		t.Errorf("a path the payload lacks = %d %s", status, reply)
	}
	// What the payload holds must fit the object, as a request's must.
	fields["mapping"] = map[string]any{"Ticket": map[string]any{"path": "issue"}, "Note": map[string]any{"path": "label.name"}}
	a.call("PUT", fmt.Sprintf("/api/webhooks/%d", h.ID), fields, http.StatusOK, nil)
	status, reply = hookGitHub(a, h, h.Secret, "issues", delivery, hookIssue(3, "Jam", "opened"))
	if status != http.StatusBadRequest || !strings.Contains(string(reply), `"Ticket"`) || !strings.Contains(string(reply), `"number"`) {
		t.Errorf("a ticket of other attributes = %d %s", status, reply)
	}
	// Put right, the same delivery starts its run, keeping the secret.
	fields["mapping"] = map[string]any{"Ticket": map[string]any{"attributes": map[string]any{"id": "issue.number", "subject": "issue.title"}},
		"Note": map[string]any{"path": "label.name"}}
	var changed webhook
	a.call("PUT", fmt.Sprintf("/api/webhooks/%d", h.ID), fields, http.StatusOK, &changed)
	if changed.Secret != "" || len(changed.Problems) != 0 {
		t.Errorf("changed to %+v", changed)
	}
	status, reply = hookGitHub(a, h, h.Secret, "issues", delivery, hookIssue(3, "Jam", "opened"))
	rn := hookRun(a, status, reply, http.StatusAccepted)
	if d := a.finish(rn); d.Status != runDone {
		t.Errorf("the run ended %s: %s", d.Status, describeSteps(d))
	}
	// Turned off, it takes nothing, but still knows what it took.
	off := false
	fields["enabled"] = off
	a.call("PUT", fmt.Sprintf("/api/webhooks/%d", h.ID), fields, http.StatusOK, nil)
	status, reply = hookGitHub(a, h, h.Secret, "issues", "while-off", hookIssue(4, "Toner", "opened"))
	if status != http.StatusOK || !strings.Contains(string(reply), "turned off") {
		t.Errorf("while turned off = %d %s", status, reply)
	}
	status, reply = hookGitHub(a, h, h.Secret, "issues", delivery, hookIssue(3, "Jam", "opened"))
	if again := hookRun(a, status, reply, http.StatusOK); again != rn {
		t.Errorf("sent again while turned off, it got run %d, not %d", again, rn)
	}
	// A new secret is made, shown once, and the old one no longer signs.
	var renewed webhook
	fields["newSecret"] = true
	a.call("PUT", fmt.Sprintf("/api/webhooks/%d", h.ID), fields, http.StatusOK, &renewed)
	if !strings.HasPrefix(renewed.Secret, "whsec_") || renewed.Secret == h.Secret {
		t.Errorf("the new secret is %q", renewed.Secret)
	}
	if status, _ := hookGitHub(a, h, h.Secret, "issues", "old-secret", hookIssue(5, "Fax", "opened")); status != http.StatusUnauthorized {
		t.Errorf("the old secret still signs: %d", status)
	}
	a.refused("POST", "/api/webhooks", map[string]any{"flow": tf.flow.ID, "name": "No events", "kind": "github"}, http.StatusBadRequest, "name the events")
	a.refused("POST", "/api/webhooks", map[string]any{"flow": 999999999, "name": "Nowhere", "kind": "generic", "verify": "token", "header": "X-Token"}, http.StatusNotFound, "flow not found")
	a.refused("PUT", "/api/webhooks/999999999", fields, http.StatusNotFound, "webhook not found")
}

// Slack's URL verification is answered once it is signed with the app's
// signing secret, and each event, signed with its timestamp, starts a run
// once, however often Slack retries it; one signed long ago is refused.
func TestWebhooksTakeSlackEvents(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Answer mentions")
	answer := a.agent(f.ID, "Answer", "Answer the mention.")
	mention := a.newObject(f.ID, "Mention")
	a.input(answer.ID, mention.ID)
	a.startAt(f.ID, answer.ID)
	a.worker.onAgent("Answer", handsIn(data("done")))
	signing := "8f742231b10e8888abcd99yyyzzz85a5"
	h := hooksAdd(a, f.ID, map[string]any{"name": "Mentions", "kind": "slack", "secret": signing, "events": []string{"app_mention"},
		"mapping": map[string]any{"Mention": map[string]any{"path": "event.text"}}})
	if h.Secret != "" {
		t.Errorf("a pasted secret came back")
	}
	send := func(at time.Time, retry bool, payload any) (int, []byte) {
		body, _ := json.Marshal(payload)
		ts := strconv.FormatInt(at.Unix(), 10)
		headers := map[string]string{"X-Slack-Request-Timestamp": ts, "X-Slack-Signature": "v0=" + hookMAC(signing, "v0:"+ts+":", string(body))}
		if retry {
			headers["X-Slack-Retry-Num"] = "1"
		}
		return hookDeliver(a, h, headers, body)
	}
	challenge := map[string]any{"type": "url_verification", "challenge": "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"}
	status, reply := send(time.Now(), false, challenge)
	if status != http.StatusOK || !strings.Contains(string(reply), `"challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"`) {
		t.Errorf("the URL verification = %d %s", status, reply)
	}
	body, _ := json.Marshal(challenge)
	if status, reply := hookDeliver(a, h, nil, body); status != http.StatusUnauthorized || strings.Contains(string(reply), "3eZbrw") {
		t.Errorf("an unsigned URL verification = %d %s", status, reply)
	}
	event := map[string]any{"type": "event_callback", "event_id": fmt.Sprint("Ev", h.ID),
		"event": map[string]any{"type": "app_mention", "text": "<@U1> what's up?"}}
	status, reply = send(time.Now(), false, event)
	rn := hookRun(a, status, reply, http.StatusAccepted)
	if d := a.finish(rn); d.Status != runDone || !slices.Equal(contentsOf(d, "Mention"), []string{"<@U1> what's up?"}) {
		t.Errorf("the run ended %s holding %q", d.Status, contentsOf(d, "Mention"))
	}
	status, reply = send(time.Now(), true, event)
	if again := hookRun(a, status, reply, http.StatusOK); again != rn {
		t.Errorf("Slack's retry started run %d after %d", again, rn)
	}
	event["event_id"] = fmt.Sprint("Ev-old-", h.ID)
	if status, reply := send(time.Now().Add(-10*time.Minute), false, event); status != http.StatusUnauthorized || !strings.Contains(string(reply), "5 minutes off") {
		t.Errorf("one signed long ago = %d %s", status, reply)
	}
	message := map[string]any{"type": "event_callback", "event_id": fmt.Sprint("Ev-msg-", h.ID), "event": map[string]any{"type": "message", "text": "hi"}}
	if status, reply := send(time.Now(), false, message); status != http.StatusOK || !strings.Contains(string(reply), "ignored") {
		t.Errorf("an event it doesn't take = %d %s", status, reply)
	}
}

// A generic webhook checks an HMAC of the body, or the secret itself, in
// the header it names, takes a delivery once by the id another header
// holds, and gives the whole payload, JSON or text.
func TestWebhooksGeneric(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Handle alerts")
	handle := a.agent(f.ID, "Handle", "Handle the alert.")
	alert := a.newObject(f.ID, "Alert")
	a.input(handle.ID, alert.ID)
	a.startAt(f.ID, handle.ID)
	a.worker.onAgent("Handle", handsIn(data("handled")))
	mapping := map[string]any{"Alert": map[string]any{"path": ""}}
	signed := hooksAdd(a, f.ID, map[string]any{"name": "Monitor", "kind": "generic", "verify": "hmac", "header": "X-Signature", "idHeader": "X-Alert-Id", "mapping": mapping})
	text := []byte("disk full on db-1")
	headers := map[string]string{"X-Signature": "sha256=" + hookMAC(signed.Secret, string(text)), "X-Alert-Id": fmt.Sprint("alert-", signed.ID), "Content-Type": "text/plain"}
	status, reply := hookDeliver(a, signed, headers, text)
	rn := hookRun(a, status, reply, http.StatusAccepted)
	if d := a.finish(rn); !slices.Equal(contentsOf(d, "Alert"), []string{"disk full on db-1"}) {
		t.Errorf("the alert held %q", contentsOf(d, "Alert"))
	}
	status, reply = hookDeliver(a, signed, headers, text)
	if again := hookRun(a, status, reply, http.StatusOK); again != rn {
		t.Errorf("sending it again started run %d after %d", again, rn)
	}
	headers["X-Signature"] = hookMAC(signed.Secret, "something else")
	if status, _ := hookDeliver(a, signed, headers, text); status != http.StatusUnauthorized {
		t.Errorf("a wrong HMAC = %d", status)
	}

	token := hooksAdd(a, f.ID, map[string]any{"name": "Pager", "kind": "generic", "verify": "token", "header": "Authorization", "mapping": mapping})
	payload := []byte(`{"severity": "high", "count": 3}`)
	status, reply = hookDeliver(a, token, map[string]string{"Authorization": "Bearer " + token.Secret}, payload)
	rn = hookRun(a, status, reply, http.StatusAccepted)
	if d := a.finish(rn); len(contentsOf(d, "Alert")) != 1 || !strings.Contains(contentsOf(d, "Alert")[0], `"severity":"high"`) {
		t.Errorf("the alert held %q", contentsOf(d, "Alert"))
	}
	if status, reply := hookDeliver(a, token, map[string]string{"Authorization": "Bearer " + signed.Secret}, payload); status != http.StatusUnauthorized || strings.Contains(string(reply), token.Secret) {
		t.Errorf("another webhook's secret = %d %s", status, reply)
	}
	if status, _ := hookDeliver(a, token, nil, payload); status != http.StatusUnauthorized {
		t.Errorf("no secret = %d", status)
	}
	// Without the id header, each delivery is taken anew.
	status, reply = hookDeliver(a, token, map[string]string{"Authorization": token.Secret}, payload)
	again := hookRun(a, status, reply, http.StatusAccepted)
	if again == rn {
		t.Errorf("a delivery without an id got the run of another")
	}
	a.finish(again)
	var nowhere webhook
	nowhere.URL = "/hooks/not-a-webhook"
	if status, _ := hookDeliver(a, nowhere, nil, payload); status != http.StatusNotFound {
		t.Errorf("a delivery to no webhook = %d", status)
	}
}

// A webhook's run takes the version the flow published, as the API's
// does, and what it starts from is that version's.
func TestWebhooksRunThePublishedVersion(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	published := pubPublish(a, tf.flow.ID, "the ticket and the note")
	h := hooksAdd(a, tf.flow.ID, map[string]any{"name": "Issues", "kind": "github", "events": []string{"issues"},
		"mapping": map[string]any{"Ticket": map[string]any{"attributes": map[string]any{"id": "issue.number", "subject": "issue.title"}},
			"Note": map[string]any{"path": "label.name"}}})
	// The draft takes another object the mapping doesn't give, which the
	// version published doesn't.
	extra := a.newObject(tf.flow.ID, "Customer")
	a.input(a.agent(tf.flow.ID, "Look up", "Look the customer up.").ID, extra.ID)
	var list webhookList
	a.call("GET", fmt.Sprintf("/api/webhooks?flow=%d", tf.flow.ID), nil, http.StatusOK, &list)
	if len(list.Inputs) != 2 || len(list.Webhooks[0].Problems) != 0 {
		t.Errorf("the published version takes %+v, with problems %q", list.Inputs, list.Webhooks[0].Problems)
	}
	status, reply := hookGitHub(a, h, h.Secret, "issues", fmt.Sprint("published-", h.ID), hookIssue(1, "Jam", "opened"))
	d := a.finish(hookRun(a, status, reply, http.StatusAccepted))
	if d.Status != runDone || d.Version == nil || *d.Version != published.ID {
		t.Errorf("the run ended %s on version %v, not %d", d.Status, d.Version, published.ID)
	}
}

// A webhook's runs are for whoever added it: they start only while that
// person may run the flow, and only those who may change the flow see and
// change its webhooks.
func TestWebhooksArePeoples(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	bobRow := usersAdd(a, bob.email)
	tf := newAPITicketFlow(a)
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", tf.flow.ID), map[string]any{"kind": "user", "id": usersMeID(a)}, http.StatusOK, nil)
	fields := map[string]any{"name": "Issues", "kind": "github", "events": []string{"issues"},
		"mapping": map[string]any{"Ticket": map[string]any{"attributes": map[string]any{"id": "issue.number", "subject": "issue.title"}},
			"Note": map[string]any{"path": "label.name"}}}

	usersSignIn(a, p, bob)
	a.refused("POST", "/api/webhooks", map[string]any{"flow": tf.flow.ID, "name": "Sneak", "kind": "generic", "verify": "token", "header": "X-Token"}, http.StatusNotFound, "flow not found")
	a.refused("GET", fmt.Sprintf("/api/webhooks?flow=%d", tf.flow.ID), nil, http.StatusNotFound, "flow not found")

	usersSignIn(a, p, usersAda)
	usersShare(a, tf.flow.ID)
	usersSignIn(a, p, bob)
	h := hooksAdd(a, tf.flow.ID, fields)
	if h.CreatedBy != "Bob" {
		t.Errorf("the webhook is %q's", h.CreatedBy)
	}
	status, reply := hookGitHub(a, h, h.Secret, "issues", fmt.Sprint("bob-1-", h.ID), hookIssue(1, "Jam", "opened"))
	if d := a.finish(hookRun(a, status, reply, http.StatusAccepted)); d.StartedBy != "Bob" || d.Webhook == nil || *d.Webhook != "Issues" {
		t.Errorf("the run was started by %q through %v", d.StartedBy, d.Webhook)
	}

	// Once Bob may no longer run the flow, its deliveries are refused.
	usersSignIn(a, p, usersAda)
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", tf.flow.ID), map[string]any{"kind": "user", "id": usersMeID(a)}, http.StatusOK, nil)
	status, reply = hookGitHub(a, h, h.Secret, "issues", fmt.Sprint("bob-2-", h.ID), hookIssue(2, "Toner", "opened"))
	if status != http.StatusForbidden || !strings.Contains(string(reply), "may no longer run the flow") {
		t.Errorf("after Bob lost the flow = %d %s", status, reply)
	}
	// Ada may change the flow, so she sees its webhooks.
	var list webhookList
	a.call("GET", fmt.Sprintf("/api/webhooks?flow=%d", tf.flow.ID), nil, http.StatusOK, &list)
	if len(list.Webhooks) != 1 || list.Webhooks[0].ID != h.ID {
		t.Errorf("ada sees %+v", list.Webhooks)
	}
	// Carl only decides its reviews, so he sees none of its webhooks.
	carl := usersNewPerson("Carl")
	usersAdd(a, carl.email, "Editor")
	humanPerson(a, tf.flow.ID, humanHuman(a, tf.flow.ID, "Check").ID, "Editor")
	usersSignIn(a, p, carl)
	a.refused("GET", fmt.Sprintf("/api/webhooks?flow=%d", tf.flow.ID), nil, http.StatusForbidden, "only those who may change the flow")
	a.refused("GET", fmt.Sprintf("/api/webhooks/%d/deliveries", h.ID), nil, http.StatusForbidden, "only those who may change the flow")

	usersSignIn(a, p, usersAda)
	usersShare(a, tf.flow.ID)
	a.call("DELETE", fmt.Sprintf("/api/users/%d", bobRow.ID), nil, http.StatusNoContent, nil)
	status, reply = hookGitHub(a, h, h.Secret, "issues", fmt.Sprint("bob-3-", h.ID), hookIssue(3, "Fax", "opened"))
	if status != http.StatusForbidden || !strings.Contains(string(reply), "was removed") {
		t.Errorf("after Bob was removed = %d %s", status, reply)
	}
}
