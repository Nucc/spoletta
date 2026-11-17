package main

// A user's roles say which human processes they carry out. A role is a name,
// such as Finance, and stands for the human objects of that name on any
// board of the project it is held in: where "Finance handles Approve
// payment", only someone holding the role Finance there decides Approve
// payment's reviews. A human process no human object handles is anyone's to
// decide. Names match ignoring case and the spaces around them. A project's
// owners decide any review in it, so one nobody holds the role for can still
// go on, and their decision records them as any other does.
//
// Roles are the project's (see projects.go): someone holds them in each
// project apart, a role of the same name in two projects being two roles,
// and the project's owners give them to its members, those who may enter
// it. The instance's admins add people, before they first sign in if need
// be, make others admins and team managers, and remove them. Roles belong
// to people, not boards, so they stay out of versions and the setup file.
//
// These aren't a link's role, the way a process takes an object (instrument,
// consumption); see objects.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The longest role name, as an object's name.
const maxRoleName = 255

// The key a role name matches by.
func roleKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// The roles the user holds in the project, by name.
func userRoles(ctx context.Context, db *sql.DB, id, project int64) ([]string, error) {
	roles := []string{}
	err := scanAll(ctx, db, "SELECT name FROM user_roles WHERE user_id = ? AND project_id = ? ORDER BY name", func(rows *sql.Rows) error {
		var n string
		err := rows.Scan(&n)
		roles = append(roles, n)
		return err
	}, id, project)
	return roles, err
}

// The roles everyone holds in the project, by user.
func projectRoles(ctx context.Context, db rowsQuerier, project int64) (map[int64][]string, error) {
	roles := map[int64][]string{}
	err := scanAll(ctx, db, "SELECT user_id, name FROM user_roles WHERE project_id = ? ORDER BY user_id, name", func(rows *sql.Rows) error {
		var id int64
		var n string
		err := rows.Scan(&id, &n)
		roles[id] = append(roles[id], n)
		return err
	}, project)
	return roles, err
}

// Keeps the roles a version's human objects name, and those its human
// processes hand reviews on to (see notify.go), for visibleFlows: by
// roleKey, once each, in the version's project.
func saveVersionRoles(ctx context.Context, tx *sql.Tx, version int64, s snapshot) error {
	var names []string
	for _, o := range s.Objects {
		if o.Human {
			names = append(names, o.Name)
		}
	}
	for _, p := range s.Processes {
		if p.Kind == kindHuman {
			names = append(names, p.Settings.EscalateTo...)
		}
	}
	seen := map[string]bool{}
	for _, name := range names {
		if key := roleKey(name); key != "" && !seen[key] {
			seen[key] = true
			if _, err := tx.ExecContext(ctx, "INSERT INTO version_roles (version_id, role, project_id) SELECT id, ?, project_id FROM flow_versions WHERE id = ?", key, version); err != nil {
				return err
			}
		}
	}
	return nil
}

// Whether the user may decide a review of a process handled by these roles,
// the roles they hold in its project; an owner of the project (Admin) may
// decide any.
func mayDecide(u *user, roles, handledBy []string) bool {
	if u == nil || u.Admin || len(handledBy) == 0 {
		return true
	}
	for _, h := range handledBy {
		if slices.ContainsFunc(roles, func(r string) bool { return roleKey(r) == roleKey(h) }) {
			return true
		}
	}
	return false
}

// Whether the user, holding these roles and with this access to its flow
// (see flowAccess), may decide a step of the kind waiting for a person: a
// loop step if they may edit the flow (see loops.go), a review if they see
// the flow and may decide those handledBy names. The run's panel, the inbox
// and who is told of it all ask this; see inbox.go and notify.go.
func decidesStep(u *user, roles []string, access, kind string, handledBy []string) bool {
	switch {
	case access == "":
		return false
	case kind == kindLoop:
		return access == accessEdit
	}
	return mayDecide(u, roles, handledBy)
}

// The access someone at the level given in a flow's project has to it, of
// the access ownership gives them (flowAccess): a viewer changes nothing
// there, so sees at most, and answers no loop question.
func levelAccess(level, access string) string {
	if level == levelViewer && access == accessEdit {
		return accessReview
	}
	return access
}

