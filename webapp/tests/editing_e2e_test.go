package tests

// Board editing, through the API as the frontend edits a board: processes,
// objects and what they are made of, states, attributes, links, connections,
// the start and end of boards, and wrapping processes into a flow process.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The board's processes by name.
func editProcesses(a *app, f int64) map[string]process {
	a.t.Helper()
	var ps []process
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", f), nil, http.StatusOK, &ps)
	m := map[string]process{}
	for _, p := range ps {
		m[p.Name] = p
	}
	return m
}

// The flow's objects by name.
func editObjects(a *app, f int64) map[string]object {
	a.t.Helper()
	var os []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f), nil, http.StatusOK, &os)
	m := map[string]object{}
	for _, o := range os {
		m[o.Name] = o
	}
	return m
}

func editLinks(a *app, f int64) []link {
	a.t.Helper()
	var ls []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f), nil, http.StatusOK, &ls)
	return ls
}

func editConnections(a *app, f int64) []connection {
	a.t.Helper()
	var cs []connection
	a.call("GET", fmt.Sprintf("/api/connections?flow=%d", f), nil, http.StatusOK, &cs)
	return cs
}

func editPatchProcess(a *app, id int64, patch map[string]any) process {
	a.t.Helper()
	var p process
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", id), patch, http.StatusOK, &p)
	return p
}

func editPatchObject(a *app, id int64, patch map[string]any) object {
	a.t.Helper()
	var o object
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", id), patch, http.StatusOK, &o)
	return o
}

// An object making up owner, as relation says, or exhibition by default.
func editOwned(a *app, owner int64, name, relation string) object {
	a.t.Helper()
	body := map[string]any{"owner": owner, "name": name}
	if relation != "" {
		body["relation"] = relation
	}
	var o object
	a.call("POST", "/api/objects", body, http.StatusCreated, &o)
	return o
}

func editAttribute(a *app, object int64, name string) attribute {
	a.t.Helper()
	var at attribute
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", object), map[string]any{"name": name, "definition": "The " + name}, http.StatusCreated, &at)
	return at
}

// A flow process on the flow's top board, with its board.
func editFlowProcess(a *app, f int64, name string) process {
	a.t.Helper()
	return a.newProcess(f, map[string]any{"name": name, "kind": kindFlow})
}

// Whether a link from process to object in direction is on the board.
func editHasLink(ls []link, process, object int64, direction string) bool {
	return slices.ContainsFunc(ls, func(l link) bool {
		return l.Process == process && l.Object == object && l.Direction == direction
	})
}

func editHasConnection(cs []connection, source, target int64) bool {
	return slices.ContainsFunc(cs, func(c connection) bool { return c.Source == source && c.Target == target })
}

func editBoards(a *app, f int64) []innerBoard {
	a.t.Helper()
	var bs []innerBoard
	a.call("GET", fmt.Sprintf("/api/boards?flow=%d", f), nil, http.StatusOK, &bs)
	return bs
}

func TestEditProcessFields(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Process fields")

	var p process
	a.call("POST", "/api/processes", map[string]any{
		"flow": f.ID, "name": "  Triage  ", "prompt": "Look at it.", "script": "echo hi",
		"model": "anthropic/claude", "thinking": "high", "x": 12.5, "y": -3, "width": 300, "recording": true,
	}, http.StatusCreated, &p)
	if p.Name != "Triage" || p.Kind != kindAgent || p.Flow != f.ID || p.Parent != nil {
		t.Errorf("created process = %+v, want Triage, an agent by default, on the top board", p)
	}
	got := editProcesses(a, f.ID)["Triage"]
	if got.Prompt != "Look at it." || got.Script != "echo hi" || got.Model != "anthropic/claude" || got.Thinking != "high" ||
		got.X != 12.5 || got.Y != -3 || got.Width == nil || *got.Width != 300 || !got.Recording {
		t.Errorf("stored process = %+v", got)
	}

	// Each field patches alone, leaving the others.
	for _, c := range []struct {
		patch map[string]any
		check func(process) bool
	}{
		{map[string]any{"name": " Sort "}, func(p process) bool { return p.Name == "Sort" && p.Prompt == "Look at it." }},
		{map[string]any{"prompt": "New prompt."}, func(p process) bool { return p.Prompt == "New prompt." && p.Script == "echo hi" }},
		{map[string]any{"script": "true"}, func(p process) bool { return p.Script == "true" && p.Model == "anthropic/claude" }},
		{map[string]any{"model": ""}, func(p process) bool { return p.Model == "" && p.Thinking == "high" }},
		{map[string]any{"thinking": ""}, func(p process) bool { return p.Thinking == "" }},
		{map[string]any{"x": 1, "y": 2}, func(p process) bool { return p.X == 1 && p.Y == 2 && *p.Width == 300 }},
		{map[string]any{"width": 80}, func(p process) bool { return *p.Width == 80 }},
		{map[string]any{"width": 2000}, func(p process) bool { return *p.Width == 2000 }},
		{map[string]any{"recording": false}, func(p process) bool { return !p.Recording && p.X == 1 }},
		{map[string]any{"kind": kindScript}, func(p process) bool { return p.Kind == kindScript && p.Script == "true" }},
	} {
		if p := editPatchProcess(a, p.ID, c.patch); !c.check(p) {
			t.Errorf("after patching %v, process = %+v", c.patch, p)
		}
	}
	for _, level := range thinkingLevels {
		if p := editPatchProcess(a, p.ID, map[string]any{"thinking": level}); p.Thinking != level {
			t.Errorf("thinking %q stored as %q", level, p.Thinking)
		}
	}

	// What can't be stored is refused, on create and on patch, and changes nothing.
	for _, c := range []struct {
		body map[string]any
		msg  string
	}{
		{map[string]any{"name": "  "}, "name"},
		{map[string]any{"kind": "robot"}, "kind must be"},
		{map[string]any{"model": "--help"}, "invalid model"},
		{map[string]any{"model": strings.Repeat("m", 256)}, "invalid model"},
		{map[string]any{"thinking": "huge"}, "invalid model or thinking"},
		{map[string]any{"width": 79}, "width must be"},
		{map[string]any{"width": 2001}, "width must be"},
		{map[string]any{"settings": map[string]any{"sentry": map[string]any{"organization": "acme corp"}}}, "slugs"},
		{map[string]any{"settings": map[string]any{"sentry": map[string]any{"organization": "acme", "project": "a/b"}}}, "slugs"},
		{map[string]any{"settings": map[string]any{"sentry": map[string]any{"organization": "acme", "sort": "random"}}}, "sort must be"},
		{map[string]any{"settings": map[string]any{"sentry": map[string]any{"organization": "acme", "limit": -1}}}, "limit must be"},
		{map[string]any{"settings": map[string]any{"sentry": map[string]any{"organization": "acme", "limit": maxSentryIssues + 1}}}, "limit must be"},
		{map[string]any{"settings": map[string]any{"sentry": map[string]any{"organization": "acme", "query": strings.Repeat("q", 2001)}}}, "too long"},
	} {
		create := map[string]any{"flow": f.ID, "name": "Refused"}
		for k, v := range c.body {
			create[k] = v
		}
		a.refused("POST", "/api/processes", create, http.StatusBadRequest, c.msg)
		a.refused("PATCH", fmt.Sprintf("/api/processes/%d", p.ID), c.body, http.StatusBadRequest, c.msg)
	}
	if _, ok := editProcesses(a, f.ID)["Refused"]; ok {
		t.Error("a refused process was stored")
	}
	if got := editProcesses(a, f.ID)["Sort"]; got.Kind != kindScript || got.Model != "" || *got.Width != 2000 || got.Settings.Sentry != nil {
		t.Errorf("refused patches changed the process: %+v", got)
	}
	a.refused("POST", "/api/processes", map[string]any{"flow": f.ID, "name": "Bad", "x": "left"}, http.StatusBadRequest, "invalid JSON")
	a.refused("POST", "/api/processes", map[string]any{"flow": 1 << 40, "name": "Lost"}, http.StatusBadRequest, "flow not found")

	// Settings round-trip whole, and a patch replaces them all.
	sentry := map[string]any{"organization": "acme", "project": "web_1", "environment": "prod", "query": "is:unresolved", "sort": "freq", "limit": 5}
	sp := editPatchProcess(a, p.ID, map[string]any{"kind": kindSentry, "settings": map[string]any{"sentry": sentry, "repositories": []string{"api"}}})
	if sp.Settings.Sentry == nil || *sp.Settings.Sentry != (sentrySettings{Organization: "acme", Project: "web_1", Environment: "prod", Query: "is:unresolved", Sort: "freq", Limit: 5}) ||
		sp.Settings.Repositories == nil || !slices.Equal(*sp.Settings.Repositories, []string{"api"}) {
		t.Errorf("settings = %+v", sp.Settings)
	}
	sp = editPatchProcess(a, p.ID, map[string]any{"settings": map[string]any{"repositories": []string{}}})
	if sp.Settings.Sentry != nil || sp.Settings.Repositories == nil || len(*sp.Settings.Repositories) != 0 {
		t.Errorf("settings after replacing = %+v, want no Sentry settings and no repositories", sp.Settings)
	}
	// Switching kinds keeps the settings of the others.
	sp = editPatchProcess(a, p.ID, map[string]any{"settings": map[string]any{"sentry": sentry}})
	sp = editPatchProcess(a, p.ID, map[string]any{"kind": kindAgent})
	if sp.Kind != kindAgent || sp.Settings.Sentry == nil {
		t.Errorf("switching to an agent lost the Sentry settings: %+v", sp)
	}

	a.refused("PATCH", fmt.Sprintf("/api/processes/%d", 1<<40), map[string]any{"name": "Ghost"}, http.StatusNotFound, "process not found")
	a.refused("PATCH", "/api/processes/abc", map[string]any{"name": "Ghost"}, http.StatusBadRequest, "invalid id")
	a.refused("GET", "/api/processes", nil, http.StatusBadRequest, "flow is required")

	// Processes list per flow, in the order they were added.
	other := a.newFlow("Other processes")
	a.agent(other.ID, "Elsewhere", "")
	a.agent(f.ID, "Second", "")
	var ps []process
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", f.ID), nil, http.StatusOK, &ps)
	if len(ps) != 2 || ps[0].Name != "Sort" || ps[1].Name != "Second" {
		t.Errorf("processes of the flow = %+v, want Sort then Second", ps)
	}
}

