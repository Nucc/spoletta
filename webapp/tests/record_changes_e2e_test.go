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

// A change of a shared record, as its history lists it.
type recordChange struct {
	Kind    string  `json:"kind"`
	State   string  `json:"state"`
	From    *string `json:"from"`
	Changed []struct {
		Name   string          `json:"name"`
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
	} `json:"changed"`
	Content       json.RawMessage `json:"content"`
	Run           *int64          `json:"run"`
	Flow          *int64          `json:"flow"`
	FlowName      string          `json:"flowName"`
	Hidden        bool            `json:"hidden"`
	Inner         *int64          `json:"inner"`
	InnerFlow     *int64          `json:"innerFlow"`
	InnerFlowName string          `json:"innerFlowName"`
	Process       string          `json:"process"`
	ProcessKind   string          `json:"processKind"`
	By            string          `json:"by"`
}

// A record's changes, the latest first.
func recordChangesOf(a *app, s, record int64) []recordChange {
	a.t.Helper()
	var h struct {
		Record  sharedRecord   `json:"record"`
		Fields  []string       `json:"fields"`
		Changes []recordChange `json:"changes"`
		More    bool           `json:"more"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records/%d/changes", s, record), nil, http.StatusOK, &h)
	if h.Record.ID != record {
		a.t.Fatalf("the history of record %d is of record %d", record, h.Record.ID)
	}
	return h.Changes
}

// A change said short: its kind and state, the state it came from, what
// made it and the fields it changed, as name:before>after.
func describeChange(c recordChange) string {
	from := "-"
	if c.From != nil {
		from = *c.From
	}
	who := "by hand"
	if c.Run != nil {
		who = fmt.Sprintf("%s:%s run", c.FlowName, c.Process)
		if c.Inner != nil {
			who += fmt.Sprintf(" in %s", c.InnerFlowName)
		}
	}
	var fields []string
	for _, f := range c.Changed {
		b, a := "-", "-"
		if f.Before != nil {
			b = string(f.Before)
		}
		if f.After != nil {
			a = string(f.After)
		}
		fields = append(fields, f.Name+":"+b+">"+a)
	}
	return fmt.Sprintf("%s %s<%s %s [%s]", c.Kind, c.State, from, who, strings.Join(fields, " "))
}

func describeChanges(cs []recordChange) string {
	var out []string
	for _, c := range cs {
		out = append(out, describeChange(c))
	}
	return strings.Join(out, " | ")
}

// A record's history tells each change of it, the latest first: added by a
// fetching flow in the state its rule gives it, as one change, refreshed
// with a field changed, moved on by another flow's run, corrected by hand.
// Fetching the same again, or a step writing it back as it was, adds none,
// nor does trying a process. A record deleted has no history.
func TestRecordChanges(t *testing.T) {
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Exception", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "title"}, http.StatusCreated, nil)
	rulesState(a, root.ID, "new", `id != ""`)
	a.newState(root.ID, "completed")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	use := func(f int64) modObject {
		a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f, "x": 0, "y": 0}, http.StatusCreated, nil)
		return modNamed(t, modObjects(a, f), "Exception")
	}

	fetch := a.newFlow("Fetch exceptions")
	fetched := use(fetch.ID)
	sentry := a.newProcess(fetch.ID, map[string]any{"name": "Sentry", "kind": kindScript, "script": "fetch"})
	a.output(sentry.ID, fetched.ID)
	a.startAt(fetch.ID, sentry.ID)

	process := a.newFlow("Process exceptions")
	taken := use(process.ID)
	complete := a.newProcess(process.ID, map[string]any{"name": "Complete", "kind": kindScript, "script": "complete"})
	a.link(map[string]any{"process": complete.ID, "object": taken.ID, "direction": directionInput,
		"query": linkQuery{State: "new", Limit: 1, Oldest: true}})
	a.link(map[string]any{"process": complete.ID, "object": taken.ID, "direction": directionOutput, "state": pickState(t, taken, "completed")})
	a.startAt(process.ID, complete.ID)

	var mu sync.Mutex
	var next string
	a.worker.onScript("Sentry", func(context.Context, scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		return runResult{OK: true, Output: next}
	})
	a.worker.onScript("Complete", func(_ context.Context, run scriptRun) runResult {
		var records []json.RawMessage
		if len(run.Inputs) != 1 || json.Unmarshal([]byte(run.Inputs[0].Content), &records) != nil || len(records) == 0 {
			return runResult{Error: fmt.Sprintf("given %+v", run.Inputs)}
		}
		return runResult{OK: true, Output: string(records[0])}
	})
	ingest := func(out string) int64 {
		t.Helper()
		mu.Lock()
		next = out
		mu.Unlock()
		rn := a.startRun(fetch.ID)
		if d := a.finish(rn.ID); d.Status != runDone {
			t.Fatalf("fetching %s %s; steps %s", out, d.Status, describeSteps(d))
		}
		return rn.ID
	}
	expect := func(record int64, want ...string) []recordChange {
		t.Helper()
		cs := recordChangesOf(a, s.ID, record)
		if got := describeChanges(cs); got != strings.Join(want, " | ") {
			t.Errorf("record %d's history:\n  %s\nwant\n  %s", record, got, strings.Join(want, " | "))
		}
		return cs
	}

	// Added in the state its rule gives it: one change, the step's.
	first := ingest(`{"id": "1", "title": "Timeout"}`)
	id := sharedRecords(a, s.ID)["1"].ID
	added := `added new<- Fetch exceptions:Sentry run [id:->"1" title:->"Timeout"]`
	cs := expect(id, added)
	if c := cs[0]; c.Run == nil || *c.Run != first || c.Flow == nil || *c.Flow != fetch.ID || c.ProcessKind != "script" || c.Inner != nil {
		t.Errorf("the record was added by %+v, want run %d of flow %d's script Sentry", c, first, fetch.ID)
	}

	// Fetched the same again, nothing changed; then with a new title.
	ingest(`{"id": "1", "title": "Timeout"}`)
	expect(id, added)
	ingest(`{"id": "1", "title": "Timeout in checkout"}`)
	retitled := `written new<new Fetch exceptions:Sentry run [title:"Timeout">"Timeout in checkout"]`
	expect(id, retitled, added)

	// Trying the fetch on something else changes no record.
	mu.Lock()
	next = `{"id": "1", "title": "Tried"}`
	mu.Unlock()
	if d := a.finish(tryProcess(a, sentry.ID).ID); d.Status != runDone {
		t.Fatalf("trying Sentry ended %s: %s", d.Status, describeSteps(d))
	}
	expect(id, retitled, added)

	// Another flow's run moves it on.
	worked := a.startRun(process.ID)
	if d := a.finish(worked.ID); d.Status != runDone {
		t.Fatalf("processing ended %s: %s", d.Status, describeSteps(d))
	}
	completed := `written completed<new Process exceptions:Complete run []`
	cs = expect(id, completed, retitled, added)
	if c := cs[0]; c.Run == nil || *c.Run != worked.ID {
		t.Errorf("the record was completed by run %v, want %d", c.Run, worked.ID)
	}

	// A person corrects it by hand, its state too.
	a.call("PUT", fmt.Sprintf("/api/structs/%d/records/%d", s.ID, id),
		map[string]any{"content": map[string]string{"id": "1", "title": "Timeout at checkout"}, "state": "new"}, http.StatusOK, nil)
	edited := `edited new<completed by hand [title:"Timeout in checkout">"Timeout at checkout"]`
	cs = expect(id, edited, completed, retitled, added)
	var then struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(cs[2].Content, &then); then.Title != "Timeout in checkout" {
		t.Errorf("the record as the third latest change left it is titled %q, want it as then", then.Title)
	}

	// A record added by hand starts its history so. Deleted, it is kept
	// with its history, which notes the delete, but nothing finds it: not
	// the records, its history, an edit, a query; and its unique value is
	// free, so a fetch writing it adds a new record with a history of its
	// own. Deleting it again deletes nothing.
	var manual sharedRecord
	a.call("POST", fmt.Sprintf("/api/structs/%d/records", s.ID),
		map[string]any{"content": map[string]string{"id": "9", "title": "Manual"}, "state": "new"}, http.StatusCreated, &manual)
	expect(manual.ID, `added new<- by hand [id:->"9" title:->"Manual"]`)
	var deleted struct {
		Deleted int `json:"deleted"`
	}
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/delete", s.ID), map[string]any{"ids": []int64{manual.ID}}, http.StatusOK, &deleted)
	if deleted.Deleted != 1 {
		t.Errorf("deleting the record deleted %d", deleted.Deleted)
	}
	if _, ok := sharedRecords(a, s.ID)["9"]; ok {
		t.Errorf("the deleted record is still listed")
	}
	a.refused("GET", fmt.Sprintf("/api/structs/%d/records/%d/changes", s.ID, manual.ID), nil, http.StatusNotFound, "no such record")
	a.refused("PUT", fmt.Sprintf("/api/structs/%d/records/%d", s.ID, manual.ID),
		map[string]any{"content": map[string]string{"id": "9", "title": "Back"}, "state": "new"}, http.StatusNotFound, "no such record")
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/delete", s.ID), map[string]any{"ids": []int64{manual.ID}}, http.StatusOK, &deleted)
	if deleted.Deleted != 0 {
		t.Errorf("deleting the deleted record again deleted %d", deleted.Deleted)
	}
	db := secretsDB(t)
	var gone bool
	var kinds string
	if err := db.QueryRow(`SELECT sr.deleted_at IS NOT NULL, (SELECT GROUP_CONCAT(kind ORDER BY id) FROM record_changes WHERE record_id = sr.id)
		FROM shared_records sr WHERE sr.id = ?`, manual.ID).Scan(&gone, &kinds); err != nil {
		t.Fatalf("the deleted record isn't kept: %v", err)
	}
	if !gone || kinds != "added,deleted" {
		t.Errorf("the deleted record is kept deleted %v with changes %s, want added then deleted", gone, kinds)
	}
	again := ingest(`{"id": "9", "title": "Manual again"}`)
	back := sharedRecords(a, s.ID)["9"]
	if back.ID == manual.ID || back.ID == 0 {
		t.Fatalf("fetching the deleted record's id gave record %d, want a new one", back.ID)
	}
	cs = expect(back.ID, `added new<- Fetch exceptions:Sentry run [id:->"9" title:->"Manual again"]`)
	if c := cs[0]; c.Run == nil || *c.Run != again {
		t.Errorf("the new record was added by run %v, want %d", c.Run, again)
	}
	// The query of the oldest new record completes record 1, then the new
	// one, never the deleted one, though it is older and new too.
	for range 2 {
		if d := a.finish(a.startRun(process.ID).ID); d.Status != runDone {
			t.Fatalf("processing ended %s: %s", d.Status, describeSteps(d))
		}
	}
	if r := sharedRecords(a, s.ID); r["1"].State != "completed" || r["9"].ID != back.ID || r["9"].State != "completed" {
		t.Errorf("after processing twice record 1 is %s and 9 is %+v, want both completed", r["1"].State, r["9"])
	}
	if err := db.QueryRow("SELECT (SELECT GROUP_CONCAT(kind ORDER BY id) FROM record_changes WHERE record_id = ?)", manual.ID).Scan(&kinds); err != nil || kinds != "added,deleted" {
		t.Errorf("the deleted record's changes are %s (%v), want it left alone", kinds, err)
	}
	a.refused("GET", fmt.Sprintf("/api/structs/%d/records/%d/changes", fetch.ID, id), nil, http.StatusBadRequest, "keeps no records")
}

// A step writing a record twice, as a list holding it twice does, is one
// change of it, the step's; written back as it was before the step, it is
// none.
func TestRecordChangesOfAList(t *testing.T) {
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Issue", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "count"}, http.StatusCreated, nil)
	a.newState(root.ID, "new")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"many": true, "shared": true}, http.StatusOK, nil)
	fetch := a.newFlow("Fetch issues")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": fetch.ID}, http.StatusCreated, nil)
	fetched := modNamed(t, modObjects(a, fetch.ID), "Issue")
	all := a.newProcess(fetch.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "fetch"})
	a.link(map[string]any{"process": all.ID, "object": fetched.ID, "direction": directionOutput, "state": pickState(t, fetched, "new")})
	a.startAt(fetch.ID, all.ID)
	var mu sync.Mutex
	next := ""
	a.worker.onScript("Fetch", func(context.Context, scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		return runResult{OK: true, Output: next}
	})
	ingest := func(out string) {
		t.Helper()
		mu.Lock()
		next = out
		mu.Unlock()
		if d := a.finish(a.startRun(fetch.ID).ID); d.Status != runDone {
			t.Fatalf("fetching %s ended %s: %s", out, d.Status, describeSteps(d))
		}
	}
	expect := func(want ...string) {
		t.Helper()
		got := describeChanges(recordChangesOf(a, s.ID, sharedRecords(a, s.ID)["1"].ID))
		if got != strings.Join(want, " | ") {
			t.Errorf("the issue's history:\n  %s\nwant\n  %s", got, strings.Join(want, " | "))
		}
	}

	ingest(`[{"id": "1", "count": 1}, {"id": "1", "count": 2}]`)
	added := `added new<- Fetch issues:Fetch run [id:->"1" count:->2]`
	expect(added)
	ingest(`[{"id": "1", "count": 3}, {"id": "1", "count": 2}]`)
	expect(added)
	ingest(`[{"id": "1", "count": 4}]`)
	expect(`written new<new Fetch issues:Fetch run [count:2>4]`, added)
}

// A person's review moving the record its run took is the review step's
// change, and a procedure's step writing a record changes it in the run
// that called the procedure, inside the procedure's own run.
func TestRecordChangesByReviewsAndProcedures(t *testing.T) {
	a := newApp(t)
	s, f, ticket := pickTickets(a, "Review tickets")
	a.call("PUT", fmt.Sprintf("/api/objects/%d/pick", ticket.ID), map[string]any{"pick": map[string]any{"state": "open"}}, http.StatusNoContent, nil)
	approve := a.newProcess(f.ID, map[string]any{"name": "Approve", "kind": kindHuman})
	a.input(approve.ID, ticket.ID)
	a.link(map[string]any{"process": approve.ID, "object": ticket.ID, "direction": directionOutput,
		"states": []int64{pickState(t, ticket, "created"), pickState(t, ticket, "review")}})
	a.startAt(f.ID, approve.ID)
	b1 := pickAdd(a, s.ID, "B-1", "open")

	rn := a.startRun(f.ID)
	review := a.waitStep(rn.ID, "Approve", stepWaiting)
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": pickState(t, ticket, "review")})
	a.finish(rn.ID)
	cs := recordChangesOf(a, s.ID, b1)
	want := `written review<open Review tickets:Approve run [] | added open<- by hand [id:->"B-1" title:->"Ticket B-1" status:->""]`
	if got := describeChanges(cs); got != want {
		t.Errorf("B-1's history:\n  %s\nwant\n  %s", got, want)
	}
	if c := cs[0]; c.Run == nil || *c.Run != rn.ID || c.ProcessKind != "human" {
		t.Errorf("B-1 was moved by %+v, want run %d's human step", c, rn.ID)
	}
	// A record deleted is never picked, nor a run started on it.
	b3 := pickAdd(a, s.ID, "B-3", "open")
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/delete", s.ID), map[string]any{"ids": []int64{b3}}, http.StatusOK, nil)
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, `no "Ticket" record in open is there to work on`)
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": f.ID, "records": []int64{b3}}, http.StatusNotFound, fmt.Sprintf("no record %d", b3))

	// A procedure patching the ticket its caller loaded.
	patch := a.newFlow("Patch")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": patch.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	inPatch := modNamed(t, modObjects(a, patch.ID), "Ticket")
	fix := a.newProcess(patch.ID, map[string]any{"name": "Fix", "kind": kindScript, "script": "fix"})
	a.input(fix.ID, inPatch.ID)
	a.link(map[string]any{"process": fix.ID, "object": inPatch.ID, "direction": directionOutput, "state": pickState(t, inPatch, "created")})
	a.startAt(patch.ID, fix.ID)

	triage := a.newFlow("Triage")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": triage.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	loaded := modNamed(t, modObjects(a, triage.ID), "Ticket")
	load := a.newProcess(triage.ID, map[string]any{"name": "Load", "kind": kindScript, "script": "load"})
	a.output(load.ID, loaded.ID)
	call := a.newProcess(triage.ID, map[string]any{"name": "Call", "kind": kindSubflow, "settings": map[string]any{"flow": patch.ID}})
	a.input(call.ID, loaded.ID)
	a.startAt(triage.ID, load.ID)
	a.worker.onScript("Load", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"id": "C-1", "title": "Crash", "status": "new"}`}
	})
	a.worker.onScript("Fix", func(_ context.Context, run scriptRun) runResult {
		fields, err := pickGiven(run)
		if err != nil {
			return runResult{Error: err.Error()}
		}
		fields["status"] = "patched"
		out, _ := json.Marshal(fields)
		return runResult{OK: true, Output: string(out)}
	})
	called := a.startRun(triage.ID)
	d := a.finish(called.ID)
	if d.Status != runDone {
		t.Fatalf("triage ended %s: %s", d.Status, describeSteps(d))
	}
	child := onlyStep(t, d, "Call").ChildRun
	c1 := sharedRecords(a, s.ID)["C-1"].ID
	cs = recordChangesOf(a, s.ID, c1)
	want = `written created< Triage:Fix run in Patch [status:"new">"patched"] | added <- Triage:Load run [id:->"C-1" title:->"Crash" status:->"new"]`
	if got := describeChanges(cs); got != want {
		t.Errorf("C-1's history:\n  %s\nwant\n  %s", got, want)
	}
	if len(cs) == 2 {
		if c := cs[0]; c.Run == nil || *c.Run != called.ID || child == nil || c.Inner == nil || *c.Inner != *child ||
			c.InnerFlow == nil || *c.InnerFlow != patch.ID {
			t.Errorf("C-1 was patched by %+v, want run %d inside procedure %d's run %v", c, called.ID, patch.ID, child)
		}
		if c := cs[1]; c.Run == nil || *c.Run != called.ID || c.Inner != nil {
			t.Errorf("C-1 was loaded by %+v, want run %d itself", c, called.ID)
		}
	}
}

