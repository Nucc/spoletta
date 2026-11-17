package tests

// Boards in a project other than Legacy: every row of one is its flow's
// project's, whatever wrote it (a route, a version brought back, undo and
// redo, paste, a setup file, a template, the assistant), and nothing on one
// refers to another project, which the routes refuse, what brings a board
// back leaves out, and the database's keys check. See board_projects.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The rows of a flow's board, by table: each query gives the projects its
// rows of the flow name.
var projBoardTables = []struct{ table, query string }{
	{"processes", "SELECT project_id FROM processes WHERE flow_id = ?"},
	{"objects", "SELECT project_id FROM objects WHERE flow_id = ?"},
	{"object_states", "SELECT s.project_id FROM object_states s JOIN objects o ON o.id = s.object_id WHERE o.flow_id = ?"},
	{"object_attributes", "SELECT a.project_id FROM object_attributes a JOIN objects o ON o.id = a.object_id WHERE o.flow_id = ?"},
	{"object_links", "SELECT l.project_id FROM object_links l JOIN processes p ON p.id = l.process_id WHERE p.flow_id = ?"},
	{"link_states", "SELECT ls.project_id FROM link_states ls JOIN object_links l ON l.id = ls.link_id JOIN processes p ON p.id = l.process_id WHERE p.flow_id = ?"},
	{"connections", "SELECT c.project_id FROM connections c JOIN processes p ON p.id = c.source_id WHERE p.flow_id = ?"},
	{"flow_starts", "SELECT project_id FROM flow_starts WHERE flow_id = ?"},
	{"start_targets", "SELECT t.project_id FROM start_targets t JOIN processes p ON p.id = t.process_id WHERE p.flow_id = ?"},
	{"inner_boards", "SELECT b.project_id FROM inner_boards b JOIN processes p ON p.id = b.process_id WHERE p.flow_id = ?"},
	{"notes", "SELECT project_id FROM notes WHERE flow_id = ?"},
	{"process_samples", "SELECT s.project_id FROM process_samples s JOIN processes p ON p.id = s.process_id WHERE p.flow_id = ?"},
	{"schedules", "SELECT project_id FROM schedules WHERE flow_id = ?"},
	{"flow_versions", "SELECT project_id FROM flow_versions WHERE flow_id = ?"},
	{"version_roles", "SELECT r.project_id FROM version_roles r JOIN flow_versions v ON v.id = r.version_id WHERE v.flow_id = ?"},
	{"flow_history", "SELECT project_id FROM flow_history WHERE flow_id = ?"},
	{"board_changes", "SELECT project_id FROM board_changes WHERE flow_id = ?"},
	{"board_viewers", "SELECT project_id FROM board_viewers WHERE flow_id = ?"},
	{"flow_tags", "SELECT project_id FROM flow_tags WHERE flow_id = ?"},
	{"assistant_messages", "SELECT project_id FROM assistant_messages WHERE flow_id = ?"},
}

// Fails the test for each row of the flow's board in another project than
// project, and for each table of want the flow has no row of.
func projBoardIn(t *testing.T, db *sql.DB, flow, project int64, want ...string) {
	t.Helper()
	for _, bt := range projBoardTables {
		rows, err := db.Query(bt.query, flow)
		if err != nil {
			t.Fatalf("%s: %v", bt.table, err)
		}
		n := 0
		for rows.Next() {
			var in int64
			if err := rows.Scan(&in); err != nil {
				t.Fatal(err)
			}
			n++
			if in != project {
				t.Errorf("a row of %s of flow %d is in project %d, want %d", bt.table, flow, in, project)
			}
		}
		rows.Close()
		if slices.Contains(want, bt.table) && n == 0 {
			t.Errorf("flow %d has no rows of %s", flow, bt.table)
		}
	}
}

// Every table holding a board's rows, which a whole board fills.
var projWholeBoard = []string{"processes", "objects", "object_states", "object_attributes", "object_links", "link_states", "connections",
	"flow_starts", "start_targets", "inner_boards", "notes", "process_samples", "schedules", "flow_versions", "version_roles", "flow_history",
	"board_changes", "flow_tags"}

