package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// A route says which process runs after a human process decides. Like the
// outcomes of a human agent's decision in OPM, a human process has one route
// per outcome: approved, or changes requested.
type route struct {
	ID      int64  `json:"id"`
	Process int64  `json:"process"`
	Outcome string `json:"outcome"`
	Target  int64  `json:"target"`
}

const (
	outcomeApproved = "approved"
	outcomeChanges  = "changes"
)

func migrateRoutes(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS routes (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		process_id BIGINT NOT NULL,
		outcome VARCHAR(16) NOT NULL,
		target_id BIGINT NOT NULL,
		UNIQUE KEY process_outcome (process_id, outcome),
		FOREIGN KEY (process_id) REFERENCES processes(id) ON DELETE CASCADE,
		FOREIGN KEY (target_id) REFERENCES processes(id) ON DELETE CASCADE
	)`)
	return err
}

func registerRouteRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/routes", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, process_id, outcome, target_id FROM routes ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		rs := []route{}
		for rows.Next() {
			var rt route
			if err := rows.Scan(&rt.ID, &rt.Process, &rt.Outcome, &rt.Target); err != nil {
				serverError(w, err)
				return
			}
			rs = append(rs, rt)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rs)
	})

	// Sets the route for an outcome, replacing the previous one.
	mux.HandleFunc("PUT /api/routes", func(w http.ResponseWriter, r *http.Request) {
		var rt route
		if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if rt.Outcome != outcomeApproved && rt.Outcome != outcomeChanges {
			http.Error(w, "outcome must be approved or changes", http.StatusBadRequest)
			return
		}
		if rt.Process == rt.Target {
			http.Error(w, "a process can't route to itself", http.StatusBadRequest)
			return
		}

		var kind string
		err := db.QueryRowContext(r.Context(), "SELECT kind FROM processes WHERE id = ?", rt.Process).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "process not found", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if kind != kindHuman {
			http.Error(w, "only human processes have routes", http.StatusBadRequest)
			return
		}

		// LAST_INSERT_ID(id) makes the replaced row's id the one returned.
		res, err := db.ExecContext(r.Context(),
			`INSERT INTO routes (process_id, outcome, target_id) VALUES (?, ?, ?) AS new
			ON DUPLICATE KEY UPDATE target_id = new.target_id, id = LAST_INSERT_ID(routes.id)`,
			rt.Process, rt.Outcome, rt.Target)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "target process not found", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if rt.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rt)
	})

	mux.HandleFunc("DELETE /api/routes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM routes WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
