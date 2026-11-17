package tests

// Runs and records in a project of their own: a flow run to the end in a
// second project leaves every row it writes there, the worker is told the
// project with each workspace and diff, an API key, a webhook, a schedule
// and a task board start runs only in their own project, an archived
// project starts none, a run taken over keeps its project, and nothing of
// one project is reached from another, by a board naming it, a record's id
// or a key. See run_projects.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// Moves the flows, procedures and structs into the project, with what
// their boards, versions, schedules and webhooks hold, as if they had been
// made there. Boards are built in Legacy, whose routes all name it, and
// moved on a connection of their own with the foreign keys unchecked, as
// deleting a project empties it: a stopgap for as long as routes making a
// board don't name the project.
func prMove(t *testing.T, db *sql.DB, project int64, flows ...int64) {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1")
	in := "(?" + strings.Repeat(", ?", len(flows)-1) + ")"
	processes := "(SELECT id FROM processes WHERE flow_id IN " + in + ")"
	objects := "(SELECT id FROM objects WHERE flow_id IN " + in + ")"
	where := map[string]string{
		"flows":         "id IN " + in,
		"object_states": "object_id IN " + objects, "object_attributes": "object_id IN " + objects,
		"object_links": "process_id IN " + processes, "connections": "source_id IN " + processes,
		"inner_boards": "process_id IN " + processes, "start_targets": "process_id IN " + processes,
		"process_samples": "process_id IN " + processes,
		"link_states":     "link_id IN (SELECT l.id FROM object_links l JOIN processes p ON p.id = l.process_id WHERE p.flow_id IN " + in + ")",
		"version_roles":   "version_id IN (SELECT id FROM flow_versions WHERE flow_id IN " + in + ")",
	}
	for _, table := range []string{"flow_versions", "flow_history", "flow_starts", "flow_tags", "favourite_flows", "board_changes",
		"board_viewers", "assistant_messages", "notes", "schedules", "webhooks", "processes", "objects"} {
		where[table] = "flow_id IN " + in
	}
	// Those reading the flows' rows first, before these move.
	order := []string{"link_states", "version_roles", "object_states", "object_attributes", "object_links", "connections",
		"inner_boards", "start_targets", "process_samples"}
	for table := range where {
		if !slices.Contains(order, table) {
			order = append(order, table)
		}
	}
	args := []any{project}
	for _, f := range flows {
		args = append(args, f)
	}
	for _, table := range order {
		if _, err := conn.ExecContext(ctx, "UPDATE `"+table+"` SET project_id = ? WHERE "+where[table], args...); err != nil {
			t.Fatalf("move %s: %v", table, err)
		}
	}
}

// Saves the board of each flow as a version, which runs of it then take as
// it is, so starting one saves none.
func prSave(a *app, flows ...int64) {
	a.t.Helper()
	for _, f := range flows {
		a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f), map[string]any{}, http.StatusCreated, nil)
	}
}

// The run and the runs inside it, of its flow processes and subflows.
func prRunsOf(t *testing.T, db *sql.DB, run int64) []int64 {
	t.Helper()
	runs := []int64{run}
	for next := []int64{run}; len(next) > 0; {
		var found []int64
		rows, err := db.Query("SELECT id FROM runs WHERE parent_run_id IN (?"+strings.Repeat(", ?", len(next)-1)+")", int64Any(next)...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			found = append(found, id)
		}
		rows.Close()
		runs, next = append(runs, found...), found
	}
	return runs
}