// The flows' process names, by flow.
func projProcessNames(a *app, f int64) []string {
	a.t.Helper()
	var names []string
	for name := range editProcesses(a, f) {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Imports the file into the project the app's requests name, answering the
// status and reply.
func projSetupImport(a *app, file string) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest("POST", a.url+"/api/setup", strings.NewReader(file))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/yaml")
	a.inProject(req)
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func projSetupImportOK(a *app, file string) setupImported {
	a.t.Helper()
	status, body := projSetupImport(a, file)
	if status != http.StatusOK {
		a.t.Fatalf("POST /api/setup = %d %s", status, strings.TrimSpace(string(body)))
	}
	var res setupImported
	if err := json.Unmarshal(body, &res); err != nil {
		a.t.Fatal(err)
	}
	return res
}

// The setup file of the project the app's requests name, whole.
func projSetupExport(a *app) string {
	a.t.Helper()
	status, body := a.do("GET", "/api/setup", nil)
	if status != http.StatusOK {
		a.t.Fatalf("GET /api/setup = %d %s", status, body)
	}
	return string(body)
}

// The names of what a setup file has, by section.
func projSetupNames(t *testing.T, file string) map[string][]string {
	t.Helper()
	var f map[string]any
	if err := yaml.Unmarshal([]byte(file), &f); err != nil {
		t.Fatalf("the export isn't YAML: %v\n%s", err, file)
	}
	out := map[string][]string{}
	for section, v := range f {
		items, ok := v.([]any)
		if !ok {
			continue
		}
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				out[section] = append(out[section], fmt.Sprint(m["name"]))
			}
		}
		slices.Sort(out[section])
	}
	return out
}

