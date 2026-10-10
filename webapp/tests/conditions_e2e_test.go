package tests

// Condition links, OPM's: a process taking an object on a condition runs
// only if the object is in the state the link names, and is skipped
// otherwise, the flow going on without what it would have generated. And
// quorums: a process running once enough of what it takes came.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func conditionsOn(a *app, process, object, state int64) link {
	a.t.Helper()
	return a.link(map[string]any{"process": process, "object": object, "direction": directionInput, "state": state, "condition": true})
}

// A security review runs only for changes touching auth; otherwise it is
// skipped, and so is what takes only its report, while the merge goes on
// with the change alone.
func TestConditionSkipsAndTheFlowGoesOn(t *testing.T) {
	for _, c := range []struct {
		kind    string
		reviews bool
	}{{"auth", true}, {"other", false}} {
		t.Run(c.kind, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Conditions")
			classify := a.agent(f.ID, "Classify", "Classify the change.")
			change := a.newObject(f.ID, "Change")
			auth := a.newState(change.ID, "auth")
			other := a.newState(change.ID, "other")
			security := a.agent(f.ID, "Security", "Review security.")
			report := a.newObject(f.ID, "Report")
			summarize := a.agent(f.ID, "Summarize", "Summarize the report.")
			summary := a.newObject(f.ID, "Summary")
			merge := a.agent(f.ID, "Merge", "Merge it.")
			decisionsDecides(a, classify.ID, change.ID, auth.ID, other.ID)
			l := conditionsOn(a, security.ID, change.ID, auth.ID)
			if !l.Condition {
				t.Errorf("link = %+v", l)
			}
			a.output(security.ID, report.ID)
			a.input(summarize.ID, report.ID)
			a.output(summarize.ID, summary.ID)
			a.input(merge.ID, change.ID)
			a.input(merge.ID, report.ID)
			a.startAt(f.ID, classify.ID)
			a.worker.onAgent("Classify", handsIn(decisionsReply(data("diff"), map[string]any{"Change": decisionsState(c.kind)})))
			a.worker.onAgent("Security", handsIn(data("no issues")))
			a.worker.onAgent("Summarize", handsIn(data("fine")))
			a.worker.onAgent("Merge", handsIn(data("merged")))

			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != runDone {
				t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
			}
			sec, sum := onlyStep(t, d, "Security"), onlyStep(t, d, "Summarize")
			names := coreInputNames(onlyStep(t, d, "Merge"))
			if c.reviews {
				if sec.Status != stepDone || sum.Status != stepDone || !slices.Equal(names, []string{"Change", "Report"}) {
					t.Errorf("steps %s, Merge took %v", describeSteps(d), names)
				}
				return
			}
			if sec.Status != stepSkipped || sec.Error != `Its condition doesn't hold: "Change" is "other", not "auth".` {
				t.Errorf("Security = %s %q", sec.Status, sec.Error)
			}
			if sum.Status != stepSkipped || !strings.Contains(sum.Error, "Nothing it takes came") {
				t.Errorf("Summarize = %s %q", sum.Status, sum.Error)
			}
			if !slices.Equal(names, []string{"Change"}) {
				t.Errorf("Merge took %v, want the change alone", names)
			}
			for _, p := range []string{"Security", "Summarize"} {
				if len(a.worker.runsOf(p)) != 0 {
					t.Errorf("%s ran in the worker", p)
				}
			}
		})
	}
}

// A process started on, whose condition doesn't hold, is skipped too.
func TestConditionOfTheFirstProcess(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("First")
	gate := a.newObject(f.ID, "Gate")
	open := a.newState(gate.ID, "open")
	first := a.agent(f.ID, "First", "Go.")
	conditionsOn(a, first.ID, gate.ID, open.ID)
	a.startAt(f.ID, first.ID)
	d := a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "First"); d.Status != runDone || s.Status != stepSkipped || !strings.Contains(s.Error, `"Gate" is in no state, not "open"`) {
		t.Errorf("run %s, First %s %q", d.Status, s.Status, s.Error)
	}
}

