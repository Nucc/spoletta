package tests

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

// The ids of the flow's runs the list gives with the query, in its order.
func listedRuns(a *app, f int64, query string) []int64 {
	a.t.Helper()
	var rs []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d%s", f, query), nil, http.StatusOK, &rs)
	ids := []int64{}
	for _, rn := range rs {
		ids = append(ids, rn.ID)
	}
	return ids
}

// The run list picks runs by how they stand, a run waiting for a person
// apart from one going, and by how they started, over all of the flow's
// runs a page at a time; a re-run shows whether or not the run it re-runs
// does.
func TestRunListFiltersByStatusAndStart(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Listed")
	draft := a.agent(f.ID, "Draft", "Write a ticket.")
	ticket := a.newObject(f.ID, "Ticket")
	review := humanHuman(a, f.ID, "Review")
	a.output(draft.ID, ticket.ID)
	a.input(review.ID, ticket.ID)
	a.startAt(f.ID, draft.ID)

	// Draft fails or succeeds as each run needs, in the order they start.
	var mu sync.Mutex
	var fails []bool
	a.worker.onAgent("Draft", func(context.Context, agentRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		fail := fails[0]
		fails = fails[1:]
		if fail {
			return runResult{Error: "the model is down"}
		}
		return submitted(data("Fix the login"))
	})
	next := func(fail bool) {
		mu.Lock()
		fails = append(fails, fail)
		mu.Unlock()
	}

	next(true)
	failed := a.finish(a.startRun(f.ID).ID)
	next(false)
	waiting := a.startRun(f.ID)
	a.waitStep(waiting.ID, "Review", stepWaiting)
	next(false)
	stopped := a.startRun(f.ID)
	a.waitStep(stopped.ID, "Review", stepWaiting)
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", stopped.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
	next(false)
	var rerun run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", failed.ID, onlyStep(t, failed, "Draft").ID), map[string]any{}, http.StatusCreated, &rerun)
	decide := a.waitStep(rerun.ID, "Review", stepWaiting)
	a.call("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rerun.ID, decide.ID), map[string]any{}, http.StatusNoContent, nil)
	if d := a.finish(rerun.ID); d.Status != runDone {
		t.Fatalf("re-run %s, want done; steps %s", d.Status, describeSteps(d))
	}
	next(true)
	byAPI := apiStart(a, apiNewKey(a, "Lister").Key, f.ID, nil, http.StatusCreated)
	a.finish(byAPI.ID)
	t.Cleanup(func() {
		a.do("PATCH", fmt.Sprintf("/api/runs/%d", waiting.ID), map[string]any{"status": runInterrupted})
	})

	var all []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusOK, &all)
	if len(all) != 5 {
		t.Fatalf("listed %d runs, want 5: %+v", len(all), all)
	}
	for _, rn := range all {
		if rn.Waiting != (rn.ID == waiting.ID) {
			t.Errorf("run %d %s listed as waiting %v", rn.ID, rn.Status, rn.Waiting)
		}
	}

	for _, c := range []struct {
		query string
		want  []int64
	}{
		{"", []int64{byAPI.ID, rerun.ID, stopped.ID, waiting.ID, failed.ID}},
		{"&status=waiting", []int64{waiting.ID}},
		{"&status=running", []int64{}},
		{"&status=running,waiting", []int64{waiting.ID}},
		{"&status=interrupted", []int64{stopped.ID}},
		{"&status=failed", []int64{byAPI.ID, failed.ID}},
		{"&status=failed&status=done", []int64{byAPI.ID, rerun.ID, failed.ID}},
		{"&status=failed&started=person", []int64{failed.ID}},
		{"&started=api", []int64{byAPI.ID}},
		// The re-run of a failed run shows on its own.
		{"&started=rerun", []int64{rerun.ID}},
		{"&status=done", []int64{rerun.ID}},
		{"&started=person", []int64{stopped.ID, waiting.ID, failed.ID}},
		{"&started=schedule,record,run", []int64{}},
		// A page at a time, the next starting after the last of one.
		{"&limit=2", []int64{byAPI.ID, rerun.ID}},
		{fmt.Sprintf("&limit=2&before=%d", rerun.ID), []int64{stopped.ID, waiting.ID}},
		{fmt.Sprintf("&limit=2&before=%d", waiting.ID), []int64{failed.ID}},
		{fmt.Sprintf("&started=person&limit=1&before=%d", stopped.ID), []int64{waiting.ID}},
	} {
		if got := listedRuns(a, f.ID, c.query); !slices.Equal(got, c.want) {
			t.Errorf("runs listed with %q = %v, want %v", c.query, got, c.want)
		}
	}

	a.refused("GET", fmt.Sprintf("/api/runs?flow=%d&status=stopped", f.ID), nil, http.StatusBadRequest, "status must be one of")
	a.refused("GET", fmt.Sprintf("/api/runs?flow=%d&started=cron", f.ID), nil, http.StatusBadRequest, "started must be one of")
	a.refused("GET", fmt.Sprintf("/api/runs?flow=%d&limit=500", f.ID), nil, http.StatusBadRequest, "limit must be")

	// A review waits for a person, not for a slot.
	if s := onlyStep(t, a.getRun(waiting.ID), "Review"); s.SlotWaitMS != nil || s.Try != nil {
		t.Errorf("the review recorded a wait of %v ms on try %v", s.SlotWaitMS, s.Try)
	}
}

// A step records which try of its process it is, and how long it waited for
// a slot: with one slot, of two processes going at once one waits for the
// other to end.
func TestStepsRecordTriesAndSlotWaits(t *testing.T) {
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, "PARALLEL_STEPS=1")
	a.worker = fw
	f := a.newFlow("Waits")
	draft := a.agent(f.ID, "Draft", "Write a ticket.")
	left := a.agent(f.ID, "Left", "Go left.")
	right := a.agent(f.ID, "Right", "Go right.")
	for _, p := range []int64{left.ID, right.ID} {
		a.call("POST", "/api/connections", map[string]any{"source": draft.ID, "target": p}, http.StatusCreated, nil)
	}
	a.startAt(f.ID, draft.ID)
	failuresSettings(a, draft.ID, processSettings{Retries: ptr(1)})

	const hold = 400 * time.Millisecond
	slow := func(context.Context, agentRun) runResult {
		time.Sleep(hold)
		return submitted(data("went"))
	}
	a.worker.onAgent("Draft", failuresFlaky(1, data("made")))
	a.worker.onAgent("Left", slow)
	a.worker.onAgent("Right", slow)

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	tries := stepsOf(d, "Draft")
	if len(tries) != 2 || tries[0].Try == nil || *tries[0].Try != 1 || tries[1].Try == nil || *tries[1].Try != 2 {
		t.Fatalf("Draft's steps = %+v, want tries 1 and 2", tries)
	}
	var waits []int64
	for _, name := range []string{"Draft", "Left", "Right"} {
		for _, s := range stepsOf(d, name) {
			if s.SlotWaitMS == nil {
				t.Fatalf("%s's step %d recorded no wait", name, s.ID)
			}
			if name != "Draft" {
				waits = append(waits, *s.SlotWaitMS)
			}
		}
	}
	slices.Sort(waits)
	if len(waits) != 2 || waits[0] > hold.Milliseconds()/2 || waits[1] < hold.Milliseconds()*3/4 {
		t.Errorf("Left and Right waited %v ms for their slots, want one about %v", waits, hold)
	}
}
