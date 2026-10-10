package tests

// Agents deciding states: an agent's output link can list states of its
// object, and the agent decides which one the object comes out in. One it
// generates comes out in the state it decides, as a classifier would put
// it; one it takes too it reviews, as a human does, so it keeps what it
// holds.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// An agent's link to an object, listing the states it decides between.
func decisionsDecides(a *app, process, object int64, states ...int64) link {
	a.t.Helper()
	return a.link(map[string]any{"process": process, "object": object, "direction": directionOutput, "states": states})
}

// The state of each run object of that name, in the order generated.
func decisionsStatesOf(d runDetail, object string) []string {
	var ss []string
	for _, o := range d.Objects {
		if o.Name == object {
			ss = append(ss, o.State)
		}
	}
	return ss
}

// What an agent deciding states hands in: its output, if it generates
// anything besides what it reviews, and its decisions by object name.
func decisionsReply(output any, decisions map[string]any) map[string]any {
	reply := map[string]any{"decisions": decisions}
	if output != nil {
		reply["output"] = output
	}
	return reply
}

func decisionsState(state string) map[string]any { return map[string]any{"state": state} }

// A triage agent generates a ticket in the state it decides, and only the
// processes taking the ticket in that state run.
func TestAgentDecidesTheStateItGeneratesIn(t *testing.T) {
	for _, c := range []struct{ state, next string }{{"bug", "Fix"}, {"feature", "Plan"}} {
		t.Run(c.state, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Triage")
			triage := a.agent(f.ID, "Triage", "Triage the report.")
			ticket := a.newObject(f.ID, "Ticket")
			bug := a.newState(ticket.ID, "bug")
			feature := a.newState(ticket.ID, "feature")
			fix := a.agent(f.ID, "Fix", "Fix it.")
			plan := a.agent(f.ID, "Plan", "Plan it.")
			decision := decisionsDecides(a, triage.ID, ticket.ID, bug.ID, feature.ID)
			if !slices.Equal(decision.States, []int64{bug.ID, feature.ID}) || decision.State != nil {
				t.Fatalf("decision link = %+v", decision)
			}
			humanTakesIn(a, fix.ID, ticket.ID, bug.ID)
			humanTakesIn(a, plan.ID, ticket.ID, feature.ID)
			a.startAt(f.ID, triage.ID)
			a.worker.onAgent("Triage", handsIn(decisionsReply(data("it crashes"), map[string]any{"Ticket": decisionsState(c.state)})))
			a.worker.onAgent("Fix", handsIn(data("fixed")))
			a.worker.onAgent("Plan", handsIn(data("planned")))

			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != runDone {
				t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
			}
			for _, p := range []string{"Fix", "Plan"} {
				if n := len(stepsOf(d, p)); (p == c.next) != (n == 1) || n > 1 {
					t.Errorf("%d steps of %s, want the ticket in %s to go to %s only; steps %s", n, p, c.state, c.next, describeSteps(d))
				}
			}
			if got := decisionsStatesOf(d, "Ticket"); !slices.Equal(got, []string{c.state}) {
				t.Errorf("ticket states = %q, want %q", got, c.state)
			}
			if got := contentsOf(d, "Ticket"); len(got) != 1 || !strings.Contains(got[0], "it crashes") || strings.Contains(got[0], "decisions") {
				t.Errorf("ticket holds %q, want only what was generated", got)
			}
			if s := onlyStep(t, d, "Triage"); s.Outcome != c.state {
				t.Errorf("triage step outcome = %q, want %q", s.Outcome, c.state)
			}
			// The next one takes the ticket as it was generated.
			if in := onlyStep(t, d, c.next).Inputs; len(in) != 1 || in[0].State != c.state || !strings.Contains(in[0].Content, "it crashes") {
				t.Errorf("%s took %+v", c.next, in)
			}

			// The agent was told what to decide between, and its tool takes it.
			run := a.worker.runsOf("Triage")[0]
			want := []specDecision{{Name: "Ticket", States: []string{"bug", "feature"}}}
			if run.Output == nil || run.Output.Shape != "object" || !slices.EqualFunc(run.Output.Decisions, want, func(x, y specDecision) bool {
				return x.Name == y.Name && x.Review == y.Review && slices.Equal(x.States, y.States)
			}) {
				t.Errorf("output spec = %+v, want decisions %+v", run.Output, want)
			}
			if !strings.Contains(run.Prompt, `"Ticket": one of "bug", "feature"`) {
				t.Errorf("prompt doesn't list the states to decide between:\n%s", run.Prompt)
			}
		})
	}
}

