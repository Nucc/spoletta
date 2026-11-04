package main

import (
	"slices"
	"testing"
	"time"
)

// A record goes to the flow its column names, if that selects it, else to
// the one flow taking records in its state whose rule holds of it, or when
// no flow names its state, the one querying any state whose rule holds;
// never to either of two taking it.
func TestRecordGoesTo(t *testing.T) {
	triage := &recordFlow{ID: 1, queries: []linkQuery{{State: "new"}}}
	urgent := &recordFlow{ID: 2, queries: []linkQuery{{State: "new", Where: `priority == "high"`}}}
	fix := &recordFlow{ID: 3, queries: []linkQuery{{State: "triaged"}, {}}}
	sweep := &recordFlow{ID: 4, queries: []linkQuery{{}}}
	escalate := &recordFlow{ID: 5, queries: []linkQuery{{Where: `priority == "high"`}}}
	all := []*recordFlow{triage, urgent, fix, sweep}
	low, high := `{"priority": "low"}`, `{"priority": "high"}`
	named := func(id int64) *int64 { return &id }
	cases := []struct {
		name           string
		flows          []*recordFlow
		named          *int64
		state, content string
		want           int64
	}{
		{"one flow takes it", all, nil, "new", low, triage.ID},
		{"two flows take it", all, nil, "new", high, 0},
		{"one names it, beside any state", all, nil, "triaged", low, fix.ID},
		{"two take any state", all, nil, "fixed", low, 0},
		{"one takes any state", []*recordFlow{triage, sweep}, nil, "fixed", low, sweep.ID},
		{"one takes any state, its rule holding", []*recordFlow{triage, escalate}, nil, "fixed", high, escalate.ID},
		{"one takes any state, its rule not holding", []*recordFlow{triage, escalate}, nil, "fixed", low, 0},
		{"none takes it", []*recordFlow{triage}, nil, "fixed", low, 0},
		{"the column's flow", all, named(urgent.ID), "new", high, urgent.ID},
		{"the column's flow, not selecting it", all, named(urgent.ID), "new", low, 0},
		{"the column's flow, taking any state", all, named(sweep.ID), "fixed", low, sweep.ID},
		{"a flow that can't be started", all, named(99), "new", low, 0},
	}
	for _, c := range cases {
		var got int64
		if f := recordGoesTo(c.flows, c.named, c.state, c.content); f != nil {
			got = f.ID
		}
		if got != c.want {
			t.Errorf("%s: goes to %d, want %d", c.name, got, c.want)
		}
	}
}

// The states a column's query names, as the board shows the flows taking
// each.
func TestQueryStates(t *testing.T) {
	for q, want := range map[string][]string{
		"state:new":                        {"new"},
		`state:"in review" @priority:high`: {"in review"},
		"state:(new OR triaged)":           {"new", "triaged"},
		"-state:done":                      nil,
		"state:*":                          nil,
		"crash":                            nil,
	} {
		if got := queryStates(parseSearch(q)); !slices.Equal(got, want) {
			t.Errorf("%s names %q, want %q", q, got, want)
		}
	}
}

// A record is stuck once the board's latest run on it ended leaving it in
// the state it started it in, or after going round the board's flows, until
// a person tries it again or corrects it; a run a person started from its
// card counts its rounds anew.
func TestStuckRecords(t *testing.T) {
	at := func(minute int) *time.Time {
		t := time.Date(2026, 10, 30, 12, minute, 0, 0, time.UTC)
		return &t
	}
	pass := func(flow int64, status, state string, started, finished int) boardPass {
		p := boardPass{run: int64(started), flow: flow, status: status, state: state, created: *at(started)}
		if finished > 0 {
			p.finished = at(finished)
		}
		return p
	}
	retried := func(p boardPass, minute int) boardPass {
		p.retried = at(minute)
		return p
	}
	byHand := func(p boardPass) boardPass {
		p.byHand = true
		return p
	}
	// Rounds from new, each moved on to triaged by one flow and back again
	// by another.
	var rounds []boardPass
	for i := range boardRounds {
		rounds = append(rounds, pass(1, runDone, "new", 4*i+1, 4*i+2), pass(2, runDone, "triaged", 4*i+3, 4*i+4))
	}
	last := len(rounds) - 1
	cases := []struct {
		name   string
		passes []boardPass
		state  string
		edited *time.Time
		flow   int64
		want   string
	}{
		{"going", []boardPass{pass(1, runRunning, "new", 1, 0)}, "new", nil, 1, ""},
		{"failed", []boardPass{pass(1, runFailed, "new", 1, 2)}, "new", nil, 1, runFailed},
		{"interrupted", []boardPass{pass(1, runInterrupted, "new", 1, 2)}, "new", nil, 1, runInterrupted},
		{"done without moving it", []boardPass{pass(1, runDone, "new", 1, 2)}, "new", nil, 1, stuckUnmoved},
		{"moved on", []boardPass{pass(1, runDone, "new", 1, 2)}, "triaged", nil, 1, ""},
		{"failed after moving it", []boardPass{pass(1, runFailed, "new", 1, 2)}, "triaged", nil, 1, ""},
		{"corrected since", []boardPass{pass(1, runFailed, "new", 1, 2)}, "new", at(3), 1, ""},
		{"corrected while it went", []boardPass{pass(1, runFailed, "new", 1, 3)}, "new", at(2), 1, runFailed},
		{"tried again", []boardPass{retried(pass(1, runFailed, "new", 1, 2), 3)}, "new", nil, 1, ""},
		{"another flow now", []boardPass{pass(1, runFailed, "new", 1, 2)}, "new", nil, 2, ""},
		{"round and round", rounds, "new", nil, 1, stuckRounds},
		{"round and round, tried again", append(rounds[:last:last], retried(rounds[last], 4*boardRounds+1)), "new", nil, 1, ""},
		{"round and round, corrected", rounds, "new", at(4*boardRounds + 1), 1, ""},
		{"round and round, then run by hand", append(slices.Clone(rounds), byHand(pass(1, runDone, "new", 4*boardRounds+1, 4*boardRounds+2))), "new", nil, 1, stuckUnmoved},
		{"run by hand, going", []boardPass{pass(1, runFailed, "new", 1, 2), byHand(pass(1, runRunning, "new", 3, 0))}, "new", nil, 1, ""},
		{"fewer rounds", rounds[:last-1], "new", nil, 1, ""},
	}
	for _, c := range cases {
		rp := &recordPasses{passes: c.passes, state: c.state, edited: c.edited}
		if h := rp.stuck(c.flow, nil, *at(4*boardRounds + 30)); h.why != c.want || h.retryAt != nil {
			t.Errorf("%s: stuck = %q, retrying at %v, want %q", c.name, h.why, h.retryAt, c.want)
		}
	}
}

