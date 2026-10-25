package tests

// The run engine on boards inside boards: flow processes, the lists they
// run once per item of, and the persistent objects runs store.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A flow process on the flow's top board.
func nestedFlowProcess(a *app, f int64, name string, recording bool) process {
	a.t.Helper()
	return a.newProcess(f, map[string]any{"name": name, "kind": kindFlow, "recording": recording})
}

// A process on the board of the flow process parent.
func nestedProcess(a *app, f, parent int64, name, kind string) process {
	a.t.Helper()
	return a.newProcess(f, map[string]any{"name": name, "kind": kind, "prompt": name + " it.", "parent": parent})
}

func nestedAgent(a *app, f, parent int64, name string) process {
	a.t.Helper()
	return nestedProcess(a, f, parent, name, kindAgent)
}

func nestedObject(a *app, parent int64, name string) object {
	a.t.Helper()
	var o object
	a.call("POST", "/api/objects", map[string]any{"name": name, "parent": parent}, http.StatusCreated, &o)
	return o
}

// Points the start of the flow process board's board at p.
func nestedStartAt(a *app, board, p int64) {
	a.t.Helper()
	a.call("PATCH", fmt.Sprintf("/api/start?board=%d", board), map[string]any{"target": p}, http.StatusOK, nil)
}

func nestedPatchObject(a *app, o int64, patch map[string]any) object {
	a.t.Helper()
	var out object
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", o), patch, http.StatusOK, &out)
	return out
}

// Makes o a list of at least min items, and at most max unless it is negative.
func nestedList(a *app, o int64, min, max int) {
	a.t.Helper()
	patch := map[string]any{"many": true, "min": min, "max": nil}
	if max >= 0 {
		patch["max"] = max
	}
	nestedPatchObject(a, o, patch)
}

func nestedAttribute(a *app, o int64, name string, unique bool) attribute {
	a.t.Helper()
	var at attribute
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", o), map[string]any{"name": name, "definition": "Its " + name}, http.StatusCreated, &at)
	if unique {
		a.call("PATCH", fmt.Sprintf("/api/attributes/%d", at.ID), map[string]any{"unique": true}, http.StatusOK, &at)
	}
	return at
}

func nestedRuns(a *app, f int64) []run {
	a.t.Helper()
	var rs []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d", f), nil, http.StatusOK, &rs)
	return rs
}

// The runs of the flow inside run parent, oldest first.
func nestedChildren(a *app, f, parent int64) []run {
	a.t.Helper()
	var cs []run
	for _, r := range nestedRuns(a, f) {
		if r.Parent != nil && *r.Parent == parent {
			cs = append(cs, r)
		}
	}
	slices.Reverse(cs)
	return cs
}

func nestedItemKeys(rs []run) []string {
	var keys []string
	for _, r := range rs {
		keys = append(keys, r.ItemKey)
	}
	slices.Sort(keys)
	return keys
}

// The objects the run recorded of the step.
func nestedObjectsOf(d runDetail, stepID int64) []runObject {
	var os []runObject
	for _, o := range d.Objects {
		if o.Step == stepID {
			os = append(os, o)
		}
	}
	return os
}

// A run that must end as want.
func nestedFinish(t *testing.T, a *app, id int64, want string) runDetail {
	t.Helper()
	d := a.finish(id)
	if d.Status != want {
		t.Fatalf("run %d %s, want %s; steps %s", id, d.Status, want, describeSteps(d))
	}
	return d
}

// An agent handing in data(prefix + what tells its input apart), for agents
// running once per item.
func nestedEcho(prefix string) func(context.Context, agentRun) runResult {
	return func(_ context.Context, run agentRun) runResult {
		for _, key := range []string{"A", "B", "C", "D"} {
			if strings.Contains(run.Prompt, `"title": "`+key+`"`) {
				return submitted(data(prefix + key))
			}
		}
		return runResult{Error: "no item in the prompt"}
	}
}

// Write generates a Ticket that the flow process Summarize takes, whose
// board reads it and summarizes it into the Summary that Publish takes.
type nestedTriage struct {
	flow                   flow
	write, fp, publish     process
	read, sum              process
	ticket, summary, notes object
}

