package tests

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

// A query link gives its process the stored records of a persistent object
// that its rule and state select, from runs that ended done, newest first
// or oldest first, at most its limit. It doesn't wait, and a process
// querying an object it yields too neither waits for it nor has it carried
// over.
func TestQueryLinkTakesStoredRecords(t *testing.T) {
	parallel(t)
	a := newApp(t)
	f := a.newFlow("Reviews")
	write := a.newProcess(f.ID, map[string]any{"name": "Write", "kind": kindScript, "script": "cat $INPUTS/Review"})
	review := a.newObject(f.ID, "Review")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", review.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	rulesState(a, review.ID, "approved", `data contains "ok"`)
	a.output(write.ID, review.ID)
	q := a.link(map[string]any{"process": write.ID, "object": review.ID, "direction": directionInput,
		"query": linkQuery{Where: `data contains "timeout"`, State: "approved"}})
	if q.Query == nil || q.Query.State != "approved" {
		t.Fatalf("the query link = %+v", q)
	}
	a.startAt(f.ID, write.ID)

	var mu sync.Mutex
	var given [][]scriptInput
	outputs := []string{"ok timeout 1", "bad timeout 2", "ok 3", "", "ok timeout 5", "noise", "noise"}
	a.worker.onScript("Write", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		given = append(given, run.Inputs)
		out := outputs[len(given)-1]
		if out == "" {
			return runResult{Error: "the reviewer is away"}
		}
		return runResult{OK: true, Output: fmt.Sprintf(`{"data": %q}`, out)}
	})
	records := func(run int) []string {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		in := given[run-1]
		if len(in) != 1 || in[0].Name != "Review" {
			t.Fatalf("run %d's Write was given %+v, want the records of Review alone", run, in)
		}
		var rs []struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal([]byte(in[0].Content), &rs); err != nil {
			t.Fatalf("run %d's records %q: %v", run, in[0].Content, err)
		}
		var out []string
		for _, r := range rs {
			out = append(out, r.Data)
		}
		return out
	}

	for i := 1; i <= 6; i++ {
		d := a.finish(a.startRun(f.ID).ID)
		if want := i != 4; (d.Status == runDone) != want {
			t.Fatalf("run %d %s; steps %s", i, d.Status, describeSteps(d))
		}
	}
	if got := records(1); len(got) != 0 {
		t.Errorf("the first run's records = %v, want none", got)
	}
	// Approved and about a timeout: not the second, rejected, nor the third,
	// nor anything of the failed fourth.
	if got, want := records(6), []string{"ok timeout 5", "ok timeout 1"}; !slices.Equal(got, want) {
		t.Errorf("the sixth run's records = %v, want %v", got, want)
	}

	a.call("PUT", fmt.Sprintf("/api/links/%d/query", q.ID), map[string]any{"query": linkQuery{State: "approved", Limit: 1, Oldest: true}}, http.StatusNoContent, nil)
	a.finish(a.startRun(f.ID).ID)
	if got, want := records(7), []string{"ok timeout 1"}; !slices.Equal(got, want) {
		t.Errorf("the seventh run's records = %v, want %v", got, want)
	}
}

