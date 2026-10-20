package tests

// Other systems starting flows over the API with a key, given what the flows
// start from, and reading back what the runs came to.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Makes an API key for whoever the app's requests are from.
func apiNewKey(a *app, name string) apiKey {
	a.t.Helper()
	var k apiKey
	a.call("POST", "/api/api-keys", map[string]any{"name": name}, http.StatusCreated, &k)
	if !strings.HasPrefix(k.Key, "spk_") || !strings.HasPrefix(k.Key, k.Prefix) {
		a.t.Fatalf("made key %+v", k)
	}
	return k
}

// Sends a request with the key, and no session, as another system does, and
// returns the status and reply. An idempotency key goes with it, if given.
func apiCall(a *app, key, method, path string, body any, idempotency ...string) (int, []byte) {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.url+path, r)
	if err != nil {
		a.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for _, k := range idempotency {
		req.Header.Set("Idempotency-Key", k)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, reply
}

// Starts a run of the flow with the key, which must get status want.
func apiStart(a *app, key string, f int64, body any, want int, idempotency ...string) apiRun {
	a.t.Helper()
	status, reply := apiCall(a, key, "POST", fmt.Sprintf("/v1/flows/%d/runs", f), body, idempotency...)
	if status != want {
		a.t.Fatalf("starting flow %d = %d %s, want %d", f, status, bytes.TrimSpace(reply), want)
	}
	var rn apiRun
	if err := json.Unmarshal(reply, &rn); err != nil {
		a.t.Fatalf("decode %s: %v", reply, err)
	}
	return rn
}

// Sends a request with the key that must be refused with status want, with
// a message holding msg.
func apiRefused(a *app, key, method, path string, body any, want int, msg string) {
	a.t.Helper()
	status, reply := apiCall(a, key, method, path, body)
	if status != want || !strings.Contains(string(reply), msg) {
		a.t.Errorf("%s %s = %d %q, want %d with %q", method, path, status, bytes.TrimSpace(reply), want, msg)
	}
}

// A flow summarizing a ticket and a note it is given, leaving the summary.
type apiTicketFlow struct {
	flow                         flow
	ticket, note, summary, other object
	id                           attribute
	urgent                       objectState
}

func newAPITicketFlow(a *app) apiTicketFlow {
	a.t.Helper()
	var t apiTicketFlow
	t.flow = a.newFlow("Summarize tickets")
	summarize := a.agent(t.flow.ID, "Summarize", "Summarize the ticket.")
	t.ticket = a.newObject(t.flow.ID, "Ticket")
	t.id = nestedAttribute(a, t.ticket.ID, "id", false)
	nestedAttribute(a, t.ticket.ID, "subject", false)
	t.urgent = a.newState(t.ticket.ID, "urgent")
	t.note = a.newObject(t.flow.ID, "Note")
	t.summary = a.newObject(t.flow.ID, "Summary")
	a.input(summarize.ID, t.ticket.ID)
	a.input(summarize.ID, t.note.ID)
	a.output(summarize.ID, t.summary.ID)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", t.summary.ID), map[string]any{"end": true}, http.StatusOK, nil)
	// Nothing takes it, so it isn't asked for.
	t.other = a.newObject(t.flow.ID, "Unused")
	a.startAt(t.flow.ID, summarize.ID)
	a.worker.onAgent("Summarize", handsIn(data("short")))
	return t
}

// A key starts a flow given the objects its first process takes, which
// reach it, and reads back what the run left at the end.
func TestAPIStartsFlowWithInputs(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	k := apiNewKey(a, "Helpdesk")

	status, reply := apiCall(a, k.Key, "GET", fmt.Sprintf("/v1/flows/%d", tf.flow.ID), nil)
	var d flowAPI
	if status != http.StatusOK || json.Unmarshal(reply, &d) != nil {
		t.Fatalf("describing the flow = %d %s", status, reply)
	}
	if !strings.HasSuffix(d.URL, fmt.Sprintf("/v1/flows/%d/runs", tf.flow.ID)) || d.Problem != "" || len(d.Inputs) != 2 ||
		d.Inputs[0].Name != "Ticket" || d.Inputs[1].Name != "Note" || fmt.Sprint(d.Inputs[0].States) != "[urgent]" {
		t.Fatalf("the flow is described as %s", reply)
	}
	if req := d.Inputs[0].Schema["required"]; fmt.Sprint(req) != "[id subject]" {
		t.Errorf("the ticket's schema requires %v", req)
	}
	// The flow's settings show the same, behind the session.
	var shown flowAPI
	a.call("GET", fmt.Sprintf("/api/flows/%d/api", tf.flow.ID), nil, http.StatusOK, &shown)
	if shown.URL != d.URL || len(shown.Inputs) != 2 {
		t.Errorf("the settings show %+v", shown)
	}

	ticket := map[string]any{"id": "T-1", "subject": "Printer on fire"}
	path := fmt.Sprintf("/v1/flows/%d/runs", tf.flow.ID)
	good := map[string]any{"inputs": map[string]any{"Ticket": ticket, "Note": "call back"}}
	apiRefused(a, "", "POST", path, good, http.StatusUnauthorized, "API key is needed")
	apiRefused(a, "spk_made-up", "POST", path, good, http.StatusUnauthorized, "isn't one")
	for _, c := range []struct {
		name string
		body any
		msg  string
	}{
		{"nothing", nil, `lacks "Ticket", "Note"`},
		{"a missing input", map[string]any{"inputs": map[string]any{"Ticket": ticket}}, `lacks "Note"`},
		{"an input not taken", map[string]any{"inputs": map[string]any{"Ticket": ticket, "Note": "x", "Unused": "x"}}, `not asked for: "Unused"`},
		{"a missing attribute", map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"id": "T-1"}, "Note": "x"}}, `"Ticket": it lacks "subject"`},
		{"an object for a list", map[string]any{"inputs": map[string]any{"Ticket": "T-1", "Note": "x"}}, `"Ticket": it is not a JSON object`},
		{"an unknown state", map[string]any{"inputs": good["inputs"], "states": map[string]any{"Ticket": "cold"}}, `has no state "cold"`},
		{"an unknown field", map[string]any{"input": good["inputs"]}, "unknown field"},
	} {
		t.Run(c.name, func(t *testing.T) { apiRefused(a, k.Key, "POST", path, c.body, http.StatusBadRequest, c.msg) })
	}

	rn := apiStart(a, k.Key, tf.flow.ID, map[string]any{"inputs": good["inputs"], "states": map[string]any{"Ticket": "urgent"}}, http.StatusCreated)
	if rn.Status != runRunning || !strings.HasSuffix(rn.URL, fmt.Sprintf("#/flows/%d/runs/%d", tf.flow.ID, rn.ID)) {
		t.Errorf("started %+v", rn)
	}
	det := a.finish(rn.ID)
	if det.Status != runDone || det.APIKey != "Helpdesk" {
		t.Fatalf("the run ended %s, started with key %q: %s", det.Status, det.APIKey, describeSteps(det))
	}
	if given := onlyStep(t, det, "Given through the API"); given.Kind != kindInput {
		t.Errorf("what it was given is a step of kind %q", given.Kind)
	}
	ins := map[string]runInput{}
	for _, in := range onlyStep(t, det, "Summarize").Inputs {
		ins[in.Name] = in
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(ins["Ticket"].Content), &got); err != nil || got["subject"] != "Printer on fire" || ins["Ticket"].State != "urgent" {
		t.Errorf("Summarize took the ticket as %+v", ins["Ticket"])
	}
	if ins["Note"].Content != "call back" {
		t.Errorf("Summarize took the note as %+v", ins["Note"])
	}
	if _, ok := ins["Unused"]; ok {
		t.Errorf("Summarize took an object nothing takes")
	}

	status, reply = apiCall(a, k.Key, "GET", fmt.Sprintf("/v1/runs/%d", rn.ID), nil)
	var res apiRun
	if status != http.StatusOK || json.Unmarshal(reply, &res) != nil {
		t.Fatalf("reading the run = %d %s", status, reply)
	}
	if res.Status != runDone || string(res.Outcomes["Summary"].Value) != `"short"` || len(res.Outcomes) != 1 || len(res.Errors) != 0 {
		t.Errorf("the run came to %s", reply)
	}

	var keys []apiKey
	a.call("GET", "/api/api-keys", nil, http.StatusOK, &keys)
	if len(keys) == 0 || keys[len(keys)-1].ID != k.ID || keys[len(keys)-1].Key != "" || keys[len(keys)-1].LastUsedAt == nil {
		t.Errorf("the keys are listed as %+v", keys)
	}
	a.call("DELETE", fmt.Sprintf("/api/api-keys/%d", k.ID), nil, http.StatusNoContent, nil)
	apiRefused(a, k.Key, "POST", path, good, http.StatusUnauthorized, "isn't one")
}

