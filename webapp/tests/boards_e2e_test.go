package tests

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestFlowLifecycle(t *testing.T) {
	a := newApp(t)
	a.refused("POST", "/api/flows", map[string]any{"name": "  "}, http.StatusBadRequest, "name is required")

	f := a.newFlow(" Bump dependencies ")
	if f.Name != "Bump dependencies" || f.LastRun != nil {
		t.Errorf("new flow = %+v", f)
	}
	var renamed flow
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"name": "Bump deps"}, http.StatusOK, &renamed)
	if renamed.Name != "Bump deps" {
		t.Errorf("renamed flow = %+v", renamed)
	}
	var all []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &all)
	if !slices.ContainsFunc(all, func(g flow) bool { return g.ID == f.ID && g.Name == "Bump deps" }) {
		t.Errorf("flows %+v lack the renamed one", all)
	}

	// A flow goes with everything in it, its runs included.
	p := a.agent(f.ID, "Bump", "Bump them.")
	a.output(p.ID, a.newObject(f.ID, "Diff").ID)
	a.startAt(f.ID, p.ID)
	a.worker.onAgent("Bump", handsIn(data("bumped")))
	rn := a.startRun(f.ID)
	a.finish(rn.ID)
	var withRun flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusOK, &withRun)
	if withRun.LastRun == nil || withRun.LastRun.ID != rn.ID {
		t.Errorf("flow's last run = %+v, want run %d", withRun.LastRun, rn.ID)
	}

	a.call("DELETE", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusNoContent, nil)
	a.refused("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusNotFound, "flow not found")
	a.refused("GET", fmt.Sprintf("/api/runs/%d", rn.ID), nil, http.StatusNotFound, "run not found")
}

// What can't be drawn on a board is refused, saying why.
func TestBoardRefusesWhatCantBe(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Rules")
	other := a.newFlow("Elsewhere")
	draft := a.agent(f.ID, "Draft", "Draft it.")
	review := a.newProcess(f.ID, map[string]any{"name": "Review", "kind": kindHuman})
	ticket := a.newObject(f.ID, "Ticket")
	elsewhere := a.agent(other.ID, "Elsewhere", "")
	person := a.newObject(f.ID, "Requester")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", person.ID), map[string]any{"human": true}, http.StatusOK, nil)
	open := a.newState(a.newObject(f.ID, "Issue").ID, "open")

	a.refused("POST", "/api/processes", map[string]any{"flow": f.ID, "name": "X", "kind": "robot"}, http.StatusBadRequest, "kind must be")
	a.refused("POST", "/api/processes", map[string]any{"flow": f.ID, "name": ""}, http.StatusBadRequest, "name is required")
	a.refused("POST", "/api/processes", map[string]any{"flow": f.ID, "name": "X", "width": 5}, http.StatusBadRequest, "width must be")
	a.refused("POST", "/api/objects", map[string]any{"flow": 1 << 40, "name": "X"}, http.StatusBadRequest, "flow not found")

	a.refused("POST", "/api/connections", map[string]any{"source": draft.ID, "target": draft.ID}, http.StatusBadRequest, "itself")
	a.refused("POST", "/api/connections", map[string]any{"source": draft.ID, "target": elsewhere.ID}, http.StatusBadRequest, "different flows")
	a.call("POST", "/api/connections", map[string]any{"source": draft.ID, "target": review.ID}, http.StatusCreated, nil)
	a.refused("POST", "/api/connections", map[string]any{"source": draft.ID, "target": review.ID}, http.StatusConflict, "already exists")

	a.output(draft.ID, ticket.ID)
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": ticket.ID, "direction": "sideways"}, http.StatusBadRequest, "direction must be")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": ticket.ID, "direction": directionOutput, "state": open.ID}, http.StatusBadRequest, "state not found on that object")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": person.ID, "direction": directionInput}, http.StatusBadRequest, agentLinkMessage)
	a.refused("POST", "/api/links", map[string]any{"process": elsewhere.ID, "object": ticket.ID, "direction": directionInput}, http.StatusBadRequest, "isn't on the process's board")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": ticket.ID, "direction": directionInput, "states": []int64{open.ID}}, http.StatusBadRequest, "only a human's output link lists states")
	// A person carries out a human process.
	a.input(review.ID, person.ID)

	// Turning an object into a person linked into an agent, or putting a
	// person at the end, is refused too.
	a.input(draft.ID, a.newObject(f.ID, "Spec").ID)
	var objs []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f.ID), nil, http.StatusOK, &objs)
	spec := objs[slices.IndexFunc(objs, func(o object) bool { return o.Name == "Spec" })]
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", spec.ID), map[string]any{"human": true}, http.StatusBadRequest, agentLinkMessage)
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", person.ID), map[string]any{"end": true}, http.StatusBadRequest, "not a person")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"many": true, "min": 5, "max": 3}, http.StatusBadRequest, "")
}

