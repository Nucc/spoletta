package main

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A walk of board, nil for the top one, of a run of a flow whose board is b,
// with no engine behind it: enough to see how it reads the board.
func walkOn(b snapshot, board *int64, given map[int64]value) *walk {
	return newWalk(context.Background(), nil, 0, nil, newFlowRun(1, 1, b, nil, ""), board, given, nil)
}

// Write turns the brief into a draft, which a person reviews, approving or
// rejecting it. Publish takes the draft on condition it is approved, Redo
// waits for it to be rejected, and Mixed takes the brief too. MakeA and
// MakeB each make an object; Both takes the two, Either needs just one.
func reviewBoard() snapshot {
	return snapshot{
		Processes: []process{
			{ID: 1, Name: "Write", Kind: kindAgent},
			{ID: 2, Name: "Review", Kind: kindHuman},
			{ID: 3, Name: "Publish", Kind: kindAgent},
			{ID: 4, Name: "Either", Kind: kindAgent, Settings: processSettings{Quorum: count(1)}},
			{ID: 5, Name: "Redo", Kind: kindAgent},
			{ID: 6, Name: "MakeA", Kind: kindScript},
			{ID: 7, Name: "MakeB", Kind: kindScript},
			{ID: 8, Name: "Both", Kind: kindAgent},
			{ID: 9, Name: "Mixed", Kind: kindAgent},
		},
		Objects: []snapshotObject{{ID: 10, Name: "Brief"}, {ID: 11, Name: "Draft"}, {ID: 12, Name: "A"}, {ID: 13, Name: "B"}},
		States: []objectState{
			{ID: 20, Object: 11, Name: "approved", Rule: `verdict == "yes"`},
			{ID: 21, Object: 11, Name: "rejected", Rule: `verdict == "no"`},
			{ID: 22, Object: 11, Name: "broken", Rule: `verdict >`},
		},
		Links: []snapshotLink{
			{ID: 101, Process: 1, Object: 10, Direction: directionInput},
			{ID: 102, Process: 1, Object: 11, Direction: directionOutput},
			{ID: 103, Process: 2, Object: 11, Direction: directionInput},
			{ID: 104, Process: 2, Object: 11, Direction: directionOutput},
			{ID: 105, Process: 3, Object: 11, Direction: directionInput, State: 20, Condition: true},
			{ID: 106, Process: 5, Object: 11, Direction: directionInput, State: 21},
			{ID: 107, Process: 6, Object: 12, Direction: directionOutput},
			{ID: 108, Process: 7, Object: 13, Direction: directionOutput},
			{ID: 109, Process: 4, Object: 12, Direction: directionInput},
			{ID: 110, Process: 4, Object: 13, Direction: directionInput},
			{ID: 111, Process: 8, Object: 12, Direction: directionInput},
			{ID: 112, Process: 8, Object: 13, Direction: directionInput},
			{ID: 113, Process: 9, Object: 10, Direction: directionInput},
			{ID: 114, Process: 9, Object: 11, Direction: directionInput, State: 21},
		},
		LinkStates: []snapshotLinkState{{Link: 104, State: 20}, {Link: 104, State: 21}},
	}
}

// Objects no process generates are given, so they exist from the start; a
// condition link takes its object as one without a state does; a human
// generates none of the objects whose state they decide.
func TestWalkReadsTheBoard(t *testing.T) {
	w := walkOn(reviewBoard(), nil, nil)
	if !slices.Equal(slices.Sorted(maps.Keys(w.values)), []int64{10}) {
		t.Errorf("values from the start = %v, want only the brief", w.values)
	}
	for o, want := range map[int64]bool{10: false, 11: true, 12: true, 13: true} {
		if w.generated[o] != want {
			t.Errorf("generated[%d] = %v, want %v", o, w.generated[o], want)
		}
	}
	if got := w.plainInputs(3); !slices.Equal(got, []linkEnd{{11, 0}}) {
		t.Errorf("plainInputs(Publish) = %v", got)
	}
	if got := w.plainInputs(9); !slices.Equal(got, []linkEnd{{10, 0}}) {
		t.Errorf("plainInputs(Mixed) = %v", got)
	}
	if got := w.plainInputs(5); got != nil {
		t.Errorf("plainInputs(Redo) = %v", got)
	}
	if got := w.outputs[2]; !slices.Equal(got, []linkEnd{{11, 20}, {11, 21}}) {
		t.Errorf("outputs(Review) = %v", got)
	}
	for _, c := range []struct {
		state int64
		want  []int64
	}{{0, []int64{2, 3}}, {20, nil}, {21, []int64{5, 9}}} {
		if got := w.takersIn(11, c.state); !slices.Equal(got, c.want) {
			t.Errorf("takersIn(Draft, %d) = %v, want %v", c.state, got, c.want)
		}
	}
}

