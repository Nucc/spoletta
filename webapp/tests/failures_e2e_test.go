package tests

// Failures: a step's timeout and retries, and failure connections, OPM's
// exception links, to the processes that handle a failure instead of it
// failing the run.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

func failuresSettings(a *app, process int64, s processSettings) {
	a.t.Helper()
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", process), map[string]any{"settings": s}, http.StatusOK, nil)
}

func failuresHandles(a *app, source, target int64) connection {
	a.t.Helper()
	var c connection
	a.call("POST", "/api/connections", map[string]any{"source": source, "target": target, "kind": connectionFailure}, http.StatusCreated, &c)
	return c
}

// An agent failing the first n times it is asked, then handing in v.
func failuresFlaky(n int, v any) func(context.Context, agentRun) runResult {
	var mu sync.Mutex
	tries := 0
	return func(context.Context, agentRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		tries++
		if tries <= n {
			return runResult{Error: fmt.Sprintf("flaked %d", tries)}
		}
		return submitted(v)
	}
}

func ptr[T any](v T) *T { return &v }

// A failed step is tried again, as a step of its own each time, as many
// times as its retries say; once one succeeds the branch goes on.
func TestFailedStepIsRetried(t *testing.T) {
	for _, c := range []struct {
		name           string
		flakes, steps  int
		status, follow string
	}{
		{"succeeds on a retry", 2, 3, runDone, "Use"},
		{"fails every try", 5, 3, runFailed, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Retries")
			flaky := a.agent(f.ID, "Flaky", "Try it.")
			use := a.agent(f.ID, "Use", "Use it.")
			out := a.newObject(f.ID, "Out")
			a.output(flaky.ID, out.ID)
			a.input(use.ID, out.ID)
			a.startAt(f.ID, flaky.ID)
			failuresSettings(a, flaky.ID, processSettings{Retries: ptr(2)})
			a.worker.onAgent("Flaky", failuresFlaky(c.flakes, data("made")))
			a.worker.onAgent("Use", handsIn(data("used")))

			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != c.status || len(stepsOf(d, "Flaky")) != c.steps || (len(stepsOf(d, "Use")) == 1) != (c.follow != "") {
				t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
			}
			if s := stepsOf(d, "Flaky")[0]; s.Status != stepError || s.Error != "flaked 1" {
				t.Errorf("first try = %s %q", s.Status, s.Error)
			}
		})
	}
}

// A failure connection runs its target instead of failing the run: it takes
// what the failed process took and what went wrong, in a fresh pass, so it
// runs even if it ran already.
func TestFailureConnectionHandlesTheFailure(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Handled")
	seed := a.agent(f.ID, "Seed", "Seed.")
	issue := a.newObject(f.ID, "Issue")
	fix := a.agent(f.ID, "Fix", "Fix it.")
	patch := a.newObject(f.ID, "Patch")
	ship := a.agent(f.ID, "Ship", "Ship it.")
	escalate := a.agent(f.ID, "Escalate", "Tell someone.")
	a.output(seed.ID, issue.ID)
	a.input(fix.ID, issue.ID)
	a.output(fix.ID, patch.ID)
	a.input(ship.ID, patch.ID)
	a.startAt(f.ID, seed.ID)
	c := failuresHandles(a, fix.ID, escalate.ID)
	if c.Kind != connectionFailure {
		t.Errorf("connection = %+v", c)
	}
	a.worker.onAgent("Seed", handsIn(data("it crashes")))
	a.worker.onAgent("Fix", func(context.Context, agentRun) runResult { return runResult{Error: "can't reproduce"} })
	a.worker.onAgent("Ship", handsIn(data("shipped")))
	a.worker.onAgent("Escalate", corePlain("told"))

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s with steps %s, want the failure handled", d.Status, describeSteps(d))
	}
	if len(stepsOf(d, "Ship")) != 0 {
		t.Errorf("Ship ran after Fix failed; steps %s", describeSteps(d))
	}
	in := onlyStep(t, d, "Escalate").Inputs
	names := coreInputNames(onlyStep(t, d, "Escalate"))
	if !slices.Equal(names, []string{"Failure of Fix", "Issue"}) || in[0].Content != "can't reproduce" || !strings.Contains(in[1].Content, "it crashes") {
		t.Errorf("Escalate took %+v", in)
	}

	// Without the connection the failure fails the run, as before.
	a.call("DELETE", fmt.Sprintf("/api/connections/%d", c.ID), nil, http.StatusNoContent, nil)
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runFailed {
		t.Errorf("run without the handler %s", d.Status)
	}
}

// A failure is handled once the retries are used up, and a human can handle
// it, deciding what comes next.
func TestFailureHandledAfterRetriesByAHuman(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Escalated")
	fix := a.agent(f.ID, "Fix", "Fix it.")
	triage := humanHuman(a, f.ID, "Triage")
	humanPerson(a, f.ID, triage.ID, "On call")
	a.startAt(f.ID, fix.ID)
	failuresSettings(a, fix.ID, processSettings{Retries: ptr(1)})
	failuresHandles(a, fix.ID, triage.ID)
	a.worker.onAgent("Fix", failuresFlaky(10, data("never")))

	rn := a.startRun(f.ID)
	review := a.waitStep(rn.ID, "Triage", stepWaiting)
	if len(review.Inputs) != 1 || review.Inputs[0].Name != "Failure of Fix" || review.Inputs[0].Content != "flaked 2" {
		t.Errorf("the person reviews %+v, want the last failure", review.Inputs)
	}
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"instructions": "Looked into it."})
	if d := a.finish(rn.ID); d.Status != runDone || len(stepsOf(d, "Fix")) != 2 {
		t.Errorf("run %s with steps %s", d.Status, describeSteps(d))
	}
}

