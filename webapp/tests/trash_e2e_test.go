package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// A flow in the trash as the trash lists it; see trash.go.
type trashedFlow struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Module    bool      `json:"module"`
	Struct    bool      `json:"struct"`
	DeletedAt time.Time `json:"deletedAt"`
	DeletedBy string    `json:"deletedBy"`
	GoesAt    time.Time `json:"goesAt"`
	NameTaken bool      `json:"nameTaken"`
	UsedBy    int       `json:"usedBy"`
}

func trashMove(a *app, id int64) {
	a.t.Helper()
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", id), nil, http.StatusNoContent, nil)
}

func trashList(a *app) []trashedFlow {
	a.t.Helper()
	var fs []trashedFlow
	a.call("GET", "/api/trash", nil, http.StatusOK, &fs)
	return fs
}

// The flow as the trash lists it, nil if it doesn't.
func trashListed(a *app, id int64) *trashedFlow {
	a.t.Helper()
	fs := trashList(a)
	if i := slices.IndexFunc(fs, func(f trashedFlow) bool { return f.ID == id }); i >= 0 {
		return &fs[i]
	}
	return nil
}

func trashRestore(a *app, id int64, name string) flow {
	a.t.Helper()
	var body any
	if name != "" {
		body = map[string]any{"name": name}
	}
	var f flow
	a.call("POST", fmt.Sprintf("/api/trash/%d/restore", id), body, http.StatusOK, &f)
	return f
}

// A process as its board lists it, with the procedure in the trash it runs.
type trashListedProcess struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	TrashedFlow string `json:"trashedFlow"`
}