// Becoming or ceasing to be a human drops the edges the new kind can't have.
func TestEditSwitchingToAndFromHumanDropsEdges(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Switching kinds")
	draft := a.agent(f.ID, "Draft", "Draft it.")
	before := a.agent(f.ID, "Before", "")
	after := a.agent(f.ID, "After", "")
	notes := a.newObject(f.ID, "Notes")
	spec := a.newObject(f.ID, "Spec")
	a.output(draft.ID, notes.ID)
	a.input(draft.ID, spec.ID)
	a.call("POST", "/api/connections", map[string]any{"source": draft.ID, "target": after.ID}, http.StatusCreated, nil)
	a.call("POST", "/api/connections", map[string]any{"source": before.ID, "target": draft.ID}, http.StatusCreated, nil)

	editPatchProcess(a, draft.ID, map[string]any{"kind": kindHuman})
	ls, cs := editLinks(a, f.ID), editConnections(a, f.ID)
	if editHasLink(ls, draft.ID, notes.ID, directionOutput) {
		t.Error("a human kept the output it generated as an agent")
	}
	if !editHasLink(ls, draft.ID, spec.ID, directionInput) {
		t.Error("becoming a human dropped what it takes")
	}
	if editHasConnection(cs, draft.ID, after.ID) {
		t.Error("a human kept its connection to the next process")
	}
	if !editHasConnection(cs, before.ID, draft.ID) {
		t.Error("becoming a human dropped a connection into it")
	}

	// As a human it lists states it decides between, and a person carries it out.
	approved := a.newState(spec.ID, "approved")
	a.link(map[string]any{"process": draft.ID, "object": spec.ID, "direction": directionOutput, "states": []int64{approved.ID}})
	person := a.newObject(f.ID, "Reviewer")
	editPatchObject(a, person.ID, map[string]any{"human": true})
	a.input(draft.ID, person.ID)

	// Patching anything but the kind drops nothing.
	editPatchProcess(a, draft.ID, map[string]any{"name": "Review", "kind": kindHuman})
	if ls := editLinks(a, f.ID); len(ls) != 3 {
		t.Errorf("renaming a human changed its links: %+v", ls)
	}

	editPatchProcess(a, draft.ID, map[string]any{"kind": kindScript})
	ls = editLinks(a, f.ID)
	if editHasLink(ls, draft.ID, spec.ID, directionOutput) {
		t.Error("a script kept a human's decision")
	}
	if editHasLink(ls, draft.ID, person.ID, directionInput) {
		t.Error("a script kept the person who carried it out")
	}
	if !editHasLink(ls, draft.ID, spec.ID, directionInput) {
		t.Error("ceasing to be a human dropped what it takes")
	}
	var lss []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &lss)
	for _, l := range lss {
		if len(l.States) != 0 {
			t.Errorf("link %+v still lists states", l)
		}
	}

	// Between kinds other than human, nothing is dropped.
	a.output(draft.ID, notes.ID)
	editPatchProcess(a, draft.ID, map[string]any{"kind": kindAgent})
	if !editHasLink(editLinks(a, f.ID), draft.ID, notes.ID, directionOutput) {
		t.Error("switching from a script to an agent dropped its output")
	}
}

