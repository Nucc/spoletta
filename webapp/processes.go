package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type process struct {
	ID   int64   `json:"id"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS processes (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		x DOUBLE NOT NULL DEFAULT 0,
		y DOUBLE NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	return err
}

func registerProcessRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/processes", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, name, x, y FROM processes ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		ps := []process{}
		for rows.Next() {
			var p process
			if err := rows.Scan(&p.ID, &p.Name, &p.X, &p.Y); err != nil {
				serverError(w, err)
				return
			}
			ps = append(ps, p)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ps)
	})

	mux.HandleFunc("POST /api/processes", func(w http.ResponseWriter, r *http.Request) {
		var p process
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		res, err := db.ExecContext(r.Context(), "INSERT INTO processes (name, x, y) VALUES (?, ?, ?)", p.Name, p.X, p.Y)
		if err != nil {
			serverError(w, err)
			return
		}
		if p.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})

	mux.HandleFunc("PATCH /api/processes/{id}", func(w http.ResponseWriter, r *http.Request) {
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

		// COALESCE keeps the stored value for fields absent from the patch.
		_, err = db.ExecContext(r.Context(),
			"UPDATE processes SET name = COALESCE(?, name), x = COALESCE(?, x), y = COALESCE(?, y) WHERE id = ?",
			patch.Name, patch.X, patch.Y, id)
		if err != nil {
			serverError(w, err)
			return
		}

		var p process
		err = db.QueryRowContext(r.Context(), "SELECT id, name, x, y FROM processes WHERE id = ?", id).
			Scan(&p.ID, &p.Name, &p.X, &p.Y)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "process not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
