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

// A shared struct's Ticket with an id telling records apart, a title and a
// status, in states open, created and review, copied onto a new flow.
func pickTickets(a *app, flow string) (structFlow, flow, modObject) {
	a.t.Helper()
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Ticket", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "title"}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "status"}, http.StatusCreated, nil)
	for _, st := range []string{"open", "created", "review"} {
		a.newState(root.ID, st)
	}
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	f := a.newFlow(flow)
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	return s, f, modNamed(a.t, modObjects(a, f.ID), "Ticket")
}

func pickState(t *testing.T, o modObject, name string) int64 {
	t.Helper()
	for _, st := range o.States {
		if st.Name == name {
			return st.ID
		}
	}
	t.Fatalf("%s has no state %s", o.Name, name)
	return 0
}

// The ticket a script was given, which must be one record, not a list.
func pickGiven(run scriptRun) (map[string]string, error) {
	for _, in := range run.Inputs {
		if in.Name == "Ticket" {
			var fields map[string]string
			if err := json.Unmarshal([]byte(in.Content), &fields); err != nil {
				return nil, fmt.Errorf("given Ticket %s, not one record: %v", in.Content, err)
			}
			return fields, nil
		}
	}
	return nil, fmt.Errorf("given no Ticket but %+v", run.Inputs)
}

func pickAdd(a *app, s int64, id, state string) int64 {
	a.t.Helper()
	var r sharedRecord
	a.call("POST", fmt.Sprintf("/api/structs/%d/records", s),
		map[string]any{"content": map[string]string{"id": id, "title": "Ticket " + id, "status": ""}, "state": state}, http.StatusCreated, &r)
	return r.ID
}