// A process becoming a flow process gets a board; going back keeps it.
func TestEditFlowProcessBoards(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Boards")
	outer := editFlowProcess(a, f.ID, "Outer")
	plain := a.agent(f.ID, "Plain", "")

	bs := editBoards(a, f.ID)
	if len(bs) != 1 || bs[0].Process != outer.ID || bs[0].Start.X != 40 || bs[0].End.Y != 480 {
		t.Fatalf("boards = %+v, want Outer's at its default place", bs)
	}
	editPatchProcess(a, plain.ID, map[string]any{"kind": kindFlow})
	if bs := editBoards(a, f.ID); len(bs) != 2 {
		t.Errorf("boards after Plain became a flow process = %+v", bs)
	}
	editPatchProcess(a, plain.ID, map[string]any{"kind": kindAgent})
	if bs := editBoards(a, f.ID); len(bs) != 2 {
		t.Errorf("Plain's board went when it stopped being a flow process: %+v", bs)
	}

	// Inside a flow process: its processes and objects are in its flow, whatever flow is given.
	other := a.newFlow("Other boards")
	var inner process
	a.call("POST", "/api/processes", map[string]any{"flow": other.ID, "name": "Inner", "parent": outer.ID}, http.StatusCreated, &inner)
	if inner.Flow != f.ID || inner.Parent == nil || *inner.Parent != outer.ID {
		t.Errorf("inner process = %+v, want on Outer's board in its flow", inner)
	}
	var innerObj object
	a.call("POST", "/api/objects", map[string]any{"flow": other.ID, "name": "Draft", "parent": outer.ID}, http.StatusCreated, &innerObj)
	if innerObj.Flow != f.ID || innerObj.Parent == nil || *innerObj.Parent != outer.ID {
		t.Errorf("inner object = %+v", innerObj)
	}
	// Only a flow process has a board, however it is asked.
	for _, parent := range []int64{plain.ID, 1 << 40} {
		a.refused("POST", "/api/processes", map[string]any{"flow": f.ID, "name": "X", "parent": parent}, http.StatusBadRequest, "parent must be a flow process")
		a.refused("POST", "/api/objects", map[string]any{"flow": f.ID, "name": "X", "parent": parent}, http.StatusBadRequest, "parent must be a flow process")
	}

	// A process inside links to objects on its board and to those its flow
	// process is linked to, not to others on the top board.
	top := a.newObject(f.ID, "Ticket")
	unlinked := a.newObject(f.ID, "Unlinked")
	a.refused("POST", "/api/links", map[string]any{"process": inner.ID, "object": top.ID, "direction": directionInput}, http.StatusBadRequest, "isn't on the process's board")
	a.input(outer.ID, top.ID)
	a.input(inner.ID, top.ID)
	a.output(inner.ID, innerObj.ID)
	a.refused("POST", "/api/links", map[string]any{"process": plain.ID, "object": innerObj.ID, "direction": directionInput}, http.StatusBadRequest, "isn't on the process's board")
	a.refused("POST", "/api/links", map[string]any{"process": inner.ID, "object": unlinked.ID, "direction": directionInput}, http.StatusBadRequest, "isn't on the process's board")
	// Only the top board leads into a flow's end.
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", innerObj.ID), map[string]any{"end": true}, http.StatusBadRequest, "only an object of the top board")

	// Deleting the flow process takes its board with everything on it.
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", outer.ID), nil, http.StatusNoContent, nil)
	if _, ok := editProcesses(a, f.ID)["Inner"]; ok {
		t.Error("the inner process outlived its flow process")
	}
	objs := editObjects(a, f.ID)
	if _, ok := objs["Draft"]; ok {
		t.Error("the inner object outlived its flow process")
	}
	if _, ok := objs["Ticket"]; !ok {
		t.Error("deleting a flow process took an object of the top board linked to it")
	}
	if bs := editBoards(a, f.ID); len(bs) != 1 || bs[0].Process != plain.ID {
		t.Errorf("boards after deleting Outer = %+v", bs)
	}
	if ls := editLinks(a, f.ID); len(ls) != 0 {
		t.Errorf("links left = %+v", ls)
	}
}

// Deleting a process takes its links and connections, and the start
// pointing at it lets go.
func TestEditDeletingAProcess(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Deleting processes")
	first := a.agent(f.ID, "First", "")
	second := a.agent(f.ID, "Second", "")
	third := a.agent(f.ID, "Third", "")
	o := a.newObject(f.ID, "Ticket")
	a.output(first.ID, o.ID)
	a.input(second.ID, o.ID)
	a.call("POST", "/api/connections", map[string]any{"source": first.ID, "target": second.ID}, http.StatusCreated, nil)
	a.call("POST", "/api/connections", map[string]any{"source": second.ID, "target": third.ID}, http.StatusCreated, nil)
	a.startAt(f.ID, first.ID)

	a.call("DELETE", fmt.Sprintf("/api/processes/%d", first.ID), nil, http.StatusNoContent, nil)
	ls, cs := editLinks(a, f.ID), editConnections(a, f.ID)
	if len(ls) != 1 || !editHasLink(ls, second.ID, o.ID, directionInput) {
		t.Errorf("links left = %+v", ls)
	}
	if len(cs) != 1 || !editHasConnection(cs, second.ID, third.ID) {
		t.Errorf("connections left = %+v", cs)
	}
	var s startNode
	a.call("GET", fmt.Sprintf("/api/start?flow=%d", f.ID), nil, http.StatusOK, &s)
	if s.Target != nil {
		t.Errorf("the start still points at %d", *s.Target)
	}
	if _, ok := editObjects(a, f.ID)["Ticket"]; !ok {
		t.Error("deleting a process took the object it generated")
	}
	// Deleting what isn't there is no error.
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", first.ID), nil, http.StatusNoContent, nil)
}

func TestEditObjectFields(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Object fields")
	var o object
	a.call("POST", "/api/objects", map[string]any{"flow": f.ID, "name": "  Ticket ", "x": 5, "y": 6, "width": 120}, http.StatusCreated, &o)
	if o.Name != "Ticket" || o.Flow != f.ID || o.Width == nil || *o.Width != 120 || o.States == nil || o.Attributes == nil {
		t.Errorf("created object = %+v", o)
	}
	// What can only be patched is left out on create.
	a.call("POST", "/api/objects", map[string]any{"flow": f.ID, "name": "Sneaky", "persistent": true, "many": true, "end": true}, http.StatusCreated, nil)
	sneaky := editObjects(a, f.ID)["Sneaky"]
	if sneaky.Persistent || sneaky.Many || sneaky.End {
		t.Errorf("create stored %+v", sneaky)
	}

	for _, c := range []struct {
		patch map[string]any
		check func(object) bool
	}{
		{map[string]any{"name": " Issue "}, func(o object) bool { return o.Name == "Issue" && o.X == 5 }},
		{map[string]any{"x": 7, "y": 8}, func(o object) bool { return o.X == 7 && o.Y == 8 && *o.Width == 120 }},
		{map[string]any{"width": 80}, func(o object) bool { return *o.Width == 80 }},
		{map[string]any{"persistent": true}, func(o object) bool { return o.Persistent }},
		{map[string]any{"many": true}, func(o object) bool { return o.Many && o.Min == 0 && o.Max == nil }},
		{map[string]any{"min": 2, "max": 5}, func(o object) bool { return o.Many && o.Min == 2 && o.Max != nil && *o.Max == 5 }},
		// An absent max keeps it; an explicit null takes it away.
		{map[string]any{"min": 3}, func(o object) bool { return o.Min == 3 && o.Max != nil && *o.Max == 5 }},
		{map[string]any{"max": nil}, func(o object) bool { return o.Min == 3 && o.Max == nil }},
		{map[string]any{"max": 3}, func(o object) bool { return o.Min == 3 && *o.Max == 3 }},
		// One object holds no bounds of the list it was.
		{map[string]any{"many": false}, func(o object) bool { return !o.Many && o.Min == 0 && o.Max == nil }},
		{map[string]any{"persistent": false}, func(o object) bool { return !o.Persistent }},
		{map[string]any{"end": true}, func(o object) bool { return o.End }},
		{map[string]any{"end": false}, func(o object) bool { return !o.End }},
	} {
		if got := editPatchObject(a, o.ID, c.patch); !c.check(got) {
			t.Errorf("after patching %v, object = %+v", c.patch, got)
		}
	}

	for _, c := range []struct {
		patch map[string]any
		msg   string
	}{
		{map[string]any{"name": " "}, "name must not be empty"},
		{map[string]any{"width": 2001}, "width must be"},
		{map[string]any{"min": 2}, "only a list"},
		{map[string]any{"max": 3}, "only a list"},
		{map[string]any{"many": true, "min": -1}, "fewer than 0"},
		{map[string]any{"many": true, "max": 1}, "no list"},
		{map[string]any{"many": true, "min": 5, "max": 3}, "fewer items at most"},
		{map[string]any{"many": true, "max": "lots"}, "invalid max"},
		{map[string]any{"relation": relationAggregation}, "only an object making up another"},
		{map[string]any{"general": "x"}, "invalid general"},
	} {
		a.refused("PATCH", fmt.Sprintf("/api/objects/%d", o.ID), c.patch, http.StatusBadRequest, c.msg)
	}
	if got := editObjects(a, f.ID)["Issue"]; got.Many || got.Min != 0 || *got.Width != 80 || got.Relation != "" {
		t.Errorf("refused patches changed the object: %+v", got)
	}
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", 1<<40), map[string]any{"x": 1}, http.StatusNotFound, "object not found")
	a.refused("POST", "/api/objects", map[string]any{"flow": f.ID, "name": ""}, http.StatusBadRequest, "name is required")
	a.refused("POST", "/api/objects", map[string]any{"flow": f.ID, "name": "Wide", "width": 9999}, http.StatusBadRequest, "width must be")
	a.refused("GET", "/api/objects", nil, http.StatusBadRequest, "flow is required")
}