func newNestedTriage(t *testing.T, a *app, recording bool) nestedTriage {
	t.Helper()
	var n nestedTriage
	n.flow = a.newFlow("Nested")
	f := n.flow.ID
	n.write = a.agent(f, "Write", "Write a ticket.")
	n.ticket = a.newObject(f, "Ticket")
	n.fp = nestedFlowProcess(a, f, "Summarize", recording)
	n.summary = a.newObject(f, "Summary")
	n.publish = a.agent(f, "Publish", "Publish it.")
	a.output(n.write.ID, n.ticket.ID)
	a.input(n.fp.ID, n.ticket.ID)
	a.output(n.fp.ID, n.summary.ID)
	a.input(n.publish.ID, n.summary.ID)
	a.startAt(f, n.write.ID)

	// Read isn't linked to the ticket: the start leads it in.
	n.read = nestedAgent(a, f, n.fp.ID, "Read")
	n.notes = nestedObject(a, n.fp.ID, "Notes")
	n.sum = nestedAgent(a, f, n.fp.ID, "Sum")
	a.output(n.read.ID, n.notes.ID)
	a.input(n.sum.ID, n.notes.ID)
	a.output(n.sum.ID, n.summary.ID)
	nestedStartAt(a, n.fp.ID, n.read.ID)

	a.worker.onAgent("Write", handsIn(data("login crashes")))
	a.worker.onAgent("Read", handsIn(data("notes on the crash")))
	a.worker.onAgent("Sum", handsIn(data("a summary")))
	a.worker.onAgent("Publish", handsIn(data("posted")))
	return n
}

// A flow process runs its board in the run it is in: what it takes reaches
// its start's process, and what the processes inside generate of what it
// generates comes out, and only that.
func TestFlowProcessRunsItsBoardInTheRun(t *testing.T) {
	a := newApp(t)
	n := newNestedTriage(t, a, false)

	// A process inside can only be linked to what the flow process is.
	other := a.newObject(n.flow.ID, "Unrelated")
	a.refused("POST", "/api/links", map[string]any{"process": n.sum.ID, "object": other.ID, "direction": directionInput}, http.StatusBadRequest, "isn't on the process's board")
	a.refused("PATCH", fmt.Sprintf("/api/start?board=%d", n.fp.ID), map[string]any{"target": n.write.ID}, http.StatusBadRequest, "not found on that board")

	rn := a.startRun(n.flow.ID)
	d := nestedFinish(t, a, rn.ID, runDone)

	for _, name := range []string{"Write", "Summarize", "Read", "Sum", "Publish"} {
		if s := onlyStep(t, d, name); s.Status != stepDone {
			t.Errorf("%s step %s %q", name, s.Status, s.Error)
		}
	}
	if rs := nestedRuns(a, n.flow.ID); len(rs) != 1 {
		t.Errorf("%d runs of the flow, want only the one: a flow process that doesn't record runs in it", len(rs))
	}
	fs := onlyStep(t, d, "Summarize")
	if fs.ChildRun != nil {
		t.Errorf("the flow process's step refers to run %d", *fs.ChildRun)
	}
	if len(fs.Inputs) != 1 || fs.Inputs[0].Name != "Ticket" {
		t.Errorf("Summarize took %+v", fs.Inputs)
	}
	if rs := a.worker.runsOf("Read"); len(rs) != 1 || !strings.Contains(rs[0].Prompt, "login crashes") {
		t.Errorf("Read, at the start, got %+v; want the ticket", rs)
	}
	if rs := a.worker.runsOf("Sum"); len(rs) != 1 || !strings.Contains(rs[0].Prompt, "notes on the crash") || strings.Contains(rs[0].Prompt, "login crashes") {
		t.Errorf("Sum got %+v; want only the notes", rs)
	}
	var names []string
	for _, o := range nestedObjectsOf(d, fs.ID) {
		names = append(names, o.Name)
		if !strings.Contains(o.Content, "a summary") {
			t.Errorf("%s came out holding %q", o.Name, o.Content)
		}
	}
	if !slices.Equal(names, []string{"Summary"}) {
		t.Errorf("out of Summarize came %v, want only Summary, not the notes inside", names)
	}
	ps := onlyStep(t, d, "Publish")
	if len(ps.Inputs) != 1 || !strings.Contains(ps.Inputs[0].Content, "a summary") {
		t.Errorf("Publish took %+v", ps.Inputs)
	}
}

// An object a flow process generates in a state comes out in that state,
// unless the process inside generated it in one of its own.
func TestFlowProcessOutputState(t *testing.T) {
	for _, c := range []struct {
		name string
		// The state the process inside generates it in, if any.
		inside string
		state  string
		ran    string
	}{
		{"takes the flow process's", "", "ready", "Ship"},
		{"keeps the one inside", "draft", "draft", "Hold"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("States").ID
			fp := nestedFlowProcess(a, f, "Prepare", false)
			out := a.newObject(f, "Release")
			ready := a.newState(out.ID, "ready")
			draft := a.newState(out.ID, "draft")
			ship := a.agent(f, "Ship", "Ship it.")
			hold := a.agent(f, "Hold", "Hold it.")
			a.link(map[string]any{"process": fp.ID, "object": out.ID, "direction": directionOutput, "state": ready.ID})
			a.link(map[string]any{"process": ship.ID, "object": out.ID, "direction": directionInput, "state": ready.ID})
			a.link(map[string]any{"process": hold.ID, "object": out.ID, "direction": directionInput, "state": draft.ID})
			a.startAt(f, fp.ID)
			make := nestedAgent(a, f, fp.ID, "Make")
			l := map[string]any{"process": make.ID, "object": out.ID, "direction": directionOutput}
			if c.inside != "" {
				l["state"] = draft.ID
			}
			a.link(l)
			nestedStartAt(a, fp.ID, make.ID)
			for _, p := range []string{"Make", "Ship", "Hold"} {
				a.worker.onAgent(p, handsIn(data(p)))
			}

			d := nestedFinish(t, a, a.startRun(f).ID, runDone)
			fs := onlyStep(t, d, "Prepare")
			if os := nestedObjectsOf(d, fs.ID); len(os) != 1 || os[0].State != c.state {
				t.Errorf("Release came out as %+v, want in state %s", os, c.state)
			}
			for _, p := range []string{"Ship", "Hold"} {
				want := 0
				if p == c.ran {
					want = 1
				}
				if n := len(stepsOf(d, p)); n != want {
					t.Errorf("%s ran %d times, want %d", p, n, want)
				}
			}
		})
	}
}

