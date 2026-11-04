package tests

// A flow may name the branch its runs work on after what they work on, such
// as feature/{@Ticket.key}; see branch_names.go.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A flow fixing the ticket each run is given, on its repository, with its
// runs' branch named by rule.
func newBranchFlow(a *app, rule string) (flow, process) {
	a.t.Helper()
	f := a.newFlow("Fix tickets")
	branchRule(a, f.ID, rule, http.StatusOK)
	fix := a.agent(f.ID, "Fix", "Fix the ticket.")
	ticket := a.newObject(f.ID, "Ticket")
	nestedAttribute(a, ticket.ID, "key", false)
	a.input(fix.ID, ticket.ID)
	diff := a.newObject(f.ID, "Diff")
	a.output(fix.ID, diff.ID)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", diff.ID), map[string]any{"end": true}, http.StatusOK, nil)
	a.startAt(f.ID, fix.ID)
	a.worker.onAgent("Fix", func(_ context.Context, run agentRun) runResult {
		if run.Resume {
			return runResult{OK: true, Output: "pushed"}
		}
		return submitted(data("fixed"))
	})
	return f, fix
}

func branchRule(a *app, f int64, rule string, want int) {
	a.t.Helper()
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
		"branch":       rule,
	}}, want, nil)
}

func runOnTicket(a *app, f int64, key string) runDetail {
	a.t.Helper()
	var rn run
	a.call("POST", fmt.Sprintf("/api/runs?flow=%d", f), map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"key": key}}}, http.StatusCreated, &rn)
	d := a.finish(rn.ID)
	if d.Status != runDone {
		a.t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	return d
}

// A run given ticket PROJ-123 works on feature/PROJ-123, as its agent is
// told, while the worker keeps the run's own branch; a follow-up and a
// re-run work on it too.
func TestRunsWorkOnTheBranchTheirFlowNames(t *testing.T) {
	a := newApp(t)
	f, _ := newBranchFlow(a, "feature/{@Ticket.key}")
	d := runOnTicket(a, f.ID, "PROJ-123")

	ws := a.worker.runsOf("Fix")[0].Workspace
	if ws == nil || ws.Named != "feature/PROJ-123" || !strings.HasSuffix(ws.Branch, fmt.Sprintf("/run-%d", d.ID)) {
		t.Fatalf("Fix worked in %+v, want its checkouts on feature/PROJ-123 and the run's own branch kept", ws)
	}
	if p := a.worker.runsOf("Fix")[0].Prompt; !strings.Contains(p, `on the branch "feature/PROJ-123"`) || !strings.Contains(p, "Push it with `git push origin feature/PROJ-123`") {
		t.Errorf("Fix wasn't told its branch:\n%s", p)
	}
	step := onlyStep(t, d, "Fix")
	if step.Named != "feature/PROJ-123" || step.Branch != ws.Branch {
		t.Errorf("the step says it worked on %q, kept as %q", step.Named, step.Branch)
	}
	logLineWith(t, a.runLogs(d.ID, 0), "works on the branch feature/PROJ-123")

	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, step.ID), map[string]any{"message": "Push it."}, http.StatusCreated, nil)
	a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
		s := onlyStep(t, d, "Fix")
		return len(s.FollowUps) == 1 && s.FollowUps[0].Status == stepDone
	})
	runs := a.worker.runsOf("Fix")
	if last := runs[len(runs)-1]; !last.Resume || last.Workspace == nil || last.Workspace.Named != "feature/PROJ-123" {
		t.Errorf("the follow-up worked in %+v, want feature/PROJ-123", last.Workspace)
	}

	var rn run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", d.ID, step.ID), map[string]any{}, http.StatusCreated, &rn)
	again := a.finish(rn.ID)
	if again.Status != runDone {
		t.Fatalf("re-run %s; steps %s", again.Status, describeSteps(again))
	}
	runs = a.worker.runsOf("Fix")
	if ws := runs[len(runs)-1].Workspace; ws == nil || ws.Named != "feature/PROJ-123" || !strings.HasSuffix(ws.Branch, fmt.Sprintf("/run-%d", rn.ID)) {
		t.Errorf("the re-run worked in %+v, want feature/PROJ-123 on a branch of its own", ws)
	}

	// Another ticket, another branch.
	d = runOnTicket(a, f.ID, "PROJ-7")
	runs = a.worker.runsOf("Fix")
	if ws := runs[len(runs)-1].Workspace; ws == nil || ws.Named != "feature/PROJ-7" {
		t.Errorf("a run on PROJ-7 worked in %+v", ws)
	}
}

