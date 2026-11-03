package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A Pull Request struct, shared and told apart by its url, whose ticket
// refers to the Ticket struct's records; copied onto flow.
func refPullRequests(a *app, tickets structFlow, flow int64) (structFlow, modObject) {
	a.t.Helper()
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Pull Request", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	ticketRoot := modObjects(a, tickets.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "id", "unique": true}, http.StatusCreated, nil)
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "key", "unique": true, "ref": ticketRoot.ID},
		http.StatusBadRequest, "can't be unique too")
	a.refused("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "key", "ref": root.ID},
		http.StatusBadRequest, "isn't shared")
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "ticket", "ref": ticketRoot.ID}, http.StatusCreated, nil)
	a.newState(root.ID, "open")
	a.newState(root.ID, "merged")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": flow, "x": 0, "y": 200}, http.StatusCreated, nil)
	return s, modNamed(a.t, modObjects(a, flow), "Pull Request")
}

func refMerge(a *app, s, record int64, id, ticket string) {
	a.t.Helper()
	a.call("PUT", fmt.Sprintf("/api/structs/%d/records/%d", s, record),
		map[string]any{"content": map[string]string{"id": id, "ticket": ticket}, "state": "merged"}, http.StatusOK, nil)
}

func refAddPR(a *app, s int64, id, ticket string) int64 {
	a.t.Helper()
	var r sharedRecord
	a.call("POST", fmt.Sprintf("/api/structs/%d/records", s),
		map[string]any{"content": map[string]string{"id": id, "ticket": ticket}, "state": "open"}, http.StatusCreated, &r)
	return r.ID
}

