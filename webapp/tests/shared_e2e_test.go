package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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
	parallel(t)
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

	// The records are searched and sorted on the server, by their state,
	// their fields and what wrote them, and the state facet counts every
	// state with one of them ticked.
	ingest(`{"id": "3", "title": "Disk full"}`)
	search := func(q, sort string) (ids []string, facets []searchFacet) {
		t.Helper()
		var rs struct {
			Records []sharedRecord `json:"records"`
			Facets  []searchFacet  `json:"facets"`
			Matched int            `json:"matched"`
		}
		a.call("GET", fmt.Sprintf("/api/structs/%d/records?q=%s&sort=%s", s.ID, url.QueryEscape(q), url.QueryEscape(sort)), nil, http.StatusOK, &rs)
		for _, r := range rs.Records {
			var fields struct {
				ID string `json:"id"`
			}
			json.Unmarshal(r.Content, &fields)
			ids = append(ids, fields.ID)
		}
		if rs.Matched != len(ids) {
			t.Errorf("%q matched %d records but listed %d", q, rs.Matched, len(ids))
		}
		return ids, rs.Facets
	}
	for _, c := range []struct{ query, sort, want string }{
		{"state:new", "", "3"},
		{"-state:new", "id", "1,2"},
		{"title:*null*", "", "2"},
		{"@id:1 OR @id:3", "-@title", "1,3"},
		{"", "@title", "3,2,1"},
		{"run:* -state:completed", "", "3"},
		{"timeout", "", "1"},
	} {
		if got, _ := search(c.query, c.sort); strings.Join(got, ",") != c.want {
			t.Errorf("records %q sorted by %q = %v, want %s", c.query, c.sort, got, c.want)
		}
	}
	if _, facets := search("state:new", ""); facetCounts(facets, "state") != "completed=2,new=1" {
		t.Errorf("state counts with new ticked = %s, want both states", facetCounts(facets, "state"))
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
	parallel(t)
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

// An import whose output link keeps the records already stored adds only
// the tickets that are new, leaving one it fetches again as it was, fields,
// history and when it was written, while a flow syncing them refreshes it.
// Only a process generating a shared object alone keeps what is stored; the
// run's version, OPL, the diff and the problems say so.
func TestSharedRecordsKeptAsStored(t *testing.T) {
	parallel(t)
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Ticket", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "summary"}, http.StatusCreated, nil)
	rulesState(a, root.ID, "new", `id != ""`)
	a.newState(root.ID, "done")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	use := func(f int64) modObject {
		a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f, "x": 0, "y": 0}, http.StatusCreated, nil)
		return modNamed(t, modObjects(a, f), "Ticket")
	}
	keep := func(l int64, on bool) {
		t.Helper()
		a.call("PUT", fmt.Sprintf("/api/links/%d/keep-stored", l), map[string]any{"keepStored": on}, http.StatusNoContent, nil)
	}

	imp := a.newFlow("Import tickets")
	imported := use(imp.ID)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", imported.ID), map[string]any{"many": true}, http.StatusOK, nil)
	importer := a.newProcess(imp.ID, map[string]any{"name": "Import", "kind": kindScript, "script": "import"})
	out := a.output(importer.ID, imported.ID)
	a.startAt(imp.ID, importer.ID)
	if out.KeepStored {
		t.Fatalf("a new link keeps the records already stored")
	}
	keep(out.ID, true)
	var links []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", imp.ID), nil, http.StatusOK, &links)
	if len(links) != 1 || !links[0].KeepStored {
		t.Fatalf("the import's links = %+v, want its output link keeping what is stored", links)
	}

	syncing := a.newFlow("Sync tickets")
	synced := use(syncing.ID)
	syncer := a.newProcess(syncing.ID, map[string]any{"name": "Sync", "kind": kindScript, "script": "sync"})
	a.output(syncer.ID, synced.ID)
	a.startAt(syncing.ID, syncer.ID)

	// Only a process generating a shared object alone keeps what is stored.
	closer := a.newProcess(syncing.ID, map[string]any{"name": "Close", "kind": kindScript, "script": "close"})
	in := a.link(map[string]any{"process": closer.ID, "object": synced.ID, "direction": directionInput, "query": linkQuery{State: "new"}})
	closed := a.output(closer.ID, synced.ID)
	a.refused("PUT", fmt.Sprintf("/api/links/%d/keep-stored", in.ID), map[string]any{"keepStored": true}, http.StatusBadRequest, "only a link to what the process generates")
	a.refused("PUT", fmt.Sprintf("/api/links/%d/keep-stored", closed.ID), map[string]any{"keepStored": true}, http.StatusBadRequest, "takes that object too")
	note := a.newObject(syncing.ID, "Note")
	plain := a.output(closer.ID, note.ID)
	a.refused("PUT", fmt.Sprintf("/api/links/%d/keep-stored", plain.ID), map[string]any{"keepStored": true}, http.StatusBadRequest, "shared struct's object")
	keep(closed.ID, false)

	var mu sync.Mutex
	outputs := map[string]string{}
	for _, name := range []string{"Import", "Sync"} {
		a.worker.onScript(name, func(context.Context, scriptRun) runResult {
			mu.Lock()
			defer mu.Unlock()
			return runResult{OK: true, Output: outputs[name]}
		})
	}
	runFlow := func(f int64, name, output string) run {
		t.Helper()
		mu.Lock()
		outputs[name] = output
		mu.Unlock()
		d := a.finish(a.startRun(f).ID)
		if d.Status != runDone {
			t.Fatalf("%s %s ended %s; steps %s", name, output, d.Status, describeSteps(d))
		}
		return d.run
	}
	summary := func(r sharedRecord) string {
		var fields struct {
			Summary string `json:"summary"`
		}
		json.Unmarshal(r.Content, &fields)
		return fields.Summary
	}
	type listed struct {
		sharedRecord
		Updated string `json:"updated"`
	}
	records := func() map[string]listed {
		t.Helper()
		var rs struct {
			Records []listed `json:"records"`
		}
		a.call("GET", fmt.Sprintf("/api/structs/%d/records", s.ID), nil, http.StatusOK, &rs)
		byID := map[string]listed{}
		for _, r := range rs.Records {
			var fields struct {
				ID string `json:"id"`
			}
			json.Unmarshal(r.Content, &fields)
			byID[fields.ID] = r
		}
		return byID
	}

	first := runFlow(imp.ID, "Import", `[{"id": "T-1", "summary": "Login fails"}]`)
	was := records()["T-1"]
	if summary(was.sharedRecord) != "Login fails" || was.State != "new" {
		t.Fatalf("the imported ticket = %s in %q", was.Content, was.State)
	}
	history := len(recordChangesOf(a, s.ID, was.ID))

	// Fetched again beside a new one, it is left as it was.
	runFlow(imp.ID, "Import", `[{"id": "T-1", "summary": "Login fails on Safari"}, {"id": "T-2", "summary": "Export is slow"}]`)
	now := records()
	if r := now["T-1"]; summary(r.sharedRecord) != "Login fails" || r.Updated != was.Updated || r.Run == nil || *r.Run != first.ID {
		t.Errorf("the ticket imported again = %s, updated %s by run %v; want it as run %d left it, updated %s", r.Content, r.Updated, r.Run, first.ID, was.Updated)
	}
	if n := len(recordChangesOf(a, s.ID, was.ID)); n != history {
		t.Errorf("the ticket imported again has %d changes, want %d", n, history)
	}
	if r, ok := now["T-2"]; !ok || summary(r.sharedRecord) != "Export is slow" {
		t.Errorf("the new ticket wasn't added: %+v", now)
	}

	// The sync refreshes it.
	runFlow(syncing.ID, "Sync", `{"id": "T-1", "summary": "Login fails on Safari"}`)
	if r := records()["T-1"]; summary(r.sharedRecord) != "Login fails on Safari" || r.State != "new" || r.Updated == was.Updated {
		t.Errorf("the synced ticket = %s in %q, updated %s; want it refreshed, still new", r.Content, r.State, r.Updated)
	}

	// The board says it as the run's version holds it.
	var opl struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", imp.ID), nil, http.StatusOK, &opl)
	if !strings.Contains(opl.Text, "adds only new records of") {
		t.Errorf("OPL doesn't say the import keeps what is stored:\n%s", opl.Text)
	}
	if first.Version == nil {
		t.Fatalf("the import's run has no version")
	}
	keep(out.ID, false)
	d := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", *first.Version))
	if len(d.Links) != 1 || len(d.Links[0].Fields) != 1 || d.Links[0].Fields[0].Field != "keeps stored records" || d.Links[0].Fields[0].Before != "yes" {
		t.Errorf("the diff after turning it off = %+v, want the link no longer keeping what is stored", d.Links)
	}
	keep(out.ID, true)

	// Taking the object too, it keeps nothing, which the problems say.
	a.input(importer.ID, imported.ID)
	var warned bool
	for _, p := range prbGet(a, imp.ID).Problems {
		warned = warned || (p.Rule == "keep-stored" && p.Level == "warning")
	}
	if !warned {
		t.Errorf("no warning of a link keeping what is stored whose process takes the object: %s", prbDescribe(prbGet(a, imp.ID).Problems))
	}
}