// A rule that isn't one is refused; one naming what runs don't hold is
// warned of, and its runs work on branches of their own, saying why.
func TestBranchRulesNamingNothingLeaveRunsOnTheirOwn(t *testing.T) {
	a := newApp(t)
	f, _ := newBranchFlow(a, "feature/{@Ticket.key}")
	a.refused("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
		"branch":       "feature/{Ticket.key}",
	}}, http.StatusBadRequest, "write each value as {@Object.attribute}")
	if ps := prbGet(a, f.ID); strings.Contains(prbDescribe(ps.Problems), "branch") {
		t.Errorf("a rule naming what runs hold is warned of: %s", prbDescribe(ps.Problems))
	}

	branchRule(a, f.ID, "feature/{@Bug.key}", http.StatusOK)
	ps := prbGet(a, f.ID)
	if got := prbDescribe(ps.Problems); !strings.Contains(got, `warning branch: The branch feature/{@Bug.key} names "Bug"`) {
		t.Errorf("problems: %s", got)
	}
	d := runOnTicket(a, f.ID, "PROJ-123")
	runs := a.worker.runsOf("Fix")
	if ws := runs[len(runs)-1].Workspace; ws == nil || ws.Named != "" || ws.Unnamed != `the branch feature/{@Bug.key} couldn't be named: the top board has no object "Bug"` {
		t.Errorf("Fix worked in %+v, want a branch of the run's own, its clone told why", ws)
	}
	if s := onlyStep(t, d, "Fix"); s.Named != "" {
		t.Errorf("the step says it worked on %q", s.Named)
	}
	logLineWith(t, a.runLogs(d.ID, 0), `works on a branch of its own, as the branch feature/{@Bug.key} couldn't be named: the top board has no object "Bug"`)

	// A blank key names none either: the run works on <prefix>/run-<id>.
	branchRule(a, f.ID, "feature/{@Ticket.key}", http.StatusOK)
	d = runOnTicket(a, f.ID, "   ")
	runs = a.worker.runsOf("Fix")
	ws := runs[len(runs)-1].Workspace
	if ws == nil || ws.Named != "" || !strings.HasSuffix(ws.Branch, fmt.Sprintf("/run-%d", d.ID)) || ws.Unnamed != `the branch feature/{@Ticket.key} couldn't be named: "Ticket"'s "key" is blank` {
		t.Errorf("a run on a blank key worked in %+v, want its own branch, its clone told why", ws)
	}
	logLineWith(t, a.runLogs(d.ID, 0), `works on a branch of its own, as the branch feature/{@Ticket.key} couldn't be named: "Ticket"'s "key" is blank`)

	// A re-run of it says so too.
	var rn run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", d.ID, onlyStep(t, d, "Fix").ID), map[string]any{}, http.StatusCreated, &rn)
	a.finish(rn.ID)
	runs = a.worker.runsOf("Fix")
	if ws := runs[len(runs)-1].Workspace; ws == nil || ws.Named != "" || ws.Unnamed != "the run it re-runs worked on a branch of its own, which a re-run keeps to" {
		t.Errorf("the re-run worked in %+v, want its own branch, its clone told why", ws)
	}
}