// Deleting a process takes its links and connections with it.
func TestDeletingAProcessTakesItsEdges(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Deleting")
	draft := a.agent(f.ID, "Draft", "Draft it.")
	publish := a.agent(f.ID, "Publish", "Publish it.")
	ticket := a.newObject(f.ID, "Ticket")
	a.output(draft.ID, ticket.ID)
	a.input(publish.ID, ticket.ID)
	a.call("POST", "/api/connections", map[string]any{"source": draft.ID, "target": publish.ID}, http.StatusCreated, nil)

	a.call("DELETE", fmt.Sprintf("/api/processes/%d", draft.ID), nil, http.StatusNoContent, nil)
	var links []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &links)
	if len(links) != 1 || links[0].Process != publish.ID {
		t.Errorf("links left = %+v, want only Publish's", links)
	}
	var conns []connection
	a.call("GET", fmt.Sprintf("/api/connections?flow=%d", f.ID), nil, http.StatusOK, &conns)
	if len(conns) != 0 {
		t.Errorf("connections left = %+v", conns)
	}
}

// Jumping back to a version puts the board back as it was saved.
func TestRestoringAVersion(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Versions")
	draft := a.agent(f.ID, "Draft", "Draft it.")
	ticket := a.newObject(f.ID, "Ticket")
	a.output(draft.ID, ticket.ID)
	a.startAt(f.ID, draft.ID)

	var saved version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &saved)

	a.call("PATCH", fmt.Sprintf("/api/processes/%d", draft.ID), map[string]any{"name": "Write", "prompt": "Write it."}, http.StatusOK, nil)
	a.agent(f.ID, "Extra", "")
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", ticket.ID), nil, http.StatusNoContent, nil)

	var versions struct {
		Current  *int64    `json:"current"`
		Versions []version `json:"versions"`
	}
	a.call("GET", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusOK, &versions)
	if versions.Current == nil || *versions.Current != saved.ID || len(versions.Versions) != 1 {
		t.Errorf("versions = %+v, want only %d, current", versions, saved.ID)
	}

	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusNoContent, nil)
	var ps []process
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", f.ID), nil, http.StatusOK, &ps)
	if len(ps) != 1 || ps[0].Name != "Draft" || ps[0].Prompt != "Draft it." {
		t.Fatalf("processes after restoring = %+v, want Draft as saved", ps)
	}
	var objs []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f.ID), nil, http.StatusOK, &objs)
	var links []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &links)
	if len(objs) != 1 || objs[0].Name != "Ticket" || len(links) != 1 || links[0].Process != ps[0].ID || links[0].Object != objs[0].ID {
		t.Errorf("after restoring, objects %+v and links %+v, want Draft generating Ticket", objs, links)
	}

	// A run of the restored board runs it as saved.
	a.worker.onAgent("Draft", handsIn(data("drafted")))
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone || len(stepsOf(d, "Draft")) != 1 {
		t.Errorf("run of the restored board %s with steps %s", d.Status, describeSteps(d))
	}
}

// An agent generating objects with attributes hands in each object's fields,
// and each object holds its own part.
func TestOutputSplitsIntoObjectsWithAttributes(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Attributes")
	triage := a.agent(f.ID, "Triage", "Triage the crash.")
	issue := a.newObject(f.ID, "Issue")
	owner := a.newObject(f.ID, "Owner")
	for _, at := range []struct {
		object int64
		name   string
	}{{issue.ID, "title"}, {issue.ID, "severity"}, {owner.ID, "team"}} {
		a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", at.object), map[string]any{"name": at.name, "definition": "The " + at.name}, http.StatusCreated, nil)
	}
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", issue.ID), map[string]any{"name": "title"}, http.StatusBadRequest, "two fields named")
	notify := a.agent(f.ID, "Notify", "Tell the owner.")
	a.output(triage.ID, issue.ID)
	a.output(triage.ID, owner.ID)
	a.input(notify.ID, owner.ID)
	a.startAt(f.ID, triage.ID)

	a.worker.onAgent("Triage", handsIn(map[string]any{
		"Issue": map[string]any{"severity": "high", "title": "Login crashes"},
		"Owner": map[string]any{"team": "auth"},
	}))
	a.worker.onAgent("Notify", handsIn(data("told")))

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s, want done; steps %s", d.Status, describeSteps(d))
	}
	runs := a.worker.runsOf("Triage")
	if len(runs) != 1 || runs[0].Output == nil {
		t.Fatalf("Triage ran %+v, want once, told what to hand in", runs)
	}
	// Fields come in the order the attributes were added.
	got := contentsOf(d, "Issue")
	if len(got) != 1 || strings.Contains(got[0], "auth") || !strings.Contains(got[0], `"severity": "high"`) ||
		!(strings.Contains(got[0], `"title": "Login crashes"`) && strings.Index(got[0], `"title"`) < strings.Index(got[0], `"severity"`)) {
		t.Errorf("Issue held %q", got)
	}
	ns := onlyStep(t, d, "Notify")
	if len(ns.Inputs) != 1 || ns.Inputs[0].Name != "Owner" || !strings.Contains(ns.Inputs[0].Content, "auth") || strings.Contains(ns.Inputs[0].Content, "Login") {
		t.Errorf("Notify took %+v, want only the owner", ns.Inputs)
	}
}
