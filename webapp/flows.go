package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A flow is a board of its own with everything on it: its processes and
// objects, the boards of its flow processes, its versions and its runs. Each
// flow does one job, such as fixing exceptions or bumping dependencies.
type flow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// The repositories its agents and scripts work on, and how its runs'
	// branches are named; see workspace.go.
	Settings  flowSettings `json:"settings"`
	CreatedAt time.Time    `json:"createdAt"`
	// Its latest run of the whole flow, if any.
	LastRun *run `json:"lastRun"`
}

// The tables whose rows belong to a flow, by flow_id.
var flowTables = []string{"processes", "objects", "runs", "flow_versions"}

// Before flows there was a single board, whose start lived in the one row of
// start_node and whose current version in the one row of flow_current. It
// becomes the first flow.
func migrateFlows(db *sql.DB) error {
	stmts := []string{
		// version_id is the version the board was last saved as or jumped to.
		`CREATE TABLE IF NOT EXISTS flows (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			version_id BIGINT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			FOREIGN KEY (version_id) REFERENCES flow_versions(id) ON DELETE SET NULL
		)`,
		// The start of a flow's top board, with the columns of inner_boards' start.
		`CREATE TABLE IF NOT EXISTS flow_starts (
			flow_id BIGINT PRIMARY KEY,
			x DOUBLE NOT NULL DEFAULT 0,
			y DOUBLE NOT NULL DEFAULT 0,
			target_id BIGINT NULL,
			route JSON NULL,
			FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE,
			FOREIGN KEY (target_id) REFERENCES processes(id) ON DELETE SET NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	for _, t := range flowTables {
		if err := addColumn(db, t, "flow_id", "BIGINT NULL"); err != nil {
			return err
		}
	}
	if err := addColumn(db, "flows", "settings", "JSON NULL"); err != nil {
		return err
	}
	if err := addColumn(db, "run_steps", "branch", "VARCHAR(255) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn(db, "run_steps", "commits", "JSON NULL"); err != nil {
		return err
	}
	if err := migrateFollowUps(db); err != nil {
		return err
	}

	legacy, err := hasTable(db, "start_node")
	if err != nil {
		return err
	}
	if legacy {
		var flows int
		if err := db.QueryRow("SELECT COUNT(*) FROM flows").Scan(&flows); err != nil {
			return err
		}
		// A start up that went down before dropping the old tables already moved them.
		if flows == 0 {
			if err := adoptBoard(db); err != nil {
				return err
			}
		}
		for _, t := range []string{"flow_current", "start_node"} {
			if _, err := db.Exec("DROP TABLE IF EXISTS " + t); err != nil {
				return err
			}
		}
	}

	for _, t := range flowTables {
		var nullable string
		err := db.QueryRow(`SELECT IS_NULLABLE FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'flow_id'`, t).Scan(&nullable)
		if err != nil {
			return err
		}
		if nullable == "YES" {
			if _, err := db.Exec("ALTER TABLE " + t + " MODIFY flow_id BIGINT NOT NULL"); err != nil {
				return err
			}
		}
		ok, err := hasConstraint(db, t, t+"_flow")
		if err != nil {
			return err
		}
		if !ok {
			// Deleting a flow deletes what is in it first; see deleteFlow.
			if _, err := db.Exec("ALTER TABLE " + t + " ADD CONSTRAINT " + t + "_flow FOREIGN KEY (flow_id) REFERENCES flows(id)"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Makes the single board from before flows the first flow.
func adoptBoard(db *sql.DB) error {
	if err := addColumn(db, "start_node", "route", "JSON NULL"); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version *int64
	hasCurrent, err := hasTable(db, "flow_current")
	if err != nil {
		return err
	}
	if hasCurrent {
		err := tx.QueryRow("SELECT version_id FROM flow_current WHERE id = 1").Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	res, err := tx.Exec("INSERT INTO flows (name, version_id) VALUES ('Flow', ?)", version)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO flow_starts (flow_id, x, y, target_id, route) SELECT ?, x, y, target_id, route FROM start_node WHERE id = 1", id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT IGNORE INTO flow_starts (flow_id, x, y) VALUES (?, 40, 40)", id); err != nil {
		return err
	}
	for _, t := range flowTables {
		if _, err := tx.Exec("UPDATE "+t+" SET flow_id = ? WHERE flow_id IS NULL", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func hasTable(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, name).Scan(&n)
	return n > 0, err
}

// The flow a request is about, from ?flow=<id>. Writes the error and returns
// false if it is missing or invalid.
func flowParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("flow"), 10, 64)
	if err != nil {
		http.Error(w, "flow is required", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// The flow a process is in.
func flowOf(ctx context.Context, db querier, process int64) (int64, error) {
	var f int64
	err := db.QueryRowContext(ctx, "SELECT flow_id FROM processes WHERE id = ?", process).Scan(&f)
	return f, err
}

// The flow something new on a board goes in: its parent's, or for the top
// board the one given. Says what is wrong, if anything.
func flowFor(ctx context.Context, db *sql.DB, given int64, parent *int64) (int64, string, error) {
	if parent != nil {
		f, err := flowOf(ctx, db, *parent)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "parent must be a flow process", nil
		}
		return f, "", err
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM flows WHERE id = ?", given).Scan(&n); err != nil {
		return 0, "", err
	}
	if n == 0 {
		return 0, "flow not found", nil
	}
	return given, "", nil
}

func getFlow(ctx context.Context, db *sql.DB, id int64) (flow, error) {
	var f flow
	err := db.QueryRowContext(ctx, "SELECT id, name, settings, created_at FROM flows WHERE id = ?", id).Scan(&f.ID, &f.Name, &f.Settings, &f.CreatedAt)
	if err != nil {
		return f, err
	}
	var rn run
	err = scanRun(db.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE flow_id = ? AND parent_run_id IS NULL ORDER BY id DESC LIMIT 1", id), &rn)
	if err == nil {
		f.LastRun = &rn
	} else if !errors.Is(err, sql.ErrNoRows) {
		return f, err
	}
	return f, nil
}

// Deletes a flow with everything in it and every run of it, and returns
// the runs, whose workspaces are to go too.
func deleteFlow(ctx context.Context, tx *sql.Tx, id int64) ([]int64, error) {
	var runs []int64
	rows, err := tx.QueryContext(ctx, "SELECT id FROM runs WHERE flow_id = ?", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r int64
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stmts := []string{
		"UPDATE flows SET version_id = NULL WHERE id = ?",
		"DELETE FROM runs WHERE flow_id = ?",
		// The processes inside flow processes, and the objects on their boards, go with them.
		"DELETE FROM processes WHERE flow_id = ? AND parent_id IS NULL",
		"DELETE FROM processes WHERE flow_id = ?",
		"DELETE FROM objects WHERE flow_id = ?",
		"DELETE FROM flow_versions WHERE flow_id = ?",
		"DELETE FROM flows WHERE id = ?",
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s, id); err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func registerFlowListRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	mux.HandleFunc("GET /api/flows", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id FROM flows ORDER BY name, id")
		if err != nil {
			serverError(w, err)
			return
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				serverError(w, err)
				return
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		fs := []flow{}
		for _, id := range ids {
			f, err := getFlow(r.Context(), db, id)
			if err != nil {
				serverError(w, err)
				return
			}
			fs = append(fs, f)
		}
		writeJSON(w, http.StatusOK, fs)
	})

	mux.HandleFunc("GET /api/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	})

	mux.HandleFunc("POST /api/flows", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Name = strings.TrimSpace(body.Name); body.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(r.Context(), "INSERT INTO flows (name) VALUES (?)", body.Name)
		if err != nil {
			serverError(w, err)
			return
		}
		id, err := res.LastInsertId()
		if err != nil {
			serverError(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO flow_starts (flow_id, x, y) VALUES (?, 40, 40)", id); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, f)
	})

	mux.HandleFunc("PATCH /api/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			Name *string `json:"name"`
			// Replaces them all; runs going on keep the ones they started with.
			Settings *flowSettings `json:"settings"`
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
		if patch.Settings != nil {
			if msg := patch.Settings.check(); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE flows SET name = COALESCE(?, name), settings = COALESCE(?, settings) WHERE id = ?",
			patch.Name, patch.Settings, id); err != nil {
			serverError(w, err)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	})

	mux.HandleFunc("DELETE /api/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		var running int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE flow_id = ? AND status = 'running'", id).Scan(&running); err != nil {
			serverError(w, err)
			return
		}
		if running > 0 {
			http.Error(w, "can't delete a flow while it is running", http.StatusConflict)
			return
		}
		runs, err := deleteFlow(ctx, tx, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		dropWorkspaces(workerURL, runs)
		w.WriteHeader(http.StatusNoContent)
	})
}