// Says why the request's user may not decide a review of a process the
// roles in handledBy decide, if they may not, by the roles they hold in the
// request's project, which guardFlows made sure the review's run is in.
func refuseDecider(ctx context.Context, db *sql.DB, r *http.Request, handledBy []string) (string, error) {
	u := requestUser(r)
	if u == nil || u.Admin || len(handledBy) == 0 {
		return "", nil
	}
	roles, err := userRoles(ctx, db, u.ID, requestProject(r))
	if err != nil || mayDecide(u, roles, handledBy) {
		return "", err
	}
	return "only " + strings.Join(handledBy, " or ") + " decides this review; ask an owner of the project for the role", nil
}

// Someone as admins see them, with the roles they hold and the teams they
// are in in the project they are seen from.
type person struct {
	user
	Roles      []string   `json:"roles"`
	Teams      []teamRef  `json:"teams"`
	SignedInAt *time.Time `json:"signedInAt"`
}

// The person, their Admin the instance's, with their roles and teams in the
// project.
func getPerson(ctx context.Context, db *sql.DB, id, project int64) (person, error) {
	var p person
	err := db.QueryRowContext(ctx, "SELECT id, email, name, admin, team_manager, signed_in_at FROM users WHERE id = ?", id).
		Scan(&p.ID, &p.Email, &p.Name, &p.Admin, &p.TeamManager, &p.SignedInAt)
	if err != nil {
		return p, err
	}
	if p.Roles, err = userRoles(ctx, db, id, project); err != nil {
		return p, err
	}
	p.Teams, err = userTeams(ctx, db, id, project)
	return p, err
}

// Someone who may enter a project, as they are there (see userInProject):
// Admin an owner of it, Project it, Super the instance's admin; and their
// level there.
type projectPerson struct {
	user
	level string
}

// Everyone not removed who may enter the project, as they are there, by
// email: their manual membership, groups, what the project lets anyone
// signed in do, and being the instance's admin read as projectsOf reads
// them, and weighed as it does (projectView.work).
func projectPeople(ctx context.Context, db rowsQuerier, project int64) ([]projectPerson, error) {
	people := []projectPerson{}
	err := scanAll(ctx, db, `SELECT u.id, u.email, u.name, u.admin, u.team_manager,
			p.archived_at IS NOT NULL, p.others_access, m.user_id IS NOT NULL, COALESCE(m.level, ''), COALESCE(m.source, ''),
			COALESCE((SELECT MAX(CASE g.level WHEN 'owner' THEN 3 WHEN 'editor' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END)
				FROM project_groups g JOIN user_groups ug ON ug.group_name = g.group_name
				WHERE g.project_id = p.id AND ug.user_id = u.id), 0)
		FROM users u JOIN projects p ON p.id = ?
		LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = u.id AND m.removed_at IS NULL
		WHERE u.removed_at IS NULL ORDER BY u.email`, func(rows *sql.Rows) error {
		var pp projectPerson
		var v projectView
		if err := rows.Scan(&pp.ID, &pp.Email, &pp.Name, &pp.Super, &pp.TeamManager,
			&v.Archived, &v.OthersAccess, &v.member, &v.memberLevel, &v.memberSource, &v.groupRank); err != nil {
			return err
		}
		v.work(&pp.user)
		if v.Level != "" {
			pp.Admin, pp.Project, pp.level = v.Level == levelOwner, project, v.Level
			people = append(people, pp)
		}
		return nil
	}, project)
	return people, err
}

// Why someone can't be given a role or a team in a project.
var errNotInProject = errors.New("someone given it may not enter the project; give them access to it first")

