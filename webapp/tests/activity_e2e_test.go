package tests

// The Activity page: what goes on across the flows someone sees.

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

type activityRun struct {
	run
	FlowName      string `json:"flowName"`
	FailedProcess string `json:"failedProcess"`
	Error         string `json:"error"`
}

type activityRuns struct {
	Total int           `json:"total"`
	Runs  []activityRun `json:"runs"`
}

type activityFlow struct {
	Flow     int64     `json:"flow"`
	Name     string    `json:"name"`
	Module   bool      `json:"module"`
	Runs     []int     `json:"runs"`
	Failed   []int     `json:"failed"`
	Cost     []float64 `json:"cost"`
	Unpriced int64     `json:"unpriced"`
}

type activityProcess struct {
	Flow      int64  `json:"flow"`
	FlowName  string `json:"flowName"`
	Process   int64  `json:"process"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Steps     int    `json:"steps"`
	Failed    int    `json:"failed"`
	LastRun   int64  `json:"lastRun"`
	LastError string `json:"lastError"`
}

type activityReview struct {
	Flow       int64   `json:"flow"`
	FlowName   string  `json:"flowName"`
	Process    int64   `json:"process"`
	Name       string  `json:"name"`
	Decided    int     `json:"decided"`
	Median     float64 `json:"median"`
	P90        float64 `json:"p90"`
	Longest    float64 `json:"longest"`
	Changes    int     `json:"changes"`
	Runs       int     `json:"runs"`
	MostRounds int     `json:"mostRounds"`
}

type activityPage struct {
	Zone       string            `json:"zone"`
	Days       []string          `json:"days"`
	Running    activityRuns      `json:"running"`
	Failed     activityRuns      `json:"failed"`
	Total      activityFlow      `json:"total"`
	Flows      []activityFlow    `json:"flows"`
	Processes  []activityProcess `json:"processes"`
	Reviews    []activityReview  `json:"reviews"`
	AllReviews activityReview    `json:"allReviews"`
}

func activityOf(a *app, days int, zone string) activityPage {
	a.t.Helper()
	var p activityPage
	a.call("GET", fmt.Sprintf("/api/activity?days=%d&tz=%s", days, zone), nil, http.StatusOK, &p)
	return p
}

func (p activityPage) flow(id int64) *activityFlow {
	for i := range p.Flows {
		if p.Flows[i].Flow == id {
			return &p.Flows[i]
		}
	}
	return nil
}

func (l activityRuns) has(run int64) *activityRun {
	for i := range l.Runs {
		if l.Runs[i].ID == run {
			return &l.Runs[i]
		}
	}
	return nil
}

// Whether anything on the page is of the flow.
func (p activityPage) mentions(flow int64) bool {
	return p.flow(flow) != nil ||
		slices.ContainsFunc(p.Running.Runs, func(r activityRun) bool { return r.Flow == flow }) ||
		slices.ContainsFunc(p.Failed.Runs, func(r activityRun) bool { return r.Flow == flow }) ||
		slices.ContainsFunc(p.Processes, func(pf activityProcess) bool { return pf.Flow == flow }) ||
		slices.ContainsFunc(p.Reviews, func(rt activityReview) bool { return rt.Flow == flow })
}

// What the run has spent on its models, in millionths of a dollar, as it
// keeps it, and as the calls recorded counting toward it add up.
func activitySpent(t *testing.T, db *sql.DB, run int64) (kept, calls int64) {
	t.Helper()
	err := db.QueryRow("SELECT spent_micros, (SELECT COALESCE(SUM(cost_micros), 0) FROM llm_usage WHERE root_run_id = runs.id) FROM runs WHERE id = ?", run).
		Scan(&kept, &calls)
	if err != nil {
		t.Fatal(err)
	}
	return kept, calls
}

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Each day of the period counts the runs of a whole flow started on it in
// the viewer's time zone, how many of them failed, and what they spent on
// their models, which each run keeps as its calls are recorded. A run
// inside another isn't a run of its own flow, but its steps count toward
// the process they ran in.
func TestActivityRunsFailuresAndCost(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	f, build := buildsFlow(a, "Builds", func(n int) bool { return n == 2 })
	var runs []run
	for range 4 {
		rn := a.startRun(f.ID)
		a.finish(rn.ID)
		runs = append(runs, rn)
	}
	backdate(t, db, runs[0].ID, 49*time.Hour)
	backdate(t, db, runs[3].ID, 10*24*time.Hour)

	// A procedure's run inside the third, which failed there, spending.
	proc := a.newFlow("Packaging")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", proc.ID), map[string]any{"module": true}, http.StatusOK, nil)
	pack := a.agent(proc.ID, "Package", "Package it.")
	res, err := db.Exec("INSERT INTO runs (flow_id, status, parent_run_id, finished_at) VALUES (?, 'failed', ?, NOW(3))", proc.ID, runs[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO run_steps (run_id, process_ref, process_name, kind, prompt, inputs, status, output, error, instructions, finished_at)
		VALUES (?, ?, 'Package', 'agent', '', '[]', 'error', '', 'out of disk', '', NOW(3))`, child, pack.ID); err != nil {
		t.Fatal(err)
	}
	// Each run's agent reached its models, which the run kept count of.
	spent := map[int64]int64{}
	for _, rn := range runs {
		kept, calls := activitySpent(t, db, rn.ID)
		if kept <= 0 || kept != calls {
			t.Errorf("run %d keeps %d spent, its calls cost %d", rn.ID, kept, calls)
		}
		spent[rn.ID] = kept
	}
	if _, err := db.Exec("UPDATE runs SET unpriced_calls = 2 WHERE id = ?", runs[0].ID); err != nil {
		t.Fatal(err)
	}

	zone, err := time.LoadLocation("Europe/Budapest")
	if err != nil {
		t.Fatal(err)
	}
	page := activityOf(a, 7, "Europe/Budapest")
	if page.Zone != "Europe/Budapest" || len(page.Days) != 7 || page.Days[6] != time.Now().In(zone).Format(time.DateOnly) {
		t.Fatalf("the period is %v in %s", page.Days, page.Zone)
	}
	day := func(at time.Time) int { return slices.Index(page.Days, at.In(zone).Format(time.DateOnly)) }
	wantRuns, wantFailed, wantCost := make([]int, 7), make([]int, 7), make([]float64, 7)
	for i, rn := range runs[:3] {
		d := day(a.getRun(rn.ID).StartedAt)
		wantRuns[d]++
		if i == 1 {
			wantFailed[d]++
		}
		wantCost[d] += float64(spent[rn.ID]) / 1e6
	}
	got := page.flow(f.ID)
	if got == nil {
		t.Fatalf("the page has nothing of the flow: %+v", page.Flows)
	}
	if got.Name != "Builds" || !slices.Equal(got.Runs, wantRuns) || !slices.Equal(got.Failed, wantFailed) {
		t.Errorf("the flow's runs a day = %v, failed %v, want %v and %v", got.Runs, got.Failed, wantRuns, wantFailed)
	}
	for d := range 7 {
		if !approxEqual(got.Cost[d], wantCost[d]) {
			t.Errorf("the flow spent %v a day, want %v", got.Cost, wantCost)
			break
		}
	}
	if got.Unpriced != 2 {
		t.Errorf("the flow made %d calls without a price, want 2", got.Unpriced)
	}
	if page.flow(proc.ID) != nil {
		t.Errorf("the procedure's run inside another counts as one of its own: %+v", page.flow(proc.ID))
	}
	for d := range 7 {
		if page.Total.Runs[d] < got.Runs[d] || page.Total.Cost[d] < got.Cost[d]-1e-9 {
			t.Errorf("all flows on day %d: %d runs costing %v, less than one of them", d, page.Total.Runs[d], page.Total.Cost[d])
		}
	}

	// The failed run is listed with what went wrong; the run inside isn't.
	failed := page.Failed.has(runs[1].ID)
	if failed == nil || failed.FlowName != "Builds" || failed.FailedProcess != "Build" || !strings.Contains(failed.Error, "the compiler gave up") {
		t.Errorf("the failed run is listed as %+v", failed)
	}
	if page.Failed.has(child) != nil || page.Failed.has(runs[0].ID) != nil {
		t.Errorf("the failed runs list %+v", page.Failed.Runs)
	}
	if page.Running.has(runs[2].ID) != nil {
		t.Errorf("a run that ended is listed as going on")
	}

	var builds, packs *activityProcess
	for i, pf := range page.Processes {
		switch {
		case pf.Flow == f.ID && pf.Process == build.ID:
			builds = &page.Processes[i]
		case pf.Flow == proc.ID && pf.Process == pack.ID:
			packs = &page.Processes[i]
		}
	}
	if builds == nil || builds.Name != "Build" || builds.Kind != "agent" || builds.Steps != 3 || builds.Failed != 1 || builds.LastRun != runs[1].ID ||
		!strings.Contains(builds.LastError, "the compiler gave up") {
		t.Errorf("Build failing = %+v", builds)
	}
	if packs == nil || packs.FlowName != "Packaging" || packs.Failed != 1 || packs.LastRun != child || packs.LastError != "out of disk" {
		t.Errorf("Package failing = %+v", packs)
	}

	// Over 30 days the run from 10 days ago counts too.
	if got := page30(a).flow(f.ID); got == nil || sum(got.Runs) != 4 || sum(got.Failed) != 1 {
		t.Errorf("over 30 days the flow has %+v", got)
	}

	a.refused("GET", "/api/activity?days=12", nil, http.StatusBadRequest, "7, 30 or 90")
	if p := activityOf(a, 90, "Nowhere/Atlantis"); p.Zone != "UTC" || len(p.Days) != 90 {
		t.Errorf("in a time zone nobody knows the period is %d days in %s", len(p.Days), p.Zone)
	}
}

