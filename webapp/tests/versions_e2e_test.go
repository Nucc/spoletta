package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The versions a flow has, newest first, and the current one.
type verList struct {
	Current  *int64    `json:"current"`
	Versions []version `json:"versions"`
}

func verVersions(a *app, f int64) verList {
	a.t.Helper()
	var l verList
	a.call("GET", fmt.Sprintf("/api/versions?flow=%d", f), nil, http.StatusOK, &l)
	return l
}

func verSave(a *app, f int64) version {
	a.t.Helper()
	var v version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f), nil, http.StatusCreated, &v)
	return v
}

func verRestore(a *app, v int64) {
	a.t.Helper()
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", v), nil, http.StatusNoContent, nil)
}

func verIDs(vs []version) []int64 {
	ids := []int64{}
	for _, v := range vs {
		ids = append(ids, v.ID)
	}
	return ids
}

// Everything the API shows of a flow's board, as decoded JSON, ids included.
func verDump(a *app, f int64) map[string]any {
	a.t.Helper()
	dump := map[string]any{}
	for key, path := range map[string]string{
		"processes":   "/api/processes?flow=%d",
		"objects":     "/api/objects?flow=%d",
		"links":       "/api/links?flow=%d",
		"connections": "/api/connections?flow=%d",
		"start":       "/api/start?flow=%d",
		"end":         "/api/end?flow=%d",
		"boards":      "/api/boards?flow=%d",
	} {
		var v any
		a.call("GET", fmt.Sprintf(path, f), nil, http.StatusOK, &v)
		dump[key] = v
	}
	return dump
}

// Fails naming each part of the board that differs.
func verSameBoard(t *testing.T, want, got map[string]any) {
	t.Helper()
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !reflect.DeepEqual(want[k], got[k]) {
			w, _ := json.MarshalIndent(want[k], "", " ")
			g, _ := json.MarshalIndent(got[k], "", " ")
			t.Errorf("%s differ:\nwant %s\n got %s", k, w, g)
		}
	}
}

func verObjects(a *app, f int64) []object {
	a.t.Helper()
	var objs []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f), nil, http.StatusOK, &objs)
	return objs
}

func verObjectNamed(t *testing.T, objs []object, name string) []object {
	t.Helper()
	var found []object
	for _, o := range objs {
		if o.Name == name {
			found = append(found, o)
		}
	}
	return found
}

func verLinks(a *app, f int64) []link {
	a.t.Helper()
	var ls []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f), nil, http.StatusOK, &ls)
	return ls
}

func verProcesses(a *app, f int64) []process {
	a.t.Helper()
	var ps []process
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", f), nil, http.StatusOK, &ps)
	return ps
}

func verPatch(a *app, path string, body any) {
	a.t.Helper()
	status, reply := a.do("PATCH", path, body)
	if status != http.StatusOK && status != http.StatusNoContent {
		a.t.Fatalf("PATCH %s = %d %s", path, status, reply)
	}
}

func verAttribute(a *app, object int64, name string) attribute {
	a.t.Helper()
	var at attribute
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", object), map[string]any{"name": name, "definition": "The " + name}, http.StatusCreated, &at)
	return at
}

// Saving always adds a version on top of the current one; jumping back and
// saving again branches off the older one. Versions are each flow's own.
func TestVersionChain(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Chain of versions")
	other := a.newFlow("Other flow")

	if l := verVersions(a, f.ID); l.Current != nil || len(l.Versions) != 0 {
		t.Fatalf("a new flow has versions %+v", l)
	}
	a.agent(f.ID, "Draft", "Draft it.")
	v1 := verSave(a, f.ID)
	if v1.BasedOn != nil {
		t.Errorf("the first version is based on %d", *v1.BasedOn)
	}
	// Saving by hand saves even an unchanged board.
	v2 := verSave(a, f.ID)
	if v2.ID == v1.ID || v2.BasedOn == nil || *v2.BasedOn != v1.ID {
		t.Errorf("second save = %+v, want a new version based on %d", v2, v1.ID)
	}
	verRestore(a, v1.ID)
	if l := verVersions(a, f.ID); l.Current == nil || *l.Current != v1.ID {
		t.Errorf("after jumping to %d, current = %v", v1.ID, l.Current)
	}
	v3 := verSave(a, f.ID)
	if v3.BasedOn == nil || *v3.BasedOn != v1.ID {
		t.Errorf("save after jumping back = %+v, want based on %d", v3, v1.ID)
	}
	l := verVersions(a, f.ID)
	if want := []int64{v3.ID, v2.ID, v1.ID}; !slices.Equal(verIDs(l.Versions), want) || *l.Current != v3.ID {
		t.Errorf("versions %v current %v, want %v current %d", verIDs(l.Versions), *l.Current, want, v3.ID)
	}
	if ol := verVersions(a, other.ID); ol.Current != nil || len(ol.Versions) != 0 {
		t.Errorf("the other flow got versions %+v", ol)
	}

	// The saved board, as the version has it.
	var s snapshot
	a.call("GET", fmt.Sprintf("/api/versions/%d", v1.ID), nil, http.StatusOK, &s)
	if len(s.Processes) != 1 || s.Processes[0].Name != "Draft" || s.Processes[0].Flow != 0 {
		t.Errorf("v1 holds processes %+v", s.Processes)
	}

	missing := int64(1 << 40)
	a.refused("GET", fmt.Sprintf("/api/versions/%d", missing), nil, http.StatusNotFound, "version not found")
	a.refused("POST", fmt.Sprintf("/api/versions/%d/restore", missing), nil, http.StatusNotFound, "version not found")
	a.refused("GET", fmt.Sprintf("/api/versions?flow=%d", missing), nil, http.StatusNotFound, "flow not found")
	a.refused("POST", fmt.Sprintf("/api/versions?flow=%d", missing), nil, http.StatusNotFound, "flow not found")
	a.refused("GET", "/api/versions", nil, http.StatusBadRequest, "flow is required")
	a.refused("GET", "/api/versions/abc", nil, http.StatusBadRequest, "invalid id")
}