// A pull request's ticket refers to a Ticket record by its id: a record
// naming a ticket there isn't is refused. A flow picking a pull request and
// following its ticket has every run take the ticket the pull request names,
// unheld, so two pull requests of one ticket run at once, and the process
// changing it moves that ticket, failing if someone else moved it
// meanwhile. A pull request naming no ticket starts no run. The ticket's
// dialog lists the pull requests referring to it, and the setup file and
// OPL keep the reference and the follow.
func TestRecordReferences(t *testing.T) {
	a := newApp(t)
	tickets, f, ticket := pickTickets(a, "Merge pull requests")
	prs, pr := refPullRequests(a, tickets, f.ID)

	t1 := pickAdd(a, tickets.ID, "T-1", "open")
	pickAdd(a, tickets.ID, "T-2", "open")
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records", prs.ID),
		map[string]any{"content": map[string]string{"id": "PR-0", "ticket": "T-9"}, "state": "open"}, http.StatusBadRequest, `ticket names "T-9", but no "Ticket" record holds it`)

	follow := func(object int64, f any) string {
		t.Helper()
		status, reply := a.do("PUT", fmt.Sprintf("/api/objects/%d/follows", object), map[string]any{"follows": f})
		if status != http.StatusNoContent {
			return strings.TrimSpace(string(reply))
		}
		return ""
	}
	if msg := follow(ticket.ID, map[string]any{"object": pr.ID, "attribute": "ticket"}); !strings.Contains(msg, "picking the record") {
		t.Errorf("following a Pull Request that picks nothing = %q, want it refused", msg)
	}
	a.call("PUT", fmt.Sprintf("/api/objects/%d/pick", pr.ID), map[string]any{"pick": map[string]any{"state": "open", "oldest": true}}, http.StatusNoContent, nil)
	if msg := follow(ticket.ID, map[string]any{"object": pr.ID, "attribute": "id"}); !strings.Contains(msg, "no attribute") {
		t.Errorf("following an attribute that refers to nothing = %q, want it refused", msg)
	}
	if msg := follow(ticket.ID, map[string]any{"object": pr.ID, "attribute": "ticket"}); msg != "" {
		t.Fatalf("following the Pull Request's ticket: %s", msg)
	}

	move := a.newProcess(f.ID, map[string]any{"name": "Move", "kind": kindScript, "script": "move"})
	a.link(map[string]any{"process": move.ID, "object": pr.ID, "direction": directionInput, "role": "instrument"})
	a.input(move.ID, ticket.ID)
	a.link(map[string]any{"process": move.ID, "object": ticket.ID, "direction": directionOutput, "state": pickState(t, ticket, "review")})
	a.startAt(f.ID, move.ID)

	var mu sync.Mutex
	var given []string
	release := make(chan struct{})
	a.worker.onScript("Move", func(ctx context.Context, run scriptRun) runResult {
		fields, err := pickGiven(run)
		if err != nil {
			return runResult{Error: err.Error()}
		}
		mu.Lock()
		given = append(given, fields["id"])
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
		fields["status"] = "in review"
		out, _ := json.Marshal(fields)
		return runResult{OK: true, Output: string(out)}
	})

	// Two pull requests of T-1: both runs start, each following T-1.
	pr1 := refAddPR(a, prs.ID, "PR-1", "T-1")
	pr2 := refAddPR(a, prs.ID, "PR-2", "T-1")
	rn1 := a.startRun(f.ID)
	a.waitStep(rn1.ID, "Move", stepActive)
	rn2 := a.startRun(f.ID)
	a.waitStep(rn2.ID, "Move", stepActive)
	mu.Lock()
	if len(given) != 2 || given[0] != "T-1" || given[1] != "T-1" {
		t.Errorf("Move was given %v, want T-1 twice", given)
	}
	mu.Unlock()

	// T-1's dialog lists both pull requests, each with its run going.
	var groups []struct {
		Name      string `json:"name"`
		Attribute string `json:"attribute"`
		Records   []struct {
			Content json.RawMessage `json:"content"`
			Going   bool            `json:"going"`
		} `json:"records"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records/%d/references", tickets.ID, t1), nil, http.StatusOK, &groups)
	if len(groups) != 1 || groups[0].Name != "Pull Request" || groups[0].Attribute != "ticket" || len(groups[0].Records) != 2 || !groups[0].Records[0].Going {
		t.Errorf("T-1's references = %+v, want both pull requests going", groups)
	}

	// The first run moves T-1 to review; the second then finds it moved
	// by someone else and fails rather than writing over it.
	release <- struct{}{}
	if d := a.finish(rn1.ID); d.Status != runDone {
		t.Fatalf("first run ended %s: %s", d.Status, describeSteps(d))
	}
	if r := sharedRecords(a, tickets.ID)["T-1"]; r.State != "review" || !strings.Contains(string(r.Content), "in review") {
		t.Errorf("T-1 = %s in %s, want it in review", r.Content, r.State)
	}
	// The pull requests are searched through their ticket: its state and
	// its fields.
	for q, want := range map[string]int{"@ticket.state:review": 2, "@ticket.state:open": 0, `@ticket.status:"in review"`: 2, "-@ticket.state:review": 0} {
		var found struct {
			Records []sharedRecord `json:"records"`
		}
		a.call("GET", fmt.Sprintf("/api/structs/%d/records?q=%s", prs.ID, url.QueryEscape(q)), nil, http.StatusOK, &found)
		if len(found.Records) != want {
			t.Errorf("searching the pull requests for %s found %d, want %d", q, len(found.Records), want)
		}
	}
	close(release)
	d2 := a.finish(rn2.ID)
	if failed := onlyStep(t, d2, "Move"); d2.Status != runFailed || !strings.Contains(failed.Error, "by someone else") {
		t.Errorf("second run ended %s, Move %q; want it failed on T-1 moved", d2.Status, failed.Error)
	}

	// A task board over the tickets counts each one's pull requests by
	// state on its card.
	var board struct {
		ID      int64 `json:"id"`
		Columns []struct {
			Cards struct {
				Records   []sharedRecord `json:"records"`
				Referring map[string][]struct {
					Name   string `json:"name"`
					Total  int    `json:"total"`
					States []struct {
						State string `json:"state"`
						Count int    `json:"count"`
					} `json:"states"`
				} `json:"referring"`
			} `json:"cards"`
		} `json:"columns"`
	}
	a.call("POST", "/api/taskboards", map[string]any{"name": "Tickets"}, http.StatusCreated, &board)
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", board.ID), map[string]any{"name": "Tickets", "columns": []any{
		map[string]any{"name": "All", "struct": tickets.ID, "query": ""},
	}}, http.StatusOK, nil)
	refMerge(a, prs.ID, pr2, "PR-2", "T-1")
	a.call("GET", fmt.Sprintf("/api/taskboards/%d", board.ID), nil, http.StatusOK, &board)
	counts := board.Columns[0].Cards.Referring[fmt.Sprint(t1)]
	if len(counts) != 1 || counts[0].Name != "Pull Request" || counts[0].Total != 2 || len(counts[0].States) != 2 {
		t.Errorf("T-1's card counts %+v, want 2 pull requests, one merged and one open", counts)
	}
	if len(board.Columns[0].Cards.Referring) != 1 {
		t.Errorf("cards counting references: %v, want T-1's alone", board.Columns[0].Cards.Referring)
	}

	// A reference may hold a list, each of which must name a record; the
	// records it names each list it, and a search reaches through each.
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records", prs.ID),
		map[string]any{"content": map[string]any{"id": "PR-4", "ticket": []string{"T-1", "T-9"}}, "state": "merged"}, http.StatusBadRequest, `ticket names "T-9"`)
	a.call("POST", fmt.Sprintf("/api/structs/%d/records", prs.ID),
		map[string]any{"content": map[string]any{"id": "PR-4", "ticket": []string{"T-1", "T-2"}}, "state": "merged"}, http.StatusCreated, nil)
	t2 := sharedRecords(a, tickets.ID)["T-2"].ID
	a.call("GET", fmt.Sprintf("/api/structs/%d/records/%d/references", tickets.ID, t2), nil, http.StatusOK, &groups)
	if len(groups) != 1 || len(groups[0].Records) != 1 || !strings.Contains(string(groups[0].Records[0].Content), "PR-4") {
		t.Errorf("T-2's references = %+v, want PR-4", groups)
	}
	var found struct {
		Records []sharedRecord `json:"records"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records?q=%s", prs.ID, url.QueryEscape("@ticket.state:open")), nil, http.StatusOK, &found)
	if len(found.Records) != 1 {
		t.Errorf("pull requests with an open ticket: %d, want PR-4 alone, through T-2", len(found.Records))
	}
	a.call("GET", fmt.Sprintf("/api/taskboards/%d", board.ID), nil, http.StatusOK, &board)
	if counts := board.Columns[0].Cards.Referring[fmt.Sprint(t1)]; len(counts) != 1 || counts[0].Total != 3 {
		t.Errorf("T-1's card counts %+v, want 3 pull requests with PR-4", counts)
	}

	// A pull request naming no ticket starts no run.
	refMerge(a, prs.ID, pr1, "PR-1", "T-1")
	refMerge(a, prs.ID, pr2, "PR-2", "T-1")
	refAddPR(a, prs.ID, "PR-3", "")
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, `names no "Ticket" in ticket`)

	var opl struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil, http.StatusOK, &opl)
	if !strings.Contains(opl.Text, `"Ticket" is the record that "Pull Request"'s ticket names.`) {
		t.Errorf("OPL doesn't say which ticket each run follows:\n%s", opl.Text)
	}
	var setup struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", f.ID), nil, http.StatusOK, &setup)
	if !strings.Contains(setup.Text, "follows:") {
		t.Errorf("the setup file leaves the follow out:\n%s", setup.Text)
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", prs.ID), nil, http.StatusOK, &setup)
	if !strings.Contains(setup.Text, "ref: Ticket") {
		t.Errorf("the struct's setup file leaves the reference out:\n%s", setup.Text)
	}
	if ps := prbGet(a, f.ID).Problems; prbHas(ps, "error", "follow", ticket.ID, "") {
		t.Errorf("problems %s, want none about following", prbDescribe(ps))
	}
}

