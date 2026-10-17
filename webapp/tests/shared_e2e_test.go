package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// A record of a shared struct's object, as its board lists it.
type sharedRecord struct {
	ID      int64           `json:"id"`
	Content json.RawMessage `json:"content"`
	State   string          `json:"state"`
	Run     *int64          `json:"run"`
}

func sharedRecords(a *app, s int64) map[string]sharedRecord {
	a.t.Helper()
	var rs struct {
		Records []sharedRecord `json:"records"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records", s), nil, http.StatusOK, &rs)
	byID := map[string]sharedRecord{}
	for _, r := range rs.Records {
		var fields struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(r.Content, &fields); err != nil {
			a.t.Fatalf("record %d holds %s: %v", r.ID, r.Content, err)
		}
		byID[fields.ID] = r
	}
	return byID
}

// One flow fetches exceptions into a shared struct's records and another
// queries those still new and completes them. Fetching one again refreshes
// it and leaves it completed, the completing flow's query then passes it
// over, and a run of it that fails changes nothing.
func TestSharedRecordsAcrossFlows(t *testing.T) {
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Exception", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]

	// Records are told apart by a unique attribute.
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusBadRequest, "unique attribute")
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "title"}, http.StatusCreated, nil)
	rulesState(a, root.ID, "new", `id != ""`)
	a.newState(root.ID, "completed")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)

	use := func(f int64) modObject {
		a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f, "x": 0, "y": 0}, http.StatusCreated, nil)
		cp := modNamed(t, modObjects(a, f), "Exception")
		if !cp.Shared {
			t.Fatalf("the copy of the shared struct in flow %d isn't shared", f)
		}
		return cp
	}

	fetch := a.newFlow("Fetch exceptions")
	fetched := use(fetch.ID)
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", fetched.ID), map[string]any{"persistent": true}, http.StatusBadRequest, "shared")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", fetched.ID), map[string]any{"shared": false}, http.StatusBadRequest, "only a struct's object")
	sentry := a.newProcess(fetch.ID, map[string]any{"name": "Sentry", "kind": kindScript, "script": "fetch"})
	a.output(sentry.ID, fetched.ID)
	a.startAt(fetch.ID, sentry.ID)

	process := a.newFlow("Process exceptions")
	taken := use(process.ID)
	complete := a.newProcess(process.ID, map[string]any{"name": "Complete", "kind": kindScript, "script": "complete"})
	a.link(map[string]any{"process": complete.ID, "object": taken.ID, "direction": directionInput,
		"query": linkQuery{State: "new", Limit: 1, Oldest: true}})
	var completed int64
	for _, st := range taken.States {
		if st.Name == "completed" {
			completed = st.ID
		}
	}
	a.link(map[string]any{"process": complete.ID, "object": taken.ID, "direction": directionOutput, "state": completed})
	a.startAt(process.ID, complete.ID)

	var mu sync.Mutex
	var next string
	a.worker.onScript("Sentry", func(context.Context, scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		return runResult{OK: true, Output: next}
	})
	var given []string
	fail := false
	a.worker.onScript("Complete", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		if len(run.Inputs) != 1 {
			return runResult{Error: fmt.Sprintf("given %+v", run.Inputs)}
		}
		given = append(given, run.Inputs[0].Content)
		var records []json.RawMessage
		if err := json.Unmarshal([]byte(run.Inputs[0].Content), &records); err != nil || len(records) == 0 {
			return runResult{Error: "nothing to complete"}
		}
		if fail {
			return runResult{Error: "the fix didn't build"}
		}
		return runResult{OK: true, Output: string(records[0])}
	})
	ingest := func(out string) {
		t.Helper()
		mu.Lock()
		next = out
		mu.Unlock()
		if d := a.finish(a.startRun(fetch.ID).ID); d.Status != runDone {
			t.Fatalf("fetching %s %s; steps %s", out, d.Status, describeSteps(d))
		}
	}
	work := func(want string) {
		t.Helper()
		if d := a.finish(a.startRun(process.ID).ID); d.Status != want {
			t.Fatalf("processing ended %s, want %s; steps %s", d.Status, want, describeSteps(d))
		}
	}
	state := func(id, want, title string) {
		t.Helper()
		r, ok := sharedRecords(a, s.ID)[id]
		if !ok {
			t.Fatalf("no record of exception %s", id)
		}
		var fields struct {
			Title string `json:"title"`
		}
		json.Unmarshal(r.Content, &fields)
		if r.State != want || fields.Title != title {
			t.Errorf("exception %s is %q titled %q, want %q titled %q", id, r.State, fields.Title, want, title)
		}
	}

	ingest(`{"id": "1", "title": "Timeout"}`)
	state("1", "new", "Timeout")
	work(runDone)
	state("1", "completed", "Timeout")

	// Fetched again, it is refreshed but stays completed, so the query
	// passes it over.
	ingest(`{"id": "1", "title": "Timeout in checkout"}`)
	state("1", "completed", "Timeout in checkout")
	work(runFailed)
	if got := given[len(given)-1]; got != "[]" {
		t.Errorf("processing with nothing new was given %s", got)
	}

	// A run failing to complete one leaves it new for the next.
	ingest(`{"id": "2", "title": "Null pointer"}`)
	mu.Lock()
	fail = true
	mu.Unlock()
	work(runFailed)
	state("2", "new", "Null pointer")
	mu.Lock()
	fail = false
	mu.Unlock()
	work(runDone)
	state("2", "completed", "Null pointer")
	if n := len(sharedRecords(a, s.ID)); n != 2 {
		t.Errorf("the struct has %d records, want 2", n)
	}

	// The table of them has the object's fields as columns, and each
	// record names the flow of the run that last wrote it.
	var listed struct {
		Fields  []string `json:"fields"`
		Records []struct {
			RunFlow *int64 `json:"runFlow"`
		} `json:"records"`
		More bool `json:"more"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records", s.ID), nil, http.StatusOK, &listed)
	if strings.Join(listed.Fields, ",") != "id,title" || listed.More {
		t.Errorf("the records have the fields %v, more %v; want id, title and no more", listed.Fields, listed.More)
	}
	if len(listed.Records) == 0 || listed.Records[0].RunFlow == nil || *listed.Records[0].RunFlow != process.ID {
		t.Errorf("the latest record wasn't written by a run of flow %d", process.ID)
	}

	// Turned off, the copies keep to their flows' runs again.
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": false}, http.StatusOK, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", fetched.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	// And a struct whose copy is stored in its runs isn't shared.
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusBadRequest, "stored in its flow's runs")
}