// Makes sure the person, not removed, may enter the project, and has a
// member row there for what names them in it to point at: someone entering
// it otherwise than by hand who has none, or one removed, gets one, as
// entering it would make (enterAsMember). errUnknownMember for no such
// person, errNotInProject for someone who may not enter it.
func memberOfProject(ctx context.Context, tx *sql.Tx, project, id int64) error {
	var super bool
	err := tx.QueryRowContext(ctx, "SELECT admin FROM users WHERE id = ? AND removed_at IS NULL", id).Scan(&super)
	if errors.Is(err, sql.ErrNoRows) {
		return errUnknownMember
	}
	if err != nil {
		return err
	}
	v, found, err := projectOf(ctx, tx, &user{ID: id, Super: super}, project)
	switch {
	case err != nil:
		return err
	case !found || v.Level == "":
		return errNotInProject
	case !v.needsRow():
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO project_members (project_id, user_id, level, source) VALUES (?, ?, ?, ?) AS entering
		ON DUPLICATE KEY UPDATE level = IF(project_members.removed_at IS NULL, project_members.level, entering.level),
			source = IF(project_members.removed_at IS NULL, project_members.source, entering.source), removed_at = NULL`,
		project, id, v.Level, v.how)
	return err
}

// Turns away a request from someone not an owner of the project it is
// about; managing people is a super admin's (refuseNonSuper).
func refuseNonAdmin(w http.ResponseWriter, r *http.Request) bool {
	if u := requestUser(r); u == nil || !u.Admin {
		http.Error(w, "only an admin can do that", http.StatusForbidden)
		return true
	}
	return false
}

// Role names as given: trimmed, without empty ones or ones repeated.
func cleanRoles(names []string) ([]string, string) {
	roles := []string{}
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[roleKey(n)] {
			continue
		}
		if len(n) > maxRoleName {
			return nil, "a role's name is at most 255 characters"
		}
		seen[roleKey(n)] = true
		roles = append(roles, n)
	}
	return roles, ""
}

// Says what went wrong giving someone roles, if it was the request's fault.
func roleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotInProject):
		http.Error(w, "they may not enter this project, so they hold no roles in it; give them access to it first", http.StatusBadRequest)
	case errors.Is(err, errUnknownMember), errors.Is(err, sql.ErrNoRows):
		http.Error(w, "no one with that id", http.StatusNotFound)
	default:
		serverError(w, err)
	}
}

// The routes about people; whether the user signed in may decide each
// review of a run is in runs.go.
func registerUserRoleRoutes(mux *http.ServeMux, db *sql.DB, a *auth) {
	if a == nil {
		return
	}

	// The people to pick from, to give access, roles or a team in the
	// request's project, each with their roles and teams there: everyone,
	// for the instance's admins; for the project's owners and team
	// managers, those who may enter it.
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonTeamManager(w, r) {
			return
		}
		project := requestProject(r)
		var ids []int64
		if requestUser(r).Super {
			if err := scanAll(r.Context(), db, "SELECT id FROM users WHERE removed_at IS NULL ORDER BY email", func(rows *sql.Rows) error {
				var id int64
				err := rows.Scan(&id)
				ids = append(ids, id)
				return err
			}); err != nil {
				serverError(w, err)
				return
			}
		} else {
			pps, err := projectPeople(r.Context(), db, project)
			if err != nil {
				serverError(w, err)
				return
			}
			for _, pp := range pps {
				ids = append(ids, pp.ID)
			}
		}
		people := []person{}
		for _, id := range ids {
			p, err := getPerson(r.Context(), db, id, project)
			if err != nil {
				serverError(w, err)
				return
			}
			people = append(people, p)
		}
		writeJSON(w, http.StatusOK, people)
	})

	// Adds someone by their email, so they can sign in, before they first
	// do. They are in no project until given one (projects.go), which a
	// project and a level here give them at once, by hand, with roles in
	// it: {"email", "project", "level", "roles"}; roles only so, as roles
	// are a project's. An instance admin's.
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonSuper(w, r) {
			return
		}
		var body struct {
			Email       string   `json:"email"`
			Name        string   `json:"name"`
			Admin       bool     `json:"admin"`
			TeamManager bool     `json:"teamManager"`
			Project     int64    `json:"project"`
			Level       string   `json:"level"`
			Roles       []string `json:"roles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if local, domain, ok := strings.Cut(email, "@"); !ok || local == "" || domain == "" || len(email) > 320 {
			http.Error(w, "email must be an email address", http.StatusBadRequest)
			return
		}
		roles, msg := cleanRoles(body.Roles)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		switch {
		case body.Project == 0 && (body.Level != "" || len(roles) > 0):
			http.Error(w, "a level and roles are given in a project: name it too", http.StatusBadRequest)
			return
		case body.Project != 0 && levelRank[body.Level] == 0:
			http.Error(w, `the level given in the project is "owner", "editor" or "viewer"`, http.StatusBadRequest)
			return
		}
		if body.Project != 0 {
			if _, found, err := projectOf(r.Context(), db, requestUser(r), body.Project); err != nil {
				serverError(w, err)
				return
			} else if !found {
				http.Error(w, "no such project", http.StatusBadRequest)
				return
			}
		}
		var id int64
		err := inTx(r.Context(), db, func(tx *sql.Tx) error {
			// Someone removed comes back as they were, with their runs and
			// decisions.
			err := tx.QueryRowContext(r.Context(), "SELECT id FROM users WHERE email = ? AND removed_at IS NOT NULL FOR UPDATE", email).Scan(&id)
			switch {
			case err == nil:
				_, err = tx.ExecContext(r.Context(), "UPDATE users SET removed_at = NULL, name = COALESCE(NULLIF(?, ''), name), admin = ?, team_manager = ? WHERE id = ?",
					strings.TrimSpace(body.Name), body.Admin, body.TeamManager, id)
			case errors.Is(err, sql.ErrNoRows):
				var res sql.Result
				res, err = tx.ExecContext(r.Context(), "INSERT INTO users (email, name, admin, team_manager) VALUES (?, ?, ?, ?)",
					email, strings.TrimSpace(body.Name), body.Admin, body.TeamManager)
				if err == nil {
					id, err = res.LastInsertId()
				}
			}
			if err != nil || body.Project == 0 {
				return err
			}
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO project_members (project_id, user_id, level, source) VALUES (?, ?, ?, ?) AS given
				ON DUPLICATE KEY UPDATE level = given.level, source = given.source, removed_at = NULL`, body.Project, id, body.Level, sourceManual); err != nil {
				return err
			}
			return setRoles(r.Context(), tx, id, body.Project, roles)
		})
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			http.Error(w, "someone with that email is already added", http.StatusConflict)
			return
		}
		if err != nil {
			roleError(w, err)
			return
		}
		if body.Admin {
			instanceEvent(r.Context(), db, requestUserID(r), "user.super", "added %s as an admin", email)
		}
		project := requestProject(r)
		if body.Project != 0 {
			project = body.Project
			auditProject(project, "project.members", requestUserID(r), "gave the project by hand to %s as %s, adding them", email, body.Level)
		}
		p, err := getPerson(r.Context(), db, id, project)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})

	// Makes someone an admin or a team manager or not, an instance admin's;
	// and replaces the roles they hold in the request's project, which its
	// owners do for those who may enter it.
	mux.HandleFunc("PATCH /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			Admin       *bool     `json:"admin"`
			TeamManager *bool     `json:"teamManager"`
			Roles       *[]string `json:"roles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		me := requestUser(r)
		if (patch.Admin != nil || patch.TeamManager != nil || patch.Roles == nil) && refuseNonSuper(w, r) {
			return
		}
		if refuseNonAdmin(w, r) {
			return
		}
		project := requestProject(r)
		// Someone has to stay able to make admins.
		if patch.Admin != nil && !*patch.Admin && id == me.ID {
			http.Error(w, "you can't stop being an admin yourself; ask another admin", http.StatusBadRequest)
			return
		}
		var roles []string
		if patch.Roles != nil {
			var msg string
			if roles, msg = cleanRoles(*patch.Roles); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}
		// Whether they were made the instance's admin, or stopped being one.
		var madeSuper *bool
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users WHERE id = ? AND removed_at IS NULL FOR UPDATE", id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return sql.ErrNoRows
			}
			if patch.Admin != nil {
				res, err := tx.ExecContext(r.Context(), "UPDATE users SET admin = ? WHERE id = ?", *patch.Admin, id)
				if err != nil {
					return err
				}
				if n, err := res.RowsAffected(); err != nil {
					return err
				} else if n > 0 {
					// Their level in each project stays as it was given:
					// an admin enters every one as its owner anyway.
					madeSuper = patch.Admin
				}
			}
			if patch.TeamManager != nil {
				if _, err := tx.ExecContext(r.Context(), "UPDATE users SET team_manager = ? WHERE id = ?", *patch.TeamManager, id); err != nil {
					return err
				}
			}
			if patch.Roles != nil {
				// Even taking every role away is the project's owners' to do
				// only for someone in it, so they learn nothing of others.
				if !me.Super {
					if err := memberOfProject(r.Context(), tx, project, id); err != nil {
						return err
					}
				}
				return setRoles(r.Context(), tx, id, project, roles)
			}
			return nil
		})
		if err != nil {
			roleError(w, err)
			return
		}
		p, err := getPerson(r.Context(), db, id, project)
		if err != nil {
			serverError(w, err)
			return
		}
		if madeSuper != nil {
			what := "made %s an admin"
			if !*madeSuper {
				what = "made %s an admin no more"
			}
			instanceEvent(r.Context(), db, requestUserID(r), "user.super", what, p.Email)
		}
		writeJSON(w, http.StatusOK, p)
	})

	// Removes someone: they are signed out and can't sign in again until an
	// admin adds them back. Their flows and schedules can't be left to nobody,
	// so they go to the admin removing them; their roles, teams and MCP
	// connections go, in every project. They stay in the table, removed, so
	// the runs they started and the reviews they decided still say so.
	mux.HandleFunc("DELETE /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonSuper(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		me := requestUser(r)
		if id == me.ID {
			http.Error(w, "you can't remove yourself; ask another admin", http.StatusBadRequest)
			return
		}
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users WHERE id = ? AND removed_at IS NULL FOR UPDATE", id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return sql.ErrNoRows
			}
			// In whichever projects they are, which the admin may never
			// have entered.
			if err := memberWhereFlows(r.Context(), tx, me.ID, "f.owner_user_id = ?", id); err != nil {
				return err
			}
			for _, q := range []string{
				"UPDATE flows SET owner_user_id = ? WHERE owner_user_id = ?",
				"UPDATE schedules SET created_by = ? WHERE created_by = ?",
			} {
				if _, err := tx.ExecContext(r.Context(), q, me.ID, id); err != nil {
					return err
				}
			}
			for _, q := range []string{
				"DELETE FROM sessions WHERE user_id = ?",
				"DELETE FROM user_roles WHERE user_id = ?",
				"DELETE FROM team_members WHERE user_id = ?",
				"DELETE FROM mcp_tokens WHERE user_id = ?",
				"UPDATE users SET removed_at = CURRENT_TIMESTAMP(3), admin = FALSE, team_manager = FALSE WHERE id = ?",
			} {
				if _, err := tx.ExecContext(r.Context(), q, id); err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no one with that id", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// The roles the project's boards name, the human objects on them, with
	// those its people hold, for its owners to pick from.
	mux.HandleFunc("GET /api/roles", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonAdmin(w, r) {
			return
		}
		project := requestProject(r)
		names := []string{}
		seen := map[string]bool{}
		err := scanAll(r.Context(), db,
			`SELECT TRIM(name) FROM objects WHERE project_id = ? AND human AND TRIM(name) <> ''
			UNION SELECT name FROM user_roles WHERE project_id = ? ORDER BY 1`, func(rows *sql.Rows) error {
				var n string
				if err := rows.Scan(&n); err != nil {
					return err
				}
				if !seen[roleKey(n)] {
					seen[roleKey(n)] = true
					names = append(names, n)
				}
				return nil
			}, project, project)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, names)
	})
}

// Replaces the roles the person holds in the project; giving them any
// makes sure they may enter it first (memberOfProject).
func setRoles(ctx context.Context, tx *sql.Tx, id, project int64, roles []string) error {
	if len(roles) > 0 {
		if err := memberOfProject(ctx, tx, project, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_roles WHERE user_id = ? AND project_id = ?", id, project); err != nil {
		return err
	}
	for _, n := range roles {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_roles (user_id, project_id, name) VALUES (?, ?, ?)", id, project, n); err != nil {
			return err
		}
	}
	return nil
}