// Someone who sees a shared struct but not the flow whose run changed one
// of its records is told the run's number, but not its flow, its process or
// who decided it; whoever sees the flow is told all of it. A correction by
// hand names who made it, as the struct is theirs to see.
func TestRecordChangesOfFlowsNotSeen(t *testing.T) {
	p := newFakeProvider(t)
	a := inboxApp(t, p, "")
	usersSignIn(a, p, usersAda)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Lead", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "name"}, http.StatusCreated, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	usersShare(a, s.ID)

	// Ada's own flow, which Bob doesn't see, adds a lead.
	secret := a.newFlow("Secret sourcing")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": secret.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	lead := modNamed(t, modObjects(a, secret.ID), "Lead")
	scrape := a.newProcess(secret.ID, map[string]any{"name": "Scrape", "kind": kindScript, "script": "scrape"})
	a.output(scrape.ID, lead.ID)
	a.startAt(secret.ID, scrape.ID)
	a.worker.onScript("Scrape", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"id": "L-1", "name": "Acme"}`}
	})
	rn := a.startRun(secret.ID)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Fatalf("sourcing ended %s: %s", d.Status, describeSteps(d))
	}
	id := sharedRecords(a, s.ID)["L-1"].ID
	a.call("PUT", fmt.Sprintf("/api/structs/%d/records/%d", s.ID, id),
		map[string]any{"content": map[string]string{"id": "L-1", "name": "Acme Ltd"}, "state": ""}, http.StatusOK, nil)

	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	asBob := inboxAs(a, p, bob)
	cs := recordChangesOf(asBob, s.ID, id)
	if len(cs) != 2 {
		t.Fatalf("Bob is told %s, want the correction and the run adding it", describeChanges(cs))
	}
	if c := cs[0]; c.Kind != "edited" || c.Run != nil || c.By != "Ada Lovelace" {
		t.Errorf("Bob is told the correction as %+v, want Ada's by hand", c)
	}
	if c := cs[1]; c.Kind != "added" || c.Run == nil || *c.Run != rn.ID || !c.Hidden || c.Flow != nil || c.FlowName != "" ||
		c.Process != "" || c.ProcessKind != "" || c.By != "" {
		t.Errorf("Bob is told the run adding it as %+v, want only its number", c)
	}
	cs = recordChangesOf(a, s.ID, id)
	if c := cs[1]; c.Hidden || c.Flow == nil || *c.Flow != secret.ID || c.FlowName != "Secret sourcing" || c.Process != "Scrape" || c.ProcessKind != "script" {
		t.Errorf("Ada is told the run adding it as %+v, want its flow and process", c)
	}
}
