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
// a person tries it again or corrects it.
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
		{"fewer rounds", rounds[:last-1], "new", nil, 1, ""},
	}
	for _, c := range cases {
		rp := &recordPasses{passes: c.passes, state: c.state, edited: c.edited}
		if got, _ := rp.stuck(c.flow); got != c.want {
			t.Errorf("%s: stuck = %q, want %q", c.name, got, c.want)
		}
	}
}