// A request sent again with the same idempotency key gets the run the first
// started, rather than another.
func TestAPIIdempotencyKey(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	other := newAPITicketFlow(a)
	k := apiNewKey(a, "Retrying")
	body := map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"id": "T-2", "subject": "Paper jam"}, "Note": ""}}
	idem := fmt.Sprint("event-", tf.flow.ID)
	first := apiStart(a, k.Key, tf.flow.ID, body, http.StatusCreated, idem)
	again := apiStart(a, k.Key, tf.flow.ID, body, http.StatusOK, idem)
	if again.ID != first.ID {
		t.Errorf("sending it again started run %d after %d", again.ID, first.ID)
	}
	status, reply := apiCall(a, k.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", other.flow.ID), body, idem)
	if status != http.StatusConflict || !strings.Contains(string(reply), "of another flow") {
		t.Errorf("reusing it for another flow = %d %s", status, reply)
	}
	if next := apiStart(a, k.Key, tf.flow.ID, body, http.StatusCreated); next.ID == first.ID {
		t.Errorf("without one, the run started again")
	}
	a.finish(first.ID)
}

// A flow whose processes start from nothing takes nothing, and refuses inputs.
func TestAPIFlowWithoutInputs(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Fetch on its own")
	fetch := a.agent(f.ID, "Fetch", "Fetch the news.")
	news := a.newObject(f.ID, "News")
	a.output(fetch.ID, news.ID)
	a.startAt(f.ID, fetch.ID)
	a.worker.onAgent("Fetch", handsIn(data("nothing new")))
	k := apiNewKey(a, "Cron elsewhere")

	var d flowAPI
	a.call("GET", fmt.Sprintf("/api/flows/%d/api", f.ID), nil, http.StatusOK, &d)
	if len(d.Inputs) != 0 {
		t.Errorf("it takes %+v", d.Inputs)
	}
	apiRefused(a, k.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", f.ID), map[string]any{"inputs": map[string]any{"News": "x"}}, http.StatusBadRequest, "takes no inputs")
	rn := apiStart(a, k.Key, f.ID, nil, http.StatusCreated)
	det := a.finish(rn.ID)
	if det.Status != runDone || len(stepsOf(det, "Given through the API")) != 0 {
		t.Errorf("the run ended %s with steps %s", det.Status, describeSteps(det))
	}
	apiRefused(a, k.Key, "POST", "/v1/flows/999999999/runs", nil, http.StatusNotFound, "flow not found")
}

// A persistent object given holding a unique value another run stored ends
// the run before it starts, so an event sent twice is handled once.
func TestAPIGivenTwice(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	nestedPatchObject(a, tf.ticket.ID, map[string]any{"persistent": true})
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", tf.id.ID), map[string]any{"unique": true}, http.StatusOK, nil)
	k := apiNewKey(a, "Webhook")
	id := fmt.Sprint("T-", tf.flow.ID)
	body := map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"id": id, "subject": "Toner"}, "Note": ""}}

	first := apiStart(a, k.Key, tf.flow.ID, body, http.StatusCreated)
	if d := a.finish(first.ID); d.Status != runDone {
		t.Fatalf("the first run ended %s: %s", d.Status, describeSteps(d))
	}
	second := apiStart(a, k.Key, tf.flow.ID, body, http.StatusConflict)
	if second.Status != runTerminated || len(second.Errors) != 1 || !strings.Contains(second.Errors[0].Error, fmt.Sprintf("stored by run #%d", first.ID)) {
		t.Errorf("the second run came to %+v", second)
	}
	if d := a.getRun(second.ID); len(stepsOf(d, "Summarize")) != 0 {
		t.Errorf("the second run went on: %s", describeSteps(d))
	}
}

