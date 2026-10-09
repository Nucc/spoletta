package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-sql-driver/mysql"
)

// The start node is a board's single UML initial node. It points at no more
// than one process; a flow's top board keeps it in flow_starts.
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
	if err := migrateObjects(db); err != nil {
		return err
	}
	if err := migrateBoards(db); err != nil {
		return err
	}
	if err := migrateStructure(db); err != nil {
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
	if err := migrateItems(db); err != nil {
		return err
	}
	if err := migrateVersions(db); err != nil {
		return err
	}
	if err := migrateFlows(db); err != nil {
		return err
	}
	if err := migrateModules(db); err != nil {
		return err
	}
	if err := migrateStructs(db); err != nil {
		return err
	}
	return migrateAgent(db)
}

func registerFlowRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/connections", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		rows, err := db.QueryContext(r.Context(),
			"SELECT c.id, c.source_id, c.target_id FROM connections c JOIN processes p ON p.id = c.source_id WHERE p.flow_id = ? ORDER BY c.id", f)
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
		var flows int
		err := db.QueryRowContext(r.Context(), "SELECT COUNT(DISTINCT flow_id) FROM processes WHERE id IN (?, ?)", c.Source, c.Target).Scan(&flows)
		if err != nil {
			serverError(w, err)
			return
		}
		if flows > 1 {
			http.Error(w, "the processes are in different flows", http.StatusBadRequest)
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