// A person carries out only a human process, and doesn't lead into an end.
func TestEditPersons(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Persons")
	review := a.newProcess(f.ID, map[string]any{"name": "Review", "kind": kindHuman})
	draft := a.agent(f.ID, "Draft", "")
	requester := a.newObject(f.ID, "Requester")
	ticket := a.newObject(f.ID, "Ticket")

	a.input(draft.ID, ticket.ID)
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"human": true}, http.StatusBadRequest, agentLinkMessage)
	editPatchObject(a, ticket.ID, map[string]any{"end": true})

	if p := editPatchObject(a, requester.ID, map[string]any{"human": true}); !p.Human {
		t.Fatalf("requester = %+v, want a person", p)
	}
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": requester.ID, "direction": directionInput}, http.StatusBadRequest, agentLinkMessage)
	a.input(review.ID, requester.ID)
	// An agent can still generate a person's object, and a human take one in a state.
	a.output(draft.ID, requester.ID)
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", requester.ID), map[string]any{"end": true}, http.StatusBadRequest, "not a person")
	// Ceasing to be a person is always fine.
	editPatchObject(a, requester.ID, map[string]any{"human": false})
	a.input(draft.ID, requester.ID)
}

// Deleting an object takes its links, states and attributes, and what makes it up.
func TestEditDeletingAnObject(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Deleting objects")
	draft := a.agent(f.ID, "Draft", "")
	car := a.newObject(f.ID, "Car")
	a.newState(car.ID, "parked")
	editAttribute(a, car.ID, "model")
	editOwned(a, car.ID, "engine", relationAggregation)
	sports := a.newObject(f.ID, "Sports car")
	editPatchObject(a, sports.ID, map[string]any{"general": car.ID})
	a.output(draft.ID, car.ID)

	a.call("DELETE", fmt.Sprintf("/api/objects/%d", car.ID), nil, http.StatusNoContent, nil)
	objs := editObjects(a, f.ID)
	if _, ok := objs["engine"]; ok {
		t.Error("an owned object outlived its owner")
	}
	if s := objs["Sports car"]; s.General != nil {
		t.Errorf("a specialization still names its deleted general: %+v", s)
	}
	if ls := editLinks(a, f.ID); len(ls) != 0 {
		t.Errorf("links left = %+v", ls)
	}
	if _, ok := editProcesses(a, f.ID)["Draft"]; !ok {
		t.Error("deleting an object took the process generating it")
	}
}

// Objects making up others, and objects specializing others.
func TestEditStructure(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Structure")
	person := a.newObject(f.ID, "Person")
	editAttribute(a, person.ID, "name")

	address := editOwned(a, person.ID, "address", "")
	if address.Relation != relationExhibition || address.Owner == nil || *address.Owner != person.ID || address.Flow != f.ID || address.Parent != nil {
		t.Errorf("owned object = %+v, want exhibited by Person, in its flow", address)
	}
	pets := editOwned(a, person.ID, "pets", relationAggregation)
	if pets.Relation != relationAggregation {
		t.Errorf("pets = %+v, want aggregated", pets)
	}
	// An owned object goes on its owner's board, whatever else is asked.
	flow := editFlowProcess(a, f.ID, "Flow")
	var placed object
	a.call("POST", "/api/objects", map[string]any{"owner": person.ID, "name": "age", "parent": flow.ID, "flow": 1 << 40}, http.StatusCreated, &placed)
	if placed.Parent != nil || placed.Flow != f.ID {
		t.Errorf("owned object placed at %+v", placed)
	}

	for _, c := range []struct {
		body map[string]any
		msg  string
	}{
		{map[string]any{"owner": person.ID, "name": "name"}, `would have two fields named "name"`},
		{map[string]any{"owner": person.ID, "name": "address"}, "two fields"},
		{map[string]any{"owner": person.ID, "name": "boss", "human": true}, "a person can't make up an object"},
		{map[string]any{"owner": person.ID, "name": "x", "relation": "composition"}, "relation must be"},
		{map[string]any{"owner": 1 << 40, "name": "x"}, "owner not found"},
	} {
		a.refused("POST", "/api/objects", c.body, http.StatusBadRequest, c.msg)
	}

	// An owned object is part of what its owner holds, and no more.
	for _, patch := range []map[string]any{{"human": true}, {"persistent": true}, {"end": true}} {
		a.refused("PATCH", fmt.Sprintf("/api/objects/%d", address.ID), patch, http.StatusBadRequest, "making up another")
	}
	a.refused("POST", fmt.Sprintf("/api/objects/%d/states", address.ID), map[string]any{"name": "valid"}, http.StatusBadRequest, "no states of its own")
	draft := a.agent(f.ID, "Draft", "")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": address.ID, "direction": directionOutput}, http.StatusBadRequest, "isn't on the process's board")
	if got := editPatchObject(a, address.ID, map[string]any{"relation": relationAggregation}); got.Relation != relationAggregation {
		t.Errorf("relation = %q", got.Relation)
	}
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", address.ID), map[string]any{"relation": "composition"}, http.StatusBadRequest, "exhibition or aggregation")
	// Owned objects can be lists, and own objects in turn.
	editPatchObject(a, pets.ID, map[string]any{"many": true, "min": 0, "max": 3})
	street := editOwned(a, address.ID, "street", "")
	// Fields only clash within one object: a part of a part may share a name with the owner's.
	editPatchObject(a, street.ID, map[string]any{"name": "name"})
	// Renaming into a clash is refused.
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", pets.ID), map[string]any{"name": "address"}, http.StatusBadRequest, "two fields")
	editAttribute(a, address.ID, "city")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", street.ID), map[string]any{"name": "city"}, http.StatusBadRequest, "two fields")

	// Specialization: the general's fields come first, and no name twice.
	employee := a.newObject(f.ID, "Employee")
	editAttribute(a, employee.ID, "name")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", employee.ID), map[string]any{"general": person.ID}, http.StatusBadRequest, `two fields named "name"`)
	attrs := editObjects(a, f.ID)["Employee"].Attributes
	a.call("DELETE", fmt.Sprintf("/api/attributes/%d", attrs[0].ID), nil, http.StatusNoContent, nil)
	if got := editPatchObject(a, employee.ID, map[string]any{"general": person.ID}); got.General == nil || *got.General != person.ID {
		t.Fatalf("employee = %+v, want specializing Person", got)
	}
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", employee.ID), map[string]any{"name": "name"}, http.StatusBadRequest, nil)
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", employee.ID), map[string]any{"name": "address"}, http.StatusBadRequest, "two fields")
	editAttribute(a, employee.ID, "salary")
	// The general can't gain a field its specialization has.
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", person.ID), map[string]any{"name": "salary"}, http.StatusBadRequest, `"Employee" would have two fields named "salary"`)

	// Nothing can hold itself, through generals or owners.
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", person.ID), map[string]any{"general": employee.ID}, http.StatusBadRequest, "would hold itself")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", person.ID), map[string]any{"general": person.ID}, http.StatusBadRequest, "would hold itself")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", address.ID), map[string]any{"general": person.ID}, http.StatusBadRequest, "would hold itself")
	// Nor specialize an object of another flow, or none at all.
	other := a.newObject(a.newFlow("Other structure").ID, "Thing")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", person.ID), map[string]any{"general": other.ID}, http.StatusBadRequest, "only specialize an object of its flow")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", person.ID), map[string]any{"general": 1 << 40}, http.StatusBadRequest, "only specialize an object of its flow")
	// An explicit null specializes nothing; an absent general keeps it.
	if got := editPatchObject(a, employee.ID, map[string]any{"x": 3}); got.General == nil {
		t.Error("patching x dropped the general")
	}
	if got := editPatchObject(a, employee.ID, map[string]any{"general": nil}); got.General != nil {
		t.Errorf("employee still specializes %d", *got.General)
	}

	// What the process generating Person must hand in follows the structure.
	a.output(draft.ID, person.ID)
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", draft.ID), map[string]any{"prompt": "Describe the person."}, http.StatusOK, nil)
	a.startAt(f.ID, draft.ID)
	a.worker.onAgent("Draft", func(context.Context, agentRun) runResult { return runResult{Error: "only looking"} })
	a.finish(a.startRun(f.ID).ID)
	runs := a.worker.runsOf("Draft")
	if len(runs) != 1 || runs[0].Output == nil || len(runs[0].Output.Objects) != 1 {
		t.Fatalf("Draft was asked %+v, want once, told to hand in Person", runs)
	}
	var names []string
	for _, at := range runs[0].Output.Objects[0].Attributes {
		names = append(names, at.Name)
	}
	if strings.Join(names, ",") != "name,address,pets,age" {
		t.Errorf("Person's fields = %v, want its attribute then what it owns, in order", names)
	}
}

