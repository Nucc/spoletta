package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// A state is one of the situations an OPM object can be in, such as a draft
// being approved or having changes requested. Links can name a state: an
// output link generates its object in that state, and an input link is a
// condition link that runs its process when the object enters the state. A
// human process decides by setting its object to one of the states its
// output link lists.
type objectState struct {
	ID     int64  `json:"id"`
	Object int64  `json:"object"`
	Name   string `json:"name"`
	// When an object generated in no state is in this one; see rules.go.
	Rule string `json:"rule,omitempty"`
	// The state of a module's object it is a copy of; see modules.go.
	Source *int64 `json:"source,omitempty"`
}

func registerStateRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("POST /api/objects/{id}/states", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var s objectState
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.Object = id
		if s.Name = strings.TrimSpace(s.Name); s.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		s.Rule = strings.TrimSpace(s.Rule)
		if _, err := parseRule(s.Rule); err != nil {
			http.Error(w, "rule: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Processes set an object's states, and only objects on a board meet them.
		var owned bool
		if err := db.QueryRowContext(r.Context(), "SELECT owner_id IS NOT NULL FROM objects WHERE id = ?", s.Object).Scan(&owned); err == nil && owned {
			http.Error(w, "an object making up another has no states of its own; give them to the object on the board", http.StatusBadRequest)
			return
		}
		if !ownObject(w, r, db, s.Object) {
			return
		}
		res, err := db.ExecContext(r.Context(), "INSERT INTO object_states (object_id, name, rule) VALUES (?, ?, ?)", s.Object, s.Name, s.Rule)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "object not found", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if s.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		if !propagateFrom(w, r, db, s.Object, false) {
			return
		}
		writeJSON(w, http.StatusCreated, s)
	})

	mux.HandleFunc("PATCH /api/states/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		// Either or both; a rule left empty is none.
		var patch struct {
			Name *string `json:"name"`
			Rule *string `json:"rule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Name != nil {
			if *patch.Name = strings.TrimSpace(*patch.Name); *patch.Name == "" {
				http.Error(w, "name must not be empty", http.StatusBadRequest)
				return
			}
		}
		if patch.Rule != nil {
			*patch.Rule = strings.TrimSpace(*patch.Rule)
			if _, err := parseRule(*patch.Rule); err != nil {
				http.Error(w, "rule: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		object, err := objectOfState(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if !ownObject(w, r, db, object) {
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_states SET name = COALESCE(?, name), rule = COALESCE(?, rule) WHERE id = ?", patch.Name, patch.Rule, id); err != nil {
			serverError(w, err)
			return
		}
		if !propagateFrom(w, r, db, object, false) {
			return
		}

		var s objectState
		err = db.QueryRowContext(r.Context(), "SELECT id, object_id, name, COALESCE(rule, '') FROM object_states WHERE id = ?", id).Scan(&s.ID, &s.Object, &s.Name, &s.Rule)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "state not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	// The links naming the state go with it.
	mux.HandleFunc("DELETE /api/states/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		object, err := objectOfState(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if !ownObject(w, r, db, object) {
			return
		}
		if err := deleteState(r.Context(), db, id); err != nil {
			serverError(w, err)
			return
		}
		if !propagateFrom(w, r, db, object, false) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// object_links.state_id has no foreign key: 0 stands for no state, which
// keeps it in the links' unique key, so its links are deleted here.
func deleteState(ctx context.Context, db *sql.DB, id int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM object_links WHERE state_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM object_states WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// The states of every object, by object id.
func listStates(ctx context.Context, db *sql.DB) (map[int64][]objectState, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, object_id, name, COALESCE(rule, ''), source_id FROM object_states ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[int64][]objectState{}
	for rows.Next() {
		var s objectState
		if err := rows.Scan(&s.ID, &s.Object, &s.Name, &s.Rule, &s.Source); err != nil {
			return nil, err
		}
		states[s.Object] = append(states[s.Object], s)
	}
	return states, rows.Err()
}