func int64Any(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// What the parts working on the model proxy, the audit log and notices
// write of a run; this part's tests leave them out.
var prOthersTables = []string{"llm_usage", "proxy_grants", "audit_events", "notifications"}

// How many rows each table holds of the runs in the project, by table,
// failing the test for any row of them in another project: the runs
// themselves, every table naming a run, those naming a run's step, and
// the keys of what the runs stored.
func prRowsOf(t *testing.T, db *sql.DB, project int64, runs []int64) map[string]int {
	t.Helper()
	in := "(?" + strings.Repeat(", ?", len(runs)-1) + ")"
	where := map[string]string{
		"runs":               "id IN " + in,
		"run_step_messages":  "step_id IN (SELECT id FROM run_steps WHERE run_id IN " + in + ")",
		"run_step_output":    "step_id IN (SELECT id FROM run_steps WHERE run_id IN " + in + ")",
		"run_step_followups": "step_id IN (SELECT id FROM run_steps WHERE run_id IN " + in + ")",
		"instance_keys":      "instance_id IN (SELECT id FROM object_instances WHERE run_id IN " + in + ")",
	}
	rows, err := db.Query(`SELECT c.TABLE_NAME FROM information_schema.COLUMNS c JOIN information_schema.COLUMNS p
		ON p.TABLE_SCHEMA = c.TABLE_SCHEMA AND p.TABLE_NAME = c.TABLE_NAME AND p.COLUMN_NAME = 'project_id'
		WHERE c.TABLE_SCHEMA = DATABASE() AND c.COLUMN_NAME = 'run_id'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(prOthersTables, table) {
			where[table] = "run_id IN " + in
		}
	}
	rows.Close()
	counts := map[string]int{}
	for table, w := range where {
		var here, elsewhere int
		args := append([]any{project, project}, int64Any(runs)...)
		if err := db.QueryRow("SELECT COALESCE(SUM(project_id = ?), 0), COALESCE(SUM(project_id <> ?), 0) FROM `"+table+"` WHERE "+w, args...).
			Scan(&here, &elsewhere); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if elsewhere > 0 {
			t.Errorf("%d rows of %s the runs %v wrote are in another project than %d", elsewhere, table, runs, project)
		}
		counts[table] = here
	}
	return counts
}

// The id of the flow imported by the name.
func prImported(t *testing.T, res setupImported, name string) int64 {
	t.Helper()
	for _, f := range res.Flows {
		if f.Name == name {
			return f.ID
		}
	}
	t.Fatalf("imported %+v, without %s", res.Flows, name)
	return 0
}

// A flow working on the ticket a change it picks names: Move moves the
// ticket to review, which Write drafts from and Mark stamps, a person
// approves the draft, and a procedure summarises it. Shared tickets and
// changes, each change referring to its ticket; the stamp persistent and
// unique. Named apart by n.
func prBoardFile(n int64) string {
	return strings.ReplaceAll(`spoletta: 1
structs:
  - name: PR Ticket N
    shared: true
    states: [open, review, done]
    attributes:
      - {name: id, unique: true}
      - {name: status}
  - name: PR Change N
    shared: true
    states: [open, merged]
    attributes:
      - {name: url, unique: true}
      - {name: ticket, ref: PR Ticket N}
procedures:
  - name: PR Summarise N
    start: {next: Sum}
    objects:
      - name: Draft
      - name: Summary
        end: true
    processes:
      - name: Sum
        kind: script
        script: sum
        takes: [Draft]
        yields: [Summary]
flows:
  - name: PR Work N
    repositories:
      - name: app
        url: https://github.com/acme/app.git
    start: {next: Move}
    objects:
      - name: Editor
        human: true
      - name: Draft
        states: [approved, rejected]
      - name: Summary
      - name: Stamp
        persistent: true
        attributes:
          - {name: id, unique: true}
      - copyOf: {flow: PR Ticket N}
        follows: {object: PR Change N, attribute: ticket}
      - copyOf: {flow: PR Change N}
        pick: {state: open}
    processes:
      - name: Move
        kind: script
        script: move
        takes: [{object: PR Change N, role: instrument}, PR Ticket N]
        yields: [{object: PR Ticket N, state: review}]
      - name: Write
        prompt: Write a draft of the ticket.
        takes: [{object: PR Ticket N, state: review}]
        yields: [Draft]
      - name: Mark
        kind: script
        script: mark
        takes: [{object: PR Ticket N, state: review}]
        yields: [Stamp]
      - name: Approve
        kind: human
        takes: [Editor, Draft]
        yields:
          - object: Draft
            states: [approved, rejected]
      - name: Summarise
        kind: subflow
        runs: PR Summarise N
        takes: [{object: Draft, state: approved}]
        yields: [Summary]
`, " N", fmt.Sprintf(" %d", n))
}

// A flow built and run to the end in a second project: a script moving the
// ticket the change it picked names, an agent working in a workspace, a
// person's review with a comment, a procedure, a persistent object, shared
// records, a follow-up and the step's changes. Every row the run and the
// runs inside it write is in the project, the records it writes and their
// history too, the worker is told the project with each workspace and
// diff, and Legacy reaches none of it.
func TestProjectsRunAFlowInASecondProject(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	res := setupImportOK(a, prBoardFile(n))
	ticketName := fmt.Sprintf("PR Ticket %d", n)
	tickets, changes := prImported(t, res, ticketName), prImported(t, res, fmt.Sprintf("PR Change %d", n))
	summarise, work := prImported(t, res, fmt.Sprintf("PR Summarise %d", n)), prImported(t, res, fmt.Sprintf("PR Work %d", n))
	prSave(a, summarise, work)
	p := newProject(a, "Runs")
	prMove(t, db, p.ID, tickets, changes, summarise, work)
	a.project = p.ID

	// Records made by hand there are its own, and a change names a ticket
	// of its own.
	taskRecord(a, tickets, map[string]any{"id": "T-1", "status": "new"}, "open")
	taskRecord(a, changes, map[string]any{"url": "C-1", "ticket": "T-1"}, "open")

	commit := func(n int) string { return strings.Repeat(fmt.Sprint(n), 40) }
	a.worker.mu.Lock()
	a.worker.scriptOutput["Move"] = [][2]string{{"stdout", "moving the ticket\n"}}
	a.worker.diffReply = func(map[string]string) map[string]any {
		return map[string]any{"files": []map[string]any{{"path": "draft.md", "status": "added", "additions": 1}}, "more": 0}
	}
	a.worker.mu.Unlock()
	a.worker.onScript("Move", func(_ context.Context, run scriptRun) runResult {
		for _, in := range run.Inputs {
			if in.Name == ticketName {
				var fields map[string]string
				if err := json.Unmarshal([]byte(in.Content), &fields); err != nil {
					return runResult{Error: err.Error()}
				}
				fields["status"] = "moved"
				out, _ := json.Marshal(fields)
				return runResult{OK: true, Output: string(out)}
			}
		}
		return runResult{Error: fmt.Sprintf("given no ticket but %+v", run.Inputs)}
	})
	a.worker.onScript("Mark", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"id": "S-1"}`} })
	a.worker.onScript("Sum", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"data": "in short"}`} })
	a.worker.onAgent("Write", func(_ context.Context, run agentRun) runResult {
		if run.Resume {
			return runResult{OK: true, Output: "pushed", Commits: []repoCommits{{Name: "app", Before: commit(2), After: commit(3), Commits: []string{}}}}
		}
		out := submitted(data("the draft"))
		out.Commits = []repoCommits{{Name: "app", Before: commit(1), After: commit(2), Commits: []string{}}}
		return out
	})

	rn := a.startRun(work)
	review := a.waitStep(rn.ID, "Approve", stepWaiting)
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/comments", rn.ID, review.ID), map[string]any{"text": "Reads well."}, http.StatusCreated, nil)
	draft := modNamed(t, modObjects(a, work), "Draft")
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": pickState(t, draft, "approved")})
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("the run ended %s: %s", d.Status, describeSteps(d))
	}
	if s := onlyStep(t, d, "Summarise"); s.ChildRun == nil || !strings.Contains(fmt.Sprint(contentsOf(d, "Summary")), "in short") {
		t.Errorf("the procedure ran %v, leaving Summary %q", s.ChildRun, contentsOf(d, "Summary"))
	}
	if r := sharedRecords(a, tickets)["T-1"]; r.State != "review" || !strings.Contains(string(r.Content), "moved") {
		t.Errorf("T-1 = %s in %s, want it moved to review", r.Content, r.State)
	}

	// A follow-up of the agent, and what its step changed, go on in the
	// project too.
	write := onlyStep(t, d, "Write")
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, write.ID), map[string]any{"message": "Push it."}, http.StatusCreated, nil)
	a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
		s := onlyStep(t, d, "Write")
		return len(s.FollowUps) == 1 && s.FollowUps[0].Status == stepDone
	})
	var spans []repoChanges
	a.call("GET", fmt.Sprintf("/api/runs/%d/steps/%d/changes", d.ID, write.ID), nil, http.StatusOK, &spans)
	if len(spans) != 1 || len(spans[0].Files) != 1 || spans[0].Error != "" {
		t.Errorf("the step's changes: %+v", spans)
	}

	// The worker was told the project with every workspace and diff.
	project := fmt.Sprint(p.ID)
	writes := a.worker.runsOf("Write")
	if len(writes) != 2 || !writes[1].Resume {
		t.Fatalf("Write ran %d times, want once and its follow-up", len(writes))
	}
	for _, w := range writes {
		if w.Workspace == nil || w.Workspace.Project != p.ID {
			t.Errorf("Write's workspace %+v, want project %d's", w.Workspace, p.ID)
		}
	}
	for _, s := range a.worker.scriptRunsSoFar() {
		if (s.ProcessName == "Move" || s.ProcessName == "Mark") && (s.Workspace == nil || s.Workspace.Project != p.ID) {
			t.Errorf("%s's workspace %+v, want project %d's", s.ProcessName, s.Workspace, p.ID)
		}
	}
	a.worker.mu.Lock()
	diffs := slices.Clone(a.worker.diffs)
	a.worker.mu.Unlock()
	if len(diffs) == 0 {
		t.Error("the worker was asked for no diff")
	}
	for _, df := range diffs {
		if df["project"] != project {
			t.Errorf("the worker was asked for the diff %v, want project %s's", df, project)
		}
	}

	// Every row the runs wrote is in the project, and each kind there is.
	runs := prRunsOf(t, db, rn.ID)
	if len(runs) != 2 {
		t.Errorf("the run and those inside it: %v, want the run and the procedure's", runs)
	}
	counts := prRowsOf(t, db, p.ID, runs)
	for _, table := range []string{"runs", "run_steps", "run_objects", "run_logs", "run_step_messages", "run_step_output", "run_step_followups",
		"object_instances", "instance_keys", "taken_records", "review_comments", "shared_records", "record_changes"} {
		if counts[table] == 0 {
			t.Errorf("the runs left no rows of %s in the project; rows there: %v", table, counts)
		}
	}
	var records, keys, history, elsewhere int
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(project_id <> ?), 0) FROM shared_records WHERE object_id IN (SELECT id FROM objects WHERE flow_id IN (?, ?))`,
		p.ID, tickets, changes).Scan(&records, &elsewhere); err != nil || records < 2 || elsewhere > 0 {
		t.Errorf("the stores hold %d records, %d of another project (%v)", records, elsewhere, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(project_id <> ?), 0) FROM shared_keys WHERE object_id IN (SELECT id FROM objects WHERE flow_id IN (?, ?))`,
		p.ID, tickets, changes).Scan(&keys, &elsewhere); err != nil || keys < 2 || elsewhere > 0 {
		t.Errorf("the stores' records have %d keys, %d of another project (%v)", keys, elsewhere, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(c.project_id <> ?), 0) FROM record_changes c JOIN shared_records sr ON sr.id = c.record_id
		WHERE sr.object_id IN (SELECT id FROM objects WHERE flow_id IN (?, ?))`, p.ID, tickets, changes).Scan(&history, &elsewhere); err != nil || history < 3 || elsewhere > 0 {
		t.Errorf("the records' history has %d changes, %d of another project (%v)", history, elsewhere, err)
	}

	// Legacy reaches none of it.
	a.project = 0
	a.refused("GET", fmt.Sprintf("/api/runs/%d", rn.ID), nil, http.StatusNotFound, "not found")
	a.refused("GET", fmt.Sprintf("/api/structs/%d/records", tickets), nil, http.StatusNotFound, "not found")
}