// A column trying its records again on its own has one whose run failed,
// or ended done leaving it where it was, wait its while after that run
// ended and go again, so many times in a row at most, then stuck; those
// tries count as one round however many there are, and start anew from a
// run a person started on it from its card. A run stopped, or one going
// round the board's flows, is never tried again on its own.
func TestRetriedRecords(t *testing.T) {
	at := func(minute int) time.Time {
		return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC).Add(time.Duration(minute) * time.Minute)
	}
	pass := func(flow int64, status, state string, started, finished int) boardPass {
		ended := at(finished)
		return boardPass{run: int64(started), flow: flow, status: status, state: state, created: at(started), finished: &ended}
	}
	// n runs in a row of flow 1 from new, each ending as status, two
	// minutes apart, the last ending at minute 2n.
	tries := func(n int, status string) []boardPass {
		var out []boardPass
		for i := range n {
			out = append(out, pass(1, status, "new", 2*i+1, 2*i+2))
		}
		return out
	}
	retry := &columnRetry{Every: 3, Times: 10}
	many := boardRounds + 2
	exhausted := tries(retry.Times+1, runFailed)
	tried := slices.Clone(exhausted)
	person := at(2*len(tried) + 1)
	tried[len(tried)-1].retried = &person
	var rounds []boardPass
	for i := range boardRounds {
		rounds = append(rounds, pass(1, runFailed, "new", 4*i+1, 4*i+2), pass(2, runDone, "triaged", 4*i+3, 4*i+4))
	}
	hand := pass(1, runFailed, "new", 2*len(exhausted)+1, 2*len(exhausted)+2)
	hand.byHand = true
	byHand := append(slices.Clone(exhausted), hand)
	// Due is how long after the last run ended it is looked at, less a
	// second for just before its try is due.
	soon := func(n int) time.Time { return at(2 * n).Add(retry.wait() - time.Second) }
	due := func(n int) time.Time { return at(2 * n).Add(retry.wait()) }
	// Retrying is how its run left it when the column is to try it again,
	// waiting to or due.
	cases := []struct {
		name     string
		passes   []boardPass
		state    string
		retry    *columnRetry
		now      time.Time
		why      string
		retried  int
		retrying string
	}{
		{"failed, waiting", tries(1, runFailed), "new", retry, soon(1), runFailed, 0, runFailed},
		{"failed, due", tries(1, runFailed), "new", retry, due(1), "", 0, runFailed},
		{"left where it was, waiting", tries(1, runDone), "new", retry, soon(1), stuckUnmoved, 0, stuckUnmoved},
		{"left where it was, due", tries(1, runDone), "new", retry, due(1), "", 0, stuckUnmoved},
		{"tried again, waiting", tries(3, runFailed), "new", retry, soon(3), runFailed, 2, runFailed},
		{"tried again more often than rounds", tries(many, runDone), "new", retry, due(many), "", many - 1, stuckUnmoved},
		{"tried as often as it may", exhausted, "new", retry, due(len(exhausted)), runFailed, retry.Times, ""},
		{"the last try left", tries(retry.Times, runFailed), "new", retry, due(retry.Times), "", retry.Times - 1, runFailed},
		{"tried again by a person", tried, "new", retry, due(len(tried)), "", 0, ""},
		{"run by hand once tried as often as it may", byHand, "new", retry, soon(len(byHand)), runFailed, 0, runFailed},
		{"run by hand, due", byHand, "new", retry, due(len(byHand)), "", 0, runFailed},
		{"interrupted", tries(1, runInterrupted), "new", retry, due(1), runInterrupted, 0, ""},
		{"terminated", tries(1, runTerminated), "new", retry, due(1), runTerminated, 0, ""},
		{"moved on", tries(1, runDone), "triaged", retry, soon(1), "", 0, ""},
		{"round and round", rounds, "new", retry, due(len(rounds)), stuckRounds, 0, ""},
		{"without retrying", tries(1, runFailed), "new", nil, due(1), runFailed, 0, ""},
	}
	for _, c := range cases {
		rp := &recordPasses{passes: c.passes, state: c.state}
		h := rp.stuck(1, c.retry, c.now)
		if h.why != c.why || h.retried != c.retried || h.retrying != c.retrying {
			t.Errorf("%s: stuck = %q, tried again %d times, retrying as %q; want %q, %d times, retrying as %q", c.name, h.why, h.retried, h.retrying, c.why, c.retried, c.retrying)
		}
		want := c.passes[len(c.passes)-1].finished.Add(retry.wait())
		if (h.retryAt != nil) != (c.retrying != "") || h.retryAt != nil && !h.retryAt.Equal(want) {
			t.Errorf("%s: tried again at %v, want %v", c.name, h.retryAt, want)
		}
	}
}
