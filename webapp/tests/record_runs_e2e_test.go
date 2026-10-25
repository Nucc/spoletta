package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// A person starts a flow querying a shared struct's records on the record
// they pick, from the struct's Records page: the run's query reads that
// record alone, whatever it selects, and moves it on as it would any it
// queried, and a re-run of it reads the record again. Before starting it,
// they're told which records the query wouldn't select and which have a
// run going on them already.
func TestRunsOnRecords(t *testing.T) {
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Ticket", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "title"}, http.StatusCreated, nil)
	a.newState(root.ID, "open")
	a.newState(root.ID, "done")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)

	// Prepare runs first, then Implement queries the oldest open ticket and
	// leaves it done.
	f := a.newFlow("Implement tickets")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	ticket := modNamed(t, modObjects(a, f.ID), "Ticket")
	plan := a.newObject(f.ID, "Plan")
	prepare := a.newProcess(f.ID, map[string]any{"name": "Prepare", "kind": kindScript, "script": "prepare"})
	a.output(prepare.ID, plan.ID)
	implement := a.newProcess(f.ID, map[string]any{"name": "Implement", "kind": kindScript, "script": "implement"})
	a.input(implement.ID, plan.ID)
	a.link(map[string]any{"process": implement.ID, "object": ticket.ID, "direction": directionInput,
		"query": linkQuery{State: "open", Limit: 1, Oldest: true}})
	var done int64
	for _, st := range ticket.States {
		if st.Name == "done" {
			done = st.ID
		}
	}
	a.link(map[string]any{"process": implement.ID, "object": ticket.ID, "direction": directionOutput, "state": done})
	a.startAt(f.ID, prepare.ID)
	// A flow not querying the tickets isn't one to start on them.
	other := a.newFlow("Unrelated")

	add := func(id, state string) int64 {
		t.Helper()
		var r sharedRecord
		a.call("POST", fmt.Sprintf("/api/structs/%d/records", s.ID),
			map[string]any{"content": map[string]string{"id": id, "title": "Ticket " + id}, "state": state}, http.StatusCreated, &r)
		return r.ID
	}
	first, second, closed := add("A-1", "open"), add("A-2", "open"), add("A-3", "done")

	var mu sync.Mutex
	var given []string
	hold := map[string]chan struct{}{}
	a.worker.onScript("Prepare", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"data": "plan"}`} })
	a.worker.onScript("Implement", func(ctx context.Context, run scriptRun) runResult {
		var records []json.RawMessage
		for _, in := range run.Inputs {
			if in.Name == "Ticket" {
				json.Unmarshal([]byte(in.Content), &records)
			}
		}
		if len(records) != 1 {
			return runResult{Error: fmt.Sprintf("given %+v", run.Inputs)}
		}
		var fields struct {
			ID string `json:"id"`
		}
		json.Unmarshal(records[0], &fields)
		mu.Lock()
		given = append(given, fields.ID)
		wait := hold[fields.ID]
		mu.Unlock()
		if wait != nil {
			select {
			case <-wait:
			case <-ctx.Done():
			}
		}
		return runResult{OK: true, Output: string(records[0])}
	})

	type flowsAnswer struct {
		Flows []struct {
			ID   int64             `json:"id"`
			Name string            `json:"name"`
			Off  map[string]string `json:"off"`
		} `json:"flows"`
		Going map[string][]struct {
			Run  int64 `json:"run"`
			Flow int64 `json:"flow"`
		} `json:"going"`
	}
	flows := func(records ...int64) flowsAnswer {
		t.Helper()
		ids := make([]string, len(records))
		for i, r := range records {
			ids[i] = fmt.Sprint(r)
		}
		var fa flowsAnswer
		a.call("GET", fmt.Sprintf("/api/structs/%d/records/flows?records=%s", s.ID, strings.Join(ids, ",")), nil, http.StatusOK, &fa)
		return fa
	}
	start := func(records ...int64) []run {
		t.Helper()
		var rs []run
		a.call("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": f.ID, "records": records}, http.StatusCreated, &rs)
		if len(rs) != len(records) {
			t.Fatalf("started %d runs on %d records", len(rs), len(records))
		}
		return rs
	}
	stateOf := func(id string) string {
		t.Helper()
		return sharedRecords(a, s.ID)[id].State
	}
	lastGiven := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(given) == 0 {
			return ""
		}
		return given[len(given)-1]
	}

	// Only the flow querying the tickets is offered, and the done ticket is
	// one its query wouldn't select.
	fa := flows(second, closed)
	if len(fa.Flows) != 1 || fa.Flows[0].ID != f.ID {
		t.Fatalf("flows offered %+v, want only %q", fa.Flows, f.Name)
	}
	if off := fa.Flows[0].Off; off[fmt.Sprint(closed)] == "" || off[fmt.Sprint(second)] != "" || len(fa.Going) != 0 {
		t.Errorf("off the query %v, going %v; want the done ticket alone off it and nothing going", off, fa.Going)
	}

	// Started on the second open ticket, the run works on it rather than
	// the oldest, and leaves it done.
	rn := start(second)[0]
	if rn.Record == nil || *rn.Record != second {
		t.Errorf("the run was started on record %v, want %d", rn.Record, second)
	}
	d := a.finish(rn.ID)
	if d.Status != runDone || lastGiven() != "A-2" {
		t.Fatalf("the run on A-2 %s, given %s; steps %s", d.Status, lastGiven(), describeSteps(d))
	}
	// Its log says which record it was started on.
	logLineWith(t, a.runLogs(rn.ID, 0), fmt.Sprintf("by someone unknown, as nobody signs in, on record %d", second))
	if stateOf("A-2") != "done" || stateOf("A-1") != "open" {
		t.Errorf("A-1 is %s and A-2 %s, want open and done", stateOf("A-1"), stateOf("A-2"))
	}

	// One the query wouldn't select runs all the same.
	if d := a.finish(start(closed)[0].ID); d.Status != runDone || lastGiven() != "A-3" {
		t.Errorf("the run on the done A-3 %s, given %s", d.Status, lastGiven())
	}

	// A re-run from before the query reads its record again.
	var again run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", d.ID, onlyStep(t, d, "Prepare").ID), map[string]any{}, http.StatusCreated, &again)
	if d := a.finish(again.ID); d.Status != runDone || lastGiven() != "A-2" || again.Record == nil || *again.Record != second {
		t.Errorf("the re-run %s on record %v, given %s; want done on A-2", d.Status, again.Record, lastGiven())
	}

	// A run going on a ticket shows, and doesn't stop another starting.
	release := make(chan struct{})
	mu.Lock()
	hold["A-1"] = release
	mu.Unlock()
	busy := start(first)[0]
	a.waitStep(busy.ID, "Implement", "active")
	fa = flows(first)
	if g := fa.Going[fmt.Sprint(first)]; len(g) != 1 || g[0].Run != busy.ID || g[0].Flow != f.ID {
		t.Errorf("going on A-1 %+v, want run %d", g, busy.ID)
	}
	another := start(first)[0]
	if another.ID == busy.ID {
		t.Errorf("starting another on A-1 returned the run going")
	}
	close(release)
	for _, r := range []run{busy, another} {
		if d := a.finish(r.ID); d.Status != runDone {
			t.Errorf("run %d on A-1 %s; steps %s", r.ID, d.Status, describeSteps(d))
		}
	}

	// Records named twice get one run; what isn't the store's, or a flow
	// not querying it, is refused.
	var once []run
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": f.ID, "records": []int64{closed, first, closed}}, http.StatusCreated, &once)
	if len(once) != 2 || *once[0].Record != closed || *once[1].Record != first {
		t.Errorf("starting on A-3, A-1 and A-3 again started %+v", once)
	}
	for _, r := range once {
		a.finish(r.ID)
	}
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": other.ID, "records": []int64{first}}, http.StatusBadRequest, "doesn't query")
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": f.ID, "records": []int64{first + 1000}}, http.StatusNotFound, "no record")
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": f.ID, "records": []int64{}}, http.StatusBadRequest, "name the records")

	// Once its record is deleted, a re-run of a run on it fails rather than
	// reading another.
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/delete", s.ID), map[string]any{"ids": []int64{second}}, http.StatusOK, nil)
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", rn.ID, onlyStep(t, d, "Prepare").ID), map[string]any{}, http.StatusCreated, &again)
	if d := a.finish(again.ID); d.Status != runFailed {
		t.Errorf("a re-run on the deleted A-2 %s, want failed; steps %s", d.Status, describeSteps(d))
	}
}

// A shared struct with a unique id and the states given, for runs on its
// records.
func recordStruct(a *app, name string, states ...string) structFlow {
	a.t.Helper()
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": name, "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	for _, st := range states {
		a.newState(root.ID, st)
	}
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	return s
}

// Copies the struct onto the flow's top board, or the board of the flow
// process parent.
func recordCopy(a *app, s structFlow, f int64, parent *int64) modObject {
	a.t.Helper()
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f, "parent": parent, "x": 0, "y": 0}, http.StatusCreated, nil)
	return modNamed(a.t, modObjects(a, f), s.Name)
}

func recordAdd(a *app, s structFlow, id, state string) int64 {
	a.t.Helper()
	var r sharedRecord
	a.call("POST", fmt.Sprintf("/api/structs/%d/records", s.ID), map[string]any{"content": map[string]string{"id": id}, "state": state}, http.StatusCreated, &r)
	return r.ID
}

// The ids of the records a step was given of the object.
func recordsGiven(run scriptRun, object string) []string {
	var ids []string
	for _, in := range run.Inputs {
		if in.Name != object {
			continue
		}
		var records []struct {
			ID string `json:"id"`
		}
		json.Unmarshal([]byte(in.Content), &records)
		for _, r := range records {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// A query on a board inside a flow process reads the record the run was
// started on, as one on the top board does, while a query of another
// struct's records in the same run reads what it selects.
func TestRunsOnRecordsInsideBoards(t *testing.T) {
	a := newApp(t)
	tickets := recordStruct(a, "Ticket", "open", "done")
	customers := recordStruct(a, "Customer")
	f := a.newFlow("Fix tickets")
	work := nestedFlowProcess(a, f.ID, "Work", false)
	a.startAt(f.ID, work.ID)
	ticket := recordCopy(a, tickets, f.ID, &work.ID)
	customer := recordCopy(a, customers, f.ID, &work.ID)
	fix := a.newProcess(f.ID, map[string]any{"name": "Fix", "kind": kindScript, "script": "fix", "parent": work.ID})
	a.link(map[string]any{"process": fix.ID, "object": ticket.ID, "direction": directionInput, "query": linkQuery{State: "open", Limit: 1}})
	a.link(map[string]any{"process": fix.ID, "object": customer.ID, "direction": directionInput, "query": linkQuery{Limit: 5, Oldest: true}})
	nestedStartAt(a, work.ID, fix.ID)

	recordAdd(a, tickets, "T-1", "open")
	closed := recordAdd(a, tickets, "T-2", "done")
	recordAdd(a, customers, "C-1", "")
	recordAdd(a, customers, "C-2", "")

	var mu sync.Mutex
	var gotTickets, gotCustomers []string
	a.worker.onScript("Fix", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		gotTickets, gotCustomers = recordsGiven(run, "Ticket"), recordsGiven(run, "Customer")
		return runResult{OK: true, Output: `{"data": "fixed"}`}
	})

	// Offered for the tickets, where it queries them inside Work, and for
	// the customers, though neither is on its top board.
	var fa struct {
		Flows []struct {
			ID int64 `json:"id"`
		} `json:"flows"`
	}
	for _, s := range []structFlow{tickets, customers} {
		a.call("GET", fmt.Sprintf("/api/structs/%d/records/flows", s.ID), nil, http.StatusOK, &fa)
		if len(fa.Flows) != 1 || fa.Flows[0].ID != f.ID {
			t.Errorf("flows offered for %s %+v, want %d", s.Name, fa.Flows, f.ID)
		}
	}

	var rs []run
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/runs", tickets.ID), map[string]any{"flow": f.ID, "records": []int64{closed}}, http.StatusCreated, &rs)
	if d := a.finish(rs[0].ID); d.Status != runDone {
		t.Fatalf("the run on T-2 %s; steps %s", d.Status, describeSteps(d))
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(gotTickets, ",") != "T-2" || strings.Join(gotCustomers, ",") != "C-1,C-2" {
		t.Errorf("Fix was given tickets %v and customers %v, want T-2 and both customers", gotTickets, gotCustomers)
	}
}

// Only the flows a person may change are offered and started on records,
// and a struct they don't see has no records to start them on.
func TestRunsOnRecordsWhoMay(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)

	tickets := recordStruct(a, "Ticket", "open")
	f := a.newFlow("Implement tickets")
	ticket := recordCopy(a, tickets, f.ID, nil)
	implement := a.newProcess(f.ID, map[string]any{"name": "Implement", "kind": kindScript, "script": "implement"})
	a.link(map[string]any{"process": implement.ID, "object": ticket.ID, "direction": directionInput, "query": linkQuery{State: "open"}})
	a.startAt(f.ID, implement.ID)
	record := recordAdd(a, tickets, "T-1", "open")
	a.worker.onScript("Implement", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"data": "done"}`} })

	offered := func() []int64 {
		t.Helper()
		var fa struct {
			Flows []struct {
				ID int64 `json:"id"`
			} `json:"flows"`
		}
		a.call("GET", fmt.Sprintf("/api/structs/%d/records/flows?records=%d", tickets.ID, record), nil, http.StatusOK, &fa)
		var ids []int64
		for _, f := range fa.Flows {
			ids = append(ids, f.ID)
		}
		return ids
	}
	start := map[string]any{"flow": f.ID, "records": []int64{record}}
	runsPath := fmt.Sprintf("/api/structs/%d/records/runs", tickets.ID)

	// Neither is Bob's to see.
	usersSignIn(a, p, bob)
	a.refused("GET", fmt.Sprintf("/api/structs/%d/records/flows", tickets.ID), nil, http.StatusNotFound, "not found")
	a.refused("POST", runsPath, start, http.StatusNotFound, "not found")

	// He sees the struct, but the flow is still Ada's own.
	usersSignIn(a, p, usersAda)
	usersShare(a, tickets.ID)
	usersSignIn(a, p, bob)
	if got := offered(); len(got) != 0 {
		t.Errorf("bob is offered %v, none of them his", got)
	}
	a.refused("POST", runsPath, start, http.StatusNotFound, "not found")

	// Shared with everyone, the flow is his to start on the record too, and
	// the run is his.
	usersSignIn(a, p, usersAda)
	usersShare(a, f.ID)
	usersSignIn(a, p, bob)
	if got := offered(); len(got) != 1 || got[0] != f.ID {
		t.Errorf("bob is offered %v, want %d", got, f.ID)
	}
	var rs []run
	a.call("POST", runsPath, start, http.StatusCreated, &rs)
	if d := a.finish(rs[0].ID); d.Status != runDone || d.StartedBy != "Bob" {
		t.Errorf("bob's run on T-1 %s, started by %q; steps %s", d.Status, d.StartedBy, describeSteps(d))
	}
}
