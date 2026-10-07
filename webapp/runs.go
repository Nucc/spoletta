package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// A run is one execution of the flow from the start node. The frontend walks
// the flow and records each process it executes as a step; agent steps are
// recorded by the run endpoint, which knows the exact prompt sent.
type run struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// A step is one execution of a process in a run. A process can run several
// times in one run when a human routes changes back to it. The process's
// name and settings are copied, so the history survives later edits.
type step struct {
	ID          int64  `json:"id"`
	Process     *int64 `json:"process"`
	ProcessName string `json:"processName"`
	Kind        string `json:"kind"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	// The prompt as sent to the agent, inputs and change request included.
	Prompt        string         `json:"prompt"`
	Inputs        []runInput     `json:"inputs"`
	ChangeRequest *changeRequest `json:"changeRequest"`
	Status        string         `json:"status"`
	Output        string         `json:"output"`
	Error         string         `json:"error"`
	// For human steps: approved or changes, and the reviewer's instructions.
	Outcome      string     `json:"outcome"`
	Instructions string     `json:"instructions"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
}

// The content an object got in a run from the step that generated it.
type runObject struct {
	ID      int64  `json:"id"`
	Step    int64  `json:"step"`
	Object  *int64 `json:"object"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

const (
	runRunning     = "running"
	runDone        = "done"
	runFailed      = "failed"
	runInterrupted = "interrupted"

	stepActive  = "active"
	stepWaiting = "waiting"
	stepDone    = "done"
	stepError   = "error"
)

func validRunEnd(s string) bool {
	return s == runDone || s == runFailed || s == runInterrupted
}

func validStepStatus(s string) bool {
	return s == stepActive || s == stepWaiting || s == stepDone || s == stepError
}

// Processes and objects can be deleted later; their history stays.
func migrateRuns(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			status VARCHAR(16) NOT NULL,
			started_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			finished_at TIMESTAMP(3) NULL
		)`,
		`CREATE TABLE IF NOT EXISTS run_steps (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			run_id BIGINT NOT NULL,
			process_id BIGINT NULL,
			process_name VARCHAR(255) NOT NULL,
			kind VARCHAR(16) NOT NULL,
			model VARCHAR(255) NOT NULL DEFAULT '',
			thinking VARCHAR(16) NOT NULL DEFAULT '',
			prompt LONGTEXT NOT NULL,
			inputs JSON NOT NULL,
			change_request JSON NULL,
			status VARCHAR(16) NOT NULL,
			output LONGTEXT NOT NULL,
			error TEXT NOT NULL,
			outcome VARCHAR(16) NOT NULL DEFAULT '',
			instructions TEXT NOT NULL,
			started_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			finished_at TIMESTAMP(3) NULL,
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
			FOREIGN KEY (process_id) REFERENCES processes(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS run_objects (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			run_id BIGINT NOT NULL,
			step_id BIGINT NOT NULL,
			object_id BIGINT NULL,
			name VARCHAR(255) NOT NULL,
			content LONGTEXT NOT NULL,
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE,
			FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE SET NULL
		)`,
		// Runs are driven by a browser; whatever was in flight when the server
		// went down can't continue. Their open steps end first, while the runs
		// still read as running.
		`UPDATE run_steps s JOIN runs r ON r.id = s.run_id
		SET s.status = 'error', s.error = 'The run ended before this step finished.', s.finished_at = CURRENT_TIMESTAMP(3)
		WHERE r.status = 'running' AND s.status IN ('active', 'waiting')`,
		"UPDATE runs SET status = 'interrupted', finished_at = CURRENT_TIMESTAMP(3) WHERE status = 'running'",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Starts a step of a run, copying the process's current settings into it.
func startStep(ctx context.Context, db *sql.DB, runID, processID int64, status, prompt string, inputs []runInput, cr *changeRequest) (int64, error) {
	if inputs == nil {
		inputs = []runInput{}
	}
	in, err := json.Marshal(inputs)
	if err != nil {
		return 0, err
	}
	var crJSON []byte
	if cr != nil {
		if crJSON, err = json.Marshal(cr); err != nil {
			return 0, err
		}
	}
	res, err := db.ExecContext(ctx,
		`INSERT INTO run_steps (run_id, process_id, process_name, kind, model, thinking, prompt, inputs, change_request, status, output, error, instructions)
		SELECT ?, id, name, kind, model, thinking, ?, ?, ?, ?, '', '', '' FROM processes WHERE id = ?`,
		runID, prompt, in, crJSON, status, processID)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err == nil {
			err = errNotFound
		}
		return 0, err
	}
	return res.LastInsertId()
}

