package main

// Each run keeps a log of what happened in it, which its Logs window shows
// (RunLogs.tsx): when it started and who started it, each step starting and
// how it ended, what went wrong, the lines the worker sent of the steps it
// ran, and how the run finished. The webapp's lines go to its own log too,
// prefixed with the run, so the server's log tells the same story of every
// run at once. A line is the webapp's or the worker's, and info or an error.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	logInfo  = "info"
	logError = "error"

	logWebapp = "webapp"
	logWorker = "worker"
)

// A line of a run's log. Step is the step it is about, if any.
type runLogLine struct {
	ID      int64     `json:"id"`
	Step    *int64    `json:"step"`
	Source  string    `json:"source"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

// A line the worker sent of a step it ran, as it sends them.
type workerLogLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Logs a line of the webapp's about a run, and step in it if not 0, in the
// server's log and the run's. It doesn't take the run's context: how a
// stopped run ended is written once that has ended.
func runLog(db *sql.DB, run, step int64, level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	prefix := ""
	if level == logError {
		prefix = "ERROR: "
	}
	log.Printf("%srun %d: %s", prefix, run, msg)
	// Timed by this clock, as the worker times its lines, not the database's.
	if _, err := db.ExecContext(context.Background(),
		"INSERT INTO run_logs (run_id, step_id, source, level, message, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		run, nullID(step), logWebapp, level, msg, time.Now().UTC()); err != nil {
		log.Printf("record the log of run %d: %v", run, err)
	}
}

// Logs a line about a step, in its run's log.
func stepLog(db *sql.DB, step int64, level, format string, args ...any) {
	var run int64
	if err := db.QueryRowContext(context.Background(), "SELECT run_id FROM run_steps WHERE id = ?", step).Scan(&run); err != nil {
		log.Printf("log step %d: %v", step, err)
		return
	}
	runLog(db, run, step, level, format, args...)
}

// Records the lines the worker sent of a step in its run's log. They are in
// the worker's log already, so not in the webapp's.
func recordWorkerLog(db *sql.DB, step int64, lines []workerLogLine) {
	for _, l := range lines {
		level := logInfo
		if l.Level == logError {
			level = logError
		}
		at := l.Time
		if at.IsZero() {
			at = time.Now()
		}
		if _, err := db.ExecContext(context.Background(),
			`INSERT INTO run_logs (run_id, step_id, source, level, message, created_at)
			SELECT run_id, id, ?, ?, ?, ? FROM run_steps WHERE id = ?`,
			logWorker, level, l.Message, at.UTC(), step); err != nil {
			log.Printf("record the worker's log of step %d: %v", step, err)
			return
		}
	}
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// Logs that a run started: of which flow, who or what started it, and for
// a run within another, which.
func logRunStarted(db *sql.DB, id int64) {
	ctx := context.Background()
	rn, err := getRun(ctx, db, id)
	if err != nil {
		log.Printf("log the start of run %d: %v", id, err)
		return
	}
	var flow string
	if err := db.QueryRowContext(ctx, "SELECT name FROM flows WHERE id = ?", rn.Flow).Scan(&flow); err != nil {
		log.Printf("log the start of run %d: %v", id, err)
	}
	msg := fmt.Sprintf("started, a run of %q", flow)
	if rn.Version != nil {
		msg += fmt.Sprintf(" on version %d", *rn.Version)
	}
	switch {
	case rn.StartedBy != "":
		msg += ", by " + rn.StartedBy
	case rn.APIKey != "":
		msg += fmt.Sprintf(", through the API key %q", rn.APIKey)
	case rn.Webhook != nil:
		// Which webhook is said below.
	case rn.Schedule != nil:
		msg += fmt.Sprintf(", by schedule %d", *rn.Schedule)
	case rn.Parent == nil:
		msg += ", by someone unknown, as nobody signs in"
	}
	switch {
	case rn.Webhook != nil && *rn.Webhook != "":
		msg += fmt.Sprintf(", for a delivery to the webhook %q", *rn.Webhook)
	case rn.Webhook != nil:
		msg += ", for a delivery to a webhook since removed"
	}
	if rn.Record != nil {
		msg += fmt.Sprintf(", on record %d", *rn.Record)
		// Started by a task board playing; see taskboard_play.go.
		var board string
		err := db.QueryRowContext(ctx, "SELECT b.name FROM task_board_runs t JOIN task_boards b ON b.id = t.board_id WHERE t.run_id = ?", id).Scan(&board)
		switch {
		case err == nil:
			msg += fmt.Sprintf(", as the task board %q plays", board)
		case !errors.Is(err, sql.ErrNoRows):
			log.Printf("log the start of run %d: %v", id, err)
		}
	}
	if rn.RerunOf != nil {
		msg += fmt.Sprintf("; a re-run of run %d from %s", *rn.RerunOf, rn.RerunFromName)
	}
	if rn.TrialOf != nil {
		msg += "; a trial of " + rn.TrialOfName + " alone, which stores nothing real runs see"
	}
	if rn.Parent != nil {
		msg += fmt.Sprintf("; within run %d", *rn.Parent)
		if rn.ProcessName != "" {
			msg += ", for " + rn.ProcessName
		}
		if rn.ItemKey != "" {
			msg += fmt.Sprintf(", item %q", rn.ItemKey)
		}
	}
	runLog(db, id, 0, logInfo, "%s", msg)
}

// Logs how a run ended, and how long it took.
func logRunFinished(db *sql.DB, rn run) {
	took := ""
	if rn.FinishedAt != nil {
		took = " after " + rn.FinishedAt.Sub(rn.StartedAt).Round(time.Millisecond).String()
	}
	level := logInfo
	if rn.Status != runDone {
		level = logError
	}
	runLog(db, rn.ID, 0, level, "finished: %s%s", rn.Status, took)
}

// Logs that a step started, with what it is: its process and its kind; a
// person's starts waiting for them.
func logStepStarted(db *sql.DB, step int64, status string) {
	var run int64
	var name, kind, model string
	err := db.QueryRowContext(context.Background(), "SELECT run_id, process_name, kind, model FROM run_steps WHERE id = ?", step).
		Scan(&run, &name, &kind, &model)
	if err != nil {
		log.Printf("log the start of step %d: %v", step, err)
		return
	}
	if status == stepWaiting {
		runLog(db, run, step, logInfo, "step %d, %s %q, waits for a person", step, kind, name)
		return
	}
	with := ""
	if model != "" {
		with = " with " + model
	}
	runLog(db, run, step, logInfo, "step %d, %s %q, started%s", step, kind, name, with)
}

// Logs how a step ended: what went wrong if it failed, why it didn't run if
// it was skipped, and who decided for a person's. A person's decision is
// logged as it is made, so decided leaves out a person's step that ended so.
func logStepFinished(db *sql.DB, step int64, decided bool) {
	var run int64
	var name, kind, status, errMsg, outcome, decidedBy string
	var started time.Time
	var finished sql.NullTime
	err := db.QueryRowContext(context.Background(),
		"SELECT run_id, process_name, kind, status, error, outcome, "+userLabelColumn("decided_by")+", started_at, finished_at FROM run_steps WHERE id = ?", step).
		Scan(&run, &name, &kind, &status, &errMsg, &outcome, &decidedBy, &started, &finished)
	if err != nil {
		log.Printf("log the end of step %d: %v", step, err)
		return
	}
	if !decided && kind == kindHuman && status == stepDone {
		return
	}
	took := ""
	if finished.Valid {
		took = " after " + finished.Time.Sub(started).Round(time.Millisecond).String()
	}
	errMsg = strings.TrimSpace(errMsg)
	switch {
	case status == stepError || status == stepDuplicate:
		msg := fmt.Sprintf("step %d, %q, failed%s", step, name, took)
		if errMsg != "" {
			msg += ": " + errMsg
		}
		runLog(db, run, step, logError, "%s", msg)
	case status == stepSkipped:
		runLog(db, run, step, logInfo, "step %d, %q, skipped: %s", step, name, errMsg)
	case decidedBy != "" || outcome != "":
		msg := fmt.Sprintf("step %d, %q, decided%s", step, name, took)
		if decidedBy != "" {
			msg += " by " + decidedBy
		}
		if outcome != "" {
			msg += ": " + outcome
		}
		runLog(db, run, step, logInfo, "%s", msg)
	default:
		runLog(db, run, step, logInfo, "step %d, %q, ended %s%s", step, name, status, took)
	}
}

func registerLogRoutes(mux *http.ServeMux, db *sql.DB) {
	// The run's log, oldest first; after gives only the lines past that id,
	// so a window following a run going on fetches what is new.
	mux.HandleFunc("GET /api/runs/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var after int64
		if a := r.URL.Query().Get("after"); a != "" {
			if after, err = strconv.ParseInt(a, 10, 64); err != nil {
				http.Error(w, "invalid after", http.StatusBadRequest)
				return
			}
		}
		if _, err := getRun(r.Context(), db, id); err == sql.ErrNoRows {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		} else if err != nil {
			serverError(w, err)
			return
		}
		rows, err := db.QueryContext(r.Context(),
			"SELECT id, step_id, source, level, message, created_at FROM run_logs WHERE run_id = ? AND id > ? ORDER BY id LIMIT 5000", id, after)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		lines := []runLogLine{}
		for rows.Next() {
			var l runLogLine
			if err := rows.Scan(&l.ID, &l.Step, &l.Source, &l.Level, &l.Message, &l.Time); err != nil {
				serverError(w, err)
				return
			}
			lines = append(lines, l)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, lines)
	})
}