// A setup file keeps a link keeping the records already stored, and refuses
// one that can't.
func TestKeepStoredInSetup(t *testing.T) {
	parallel(t)
	a := setupApp(t)
	const file = `spoletta: 1
structs:
  - name: KPS Ticket
    shared: true
    attributes:
      - name: id
        unique: true
flows:
  - name: KPS Import
    start:
      next: Import
    objects:
      - copyOf: {flow: KPS Ticket}
    processes:
      - name: Import
        kind: script
        script: import
        yields:
          - object: KPS Ticket
            keepStored: true
`
	setupImportOK(a, file)
	if out := setupExport(a, "KPS "); !strings.Contains(out, "keepStored: true") {
		t.Errorf("the export doesn't keep what is stored:\n%s", out)
	}
	takes := strings.Replace(file, "        yields:\n", "        takes:\n          - KPS Ticket\n        yields:\n", 1)
	status, body := setupImport(a, takes)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "takes that object too") {
		t.Errorf("importing a link keeping what is stored whose process takes the object = %d %s", status, body)
	}
	input := strings.Replace(file, "        yields:\n", "        takes:\n", 1)
	status, body = setupImport(a, input)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "only a link to what the process generates") {
		t.Errorf("importing an input link keeping what is stored = %d %s", status, body)
	}
}