func TestEditStates(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("States")
	issue := a.newObject(f.ID, "Issue")
	fix := a.agent(f.ID, "Fix", "")
	review := a.newProcess(f.ID, map[string]any{"name": "Review", "kind": kindHuman})

	open := a.newState(issue.ID, "  open ")
	if open.Name != "open" || open.Object != issue.ID {
		t.Errorf("state = %+v", open)
	}
	closed := a.newState(issue.ID, "closed")
	wontfix := a.newState(issue.ID, "won't fix")
	a.refused("POST", fmt.Sprintf("/api/objects/%d/states", issue.ID), map[string]any{"name": " "}, http.StatusBadRequest, "name is required")
	a.refused("POST", fmt.Sprintf("/api/objects/%d/states", 1<<40), map[string]any{"name": "lost"}, http.StatusBadRequest, "object not found")

	var renamed objectState
	a.call("PATCH", fmt.Sprintf("/api/states/%d", closed.ID), map[string]any{"name": " resolved "}, http.StatusOK, &renamed)
	if renamed.Name != "resolved" || renamed.Object != issue.ID {
		t.Errorf("renamed state = %+v", renamed)
	}
	a.refused("PATCH", fmt.Sprintf("/api/states/%d", closed.ID), map[string]any{"name": ""}, http.StatusBadRequest, "must not be empty")
	a.refused("PATCH", fmt.Sprintf("/api/states/%d", 1<<40), map[string]any{"name": "x"}, http.StatusNotFound, "state not found")
	if got := editObjects(a, f.ID)["Issue"].States; len(got) != 3 || got[0].Name != "open" || got[1].Name != "resolved" {
		t.Errorf("Issue's states = %+v", got)
	}

	// Deleting a state takes the links naming it, and drops it from decisions.
	a.link(map[string]any{"process": fix.ID, "object": issue.ID, "direction": directionInput, "state": open.ID})
	a.link(map[string]any{"process": fix.ID, "object": issue.ID, "direction": directionOutput, "state": closed.ID})
	a.input(review.ID, issue.ID)
	decision := a.link(map[string]any{"process": review.ID, "object": issue.ID, "direction": directionOutput, "states": []int64{open.ID, wontfix.ID}})
	a.call("DELETE", fmt.Sprintf("/api/states/%d", open.ID), nil, http.StatusNoContent, nil)
	ls := editLinks(a, f.ID)
	if editHasLink(ls, fix.ID, issue.ID, directionInput) {
		t.Error("the condition link on a deleted state is still there")
	}
	if !editHasLink(ls, fix.ID, issue.ID, directionOutput) {
		t.Error("deleting one state took a link naming another")
	}
	for _, l := range ls {
		if l.ID == decision.ID && !slices.Equal(l.States, []int64{wontfix.ID}) {
			t.Errorf("the decision lists %v, want only won't fix", l.States)
		}
	}
	if got := editObjects(a, f.ID)["Issue"].States; len(got) != 2 {
		t.Errorf("Issue's states after deleting = %+v", got)
	}
}