// A process inside failing fails the flow process and the run; nothing
// comes out of it.
func TestFailureInsideAFlowProcess(t *testing.T) {
	for _, recording := range []bool{false, true} {
		t.Run(fmt.Sprintf("recording %v", recording), func(t *testing.T) {
			a := newApp(t)
			n := newNestedTriage(t, a, recording)
			a.worker.onAgent("Sum", func(context.Context, agentRun) runResult { return runResult{Error: "no model"} })

			rn := a.startRun(n.flow.ID)
			d := nestedFinish(t, a, rn.ID, runFailed)
			fs := onlyStep(t, d, "Summarize")
			want := "A process inside failed."
			if recording {
				if fs.ChildRun == nil {
					t.Fatal("the recording flow process's step refers to no run")
				}
				want = fmt.Sprintf("Run #%d failed.", *fs.ChildRun)
				child := a.getRun(*fs.ChildRun)
				if child.Status != runFailed || onlyStep(t, child, "Sum").Error != "no model" {
					t.Errorf("its run %s with steps %s", child.Status, describeSteps(child))
				}
			} else if s := onlyStep(t, d, "Sum"); s.Error != "no model" {
				t.Errorf("Sum's step error = %q", s.Error)
			}
			if fs.Status != stepError || fs.Error != want {
				t.Errorf("Summarize step %s %q, want error %q", fs.Status, fs.Error, want)
			}
			if len(nestedObjectsOf(d, fs.ID)) != 0 || len(stepsOf(d, "Publish")) != 0 {
				t.Errorf("something came out of the failed flow process; steps %s", describeSteps(d))
			}
		})
	}
}

// A recording flow process runs its board as a run of its own, in the run
// it is in, whose steps are that run's.
func TestRecordingFlowProcessRunsAsItsOwnRun(t *testing.T) {
	a := newApp(t)
	n := newNestedTriage(t, a, true)

	rn := a.startRun(n.flow.ID)
	d := nestedFinish(t, a, rn.ID, runDone)
	fs := onlyStep(t, d, "Summarize")
	if fs.Status != stepDone || fs.ChildRun == nil {
		t.Fatalf("Summarize step %s with run %v", fs.Status, fs.ChildRun)
	}
	for _, name := range []string{"Read", "Sum"} {
		if len(stepsOf(d, name)) != 0 {
			t.Errorf("%s ran as a step of the outer run", name)
		}
	}
	child := a.getRun(*fs.ChildRun)
	if child.Flow != n.flow.ID || child.Parent == nil || *child.Parent != rn.ID || child.Process == nil || *child.Process != n.fp.ID ||
		child.ProcessName != "Summarize" || child.ItemKey != "" || child.Status != runDone || child.Version == nil || *child.Version != *rn.Version {
		t.Errorf("its run = %+v", child.run)
	}
	onlyStep(t, child, "Read")
	onlyStep(t, child, "Sum")
	if cs := nestedChildren(a, n.flow.ID, rn.ID); len(cs) != 1 || cs[0].ID != child.ID {
		t.Errorf("runs in run %d = %+v", rn.ID, cs)
	}
	// The flow's last run is the whole flow's, not the flow process's.
	var f flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", n.flow.ID), nil, http.StatusOK, &f)
	if f.LastRun == nil || f.LastRun.ID != rn.ID {
		t.Errorf("the flow's last run = %+v, want %d", f.LastRun, rn.ID)
	}
	if ps := onlyStep(t, d, "Publish"); len(ps.Inputs) != 1 || !strings.Contains(ps.Inputs[0].Content, "a summary") {
		t.Errorf("Publish took %+v", ps.Inputs)
	}
}