// A setup file shares a struct's object, which needs a unique attribute,
// lets a copy of it be queried, and exports it shared again.
func TestSharedStructInSetup(t *testing.T) {
	a := setupApp(t)
	const file = `spoletta: 1
structs:
  - name: SHR Exception
    shared: true
    states: [new, completed]
    attributes:
      - name: id
        unique: true
flows:
  - name: SHR Process exceptions
    start:
      next: Complete
    objects:
      - copyOf: {flow: SHR Exception}
    processes:
      - name: Complete
        kind: script
        script: cat "$INPUTS/SHR Exception"
        takes:
          - object: SHR Exception
            query: {state: new, limit: 1}
        yields:
          - object: SHR Exception
            state: completed
`
	res := setupImportOK(a, file)
	var flow int64
	for _, f := range res.Flows {
		if f.Name == "SHR Process exceptions" {
			flow = f.ID
		}
	}
	if cp := modNamed(t, modObjects(a, flow), "SHR Exception"); !cp.Shared {
		t.Errorf("the imported copy = %+v, want it shared", cp)
	}
	if out := setupExport(a, "SHR "); !strings.Contains(out, "shared: true") {
		t.Errorf("the export doesn't share the struct's object:\n%s", out)
	}
	status, body := setupImport(a, strings.Replace(file, "        unique: true\n", "", 1))
	if status != http.StatusBadRequest || !strings.Contains(string(body), "unique attribute") {
		t.Errorf("importing a shared struct without a unique attribute = %d %s", status, body)
	}
}

