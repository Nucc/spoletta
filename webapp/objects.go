package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// An object is an OPM object: a named thing that processes generate and take as input.
type object struct {
	ID   int64   `json:"id"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

// A link ties an object to a process: the process generates it (output) or takes it as input.
type link struct {
	ID        int64  `json:"id"`
	Process   int64  `json:"process"`
	Object    int64  `json:"object"`
	Direction string `json:"direction"`
}

const (
	directionOutput = "output"
	directionInput  = "input"
)

func migrateObjects(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS objects (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			x DOUBLE NOT NULL DEFAULT 0,
			y DOUBLE NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS object_links (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			process_id BIGINT NOT NULL,
			object_id BIGINT NOT NULL,
			direction VARCHAR(8) NOT NULL,
			UNIQUE KEY process_object_direction (process_id, object_id, direction),
			FOREIGN KEY (process_id) REFERENCES processes(id) ON DELETE CASCADE,
			FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func registerObjectRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/objects", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, name, x, y FROM objects ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		objs := []object{}
		for rows.Next() {
			var o object
			if err := rows.Scan(&o.ID, &o.Name, &o.X, &o.Y); err != nil {
				serverError(w, err)
				return
			}
			objs = append(objs, o)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, objs)
	})

	mux.HandleFunc("POST /api/objects", func(w http.ResponseWriter, r *http.Request) {
		var o object
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		o.Name = strings.TrimSpace(o.Name)
		if o.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		res, err := db.ExecContext(r.Context(), "INSERT INTO objects (name, x, y) VALUES (?, ?, ?)", o.Name, o.X, o.Y)
		if err != nil {
			serverError(w, err)
			return
		}
		if o.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, o)
	})

	mux.HandleFunc("PATCH /api/objects/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var patch struct {
			Name *string  `json:"name"`
			X    *float64 `json:"x"`
			Y    *float64 `json:"y"`
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

		_, err = db.ExecContext(r.Context(),
			"UPDATE objects SET name = COALESCE(?, name), x = COALESCE(?, x), y = COALESCE(?, y) WHERE id = ?",
			patch.Name, patch.X, patch.Y, id)
		if err != nil {
			serverError(w, err)
			return
		}

		var o object
		err = db.QueryRowContext(r.Context(), "SELECT id, name, x, y FROM objects WHERE id = ?", id).Scan(&o.ID, &o.Name, &o.X, &o.Y)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "object not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, o)
	})

	mux.HandleFunc("GET /api/links", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, process_id, object_id, direction FROM object_links ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		ls := []link{}
		for rows.Next() {
			var l link
			if err := rows.Scan(&l.ID, &l.Process, &l.Object, &l.Direction); err != nil {
				serverError(w, err)
				return
			}
			ls = append(ls, l)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ls)
	})

	mux.HandleFunc("POST /api/links", func(w http.ResponseWriter, r *http.Request) {
		var l link
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if l.Direction != directionOutput && l.Direction != directionInput {
			http.Error(w, "direction must be output or input", http.StatusBadRequest)
			return
		}

		res, err := db.ExecContext(r.Context(), "INSERT INTO object_links (process_id, object_id, direction) VALUES (?, ?, ?)",
			l.Process, l.Object, l.Direction)
		switch {
		case mysqlErrorIs(err, mysqlErrDuplicate):
			http.Error(w, "link already exists", http.StatusConflict)
			return
		case mysqlErrorIs(err, mysqlErrForeignKey):
			http.Error(w, "process or object not found", http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		if l.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, l)
	})

	mux.HandleFunc("DELETE /api/links/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM object_links WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
