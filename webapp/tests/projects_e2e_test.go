package tests

// Projects: tenants, each a space of its own, which nothing in another
// reaches. Every table below them names its project, which the database
// checks; a request names its project in a header, and is refused there if
// its person may not enter it, or, a viewer, change anything. See
// projects.go and docs/PROJECTS.md.

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

type projectRow struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Level            string `json:"level"`
	Archived         bool   `json:"archived"`
	OthersAccess     string `json:"othersAccess"`
	InheritsInstance bool   `json:"inheritsInstance"`
}

type projectMemberRow struct {
	User      int64      `json:"user"`
	Email     string     `json:"email"`
	Level     string     `json:"level"`
	Source    string     `json:"source"`
	RemovedAt *time.Time `json:"removedAt"`
}

type projectsMe struct {
	Super bool `json:"super"`
	User  *struct {
		ID    int64 `json:"id"`
		Admin bool  `json:"admin"`
	} `json:"user"`
	Projects []projectRow `json:"projects"`
}

// The tables above the projects, which name none, or have it as part of
// what they are.
var aboveProjects = []string{"projects", "users", "user_identities", "user_groups", "sessions",
	"project_groups", "project_members", "instance_events", "replicas", "replica_commands", "goose_db_version"}

// Makes a project named apart from those of other tests, which share the
// database.
func newProject(a *app, name string) projectRow {
	a.t.Helper()
	var p projectRow
	a.call("POST", "/api/projects", map[string]any{"name": fmt.Sprintf("%s %d", name, time.Now().UnixNano())}, http.StatusCreated, &p)
	return p
}

func projectIn(ps []projectRow, id int64) (projectRow, bool) {
	i := slices.IndexFunc(ps, func(p projectRow) bool { return p.ID == id })
	if i < 0 {
		return projectRow{}, false
	}
	return ps[i], true
}

func projectMemberOf(ms []projectMemberRow, email string) (projectMemberRow, bool) {
	i := slices.IndexFunc(ms, func(m projectMemberRow) bool { return m.Email == email })
	if i < 0 {
		return projectMemberRow{}, false
	}
	return ms[i], true
}

func projectFlowNames(a *app) []string {
	a.t.Helper()
	var fs []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &fs)
	names := []string{}
	for _, f := range fs {
		names = append(names, f.Name)
	}
	return names
}

// The id of the user with the email.
func projectUserID(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&id); err != nil {
		t.Fatalf("user %s: %v", email, err)
	}
	return id
}