// Deleting a flow moves it to the trash: out of the flow list and the
// schedules, no one's to reach by its id or anything in it, its name free
// for another, until it is restored, under another name if it was taken,
// with its runs, tags, favourite and schedules; then deleted for good.
func TestTrashHidesAFlowUntilRestored(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprint("Trashed ", time.Now().UnixNano())
	f := a.newFlow(name)
	note := a.agent(f.ID, "Note", "Note it.")
	obj := a.newObject(f.ID, "Note")
	a.output(note.ID, obj.ID)
	a.startAt(f.ID, note.ID)
	a.worker.onAgent("Note", handsIn(data("noted")))
	rn := a.finish(a.startRun(f.ID).ID)
	a.call("PUT", fmt.Sprintf("/api/flows/%d/tags", f.ID), map[string]any{"tags": []string{"kept"}}, http.StatusOK, nil)
	a.call("PUT", fmt.Sprintf("/api/flows/%d/favourite", f.ID), nil, http.StatusNoContent, nil)
	sched := a.newSchedule(f.ID, "0 9 * * *")
	var ver struct {
		ID int64 `json:"id"`
	}
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &ver)

	before := time.Now()
	trashMove(a, f.ID)

	if slices.Contains(teamsFlows(a), f.ID) {
		t.Error("the flow in the trash is listed")
	}
	var scheds []schedule
	a.call("GET", "/api/schedules", nil, http.StatusOK, &scheds)
	if slices.ContainsFunc(scheds, func(s schedule) bool { return s.ID == sched.ID }) {
		t.Error("the schedule of the flow in the trash is listed")
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", fmt.Sprintf("/api/flows/%d", f.ID), nil},
		{"PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"name": "Back"}},
		{"DELETE", fmt.Sprintf("/api/flows/%d", f.ID), nil},
		{"GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil},
		{"PUT", fmt.Sprintf("/api/flows/%d/favourite", f.ID), nil},
		{"GET", fmt.Sprintf("/api/processes?flow=%d", f.ID), nil},
		{"PATCH", fmt.Sprintf("/api/processes/%d", note.ID), map[string]any{"name": "Again"}},
		{"POST", "/api/processes", map[string]any{"flow": f.ID, "name": "Sneak", "kind": "agent"}},
		{"GET", fmt.Sprintf("/api/runs/%d", rn.ID), nil},
		{"GET", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil},
		{"POST", "/api/runs", map[string]any{"flow": f.ID}},
		{"GET", fmt.Sprintf("/api/versions/%d", ver.ID), nil},
		{"PATCH", fmt.Sprintf("/api/schedules/%d", sched.ID), map[string]any{"cron": "0 10 * * *"}},
		{"GET", fmt.Sprintf("/api/credentials?flow=%d", f.ID), nil},
		{"GET", fmt.Sprintf("/api/data?flow=%d", f.ID), nil},
		{"POST", fmt.Sprintf("/api/history?flow=%d", f.ID), nil},
	} {
		a.refused(c.method, c.path, c.body, http.StatusNotFound, "flow not found")
	}

	tr := trashListed(a, f.ID)
	if tr == nil {
		t.Fatal("the trash doesn't list the flow")
	}
	if tr.Name != name || tr.Module || tr.Struct || tr.NameTaken || tr.DeletedBy != "" ||
		tr.DeletedAt.Before(before.Add(-time.Second)) || tr.DeletedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("the trash lists %+v", tr)
	}
	if keep := tr.GoesAt.Sub(tr.DeletedAt); keep != 30*24*time.Hour {
		t.Errorf("the trash keeps it %v", keep)
	}

	// Its name is free, and taken, it comes back under another.
	dup := a.newFlow(name)
	if tr := trashListed(a, f.ID); tr == nil || !tr.NameTaken {
		t.Errorf("with its name taken, the trash lists %+v", tr)
	}
	a.refused("POST", fmt.Sprintf("/api/trash/%d/restore", f.ID), nil, http.StatusConflict, "out of the trash already")
	back := trashRestore(a, f.ID, name+" again")
	if back.ID != f.ID || back.Name != name+" again" || back.Access != "edit" {
		t.Errorf("restored as %+v", back)
	}
	if l := listed(a, f.ID); !slices.Equal(l.Tags, []string{"kept"}) || !l.Favourite || l.LastRun == nil || l.LastRun.ID != rn.ID {
		t.Errorf("restored, the flow is listed as %+v", l)
	}
	a.call("GET", "/api/schedules", nil, http.StatusOK, &scheds)
	if i := slices.IndexFunc(scheds, func(s schedule) bool { return s.ID == sched.ID }); i < 0 || scheds[i].NextRunAt == nil || !scheds[i].NextRunAt.After(time.Now()) {
		t.Errorf("restored, its schedules are %+v", scheds)
	}
	if d := a.getRun(rn.ID); d.Status != runDone {
		t.Errorf("restored, its run is %s", d.Status)
	}
	if trashListed(a, f.ID) != nil {
		t.Error("the trash still lists the flow restored")
	}
	a.refused("POST", fmt.Sprintf("/api/trash/%d/restore", f.ID), nil, http.StatusNotFound, "nothing of that id is in the trash")
	a.refused("DELETE", fmt.Sprintf("/api/trash/%d", f.ID), nil, http.StatusNotFound, "nothing of that id is in the trash")

	// Deleted for good, it is gone, runs and all.
	trashMove(a, f.ID)
	a.call("DELETE", fmt.Sprintf("/api/trash/%d", f.ID), nil, http.StatusNoContent, nil)
	if trashListed(a, f.ID) != nil {
		t.Error("the trash lists what was deleted for good")
	}
	a.refused("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusNotFound, "flow not found")
	a.refused("GET", fmt.Sprintf("/api/runs/%d", rn.ID), nil, http.StatusNotFound, "run not found")
	a.refused("DELETE", fmt.Sprintf("/api/trash/%d", f.ID), nil, http.StatusNotFound, "nothing of that id is in the trash")
	if l := listed(a, dup.ID); l.Name != name {
		t.Errorf("the flow that took the name is %+v", l)
	}
}

// Runs going on in a flow are stopped as it goes to the trash, which is
// asked for first; so are the reviews waiting in them, which leave the inbox.
func TestTrashStopsRunsGoingOn(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Dawdling")
	slow := a.agent(f.ID, "Dawdle", "Take your time.")
	a.startAt(f.ID, slow.ID)
	cancelled := make(chan bool, 1)
	a.worker.onAgent("Dawdle", func(ctx context.Context, _ agentRun) runResult {
		select {
		case <-ctx.Done():
			cancelled <- true
		case <-time.After(20 * time.Second):
			cancelled <- false
		}
		return runResult{Error: "cancelled"}
	})
	rn := a.startRun(f.ID)
	a.waitStep(rn.ID, "Dawdle", stepActive)

	status, body := a.do("DELETE", fmt.Sprintf("/api/flows/%d", f.ID), nil)
	var refusal struct {
		Error   string `json:"error"`
		Running int    `json:"running"`
	}
	if err := json.Unmarshal(body, &refusal); status != http.StatusConflict || err != nil || refusal.Running != 1 || !strings.Contains(refusal.Error, "going on") {
		t.Fatalf("moving a flow with a run going on = %d %s", status, body)
	}
	if !slices.Contains(teamsFlows(a), f.ID) {
		t.Fatal("refused, the flow went to the trash all the same")
	}
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"stopRuns": true}, http.StatusNoContent, nil)
	if !receive(t, cancelled, "the step to say whether it was cancelled") {
		t.Fatal("moving the flow to the trash didn't cancel its step")
	}
	trashRestore(a, f.ID, "")
	if d := a.finish(rn.ID); d.Status != runInterrupted {
		t.Errorf("the run stopped by the trash is %s", d.Status)
	}
	logLineWith(t, a.runLogs(rn.ID, 0), "to the trash")

	// A review waiting is stopped with its run.
	b := humanNewReviewBoard(a)
	review := a.startRun(b.flow.ID)
	waiting := a.waitStep(review.ID, "Review", stepWaiting)
	inInbox := func() bool {
		var items []struct {
			Step int64 `json:"step"`
		}
		a.call("GET", "/api/inbox", nil, http.StatusOK, &items)
		return slices.ContainsFunc(items, func(it struct {
			Step int64 `json:"step"`
		}) bool {
			return it.Step == waiting.ID
		})
	}
	if !inInbox() {
		t.Fatal("the review waiting isn't in the inbox")
	}
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", b.flow.ID), map[string]any{"stopRuns": true}, http.StatusNoContent, nil)
	if inInbox() {
		t.Error("the review of a flow in the trash is in the inbox")
	}
	trashRestore(a, b.flow.ID, "")
	if d := a.finish(review.ID); d.Status != runInterrupted {
		t.Errorf("the run waiting on the review is %s", d.Status)
	}
}

