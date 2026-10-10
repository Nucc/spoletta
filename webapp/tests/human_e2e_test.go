package tests

// Human steps: reviews a person decides, and the objects a person fills in.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Decides a human step, and returns the status and reply.
func humanDecide(a *app, run, step int64, body any) (int, string) {
	a.t.Helper()
	status, reply := a.do("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", run, step), body)
	return status, strings.TrimSpace(string(reply))
}

// Decides a human step, which must succeed.
func humanMustDecide(a *app, run, step int64, body any) {
	a.t.Helper()
	if status, reply := humanDecide(a, run, step, body); status != http.StatusNoContent {
		a.t.Fatalf("deciding step %d = %d %q, want 204", step, status, reply)
	}
}

// A person, linked by an agent link into the human process.
func humanPerson(a *app, flow, process int64, name string) object {
	a.t.Helper()
	p := a.newObject(flow, name)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", p.ID), map[string]any{"human": true}, http.StatusOK, nil)
	a.input(process, p.ID)
	return p
}

func humanAttribute(a *app, object int64, name string) attribute {
	a.t.Helper()
	var at attribute
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", object), map[string]any{"name": name, "definition": "The " + name}, http.StatusCreated, &at)
	return at
}

func humanHuman(a *app, flow int64, name string) process {
	a.t.Helper()
	return a.newProcess(flow, map[string]any{"name": name, "kind": kindHuman, "prompt": name + " it."})
}

// Lists states for a human's output link to the object.
func humanDecides(a *app, process, object int64, states ...int64) link {
	a.t.Helper()
	return a.link(map[string]any{"process": process, "object": object, "direction": directionOutput, "states": states})
}

func humanTakesIn(a *app, process, object, state int64) link {
	a.t.Helper()
	return a.link(map[string]any{"process": process, "object": object, "direction": directionInput, "state": state})
}

// An agent counting its runs, handing in "<prefix> v<n>" each time.
func humanCounting(prefix string) func(context.Context, agentRun) runResult {
	var mu sync.Mutex
	n := 0
	return func(context.Context, agentRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		n++
		return submitted(data(fmt.Sprintf("%s v%d", prefix, n)))
	}
}

// A draft reviewed by a person who approves it, sends it back for changes,
// or rejects it; a second reviewer takes the rejected draft.
type humanReviewBoard struct {
	flow                        flow
	draft, review, escalate     process
	publish                     process
	doc, person                 object
	approved, changes, rejected objectState
}

func humanNewReviewBoard(a *app) humanReviewBoard {
	a.t.Helper()
	var b humanReviewBoard
	b.flow = a.newFlow("Review")
	b.draft = a.agent(b.flow.ID, "Draft", "Write the release notes.")
	b.doc = a.newObject(b.flow.ID, "Notes")
	b.approved = a.newState(b.doc.ID, "approved")
	b.changes = a.newState(b.doc.ID, "changes")
	b.rejected = a.newState(b.doc.ID, "rejected")
	b.review = humanHuman(a, b.flow.ID, "Review")
	b.escalate = humanHuman(a, b.flow.ID, "Escalate")
	b.publish = a.agent(b.flow.ID, "Publish", "Publish the notes.")
	a.output(b.draft.ID, b.doc.ID)
	a.input(b.review.ID, b.doc.ID)
	b.person = humanPerson(a, b.flow.ID, b.review.ID, "Editor")
	humanDecides(a, b.review.ID, b.doc.ID, b.approved.ID, b.changes.ID, b.rejected.ID)
	humanTakesIn(a, b.draft.ID, b.doc.ID, b.changes.ID)
	humanTakesIn(a, b.publish.ID, b.doc.ID, b.approved.ID)
	humanTakesIn(a, b.escalate.ID, b.doc.ID, b.rejected.ID)
	a.startAt(b.flow.ID, b.draft.ID)
	a.worker.onAgent("Draft", humanCounting("notes"))
	a.worker.onAgent("Publish", handsIn(data("published")))
	return b
}