// The tables naming a project, which a project's rows are in.
func projectTables(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT TABLE_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND COLUMN_NAME = 'project_id' ORDER BY TABLE_NAME`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	return tables
}

// Whether err is MySQL's refusing a row whose foreign key finds nothing.
func refusedByForeignKey(err error) bool {
	var my *mysql.MySQLError
	return errors.As(err, &my) && my.Number == 1452
}

// Every table names its project, never null, but those above the projects;
// a new table forgetting it fails here.
func TestProjectsEveryTableNamesItsProject(t *testing.T) {
	needDB(t)
	db := secretsDB(t)
	rows, err := db.Query(`SELECT t.TABLE_NAME, COALESCE(c.COLUMN_TYPE, ''), COALESCE(c.IS_NULLABLE, '')
		FROM information_schema.TABLES t LEFT JOIN information_schema.COLUMNS c
			ON c.TABLE_SCHEMA = t.TABLE_SCHEMA AND c.TABLE_NAME = t.TABLE_NAME AND c.COLUMN_NAME = 'project_id'
		WHERE t.TABLE_SCHEMA = DATABASE() AND t.TABLE_TYPE = 'BASE TABLE' ORDER BY t.TABLE_NAME`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table, typ, null string
		if err := rows.Scan(&table, &typ, &null); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
		if slices.Contains(aboveProjects, table) {
			continue
		}
		switch {
		case typ == "":
			t.Errorf("%s names no project: give it project_id BIGINT NOT NULL, or say why it is above projects", table)
		case typ != "bigint" || null != "NO":
			t.Errorf("%s.project_id is %s, nullable %s; want bigint, not null", table, typ, null)
		}
	}
	for _, above := range aboveProjects {
		if !slices.Contains(tables, above) {
			t.Errorf("%s, said to be above the projects, isn't a table", above)
		}
	}
}

// A database from before projects comes out of the migrations with
// everything in Legacy, project 1, and everyone its member: an admin as an
// owner, others as editors, and someone removed, who still owns things,
// removed from it too.
func TestProjectsMigrateEverythingIntoLegacy(t *testing.T) {
	needDB(t)
	root := secretsDB(t)
	name := fmt.Sprintf("%s%d_legacy", testDBPrefix, time.Now().UnixNano())
	if _, err := root.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE IF EXISTS " + name) })
	db := openDB(t, name)
	startApp(t, newFakeWorker(t).url, "DB_NAME="+name).proc.stop()
	if out, err := migrateDB(name, "down-to", "48"); err != nil {
		t.Fatalf("migrate down-to 48: %v\n%s", err, out)
	}

	for _, s := range []string{
		"INSERT INTO users (id, email, admin) VALUES (10, 'admin@example.org', TRUE), (11, 'bob@example.org', FALSE)",
		"INSERT INTO users (id, email, removed_at) VALUES (12, 'gone@example.org', CURRENT_TIMESTAMP(3))",
		"INSERT INTO teams (id, name) VALUES (20, 'Payments')",
		"INSERT INTO team_members (team_id, user_id) VALUES (20, 11)",
		"INSERT INTO user_roles (user_id, name) VALUES (11, 'Finance')",
		"INSERT INTO flows (id, name, owner_team_id) VALUES (30, 'Pay', 20)",
		"INSERT INTO processes (id, flow_id, name, prompt, script) VALUES (40, 30, 'Pay it', '', '')",
		"INSERT INTO runs (id, flow_id, status) VALUES (50, 30, 'done')",
		`INSERT INTO run_steps (id, run_id, process_id, process_name, kind, prompt, inputs, status, output, error, instructions)
			VALUES (60, 50, 40, 'Pay it', 'agent', '', '[]', 'done', '', '', '')`,
		"INSERT INTO skills (kind, name, description, content, owner_user_id) VALUES ('skill', 'old', '', '', 12)",
		"INSERT INTO task_boards (id, name, owner_user_id) VALUES (70, 'Old board', 12)",
		"INSERT INTO task_board_columns (board_id, position, name, struct_id, query) VALUES (70, 0, 'All', 30, '')",
		"INSERT INTO api_keys (user_id, name, prefix, token_hash) VALUES (12, 'old', 'sk-old', REPEAT('a', 64))",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	a := startApp(t, newFakeWorker(t).url, "DB_NAME="+name)
	var legacy struct {
		id               int64
		name, others     string
		inheritsInstance bool
	}
	if err := db.QueryRow("SELECT id, name, others_access, inherits_instance FROM projects").
		Scan(&legacy.id, &legacy.name, &legacy.others, &legacy.inheritsInstance); err != nil {
		t.Fatal(err)
	}
	if legacy.id != 1 || legacy.name != "Legacy" || legacy.others != "none" || !legacy.inheritsInstance {
		t.Errorf("the project made is %+v, want Legacy, 1, inheriting the instance, letting others do nothing", legacy)
	}
	rows, err := db.Query("SELECT user_id, project_id, level, source, removed_at IS NOT NULL FROM project_members ORDER BY user_id")
	if err != nil {
		t.Fatal(err)
	}
	var members []string
	for rows.Next() {
		var user, project int64
		var level, source string
		var removed bool
		if err := rows.Scan(&user, &project, &level, &source, &removed); err != nil {
			t.Fatal(err)
		}
		members = append(members, fmt.Sprintf("%d in %d: %s %s removed=%v", user, project, level, source, removed))
	}
	rows.Close()
	if want := []string{
		"10 in 1: owner manual removed=false",
		"11 in 1: editor manual removed=false",
		"12 in 1: editor manual removed=true",
	}; !slices.Equal(members, want) {
		t.Errorf("members = %q, want %q", members, want)
	}
	for _, table := range projectTables(t, db) {
		var elsewhere int
		if err := db.QueryRow("SELECT COUNT(*) FROM `" + table + "` WHERE project_id <> 1").Scan(&elsewhere); err != nil {
			t.Fatal(err)
		}
		if elsewhere > 0 {
			t.Errorf("%d rows of %s aren't Legacy's", elsewhere, table)
		}
	}

	// What there was is there as before.
	if names := projectFlowNames(a); !slices.Contains(names, "Pay") {
		t.Errorf("flows = %q, want Pay", names)
	}
	if b, ok := taskBoardIn(taskBoards(a), 70); !ok || len(b.Columns) != 1 {
		t.Errorf("the old board is %+v (found %v), want it with its column", b, ok)
	}
	var steps int
	if err := db.QueryRow("SELECT COUNT(*) FROM run_steps WHERE run_id = 50").Scan(&steps); err != nil || steps != 1 {
		t.Errorf("the old run has %d steps (%v), want 1", steps, err)
	}
}

// A new project starts empty, and its flows, procedures, structs, task
// boards and trash are its own: neither project lists the other's, nor
// reaches them by id.
func TestProjectsKeepFlowsAndTaskBoardsApart(t *testing.T) {
	a := newApp(t)
	n := time.Now().UnixNano()
	legacyFlow := a.newFlow(fmt.Sprintf("Legacy flow %d", n))
	var legacyStruct structFlow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprintf("Legacy struct %d", n), "struct": true}, http.StatusCreated, &legacyStruct)
	legacyBoard := taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": fmt.Sprintf("Legacy board %d", n)}, http.StatusCreated)

	// Without sign-in anyone may do anything, in any project.
	var me projectsMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if l, ok := projectIn(me.Projects, 1); !me.Super || !ok || l.Level != "owner" || !l.InheritsInstance {
		t.Errorf("me = %+v, want super, owning Legacy, which inherits the instance", me)
	}

	p := newProject(a, "Apart")
	if p.Level != "owner" || p.Archived || p.OthersAccess != "none" || p.InheritsInstance {
		t.Errorf("the new project is %+v", p)
	}
	a.project = p.ID
	if names := projectFlowNames(a); len(names) != 0 {
		t.Errorf("a new project has the flows %q", names)
	}
	if bs := taskBoards(a); len(bs) != 0 {
		t.Errorf("a new project has the task boards %+v", bs)
	}
	var trash []struct {
		ID int64 `json:"id"`
	}
	a.call("GET", "/api/trash", nil, http.StatusOK, &trash)
	if len(trash) != 0 {
		t.Errorf("a new project's trash holds %+v", trash)
	}

	// Legacy's are nowhere to be found from here.
	for _, c := range []struct {
		method, path string
		body         any
		msg          string
	}{
		{"GET", fmt.Sprintf("/api/flows/%d", legacyFlow.ID), nil, "flow not found"},
		{"PATCH", fmt.Sprintf("/api/flows/%d", legacyFlow.ID), map[string]any{"name": "Mine"}, "flow not found"},
		{"DELETE", fmt.Sprintf("/api/flows/%d", legacyFlow.ID), nil, "flow not found"},
		{"GET", fmt.Sprintf("/api/processes?flow=%d", legacyFlow.ID), nil, "flow not found"},
		{"POST", "/api/processes", map[string]any{"flow": legacyFlow.ID, "name": "Stray", "kind": "agent"}, "flow not found"},
		{"GET", fmt.Sprintf("/api/structs/%d/records", legacyStruct.ID), nil, "flow not found"},
		{"GET", fmt.Sprintf("/api/taskboards/%d", legacyBoard.ID), nil, "task board not found"},
		{"PUT", fmt.Sprintf("/api/taskboards/%d", legacyBoard.ID), map[string]any{"name": "Mine"}, "task board not found"},
		{"DELETE", fmt.Sprintf("/api/taskboards/%d", legacyBoard.ID), nil, "task board not found"},
		{"POST", fmt.Sprintf("/api/structs/%d/records/runs", legacyStruct.ID), map[string]any{"records": []int64{1}}, "flow not found"},
	} {
		a.refused(c.method, c.path, c.body, http.StatusNotFound, c.msg)
	}

	// What is made here is kept here.
	here := a.newFlow(fmt.Sprintf("Here %d", n))
	var hereStruct structFlow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprintf("Here struct %d", n), "struct": true}, http.StatusCreated, &hereStruct)
	hereBoard := taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": fmt.Sprintf("Here board %d", n)}, http.StatusCreated)
	if names := projectFlowNames(a); !slices.Equal(names, []string{here.Name, hereStruct.Name}) {
		t.Errorf("the project's flows are %q, want %s and %s", names, here.Name, hereStruct.Name)
	}
	if bs := taskBoards(a); len(bs) != 1 || bs[0].ID != hereBoard.ID {
		t.Errorf("the project's task boards are %+v, want %d alone", bs, hereBoard.ID)
	}
	// A column can't hold another project's struct.
	a.refused("PUT", fmt.Sprintf("/api/taskboards/%d", hereBoard.ID), map[string]any{
		"name": hereBoard.Name, "columns": []map[string]any{{"name": "Theirs", "struct": legacyStruct.ID, "query": ""}},
	}, http.StatusBadRequest, "isn't there")

	// The trash keeps to its project.
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", here.ID), nil, http.StatusNoContent, nil)
	a.call("GET", "/api/trash", nil, http.StatusOK, &trash)
	if len(trash) != 1 || trash[0].ID != here.ID {
		t.Errorf("the project's trash holds %+v, want %d", trash, here.ID)
	}

	a.project = 0
	if names := projectFlowNames(a); slices.Contains(names, here.Name) || slices.Contains(names, hereStruct.Name) {
		t.Errorf("Legacy lists another project's flows: %q", names)
	}
	if _, ok := taskBoardIn(taskBoards(a), hereBoard.ID); ok {
		t.Errorf("Legacy lists another project's task board")
	}
	a.call("GET", "/api/trash", nil, http.StatusOK, &trash)
	if slices.ContainsFunc(trash, func(f struct {
		ID int64 `json:"id"`
	}) bool {
		return f.ID == here.ID
	}) {
		t.Errorf("Legacy's trash holds another project's flow")
	}
	a.refused("GET", fmt.Sprintf("/api/flows/%d", hereStruct.ID), nil, http.StatusNotFound, "flow not found")
	a.refused("GET", fmt.Sprintf("/api/taskboards/%d", hereBoard.ID), nil, http.StatusNotFound, "task board not found")
	a.refused("POST", fmt.Sprintf("/api/trash/%d/restore", here.ID), nil, http.StatusNotFound, "nothing of that id")
	a.refused("DELETE", fmt.Sprintf("/api/trash/%d", here.ID), nil, http.StatusNotFound, "nothing of that id")

	// A project that isn't, and a header that names none.
	a.project = 1 << 40
	a.refused("GET", "/api/flows", nil, http.StatusNotFound, "no such project")
	a.project = 0
	req, err := http.NewRequest("GET", a.url+"/api/flows", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Spoletta-Project", "Legacy")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a header naming no id got %d, want 400", resp.StatusCode)
	}
}

// A viewer sees and changes nothing but their star; an editor changes the
// project's flows but not who has access; an owner changes that too, but
// makes no projects, which a super admin does, entering any project as its
// owner. Someone given none has no access at all.
func TestProjectsLevels(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	db := secretsDB(t)
	p := newProject(a, "Levels")
	vera, ed, olga, nobu := usersNewPerson("Vera"), usersNewPerson("Ed"), usersNewPerson("Olga"), usersNewPerson("Nobu")
	v, e, o := usersAdd(a, vera.email), usersAdd(a, ed.email), usersAdd(a, olga.email)

	a.project = p.ID
	seen := a.newFlow(fmt.Sprintf("Seen %d", time.Now().UnixNano()))
	usersShare(a, seen.ID)
	var ms []projectMemberRow
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", p.ID), []map[string]any{
		{"user": v.ID, "level": "viewer"}, {"user": e.ID, "level": "editor"}, {"user": o.ID, "level": "owner"},
	}, http.StatusOK, &ms)
	// Ada entered as a super admin, which made her a member that way.
	if m, ok := projectMemberOf(ms, usersAda.email); !ok || m.Source != "super" || m.Level != "owner" {
		t.Errorf("ada in the project = %+v (found %v), want an owner who came in as super admin", m, ok)
	}
	if m, ok := projectMemberOf(ms, vera.email); !ok || m.Source != "manual" || m.Level != "viewer" {
		t.Errorf("vera in the project = %+v (found %v)", m, ok)
	}
	// Who was given it is in the project's audit log.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM audit_events WHERE project_id = ? AND kind = 'project.members' AND detail LIKE ?",
			p.ID, "%"+vera.email+" as viewer%").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the project's audit log has %d events of its members given", n)
		}
		time.Sleep(50 * time.Millisecond)
	}

	usersSignIn(a, prov, vera)
	if names := projectFlowNames(a); !slices.Contains(names, seen.Name) {
		t.Errorf("the viewer sees %q", names)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/flows", map[string]any{"name": "Nope"}},
		{"PATCH", fmt.Sprintf("/api/flows/%d", seen.ID), map[string]any{"name": "Nope"}},
		{"POST", "/api/taskboards", map[string]any{"name": "Nope"}},
		{"POST", "/api/runs", map[string]any{"flow": seen.ID}},
	} {
		a.refused(c.method, c.path, c.body, http.StatusForbidden, "you only see this project")
	}
	a.call("PUT", fmt.Sprintf("/api/flows/%d/favourite", seen.ID), nil, http.StatusNoContent, nil)
	a.refused("GET", fmt.Sprintf("/api/projects/%d/members", p.ID), nil, http.StatusForbidden, "only the project's owners")
	var me projectsMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if mine, ok := projectIn(me.Projects, p.ID); me.Super || me.User.Admin || !ok || mine.Level != "viewer" {
		t.Errorf("the viewer's me = %+v", me)
	}

	usersSignIn(a, prov, ed)
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprintf("Ed's %d", time.Now().UnixNano())}, http.StatusCreated, nil)
	a.refused("PATCH", fmt.Sprintf("/api/projects/%d", p.ID), map[string]any{"name": "Mine"}, http.StatusForbidden, "only the project's owners")
	a.refused("PUT", fmt.Sprintf("/api/projects/%d/groups", p.ID), []any{}, http.StatusForbidden, "only the project's owners")

	usersSignIn(a, prov, olga)
	renamed := fmt.Sprintf("Levels renamed %d", time.Now().UnixNano())
	var after projectRow
	a.call("PATCH", fmt.Sprintf("/api/projects/%d", p.ID), map[string]any{"name": renamed}, http.StatusOK, &after)
	if after.Name != renamed {
		t.Errorf("renamed to %q, want %q", after.Name, renamed)
	}
	a.call("GET", fmt.Sprintf("/api/projects/%d/members", p.ID), nil, http.StatusOK, nil)
	a.refused("PUT", fmt.Sprintf("/api/projects/%d/members", p.ID), []map[string]any{{"user": o.ID, "level": "editor"}},
		http.StatusBadRequest, "yourself")
	a.refused("POST", "/api/projects", map[string]any{"name": "Olga's own"}, http.StatusForbidden, "only an admin")
	a.refused("DELETE", fmt.Sprintf("/api/projects/%d", p.ID), nil, http.StatusForbidden, "only an admin")
	a.refused("POST", "/api/users", map[string]any{"email": "x@example.org"}, http.StatusForbidden, "only an admin")
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if me.Super || !me.User.Admin {
		t.Errorf("the owner's me in her project = %+v, want its admin, not super", me)
	}
	// In Legacy, which she joined as she was added, she is an editor.
	a.project = 0
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if mine, ok := projectIn(me.Projects, 1); me.User.Admin || !ok || mine.Level != "editor" {
		t.Errorf("the owner's me in Legacy = %+v", me)
	}

	// Someone signing in who was given none of it has no access to it.
	usersSignIn(a, prov, nobu)
	a.project = p.ID
	a.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")
	a.refused("GET", fmt.Sprintf("/api/flows/%d", seen.ID), nil, http.StatusForbidden, "no access to this project")
	a.refused("GET", fmt.Sprintf("/api/projects/%d/members", p.ID), nil, http.StatusNotFound, "no such project")
	var ps []projectRow
	a.call("GET", "/api/projects", nil, http.StatusOK, &ps)
	if _, ok := projectIn(ps, p.ID); ok {
		t.Errorf("someone with no access lists the project")
	}
}

// While projects come in, the instance's admins own Legacy, as they had
// everything before: someone made an admin no longer is its owner no more,
// as they were no admin, and made one again, or added back as one, is
// again.
func TestProjectsAdminsOwnLegacy(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	amy := usersNewPerson("Amy")
	var row usersPersonRow
	a.call("POST", "/api/users", map[string]any{"email": amy.email, "admin": true}, http.StatusCreated, &row)
	legacyAdmin := func(want bool) {
		t.Helper()
		usersSignIn(a, prov, amy)
		var me projectsMe
		a.call("GET", "/api/me", nil, http.StatusOK, &me)
		level := "editor"
		if want {
			level = "owner"
		}
		if l, _ := projectIn(me.Projects, 1); me.Super != want || me.User.Admin != want || l.Level != level {
			t.Errorf("amy's me = %+v, want super and Legacy's admin %v, %s", me, want, level)
		}
		if want {
			a.call("GET", "/api/model-keys", nil, http.StatusOK, nil)
		} else {
			a.refused("GET", "/api/model-keys", nil, http.StatusForbidden, "only an admin")
		}
		usersSignIn(a, prov, usersAda)
	}
	legacyAdmin(true)
	a.call("PATCH", fmt.Sprintf("/api/users/%d", row.ID), map[string]any{"admin": false}, http.StatusOK, nil)
	legacyAdmin(false)
	a.call("PATCH", fmt.Sprintf("/api/users/%d", row.ID), map[string]any{"admin": true}, http.StatusOK, nil)
	legacyAdmin(true)
	a.call("DELETE", fmt.Sprintf("/api/users/%d", row.ID), nil, http.StatusNoContent, nil)
	a.call("POST", "/api/users", map[string]any{"email": amy.email}, http.StatusCreated, nil)
	legacyAdmin(false)
	a.call("DELETE", fmt.Sprintf("/api/users/%d", row.ID), nil, http.StatusNoContent, nil)
	a.call("POST", "/api/users", map[string]any{"email": amy.email, "admin": true}, http.StatusCreated, nil)
	legacyAdmin(true)
}

// A group the identity provider puts people in, given to a project, lets
// them in at once, as a member from their group; taking it away takes them
// out, leaving what they made.
func TestProjectsGroupsGiveAccessAtOnce(t *testing.T) {
	a, prov := peopleApp(t)
	gina := usersNewPerson("Gina")
	usersSignIn(a, prov, gina)
	usersSignIn(a, prov, usersAda)
	p := newProject(a, "Grouped")
	a.project = p.ID
	shared := a.newFlow(fmt.Sprintf("For the group %d", time.Now().UnixNano()))
	usersShare(a, shared.ID)

	usersSignIn(a, prov, gina)
	a.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")
	// The groups she is in, as she signs in; see projects_people_e2e_test.go.
	group := fmt.Sprintf("engineering-%d", time.Now().UnixNano())
	peopleInGroups(prov, gina, []string{group})
	usersSignIn(a, prov, gina)
	a.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")

	usersSignIn(a, prov, usersAda)
	var gs []map[string]string
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", p.ID), []map[string]any{{"group": group, "level": "editor"}}, http.StatusOK, &gs)
	if len(gs) != 1 || gs[0]["group"] != group || gs[0]["level"] != "editor" {
		t.Errorf("groups = %v", gs)
	}

	usersSignIn(a, prov, gina)
	if names := projectFlowNames(a); !slices.Contains(names, shared.Name) {
		t.Errorf("gina sees %q", names)
	}
	hers := a.newFlow(fmt.Sprintf("Gina's %d", time.Now().UnixNano()))
	usersShare(a, hers.ID)

	usersSignIn(a, prov, usersAda)
	var ms []projectMemberRow
	a.call("GET", fmt.Sprintf("/api/projects/%d/members", p.ID), nil, http.StatusOK, &ms)
	if m, ok := projectMemberOf(ms, gina.email); !ok || m.Source != "group" || m.Level != "editor" || m.RemovedAt != nil {
		t.Errorf("gina in the project = %+v (found %v), want an editor from her group", m, ok)
	}
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", p.ID), []any{}, http.StatusOK, nil)
	a.call("GET", fmt.Sprintf("/api/projects/%d/members", p.ID), nil, http.StatusOK, &ms)
	if m, ok := projectMemberOf(ms, gina.email); !ok || m.RemovedAt == nil {
		t.Errorf("gina in the project = %+v (found %v), want her removed", m, ok)
	}
	if names := projectFlowNames(a); !slices.Contains(names, hers.Name) {
		t.Errorf("what gina made went with her: %q", names)
	}

	usersSignIn(a, prov, gina)
	a.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")
}

// What a project lets anyone signed in do: nothing, seeing, or changing;
// those who came in that way are removed as it goes back.
func TestProjectsOthersAccess(t *testing.T) {
	a, prov := usersApp(t)
	frank := usersNewPerson("Frank")
	usersSignIn(a, prov, frank)
	usersSignIn(a, prov, usersAda)
	p := newProject(a, "Open")
	a.project = p.ID
	shared := a.newFlow(fmt.Sprintf("Open to all %d", time.Now().UnixNano()))
	usersShare(a, shared.ID)

	others := func(access string) {
		t.Helper()
		usersSignIn(a, prov, usersAda)
		var after projectRow
		a.call("PATCH", fmt.Sprintf("/api/projects/%d", p.ID), map[string]any{"othersAccess": access}, http.StatusOK, &after)
		if after.OthersAccess != access {
			t.Fatalf("others may %s, want %s", after.OthersAccess, access)
		}
		usersSignIn(a, prov, frank)
	}
	usersSignIn(a, prov, frank)
	a.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")
	others("view")
	if names := projectFlowNames(a); !slices.Contains(names, shared.Name) {
		t.Errorf("frank sees %q", names)
	}
	a.refused("POST", "/api/flows", map[string]any{"name": "Nope"}, http.StatusForbidden, "you only see this project")
	others("edit")
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprintf("Frank's %d", time.Now().UnixNano())}, http.StatusCreated, nil)

	usersSignIn(a, prov, usersAda)
	var ms []projectMemberRow
	a.call("GET", fmt.Sprintf("/api/projects/%d/members", p.ID), nil, http.StatusOK, &ms)
	if m, ok := projectMemberOf(ms, frank.email); !ok || m.Source != "open" || m.RemovedAt != nil {
		t.Errorf("frank in the project = %+v (found %v), want a member who came in as others may", m, ok)
	}
	others("none")
	a.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")
	usersSignIn(a, prov, usersAda)
	a.call("GET", fmt.Sprintf("/api/projects/%d/members", p.ID), nil, http.StatusOK, &ms)
	if m, ok := projectMemberOf(ms, frank.email); !ok || m.RemovedAt == nil {
		t.Errorf("frank in the project = %+v (found %v), want him removed", m, ok)
	}
	a.refused("PATCH", fmt.Sprintf("/api/projects/%d", p.ID), map[string]any{"othersAccess": "everything"}, http.StatusBadRequest, "othersAccess")
}

// The database itself keeps a row in its parent's project: a process
// inserted without naming its flow's project, as one not yet made aware of
// projects would be, is refused, as are an owner of the flow outside its
// project, a team or a person.
func TestProjectsCompositeKeysRefuseAnotherProject(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	p := newProject(a, "Keys")
	a.project = p.ID
	f := a.newFlow(fmt.Sprintf("Keyed %d", time.Now().UnixNano()))

	if _, err := db.Exec("INSERT INTO processes (flow_id, name, prompt, script) VALUES (?, 'Stray', '', '')", f.ID); !refusedByForeignKey(err) {
		t.Errorf("a process of Legacy in the project's flow: %v, want refused", err)
	}
	if _, err := db.Exec("INSERT INTO processes (flow_id, name, prompt, script, project_id) VALUES (?, 'Kept', '', '', ?)", f.ID, p.ID); err != nil {
		t.Errorf("a process of the flow's project: %v", err)
	}
	res, err := db.Exec("INSERT INTO teams (name) VALUES (?)", fmt.Sprintf("Legacy team %d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	team, _ := res.LastInsertId()
	if _, err := db.Exec("UPDATE flows SET owner_team_id = ? WHERE id = ?", team, f.ID); !refusedByForeignKey(err) {
		t.Errorf("a team of Legacy owning the project's flow: %v, want refused", err)
	}
	res, err = db.Exec("INSERT INTO users (email) VALUES (?)", fmt.Sprintf("outsider%d@example.org", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	outsider, _ := res.LastInsertId()
	if _, err := db.Exec("UPDATE flows SET owner_user_id = ? WHERE id = ?", outsider, f.ID); !refusedByForeignKey(err) {
		t.Errorf("someone outside the project owning its flow: %v, want refused", err)
	}
	if _, err := db.Exec("INSERT INTO team_members (team_id, user_id) VALUES (?, ?)", team, outsider); !refusedByForeignKey(err) {
		t.Errorf("someone outside Legacy in its team: %v, want refused", err)
	}
	board := taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": fmt.Sprintf("Keyed board %d", time.Now().UnixNano())}, http.StatusCreated)
	a.project = 0
	legacy := a.newFlow(fmt.Sprintf("Legacy's %d", time.Now().UnixNano()))
	if _, err := db.Exec("INSERT INTO task_board_columns (board_id, position, name, struct_id, query, project_id) VALUES (?, 0, 'Theirs', ?, '', ?)",
		board.ID, legacy.ID, p.ID); !refusedByForeignKey(err) {
		t.Errorf("a column holding another project's struct: %v, want refused", err)
	}
}

// Archiving a project keeps everyone but a super admin out of it; deleting
// it for good, once archived, takes everything in it, and both are
// recorded above the projects. Legacy is never deleted.
func TestProjectsArchiveAndDelete(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	p := newProject(a, "Doomed")
	a.project = p.ID
	a.newFlow("Doomed flow")
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Doomed struct", "struct": true}, http.StatusCreated, &s)
	taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": "Doomed board"}, http.StatusCreated)

	a.project = 0
	a.refused("DELETE", fmt.Sprintf("/api/projects/%d?forever=true", p.ID), nil, http.StatusConflict, "archive the project")
	a.call("DELETE", fmt.Sprintf("/api/projects/%d", p.ID), nil, http.StatusNoContent, nil)
	var ps []projectRow
	a.call("GET", "/api/projects", nil, http.StatusOK, &ps)
	if got, ok := projectIn(ps, p.ID); !ok || !got.Archived {
		t.Errorf("the archived project is %+v (found %v)", got, ok)
	}
	a.call("DELETE", fmt.Sprintf("/api/projects/%d?forever=true", p.ID), nil, http.StatusNoContent, nil)
	a.call("GET", "/api/projects", nil, http.StatusOK, &ps)
	if _, ok := projectIn(ps, p.ID); ok {
		t.Errorf("the project deleted for good is still listed")
	}
	for _, table := range projectTables(t, db) {
		var left int
		if err := db.QueryRow("SELECT COUNT(*) FROM `"+table+"` WHERE project_id = ?", p.ID).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left > 0 {
			t.Errorf("%d rows of %s are left of the project deleted for good", left, table)
		}
	}
	var kinds []string
	rows, err := db.Query("SELECT kind FROM instance_events WHERE detail LIKE ? ORDER BY id", fmt.Sprintf("%%(%d)%%", p.ID))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		rows.Scan(&k)
		kinds = append(kinds, k)
	}
	rows.Close()
	if want := []string{"project.made", "project.archived", "project.deleted"}; !slices.Equal(kinds, want) {
		t.Errorf("instance events = %q, want %q", kinds, want)
	}
	a.refused("DELETE", "/api/projects/1?forever=true", nil, http.StatusConflict, "can be archived but not deleted")
}

// An archived project is listed to a super admin alone, and entered by
// them alone, who may bring it back.
func TestProjectsArchivedOnlyForSuperAdmins(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	p := newProject(a, "Shelved")
	olga := usersNewPerson("Olga")
	o := usersAdd(a, olga.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", p.ID), []map[string]any{{"user": o.ID, "level": "owner"}}, http.StatusOK, nil)
	a.call("DELETE", fmt.Sprintf("/api/projects/%d", p.ID), nil, http.StatusNoContent, nil)
	a.project = p.ID
	a.call("GET", "/api/flows", nil, http.StatusOK, nil)

	usersSignIn(a, prov, olga)
	a.refused("GET", "/api/flows", nil, http.StatusNotFound, "no such project")
	var ps []projectRow
	a.call("GET", "/api/projects", nil, http.StatusOK, &ps)
	if _, ok := projectIn(ps, p.ID); ok {
		t.Errorf("an owner who isn't super lists the archived project")
	}
	a.refused("PATCH", fmt.Sprintf("/api/projects/%d", p.ID), map[string]any{"archived": false}, http.StatusNotFound, "no such project")

	usersSignIn(a, prov, usersAda)
	a.call("PATCH", fmt.Sprintf("/api/projects/%d", p.ID), map[string]any{"archived": false}, http.StatusOK, nil)
	usersSignIn(a, prov, olga)
	a.call("GET", "/api/flows", nil, http.StatusOK, nil)
}

// Going down past the projects loses which project everything was in, so
// it is refused while a project but Legacy is left, undoing nothing; with
// Legacy alone it goes down, and the webapp starting brings it up again as
// it was.
func TestProjectsMigrateDownWithLegacyAlone(t *testing.T) {
	needDB(t)
	root := secretsDB(t)
	name := fmt.Sprintf("%s%d_projects_down", testDBPrefix, time.Now().UnixNano())
	if _, err := root.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE IF EXISTS " + name) })
	db := openDB(t, name)
	startApp(t, newFakeWorker(t).url, "DB_NAME="+name).proc.stop()
	up := columns(t, db)
	latest := func() int {
		t.Helper()
		var v int
		if err := db.QueryRow("SELECT MAX(version_id) FROM goose_db_version WHERE is_applied").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	top := latest()

	if _, err := db.Exec("INSERT INTO projects (name) VALUES ('Another')"); err != nil {
		t.Fatal(err)
	}
	out, err := migrateDB(name, "down-to", "48")
	if err == nil || !strings.Contains(out, "delete every project but Legacy before migrating down") {
		t.Errorf("migrate down-to 48 with another project = %v, want it refused:\n%s", err, out)
	}
	if v := latest(); v != top {
		t.Errorf("refused, the database went down to %d from %d", v, top)
	}
	if again := columns(t, db); !slices.Equal(up, again) {
		t.Errorf("refusing to go down changed the columns")
	}

	if _, err := db.Exec("DELETE FROM projects WHERE name = 'Another'"); err != nil {
		t.Fatal(err)
	}
	if out, err := migrateDB(name, "down-to", "48"); err != nil {
		t.Fatalf("migrate down-to 48 with Legacy alone: %v\n%s", err, out)
	}
	if v := latest(); v != 48 {
		t.Errorf("down to %d, want 48", v)
	}
	if down := columns(t, db); slices.ContainsFunc(down, func(c string) bool { return strings.Contains(c, ".project_id ") }) {
		t.Errorf("a project_id column is still there after going down")
	}
	var projects int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'projects'").Scan(&projects); err != nil || projects != 0 {
		t.Errorf("the projects table is still there (%v)", err)
	}

	startApp(t, newFakeWorker(t).url, "DB_NAME="+name)
	if again := columns(t, db); !slices.Equal(up, again) {
		t.Errorf("going down and up again changed the columns:\nbefore %v\nafter  %v", up, again)
	}
}
