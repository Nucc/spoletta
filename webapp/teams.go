package main

// A team is a group of people, such as Payments or Platform, that can own
// flows together; see owners.go. Admins, and people an admin made team
// managers, make teams and say who is in them. Anyone signed in sees every
// team, so they can give a flow to one of theirs and see who else has it.
//
// Teams belong to people, not boards, so they stay out of versions and the
// setup file. Without sign-in there are no people, so no teams either.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// The longest team name.
const maxTeamName = 255

type team struct {
	ID      int64        `json:"id"`
	Name    string       `json:"name"`
	Members []teamMember `json:"members"`
}

type teamMember struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// A team as a person's list of them names it.
type teamRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// The teams the user is in, by name.
func userTeams(ctx context.Context, db rowsQuerier, id int64) ([]teamRef, error) {
	teams := []teamRef{}
	err := scanAll(ctx, db, "SELECT t.id, t.name FROM team_members m JOIN teams t ON t.id = m.team_id WHERE m.user_id = ? ORDER BY t.name", func(rows *sql.Rows) error {
		var t teamRef
		err := rows.Scan(&t.ID, &t.Name)
		teams = append(teams, t)
		return err
	}, id)
	return teams, err
}

// Whether the user is in the team.
func inTeam(ctx context.Context, db querier, user, team int64) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM team_members WHERE team_id = ? AND user_id = ?", team, user).Scan(&n)
	return n > 0, err
}

func getTeam(ctx context.Context, db dbQuerier, id int64) (team, error) {
	t := team{ID: id, Members: []teamMember{}}
	if err := db.QueryRowContext(ctx, "SELECT name FROM teams WHERE id = ?", id).Scan(&t.Name); err != nil {
		return t, err
	}
	err := scanAll(ctx, db, `SELECT u.id, u.email, u.name FROM team_members m JOIN users u ON u.id = m.user_id
		WHERE m.team_id = ? ORDER BY COALESCE(NULLIF(u.name, ''), u.email)`, func(rows *sql.Rows) error {
		var m teamMember
		err := rows.Scan(&m.ID, &m.Email, &m.Name)
		t.Members = append(t.Members, m)
		return err
	}, id)
	return t, err
}

// Reads rows one at a time or many.
type dbQuerier interface {
	querier
	rowsQuerier
}

// Turns away a request from someone who may not make or change teams.
func refuseNonTeamManager(w http.ResponseWriter, r *http.Request) bool {
	if u := requestUser(r); u == nil || !(u.Admin || u.TeamManager) {
		http.Error(w, "only an admin or a team manager can do that", http.StatusForbidden)
		return true
	}
	return false
}

// A team's name and members as given, or why they can't be.
type teamBody struct {
	Name    *string  `json:"name"`
	Members *[]int64 `json:"members"`
}

func (b *teamBody) check() string {
	if b.Name != nil {
		*b.Name = strings.TrimSpace(*b.Name)
		if *b.Name == "" {
			return "name is required"
		}
		if len(*b.Name) > maxTeamName {
			return "a team's name is at most 255 characters"
		}
	}
	if b.Members != nil {
		slices.Sort(*b.Members)
		*b.Members = slices.Compact(*b.Members)
	}
	return ""
}

func setMembers(ctx context.Context, tx *sql.Tx, team int64, members []int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM team_members WHERE team_id = ?", team); err != nil {
		return err
	}
	for _, u := range members {
		_, err := tx.ExecContext(ctx, "INSERT INTO team_members (team_id, user_id) VALUES (?, ?)", team, u)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			return errUnknownMember
		}
		if err != nil {
			return err
		}
	}
	return nil
}

var errUnknownMember = errors.New("a member isn't anyone added")

// Says what went wrong saving a team, if it was the request's fault.
func teamError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnknownMember):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case mysqlErrorIs(err, mysqlErrDuplicate):
		http.Error(w, "another team has that name", http.StatusConflict)
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "team not found", http.StatusNotFound)
	default:
		serverError(w, err)
	}
}

func registerTeamRoutes(mux *http.ServeMux, db *sql.DB, a *auth) {
	if a == nil {
		return
	}

	mux.HandleFunc("GET /api/teams", func(w http.ResponseWriter, r *http.Request) {
		var ids []int64
		if err := scanAll(r.Context(), db, "SELECT id FROM teams ORDER BY name", func(rows *sql.Rows) error {
			var id int64
			err := rows.Scan(&id)
			ids = append(ids, id)
			return err
		}); err != nil {
			serverError(w, err)
			return
		}
		teams := []team{}
		for _, id := range ids {
			t, err := getTeam(r.Context(), db, id)
			if err != nil {
				serverError(w, err)
				return
			}
			teams = append(teams, t)
		}
		writeJSON(w, http.StatusOK, teams)
	})

	mux.HandleFunc("POST /api/teams", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonTeamManager(w, r) {
			return
		}
		var body teamBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Name == nil {
			body.Name = new(string)
		}
		if msg := body.check(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		var id int64
		err := inTx(r.Context(), db, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(r.Context(), "INSERT INTO teams (name) VALUES (?)", *body.Name)
			if err != nil {
				return err
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
			if body.Members == nil {
				return nil
			}
			return setMembers(r.Context(), tx, id, *body.Members)
		})
		if err != nil {
			teamError(w, err)
			return
		}
		t, err := getTeam(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, t)
	})

	// Renames the team, or replaces its members.
	mux.HandleFunc("PATCH /api/teams/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonTeamManager(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body teamBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if msg := body.check(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			var n int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM teams WHERE id = ? FOR UPDATE", id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return sql.ErrNoRows
			}
			if body.Name != nil {
				if _, err := tx.ExecContext(r.Context(), "UPDATE teams SET name = ? WHERE id = ?", *body.Name, id); err != nil {
					return err
				}
			}
			if body.Members != nil {
				return setMembers(r.Context(), tx, id, *body.Members)
			}
			return nil
		})
		if err != nil {
			teamError(w, err)
			return
		}
		t, err := getTeam(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, t)
	})

	// A team that owns flows, skills or commands stays until they are given
	// to someone else, so removing it doesn't take them from its members.
	mux.HandleFunc("DELETE /api/teams/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonTeamManager(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		// Those in the trash too, which are the team's still (trash.go).
		var owned, trashed int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*), COALESCE(SUM(deleted_at IS NOT NULL), 0) FROM flows WHERE owner_team_id = ?", id).Scan(&owned, &trashed); err != nil {
			serverError(w, err)
			return
		}
		if owned > 0 {
			msg := fmt.Sprintf("the team owns %d flows, procedures or structs; give them to someone else first", owned)
			if trashed > 0 {
				msg = fmt.Sprintf("the team owns %d flows, procedures or structs, %d of them in the trash; give them to someone else, or delete those for good, first", owned, trashed)
			}
			http.Error(w, msg, http.StatusConflict)
			return
		}
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM skills WHERE owner_team_id = ?", id).Scan(&owned); err != nil {
			serverError(w, err)
			return
		}
		if owned > 0 {
			http.Error(w, fmt.Sprintf("the team owns %d skills or commands; give them to someone else first", owned), http.StatusConflict)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM teams WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