// A run saves the board as a new version only if it changed since its
// current one, moving a box included; otherwise it runs on that version.
func TestRunSavesAVersionOnlyWhenTheBoardChanged(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Run versions")
	p := a.agent(f.ID, "Draft", "Draft it.")

	r1 := a.finish(a.startRun(f.ID).ID)
	if r1.Status != runDone || r1.Version == nil {
		t.Fatalf("first run = %+v", r1.run)
	}
	l := verVersions(a, f.ID)
	if len(l.Versions) != 1 || *l.Current != *r1.Version {
		t.Fatalf("after the first run, versions %v current %v, want only run's %d", verIDs(l.Versions), l.Current, *r1.Version)
	}

	r2 := a.finish(a.startRun(f.ID).ID)
	if *r2.Version != *r1.Version {
		t.Errorf("an unchanged board ran on version %d, not %d", *r2.Version, *r1.Version)
	}

	verPatch(a, fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"prompt": "Draft it better."})
	r3 := a.finish(a.startRun(f.ID).ID)
	if *r3.Version == *r1.Version {
		t.Error("a changed prompt ran on the old version")
	}
	verPatch(a, fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"x": 300})
	r4 := a.finish(a.startRun(f.ID).ID)
	if *r4.Version == *r3.Version {
		t.Error("a moved process ran on the old version")
	}
	l = verVersions(a, f.ID)
	if want := []int64{*r4.Version, *r3.Version, *r1.Version}; !slices.Equal(verIDs(l.Versions), want) {
		t.Errorf("versions %v, want %v", verIDs(l.Versions), want)
	}
	if b := l.Versions[0].BasedOn; b == nil || *b != *r3.Version {
		t.Errorf("run 4's version based on %v, want %d", b, *r3.Version)
	}
	var s snapshot
	a.call("GET", fmt.Sprintf("/api/versions/%d", *r3.Version), nil, http.StatusOK, &s)
	if s.Processes[0].Prompt != "Draft it better." || s.Processes[0].X != 0 {
		t.Errorf("run 3's version holds %+v", s.Processes[0])
	}
}

// Jumping back to a version puts back everything on the board, keeping
// every id, whatever was changed, deleted or added since.
func TestRestoreBringsBackTheWholeBoard(t *testing.T) {
	a := newApp(t)
	callee := a.newFlow("Summarizing")
	sum := a.newObject(callee.ID, "Summary")
	verPatch(a, fmt.Sprintf("/api/objects/%d", sum.ID), map[string]any{"end": true})

	f := a.newFlow("Rich board")
	model := "fake/model"
	draft := a.newProcess(f.ID, map[string]any{"name": "Draft", "kind": kindAgent, "prompt": "Draft it.", "model": model, "thinking": "high", "width": 320, "x": 10, "y": 20,
		"settings": map[string]any{"repositories": []string{"api"}}})
	lint := a.newProcess(f.ID, map[string]any{"name": "Lint", "kind": kindScript, "script": "echo ok"})
	review := a.newProcess(f.ID, map[string]any{"name": "Review", "kind": kindHuman, "prompt": "Check it."})
	inner := a.newProcess(f.ID, map[string]any{"name": "Inner", "kind": kindFlow, "recording": true})
	a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindSubflow, "settings": map[string]any{"flow": callee.ID}})

	ticket := a.newObject(f.ID, "Ticket")
	open := a.newState(ticket.ID, "open")
	closed := a.newState(ticket.ID, "closed")
	title := verAttribute(a, ticket.ID, "title")
	attachment := verAttribute(a, ticket.ID, "attachment")
	verPatch(a, fmt.Sprintf("/api/attributes/%d", title.ID), map[string]any{"unique": true})
	verPatch(a, fmt.Sprintf("/api/attributes/%d", attachment.ID), map[string]any{"file": true})
	verPatch(a, fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"persistent": true, "width": 250})
	var location object
	a.call("POST", "/api/objects", map[string]any{"name": "location", "owner": ticket.ID, "relation": relationAggregation}, http.StatusCreated, &location)
	verAttribute(a, location.ID, "city")
	bug := a.newObject(f.ID, "Bug")
	verPatch(a, fmt.Sprintf("/api/objects/%d", bug.ID), map[string]any{"general": ticket.ID})
	tickets := a.newObject(f.ID, "Tickets")
	verPatch(a, fmt.Sprintf("/api/objects/%d", tickets.ID), map[string]any{"many": true, "min": 1, "max": 5})
	var requester object
	a.call("POST", "/api/objects", map[string]any{"flow": f.ID, "name": "Requester", "human": true}, http.StatusCreated, &requester)
	report := a.newObject(f.ID, "Report")
	verPatch(a, fmt.Sprintf("/api/objects/%d", report.ID), map[string]any{"end": true})

	drafted := a.link(map[string]any{"process": draft.ID, "object": ticket.ID, "direction": directionOutput, "state": open.ID})
	a.input(review.ID, ticket.ID)
	a.input(review.ID, requester.ID)
	a.link(map[string]any{"process": review.ID, "object": ticket.ID, "direction": directionOutput, "states": []int64{open.ID, closed.ID}})
	a.link(map[string]any{"process": lint.ID, "object": ticket.ID, "direction": directionInput, "state": closed.ID})
	a.output(lint.ID, report.ID)
	toInner := a.input(inner.ID, ticket.ID)
	a.call("PATCH", fmt.Sprintf("/api/links/%d", drafted.ID), map[string]any{"route": []float64{15, 25, 35}}, http.StatusNoContent, nil)
	a.call("PUT", fmt.Sprintf("/api/links/%d/at", toInner.ID), map[string]any{"x": 5, "y": 6}, http.StatusNoContent, nil)
	var conn connection
	a.call("POST", "/api/connections", map[string]any{"source": draft.ID, "target": lint.ID}, http.StatusCreated, &conn)

	step := a.newProcess(f.ID, map[string]any{"name": "Step", "kind": kindAgent, "prompt": "Inside.", "parent": inner.ID})
	var note object
	a.call("POST", "/api/objects", map[string]any{"flow": f.ID, "name": "Note", "parent": inner.ID}, http.StatusCreated, &note)
	a.output(step.ID, note.ID)
	a.input(step.ID, ticket.ID)
	verPatch(a, fmt.Sprintf("/api/start?board=%d", inner.ID), map[string]any{"target": step.ID, "route": []float64{1, 2}})
	verPatch(a, fmt.Sprintf("/api/end?board=%d", inner.ID), map[string]any{"x": 7, "y": 8})
	verPatch(a, fmt.Sprintf("/api/start?flow=%d", f.ID), map[string]any{"target": draft.ID, "route": []float64{3, 4}, "x": 11, "y": 12})
	verPatch(a, fmt.Sprintf("/api/end?flow=%d", f.ID), map[string]any{"x": 100, "y": 200})

	saved := verDump(a, f.ID)
	v := verSave(a, f.ID)

	// Change everything there is to change.
	verPatch(a, fmt.Sprintf("/api/processes/%d", draft.ID), map[string]any{"name": "Write", "prompt": "Write it.", "model": "other/model", "thinking": "low", "width": 500, "x": 99,
		"settings": map[string]any{"repositories": []string{}}})
	verPatch(a, fmt.Sprintf("/api/processes/%d", lint.ID), map[string]any{"kind": kindAgent})
	verPatch(a, fmt.Sprintf("/api/processes/%d", inner.ID), map[string]any{"recording": false})
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", review.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/states/%d", closed.ID), nil, http.StatusNoContent, nil)
	verPatch(a, fmt.Sprintf("/api/attributes/%d", title.ID), map[string]any{"name": "headline", "unique": false})
	a.call("DELETE", fmt.Sprintf("/api/attributes/%d", attachment.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", location.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", bug.ID), nil, http.StatusNoContent, nil)
	verPatch(a, fmt.Sprintf("/api/objects/%d", tickets.ID), map[string]any{"many": false})
	verPatch(a, fmt.Sprintf("/api/objects/%d", report.ID), map[string]any{"end": false, "name": "Summary report"})
	verPatch(a, fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"persistent": false})
	a.call("PATCH", fmt.Sprintf("/api/links/%d", drafted.ID), map[string]any{"route": nil}, http.StatusNoContent, nil)
	a.call("PUT", fmt.Sprintf("/api/links/%d/at", toInner.ID), json.RawMessage("null"), http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/connections/%d", conn.ID), nil, http.StatusNoContent, nil)
	verPatch(a, fmt.Sprintf("/api/start?flow=%d", f.ID), map[string]any{"target": nil, "x": 0})
	verPatch(a, fmt.Sprintf("/api/end?flow=%d", f.ID), map[string]any{"x": 1})
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", inner.ID), nil, http.StatusNoContent, nil)
	added := a.agent(f.ID, "Added", "New.")
	addedObj := a.newObject(f.ID, "Added object")
	a.output(added.ID, addedObj.ID)

	if changed := verDump(a, f.ID); reflect.DeepEqual(saved, changed) {
		t.Fatal("the changes changed nothing")
	}
	verRestore(a, v.ID)
	verSameBoard(t, saved, verDump(a, f.ID))
	if l := verVersions(a, f.ID); *l.Current != v.ID || len(l.Versions) != 1 {
		t.Errorf("restoring made versions %v current %d", verIDs(l.Versions), *l.Current)
	}
	// The board as restored is the version, so a run makes no new one.
	a.worker.onAgent("Draft", func(context.Context, agentRun) runResult { return runResult{Error: "not this time"} })
	rn := a.startRun(f.ID)
	if rn.Version == nil || *rn.Version != v.ID {
		t.Errorf("a run of the restored board ran on version %v, want %d", rn.Version, v.ID)
	}
	if d := a.finish(rn.ID); d.Status != runFailed || onlyStep(t, d, "Draft").Error != "not this time" {
		t.Errorf("the restored board's run %s; steps %s", d.Status, describeSteps(d))
	}
	// The callee kept its board.
	if objs := verObjects(a, callee.ID); len(objs) != 1 || objs[0].ID != sum.ID {
		t.Errorf("the callee's objects are now %+v", objs)
	}
}

