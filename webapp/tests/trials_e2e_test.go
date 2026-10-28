package tests

// Trying one process on inputs a person gives it: a trial runs it alone, on
// the board as it is, and stores nothing the flow's real runs see; the
// inputs can be saved with the process as samples, which are part of the
// board.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

type trialObject struct {
	Object int64    `json:"object"`
	Name   string   `json:"name"`
	States []string `json:"states"`
	State  string   `json:"state"`
}

type trialValue struct {
	Object  int64  `json:"object"`
	State   string `json:"state"`
	Content string `json:"content"`
}

type trialSample struct {
	ID     int64        `json:"id"`
	Name   string       `json:"name"`
	Inputs []trialValue `json:"inputs"`
}

// A run trying a process may start from.
type trialPast struct {
	Run     int64  `json:"run"`
	Trial   bool   `json:"trial"`
	TrialOf string `json:"trialOf"`
	Status  string `json:"status"`
	Reached bool   `json:"reached"`
}

type trialPlan struct {
	Process     int64         `json:"process"`
	ProcessName string        `json:"processName"`
	Kind        string        `json:"kind"`
	Takes       []trialObject `json:"takes"`
	Samples     []trialSample `json:"samples"`
	Past        []trialPast   `json:"past"`
	Effects     []string      `json:"effects"`
}

func trialPlanOf(a *app, p int64) trialPlan {
	a.t.Helper()
	var plan trialPlan
	a.call("GET", fmt.Sprintf("/api/processes/%d/trial", p), nil, http.StatusOK, &plan)
	return plan
}

// What the run gives the process to try it on.
func trialRunValues(a *app, p, run int64) []trialValue {
	a.t.Helper()
	var values []trialValue
	a.call("GET", fmt.Sprintf("/api/processes/%d/trial/runs/%d", p, run), nil, http.StatusOK, &values)
	return values
}

// The runs a plan offers, as run:reached, or run:held, the latest first.
func describePast(past []trialPast) string {
	var out []string
	for _, p := range past {
		how := "held"
		if p.Reached {
			how = "reached"
		}
		out = append(out, fmt.Sprintf("%d:%s", p.Run, how))
	}
	return strings.Join(out, " ")
}

func tryProcess(a *app, p int64, inputs ...trialValue) run {
	a.t.Helper()
	var rn run
	a.call("POST", fmt.Sprintf("/api/processes/%d/trial", p), map[string]any{"inputs": inputs}, http.StatusCreated, &rn)
	return rn
}

func saveSample(a *app, p int64, name string, inputs ...trialValue) []trialSample {
	a.t.Helper()
	var samples []trialSample
	a.call("PUT", fmt.Sprintf("/api/processes/%d/samples", p), map[string]any{"name": name, "inputs": inputs}, http.StatusOK, &samples)
	return samples
}

// What each sample gives, as name=content of each object joined by commas,
// the samples by semicolons.
func describeSamples(samples []trialSample) string {
	var out []string
	for _, s := range samples {
		var ins []string
		for _, in := range s.Inputs {
			v := in.Content
			if in.State != "" {
				v += " (" + in.State + ")"
			}
			ins = append(ins, v)
		}
		out = append(out, s.Name+"="+strings.Join(ins, ","))
	}
	return strings.Join(out, ";")
}