// What a query link can't be.
func TestQueryLinkRefusals(t *testing.T) {
	parallel(t)
	a := newApp(t)
	f := a.newFlow("Bad queries")
	agent := a.agent(f.ID, "Triage", "Triage it.")
	human := a.newProcess(f.ID, map[string]any{"name": "Approve", "kind": kindHuman})
	stored := a.newObject(f.ID, "Exception")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", stored.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	a.newState(stored.ID, "fixed")
	plain := a.newObject(f.ID, "Note")
	query := func(p, o int64, direction string, extra map[string]any) map[string]any {
		l := map[string]any{"process": p, "object": o, "direction": direction, "query": linkQuery{}}
		for k, v := range extra {
			l[k] = v
		}
		return l
	}
	for _, c := range []struct {
		link map[string]any
		msg  string
	}{
		{query(agent.ID, stored.ID, directionOutput, nil), "only an input link has a query"},
		{query(agent.ID, plain.ID, directionInput, nil), "only a persistent or shared object has records"},
		{query(human.ID, stored.ID, directionInput, nil), "only an agent or a script queries"},
		{query(agent.ID, stored.ID, directionInput, map[string]any{"role": "consumption"}), "can't consume"},
		{query(agent.ID, stored.ID, directionInput, map[string]any{"query": linkQuery{Where: "score"}}), "the query's rule"},
		{query(agent.ID, stored.ID, directionInput, map[string]any{"query": linkQuery{Limit: 101}}), "at most 100 records"},
		{query(agent.ID, stored.ID, directionInput, map[string]any{"query": linkQuery{State: "open"}}), `no state "open"`},
	} {
		a.refused("POST", "/api/links", c.link, http.StatusBadRequest, c.msg)
	}

	// A process takes an object it queries by its query alone.
	q := a.link(query(agent.ID, stored.ID, directionInput, map[string]any{"query": linkQuery{State: "fixed"}}))
	a.refused("POST", "/api/links", map[string]any{"process": agent.ID, "object": stored.ID, "direction": directionInput},
		http.StatusBadRequest, "queries that object already")
	a.refused("PUT", fmt.Sprintf("/api/links/%d/role", q.ID), map[string]any{"role": "consumption"}, http.StatusBadRequest, "can't consume")
	a.call("PUT", fmt.Sprintf("/api/links/%d/query", q.ID), map[string]any{"query": nil}, http.StatusNoContent, nil)
	other := a.newProcess(f.ID, map[string]any{"name": "Look", "kind": kindScript, "script": "true"})
	l := a.input(other.ID, stored.ID)
	a.call("PUT", fmt.Sprintf("/api/links/%d/query", l.ID), map[string]any{"query": linkQuery{}}, http.StatusNoContent, nil)
	a.refused("POST", "/api/links", map[string]any{"process": other.ID, "object": stored.ID, "direction": directionInput, "query": linkQuery{}},
		http.StatusBadRequest, "queries that object already")
}

// A query is part of the board: a version restored keeps it, and a setup
// file carries it out and back in.
func TestQueryLinkIsKept(t *testing.T) {
	parallel(t)
	a := setupApp(t)
	prefix := fmt.Sprintf("q%d", time.Now().UnixNano())
	f := a.newFlow(prefix + " Lookup")
	look := a.newProcess(f.ID, map[string]any{"name": "Look", "kind": kindScript, "script": "true"})
	review := a.newObject(f.ID, "Review")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", review.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	want := linkQuery{Where: "score >= 4", Limit: 5, Oldest: true, Like: "{@Review.summary} timeout"}
	l := a.link(map[string]any{"process": look.ID, "object": review.ID, "direction": directionInput, "query": want})

	var saved version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &saved)
	a.call("PUT", fmt.Sprintf("/api/links/%d/query", l.ID), map[string]any{"query": nil}, http.StatusNoContent, nil)
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusNoContent, nil)
	var ls []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &ls)
	if len(ls) != 1 || ls[0].Query == nil || *ls[0].Query != want {
		t.Fatalf("after restoring, links %+v", ls)
	}

	// Undoing taking the query off puts it back.
	a.call("POST", fmt.Sprintf("/api/history?flow=%d", f.ID), nil, http.StatusOK, nil)
	a.call("PUT", fmt.Sprintf("/api/links/%d/query", ls[0].ID), map[string]any{"query": nil}, http.StatusNoContent, nil)
	a.call("POST", fmt.Sprintf("/api/history?flow=%d", f.ID), nil, http.StatusOK, nil)
	a.call("POST", fmt.Sprintf("/api/history/undo?flow=%d", f.ID), nil, http.StatusOK, nil)
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &ls)
	if len(ls) != 1 || ls[0].Query == nil || *ls[0].Query != want {
		t.Fatalf("after undoing, links %+v", ls)
	}

	exported := setupExport(a, prefix)
	if !strings.Contains(exported, "where: score >= 4") {
		t.Fatalf("the export leaves the query out:\n%s", exported)
	}
	a.call("PUT", fmt.Sprintf("/api/links/%d/query", ls[0].ID), map[string]any{"query": nil}, http.StatusNoContent, nil)
	setupImportOK(a, exported)
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &ls)
	if len(ls) != 1 || ls[0].Query == nil || *ls[0].Query != want {
		t.Errorf("after importing the export, links %+v", ls)
	}
}