// A whole board built in a project of its own, with every kind of process
// and object, links and the states they decide between, connections,
// starts, a flow process's board, notes, samples, a schedule, tags, a
// version published and gone back to, and history undone and redone: every
// row of it is the project's. What it refers to, the structs it copies and
// the procedure it runs, is the project's too.
func TestProjectsBoardsBuiltInAnotherProject(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	p := newProject(a, "Boards")
	a.project = p.ID

	// Structs whose records the flow works on, one made of a part, and a
	// procedure leaving a summary.
	ticket := recordStruct(a, fmt.Sprintf("Ticket %d", n), "open", "done")
	ticketRoot := modObjects(a, ticket.ID)[0]
	var author object
	a.call("POST", "/api/objects", map[string]any{"owner": ticketRoot.ID, "name": "author"}, http.StatusCreated, &author)
	change := recordStruct(a, fmt.Sprintf("Change %d", n), "open")
	proc := a.newFlow(fmt.Sprintf("Summarize %d", n))
	summary := a.newObject(proc.ID, "Summary")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", summary.ID), map[string]any{"end": true}, http.StatusOK, nil)
	write := a.agent(proc.ID, "Write", "Write it.")
	a.output(write.ID, summary.ID)
	a.startAt(proc.ID, write.ID)

	f := a.newFlow(fmt.Sprintf("Board %d", n))
	agent := a.agent(f.ID, "Agent", "Do it.")
	script := a.newProcess(f.ID, map[string]any{"name": "Script", "kind": kindScript, "script": "echo hi"})
	review := humanHuman(a, f.ID, "Review")
	inner := nestedFlowProcess(a, f.ID, "Inner", false)
	call := a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindSubflow, "settings": map[string]any{"flow": proc.ID}})
	a.newProcess(f.ID, map[string]any{"name": "Post", "kind": kindConnector})
	lone := a.agent(f.ID, "Lone", "Alone.")

	plain := a.newObject(f.ID, "Plain")
	draft := a.newState(plain.ID, "draft")
	final := a.newState(plain.ID, "final")
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", plain.ID), map[string]any{"name": "title"}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", plain.ID), map[string]any{"name": "ticket", "ref": ticketRoot.ID}, http.StatusCreated, nil)
	memo := a.newObject(f.ID, "Memo")
	humanPerson(a, f.ID, review.ID, "Reviewer")
	ticketCopy := recordCopy(a, ticket, f.ID, nil)
	changeCopy := recordCopy(a, change, f.ID, nil)
	a.call("PUT", fmt.Sprintf("/api/objects/%d/pick", changeCopy.ID), map[string]any{"pick": map[string]any{"states": []string{"open"}}}, http.StatusNoContent, nil)
	a.call("PUT", fmt.Sprintf("/api/objects/%d/follows", ticketCopy.ID), map[string]any{"follows": map[string]any{"object": plain.ID, "attribute": "ticket"}}, http.StatusNoContent, nil)

	a.link(map[string]any{"process": agent.ID, "object": plain.ID, "direction": directionInput, "state": draft.ID})
	a.input(review.ID, plain.ID)
	humanDecides(a, review.ID, plain.ID, draft.ID, final.ID)
	a.input(script.ID, changeCopy.ID)
	a.input(script.ID, memo.ID)
	a.output(script.ID, ticketCopy.ID)
	a.call("POST", "/api/connections", map[string]any{"source": agent.ID, "target": script.ID}, http.StatusCreated, nil)
	a.call("POST", "/api/connections", map[string]any{"source": script.ID, "target": review.ID, "kind": connectionFailure}, http.StatusCreated, nil)
	a.startAt(f.ID, agent.ID)
	a.call("PUT", fmt.Sprintf("/api/processes/%d/start", lone.ID), nil, http.StatusOK, nil)

	inside := nestedAgent(a, f.ID, inner.ID, "Inside")
	kept := nestedObject(a, inner.ID, "Kept")
	a.output(inside.ID, kept.ID)
	nestedStartAt(a, inner.ID, inside.ID)
	notesNew(a, map[string]any{"flow": f.ID, "text": "Look here", "process": agent.ID})
	notesNew(a, map[string]any{"flow": f.ID, "parent": inner.ID, "text": "Inside", "object": kept.ID})
	pointing := notesNew(a, map[string]any{"flow": f.ID, "text": "The kept one", "object": kept.ID})
	saveSample(a, script.ID, "One", trialValue{Object: memo.ID, Content: "hello"})
	var s schedule
	a.call("POST", "/api/schedules", map[string]any{"flow": f.ID, "cron": "0 9 * * *", "timezone": "UTC", "enabled": false}, http.StatusCreated, &s)
	a.call("PUT", fmt.Sprintf("/api/flows/%d/tags", f.ID), map[string]any{"tags": []string{"boards"}}, http.StatusOK, nil)
	var group process
	a.call("POST", "/api/processes/wrap", map[string]any{"name": "Group", "processes": []int64{lone.ID}}, http.StatusCreated, &group)

	// Saved, published, changed, undone and redone, and gone back to.
	histRecord(a, f.ID)
	v := verSave(a, f.ID)
	pubPublish(a, f.ID, "first")
	a.agent(f.ID, "Later", "Later.")
	histRecord(a, f.ID)
	histUndo(a, f.ID)
	if names := projProcessNames(a, f.ID); slices.Contains(names, "Later") {
		t.Errorf("undoing left %v", names)
	}
	histRedo(a, f.ID)
	if names := projProcessNames(a, f.ID); !slices.Contains(names, "Later") {
		t.Errorf("redoing left %v", names)
	}
	verRestore(a, v.ID)
	if names := projProcessNames(a, f.ID); slices.Contains(names, "Later") || !slices.Contains(names, "Inside") || !slices.Contains(names, "Group") {
		t.Errorf("going back to the version left %v", names)
	}

	projBoardIn(t, db, f.ID, p.ID, projWholeBoard...)
	projBoardIn(t, db, proc.ID, p.ID, "processes", "objects", "object_links", "start_targets", "flow_starts")
	for _, of := range []int64{ticket.ID, change.ID} {
		projBoardIn(t, db, of, p.ID, "objects", "object_states", "object_attributes", "flow_starts")
	}

	// What INSERT IGNORE would let go missing in another project is there:
	// the boards of the flow processes and where they start, the states the
	// review decides between, and the outcome the subflow process brought.
	boards := editBoards(a, f.ID)
	starts := map[int64][]int64{}
	for _, b := range boards {
		starts[b.Process] = startTargets(b.Start)
	}
	if !slices.Equal(starts[inner.ID], []int64{inside.ID}) || !slices.Equal(starts[group.ID], []int64{lone.ID}) {
		t.Errorf("the flow processes' boards start with %v, want Inner with Inside and Group with Lone", starts)
	}
	var top startNode
	a.call("GET", fmt.Sprintf("/api/start?flow=%d", f.ID), nil, http.StatusOK, &top)
	if got := startTargets(top); !slices.Contains(got, agent.ID) || !slices.Contains(got, group.ID) {
		t.Errorf("the top board starts with %v, want Agent and Group", got)
	}
	var decided, outcome bool
	objs := editObjects(a, f.ID)
	for _, l := range editLinks(a, f.ID) {
		if l.Process == review.ID && l.Direction == directionOutput && len(l.States) == 2 {
			decided = true
		}
		if l.Process == call.ID && l.Direction == directionOutput && l.Object == objs["Summary"].ID {
			outcome = true
		}
	}
	if !decided || !outcome {
		t.Errorf("the review decides between two states %v, the call yields the summary %v", decided, outcome)
	}
	copies := modObjects(a, f.ID)
	if parts := modPartsOf(copies, ticketCopy.ID); len(parts) != 1 || parts[0].Name != "author" {
		t.Errorf("the ticket's copy is made of %+v, want author", parts)
	}

	// Taking things away lets go of what refers to them, the keys naming
	// the project too: the struct's part goes from its copy, and a note on
	// the top board pointing into a flow process deleted points at nothing.
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", author.ID), nil, http.StatusNoContent, nil)
	if parts := modPartsOf(modObjects(a, f.ID), ticketCopy.ID); len(parts) != 0 {
		t.Errorf("the ticket's copy is still made of %+v", parts)
	}
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", inner.ID), nil, http.StatusNoContent, nil)
	for _, note := range notesOf(a, f.ID) {
		if note.ID == pointing.ID && note.Object != nil {
			t.Errorf("the note still points at %d, deleted with its flow process", *note.Object)
		}
	}
	projBoardIn(t, db, f.ID, p.ID)

	// Its problems, OPL and setup file read in the project, and its
	// schedule is listed there alone.
	a.call("GET", fmt.Sprintf("/api/flows/%d/problems", f.ID), nil, http.StatusOK, nil)
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil, http.StatusOK, nil)
	names := projSetupNames(t, projSetupExport(a))
	if !slices.Contains(names["flows"], f.Name) || !slices.Contains(names["procedures"], proc.Name) || len(names["structs"]) != 2 {
		t.Errorf("the project's setup file has %v", names)
	}
	var listed []schedule
	a.call("GET", "/api/schedules", nil, http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].ID != s.ID {
		t.Errorf("the project's schedules are %+v, want %d alone", listed, s.ID)
	}
	a.project = 0
	a.call("GET", "/api/schedules", nil, http.StatusOK, &listed)
	if slices.ContainsFunc(listed, func(l schedule) bool { return l.ID == s.ID }) {
		t.Errorf("Legacy lists another project's schedule")
	}
}