// A procedure in the trash fails the runs of it going on inside other
// flows' runs, failing those, and the calls of it after, which the board
// shows broken; its callers can still be changed, but none can pick it.
// Restored, it runs again.
func TestTrashBreaksCallsOfAProcedure(t *testing.T) {
	a := newApp(t)
	s := verBuildSubflow(a)
	started := make(chan struct{}, 1)
	slow := true
	a.worker.onAgent("Fix", func(ctx context.Context, _ agentRun) runResult {
		if !slow {
			return submitted(data("the fix"))
		}
		started <- struct{}{}
		<-ctx.Done()
		return runResult{Error: "cancelled"}
	})
	rn := a.startRun(s.caller.ID)
	receive(t, started, "the procedure's step to start")

	status, body := a.do("DELETE", fmt.Sprintf("/api/flows/%d", s.callee.ID), nil)
	if status != http.StatusConflict || !strings.Contains(string(body), `"running":1`) {
		t.Fatalf("moving a procedure running inside another flow's run = %d %s", status, body)
	}
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", s.callee.ID), map[string]any{"stopRuns": true}, http.StatusNoContent, nil)
	d := a.finish(rn.ID)
	call := onlyStep(t, d, "Call")
	if d.Status != runFailed || call.Status != stepError || !strings.Contains(call.Error, "failed") {
		t.Errorf("the caller's run %s, Call %s %q", d.Status, call.Status, call.Error)
	}
	logLineWith(t, a.runLogs(rn.ID, 0), "moved the procedure")

	// Run again, the call fails saying why.
	slow = false
	d = a.finish(a.startRun(s.caller.ID).ID)
	if call := onlyStep(t, d, "Call"); d.Status != runFailed || !strings.Contains(call.Error, "is in the trash") {
		t.Errorf("calling a procedure in the trash: run %s, Call %s %q", d.Status, call.Status, call.Error)
	}

	// The board shows the call broken.
	var ps []trashListedProcess
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", s.caller.ID), nil, http.StatusOK, &ps)
	for _, p := range ps {
		if want := map[bool]string{true: s.callee.Name}[p.ID == s.call.ID]; p.TrashedFlow != want {
			t.Errorf("process %s runs a procedure in the trash named %q, want %q", p.Name, p.TrashedFlow, want)
		}
	}

	// The process calling it is still changed, but none picks it anew.
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", s.call.ID), map[string]any{"name": "Call it", "settings": map[string]any{"flow": s.callee.ID}}, http.StatusOK, nil)
	other := a.newProcess(s.caller.ID, map[string]any{"name": "Other", "kind": kindSubflow})
	a.refused("PATCH", fmt.Sprintf("/api/processes/%d", other.ID), map[string]any{"settings": map[string]any{"flow": s.callee.ID}}, http.StatusBadRequest, "in the trash")
	a.refused("POST", "/api/processes", map[string]any{"flow": s.caller.ID, "name": "Sneak", "kind": kindSubflow, "settings": map[string]any{"flow": s.callee.ID}},
		http.StatusBadRequest, "in the trash")
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", other.ID), nil, http.StatusNoContent, nil)
	if tr := trashListed(a, s.callee.ID); tr == nil || tr.UsedBy != 1 {
		t.Errorf("the trash lists the procedure as %+v", tr)
	}

	trashRestore(a, s.callee.ID, "")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", s.call.ID), map[string]any{"name": "Call"}, http.StatusOK, nil)
	if d := a.finish(a.startRun(s.caller.ID).ID); d.Status != runDone {
		t.Errorf("restored, the procedure's caller ran %s; steps %s", d.Status, describeSteps(d))
	}
}