// A key is its maker's: it starts only the flows they may run, as them, and
// stops working once they are removed.
func TestAPIKeysArePeoples(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	bobRow := usersAdd(a, bob.email)
	tf := newAPITicketFlow(a)
	var mine flow
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", tf.flow.ID), map[string]any{"kind": "user", "id": usersMeID(a)}, http.StatusOK, &mine)
	adaKey := apiNewKey(a, "Ada's")

	usersSignIn(a, p, bob)
	k := apiNewKey(a, "Bob's")
	var keys []apiKey
	a.call("GET", "/api/api-keys", nil, http.StatusOK, &keys)
	if len(keys) != 1 || keys[0].ID != k.ID {
		t.Errorf("bob sees the keys %+v", keys)
	}
	a.refused("DELETE", fmt.Sprintf("/api/api-keys/%d", adaKey.ID), nil, http.StatusNotFound, "key not found")
	body := map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"id": "T-9", "subject": "Fax"}, "Note": ""}}
	apiRefused(a, k.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", tf.flow.ID), body, http.StatusNotFound, "flow not found")
	apiRefused(a, k.Key, "GET", fmt.Sprintf("/v1/flows/%d", tf.flow.ID), nil, http.StatusNotFound, "flow not found")
	// A key reaches nothing behind the session.
	apiRefused(a, k.Key, "GET", "/api/flows", nil, http.StatusUnauthorized, "sign in first")

	usersSignIn(a, p, usersAda)
	usersShare(a, tf.flow.ID)
	rn := apiStart(a, k.Key, tf.flow.ID, body, http.StatusCreated)
	if d := a.finish(rn.ID); d.StartedBy != "Bob" || d.APIKey != "Bob's" {
		t.Errorf("the run was started by %q with key %q", d.StartedBy, d.APIKey)
	}
	a.call("DELETE", fmt.Sprintf("/api/users/%d", bobRow.ID), nil, http.StatusNoContent, nil)
	apiRefused(a, k.Key, "GET", fmt.Sprintf("/v1/runs/%d", rn.ID), nil, http.StatusUnauthorized, "owner was removed")
}

// The id of whoever the app's requests are from.
func usersMeID(a *app) int64 {
	a.t.Helper()
	var me usersMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	return me.User.ID
}
