package tests

// A flow publishes one of its versions for the runs nobody starts by hand
// from the board, schedules, the API and runs on records, while its board is
// a draft; versions keep notes; and what changed between two boards.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Which version a flow published, as the Versions panel shows it.
type pubVersions struct {
	Current     *int64     `json:"current"`
	Published   *int64     `json:"published"`
	PublishedAt *time.Time `json:"publishedAt"`
	PublishedBy string     `json:"publishedBy"`
	Draft       bool       `json:"draft"`
	Versions    []version  `json:"versions"`
}

func pubList(a *app, f int64) pubVersions {
	a.t.Helper()
	var l pubVersions
	a.call("GET", fmt.Sprintf("/api/versions?flow=%d", f), nil, http.StatusOK, &l)
	return l
}

// Publishes the board as it is, with a note.
func pubPublish(a *app, f int64, note string) version {
	a.t.Helper()
	var v version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f), map[string]any{"note": note, "publish": true}, http.StatusCreated, &v)
	return v
}

// What changed, as the diff route answers.
type pubItem struct {
	Change string `json:"change"`
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Fields []struct {
		Field  string `json:"field"`
		Before string `json:"before"`
		After  string `json:"after"`
	} `json:"fields"`
}

type pubDiff struct {
	From        int64     `json:"from"`
	To          *int64    `json:"to"`
	Processes   []pubItem `json:"processes"`
	Objects     []pubItem `json:"objects"`
	States      []pubItem `json:"states"`
	Attributes  []pubItem `json:"attributes"`
	Links       []pubItem `json:"links"`
	Connections []pubItem `json:"connections"`
	Settings    []struct {
		Field string `json:"field"`
		After string `json:"after"`
	} `json:"settings"`
	Layout bool `json:"layout"`
}

func pubDiffOf(a *app, path string) pubDiff {
	a.t.Helper()
	var d pubDiff
	a.call("GET", path, nil, http.StatusOK, &d)
	return d
}

// The change and name of each item, for comparing.
func pubNames(items []pubItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Change+" "+it.Name)
	}
	return out
}

// The prompts an agent process ran with, as the worker got them.
type pubPrompts struct {
	mu   sync.Mutex
	seen []string
}

func (p *pubPrompts) agent(out any) func(context.Context, agentRun) runResult {
	return func(_ context.Context, run agentRun) runResult {
		p.mu.Lock()
		p.seen = append(p.seen, run.Prompt)
		p.mu.Unlock()
		return submitted(out)
	}
}

// Whether the latest prompt says want.
func (p *pubPrompts) latest(t *testing.T, want string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) == 0 || !strings.Contains(p.seen[len(p.seen)-1], want) {
		t.Errorf("the latest prompt isn't %q: %q", want, p.seen)
	}
}