// A struct in the trash leaves the copies of its object their flows' own,
// as deleting it for good does, and they keep working on them, a shared
// one on its records; restored, the copies follow it again.
func TestTrashLeavesStructCopiesTheirOwn(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprint("Ticket ", time.Now().UnixNano())
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": name, "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.newState(root.ID, "open")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)

	job := a.newFlow("Fetch tickets")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": job.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	cp := modNamed(t, modObjects(a, job.ID), name)
	fetch := a.newProcess(job.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "fetch"})
	a.output(fetch.ID, cp.ID)
	a.startAt(job.ID, fetch.ID)
	a.worker.onScript("Fetch", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"id": "T1"}`}
	})
	a.refused("POST", fmt.Sprintf("/api/objects/%d/states", cp.ID), map[string]any{"name": "mine"}, http.StatusBadRequest, "comes from the struct")

	trashMove(a, s.ID)
	cp = modNamed(t, modObjects(a, job.ID), name)
	if cp.Source != nil || cp.SourceFlow != nil || !cp.Shared {
		t.Errorf("the copy of a struct in the trash = %+v", cp)
	}
	a.call("POST", fmt.Sprintf("/api/objects/%d/states", cp.ID), map[string]any{"name": "mine"}, http.StatusCreated, nil)
	if d := a.finish(a.startRun(job.ID).ID); d.Status != runDone {
		t.Errorf("with the struct in the trash, its user ran %s; steps %s", d.Status, describeSteps(d))
	}
	if tr := trashListed(a, s.ID); tr == nil || !tr.Struct || tr.UsedBy != 1 {
		t.Errorf("the trash lists the struct as %+v", tr)
	}

	trashRestore(a, s.ID, "")
	cp = modNamed(t, modObjects(a, job.ID), name)
	if cp.Source == nil || *cp.Source != root.ID || modStateNames(cp) != "open" {
		t.Errorf("restored, the copy is %+v", cp)
	}
	if r, ok := sharedRecords(a, s.ID)["T1"]; !ok {
		t.Errorf("the record written while the struct was in the trash is gone: %+v", r)
	}

	// Deleted for good, the copy is its own for good.
	trashMove(a, s.ID)
	a.call("DELETE", fmt.Sprintf("/api/trash/%d", s.ID), nil, http.StatusNoContent, nil)
	cp = modNamed(t, modObjects(a, job.ID), name)
	if cp.Source != nil {
		t.Errorf("the copy of a struct deleted for good = %+v", cp)
	}
	if d := a.finish(a.startRun(job.ID).ID); d.Status != runDone {
		t.Errorf("with the struct deleted, its user ran %s; steps %s", d.Status, describeSteps(d))
	}
}

// A flow in the trash starts no runs: its schedules don't fire, and come
// back at their next time once it is restored, and the API answers as for
// a flow that doesn't exist.
func TestTrashStartsNoRuns(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	f := a.newFlow("Ticking")
	tick := a.newProcess(f.ID, map[string]any{"name": "Tick", "kind": kindScript, "script": "date"})
	a.startAt(f.ID, tick.ID)
	a.worker.onScript("Tick", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"data": "tick"}`}
	})
	key := apiNewKey(a, "trash")
	first := apiStart(a, key.Key, f.ID, nil, http.StatusCreated)
	a.finish(first.ID)
	s := a.newSchedule(f.ID, everySecond)
	a.waitScheduledRuns(f.ID, s.ID, 1)

	a.call("DELETE", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"stopRuns": true}, http.StatusNoContent, nil)
	scheduled := func() int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM runs WHERE schedule_id = ?", s.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	was := scheduled()
	time.Sleep(3 * time.Second)
	if now := scheduled(); now != was {
		t.Errorf("in the trash, its schedule started %d runs", now-was)
	}
	apiRefused(a, key.Key, "GET", fmt.Sprintf("/v1/flows/%d", f.ID), nil, http.StatusNotFound, "flow not found")
	apiRefused(a, key.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", f.ID), nil, http.StatusNotFound, "flow not found")
	apiRefused(a, key.Key, "GET", fmt.Sprintf("/v1/runs/%d", first.ID), nil, http.StatusNotFound, "flow not found")

	trashRestore(a, f.ID, "")
	a.waitScheduledRuns(f.ID, s.ID, was+1)
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"enabled": false}, http.StatusOK, nil)
	a.finish(apiStart(a, key.Key, f.ID, nil, http.StatusCreated).ID)
}