var errNotFound = errors.New("not found")

// Finishes a step. A successful step generates its process's output objects,
// which all hold what it printed.
func finishStep(ctx context.Context, db *sql.DB, stepID int64, status, output, errMsg string) error {
	_, err := db.ExecContext(ctx,
		"UPDATE run_steps SET status = ?, output = ?, error = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ?",
		status, output, errMsg, stepID)
	if err != nil || status != stepDone {
		return err
	}
	_, err = db.ExecContext(ctx,
		`INSERT INTO run_objects (run_id, step_id, object_id, name, content)
		SELECT s.run_id, s.id, o.id, o.name, ? FROM run_steps s
		JOIN object_links l ON l.process_id = s.process_id AND l.direction = 'output'
		JOIN objects o ON o.id = l.object_id
		WHERE s.id = ? AND s.kind <> 'human'
		ORDER BY o.id`,
		output, stepID)
	return err
}

func registerRunRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, status, started_at, finished_at FROM runs ORDER BY id DESC LIMIT 200")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		rs := []run{}
		for rows.Next() {
			var rn run
			if err := rows.Scan(&rn.ID, &rn.Status, &rn.StartedAt, &rn.FinishedAt); err != nil {
				serverError(w, err)
				return
			}
			rs = append(rs, rn)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rs)
	})

	getRun := func(ctx context.Context, id int64) (run, error) {
		var rn run
		err := db.QueryRowContext(ctx, "SELECT id, status, started_at, finished_at FROM runs WHERE id = ?", id).
			Scan(&rn.ID, &rn.Status, &rn.StartedAt, &rn.FinishedAt)
		return rn, err
	}

	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		res, err := db.ExecContext(r.Context(), "INSERT INTO runs (status) VALUES (?)", runRunning)
		if err != nil {
			serverError(w, err)
			return
		}
		id, err := res.LastInsertId()
		if err != nil {
			serverError(w, err)
			return
		}
		rn, err := getRun(r.Context(), id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rn)
	})

	// The run with its steps in the order they started and the objects they generated.
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		rn, err := getRun(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}

		detail := struct {
			run
			Steps   []step      `json:"steps"`
			Objects []runObject `json:"objects"`
		}{run: rn, Steps: []step{}, Objects: []runObject{}}

		rows, err := db.QueryContext(r.Context(),
			`SELECT id, process_id, process_name, kind, model, thinking, prompt, inputs, change_request,
				status, output, error, outcome, instructions, started_at, finished_at
			FROM run_steps WHERE run_id = ? ORDER BY id`, id)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var s step
			var inputs []byte
			var cr []byte
			if err := rows.Scan(&s.ID, &s.Process, &s.ProcessName, &s.Kind, &s.Model, &s.Thinking, &s.Prompt, &inputs, &cr,
				&s.Status, &s.Output, &s.Error, &s.Outcome, &s.Instructions, &s.StartedAt, &s.FinishedAt); err != nil {
				serverError(w, err)
				return
			}
			if err := json.Unmarshal(inputs, &s.Inputs); err != nil {
				serverError(w, err)
				return
			}
			if cr != nil {
				if err := json.Unmarshal(cr, &s.ChangeRequest); err != nil {
					serverError(w, err)
					return
				}
			}
			detail.Steps = append(detail.Steps, s)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}

		orows, err := db.QueryContext(r.Context(),
			"SELECT id, step_id, object_id, name, content FROM run_objects WHERE run_id = ? ORDER BY id", id)
		if err != nil {
			serverError(w, err)
			return
		}
		defer orows.Close()
		for orows.Next() {
			var o runObject
			if err := orows.Scan(&o.ID, &o.Step, &o.Object, &o.Name, &o.Content); err != nil {
				serverError(w, err)
				return
			}
			detail.Objects = append(detail.Objects, o)
		}
		if err := orows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	})

	// Ends a run. Only a running run can end, so a late update from a closed
	// page can't overwrite how it ended.
	mux.HandleFunc("PATCH /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !validRunEnd(patch.Status) {
			http.Error(w, "status must be done, failed or interrupted", http.StatusBadRequest)
			return
		}

		_, err = db.ExecContext(r.Context(),
			"UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ? AND status = ?",
			patch.Status, id, runRunning)
		if err != nil {
			serverError(w, err)
			return
		}
		// Steps left open, such as a review nobody decided, ended with the run.
		_, err = db.ExecContext(r.Context(),
			"UPDATE run_steps SET status = ?, error = 'The run ended before this step finished.', finished_at = CURRENT_TIMESTAMP(3) WHERE run_id = ? AND status IN (?, ?)",
			stepError, id, stepActive, stepWaiting)
		if err != nil {
			serverError(w, err)
			return
		}
		rn, err := getRun(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rn)
	})

	// Records a script or human step; agent steps are recorded when they run.
	mux.HandleFunc("POST /api/runs/{id}/steps", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Process int64      `json:"process"`
			Status  string     `json:"status"`
			Inputs  []runInput `json:"inputs"`
		}
		err = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&body)
		if err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Status != stepActive && body.Status != stepWaiting {
			http.Error(w, "status must be active or waiting", http.StatusBadRequest)
			return
		}

		stepID, err := startStep(r.Context(), db, id, body.Process, body.Status, "", body.Inputs, nil)
		switch {
		case errors.Is(err, errNotFound):
			http.Error(w, "process not found", http.StatusBadRequest)
			return
		case mysqlErrorIs(err, mysqlErrForeignKey):
			http.Error(w, "run not found", http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, struct {
			ID int64 `json:"id"`
		}{stepID})
	})

	// Finishes a script or human step; a human step also records the decision.
	mux.HandleFunc("PATCH /api/runs/{run}/steps/{id}", func(w http.ResponseWriter, r *http.Request) {
		runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
		id, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			Status       string `json:"status"`
			Output       string `json:"output"`
			Error        string `json:"error"`
			Outcome      string `json:"outcome"`
			Instructions string `json:"instructions"`
		}
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&patch)
		if err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Status != stepDone && patch.Status != stepError {
			http.Error(w, "status must be done or error", http.StatusBadRequest)
			return
		}
		if patch.Outcome != "" && patch.Outcome != outcomeApproved && patch.Outcome != outcomeChanges {
			http.Error(w, "outcome must be approved or changes", http.StatusBadRequest)
			return
		}

		// Only open steps of this run; finishing one twice would generate its objects twice.
		var open int
		if err := db.QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM run_steps WHERE id = ? AND run_id = ? AND status IN (?, ?)",
			id, runID, stepActive, stepWaiting).Scan(&open); err != nil {
			serverError(w, err)
			return
		}
		if open == 0 {
			http.Error(w, "no open step with that id in this run", http.StatusConflict)
			return
		}
		_, err = db.ExecContext(r.Context(), "UPDATE run_steps SET outcome = ?, instructions = ? WHERE id = ?",
			patch.Outcome, patch.Instructions, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if err := finishStep(r.Context(), db, id, patch.Status, patch.Output, patch.Error); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