// An agent taking the object it decides on reviews it: it hands in only its
// decision, the object keeps what it held, and its instructions go to the
// process taking it in that state as a change request.
func TestAgentReviewsWhatItTakes(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Critique")
	write := a.agent(f.ID, "Write", "Write the post.")
	critic := a.agent(f.ID, "Critic", "Critique the post.")
	revise := a.agent(f.ID, "Revise", "Revise the post.")
	publish := a.agent(f.ID, "Publish", "Publish the post.")
	post := a.newObject(f.ID, "Post")
	good := a.newState(post.ID, "good")
	weak := a.newState(post.ID, "weak")
	a.output(write.ID, post.ID)
	a.input(critic.ID, post.ID)
	decisionsDecides(a, critic.ID, post.ID, good.ID, weak.ID)
	humanTakesIn(a, revise.ID, post.ID, weak.ID)
	humanTakesIn(a, publish.ID, post.ID, good.ID)
	a.startAt(f.ID, write.ID)
	a.worker.onAgent("Write", handsIn(data("first draft")))
	a.worker.onAgent("Critic", handsIn(decisionsReply(nil, map[string]any{"Post": map[string]any{"state": "weak", "instructions": "Tighten the intro."}})))
	a.worker.onAgent("Revise", handsIn(data("revised")))
	a.worker.onAgent("Publish", handsIn(data("published")))

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone || len(stepsOf(d, "Revise")) != 1 || len(stepsOf(d, "Publish")) != 0 {
		t.Fatalf("run %s with steps %s, want the weak post revised", d.Status, describeSteps(d))
	}
	critique := onlyStep(t, d, "Critic")
	if critique.Outcome != "weak" || critique.Instructions != "Tighten the intro." {
		t.Errorf("critic step decided %q with %q", critique.Outcome, critique.Instructions)
	}
	// The post the critic reviewed keeps what Write generated.
	if got := contentsOf(d, "Post"); len(got) != 2 || got[0] != got[1] || !strings.Contains(got[1], "first draft") {
		t.Errorf("posts = %q, want the written one, then the reviewed one as written", got)
	}
	if got := decisionsStatesOf(d, "Post"); len(got) < 2 || got[1] != "weak" {
		t.Errorf("post states = %q, want the reviewed one weak", got)
	}
	cr := onlyStep(t, d, "Revise").ChangeRequest
	if cr == nil || cr.Reviewer != "Critic" || cr.State != "weak" || cr.Instructions != "Tighten the intro." ||
		len(cr.Reviewed) != 1 || !strings.Contains(cr.Reviewed[0].Content, "first draft") {
		t.Errorf("Revise's change request = %+v", cr)
	}

	run := a.worker.runsOf("Critic")[0]
	if run.Output == nil || run.Output.Shape != "decisions" || len(run.Output.Objects) != 0 ||
		len(run.Output.Decisions) != 1 || !run.Output.Decisions[0].Review {
		t.Errorf("critic's output spec = %+v, want only its review", run.Output)
	}
	if !strings.Contains(run.Prompt, "Its argument is exactly decisions.") || !strings.Contains(run.Prompt, "keeps what it holds") {
		t.Errorf("critic's prompt:\n%s", run.Prompt)
	}
}

// A decision that isn't one of the link's states, or comes with the wrong
// keys, is handed back once, as any reply breaking the contract is.
func TestAgentDecisionMustBeOneOfTheStates(t *testing.T) {
	for _, c := range []struct {
		name  string
		first any
		// Why the first is refused.
		why string
	}{
		{"unknown state", decisionsReply(data("x"), map[string]any{"Ticket": decisionsState("urgent")}), `isn't one of "bug", "feature"`},
		{"no decisions", data("x"), `lacks "output", "decisions"`},
		{"no output", decisionsReply(nil, map[string]any{"Ticket": decisionsState("bug")}), `lacks "output"`},
		{"extra key", decisionsReply(data("x"), map[string]any{"Ticket": map[string]any{"state": "bug", "why": "it crashes"}}), `not asked for: "why"`},
		{"state not a string", decisionsReply(data("x"), map[string]any{"Ticket": map[string]any{"state": 1}}), "the name of a state"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Triage")
			triage := a.agent(f.ID, "Triage", "Triage the report.")
			ticket := a.newObject(f.ID, "Ticket")
			bug := a.newState(ticket.ID, "bug")
			feature := a.newState(ticket.ID, "feature")
			fix := a.agent(f.ID, "Fix", "Fix it.")
			decisionsDecides(a, triage.ID, ticket.ID, bug.ID, feature.ID)
			humanTakesIn(a, fix.ID, ticket.ID, bug.ID)
			a.startAt(f.ID, triage.ID)
			var mu sync.Mutex
			tries := 0
			a.worker.onAgent("Triage", func(context.Context, agentRun) runResult {
				mu.Lock()
				defer mu.Unlock()
				tries++
				if tries == 1 {
					return submitted(c.first)
				}
				return submitted(decisionsReply(data("it crashes"), map[string]any{"Ticket": decisionsState("bug")}))
			})
			a.worker.onAgent("Fix", handsIn(data("fixed")))

			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != runDone || len(stepsOf(d, "Fix")) != 1 {
				t.Fatalf("run %s with steps %s, want the second reply to go on", d.Status, describeSteps(d))
			}
			runs := a.worker.runsOf("Triage")
			if len(runs) != 2 || !strings.Contains(runs[1].Prompt, c.why) {
				t.Errorf("%d tries, the second asking:\n%s\nwant it to say %q", len(runs), runs[len(runs)-1].Prompt, c.why)
			}
		})
	}
}