// The trash empties itself of what it kept long enough, each flow once
// across replicas, with its runs.
func TestTrashEmptiesItself(t *testing.T) {
	var other *app
	a, db := activityApp(t, func(t *testing.T) *app {
		fw := newFakeWorker(t)
		a := startApp(t, fw.url, "TRASH_KEEP=2s")
		a.worker = fw
		other = startApp(t, fw.url, "TRASH_KEEP=2s")
		return a
	})
	var ids []int64
	var runs []int64
	for i := range 4 {
		f := a.newFlow(fmt.Sprint("Gone ", i))
		p := a.agent(f.ID, "Note", "Note it.")
		a.startAt(f.ID, p.ID)
		a.worker.onAgent("Note", handsIn(data("noted")))
		runs = append(runs, a.finish(a.startRun(f.ID).ID).ID)
		ids = append(ids, f.ID)
	}
	kept := a.newFlow("Kept")
	for _, id := range ids {
		trashMove(a, id)
	}
	if tr := trashListed(a, ids[0]); tr == nil || tr.GoesAt.Sub(tr.DeletedAt) != 2*time.Second {
		t.Errorf("the trash keeping things 2s lists %+v", tr)
	}
	deadline := time.Now().Add(20 * time.Second)
	for len(trashList(a)) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the trash still holds %+v", trashList(a))
		}
		time.Sleep(200 * time.Millisecond)
	}
	var left int
	if err := db.QueryRow("SELECT (SELECT COUNT(*) FROM flows WHERE id IN (?, ?, ?, ?)) + (SELECT COUNT(*) FROM runs WHERE id IN (?, ?, ?, ?))",
		ids[0], ids[1], ids[2], ids[3], runs[0], runs[1], runs[2], runs[3]).Scan(&left); err != nil || left != 0 {
		t.Errorf("the trash left %d flows and runs (%v)", left, err)
	}
	logs := a.proc.log.String() + other.proc.log.String()
	for _, id := range ids {
		if n := strings.Count(logs, fmt.Sprintf("emptied flow %d out of the trash", id)); n != 1 {
			t.Errorf("flow %d was emptied out of the trash %d times", id, n)
		}
	}
	listed(a, kept.ID)
}