// A run picking a ticket's record works on the branch named after it, and
// so does a run a person or a task board started on a record of a flow
// querying the tickets, which isn't given the record as it starts.
func TestBranchesAreNamedAfterRecords(t *testing.T) {
	a := newApp(t)
	s, picking, ticket := pickTickets(a, "Fix picked tickets")
	branchRule(a, picking.ID, "fix/{@Ticket.id}", http.StatusOK)
	a.call("PUT", fmt.Sprintf("/api/objects/%d/pick", ticket.ID), map[string]any{"pick": map[string]any{"state": "open"}}, http.StatusNoContent, nil)
	fix := a.agent(picking.ID, "Fix", "Fix the ticket.")
	a.input(fix.ID, ticket.ID)
	diff := a.newObject(picking.ID, "Diff")
	a.output(fix.ID, diff.ID)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", diff.ID), map[string]any{"end": true}, http.StatusOK, nil)
	a.startAt(picking.ID, fix.ID)
	a.worker.onAgent("Fix", handsIn(data("fixed")))
	pickAdd(a, s.ID, "PROJ-5", "open")

	if d := a.finish(a.startRun(picking.ID).ID); d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if ws := a.worker.runsOf("Fix")[0].Workspace; ws == nil || ws.Named != "fix/PROJ-5" {
		t.Errorf("a run picking PROJ-5 worked in %+v, want fix/PROJ-5", ws)
	}

	querying := a.newFlow("Fix queried tickets")
	branchRule(a, querying.ID, "fix/{@Ticket.id}", http.StatusOK)
	recordCopy(a, s, querying.ID, nil)
	queried := modNamed(t, modObjects(a, querying.ID), "Ticket")
	review := a.agent(querying.ID, "Review", "Review the ticket's fix.")
	a.link(map[string]any{"process": review.ID, "object": queried.ID, "direction": directionInput, "query": linkQuery{State: "open", Limit: 1}})
	notes := a.newObject(querying.ID, "Notes")
	a.output(review.ID, notes.ID)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", notes.ID), map[string]any{"end": true}, http.StatusOK, nil)
	a.startAt(querying.ID, review.ID)
	a.worker.onAgent("Review", handsIn(data("fine")))
	record := pickAdd(a, s.ID, "PROJ-8", "open")

	var rs []run
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": querying.ID, "records": []int64{record}}, http.StatusCreated, &rs)
	if d := a.finish(rs[0].ID); d.Status != runDone {
		t.Fatalf("run on PROJ-8 %s; steps %s", d.Status, describeSteps(d))
	}
	if ws := a.worker.runsOf("Review")[0].Workspace; ws == nil || ws.Named != "fix/PROJ-8" {
		t.Errorf("a run started on PROJ-8 worked in %+v, want fix/PROJ-8", ws)
	}

	// A run on a record runs the version published, which keeps the rule
	// it was published with: changing the rule alone makes a draft, which
	// runs on records take only once published.
	pubPublish(a, querying.ID, "Fix branches")
	branchRule(a, querying.ID, "review/{@Ticket.id}", http.StatusOK)
	if !pubList(a, querying.ID).Draft {
		t.Error("changing the branch alone made no draft")
	}
	onRecord := func(id string) string {
		t.Helper()
		record := pickAdd(a, s.ID, id, "open")
		var rs []run
		a.call("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": querying.ID, "records": []int64{record}}, http.StatusCreated, &rs)
		if d := a.finish(rs[0].ID); d.Status != runDone {
			t.Fatalf("run on %s %s; steps %s", id, d.Status, describeSteps(d))
		}
		runs := a.worker.runsOf("Review")
		if ws := runs[len(runs)-1].Workspace; ws != nil {
			return ws.Named
		}
		return ""
	}
	if got := onRecord("PROJ-10"); got != "fix/PROJ-10" {
		t.Errorf("a run on the version published worked on %q, want fix/PROJ-10", got)
	}
	pubPublish(a, querying.ID, "Review branches")
	if got := onRecord("PROJ-11"); got != "review/PROJ-11" {
		t.Errorf("a run on the version published anew worked on %q, want review/PROJ-11", got)
	}
}

// A subflow process's run works on the branch its procedure names after
// what the process gives it.
func TestSubflowsNameTheirOwnBranch(t *testing.T) {
	a := newApp(t)
	lib := a.newFlow("Fix a ticket")
	branchRule(a, lib.ID, "feature/{@Ticket.key}", http.StatusOK)
	fix := a.agent(lib.ID, "Fix", "Fix the ticket.")
	ticket := a.newObject(lib.ID, "Ticket")
	nestedAttribute(a, ticket.ID, "key", false)
	a.input(fix.ID, ticket.ID)
	diff := a.newObject(lib.ID, "Diff")
	a.output(fix.ID, diff.ID)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", diff.ID), map[string]any{"end": true}, http.StatusOK, nil)
	a.startAt(lib.ID, fix.ID)
	a.worker.onAgent("Fix", handsIn(data("fixed")))

	job := a.newFlow("Fix tickets nightly")
	call := a.newProcess(job.ID, map[string]any{"name": "Fix ticket", "kind": kindSubflow, "settings": map[string]any{"flow": lib.ID}})
	given := a.newObject(job.ID, "Ticket")
	nestedAttribute(a, given.ID, "key", false)
	a.input(call.ID, given.ID)
	a.startAt(job.ID, call.ID)
	var rn run
	a.call("POST", fmt.Sprintf("/api/runs?flow=%d", job.ID), map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"key": "PROJ-3"}}}, http.StatusCreated, &rn)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if ws := a.worker.runsOf("Fix")[0].Workspace; ws == nil || ws.Named != "feature/PROJ-3" {
		t.Errorf("the procedure's Fix worked in %+v, want feature/PROJ-3", ws)
	}
}