// The waiting review shows what it reviews, without the person carrying it
// out, and each state goes to the processes taking the object in it.
func TestHumanReviewRoutesEachState(t *testing.T) {
	for _, c := range []struct {
		state string
		// The process that runs next, and whether the draft is revised.
		next string
	}{
		{"approved", "Publish"},
		{"changes", "Draft"},
		{"rejected", "Escalate"},
	} {
		t.Run(c.state, func(t *testing.T) {
			a := newApp(t)
			b := humanNewReviewBoard(a)
			state := map[string]objectState{"approved": b.approved, "changes": b.changes, "rejected": b.rejected}[c.state]

			rn := a.startRun(b.flow.ID)
			review := a.waitStep(rn.ID, "Review", stepWaiting)
			if len(review.Inputs) != 1 || review.Inputs[0].Name != "Notes" || review.Inputs[0].State != "" || !strings.Contains(review.Inputs[0].Content, "notes v1") {
				t.Fatalf("the review shows %+v, want only the notes", review.Inputs)
			}
			if review.Prompt != "" || review.FinishedAt != nil || review.Kind != kindHuman {
				t.Errorf("waiting review = %+v", review)
			}

			humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": state.ID})
			switch c.next {
			case "Publish":
				d := a.finish(rn.ID)
				if d.Status != runDone {
					t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
				}
				p := onlyStep(t, d, "Publish")
				if len(p.Inputs) != 1 || p.Inputs[0].State != "approved" || p.ChangeRequest != nil {
					t.Errorf("Publish took %+v with change request %+v", p.Inputs, p.ChangeRequest)
				}
				if !strings.Contains(p.Prompt, `<input name="Notes" state="approved">`) {
					t.Errorf("Publish's prompt doesn't give the notes as approved:\n%s", p.Prompt)
				}
			case "Draft":
				a.waitStep(rn.ID, "Review", stepWaiting, review.ID)
				// Without instructions the draft comes back as an input in its
				// state, to change, since Draft generates it too.
				runs := a.worker.runsOf("Draft")
				if len(runs) != 2 || !strings.Contains(runs[1].Prompt, `<input name="Notes" state="changes" role="affected">`) || strings.Contains(runs[1].Prompt, "<change_request>") {
					t.Errorf("the revision's prompt:\n%s", runs[len(runs)-1].Prompt)
				}
				a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
			case "Escalate":
				esc := a.waitStep(rn.ID, "Escalate", stepWaiting)
				if len(esc.Inputs) != 1 || esc.Inputs[0].State != "rejected" {
					t.Errorf("Escalate reviews %+v, want the rejected notes", esc.Inputs)
				}
				humanMustDecide(a, rn.ID, esc.ID, map[string]any{})
				d := a.finish(rn.ID)
				if d.Status != runDone || len(stepsOf(d, "Publish")) != 0 {
					t.Errorf("run %s; steps %s", d.Status, describeSteps(d))
				}
			}

			d := a.getRun(rn.ID)
			r := stepsOf(d, "Review")[0]
			if r.Status != stepDone || r.Outcome != c.state || r.FinishedAt == nil {
				t.Errorf("decided review = %s outcome %q", r.Status, r.Outcome)
			}
			// The run records the notes in the state set, with their content.
			var decided []runObject
			for _, o := range d.Objects {
				if o.Step == r.ID {
					decided = append(decided, o)
				}
			}
			if len(decided) != 1 || decided[0].Name != "Notes" || decided[0].State != c.state || !strings.Contains(decided[0].Content, "notes v1") {
				t.Errorf("the review recorded %+v", decided)
			}
		})
	}
}