// A version kept as JSON names no project and is the flow's whatever it
// holds: brought back, it writes every row in the flow's project, and what
// it names of another, a procedure, an MCP server, a source, a store, a
// general, a struct referred to, a process a note points at, is left out;
// one naming another flow's process is refused, changing nothing there.
func TestProjectsRestoreKeepsToTheFlowsProject(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	legacyStruct := recordStruct(a, fmt.Sprintf("Legacy ticket %d", n), "open")
	legacyRoot := modObjects(a, legacyStruct.ID)[0]
	legacyProc := a.newFlow(fmt.Sprintf("Legacy procedure %d", n))
	legacyFlow := a.newFlow(fmt.Sprintf("Legacy flow %d", n))
	legacyProcess := a.agent(legacyFlow.ID, "Theirs", "Theirs.")
	legacyObject := a.newObject(legacyFlow.ID, "Their object")
	server := fmt.Sprintf("legacy_mcp_%d", n)
	forgetMCPServers(t, server)
	var legacyServer struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/mcp-servers", map[string]any{"name": server, "url": "https://mcp.example.com/mcp"}, http.StatusCreated, &legacyServer)

	p := newProject(a, "Restore")
	a.project = p.ID
	s := recordStruct(a, fmt.Sprintf("Ticket %d", n), "open")
	root := modObjects(a, s.ID)[0]
	proc := a.newFlow(fmt.Sprintf("Procedure %d", n))
	f := a.newFlow(fmt.Sprintf("Restored %d", n))
	call := a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindSubflow, "settings": map[string]any{"flow": proc.ID}})
	copied := recordCopy(a, s, f.ID, nil)
	plain := a.newObject(f.ID, "Plain")
	general := a.newObject(f.ID, "General")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", plain.ID), map[string]any{"general": general.ID}, http.StatusOK, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", plain.ID), map[string]any{"name": "ticket", "ref": root.ID}, http.StatusCreated, nil)
	notesNew(a, map[string]any{"flow": f.ID, "text": "Here", "process": call.ID})
	v := verSave(a, f.ID)

	var body []byte
	if err := db.QueryRow("SELECT snapshot FROM flow_versions WHERE id = ?", v.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	tamper := func(change func(snap map[string]any)) {
		t.Helper()
		var snap map[string]any
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatal(err)
		}
		change(snap)
		out, err := json.Marshal(snap)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE flow_versions SET snapshot = ? WHERE id = ?", out, v.ID); err != nil {
			t.Fatal(err)
		}
	}
	items := func(snap map[string]any, key string) []map[string]any {
		var out []map[string]any
		list, _ := snap[key].([]any)
		for _, it := range list {
			out = append(out, it.(map[string]any))
		}
		return out
	}
	tamper(func(snap map[string]any) {
		for _, key := range []string{"processes", "objects", "states", "attributes", "links", "notes", "boards"} {
			for _, it := range items(snap, key) {
				it["project"], it["projectId"], it["project_id"] = 1, 1, 1
			}
		}
		snap["project"] = 1
		for _, pr := range items(snap, "processes") {
			if int64(pr["id"].(float64)) == call.ID {
				pr["settings"] = map[string]any{"flow": legacyProc.ID, "mcpServers": []int64{legacyServer.ID}}
			}
		}
		for _, o := range items(snap, "objects") {
			switch int64(o["id"].(float64)) {
			case copied.ID:
				o["source"], o["store"] = legacyRoot.ID, legacyRoot.ID
				o["follows"] = map[string]any{"object": legacyObject.ID, "attribute": "ticket"}
			case plain.ID:
				o["general"] = legacyObject.ID
			}
		}
		for _, at := range items(snap, "attributes") {
			if at["name"] == "ticket" {
				at["ref"] = legacyRoot.ID
			}
		}
		for _, note := range items(snap, "notes") {
			note["process"] = legacyProcess.ID
		}
	})
	verRestore(a, v.ID)
	projBoardIn(t, db, f.ID, p.ID, "processes", "objects", "object_states", "object_attributes", "notes", "flow_versions")
	if c := editProcesses(a, f.ID)["Call"].Settings; c.Flow != nil || len(c.MCPServers) != 0 {
		t.Errorf("the call still runs %v with the MCP servers %v", show(c.Flow), c.MCPServers)
	}
	// Nothing on it refers to a row of another project than its own.
	for _, c := range []struct{ what, query string }{
		{"a source", "SELECT COUNT(*) FROM objects o JOIN objects r ON r.id = o.source_id WHERE o.flow_id = ? AND r.project_id <> o.project_id"},
		{"a store", "SELECT COUNT(*) FROM objects o JOIN objects r ON r.id = o.store_id WHERE o.flow_id = ? AND r.project_id <> o.project_id"},
		{"a general", "SELECT COUNT(*) FROM objects o JOIN objects r ON r.id = o.general_id WHERE o.flow_id = ? AND r.project_id <> o.project_id"},
		{"a struct referred to", `SELECT COUNT(*) FROM object_attributes a JOIN objects o ON o.id = a.object_id JOIN objects r ON r.id = a.ref_id
			WHERE o.flow_id = ? AND r.project_id <> a.project_id`},
		{"a process pointed at", "SELECT COUNT(*) FROM notes n JOIN processes r ON r.id = n.process_id WHERE n.flow_id = ? AND r.project_id <> n.project_id"},
	} {
		var others int
		if err := db.QueryRow(c.query, f.ID).Scan(&others); err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if others != 0 {
			t.Errorf("the board brought back has %d of %s of another project", others, c.what)
		}
	}
	objs := editObjects(a, f.ID)
	if objs[copied.Name].Source != nil || objs["Plain"].General != nil {
		t.Errorf("the copy follows %v and Plain specializes %v, want neither", show(objs[copied.Name].Source), show(objs["Plain"].General))
	}
	var followsNone bool
	if err := db.QueryRow("SELECT follows IS NULL FROM objects WHERE id = ?", copied.ID).Scan(&followsNone); err != nil || !followsNone {
		t.Errorf("the copy follows a reference held by Legacy's object: %v", err)
	}
	for _, note := range notesOf(a, f.ID) {
		if note.Process != nil {
			t.Errorf("the note points at %d", *note.Process)
		}
	}

	// A version naming a process of Legacy's flow would change it: it is
	// refused, and Legacy's process is as it was.
	tamper(func(snap map[string]any) {
		for _, pr := range items(snap, "processes") {
			if int64(pr["id"].(float64)) == call.ID {
				pr["id"], pr["name"] = legacyProcess.ID, "Taken over"
			}
		}
	})
	a.refused("POST", fmt.Sprintf("/api/versions/%d/restore", v.ID), nil, http.StatusConflict, "another flow")
	a.project = 0
	if got := editProcesses(a, legacyFlow.ID); got["Theirs"].ID != legacyProcess.ID || got["Theirs"].Prompt != "Theirs." {
		t.Errorf("Legacy's flow has %+v", got)
	}
}

