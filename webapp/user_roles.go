package main

// A user's roles say which human processes they carry out. A role is a name,
// such as Finance, and stands for the human objects of that name on any
// board: where "Finance handles Approve payment", only someone holding the
// role Finance decides Approve payment's reviews. A human process no human
// object handles is anyone's to decide. Names match ignoring case and the
// spaces around them. Admins decide any review, so one nobody holds the role
// for can still go on, and their decision records them as any other does.
//
// Admins add people, before they first sign in if need be, make others
// admins, and give them roles. Roles belong to people, not boards, so they
// stay out of versions and the setup file.
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

// The roles of the user, by name.
func userRoles(ctx context.Context, db *sql.DB, id int64) ([]string, error) {
	roles := []string{}
	err := scanAll(ctx, db, "SELECT name FROM user_roles WHERE user_id = ? ORDER BY name", func(rows *sql.Rows) error {
		var n string
		err := rows.Scan(&n)
		roles = append(roles, n)
		return err
	}, id)
	return roles, err
}

// Keeps the roles a version's human objects name, and those its human
// processes hand reviews on to (see notify.go), for visibleFlows: by
// roleKey, once each.
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
			if _, err := tx.ExecContext(ctx, "INSERT INTO version_roles (version_id, role) VALUES (?, ?)", version, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// Whether the user may decide a review of a process handled by these roles.
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

// Says why the request's user may not decide a review of a process the
// roles in handledBy decide, if they may not.
func refuseDecider(ctx context.Context, db *sql.DB, r *http.Request, handledBy []string) (string, error) {
	u := requestUser(r)
	if u == nil || u.Admin || len(handledBy) == 0 {
		return "", nil
	}
	roles, err := userRoles(ctx, db, u.ID)
	if err != nil || mayDecide(u, roles, handledBy) {
		return "", err
	}
	return "only " + strings.Join(handledBy, " or ") + " decides this review; ask an admin for the role", nil
}

// Someone as admins see them.
type person struct {
	user
	Roles      []string   `json:"roles"`
	Teams      []teamRef  `json:"teams"`
	SignedInAt *time.Time `json:"signedInAt"`
}

func getPerson(ctx context.Context, db *sql.DB, id int64) (person, error) {
	var p person
	err := db.QueryRowContext(ctx, "SELECT id, email, name, admin, team_manager, signed_in_at FROM users WHERE id = ?", id).
		Scan(&p.ID, &p.Email, &p.Name, &p.Admin, &p.TeamManager, &p.SignedInAt)
	if err != nil {
		return p, err
	}
	if p.Roles, err = userRoles(ctx, db, id); err != nil {
		return p, err
	}
	p.Teams, err = userTeams(ctx, db, id)
	return p, err
}

// Turns away a request from someone not an admin.
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

// Admins' routes; whether the user signed in may decide each review of a run
// is in runs.go.
func registerUserRoleRoutes(mux *http.ServeMux, db *sql.DB, a *auth) {
	if a == nil {
		return
	}

	// Team managers see everyone too, to pick teams' members from.
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonTeamManager(w, r) {
			return
		}
		var ids []int64
		if err := scanAll(r.Context(), db, "SELECT id FROM users WHERE removed_at IS NULL ORDER BY email", func(rows *sql.Rows) error {
			var id int64
			err := rows.Scan(&id)
			ids = append(ids, id)
			return err
		}); err != nil {
			serverError(w, err)
			return
		}
		people := []person{}
		for _, id := range ids {
			p, err := getPerson(r.Context(), db, id)
			if err != nil {
				serverError(w, err)
				return
			}
			people = append(people, p)
		}
		writeJSON(w, http.StatusOK, people)
	})

	// Adds someone by their email, so they can sign in and hold roles before
	// they first do.
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonAdmin(w, r) {
			return
		}
		var body struct {
			Email       string   `json:"email"`
			Name        string   `json:"name"`
			Admin       bool     `json:"admin"`
			TeamManager bool     `json:"teamManager"`
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
			if err != nil {
				return err
			}
			return setRoles(r.Context(), tx, id, roles)
		})
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			http.Error(w, "someone with that email is already added", http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		p, err := getPerson(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})

	// Makes someone an admin or a team manager or not, and replaces their roles.
	mux.HandleFunc("PATCH /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonAdmin(w, r) {
			return
		}
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
		// Someone has to stay able to make admins.
		if patch.Admin != nil && !*patch.Admin && id == requestUser(r).ID {
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
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users WHERE id = ? AND removed_at IS NULL FOR UPDATE", id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return sql.ErrNoRows
			}
			if patch.Admin != nil {
				if _, err := tx.ExecContext(r.Context(), "UPDATE users SET admin = ? WHERE id = ?", *patch.Admin, id); err != nil {
					return err
				}
			}
			if patch.TeamManager != nil {
				if _, err := tx.ExecContext(r.Context(), "UPDATE users SET team_manager = ? WHERE id = ?", *patch.TeamManager, id); err != nil {
					return err
				}
			}
			if patch.Roles != nil {
				return setRoles(r.Context(), tx, id, roles)
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
		p, err := getPerson(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	// Removes someone: they are signed out and can't sign in again until an
	// admin adds them back. Their flows and schedules can't be left to nobody,
	// so they go to the admin removing them; their roles, teams and MCP
	// connections go. They stay in the table, removed, so the runs
	// they started and the reviews they decided still say so.
	mux.HandleFunc("DELETE /api/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonAdmin(w, r) {
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

	// The roles boards name, the human objects on them, with those people
	// hold, for admins to pick from.
	mux.HandleFunc("GET /api/roles", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonAdmin(w, r) {
			return
		}
		names := []string{}
		seen := map[string]bool{}
		err := scanAll(r.Context(), db,
			"SELECT TRIM(name) FROM objects WHERE human AND TRIM(name) <> '' UNION SELECT name FROM user_roles ORDER BY 1", func(rows *sql.Rows) error {
				var n string
				if err := rows.Scan(&n); err != nil {
					return err
				}
				if !seen[roleKey(n)] {
					seen[roleKey(n)] = true
					names = append(names, n)
				}
				return nil
			})
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, names)
	})
}

func setRoles(ctx context.Context, tx *sql.Tx, id int64, roles []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_roles WHERE user_id = ?", id); err != nil {
		return err
	}
	for _, n := range roles {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_roles (user_id, name) VALUES (?, ?)", id, n); err != nil {
			return err
		}
	}
	return nil
}