// Instructions become a change request for an agent taking the object in
// the state set; a person taking it in that state gets the object itself.
func TestHumanInstructionsGoToAgentsAsChangeRequests(t *testing.T) {
	a := newApp(t)
	b := humanNewReviewBoard(a)
	// A second reviewer also takes the notes sent back for changes.
	second := humanHuman(a, b.flow.ID, "Second look")
	humanTakesIn(a, second.ID, b.doc.ID, b.changes.ID)

	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.changes.ID, "instructions": "Mention the login fix."})

	look := a.waitStep(rn.ID, "Second look", stepWaiting)
	if len(look.Inputs) != 1 || look.Inputs[0].State != "changes" || !strings.Contains(look.Inputs[0].Content, "notes v1") || look.ChangeRequest != nil {
		t.Errorf("the second reviewer got %+v with change request %+v", look.Inputs, look.ChangeRequest)
	}
	again := a.waitStep(rn.ID, "Review", stepWaiting, review.ID)
	d := a.getRun(rn.ID)
	draft := stepsOf(d, "Draft")
	if len(draft) != 2 {
		t.Fatalf("Draft ran %d times", len(draft))
	}
	cr := draft[1].ChangeRequest
	if cr == nil || cr.Reviewer != "Review" || cr.State != "changes" || cr.Instructions != "Mention the login fix." ||
		len(cr.Reviewed) != 1 || !strings.Contains(cr.Reviewed[0].Content, "notes v1") {
		t.Errorf("the revision's change request = %+v", cr)
	}
	// The reviewed notes come as the change request, not as an input too.
	if len(draft[1].Inputs) != 0 {
		t.Errorf("the revision took inputs %+v", draft[1].Inputs)
	}
	for _, want := range []string{`The reviewer "Review" marked the work below as "changes"`, "<reviewed name=\"Notes\" state=\"changes\">", "<change_request>\nMention the login fix.\n</change_request>"} {
		if !strings.Contains(draft[1].Prompt, want) {
			t.Errorf("the revision's prompt lacks %q:\n%s", want, draft[1].Prompt)
		}
	}
	// The second review sees the revision, in no state.
	if len(again.Inputs) != 1 || again.Inputs[0].State != "" || !strings.Contains(again.Inputs[0].Content, "notes v2") {
		t.Errorf("the second review shows %+v", again.Inputs)
	}

	humanMustDecide(a, rn.ID, look.ID, map[string]any{})
	humanMustDecide(a, rn.ID, again.ID, map[string]any{"state": b.approved.ID, "instructions": "Ship it."})
	d = a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if p := onlyStep(t, d, "Publish"); p.ChangeRequest == nil || p.ChangeRequest.State != "approved" || p.ChangeRequest.Instructions != "Ship it." {
		t.Errorf("Publish's change request = %+v", p.ChangeRequest)
	}
	if s := stepsOf(d, "Review"); s[1].Instructions != "Ship it." || s[0].Instructions != "Mention the login fix." {
		t.Errorf("reviews' instructions = %q, %q", s[0].Instructions, s[1].Instructions)
	}
}

// Sending the work back repeats the loop for as long as the person does.
func TestHumanReviewLoopRepeats(t *testing.T) {
	a := newApp(t)
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	var seen []int64
	for i := 1; i <= 3; i++ {
		r := a.waitStep(rn.ID, "Review", stepWaiting, seen...)
		if !strings.Contains(r.Inputs[0].Content, fmt.Sprintf("notes v%d", i)) {
			t.Fatalf("review %d shows %+v", i, r.Inputs)
		}
		seen = append(seen, r.ID)
		humanMustDecide(a, rn.ID, r.ID, map[string]any{"state": b.changes.ID, "instructions": fmt.Sprintf("round %d", i)})
	}
	last := a.waitStep(rn.ID, "Review", stepWaiting, seen...)
	humanMustDecide(a, rn.ID, last.ID, map[string]any{"state": b.approved.ID})
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if n, m := len(stepsOf(d, "Draft")), len(stepsOf(d, "Review")); n != 4 || m != 4 {
		t.Errorf("Draft ran %d times and Review %d, want 4 each", n, m)
	}
	if p := onlyStep(t, d, "Publish"); p.ChangeRequest != nil || len(p.Inputs) != 1 || !strings.Contains(p.Inputs[0].Content, "notes v4") {
		t.Errorf("Publish took %+v with change request %+v, want notes v4", p.Inputs, p.ChangeRequest)
	}
}

