package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// A struct is a flow holding a single object and nothing else: no processes,
// no people, no runs. It defines that object once, its states, attributes
// and the objects making it up, for flows and procedures to use. Using it
// puts a copy of its object on their board, which follows it as a copy of a
// procedure's object does (see modules.go): what it has changes only in the
// struct, and wherever it is copied with it. The struct and its object share
// a name, and renaming either renames both.

func migrateStructs(db *sql.DB) error {
	return addColumn(db, "flows", "is_struct", "BOOLEAN NOT NULL DEFAULT FALSE")
}

const notInStruct = "a struct holds a single object and nothing else; use it in a flow or a procedure instead"

// Whether a flow is a struct.
func isStruct(ctx context.Context, db querier, flow int64) (bool, error) {
	var s bool
	err := db.QueryRowContext(ctx, "SELECT is_struct FROM flows WHERE id = ?", flow).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return s, err
}

// The object a struct is, 0 if it has none.
func structRoot(ctx context.Context, db querier, flow int64) (int64, error) {
	return idBy(ctx, db, "SELECT id FROM objects WHERE flow_id = ? AND parent_id IS NULL AND owner_id IS NULL ORDER BY id LIMIT 1", flow)
}

// Refuses what a struct can't hold, writing why, when flow is one.
func refuseInStruct(w http.ResponseWriter, r *http.Request, db *sql.DB, flow int64) bool {
	s, err := isStruct(r.Context(), db, flow)
	if err != nil {
		serverError(w, err)
		return true
	}
	if s {
		http.Error(w, notInStruct, http.StatusBadRequest)
	}
	return s
}

// Whether an object is the one a struct is, which goes only with the struct.
func isStructRoot(ctx context.Context, db querier, object int64) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM objects o JOIN flows f ON f.id = o.flow_id
		WHERE o.id = ? AND f.is_struct AND o.parent_id IS NULL AND o.owner_id IS NULL`, object).Scan(&n)
	return n > 0, err
}

// Makes a struct's flow and its object share a name, renaming the other of
// the two that was renamed, and the copies of the object with it.
func renameStruct(ctx context.Context, db *sql.DB, flow int64, name string) error {
	return inTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE flows SET name = ? WHERE id = ?", name, flow); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE objects SET name = ? WHERE flow_id = ? AND parent_id IS NULL AND owner_id IS NULL", name, flow); err != nil {
			return err
		}
		return syncCopies(ctx, tx, flow, false)
	})
}

// How many other flows hold a copy of a struct's object.
func structUsers(ctx context.Context, db querier, flow int64) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT o.flow_id) FROM objects o JOIN objects src ON src.id = o.source_id
		WHERE src.flow_id = ? AND o.flow_id <> ?`, flow, flow).Scan(&n)
	return n, err
}

func registerStructRoutes(mux *http.ServeMux, db *sql.DB) {
	// Puts a copy of the struct's object on a board of a flow: its top board,
	// or with parent, a flow process's.
	mux.HandleFunc("POST /api/structs/{id}/copies", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Flow   int64   `json:"flow"`
			Parent *int64  `json:"parent"`
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		s, err := isStruct(ctx, db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if !s {
			http.Error(w, "struct not found", http.StatusNotFound)
			return
		}
		msg, err := checkParent(ctx, db, body.Parent)
		if err == nil && msg == "" {
			body.Flow, msg, err = flowFor(ctx, db, body.Flow, body.Parent)
		}
		if err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if refuseInStruct(w, r, db, body.Flow) {
			return
		}
		var copied int64
		err = inTx(ctx, db, func(tx *sql.Tx) error {
			source, err := structRoot(ctx, tx, id)
			if err != nil || source == 0 {
				return err
			}
			made, err := loadStructure(ctx, tx, id)
			if err != nil {
				return err
			}
			copied, err = copyStructure(ctx, tx, made, source, body.Flow, body.Parent, nil, &point{body.X, body.Y})
			return err
		})
		if err != nil {
			serverError(w, err)
			return
		}
		if copied == 0 {
			http.Error(w, "the struct has no object", http.StatusConflict)
			return
		}
		o, err := getObject(ctx, db, copied)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, o)
	})
}
