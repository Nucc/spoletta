package main

// A project is a tenant: a standalone space with its own flows, task boards,
// procedures and structs, its own settings, teams, roles and audit log.
// Nothing in one reaches anything in another; every table below the
// projects names its project, and the database checks that a row is in its
// parent's (see migrations 00049 to 00053, and docs/PROJECTS.md).
//
// People stay the instance's: one sign-in, one user. A super admin
// (users.admin) makes, renames, archives and deletes projects and manages
// people. Within a project someone is at one of three levels: an owner may
// do everything, its settings and who has access included, as an admin did
// before projects; an editor changes and runs flows, task boards,
// procedures and structs, as an ordinary user did; a viewer sees boards,
// runs and records and changes and starts nothing.
//
// Access comes from, the highest winning: being a member assigned by hand
// (project_members, source manual); a group the identity provider puts them
// in that is assigned to the project (project_groups, matched against
// user_groups); what the project lets anyone signed in do (others_access);
// and being a super admin, who enters any project as owner. Only a manual
// member's level is kept: what a group, others_access or being a super
// admin give is worked out at each request, so assigning or unassigning a
// group takes effect at once. Someone coming in another way than by hand
// gets a member row the first time they enter, with that source, so what
// they own has a member to point at; it is soft-removed (removed_at) when
// the group is unassigned or others_access goes back, and what they made
// stays. Removing a member is always soft.
//
// Every /api/ request names its project in the X-Spoletta-Project header,
// and is worked on there: enterProject, before guardFlows, works out the
// person's level and refuses them if they have none (403) or there is no
// such project (404), puts the project in the request's context
// (requestProject), and the request's user with Admin meaning owner of this
// project and Super the instance's admin. A viewer's requests that change
// or start anything are refused there, in one place.
//
// Transition, until the projects work is done: a request without the header
// is Legacy's; and the instance's people are Legacy's members, someone an
// admin adds or who signs in the first time becoming one, an owner if an
// admin else an editor, as everyone was in the one space there was before
// projects (joinLegacy), and making someone an admin or no longer one moves
// them to owner or editor there (followAdminInLegacy). Legacy is project 1,
// inheriting what the instance is configured with outside the database.
//
// Without sign-in there is nobody, as before: every request may do
// anything, including making and switching projects.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Legacy, which everything from before projects belongs to.
const legacyProject int64 = 1

// The header every /api/ request names its project in.
const projectHeader = "X-Spoletta-Project"

// How far someone may go in a project.
const (
	levelViewer = "viewer"
	levelEditor = "editor"
	levelOwner  = "owner"
)

// The levels in order, the higher winning.
var levelRank = map[string]int{levelViewer: 1, levelEditor: 2, levelOwner: 3}

// How someone came to be a project's member.
const (
	sourceManual = "manual"
	sourceGroup  = "group"
	sourceOpen   = "open"
	sourceSuper  = "super"
)

// What a project lets anyone signed in to the instance do, and the level
// that gives them.
var othersLevels = map[string]string{"none": "", "view": levelViewer, "edit": levelEditor}

// The longest project and group names.
const (
	maxProjectName = 255
	maxGroupName   = 255
)

type projectKey struct{}

// The project the request is worked on in; Legacy outside /api/.
func requestProject(r *http.Request) int64 {
	if p, ok := r.Context().Value(projectKey{}).(int64); ok {
		return p
	}
	return legacyProject
}

// A project as someone may enter it.
type projectView struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Level            string `json:"level"`
	Archived         bool   `json:"archived"`
	OthersAccess     string `json:"othersAccess"`
	InheritsInstance bool   `json:"inheritsInstance"`

	// How they come to their level, and whether they have a member row that
	// isn't removed, and its level and source.
	how                       string
	member                    bool
	memberLevel, memberSource string
	// The highest level of a group of theirs assigned to it.
	groupRank int
}

// The level of the rank given; none for 0.
func rankLevel(rank int) string {
	for l, r := range levelRank {
		if r == rank {
			return l
		}
	}
	return ""
}