// A board somehow naming another project's procedure, struct or record,
// as only moving it there can make one, reaches none of it: a subflow
// process's step fails, a pick or a query of another project's records
// takes none, a reference to another project's struct is refused, and a
// record, a run or a flow of another project isn't there by its id.
func TestProjectsRunsReachNoOtherProject(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()

	// Legacy's: tickets with a record, and a procedure.
	tickets := taskStruct(a, fmt.Sprintf("Legacy ticket %d", n), nil, "id", []string{"title"}, "open")
	legacyRecord := taskRecord(a, tickets.ID, map[string]any{"id": "T-1", "title": "Theirs"}, "open")
	ticketRoot := modObjects(a, tickets.ID)[0]
	sub := verBuildSubflow(a)
	a.worker.onAgent("Fix", handsIn(data("fixed")))
	legacyRun := a.finish(a.startRun(sub.callee.ID).ID)

	// A flow picking one of Legacy's tickets.
	picking := a.newFlow(fmt.Sprintf("Picking %d", n))
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", tickets.ID), map[string]any{"flow": picking.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	pickedCopy := modNamed(t, modObjects(a, picking.ID), tickets.Name)
	a.call("PUT", fmt.Sprintf("/api/objects/%d/pick", pickedCopy.ID), map[string]any{"pick": map[string]any{"state": "open"}}, http.StatusNoContent, nil)
	take := a.newProcess(picking.ID, map[string]any{"name": "Take", "kind": kindScript, "script": "take"})
	a.input(take.ID, pickedCopy.ID)
	a.startAt(picking.ID, take.ID)

	// A flow querying Legacy's tickets.
	querying := a.newFlow(fmt.Sprintf("Querying %d", n))
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", tickets.ID), map[string]any{"flow": querying.ID, "x": 0, "y": 0}, http.StatusCreated, nil)
	queriedCopy := modNamed(t, modObjects(a, querying.ID), tickets.Name)
	look := a.newProcess(querying.ID, map[string]any{"name": "Look", "kind": kindScript, "script": "look"})
	a.link(map[string]any{"process": look.ID, "object": queriedCopy.ID, "direction": directionInput, "query": linkQuery{State: "open", Limit: 1}})
	a.output(look.ID, a.newObject(querying.ID, "Seen").ID)
	a.startAt(querying.ID, look.ID)
	a.worker.onScript("Look", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"data": "seen"}`} })

	// A struct whose records refer to Legacy's tickets.
	referring := taskStruct(a, fmt.Sprintf("Referring %d", n), nil, "key", nil, "open")
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", modObjects(a, referring.ID)[0].ID), map[string]any{"name": "ticket", "ref": ticketRoot.ID}, http.StatusCreated, nil)

	prSave(a, sub.caller.ID, picking.ID, querying.ID)
	p := newProject(a, "Apart")
	// The callers move, what they name stays in Legacy.
	prMove(t, db, p.ID, sub.caller.ID, picking.ID, querying.ID, referring.ID)
	a.project = p.ID

	// The procedure is Legacy's: its subflow process's step fails.
	d := a.finish(a.startRun(sub.caller.ID).ID)
	if call := onlyStep(t, d, "Call"); d.Status != runFailed || !strings.Contains(call.Error, "another project's") || call.ChildRun != nil {
		t.Errorf("the run calling Legacy's procedure ended %s, Call %q with run %v; want it failed, running none", d.Status, call.Error, call.ChildRun)
	}
	var called int
	if err := db.QueryRow("SELECT COUNT(*) FROM runs WHERE flow_id = ? AND parent_run_id IS NOT NULL", sub.callee.ID).Scan(&called); err != nil || called != 0 {
		t.Errorf("Legacy's procedure ran %d times for another project's run (%v)", called, err)
	}
	// Legacy's tickets are none to pick, or to query.
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", picking.ID), nil, http.StatusConflict, "another project's struct")
	d = a.finish(a.startRun(querying.ID).ID)
	if s := onlyStep(t, d, "Look"); d.Status != runFailed || !strings.Contains(s.Error, "another project's struct") {
		t.Errorf("the run querying Legacy's tickets ended %s, Look %q; want it failed", d.Status, s.Error)
	}
	// A record can't refer to Legacy's tickets, whose records nobody here
	// reaches through the Records page either.
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records", referring.ID), map[string]any{"content": map[string]any{"key": "R-1", "ticket": "T-1"}, "state": "open"},
		http.StatusBadRequest, "another project's struct")
	var page struct {
		Refs map[string]int64 `json:"refs"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records", referring.ID), nil, http.StatusOK, &page)
	if len(page.Refs) != 0 {
		t.Errorf("the Records page links the references to %v", page.Refs)
	}
	// Legacy's record, run and flows aren't there by their ids.
	for _, path := range []string{
		fmt.Sprintf("/api/structs/%d/records/%d/changes", referring.ID, legacyRecord.ID),
		fmt.Sprintf("/api/structs/%d/records/%d/references", referring.ID, legacyRecord.ID),
		fmt.Sprintf("/api/runs/%d", legacyRun.ID),
		fmt.Sprintf("/api/structs/%d/records", tickets.ID),
	} {
		a.refused("GET", path, nil, http.StatusNotFound, "")
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", sub.callee.ID), nil, http.StatusNotFound, "not found")
	// Nothing of these runs went to Legacy.
	var runs []int64
	rows, err := db.Query("SELECT id FROM runs WHERE flow_id IN (?, ?, ?)", sub.caller.ID, picking.ID, querying.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		runs = append(runs, id)
	}
	rows.Close()
	if len(runs) != 2 {
		t.Fatalf("the callers ran %v, want the caller's and the querying flow's", runs)
	}
	prRowsOf(t, db, p.ID, runs)
}

// A flow of a second project taking an issue, run by a script.
func prIssueFlow(a *app, name string) flow {
	a.t.Helper()
	f := a.newFlow(name)
	issue := a.newObject(f.ID, "Issue")
	handle := a.newProcess(f.ID, map[string]any{"name": "Handle", "kind": kindScript, "script": "handle"})
	a.input(handle.ID, issue.ID)
	a.output(handle.ID, a.newObject(f.ID, "Handled").ID)
	a.startAt(f.ID, handle.ID)
	return f
}

// An API key, a webhook, a schedule and a task board of a second project
// start runs in it, as a run's links say, and none of another's; once the
// project is archived none of them, nor a person, nor a record's Run,
// starts anything there, and the board stops saying why.
func TestProjectsStartRunsInTheirOwnProject(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	a.worker.onScript("Handle", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"data": "handled"}`} })

	// Built in Legacy, as routes making a board don't name the project yet.
	issues := prIssueFlow(a, fmt.Sprintf("Issues %d", n))
	pubPublish(a, issues.ID, "issues")
	var sched schedule
	a.call("POST", "/api/schedules", map[string]any{"flow": issues.ID, "cron": everySecond, "timezone": "UTC", "enabled": false}, http.StatusCreated, &sched)
	t.Cleanup(func() { db.Exec("DELETE FROM schedules WHERE id = ?", sched.ID) })
	bugs := taskStruct(a, fmt.Sprintf("Bug %d", n), nil, "id", []string{"title"}, "new", "triaged")
	triage := movingFlow(a, bugs, "Triage", "new", "triaged")
	pubPublish(a, triage.ID, "triage")
	legacyFlow := a.newFlow(fmt.Sprintf("Legacy's own %d", n))
	legacyKey := apiNewKey(a, "Legacy's")

	p := newProject(a, "Starting")
	prMove(t, db, p.ID, issues.ID, bugs.ID, triage.ID)
	a.project = p.ID
	address := func(flow, run int64) string { return fmt.Sprintf("/#/p/%d/flows/%d/runs/%d", p.ID, flow, run) }
	inProject := func(what string, run int64) {
		t.Helper()
		var in int64
		if err := db.QueryRow("SELECT project_id FROM runs WHERE id = ?", run).Scan(&in); err != nil || in != p.ID {
			t.Errorf("%s started run %d in project %d (%v), want %d", what, run, in, err, p.ID)
		}
	}

	// An API key of the project: its runs are there; Legacy's key reaches
	// none of it, nor the project's key Legacy's flow.
	key := apiNewKey(a, "The project's")
	if _, err := db.Exec("UPDATE api_keys SET project_id = ? WHERE id = ?", p.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"inputs": map[string]any{"Issue": "Printer jam"}}
	viaKey := apiStart(a, key.Key, issues.ID, body, http.StatusCreated)
	if !strings.HasSuffix(viaKey.URL, address(issues.ID, viaKey.ID)) {
		t.Errorf("the API's run is at %s, want its project's address", viaKey.URL)
	}
	inProject("the API key", viaKey.ID)
	if d := a.finish(viaKey.ID); d.Status != runDone {
		t.Errorf("the API's run ended %s: %s", d.Status, describeSteps(d))
	}
	apiRefused(a, legacyKey.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", issues.ID), body, http.StatusNotFound, "flow not found")
	apiRefused(a, legacyKey.Key, "GET", fmt.Sprintf("/v1/flows/%d", issues.ID), nil, http.StatusNotFound, "flow not found")
	apiRefused(a, legacyKey.Key, "GET", fmt.Sprintf("/v1/runs/%d", viaKey.ID), nil, http.StatusNotFound, "run not found")
	apiRefused(a, key.Key, "GET", fmt.Sprintf("/v1/flows/%d", legacyFlow.ID), nil, http.StatusNotFound, "flow not found")

	// A webhook added there starts its runs there, and keeps its
	// deliveries and the digests of what it took there.
	h := hooksAdd(a, issues.ID, map[string]any{"name": "Issues", "kind": "github", "events": []string{"issues"},
		"mapping": map[string]any{"Issue": map[string]any{"path": "issue.title"}}})
	status, reply := hookGitHub(a, h, h.Secret, "issues", "d-1", hookIssue(1, "Jam", "opened"))
	viaHook := hookRun(a, status, reply, http.StatusAccepted)
	if !strings.Contains(string(reply), address(issues.ID, viaHook)) {
		t.Errorf("the delivery's reply %s doesn't link the run in its project", reply)
	}
	inProject("the webhook", viaHook)
	a.finish(viaHook)
	status, reply = hookGitHub(a, h, h.Secret, "issues", "d-2", hookIssue(1, "Jam", "opened"))
	if again := hookRun(a, status, reply, http.StatusOK); again != viaHook {
		t.Errorf("the same payload sent again started run %d, want %d", again, viaHook)
	}
	var deliveries, digests int
	db.QueryRow("SELECT COUNT(*) FROM webhook_deliveries WHERE webhook_id = ? AND project_id = ?", h.ID, p.ID).Scan(&deliveries)
	db.QueryRow("SELECT COUNT(*) FROM webhook_digests WHERE webhook_id = ? AND project_id = ?", h.ID, p.ID).Scan(&digests)
	if deliveries != 2 || digests != 1 {
		t.Errorf("the project holds %d deliveries and %d digests of the webhook, want 2 and 1", deliveries, digests)
	}
	prRowsOf(t, db, p.ID, []int64{viaKey.ID, viaHook})

	// A schedule of the flow runs it there.
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", sched.ID), map[string]any{"enabled": true}, http.StatusOK, nil)
	scheduled := a.waitScheduledRuns(issues.ID, sched.ID, 1)
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", sched.ID), map[string]any{"enabled": false}, http.StatusOK, nil)
	inProject("the schedule", scheduled[0].ID)

	// A task board of the project plays there.
	var b playBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Bugs"}, http.StatusCreated, &b)
	t.Cleanup(func() { db.Exec("UPDATE task_boards SET playing = FALSE WHERE id = ?", b.ID) })
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": "Bugs", "columns": []any{
		map[string]any{"name": "New", "struct": bugs.ID, "query": "state:new"},
		map[string]any{"name": "Triaged", "struct": bugs.ID, "query": "state:triaged"},
	}}, http.StatusOK, &b)
	newPlayScripts(a, "Triage")
	bug := taskRecord(a, bugs.ID, map[string]any{"id": "B-1", "title": "Crash"}, "new")
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/play", b.ID), nil, http.StatusOK, &b)
	b = waitBoard(a, b.ID, "B-1 triaged", func(b playBoard) bool {
		return len(b.Columns) == 2 && b.Columns[1].Cards != nil && len(b.Columns[1].Cards.Records) == 1 && b.Running == 0
	})
	var boardRun int64
	var boardRunIn int64
	if err := db.QueryRow("SELECT run_id, project_id FROM task_board_runs WHERE board_id = ?", b.ID).Scan(&boardRun, &boardRunIn); err != nil || boardRunIn != p.ID {
		t.Errorf("the board's run is in project %d (%v), want %d", boardRunIn, err, p.ID)
	}
	inProject("the task board", boardRun)

	// Archived, the project starts nothing.
	before := len(a.scheduledRuns(issues.ID, sched.ID))
	a.call("DELETE", fmt.Sprintf("/api/projects/%d", p.ID), nil, http.StatusNoContent, nil)
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", issues.ID), nil, http.StatusConflict, "archived")
	apiRefused(a, key.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", issues.ID), body, http.StatusNotFound, "flow not found")
	if status, reply := hookGitHub(a, h, h.Secret, "issues", "d-3", hookIssue(2, "Smoke", "opened")); status != http.StatusNotFound {
		t.Errorf("a delivery to the archived project's webhook = %d %s, want 404", status, reply)
	}
	a.refused("POST", fmt.Sprintf("/api/structs/%d/records/runs", bugs.ID), map[string]any{"flow": triage.ID, "records": []int64{bug.ID}}, http.StatusConflict, "archived")
	b = waitBoard(a, b.ID, "the board to stop", func(b playBoard) bool { return !b.Playing })
	if !strings.Contains(b.Stopped, "archived") {
		t.Errorf("the board stopped saying %q, want that its project is archived", b.Stopped)
	}
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", sched.ID), map[string]any{"enabled": true}, http.StatusOK, nil)
	time.Sleep(2500 * time.Millisecond)
	a.call("PATCH", fmt.Sprintf("/api/schedules/%d", sched.ID), map[string]any{"enabled": false}, http.StatusOK, nil)
	if after := len(a.scheduledRuns(issues.ID, sched.ID)); after != before {
		t.Errorf("the archived project's schedule went from %d runs to %d", before, after)
	}
}

// A run of a second project taken over by another replica, as the one
// walking it stops, goes on in its project: the replica reads it again
// from the run, and what it records of the run's moves and steps from
// then on is in the project too.
func TestProjectsTakenOverRunKeepsItsProject(t *testing.T) {
	d := newAdoptDB(t)
	a := d.start(adoptTimeout)
	b := newAdoptReview(a, "Reviewed in a project")
	prSave(a, b.flow.ID)
	p := newProject(a, "Moving")
	prMove(t, d.db, p.ID, b.flow.ID)
	a.project = p.ID

	rn := a.startRun(b.flow.ID)
	step := a.waitStep(rn.ID, "Review", stepWaiting)
	a.proc.stop()
	next := d.start(adoptTimeout)
	next.project = p.ID
	if got := adoptWait(next, rn.ID); got.Status != runRunning {
		t.Fatalf("taken over, the run is %s; steps %s", got.Status, describeSteps(got))
	}
	b.approve(next, rn.ID, step.ID, "lgtm")
	b.check(t, next.finish(rn.ID), step.ID, "lgtm")
	counts := prRowsOf(t, d.db, p.ID, []int64{rn.ID})
	if counts["run_moves"] != 1 || counts["run_steps"] < 3 {
		t.Errorf("the project holds %v of the run, want its move and steps", counts)
	}
	next.project = 0
	next.refused("GET", fmt.Sprintf("/api/runs/%d", rn.ID), nil, http.StatusNotFound, "not found")
}
