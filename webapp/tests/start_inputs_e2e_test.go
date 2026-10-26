package tests

// A person starting a run by hand from Run's menu, given what the flow
// starts from as a run over the API is given it: on the board as it is, or
// on the version published.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// What the board says a run started by hand is given.
type handInputs struct {
	Inputs  []apiInputDescription `json:"inputs"`
	Problem string                `json:"problem"`
}

type apiInputDescription struct {
	Name   string         `json:"name"`
	Many   bool           `json:"many"`
	States []string       `json:"states"`
	Schema map[string]any `json:"schema"`
}

func handInputsOf(a *app, f int64, published bool) handInputs {
	a.t.Helper()
	var in handInputs
	a.call("GET", fmt.Sprintf("/api/flows/%d/inputs?published=%t", f, published), nil, http.StatusOK, &in)
	return in
}

func inputNames(in handInputs) string {
	names := []string{}
	for _, i := range in.Inputs {
		names = append(names, i.Name)
	}
	return strings.Join(names, ", ")
}

// What a person gives reaches the process taking it, checked as the API
// checks it, and the run is theirs, as any run from the board.
func TestStartingWithInputsByHand(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)

	in := handInputsOf(a, tf.flow.ID, false)
	if inputNames(in) != "Ticket, Note" || in.Problem != "" || fmt.Sprint(in.Inputs[0].States) != "[urgent]" {
		t.Fatalf("the board starts from %+v", in)
	}
	if req := in.Inputs[0].Schema["required"]; fmt.Sprint(req) != "[id subject]" {
		t.Errorf("the ticket's schema requires %v", req)
	}

	path := fmt.Sprintf("/api/runs?flow=%d", tf.flow.ID)
	ticket := map[string]any{"id": "T-1", "subject": "Printer on fire"}
	a.refused("POST", path, map[string]any{"inputs": map[string]any{"Ticket": ticket}}, http.StatusBadRequest, `lacks "Note"`)
	a.refused("POST", path, map[string]any{"inputs": map[string]any{"Ticket": ticket, "Note": "x"}, "states": map[string]any{"Ticket": "cold"}},
		http.StatusBadRequest, `has no state "cold"`)
	a.refused("POST", path, map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"id": "T-1"}, "Note": "x"}},
		http.StatusBadRequest, `it lacks "subject"`)

	var rn run
	a.call("POST", path, map[string]any{"inputs": map[string]any{"Ticket": ticket, "Note": "call back"}, "states": map[string]any{"Ticket": "urgent"}},
		http.StatusCreated, &rn)
	det := a.finish(rn.ID)
	if det.Status != runDone || det.APIKey != "" || det.Webhook != nil {
		t.Fatalf("the run ended %s, started by key %q, webhook %v: %s", det.Status, det.APIKey, det.Webhook, describeSteps(det))
	}
	if given := onlyStep(t, det, "Given by hand"); given.Kind != kindInput {
		t.Errorf("what it was given is a step of kind %q", given.Kind)
	}
	ins := map[string]runInput{}
	for _, i := range onlyStep(t, det, "Summarize").Inputs {
		ins[i.Name] = i
	}
	if !strings.Contains(ins["Ticket"].Content, "Printer on fire") || ins["Ticket"].State != "urgent" || ins["Note"].Content != "call back" {
		t.Errorf("Summarize took %+v", ins)
	}

	// The run list counts it as a person's.
	listed := func(started string) bool {
		var rs []run
		a.call("GET", fmt.Sprintf("/api/runs?flow=%d&started=%s", tf.flow.ID, started), nil, http.StatusOK, &rs)
		return slices.ContainsFunc(rs, func(r run) bool { return r.ID == rn.ID })
	}
	if !listed("person") || listed("api") {
		t.Errorf("the run isn't listed as a person's")
	}
}

// Run's menu may start the version published with what it starts from,
// which the board as it is may no longer take, and the errors that refuse
// Run refuse it too.
func TestStartingThePublishedVersionWithInputs(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	published := pubPublish(a, tf.flow.ID, "")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", tf.note.ID), map[string]any{"name": "Remark"}, http.StatusOK, nil)

	if got := inputNames(handInputsOf(a, tf.flow.ID, false)); got != "Ticket, Remark" {
		t.Errorf("the board starts from %s", got)
	}
	if got := inputNames(handInputsOf(a, tf.flow.ID, true)); got != "Ticket, Note" {
		t.Errorf("the version published starts from %s", got)
	}

	path := fmt.Sprintf("/api/runs?flow=%d", tf.flow.ID)
	given := map[string]any{"Ticket": map[string]any{"id": "T-2", "subject": "Paper jam"}, "Note": "soon"}
	a.refused("POST", path, map[string]any{"inputs": given}, http.StatusBadRequest, `lacks "Remark"`)
	var rn run
	a.call("POST", path, map[string]any{"published": true, "inputs": given}, http.StatusCreated, &rn)
	det := a.finish(rn.ID)
	if det.Status != runDone || det.Version == nil || *det.Version != published.ID {
		t.Fatalf("the run ended %s on version %v, not %d: %s", det.Status, det.Version, published.ID, describeSteps(det))
	}
	if note := onlyStep(t, det, "Summarize").Inputs; !slices.ContainsFunc(note, func(i runInput) bool { return i.Name == "Note" && i.Content == "soon" }) {
		t.Errorf("Summarize took %+v", note)
	}

	// A subflow process running no flow is an error Run refuses the board
	// for, and so is starting it with inputs; the version published hasn't
	// got it.
	a.newProcess(tf.flow.ID, map[string]any{"name": "Call", "kind": "subflow"})
	a.refused("POST", path, map[string]any{"inputs": map[string]any{"Ticket": given["Ticket"], "Remark": "x"}}, http.StatusConflict, "can't run")
	a.call("POST", path, map[string]any{"published": true, "inputs": given}, http.StatusCreated, &rn)
	a.finish(rn.ID)

	// A struct starts nothing.
	var s flow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprint("Ticket kind ", tf.flow.ID), "struct": true}, http.StatusCreated, &s)
	if in := handInputsOf(a, s.ID, false); in.Problem == "" || len(in.Inputs) != 0 {
		t.Errorf("a struct is said to start from %+v", in)
	}
}