// Works out the user's level in the project, and how they come to it: the
// highest of what their manual membership, their groups, others_access and
// being a super admin give, earlier ones winning ties. Nobody signed in is
// everyone's owner; an archived project is entered only by a super admin.
func (v *projectView) work(u *user) {
	if u == nil {
		v.Level, v.how = levelOwner, ""
		return
	}
	best, how := "", ""
	consider := func(level, source string) {
		if levelRank[level] > levelRank[best] {
			best, how = level, source
		}
	}
	if v.member && v.memberSource == sourceManual {
		consider(v.memberLevel, sourceManual)
	}
	consider(rankLevel(v.groupRank), sourceGroup)
	consider(othersLevels[v.OthersAccess], sourceOpen)
	if u.Super {
		consider(levelOwner, sourceSuper)
	}
	if v.Archived && !u.Super {
		best, how = "", ""
	}
	v.Level, v.how = best, how
}

// Whether entering makes them a member row, as they have none.
func (v *projectView) needsRow() bool {
	return v.Level != "" && v.how != "" && v.how != sourceManual && !v.member
}

// Every project, or only the one given, with the user's level there, by
// name; one they may not enter has no level.
func projectsOf(ctx context.Context, db rowsQuerier, u *user, only int64) ([]projectView, error) {
	var uid int64
	if u != nil {
		uid = u.ID
	}
	q := `SELECT p.id, p.name, p.archived_at IS NOT NULL, p.inherits_instance, p.others_access,
			m.user_id IS NOT NULL, COALESCE(m.level, ''), COALESCE(m.source, ''),
			COALESCE((SELECT MAX(CASE g.level WHEN 'owner' THEN 3 WHEN 'editor' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END)
				FROM project_groups g JOIN user_groups ug ON ug.group_name = g.group_name
				WHERE g.project_id = p.id AND ug.user_id = ?), 0)
		FROM projects p LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = ? AND m.removed_at IS NULL`
	args := []any{uid, uid}
	if only != 0 {
		q += " WHERE p.id = ?"
		args = append(args, only)
	}
	vs := []projectView{}
	err := scanAll(ctx, db, q+" ORDER BY p.name, p.id", func(rows *sql.Rows) error {
		var v projectView
		if err := rows.Scan(&v.ID, &v.Name, &v.Archived, &v.InheritsInstance, &v.OthersAccess,
			&v.member, &v.memberLevel, &v.memberSource, &v.groupRank); err != nil {
			return err
		}
		v.work(u)
		vs = append(vs, v)
		return nil
	}, args...)
	return vs, err
}

// The project as the user may enter it; false if there is no such project.
func projectOf(ctx context.Context, db rowsQuerier, u *user, id int64) (projectView, bool, error) {
	vs, err := projectsOf(ctx, db, u, id)
	if err != nil || len(vs) == 0 {
		return projectView{}, false, err
	}
	return vs[0], true, nil
}

// The projects the user may enter, as /api/me and the projects' list name
// them: every one for a super admin, archived too.
func enterableProjects(ctx context.Context, db rowsQuerier, u *user) ([]projectView, error) {
	vs, err := projectsOf(ctx, db, u, 0)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(vs, func(v projectView) bool { return v.Level == "" }), nil
}