// Trying Count runs it alone on the ticket given: not Draft before it, nor
// Publish taking what it generates or Label after Draft, nor what handles
// its failure when it fails. The trial is marked as one, is no last run of
// the flow, and isn't re-run.
func TestTrialRunsOnlyTheProcess(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	fix := a.agent(r.f.ID, "Fix", "Fix the count.")
	a.call("POST", "/api/connections", map[string]any{"source": r.count.ID, "target": fix.ID, "kind": "failure"}, http.StatusCreated, nil)

	plan := trialPlanOf(a, r.count.ID)
	if plan.Process != r.count.ID || plan.ProcessName != "Count" || plan.Kind != kindScript || len(plan.Takes) != 1 ||
		plan.Takes[0].Object != r.ticket.ID || plan.Takes[0].Name != "Ticket" || len(plan.Past) != 0 || len(plan.Samples) != 0 {
		t.Fatalf("plan = %+v, want Count taking the ticket, never run, without samples", plan)
	}
	if !slices.ContainsFunc(plan.Effects, func(e string) bool { return strings.Contains(e, "script runs") }) {
		t.Errorf("effects = %q, want them to say the script runs", plan.Effects)
	}

	ticket := `{"data": "Fix the logout"}`
	rn := tryProcess(a, r.count.ID, trialValue{Object: r.ticket.ID, Content: ticket})
	if rn.TrialOf == nil || *rn.TrialOf != r.count.ID || rn.TrialOfName != "Count" || rn.Parent != nil || rn.RerunOf != nil {
		t.Fatalf("trial = %+v, want one of Count", rn)
	}
	d := a.finish(rn.ID)
	if d.Status != runDone || len(d.Steps) != 1 {
		t.Fatalf("trial %s, want done with Count's step alone; steps %s", d.Status, describeSteps(d))
	}
	cs := onlyStep(t, d, "Count")
	if len(cs.Inputs) != 1 || cs.Inputs[0].Name != "Ticket" || cs.Inputs[0].Content != ticket {
		t.Errorf("Count took %+v, want the ticket given", cs.Inputs)
	}
	if got := contentsOf(d, "Size"); len(got) != 1 || !strings.Contains(got[0], fmt.Sprint(len(ticket))) {
		t.Errorf("Size held %q, want the ticket's length", got)
	}
	for _, p := range []string{"Draft", "Publish", "Label", "Fix"} {
		if n := len(a.worker.runsOf(p)); n != 0 {
			t.Errorf("%s ran %d times in a trial of Count", p, n)
		}
	}

	var f flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", r.f.ID), nil, http.StatusOK, &f)
	if f.LastRun != nil {
		t.Errorf("the flow's last run is %+v, want none: a trial isn't one", f.LastRun)
	}
	if rs := nestedRuns(a, r.f.ID); len(rs) != 0 {
		t.Errorf("runs = %+v, want none: trials are listed only when asked for", rs)
	}
	var tried []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d&started=trial", r.f.ID), nil, http.StatusOK, &tried)
	if len(tried) != 1 || tried[0].TrialOf == nil {
		t.Errorf("trials = %+v, want the trial, marked as one", tried)
	}
	a.refused("GET", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", d.ID, cs.ID), nil, http.StatusConflict, "is a trial of Count")
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", d.ID, cs.ID), map[string]any{}, http.StatusConflict, "is a trial of Count")

	// The plan may start from what the trial took now.
	plan = trialPlanOf(a, r.count.ID)
	if len(plan.Past) != 1 || plan.Past[0].Run != rn.ID || !plan.Past[0].Trial || plan.Past[0].TrialOf != "Count" || !plan.Past[0].Reached {
		t.Errorf("past = %+v, want the trial, which reached Count", plan.Past)
	}
	if v := trialRunValues(a, r.count.ID, rn.ID); len(v) != 1 || v[0].Object != r.ticket.ID || v[0].Content != ticket {
		t.Errorf("the trial gives %+v, want the ticket it took", v)
	}

	// Without a ticket the script fails, and nothing handles it.
	failed := a.finish(tryProcess(a, r.count.ID).ID)
	if failed.Status != runFailed || len(failed.Steps) != 1 || failed.Steps[0].Status != stepError {
		t.Errorf("a failing trial %s, want failed with Count's step alone; steps %s", failed.Status, describeSteps(failed))
	}
	if n := len(a.worker.runsOf("Fix")); n != 0 {
		t.Errorf("Fix ran %d times, want none: a trial's failure isn't handled", n)
	}

	// A real run is the flow's last, and its rows on the Data page are the
	// real runs' unless the query asks for trials.
	real := a.finish(a.startRun(r.f.ID).ID)
	a.call("GET", fmt.Sprintf("/api/flows/%d", r.f.ID), nil, http.StatusOK, &f)
	if f.LastRun == nil || f.LastRun.ID != real.ID {
		t.Errorf("the flow's last run is %+v, want run %d", f.LastRun, real.ID)
	}
	dd := a.data(r.f.ID)
	if len(dd.Rows) != 1 || dd.Rows[0].ID != real.ID || dd.Trials != 2 || dd.Searched != 3 {
		t.Errorf("data rows %v of %d searched with %d trials, want run %d alone of 3, leaving 2 trials out", dataRunIDs(dd), dd.Searched, dd.Trials, real.ID)
	}
	if got := facetCounts(dd.Facets, "trial"); got != "Count=2" {
		t.Errorf("trial facet = %s, want Count=2", got)
	}
	for _, q := range []string{"trial:*", "trial:Count", "trial:count status:(done OR failed)"} {
		if dd := a.searchData(r.f.ID, q, ""); len(dd.Rows) != 2 || dd.Trials != 0 || slices.Contains(dataRunIDs(dd), real.ID) {
			t.Errorf("%s matches %v with %d trials left out, want the 2 trials", q, dataRunIDs(dd), dd.Trials)
		}
	}
	if dd := a.searchData(r.f.ID, "-trial:*", ""); len(dd.Rows) != 1 || dd.Rows[0].ID != real.ID {
		t.Errorf("-trial:* matches %v, want run %d", dataRunIDs(dd), real.ID)
	}
	if dd := a.searchData(r.f.ID, "status:failed", ""); len(dd.Rows) != 0 {
		t.Errorf("status:failed matches %v, want none: the failed trial stays out", dataRunIDs(dd))
	}
}

// Trying a process may start from the flow's latest runs: a trial of the
// process before it, giving what that generated and what it was given, so
// each process is tried on what the one before it really generated; and a
// run that reached it, giving what it took there. A run of another flow is
// neither offered nor read.
func TestTrialStartsFromTheFlowsRuns(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)

	drafted := a.finish(tryProcess(a, r.draft.ID).ID)
	ticket := contentsOf(drafted, "Ticket")
	if len(ticket) != 1 {
		t.Fatalf("the trial of Draft held tickets %q, want one", ticket)
	}
	if got := describePast(trialPlanOf(a, r.count.ID).Past); got != fmt.Sprintf("%d:held", drafted.ID) {
		t.Errorf("Count may start from %s, want the trial of Draft", got)
	}
	values := trialRunValues(a, r.count.ID, drafted.ID)
	if len(values) != 1 || values[0].Object != r.ticket.ID || values[0].Content != ticket[0] {
		t.Fatalf("the trial of Draft gives Count %+v, want its ticket", values)
	}

	counted := a.finish(tryProcess(a, r.count.ID, values...).ID)
	size := contentsOf(counted, "Size")
	if counted.Status != runDone || len(size) != 1 {
		t.Fatalf("the trial of Count %s, holding sizes %q", counted.Status, size)
	}
	plan := trialPlanOf(a, r.publish.ID)
	if got := describePast(plan.Past); got != fmt.Sprintf("%d:held %d:held", counted.ID, drafted.ID) {
		t.Errorf("Publish may start from %s, want the trials of Count and Draft", got)
	}
	if plan.Past[0].TrialOf != "Count" || !plan.Past[0].Trial {
		t.Errorf("the latest run offered is %+v, want the trial of Count", plan.Past[0])
	}
	// The ticket Count was given, and the size it generated.
	given := map[int64]string{}
	for _, v := range trialRunValues(a, r.publish.ID, counted.ID) {
		given[v.Object] = v.Content
	}
	if len(given) != 2 || given[r.ticket.ID] != ticket[0] || given[r.size.ID] != size[0] {
		t.Errorf("the trial of Count gives Publish %q, want the ticket %q and the size %q", given, ticket[0], size[0])
	}

	real := a.finish(a.startRun(r.f.ID).ID)
	plan = trialPlanOf(a, r.publish.ID)
	if len(plan.Past) != 3 || plan.Past[0].Run != real.ID || !plan.Past[0].Reached || plan.Past[0].Trial {
		t.Errorf("Publish may start from %s, want run %d first, which reached it", describePast(plan.Past), real.ID)
	}
	ps := onlyStep(t, real, "Publish")
	for _, v := range trialRunValues(a, r.publish.ID, real.ID) {
		if i := slices.IndexFunc(ps.Inputs, func(in runInput) bool { return in.Content == v.Content }); i < 0 {
			t.Errorf("run %d gives Publish %q, which it didn't take there: %+v", real.ID, v.Content, ps.Inputs)
		}
	}

	other, _ := buildsFlow(a, "Other", func(int) bool { return false })
	elsewhere := a.finish(a.startRun(other.ID).ID)
	a.refused("GET", fmt.Sprintf("/api/processes/%d/trial/runs/%d", r.publish.ID, elsewhere.ID), nil, http.StatusNotFound, "no such run")
	a.refused("GET", fmt.Sprintf("/api/processes/%d/trial/runs/999999999", r.publish.ID), nil, http.StatusNotFound, "no such run")
	if got := describePast(trialPlanOf(a, r.publish.ID).Past); strings.Contains(got, fmt.Sprint(elsewhere.ID)+":") {
		t.Errorf("Publish may start from %s, which has run %d of another flow", got, elsewhere.ID)
	}
}