// Two reviews waiting at once can be decided in either order, and each
// decision goes on its own way.
func TestHumanParallelReviews(t *testing.T) {
	for _, order := range [][]string{{"Legal", "Product"}, {"Product", "Legal"}} {
		t.Run(strings.Join(order, " then "), func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Parallel")
			draft := a.agent(f.ID, "Draft", "Draft it.")
			doc := a.newObject(f.ID, "Notes")
			a.output(draft.ID, doc.ID)
			states := map[string]objectState{}
			for _, name := range []string{"Legal", "Product"} {
				st := a.newState(doc.ID, name+" ok")
				states[name] = st
				r := humanHuman(a, f.ID, name)
				a.input(r.ID, doc.ID)
				humanDecides(a, r.ID, doc.ID, st.ID)
				after := a.agent(f.ID, "After "+name, "Go on.")
				humanTakesIn(a, after.ID, doc.ID, st.ID)
				a.worker.onAgent("After "+name, handsIn(data("done")))
			}
			a.startAt(f.ID, draft.ID)
			a.worker.onAgent("Draft", handsIn(data("notes")))

			rn := a.startRun(f.ID)
			steps := map[string]step{}
			for _, name := range []string{"Legal", "Product"} {
				steps[name] = a.waitStep(rn.ID, name, stepWaiting)
			}
			humanMustDecide(a, rn.ID, steps[order[0]].ID, map[string]any{"state": states[order[0]].ID})
			a.waitStep(rn.ID, "After "+order[0], stepDone)
			if d := a.getRun(rn.ID); d.Status != runRunning || len(stepsOf(d, "After "+order[1])) != 0 {
				t.Fatalf("after the first decision run is %s with steps %s", d.Status, describeSteps(d))
			}
			humanMustDecide(a, rn.ID, steps[order[1]].ID, map[string]any{"state": states[order[1]].ID})
			d := a.finish(rn.ID)
			if d.Status != runDone {
				t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
			}
			for _, name := range []string{"Legal", "Product"} {
				s := onlyStep(t, d, "After "+name)
				if len(s.Inputs) != 1 || s.Inputs[0].State != name+" ok" {
					t.Errorf("After %s took %+v", name, s.Inputs)
				}
			}
		})
	}
}

// A decision that can't be taken is refused, saying why, and leaves the
// review waiting.
func TestHumanRefusedDecisions(t *testing.T) {
	a := newApp(t)
	b := humanNewReviewBoard(a)
	stranger := a.newState(a.newObject(b.flow.ID, "Other").ID, "done")
	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	draft := onlyStep(t, a.getRun(rn.ID), "Draft")

	other := humanNewReviewBoard(a)
	otherRun := a.startRun(other.flow.ID)
	otherReview := a.waitStep(otherRun.ID, "Review", stepWaiting)

	path := fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, review.ID)
	for _, c := range []struct {
		name   string
		path   string
		body   any
		status int
		msg    string
	}{
		{"state of another object", path, map[string]any{"state": stranger.ID}, http.StatusBadRequest, "can't decide on that state"},
		{"state of the object it doesn't list", path, map[string]any{"state": 1 << 40}, http.StatusBadRequest, "can't decide on that state"},
		{"state of the same name on another flow", path, map[string]any{"state": other.approved.ID}, http.StatusBadRequest, "can't decide on that state"},
		{"step of another run", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, otherReview.ID), map[string]any{}, http.StatusConflict, "no review waiting"},
		{"unknown step", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, 1<<40), map[string]any{}, http.StatusConflict, "no review waiting"},
		{"an agent's step", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, draft.ID), map[string]any{}, http.StatusConflict, "no review waiting"},
		{"entries a review doesn't fill in", path, map[string]any{"state": b.approved.ID, "entries": map[string]any{fmt.Sprint(b.doc.ID): "x"}}, http.StatusBadRequest, "don't match"},
		{"invalid id", fmt.Sprintf("/api/runs/%d/steps/x", rn.ID), map[string]any{}, http.StatusBadRequest, "invalid id"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a.t = t
			a.refused("PATCH", c.path, c.body, c.status, c.msg)
		})
	}
	a.t = t
	status, reply := humanRaw(a, "PATCH", path, `{"state":`)
	if status != http.StatusBadRequest || !strings.Contains(reply, "invalid JSON") {
		t.Errorf("invalid JSON = %d %q", status, reply)
	}

	if s := a.getRun(rn.ID).Steps; !slices.ContainsFunc(s, func(s step) bool { return s.ID == review.ID && s.Status == stepWaiting }) {
		t.Fatalf("the review didn't stay waiting: %+v", s)
	}
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	if status, reply := humanDecide(a, rn.ID, review.ID, map[string]any{"state": b.rejected.ID}); status != http.StatusConflict {
		t.Errorf("deciding twice = %d %q", status, reply)
	}
	if d := a.finish(rn.ID); d.Status != runDone || len(stepsOf(d, "Escalate")) != 0 || len(stepsOf(d, "Publish")) != 1 {
		t.Errorf("run %s; steps %s", d.Status, describeSteps(d))
	}
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", otherRun.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
}