// Nothing in a project refers to another's, whoever writes it: a subflow
// process's procedure, a struct copied, a reference, an object followed,
// one picking, one specialized, what a note points at, the MCP servers a
// process uses and what is pasted are refused.
func TestProjectsRefuseReferencesAcross(t *testing.T) {
	a := newApp(t)
	n := time.Now().UnixNano()
	legacyStruct := recordStruct(a, fmt.Sprintf("Legacy ticket %d", n), "open")
	legacyRoot := modObjects(a, legacyStruct.ID)[0]
	legacyProc := a.newFlow(fmt.Sprintf("Legacy procedure %d", n))
	legacyFlow := a.newFlow(fmt.Sprintf("Legacy flow %d", n))
	legacyProcess := a.agent(legacyFlow.ID, "Theirs", "Theirs.")
	legacyObject := a.newObject(legacyFlow.ID, "Their object")
	legacyCopy := recordCopy(a, legacyStruct, legacyFlow.ID, nil)
	server := fmt.Sprintf("legacy_mcp_%d", n)
	forgetMCPServers(t, server)
	var legacyServer struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/mcp-servers", map[string]any{"name": server, "url": "https://mcp.example.com/mcp"}, http.StatusCreated, &legacyServer)

	p := newProject(a, "Apart")
	a.project = p.ID
	s := recordStruct(a, fmt.Sprintf("Ticket %d", n), "open")
	f := a.newFlow(fmt.Sprintf("Here %d", n))
	call := a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindSubflow})
	plain := a.newObject(f.ID, "Plain")
	mine := recordCopy(a, s, f.ID, nil)

	for _, c := range []struct {
		what, method, path string
		body               any
		status             int
		says               string
	}{
		{"a procedure run", "PATCH", fmt.Sprintf("/api/processes/%d", call.ID), map[string]any{"settings": map[string]any{"flow": legacyProc.ID}}, http.StatusNotFound, "flow not found"},
		{"a procedure run by a new process", "POST", "/api/processes", map[string]any{"flow": f.ID, "name": "Again", "kind": kindSubflow, "settings": map[string]any{"flow": legacyProc.ID}}, http.StatusNotFound, "flow not found"},
		{"an MCP server used", "PATCH", fmt.Sprintf("/api/processes/%d", call.ID), map[string]any{"kind": kindAgent, "settings": map[string]any{"mcpServers": []int64{legacyServer.ID}}}, http.StatusBadRequest, "no such MCP server"},
		{"a struct copied", "POST", fmt.Sprintf("/api/structs/%d/copies", legacyStruct.ID), map[string]any{"flow": f.ID}, http.StatusNotFound, "flow not found"},
		{"a struct referred to", "POST", fmt.Sprintf("/api/objects/%d/attributes", plain.ID), map[string]any{"name": "ticket", "ref": legacyRoot.ID}, http.StatusBadRequest, "refers to the records of a struct"},
		{"an object followed", "PUT", fmt.Sprintf("/api/objects/%d/follows", mine.ID), map[string]any{"follows": map[string]any{"object": legacyObject.ID, "attribute": "ticket"}}, http.StatusBadRequest, "isn't on the flow's board"},
		{"a pick", "PUT", fmt.Sprintf("/api/objects/%d/pick", legacyCopy.ID), map[string]any{"pick": map[string]any{"states": []string{"open"}}}, http.StatusNotFound, "flow not found"},
		{"a general", "PATCH", fmt.Sprintf("/api/objects/%d", plain.ID), map[string]any{"general": legacyObject.ID}, http.StatusBadRequest, "can only specialize an object of its flow"},
		{"a note pointing", "POST", "/api/notes", map[string]any{"flow": f.ID, "text": "There", "process": legacyProcess.ID}, http.StatusNotFound, "flow not found"},
		{"a paste", "POST", "/api/paste", map[string]any{"from": legacyFlow.ID, "processes": []int64{legacyProcess.ID}, "flow": f.ID}, http.StatusNotFound, "flow not found"},
	} {
		if status, reply := a.do(c.method, c.path, c.body); status != c.status || !strings.Contains(string(reply), c.says) {
			t.Errorf("%s from another project: %s %s = %d %q, want %d with %q", c.what, c.method, c.path, status, strings.TrimSpace(string(reply)), c.status, c.says)
		}
	}
	// Nothing came of them.
	if c := editProcesses(a, f.ID)["Call"].Settings; c.Flow != nil || len(c.MCPServers) != 0 {
		t.Errorf("the call runs %v with %v", show(c.Flow), c.MCPServers)
	}
	if got := editObjects(a, f.ID)["Plain"]; got.General != nil || len(got.Attributes) != 0 {
		t.Errorf("Plain is %+v", got)
	}
	if used := structFlowOf(a, s.ID).UsedBy; used != 1 {
		t.Errorf("the project's struct is used by %d flows, want 1", used)
	}
	a.project = 0
	if used := structFlowOf(a, legacyStruct.ID).UsedBy; used != 1 {
		t.Errorf("Legacy's struct is used by %d flows, want its own 1", used)
	}
}