// A setup file names the struct an attribute refers to and the reference
// an object follows, and importing it makes a board with no problems with
// them, which exports as it was written.
func TestRecordReferencesSetup(t *testing.T) {
	a := newApp(t)
	file := `spoletta: 1
structs:
  - name: REF Ticket
    shared: true
    states: [open, review]
    attributes:
      - {name: key, unique: true}
  - name: REF Pull Request
    shared: true
    states: [open, merged]
    attributes:
      - {name: url, unique: true}
      - {name: ticket, ref: REF Ticket}
flows:
  - name: REF Merge
    start: {next: Move}
    processes:
      - name: Move
        kind: script
        script: move
        takes: [{object: REF Pull Request, role: instrument}, REF Ticket]
        yields: [{object: REF Ticket, state: review}]
    objects:
      - copyOf: {flow: REF Ticket}
        follows: {object: REF Pull Request, attribute: ticket}
      - copyOf: {flow: REF Pull Request}
        pick: {state: open}
`
	res := setupImportOK(a, file)
	var merge int64
	for _, f := range res.Flows {
		if f.Name == "REF Merge" {
			merge = f.ID
		}
	}
	if merge == 0 {
		t.Fatalf("imported %+v, want REF Merge among them", res.Flows)
	}
	if ps := prbGet(a, merge).Problems; len(ps) > 0 && slices.ContainsFunc(ps, func(p prbProblem) bool { return p.Level == "error" }) {
		t.Errorf("problems %s, want no errors", prbDescribe(ps))
	}
	var setup struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", merge), nil, http.StatusOK, &setup)
	if !strings.Contains(setup.Text, "follows:") || !strings.Contains(setup.Text, "object: REF Pull Request") {
		t.Errorf("the flow's setup leaves the follow out:\n%s", setup.Text)
	}

	// Following a reference from an object that picks nothing is refused.
	bad := strings.Replace(file, "        pick: {state: open}\n", "", 1)
	if status, body := setupImport(a, bad); status == http.StatusOK || !strings.Contains(string(body), "picking a record") {
		t.Errorf("importing a follow of an object picking nothing = %d %s, want it refused", status, body)
	}
}

