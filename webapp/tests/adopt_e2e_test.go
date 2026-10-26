package tests

// A run whose replica stops is taken over by a replica alive and carries
// on: its reviews wait again, the same steps, a step it lost goes again if
// its retries let it, and what it ran inside flow processes and items goes
// on from where it was.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The webapps of a test taking over each other's runs, on a database of
// its own, so they take over no run another test left going, sharing a
// fake worker.
type adoptDB struct {
	t      *testing.T
	name   string
	worker *fakeWorker
	db     *sql.DB
}

func newAdoptDB(t *testing.T) *adoptDB {
	t.Helper()
	switch {
	case testDB.fail != "":
		t.Fatal(testDB.fail)
	case testDB.skip != "":
		t.Skip(testDB.skip)
	}
	root := secretsDB(t)
	name := fmt.Sprintf("%s%d", testDBPrefix, time.Now().UnixNano())
	if _, err := root.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE IF EXISTS " + name) })
	return &adoptDB{t: t, name: name, worker: newFakeWorker(t), db: openDB(t, name)}
}

// A webapp on the database taking over runs, with the environment
// variables in extra.
func (d *adoptDB) start(extra ...string) *app {
	d.t.Helper()
	a := startApp(d.t, d.worker.url, append([]string{"DB_NAME=" + d.name, "RESUME_RUNS=true"}, extra...)...)
	a.worker = d.worker
	return a
}

// Every replica of a test judges the others by the same timeout, short so
// one killed is soon taken for stopped.
const adoptTimeout = "REPLICA_TIMEOUT=3s"

// A run's moves to other replicas, as its panel shows them.
type adoptMove struct {
	From string `json:"from"`
	To   string `json:"to"`
	Host string `json:"host"`
	Lost []struct {
		Step    int64  `json:"step"`
		Process string `json:"process"`
		Retried bool   `json:"retried"`
	} `json:"lost"`
}

func adoptMoves(a *app, run int64) []adoptMove {
	a.t.Helper()
	var d struct {
		Moves []adoptMove `json:"moves"`
	}
	a.call("GET", fmt.Sprintf("/api/runs/%d", run), nil, http.StatusOK, &d)
	return d.Moves
}