// A setup file is its project's: exported from one, it holds that
// project's alone and makes everything over in an empty one; the same file
// imported into two projects makes two sets apart, and into a project that
// has a flow of a name in it, makes that one over and nothing elsewhere.
// What it names, a credential say, is looked for in the project alone.
func TestProjectsSetupFilesKeepToTheirProject(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	prefix := fmt.Sprintf("pj%d", time.Now().UnixNano())
	file := strings.ReplaceAll(setupWritten, "PFX", prefix)
	forgetMCPServers(t, prefix+"_jira")
	legacyTriage := a.newFlow(prefix + " Triage")
	from := newProject(a, "Exported")
	to := newProject(a, "Imported")

	a.project = from.ID
	made := projSetupImportOK(a, file)
	if len(made.Flows) != 3 {
		t.Fatalf("importing into the first project made %+v", made.Flows)
	}
	exported := projSetupExport(a)
	names := projSetupNames(t, exported)
	want := map[string][]string{"mcpServers": {prefix + "_jira"}, "structs": {prefix + " Ticket"}, "procedures": {prefix + " Summarize"}, "flows": {prefix + " Triage"}}
	for section, w := range want {
		if !slices.Equal(names[section], w) {
			t.Errorf("the first project's file has %s %v, want %v alone", section, names[section], w)
		}
	}

	a.project = to.ID
	there := projSetupImportOK(a, exported)
	// A struct holds an object alone; the others processes and their links.
	filled := func(name string) []string {
		if name == prefix+" Ticket" {
			return []string{"objects", "object_states", "object_attributes", "flow_starts", "flow_history"}
		}
		return []string{"processes", "objects", "object_links", "start_targets", "flow_starts", "flow_history"}
	}
	ids := map[int64]bool{}
	for _, fl := range made.Flows {
		ids[fl.ID] = true
		projBoardIn(t, db, fl.ID, from.ID, filled(fl.Name)...)
	}
	for _, fl := range there.Flows {
		if !fl.Created || ids[fl.ID] {
			t.Errorf("importing into an empty project made %+v, want it created anew", fl)
		}
		projBoardIn(t, db, fl.ID, to.ID, filled(fl.Name)...)
	}
	if again := projSetupExport(a); again != exported {
		t.Errorf("the second project's file differs from the first's:\n%s\nwant\n%s", again, exported)
	}
	// The MCP server the file has, one in each project, and the flow, in
	// each and Legacy, which has one of that name of its own.
	projectsOf := func(table, name string) []int64 {
		t.Helper()
		rows, err := db.Query("SELECT project_id FROM "+table+" WHERE name = ? ORDER BY project_id", name)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var in []int64
		for rows.Next() {
			var p int64
			if err := rows.Scan(&p); err != nil {
				t.Fatal(err)
			}
			in = append(in, p)
		}
		return in
	}
	if in := projectsOf("mcp_servers", prefix+"_jira"); !slices.Equal(in, []int64{from.ID, to.ID}) {
		t.Errorf("the MCP server is in the projects %v, want %d and %d", in, from.ID, to.ID)
	}
	if in := projectsOf("flows", prefix+" Triage"); !slices.Equal(in, []int64{1, from.ID, to.ID}) {
		t.Errorf("the flow is in the projects %v, want Legacy, %d and %d", in, from.ID, to.ID)
	}

	// Imported again, it makes over the project's own.
	again := projSetupImportOK(a, exported)
	for i, fl := range again.Flows {
		if fl.Created || fl.ID != there.Flows[i].ID {
			t.Errorf("importing again made %+v, want %d made over", fl, there.Flows[i].ID)
		}
		ids[fl.ID] = true
	}
	// Into Legacy, it makes over Legacy's flow of that name, and adds the
	// rest there, apart from both projects'.
	a.project = 0
	legacy := projSetupImportOK(a, file)
	for _, fl := range legacy.Flows {
		if fl.Name == prefix+" Triage" && (fl.Created || fl.ID != legacyTriage.ID) {
			t.Errorf("Legacy's import made %+v, want its own flow %d made over", fl, legacyTriage.ID)
		}
		if ids[fl.ID] {
			t.Errorf("Legacy's import made over another project's flow %+v", fl)
		}
		projBoardIn(t, db, fl.ID, 1)
	}
	a.project = from.ID
	if got := projSetupNames(t, projSetupExport(a)); !slices.Equal(got["flows"], want["flows"]) {
		t.Errorf("the first project has the flows %v after the others' imports", got["flows"])
	}

	// A credential of Legacy's is none in another project.
	a.project = 0
	cred := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Legacy key"), "baseUrl": "https://api.example.com/", "auth": "header",
		"template": "Bearer {secret}", "secret": "legacy-secret-0123456789", "everyone": true})
	a.project = to.ID
	status, reply := projSetupImport(a, fmt.Sprintf("spoletta: 1\nflows:\n  - name: %s Keyed\n    processes:\n      - name: Call\n        prompt: Call it.\n        credentials: [%q]\n", prefix, cred.Name))
	if status != http.StatusBadRequest || !strings.Contains(string(reply), "there is no credential named") {
		t.Errorf("a file naming Legacy's credential = %d %s, want it refused", status, reply)
	}
}