// Runs keep the ids of processes deleted since, and jumping back to the
// version a run ran on brings them back under the same ids.
func TestRunsKeepDeletedProcesses(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Deleted processes")
	gone := a.agent(f.ID, "Gone", "Do it.")
	result := a.newObject(f.ID, "Result")
	a.output(gone.ID, result.ID)
	a.startAt(f.ID, gone.ID)
	a.worker.onAgent("Gone", handsIn(data("done")))
	rn := a.finish(a.startRun(f.ID).ID)

	a.call("DELETE", fmt.Sprintf("/api/processes/%d", gone.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", result.ID), nil, http.StatusNoContent, nil)
	d := a.getRun(rn.ID)
	s := onlyStep(t, d, "Gone")
	if s.Process == nil || *s.Process != gone.ID {
		t.Errorf("the step's process = %v, want %d", s.Process, gone.ID)
	}
	if len(d.Objects) != 1 || d.Objects[0].Object == nil || *d.Objects[0].Object != result.ID {
		t.Errorf("the run's objects = %+v, want Result as %d", d.Objects, result.ID)
	}

	verRestore(a, *rn.Version)
	ps := verProcesses(a, f.ID)
	if len(ps) != 1 || ps[0].ID != gone.ID || ps[0].Name != "Gone" {
		t.Errorf("after restoring, processes %+v, want Gone as %d", ps, gone.ID)
	}
	if objs := verObjects(a, f.ID); len(objs) != 1 || objs[0].ID != result.ID {
		t.Errorf("after restoring, objects %+v, want Result as %d", objs, result.ID)
	}
	if s := onlyStep(t, a.getRun(rn.ID), "Gone"); *s.Process != gone.ID {
		t.Errorf("after restoring, the step's process = %d", *s.Process)
	}
}

// A run follows the board as it goes, so it can't be rewritten under it.
func TestRestoreRefusedWhileRunning(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Busy")
	review := a.newProcess(f.ID, map[string]any{"name": "Review", "kind": kindHuman})
	a.startAt(f.ID, review.ID)
	v := verSave(a, f.ID)
	rn := a.startRun(f.ID)
	a.waitStep(rn.ID, "Review", stepWaiting)

	a.refused("POST", fmt.Sprintf("/api/versions/%d/restore", v.ID), nil, http.StatusConflict, "while the flow is running")
	// Saving is fine while it runs.
	verSave(a, f.ID)
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
	a.finish(rn.ID)
	verRestore(a, v.ID)

	// Another flow running doesn't stop this one's restore.
	other := a.newFlow("Other busy")
	r2 := a.newProcess(other.ID, map[string]any{"name": "Review", "kind": kindHuman})
	a.startAt(other.ID, r2.ID)
	orn := a.startRun(other.ID)
	a.waitStep(orn.ID, "Review", stepWaiting)
	verRestore(a, v.ID)
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", orn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
}

// Flow settings are tidied and checked, and name the branches runs work on.
func TestFlowSettings(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Bump Deps!")
	if f.Settings.Repositories == nil || len(f.Settings.Repositories) != 0 || f.Settings.BranchPrefix != "" {
		t.Errorf("a new flow's settings = %+v", f.Settings)
	}
	path := fmt.Sprintf("/api/flows/%d", f.ID)
	for _, c := range []struct {
		settings map[string]any
		msg      string
	}{
		{map[string]any{"repositories": []map[string]any{{"url": "http://github.com/acme/api"}}}, "isn't an https URL"},
		{map[string]any{"repositories": []map[string]any{{"url": "https://github.com/acme/api x"}}}, "isn't an https URL"},
		{map[string]any{"repositories": []map[string]any{{"url": "https://github.com/acme/api", "name": "a b"}}}, "isn't a valid repository name"},
		{map[string]any{"repositories": []map[string]any{{"url": "https://github.com/acme/api"}, {"url": "https://gitlab.com/acme/api.git"}}}, `two repositories are named "api"`},
		{map[string]any{"repositories": []map[string]any{{"url": "https://github.com/acme/api", "base": "bad..base"}}}, "isn't a valid branch name"},
		{map[string]any{"branchPrefix": "bad..prefix"}, "branch prefix isn't a valid branch name"},
		{map[string]any{"branchPrefix": "x.lock"}, "branch prefix isn't a valid branch name"},
	} {
		a.refused("PATCH", path, map[string]any{"settings": c.settings}, http.StatusBadRequest, c.msg)
	}
	a.refused("PATCH", path, map[string]any{"name": " "}, http.StatusBadRequest, "name must not be empty")
	a.refused("PATCH", fmt.Sprintf("/api/flows/%d", int64(1<<40)), map[string]any{"name": "x"}, http.StatusNotFound, "flow not found")
	a.refused("GET", "/api/flows/x", nil, http.StatusBadRequest, "invalid id")

	var got flow
	a.call("PATCH", path, map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": " https://github.com/acme/api.git ", "base": " main "}, {"url": "https://github.com/acme/web/", "name": "site"}},
	}}, http.StatusOK, &got)
	want := []repository{{Name: "api", URL: "https://github.com/acme/api.git", Base: "main"}, {Name: "site", URL: "https://github.com/acme/web/"}}
	if !reflect.DeepEqual(got.Settings.Repositories, want) || got.Settings.BranchPrefix != "" || got.Name != "Bump Deps!" {
		t.Errorf("settings = %+v, want repositories %+v", got.Settings, want)
	}

	// Runs work on <prefix>/run-<id>; the prefix defaults to the flow's name.
	p := a.agent(f.ID, "Bump", "Bump them.")
	a.output(p.ID, a.newObject(f.ID, "Diff").ID)
	a.startAt(f.ID, p.ID)
	a.worker.onAgent("Bump", handsIn(data("bumped")))
	rn := a.finish(a.startRun(f.ID).ID)
	runs := a.worker.runsOf("Bump")
	if len(runs) != 1 || runs[0].Workspace == nil {
		t.Fatalf("Bump ran %+v, want once in a workspace", runs)
	}
	if ws := runs[0].Workspace; ws.Branch != fmt.Sprintf("flow/bump-deps/run-%d", rn.ID) || !reflect.DeepEqual(ws.Repositories, want) {
		t.Errorf("workspace = %+v", ws)
	}
	if s := onlyStep(t, rn, "Bump"); s.Branch != runs[0].Workspace.Branch {
		t.Errorf("the step's branch = %q", s.Branch)
	}

	a.call("PATCH", path, map[string]any{"settings": map[string]any{"repositories": want, "branchPrefix": " /team/bots/ "}}, http.StatusOK, &got)
	if got.Settings.BranchPrefix != "team/bots" {
		t.Errorf("branch prefix = %q", got.Settings.BranchPrefix)
	}
	// A process picking only some repositories works on those.
	verPatch(a, fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"settings": map[string]any{"repositories": []string{"site"}}})
	rn = a.finish(a.startRun(f.ID).ID)
	runs = a.worker.runsOf("Bump")
	if ws := runs[1].Workspace; ws == nil || ws.Branch != fmt.Sprintf("team/bots/run-%d", rn.ID) || len(ws.Repositories) != 1 || ws.Repositories[0].Name != "site" {
		t.Errorf("second workspace = %+v", ws)
	}
	// One picking none works in no workspace.
	verPatch(a, fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"settings": map[string]any{"repositories": []string{}}})
	a.finish(a.startRun(f.ID).ID)
	if ws := a.worker.runsOf("Bump")[2].Workspace; ws != nil {
		t.Errorf("a process with no repositories worked in %+v", ws)
	}
}