// A process inside generating a value another run stored terminates the
// run inside: for a recording flow process only its own run, so the rest of
// the outer run goes on; otherwise the outer run.
func TestDuplicateInsideAFlowProcess(t *testing.T) {
	for _, recording := range []bool{false, true} {
		t.Run(fmt.Sprintf("recording %v", recording), func(t *testing.T) {
			a := newApp(t)
			n := newNestedTriage(t, a, recording)
			nestedPatchObject(a, n.summary.ID, map[string]any{"persistent": true})
			nestedAttribute(a, n.summary.ID, "id", true)
			a.worker.onAgent("Sum", handsIn(map[string]any{"id": "E1"}))
			// Another branch taking the ticket.
			side := a.agent(n.flow.ID, "Side", "Side it.")
			a.input(side.ID, n.ticket.ID)
			release := make(chan struct{})
			a.worker.onAgent("Side", func(ctx context.Context, _ agentRun) runResult {
				select {
				case <-release:
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
				return runResult{OK: true}
			})

			close(release)
			first := a.startRun(n.flow.ID)
			nestedFinish(t, a, first.ID, runDone)

			// Side goes on only once the duplicate was found, so it is still
			// going when the run inside is terminated.
			release = make(chan struct{})
			second := a.startRun(n.flow.ID)
			fs := a.waitStep(second.ID, "Summarize", stepDuplicate)
			close(release)
			want := runTerminated
			if recording {
				want = runDone
			}
			d := nestedFinish(t, a, second.ID, want)
			which := "A process inside"
			if recording {
				which = fmt.Sprintf("Run #%d", *fs.ChildRun)
				child := a.getRun(*fs.ChildRun)
				if child.Status != runTerminated {
					t.Errorf("its run %s, want terminated", child.Status)
				}
				if s := onlyStep(t, child, "Sum"); s.Status != stepDuplicate || !strings.Contains(s.Error, fmt.Sprintf("already stored by run #%d", nestedChildren(a, n.flow.ID, first.ID)[0].ID)) {
					t.Errorf("Sum step %s %q", s.Status, s.Error)
				}
				if s := onlyStep(t, d, "Side"); s.Status != stepDone {
					t.Errorf("Side, on another branch, %s %q", s.Status, s.Error)
				}
			} else if s := onlyStep(t, d, "Sum"); s.Status != stepDuplicate || !strings.Contains(s.Error, fmt.Sprintf("already stored by run #%d", first.ID)) {
				t.Errorf("Sum step %s %q", s.Status, s.Error)
			}
			if fs.Error != which+" found what it generated already processed." {
				t.Errorf("Summarize step error = %q", fs.Error)
			}
			if len(stepsOf(d, "Publish")) != 0 {
				t.Error("Publish took what a duplicate generated")
			}
		})
	}
}

// A flow process inside a flow process runs its board too, all as steps of
// the one run.
func TestNestedFlowProcesses(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Nesting").ID
	write := a.agent(f, "Write", "Write a ticket.")
	ticket := a.newObject(f, "Ticket")
	outer := nestedFlowProcess(a, f, "Outer", false)
	result := a.newObject(f, "Result")
	publish := a.agent(f, "Publish", "Publish it.")
	a.output(write.ID, ticket.ID)
	a.input(outer.ID, ticket.ID)
	a.output(outer.ID, result.ID)
	a.input(publish.ID, result.ID)
	a.startAt(f, write.ID)

	inner := nestedProcess(a, f, outer.ID, "Inner", kindFlow)
	a.input(inner.ID, ticket.ID)
	a.output(inner.ID, result.ID)
	nestedStartAt(a, outer.ID, inner.ID)
	deep := nestedAgent(a, f, inner.ID, "Deep")
	a.input(deep.ID, ticket.ID)
	a.output(deep.ID, result.ID)
	nestedStartAt(a, inner.ID, deep.ID)

	a.worker.onAgent("Write", handsIn(data("the ticket")))
	a.worker.onAgent("Deep", handsIn(data("deep result")))
	a.worker.onAgent("Publish", handsIn(data("posted")))

	d := nestedFinish(t, a, a.startRun(f).ID, runDone)
	for _, name := range []string{"Write", "Outer", "Inner", "Deep", "Publish"} {
		if s := onlyStep(t, d, name); s.Status != stepDone {
			t.Errorf("%s step %s %q", name, s.Status, s.Error)
		}
	}
	if rs := a.worker.runsOf("Deep"); len(rs) != 1 || !strings.Contains(rs[0].Prompt, "the ticket") {
		t.Errorf("Deep got %+v, want the ticket from two boards up", rs)
	}
	if ps := onlyStep(t, d, "Publish"); len(ps.Inputs) != 1 || !strings.Contains(ps.Inputs[0].Content, "deep result") {
		t.Errorf("Publish took %+v, want what came out from two boards down", ps.Inputs)
	}
}

// Find generates the list Issues, each with a title, that the flow process
// Handle runs once per item of; its Fix generates the list Fixes, one per
// item, which Report takes.
type nestedLists struct {
	flow                  flow
	find, fp, fix, report process
	issues, fixes         object
	title                 attribute
}

func newNestedLists(t *testing.T, a *app, recording, unique bool) nestedLists {
	t.Helper()
	var n nestedLists
	n.flow = a.newFlow("Lists")
	f := n.flow.ID
	n.find = a.agent(f, "Find", "Find the issues.")
	n.issues = a.newObject(f, "Issues")
	nestedList(a, n.issues.ID, 0, -1)
	n.title = nestedAttribute(a, n.issues.ID, "title", unique)
	n.fp = nestedFlowProcess(a, f, "Handle", recording)
	n.fixes = a.newObject(f, "Fixes")
	nestedList(a, n.fixes.ID, 0, -1)
	n.report = a.agent(f, "Report", "Report the fixes.")
	a.output(n.find.ID, n.issues.ID)
	a.input(n.fp.ID, n.issues.ID)
	a.output(n.fp.ID, n.fixes.ID)
	a.input(n.report.ID, n.fixes.ID)
	a.startAt(f, n.find.ID)
	n.fix = nestedAgent(a, f, n.fp.ID, "Fix")
	a.input(n.fix.ID, n.issues.ID)
	a.output(n.fix.ID, n.fixes.ID)
	nestedStartAt(a, n.fp.ID, n.fix.ID)

	a.worker.onAgent("Fix", nestedEcho("fixed "))
	a.worker.onAgent("Report", handsIn(data("reported")))
	return n
}

func nestedTitles(keys ...string) []map[string]any {
	items := []map[string]any{}
	for _, k := range keys {
		items = append(items, map[string]any{"title": k})
	}
	return items
}

// What a process generates of a list is a JSON array of as many items as the
// list says; one that isn't fails its step after one more try.
func TestListMustHoldItsQuantity(t *testing.T) {
	for _, c := range []struct {
		name    string
		replies []any
		status  string
		err     string
	}{
		{"in bounds", []any{nestedTitles("A", "B")}, runDone, ""},
		{"too many, then in bounds", []any{nestedTitles("A", "B", "C", "D"), nestedTitles("A", "B", "C")}, runDone, ""},
		{"too few twice", []any{nestedTitles("A"), nestedTitles("A")}, runFailed, "it has 1 item; it must have between 2 and 3"},
		{"no list", []any{map[string]any{"title": "A"}, "A"}, runFailed, "not a JSON array"},
		{"an item without its fields", []any{[]any{map[string]any{"title": "A"}, map[string]any{"name": "B"}}, []any{map[string]any{"title": "A"}, map[string]any{"name": "B"}}}, runFailed, "item 2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Quantity").ID
			find := a.agent(f, "Find", "Find the issues.")
			issues := a.newObject(f, "Issues")
			nestedList(a, issues.ID, 2, 3)
			nestedAttribute(a, issues.ID, "title", false)
			a.output(find.ID, issues.ID)
			a.startAt(f, find.ID)
			var mu sync.Mutex
			tries := 0
			a.worker.onAgent("Find", func(context.Context, agentRun) runResult {
				mu.Lock()
				defer mu.Unlock()
				tries++
				return submitted(c.replies[tries-1])
			})

			d := nestedFinish(t, a, a.startRun(f).ID, c.status)
			runs := a.worker.runsOf("Find")
			if len(runs) != len(c.replies) {
				t.Fatalf("Find ran %d times, want %d", len(runs), len(c.replies))
			}
			if !strings.Contains(runs[0].Prompt, "between 2 and 3") {
				t.Errorf("Find wasn't told how many items to generate:\n%s", runs[0].Prompt)
			}
			s := onlyStep(t, d, "Find")
			if c.err != "" && !strings.Contains(s.Error, c.err) {
				t.Errorf("Find step error = %q, want %q", s.Error, c.err)
			}
			if c.status == runDone {
				if len(runs) == 2 && !strings.Contains(runs[1].Prompt, "it has 4 items") {
					t.Errorf("the second try wasn't told what was wrong:\n%s", runs[1].Prompt)
				}
				if got := contentsOf(d, "Issues"); len(got) != 1 || !strings.HasPrefix(got[0], "[") {
					t.Errorf("Issues held %q", got)
				}
			}
		})
	}
}