// Makes the member row of someone entering the project another way than by
// hand, or brings back their removed one; a row someone else's request made
// meanwhile stays as it is.
func enterAsMember(ctx context.Context, db *sql.DB, project, user int64, level, source string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, level, source) VALUES (?, ?, ?, ?) AS entering
		ON DUPLICATE KEY UPDATE level = IF(project_members.removed_at IS NULL, project_members.level, entering.level),
			source = IF(project_members.removed_at IS NULL, project_members.source, entering.source), removed_at = NULL`,
		project, user, level, source)
	return err
}

// Requests about the person or the projects themselves, rather than what is
// in the project their header names: they go on whatever their level there.
// So does the OAuth callback of an MCP server's sign-in, which comes back
// with only the sign-in's state, whose project it is (mcp.go).
func outsideProjects(path string) bool {
	return path == "/api/me" || strings.HasPrefix(path, "/api/me/") ||
		path == "/api/projects" || strings.HasPrefix(path, "/api/projects/") ||
		path == "/api/mcp-oauth/callback"
}

// Works out the project a request to /api/ is about, from its header, and
// the person's level there; refuses them if they may not enter it, or, a
// viewer, if the request would change or start anything; and passes it on
// with the project and the person as they are there. See the top of this
// file.
func enterProject(db *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		project := legacyProject
		h := strings.TrimSpace(r.Header.Get(projectHeader))
		// A page the browser opens, such as an MCP server's sign-in window
		// or a run's file, can't send the header, so a GET names its
		// project in its address instead.
		if h == "" && r.Method == http.MethodGet {
			h = strings.TrimSpace(r.URL.Query().Get("project"))
		}
		if h != "" {
			id, err := strconv.ParseInt(h, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, projectHeader+" must be a project's id", http.StatusBadRequest)
				return
			}
			project = id
		}
		ctx := r.Context()
		u := requestUser(r)
		v, found, err := projectOf(ctx, db, u, project)
		if err != nil {
			serverError(w, err)
			return
		}
		if !outsideProjects(r.URL.Path) {
			switch {
			case !found || (v.Archived && v.Level == ""):
				http.Error(w, "no such project", http.StatusNotFound)
				return
			case v.Level == "":
				http.Error(w, "you have no access to this project; ask one of its owners", http.StatusForbidden)
				return
			case v.Level == levelViewer && neededAccess(r) == accessEdit:
				http.Error(w, "you only see this project; ask one of its owners to let you change it", http.StatusForbidden)
				return
			}
			if u != nil && v.needsRow() {
				if err := enterAsMember(ctx, db, project, u.ID, v.Level, v.how); err != nil {
					serverError(w, err)
					return
				}
			}
		}
		ctx = context.WithValue(ctx, projectKey{}, project)
		if u != nil {
			as := *u
			as.Admin = v.Level == levelOwner
			as.Project = project
			ctx = context.WithValue(ctx, userKey{}, &as)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Turns away a request from someone not a super admin, when people sign
// in: managing people and projects is the instance's.
func refuseNonSuper(w http.ResponseWriter, r *http.Request) bool {
	if u := requestUser(r); u != nil && !u.Super {
		http.Error(w, "only an admin can do that", http.StatusForbidden)
		return true
	}
	return false
}

// The level an admin of the instance has in Legacy, and anyone else.
func legacyLevel(admin bool) string {
	if admin {
		return levelOwner
	}
	return levelEditor
}

// Transition: makes the person a member of Legacy by hand, an owner if an
// admin else an editor, as everyone was in the one space there was before
// projects; brings them back if they were removed from it, at that level.
// Run as an admin adds someone, or adds them back, and as someone signs in
// the first time.
func joinLegacy(ctx context.Context, tx *sql.Tx, user int64, admin bool) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, level, source) VALUES (?, ?, ?, ?) AS joining
		ON DUPLICATE KEY UPDATE level = joining.level, source = joining.source, removed_at = NULL`,
		legacyProject, user, legacyLevel(admin), sourceManual)
	return err
}

// Transition: keeps someone's level in Legacy, given by hand, in step with
// whether they are the instance's admin, so making them one or no longer
// one gives or takes Legacy as it gave or took everything before projects.
// It overrides a level an owner gave them there by hand meanwhile.
func followAdminInLegacy(ctx context.Context, tx *sql.Tx, user int64, admin bool) error {
	_, err := tx.ExecContext(ctx, "UPDATE project_members SET level = ? WHERE project_id = ? AND user_id = ? AND source = ?",
		legacyLevel(admin), legacyProject, user, sourceManual)
	return err
}