func page30(a *app) activityPage { return activityOf(a, 30, "UTC") }

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// A run waiting for a review is going on; once decided, the page says how
// long the review took, how many asked for changes, and how many rounds
// it went in the run.
func TestActivityReviews(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	first := a.waitStep(rn.ID, "Review", stepWaiting)
	if r := activityOf(a, 7, "UTC").Running.has(rn.ID); r == nil || r.FlowName != "Review" {
		t.Errorf("the run waiting for its review is listed as %+v", r)
	}
	humanMustDecide(a, rn.ID, first.ID, map[string]any{"state": b.changes.ID, "instructions": "Shorter, please."})
	second := a.waitStep(rn.ID, "Review", stepWaiting, first.ID)
	humanMustDecide(a, rn.ID, second.ID, map[string]any{"state": b.approved.ID})
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Fatalf("the run %s; steps %s", d.Status, describeSteps(d))
	}
	for step, waited := range map[int64]int{first.ID: 7200, second.ID: 600} {
		if _, err := db.Exec("UPDATE run_steps SET started_at = finished_at - INTERVAL ? SECOND WHERE id = ?", waited, step); err != nil {
			t.Fatal(err)
		}
	}

	page := activityOf(a, 7, "UTC")
	i := slices.IndexFunc(page.Reviews, func(r activityReview) bool { return r.Flow == b.flow.ID && r.Process == b.review.ID })
	if i < 0 {
		t.Fatalf("the reviews are %+v", page.Reviews)
	}
	want := activityReview{Flow: b.flow.ID, FlowName: "Review", Process: b.review.ID, Name: "Review", Decided: 2, Median: 600, P90: 7200, Longest: 7200,
		Changes: 1, Runs: 1, MostRounds: 2}
	if page.Reviews[i] != want {
		t.Errorf("the review = %+v, want %+v", page.Reviews[i], want)
	}
	if all := page.AllReviews; all.Decided < 2 || all.Longest < 7200 || all.MostRounds < 2 || all.Flow != 0 {
		t.Errorf("all reviews = %+v", all)
	}
	if page.Running.has(rn.ID) != nil {
		t.Errorf("the run that ended is listed as going on")
	}
}