// A template is used in the project it is used from: its flow named apart
// from that project's flows alone, given that project's connectors, and
// what it needs met by what that project has. The assistant works there
// too: it is told what that project has, and what it replies with is
// imported there.
func TestProjectsTemplatesAndTheAssistant(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	noSlackInstalled(a)
	slack := newTplSlack(t)
	slack.install(a, fmt.Sprintf("legacy_slack_%d", n))
	digest := tplFind(a, "daily-digest-to-slack")
	if len(digest.Needs) != 1 || !digest.Needs[0].Met {
		t.Fatalf("with Slack in Legacy the digest needs %+v there", digest.Needs)
	}
	legacyDigest := a.newFlow(digest.Flow)
	secret := skillsNew(a, "skills", map[string]any{"name": skillsName("legacysecret"), "description": "Legacy's own.", "content": "Not for others."})

	p := newProject(a, "Templates")
	a.project = p.ID
	if needs := tplFind(a, "daily-digest-to-slack").Needs; len(needs) != 1 || needs[0].Met {
		t.Errorf("in a project without Slack the digest needs %+v", needs)
	}
	f := tplUse(a, "daily-digest-to-slack", nil)
	if f.Name != digest.Flow || f.ID == legacyDigest.ID {
		t.Errorf("the template made %+v, want a flow named %s of the project's own", f, digest.Flow)
	}
	procs, _ := tplBoard(a, f.ID)
	if c := procs["Read the channel"].Settings.Connector; c == nil || c.Server != 0 {
		t.Errorf("Read the channel took Legacy's connector: %+v", c)
	}
	projBoardIn(t, db, f.ID, p.ID, "processes", "objects", "flow_versions", "flow_history")

	// The assistant of a flow of the project.
	name := fmt.Sprintf("Jira %d", n)
	fl := a.newFlow(name)
	a.worker.onAgent("Assistant", func(context.Context, agentRun) runResult {
		return replyWithSetup("I built it.", name, jiraProcesses)
	})
	askAssistant(a, fl.ID, "Build a flow that processes Jira tickets")
	if reply := assistantReply(a, fl.ID); reply.Status != "done" || !reply.Applied {
		t.Fatalf("the reply = %+v, want it applied", reply)
	}
	if got := projProcessNames(a, fl.ID); !slices.Equal(got, []string{"Fetch", "Summarize"}) {
		t.Errorf("the assistant left the processes %v", got)
	}
	projBoardIn(t, db, fl.ID, p.ID, "processes", "objects", "object_links", "connections", "flow_versions", "flow_history", "assistant_messages", "board_changes")
	runs := a.worker.runsOf("Assistant")
	if len(runs) == 0 || strings.Contains(runs[len(runs)-1].Prompt, secret.Name) {
		t.Errorf("the assistant in another project was told of Legacy's skill %s", secret.Name)
	}
	a.project = 0
	if procs := editProcesses(a, legacyDigest.ID); len(procs) != 0 {
		t.Errorf("Legacy's flow of the template's name got %v", procs)
	}
}