func humanRaw(a *app, method, path, body string) (int, string) {
	a.t.Helper()
	req, err := http.NewRequest(method, a.url+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Stopping a run ends the reviews waiting in it, which can't be decided then.
func TestHumanStoppingEndsWaitingReviews(t *testing.T) {
	a := newApp(t)
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)

	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
	d := a.finish(rn.ID)
	r := onlyStep(t, d, "Review")
	if d.Status != runInterrupted || r.Status != stepError || r.Error != "The run ended before this step finished." || r.FinishedAt == nil {
		t.Errorf("run %s, review %s %q", d.Status, r.Status, r.Error)
	}
	if status, reply := humanDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID}); status != http.StatusConflict {
		t.Errorf("deciding after the run ended = %d %q", status, reply)
	}
	// Nothing ran on after the stop.
	time.Sleep(100 * time.Millisecond)
	if d := a.getRun(rn.ID); len(stepsOf(d, "Publish")) != 0 || d.Status != runInterrupted {
		t.Errorf("after deciding, run %s with steps %s", d.Status, describeSteps(d))
	}
}

// A step elsewhere in the run finding its value already stored terminates
// the run, which ends the review waiting beside it.
func TestHumanTerminatedRunEndsWaitingReviews(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Terminate")
	fetch := a.agent(f.ID, "Fetch", "Fetch the issue.")
	issue := a.newObject(f.ID, "Issue")
	review := humanHuman(a, f.ID, "Look")
	store := a.agent(f.ID, "Store", "Record the issue.")
	record := a.newObject(f.ID, "Record")
	key := humanAttribute(a, record.ID, "key")
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", key.ID), map[string]any{"unique": true}, http.StatusOK, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", record.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	a.output(fetch.ID, issue.ID)
	a.input(review.ID, issue.ID)
	a.input(store.ID, issue.ID)
	a.output(store.ID, record.ID)
	a.startAt(f.ID, fetch.ID)
	a.worker.onAgent("Fetch", handsIn(data("issue")))

	// The first run stores the key and ends done.
	a.worker.onAgent("Store", handsIn(map[string]any{"key": "K-1"}))
	first := a.startRun(f.ID)
	look := a.waitStep(first.ID, "Look", stepWaiting)
	humanMustDecide(a, first.ID, look.ID, map[string]any{})
	if d := a.finish(first.ID); d.Status != runDone {
		t.Fatalf("first run %s; steps %s", d.Status, describeSteps(d))
	}

	// The second stores it again once its review waits.
	reviewing := make(chan struct{})
	a.worker.onAgent("Store", func(ctx context.Context, _ agentRun) runResult {
		select {
		case <-reviewing:
		case <-time.After(5 * time.Second):
		}
		return submitted(map[string]any{"key": "K-1"})
	})
	second := a.startRun(f.ID)
	waiting := a.waitStep(second.ID, "Look", stepWaiting)
	close(reviewing)
	d := a.finish(second.ID)
	if d.Status != runTerminated {
		t.Fatalf("second run %s, want terminated; steps %s", d.Status, describeSteps(d))
	}
	s := onlyStep(t, d, "Store")
	if s.Status != stepDuplicate || !strings.Contains(s.Error, fmt.Sprintf("already stored by run #%d", first.ID)) {
		t.Errorf("Store step = %s %q", s.Status, s.Error)
	}
	// The run reads as terminated as soon as the duplicate is found; the
	// review ends once the engine winds the run down.
	d = a.waitRun(second.ID, "the review to end", func(d runDetail) bool {
		return onlyStep(t, d, "Look").Status != stepWaiting
	})
	l := onlyStep(t, d, "Look")
	if l.Status != stepError || l.Error != "The run ended before this step finished." {
		t.Errorf("the waiting review = %s %q", l.Status, l.Error)
	}
	if status, _ := humanDecide(a, second.ID, waiting.ID, map[string]any{}); status != http.StatusConflict {
		t.Errorf("deciding in the terminated run = %d", status)
	}
}