// A flow process taking a list runs its board once per item, each with its
// item, and gathers what they generate into its lists in the items' order.
func TestFlowProcessRunsOncePerItem(t *testing.T) {
	a := newApp(t)
	n := newNestedLists(t, a, false, false)
	a.worker.onAgent("Find", handsIn(nestedTitles("A", "B", "C")))
	// A finishes last, yet comes first.
	others := sync.WaitGroup{}
	others.Add(2)
	a.worker.onAgent("Fix", func(ctx context.Context, run agentRun) runResult {
		if strings.Contains(run.Prompt, `"title": "A"`) {
			done := make(chan struct{})
			go func() { others.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		} else {
			defer others.Done()
		}
		return nestedEcho("fixed ")(ctx, run)
	})

	d := nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)
	if n := len(stepsOf(d, "Handle")); n != 3 {
		t.Errorf("Handle has %d steps, want one per item", n)
	}
	var prompts []string
	for _, r := range a.worker.runsOf("Fix") {
		prompts = append(prompts, r.Prompt)
	}
	for _, k := range []string{"A", "B", "C"} {
		if n := len(slices.DeleteFunc(slices.Clone(prompts), func(p string) bool { return !strings.Contains(p, `"title": "`+k+`"`) })); n != 1 {
			t.Errorf("Fix got item %s %d times", k, n)
		}
	}
	for _, p := range prompts {
		if strings.Contains(p, "[") {
			t.Errorf("Fix got the whole list rather than an item:\n%s", p)
		}
	}
	rp := onlyStep(t, d, "Report")
	if len(rp.Inputs) != 1 {
		t.Fatalf("Report took %+v", rp.Inputs)
	}
	got := rp.Inputs[0].Content
	a1, b1, c1 := strings.Index(got, "fixed A"), strings.Index(got, "fixed B"), strings.Index(got, "fixed C")
	if !strings.HasPrefix(got, "[") || a1 < 0 || !(a1 < b1 && b1 < c1) {
		t.Errorf("Report took Fixes as %q, want fixes of A, B and C in order", got)
	}
}

