package main

import (
	"os"
	"slices"
	"testing"
)

// A walk taken over takes up each process's steps in the order they
// started, one that took the same inputs first, and each once.
func TestPastStepsTakenUpInOrder(t *testing.T) {
	in := func(content string) []runInput { return []runInput{{Name: "Ticket", Content: content}} }
	ps := &pastSteps{steps: []*pastStep{
		{id: 1, process: 7, kind: kindAgent, status: stepError, inputs: in("a")},
		{id: 2, process: 8, kind: kindAgent, status: stepDone, inputs: in("a")},
		{id: 3, process: 7, kind: kindAgent, status: stepDone, inputs: in("b")},
		{id: 4, process: 7, kind: kindAgent, status: stepSkipped, inputs: []runInput{}},
		{id: 5, process: 7, kind: kindAgent, status: stepActive, inputs: in("a")},
		{id: 6, process: 9, kind: kindLoop, status: stepWaiting, inputs: in("a")},
		{id: 7, process: 9, kind: kindHuman, status: stepWaiting, inputs: in("a")},
	}}
	took := func(s *pastStep) int64 {
		if s == nil {
			return 0
		}
		return s.id
	}
	if id := took(ps.peek(7, ranStep)); id != 1 {
		t.Errorf("peeked at step %d, want 1", id)
	}
	// The same inputs first, then the first left, never a skipped one.
	for _, c := range []struct {
		inputs []runInput
		want   int64
	}{{in("b"), 3}, {in("a"), 1}, {in("c"), 5}, {in("a"), 0}} {
		if id := took(ps.take(7, ranStep, c.inputs)); id != c.want {
			t.Errorf("took step %d for %v, want %d", id, c.inputs, c.want)
		}
	}
	if id := took(ps.take(7, noted(stepSkipped), []runInput{})); id != 4 {
		t.Errorf("took step %d as skipped, want 4", id)
	}
	// A loop's question and a review of one process are told apart.
	if id := took(ps.take(9, reviewed(false), in("a"))); id != 7 {
		t.Errorf("took step %d as the review, want 7", id)
	}
	if open := ps.open(); !slices.Equal(open, []int64{6}) {
		t.Errorf("left open %v, want the loop's question", open)
	}
	if id := took(ps.take(9, reviewed(true), in("a"))); id != 6 {
		t.Errorf("took step %d as the loop's question, want 6", id)
	}
	var none *pastSteps
	if none.take(7, ranStep, nil) != nil || none.peek(7, ranStep) != nil || none.open() != nil {
		t.Errorf("a run walked from its start took up a step")
	}
}

// How a flow process's step that ended had its board end.
func TestPastStepEnded(t *testing.T) {
	for _, c := range []struct {
		status, err, want string
	}{
		{stepDone, "", runDone},
		{stepDuplicate, "x", runTerminated},
		{stepError, "Run #4" + stoppedInside, runInterrupted},
		{stepError, "A process inside failed.", runFailed},
	} {
		if got := (&pastStep{status: c.status, err: c.err}).ended(); got != c.want {
			t.Errorf("%s %q ended %s, want %s", c.status, c.err, got, c.want)
		}
	}
}

// RESUME_RUNS takes runs over unless it says false.
func TestResumingRuns(t *testing.T) {
	for _, c := range []struct {
		v    string
		want bool
		ok   bool
	}{{"", true, true}, {"true", true, true}, {"FALSE", false, true}, {"0", false, true}, {"later", false, false}} {
		os.Setenv("RESUME_RUNS", c.v)
		got, err := resumingRuns()
		if got != c.want || (err == nil) != c.ok {
			t.Errorf("RESUME_RUNS=%q: %v, %v", c.v, got, err)
		}
	}
	os.Unsetenv("RESUME_RUNS")
}