// A run starting at a person filling in a ticket goes on with what they
// entered, checked against the ticket's attributes.
func TestHumanEntriesStartARun(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Entries")
	request := humanHuman(a, f.ID, "Request")
	humanPerson(a, f.ID, request.ID, "Requester")
	ticket := a.newObject(f.ID, "Ticket")
	humanAttribute(a, ticket.ID, "key")
	humanAttribute(a, ticket.ID, "summary")
	fix := a.agent(f.ID, "Fix", "Fix the ticket.")
	a.output(request.ID, ticket.ID)
	a.input(fix.ID, ticket.ID)
	a.output(fix.ID, a.newObject(f.ID, "Patch").ID)
	a.startAt(f.ID, request.ID)
	a.worker.onAgent("Fix", handsIn(data("patched")))

	rn := a.startRun(f.ID)
	req := a.waitStep(rn.ID, "Request", stepWaiting)
	if len(req.Inputs) != 0 {
		t.Errorf("the person is given %+v; a person isn't an input", req.Inputs)
	}
	id := fmt.Sprint(ticket.ID)
	for _, c := range []struct {
		name string
		body map[string]any
		msg  string
	}{
		{"no entries", map[string]any{}, "don't match"},
		{"another object", map[string]any{"entries": map[string]any{fmt.Sprint(fix.ID): map[string]any{}}}, "don't match"},
		{"an extra object", map[string]any{"entries": map[string]any{id: map[string]any{"key": "A", "summary": "s"}, "999999": "x"}}, "not asked for"},
		{"a missing field", map[string]any{"entries": map[string]any{id: map[string]any{"key": "A"}}}, `lacks "summary"`},
		{"an extra field", map[string]any{"entries": map[string]any{id: map[string]any{"key": "A", "summary": "s", "owner": "me"}}}, `"owner"`},
		{"text, not fields", map[string]any{"entries": map[string]any{id: "A-1"}}, "Ticket"},
		{"a list, not fields", map[string]any{"entries": map[string]any{id: []any{map[string]any{"key": "A", "summary": "s"}}}}, "Ticket"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a.t = t
			a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, req.ID), c.body, http.StatusBadRequest, c.msg)
		})
	}
	a.t = t
	if len(stepsOf(a.getRun(rn.ID), "Fix")) != 0 {
		t.Fatal("Fix ran before anything valid was entered")
	}

	// Fields entered in any order are kept in the attributes' order.
	humanMustDecide(a, rn.ID, req.ID, map[string]any{"entries": map[string]any{id: map[string]any{"summary": "Login crashes", "key": "AUTH-1"}}})
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	r := onlyStep(t, d, "Request")
	if r.Status != stepDone || r.Outcome != "" || !humanOrdered(r.Output, `"key"`, `"summary"`) {
		t.Errorf("Request step = %s output %q", r.Status, r.Output)
	}
	got := contentsOf(d, "Ticket")
	if len(got) != 1 || !strings.Contains(got[0], "AUTH-1") || !humanOrdered(got[0], `"key"`, `"summary"`) {
		t.Errorf("Ticket held %q", got)
	}
	fs := onlyStep(t, d, "Fix")
	if len(fs.Inputs) != 1 || fs.Inputs[0].Name != "Ticket" || !strings.Contains(fs.Inputs[0].Content, "Login crashes") {
		t.Errorf("Fix took %+v", fs.Inputs)
	}
	if strings.Contains(fs.Prompt, "Requester") {
		t.Errorf("the person reached the agent:\n%s", fs.Prompt)
	}
}

// Whether s holds each of parts, in that order.
func humanOrdered(s string, parts ...string) bool {
	at := -1
	for _, p := range parts {
		i := strings.Index(s, p)
		if i <= at {
			return false
		}
		at = i
	}
	return true
}

