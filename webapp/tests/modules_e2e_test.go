package tests

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// A flow as the main page lists it: a module or a flow that runs, and how
// many flows run it.
type modFlow struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Module bool   `json:"module"`
	UsedBy int    `json:"usedBy"`
}

type modState struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Rule   string `json:"rule"`
	Source *int64 `json:"source"`
}

type modAttribute struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Source     *int64 `json:"source"`
}

// An object with the module's object it is a copy of, if any.
type modObject struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Many       bool           `json:"many"`
	Persistent bool           `json:"persistent"`
	Shared     bool           `json:"shared"`
	Owner      *int64         `json:"owner"`
	Source     *int64         `json:"source"`
	SourceFlow *int64         `json:"sourceFlow"`
	States     []modState     `json:"states"`
	Attributes []modAttribute `json:"attributes"`
}

func modFlowOf(a *app, id int64) modFlow {
	a.t.Helper()
	var f modFlow
	a.call("GET", fmt.Sprintf("/api/flows/%d", id), nil, http.StatusOK, &f)
	return f
}

func modObjects(a *app, f int64) []modObject {
	a.t.Helper()
	var objs []modObject
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f), nil, http.StatusOK, &objs)
	return objs
}

// The one object named so on the flow's boards that makes up none.
func modNamed(t *testing.T, objs []modObject, name string) modObject {
	t.Helper()
	var found []modObject
	for _, o := range objs {
		if o.Name == name && o.Owner == nil {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		t.Fatalf("objects named %s = %+v, want one", name, found)
	}
	return found[0]
}

func modPartsOf(objs []modObject, owner int64) []modObject {
	var parts []modObject
	for _, o := range objs {
		if o.Owner != nil && *o.Owner == owner {
			parts = append(parts, o)
		}
	}
	return parts
}

func modStateNames(o modObject) string {
	var ns []string
	for _, s := range o.States {
		ns = append(ns, s.Name)
	}
	return strings.Join(ns, ",")
}

func modAttrNames(o modObject) string {
	var ns []string
	for _, at := range o.Attributes {
		ns = append(ns, at.Name+"="+at.Definition)
	}
	return strings.Join(ns, ",")
}

// A flow is made a module, or becomes one once another runs it, and stays
// one while any does.
func TestModules(t *testing.T) {
	a := newApp(t)
	var lib modFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Fix exceptions", "module": true}, http.StatusCreated, &lib)
	if !lib.Module {
		t.Errorf("a flow made a module = %+v", lib)
	}
	job := a.newFlow("Nightly")
	other := a.newFlow("Triage")
	if f := modFlowOf(a, other.ID); f.Module || f.UsedBy != 0 {
		t.Errorf("a new flow = %+v, want one that runs", f)
	}

	a.newProcess(job.ID, map[string]any{"name": "Fix", "kind": kindSubflow, "settings": map[string]any{"flow": lib.ID}})
	call := a.newProcess(job.ID, map[string]any{"name": "Triage", "kind": kindSubflow, "settings": map[string]any{"flow": other.ID}})
	if f := modFlowOf(a, other.ID); !f.Module || f.UsedBy != 1 {
		t.Errorf("a flow another runs = %+v, want a module used by one", f)
	}
	a.newProcess(other.ID, map[string]any{"name": "Fix", "kind": kindSubflow, "settings": map[string]any{"flow": lib.ID}})
	if f := modFlowOf(a, lib.ID); f.UsedBy != 2 {
		t.Errorf("Fix exceptions is used by %d flows, want 2", f.UsedBy)
	}

	var all []modFlow
	a.call("GET", "/api/flows", nil, http.StatusOK, &all)
	// Earlier tests' flows share the database, so only this test's count.
	var modules []string
	for _, f := range all {
		if f.Module && (f.ID == lib.ID || f.ID == job.ID || f.ID == other.ID) {
			modules = append(modules, f.Name)
		}
	}
	if strings.Join(modules, ",") != "Fix exceptions,Triage" {
		t.Errorf("modules listed = %v", modules)
	}

	path := fmt.Sprintf("/api/flows/%d", other.ID)
	a.refused("PATCH", path, map[string]any{"module": false}, http.StatusConflict, "1 other flows run this procedure")
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", call.ID), nil, http.StatusNoContent, nil)
	verPatch(a, path, map[string]any{"module": false})
	if f := modFlowOf(a, other.ID); f.Module {
		t.Errorf("Triage = %+v, want a flow that runs again", f)
	}
}

// What a subflow process brings in follows the module's object it copies:
// its name, how many it is, its states, attributes and parts, keeping the
// ids the caller's links and stored values use. Only the module changes it.
func TestModuleCopiesFollow(t *testing.T) {
	a := newApp(t)
	lib := a.newFlow("Ticketing")
	ticket := a.newObject(lib.ID, "Jira ticket")
	open := a.newState(ticket.ID, "open")
	closed := a.newState(ticket.ID, "closed")
	summary := verAttribute(a, ticket.ID, "summary")
	var meta object
	a.call("POST", "/api/objects", map[string]any{"name": "meta", "owner": ticket.ID}, http.StatusCreated, &meta)
	verAttribute(a, meta.ID, "lang")
	verPatch(a, fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"end": true})

	job := a.newFlow("Nightly")
	call := a.newProcess(job.ID, map[string]any{"name": "Ticket", "kind": kindSubflow, "settings": map[string]any{"flow": lib.ID}})
	objs := modObjects(a, job.ID)
	cp := modNamed(t, objs, "Jira ticket")
	if cp.Source == nil || *cp.Source != ticket.ID || cp.SourceFlow == nil || *cp.SourceFlow != lib.ID {
		t.Fatalf("the copy = %+v, want it from %d in %d", cp, ticket.ID, lib.ID)
	}
	if len(cp.States) != 2 || *cp.States[0].Source != open.ID || *cp.States[1].Source != closed.ID || len(cp.Attributes) != 1 || *cp.Attributes[0].Source != summary.ID {
		t.Errorf("the copy's states and attributes = %+v %+v", cp.States, cp.Attributes)
	}
	openHere, closedHere := cp.States[0].ID, cp.States[1].ID
	// The caller takes the ticket open, and closed.
	takeOpen := a.agent(job.ID, "Work", "Work on it.")
	a.link(map[string]any{"process": takeOpen.ID, "object": cp.ID, "direction": directionInput, "state": openHere})
	takeClosed := a.agent(job.ID, "Report", "Report it.")
	a.link(map[string]any{"process": takeClosed.ID, "object": cp.ID, "direction": directionInput, "state": closedHere})

	// The copy is changed only in the module.
	p := fmt.Sprintf("/api/objects/%d", cp.ID)
	a.refused("POST", p+"/states", map[string]any{"name": "mine"}, http.StatusBadRequest, "comes from the procedure")
	a.refused("POST", p+"/attributes", map[string]any{"name": "mine"}, http.StatusBadRequest, "comes from the procedure")
	a.refused("PATCH", p, map[string]any{"name": "Mine"}, http.StatusBadRequest, "comes from the procedure")
	a.refused("PATCH", p, map[string]any{"many": true}, http.StatusBadRequest, "comes from the procedure")
	a.refused("PATCH", fmt.Sprintf("/api/states/%d", openHere), map[string]any{"name": "mine"}, http.StatusBadRequest, "comes from the procedure")
	a.refused("DELETE", fmt.Sprintf("/api/attributes/%d", cp.Attributes[0].ID), nil, http.StatusBadRequest, "comes from the procedure")
	a.refused("POST", "/api/objects", map[string]any{"name": "mine", "owner": cp.ID}, http.StatusBadRequest, "comes from the procedure")
	part := modPartsOf(objs, cp.ID)
	if len(part) != 1 || part[0].Source == nil || *part[0].Source != meta.ID {
		t.Fatalf("the copy's parts = %+v", part)
	}
	a.refused("DELETE", fmt.Sprintf("/api/objects/%d", part[0].ID), nil, http.StatusBadRequest, "comes from the procedure")
	// Where it goes and whether it is stored are this flow's.
	verPatch(a, p, map[string]any{"x": 500, "persistent": true})

	// The module changes, and the copy with it.
	verPatch(a, fmt.Sprintf("/api/states/%d", open.ID), map[string]any{"name": "to do"})
	a.call("DELETE", fmt.Sprintf("/api/states/%d", closed.ID), nil, http.StatusNoContent, nil)
	a.newState(ticket.ID, "done")
	verPatch(a, fmt.Sprintf("/api/attributes/%d", summary.ID), map[string]any{"name": "title", "definition": "What it is about"})
	verAttribute(a, ticket.ID, "priority")
	verAttribute(a, meta.ID, "reporter")
	var labels object
	a.call("POST", "/api/objects", map[string]any{"name": "labels", "owner": ticket.ID, "relation": "aggregation"}, http.StatusCreated, &labels)
	verPatch(a, fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"name": "Issue", "many": true})

	objs = modObjects(a, job.ID)
	cp = modNamed(t, objs, "Issue")
	if !cp.Many || !cp.Persistent {
		t.Errorf("the copy = %+v, want a list, still stored", cp)
	}
	if got := modStateNames(cp); got != "to do,done" {
		t.Errorf("the copy's states = %s", got)
	}
	if cp.States[0].ID != openHere {
		t.Errorf("the renamed state is %d, want it kept as %d", cp.States[0].ID, openHere)
	}
	if got := modAttrNames(cp); got != "title=What it is about,priority=The priority" {
		t.Errorf("the copy's attributes = %s", got)
	}
	var parts []string
	for _, pt := range modPartsOf(objs, cp.ID) {
		parts = append(parts, pt.Name+"("+modAttrNames(pt)+")")
	}
	if strings.Join(parts, ",") != "meta(lang=The lang,reporter=The reporter),labels()" {
		t.Errorf("the copy's parts = %v", parts)
	}
	var kept []int64
	for _, l := range verLinks(a, job.ID) {
		if l.Object == cp.ID && l.Direction == directionInput {
			kept = append(kept, l.Process)
		}
	}
	if !slices.Equal(kept, []int64{takeOpen.ID}) {
		t.Errorf("processes taking the copy = %v, want only Work, the closed state being gone", kept)
	}
	if !slices.ContainsFunc(verLinks(a, job.ID), func(l link) bool { return l.Process == call.ID && l.Object == cp.ID }) {
		t.Error("the subflow process no longer generates the copy")
	}

	// A new outcome comes out of the subflow processes running the module.
	note := a.newObject(lib.ID, "Note")
	verPatch(a, fmt.Sprintf("/api/objects/%d", note.ID), map[string]any{"end": true})
	n := modNamed(t, modObjects(a, job.ID), "Note")
	if n.Source == nil || *n.Source != note.ID {
		t.Errorf("the new outcome on the caller = %+v", n)
	}

	// Going back to a version of the module takes the copy back too, and a
	// version of the caller follows the module as it is.
	v := verSave(a, lib.ID)
	verPatch(a, fmt.Sprintf("/api/states/%d", open.ID), map[string]any{"name": "backlog"})
	if got := modStateNames(modNamed(t, modObjects(a, job.ID), "Issue")); got != "backlog,done" {
		t.Errorf("the copy's states = %s", got)
	}
	jv := verSave(a, job.ID)
	verRestore(a, v.ID)
	if got := modStateNames(modNamed(t, modObjects(a, job.ID), "Issue")); got != "to do,done" {
		t.Errorf("after the module went back, the copy's states = %s", got)
	}
	verRestore(a, jv.ID)
	cp = modNamed(t, modObjects(a, job.ID), "Issue")
	if got := modStateNames(cp); got != "to do,done" || cp.Source == nil {
		t.Errorf("after the caller went back, the copy = %+v", cp)
	}

	// Deleting the module's object leaves the copy an object of its own.
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", ticket.ID), nil, http.StatusNoContent, nil)
	cp = modNamed(t, modObjects(a, job.ID), "Issue")
	if cp.Source != nil {
		t.Errorf("the copy of a deleted object = %+v", cp)
	}
	a.call("POST", fmt.Sprintf("/api/objects/%d/states", cp.ID), map[string]any{"name": "mine"}, http.StatusCreated, nil)
}