// What tells a recording flow process's item runs apart: the list's first
// unique attribute's value, or else the item's place.
func TestItemRunsAreKeyed(t *testing.T) {
	for _, c := range []struct {
		unique bool
		keys   []string
	}{
		{true, []string{"A", "B"}},
		{false, []string{"item 1", "item 2"}},
	} {
		t.Run(fmt.Sprintf("unique %v", c.unique), func(t *testing.T) {
			a := newApp(t)
			n := newNestedLists(t, a, true, c.unique)
			a.worker.onAgent("Find", handsIn(nestedTitles("A", "B")))

			rn := a.startRun(n.flow.ID)
			d := nestedFinish(t, a, rn.ID, runDone)
			children := nestedChildren(a, n.flow.ID, rn.ID)
			if got := nestedItemKeys(children); !slices.Equal(got, c.keys) {
				t.Errorf("item runs keyed %v, want %v", got, c.keys)
			}
			var stepRuns []int64
			for _, s := range stepsOf(d, "Handle") {
				if s.ChildRun != nil {
					stepRuns = append(stepRuns, *s.ChildRun)
				}
			}
			for _, child := range children {
				if child.ProcessName != "Handle" || child.Status != runDone || !slices.Contains(stepRuns, child.ID) {
					t.Errorf("item run %+v, want a done run of Handle a step of it refers to", child)
				}
				if len(stepsOf(a.getRun(child.ID), "Fix")) != 1 {
					t.Errorf("item run %d has no Fix step", child.ID)
				}
			}
		})
	}
}

// An empty list runs nothing, and what the flow process gathers is empty.
func TestFlowProcessOnAnEmptyList(t *testing.T) {
	a := newApp(t)
	n := newNestedLists(t, a, false, false)
	a.worker.onAgent("Find", handsIn([]any{}))

	d := nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)
	if len(a.worker.runsOf("Fix")) != 0 || len(stepsOf(d, "Handle")) != 0 {
		t.Errorf("an empty list ran something; steps %s", describeSteps(d))
	}
	if rp := onlyStep(t, d, "Report"); len(rp.Inputs) != 1 || strings.TrimSpace(rp.Inputs[0].Content) != "[]" {
		t.Errorf("Report took %+v, want an empty list", rp.Inputs)
	}
}

// A flow process takes items of one list at a time.
func TestFlowProcessTakingTwoLists(t *testing.T) {
	a := newApp(t)
	n := newNestedLists(t, a, false, false)
	more := a.newObject(n.flow.ID, "More")
	nestedList(a, more.ID, 0, -1)
	a.output(n.find.ID, more.ID)
	a.input(n.fp.ID, more.ID)
	a.worker.onAgent("Find", handsIn(map[string]any{"Issues": nestedTitles("A"), "More": []any{map[string]any{"data": 1}}}))

	d := nestedFinish(t, a, a.startRun(n.flow.ID).ID, runFailed)
	if s := onlyStep(t, d, "Handle"); s.Status != stepError || !strings.Contains(s.Error, `"Issues" and "More"`) {
		t.Errorf("Handle step %s %q", s.Status, s.Error)
	}
	if len(a.worker.runsOf("Fix")) != 0 {
		t.Error("Fix ran")
	}
}