// What can be entered of objects without attributes, lists and files, and
// several objects at once.
func TestHumanEntryShapes(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Shapes")
	enter := humanHuman(a, f.ID, "Enter")
	note := a.newObject(f.ID, "Note")
	tickets := a.newObject(f.ID, "Tickets")
	humanAttribute(a, tickets.ID, "key")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", tickets.ID), map[string]any{"many": true, "min": 1, "max": 2}, http.StatusOK, nil)
	a.output(enter.ID, note.ID)
	a.output(enter.ID, tickets.ID)
	use := a.agent(f.ID, "Use", "Use them.")
	a.input(use.ID, note.ID)
	a.input(use.ID, tickets.ID)
	a.output(use.ID, a.newObject(f.ID, "Done").ID)
	a.startAt(f.ID, enter.ID)
	a.worker.onAgent("Use", handsIn(data("used")))

	rn := a.startRun(f.ID)
	step := a.waitStep(rn.ID, "Enter", stepWaiting)
	n, ts := fmt.Sprint(note.ID), fmt.Sprint(tickets.ID)
	item := func(k string) map[string]any { return map[string]any{"key": k} }
	for _, c := range []struct {
		name    string
		entries map[string]any
		msg     string
	}{
		{"a note that isn't text", map[string]any{n: 42, ts: []any{item("A")}}, `"Note": it must be text`},
		{"a note that is an object", map[string]any{n: map[string]any{"data": "x"}, ts: []any{item("A")}}, "must be text"},
		{"too few tickets", map[string]any{n: "hi", ts: []any{}}, `"Tickets"`},
		{"too many tickets", map[string]any{n: "hi", ts: []any{item("A"), item("B"), item("C")}}, `"Tickets"`},
		{"tickets not a list", map[string]any{n: "hi", ts: item("A")}, "JSON array"},
		{"a ticket lacking its key", map[string]any{n: "hi", ts: []any{map[string]any{}}}, "item 1"},
		{"only the note", map[string]any{n: "hi"}, "lacks"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a.t = t
			a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, step.ID), map[string]any{"entries": c.entries}, http.StatusBadRequest, c.msg)
		})
	}
	a.t = t

	humanMustDecide(a, rn.ID, step.ID, map[string]any{"entries": map[string]any{n: "Be careful", ts: []any{item("A-1"), item("A-2")}}})
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if got := contentsOf(d, "Note"); len(got) != 1 || got[0] != "Be careful" {
		t.Errorf("Note held %q, want the text as entered", got)
	}
	var list []map[string]string
	if got := contentsOf(d, "Tickets"); len(got) != 1 || json.Unmarshal([]byte(got[0]), &list) != nil || len(list) != 2 || list[1]["key"] != "A-2" {
		t.Errorf("Tickets held %q", got)
	}
	// The step's output names each object.
	e := onlyStep(t, d, "Enter")
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.Output), &out); err != nil || len(out) != 2 || string(out["Note"]) != `"Be careful"` {
		t.Errorf("Enter's output = %q", e.Output)
	}
	u := onlyStep(t, d, "Use")
	if len(u.Inputs) != 2 {
		t.Errorf("Use took %+v, want the note and the tickets", u.Inputs)
	}
}

// A file can't be entered by hand, since no step wrote it.
func TestHumanFileCantBeEntered(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Files")
	enter := humanHuman(a, f.ID, "Upload")
	report := a.newObject(f.ID, "Report")
	file := humanAttribute(a, report.ID, "pdf")
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", file.ID), map[string]any{"file": true}, http.StatusOK, nil)
	a.output(enter.ID, report.ID)
	a.startAt(f.ID, enter.ID)

	rn := a.startRun(f.ID)
	step := a.waitStep(rn.ID, "Upload", stepWaiting)
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, step.ID),
		map[string]any{"entries": map[string]any{fmt.Sprint(report.ID): map[string]any{"pdf": "/etc/passwd"}}},
		http.StatusBadRequest, "can't be filled in by hand")
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
}

// A review can set the object it reviews to a state and fill in another
// object in one decision; each goes its own way.
func TestHumanEntriesWithADecision(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Both")
	draft := a.agent(f.ID, "Draft", "Draft it.")
	doc := a.newObject(f.ID, "Notes")
	approved := a.newState(doc.ID, "approved")
	review := humanHuman(a, f.ID, "Review")
	comment := a.newObject(f.ID, "Comment")
	publish := a.agent(f.ID, "Publish", "Publish.")
	archive := a.agent(f.ID, "Archive", "Archive the comment.")
	a.output(draft.ID, doc.ID)
	a.input(review.ID, doc.ID)
	humanDecides(a, review.ID, doc.ID, approved.ID)
	a.output(review.ID, comment.ID)
	humanTakesIn(a, publish.ID, doc.ID, approved.ID)
	a.input(archive.ID, comment.ID)
	for _, p := range []process{publish, archive} {
		a.output(p.ID, a.newObject(f.ID, p.Name+" result").ID)
	}
	a.startAt(f.ID, draft.ID)
	a.worker.onAgent("Draft", handsIn(data("notes")))
	a.worker.onAgent("Publish", handsIn(data("published")))
	a.worker.onAgent("Archive", handsIn(data("archived")))

	rn := a.startRun(f.ID)
	step := a.waitStep(rn.ID, "Review", stepWaiting)
	// Filling in is required; the state alone isn't enough.
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, step.ID), map[string]any{"state": approved.ID}, http.StatusBadRequest, "don't match")
	humanMustDecide(a, rn.ID, step.ID, map[string]any{"state": approved.ID, "instructions": "Nice.", "entries": map[string]any{fmt.Sprint(comment.ID): "lgtm"}})

	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	r := onlyStep(t, d, "Review")
	if r.Outcome != "approved" || r.Instructions != "Nice." || r.Output != "lgtm" {
		t.Errorf("Review = outcome %q instructions %q output %q", r.Outcome, r.Instructions, r.Output)
	}
	if p := onlyStep(t, d, "Publish"); p.ChangeRequest == nil || p.ChangeRequest.State != "approved" {
		t.Errorf("Publish got %+v", p.ChangeRequest)
	}
	if ar := onlyStep(t, d, "Archive"); len(ar.Inputs) != 1 || ar.Inputs[0].Content != "lgtm" {
		t.Errorf("Archive took %+v", ar.Inputs)
	}
	var reviewObjects []string
	for _, o := range d.Objects {
		if o.Step == r.ID {
			reviewObjects = append(reviewObjects, o.Name+":"+o.State+":"+o.Content)
		}
	}
	slices.Sort(reviewObjects)
	if strings.Join(reviewObjects, "|") != "Comment::lgtm|Notes:approved:"+contentsOf(d, "Notes")[0] {
		t.Errorf("the review recorded %q", reviewObjects)
	}
}

