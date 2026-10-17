package tests

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// No two objects a process takes share a name, as a script finds them as
// files named after them, nor two it generates; one it takes and another it
// generates may. Names are compared as file names, so Jira Ticket and
// Jira_Ticket are the same.
func TestProcessTellsItsObjectsApartByName(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Names")
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "cat $INPUTS/*"})
	ticket := a.newObject(f.ID, "Jira Ticket")
	underscored := a.newObject(f.ID, "Jira_Ticket")
	spaced := a.newObject(f.ID, "Jira  Ticket")

	a.input(fetch.ID, ticket.ID)
	a.refused("POST", "/api/links", map[string]any{"process": fetch.ID, "object": underscored.ID, "direction": directionInput},
		http.StatusBadRequest, `"Fetch" already takes an object named "Jira Ticket"`)
	// The ticket coming in and the updated one going out.
	a.output(fetch.ID, underscored.ID)
	a.refused("POST", "/api/links", map[string]any{"process": fetch.ID, "object": spaced.ID, "direction": directionOutput},
		http.StatusBadRequest, `"Fetch" already generates an object named "Jira_Ticket"`)

	// Taking one object in two states is taking one object.
	open, closed := a.newState(ticket.ID, "open"), a.newState(ticket.ID, "closed")
	a.link(map[string]any{"process": fetch.ID, "object": ticket.ID, "direction": directionInput, "state": open.ID, "condition": true})
	a.link(map[string]any{"process": fetch.ID, "object": ticket.ID, "direction": directionInput, "state": closed.ID, "condition": true})

	// Renaming an object can't make it share a name with another the
	// process takes; renaming it to its own name, spelled otherwise, can.
	cursor := a.newObject(f.ID, "Cursor")
	a.input(fetch.ID, cursor.ID)
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", cursor.ID), map[string]any{"name": "Jira Ticket."},
		http.StatusBadRequest, `"Fetch" already takes an object named "Jira Ticket"`)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", cursor.ID), map[string]any{"name": "Pull request cursor"}, http.StatusOK, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"name": "Jira_Ticket"}, http.StatusOK, nil)

	// Nor can renaming a struct, whose copies follow its name.
	var s flow
	a.call("POST", "/api/flows", map[string]any{"name": "Issue", "struct": true}, http.StatusCreated, &s)
	var cp object
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f.ID, "x": 300, "y": 200}, http.StatusCreated, &cp)
	a.input(fetch.ID, cp.ID)
	a.refused("PATCH", fmt.Sprintf("/api/flows/%d", s.ID), map[string]any{"name": "Pull request cursor"},
		http.StatusBadRequest, `"Fetch" already takes an object named "Pull request cursor"`)
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", s.ID), map[string]any{"name": "Bug"}, http.StatusOK, nil)
}

// A process taking an object both plainly and when it enters a state takes
// it once, so its step doesn't see two objects of one name.
func TestProcessTakesAnObjectOnceWhateverLinksItTakesItBy(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Once")
	file := a.newProcess(f.ID, map[string]any{"name": "File", "kind": kindScript, "script": "echo"})
	fix := a.newProcess(f.ID, map[string]any{"name": "Fix", "kind": kindScript, "script": "cat $INPUTS/Ticket"})
	ticket := a.newObject(f.ID, "Ticket")
	bug := a.newState(ticket.ID, "bug")
	a.link(map[string]any{"process": file.ID, "object": ticket.ID, "direction": directionOutput, "state": bug.ID})
	a.input(fix.ID, ticket.ID)
	a.link(map[string]any{"process": fix.ID, "object": ticket.ID, "direction": directionInput, "state": bug.ID})
	a.output(fix.ID, a.newObject(f.ID, "Patch").ID)
	a.startAt(f.ID, file.ID)
	a.worker.onScript("File", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"data": "crash"}`}
	})
	var inputs []scriptInput
	a.worker.onScript("Fix", func(_ context.Context, run scriptRun) runResult {
		inputs = run.Inputs
		return runResult{OK: true, Output: `{"data": "patched"}`}
	})

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone || len(inputs) != 1 {
		t.Errorf("run %s with Fix given %+v, want done with Ticket once; steps %s", d.Status, inputs, describeSteps(d))
	}
}