// Deleting a flow deletes all in it, inner boards, versions and runs
// included, and leaves other flows as they were.
func TestDeletingAFlowLeavesOthersAlone(t *testing.T) {
	a := newApp(t)
	build := func(name string) (flow, int64, int64) {
		f := a.newFlow(name)
		inner := a.newProcess(f.ID, map[string]any{"name": "Inner", "kind": kindFlow})
		step := a.newProcess(f.ID, map[string]any{"name": name + " step", "kind": kindAgent, "prompt": "Go.", "parent": inner.ID})
		var note object
		a.call("POST", "/api/objects", map[string]any{"flow": f.ID, "name": "Note", "parent": inner.ID}, http.StatusCreated, &note)
		a.output(step.ID, note.ID)
		verPatch(a, fmt.Sprintf("/api/start?board=%d", inner.ID), map[string]any{"target": step.ID})
		top := a.newObject(f.ID, "Ticket")
		a.newState(top.ID, "open")
		verAttribute(a, top.ID, "title")
		a.startAt(f.ID, inner.ID)
		a.worker.onAgent(name+" step", handsIn(data("noted")))
		rn := a.finish(a.startRun(f.ID).ID)
		if rn.Status != runDone {
			t.Fatalf("run of %s %s; steps %s", name, rn.Status, describeSteps(rn))
		}
		return f, rn.ID, *rn.Version
	}
	doomed, doomedRun, doomedVersion := build("Doomed")
	kept, keptRun, keptVersion := build("Kept")
	before := verDump(a, kept.ID)

	a.call("DELETE", fmt.Sprintf("/api/flows/%d", doomed.ID), nil, http.StatusNoContent, nil)
	a.refused("GET", fmt.Sprintf("/api/runs/%d", doomedRun), nil, http.StatusNotFound, "run not found")
	a.refused("GET", fmt.Sprintf("/api/versions/%d", doomedVersion), nil, http.StatusNotFound, "version not found")
	for _, path := range []string{"processes", "objects", "links", "boards", "runs"} {
		var rows []any
		a.call("GET", fmt.Sprintf("/api/%s?flow=%d", path, doomed.ID), nil, http.StatusOK, &rows)
		if len(rows) != 0 {
			t.Errorf("the deleted flow still has %s %v", path, rows)
		}
	}
	var flows []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
	if slices.ContainsFunc(flows, func(f flow) bool { return f.ID == doomed.ID }) {
		t.Error("the deleted flow is still listed")
	}

	verSameBoard(t, before, verDump(a, kept.ID))
	if d := a.getRun(keptRun); d.Status != runDone || len(d.Steps) != 2 {
		t.Errorf("the kept flow's run is now %s with steps %s", d.Status, describeSteps(d))
	}
	a.call("GET", fmt.Sprintf("/api/versions/%d", keptVersion), nil, http.StatusOK, nil)
	// Deleting it again, or one never there, is no error.
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", doomed.ID), nil, http.StatusNoContent, nil)
}