// Only an agent decides between states among the processes other than a
// human; switching an agent to another kind leaves its links generating
// the object as it is.
func TestAgentDecisionLinks(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Links")
	triage := a.agent(f.ID, "Triage", "Triage it.")
	script := a.newProcess(f.ID, map[string]any{"name": "Sort", "kind": kindScript, "script": "echo bug"})
	ticket := a.newObject(f.ID, "Ticket")
	bug := a.newState(ticket.ID, "bug")
	feature := a.newState(ticket.ID, "feature")

	a.refused("POST", "/api/links", map[string]any{"process": script.ID, "object": ticket.ID, "direction": directionOutput, "states": []int64{bug.ID}}, http.StatusBadRequest, decisionLinkMessage)
	a.refused("POST", "/api/links", map[string]any{"process": triage.ID, "object": ticket.ID, "direction": directionOutput, "state": bug.ID, "states": []int64{feature.ID}}, http.StatusBadRequest, "either names the state")
	plain := a.output(script.ID, ticket.ID)
	a.refused("PUT", fmt.Sprintf("/api/links/%d/states", plain.ID), map[string]any{"states": []int64{bug.ID}}, http.StatusNotFound, "no human or agent output link")

	// An agent's plain link can list states later, and the list is replaced whole.
	l := a.output(triage.ID, ticket.ID)
	a.call("PUT", fmt.Sprintf("/api/links/%d/states", l.ID), map[string]any{"states": []int64{bug.ID, feature.ID}}, http.StatusNoContent, nil)
	statesOf := func() []int64 {
		var ls []link
		a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &ls)
		for _, x := range ls {
			if x.ID == l.ID {
				return x.States
			}
		}
		t.Fatalf("link %d is gone", l.ID)
		return nil
	}
	if got := statesOf(); !slices.Equal(got, []int64{bug.ID, feature.ID}) {
		t.Errorf("states = %v", got)
	}

	// A version keeps them.
	var saved version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &saved)
	a.call("PUT", fmt.Sprintf("/api/links/%d/states", l.ID), map[string]any{"states": []int64{}}, http.StatusNoContent, nil)
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusNoContent, nil)
	var ls []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &ls)
	i := slices.IndexFunc(ls, func(x link) bool { return x.Process == triage.ID && x.Direction == directionOutput })
	if i < 0 || !slices.Equal(ls[i].States, []int64{bug.ID, feature.ID}) {
		t.Fatalf("after restoring, links %+v, want the agent deciding between both states", ls)
	}
	l = ls[i]

	a.call("PATCH", fmt.Sprintf("/api/processes/%d", triage.ID), map[string]any{"kind": kindScript, "script": "echo x"}, http.StatusOK, nil)
	if got := statesOf(); len(got) != 0 {
		t.Errorf("after becoming a script, the link lists %v", got)
	}
}

// The prompt of an agent generating an object and deciding another's state
// says where each goes.
func TestAgentDecisionPrompt(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Prompt")
	triage := a.agent(f.ID, "Triage", "Triage it.")
	ticket := a.newObject(f.ID, "Ticket")
	notes := a.newObject(f.ID, "Notes")
	humanAttribute(a, notes.ID, "summary")
	bug := a.newState(ticket.ID, "bug")
	feature := a.newState(ticket.ID, "feature")
	decisionsDecides(a, triage.ID, ticket.ID, bug.ID, feature.ID)
	a.output(triage.ID, notes.ID)
	a.startAt(f.ID, triage.ID)
	reply := decisionsReply(map[string]any{"Ticket": data("crash"), "Notes": map[string]any{"summary": "short"}}, map[string]any{"Ticket": decisionsState("feature")})
	a.worker.onAgent("Triage", handsIn(reply))

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
	}
	if got := decisionsStatesOf(d, "Ticket"); !slices.Equal(got, []string{"feature"}) {
		t.Errorf("ticket states = %q", got)
	}
	if got := decisionsStatesOf(d, "Notes"); !slices.Equal(got, []string{""}) {
		t.Errorf("notes states = %q, want none", got)
	}
	// The step's output is the reply, in order.
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(onlyStep(t, d, "Triage").Output), &out); err != nil || out["output"] == nil || out["decisions"] == nil {
		t.Errorf("step output = %q", onlyStep(t, d, "Triage").Output)
	}
	prompt := a.worker.runsOf("Triage")[0].Prompt
	for _, want := range []string{"Its arguments are exactly output and decisions. output is a JSON object with one key per object below", `"summary"`, `"Ticket": one of "bug", "feature"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
}