func TestEditAttributes(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Attributes")
	issue := a.newObject(f.ID, "Issue")
	title := editAttribute(a, issue.ID, "title")
	severity := editAttribute(a, issue.ID, "severity")
	if title.Object != issue.ID || title.Definition != "The title" || title.Unique || title.File {
		t.Errorf("attribute = %+v", title)
	}
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", issue.ID), map[string]any{"name": "  "}, http.StatusBadRequest, "name is required")
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", issue.ID), map[string]any{"name": " title "}, http.StatusBadRequest, "two fields")
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", 1<<40), map[string]any{"name": "x"}, http.StatusBadRequest, "object not found")
	// The same name on another object is fine.
	editAttribute(a, a.newObject(f.ID, "Comment").ID, "title")

	for _, c := range []struct {
		patch map[string]any
		check func(attribute) bool
	}{
		{map[string]any{"name": " heading "}, func(at attribute) bool { return at.Name == "heading" && at.Definition == "The title" }},
		{map[string]any{"definition": "What it is called"}, func(at attribute) bool { return at.Definition == "What it is called" && at.Name == "heading" }},
		{map[string]any{"unique": true}, func(at attribute) bool { return at.Unique && !at.File }},
		{map[string]any{"file": true}, func(at attribute) bool { return at.File && at.Unique }},
		{map[string]any{"unique": false, "file": false}, func(at attribute) bool { return !at.Unique && !at.File }},
	} {
		var got attribute
		a.call("PATCH", fmt.Sprintf("/api/attributes/%d", title.ID), c.patch, http.StatusOK, &got)
		if !c.check(got) {
			t.Errorf("after patching %v, attribute = %+v", c.patch, got)
		}
	}
	a.refused("PATCH", fmt.Sprintf("/api/attributes/%d", title.ID), map[string]any{"name": "severity"}, http.StatusBadRequest, "two fields")
	a.refused("PATCH", fmt.Sprintf("/api/attributes/%d", title.ID), map[string]any{"name": ""}, http.StatusBadRequest, "must not be empty")
	a.refused("PATCH", fmt.Sprintf("/api/attributes/%d", 1<<40), map[string]any{"name": "x"}, http.StatusNotFound, "attribute not found")
	a.refused("PATCH", fmt.Sprintf("/api/attributes/%d", 1<<40), map[string]any{"definition": "x"}, http.StatusNotFound, "attribute not found")

	// Only an object on a board is stored, so only its attributes are unique.
	owned := editOwned(a, issue.ID, "reporter", "")
	email := editAttribute(a, owned.ID, "email")
	a.refused("PATCH", fmt.Sprintf("/api/attributes/%d", email.ID), map[string]any{"unique": true}, http.StatusBadRequest, "only its own attributes can be unique")
	var fileAttr attribute
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", email.ID), map[string]any{"file": true}, http.StatusOK, &fileAttr)
	if !fileAttr.File {
		t.Errorf("owned attribute = %+v, want a file", fileAttr)
	}

	got := editObjects(a, f.ID)["Issue"].Attributes
	if len(got) != 2 || got[0].Name != "heading" || got[1].ID != severity.ID {
		t.Errorf("Issue's attributes = %+v, in the order added", got)
	}
	a.call("DELETE", fmt.Sprintf("/api/attributes/%d", severity.ID), nil, http.StatusNoContent, nil)
	if got := editObjects(a, f.ID)["Issue"].Attributes; len(got) != 1 {
		t.Errorf("attributes after deleting = %+v", got)
	}
	// Its name is free again.
	editAttribute(a, issue.ID, "severity")
}

// An attribute is created as the board stores it: what a create says it is
// must be what a reload shows.
func TestEditAttributeCreateReplyMatchesStored(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Attribute flags")
	o := a.newObject(f.ID, "Issue")
	var created attribute
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", o.ID), map[string]any{"name": "key", "unique": true, "file": true}, http.StatusCreated, &created)
	stored := editObjects(a, f.ID)["Issue"].Attributes
	if len(stored) != 1 || stored[0] != created {
		t.Errorf("create replied %+v, but the board stores %+v", created, stored)
	}
}

func TestEditLinks(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Links")
	draft := a.agent(f.ID, "Draft", "")
	review := a.newProcess(f.ID, map[string]any{"name": "Review", "kind": kindHuman})
	notes := a.newObject(f.ID, "Notes")
	other := a.newObject(f.ID, "Other")
	approved := a.newState(notes.ID, "approved")
	changes := a.newState(notes.ID, "changes")
	foreign := a.newState(other.ID, "foreign")

	out := a.output(draft.ID, notes.ID)
	if out.State != nil || len(out.States) != 0 || string(out.Route) != "null" || out.At != nil {
		t.Errorf("output link = %+v", out)
	}
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": notes.ID, "direction": directionOutput}, http.StatusBadRequest, "already generates")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": notes.ID, "direction": directionOutput, "state": approved.ID}, http.StatusBadRequest, "already generates")
	// Taking an object in each of its states, and whatever state it is in, are different links.
	a.link(map[string]any{"process": draft.ID, "object": notes.ID, "direction": directionInput, "state": changes.ID})
	a.link(map[string]any{"process": draft.ID, "object": notes.ID, "direction": directionInput, "state": approved.ID})
	in := a.input(draft.ID, notes.ID)
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": notes.ID, "direction": directionInput}, http.StatusConflict, "link already exists")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": notes.ID, "direction": directionInput, "state": foreign.ID}, http.StatusBadRequest, "state not found on that object")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": notes.ID, "direction": directionInput, "state": 1 << 40}, http.StatusBadRequest, "state not found")
	a.refused("POST", "/api/links", map[string]any{"process": 1 << 40, "object": notes.ID, "direction": directionInput}, http.StatusBadRequest, "")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": 1 << 40, "direction": directionInput}, http.StatusBadRequest, "")
	a.refused("POST", "/api/links", map[string]any{"process": draft.ID, "object": other.ID, "direction": directionOutput, "states": []int64{approved.ID}}, http.StatusBadRequest, "state not found on that object")

	// A human decides by listing states, not by naming one.
	a.refused("POST", "/api/links", map[string]any{"process": review.ID, "object": notes.ID, "direction": directionOutput, "state": approved.ID}, http.StatusBadRequest, "lists the states it decides between")
	a.refused("POST", "/api/links", map[string]any{"process": review.ID, "object": notes.ID, "direction": directionOutput, "states": []int64{foreign.ID}}, http.StatusBadRequest, "state not found on that object")
	if ls := editLinks(a, f.ID); slices.ContainsFunc(ls, func(l link) bool { return l.Process == review.ID }) {
		t.Errorf("a refused decision left a link: %+v", ls)
	}
	decision := a.link(map[string]any{"process": review.ID, "object": notes.ID, "direction": directionOutput, "states": []int64{approved.ID}})
	if !slices.Equal(decision.States, []int64{approved.ID}) {
		t.Errorf("decision = %+v", decision)
	}

	// The states a decision lists are replaced whole, and must be the object's.
	a.call("PUT", fmt.Sprintf("/api/links/%d/states", decision.ID), map[string]any{"states": []int64{changes.ID, approved.ID}}, http.StatusNoContent, nil)
	states := func() []int64 {
		for _, l := range editLinks(a, f.ID) {
			if l.ID == decision.ID {
				return l.States
			}
		}
		return nil
	}
	if got := states(); !slices.Equal(got, []int64{approved.ID, changes.ID}) {
		t.Errorf("decision lists %v", got)
	}
	a.refused("PUT", fmt.Sprintf("/api/links/%d/states", decision.ID), map[string]any{"states": []int64{foreign.ID}}, http.StatusBadRequest, "state not found on that object")
	if got := states(); len(got) != 2 {
		t.Errorf("a refused replacement changed the decision to %v", got)
	}
	a.refused("PUT", fmt.Sprintf("/api/links/%d/states", in.ID), map[string]any{"states": []int64{approved.ID}}, http.StatusNotFound, "no human or agent output link")
	a.call("PUT", fmt.Sprintf("/api/links/%d/states", decision.ID), map[string]any{"states": []int64{}}, http.StatusNoContent, nil)
	if got := states(); len(got) != 0 {
		t.Errorf("decision lists %v, want none", got)
	}

	// Routes: set, refused when too long or not numbers, cleared with null.
	a.call("PATCH", fmt.Sprintf("/api/links/%d", out.ID), map[string]any{"route": []float64{10, 20.5, 30}}, http.StatusNoContent, nil)
	route := func() string {
		for _, l := range editLinks(a, f.ID) {
			if l.ID == out.ID {
				return string(l.Route)
			}
		}
		return ""
	}
	if got := route(); got != "[10,20.5,30]" {
		t.Errorf("route = %s", got)
	}
	a.refused("PATCH", fmt.Sprintf("/api/links/%d", out.ID), map[string]any{"route": make([]float64, maxRouteLanes+1)}, http.StatusBadRequest, "too many segments")
	a.refused("PATCH", fmt.Sprintf("/api/links/%d", out.ID), map[string]any{"route": []string{"up"}}, http.StatusBadRequest, "invalid JSON")
	if got := route(); got != "[10,20.5,30]" {
		t.Errorf("a refused route changed it to %s", got)
	}
	a.call("PATCH", fmt.Sprintf("/api/links/%d", out.ID), map[string]any{"route": nil}, http.StatusNoContent, nil)
	if got := route(); got != "null" {
		t.Errorf("route after clearing = %s", got)
	}

	// Where the object shows on a flow process's board.
	at := func() *point {
		for _, l := range editLinks(a, f.ID) {
			if l.ID == out.ID {
				return l.At
			}
		}
		return nil
	}
	a.call("PUT", fmt.Sprintf("/api/links/%d/at", out.ID), point{X: 3, Y: 4}, http.StatusNoContent, nil)
	if got := at(); got == nil || *got != (point{3, 4}) {
		t.Errorf("at = %+v", got)
	}
	a.call("PUT", fmt.Sprintf("/api/links/%d/at", out.ID), json.RawMessage("null"), http.StatusNoContent, nil)
	if got := at(); got != nil {
		t.Errorf("at after clearing = %+v", got)
	}
	a.refused("PUT", fmt.Sprintf("/api/links/%d/at", out.ID), map[string]any{"x": "left"}, http.StatusBadRequest, "invalid JSON")

	// Links list per flow.
	elsewhere := a.newFlow("Other links")
	a.output(a.agent(elsewhere.ID, "E", "").ID, a.newObject(elsewhere.ID, "EO").ID)
	if ls := editLinks(a, f.ID); len(ls) != 5 {
		t.Errorf("%d links in the flow, want 5: %+v", len(ls), ls)
	}
	a.refused("GET", "/api/links", nil, http.StatusBadRequest, "flow is required")

	a.call("DELETE", fmt.Sprintf("/api/links/%d", out.ID), nil, http.StatusNoContent, nil)
	if editHasLink(editLinks(a, f.ID), draft.ID, notes.ID, directionOutput) {
		t.Error("deleted link still listed")
	}
	// Once it no longer generates it, it can again.
	a.output(draft.ID, notes.ID)
}