// A critic out of rounds fails, which its failure connection handles.
func TestFailureConnectionHandlesARoundsLimit(t *testing.T) {
	a := newApp(t)
	l := decisionsNewLoop(a, "weak")
	escalate := a.agent(l.flow.ID, "Escalate", "Ask an editor.")
	failuresSettings(a, l.critic.ID, processSettings{Rounds: ptr(1)})
	failuresHandles(a, l.critic.ID, escalate.ID)
	a.worker.onAgent("Escalate", corePlain("asked"))

	d := a.finish(a.startRun(l.flow.ID).ID)
	if d.Status != runDone || len(stepsOf(d, "Write")) != 1 {
		t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
	}
	in := onlyStep(t, d, "Escalate").Inputs
	if len(in) != 2 || in[0].Name != "Failure of Critic" || !strings.Contains(in[0].Content, "after 1 rounds") || in[1].Name != "Post" {
		t.Errorf("Escalate took %+v", in)
	}
}

// A step stopped by a person is neither retried nor handled.
func TestStoppedStepIsNotRetriedNorHandled(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Stopped")
	slow := a.agent(f.ID, "Slow", "Slow.")
	escalate := a.agent(f.ID, "Escalate", "Escalate.")
	a.startAt(f.ID, slow.ID)
	failuresSettings(a, slow.ID, processSettings{Retries: ptr(3)})
	failuresHandles(a, slow.ID, escalate.ID)
	g := newCoreGate()
	a.worker.onAgent("Slow", g.agent("Slow", corePlain("late")(context.Background(), agentRun{})))
	a.worker.onAgent("Escalate", corePlain("escalated"))

	rn := a.startRun(f.ID)
	g.wait(t, "Slow")
	step := a.waitStep(rn.ID, "Slow", stepActive)
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/stop", rn.ID, step.ID), nil, http.StatusNoContent, nil)
	d := a.finish(rn.ID)
	if d.Status != runFailed || len(stepsOf(d, "Slow")) != 1 || len(stepsOf(d, "Escalate")) != 0 {
		t.Errorf("run %s with steps %s, want only the stopped step", d.Status, describeSteps(d))
	}
}

// A process's timeout goes to the worker, which ends the step then.
func TestStepTimeoutGoesToTheWorker(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Timeouts")
	agent := a.agent(f.ID, "Agent", "Think.")
	script := a.newProcess(f.ID, map[string]any{"name": "Script", "kind": kindScript, "script": "sleep 1"})
	o := a.newObject(f.ID, "O")
	a.output(agent.ID, o.ID)
	a.input(script.ID, o.ID)
	a.startAt(f.ID, agent.ID)
	failuresSettings(a, agent.ID, processSettings{Timeout: ptr(90)})
	failuresSettings(a, script.ID, processSettings{Timeout: ptr(5)})
	a.worker.onAgent("Agent", handsIn(data("x")))
	var mu sync.Mutex
	var scriptTimeout int64
	a.worker.onScript("Script", func(_ context.Context, r scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		scriptTimeout = r.TimeoutMS
		return runResult{Error: "timed out after 5s"}
	})

	d := a.finish(a.startRun(f.ID).ID)
	if got := a.worker.runsOf("Agent")[0].TimeoutMS; got != 90_000 {
		t.Errorf("agent's timeout = %dms, want 90000", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if scriptTimeout != 5_000 {
		t.Errorf("script's timeout = %dms, want 5000", scriptTimeout)
	}
	if s := onlyStep(t, d, "Script"); d.Status != runFailed || s.Error != "timed out after 5s" {
		t.Errorf("run %s, script step %q", d.Status, s.Error)
	}
}

// What can't be: failure connections only from processes that fail, of a
// known kind, and settings in range; a version keeps the connection's kind.
func TestFailureRefusals(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Refusals")
	fix := a.agent(f.ID, "Fix", "Fix it.")
	outer := nestedFlowProcess(a, f.ID, "Outer", false)
	escalate := a.agent(f.ID, "Escalate", "Escalate.")
	a.refused("POST", "/api/connections", map[string]any{"source": outer.ID, "target": escalate.ID, "kind": connectionFailure}, http.StatusBadRequest, failureSourceMessage)
	a.refused("POST", "/api/connections", map[string]any{"source": fix.ID, "target": escalate.ID, "kind": "sometimes"}, http.StatusBadRequest, "kind must be")
	for _, s := range []processSettings{{Timeout: ptr(0)}, {Timeout: ptr(maxTimeout + 1)}, {Retries: ptr(-1)}, {Retries: ptr(maxRetries + 1)}} {
		a.refused("PATCH", fmt.Sprintf("/api/processes/%d", fix.ID), map[string]any{"settings": s}, http.StatusBadRequest, "must be from")
	}

	failuresHandles(a, fix.ID, escalate.ID)
	var saved version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &saved)
	var cs []connection
	a.call("GET", fmt.Sprintf("/api/connections?flow=%d", f.ID), nil, http.StatusOK, &cs)
	a.call("DELETE", fmt.Sprintf("/api/connections/%d", cs[0].ID), nil, http.StatusNoContent, nil)
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusNoContent, nil)
	a.call("GET", fmt.Sprintf("/api/connections?flow=%d", f.ID), nil, http.StatusOK, &cs)
	if len(cs) != 1 || cs[0].Kind != connectionFailure {
		t.Errorf("after restoring, connections %+v", cs)
	}
}