// The keys a board's references name the project in refuse a row of one
// project referring to another's: a copy's source, a general, and what a
// note points at.
func TestProjectsBoardReferenceKeys(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	legacyFlow := a.newFlow(fmt.Sprintf("Legacy keys %d", n))
	legacyProcess := a.agent(legacyFlow.ID, "Theirs", "Theirs.")
	legacyObject := a.newObject(legacyFlow.ID, "Their object")
	p := newProject(a, "Keys")
	a.project = p.ID
	f := a.newFlow(fmt.Sprintf("Keyed %d", n))
	mine := a.newObject(f.ID, "Mine")
	note := notesNew(a, map[string]any{"flow": f.ID, "text": "Here"})

	for _, c := range []struct {
		what  string
		query string
		args  []any
	}{
		{"a copy of Legacy's object", "INSERT INTO objects (flow_id, name, source_id, project_id) VALUES (?, 'Copy', ?, ?)", []any{f.ID, legacyObject.ID, p.ID}},
		{"a source in Legacy", "UPDATE objects SET source_id = ? WHERE id = ?", []any{legacyObject.ID, mine.ID}},
		{"a general in Legacy", "UPDATE objects SET general_id = ? WHERE id = ?", []any{legacyObject.ID, mine.ID}},
		{"a note pointing at Legacy's process", "UPDATE notes SET process_id = ? WHERE id = ?", []any{legacyProcess.ID, note.ID}},
		{"a note pointing at Legacy's object", "UPDATE notes SET object_id = ? WHERE id = ?", []any{legacyObject.ID, note.ID}},
	} {
		if _, err := db.Exec(c.query, c.args...); !refusedByForeignKey(err) {
			t.Errorf("%s: %v, want refused", c.what, err)
		}
	}
	other := a.newObject(f.ID, "Other")
	if _, err := db.Exec("UPDATE objects SET general_id = ? WHERE id = ?", other.ID, mine.ID); err != nil {
		t.Errorf("a general of the project: %v", err)
	}
	// Deleting what they refer to lets go of them first.
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", other.ID), nil, http.StatusNoContent, nil)
	if got := editObjects(a, f.ID)["Mine"]; got.General != nil {
		t.Errorf("Mine still specializes %d", *got.General)
	}
}

// Who has a board open and who changed it are its project's, and a tab is
// told of them alone.
func TestProjectsPresenceKeepsToTheProject(t *testing.T) {
	a, provider := usersApp(t)
	usersSignIn(a, provider, usersAda)
	db := secretsDB(t)
	p := newProject(a, "Presence")
	a.project = p.ID
	f := a.newFlow(fmt.Sprintf("Open %d", time.Now().UnixNano()))
	presenceRecord(a, f.ID, "ada-1")
	if got := presenceBeat(a, f.ID, "ada-1", nil); got.Rev == 0 || len(got.People) != 0 {
		t.Errorf("Ada alone on the board is told %+v", got)
	}
	projBoardIn(t, db, f.ID, p.ID, "board_viewers", "board_changes", "flow_history")
	// From Legacy the board isn't there to be open.
	a.project = 0
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/presence", f.ID), map[string]any{"client": "ada-2"}, http.StatusNotFound, "flow not found")
}
