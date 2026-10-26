package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"
)

// Schedules here run every second, with the seconds field the tests'
// webapps take (see SCHEDULE_SECONDS in the harness), so their tests take
// seconds rather than minutes. Others run a minute apart at the least.
const everySecond = "* * * * * *"

// The tests share their database, so the schedule is deleted once the test
// is done, before another test's webapp could run it.
func (a *app) newSchedule(f int64, cron string) schedule {
	a.t.Helper()
	var s schedule
	a.call("POST", "/api/schedules", map[string]any{"flow": f, "cron": cron, "timezone": "UTC"}, http.StatusCreated, &s)
	a.t.Cleanup(func() { a.do("DELETE", fmt.Sprintf("/api/schedules/%d", s.ID), nil) })
	return s
}

// The runs of the flow the schedule started, newest first.
func (a *app) scheduledRuns(f, s int64) []run {
	a.t.Helper()
	var rs, of []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d", f), nil, http.StatusOK, &rs)
	for _, rn := range rs {
		if rn.Schedule != nil && *rn.Schedule == s {
			of = append(of, rn)
		}
	}
	return of
}

// Polls until the schedule started at least n runs, and returns them.
func (a *app) waitScheduledRuns(f, s int64, n int) []run {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rs := a.scheduledRuns(f, s)
		if len(rs) >= n {
			return rs
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("schedule %d: timed out waiting for %d runs; it started %d", s, n, len(rs))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A schedule runs its flow on its own, a run each time it comes round, and
// stops once disabled.
func TestScheduleRunsItsFlow(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Tick")
	tick := a.newProcess(f.ID, map[string]any{"name": "Tick", "kind": kindScript, "script": "date"})
	a.startAt(f.ID, tick.ID)
	a.worker.onScript("Tick", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"data": "tick"}`}
	})

	s := a.newSchedule(f.ID, everySecond)
	if !s.Enabled || s.NextRunAt == nil || s.FlowName != "Tick" || s.LastRun != nil {
		t.Fatalf("new schedule = %+v, want enabled with a next run, and none yet", s)
	}
	rs := a.waitScheduledRuns(f.ID, s.ID, 2)
	for _, rn := range rs {
		if d := a.finish(rn.ID); d.Status != runDone || rn.Version == nil {
			t.Errorf("scheduled run %d %s on version %v, want done on one; steps %s", rn.ID, d.Status, rn.Version, describeSteps(d))
		}
	}

	var all []schedule
	a.call("GET", fmt.Sprintf("/api/schedules?flow=%d", f.ID), nil, http.StatusOK, &all)
	if len(all) != 1 || all[0].LastRun == nil || all[0].LastRun.Schedule == nil || *all[0].LastRun.Schedule != s.ID {
		t.Errorf("the flow's schedules = %+v, want this one with its last run", all)
	}

	var got schedule
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"enabled": false}, http.StatusOK, &got)
	if got.Enabled || got.NextRunAt != nil {
		t.Errorf("disabled schedule = %+v, want no next run", got)
	}
	// A run claimed just before it was disabled may still start.
	time.Sleep(1500 * time.Millisecond)
	before := len(a.scheduledRuns(f.ID, s.ID))
	time.Sleep(2500 * time.Millisecond)
	if after := len(a.scheduledRuns(f.ID, s.ID)); after != before {
		t.Errorf("the disabled schedule went from %d runs to %d", before, after)
	}

	// Enabled again, it counts on from now.
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"enabled": true}, http.StatusOK, &got)
	if got.NextRunAt == nil || got.NextRunAt.Before(time.Now().Add(-time.Second)) {
		t.Errorf("enabled schedule runs next at %v, want from now on", got.NextRunAt)
	}
	a.waitScheduledRuns(f.ID, s.ID, before+1)

	// Runs started by hand aren't the schedule's.
	if rn := a.startRun(f.ID); rn.Schedule != nil {
		t.Errorf("a run started by hand has schedule %d", *rn.Schedule)
	}
}

// While a scheduled run is still going, the times the schedule comes round
// are skipped, rather than piling runs up; once it ends, runs start again.
func TestScheduleSkipsWhileItsRunGoes(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Slow")
	work := a.agent(f.ID, "Work", "Take your time.")
	a.startAt(f.ID, work.ID)
	release := make(chan struct{})
	a.worker.onAgent("Work", func(ctx context.Context, _ agentRun) runResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return handsIn(data("done"))(ctx, agentRun{})
	})

	s := a.newSchedule(f.ID, everySecond)
	first := a.waitScheduledRuns(f.ID, s.ID, 1)[0]
	a.waitStep(first.ID, "Work", stepActive)
	time.Sleep(3 * time.Second)
	if rs := a.scheduledRuns(f.ID, s.ID); len(rs) != 1 {
		t.Fatalf("the schedule started %d runs while its first went on, want 1", len(rs))
	}
	close(release)
	if d := a.finish(first.ID); d.Status != runDone {
		t.Errorf("first run %s, want done", d.Status)
	}
	a.waitScheduledRuns(f.ID, s.ID, 2)

	// Deleting the schedule keeps its runs, as runs no schedule started.
	a.call("DELETE", fmt.Sprintf("/api/schedules/%d", s.ID), nil, http.StatusNoContent, nil)
	got := a.getRun(first.ID)
	if got.Schedule != nil {
		t.Errorf("run of a deleted schedule has schedule %d", *got.Schedule)
	}
}

// A scheduled run's scripts are told when the schedule's last run that
// ended done started, so they can fetch only what changed since; a failed
// run doesn't move it on, and a run started by hand isn't told.
func TestScheduleTellsScriptsSince(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Poll")
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "echo $SINCE"})
	a.startAt(f.ID, fetch.ID)
	// The schedule's runs go one at a time, so the calls come in their order.
	var mu sync.Mutex
	var since []*int64
	a.worker.onScript("Fetch", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		since = append(since, run.Since)
		if len(since) == 2 {
			return runResult{Error: "GitHub is down"}
		}
		return runResult{OK: true, Output: `{"data": "fetched"}`}
	})

	s := a.newSchedule(f.ID, everySecond)
	a.waitScheduledRuns(f.ID, s.ID, 4)
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"enabled": false}, http.StatusOK, nil)
	rs := a.scheduledRuns(f.ID, s.ID)
	slices.Reverse(rs)
	for _, rn := range rs {
		a.finish(rn.ID)
	}
	if got := a.finish(rs[1].ID).Status; got == runDone {
		t.Fatalf("the second run is %s, want it to have failed", got)
	}

	mu.Lock()
	got := slices.Clone(since[:4])
	mu.Unlock()
	first, third := rs[0].StartedAt.Unix(), rs[2].StartedAt.Unix()
	want := []*int64{nil, &first, &first, &third}
	for i := range want {
		switch {
		case (got[i] == nil) != (want[i] == nil):
			t.Errorf("run %d's script was told since %v, want %v", i+1, show(got[i]), show(want[i]))
		case got[i] != nil && *got[i] != *want[i]:
			t.Errorf("run %d's script was told since %d, want %d", i+1, *got[i], *want[i])
		}
	}

	a.finish(a.startRun(f.ID).ID)
	mu.Lock()
	byHand := since[len(since)-1]
	mu.Unlock()
	if byHand != nil {
		t.Errorf("a run started by hand was told since %d, want nothing", *byHand)
	}
}

// What a pointer holds, or nil.
func show(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Only what can come round is saved, for a flow that runs.
func TestScheduleRefusals(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Job")
	post := func(body map[string]any, want int, msg string) {
		t.Helper()
		a.refused("POST", "/api/schedules", body, want, msg)
	}
	post(map[string]any{"flow": f.ID, "cron": "every minute", "timezone": "UTC"}, http.StatusBadRequest, "invalid cron expression")
	post(map[string]any{"flow": f.ID, "cron": "* * * * *", "timezone": "Mars/Base"}, http.StatusBadRequest, "unknown time zone")
	post(map[string]any{"flow": f.ID, "cron": "* * * * *", "timezone": ""}, http.StatusBadRequest, "time zone is required")
	post(map[string]any{"flow": f.ID, "cron": "CRON_TZ=UTC * * * * *", "timezone": "UTC"}, http.StatusBadRequest, "time zone on its own")
	post(map[string]any{"flow": f.ID, "cron": "0 0 30 2 *", "timezone": "UTC"}, http.StatusBadRequest, "never comes round")
	post(map[string]any{"flow": f.ID + 1000, "cron": "* * * * *", "timezone": "UTC"}, http.StatusNotFound, "flow not found")
	var st flow
	a.call("POST", "/api/flows", map[string]any{"name": "Ticket", "struct": true}, http.StatusCreated, &st)
	post(map[string]any{"flow": st.ID, "cron": "* * * * *", "timezone": "UTC"}, http.StatusBadRequest, "a struct holds a single object")

	// Five fields are minutes, hours, days, months and weekdays, in the zone given.
	var preview struct {
		Upcoming []time.Time `json:"upcoming"`
	}
	q := url.Values{"cron": {"30 9 * * 1-5"}, "timezone": {"Europe/London"}}
	a.call("GET", "/api/schedules/preview?"+q.Encode(), nil, http.StatusOK, &preview)
	london, _ := time.LoadLocation("Europe/London")
	if len(preview.Upcoming) != 5 {
		t.Fatalf("preview gave %d times, want 5", len(preview.Upcoming))
	}
	for i, at := range preview.Upcoming {
		l := at.In(london)
		if l.Hour() != 9 || l.Minute() != 30 || l.Weekday() == time.Saturday || l.Weekday() == time.Sunday || (i > 0 && !at.After(preview.Upcoming[i-1])) {
			t.Errorf("preview time %d is %v, want weekdays at 9:30 in London, in order", i, l)
		}
	}
	a.refused("GET", "/api/schedules/preview?cron=nope&timezone=UTC", nil, http.StatusBadRequest, "invalid cron expression")

	s := a.newSchedule(f.ID, "@hourly")
	a.refused("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"cron": "61 * * * *"}, http.StatusBadRequest, "invalid cron expression")
	a.refused("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"flow": st.ID}, http.StatusBadRequest, "a struct holds a single object")
	var moved schedule
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"cron": "0 12 * * *"}, http.StatusOK, &moved)
	if moved.NextRunAt == nil || moved.NextRunAt.UTC().Hour() != 12 || moved.NextRunAt.Minute() != 0 {
		t.Errorf("rescheduled at noon, it runs next at %v", moved.NextRunAt)
	}

	// A flow's schedules go with it, deleted for good.
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/trash/%d", f.ID), nil, http.StatusNoContent, nil)
	var all []schedule
	a.call("GET", "/api/schedules", nil, http.StatusOK, &all)
	for _, o := range all {
		if o.ID == s.ID {
			t.Errorf("schedule %+v outlived its flow", o)
		}
	}
	a.refused("DELETE", fmt.Sprintf("/api/schedules/%d", s.ID), nil, http.StatusNotFound, "schedule not found")
}