// A plain object a run is given as it starts, such as the key of the
// ticket a person enters, may refer to a struct's records, and a copy of the
// struct may follow it: the run then takes the ticket the key names and the
// process changing it writes that ticket. A key naming no ticket is refused
// before the run starts, a run given nothing has nothing to follow, and a
// step generating such an object naming no ticket fails.
func TestFollowGivenReference(t *testing.T) {
	a := newApp(t)
	tickets, f, ticket := pickTickets(a, "Triage an entered ticket")
	ticketRoot := modObjects(a, tickets.ID)[0]
	pickAdd(a, tickets.ID, "T-1", "open")

	entered := a.newObject(f.ID, "Entered")
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", entered.ID), map[string]any{"name": "key", "ref": ticketRoot.ID}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", entered.ID), map[string]any{"name": "note"}, http.StatusCreated, nil)
	a.call("PUT", fmt.Sprintf("/api/objects/%d/follows", ticket.ID), map[string]any{"follows": map[string]any{"object": entered.ID, "attribute": "key"}}, http.StatusNoContent, nil)

	triage := a.newProcess(f.ID, map[string]any{"name": "Triage", "kind": kindScript, "script": "triage"})
	a.input(triage.ID, entered.ID)
	a.input(triage.ID, ticket.ID)
	a.link(map[string]any{"process": triage.ID, "object": ticket.ID, "direction": directionOutput, "state": pickState(t, ticket, "review")})
	a.startAt(f.ID, triage.ID)
	a.worker.onScript("Triage", func(ctx context.Context, run scriptRun) runResult {
		fields, err := pickGiven(run)
		if err != nil {
			return runResult{Error: err.Error()}
		}
		fields["status"] = "triaged"
		out, _ := json.Marshal(fields)
		return runResult{OK: true, Output: string(out)}
	})

	start := func(key string) (int, string, run) {
		t.Helper()
		status, reply := a.do("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), map[string]any{"inputs": map[string]any{"Entered": map[string]string{"key": key, "note": "x"}}})
		var rn run
		if status == http.StatusCreated {
			json.Unmarshal(reply, &rn)
		}
		return status, strings.TrimSpace(string(reply)), rn
	}
	if status, msg, _ := start("T-9"); status != http.StatusUnprocessableEntity || !strings.Contains(msg, `no "Ticket" record holds it`) {
		t.Errorf("a run given a key naming no ticket = %d %q, want it refused", status, msg)
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, "neither picked a record for nor was given")

	status, msg, rn := start("T-1")
	if status != http.StatusCreated {
		t.Fatalf("a run given T-1 = %d %s", status, msg)
	}
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Fatalf("the run ended %s: %s", d.Status, describeSteps(d))
	}
	if r := sharedRecords(a, tickets.ID)["T-1"]; r.State != "review" || !strings.Contains(string(r.Content), "triaged") {
		t.Errorf("T-1 = %s in %s, want it triaged in review", r.Content, r.State)
	}

	// A step generating such an object naming no ticket fails.
	g := a.newFlow("Name a ticket")
	named := a.newObject(g.ID, "Named")
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", named.ID), map[string]any{"name": "key", "ref": ticketRoot.ID}, http.StatusCreated, nil)
	name := a.newProcess(g.ID, map[string]any{"name": "Name", "kind": kindScript, "script": "name"})
	a.output(name.ID, named.ID)
	a.startAt(g.ID, name.ID)
	a.worker.onScript("Name", func(ctx context.Context, run scriptRun) runResult {
		return runResult{OK: true, Output: `{"key": "T-404"}`}
	})
	d := a.finish(a.startRun(g.ID).ID)
	if failed := onlyStep(t, d, "Name"); d.Status != runFailed || !strings.Contains(failed.Error, `no "Ticket" record holds it`) {
		t.Errorf("a step naming no ticket ended the run %s, Name %q; want it failed", d.Status, failed.Error)
	}
}