// Runs over the API, and by hand on the version published, run what was
// published, while the Run button runs the board as it is; what the draft
// deleted still runs in the version published.
func TestPublishedVersionRunsWhileTheBoardIsADraft(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Newsletter")
	write := a.agent(f.ID, "Write", "Write the first issue.")
	issue := a.newObject(f.ID, "Issue")
	a.output(write.ID, issue.ID)
	a.startAt(f.ID, write.ID)
	var prompts pubPrompts
	a.worker.onAgent("Write", prompts.agent(data("the issue")))
	a.worker.onAgent("Proofread", handsIn(data("proofread")))
	k := apiNewKey(a, "newsletter")

	// Never published, a flow runs its board as it is, as ever.
	if l := pubList(a, f.ID); l.Published != nil || l.Draft {
		t.Fatalf("a new flow's versions = %+v", l)
	}
	before := apiStart(a, k.Key, f.ID, nil, http.StatusCreated)
	a.finish(before.ID)
	prompts.latest(t, "Write the first issue.")

	pub := pubPublish(a, f.ID, "The first issue")
	l := pubList(a, f.ID)
	if l.Published == nil || *l.Published != pub.ID || l.PublishedAt == nil || l.Draft || pub.Note != "The first issue" {
		t.Fatalf("published %+v, versions %+v", pub, l)
	}

	// Halfway through an edit: a new prompt, and a process added after.
	verPatch(a, fmt.Sprintf("/api/processes/%d", write.ID), map[string]any{"prompt": "Write the second issue."})
	proof := a.agent(f.ID, "Proofread", "Proofread it.")
	a.input(proof.ID, issue.ID)
	if l := pubList(a, f.ID); !l.Draft || *l.Published != pub.ID {
		t.Fatalf("after editing, versions = %+v, want a draft of what is published", l)
	}

	rn := apiStart(a, k.Key, f.ID, nil, http.StatusCreated)
	d := a.finish(rn.ID)
	if d.Status != runDone || d.Version == nil || *d.Version != pub.ID || len(stepsOf(d, "Proofread")) != 0 {
		t.Fatalf("the API's run is %s on version %v with steps %s, want done on %d without Proofread", d.Status, show(d.Version), describeSteps(d), pub.ID)
	}
	prompts.latest(t, "Write the first issue.")

	// The Run button runs the draft, saved as a version first.
	d = a.finish(a.startRun(f.ID).ID)
	if *d.Version == pub.ID || len(stepsOf(d, "Proofread")) != 1 {
		t.Errorf("the run by hand was on version %d with steps %s", *d.Version, describeSteps(d))
	}
	prompts.latest(t, "Write the second issue.")
	var mine run
	a.call("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), map[string]any{"published": true}, http.StatusCreated, &mine)
	if d := a.finish(mine.ID); *d.Version != pub.ID || len(stepsOf(d, "Proofread")) != 0 {
		t.Errorf("the run of the version published by hand was on version %d with steps %s", *d.Version, describeSteps(d))
	}
	prompts.latest(t, "Write the first issue.")

	// The draft drops the process and its object; the version published
	// still has them, and runs them.
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", write.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", issue.ID), nil, http.StatusNoContent, nil)
	d = a.finish(apiStart(a, k.Key, f.ID, nil, http.StatusCreated).ID)
	w := onlyStep(t, d, "Write")
	if d.Status != runDone || w.Process == nil || *w.Process != write.ID {
		t.Fatalf("with the process gone from the draft, the run %s with Write %+v; steps %s", d.Status, w, describeSteps(d))
	}
	if got := contentsOf(d, "Issue"); len(got) != 1 || !strings.Contains(got[0], "the issue") {
		t.Errorf("the run holds Issue %q", got)
	}
	if o := d.Objects; len(o) != 1 || o[0].Object == nil || *o[0].Object != issue.ID {
		t.Errorf("the run's objects = %+v, want Issue by its id", o)
	}
	var got apiRun
	status, reply := apiCall(a, k.Key, "GET", fmt.Sprintf("/v1/runs/%d", d.ID), nil)
	if json.Unmarshal(reply, &got); status != http.StatusOK || got.Status != runDone {
		t.Errorf("the API's run = %d %s", status, reply)
	}

	// The runs by hand saved the board first, which their versions say.
	l = pubList(a, f.ID)
	var reasons []string
	for _, v := range l.Versions {
		reasons = append(reasons, v.Reason)
	}
	if !slices.Contains(reasons, "run") {
		t.Errorf("the versions' reasons = %q, want a run's", reasons)
	}
}

// What the API says a flow takes, and what it checks a request against, is
// what the version published takes.
func TestAPIDescribesThePublishedVersion(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Briefing")
	write := a.agent(f.ID, "Write", "Write the briefing.")
	topic := a.newObject(f.ID, "Topic")
	a.input(write.ID, topic.ID)
	a.output(write.ID, a.newObject(f.ID, "Briefing").ID)
	a.startAt(f.ID, write.ID)
	a.worker.onAgent("Write", handsIn(data("briefed")))
	k := apiNewKey(a, "briefing")
	pubPublish(a, f.ID, "")

	a.input(write.ID, a.newObject(f.ID, "Audience").ID)
	var described struct {
		Inputs []struct {
			Name string `json:"name"`
		} `json:"inputs"`
	}
	status, reply := apiCall(a, k.Key, "GET", fmt.Sprintf("/v1/flows/%d", f.ID), nil)
	if json.Unmarshal(reply, &described); status != http.StatusOK || len(described.Inputs) != 1 || described.Inputs[0].Name != "Topic" {
		t.Fatalf("described as %d %s, want Topic alone", status, reply)
	}
	apiRefused(a, k.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", f.ID), map[string]any{"inputs": map[string]any{"Topic": "news", "Audience": "all"}}, http.StatusBadRequest, "don't match")
	rn := apiStart(a, k.Key, f.ID, map[string]any{"inputs": map[string]any{"Topic": "news"}}, http.StatusCreated)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Errorf("the run %s; steps %s", d.Status, describeSteps(d))
	}

	// Once nothing is published, the board as it is takes both.
	a.call("DELETE", fmt.Sprintf("/api/versions/published?flow=%d", f.ID), nil, http.StatusOK, nil)
	status, reply = apiCall(a, k.Key, "GET", fmt.Sprintf("/v1/flows/%d", f.ID), nil)
	if json.Unmarshal(reply, &described); status != http.StatusOK || len(described.Inputs) != 2 {
		t.Errorf("unpublished, described as %d %s", status, reply)
	}
	if l := pubList(a, f.ID); l.Published != nil || l.Draft {
		t.Errorf("unpublished, versions = %+v", l)
	}
}

