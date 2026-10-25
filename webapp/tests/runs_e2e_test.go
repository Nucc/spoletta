package tests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// An agent's output goes to the script taking it, and the run records both
// steps, what they generated and the board it ran on.
func TestRunPassesOutputsDownTheChain(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Chain")
	draft := a.agent(f.ID, "Draft", "Write a ticket.")
	ticket := a.newObject(f.ID, "Ticket")
	count := a.newProcess(f.ID, map[string]any{"name": "Count", "kind": kindScript, "script": "wc -c < \"$INPUTS/Ticket\""})
	size := a.newObject(f.ID, "Size")
	a.output(draft.ID, ticket.ID)
	a.input(count.ID, ticket.ID)
	a.output(count.ID, size.ID)
	a.startAt(f.ID, draft.ID)

	a.worker.onAgent("Draft", handsIn(data("Fix the login")))
	a.worker.onScript("Count", func(_ context.Context, run scriptRun) runResult {
		return runResult{OK: true, Output: `{"data": 13}`}
	})

	rn := a.startRun(f.ID)
	if rn.Status != runRunning || rn.Version == nil {
		t.Fatalf("started run = %+v, want running on a version", rn)
	}
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s, want done; steps %s", d.Status, describeSteps(d))
	}

	ds := onlyStep(t, d, "Draft")
	if ds.Status != stepDone || !strings.Contains(ds.Prompt, "Write a ticket.") {
		t.Errorf("Draft step = %s with prompt %q", ds.Status, ds.Prompt)
	}
	cs := onlyStep(t, d, "Count")
	if cs.Status != stepDone || len(cs.Inputs) != 1 || cs.Inputs[0].Name != "Ticket" {
		t.Errorf("Count step = %s with inputs %+v", cs.Status, cs.Inputs)
	}

	a.worker.mu.Lock()
	scripts := a.worker.scriptRuns
	a.worker.mu.Unlock()
	if len(scripts) != 1 || len(scripts[0].Inputs) != 1 || !strings.Contains(scripts[0].Inputs[0].Content, "Fix the login") {
		t.Errorf("the worker ran scripts %+v, want Count with the ticket", scripts)
	}
	if got := contentsOf(d, "Ticket"); len(got) != 1 || !strings.Contains(got[0], "Fix the login") {
		t.Errorf("Ticket held %q", got)
	}
	if got := contentsOf(d, "Size"); len(got) != 1 || !strings.Contains(got[0], "13") {
		t.Errorf("Size held %q", got)
	}

	// The agent's conversation is recorded as its step's.
	var msgs []stepMessage
	a.call("GET", fmt.Sprintf("/api/runs/%d/steps/%d/messages", rn.ID, ds.ID), nil, http.StatusOK, &msgs)
	if len(msgs) != 1 || !strings.Contains(string(msgs[0].Message), "working on Draft") {
		t.Errorf("Draft's messages = %+v", msgs)
	}

	// The run's version has the board as it ran.
	var s snapshot
	a.call("GET", fmt.Sprintf("/api/versions/%d", *rn.Version), nil, http.StatusOK, &s)
	if len(s.Processes) != 2 || s.Start.Target == nil || *s.Start.Target != draft.ID {
		t.Errorf("version has %d processes and start %+v", len(s.Processes), s.Start)
	}
}

// A failing step fails the run, and what would take its output never runs.
func TestFailedStepEndsItsBranch(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Failing")
	draft := a.agent(f.ID, "Draft", "Write a ticket.")
	ticket := a.newObject(f.ID, "Ticket")
	publish := a.agent(f.ID, "Publish", "Publish it.")
	a.output(draft.ID, ticket.ID)
	a.input(publish.ID, ticket.ID)
	a.startAt(f.ID, draft.ID)

	a.worker.onAgent("Draft", func(context.Context, agentRun) runResult {
		return runResult{Error: "the model is down"}
	})
	a.worker.onAgent("Publish", handsIn(data("published")))

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runFailed {
		t.Fatalf("run %s, want failed; steps %s", d.Status, describeSteps(d))
	}
	if s := onlyStep(t, d, "Draft"); s.Status != stepError || s.Error != "the model is down" {
		t.Errorf("Draft step = %s %q", s.Status, s.Error)
	}
	if n := len(stepsOf(d, "Publish")); n != 0 {
		t.Errorf("Publish ran %d times after Draft failed", n)
	}
	if len(d.Objects) != 0 {
		t.Errorf("the failed run generated %+v", d.Objects)
	}
}