// What a person fills in of a persistent object is stored; entering a
// unique value another run stored terminates the run.
func TestHumanEntriesOfPersistentObjects(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Persistent")
	request := humanHuman(a, f.ID, "Request")
	ticket := a.newObject(f.ID, "Ticket")
	key := humanAttribute(a, ticket.ID, "key")
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", key.ID), map[string]any{"unique": true}, http.StatusOK, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	fix := a.agent(f.ID, "Fix", "Fix it.")
	a.output(request.ID, ticket.ID)
	a.input(fix.ID, ticket.ID)
	a.output(fix.ID, a.newObject(f.ID, "Patch").ID)
	a.startAt(f.ID, request.ID)
	a.worker.onAgent("Fix", handsIn(data("patched")))

	enter := func(k string) runDetail {
		t.Helper()
		rn := a.startRun(f.ID)
		s := a.waitStep(rn.ID, "Request", stepWaiting)
		humanMustDecide(a, rn.ID, s.ID, map[string]any{"entries": map[string]any{fmt.Sprint(ticket.ID): map[string]any{"key": k}}})
		return a.finish(rn.ID)
	}
	first := enter("T-1")
	if first.Status != runDone {
		t.Fatalf("first run %s; steps %s", first.Status, describeSteps(first))
	}
	dup := enter("T-1")
	if dup.Status != runTerminated {
		t.Fatalf("entering a stored key: run %s, want terminated; steps %s", dup.Status, describeSteps(dup))
	}
	r := onlyStep(t, dup, "Request")
	if r.Status != stepDuplicate || !strings.Contains(r.Error, fmt.Sprintf("already stored by run #%d", first.ID)) {
		t.Errorf("Request step = %s %q", r.Status, r.Error)
	}
	if len(stepsOf(dup, "Fix")) != 0 {
		t.Error("Fix ran on a duplicate")
	}
	if other := enter("T-2"); other.Status != runDone {
		t.Errorf("a new key: run %s", other.Status)
	}
}

// A person carries out only a human process; when the process stops being
// human, the person's link goes, as do the states it decided between.
func TestHumanAgentLinks(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Agent links")
	review := humanHuman(a, f.ID, "Review")
	doc := a.newObject(f.ID, "Notes")
	ok := a.newState(doc.ID, "ok")
	a.input(review.ID, doc.ID)
	humanDecides(a, review.ID, doc.ID, ok.ID)
	person := humanPerson(a, f.ID, review.ID, "Editor")
	agent := a.agent(f.ID, "Bot", "")
	a.refused("POST", "/api/links", map[string]any{"process": agent.ID, "object": person.ID, "direction": directionInput}, http.StatusBadRequest, agentLinkMessage)
	// A person isn't generated, nor can a human process list states for one it doesn't take.
	a.refused("POST", "/api/links", map[string]any{"process": review.ID, "object": doc.ID, "direction": directionOutput, "state": ok.ID}, http.StatusBadRequest, "lists the states")

	a.call("PATCH", fmt.Sprintf("/api/processes/%d", review.ID), map[string]any{"kind": kindAgent}, http.StatusOK, nil)
	var links []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &links)
	if len(links) != 1 || links[0].Object != doc.ID || links[0].Direction != directionInput {
		t.Errorf("links left after the review became an agent = %+v, want only its input", links)
	}
}
