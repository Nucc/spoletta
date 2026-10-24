package tests

// A loop's circuit breaker: a process going round a loop with no person
// deciding in between, too many times or on just what it took before, waits
// for someone to say whether it goes on.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The steps of the process's kind, leaving out those asking about a loop.
func loopsRuns(d runDetail, name string) []step {
	var ss []step
	for _, s := range stepsOf(d, name) {
		if s.Kind != kindLoop {
			ss = append(ss, s)
		}
	}
	return ss
}

func loopsAsked(d runDetail, name string) []step {
	var ss []step
	for _, s := range stepsOf(d, name) {
		if s.Kind == kindLoop {
			ss = append(ss, s)
		}
	}
	return ss
}

func loopsAnswer(a *app, run, step int64, goOn bool) {
	a.t.Helper()
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/loop", run, step), map[string]any{"goOn": goOn}, http.StatusNoContent, nil)
}

// Fix fails every time; what handles its failure reworks the issue, which
// Fix takes again, so the two go round for ever unless something stops them.
// Fix goes round loops times before it asks; rework is what Rework hands in.
func loopsFailing(a *app, loops int, rework func(context.Context, agentRun) runResult) (flow, process) {
	a.t.Helper()
	f := a.newFlow("Failure loop")
	seed := a.agent(f.ID, "Seed", "Seed.")
	issue := a.newObject(f.ID, "Issue")
	fix := a.agent(f.ID, "Fix", "Fix it.")
	again := a.agent(f.ID, "Rework", "Rework the issue.")
	a.output(seed.ID, issue.ID)
	a.input(fix.ID, issue.ID)
	a.output(again.ID, issue.ID)
	a.startAt(f.ID, seed.ID)
	failuresHandles(a, fix.ID, again.ID)
	failuresSettings(a, fix.ID, processSettings{Loops: &loops})
	a.worker.onAgent("Seed", handsIn(data("it crashes")))
	// Each failure says something new, so what handles it doesn't take just
	// what it took before.
	a.worker.onAgent("Fix", failuresFlaky(1000, nil))
	a.worker.onAgent("Rework", rework)
	return f, again
}

// Past its loops, the process waits for someone to say whether it goes on;
// going on lets it run as many more times, and stopping fails the run
// without what handles its failure running again.
func TestFailureLoopAsksWhetherItGoesOn(t *testing.T) {
	a := newApp(t)
	f, _ := loopsFailing(a, 2, humanCounting("issue"))

	rn := a.startRun(f.ID)
	asked := a.waitStep(rn.ID, "Fix", stepWaiting)
	if asked.Kind != kindLoop || !strings.Contains(asked.Prompt, "has run 2 times round a loop") || !asked.CanDecide {
		t.Fatalf("asked %+v", asked)
	}
	if len(asked.Inputs) != 1 || asked.Inputs[0].Name != "Issue" || !strings.Contains(asked.Inputs[0].Content, "issue v2") {
		t.Errorf("it would take %+v", asked.Inputs)
	}
	d := a.getRun(rn.ID)
	if n := len(loopsRuns(d, "Fix")); n != 2 {
		t.Errorf("Fix ran %d times before asking, want 2; steps %s", n, describeSteps(d))
	}
	// Answering it twice would let the run go on twice; it is a loop step's
	// answer only, and a review's decision doesn't answer it.
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, asked.ID), map[string]any{"instructions": "go"}, http.StatusConflict, "no review waiting")
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/loop", rn.ID, asked.ID), map[string]any{}, http.StatusBadRequest, "goOn is required")
	loopsAnswer(a, rn.ID, asked.ID, true)
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/loop", rn.ID, asked.ID), map[string]any{"goOn": true}, http.StatusConflict, "no loop step waiting")

	again := a.waitStep(rn.ID, "Fix", stepWaiting, asked.ID)
	if !strings.Contains(again.Prompt, "has run 4 times") {
		t.Errorf("asked again %q", again.Prompt)
	}
	loopsAnswer(a, rn.ID, again.ID, false)
	d = a.finish(rn.ID)
	if d.Status != runFailed {
		t.Fatalf("run %s, want failed once stopped; steps %s", d.Status, describeSteps(d))
	}
	if f, r := len(loopsRuns(d, "Fix")), len(loopsRuns(d, "Rework")); f != 4 || r != 4 {
		t.Errorf("Fix ran %d times and Rework %d, want 4 each; steps %s", f, r, describeSteps(d))
	}
	qs := loopsAsked(d, "Fix")
	if len(qs) != 2 || qs[0].Status != stepDone || qs[1].Status != stepError || qs[1].Error != "Stopped going round the loop." {
		t.Errorf("loop steps %+v", qs)
	}
}