func TestReadiness(t *testing.T) {
	for _, c := range []struct {
		name      string
		p         int64
		values    map[int64]value
		skipped   []int64
		run, skip bool
	}{
		{"Write runs on the given brief", 1, nil, nil, true, false},
		{"Publish waits for the draft", 3, nil, nil, false, false},
		{"Publish runs on an approved draft", 3, map[int64]value{11: {state: 20}}, nil, true, false},
		{"Publish is skipped on a rejected draft", 3, map[int64]value{11: {state: 21}}, nil, false, true},
		{"Publish is skipped on a draft in no state", 3, map[int64]value{11: {}}, nil, false, true},
		{"Publish is skipped once the draft is", 3, nil, []int64{11}, false, true},
		{"Redo waits for the draft to enter a state", 5, map[int64]value{11: {state: 21}}, nil, false, false},
		{"Mixed waits for the state too", 9, map[int64]value{11: {state: 21}}, nil, false, false},
		{"Both waits for B", 8, map[int64]value{12: {}}, nil, false, false},
		{"Both runs without a skipped B", 8, map[int64]value{12: {}}, []int64{13}, true, false},
		{"Both is skipped once neither came", 8, nil, []int64{12, 13}, false, true},
		{"Either runs on A alone", 4, map[int64]value{12: {}}, nil, true, false},
		{"Either waits while B may come", 4, nil, []int64{12}, false, false},
		{"Either is skipped once too few can come", 4, nil, []int64{12, 13}, false, true},
	} {
		w := walkOn(reviewBoard(), nil, nil)
		for o, v := range c.values {
			w.values[o] = v
		}
		for _, o := range c.skipped {
			w.skipped[o] = true
		}
		if run, skip := w.readiness(c.p); run != c.run || skip != c.skip {
			t.Errorf("%s: readiness = %v, %v, want %v, %v", c.name, run, skip, c.run, c.skip)
		}
	}
}

func TestUnmetConditions(t *testing.T) {
	w := walkOn(reviewBoard(), nil, nil)
	for _, c := range []struct {
		draft *value
		want  string
	}{
		{nil, ""},
		{&value{state: 20}, ""},
		{&value{state: 21}, `"Draft" is "rejected", not "approved"`},
		{&value{}, `"Draft" is in no state, not "approved"`},
	} {
		delete(w.values, 11)
		if c.draft != nil {
			w.values[11] = *c.draft
		}
		if got := w.unmet(3); got != c.want {
			t.Errorf("unmet(Publish) with %v = %q, want %q", c.draft, got, c.want)
		}
	}
	if !w.holds(1, 10, 0) || !w.holds(3, 11, 20) || w.holds(3, 11, 21) {
		t.Error("holds")
	}
}

// An object generated in no state comes out in the first of its states
// whose rule holds of it; a rule that no longer parses holds of nothing.
func TestRuledState(t *testing.T) {
	w := walkOn(reviewBoard(), nil, nil)
	for content, want := range map[string]int64{
		`{"verdict": "yes"}`:   20,
		`{"verdict": "no"}`:    21,
		`{"verdict": "maybe"}`: 0,
		"not json":             0,
	} {
		if got := w.ruled(11, content); got != want {
			t.Errorf("ruled(%s) = %d, want %d", content, got, want)
		}
	}
	if len(w.rules[11]) != 2 {
		t.Errorf("rules = %v, want the two that parse", w.rules[11])
	}
}

// The process a flow process's start points at takes what the flow process
// was given, linked to it or not; nothing else does.
func TestStartGiven(t *testing.T) {
	b := snapshot{
		Processes: []process{
			{ID: 30, Name: "Inside", Kind: kindFlow},
			{ID: 31, Name: "First", Kind: kindAgent, Parent: ptr(30)},
			{ID: 32, Name: "Second", Kind: kindAgent, Parent: ptr(30)},
		},
		Objects: []snapshotObject{{ID: 5, Name: "Ticket"}, {ID: 10, Name: "Brief"}},
		Boards:  []innerBoard{{Process: 30, Start: startNode{Target: ptr(31)}}},
	}
	inner := walkOn(b, ptr(30), map[int64]value{10: {content: "x"}, 5: {}})
	if got := inner.startGiven(31); !slices.Equal(got, []int64{5, 10}) {
		t.Errorf("startGiven(First) = %v", got)
	}
	if got := inner.startGiven(32); got != nil {
		t.Errorf("startGiven(Second) = %v", got)
	}
	if _, ok := inner.kinds[30]; ok || inner.kinds[31] != kindAgent {
		t.Errorf("kinds on the inner board = %v", inner.kinds)
	}
	if v := inner.values[10]; v.content != "x" || !inner.generated[10] {
		t.Errorf("given brief = %v, generated %v", v, inner.generated[10])
	}
	if got := walkOn(b, nil, nil).startGiven(30); got != nil {
		t.Errorf("startGiven on a top board without a start = %v", got)
	}
}

// Every process runs at most once per pass, however many reach it at once.
func TestPassVisit(t *testing.T) {
	p := newPass()
	if !p.visit(1) || p.visit(1) || !p.visit(2) {
		t.Error("visit")
	}
	var first atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.visit(3) {
				first.Add(1)
			}
		}()
	}
	wg.Wait()
	if first.Load() != 1 {
		t.Errorf("%d visits of 3 came first", first.Load())
	}
}

func TestNotIn(t *testing.T) {
	for n, want := range map[int]string{
		0: "",
		1: " AND object_ref NOT IN (?)",
		3: " AND object_ref NOT IN (?, ?, ?)",
	} {
		if got := notIn(n); got != want {
			t.Errorf("notIn(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStopReason(t *testing.T) {
	timedOut, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-timedOut.Done()
	stopped, stop := context.WithCancel(context.Background())
	stop()
	if got := stopReason(timedOut); got != "it ran longer than its timeout" {
		t.Errorf("timed out: %q", got)
	}
	if got := stopReason(stopped); got != "it was stopped" {
		t.Errorf("stopped: %q", got)
	}
}