// An agent that doesn't hand in its output through submit_output is asked
// once more; failing again fails its step.
func TestAgentNotHandingInIsAskedOnce(t *testing.T) {
	for _, c := range []struct {
		name    string
		replies []runResult
		status  string
	}{
		{"second try hands in", []runResult{{OK: true, Output: "Fix the login"}, submitted(data("Fix the login"))}, runDone},
		{"neither does", []runResult{{OK: true, Output: "Fix the login"}, {OK: true, Output: "still plain"}}, runFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Contract")
			draft := a.agent(f.ID, "Draft", "Write a ticket.")
			a.output(draft.ID, a.newObject(f.ID, "Ticket").ID)
			a.startAt(f.ID, draft.ID)

			var mu sync.Mutex
			tries := 0
			a.worker.onAgent("Draft", func(context.Context, agentRun) runResult {
				mu.Lock()
				defer mu.Unlock()
				tries++
				return c.replies[tries-1]
			})

			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != c.status {
				t.Fatalf("run %s, want %s; steps %s", d.Status, c.status, describeSteps(d))
			}
			runs := a.worker.runsOf("Draft")
			if len(runs) != 2 {
				t.Fatalf("Draft ran %d times, want 2", len(runs))
			}
			if runs[0].Output == nil {
				t.Error("Draft wasn't told what to hand in")
			}
			if !strings.Contains(runs[1].Prompt, "Fix the login") {
				t.Errorf("the second try's prompt doesn't hold the rejected reply: %q", runs[1].Prompt)
			}
			s := onlyStep(t, d, "Draft")
			if c.status == runFailed && !strings.Contains(s.Error, "twice") {
				t.Errorf("Draft step error = %q", s.Error)
			}
		})
	}
}

// A person reviewing a draft sends it back with instructions, which reach
// the agent as a change request; approving it goes on to publishing.
func TestHumanReviewSendsChangesBack(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Review")
	draft := a.agent(f.ID, "Draft", "Write the release notes.")
	doc := a.newObject(f.ID, "Notes")
	approved := a.newState(doc.ID, "approved")
	changes := a.newState(doc.ID, "changes")
	review := a.newProcess(f.ID, map[string]any{"name": "Review", "kind": kindHuman, "prompt": "Check the notes."})
	publish := a.agent(f.ID, "Publish", "Publish the notes.")
	a.output(draft.ID, doc.ID)
	a.input(review.ID, doc.ID)
	a.link(map[string]any{"process": review.ID, "object": doc.ID, "direction": directionOutput, "states": []int64{approved.ID, changes.ID}})
	a.link(map[string]any{"process": draft.ID, "object": doc.ID, "direction": directionInput, "state": changes.ID})
	a.link(map[string]any{"process": publish.ID, "object": doc.ID, "direction": directionInput, "state": approved.ID})
	a.startAt(f.ID, draft.ID)

	var mu sync.Mutex
	version := 0
	a.worker.onAgent("Draft", func(context.Context, agentRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		version++
		return submitted(data(fmt.Sprintf("notes v%d", version)))
	})
	a.worker.onAgent("Publish", handsIn(data("published")))

	rn := a.startRun(f.ID)
	first := a.waitStep(rn.ID, "Review", stepWaiting)
	if len(first.Inputs) != 1 || !strings.Contains(first.Inputs[0].Content, "notes v1") {
		t.Fatalf("the review shows %+v", first.Inputs)
	}
	// Only a state its output link lists can be decided on.
	other := a.newState(a.newObject(f.ID, "Other").ID, "done")
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, first.ID), map[string]any{"state": other.ID}, http.StatusBadRequest, "can't decide on that state")

	a.call("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, first.ID),
		map[string]any{"state": changes.ID, "instructions": "Mention the login fix."}, http.StatusNoContent, nil)
	// A review is decided once.
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, first.ID), map[string]any{"state": approved.ID}, http.StatusConflict, "no review waiting")

	second := a.waitStep(rn.ID, "Review", stepWaiting, first.ID)
	runs := a.worker.runsOf("Draft")
	if len(runs) != 2 {
		t.Fatalf("Draft ran %d times, want 2", len(runs))
	}
	for _, want := range []string{"notes v1", `"changes"`, "<change_request>\nMention the login fix.\n</change_request>"} {
		if !strings.Contains(runs[1].Prompt, want) {
			t.Errorf("the revision's prompt lacks %q:\n%s", want, runs[1].Prompt)
		}
	}
	if len(stepsOf(a.getRun(rn.ID), "Publish")) != 0 {
		t.Error("Publish ran before the notes were approved")
	}

	a.call("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, second.ID), map[string]any{"state": approved.ID}, http.StatusNoContent, nil)
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s, want done; steps %s", d.Status, describeSteps(d))
	}
	var outcomes []string
	for _, s := range stepsOf(d, "Review") {
		outcomes = append(outcomes, s.Outcome)
	}
	if strings.Join(outcomes, ",") != "changes,approved" {
		t.Errorf("review outcomes = %v", outcomes)
	}
	ps := onlyStep(t, d, "Publish")
	if len(ps.Inputs) != 1 || ps.Inputs[0].State != "approved" || !strings.Contains(ps.Inputs[0].Content, "notes v2") {
		t.Errorf("Publish took %+v, want the approved notes v2", ps.Inputs)
	}
}