// A shared list: a fetching script adds a record per item, new by its
// output link, and an agent querying one new record decides the state it
// comes out in, which a rule on every record doesn't undo.
func TestSharedListDecidedByAnAgent(t *testing.T) {
	parallel(t)
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

// A person corrects a shared struct's records by hand: adds one a flow
// missed, changes another's fields and state, a field taken out staying
// out, and deletes them, one or many. A unique value another record holds
// is refused, and a run generating an edited record later refreshes it.
func TestSharedRecordsEditedByHand(t *testing.T) {
	parallel(t)
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Bug", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "title"}, http.StatusCreated, nil)
	a.newState(root.ID, "open")
	a.newState(root.ID, "fixed")
	records := fmt.Sprintf("/api/structs/%d/records", s.ID)

	a.refused("POST", records, map[string]any{"content": map[string]any{"id": "B1"}}, http.StatusBadRequest, "isn't shared")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)

	var added sharedRecord
	a.call("POST", records, map[string]any{"content": map[string]any{"id": "B1", "title": "Crash"}, "state": "open"}, http.StatusCreated, &added)
	a.call("POST", records, map[string]any{"content": map[string]any{"id": "B2", "title": "Hang", "note": "slow"}}, http.StatusCreated, nil)
	a.refused("POST", records, map[string]any{"content": map[string]any{"id": "B1"}}, http.StatusConflict, "holds that id")
	a.refused("POST", records, map[string]any{"content": "B3"}, http.StatusBadRequest, "JSON object")
	a.refused("POST", records, map[string]any{"content": map[string]any{"id": "B3"}, "state": "gone"}, http.StatusBadRequest, "no state")

	b2 := sharedRecords(a, s.ID)["B2"]
	edit := fmt.Sprintf("%s/%d", records, b2.ID)
	a.refused("PUT", edit, map[string]any{"content": map[string]any{"id": "B1"}}, http.StatusConflict, "holds that id")
	a.call("PUT", edit, map[string]any{"content": map[string]any{"id": "B9", "title": "Hang on start"}, "state": "fixed"}, http.StatusOK, nil)
	got := sharedRecords(a, s.ID)
	if _, ok := got["B2"]; ok || len(got) != 2 {
		t.Fatalf("records after renaming B2 to B9: %v", got)
	}
	if r := got["B9"]; r.State != "fixed" || strings.Contains(string(r.Content), "note") || !strings.Contains(string(r.Content), "Hang on start") {
		t.Fatalf("B9 is %s %s, want fixed without its note", r.State, r.Content)
	}
	// B2 is free again, as a run would find it.
	a.call("POST", records, map[string]any{"content": map[string]any{"id": "B2"}}, http.StatusCreated, nil)
	a.refused("PUT", fmt.Sprintf("%s/%d", records, 1<<40), map[string]any{"content": map[string]any{"id": "B7"}}, http.StatusNotFound, "no such record")

	// A run generating B9 refreshes the record edited by hand.
	f := a.newFlow("Fetch bugs")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	cp := modNamed(t, modObjects(a, f.ID), "Bug")
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "fetch"})
	a.output(fetch.ID, cp.ID)
	a.startAt(f.ID, fetch.ID)
	a.worker.onScript("Fetch", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: `{"id": "B9", "title": "Hang on start up"}`}
	})
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("fetching ended %s; steps %s", d.Status, describeSteps(d))
	}
	got = sharedRecords(a, s.ID)
	if r := got["B9"]; len(got) != 3 || r.State != "fixed" || !strings.Contains(string(r.Content), "Hang on start up") {
		t.Fatalf("after fetching B9 again: %v", got)
	}

	// Deleting one, then the rest at once; another struct's record stays.
	var other structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Task", "struct": true}, http.StatusCreated, &other)
	otherRoot := modObjects(a, other.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", otherRoot.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", otherRoot.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	var task sharedRecord
	a.call("POST", fmt.Sprintf("/api/structs/%d/records", other.ID), map[string]any{"content": map[string]any{"id": "T1"}}, http.StatusCreated, &task)

	var deleted struct {
		Deleted int `json:"deleted"`
	}
	a.call("POST", records+"/delete", map[string]any{"ids": []int64{added.ID}}, http.StatusOK, &deleted)
	if deleted.Deleted != 1 || len(sharedRecords(a, s.ID)) != 2 {
		t.Fatalf("deleted %d, left %v", deleted.Deleted, sharedRecords(a, s.ID))
	}
	var ids []int64
	for _, r := range sharedRecords(a, s.ID) {
		ids = append(ids, r.ID)
	}
	a.call("POST", records+"/delete", map[string]any{"ids": append(ids, task.ID)}, http.StatusOK, &deleted)
	if deleted.Deleted != 2 || len(sharedRecords(a, s.ID)) != 0 || len(sharedRecords(a, other.ID)) != 1 {
		t.Fatalf("deleted %d, left %v and %v", deleted.Deleted, sharedRecords(a, s.ID), sharedRecords(a, other.ID))
	}
	a.refused("POST", records+"/delete", map[string]any{"ids": []int64{}}, http.StatusBadRequest, "name the records")
	// B1 is free again.
	a.call("POST", records, map[string]any{"content": map[string]any{"id": "B1"}}, http.StatusCreated, nil)
}