// The Ticket a flow picks is the one record each run works on: the oldest
// open one no other run holds, the same for every process linked to it, a
// process taking nothing else among them. A run finding none free isn't
// started. The process changing it writes that record, only the fields it
// changed, moving it on; one someone else moved on meanwhile fails the
// step, and a re-run takes the record again as it is then. A process
// generating it without taking it is an error on the board.
func TestPickedRecords(t *testing.T) {
	a := newApp(t)
	s, f, ticket := pickTickets(a, "Work tickets")
	note := a.newObject(f.ID, "Note")
	checked := a.newObject(f.ID, "Checked")

	pick := func(object int64, p any) string {
		t.Helper()
		status, reply := a.do("PUT", fmt.Sprintf("/api/objects/%d/pick", object), map[string]any{"pick": p})
		if status != http.StatusNoContent {
			return strings.TrimSpace(string(reply))
		}
		return ""
	}
	if msg := pick(note.ID, map[string]any{"state": "open"}); !strings.Contains(msg, "only a shared struct's object") {
		t.Errorf("picking on Note = %q, want it refused", msg)
	}
	if msg := pick(ticket.ID, map[string]any{"state": "closed"}); !strings.Contains(msg, `no state "closed"`) {
		t.Errorf("picking a state Ticket lacks = %q, want it refused", msg)
	}
	if msg := pick(ticket.ID, map[string]any{"state": "open", "oldest": true}); msg != "" {
		t.Fatalf("picking the oldest open Ticket: %s", msg)
	}

	read := a.newProcess(f.ID, map[string]any{"name": "Read", "kind": kindScript, "script": "read"})
	a.link(map[string]any{"process": read.ID, "object": ticket.ID, "direction": directionInput, "role": "instrument"})
	a.output(read.ID, note.ID)
	check := a.newProcess(f.ID, map[string]any{"name": "Check", "kind": kindScript, "script": "check"})
	a.link(map[string]any{"process": check.ID, "object": ticket.ID, "direction": directionInput, "role": "instrument"})
	a.output(check.ID, checked.ID)
	update := a.newProcess(f.ID, map[string]any{"name": "Update", "kind": kindScript, "script": "update"})
	a.input(update.ID, note.ID)
	a.input(update.ID, ticket.ID)
	a.link(map[string]any{"process": update.ID, "object": ticket.ID, "direction": directionOutput, "state": pickState(t, ticket, "created")})
	a.startAt(f.ID, read.ID)

	first, second := pickAdd(a, s.ID, "A-1", "open"), pickAdd(a, s.ID, "A-2", "open")
	pickAdd(a, s.ID, "A-3", "created")

	var mu sync.Mutex
	seen := map[string][]string{}
	hold := map[string]chan struct{}{"A-1": make(chan struct{}), "A-2": make(chan struct{})}
	given := func(process string) func(context.Context, scriptRun) runResult {
		return func(ctx context.Context, run scriptRun) runResult {
			fields, err := pickGiven(run)
			if err != nil {
				return runResult{Error: err.Error()}
			}
			mu.Lock()
			seen[process] = append(seen[process], fields["id"])
			wait := hold[fields["id"]]
			mu.Unlock()
			if process == "Update" {
				if wait != nil {
					select {
					case <-wait:
					case <-ctx.Done():
					}
				}
				fields["status"] = "updated by " + process
				out, _ := json.Marshal(fields)
				return runResult{OK: true, Output: string(out)}
			}
			out, _ := json.Marshal(data(process + " " + fields["id"]))
			return runResult{OK: true, Output: string(out)}
		}
	}
	for _, p := range []string{"Read", "Check", "Update"} {
		a.worker.onScript(p, given(p))
	}
	seenBy := func(process string) []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen[process]...)
	}

	// The first run takes the oldest open ticket, and Check, taking only
	// it, runs on it too.
	rn1 := a.startRun(f.ID)
	a.waitStep(rn1.ID, "Update", stepActive)
	if got := seenBy("Read"); len(got) != 1 || got[0] != "A-1" {
		t.Fatalf("Read was given %v, want A-1", got)
	}
	a.waitStep(rn1.ID, "Check", stepDone)
	if got := seenBy("Check"); len(got) != 1 || got[0] != "A-1" {
		t.Fatalf("Check was given %v, want A-1", got)
	}
	// A run starting meanwhile takes the next, and a third finds none free.
	rn2 := a.startRun(f.ID)
	a.waitStep(rn2.ID, "Update", stepActive)
	if got := seenBy("Read"); len(got) != 2 || got[1] != "A-2" {
		t.Fatalf("Read was given %v, want A-1 then A-2", got)
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, `every "Ticket" record in open has a run working on it`)

	// A person retitles A-1 while its run goes; Update changes its status
	// only, so the title stays, and no new record is added.
	a.call("PUT", fmt.Sprintf("/api/structs/%d/records/%d", s.ID, first),
		map[string]any{"content": map[string]string{"id": "A-1", "title": "Retitled", "status": ""}, "state": "open"}, http.StatusOK, nil)
	close(hold["A-1"])
	if d := a.finish(rn1.ID); d.Status != runDone {
		t.Fatalf("first run ended %s: %s", d.Status, describeSteps(d))
	}
	records := sharedRecords(a, s.ID)
	if len(records) != 3 {
		t.Fatalf("records %v, want the three there were", records)
	}
	var a1 map[string]string
	json.Unmarshal(records["A-1"].Content, &a1)
	if records["A-1"].State != "created" || a1["title"] != "Retitled" || a1["status"] != "updated by Update" {
		t.Errorf("A-1 = %s in %s, want it created, retitled and updated", records["A-1"].Content, records["A-1"].State)
	}

	// A person moves A-2 on meanwhile: Update fails rather than moving it
	// back, and writes nothing.
	a.call("PUT", fmt.Sprintf("/api/structs/%d/records/%d", s.ID, second),
		map[string]any{"content": map[string]string{"id": "A-2", "title": "Ticket A-2", "status": ""}, "state": "review"}, http.StatusOK, nil)
	close(hold["A-2"])
	d2 := a.finish(rn2.ID)
	failed := onlyStep(t, d2, "Update")
	if d2.Status != runFailed || !strings.Contains(failed.Error, "moved from open to review by someone else") {
		t.Fatalf("second run ended %s, Update %s %q; want it failed on the move", d2.Status, failed.Status, failed.Error)
	}
	if r := sharedRecords(a, s.ID)["A-2"]; r.State != "review" || strings.Contains(string(r.Content), "updated") {
		t.Errorf("A-2 = %s in %s, want it as the person left it", r.Content, r.State)
	}

	// Re-run from Update, it takes A-2 as it is now and moves it on.
	var re run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", rn2.ID, failed.ID), map[string]any{}, http.StatusCreated, &re)
	if d := a.finish(re.ID); d.Status != runDone {
		t.Fatalf("re-run ended %s: %s", d.Status, describeSteps(d))
	}
	if r := sharedRecords(a, s.ID)["A-2"]; r.State != "created" {
		t.Errorf("A-2 after the re-run is in %s, want created", r.State)
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, `no "Ticket" record in open is there to work on`)

	var opl struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil, http.StatusOK, &opl)
	if !strings.Contains(opl.Text, `Each run works on the earliest stored record of "Ticket" in open`) {
		t.Errorf("OPL doesn't say which record each run works on:\n%s", opl.Text)
	}
	var setup struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", f.ID), nil, http.StatusOK, &setup)
	if !strings.Contains(setup.Text, "pick:") || !strings.Contains(setup.Text, "oldest: true") {
		t.Errorf("the setup file leaves the pick out:\n%s", setup.Text)
	}

	// Generating the Ticket without taking it would write over the record
	// blind.
	file := a.newProcess(f.ID, map[string]any{"name": "File", "kind": kindScript, "script": "file"})
	a.input(file.ID, note.ID)
	a.output(file.ID, ticket.ID)
	if ps := prbGet(a, f.ID).Problems; !prbHas(ps, "error", "pick-change", file.ID, "without taking it") {
		t.Errorf("problems %s, want File generating Ticket without taking it an error", prbDescribe(ps))
	}
}