// Report generates a stored Bug, told apart by its id, that Notify takes.
type nestedBugs struct {
	flow           flow
	report, notify process
	bug            object
}

func newNestedBugs(t *testing.T, a *app) nestedBugs {
	t.Helper()
	var n nestedBugs
	n.flow = a.newFlow("Bugs")
	f := n.flow.ID
	n.report = a.agent(f, "Report", "Report the bug.")
	n.bug = a.newObject(f, "Bug")
	nestedPatchObject(a, n.bug.ID, map[string]any{"persistent": true})
	nestedAttribute(a, n.bug.ID, "id", true)
	nestedAttribute(a, n.bug.ID, "text", false)
	n.notify = a.agent(f, "Notify", "Tell someone.")
	a.output(n.report.ID, n.bug.ID)
	a.input(n.notify.ID, n.bug.ID)
	a.startAt(f, n.report.ID)
	a.worker.onAgent("Notify", handsIn(data("told")))
	return n
}

// The bug Report hands in, with the id given.
func nestedBug(id string) func(context.Context, agentRun) runResult {
	return handsIn(map[string]any{"id": id, "text": "crash " + id})
}

// A run generating a unique value an earlier run stored is terminated: its
// step is a duplicate, saying which run stored it, and nothing after it runs.
// Values count as the same whatever their case and spacing.
func TestUniqueValueStoredTerminatesLaterRuns(t *testing.T) {
	a := newApp(t)
	n := newNestedBugs(t, a)

	a.worker.onAgent("Report", nestedBug("E1"))
	first := a.startRun(n.flow.ID)
	nestedFinish(t, a, first.ID, runDone)

	a.worker.onAgent("Report", nestedBug("  e1 "))
	d := nestedFinish(t, a, a.startRun(n.flow.ID).ID, runTerminated)
	s := onlyStep(t, d, "Report")
	if s.Status != stepDuplicate || !strings.Contains(s.Error, "Bug with id") || !strings.Contains(s.Error, fmt.Sprintf("already stored by run #%d", first.ID)) {
		t.Errorf("Report step %s %q", s.Status, s.Error)
	}
	if len(stepsOf(d, "Notify")) != 0 {
		t.Error("Notify ran on a duplicate")
	}
	// The step keeps what it generated, for the history.
	if got := contentsOf(d, "Bug"); len(got) != 1 || !strings.Contains(got[0], "e1") {
		t.Errorf("the duplicate step recorded %q", got)
	}

	// A value only a non-unique attribute shares isn't a duplicate.
	a.worker.onAgent("Report", handsIn(map[string]any{"id": "E2", "text": "crash E1"}))
	nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)
	// Nor is an empty one.
	a.worker.onAgent("Report", handsIn(map[string]any{"id": "", "text": "x"}))
	nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)
	nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)
}

// A run terminated by a duplicate ends the reviews it still waits on.
func TestDuplicateEndsWaitingReviews(t *testing.T) {
	a := newApp(t)
	n := newNestedBugs(t, a)
	kick := a.agent(n.flow.ID, "Kick", "Start.")
	signal := a.newObject(n.flow.ID, "Signal")
	review := a.newProcess(n.flow.ID, map[string]any{"name": "Review", "kind": kindHuman})
	a.output(kick.ID, signal.ID)
	a.input(review.ID, signal.ID)
	a.input(n.report.ID, signal.ID)
	a.startAt(n.flow.ID, kick.ID)
	a.worker.onAgent("Kick", handsIn(data("go")))

	a.worker.onAgent("Report", nestedBug("E1"))
	first := a.startRun(n.flow.ID)
	firstReview := a.waitStep(first.ID, "Review", stepWaiting)
	a.call("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", first.ID, firstReview.ID), map[string]any{}, http.StatusNoContent, nil)
	nestedFinish(t, a, first.ID, runDone)

	// The duplicate comes once the review waits.
	reviewing := make(chan struct{})
	a.worker.onAgent("Report", func(ctx context.Context, run agentRun) runResult {
		select {
		case <-reviewing:
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
		return nestedBug("E1")(ctx, run)
	})
	second := a.startRun(n.flow.ID)
	r := a.waitStep(second.ID, "Review", stepWaiting)
	close(reviewing)
	nestedFinish(t, a, second.ID, runTerminated)
	// The run reads as terminated as soon as the duplicate is stored; its
	// reviews end once the engine winds it up.
	d := a.waitRun(second.ID, "its review to end", func(d runDetail) bool {
		s := stepsOf(d, "Review")
		return len(s) == 1 && s[0].Status != stepWaiting
	})
	if s := onlyStep(t, d, "Review"); s.Status != stepError || s.FinishedAt == nil || d.Status != runTerminated {
		t.Errorf("the waiting review = %s %q in a %s run, want ended", s.Status, s.Error, d.Status)
	}
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", second.ID, r.ID), map[string]any{}, http.StatusConflict, "no review waiting")
}

