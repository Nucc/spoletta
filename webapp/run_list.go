package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The board's list of a flow's runs, newest first, a page at a time, as
// GET /api/runs?flow=<id> gives it, narrowed by what the person picks:
//   - status: how a run stands. A running run with a step waiting for a
//     person, a review or a loop's question, is waiting rather than running,
//     so the two pick apart the runs going.
//   - started: how it started. Each run started one way, the first its
//     markers say in this order: a trial of one process alone (trials.go),
//     a re-run of another, on records a person picked, through the API, by
//     a webhook (webhooks.go), by a schedule, in another run (a flow process's board, an item of a
//     list, a subflow), or else by a person, with the Run button. A re-run
//     of a scheduled run is a re-run, and a run without sign-in a person's.
//
// Several values of either, repeated or comma-separated, are alternatives,
// and a run must hold to both. Picking no way it started picks every way
// but a trial: trials stay out of the list unless asked for, as they do of
// the Data page. before=<id> gives the runs older than that one, the page
// after a page ending with it, and limit how many, at most runPage.

// As many runs as a page of the list holds, and the most one may.
const runPage = 200

// Whether a running run has a step waiting for a person; runs unaliased.
const runWaiting = "EXISTS (SELECT 1 FROM run_steps ws WHERE ws.run_id = runs.id AND ws.status = 'waiting')"

// How a run started, as the list tells runs apart; runs unaliased.
const runOrigin = `CASE WHEN trial_of IS NOT NULL THEN 'trial' WHEN rerun_of IS NOT NULL THEN 'rerun' WHEN record_id IS NOT NULL THEN 'record'
	WHEN api_key_id IS NOT NULL THEN 'api' WHEN webhook_id IS NOT NULL THEN 'webhook' WHEN schedule_id IS NOT NULL THEN 'schedule'
	WHEN parent_run_id IS NOT NULL THEN 'run' ELSE 'person' END`

// What the list can be narrowed by, in the order the board offers them.
var (
	runStatuses = []string{"running", "waiting", runDone, runFailed, runInterrupted, runTerminated}
	runOrigins  = []string{"person", "schedule", "api", "webhook", "rerun", "record", "run", "trial"}
)

// What narrows a page of the list.
type runFilter struct {
	statuses, origins []string
	// The run the page goes on after, 0 for the newest.
	before int64
	limit  int
}

// The filter a request's query asks for, or what is wrong with it.
func parseRunFilter(q url.Values) (runFilter, string) {
	f := runFilter{limit: runPage}
	var msg string
	if f.statuses, msg = listParam(q, "status", runStatuses); msg != "" {
		return f, msg
	}
	if f.origins, msg = listParam(q, "started", runOrigins); msg != "" {
		return f, msg
	}
	if s := q.Get("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 {
			return f, "before must be a run's id"
		}
		f.before = n
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > runPage {
			return f, fmt.Sprintf("limit must be a number from 1 to %d", runPage)
		}
		f.limit = n
	}
	return f, ""
}

// The values of a parameter, repeated or comma-separated, each one of
// those allowed; or what is wrong.
func listParam(q url.Values, key string, allowed []string) ([]string, string) {
	var out []string
	for _, v := range q[key] {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s == "" || slices.Contains(out, s) {
				continue
			}
			if !slices.Contains(allowed, s) {
				return nil, fmt.Sprintf("%s must be one of %s, not %q", key, strings.Join(allowed, ", "), s)
			}
			out = append(out, s)
		}
	}
	return out, ""
}

// The page's query of the flow's runs: what it selects runColumns from runs
// by, its order and limit, with its arguments after the flow's.
func (f runFilter) query(flow int64) (string, []any) {
	where := []string{"flow_id = ?"}
	args := []any{flow}
	if len(f.statuses) > 0 {
		var either []string
		var ended []any
		for _, s := range f.statuses {
			switch s {
			case "running":
				either = append(either, "(status = '"+runRunning+"' AND NOT "+runWaiting+")")
			case "waiting":
				either = append(either, "(status = '"+runRunning+"' AND "+runWaiting+")")
			default:
				ended = append(ended, s)
			}
		}
		if len(ended) > 0 {
			either = append(either, "status IN "+placeholders(len(ended)))
			args = append(args, ended...)
		}
		where = append(where, "("+strings.Join(either, " OR ")+")")
	}
	if len(f.origins) > 0 {
		where = append(where, "("+runOrigin+") IN "+placeholders(len(f.origins)))
		for _, o := range f.origins {
			args = append(args, o)
		}
	} else {
		where = append(where, "trial_of IS NULL")
	}
	if f.before > 0 {
		where = append(where, "id < ?")
		args = append(args, f.before)
	}
	args = append(args, f.limit)
	return " WHERE " + strings.Join(where, " AND ") + " ORDER BY id DESC LIMIT ?", args
}
