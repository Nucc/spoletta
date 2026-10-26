package main

// The Activity page (Activity.tsx) shows what goes on across every flow
// someone sees: the runs going on now and those failed lately, and over a
// period of days, how many runs started each day, how many of them failed
// and what they spent on models, by flow; the processes that fail most; how
// long people take to decide reviews and how often they send work back.
// It is worked out in the database as it is asked for, over the indexes
// added for it, reading only the period's runs and steps, as the flow
// list's health of each flow is (flows.go).
//
// A run here is a run of a whole flow, or of a procedure on its own:
// started by a person, a schedule, the API, from a record, or as a re-run,
// which is a run of its own. The runs of a flow process's board, a subflow
// or an item run inside such a run and aren't counted apart: what they
// spend counts toward the run at their top, as limits.go has it, which
// keeps it (spent_micros, added to as llm.go records each call), and when
// they fail it, it failed. A run failed only as "failed": one interrupted,
// by the webapp stopping, or terminated, by a duplicate, didn't. Steps are
// counted in the run and flow they ran in, so a procedure's process failing
// inside another flow's run counts toward the procedure; the steps a re-run
// copied, what a run was given, and the questions of loops aren't counted.
//
// Only the flows the person sees count; see visibleFlows. Days are the
// person's, in the time zone their browser names, else UTC: each starts at
// its midnight there, worked out here and given to the database as the
// bounds of the period's days, so they hold across a change of the clocks
// without MySQL's time zone tables.

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The periods the page offers, in days, and the one it shows first.
var activityPeriods = []int{7, 30, 90}

const defaultActivityDays = 30

// How far back failed runs are listed, and how many at most of them and of
// the runs going on.
const (
	recentFailures = 7 * 24 * time.Hour
	listedRuns     = 25
)

// How many of the processes failing most, and of the reviews, are listed.
const (
	listedProcesses = 10
	listedReviews   = 20
)

// The days of a period, ending today where the person is.
type period struct {
	// The time zone the days are in, and each day as YYYY-MM-DD.
	zone string
	days []string
	// The moment each day starts, oldest first.
	starts []time.Time
}

// The days days ending on the day now falls on in loc.
func periodOf(now time.Time, loc *time.Location, days int) period {
	p := period{zone: loc.String()}
	today := now.In(loc)
	for i := days - 1; i >= 0; i-- {
		start := time.Date(today.Year(), today.Month(), today.Day()-i, 0, 0, 0, 0, loc)
		// A day whose midnight the clocks skip starts when they go on; its
		// name comes from its noon, which every day has.
		p.days = append(p.days, time.Date(today.Year(), today.Month(), today.Day()-i, 12, 0, 0, 0, loc).Format(time.DateOnly))
		p.starts = append(p.starts, start)
	}
	return p
}

// When the period starts.
func (p period) from() time.Time { return p.starts[0] }

// The SQL giving the day of the period the time in column col falls on,
// from 1 for the first, and its arguments: MySQL's INTERVAL counts the
// days' starts at or before it. Times before the period give 0.
func (p period) dayOf(col string) (string, []any) {
	args := make([]any, len(p.starts))
	for i, s := range p.starts {
		args[i] = s.Unix()
	}
	return "INTERVAL(UNIX_TIMESTAMP(" + col + "), ?" + strings.Repeat(", ?", len(p.starts)-1) + ")", args
}