// Three reviewers approve or reject; the merge needs two approvals. It runs
// once two came, or is skipped once that can no longer be.
func TestQuorumOfConditions(t *testing.T) {
	for _, c := range []struct {
		verdicts []string
		merges   bool
	}{
		{[]string{"approved", "approved", "rejected"}, true},
		{[]string{"approved", "rejected", "rejected"}, false},
		{[]string{"approved", "approved", "approved"}, true},
	} {
		t.Run(strings.Join(c.verdicts, "-"), func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Quorum")
			seed := a.agent(f.ID, "Seed", "Seed.")
			pr := a.newObject(f.ID, "PR")
			merge := a.agent(f.ID, "Merge", "Merge it.")
			a.output(seed.ID, pr.ID)
			a.startAt(f.ID, seed.ID)
			for i, v := range c.verdicts {
				name := fmt.Sprintf("Reviewer %d", i+1)
				r := a.agent(f.ID, name, "Review.")
				verdict := a.newObject(f.ID, fmt.Sprintf("Verdict %d", i+1))
				approved := a.newState(verdict.ID, "approved")
				rejected := a.newState(verdict.ID, "rejected")
				a.input(r.ID, pr.ID)
				state := approved.ID
				if v == "rejected" {
					state = rejected.ID
				}
				a.link(map[string]any{"process": r.ID, "object": verdict.ID, "direction": directionOutput, "state": state})
				conditionsOn(a, merge.ID, verdict.ID, approved.ID)
				a.worker.onAgent(name, handsIn(data(v)))
			}
			failuresSettings(a, merge.ID, processSettings{Quorum: ptr(2)})
			a.worker.onAgent("Seed", handsIn(data("diff")))
			a.worker.onAgent("Merge", handsIn(data("merged")))

			d := a.finish(a.startRun(f.ID).ID)
			s := onlyStep(t, d, "Merge")
			if d.Status != runDone || (s.Status == stepDone) != c.merges {
				t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
			}
			if c.merges {
				for _, in := range s.Inputs {
					if in.State != "approved" {
						t.Errorf("Merge took %+v", s.Inputs)
					}
				}
				if len(s.Inputs) < 2 {
					t.Errorf("Merge took %d verdicts", len(s.Inputs))
				}
			} else if s.Status != stepSkipped || !strings.Contains(s.Error, "Fewer than 2") {
				t.Errorf("Merge = %s %q", s.Status, s.Error)
			}
		})
	}
}

// Only an input link naming a state is a condition; quorums are positive;
// a version keeps conditions.
func TestConditionRefusals(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Condition refusals")
	p := a.agent(f.ID, "P", "P.")
	o := a.newObject(f.ID, "O")
	st := a.newState(o.ID, "ready")
	a.refused("POST", "/api/links", map[string]any{"process": p.ID, "object": o.ID, "direction": directionInput, "condition": true}, http.StatusBadRequest, "only an input link naming a state")
	a.refused("POST", "/api/links", map[string]any{"process": p.ID, "object": o.ID, "direction": directionOutput, "state": st.ID, "condition": true}, http.StatusBadRequest, "only an input link naming a state")
	plain := a.input(p.ID, o.ID)
	a.refused("PUT", fmt.Sprintf("/api/links/%d/condition", plain.ID), map[string]any{"condition": true}, http.StatusBadRequest, "only an input link naming a state")
	event := humanTakesIn(a, p.ID, o.ID, st.ID)
	a.call("PUT", fmt.Sprintf("/api/links/%d/condition", event.ID), map[string]any{"condition": true}, http.StatusNoContent, nil)
	a.refused("PATCH", fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"settings": processSettings{Quorum: ptr(0)}}, http.StatusBadRequest, "quorum must be at least 1")

	var saved version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &saved)
	a.call("PUT", fmt.Sprintf("/api/links/%d/condition", event.ID), map[string]any{"condition": false}, http.StatusNoContent, nil)
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusNoContent, nil)
	var ls []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &ls)
	if i := slices.IndexFunc(ls, func(l link) bool { return l.State != nil }); i < 0 || !ls[i].Condition {
		t.Errorf("after restoring, links %+v", ls)
	}
}
