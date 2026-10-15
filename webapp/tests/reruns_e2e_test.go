package tests

// Re-running a run from one of its processes: a new run that copies what
// came before the process and runs it and what follows anew.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type rerunInput struct {
	Object  int64  `json:"object"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Content string `json:"content"`
	Present bool   `json:"present"`
}

type rerunPlan struct {
	Process     int64        `json:"process"`
	ProcessName string       `json:"processName"`
	Inputs      []rerunInput `json:"inputs"`
	Copied      []string     `json:"copied"`
	Anew        []string     `json:"anew"`
}

// Draft writes a ticket, Count counts it, and Publish takes both; Label,
// started along with Draft, labels the flow on a branch of its own.
type rerunFlow struct {
	f                             flow
	draft, count, publish, label  process
	ticket, size, result, labeled object
}

func newRerunFlow(a *app) rerunFlow {
	var r rerunFlow
	r.f = a.newFlow("Rerun")
	r.draft = a.agent(r.f.ID, "Draft", "Write a ticket.")
	r.count = a.newProcess(r.f.ID, map[string]any{"name": "Count", "kind": kindScript, "script": "wc -c"})
	r.publish = a.agent(r.f.ID, "Publish", "Publish it.")
	r.label = a.agent(r.f.ID, "Label", "Label it.")
	r.ticket = a.newObject(r.f.ID, "Ticket")
	r.size = a.newObject(r.f.ID, "Size")
	r.result = a.newObject(r.f.ID, "Result")
	r.labeled = a.newObject(r.f.ID, "Labeled")
	a.output(r.draft.ID, r.ticket.ID)
	a.input(r.count.ID, r.ticket.ID)
	a.output(r.count.ID, r.size.ID)
	a.input(r.publish.ID, r.ticket.ID)
	a.input(r.publish.ID, r.size.ID)
	a.output(r.publish.ID, r.result.ID)
	a.output(r.label.ID, r.labeled.ID)
	a.call("POST", "/api/connections", map[string]any{"source": r.draft.ID, "target": r.label.ID}, http.StatusCreated, nil)
	a.startAt(r.f.ID, r.draft.ID)

	a.worker.onAgent("Draft", handsIn(data("Fix the login")))
	a.worker.onAgent("Label", handsIn(data("bug")))
	a.worker.onAgent("Publish", func(_ context.Context, run agentRun) runResult {
		if strings.Contains(run.Prompt, "Fix the logout") {
			return submitted(data("published Fix the logout"))
		}
		return submitted(data("published"))
	})
	a.worker.onScript("Count", func(_ context.Context, run scriptRun) runResult {
		for _, in := range run.Inputs {
			if in.Name == "Ticket" {
				return runResult{OK: true, Output: fmt.Sprintf(`{"data": %d}`, len(in.Content))}
			}
		}
		return runResult{Error: "no ticket"}
	})
	return r
}

// The re-run starts at Count with the ticket put in its place, copies what
// Draft and Label generated without running them again, and runs Count and
// Publish anew.
func TestRerunFromAProcessCopiesWhatCameBefore(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	first := a.finish(a.startRun(r.f.ID).ID)
	if first.Status != runDone {
		t.Fatalf("first run %s; steps %s", first.Status, describeSteps(first))
	}
	count := onlyStep(t, first, "Count")

	var plan rerunPlan
	a.call("GET", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, count.ID), nil, http.StatusOK, &plan)
	if plan.Process != r.count.ID || len(plan.Inputs) != 1 || plan.Inputs[0].Object != r.ticket.ID ||
		!plan.Inputs[0].Present || !strings.Contains(plan.Inputs[0].Content, "Fix the login") {
		t.Fatalf("plan = %+v, want Count taking the ticket", plan)
	}
	if strings.Join(plan.Copied, ",") != "Labeled" || strings.Join(plan.Anew, ",") != "Size,Result" {
		t.Errorf("plan copies %v and generates %v anew, want Labeled, and Size and Result", plan.Copied, plan.Anew)
	}

	ticket := `{"data": "Fix the logout"}`
	var rn run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, count.ID),
		map[string]any{"inputs": map[string]string{fmt.Sprint(r.ticket.ID): ticket}}, http.StatusCreated, &rn)
	if rn.RerunOf == nil || *rn.RerunOf != first.ID || rn.RerunFrom == nil || *rn.RerunFrom != r.count.ID || rn.RerunFromName != "Count" || rn.Parent != nil {
		t.Errorf("re-run = %+v, want one of run %d from Count", rn, first.ID)
	}
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("re-run %s; steps %s", d.Status, describeSteps(d))
	}
	if len(stepsOf(d, "Draft"))+len(stepsOf(d, "Label")) != 0 {
		t.Errorf("the re-run ran what came before Count: %s", describeSteps(d))
	}
	copied := onlyStep(t, d, fmt.Sprintf("Copied from run #%d", first.ID))
	if copied.Kind != "copied" || copied.Status != stepDone {
		t.Errorf("copied step = %+v", copied)
	}
	var names []string
	for _, o := range nestedObjectsOf(d, copied.ID) {
		names = append(names, o.Name)
	}
	if strings.Join(names, ",") != "Ticket,Labeled" {
		t.Errorf("the re-run copied %v, want Ticket and Labeled", names)
	}
	cs := onlyStep(t, d, "Count")
	if len(cs.Inputs) != 1 || cs.Inputs[0].Content != ticket {
		t.Errorf("Count took %+v, want the ticket put in its place", cs.Inputs)
	}
	if got := contentsOf(d, "Size"); len(got) != 1 || !strings.Contains(got[0], fmt.Sprint(len(ticket))) {
		t.Errorf("Size held %q", got)
	}
	if got := contentsOf(d, "Result"); len(got) != 1 || !strings.Contains(got[0], "Fix the logout") {
		t.Errorf("Result held %q, want what Publish made of the new ticket", got)
	}
	if n := len(a.worker.runsOf("Draft")); n != 1 {
		t.Errorf("Draft ran %d times, want once", n)
	}
}

// A re-run takes over the values its run stored, so generating the same
// unique value again isn't a duplicate.
func TestRerunTakesOverStoredValues(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	nestedPatchObject(a, r.ticket.ID, map[string]any{"persistent": true})
	nestedAttribute(a, r.ticket.ID, "key", true)
	a.worker.onAgent("Draft", handsIn(map[string]any{"key": "T-1"}))
	first := a.finish(a.startRun(r.f.ID).ID)
	if first.Status != runDone {
		t.Fatalf("first run %s; steps %s", first.Status, describeSteps(first))
	}

	var rn run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, onlyStep(t, first, "Draft").ID),
		map[string]any{}, http.StatusCreated, &rn)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Fatalf("re-run from Draft %s, want done; steps %s", d.Status, describeSteps(d))
	}
	// Another run generating it finds the re-run's.
	if d := a.finish(a.startRun(r.f.ID).ID); d.Status != runTerminated {
		t.Errorf("a fresh run %s, want terminated by the re-run's ticket", d.Status)
	}
}

// Only an ended run of the whole flow, from a process still on its top board,
// with what the process takes.
func TestRerunRefusals(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	first := a.finish(a.startRun(r.f.ID).ID)
	count := onlyStep(t, first, "Count")
	path := fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, count.ID)

	a.refused("POST", path, map[string]any{"inputs": map[string]string{fmt.Sprint(r.size.ID): "{}"}}, http.StatusBadRequest, "takes no object")
	a.refused("GET", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, 999999999), nil, http.StatusConflict, "no step")
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", r.count.ID), nil, http.StatusNoContent, nil)
	a.refused("GET", path, nil, http.StatusConflict, "no longer on the flow's board")
	if rs := nestedRuns(a, r.f.ID); len(rs) != 1 {
		t.Errorf("refused re-runs left %d runs", len(rs))
	}
}

// A re-run's branch starts where the run's branch was as the process it
// re-runs from started: with what came before committed, and nothing after.
func TestRerunBranchStartsWhereTheProcessStarted(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", r.f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
	}}, http.StatusOK, nil)
	commit := func(n int) string { return strings.Repeat(fmt.Sprint(n), 40) }
	committing := func(before, after int, out runResult) runResult {
		out.Commits = []repoCommits{{Name: "api", Before: commit(before), After: commit(after), Commits: []string{}}}
		return out
	}
	a.worker.onAgent("Draft", func(context.Context, agentRun) runResult {
		return committing(1, 2, submitted(data("Fix the login")))
	})
	a.worker.onScript("Count", func(context.Context, scriptRun) runResult {
		return committing(2, 3, runResult{OK: true, Output: `{"data": 13}`})
	})
	a.worker.onAgent("Publish", func(context.Context, agentRun) runResult {
		return committing(3, 4, submitted(data("published")))
	})
	first := a.finish(a.startRun(r.f.ID).ID)
	if first.Status != runDone {
		t.Fatalf("first run %s; steps %s", first.Status, describeSteps(first))
	}

	for _, c := range []struct {
		from  string
		start int
	}{{"Count", 2}, {"Publish", 3}, {"Draft", 1}} {
		var rn run
		a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, onlyStep(t, first, c.from).ID), map[string]any{}, http.StatusCreated, &rn)
		if d := a.finish(rn.ID); d.Status != runDone {
			t.Fatalf("re-run from %s %s; steps %s", c.from, d.Status, describeSteps(d))
		}
		var ws *workspaceSpec
		if c.from == "Count" {
			a.worker.mu.Lock()
			ws = a.worker.scriptRuns[len(a.worker.scriptRuns)-1].Workspace
			a.worker.mu.Unlock()
		} else {
			runs := a.worker.runsOf(c.from)
			ws = runs[len(runs)-1].Workspace
		}
		if ws == nil || ws.Branch != fmt.Sprintf("flow/rerun/run-%d", rn.ID) || len(ws.Repositories) != 1 || ws.Repositories[0].Start != commit(c.start) {
			t.Errorf("re-run from %s worked in %+v, want its own branch from %s", c.from, ws, commit(c.start))
		}
	}
	// A run of its own starts from the base.
	d := a.finish(a.startRun(r.f.ID).ID)
	runs := a.worker.runsOf("Draft")
	if ws := runs[len(runs)-1].Workspace; d.Status != runDone || ws == nil || ws.Repositories[0].Start != "" {
		t.Errorf("a fresh run worked in %+v", ws)
	}
}

// Re-running from a recording flow process runs its board as a new run,
// which may store the same unique values the flow process's run did: that
// run's work is being redone, so its values are released with the run's.
func TestRerunFromARecordingFlowProcess(t *testing.T) {
	a := newApp(t)
	n := newNestedTriage(t, a, true)
	nestedPatchObject(a, n.summary.ID, map[string]any{"persistent": true})
	nestedAttribute(a, n.summary.ID, "id", true)
	a.worker.onAgent("Sum", handsIn(map[string]any{"id": "E1"}))
	first := nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)

	var rn run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", first.ID, onlyStep(t, first, "Summarize").ID), map[string]any{}, http.StatusCreated, &rn)
	d := nestedFinish(t, a, rn.ID, runDone)
	fs := onlyStep(t, d, "Summarize")
	if fs.Status != stepDone || fs.ChildRun == nil {
		t.Fatalf("Summarize step %s %q", fs.Status, fs.Error)
	}
	if child := a.getRun(*fs.ChildRun); child.Status != runDone {
		t.Errorf("Summarize's new run %s, want done; steps %s", child.Status, describeSteps(child))
	}
	if len(stepsOf(d, "Publish")) != 1 {
		t.Errorf("Publish didn't run after Summarize: %s", describeSteps(d))
	}
}

// The run of a recording flow process can be re-run from a process on its
// board: another run of the board in the same run, given what the flow
// process took, which the process its board starts from takes too. A re-run
// of that is given the same.
func TestRerunInsideAFlowProcessRun(t *testing.T) {
	a := newApp(t)
	n := newNestedTriage(t, a, true)
	first := nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)
	child := a.getRun(*onlyStep(t, first, "Summarize").ChildRun)

	rerunFrom := func(src runDetail, process string, inputs map[string]string) (rerunPlan, runDetail) {
		t.Helper()
		path := fmt.Sprintf("/api/runs/%d/steps/%d/rerun", src.ID, onlyStep(t, src, process).ID)
		var plan rerunPlan
		a.call("GET", path, nil, http.StatusOK, &plan)
		var rn run
		a.call("POST", path, map[string]any{"inputs": inputs}, http.StatusCreated, &rn)
		if rn.Parent == nil || *rn.Parent != first.ID || rn.Process == nil || *rn.Process != n.fp.ID || rn.ProcessName != "Summarize" ||
			*rn.RerunOf != src.ID || rn.RerunFromName != process {
			t.Errorf("re-run of run %d from %s = %+v, want another run of Summarize in run %d", src.ID, process, rn, first.ID)
		}
		return plan, nestedFinish(t, a, rn.ID, runDone)
	}

	plan, d := rerunFrom(child, "Sum", map[string]string{fmt.Sprint(n.notes.ID): `{"data": "better notes"}`})
	if len(plan.Inputs) != 1 || plan.Inputs[0].Name != "Notes" || !strings.Contains(plan.Inputs[0].Content, "notes on the crash") ||
		strings.Join(plan.Copied, ",") != "Ticket" || strings.Join(plan.Anew, ",") != "Summary" {
		t.Errorf("plan from Sum = %+v", plan)
	}
	if len(stepsOf(d, "Read")) != 0 {
		t.Errorf("Read ran again: %s", describeSteps(d))
	}
	if s := onlyStep(t, d, "Sum"); len(s.Inputs) != 1 || !strings.Contains(s.Inputs[0].Content, "better notes") {
		t.Errorf("Sum took %+v", s.Inputs)
	}
	if got := contentsOf(d, "Summary"); len(got) != 1 || !strings.Contains(got[0], "a summary") {
		t.Errorf("Summary held %q", got)
	}

	// A re-run of the re-run, from the process the board starts from, which
	// takes what came from outside.
	plan, d = rerunFrom(d, "Sum", nil)
	if len(plan.Inputs) != 1 || !strings.Contains(plan.Inputs[0].Content, "better notes") {
		t.Errorf("plan of the re-run's re-run = %+v", plan)
	}
	plan, d = rerunFrom(child, "Read", map[string]string{fmt.Sprint(n.ticket.ID): `{"data": "logout crashes"}`})
	if len(plan.Inputs) != 1 || plan.Inputs[0].Name != "Ticket" || !strings.Contains(plan.Inputs[0].Content, "login crashes") {
		t.Errorf("plan from Read = %+v", plan)
	}
	if s := onlyStep(t, d, "Read"); len(s.Inputs) != 1 || !strings.Contains(s.Inputs[0].Content, "logout crashes") {
		t.Errorf("Read took %+v", s.Inputs)
	}
	onlyStep(t, d, "Sum")
	if n := len(a.worker.runsOf("Write")) + len(a.worker.runsOf("Publish")); n != 2 {
		t.Errorf("the outer run's processes ran %d times, want once each", n)
	}
}