// A flow's last run is its latest run of the whole flow, not a run of it a
// subflow process made, nor a recording flow process's.
func TestFlowLastRunIsTheLatestTopLevelRun(t *testing.T) {
	a := newApp(t)
	callee := a.newFlow("Callee")
	work := a.agent(callee.ID, "Work", "Work.")
	out := a.newObject(callee.ID, "Out")
	a.output(work.ID, out.ID)
	verPatch(a, fmt.Sprintf("/api/objects/%d", out.ID), map[string]any{"end": true})
	a.startAt(callee.ID, work.ID)

	caller := a.newFlow("Caller")
	call := a.newProcess(caller.ID, map[string]any{"name": "Call", "kind": kindSubflow, "settings": map[string]any{"flow": callee.ID}})
	rec := a.newProcess(caller.ID, map[string]any{"name": "Rec", "kind": kindFlow, "recording": true})
	a.call("POST", "/api/connections", map[string]any{"source": call.ID, "target": rec.ID}, http.StatusCreated, nil)
	a.startAt(caller.ID, call.ID)
	a.worker.onAgent("Work", handsIn(data("worked")))

	first := a.finish(a.startRun(caller.ID).ID)
	second := a.finish(a.startRun(caller.ID).ID)
	if first.Status != runDone || second.Status != runDone {
		t.Fatalf("runs %s and %s; steps %s", first.Status, second.Status, describeSteps(second))
	}
	if cs := onlyStep(t, second, "Rec"); cs.ChildRun == nil {
		t.Error("the recording flow process made no run")
	}
	var got flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", caller.ID), nil, http.StatusOK, &got)
	if got.LastRun == nil || got.LastRun.ID != second.ID {
		t.Errorf("caller's last run = %+v, want %d", got.LastRun, second.ID)
	}
	var calleeRuns []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d", callee.ID), nil, http.StatusOK, &calleeRuns)
	if len(calleeRuns) != 2 {
		t.Fatalf("callee has runs %+v, want the two the caller made", calleeRuns)
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d", callee.ID), nil, http.StatusOK, &got)
	if got.LastRun != nil {
		t.Errorf("callee's last run = %+v, want none of its own", got.LastRun)
	}
	own := a.finish(a.startRun(callee.ID).ID)
	a.call("GET", fmt.Sprintf("/api/flows/%d", callee.ID), nil, http.StatusOK, &got)
	if got.LastRun == nil || got.LastRun.ID != own.ID {
		t.Errorf("callee's last run = %+v, want its own %d", got.LastRun, own.ID)
	}
}

// No flow runs itself, however many subflows apart, whether the subflow
// process is made so or turned into one later.
func TestSubflowCycleChecks(t *testing.T) {
	a := newApp(t)
	fa, fb, fc := a.newFlow("A"), a.newFlow("B"), a.newFlow("C")
	sub := func(f, callee int64, name string) map[string]any {
		return map[string]any{"flow": f, "name": name, "kind": kindSubflow, "settings": map[string]any{"flow": callee}}
	}
	a.refused("POST", "/api/processes", sub(fa.ID, 1<<40, "Nowhere"), http.StatusBadRequest, "flow not found")
	a.refused("POST", "/api/processes", sub(fa.ID, fa.ID, "Self"), http.StatusBadRequest, "can't run itself")
	a.newProcess(fa.ID, sub(fa.ID, fb.ID, "A calls B"))
	bc := a.newProcess(fb.ID, sub(fb.ID, fc.ID, "B calls C"))
	a.refused("POST", "/api/processes", sub(fc.ID, fa.ID, "C calls A"), http.StatusBadRequest, "can't run itself")
	a.refused("POST", "/api/processes", sub(fc.ID, fb.ID, "C calls B"), http.StatusBadRequest, "can't run itself")
	// Calling the same flow twice, or one already called elsewhere, is fine.
	a.newProcess(fa.ID, sub(fa.ID, fc.ID, "A calls C"))
	a.newProcess(fa.ID, sub(fa.ID, fb.ID, "A calls B again"))

	// Pointing a subflow process elsewhere, or turning a process into one.
	a.refused("PATCH", fmt.Sprintf("/api/processes/%d", bc.ID), map[string]any{"settings": map[string]any{"flow": fa.ID}}, http.StatusBadRequest, "can't run itself")
	a.refused("PATCH", fmt.Sprintf("/api/processes/%d", bc.ID), map[string]any{"settings": map[string]any{"flow": 1 << 40}}, http.StatusBadRequest, "flow not found")
	agentInC := a.newProcess(fc.ID, map[string]any{"name": "Waiting", "kind": kindAgent, "settings": map[string]any{"flow": fa.ID}})
	a.refused("PATCH", fmt.Sprintf("/api/processes/%d", agentInC.ID), map[string]any{"kind": kindSubflow}, http.StatusBadRequest, "can't run itself")
	// A subflow process inside a flow process counts too.
	inner := a.newProcess(fc.ID, map[string]any{"name": "Inner", "kind": kindFlow})
	a.refused("POST", "/api/processes", map[string]any{"name": "Deep", "kind": kindSubflow, "parent": inner.ID, "settings": map[string]any{"flow": fa.ID}}, http.StatusBadRequest, "can't run itself")

	// Nothing refused was saved.
	for _, f := range []int64{fb.ID, fc.ID} {
		for _, p := range verProcesses(a, f) {
			if p.Kind == kindSubflow && p.Settings.Flow != nil && *p.Settings.Flow == fa.ID {
				t.Errorf("flow %d got %+v", f, p)
			}
		}
	}
}

// A subflow process brings the outcomes of the flow it runs onto its board
// as objects it generates, copying what they are made of, or reusing an
// object of the same name there.
func TestSubflowBringsOutcomes(t *testing.T) {
	a := newApp(t)
	callee := a.newFlow("Summarize")
	summary := a.newObject(callee.ID, "Summary")
	a.newState(summary.ID, "draft")
	a.newState(summary.ID, "final")
	text := verAttribute(a, summary.ID, "text")
	verPatch(a, fmt.Sprintf("/api/attributes/%d", text.ID), map[string]any{"unique": true})
	var meta object
	a.call("POST", "/api/objects", map[string]any{"name": "meta", "owner": summary.ID}, http.StatusCreated, &meta)
	verAttribute(a, meta.ID, "lang")
	counts := a.newObject(callee.ID, "Counts")
	verPatch(a, fmt.Sprintf("/api/objects/%d", counts.ID), map[string]any{"many": true, "min": 1, "max": 3})
	hidden := a.newObject(callee.ID, "Hidden")
	for _, o := range []int64{summary.ID, counts.ID} {
		verPatch(a, fmt.Sprintf("/api/objects/%d", o), map[string]any{"end": true})
	}
	_ = hidden

	other := a.newFlow("Other callee")
	elsewhere := a.newObject(other.ID, "Elsewhere")
	verPatch(a, fmt.Sprintf("/api/objects/%d", elsewhere.ID), map[string]any{"end": true})

	caller := a.newFlow("Caller")
	existing := a.newObject(caller.ID, "Counts")
	call := a.newProcess(caller.ID, map[string]any{"name": "Call", "kind": kindSubflow, "x": 100, "y": 50, "settings": map[string]any{"flow": callee.ID}})

	objs := verObjects(a, caller.ID)
	if n := len(verObjectNamed(t, objs, "Hidden")); n != 0 {
		t.Error("an object not at the callee's end came over")
	}
	if c := verObjectNamed(t, objs, "Counts"); len(c) != 1 || c[0].ID != existing.ID {
		t.Errorf("Counts on the caller = %+v, want the existing one reused", c)
	}
	sums := verObjectNamed(t, objs, "Summary")
	if len(sums) != 1 {
		t.Fatalf("Summary on the caller = %+v", sums)
	}
	s := sums[0]
	var states, attrs []string
	for _, st := range s.States {
		states = append(states, st.Name)
	}
	for _, at := range s.Attributes {
		attrs = append(attrs, fmt.Sprintf("%s unique=%v", at.Name, at.Unique))
	}
	if strings.Join(states, ",") != "draft,final" || strings.Join(attrs, ",") != "text unique=true" || s.End || s.Parent != nil || s.Y <= call.Y {
		t.Errorf("the copied Summary = %+v", s)
	}
	parts := []object{}
	for _, o := range objs {
		if o.Owner != nil && *o.Owner == s.ID {
			parts = append(parts, o)
		}
	}
	if len(parts) != 1 || parts[0].Name != "meta" || len(parts[0].Attributes) != 1 || parts[0].Attributes[0].Name != "lang" || parts[0].Relation != relationExhibition {
		t.Errorf("Summary's parts on the caller = %+v", parts)
	}
	var outputs []int64
	for _, l := range verLinks(a, caller.ID) {
		if l.Process == call.ID && l.Direction == directionOutput {
			outputs = append(outputs, l.Object)
		}
	}
	slices.Sort(outputs)
	if want := []int64{existing.ID, s.ID}; !slices.Equal(outputs, want) {
		t.Errorf("Call generates %v, want %v", outputs, want)
	}

	// Turning a process into a subflow process brings them too, reusing
	// what is there now.
	later := a.newProcess(caller.ID, map[string]any{"name": "Later", "kind": kindAgent, "settings": map[string]any{"flow": callee.ID}})
	verPatch(a, fmt.Sprintf("/api/processes/%d", later.ID), map[string]any{"kind": kindSubflow})
	if n := len(verObjectNamed(t, verObjects(a, caller.ID), "Summary")); n != 1 {
		t.Errorf("turning Later into a subflow process made %d Summaries", n)
	}
	outputs = nil
	for _, l := range verLinks(a, caller.ID) {
		if l.Process == later.ID && l.Direction == directionOutput {
			outputs = append(outputs, l.Object)
		}
	}
	if len(outputs) != 2 {
		t.Errorf("Later generates %v, want Summary and Counts", outputs)
	}

	// Picking another flow brings its outcomes.
	verPatch(a, fmt.Sprintf("/api/processes/%d", later.ID), map[string]any{"settings": map[string]any{"flow": other.ID}})
	es := verObjectNamed(t, verObjects(a, caller.ID), "Elsewhere")
	if len(es) != 1 {
		t.Fatalf("Elsewhere on the caller = %+v", es)
	}
	if !slices.ContainsFunc(verLinks(a, caller.ID), func(l link) bool {
		return l.Process == later.ID && l.Object == es[0].ID && l.Direction == directionOutput
	}) {
		t.Error("Later doesn't generate Elsewhere")
	}

	// Inside a flow process, outcomes come onto that board.
	inner := a.newProcess(caller.ID, map[string]any{"name": "Inner", "kind": kindFlow})
	a.newProcess(caller.ID, map[string]any{"name": "Deep call", "kind": kindSubflow, "parent": inner.ID, "settings": map[string]any{"flow": other.ID}})
	es = verObjectNamed(t, verObjects(a, caller.ID), "Elsewhere")
	if len(es) != 2 || !slices.ContainsFunc(es, func(o object) bool { return o.Parent != nil && *o.Parent == inner.ID }) {
		t.Errorf("Elsewhere on the caller = %+v, want one inside Inner too", es)
	}
}

// A subflow builds the board for the tests of running one: the callee fixes
// a ticket it takes open, leaving a patch at its end; the caller writes the
// ticket, open, and publishes the patch that comes out.
type verSubflow struct {
	callee, caller          flow
	fix, write, call, pub   process
	calleeTicket, callPatch object
}

func verBuildSubflow(a *app) verSubflow {
	a.t.Helper()
	var s verSubflow
	s.callee = a.newFlow("Fixing")
	s.calleeTicket = a.newObject(s.callee.ID, "Ticket")
	calleeOpen := a.newState(s.calleeTicket.ID, "open")
	a.newState(s.calleeTicket.ID, "closed")
	a.newObject(s.callee.ID, "Unrelated")
	s.fix = a.agent(s.callee.ID, "Fix", "Fix the ticket.")
	patch := a.newObject(s.callee.ID, "Patch")
	a.link(map[string]any{"process": s.fix.ID, "object": s.calleeTicket.ID, "direction": directionInput, "state": calleeOpen.ID})
	a.output(s.fix.ID, patch.ID)
	verPatch(a, fmt.Sprintf("/api/objects/%d", patch.ID), map[string]any{"end": true})

	s.caller = a.newFlow("Triaging")
	s.write = a.agent(s.caller.ID, "Write", "Write a ticket.")
	ticket := a.newObject(s.caller.ID, "Ticket")
	open := a.newState(ticket.ID, "open")
	a.link(map[string]any{"process": s.write.ID, "object": ticket.ID, "direction": directionOutput, "state": open.ID})
	s.call = a.newProcess(s.caller.ID, map[string]any{"name": "Call", "kind": kindSubflow, "settings": map[string]any{"flow": s.callee.ID}})
	a.link(map[string]any{"process": s.call.ID, "object": ticket.ID, "direction": directionInput, "state": open.ID})
	s.callPatch = verObjectNamed(a.t, verObjects(a, s.caller.ID), "Patch")[0]
	s.pub = a.agent(s.caller.ID, "Publish", "Publish the patch.")
	a.input(s.pub.ID, s.callPatch.ID)
	a.startAt(s.caller.ID, s.write.ID)
	a.worker.onAgent("Write", handsIn(data("login crashes")))
	a.worker.onAgent("Publish", handsIn(data("published")))
	return s
}

// What a subflow process takes goes to the objects of the same names there,
// in the states of the same names; the callee runs as a run of its own, on
// a version of its board saved when it changed; its outcomes come out.
func TestSubflowRunPassesObjectsByName(t *testing.T) {
	a := newApp(t)
	s := verBuildSubflow(a)
	a.worker.onAgent("Fix", handsIn(data("the fix")))

	rn := a.finish(a.startRun(s.caller.ID).ID)
	if rn.Status != runDone {
		t.Fatalf("run %s; steps %s", rn.Status, describeSteps(rn))
	}
	fixes := a.worker.runsOf("Fix")
	if len(fixes) != 1 || !strings.Contains(fixes[0].Prompt, `<input name="Ticket" state="open">`) || !strings.Contains(fixes[0].Prompt, "login crashes") {
		t.Fatalf("Fix ran %d times with %+v", len(fixes), fixes)
	}
	if strings.Contains(fixes[0].Prompt, "Unrelated") {
		t.Error("Fix got the object nothing gave")
	}
	ps := onlyStep(t, rn, "Publish")
	if len(ps.Inputs) != 1 || ps.Inputs[0].Name != "Patch" || !strings.Contains(ps.Inputs[0].Content, "the fix") {
		t.Errorf("Publish took %+v", ps.Inputs)
	}
	cs := onlyStep(t, rn, "Call")
	if cs.Status != stepDone || cs.ChildRun == nil || len(cs.Inputs) != 1 || cs.Inputs[0].State != "open" {
		t.Fatalf("Call step = %+v", cs)
	}
	if got := contentsOf(rn, "Patch"); len(got) != 1 || !strings.Contains(got[0], "the fix") {
		t.Errorf("the caller's run holds Patch %q", got)
	}

	child := a.getRun(*cs.ChildRun)
	if child.Flow != s.callee.ID || child.Parent == nil || *child.Parent != rn.ID || child.ProcessName != "Call" || child.Process != nil || child.Status != runDone {
		t.Errorf("callee's run = %+v", child.run)
	}
	l := verVersions(a, s.callee.ID)
	if len(l.Versions) != 1 || child.Version == nil || *child.Version != l.Versions[0].ID {
		t.Fatalf("callee versions %v, child run on %v", verIDs(l.Versions), child.Version)
	}

	// The callee unchanged runs on the same version; changed, on a new one.
	rn2 := a.finish(a.startRun(s.caller.ID).ID)
	if c2 := a.getRun(*onlyStep(t, rn2, "Call").ChildRun); *c2.Version != *child.Version {
		t.Errorf("unchanged callee ran on version %d, not %d", *c2.Version, *child.Version)
	}
	verPatch(a, fmt.Sprintf("/api/processes/%d", s.fix.ID), map[string]any{"prompt": "Fix it well."})
	rn3 := a.finish(a.startRun(s.caller.ID).ID)
	c3 := a.getRun(*onlyStep(t, rn3, "Call").ChildRun)
	if *c3.Version == *child.Version || len(verVersions(a, s.callee.ID).Versions) != 2 {
		t.Errorf("changed callee ran on version %d; versions %v", *c3.Version, verIDs(verVersions(a, s.callee.ID).Versions))
	}
	if !strings.Contains(a.worker.runsOf("Fix")[2].Prompt, "Fix it well.") {
		t.Error("the callee's change didn't reach its run")
	}
}

// A callee taking the ticket only once closed gets nothing from a caller
// giving it open, so nothing comes out, and what takes the outcome never runs.
func TestSubflowStateNamesMustMatch(t *testing.T) {
	a := newApp(t)
	s := verBuildSubflow(a)
	// The callee's Fix now takes the ticket closed.
	for _, l := range verLinks(a, s.callee.ID) {
		if l.Process == s.fix.ID && l.Direction == directionInput {
			a.call("DELETE", fmt.Sprintf("/api/links/%d", l.ID), nil, http.StatusNoContent, nil)
		}
	}
	var closed int64
	for _, st := range verObjectNamed(t, verObjects(a, s.callee.ID), "Ticket")[0].States {
		if st.Name == "closed" {
			closed = st.ID
		}
	}
	a.link(map[string]any{"process": s.fix.ID, "object": s.calleeTicket.ID, "direction": directionInput, "state": closed})
	a.worker.onAgent("Fix", handsIn(data("the fix")))

	rn := a.finish(a.startRun(s.caller.ID).ID)
	if rn.Status != runDone {
		t.Fatalf("run %s; steps %s", rn.Status, describeSteps(rn))
	}
	if n := len(a.worker.runsOf("Fix")); n != 0 {
		t.Errorf("Fix ran %d times on an open ticket", n)
	}
	if n := len(stepsOf(rn, "Publish")); n != 0 {
		t.Errorf("Publish ran %d times with no patch out", n)
	}
	if cs := onlyStep(t, rn, "Call"); cs.Status != stepDone || len(contentsOf(rn, "Patch")) != 0 {
		t.Errorf("Call = %s, Patch held %q", cs.Status, contentsOf(rn, "Patch"))
	}
}

// What goes wrong in or around the flow a subflow process runs fails the
// caller's branch, saying why.
func TestSubflowFailures(t *testing.T) {
	t.Run("callee fails", func(t *testing.T) {
		a := newApp(t)
		s := verBuildSubflow(a)
		a.worker.onAgent("Fix", func(context.Context, agentRun) runResult { return runResult{Error: "can't fix"} })
		rn := a.finish(a.startRun(s.caller.ID).ID)
		if rn.Status != runFailed {
			t.Fatalf("run %s; steps %s", rn.Status, describeSteps(rn))
		}
		cs := onlyStep(t, rn, "Call")
		if cs.Status != stepError || cs.ChildRun == nil || cs.Error != fmt.Sprintf("Run #%d failed.", *cs.ChildRun) {
			t.Errorf("Call step = %s %q", cs.Status, cs.Error)
		}
		if c := a.getRun(*cs.ChildRun); c.Status != runFailed || onlyStep(t, c, "Fix").Error != "can't fix" {
			t.Errorf("callee's run = %s, steps %s", c.Status, describeSteps(c))
		}
		if len(stepsOf(rn, "Publish")) != 0 {
			t.Error("Publish ran after the callee failed")
		}
	})
	t.Run("no flow picked", func(t *testing.T) {
		a := newApp(t)
		f := a.newFlow("Unset")
		call := a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindSubflow})
		a.startAt(f.ID, call.ID)
		rn := a.finish(a.startRun(f.ID).ID)
		if cs := onlyStep(t, rn, "Call"); rn.Status != runFailed || cs.Status != stepError || !strings.Contains(cs.Error, "runs no flow") {
			t.Errorf("run %s, Call %s %q", rn.Status, cs.Status, cs.Error)
		}
	})
	t.Run("callee deleted", func(t *testing.T) {
		a := newApp(t)
		s := verBuildSubflow(a)
		a.call("DELETE", fmt.Sprintf("/api/flows/%d", s.callee.ID), nil, http.StatusNoContent, nil)
		rn := a.finish(a.startRun(s.caller.ID).ID)
		if cs := onlyStep(t, rn, "Call"); rn.Status != runFailed || cs.Status != stepError || cs.Error != "The flow it runs is gone." {
			t.Errorf("run %s, Call %s %q", rn.Status, cs.Status, cs.Error)
		}
	})
	t.Run("outcome not generated", func(t *testing.T) {
		a := newApp(t)
		s := verBuildSubflow(a)
		// Fix generates something else than the patch at the end.
		for _, l := range verLinks(a, s.callee.ID) {
			if l.Process == s.fix.ID && l.Direction == directionOutput {
				a.call("DELETE", fmt.Sprintf("/api/links/%d", l.ID), nil, http.StatusNoContent, nil)
			}
		}
		a.output(s.fix.ID, a.newObject(s.callee.ID, "Notes").ID)
		a.worker.onAgent("Fix", handsIn(data("notes only")))
		rn := a.finish(a.startRun(s.caller.ID).ID)
		if rn.Status != runDone || onlyStep(t, rn, "Call").Status != stepDone {
			t.Fatalf("run %s; steps %s", rn.Status, describeSteps(rn))
		}
		if len(stepsOf(rn, "Publish")) != 0 || len(contentsOf(rn, "Patch")) != 0 {
			t.Errorf("a patch came out that the callee never made: %q", contentsOf(rn, "Patch"))
		}
	})
}
