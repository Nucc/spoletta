package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-sql-driver/mysql"
)

// The start node is the board's single UML initial node. It lives in a
// one-row table and points at no more than one process.
type startNode struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Target *int64  `json:"target"`
	// Where its link's segments run, if moved by hand; reset when the target changes.
	Route json.RawMessage `json:"route"`
}

// A connection is a directed edge between two processes.
type connection struct {
	ID     int64 `json:"id"`
	Source int64 `json:"source"`
	Target int64 `json:"target"`
}

const (
	mysqlErrDuplicate  = 1062
	mysqlErrForeignKey = 1452
	mysqlErrDeadlock   = 1213
)

func migrateFlow(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS start_node (
			id TINYINT PRIMARY KEY,
			x DOUBLE NOT NULL DEFAULT 0,
			y DOUBLE NOT NULL DEFAULT 0,
			target_id BIGINT NULL,
			FOREIGN KEY (target_id) REFERENCES processes(id) ON DELETE SET NULL
		)`,
		"INSERT IGNORE INTO start_node (id, x, y) VALUES (1, 40, 40)",
		`CREATE TABLE IF NOT EXISTS connections (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			source_id BIGINT NOT NULL,
			target_id BIGINT NOT NULL,
			UNIQUE KEY source_target (source_id, target_id),
			FOREIGN KEY (source_id) REFERENCES processes(id) ON DELETE CASCADE,
			FOREIGN KEY (target_id) REFERENCES processes(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	if err := addColumn(db, "start_node", "route", "JSON NULL"); err != nil {
		return err
	}
	if err := migrateObjects(db); err != nil {
		return err
	}
	if err := migrateRoutes(db); err != nil {
		return err
	}
	if err := migrateDecisionLinks(db); err != nil {
		return err
	}
	if err := migrateRuns(db); err != nil {
		return err
	}
	if err := migrateInstances(db); err != nil {
		return err
	}
	return migrateAgent(db)
}

func registerFlowRoutes(mux *http.ServeMux, db *sql.DB) {
	getStart := func(w http.ResponseWriter, r *http.Request) {
		var s startNode
		var route []byte
		err := db.QueryRowContext(r.Context(), "SELECT x, y, target_id, route FROM start_node WHERE id = 1").Scan(&s.X, &s.Y, &s.Target, &route)
		if err != nil {
			serverError(w, err)
			return
		}
		s.Route = routeJSON(route)
		writeJSON(w, http.StatusOK, s)
	}

	mux.HandleFunc("GET /api/start", getStart)

	mux.HandleFunc("PATCH /api/start", func(w http.ResponseWriter, r *http.Request) {
		var patch struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
			// Raw so that an explicit null (disconnect, default route) is distinguishable from an absent field.
			Target json.RawMessage `json:"target"`
			Route  json.RawMessage `json:"route"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		_, err := db.ExecContext(r.Context(),
			"UPDATE start_node SET x = COALESCE(?, x), y = COALESCE(?, y) WHERE id = 1", patch.X, patch.Y)
		if err != nil {
			serverError(w, err)
			return
		}

		if len(patch.Target) > 0 {
			var target *int64
			if err := json.Unmarshal(patch.Target, &target); err != nil {
				http.Error(w, "invalid target: "+err.Error(), http.StatusBadRequest)
				return
			}
			// A new target needs a new route.
			_, err := db.ExecContext(r.Context(), "UPDATE start_node SET target_id = ?, route = NULL WHERE id = 1", target)
			if mysqlErrorIs(err, mysqlErrForeignKey) {
				http.Error(w, "target process not found", http.StatusBadRequest)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
		}
		if len(patch.Route) > 0 {
			var lanes route
			if err := json.Unmarshal(patch.Route, &lanes); err != nil || len(lanes) > maxRouteLanes {
				http.Error(w, "invalid route", http.StatusBadRequest)
				return
			}
			var stored []byte
			if lanes != nil {
				stored = patch.Route
			}
			if _, err := db.ExecContext(r.Context(), "UPDATE start_node SET route = ? WHERE id = 1", stored); err != nil {
				serverError(w, err)
				return
			}
		}
		getStart(w, r)
	})

	mux.HandleFunc("GET /api/connections", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, source_id, target_id FROM connections ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		cs := []connection{}
		for rows.Next() {
			var c connection
			if err := rows.Scan(&c.ID, &c.Source, &c.Target); err != nil {
				serverError(w, err)
				return
			}
			cs = append(cs, c)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cs)
	})

	mux.HandleFunc("POST /api/connections", func(w http.ResponseWriter, r *http.Request) {
		var c connection
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if c.Source == c.Target {
			http.Error(w, "a process can't connect to itself", http.StatusBadRequest)
			return
		}

		res, err := db.ExecContext(r.Context(), "INSERT INTO connections (source_id, target_id) VALUES (?, ?)", c.Source, c.Target)
		switch {
		case mysqlErrorIs(err, mysqlErrDuplicate):
			http.Error(w, "connection already exists", http.StatusConflict)
			return
		case mysqlErrorIs(err, mysqlErrForeignKey):
			http.Error(w, "process not found", http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	})

	mux.HandleFunc("DELETE /api/connections/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM connections WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func mysqlErrorIs(err error, number uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == number
}