// Makes the person a member of each project the flows the query names are
// in where they have no member row, removed or not, as a super admin
// entering it would be, so what is given to them there has a member to
// point at.
func memberWhereFlows(ctx context.Context, tx *sql.Tx, user int64, flows string, args ...any) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, level, source)
		SELECT DISTINCT f.project_id, ?, ?, ? FROM flows f WHERE `+flows+`
			AND NOT EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = f.project_id AND m.user_id = ?)`,
		append(append([]any{user, levelOwner, sourceSuper}, args...), user)...)
	return err
}

// Records what happened above the projects, such as a project made or
// deleted, in instance_events.
func instanceEvent(ctx context.Context, db *sql.DB, who *int64, kind, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	if _, err := db.ExecContext(ctx, "INSERT INTO instance_events (user_id, kind, detail) VALUES (?, ?, ?)", who, kind, truncate(detail, 4000)); err != nil {
		log.Printf("record the instance event %s %s: %v", kind, detail, err)
	}
}

// A project's member as its owners see them.
type projectMember struct {
	User      int64      `json:"user"`
	Email     string     `json:"email"`
	Name      string     `json:"name"`
	Level     string     `json:"level"`
	Source    string     `json:"source"`
	RemovedAt *time.Time `json:"removedAt"`
}

func projectMembers(ctx context.Context, db rowsQuerier, project int64) ([]projectMember, error) {
	ms := []projectMember{}
	err := scanAll(ctx, db, `SELECT m.user_id, u.email, u.name, m.level, m.source, m.removed_at FROM project_members m
		JOIN users u ON u.id = m.user_id WHERE m.project_id = ? ORDER BY m.removed_at IS NOT NULL, COALESCE(NULLIF(u.name, ''), u.email), m.user_id`,
		func(rows *sql.Rows) error {
			var m projectMember
			err := rows.Scan(&m.User, &m.Email, &m.Name, &m.Level, &m.Source, &m.RemovedAt)
			ms = append(ms, m)
			return err
		}, project)
	return ms, err
}

// A group of the identity provider's assigned to a project.
type projectGroup struct {
	Group string `json:"group"`
	Level string `json:"level"`
}

func projectGroups(ctx context.Context, db rowsQuerier, project int64) ([]projectGroup, error) {
	gs := []projectGroup{}
	err := scanAll(ctx, db, "SELECT group_name, level FROM project_groups WHERE project_id = ? ORDER BY group_name", func(rows *sql.Rows) error {
		var g projectGroup
		err := rows.Scan(&g.Group, &g.Level)
		gs = append(gs, g)
		return err
	}, project)
	return gs, err
}

// A project's name as given, trimmed, or why it can't be.
func projectName(name string) (string, string) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", "a project needs a name"
	case len(name) > maxProjectName:
		return "", fmt.Sprintf("a project's name is at most %d characters", maxProjectName)
	}
	return name, ""
}

// The tables with rows of a project, which deleting it for good empties.
func projectTables(ctx context.Context, db rowsQuerier) ([]string, error) {
	var tables []string
	err := scanAll(ctx, db, `SELECT TABLE_NAME FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND COLUMN_NAME = 'project_id' ORDER BY TABLE_NAME`, func(rows *sql.Rows) error {
		var t string
		err := rows.Scan(&t)
		tables = append(tables, t)
		return err
	})
	return tables, err
}

// Why a project can't be deleted for good.
var (
	errProjectNotArchived = errors.New("archive the project before deleting it for good")
	errProjectRunning     = errors.New("runs are going on in the project; stop them, or wait for them to end, first")
)

// Deletes an archived project for good, with everything in it, and returns
// its runs, whose workspaces are to go too. Its rows refer to each other
// alone, which the composite keys make sure of, so they go table by table
// with foreign keys unchecked, on a connection of their own.
func deleteProject(ctx context.Context, db *sql.DB, project int64) ([]int64, error) {
	tables, err := projectTables(ctx, db)
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return nil, err
	}
	// The connection goes back to the pool checking them again, whatever
	// happened; one that can't is dropped instead.
	defer func() {
		if _, err := conn.ExecContext(context.Background(), "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var archived bool
	if err := tx.QueryRowContext(ctx, "SELECT archived_at IS NOT NULL FROM projects WHERE id = ? FOR UPDATE", project).Scan(&archived); err != nil {
		return nil, err
	}
	if !archived {
		return nil, errProjectNotArchived
	}
	var running int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE project_id = ? AND status = ?", project, runRunning).Scan(&running); err != nil {
		return nil, err
	}
	if running > 0 {
		return nil, errProjectRunning
	}
	var runs []int64
	if err := scanAll(ctx, tx, "SELECT id FROM runs WHERE project_id = ?", func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		runs = append(runs, id)
		return err
	}, project); err != nil {
		return nil, err
	}
	for _, t := range tables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM `"+t+"` WHERE project_id = ?", project); err != nil {
			return nil, fmt.Errorf("empty %s: %w", t, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", project); err != nil {
		return nil, err
	}
	return runs, tx.Commit()
}

func registerProjectRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	// The project the path names, as the person may enter it, if they are
	// its owner; false once it has answered. One they may not enter at all
	// is no project of theirs.
	owned := func(w http.ResponseWriter, r *http.Request) (projectView, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return projectView{}, false
		}
		v, found, err := projectOf(r.Context(), db, requestUser(r), id)
		switch {
		case err != nil:
			serverError(w, err)
			return v, false
		case !found || v.Level == "":
			http.Error(w, "no such project", http.StatusNotFound)
			return v, false
		case v.Level != levelOwner:
			http.Error(w, "only the project's owners can do that", http.StatusForbidden)
			return v, false
		}
		return v, true
	}

	// The projects whoever asks may enter, by name, each with their level
	// there; a super admin's are all of them, archived too.
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		vs, err := enterableProjects(r.Context(), db, requestUser(r))
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, vs)
	})

	// Makes an empty project: {"name"}; a super admin's.
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonSuper(w, r) {
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		name, msg := projectName(body.Name)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		res, err := db.ExecContext(r.Context(), "INSERT INTO projects (name) VALUES (?)", name)
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			http.Error(w, "another project has that name", http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		id, err := res.LastInsertId()
		if err != nil {
			serverError(w, err)
			return
		}
		instanceEvent(r.Context(), db, requestUserID(r), "project.made", "made the project %s (%d)", name, id)
		v, _, err := projectOf(r.Context(), db, requestUser(r), id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	})

	// Renames the project, or says what anyone signed in may do in it:
	// {"name", "othersAccess": "none" | "view" | "edit"}; its owners'. A
	// super admin brings an archived one back with {"archived": false}.
	// Those who came in through others_access are removed as it goes back.
	mux.HandleFunc("PATCH /api/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := owned(w, r)
		if !ok {
			return
		}
		var patch struct {
			Name         *string `json:"name"`
			OthersAccess *string `json:"othersAccess"`
			Archived     *bool   `json:"archived"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Name != nil {
			name, msg := projectName(*patch.Name)
			if msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
			patch.Name = &name
		}
		if patch.OthersAccess != nil {
			if _, ok := othersLevels[*patch.OthersAccess]; !ok {
				http.Error(w, `othersAccess is "none", "view" or "edit"`, http.StatusBadRequest)
				return
			}
		}
		if patch.Archived != nil {
			if *patch.Archived {
				http.Error(w, "archive a project by deleting it", http.StatusBadRequest)
				return
			}
			if refuseNonSuper(w, r) {
				return
			}
		}
		ctx := r.Context()
		err := inTx(ctx, db, func(tx *sql.Tx) error {
			if patch.Name != nil {
				if _, err := tx.ExecContext(ctx, "UPDATE projects SET name = ? WHERE id = ?", *patch.Name, v.ID); err != nil {
					return err
				}
			}
			if patch.OthersAccess != nil {
				if _, err := tx.ExecContext(ctx, "UPDATE projects SET others_access = ? WHERE id = ?", *patch.OthersAccess, v.ID); err != nil {
					return err
				}
				if levelRank[othersLevels[*patch.OthersAccess]] < levelRank[othersLevels[v.OthersAccess]] {
					if _, err := tx.ExecContext(ctx, `UPDATE project_members SET removed_at = CURRENT_TIMESTAMP(3)
						WHERE project_id = ? AND source = ? AND removed_at IS NULL`, v.ID, sourceOpen); err != nil {
						return err
					}
				}
			}
			if patch.Archived != nil {
				_, err := tx.ExecContext(ctx, "UPDATE projects SET archived_at = NULL WHERE id = ?", v.ID)
				return err
			}
			return nil
		})
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			http.Error(w, "another project has that name", http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		who := requestUserID(r)
		if patch.Name != nil && *patch.Name != v.Name {
			auditProject(v.ID, "project.renamed", who, "renamed the project %s to %s", v.Name, *patch.Name)
		}
		if patch.OthersAccess != nil && *patch.OthersAccess != v.OthersAccess {
			auditProject(v.ID, "project.access", who, "let anyone signed in %s, where they could %s", *patch.OthersAccess, v.OthersAccess)
		}
		if patch.Archived != nil && v.Archived {
			instanceEvent(ctx, db, who, "project.restored", "brought the project %s (%d) back from the archive", v.Name, v.ID)
		}
		v, _, err = projectOf(ctx, db, requestUser(r), v.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})

	// Archives the project, which only a super admin then enters; once
	// archived, ?forever=true deletes it for good, with everything in it. A
	// super admin's. Legacy, which the instance's people join, is archived
	// like any but never deleted.
	mux.HandleFunc("DELETE /api/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonSuper(w, r) {
			return
		}
		v, ok := owned(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		who := requestUserID(r)
		if r.URL.Query().Get("forever") != "true" {
			if !v.Archived {
				if _, err := db.ExecContext(ctx, "UPDATE projects SET archived_at = CURRENT_TIMESTAMP(3) WHERE id = ? AND archived_at IS NULL", v.ID); err != nil {
					serverError(w, err)
					return
				}
				instanceEvent(ctx, db, who, "project.archived", "archived the project %s (%d)", v.Name, v.ID)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if v.ID == legacyProject {
			http.Error(w, "Legacy holds what there was before projects, and everyone joins it, so it can be archived but not deleted", http.StatusConflict)
			return
		}
		runs, err := deleteProject(ctx, db, v.ID)
		switch {
		case errors.Is(err, errProjectNotArchived), errors.Is(err, errProjectRunning):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "no such project", http.StatusNotFound)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		dropWorkspaces(workerURL, runs)
		instanceEvent(ctx, db, who, "project.deleted", "deleted the project %s (%d) for good, with its %d runs", v.Name, v.ID, len(runs))
		w.WriteHeader(http.StatusNoContent)
	})

	// Everyone who is or was a member of the project, those removed last:
	// whom it was given by hand and at which level, and who came in another
	// way. Its owners'.
	mux.HandleFunc("GET /api/projects/{id}/members", func(w http.ResponseWriter, r *http.Request) {
		v, ok := owned(w, r)
		if !ok {
			return
		}
		ms, err := projectMembers(r.Context(), db, v.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ms)
	})

	// Says who is given the project by hand, and at which level, in place
	// of those who were: [{"user", "level"}]. Those left out are removed;
	// someone who came in another way and is named is a member by hand from
	// then. Its owners'; an owner doesn't take their own ownership away.
	mux.HandleFunc("PUT /api/projects/{id}/members", func(w http.ResponseWriter, r *http.Request) {
		v, ok := owned(w, r)
		if !ok {
			return
		}
		var body []struct {
			User  int64  `json:"user"`
			Level string `json:"level"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		levels := map[int64]string{}
		for _, m := range body {
			if levelRank[m.Level] == 0 {
				http.Error(w, `a member's level is "owner", "editor" or "viewer"`, http.StatusBadRequest)
				return
			}
			levels[m.User] = m.Level
		}
		ctx := r.Context()
		me := requestUser(r)
		if me != nil && !me.Super && v.how == sourceManual && levels[me.ID] != levelOwner {
			http.Error(w, "you can't stop being the project's owner yourself; ask another owner", http.StatusBadRequest)
			return
		}
		ids := make([]int64, 0, len(levels))
		for id := range levels {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		err := inTx(ctx, db, func(tx *sql.Tx) error {
			if len(ids) > 0 {
				var n int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE removed_at IS NULL AND id IN "+placeholders(len(ids)), int64Args(ids)...).Scan(&n); err != nil {
					return err
				}
				if n != len(ids) {
					return errUnknownMember
				}
			}
			q, args := `UPDATE project_members SET removed_at = CURRENT_TIMESTAMP(3) WHERE project_id = ? AND source = ? AND removed_at IS NULL`, []any{v.ID, sourceManual}
			if len(ids) > 0 {
				q += " AND user_id NOT IN " + placeholders(len(ids))
				args = append(args, int64Args(ids)...)
			}
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := tx.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, level, source) VALUES (?, ?, ?, ?) AS given
					ON DUPLICATE KEY UPDATE level = given.level, source = given.source, removed_at = NULL`, v.ID, id, levels[id], sourceManual); err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, errUnknownMember) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		ms, err := projectMembers(ctx, db, v.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		var given []string
		for _, m := range ms {
			if m.Source == sourceManual && m.RemovedAt == nil {
				given = append(given, fmt.Sprintf("%s as %s", m.Email, m.Level))
			}
		}
		auditProject(v.ID, "project.members", requestUserID(r), "gave the project %s by hand to %s", v.Name, orNobody(given))
		writeJSON(w, http.StatusOK, ms)
	})

	// The identity provider's groups the project is given to, and at which
	// level. Its owners'.
	mux.HandleFunc("GET /api/projects/{id}/groups", func(w http.ResponseWriter, r *http.Request) {
		v, ok := owned(w, r)
		if !ok {
			return
		}
		gs, err := projectGroups(r.Context(), db, v.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, gs)
	})

	// Says which groups the project is given to, and at which level, in
	// place of those it was: [{"group", "level"}]. Who came in only through
	// a group it no longer is given to is removed. Its owners'.
	mux.HandleFunc("PUT /api/projects/{id}/groups", func(w http.ResponseWriter, r *http.Request) {
		v, ok := owned(w, r)
		if !ok {
			return
		}
		var body []projectGroup
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		levels := map[string]string{}
		for _, g := range body {
			name := strings.TrimSpace(g.Group)
			switch {
			case name == "" || len(name) > maxGroupName:
				http.Error(w, fmt.Sprintf("a group's name is from 1 to %d characters", maxGroupName), http.StatusBadRequest)
				return
			case levelRank[g.Level] == 0:
				http.Error(w, `a group's level is "owner", "editor" or "viewer"`, http.StatusBadRequest)
				return
			}
			levels[name] = g.Level
		}
		ctx := r.Context()
		err := inTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "DELETE FROM project_groups WHERE project_id = ?", v.ID); err != nil {
				return err
			}
			for name, level := range levels {
				if _, err := tx.ExecContext(ctx, "INSERT INTO project_groups (project_id, group_name, level) VALUES (?, ?, ?)", v.ID, name, level); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `UPDATE project_members m SET removed_at = CURRENT_TIMESTAMP(3)
				WHERE m.project_id = ? AND m.source = ? AND m.removed_at IS NULL AND NOT EXISTS (
					SELECT 1 FROM project_groups g JOIN user_groups ug ON ug.group_name = g.group_name
					WHERE g.project_id = m.project_id AND ug.user_id = m.user_id)`, v.ID, sourceGroup)
			return err
		})
		if err != nil {
			serverError(w, err)
			return
		}
		gs, err := projectGroups(ctx, db, v.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		var given []string
		for _, g := range gs {
			given = append(given, fmt.Sprintf("%s as %s", g.Group, g.Level))
		}
		auditProject(v.ID, "project.groups", requestUserID(r), "gave the project %s to the groups %s", v.Name, orNobody(given))
		writeJSON(w, http.StatusOK, gs)
	})
}