func dataRunIDs(d flowData) []int64 {
	var ids []int64
	for _, r := range d.Rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// Only an agent, script or connector process is tried, on objects it
// takes, in states they have.
func TestTrialRefusals(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	review := a.newProcess(r.f.ID, map[string]any{"name": "Review", "kind": kindHuman})
	a.refused("GET", fmt.Sprintf("/api/processes/%d/trial", review.ID), nil, http.StatusConflict, "only an agent, script or connector")
	a.refused("POST", fmt.Sprintf("/api/processes/%d/trial", review.ID), map[string]any{}, http.StatusConflict, "only an agent, script or connector")
	a.refused("POST", fmt.Sprintf("/api/processes/%d/trial", r.count.ID),
		map[string]any{"inputs": []trialValue{{Object: r.size.ID, Content: "{}"}}}, http.StatusBadRequest, "takes no object")
	a.refused("POST", fmt.Sprintf("/api/processes/%d/trial", r.count.ID),
		map[string]any{"inputs": []trialValue{{Object: r.ticket.ID, State: "nowhere", Content: "{}"}}}, http.StatusBadRequest, "has no state")
	a.refused("GET", "/api/processes/999999999/trial", nil, http.StatusNotFound, "process not found")
	a.refused("POST", "/api/processes/999999999/trial", map[string]any{}, http.StatusNotFound, "process not found")
	if rs := nestedRuns(a, r.f.ID); len(rs) != 0 {
		t.Errorf("refused trials left runs %+v", rs)
	}
}

// A trial stores no persistent object's values: what it generates is no
// duplicate for a real run, nor it of a value a real run stored, and the
// cursor carried from run to run doesn't move. Nor does it write a shared
// struct's records.
func TestTrialStoresNothingRealRunsSee(t *testing.T) {
	a := newApp(t)

	f := a.newFlow("Open tickets")
	open := a.agent(f.ID, "Open", "Open a ticket.")
	ticket := a.newObject(f.ID, "Ticket")
	nestedPatchObject(a, ticket.ID, map[string]any{"persistent": true})
	nestedAttribute(a, ticket.ID, "key", true)
	a.output(open.ID, ticket.ID)
	a.startAt(f.ID, open.ID)
	a.worker.onAgent("Open", handsIn(map[string]any{"key": "T-1"}))
	if d := a.finish(tryProcess(a, open.ID).ID); d.Status != runDone {
		t.Fatalf("trial %s, want done; steps %s", d.Status, describeSteps(d))
	}
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Errorf("a run after the trial %s, want done: the trial stored no ticket; steps %s", d.Status, describeSteps(d))
	}
	if d := a.finish(tryProcess(a, open.ID).ID); d.Status != runDone {
		t.Errorf("a trial after the run %s, want done: it stores nothing to collide; steps %s", d.Status, describeSteps(d))
	}
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runTerminated {
		t.Errorf("a second run %s, want terminated by the first run's ticket", d.Status)
	}

	c := a.newFlow("Cursor trial")
	fetch := a.newProcess(c.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "cat $INPUTS/Cursor"})
	cursor := a.newObject(c.ID, "Cursor")
	nestedPatchObject(a, cursor.ID, map[string]any{"persistent": true})
	a.input(fetch.ID, cursor.ID)
	a.output(fetch.ID, cursor.ID)
	a.startAt(c.ID, fetch.ID)
	var mu sync.Mutex
	var given []string
	a.worker.onScript("Fetch", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		offset := 0
		for _, in := range run.Inputs {
			given = append(given, in.Content)
			var v struct{ Data int }
			json.Unmarshal([]byte(in.Content), &v)
			offset = v.Data
		}
		return runResult{OK: true, Output: fmt.Sprintf(`{"data": %d}`, offset+1)}
	})
	if d := a.finish(a.startRun(c.ID).ID); d.Status != runDone {
		t.Fatalf("first run %s; steps %s", d.Status, describeSteps(d))
	}
	if d := a.finish(tryProcess(a, fetch.ID, trialValue{Object: cursor.ID, Content: `{"data": 41}`}).ID); d.Status != runDone {
		t.Fatalf("trial %s; steps %s", d.Status, describeSteps(d))
	}
	d := a.finish(a.startRun(c.ID).ID)
	if got := contentsOf(d, "Cursor"); d.Status != runDone || len(got) != 1 || !strings.Contains(got[0], "2") {
		t.Errorf("the run after the trial %s moved the cursor to %q, want 2, on from the first run's 1", d.Status, got)
	}
	mu.Lock()
	if len(given) != 2 || !strings.Contains(given[0], "41") || !strings.Contains(given[1], "1") {
		t.Errorf("Fetch was given %q, want the trial's 41 and then the first run's 1", given)
	}
	mu.Unlock()

	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Trial exception", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	g := a.newFlow("Fetch trial exceptions")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": g.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	exc := modNamed(t, modObjects(a, g.ID), "Trial exception")
	sentry := a.newProcess(g.ID, map[string]any{"name": "Sentry", "kind": kindScript, "script": "fetch"})
	a.output(sentry.ID, exc.ID)
	a.startAt(g.ID, sentry.ID)
	a.worker.onScript("Sentry", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"id": "E-1"}`}
	})
	if d := a.finish(tryProcess(a, sentry.ID).ID); d.Status != runDone {
		t.Fatalf("trial of Sentry %s; steps %s", d.Status, describeSteps(d))
	}
	if rs := sharedRecords(a, s.ID); len(rs) != 0 {
		t.Errorf("the trial wrote records %v", rs)
	}
	if d := a.finish(a.startRun(g.ID).ID); d.Status != runDone {
		t.Fatalf("run of Sentry %s; steps %s", d.Status, describeSteps(d))
	}
	if rs := sharedRecords(a, s.ID); len(rs) != 1 || rs["E-1"].ID == 0 {
		t.Errorf("records after a real run %v, want E-1", rs)
	}
}

// A process on a flow process's board is tried on that board.
func TestTrialInsideAFlowProcess(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Nested trial")
	outer := nestedFlowProcess(a, f.ID, "Handle", false)
	inner := nestedAgent(a, f.ID, outer.ID, "Summarize")
	note := nestedObject(a, outer.ID, "Note")
	summary := nestedObject(a, outer.ID, "Summary")
	a.input(inner.ID, note.ID)
	a.output(inner.ID, summary.ID)
	nestedStartAt(a, outer.ID, inner.ID)
	a.worker.onAgent("Summarize", handsIn(data("the summary")))

	if plan := trialPlanOf(a, inner.ID); len(plan.Takes) != 1 || plan.Takes[0].Object != note.ID {
		t.Fatalf("plan = %+v, want Summarize taking the note", plan)
	}
	d := a.finish(tryProcess(a, inner.ID, trialValue{Object: note.ID, Content: `{"data": "the note"}`}).ID)
	s := onlyStep(t, d, "Summarize")
	if d.Status != runDone || len(d.Steps) != 1 || len(s.Inputs) != 1 || !strings.Contains(s.Inputs[0].Content, "the note") {
		t.Errorf("trial %s with steps %s, Summarize taking %+v; want it alone on the note", d.Status, describeSteps(d), s.Inputs)
	}
}

// Samples are saved with the process, several under a name each, and
// replaced by name; the plan starts from them. They are part of the board:
// versions and history keep them, and pasting copies them along with the
// objects they give.
func TestTrialSamples(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	a.newState(r.ticket.ID, "draft")
	path := fmt.Sprintf("/api/processes/%d/samples", r.count.ID)

	samples := saveSample(a, r.count.ID, " Long ", trialValue{Object: r.ticket.ID, State: "draft", Content: "a long ticket"})
	if got := describeSamples(samples); got != "Long=a long ticket (draft)" {
		t.Errorf("samples = %s", got)
	}
	samples = saveSample(a, r.count.ID, "Short", trialValue{Object: r.ticket.ID, Content: "short"})
	samples = saveSample(a, r.count.ID, "Long", trialValue{Object: r.ticket.ID, Content: "a longer ticket"})
	if got := describeSamples(samples); got != "Long=a longer ticket;Short=short" {
		t.Errorf("samples = %s, want Long replaced", got)
	}
	a.refused("PUT", path, map[string]any{"name": " ", "inputs": []any{}}, http.StatusBadRequest, "needs a name")
	a.refused("PUT", path, map[string]any{"name": "Size", "inputs": []trialValue{{Object: r.size.ID}}}, http.StatusBadRequest, "takes no object")
	a.refused("PUT", path, map[string]any{"name": "Gone", "inputs": []trialValue{{Object: r.ticket.ID, State: "gone"}}}, http.StatusBadRequest, "has no state")
	if got := describeSamples(trialPlanOf(a, r.count.ID).Samples); got != "Long=a longer ticket;Short=short" {
		t.Errorf("plan's samples = %s", got)
	}

	// A version keeps them as they were, and the history each change.
	v := verSave(a, r.f.ID)
	histRecord(a, r.f.ID)
	short := samples[1]
	a.call("DELETE", fmt.Sprintf("%s/%d", path, short.ID), nil, http.StatusNoContent, nil)
	a.refused("DELETE", fmt.Sprintf("%s/%d", path, short.ID), nil, http.StatusNotFound, "no such sample")
	histRecord(a, r.f.ID)
	if got := describeSamples(trialPlanOf(a, r.count.ID).Samples); got != "Long=a longer ticket" {
		t.Errorf("after deleting Short: %s", got)
	}
	histUndo(a, r.f.ID)
	if got := describeSamples(trialPlanOf(a, r.count.ID).Samples); got != "Long=a longer ticket;Short=short" {
		t.Errorf("after undoing that: %s", got)
	}
	histRedo(a, r.f.ID)
	if got := describeSamples(trialPlanOf(a, r.count.ID).Samples); got != "Long=a longer ticket" {
		t.Errorf("after redoing it: %s", got)
	}
	verRestore(a, v.ID)
	if got := describeSamples(trialPlanOf(a, r.count.ID).Samples); got != "Long=a longer ticket;Short=short" {
		t.Errorf("after going back to version %d: %s", v.ID, got)
	}

	// Pasted with the ticket, Count's copy gives the copy of it; without,
	// a sample giving nothing it takes stays behind.
	other := a.newFlow("Rerun pasted")
	var res struct {
		Processes []int64 `json:"processes"`
		Objects   []int64 `json:"objects"`
	}
	a.call("POST", "/api/paste", map[string]any{"from": r.f.ID, "processes": []int64{r.count.ID}, "objects": []int64{r.ticket.ID}, "flow": other.ID},
		http.StatusCreated, &res)
	plan := trialPlanOf(a, res.Processes[0])
	if got := describeSamples(plan.Samples); got != "Long=a longer ticket;Short=short" || plan.Samples[0].Inputs[0].Object != res.Objects[0] {
		t.Errorf("pasted samples = %s giving %+v, want them giving the ticket's copy %d", got, plan.Samples, res.Objects[0])
	}
	a.call("POST", "/api/paste", map[string]any{"from": r.f.ID, "processes": []int64{r.count.ID}, "flow": other.ID, "dx": 100},
		http.StatusCreated, &res)
	if plan := trialPlanOf(a, res.Processes[0]); len(plan.Samples) != 0 {
		t.Errorf("Count pasted alone has samples %s", describeSamples(plan.Samples))
	}

	// A trial on a sample's state hands the ticket in that state.
	d := a.finish(tryProcess(a, r.count.ID, trialValue{Object: r.ticket.ID, State: "draft", Content: `{"data": "draft"}`}).ID)
	if s := onlyStep(t, d, "Count"); len(s.Inputs) != 1 || s.Inputs[0].State != "draft" {
		t.Errorf("Count took %+v, want the ticket in draft", s.Inputs)
	}
}

// A setup file has each process's samples, by the names of the objects and
// states they give; importing one without samples for a process keeps
// those it has, as the assistant's replies do.
func TestTrialSamplesInSetup(t *testing.T) {
	a := setupApp(t)
	f := a.newFlow("Sampled")
	count := a.newProcess(f.ID, map[string]any{"name": "Count", "kind": kindScript, "script": "wc -c"})
	ticket := a.newObject(f.ID, "Ticket")
	a.newState(ticket.ID, "draft")
	a.input(count.ID, ticket.ID)
	saveSample(a, count.ID, "Long", trialValue{Object: ticket.ID, State: "draft", Content: "a long ticket"})

	file := setupExport(a, "Sampled")
	for _, want := range []string{"samples:", "name: Long", "object: Ticket", "state: draft", "content: a long ticket"} {
		if !strings.Contains(file, want) {
			t.Fatalf("the export lacks %q:\n%s", want, file)
		}
	}
	setupImportOK(a, strings.Replace(file, "a long ticket", "an imported ticket", 1))
	if got := describeSamples(trialPlanOf(a, count.ID).Samples); got != "Long=an imported ticket (draft)" {
		t.Errorf("after importing: %s", got)
	}
	setupImportOK(a, withoutKey(t, file, "samples"))
	if got := describeSamples(trialPlanOf(a, count.ID).Samples); got != "Long=an imported ticket (draft)" {
		t.Errorf("after importing without samples: %s, want them kept", got)
	}
	bad := strings.Replace(file, "object: Ticket", "object: Nothing", 1)
	if status, body := setupImport(a, bad); status != http.StatusBadRequest || !strings.Contains(string(body), "takes no object") {
		t.Errorf("a sample giving an object Count doesn't take = %d %s", status, body)
	}
}

// The YAML file with every mapping's key of that name left out.
func withoutKey(t *testing.T, file, key string) string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(file), &doc); err != nil {
		t.Fatal(err)
	}
	var drop func(n *yaml.Node)
	drop = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			var kept []*yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value != key {
					kept = append(kept, n.Content[i], n.Content[i+1])
				}
			}
			n.Content = kept
		}
		for _, c := range n.Content {
			drop(c)
		}
	}
	drop(&doc)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), key+":") {
		t.Fatalf("%s is still in:\n%s", key, out)
	}
	return string(out)
}

// The flow list's health and last run, and the Activity page, count no
// trial: not as a run, a failure or one going on, nor its step as a
// process failing; what a trial spent still counts toward what its flow
// spent, as it cost that money.
func TestTrialsCountAsNoRuns(t *testing.T) {
	a, db := activityApp(t, newApp)
	f := a.newFlow("Trialled")
	build := a.agent(f.ID, "Build", "Build it.")
	a.output(build.ID, a.newObject(f.ID, "Binary").ID)
	a.startAt(f.ID, build.ID)
	var mu sync.Mutex
	mode := "ok"
	release := make(chan struct{})
	a.worker.onAgent("Build", func(ctx context.Context, _ agentRun) runResult {
		mu.Lock()
		m := mode
		mu.Unlock()
		switch m {
		case "fail":
			return runResult{Error: "the compiler gave up"}
		case "hold":
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return submitted(data("binary"))
	})
	set := func(m string) {
		mu.Lock()
		mode = m
		mu.Unlock()
	}

	real := a.finish(a.startRun(f.ID).ID)
	set("fail")
	failed := a.finish(tryProcess(a, build.ID).ID)
	set("hold")
	going := tryProcess(a, build.ID)
	a.waitStep(going.ID, "Build", stepActive)
	if real.Status != runDone || failed.Status != runFailed {
		t.Fatalf("the run %s and the failing trial %s", real.Status, failed.Status)
	}

	check := func(when string) {
		t.Helper()
		l := listed(a, f.ID)
		if l.Health == nil || *l.Health != (flowHealth{Runs: 1}) || l.LastRun == nil || l.LastRun.ID != real.ID {
			t.Errorf("%s: the flow list has health %+v and last run %+v, want run %d alone", when, l.Health, l.LastRun, real.ID)
		}
		page := page30(a)
		fa := page.flow(f.ID)
		if fa == nil || sum(fa.Runs) != 1 || sum(fa.Failed) != 0 || sum(page.Total.Runs) != 1 || sum(page.Total.Failed) != 0 {
			t.Errorf("%s: the Activity page counts %+v of the flow, %+v in all, want one run, none failed", when, fa, page.Total)
		}
		if page.Running.Total != 0 || page.Failed.Total != 0 || page.Running.has(going.ID) != nil || page.Failed.has(failed.ID) != nil {
			t.Errorf("%s: the Activity page lists %+v going on and %+v failed, want no trial", when, page.Running, page.Failed)
		}
		if slices.ContainsFunc(page.Processes, func(pf activityProcess) bool { return pf.Process == build.ID }) {
			t.Errorf("%s: the trial's failed step counts Build as failing: %+v", when, page.Processes)
		}
	}
	check("while a trial goes on")
	close(release)
	a.finish(going.ID)
	check("once it ended")

	// What the trials spent counts toward the flow's, with the run's.
	var want int64
	for _, id := range []int64{real.ID, failed.ID, going.ID} {
		kept, _ := activitySpent(t, db, id)
		if kept <= 0 {
			t.Errorf("run %d kept %d spent, want what its agent's calls cost", id, kept)
		}
		want += kept
	}
	if fa := page30(a).flow(f.ID); fa == nil || !approxEqual(sumCost(fa.Cost), float64(want)/1e6) {
		t.Errorf("the flow spent %+v, want %v with its trials'", fa, float64(want)/1e6)
	}
}

func sumCost(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s
}

// A trial tries the board as it is, the draft, even while the flow has
// published a version of it. Samples changed since a version show in its
// diff, but don't make the board a draft of what it published.
func TestTrialsTryTheDraft(t *testing.T) {
	a := newApp(t)
	r := newRerunFlow(a)
	published := pubPublish(a, r.f.ID, "counting characters")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", r.count.ID), map[string]any{"script": "wc -l"}, http.StatusOK, nil)
	var mu sync.Mutex
	var scripts []string
	a.worker.onScript("Count", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		scripts = append(scripts, run.Script)
		return runResult{OK: true, Output: `{"data": 1}`}
	})
	d := a.finish(tryProcess(a, r.count.ID, trialValue{Object: r.ticket.ID, Content: `{"data": "Fix the login"}`}).ID)
	mu.Lock()
	if d.Status != runDone || d.Version == nil || *d.Version == published.ID || len(scripts) != 1 || scripts[0] != "wc -l" {
		t.Errorf("trial %s on version %v ran %q, want the draft's script on a version after the published %d", d.Status, d.Version, scripts, published.ID)
	}
	mu.Unlock()

	// With the draft published, a sample saved changes nothing a run of it
	// would do, but the diff lists it.
	pubPublish(a, r.f.ID, "counting lines")
	saveSample(a, r.count.ID, "Long", trialValue{Object: r.ticket.ID, Content: "a long ticket"})
	l := pubList(a, r.f.ID)
	if l.Draft {
		t.Errorf("a sample saved made the board a draft of what it published")
	}
	if got := pubNames(pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", *l.Published)).Samples); !slices.Equal(got, []string{"added Count: Long"}) {
		t.Errorf("samples changed since the version published = %v, want Long added", got)
	}
	v := verSave(a, r.f.ID)
	saveSample(a, r.count.ID, "Long", trialValue{Object: r.ticket.ID, Content: "a longer ticket"})
	got := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", v.ID)).Samples
	if len(got) != 1 || got[0].Change != "changed" || len(got[0].Fields) != 1 || got[0].Fields[0].Field != "Ticket" ||
		got[0].Fields[0].Before != "a long ticket" || got[0].Fields[0].After != "a longer ticket" {
		t.Errorf("samples changed since version %d = %+v, want what Long gives the ticket", v.ID, got)
	}
}