// A person's review deciding the Ticket's state moves the record the run
// took, and a run started on a record takes that one, whatever the pick
// selects; the Records page offers the flow by its pick.
func TestPickedRecordReviewed(t *testing.T) {
	a := newApp(t)
	s, f, ticket := pickTickets(a, "Review tickets")
	a.call("PUT", fmt.Sprintf("/api/objects/%d/pick", ticket.ID), map[string]any{"pick": map[string]any{"state": "open"}}, http.StatusNoContent, nil)
	approve := a.newProcess(f.ID, map[string]any{"name": "Approve", "kind": kindHuman})
	a.input(approve.ID, ticket.ID)
	a.link(map[string]any{"process": approve.ID, "object": ticket.ID, "direction": directionOutput,
		"states": []int64{pickState(t, ticket, "created"), pickState(t, ticket, "review")}})
	a.startAt(f.ID, approve.ID)
	pickAdd(a, s.ID, "B-1", "open")
	done := pickAdd(a, s.ID, "B-2", "created")

	rn := a.startRun(f.ID)
	review := a.waitStep(rn.ID, "Approve", stepWaiting)
	if len(review.Inputs) != 1 || !strings.Contains(review.Inputs[0].Content, "B-1") {
		t.Fatalf("Approve was given %+v, want B-1", review.Inputs)
	}
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": pickState(t, ticket, "review")})
	a.finish(rn.ID)
	if r := sharedRecords(a, s.ID)["B-1"]; r.State != "review" {
		t.Errorf("B-1 after the review is in %s, want review", r.State)
	}

	var offered struct {
		Flows []struct {
			ID  int64             `json:"id"`
			Off map[string]string `json:"off"`
		} `json:"flows"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records/flows?records=%d", s.ID, done), nil, http.StatusOK, &offered)
	if len(offered.Flows) != 1 || offered.Flows[0].ID != f.ID || offered.Flows[0].Off[fmt.Sprint(done)] == "" {
		t.Fatalf("flows offered %+v, want %q, saying its pick wouldn't select B-2", offered.Flows, f.Name)
	}
	var started []run
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": f.ID, "records": []int64{done}}, http.StatusCreated, &started)
	on := a.waitStep(started[0].ID, "Approve", stepWaiting)
	if len(on.Inputs) != 1 || !strings.Contains(on.Inputs[0].Content, "B-2") {
		t.Fatalf("a run on B-2 gave Approve %+v", on.Inputs)
	}
}

// Runs of two flows picking the same open tickets, started all at once,
// each take a ticket of their own, and once every ticket is held the next
// run isn't started.
func TestPickedRecordsRace(t *testing.T) {
	a := newApp(t)
	s, f1, t1 := pickTickets(a, "Work tickets")
	f2 := a.newFlow("Work tickets too")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f2.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	t2 := modNamed(t, modObjects(a, f2.ID), "Ticket")
	release := make(chan struct{})
	for _, fo := range []struct {
		flow   int64
		ticket modObject
	}{{f1.ID, t1}, {f2.ID, t2}} {
		a.call("PUT", fmt.Sprintf("/api/objects/%d/pick", fo.ticket.ID), map[string]any{"pick": map[string]any{"state": "open", "oldest": true}}, http.StatusNoContent, nil)
		work := a.newProcess(fo.flow, map[string]any{"name": "Work", "kind": kindScript, "script": "work"})
		a.input(work.ID, fo.ticket.ID)
		a.link(map[string]any{"process": work.ID, "object": fo.ticket.ID, "direction": directionOutput, "state": pickState(t, fo.ticket, "created")})
		a.startAt(fo.flow, work.ID)
	}
	// As many as the webapp runs steps at once, so each run's step goes.
	const runs = 3
	for i := range runs {
		pickAdd(a, s.ID, fmt.Sprintf("C-%d", i), "open")
	}
	var mu sync.Mutex
	var worked []string
	a.worker.onScript("Work", func(ctx context.Context, run scriptRun) runResult {
		fields, err := pickGiven(run)
		if err != nil {
			return runResult{Error: err.Error()}
		}
		mu.Lock()
		worked = append(worked, fields["id"])
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
		out, _ := json.Marshal(fields)
		return runResult{OK: true, Output: string(out)}
	})

	var wg sync.WaitGroup
	started := make(chan int64, runs)
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := f1.ID
			if i%2 == 1 {
				f = f2.ID
			}
			status, reply := a.do("POST", fmt.Sprintf("/api/runs?flow=%d", f), nil)
			if status != http.StatusCreated {
				t.Errorf("starting run %d = %d %s", i, status, reply)
				return
			}
			var rn run
			json.Unmarshal(reply, &rn)
			started <- rn.ID
		}()
	}
	wg.Wait()
	close(started)
	var ids []int64
	for id := range started {
		ids = append(ids, id)
		a.waitStep(id, "Work", stepActive)
	}
	mu.Lock()
	taken := map[string]int{}
	for _, id := range worked {
		taken[id]++
	}
	mu.Unlock()
	if len(taken) != runs {
		t.Errorf("runs worked on %v, want each of the %d tickets once", taken, runs)
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f1.ID), nil, http.StatusConflict, "has a run working on it")
	close(release)
	for _, id := range ids {
		if d := a.finish(id); d.Status != runDone {
			t.Errorf("run %d ended %s: %s", id, d.Status, describeSteps(d))
		}
	}
	for id, r := range sharedRecords(a, s.ID) {
		if r.State != "created" {
			t.Errorf("%s is in %s, want created", id, r.State)
		}
	}
}