// The names joined, or "nobody" for none.
func orNobody(names []string) string {
	if len(names) == 0 {
		return "nobody"
	}
	return strings.Join(names, ", ")
}

// The project a flow, procedure or struct is in; sql.ErrNoRows for none
// such.
func flowProject(ctx context.Context, db querier, flow int64) (int64, error) {
	var project int64
	err := db.QueryRowContext(ctx, "SELECT project_id FROM flows WHERE id = ?", flow).Scan(&project)
	return project, err
}

// The project a run is in, its flow's; sql.ErrNoRows for no such run.
func runProject(ctx context.Context, db querier, run int64) (int64, error) {
	var project int64
	err := db.QueryRowContext(ctx, "SELECT project_id FROM runs WHERE id = ?", run).Scan(&project)
	return project, err
}

// Whether the project gets what the instance is configured with outside the
// database: the worker's own models and keys, LLM_UPSTREAMS, GIT_TOKEN and
// GITHUB_APP_*. Only Legacy does.
func inheritsInstance(ctx context.Context, db querier, project int64) (bool, error) {
	var inherits bool
	err := db.QueryRowContext(ctx, "SELECT inherits_instance FROM projects WHERE id = ?", project).Scan(&inherits)
	return inherits, err
}

// Someone as they are in a project, for what is done on their behalf
// outside a request of theirs: an API key's or a webhook's runs, a task
// board playing, who is told of a step. Admin means owner of the project,
// as enterProject makes it for a request; false if they may not enter it,
// or there is no such project.
func userInProject(ctx context.Context, db rowsQuerier, u *user, project int64) (*user, bool, error) {
	v, found, err := projectOf(ctx, db, u, project)
	if err != nil || !found || v.Level == "" || u == nil {
		return u, err == nil && found && v.Level != "", err
	}
	as := *u
	as.Admin = v.Level == levelOwner
	as.Project = project
	return &as, true, nil
}