func TestEditConnections(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Connections")
	first := a.agent(f.ID, "First", "")
	second := a.agent(f.ID, "Second", "")
	var c connection
	a.call("POST", "/api/connections", map[string]any{"source": first.ID, "target": second.ID}, http.StatusCreated, &c)
	if c.ID == 0 || c.Source != first.ID || c.Target != second.ID {
		t.Errorf("connection = %+v", c)
	}
	// The way back is another connection.
	a.call("POST", "/api/connections", map[string]any{"source": second.ID, "target": first.ID}, http.StatusCreated, nil)
	a.refused("POST", "/api/connections", map[string]any{"source": first.ID, "target": second.ID}, http.StatusConflict, "already exists")
	a.refused("POST", "/api/connections", map[string]any{"source": first.ID, "target": 1 << 40}, http.StatusBadRequest, "process not found")
	a.refused("POST", "/api/connections", map[string]any{"source": first.ID, "target": "x"}, http.StatusBadRequest, "invalid JSON")
	if cs := editConnections(a, f.ID); len(cs) != 2 {
		t.Errorf("connections = %+v", cs)
	}
	a.refused("GET", "/api/connections", nil, http.StatusBadRequest, "flow is required")
	a.call("DELETE", fmt.Sprintf("/api/connections/%d", c.ID), nil, http.StatusNoContent, nil)
	if cs := editConnections(a, f.ID); len(cs) != 1 || cs[0].Source != second.ID {
		t.Errorf("connections after deleting = %+v", cs)
	}
}

func TestEditStartAndEnd(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Start and end")
	first := a.agent(f.ID, "First", "")
	second := a.agent(f.ID, "Second", "")
	box := editFlowProcess(a, f.ID, "Box")
	var inner process
	a.call("POST", "/api/processes", map[string]any{"name": "Inner", "parent": box.ID}, http.StatusCreated, &inner)

	start := func(query string) startNode {
		var s startNode
		a.call("GET", "/api/start?"+query, nil, http.StatusOK, &s)
		return s
	}
	top := fmt.Sprintf("flow=%d", f.ID)
	if s := start(top); s.X != 40 || s.Y != 40 || s.Target != nil || string(s.Route) != "null" {
		t.Errorf("new top start = %+v", s)
	}

	var s startNode
	a.call("PATCH", "/api/start?"+top, map[string]any{"x": 1, "y": 2, "target": first.ID}, http.StatusOK, &s)
	if s.X != 1 || s.Y != 2 || s.Target == nil || *s.Target != first.ID {
		t.Errorf("start = %+v", s)
	}
	a.call("PATCH", "/api/start?"+top, map[string]any{"route": []float64{5, 6}}, http.StatusOK, &s)
	if string(s.Route) != "[5,6]" || *s.Target != first.ID {
		t.Errorf("start with a route = %+v", s)
	}
	// Moving keeps the route; a new target needs a new one.
	a.call("PATCH", "/api/start?"+top, map[string]any{"x": 9}, http.StatusOK, &s)
	if string(s.Route) != "[5,6]" || s.Y != 2 {
		t.Errorf("moved start = %+v", s)
	}
	a.call("PATCH", "/api/start?"+top, map[string]any{"target": second.ID}, http.StatusOK, &s)
	if string(s.Route) != "null" || *s.Target != second.ID {
		t.Errorf("retargeted start = %+v", s)
	}
	a.refused("PATCH", "/api/start?"+top, map[string]any{"target": inner.ID}, http.StatusBadRequest, "not found on that board")
	a.refused("PATCH", "/api/start?"+top, map[string]any{"target": a.agent(a.newFlow("Other start").ID, "Far", "").ID}, http.StatusBadRequest, "not found on that board")
	a.refused("PATCH", "/api/start?"+top, map[string]any{"route": make([]float64, maxRouteLanes+1)}, http.StatusBadRequest, "invalid route")
	a.call("PATCH", "/api/start?"+top, map[string]any{"route": []float64{1}}, http.StatusOK, nil)
	a.call("PATCH", "/api/start?"+top, map[string]any{"route": nil}, http.StatusOK, &s)
	if string(s.Route) != "null" {
		t.Errorf("route after clearing = %s", s.Route)
	}
	a.call("PATCH", "/api/start?"+top, map[string]any{"target": nil}, http.StatusOK, &s)
	if s.Target != nil || s.X != 9 {
		t.Errorf("disconnected start = %+v", s)
	}

	// A flow process's board has a start of its own.
	board := fmt.Sprintf("board=%d", box.ID)
	a.refused("PATCH", "/api/start?"+board, map[string]any{"target": first.ID}, http.StatusBadRequest, "not found on that board")
	a.call("PATCH", "/api/start?"+board, map[string]any{"target": inner.ID, "x": 11}, http.StatusOK, &s)
	if s.Target == nil || *s.Target != inner.ID || s.X != 11 {
		t.Errorf("inner start = %+v", s)
	}
	if s := start(top); s.Target != nil {
		t.Errorf("setting the inner start changed the top one: %+v", s)
	}
	a.refused("GET", "/api/start?board=x", nil, http.StatusBadRequest, "invalid board")
	a.refused("GET", fmt.Sprintf("/api/start?board=%d", 1<<40), nil, http.StatusNotFound, "board not found")
	a.refused("GET", fmt.Sprintf("/api/start?board=%d", first.ID), nil, http.StatusNotFound, "board not found")
	a.refused("GET", "/api/start", nil, http.StatusBadRequest, "flow is required")

	// A top board's end has no place until moved; an inner board's has one.
	end := func(query string) string {
		_, body := a.do("GET", "/api/end?"+query, nil)
		return strings.TrimSpace(string(body))
	}
	if got := end(top); got != "null" {
		t.Errorf("new top end = %s", got)
	}
	var e boardEnd
	a.call("PATCH", "/api/end?"+top, map[string]any{"x": 100, "y": 200}, http.StatusOK, &e)
	if e != (boardEnd{100, 200}) {
		t.Errorf("end = %+v", e)
	}
	a.call("PATCH", "/api/end?"+top, map[string]any{"y": 250}, http.StatusOK, &e)
	if e != (boardEnd{100, 250}) {
		t.Errorf("end after moving down = %+v", e)
	}
	var ie boardEnd
	if err := json.Unmarshal([]byte(end(board)), &ie); err != nil || ie != (boardEnd{40, 480}) {
		t.Errorf("inner end = %s", end(board))
	}
}