// Only a run that ends done keeps what it stored: one failing or stopped
// afterwards releases it, so a later run can store it again.
func TestRunNotDoneReleasesItsValues(t *testing.T) {
	for _, end := range []string{runFailed, runInterrupted} {
		t.Run(end, func(t *testing.T) {
			a := newApp(t)
			n := newNestedBugs(t, a)
			a.worker.onAgent("Report", nestedBug("E1"))
			started := make(chan struct{}, 1)
			a.worker.onAgent("Notify", func(ctx context.Context, _ agentRun) runResult {
				started <- struct{}{}
				if end == runFailed {
					return runResult{Error: "can't tell anyone"}
				}
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
				return runResult{Error: "stopped"}
			})

			first := a.startRun(n.flow.ID)
			receive(t, started, "Notify to start")
			if end == runInterrupted {
				a.waitStep(first.ID, "Notify", stepActive)
				a.call("PATCH", fmt.Sprintf("/api/runs/%d", first.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
			}
			d := nestedFinish(t, a, first.ID, end)
			if s := onlyStep(t, d, "Report"); s.Status != stepDone {
				t.Fatalf("Report step %s %q, want it to have stored the bug", s.Status, s.Error)
			}

			a.worker.onAgent("Notify", handsIn(data("told")))
			d = nestedFinish(t, a, a.startRun(n.flow.ID).ID, runDone)
			if s := onlyStep(t, d, "Report"); s.Status != stepDone {
				t.Errorf("Report step %s %q after the first run released E1", s.Status, s.Error)
			}
		})
	}
}

// The items of a stored list are claimed by the runs handling them: a later
// run skips those another run holds, saying so, and runs those a run ending
// otherwise than done released.
func TestStoredListItemsAreClaimedOnce(t *testing.T) {
	a := newApp(t)
	n := newNestedLists(t, a, true, true)
	nestedPatchObject(a, n.issues.ID, map[string]any{"persistent": true})
	fixes := func(k string) int {
		c := 0
		for _, r := range a.worker.runsOf("Fix") {
			if strings.Contains(r.Prompt, `"title": "`+k+`"`) {
				c++
			}
		}
		return c
	}

	a.worker.onAgent("Find", handsIn(nestedTitles("A", "B")))
	first := a.startRun(n.flow.ID)
	nestedFinish(t, a, first.ID, runDone)
	var claimB int64
	for _, c := range nestedChildren(a, n.flow.ID, first.ID) {
		if c.ItemKey == "B" {
			claimB = c.ID
		}
	}
	if claimB == 0 {
		t.Fatalf("no item run of B in %+v", nestedChildren(a, n.flow.ID, first.ID))
	}

	a.worker.onAgent("Find", handsIn(nestedTitles("b", "C")))
	second := a.startRun(n.flow.ID)
	d := nestedFinish(t, a, second.ID, runDone)
	if got := nestedItemKeys(nestedChildren(a, n.flow.ID, second.ID)); !slices.Equal(got, []string{"C"}) {
		t.Errorf("the second run ran items %v, want only C", got)
	}
	want := fmt.Sprintf(`1 of 2 items of "Issues" were already taken by other runs: b (run #%d).`, claimB)
	if !slices.ContainsFunc(stepsOf(d, "Handle"), func(s step) bool { return s.Status == stepDuplicate && s.Error == want }) {
		t.Errorf("no Handle step saying %q; steps %s", want, describeSteps(d))
	}
	if fixes("B")+fixes("b") != 1 || fixes("C") != 1 {
		t.Errorf("Fix ran B %d and C %d times, want once each", fixes("B")+fixes("b"), fixes("C"))
	}
	if rp := onlyStep(t, d, "Report"); len(rp.Inputs) != 1 || strings.Contains(rp.Inputs[0].Content, "fixed B") || !strings.Contains(rp.Inputs[0].Content, "fixed C") {
		t.Errorf("Report took %+v, want only the fix of C", rp.Inputs)
	}

	// An item whose run fails is released, and runs again next time.
	a.worker.onAgent("Find", handsIn(nestedTitles("D")))
	a.worker.onAgent("Fix", func(context.Context, agentRun) runResult { return runResult{Error: "can't fix"} })
	nestedFinish(t, a, a.startRun(n.flow.ID).ID, runFailed)
	a.worker.onAgent("Fix", nestedEcho("fixed "))
	again := a.startRun(n.flow.ID)
	nestedFinish(t, a, again.ID, runDone)
	if got := nestedItemKeys(nestedChildren(a, n.flow.ID, again.ID)); !slices.Equal(got, []string{"D"}) {
		t.Errorf("after its run failed, D ran as %v", got)
	}
	if fixes("D") != 2 {
		t.Errorf("Fix ran D %d times, want twice", fixes("D"))
	}
}