// A shared struct's query searches its records as the Records page does, a
// field typed datetime as a time, and ranks what it selects by how like it
// is to what its process takes, the most like first, telling the step how
// many it selected when its limit cut them. A query ranking records looks
// them up: it leaves out the record its run picked, and its flow isn't one
// to start on a record.
func TestQuerySearchesAndRanksRecords(t *testing.T) {
	parallel(t)
	a := newApp(t)
	p := fmt.Sprintf("QS%d", time.Now().UnixNano())
	ids := map[string]int64{}
	for _, f := range setupImportOK(a, strings.ReplaceAll(`spoletta: 1
structs:
  - name: QS Ticket
    shared: true
    states: [open, closed]
    attributes:
      - {name: key, unique: true}
      - {name: summary}
      - {name: created, type: datetime}
flows:
  - name: QS Triage
    start: {next: Enter}
    processes:
      - name: Enter
        kind: script
        script: enter
        yields: [Incoming]
      - name: Check
        kind: script
        script: check
        takes:
          - Incoming
          - object: QS Ticket
            query: {search: 'state:open @created:>now-7d', like: '{@Incoming.summary}', limit: 2}
    objects:
      - name: Incoming
        attributes:
          - {name: summary}
      - copyOf: {flow: QS Ticket}
  - name: QS Duplicates
    start: {next: Compare}
    processes:
      - name: Compare
        kind: script
        script: compare
        takes:
          - QS Ticket
          - object: Other ticket
            query: {like: '{@QS Ticket}', limit: 3}
    objects:
      - copyOf: {flow: QS Ticket}
        pick: {state: open, oldest: true}
      - name: Other ticket
        copyOf: {flow: QS Ticket}
`, "QS", p)).Flows {
		ids[f.Name] = f.ID
	}
	ticket, triage, duplicates := ids[p+" Ticket"], ids[p+" Triage"], ids[p+" Duplicates"]
	for _, f := range []int64{triage, duplicates} {
		if ps := prbGet(a, f); len(ps.Problems) != 0 {
			t.Errorf("flow %d's problems: %s", f, prbDescribe(ps.Problems))
		}
	}

	record := map[string]int64{}
	add := func(key, summary string, age time.Duration, state string) {
		t.Helper()
		var r sharedRecord
		a.call("POST", fmt.Sprintf("/api/structs/%d/records", ticket), map[string]any{"content": map[string]string{
			"key": key, "summary": summary, "created": time.Now().Add(-age).UTC().Format(time.RFC3339)}, "state": state}, http.StatusCreated, &r)
		record[key] = r.ID
	}
	day := 24 * time.Hour
	add("K-1", "Login times out on the SSO page", day, "open")
	add("K-2", "Login page is slow", 2*day, "open")
	add("K-3", "Checkout page is blank", 3*day, "open")
	add("K-4", "Login times out", 30*day, "open")
	add("K-5", "Login times out on SSO", day, "closed")
	add("K-6", "Export to CSV drops a row", day, "open")

	var mu sync.Mutex
	took := map[string]scriptInput{}
	keep := func(process string) func(context.Context, scriptRun) runResult {
		return func(_ context.Context, run scriptRun) runResult {
			mu.Lock()
			defer mu.Unlock()
			for _, in := range run.Inputs {
				took[process+"/"+in.Name] = in
			}
			return runResult{OK: true, Output: "{}"}
		}
	}
	a.worker.onScript("Enter", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"summary": "SSO login page times out"}`}
	})
	a.worker.onScript("Check", keep("Check"))
	a.worker.onScript("Compare", keep("Compare"))
	keys := func(in scriptInput) []string {
		t.Helper()
		var rs []struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(in.Content), &rs); err != nil {
			t.Fatalf("records %q: %v", in.Content, err)
		}
		var out []string
		for _, r := range rs {
			out = append(out, r.Key)
		}
		return out
	}

	// Open, of the last week and sharing a word: the first three, the one
	// saying most of it first; the limit takes two.
	d := a.finish(a.startRun(triage).ID)
	if d.Status != runDone {
		t.Fatalf("triage %s; steps %s", d.Status, describeSteps(d))
	}
	given := took["Check/"+p+" Ticket"]
	if got := keys(given); !slices.Equal(got, []string{"K-1", "K-2"}) || given.Matched != "3" {
		t.Errorf("Check took %v cut from %q, want K-1 and K-2 of 3", got, given.Matched)
	}
	for _, in := range onlyStep(t, d, "Check").Inputs {
		if in.Name == p+" Ticket" && (in.Matched != 3 || in.MatchedMore || in.Role != "records") {
			t.Errorf("the step's input %+v, want it to say its query selected 3", in)
		}
	}

	// The run picks the oldest open ticket, which its lookup leaves out:
	// the closed one saying the same comes first.
	d = a.finish(a.startRun(duplicates).ID)
	if d.Status != runDone {
		t.Fatalf("duplicates %s; steps %s", d.Status, describeSteps(d))
	}
	if got := keys(took["Compare/Other ticket"]); len(got) != 3 || got[0] != "K-5" || slices.Contains(got, "K-1") {
		t.Errorf("Compare took %v, want the closed K-5 first and not the picked K-1", got)
	}
	if picked := took["Compare/"+p+" Ticket"]; !strings.Contains(picked.Content, `"K-1"`) {
		t.Errorf("Compare picked %s, want K-1", picked.Content)
	}

	// Only the flow picking a ticket is one to start on it.
	var offered struct {
		Flows []struct {
			ID int64 `json:"id"`
		} `json:"flows"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records/flows?records=%d", ticket, record["K-1"]), nil, http.StatusOK, &offered)
	if len(offered.Flows) != 1 || offered.Flows[0].ID != duplicates {
		t.Errorf("flows offered on a ticket %+v, want %d alone", offered.Flows, duplicates)
	}

	// A search names fields of the records and values of what the process
	// takes: one naming no object of the flow doesn't run, and one naming
	// what it doesn't take fails the step, saying so.
	var links []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", triage), nil, http.StatusOK, &links)
	search := func(q string) {
		t.Helper()
		for _, l := range links {
			if l.Query != nil {
				a.call("PUT", fmt.Sprintf("/api/links/%d/query", l.ID), map[string]any{"query": linkQuery{Search: q}}, http.StatusNoContent, nil)
			}
		}
	}
	search("@summary:{@Bug.summary}")
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", triage), nil, http.StatusConflict, `names "Bug", which no object of the flow is`)
	search("@summary:{@" + p + " Ticket.summary}")
	d = a.finish(a.startRun(triage).ID)
	if s := onlyStep(t, d, "Check"); d.Status != runFailed || !strings.Contains(s.Error, `names "`+p+` Ticket", which it wasn't given`) {
		t.Errorf("a query naming what isn't given: run %s, Check %s %q", d.Status, s.Status, s.Error)
	}
}

// What a query's search and like can't be.
func TestQuerySearchRefusals(t *testing.T) {
	parallel(t)
	a := newApp(t)
	f := a.newFlow("Bad searches")
	look := a.newProcess(f.ID, map[string]any{"name": "Look", "kind": kindScript, "script": "true"})
	stored := a.newObject(f.ID, "Review")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", stored.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	query := func(q linkQuery) map[string]any {
		return map[string]any{"process": look.ID, "object": stored.ID, "direction": directionInput, "query": q}
	}
	a.refused("POST", "/api/links", query(linkQuery{Search: "state:open"}), http.StatusBadRequest, "only a shared struct's records are searched")
	a.refused("POST", "/api/links", query(linkQuery{Like: "{x}"}), http.StatusBadRequest, "the query's like: write each value as {@Object.attribute}")
	a.refused("POST", "/api/links", query(linkQuery{Like: strings.Repeat("x", 1025)}), http.StatusBadRequest, "longer than 1024")
	// A persistent object's records are ranked too.
	a.link(query(linkQuery{Like: "timeout", Limit: 3}))
}