// A module's object copied into another module, which a flow runs, reaches
// that flow too, and follows the first module there.
func TestModuleCopiesFollowThroughModules(t *testing.T) {
	a := newApp(t)
	tickets := a.newFlow("Ticketing")
	ticket := a.newObject(tickets.ID, "Jira ticket")
	open := a.newState(ticket.ID, "open")
	verPatch(a, fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"end": true})

	fixing := a.newFlow("Fix exceptions")
	a.newProcess(fixing.ID, map[string]any{"name": "Ticket", "kind": kindSubflow, "settings": map[string]any{"flow": tickets.ID}})
	inFixing := modNamed(t, modObjects(a, fixing.ID), "Jira ticket")
	verPatch(a, fmt.Sprintf("/api/objects/%d", inFixing.ID), map[string]any{"end": true})

	job := a.newFlow("Nightly")
	a.newProcess(job.ID, map[string]any{"name": "Fix", "kind": kindSubflow, "settings": map[string]any{"flow": fixing.ID}})
	inJob := modNamed(t, modObjects(a, job.ID), "Jira ticket")
	if inJob.Source == nil || *inJob.Source != inFixing.ID {
		t.Fatalf("the job's ticket = %+v, want a copy of Fix exceptions' %d", inJob, inFixing.ID)
	}

	verPatch(a, fmt.Sprintf("/api/states/%d", open.ID), map[string]any{"name": "to do"})
	verAttribute(a, ticket.ID, "summary")
	for _, f := range []int64{fixing.ID, job.ID} {
		o := modNamed(t, modObjects(a, f), "Jira ticket")
		if modStateNames(o) != "to do" || modAttrNames(o) != "summary=The summary" {
			t.Errorf("the ticket in flow %d = %+v", f, o)
		}
	}
}