// The time zone a person's browser names, else UTC.
func activityZone(name string) *time.Location {
	if name == "" || name == "Local" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// A run the page lists, with its flow's name, and for a failed one, the
// process whose step failed first and what went wrong.
type activityRun struct {
	run
	FlowName      string `json:"flowName"`
	FailedProcess string `json:"failedProcess,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Some of the runs there are, the latest first, and how many there are.
type activityRuns struct {
	Total int           `json:"total"`
	Runs  []activityRun `json:"runs"`
}

// A flow's runs, each a day of the period: how many started that day, how
// many of those failed, and the dollars they spent on models, with how many
// of their calls were to models without a known price, counted at the
// largest models'; see spend.go.
type flowActivity struct {
	Flow     int64     `json:"flow"`
	Name     string    `json:"name"`
	Module   bool      `json:"module"`
	Runs     []int     `json:"runs"`
	Failed   []int     `json:"failed"`
	Cost     []float64 `json:"cost"`
	Unpriced int64     `json:"unpriced"`
}

// A process's steps in the period, how many of them failed, a retry each
// counting, and the latest that did: its run, which may be one inside
// another flow's, and what went wrong.
type processFailures struct {
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

// The reviews a human process asked for in the period that someone
// decided, or with no process, all of them: how long they waited to be
// decided, in seconds, half of them and nine in ten within; how many came
// with instructions, the changes asked for, which go as a change request to
// what takes the object next; and the rounds, the reviews decided in one
// run, on average over the runs with any, and at most.
type reviewTimes struct {
	Flow       int64   `json:"flow,omitempty"`
	FlowName   string  `json:"flowName,omitempty"`
	Process    int64   `json:"process,omitempty"`
	Name       string  `json:"name,omitempty"`
	Decided    int     `json:"decided"`
	Median     float64 `json:"median"`
	P90        float64 `json:"p90"`
	Longest    float64 `json:"longest"`
	Changes    int     `json:"changes"`
	Runs       int     `json:"runs"`
	MostRounds int     `json:"mostRounds"`
}

type activity struct {
	Zone string   `json:"zone"`
	Days []string `json:"days"`
	// Top-level runs going on, and failed in the last recentFailures.
	Running activityRuns `json:"running"`
	Failed  activityRuns `json:"failed"`
	// Every flow that ran in the period, and all of them together.
	Total     flowActivity      `json:"total"`
	Flows     []flowActivity    `json:"flows"`
	Processes []processFailures `json:"processes"`
	Reviews   []reviewTimes     `json:"reviews"`
	// Every review the person sees.
	AllReviews reviewTimes `json:"allReviews"`
}

// The flows the person sees, nil for every one; see visibleFlows. Worked
// out once, so the queries over runs and steps needn't ask it of each.
func seenFlows(ctx context.Context, db *sql.DB, u *user) ([]int64, error) {
	cond, args := visibleFlows(u)
	if cond == "TRUE" {
		return nil, nil
	}
	ids := []int64{}
	err := scanAll(ctx, db, "SELECT f.id FROM flows f WHERE "+cond, func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		ids = append(ids, id)
		return err
	}, args...)
	return ids, err
}

// The condition that column col names one of the flows seen, and its
// arguments.
func ofFlows(col string, seen []int64) (string, []any) {
	if seen == nil {
		return "TRUE", nil
	}
	return col + " IN " + placeholders(len(seen)), int64Args(seen)
}

// Works out the page for the person over the period.
func loadActivity(ctx context.Context, db *sql.DB, u *user, p period) (activity, error) {
	a := activity{Zone: p.zone, Days: p.days, Flows: []flowActivity{}, Processes: []processFailures{}, Reviews: []reviewTimes{}}
	seen, err := seenFlows(ctx, db, u)
	if err != nil {
		return a, err
	}
	n := len(p.days)
	flows := map[int64]*flowActivity{}
	day, dayArgs := p.dayOf("started_at")
	of, ofArgs := ofFlows("flow_id", seen)
	err = scanAll(ctx, db, "SELECT flow_id, "+day+` AS d, COUNT(*), SUM(status = ?), SUM(spent_micros), SUM(unpriced_calls)
		FROM runs WHERE parent_run_id IS NULL AND started_at >= ? AND `+of+` GROUP BY flow_id, d`, func(rows *sql.Rows) error {
		var id, micros, unpriced int64
		var d, runs, failed int
		if err := rows.Scan(&id, &d, &runs, &failed, &micros, &unpriced); err != nil {
			return err
		}
		if d < 1 || d > n {
			return nil
		}
		fa := flows[id]
		if fa == nil {
			fa = &flowActivity{Flow: id, Runs: make([]int, n), Failed: make([]int, n), Cost: make([]float64, n)}
			flows[id] = fa
		}
		fa.Runs[d-1], fa.Failed[d-1], fa.Cost[d-1] = runs, failed, dollars(micros)
		fa.Unpriced += unpriced
		return nil
	}, slices.Concat(dayArgs, []any{runFailed, p.from()}, ofArgs)...)
	if err != nil {
		return a, err
	}
	if a.Running, err = listRuns(ctx, db, "status = ? AND parent_run_id IS NULL AND "+of, append([]any{runRunning}, ofArgs...)); err != nil {
		return a, err
	}
	if a.Failed, err = listRuns(ctx, db, "status = ? AND parent_run_id IS NULL AND started_at >= ? AND "+of,
		append([]any{runFailed, time.Now().Add(-recentFailures)}, ofArgs...)); err != nil {
		return a, err
	}
	if err := firstFailures(ctx, db, a.Failed.Runs); err != nil {
		return a, err
	}
	if a.Processes, err = failingProcesses(ctx, db, p, seen); err != nil {
		return a, err
	}
	if a.Reviews, a.AllReviews, err = reviewsDecided(ctx, db, p, seen); err != nil {
		return a, err
	}

	// The flows' names, and what they did together.
	ids := []int64{}
	for id := range flows {
		ids = append(ids, id)
	}
	for _, list := range [][]activityRun{a.Running.Runs, a.Failed.Runs} {
		for _, r := range list {
			ids = append(ids, r.Flow)
		}
	}
	for _, pf := range a.Processes {
		ids = append(ids, pf.Flow)
	}
	for _, rt := range a.Reviews {
		ids = append(ids, rt.Flow)
	}
	names := map[int64]string{}
	modules := map[int64]bool{}
	if len(ids) > 0 {
		err = scanAll(ctx, db, "SELECT id, name, module FROM flows WHERE id IN "+placeholders(len(ids)), func(rows *sql.Rows) error {
			var id int64
			var name string
			var module bool
			err := rows.Scan(&id, &name, &module)
			names[id], modules[id] = name, module
			return err
		}, int64Args(ids)...)
		if err != nil {
			return a, err
		}
	}
	a.Total = flowActivity{Runs: make([]int, n), Failed: make([]int, n), Cost: make([]float64, n)}
	for _, fa := range flows {
		fa.Name, fa.Module = names[fa.Flow], modules[fa.Flow]
		for d := range n {
			a.Total.Runs[d] += fa.Runs[d]
			a.Total.Failed[d] += fa.Failed[d]
			a.Total.Cost[d] += fa.Cost[d]
		}
		a.Total.Unpriced += fa.Unpriced
		a.Flows = append(a.Flows, *fa)
	}
	// The flows that ran most first, then those that spent most.
	slices.SortFunc(a.Flows, func(x, y flowActivity) int {
		if c := sum(y.Runs) - sum(x.Runs); c != 0 {
			return c
		}
		if x, y := sum(x.Cost), sum(y.Cost); x != y {
			if y > x {
				return 1
			}
			return -1
		}
		return strings.Compare(x.Name, y.Name)
	})
	for _, list := range [][]activityRun{a.Running.Runs, a.Failed.Runs} {
		for i := range list {
			list[i].FlowName = names[list[i].Flow]
		}
	}
	for i := range a.Processes {
		a.Processes[i].FlowName = names[a.Processes[i].Flow]
	}
	for i := range a.Reviews {
		a.Reviews[i].FlowName = names[a.Reviews[i].Flow]
	}
	return a, nil
}

func sum[T int | float64](xs []T) T {
	var s T
	for _, x := range xs {
		s += x
	}
	return s
}

// The latest listedRuns runs meeting cond, and how many do.
func listRuns(ctx context.Context, db *sql.DB, cond string, args []any) (activityRuns, error) {
	l := activityRuns{Runs: []activityRun{}}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE "+cond, args...).Scan(&l.Total); err != nil {
		return l, err
	}
	err := scanAll(ctx, db, "SELECT "+runColumns+" FROM runs WHERE "+cond+" ORDER BY started_at DESC, id DESC LIMIT ?", func(rows *sql.Rows) error {
		var ar activityRun
		err := scanRun(rows, &ar.run)
		l.Runs = append(l.Runs, ar)
		return err
	}, append(args, listedRuns)...)
	return l, err
}

// Fills in the process whose step failed first in each of the failed runs,
// and what went wrong.
func firstFailures(ctx context.Context, db *sql.DB, runs []activityRun) error {
	if len(runs) == 0 {
		return nil
	}
	byID := map[int64]*activityRun{}
	ids := make([]int64, len(runs))
	for i := range runs {
		byID[runs[i].ID], ids[i] = &runs[i], runs[i].ID
	}
	return scanAll(ctx, db, "SELECT run_id, process_name, error FROM run_steps WHERE status = ? AND run_id IN "+placeholders(len(ids))+" ORDER BY id",
		func(rows *sql.Rows) error {
			var run int64
			var name, msg string
			if err := rows.Scan(&run, &name, &msg); err != nil {
				return err
			}
			if r := byID[run]; r.FailedProcess == "" {
				r.FailedProcess, r.Error = name, msg
			}
			return nil
		}, append([]any{stepError}, int64Args(ids)...)...)
}

// The kinds of steps that do a process's work, whose failures count; a
// flow or subflow process fails when one of those inside it does.
var workKinds = []any{kindAgent, kindScript, kindConnector}

// The processes with the most failed steps in the period, each named as
// its latest failed step was. A process is on one flow's boards, so every
// step of it is in a run of that flow: its steps are counted from the
// steps alone, and its flow is any one's run's.
func failingProcesses(ctx context.Context, db *sql.DB, p period, seen []int64) ([]processFailures, error) {
	out := []processFailures{}
	var lastSteps []int64
	of, ofArgs := ofFlows("r.flow_id", seen)
	err := scanAll(ctx, db, `SELECT r.flow_id, g.process_ref, g.steps, g.failed, g.last FROM (
			SELECT s.process_ref, COUNT(*) AS steps, SUM(s.status = ?) AS failed, MAX(IF(s.status = ?, s.id, NULL)) AS last, MAX(s.run_id) AS run
			FROM run_steps s WHERE s.kind IN `+placeholders(len(workKinds))+` AND s.started_at >= ? AND s.process_ref IS NOT NULL
			GROUP BY s.process_ref HAVING failed > 0
		) g JOIN runs r ON r.id = g.run
		WHERE `+of+` ORDER BY g.failed DESC, g.steps DESC, g.process_ref LIMIT ?`, func(rows *sql.Rows) error {
		var pf processFailures
		var last int64
		err := rows.Scan(&pf.Flow, &pf.Process, &pf.Steps, &pf.Failed, &last)
		out = append(out, pf)
		lastSteps = append(lastSteps, last)
		return err
	}, slices.Concat([]any{stepError, stepError}, workKinds, []any{p.from()}, ofArgs, []any{listedProcesses})...)
	if err != nil || len(out) == 0 {
		return out, err
	}
	last := map[int64]*processFailures{}
	for i, id := range lastSteps {
		last[id] = &out[i]
	}
	err = scanAll(ctx, db, "SELECT id, run_id, process_name, kind, error FROM run_steps WHERE id IN "+placeholders(len(lastSteps)), func(rows *sql.Rows) error {
		var id int64
		var pf processFailures
		if err := rows.Scan(&id, &pf.LastRun, &pf.Name, &pf.Kind, &pf.LastError); err != nil {
			return err
		}
		l := last[id]
		l.LastRun, l.Name, l.Kind, l.LastError = pf.LastRun, pf.Name, pf.Kind, pf.LastError
		return nil
	}, int64Args(lastSteps)...)
	return out, err
}

// How long people took to decide the reviews asked for in the period, by
// human process and all together; see reviewTimes. Half and nine in ten
// within are the least times at least that share of the reviews took no
// longer than.
func reviewsDecided(ctx context.Context, db *sql.DB, p period, seen []int64) ([]reviewTimes, reviewTimes, error) {
	var all reviewTimes
	byProcess := map[int64]*reviewTimes{}
	var out []*reviewTimes
	var latest []int64
	of, ofArgs := ofFlows("r.flow_id", seen)
	err := scanAll(ctx, db, `WITH d AS (
			SELECT r.flow_id, s.process_ref, s.id, UNIX_TIMESTAMP(s.finished_at) - UNIX_TIMESTAMP(s.started_at) AS secs, s.instructions <> '' AS asked
			FROM run_steps s JOIN runs r ON r.id = s.run_id
			WHERE s.kind = ? AND s.started_at >= ? AND s.status = ? AND s.finished_at IS NOT NULL AND s.process_ref IS NOT NULL AND `+of+`
		), c AS (
			SELECT d.*, CUME_DIST() OVER (PARTITION BY process_ref ORDER BY secs) AS cd, CUME_DIST() OVER (ORDER BY secs) AS cd_all FROM d
		)
		SELECT MAX(flow_id), process_ref, COUNT(*), MIN(IF(cd >= 0.5, secs, NULL)), MIN(IF(cd >= 0.9, secs, NULL)), MAX(secs), SUM(asked), MAX(id)
		FROM c GROUP BY process_ref
		UNION ALL
		SELECT NULL, NULL, COUNT(*), MIN(IF(cd_all >= 0.5, secs, NULL)), MIN(IF(cd_all >= 0.9, secs, NULL)), MAX(secs), COALESCE(SUM(asked), 0), NULL
		FROM c`, func(rows *sql.Rows) error {
		var flow, process, step sql.NullInt64
		var median, p90, longest sql.NullFloat64
		rt := &reviewTimes{}
		if err := rows.Scan(&flow, &process, &rt.Decided, &median, &p90, &longest, &rt.Changes, &step); err != nil {
			return err
		}
		rt.Median, rt.P90, rt.Longest = median.Float64, p90.Float64, longest.Float64
		if !process.Valid {
			all = *rt
			return nil
		}
		rt.Flow, rt.Process = flow.Int64, process.Int64
		byProcess[rt.Process] = rt
		out = append(out, rt)
		latest = append(latest, step.Int64)
		return nil
	}, slices.Concat([]any{kindHuman, p.from(), stepDone}, ofArgs)...)
	if err != nil || len(out) == 0 {
		return []reviewTimes{}, all, err
	}
	// The rounds of each process's runs, read from the steps alone: those
	// of the processes above are of flows the person sees.
	err = scanAll(ctx, db, `SELECT process_ref, COUNT(*), MAX(n) FROM (
			SELECT process_ref, run_id, COUNT(*) AS n FROM run_steps
			WHERE kind = ? AND started_at >= ? AND status = ? AND finished_at IS NOT NULL AND process_ref IN `+placeholders(len(out))+`
			GROUP BY process_ref, run_id
		) x GROUP BY process_ref`, func(rows *sql.Rows) error {
		var process int64
		var runs, most int
		if err := rows.Scan(&process, &runs, &most); err != nil {
			return err
		}
		rt := byProcess[process]
		rt.Runs, rt.MostRounds = runs, most
		all.Runs += runs
		all.MostRounds = max(all.MostRounds, most)
		return nil
	}, slices.Concat([]any{kindHuman, p.from(), stepDone}, reviewProcesses(out))...)
	if err != nil {
		return nil, all, err
	}
	// The reviews decided most, each named as its latest review was.
	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(i, j int) int { return out[j].Decided - out[i].Decided })
	if len(order) > listedReviews {
		order = order[:listedReviews]
	}
	listed := make([]reviewTimes, len(order))
	named := map[int64]*reviewTimes{}
	steps := make([]int64, len(order))
	for k, i := range order {
		listed[k], steps[k] = *out[i], latest[i]
		named[steps[k]] = &listed[k]
	}
	err = scanAll(ctx, db, "SELECT id, process_name FROM run_steps WHERE id IN "+placeholders(len(steps)), func(rows *sql.Rows) error {
		var id int64
		var name string
		err := rows.Scan(&id, &name)
		named[id].Name = name
		return err
	}, int64Args(steps)...)
	return listed, all, err
}

func reviewProcesses(rts []*reviewTimes) []any {
	args := make([]any, len(rts))
	for i, rt := range rts {
		args[i] = rt.Process
	}
	return args
}

func registerActivityRoutes(mux *http.ServeMux, db *sql.DB) {
	// The page over the last days=7, 30 or 90 days, in the time zone tz.
	mux.HandleFunc("GET /api/activity", func(w http.ResponseWriter, r *http.Request) {
		days := defaultActivityDays
		if v := r.URL.Query().Get("days"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || !slices.Contains(activityPeriods, n) {
				http.Error(w, "days is 7, 30 or 90", http.StatusBadRequest)
				return
			}
			days = n
		}
		p := periodOf(time.Now(), activityZone(r.URL.Query().Get("tz")), days)
		a, err := loadActivity(r.Context(), db, requestUser(r), p)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	})
}