// Wrapping processes moves them, and the objects only they link to, into a
// new flow process taking their place.
func TestEditWrap(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Wrapping")
	given := a.newObject(f.ID, "Given")
	first := a.newProcess(f.ID, map[string]any{"name": "First", "kind": kindAgent, "x": 0, "y": 0})
	middle := a.newObject(f.ID, "Middle")
	second := a.newProcess(f.ID, map[string]any{"name": "Second", "kind": kindAgent, "x": 200, "y": 100})
	result := a.newObject(f.ID, "Result")
	after := a.agent(f.ID, "After", "")
	before := a.agent(f.ID, "Before", "")
	a.input(first.ID, given.ID)
	a.output(first.ID, middle.ID)
	a.input(second.ID, middle.ID)
	a.output(second.ID, result.ID)
	a.input(after.ID, result.ID)
	a.call("POST", "/api/connections", map[string]any{"source": first.ID, "target": second.ID}, http.StatusCreated, nil)
	a.call("POST", "/api/connections", map[string]any{"source": second.ID, "target": after.ID}, http.StatusCreated, nil)
	a.call("POST", "/api/connections", map[string]any{"source": before.ID, "target": first.ID}, http.StatusCreated, nil)
	a.startAt(f.ID, first.ID)

	for _, c := range []struct {
		body map[string]any
		msg  string
	}{
		{map[string]any{"name": " ", "processes": []int64{first.ID}}, "name is required"},
		{map[string]any{"name": "Box", "processes": []int64{}}, "at least one process"},
		{map[string]any{"name": "Box", "processes": []int64{first.ID, 1 << 40}}, "process not found"},
	} {
		a.refused("POST", "/api/processes/wrap", c.body, http.StatusBadRequest, c.msg)
	}

	var box process
	a.call("POST", "/api/processes/wrap", map[string]any{"name": " Box ", "processes": []int64{first.ID, second.ID}, "objects": []int64{middle.ID, result.ID}}, http.StatusCreated, &box)
	if box.Name != "Box" || box.Kind != kindFlow || box.Parent != nil || box.X != 100 || box.Y != 50 {
		t.Errorf("flow process = %+v, want Box on the top board between what it wraps", box)
	}

	ps := editProcesses(a, f.ID)
	for _, n := range []string{"First", "Second"} {
		if p := ps[n]; p.Parent == nil || *p.Parent != box.ID {
			t.Errorf("%s = %+v, want inside Box", n, p)
		}
	}
	objs := editObjects(a, f.ID)
	if o := objs["Middle"]; o.Parent == nil || *o.Parent != box.ID {
		t.Errorf("Middle = %+v, want moved in: only processes inside link to it", o)
	}
	if o := objs["Result"]; o.Parent != nil {
		t.Errorf("Result = %+v, want left outside: After takes it", o)
	}
	if o := objs["Given"]; o.Parent != nil {
		t.Errorf("Given moved in without being selected: %+v", o)
	}

	// Links across the boundary become the flow process's; those inside stay.
	ls := editLinks(a, f.ID)
	for _, want := range []struct {
		process, object int64
		direction       string
	}{
		{box.ID, given.ID, directionInput}, {box.ID, result.ID, directionOutput},
		{first.ID, given.ID, directionInput}, {first.ID, middle.ID, directionOutput},
		{second.ID, middle.ID, directionInput}, {second.ID, result.ID, directionOutput},
		{after.ID, result.ID, directionInput},
	} {
		if !editHasLink(ls, want.process, want.object, want.direction) {
			t.Errorf("no %s link from %d to %d", want.direction, want.process, want.object)
		}
	}
	if editHasLink(ls, box.ID, middle.ID, directionOutput) || editHasLink(ls, box.ID, middle.ID, directionInput) {
		t.Error("Box links to an object that moved inside it")
	}
	if len(ls) != 7 {
		t.Errorf("%d links, want 7: %+v", len(ls), ls)
	}

	// Connections across the boundary go.
	cs := editConnections(a, f.ID)
	if len(cs) != 1 || !editHasConnection(cs, first.ID, second.ID) {
		t.Errorf("connections = %+v, want only First to Second", cs)
	}

	// The start pointed at First: now at Box, whose start points at First.
	var s startNode
	a.call("GET", fmt.Sprintf("/api/start?flow=%d", f.ID), nil, http.StatusOK, &s)
	if s.Target == nil || *s.Target != box.ID {
		t.Errorf("top start = %+v, want at Box", s)
	}
	a.call("GET", fmt.Sprintf("/api/start?board=%d", box.ID), nil, http.StatusOK, &s)
	if s.Target == nil || *s.Target != first.ID {
		t.Errorf("Box's start = %+v, want at First", s)
	}

	// The processes must share a board.
	a.refused("POST", "/api/processes/wrap", map[string]any{"name": "Mixed", "processes": []int64{first.ID, after.ID}}, http.StatusBadRequest, "same board")
	other := a.agent(a.newFlow("Other wrap").ID, "Far", "")
	a.refused("POST", "/api/processes/wrap", map[string]any{"name": "Mixed", "processes": []int64{after.ID, other.ID}}, http.StatusBadRequest, "same board")

	// Wrapping inside a board nests the new flow process there.
	var nested process
	a.call("POST", "/api/processes/wrap", map[string]any{"name": "Nested", "processes": []int64{second.ID}, "objects": []int64{middle.ID}}, http.StatusCreated, &nested)
	if nested.Parent == nil || *nested.Parent != box.ID {
		t.Errorf("nested flow process = %+v, want inside Box", nested)
	}
	if o := editObjects(a, f.ID)["Middle"]; o.Parent == nil || *o.Parent != box.ID {
		t.Errorf("Middle = %+v, want left in Box: First still generates it", o)
	}
	if bs := editBoards(a, f.ID); len(bs) != 2 {
		t.Errorf("boards = %+v, want Box's and Nested's", bs)
	}
}