// The trash is theirs who may change what is in it: only they list it,
// restore it or delete it for good. No one else reaches it, admins and
// those deciding its reviews included, and a team owning one there isn't
// deleted.
func TestTrashIsTheirsWhoMayChangeIt(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carl, dora := usersNewPerson("Bob"), usersNewPerson("Carl"), usersNewPerson("Dora")
	bobRow := usersAdd(a, bob.email)
	usersAdd(a, carl.email)
	usersAdd(a, dora.email, "Approver")
	var tm teamsTeam
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Trashers", bobRow.ID), "members": []int64{bobRow.ID}}, http.StatusCreated, &tm)

	usersSignIn(a, p, bob)
	var f, teams flow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprint("Bob's ", time.Now().UnixNano()), "owner": map[string]any{"kind": "user", "id": bobRow.ID}}, http.StatusCreated, &f)
	approver := a.newObject(f.ID, "Approver")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", approver.ID), map[string]any{"human": true}, http.StatusOK, nil)
	a.call("POST", "/api/flows", map[string]any{"name": "Team's", "owner": map[string]any{"kind": "team", "id": tm.ID}}, http.StatusCreated, &teams)

	usersSignIn(a, p, dora)
	if !slices.Contains(teamsFlows(a), f.ID) {
		t.Fatal("dora doesn't see the flow whose reviews she decides")
	}

	usersSignIn(a, p, bob)
	trashMove(a, f.ID)
	trashMove(a, teams.ID)
	if tr := trashListed(a, f.ID); tr == nil || tr.DeletedBy != "Bob" {
		t.Errorf("bob's trash lists %+v", tr)
	}

	for _, who := range []usersPerson{dora, carl} {
		usersSignIn(a, p, who)
		if slices.Contains(teamsFlows(a), f.ID) || trashListed(a, f.ID) != nil {
			t.Errorf("%s sees bob's flow in the trash", who.name)
		}
		a.refused("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusNotFound, "flow not found")
		a.refused("POST", fmt.Sprintf("/api/trash/%d/restore", f.ID), nil, http.StatusNotFound, "nothing of that id is in the trash")
		a.refused("DELETE", fmt.Sprintf("/api/trash/%d", f.ID), nil, http.StatusNotFound, "nothing of that id is in the trash")
	}

	usersSignIn(a, p, usersAda)
	if tr := trashListed(a, f.ID); tr == nil {
		t.Error("the admin's trash doesn't list bob's flow")
	}
	a.refused("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusNotFound, "flow not found")
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "global"}, http.StatusNotFound, "flow not found")
	a.refused("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusConflict, "1 of them in the trash")

	usersSignIn(a, p, bob)
	trashRestore(a, f.ID, "")
	a.call("DELETE", fmt.Sprintf("/api/trash/%d", teams.ID), nil, http.StatusNoContent, nil)
	usersSignIn(a, p, dora)
	if !slices.Contains(teamsFlows(a), f.ID) {
		t.Error("restored, dora doesn't see the flow whose reviews she decides")
	}
	usersSignIn(a, p, usersAda)
	a.call("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusNoContent, nil)
}

// The Activity page leaves a flow in the trash out, for those who see
// every flow too.
func TestTrashLeavesActivity(t *testing.T) {
	a, _ := activityApp(t, newApp)
	run := func(name string) flow {
		f := a.newFlow(name)
		p := a.agent(f.ID, name+" step", "Do it.")
		a.startAt(f.ID, p.ID)
		a.worker.onAgent(name+" step", handsIn(data("done")))
		a.finish(a.startRun(f.ID).ID)
		return f
	}
	gone, kept := run("Gone"), run("Kept")
	if p := page30(a); !p.mentions(gone.ID) || !p.mentions(kept.ID) {
		t.Fatalf("the Activity page doesn't mention both flows: %+v", p)
	}
	trashMove(a, gone.ID)
	if p := page30(a); p.mentions(gone.ID) || !p.mentions(kept.ID) {
		t.Errorf("with one in the trash, the Activity page is %+v", p)
	}
}

// Setup files leave out what is in the trash, as if it were deleted, so a
// flow there sharing a name with another doesn't stop the export, and
// importing a flow of its name makes a new one.
func TestTrashLeavesSetupFiles(t *testing.T) {
	a := setupApp(t)
	gone := a.newFlow("Same name")
	trashMove(a, gone.ID)
	kept := a.newFlow("Same name")
	file := setupExport(a, "Same name")
	if strings.Count(file, "Same name") != 1 {
		t.Errorf("the export holds:\n%s", file)
	}
	trashMove(a, kept.ID)
	res := setupImportOK(a, "spoletta: 1\nflows:\n  - name: Same name\n")
	if len(res.Flows) != 1 || !res.Flows[0].Created || res.Flows[0].ID == gone.ID || res.Flows[0].ID == kept.ID {
		t.Errorf("importing the name of flows in the trash = %+v", res)
	}
	if len(trashList(a)) != 2 {
		t.Errorf("the trash holds %+v", trashList(a))
	}
}
