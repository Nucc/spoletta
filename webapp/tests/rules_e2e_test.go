package tests

// States with rules, which put an object generated in no state in the first
// state whose rule holds of it, and the usage of agent steps.

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func rulesState(a *app, object int64, name, rule string) objectState {
	a.t.Helper()
	var s objectState
	a.call("POST", fmt.Sprintf("/api/objects/%d/states", object), map[string]any{"name": name, "rule": rule}, http.StatusCreated, &s)
	return s
}

// An assessment comes out confident or unsure by its confidence, and goes
// to what takes it in that state, with no agent deciding.
func TestRuleRoutesByWhatTheObjectHolds(t *testing.T) {
	for _, c := range []struct {
		confidence float64
		state      string
		next       string
	}{{0.95, "confident", "Ship"}, {0.4, "unsure", "Ask"}, {0.8, "confident", "Ship"}} {
		t.Run(fmt.Sprint(c.confidence), func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Rules")
			assess := a.agent(f.ID, "Assess", "Assess it.")
			assessment := a.newObject(f.ID, "Assessment")
			humanAttribute(a, assessment.ID, "confidence")
			humanAttribute(a, assessment.ID, "verdict")
			confident := rulesState(a, assessment.ID, "confident", "confidence >= 0.8")
			unsure := rulesState(a, assessment.ID, "unsure", "confidence < 0.8")
			if confident.Rule != "confidence >= 0.8" {
				t.Errorf("state = %+v", confident)
			}
			ship := a.agent(f.ID, "Ship", "Ship it.")
			ask := a.agent(f.ID, "Ask", "Ask someone.")
			a.output(assess.ID, assessment.ID)
			humanTakesIn(a, ship.ID, assessment.ID, confident.ID)
			humanTakesIn(a, ask.ID, assessment.ID, unsure.ID)
			a.startAt(f.ID, assess.ID)
			a.worker.onAgent("Assess", handsIn(map[string]any{"confidence": c.confidence, "verdict": "fine"}))
			a.worker.onAgent("Ship", handsIn(data("shipped")))
			a.worker.onAgent("Ask", handsIn(data("asked")))

			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != runDone || len(stepsOf(d, c.next)) != 1 || len(stepsOf(d, "Ship"))+len(stepsOf(d, "Ask")) != 1 {
				t.Fatalf("run %s with steps %s, want %s only", d.Status, describeSteps(d), c.next)
			}
			if got := decisionsStatesOf(d, "Assessment"); !slices.Equal(got, []string{c.state}) {
				t.Errorf("assessment states %q, want %q", got, c.state)
			}
		})
	}
}

// What a person fills in comes out in the state its rules say.
func TestRuleOfWhatAPersonFillsIn(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Rules by hand")
	intake := humanHuman(a, f.ID, "Intake")
	humanPerson(a, f.ID, intake.ID, "Support")
	ticket := a.newObject(f.ID, "Ticket")
	humanAttribute(a, ticket.ID, "priority")
	urgent := rulesState(a, ticket.ID, "urgent", `priority == "p1"`)
	page := a.agent(f.ID, "Page", "Page someone.")
	a.output(intake.ID, ticket.ID)
	humanTakesIn(a, page.ID, ticket.ID, urgent.ID)
	a.startAt(f.ID, intake.ID)
	a.worker.onAgent("Page", handsIn(data("paged")))

	rn := a.startRun(f.ID)
	s := a.waitStep(rn.ID, "Intake", stepWaiting)
	humanMustDecide(a, rn.ID, s.ID, map[string]any{"entries": map[string]any{fmt.Sprint(ticket.ID): map[string]any{"priority": "p1"}}})
	d := a.finish(rn.ID)
	if d.Status != runDone || len(stepsOf(d, "Page")) != 1 {
		t.Errorf("run %s with steps %s", d.Status, describeSteps(d))
	}
}

// A rule must read as comparisons; one can be changed on its own, and
// cleared; a version keeps it.
func TestRuleRefusals(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Rule refusals")
	o := a.newObject(f.ID, "O")
	a.refused("POST", fmt.Sprintf("/api/objects/%d/states", o.ID), map[string]any{"name": "high", "rule": "score"}, http.StatusBadRequest, "rule:")
	st := rulesState(a, o.ID, "high", "score > 5")
	a.refused("PATCH", fmt.Sprintf("/api/states/%d", st.ID), map[string]any{"rule": "score >"}, http.StatusBadRequest, "rule:")
	var got objectState
	a.call("PATCH", fmt.Sprintf("/api/states/%d", st.ID), map[string]any{"rule": "score > 9"}, http.StatusOK, &got)
	if got.Name != "high" || got.Rule != "score > 9" {
		t.Errorf("after changing the rule, %+v", got)
	}
	a.call("PATCH", fmt.Sprintf("/api/states/%d", st.ID), map[string]any{"name": "top"}, http.StatusOK, &got)
	if got.Name != "top" || got.Rule != "score > 9" {
		t.Errorf("after renaming, %+v", got)
	}

	var saved version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &saved)
	got = objectState{}
	a.call("PATCH", fmt.Sprintf("/api/states/%d", st.ID), map[string]any{"rule": ""}, http.StatusOK, &got)
	if got.Rule != "" {
		t.Errorf("after clearing, %+v", got)
	}
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusNoContent, nil)
	var objs []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f.ID), nil, http.StatusOK, &objs)
	if len(objs) != 1 || len(objs[0].States) != 1 || objs[0].States[0].Rule != "score > 9" {
		t.Errorf("after restoring, %+v", objs)
	}
}

// An agent step adds up what its model's replies used.
func TestAgentStepUsage(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Usage")
	think := a.agent(f.ID, "Think", "Think.")
	tool := a.agent(f.ID, "Tool", "Use a tool.")
	a.call("POST", "/api/connections", map[string]any{"source": think.ID, "target": tool.ID}, http.StatusCreated, nil)
	a.startAt(f.ID, think.ID)
	reply := func(in, out int, cost float64) map[string]any {
		return map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "hm"}},
			"usage": map[string]any{"input": in, "output": out, "totalTokens": in + out, "cost": map[string]any{"total": cost}}}
	}
	a.worker.onMessages("Think", reply(1000, 200, 0.01), map[string]any{"role": "toolResult", "content": []map[string]any{}}, reply(1500, 300, 0.02))
	a.worker.onAgent("Think", corePlain("thought"))
	a.worker.onAgent("Tool", corePlain("done"))

	d := a.finish(a.startRun(f.ID).ID)
	u := onlyStep(t, d, "Think").Usage
	if u == nil || u.Input != 2500 || u.Output != 500 || u.Cost < 0.0299 || u.Cost > 0.0301 {
		t.Errorf("Think's usage = %+v", u)
	}
	if u := onlyStep(t, d, "Tool").Usage; u != nil {
		t.Errorf("Tool's usage = %+v, want none reported", u)
	}
}