// A schedule runs the version published.
func TestSchedulesRunThePublishedVersion(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Nightly")
	tick := a.newProcess(f.ID, map[string]any{"name": "Tick", "kind": kindScript, "script": "echo published"})
	a.startAt(f.ID, tick.ID)
	var mu sync.Mutex
	var scripts []string
	a.worker.onScript("Tick", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		scripts = append(scripts, run.Script)
		mu.Unlock()
		return runResult{OK: true, Output: `{"data": "tick"}`}
	})
	pub := pubPublish(a, f.ID, "")
	verPatch(a, fmt.Sprintf("/api/processes/%d", tick.ID), map[string]any{"script": "echo draft"})

	s := a.newSchedule(f.ID, everySecond)
	rn := a.waitScheduledRuns(f.ID, s.ID, 1)[0]
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", s.ID), map[string]any{"enabled": false}, http.StatusOK, nil)
	if d := a.finish(rn.ID); d.Status != runDone || *d.Version != pub.ID {
		t.Errorf("the scheduled run %s on version %v, want done on %d", d.Status, show(d.Version), pub.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(scripts) == 0 || scripts[0] != "echo published" {
		t.Errorf("the schedule ran %q", scripts)
	}
}

// A run started on a record runs the version published, and the flows
// offered for records query as that version does.
func TestRecordRunsRunThePublishedVersion(t *testing.T) {
	a := newApp(t)
	s := recordStruct(a, fmt.Sprintf("Bug %d", time.Now().UnixNano()), "open", "fixed")
	f := a.newFlow("Fix bugs")
	bug := recordCopy(a, s, f.ID, nil)
	fix := a.newProcess(f.ID, map[string]any{"name": "Fix", "kind": kindScript, "script": "fix published"})
	a.link(map[string]any{"process": fix.ID, "object": bug.ID, "direction": directionInput, "query": linkQuery{State: "open"}})
	a.startAt(f.ID, fix.ID)
	var mu sync.Mutex
	var scripts []string
	a.worker.onScript("Fix", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		scripts = append(scripts, run.Script)
		mu.Unlock()
		return runResult{OK: true, Output: `{"data": "fixed"}`}
	})
	record := recordAdd(a, s, "B-1", "fixed")
	pub := pubPublish(a, f.ID, "")

	// The draft queries fixed bugs instead, with another script.
	verPatch(a, fmt.Sprintf("/api/processes/%d", fix.ID), map[string]any{"script": "fix draft"})
	var links []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &links)
	for _, l := range links {
		if l.Object == bug.ID {
			a.call("PUT", fmt.Sprintf("/api/links/%d/query", l.ID), map[string]any{"query": linkQuery{State: "fixed"}}, http.StatusNoContent, nil)
		}
	}

	var offered struct {
		Flows []struct {
			ID  int64             `json:"id"`
			Off map[string]string `json:"off"`
		} `json:"flows"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records/flows?records=%d", s.ID, record), nil, http.StatusOK, &offered)
	i := slices.IndexFunc(offered.Flows, func(o struct {
		ID  int64             `json:"id"`
		Off map[string]string `json:"off"`
	}) bool {
		return o.ID == f.ID
	})
	if i < 0 || offered.Flows[i].Off[fmt.Sprint(record)] == "" {
		t.Errorf("the flows offered = %+v, want this one saying its published query wouldn't select the fixed bug", offered.Flows)
	}

	var rs []run
	a.call("POST", fmt.Sprintf("/api/structs/%d/records/runs", s.ID), map[string]any{"flow": f.ID, "records": []int64{record}}, http.StatusCreated, &rs)
	if d := a.finish(rs[0].ID); d.Status != runDone || *d.Version != pub.ID {
		t.Errorf("the run on the record %s on version %v; steps %s", d.Status, show(d.Version), describeSteps(d))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(scripts) != 1 || scripts[0] != "fix published" {
		t.Errorf("the run on the record ran %q", scripts)
	}
}

// A run of a version published runs the version a procedure published, if
// it has one, and its board as it is otherwise; a run of the board runs the
// procedure's board as it is.
func TestPublishedRunsTakeProceduresPublished(t *testing.T) {
	a := newApp(t)
	s := verBuildSubflow(a)
	var prompts pubPrompts
	a.worker.onAgent("Fix", prompts.agent(data("the fix")))
	k := apiNewKey(a, "fixing")
	pubPublish(a, s.caller.ID, "")

	// The procedure never published runs as it is.
	verPatch(a, fmt.Sprintf("/api/processes/%d", s.fix.ID), map[string]any{"prompt": "Fix it, take one."})
	a.finish(apiStart(a, k.Key, s.caller.ID, nil, http.StatusCreated).ID)
	prompts.latest(t, "Fix it, take one.")

	callee := pubPublish(a, s.callee.ID, "")
	verPatch(a, fmt.Sprintf("/api/processes/%d", s.fix.ID), map[string]any{"prompt": "Fix it, take two."})
	d := a.finish(apiStart(a, k.Key, s.caller.ID, nil, http.StatusCreated).ID)
	prompts.latest(t, "Fix it, take one.")
	if child := a.getRun(*onlyStep(t, d, "Call").ChildRun); *child.Version != callee.ID {
		t.Errorf("the procedure's run was on version %d, want %d, published", *child.Version, callee.ID)
	}

	// The board run by hand tries the procedure's draft with it.
	a.finish(a.startRun(s.caller.ID).ID)
	prompts.latest(t, "Fix it, take two.")
}

// Publishing doesn't change the board, publishing an older version rolls
// back to it, and bringing the board back to a version leaves what is
// published.
func TestPublishingLeavesTheBoard(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Rollback")
	write := a.agent(f.ID, "Write", "First.")
	a.output(write.ID, a.newObject(f.ID, "Text").ID)
	a.startAt(f.ID, write.ID)
	var prompts pubPrompts
	a.worker.onAgent("Write", prompts.agent(data("text")))
	k := apiNewKey(a, "rollback")

	first := pubPublish(a, f.ID, "First")
	verPatch(a, fmt.Sprintf("/api/processes/%d", write.ID), map[string]any{"prompt": "Second."})
	second := pubPublish(a, f.ID, "Second")
	if second.ID == first.ID {
		t.Fatal("publishing a changed board kept its version")
	}
	// Unchanged, publishing again keeps the version, which takes the note
	// unless it has one.
	if again := pubPublish(a, f.ID, "Again"); again.ID != second.ID || again.Note != "Second" {
		t.Errorf("publishing unchanged gave %+v", again)
	}

	verPatch(a, fmt.Sprintf("/api/processes/%d", write.ID), map[string]any{"prompt": "Third, a draft."})
	board := verDump(a, f.ID)
	a.call("POST", fmt.Sprintf("/api/versions/%d/publish", first.ID), nil, http.StatusOK, nil)
	verSameBoard(t, board, verDump(a, f.ID))
	if l := pubList(a, f.ID); *l.Published != first.ID || !l.Draft {
		t.Errorf("rolled back, versions = %+v", l)
	}
	a.finish(apiStart(a, k.Key, f.ID, nil, http.StatusCreated).ID)
	prompts.latest(t, "First.")

	verRestore(a, second.ID)
	if l := pubList(a, f.ID); *l.Published != first.ID {
		t.Errorf("restoring published %v", show(l.Published))
	}
	a.finish(apiStart(a, k.Key, f.ID, nil, http.StatusCreated).ID)
	prompts.latest(t, "First.")

	// A struct has nothing to run.
	var st flow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprintf("Struct %d", time.Now().UnixNano()), "struct": true}, http.StatusCreated, &st)
	a.refused("POST", fmt.Sprintf("/api/versions?flow=%d", st.ID), map[string]any{"publish": true}, http.StatusBadRequest, "a struct")
	a.refused("POST", "/api/versions/999999999/publish", nil, http.StatusNotFound, "version not found")
}

// A run of the version published doesn't hold the board, its draft, which
// can be brought back to a version and undone while the run waits; a run of
// the board holds it as before. A review in the run published is decided on
// the states of its version.
func TestPublishedRunsDontHoldTheBoard(t *testing.T) {
	a := newApp(t)
	b := humanNewReviewBoard(a)
	k := apiNewKey(a, "reviews")
	saved := verSave(a, b.flow.ID)
	pubPublish(a, b.flow.ID, "")
	rn := apiStart(a, k.Key, b.flow.ID, nil, http.StatusCreated)
	review := a.waitStep(rn.ID, "Review", stepWaiting)

	// The draft renames the approved state and drops Publish.
	verPatch(a, fmt.Sprintf("/api/states/%d", b.approved.ID), map[string]any{"name": "fine"})
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", b.publish.ID), nil, http.StatusNoContent, nil)
	verRestore(a, saved.ID)
	a.call("POST", fmt.Sprintf("/api/history/undo?flow=%d", b.flow.ID), nil, http.StatusOK, nil)

	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	d := a.finish(rn.ID)
	if d.Status != runDone || len(stepsOf(d, "Publish")) != 1 {
		t.Errorf("the run published %s; steps %s", d.Status, describeSteps(d))
	}
	if got := onlyStep(t, d, "Review"); got.Outcome != "approved" {
		t.Errorf("the review's outcome = %q, want the version's approved", got.Outcome)
	}

	// A run of the board holds it.
	verPatch(a, fmt.Sprintf("/api/processes/%d", b.draft.ID), map[string]any{"prompt": "Write them well."})
	drafted := a.startRun(b.flow.ID)
	a.waitStep(drafted.ID, "Review", stepWaiting)
	a.refused("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusConflict, "while the flow is running")
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", drafted.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
	a.finish(drafted.ID)
}

// Versions keep the note a person saves them with, and say what saved them
// on their own.
func TestVersionNotes(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Noted")
	write := a.agent(f.ID, "Write", "Write.")
	a.output(write.ID, a.newObject(f.ID, "Text").ID)
	a.startAt(f.ID, write.ID)
	a.worker.onAgent("Write", handsIn(data("text")))

	var v version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), map[string]any{"note": "  Before the rewrite  "}, http.StatusCreated, &v)
	if v.Note != "Before the rewrite" || v.Reason != "" {
		t.Errorf("saved %+v", v)
	}
	a.refused("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), map[string]any{"note": strings.Repeat("x", 2001)}, http.StatusBadRequest, "at most")
	a.refused("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), "{", http.StatusBadRequest, "invalid JSON")

	verPatch(a, fmt.Sprintf("/api/processes/%d", write.ID), map[string]any{"prompt": "Write more."})
	a.finish(a.startRun(f.ID).ID)
	l := pubList(a, f.ID)
	if len(l.Versions) != 2 || l.Versions[0].Reason != "run" || l.Versions[1].Note != "Before the rewrite" {
		t.Errorf("the versions = %+v", l.Versions)
	}

	// A setup file imported over the flow saves it first.
	verPatch(a, fmt.Sprintf("/api/processes/%d", write.ID), map[string]any{"prompt": "Write less."})
	setup := fmt.Sprintf(`{"spoletta": 1, "flows": [{"name": %q, "start": {"next": "Write"}, "processes": [{"name": "Write", "prompt": "Imported.", "yields": ["Text"]}], "objects": [{"name": "Text"}]}]}`, f.Name)
	status, reply := a.do("POST", "/api/setup", json.RawMessage(setup))
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("importing = %d %s", status, reply)
	}
	if l := pubList(a, f.ID); l.Versions[0].Reason != "import" {
		t.Errorf("after importing, the versions = %+v", l.Versions)
	}
}

// What changed from a version to the board as it is, or to another version,
// worked out on the server; a version of another flow can't be compared.
func TestVersionDiffs(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Compared")
	write := a.agent(f.ID, "Write", "Write it.")
	text := a.newObject(f.ID, "Text")
	a.output(write.ID, text.ID)
	a.startAt(f.ID, write.ID)
	first := verSave(a, f.ID)

	verPatch(a, fmt.Sprintf("/api/processes/%d", write.ID), map[string]any{"prompt": "Write it well.", "x": 500})
	a.newState(text.ID, "checked")
	check := a.agent(f.ID, "Check", "Check it.")
	a.input(check.ID, text.ID)
	settings := map[string]any{"repositories": []map[string]any{{"name": "site", "url": "https://github.com/example/site"}}}
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": settings}, http.StatusOK, nil)

	d := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", first.ID))
	if got := pubNames(d.Processes); !slices.Equal(got, []string{"changed Write", "added Check"}) {
		t.Errorf("processes = %v", got)
	}
	if f := d.Processes[0].Fields; len(f) != 1 || f[0].Field != "prompt" || f[0].Before != "Write it." || f[0].After != "Write it well." {
		t.Errorf("Write's fields = %+v", f)
	}
	if got := pubNames(d.States); !slices.Equal(got, []string{"added Text: checked"}) {
		t.Errorf("states = %v", got)
	}
	if got := pubNames(d.Links); !slices.Equal(got, []string{"added Text → Check"}) {
		t.Errorf("links = %v", got)
	}
	if len(d.Settings) != 1 || d.Settings[0].Field != "repositories" || !d.Layout || d.To != nil {
		t.Errorf("settings %+v, layout %v, to %v", d.Settings, d.Layout, show(d.To))
	}

	second := verSave(a, f.ID)
	between := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff?to=%d", first.ID, second.ID))
	if !slices.Equal(pubNames(between.Processes), pubNames(d.Processes)) || between.To == nil || *between.To != second.ID {
		t.Errorf("between the versions, processes = %v", pubNames(between.Processes))
	}
	if same := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", second.ID)); len(same.Processes)+len(same.Links)+len(same.States)+len(same.Settings) != 0 {
		t.Errorf("the board against the version it is on differs in %+v", same)
	}

	other := a.newFlow("Other")
	a.agent(other.ID, "Else", "Something else.")
	elsewhere := verSave(a, other.ID)
	a.refused("GET", fmt.Sprintf("/api/versions/%d/diff?to=%d", first.ID, elsewhere.ID), nil, http.StatusBadRequest, "different flows")
	a.refused("GET", "/api/versions/999999999/diff", nil, http.StatusNotFound, "version not found")
	a.refused("GET", fmt.Sprintf("/api/versions/%d/diff?to=x", first.ID), nil, http.StatusBadRequest, "invalid version")
}

// A board a setup file made over by name is compared by name where its ids
// changed: a process renamed in the file is new, the rest the same.
func TestVersionDiffsAfterImport(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprintf("Imported %d", time.Now().UnixNano())
	setup := func(processes string) {
		t.Helper()
		file := fmt.Sprintf(`{"spoletta": 1, "flows": [{"name": %q, "start": {"next": "Write"}, "processes": [%s], "objects": [{"name": "Text"}]}]}`, name, processes)
		if status, reply := a.do("POST", "/api/setup", json.RawMessage(file)); status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("importing = %d %s", status, reply)
		}
	}
	setup(`{"name": "Write", "prompt": "Write.", "yields": ["Text"], "next": ["Send"]}, {"name": "Send", "prompt": "Send.", "takes": ["Text"]}`)
	var flows []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
	i := slices.IndexFunc(flows, func(f flow) bool { return f.Name == name })
	if i < 0 {
		t.Fatalf("no flow %s", name)
	}
	before := verSave(a, flows[i].ID)
	setup(`{"name": "Write", "prompt": "Write better.", "yields": ["Text"], "next": ["Post"]}, {"name": "Post", "prompt": "Send.", "takes": ["Text"]}`)
	d := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", before.ID))
	if got := pubNames(d.Processes); !slices.Equal(got, []string{"changed Write", "added Post", "removed Send"}) {
		t.Errorf("processes = %v", got)
	}
	if got := pubNames(d.Objects); len(got) != 0 {
		t.Errorf("objects = %v, want Text unchanged", got)
	}
}

// After the assistant changed the flow, its reply names the version saved
// before, which the board compares with to show what it changed.
func TestAssistantChangesShowAgainstTheVersionBefore(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprintf("Assisted %d", time.Now().UnixNano())
	f := a.newFlow(name)
	a.agent(f.ID, "Old", "Do the old thing.")
	a.worker.onAgent("Assistant", func(context.Context, agentRun) runResult {
		return replyWithSetup("I rebuilt it.", name, jiraProcesses)
	})
	askAssistant(a, f.ID, "Rebuild it")
	reply := assistantReply(a, f.ID)
	var msgs []struct {
		Applied bool   `json:"applied"`
		Version *int64 `json:"version"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/assistant", f.ID), nil, http.StatusOK, &msgs)
	last := msgs[len(msgs)-1]
	if !reply.Applied || last.Version == nil {
		t.Fatalf("the reply = %+v, version %v", reply, show(last.Version))
	}
	l := pubList(a, f.ID)
	if len(l.Versions) != 1 || l.Versions[0].ID != *last.Version || l.Versions[0].Reason != "assistant" {
		t.Errorf("the versions = %+v, want the one the assistant saved", l.Versions)
	}
	d := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", *last.Version))
	if got := pubNames(d.Processes); !slices.Equal(got, []string{"added Fetch", "added Summarize", "removed Old"}) {
		t.Errorf("what the assistant changed = %v", got)
	}
}