// Waits for the run to be taken over, and returns it then.
func adoptWait(a *app, run int64) runDetail {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for len(adoptMoves(a, run)) == 0 {
		if time.Now().After(deadline) {
			a.t.Fatalf("run %d wasn't taken over", run)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return a.getRun(run)
}

// Waits longer than finish, for a run whose replica must be taken for
// stopped first.
func adoptFinish(a *app, run int64) runDetail {
	a.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		d := a.getRun(run)
		if d.Status != runRunning {
			return d
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("run %d didn't end; it is %s with steps %s", run, d.Status, describeSteps(d))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Whether the run's log holds text.
func adoptLogged(a *app, run int64, text string) bool {
	a.t.Helper()
	for _, l := range a.runLogs(run, 0) {
		if strings.Contains(l.Message, text) {
			return true
		}
	}
	return false
}

// A draft a person reviews, approving it with a comment they fill in, which
// Publish takes with the draft approved.
type adoptReview struct {
	flow           flow
	review         process
	notes, comment object
	approved       objectState
}

func newAdoptReview(a *app, name string) adoptReview {
	a.t.Helper()
	var b adoptReview
	b.flow = a.newFlow(name)
	f := b.flow.ID
	draft := a.agent(f, "Draft", "Draft it.")
	b.notes = a.newObject(f, "Notes")
	b.approved = a.newState(b.notes.ID, "approved")
	b.review = humanHuman(a, f, "Review")
	b.comment = a.newObject(f, "Comment")
	publish := a.agent(f, "Publish", "Publish.")
	a.output(draft.ID, b.notes.ID)
	a.input(b.review.ID, b.notes.ID)
	humanDecides(a, b.review.ID, b.notes.ID, b.approved.ID)
	a.output(b.review.ID, b.comment.ID)
	humanTakesIn(a, publish.ID, b.notes.ID, b.approved.ID)
	a.input(publish.ID, b.comment.ID)
	a.output(publish.ID, a.newObject(f, "Post").ID)
	a.startAt(f, draft.ID)
	a.worker.onAgent("Draft", handsIn(data("the notes")))
	a.worker.onAgent("Publish", handsIn(data("published")))
	return b
}

// Approves the review with the comment.
func (b adoptReview) approve(a *app, run, step int64, comment string) {
	a.t.Helper()
	humanMustDecide(a, run, step, map[string]any{"state": b.approved.ID, "instructions": "Ship it.",
		"entries": map[string]any{fmt.Sprint(b.comment.ID): comment}})
}

// Checks the run went on from its review as if it had never moved: one
// draft, the review it waited on, and Publish taking what the person
// decided.
func (b adoptReview) check(t *testing.T, d runDetail, review int64, comment string) {
	t.Helper()
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if n := len(stepsOf(d, "Draft")); n != 1 {
		t.Errorf("Draft ran %d times; steps %s", n, describeSteps(d))
	}
	if r := onlyStep(t, d, "Review"); r.ID != review || r.Outcome != "approved" || r.Output != comment {
		t.Errorf("the review is step %d, %q, %q; want step %d, approved, %q", r.ID, r.Outcome, r.Output, review, comment)
	}
	p := onlyStep(t, d, "Publish")
	if p.ChangeRequest == nil || p.ChangeRequest.Instructions != "Ship it." {
		t.Errorf("Publish got the change request %+v", p.ChangeRequest)
	}
	if !slices.ContainsFunc(p.Inputs, func(in runInput) bool { return in.Name == "Comment" && in.Content == comment }) {
		t.Errorf("Publish took %+v, want the comment %q", p.Inputs, comment)
	}
}

// A review waiting outlives its replica stopping: the replica that starts
// next takes the run over and waits for the same review, which a person
// then decides there. One decided while no replica walks the run, its
// replica killed, is taken in as decided once another takes it over.
func TestAReviewOutlivesItsReplica(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	b := newAdoptReview(a, "Reviewed across restarts")

	// Told to stop, and started again alone, as in a deploy.
	rn := a.startRun(b.flow.ID)
	step := a.waitStep(rn.ID, "Review", stepWaiting)
	a.proc.stop()
	next := d.start(adoptTimeout)
	got := adoptWait(next, rn.ID)
	if got.Status != runRunning {
		t.Fatalf("taken over, the run is %s; steps %s", got.Status, describeSteps(got))
	}
	if r := onlyStep(t, got, "Review"); r.ID != step.ID || r.Status != stepWaiting || !r.CanDecide {
		t.Fatalf("the review is step %d %s, may decide %v; want step %d waiting", r.ID, r.Status, r.CanDecide, step.ID)
	}
	if !adoptLogged(next, rn.ID, "taken over by replica") {
		t.Errorf("the run's log doesn't say it was taken over: %+v", next.runLogs(rn.ID, 0))
	}
	if ms := adoptMoves(next, rn.ID); len(ms) != 1 || ms[0].To == "" || ms[0].From == ms[0].To || len(ms[0].Lost) != 0 {
		t.Errorf("the run's moves: %+v", ms)
	}
	b.approve(next, rn.ID, step.ID, "lgtm")
	b.check(t, next.finish(rn.ID), step.ID, "lgtm")

	// Killed, and decided at another replica before that one takes the run
	// over: the decision waits on the review's step.
	rn = next.startRun(b.flow.ID)
	step = next.waitStep(rn.ID, "Review", stepWaiting)
	other := d.start(adoptTimeout)
	next.proc.kill()
	b.approve(other, rn.ID, step.ID, "seen")
	b.check(t, adoptFinish(other, rn.ID), step.ID, "seen")
	if ms := adoptMoves(other, rn.ID); len(ms) != 1 {
		t.Errorf("the run moved %d times", len(ms))
	}
}

// A review's time limit goes on as it was set when its run moves: it fails
// once it has waited that long, and its failure connection handles it.
func TestAReviewsTimeLimitOutlivesItsReplica(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	b := newAdoptReview(a, "Timed out across a restart")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", b.review.ID), map[string]any{"settings": map[string]any{"waitAtMost": 6}}, http.StatusOK, nil)
	late := a.agent(b.flow.ID, "Chase", "Chase the reviewer.")
	a.call("POST", "/api/connections", map[string]any{"source": b.review.ID, "target": late.ID, "kind": connectionFailure}, http.StatusCreated, nil)
	a.output(late.ID, a.newObject(b.flow.ID, "Chased").ID)
	d.worker.onAgent("Chase", handsIn(data("chased")))

	rn := a.startRun(b.flow.ID)
	step := a.waitStep(rn.ID, "Review", stepWaiting)
	a.proc.stop()
	next := d.start(adoptTimeout)
	adoptWait(next, rn.ID)
	got := adoptFinish(next, rn.ID)
	if got.Status != runDone {
		t.Fatalf("run %s; steps %s", got.Status, describeSteps(got))
	}
	r := onlyStep(t, got, "Review")
	if r.ID != step.ID || r.Status != stepError || !strings.Contains(r.Error, "longer than its time limit") {
		t.Errorf("the review is step %d %s %q; want step %d timed out", r.ID, r.Status, r.Error, step.ID)
	}
	if took := r.FinishedAt.Sub(r.StartedAt); took < 5*time.Second || took > 10*time.Second {
		t.Errorf("the review timed out after %s, want 6s", took)
	}
	c := onlyStep(t, got, "Chase")
	if !slices.ContainsFunc(c.Inputs, func(in runInput) bool {
		return in.Name == "Failure of Review" && strings.Contains(in.Content, "time limit")
	}) {
		t.Errorf("Chase took %+v", c.Inputs)
	}
}

// An agent that works until its step is stopped the first time it runs,
// telling started, and hands in its name after.
func adoptLostOnce(name string, started chan<- int64) func(context.Context, agentRun) runResult {
	var mu sync.Mutex
	tried := map[int64]bool{}
	return func(ctx context.Context, run agentRun) runResult {
		mu.Lock()
		first := !tried[run.Run]
		tried[run.Run] = true
		mu.Unlock()
		if first {
			started <- run.Run
			<-ctx.Done()
			return runResult{Error: "stopped"}
		}
		return submitted(data(name + " done"))
	}
}

// A step going when its replica is killed is lost: the replica taking the
// run over fails it, saying so, and tries it again if its retries let it;
// without, it fails as any step does, and its failure connection handles
// it.
func TestAStepGoingIsTriedAgainOnAnotherReplica(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	b := d.start(adoptTimeout)
	started := make(chan int64, 4)

	f := a.newFlow("Tried again elsewhere")
	work := a.agent(f.ID, "Work", "Work.")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", work.ID), map[string]any{"settings": map[string]any{"retries": 1}}, http.StatusOK, nil)
	result := a.newObject(f.ID, "Result")
	after := a.agent(f.ID, "After", "Go on.")
	a.output(work.ID, result.ID)
	a.input(after.ID, result.ID)
	a.output(after.ID, a.newObject(f.ID, "Done").ID)
	a.startAt(f.ID, work.ID)
	d.worker.onAgent("Work", adoptLostOnce("Work", started))
	d.worker.onAgent("After", handsIn(data("after")))

	rn := a.startRun(f.ID)
	receive(t, started, "Work starting")
	a.proc.kill()
	got := adoptFinish(b, rn.ID)
	if got.Status != runDone {
		t.Fatalf("run %s; steps %s", got.Status, describeSteps(got))
	}
	ws := stepsOf(got, "Work")
	if len(ws) != 2 || ws[0].Status != stepError || !strings.Contains(ws[0].Error, "stopped before it finished") ||
		ws[1].Status != stepDone || ws[1].Try == nil || *ws[1].Try != 2 {
		t.Fatalf("Work's steps: %s", describeSteps(got))
	}
	if !slices.Equal(ws[1].Inputs, ws[0].Inputs) {
		t.Errorf("tried again on %+v, first on %+v", ws[1].Inputs, ws[0].Inputs)
	}
	if s := onlyStep(t, got, "After"); !strings.Contains(fmt.Sprint(s.Inputs), "Work done") {
		t.Errorf("After took %+v", s.Inputs)
	}
	ms := adoptMoves(b, rn.ID)
	if len(ms) != 1 || len(ms[0].Lost) != 1 || ms[0].Lost[0].Step != ws[0].ID || ms[0].Lost[0].Process != "Work" || !ms[0].Lost[0].Retried {
		t.Errorf("the run's moves: %+v", ms)
	}
	if !adoptLogged(b, rn.ID, `"Work" was lost`) {
		t.Errorf("the log doesn't say Work was lost: %+v", b.runLogs(rn.ID, 0))
	}

	// No tries left: its failure connection takes the failure.
	g := b.newFlow("Lost and handled")
	toil := b.agent(g.ID, "Toil", "Toil.")
	b.output(toil.ID, b.newObject(g.ID, "Toiled").ID)
	handle := b.agent(g.ID, "Handle", "Handle it.")
	b.output(handle.ID, b.newObject(g.ID, "Handled").ID)
	b.call("POST", "/api/connections", map[string]any{"source": toil.ID, "target": handle.ID, "kind": connectionFailure}, http.StatusCreated, nil)
	b.startAt(g.ID, toil.ID)
	d.worker.onAgent("Toil", adoptLostOnce("Toil", started))
	d.worker.onAgent("Handle", handsIn(data("handled")))
	other := d.start(adoptTimeout)
	rn = b.startRun(g.ID)
	receive(t, started, "Toil starting")
	b.proc.kill()
	got = adoptFinish(other, rn.ID)
	if got.Status != runDone {
		t.Fatalf("run %s; steps %s", got.Status, describeSteps(got))
	}
	if s := onlyStep(t, got, "Toil"); s.Status != stepError || !strings.Contains(s.Error, "stopped before it finished") {
		t.Errorf("Toil: %s %q", s.Status, s.Error)
	}
	h := onlyStep(t, got, "Handle")
	if !slices.ContainsFunc(h.Inputs, func(in runInput) bool {
		return in.Name == "Failure of Toil" && strings.Contains(in.Content, "stopped before it finished")
	}) {
		t.Errorf("Handle took %+v", h.Inputs)
	}
	if ms := adoptMoves(other, rn.ID); len(ms) != 1 || len(ms[0].Lost) != 1 || ms[0].Lost[0].Retried {
		t.Errorf("the run's moves: %+v", ms)
	}
}

// A flow process's board goes on from where it was, whether it runs in the
// run or records a run of its own, which moves with it.
func TestAFlowProcessBoardIsTakenOver(t *testing.T) {
	d := newAdoptDB(t)
	walking := d.start(adoptTimeout)
	started := make(chan int64, 4)
	for _, recording := range []bool{false, true} {
		a := walking
		n := newNestedTriage(t, a, recording)
		a.call("PATCH", fmt.Sprintf("/api/processes/%d", n.read.ID), map[string]any{"settings": map[string]any{"retries": 1}}, http.StatusOK, nil)
		read := adoptLostOnce("Read", started)
		d.worker.onAgent("Read", func(ctx context.Context, run agentRun) runResult {
			res := read(ctx, run)
			if res.Error == "" {
				return submitted(data("notes on the crash"))
			}
			return res
		})
		walking = d.start(adoptTimeout)
		rn := a.startRun(n.flow.ID)
		receive(t, started, "Read starting")
		a.proc.kill()
		got := adoptFinish(walking, rn.ID)
		if got.Status != runDone {
			t.Fatalf("recording %v: run %s; steps %s", recording, got.Status, describeSteps(got))
		}
		inner := got
		fs := onlyStep(t, got, "Summarize")
		if recording {
			if fs.ChildRun == nil {
				t.Fatalf("Summarize ran no run of its own")
			}
			if cs := nestedChildren(walking, n.flow.ID, rn.ID); len(cs) != 1 {
				t.Errorf("Summarize ran %d runs", len(cs))
			}
			inner = walking.getRun(*fs.ChildRun)
			if ms := adoptMoves(walking, inner.ID); len(ms) != 1 || len(ms[0].Lost) != 1 {
				t.Errorf("the run of Summarize moved: %+v", ms)
			}
		}
		if rs := stepsOf(inner, "Read"); len(rs) != 2 || rs[0].Status != stepError || rs[1].Status != stepDone {
			t.Errorf("recording %v: Read's steps %s", recording, describeSteps(inner))
		}
		if s := onlyStep(t, inner, "Sum"); s.Status != stepDone {
			t.Errorf("Sum %s", s.Status)
		}
		for _, name := range []string{"Write", "Summarize", "Publish"} {
			if s := onlyStep(t, got, name); s.Status != stepDone {
				t.Errorf("recording %v: %s %s %q", recording, name, s.Status, s.Error)
			}
		}
		if p := onlyStep(t, got, "Publish"); !strings.Contains(fmt.Sprint(p.Inputs), "a summary") {
			t.Errorf("Publish took %+v", p.Inputs)
		}
	}
}

// The items of a list a flow process runs for go on, whether each runs as
// a run of its own or in the run: those done stay done, and the one going
// when its replica was killed goes again.
func TestItemsAreTakenOver(t *testing.T) {
	d := newAdoptDB(t)
	walking := d.start(adoptTimeout)
	for _, recording := range []bool{true, false} {
		a := walking
		n := newNestedLists(t, a, recording, false)
		a.call("PATCH", fmt.Sprintf("/api/processes/%d", n.fix.ID), map[string]any{"settings": map[string]any{"retries": 1}}, http.StatusOK, nil)
		d.worker.onAgent("Find", handsIn(nestedTitles("A", "B", "C")))
		started := make(chan int64, 4)
		var mu sync.Mutex
		fixed := 0
		tried := false
		d.worker.onAgent("Fix", func(ctx context.Context, run agentRun) runResult {
			res := nestedEcho("fixed ")(ctx, run)
			mu.Lock()
			first := strings.Contains(run.Prompt, `"title": "B"`) && !tried
			if first {
				tried = true
			} else {
				fixed++
			}
			mu.Unlock()
			if first {
				started <- run.Run
				<-ctx.Done()
				return runResult{Error: "stopped"}
			}
			return res
		})
		walking = d.start(adoptTimeout)
		rn := a.startRun(n.flow.ID)
		receive(t, started, "Fix of B starting")
		// A and C fixed, B going.
		a.waitRun(rn.ID, "A and C to be fixed", func(runDetail) bool {
			mu.Lock()
			defer mu.Unlock()
			return fixed == 2
		})
		time.Sleep(200 * time.Millisecond)
		a.proc.kill()
		got := adoptFinish(walking, rn.ID)
		if got.Status != runDone {
			t.Fatalf("recording %v: run %s; steps %s", recording, got.Status, describeSteps(got))
		}
		if hs := stepsOf(got, "Handle"); len(hs) != 3 {
			t.Errorf("recording %v: Handle has %d steps, want one per item: %s", recording, len(hs), describeSteps(got))
		}
		fixes := stepsOf(got, "Fix")
		if recording {
			cs := nestedChildren(walking, n.flow.ID, rn.ID)
			if keys := nestedItemKeys(cs); len(cs) != 3 || fmt.Sprint(keys) != "[item 1 item 2 item 3]" {
				t.Errorf("the items' runs: %+v", keys)
			}
			for _, c := range cs {
				fixes = append(fixes, stepsOf(walking.getRun(c.ID), "Fix")...)
			}
		}
		var statuses []string
		for _, s := range fixes {
			statuses = append(statuses, s.Status)
		}
		slices.Sort(statuses)
		if fmt.Sprint(statuses) != "[done done done error]" {
			t.Errorf("recording %v: the items' Fix steps: %v", recording, statuses)
		}
		rp := onlyStep(t, got, "Report")
		in := fmt.Sprint(rp.Inputs)
		if a1, b1, c1 := strings.Index(in, "fixed A"), strings.Index(in, "fixed B"), strings.Index(in, "fixed C"); a1 < 0 || !(a1 < b1 && b1 < c1) {
			t.Errorf("recording %v: Report took %s", recording, in)
		}
	}
}

// Two replicas alive taking over the runs of one killed take each run over
// once: each moves once, and each step it lost goes again once.
func TestTwoReplicasTakeOverEachRunOnce(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	f := a.newFlow("Raced for")
	work := a.agent(f.ID, "Work", "Work.")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", work.ID), map[string]any{"settings": map[string]any{"retries": 1}}, http.StatusOK, nil)
	a.output(work.ID, a.newObject(f.ID, "Result").ID)
	a.startAt(f.ID, work.ID)
	started := make(chan int64, 8)
	var mu sync.Mutex
	calls := map[int64]int{}
	lost := adoptLostOnce("Work", started)
	d.worker.onAgent("Work", func(ctx context.Context, run agentRun) runResult {
		mu.Lock()
		calls[run.Run]++
		mu.Unlock()
		return lost(ctx, run)
	})
	b, c := d.start(adoptTimeout), d.start(adoptTimeout)
	// As many as the replica runs steps at once, PARALLEL_STEPS.
	var runs []int64
	for range 3 {
		runs = append(runs, a.startRun(f.ID).ID)
	}
	for range runs {
		receive(t, started, "Work starting")
	}
	a.proc.kill()
	took := map[string]int{}
	for _, id := range runs {
		got := adoptFinish(b, id)
		if got.Status != runDone {
			t.Errorf("run %d %s; steps %s", id, got.Status, describeSteps(got))
		}
		if n := len(stepsOf(got, "Work")); n != 2 {
			t.Errorf("run %d: Work has %d steps: %s", id, n, describeSteps(got))
		}
		ms := adoptMoves(c, id)
		if len(ms) != 1 {
			t.Errorf("run %d moved %d times: %+v", id, len(ms), ms)
			continue
		}
		took[ms[0].To]++
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range runs {
		if calls[id] != 2 {
			t.Errorf("run %d: Work was asked for %d times, want 2", id, calls[id])
		}
	}
	t.Logf("the replicas took over %v", took)
}

// The run of a flow a subflow process runs moves with the run it is in,
// and goes on on the version of that flow it started on, though the flow
// changed meanwhile.
func TestASubflowsRunIsTakenOver(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	s := verBuildSubflow(a)
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", s.fix.ID), map[string]any{"settings": map[string]any{"retries": 1}}, http.StatusOK, nil)
	started := make(chan int64, 2)
	d.worker.onAgent("Fix", adoptLostOnce("Fix", started))
	b := d.start(adoptTimeout)
	rn := a.startRun(s.caller.ID)
	receive(t, started, "Fix starting")
	a.proc.kill()
	versions := len(verVersions(b, s.callee.ID).Versions)
	b.call("PATCH", fmt.Sprintf("/api/processes/%d", s.fix.ID), map[string]any{"prompt": "Fix it another way."}, http.StatusOK, nil)
	got := adoptFinish(b, rn.ID)
	if got.Status != runDone {
		t.Fatalf("run %s; steps %s", got.Status, describeSteps(got))
	}
	cs := onlyStep(t, got, "Call")
	if cs.ChildRun == nil {
		t.Fatalf("Call ran no run")
	}
	child := b.getRun(*cs.ChildRun)
	if fs := stepsOf(child, "Fix"); len(fs) != 2 || fs[0].Status != stepError || fs[1].Status != stepDone {
		t.Errorf("Fix's steps %s", describeSteps(child))
	}
	if n := len(verVersions(b, s.callee.ID).Versions); n != versions {
		t.Errorf("the callee has %d versions, had %d: taking the run over saved one", n, versions)
	}
	fixes := d.worker.runsOf("Fix")
	if len(fixes) != 2 || strings.Contains(fixes[1].Prompt, "another way") || !strings.Contains(fixes[1].Prompt, "Fix the ticket.") {
		t.Errorf("Fix went again on %+v, want the version its run started on", fixes)
	}
	if ps := onlyStep(t, got, "Publish"); !strings.Contains(fmt.Sprint(ps.Inputs), "Fix done") {
		t.Errorf("Publish took %+v", ps.Inputs)
	}
	if ms := adoptMoves(b, child.ID); len(ms) != 1 {
		t.Errorf("the subflow's run moved %d times", len(ms))
	}
}

// A re-run moves as any run: it goes on from the process it re-runs from,
// on what was put in its place, without copying anew.
func TestARerunIsTakenOver(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	r := newRerunFlow(a)
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", r.count.ID), map[string]any{"settings": map[string]any{"retries": 1}}, http.StatusOK, nil)
	first := a.finish(a.startRun(r.f.ID).ID)
	if first.Status != runDone {
		t.Fatalf("first run %s; steps %s", first.Status, describeSteps(first))
	}
	started := make(chan int64, 2)
	var mu sync.Mutex
	counted := map[int64]int{}
	d.worker.onScript("Count", func(ctx context.Context, run scriptRun) runResult {
		mu.Lock()
		counted[run.Run]++
		n := counted[run.Run]
		mu.Unlock()
		if run.Run != first.ID && n == 1 {
			started <- run.Run
			<-ctx.Done()
			return runResult{Error: "stopped"}
		}
		for _, in := range run.Inputs {
			if in.Name == "Ticket" {
				return runResult{OK: true, Output: fmt.Sprintf(`{"data": %d}`, len(in.Content))}
			}
		}
		return runResult{Error: "no ticket"}
	})
	b := d.start(adoptTimeout)
	ticket := `{"data": "Fix the logout"}`
	var rn run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, onlyStep(t, first, "Count").ID),
		map[string]any{"inputs": map[string]string{fmt.Sprint(r.ticket.ID): ticket}}, http.StatusCreated, &rn)
	receive(t, started, "Count starting")
	a.proc.kill()
	got := adoptFinish(b, rn.ID)
	if got.Status != runDone {
		t.Fatalf("re-run %s; steps %s", got.Status, describeSteps(got))
	}
	if len(stepsOf(got, "Draft"))+len(stepsOf(got, "Label")) != 0 {
		t.Errorf("the re-run ran what came before Count: %s", describeSteps(got))
	}
	onlyStep(t, got, fmt.Sprintf("Copied from run #%d", first.ID))
	if cs := stepsOf(got, "Count"); len(cs) != 2 || cs[1].Status != stepDone || len(cs[1].Inputs) != 1 || cs[1].Inputs[0].Content != ticket {
		t.Errorf("Count's steps %s, the last taking %+v", describeSteps(got), cs)
	}
	if res := contentsOf(got, "Result"); len(res) != 1 || !strings.Contains(res[0], "Fix the logout") {
		t.Errorf("Result held %q", res)
	}
}

// A loop's question waiting outlives its replica, and the loop goes on
// counting as it did: let go on there, it asks again only once its process
// used up as many more rounds.
func TestALoopsQuestionOutlivesItsReplica(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	f, _ := loopsFailing(a, 2, humanCounting("issue"))
	rn := a.startRun(f.ID)
	asked := a.waitStep(rn.ID, "Fix", stepWaiting)
	a.proc.stop()
	b := d.start(adoptTimeout)
	got := adoptWait(b, rn.ID)
	if qs := loopsAsked(got, "Fix"); len(qs) != 1 || qs[0].ID != asked.ID || qs[0].Status != stepWaiting {
		t.Fatalf("taken over, the loop's questions are %+v", qs)
	}
	loopsAnswer(b, rn.ID, asked.ID, true)
	again := b.waitStep(rn.ID, "Fix", stepWaiting, asked.ID)
	if !strings.Contains(again.Prompt, "has run 4 times") {
		t.Errorf("asked again %q", again.Prompt)
	}
	loopsAnswer(b, rn.ID, again.ID, false)
	got = b.finish(rn.ID)
	if got.Status != runFailed {
		t.Fatalf("run %s, want failed once stopped; steps %s", got.Status, describeSteps(got))
	}
	if f, r := len(loopsRuns(got, "Fix")), len(loopsRuns(got, "Rework")); f != 4 || r != 4 {
		t.Errorf("Fix ran %d times and Rework %d, want 4 each; steps %s", f, r, describeSteps(got))
	}
}
