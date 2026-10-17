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
		{query(agent.ID, plain.ID, directionInput, nil), "only a persistent object has records"},
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
	a := setupApp(t)
	prefix := fmt.Sprintf("q%d", time.Now().UnixNano())
	f := a.newFlow(prefix + " Lookup")
	look := a.newProcess(f.ID, map[string]any{"name": "Look", "kind": kindScript, "script": "true"})
	review := a.newObject(f.ID, "Review")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", review.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	want := linkQuery{Where: "score >= 4", Limit: 5, Oldest: true}
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