// A shared list: a fetching script adds a record per item, new by its
// output link, and an agent querying one new record decides the state it
// comes out in, which a rule on every record doesn't undo.
func TestSharedListDecidedByAnAgent(t *testing.T) {
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Issue", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "count"}, http.StatusCreated, nil)
	a.newState(root.ID, "new")
	a.newState(root.ID, "completed")
	a.newState(root.ID, "failed")
	rulesState(a, root.ID, "seen", `id != ""`)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"many": true, "shared": true}, http.StatusOK, nil)
	stateOf := func(o modObject, name string) int64 {
		for _, st := range o.States {
			if st.Name == name {
				return st.ID
			}
		}
		t.Fatalf("%s has no state %s", o.Name, name)
		return 0
	}

	fetch := a.newFlow("Fetch issues")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": fetch.ID}, http.StatusCreated, nil)
	fetched := modNamed(t, modObjects(a, fetch.ID), "Issue")
	sentry := a.newProcess(fetch.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "fetch"})
	a.link(map[string]any{"process": sentry.ID, "object": fetched.ID, "direction": directionOutput, "state": stateOf(fetched, "new")})
	a.startAt(fetch.ID, sentry.ID)

	process := a.newFlow("Fix issues")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": process.ID}, http.StatusCreated, nil)
	taken := modNamed(t, modObjects(a, process.ID), "Issue")
	fix := a.agent(process.ID, "Fix", "Fix the issue.")
	a.link(map[string]any{"process": fix.ID, "object": taken.ID, "direction": directionInput, "query": linkQuery{State: "new", Limit: 1, Oldest: true}})
	decisionsDecides(a, fix.ID, taken.ID, stateOf(taken, "completed"), stateOf(taken, "failed"))
	a.startAt(process.ID, fix.ID)

	var mu sync.Mutex
	next := ""
	a.worker.onScript("Fetch", func(context.Context, scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		return runResult{OK: true, Output: next}
	})
	decide := "completed"
	a.worker.onAgent("Fix", func(context.Context, agentRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		return submitted(decisionsReply(nil, map[string]any{"Issue": decisionsState(decide)}))
	})
	ingest := func(out string) {
		t.Helper()
		mu.Lock()
		next = out
		mu.Unlock()
		if d := a.finish(a.startRun(fetch.ID).ID); d.Status != runDone {
			t.Fatalf("fetching ended %s; steps %s", d.Status, describeSteps(d))
		}
	}
	work := func(state string) {
		t.Helper()
		mu.Lock()
		decide = state
		mu.Unlock()
		if d := a.finish(a.startRun(process.ID).ID); d.Status != runDone {
			t.Fatalf("fixing ended %s; steps %s", d.Status, describeSteps(d))
		}
	}
	states := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for id, r := range sharedRecords(a, s.ID) {
			var fields struct {
				Count int `json:"count"`
			}
			json.Unmarshal(r.Content, &fields)
			out[id] = fmt.Sprintf("%s/%d", r.State, fields.Count)
		}
		return out
	}
	want := func(w map[string]string) {
		t.Helper()
		if got := states(); fmt.Sprint(got) != fmt.Sprint(w) {
			t.Errorf("the records = %v, want %v", got, w)
		}
	}

	ingest(`[{"id": "a", "count": 1}, {"id": "b", "count": 1}]`)
	want(map[string]string{"a": "new/1", "b": "new/1"})
	work("completed")
	want(map[string]string{"a": "completed/1", "b": "new/1"})
	ingest(`[{"id": "a", "count": 2}, {"id": "b", "count": 2}, {"id": "c", "count": 1}]`)
	want(map[string]string{"a": "completed/2", "b": "new/2", "c": "new/1"})
	work("failed")
	want(map[string]string{"a": "completed/2", "b": "failed/2", "c": "new/1"})
}
