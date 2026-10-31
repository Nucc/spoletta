package tests

// Playing a task board: the board playing gives each record its columns
// hold to the flow taking its state, or the one its column names, a run per
// record, as many at once as it and each column allow, the rightmost
// columns first, until records reach a state no flow takes or a column
// letting them rest. A record its run leaves where it was is stuck until a
// person tries it again.

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

type playBoard struct {
	ID       int64        `json:"id"`
	Playing  bool         `json:"playing"`
	PlayedBy string       `json:"playedBy"`
	MaxRuns  int          `json:"maxRuns"`
	Stopped  string       `json:"stopped"`
	Running  int          `json:"running"`
	Columns  []playColumn `json:"columns"`
}

type playColumn struct {
	Name        string `json:"name"`
	Struct      *int64 `json:"struct"`
	Query       string `json:"query"`
	Flow        *int64 `json:"flow"`
	FlowName    string `json:"flowName"`
	MaxRuns     *int   `json:"maxRuns"`
	FlowProblem string `json:"flowProblem"`
	ByState     []struct {
		State string `json:"state"`
		Flows []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"flows"`
		Any bool `json:"any"`
	} `json:"byState"`
	Cards *struct {
		Records []sharedRecord     `json:"records"`
		Runs    map[string]playRun `json:"runs"`
	} `json:"cards"`
}

// A card's run: one going on its record, or the one leaving it stuck.
type playRun struct {
	Run      int64      `json:"run"`
	Flow     int64      `json:"flow"`
	Status   string     `json:"status"`
	Why      string     `json:"why"`
	Started  *time.Time `json:"started"`
	FlowName string     `json:"flowName"`
	Steps    []string   `json:"steps"`
}

// The flows the column shows taking each state, as "state: flow, flow",
// followed by " (any state)" for flows taking it by querying any state.
func (c playColumn) taking() []string {
	var out []string
	for _, sf := range c.ByState {
		var names []string
		for _, f := range sf.Flows {
			names = append(names, f.Name)
		}
		line := sf.State + ": " + strings.Join(names, ", ")
		if sf.Any {
			line += " (any state)"
		}
		out = append(out, line)
	}
	return out
}

// A flow whose script process, named name, queries the struct's records in
// state from and leaves them in state to.
func movingFlow(a *app, s structFlow, name, from, to string) flow {
	a.t.Helper()
	f := a.newFlow(name + " bugs")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	bug := modNamed(a.t, modObjects(a, f.ID), s.Name)
	p := a.newProcess(f.ID, map[string]any{"name": name, "kind": kindScript, "script": strings.ToLower(name)})
	a.link(map[string]any{"process": p.ID, "object": bug.ID, "direction": directionInput, "query": linkQuery{State: from, Limit: 1}})
	for _, st := range bug.States {
		if st.Name == to {
			a.link(map[string]any{"process": p.ID, "object": bug.ID, "direction": directionOutput, "state": st.ID})
		}
	}
	a.startAt(f.ID, p.ID)
	return f
}

// What the board's scripts were given, in order, as "Process key", and
// what they do: hold until released, fail, or hand back another bug than
// the one given, by key.
type playScripts struct {
	mu    sync.Mutex
	calls []string
	hold  map[string]chan struct{}
	fail  map[string]bool
	other map[string]bool
	// How many are going at once at most, and now.
	most, now int
}

func newPlayScripts(a *app, names ...string) *playScripts {
	ps := &playScripts{hold: map[string]chan struct{}{}, fail: map[string]bool{}, other: map[string]bool{}}
	for _, name := range names {
		a.worker.onScript(name, func(ctx context.Context, run scriptRun) runResult {
			var records []json.RawMessage
			for _, in := range run.Inputs {
				json.Unmarshal([]byte(in.Content), &records)
			}
			if len(records) != 1 {
				return runResult{Error: fmt.Sprintf("given %+v", run.Inputs)}
			}
			var fields struct {
				ID string `json:"id"`
			}
			json.Unmarshal(records[0], &fields)
			ps.mu.Lock()
			ps.calls = append(ps.calls, name+" "+fields.ID)
			ps.now++
			ps.most = max(ps.most, ps.now)
			wait, fail, other := ps.hold[fields.ID], ps.fail[fields.ID], ps.other[fields.ID]
			ps.mu.Unlock()
			defer func() {
				ps.mu.Lock()
				ps.now--
				ps.mu.Unlock()
			}()
			if wait != nil {
				select {
				case <-wait:
				case <-ctx.Done():
				}
			}
			switch {
			case fail:
				return runResult{Error: "it broke"}
			case other:
				return runResult{OK: true, Output: fmt.Sprintf(`{"id": "N%s", "title": "Noted"}`, strings.TrimPrefix(fields.ID, "B"))}
			}
			return runResult{OK: true, Output: string(records[0])}
		})
	}
	return ps
}

func (ps *playScripts) seen() []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return slices.Clone(ps.calls)
}

func (ps *playScripts) set(do func(ps *playScripts)) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	do(ps)
}

// Polls the board until cond holds of it, and returns it as it is then.
func waitBoard(a *app, id int64, what string, cond func(playBoard) bool) playBoard {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var b playBoard
		a.call("GET", fmt.Sprintf("/api/taskboards/%d", id), nil, http.StatusOK, &b)
		if cond(b) {
			return b
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("task board %d: timed out waiting for %s; it is %+v", id, what, b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The board's card of the record with the key, and its column.
func playCard(b playBoard, s structFlow, key string) (column string, status, why string) {
	column, run := playCardRun(b, key)
	return column, run.Status, run.Why
}

// The column of the board's card of the record with the key, and its run.
func playCardRun(b playBoard, key string) (string, playRun) {
	for _, c := range b.Columns {
		if c.Cards == nil {
			continue
		}
		for _, r := range c.Cards.Records {
			var fields struct {
				ID string `json:"id"`
			}
			json.Unmarshal(r.Content, &fields)
			if fields.ID == key {
				return c.Name, c.Cards.Runs[fmt.Sprint(r.ID)]
			}
		}
	}
	return "", playRun{}
}

// A board over a struct of bugs, new to triaged to fixed, one flow taking
// each state, played one run at a time with no column naming a flow: every
// bug goes to the flow taking its state, a triaged bug fixed before another
// is triaged, until all are fixed. A bug whose run fails, or ends leaving
// it where it was, is stuck and started no more until a person tries it
// again.
func TestPlayingATaskBoard(t *testing.T) {
	a := newApp(t)
	s := taskStruct(a, "Bug", nil, "id", []string{"title"}, "new", "triaged", "fixed")
	triage := movingFlow(a, s, "Triage", "new", "triaged")
	fix := movingFlow(a, s, "Fix", "triaged", "fixed")
	unrelated := a.newFlow("Unrelated")
	ps := newPlayScripts(a, "Triage", "Fix")

	var b playBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Bugs"}, http.StatusCreated, &b)
	t.Cleanup(func() { a.do("POST", fmt.Sprintf("/api/taskboards/%d/pause", b.ID), nil) })
	column := func(name, query string, flow *int64) map[string]any {
		return map[string]any{"name": name, "struct": s.ID, "query": query, "flow": flow}
	}

	// A flow not querying the bugs isn't one a column can name.
	a.refused("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs",
		"columns": []any{column("New", "state:new", &unrelated.ID)}}, http.StatusBadRequest, "doesn't query its records")

	// Each column shows the flow taking its state, none for fixed.
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs", "maxRuns": 1, "columns": []any{
		column("New", "state:new", nil), column("Triaged", "state:triaged", nil), column("Fixed", "state:fixed", nil),
	}}, http.StatusOK, &b)
	if b.MaxRuns != 1 || b.Playing {
		t.Fatalf("saved %+v", b)
	}
	for i, want := range []string{"new: " + triage.Name, "triaged: " + fix.Name, "fixed: "} {
		if got := b.Columns[i].taking(); len(got) != 1 || got[0] != want {
			t.Errorf("%s shows %q taking it, want %q", b.Columns[i].Name, got, want)
		}
	}

	taskRecord(a, s.ID, map[string]any{"id": "B-1", "title": "Crash"}, "new")
	taskRecord(a, s.ID, map[string]any{"id": "B-2", "title": "Leak"}, "new")
	ps.set(func(ps *playScripts) { ps.hold["B-2"] = make(chan struct{}) })
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/play", b.ID), nil, http.StatusOK, &b)
	if !b.Playing || b.Stopped != "" {
		t.Fatalf("played %+v", b)
	}

	// The newest bug first, as the column sorts, and only it while it goes.
	b = waitBoard(a, b.ID, "B-2 to be triaged", func(b playBoard) bool { return b.Running == 1 })
	if col, status, _ := playCard(b, s, "B-2"); col != "New" || status != "running" {
		t.Fatalf("B-2 shows in %q %q", col, status)
	}
	// Its card names the flow and the step going, and since when.
	if _, run := playCardRun(b, "B-2"); run.FlowName != triage.Name || !slices.Equal(run.Steps, []string{"Triage"}) || run.Started == nil {
		t.Errorf("B-2's card shows its run as %+v, want %s going at Triage", run, triage.Name)
	}
	time.Sleep(2500 * time.Millisecond)
	if got := ps.seen(); !slices.Equal(got, []string{"Triage B-2"}) {
		t.Fatalf("started %q while B-2 was triaged, one at a time", got)
	}
	ps.set(func(ps *playScripts) { close(ps.hold["B-2"]) })

	b = waitBoard(a, b.ID, "every bug fixed", func(b playBoard) bool {
		return len(b.Columns[2].Cards.Records) == 2 && b.Running == 0
	})
	if got := ps.seen(); !slices.Equal(got, []string{"Triage B-2", "Fix B-2", "Triage B-1", "Fix B-1"}) {
		t.Errorf("ran %q, want each bug triaged then fixed, B-2 fixed before B-1 is triaged", got)
	}
	if ps.most != 1 {
		t.Errorf("%d runs went at once, more than the board's 1", ps.most)
	}
	rs := sharedRecords(a, s.ID)
	if rs["B-1"].State != "fixed" || rs["B-2"].State != "fixed" {
		t.Errorf("bugs are %+v", rs)
	}
	// The run's log says the board started it.
	logLineWith(t, a.runLogs(*rs["B-1"].Run, 0), `as the task board "Bugs" plays`)

	// A run that fails leaves its bug stuck, as does one that ends done
	// without moving it on, triaging another bug in its place, which is
	// fixed as any.
	ps.set(func(ps *playScripts) { ps.fail["B-3"], ps.other["B-4"] = true, true })
	taskRecord(a, s.ID, map[string]any{"id": "B-3", "title": "Hang"}, "new")
	taskRecord(a, s.ID, map[string]any{"id": "B-4", "title": "Typo"}, "new")
	b = waitBoard(a, b.ID, "B-3 and B-4 stuck, N-4 fixed", func(b playBoard) bool {
		_, s3, _ := playCard(b, s, "B-3")
		_, s4, _ := playCard(b, s, "B-4")
		n4, _, _ := playCard(b, s, "N-4")
		return s3 == "stuck" && s4 == "stuck" && n4 == "Fixed"
	})
	if col, _, why := playCard(b, s, "B-3"); col != "New" || why != "failed" {
		t.Errorf("B-3 is stuck in %q as %q, want in New as failed", col, why)
	}
	if col, _, why := playCard(b, s, "B-4"); col != "New" || why != "unmoved" {
		t.Errorf("B-4 is stuck in %q as %q, want in New as unmoved", col, why)
	}
	seen := len(ps.seen())
	time.Sleep(2500 * time.Millisecond)
	if got := ps.seen(); len(got) != seen {
		t.Fatalf("ran %q; stuck bugs are started no more", got[seen:])
	}

	// Tried again once it no longer fails, it goes on to fixed; B-4 stays.
	ps.set(func(ps *playScripts) { delete(ps.fail, "B-3") })
	var r3 int64
	for id, r := range sharedRecords(a, s.ID) {
		if id == "B-3" {
			r3 = r.ID
		}
	}
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/retry", b.ID), map[string]any{"records": []int64{r3}}, http.StatusOK, nil)
	waitBoard(a, b.ID, "B-3 fixed", func(b playBoard) bool {
		col, _, _ := playCard(b, s, "B-3")
		return col == "Fixed"
	})
	if col, status, _ := playCard(waitBoard(a, b.ID, "the board", func(playBoard) bool { return true }), s, "B-4"); col != "New" || status != "stuck" {
		t.Errorf("B-4 shows in %q %q, want still stuck in New", col, status)
	}

	// Paused, it starts nothing more, though bugs come.
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/pause", b.ID), nil, http.StatusOK, &b)
	if b.Playing {
		t.Fatalf("paused %+v", b)
	}
	before := len(ps.seen())
	taskRecord(a, s.ID, map[string]any{"id": "B-5", "title": "Slow"}, "new")
	time.Sleep(2500 * time.Millisecond)
	if got := ps.seen(); len(got) != before {
		t.Errorf("ran %q while paused", got[before:])
	}
}

// A record in a state two flows take goes to neither until its column
// names one. A flow querying any state takes records in a state no flow
// names, but not one a flow names, nor those of a column letting its
// records rest.
func TestTaskBoardPicksTheFlowTakingAState(t *testing.T) {
	a := newApp(t)
	s := taskStruct(a, "Bug", nil, "id", []string{"title"}, "new", "triaged", "fixed")
	movingFlow(a, s, "Triage", "new", "triaged")
	urgent := movingFlow(a, s, "Urgent", "new", "triaged")
	movingFlow(a, s, "Sweep", "", "fixed")
	ps := newPlayScripts(a, "Triage", "Urgent", "Sweep")
	var b playBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Bugs"}, http.StatusCreated, &b)
	t.Cleanup(func() { a.do("POST", fmt.Sprintf("/api/taskboards/%d/pause", b.ID), nil) })
	rest := 0
	columns := func(newFlow *int64) []any {
		return []any{
			map[string]any{"name": "New", "struct": s.ID, "query": "state:new", "flow": newFlow},
			map[string]any{"name": "Triaged", "struct": s.ID, "query": "state:triaged"},
			map[string]any{"name": "Fixed", "struct": s.ID, "query": "state:fixed", "maxRuns": rest},
		}
	}
	// Resting, a column names no flow.
	a.refused("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs", "columns": []any{
		map[string]any{"name": "Fixed", "struct": s.ID, "query": "state:fixed", "maxRuns": rest, "flow": urgent.ID},
	}}, http.StatusBadRequest, "names no flow")
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs", "columns": columns(nil)}, http.StatusOK, &b)
	for i, want := range []string{"new: Triage bugs, Urgent bugs", "triaged: Sweep bugs (any state)", ""} {
		got := strings.Join(b.Columns[i].taking(), "; ")
		if got != want {
			t.Errorf("%s shows %q taking it, want %q", b.Columns[i].Name, got, want)
		}
	}
	if m := b.Columns[2].MaxRuns; m == nil || *m != 0 {
		t.Errorf("Fixed works on %v at once, want none as it rests", m)
	}

	taskRecord(a, s.ID, map[string]any{"id": "B-1", "title": "Crash"}, "new")
	taskRecord(a, s.ID, map[string]any{"id": "B-2", "title": "Done"}, "fixed")
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/play", b.ID), nil, http.StatusOK, nil)
	time.Sleep(2500 * time.Millisecond)
	if got := ps.seen(); len(got) != 0 {
		t.Fatalf("ran %q on a bug two flows take and a fixed one resting", got)
	}

	// Named on its column, Urgent takes it to triaged, which Sweep, querying
	// any state, takes on to fixed, where it rests.
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs", "columns": columns(&urgent.ID)}, http.StatusOK, nil)
	waitBoard(a, b.ID, "B-1 fixed", func(b playBoard) bool {
		col, _, _ := playCard(b, s, "B-1")
		return col == "Fixed" && b.Running == 0
	})
	time.Sleep(2500 * time.Millisecond)
	if got := ps.seen(); !slices.Equal(got, []string{"Urgent B-1", "Sweep B-1"}) {
		t.Errorf("ran %q, want Urgent then Sweep on B-1 alone", got)
	}
	b = waitBoard(a, b.ID, "the board", func(playBoard) bool { return true })
	if _, status, _ := playCard(b, s, "B-1"); status != "" {
		t.Errorf("B-1 shows %q, resting in Fixed", status)
	}
}

// A board's runs are for whoever pressed Play; once they are removed, it
// stops playing and says why.
func TestTaskBoardPlayedFor(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	bobRow := usersAdd(a, bob.email)
	tickets := recordStruct(a, "Ticket", "open", "done")
	f := movingFlow(a, tickets, "Implement", "open", "done")
	usersShare(a, tickets.ID)
	usersShare(a, f.ID)
	ps := newPlayScripts(a, "Implement")
	var b playBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Tickets", "owner": map[string]any{"kind": "global"}}, http.StatusCreated, &b)
	t.Cleanup(func() { a.do("POST", fmt.Sprintf("/api/taskboards/%d/pause", b.ID), nil) })
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Tickets", "columns": []any{
		map[string]any{"name": "Open", "struct": tickets.ID, "query": "state:open", "flow": f.ID},
		map[string]any{"name": "Done", "struct": tickets.ID, "query": "state:done"},
	}}, http.StatusOK, nil)

	usersSignIn(a, p, bob)
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/play", b.ID), nil, http.StatusOK, &b)
	if !b.Playing || b.PlayedBy != "Bob" {
		t.Fatalf("played %+v, want by Bob", b)
	}
	recordAdd(a, tickets, "T-1", "open")
	waitBoard(a, b.ID, "T-1 done", func(b playBoard) bool { return len(b.Columns[1].Cards.Records) == 1 && b.Running == 0 })
	if moved := sharedRecords(a, tickets.ID)["T-1"].Run; moved == nil || a.getRun(*moved).StartedBy != "Bob" {
		t.Errorf("T-1 was moved on by run %v, want one of Bob's", moved)
	}

	usersSignIn(a, p, usersAda)
	a.call("DELETE", fmt.Sprintf("/api/users/%d", bobRow.ID), nil, http.StatusNoContent, nil)
	recordAdd(a, tickets, "T-2", "open")
	b = waitBoard(a, b.ID, "it to stop", func(b playBoard) bool { return !b.Playing })
	if !strings.Contains(b.Stopped, "removed") {
		t.Errorf("it stopped saying %q", b.Stopped)
	}
	if got := ps.seen(); !slices.Equal(got, []string{"Implement T-1"}) {
		t.Errorf("ran %q, want nothing once Bob was removed", got)
	}
}

// Someone reading a board that plays for another sees the flow it gives a
// state's records to, and the run going on a card, by name only if they
// see the flow.
func TestTaskBoardNamesOnlyFlowsSeen(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carol := usersNewPerson("Bob"), usersNewPerson("Carol")
	usersAdd(a, bob.email)
	usersAdd(a, carol.email)
	tickets := recordStruct(a, "Ticket", "open", "done")
	usersShare(a, tickets.ID)
	var b playBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Tickets", "owner": map[string]any{"kind": "global"}}, http.StatusCreated, &b)
	t.Cleanup(func() { a.do("POST", fmt.Sprintf("/api/taskboards/%d/pause", b.ID), nil) })
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Tickets", "columns": []any{
		map[string]any{"name": "Open", "struct": tickets.ID, "query": "state:open"},
		map[string]any{"name": "Done", "struct": tickets.ID, "query": "state:done", "maxRuns": 0},
	}}, http.StatusOK, nil)

	// Bob's own flow, which Carol doesn't see, takes open tickets.
	usersSignIn(a, p, bob)
	movingFlow(a, tickets, "Implement", "open", "done")
	ps := newPlayScripts(a, "Implement")
	release := make(chan struct{})
	ps.set(func(ps *playScripts) { ps.hold["T-1"] = release })
	recordAdd(a, tickets, "T-1", "open")
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/play", b.ID), nil, http.StatusOK, nil)
	b = waitBoard(a, b.ID, "T-1 going", func(b playBoard) bool { return b.Running == 1 })
	if _, run := playCardRun(b, "T-1"); run.FlowName != "Implement bugs" || !slices.Equal(run.Steps, []string{"Implement"}) {
		t.Errorf("Bob sees T-1's run as %+v, want Implement bugs at Implement", run)
	}

	usersSignIn(a, p, carol)
	b = waitBoard(a, b.ID, "the board", func(playBoard) bool { return true })
	if col, run := playCardRun(b, "T-1"); col != "Open" || run.Status != "running" || run.FlowName != "" || len(run.Steps) != 0 {
		t.Errorf("Carol sees T-1's run as %+v in %q, want it running, its flow unnamed", run, col)
	}
	if got := b.Columns[0].taking(); len(got) != 1 || got[0] != "open: " {
		t.Errorf("Carol sees %q taking open, want a flow unnamed", got)
	}
	close(release)
	waitBoard(a, b.ID, "T-1 done", func(b playBoard) bool { return len(b.Columns[1].Cards.Records) == 1 && b.Running == 0 })
}

// A board runs as many of its runs at once as it allows, and a column as
// many as its own limit, the runs waiting their turn; a column whose flow
// whoever would run it can't start says why.
func TestTaskBoardLimits(t *testing.T) {
	a := newApp(t)
	s := taskStruct(a, "Bug", nil, "id", []string{"title"}, "new", "triaged", "fixed")
	triage := movingFlow(a, s, "Triage", "new", "triaged")
	fix := movingFlow(a, s, "Fix", "triaged", "fixed")
	ps := newPlayScripts(a, "Triage", "Fix")
	var b playBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Bugs"}, http.StatusCreated, &b)
	t.Cleanup(func() { a.do("POST", fmt.Sprintf("/api/taskboards/%d/pause", b.ID), nil) })
	one := 1
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs", "maxRuns": 2, "columns": []any{
		map[string]any{"name": "New", "struct": s.ID, "query": "state:new", "flow": triage.ID, "maxRuns": one},
		map[string]any{"name": "Triaged", "struct": s.ID, "query": "state:triaged", "flow": fix.ID},
		map[string]any{"name": "Fixed", "struct": s.ID, "query": "state:fixed"},
	}}, http.StatusOK, &b)

	release := make(chan struct{})
	ps.set(func(ps *playScripts) {
		for _, k := range []string{"B-1", "B-2", "B-3"} {
			ps.hold[k] = release
		}
	})
	for _, k := range []string{"B-1", "B-2", "B-3"} {
		taskRecord(a, s.ID, map[string]any{"id": k, "title": k}, "new")
	}
	// Triaged by hand, so Fix has one to start beside Triage's.
	taskRecord(a, s.ID, map[string]any{"id": "B-4", "title": "B-4"}, "triaged")
	ps.set(func(ps *playScripts) { ps.hold["B-4"] = release })
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/play", b.ID), nil, http.StatusOK, nil)
	waitBoard(a, b.ID, "two runs going", func(b playBoard) bool { return b.Running == 2 })
	time.Sleep(2500 * time.Millisecond)
	got := ps.seen()
	slices.Sort(got)
	if len(got) != 2 || got[0] != "Fix B-4" || !strings.HasPrefix(got[1], "Triage ") {
		t.Fatalf("started %q, want Fix on B-4 and Triage on one new bug alone", got)
	}
	close(release)
	waitBoard(a, b.ID, "every bug fixed", func(b playBoard) bool {
		return len(b.Columns[2].Cards.Records) == 4 && b.Running == 0
	})
	if ps.most > 2 {
		t.Errorf("%d runs went at once, more than the board's 2", ps.most)
	}

	// A column whose flow is in the trash says so, and the board plays on
	// around it.
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", fix.ID), nil, http.StatusNoContent, nil)
	b = waitBoard(a, b.ID, "the board", func(playBoard) bool { return true })
	if b.Columns[1].FlowProblem != "trash" || b.Columns[0].FlowProblem != "" || !b.Playing {
		t.Errorf("with Fix in the trash, the board is %+v", b)
	}
}