// Stopping a run cancels the step going on; while it runs, the flow can be
// neither deleted nor switched to another version.
func TestStoppingARun(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Stopping")
	slow := a.agent(f.ID, "Slow", "Take your time.")
	a.output(slow.ID, a.newObject(f.ID, "Result").ID)
	a.startAt(f.ID, slow.ID)

	// Whether the step was cancelled; it gives up after a while, so a stop
	// that cancels nothing fails the test rather than hanging it.
	cancelled := make(chan bool, 1)
	a.worker.onAgent("Slow", func(ctx context.Context, _ agentRun) runResult {
		select {
		case <-ctx.Done():
			cancelled <- true
		case <-time.After(5 * time.Second):
			cancelled <- false
		}
		return runResult{Error: "cancelled"}
	})

	rn := a.startRun(f.ID)
	a.waitStep(rn.ID, "Slow", stepActive)
	a.refused("DELETE", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusConflict, "while it is running")
	a.refused("POST", fmt.Sprintf("/api/versions/%d/restore", *rn.Version), nil, http.StatusConflict, "while the flow is running")

	var ended run
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, &ended)
	if ended.Status != runInterrupted || ended.FinishedAt == nil {
		t.Errorf("stopped run = %+v", ended)
	}
	if !receive(t, cancelled, "the step to say whether it was cancelled") {
		t.Fatal("stopping the run didn't cancel its step")
	}
	d := a.waitRun(rn.ID, "its step to end", func(d runDetail) bool {
		ss := stepsOf(d, "Slow")
		return len(ss) == 1 && ss[0].Status == stepError
	})
	if d.Status != runInterrupted {
		t.Errorf("run %s, want interrupted", d.Status)
	}
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runDone}, http.StatusBadRequest, "must be interrupted")
}

// A subflow process runs another flow with the objects it takes, as a run of
// that flow, and what that flow leaves at its end comes out of it.
func TestSubflowOutcomesComeOut(t *testing.T) {
	a := newApp(t)
	callee := a.newFlow("Summarize")
	ticketThere := a.newObject(callee.ID, "Ticket")
	summarizer := a.agent(callee.ID, "Summarizer", "Summarize the ticket.")
	summary := a.newObject(callee.ID, "Summary")
	a.input(summarizer.ID, ticketThere.ID)
	a.output(summarizer.ID, summary.ID)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", summary.ID), map[string]any{"end": true}, http.StatusOK, nil)
	a.startAt(callee.ID, summarizer.ID)

	caller := a.newFlow("Triage")
	write := a.agent(caller.ID, "Write", "Write a ticket.")
	ticket := a.newObject(caller.ID, "Ticket")
	call := a.newProcess(caller.ID, map[string]any{"name": "Call", "kind": kindSubflow, "settings": map[string]any{"flow": callee.ID}})
	a.output(write.ID, ticket.ID)
	a.input(call.ID, ticket.ID)

	// The outcome came onto the caller's board, generated by the subflow process.
	var objs []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", caller.ID), nil, http.StatusOK, &objs)
	var outcome *object
	for i := range objs {
		if objs[i].Name == "Summary" {
			outcome = &objs[i]
		}
	}
	if outcome == nil {
		t.Fatalf("no Summary on the caller's board: %+v", objs)
	}
	publish := a.agent(caller.ID, "Publish", "Publish the summary.")
	a.input(publish.ID, outcome.ID)
	a.startAt(caller.ID, write.ID)

	// Neither flow can run itself, directly or through the other.
	a.refused("POST", "/api/processes", map[string]any{"flow": caller.ID, "name": "Again", "kind": kindSubflow, "settings": map[string]any{"flow": caller.ID}}, http.StatusBadRequest, "can't run itself")
	a.refused("POST", "/api/processes", map[string]any{"flow": callee.ID, "name": "Back", "kind": kindSubflow, "settings": map[string]any{"flow": caller.ID}}, http.StatusBadRequest, "can't run itself")

	a.worker.onAgent("Write", handsIn(data("crash in login")))
	a.worker.onAgent("Summarizer", handsIn(data("login crashes")))
	a.worker.onAgent("Publish", handsIn(data("posted")))

	rn := a.startRun(caller.ID)
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s, want done; steps %s", d.Status, describeSteps(d))
	}
	if runs := a.worker.runsOf("Summarizer"); len(runs) != 1 || !strings.Contains(runs[0].Prompt, "crash in login") {
		t.Errorf("Summarizer got %+v, want the caller's ticket", runs)
	}
	ps := onlyStep(t, d, "Publish")
	if len(ps.Inputs) != 1 || !strings.Contains(ps.Inputs[0].Content, "login crashes") {
		t.Errorf("Publish took %+v, want the callee's summary", ps.Inputs)
	}

	cs := onlyStep(t, d, "Call")
	if cs.ChildRun == nil {
		t.Fatal("the subflow step refers to no run")
	}
	child := a.getRun(*cs.ChildRun)
	if child.Flow != callee.ID || child.Parent == nil || *child.Parent != rn.ID || child.Status != runDone {
		t.Errorf("the subflow's run = %+v, want a done run of the callee in run %d", child.run, rn.ID)
	}
	onlyStep(t, child, "Summarizer")
}