// A process about to take just what it took before gets nowhere, so it asks
// at once, however many loops it has left; once someone lets it go on, it
// asks only when its loops are used up.
func TestLoopGettingNowhereAsksAtOnce(t *testing.T) {
	a := newApp(t)
	f, rework := loopsFailing(a, 4, handsIn(data("the same issue")))
	failuresSettings(a, rework.ID, processSettings{Loops: ptr(maxLoops)})

	rn := a.startRun(f.ID)
	asked := a.waitStep(rn.ID, "Fix", stepWaiting)
	if !strings.Contains(asked.Prompt, "on just what it took before") {
		t.Fatalf("asked %q", asked.Prompt)
	}
	if d := a.getRun(rn.ID); len(loopsRuns(d, "Fix")) != 2 {
		t.Errorf("Fix ran %d times before asking, want 2; steps %s", len(loopsRuns(d, "Fix")), describeSteps(d))
	}
	loopsAnswer(a, rn.ID, asked.ID, true)
	again := a.waitStep(rn.ID, "Fix", stepWaiting, asked.ID)
	if !strings.Contains(again.Prompt, "has run 8 times") {
		t.Errorf("asked again %q", again.Prompt)
	}
	loopsAnswer(a, rn.ID, again.ID, false)
	if d := a.finish(rn.ID); d.Status != runFailed || len(loopsRuns(d, "Fix")) != 8 {
		t.Errorf("run %s with steps %s", d.Status, describeSteps(d))
	}
}

// Loops a person drives or a reviewer's rounds bound never ask: a person
// sending work back as often as they like, and a critic sending it back
// while its rounds last.
func TestBoundLoopsDontAsk(t *testing.T) {
	t.Run("a person sends it back", func(t *testing.T) {
		a := newApp(t)
		f := a.newFlow("Reviewed")
		write := a.agent(f.ID, "Write", "Write the post.")
		review := humanHuman(a, f.ID, "Review")
		post := a.newObject(f.ID, "Post")
		good := a.newState(post.ID, "good")
		weak := a.newState(post.ID, "weak")
		a.output(write.ID, post.ID)
		a.input(review.ID, post.ID)
		humanDecides(a, review.ID, post.ID, good.ID, weak.ID)
		humanTakesIn(a, write.ID, post.ID, weak.ID)
		a.startAt(f.ID, write.ID)
		failuresSettings(a, write.ID, processSettings{Loops: ptr(1)})
		a.worker.onAgent("Write", handsIn(data("the same post")))

		rn := a.startRun(f.ID)
		var seen []int64
		for range 3 {
			s := a.waitStep(rn.ID, "Review", stepWaiting, seen...)
			seen = append(seen, s.ID)
			humanMustDecide(a, rn.ID, s.ID, map[string]any{"state": weak.ID, "instructions": "Again."})
		}
		s := a.waitStep(rn.ID, "Review", stepWaiting, seen...)
		humanMustDecide(a, rn.ID, s.ID, map[string]any{"state": good.ID})
		d := a.finish(rn.ID)
		if d.Status != runDone || len(loopsAsked(d, "Write")) != 0 || len(loopsRuns(d, "Write")) != 4 {
			t.Errorf("run %s with steps %s", d.Status, describeSteps(d))
		}
	})
	t.Run("a critic sends it back", func(t *testing.T) {
		a := newApp(t)
		l := decisionsNewLoop(a, "weak", "weak", "good")
		failuresSettings(a, l.write.ID, processSettings{Loops: ptr(1)})
		d := a.finish(a.startRun(l.flow.ID).ID)
		if d.Status != runDone || len(loopsAsked(d, "Write")) != 0 || len(loopsRuns(d, "Write")) != 3 {
			t.Errorf("run %s with steps %s", d.Status, describeSteps(d))
		}
	})
}

// A process's loops are bounded.
func TestLoopsAreBounded(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Bounded")
	p := a.agent(f.ID, "Fix", "Fix it.")
	for _, bad := range []int{0, maxLoops + 1} {
		a.refused("PATCH", fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"settings": processSettings{Loops: &bad}}, http.StatusBadRequest, "loops must be from 1")
	}
}
