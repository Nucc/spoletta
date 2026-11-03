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
	close(release)
	d2 := a.finish(rn2.ID)
	if failed := onlyStep(t, d2, "Move"); d2.Status != runFailed || !strings.Contains(failed.Error, "by someone else") {
		t.Errorf("second run ended %s, Move %q; want it failed on T-1 moved", d2.Status, failed.Error)
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