// Nobody sees anything of a flow they don't see on the page; an admin sees
// everything.
func TestActivityOnlyFlowsSeen(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email)
	usersAdd(a, carl.email)

	// Bob is in no team, so the flow he makes is his.
	usersSignIn(a, p, bob)
	f := a.newFlow("Secret builds")
	build := a.agent(f.ID, "Build", "Build it.")
	a.startAt(f.ID, build.ID)
	a.worker.onAgent("Build", func(context.Context, agentRun) runResult { return runResult{Error: "the compiler gave up"} })
	rn := a.startRun(f.ID)
	a.finish(rn.ID)

	page := activityOf(a, 7, "UTC")
	if page.flow(f.ID) == nil || page.Failed.has(rn.ID) == nil || !slices.ContainsFunc(page.Processes, func(pf activityProcess) bool { return pf.Flow == f.ID }) {
		t.Errorf("bob doesn't see his own flow: %+v", page)
	}

	usersSignIn(a, p, carl)
	if page := activityOf(a, 7, "UTC"); page.mentions(f.ID) || page.Failed.has(rn.ID) != nil {
		t.Errorf("carl sees bob's flow: %+v", page)
	}

	usersSignIn(a, p, usersAda)
	if !activityOf(a, 7, "UTC").mentions(f.ID) {
		t.Errorf("the admin doesn't see bob's flow")
	}
}
