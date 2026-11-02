package tests

// Where flows leave a struct's records, which a task board draws over its
// columns: each flow that may be started on the records says the states it
// leaves them in, as its processes taking them generate them, from the
// version it published if any, else from its board as it is; the board's
// own page holds those flows by struct.

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// A flow a board may start on a struct's records, as its page draws it.
type drawnFlow struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	States []string `json:"states"`
	Leaves []string `json:"leaves"`
}

// The flows as "name: takes -> leaves", by name, failing on leaves sent as
// null rather than a list.
func drawnLines(t *testing.T, fs []drawnFlow) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, f := range fs {
		if f.Leaves == nil {
			t.Errorf("%s leaves null, want a list", f.Name)
		}
		out[f.Name] = strings.Join(f.States, ", ") + " -> " + strings.Join(f.Leaves, ", ")
	}
	return out
}

// The id of the object's state named name.
func stateNamed(t *testing.T, o modObject, name string) int64 {
	t.Helper()
	for _, st := range o.States {
		if st.Name == name {
			return st.ID
		}
	}
	t.Fatalf("%s has no state %q", o.Name, name)
	return 0
}

// A process, named name, of kind, on a flow's copy of the struct, querying
// its records in state from.
func queryingProcess(a *app, f int64, s structFlow, name, kind, from string) (process, modObject) {
	a.t.Helper()
	bug := modNamed(a.t, modObjects(a, f), s.Name)
	fields := map[string]any{"name": name, "kind": kind}
	if kind == kindScript {
		fields["script"] = strings.ToLower(name)
	} else {
		fields["prompt"] = name + " it."
	}
	p := a.newProcess(f, fields)
	a.link(map[string]any{"process": p.ID, "object": bug.ID, "direction": directionInput, "query": linkQuery{State: from, Limit: 1}})
	return p, bug
}

func TestTaskBoardDrawsWhereFlowsLeaveRecords(t *testing.T) {
	a := newApp(t)
	s := taskStruct(a, "Bug", nil, "id", []string{"title"}, "new", "triaged", "fixed")
	triage := movingFlow(a, s, "Triage", "new", "triaged")
	movingFlow(a, s, "Urgent", "new", "triaged")

	// An agent deciding whether a triaged bug is fixed, or new again.
	review := a.newFlow("Review bugs")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": review.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	p, bug := queryingProcess(a, review.ID, s, "Review", kindAgent, "triaged")
	a.link(map[string]any{"process": p.ID, "object": bug.ID, "direction": directionOutput,
		"states": []int64{stateNamed(t, bug, "fixed"), stateNamed(t, bug, "new")}})
	a.startAt(review.ID, p.ID)

	// A script looking at fixed bugs generates them in no state, leaving
	// them where they were.
	watch := a.newFlow("Watch bugs")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": watch.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	p, bug = queryingProcess(a, watch.ID, s, "Watch", kindScript, "fixed")
	a.link(map[string]any{"process": p.ID, "object": bug.ID, "direction": directionOutput})
	a.startAt(watch.ID, p.ID)

	// Triage publishes; its board as it is then takes fixed bugs back to
	// new, which runs on records don't take until it publishes again.
	pubPublish(a, triage.ID, "Triage")
	p, bug = queryingProcess(a, triage.ID, s, "Reopen", kindScript, "fixed")
	a.link(map[string]any{"process": p.ID, "object": bug.ID, "direction": directionOutput, "state": stateNamed(t, bug, "new")})

	want := map[string]string{
		"Review bugs": "triaged -> new, fixed",
		"Triage bugs": "new -> triaged",
		"Urgent bugs": "new -> triaged",
		"Watch bugs":  "fixed -> ",
	}
	var offered struct {
		Flows []drawnFlow `json:"flows"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records/flows", s.ID), nil, http.StatusOK, &offered)
	got := drawnLines(t, offered.Flows)
	for name, line := range want {
		if got[name] != line {
			t.Errorf("records' flows: %s is %q, want %q", name, got[name], line)
		}
	}
	if len(got) != len(want) {
		t.Errorf("records' flows are %q, want %q", got, want)
	}

	// The board's own page holds them by struct; its list of boards doesn't.
	var b struct {
		ID    int64                  `json:"id"`
		Flows map[string][]drawnFlow `json:"flows"`
	}
	a.call("POST", "/api/taskboards", map[string]any{"name": "Bugs"}, http.StatusCreated, &b)
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs", "columns": []any{
		map[string]any{"name": "New", "struct": s.ID, "query": "state:new"},
		map[string]any{"name": "Triaged", "struct": s.ID, "query": "state:triaged"},
		map[string]any{"name": "Fixed", "struct": s.ID, "query": "state:fixed", "maxRuns": 0},
	}}, http.StatusOK, nil)
	a.call("GET", fmt.Sprintf("/api/taskboards/%d", b.ID), nil, http.StatusOK, &b)
	if len(b.Flows) != 1 {
		t.Fatalf("the board holds flows of %d structs, want Bug's alone", len(b.Flows))
	}
	got = drawnLines(t, b.Flows[strconv.FormatInt(s.ID, 10)])
	for name, line := range want {
		if got[name] != line {
			t.Errorf("the board: %s is %q, want %q", name, got[name], line)
		}
	}
	var listed []struct {
		ID    int64          `json:"id"`
		Flows map[string]any `json:"flows"`
	}
	a.call("GET", "/api/taskboards", nil, http.StatusOK, &listed)
	for _, l := range listed {
		if l.ID == b.ID && l.Flows != nil {
			t.Errorf("the list of boards holds its flows: %v", l.Flows)
		}
	}
}
