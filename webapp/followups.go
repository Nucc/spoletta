package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// An agent step that worked in a workspace keeps its pi session there, so
// once it is over a person can carry its conversation on: send it a message,
// such as to push what it committed or to change something, and the agent
// acts on it with everything it did in mind, in the same checkouts on the
// same branch. Each such follow-up is another try of the step's conversation;
// what it does doesn't change what the step generated.
type followUp struct {
	ID int64 `json:"id"`
	// The try of the step's conversation its messages belong to.
	Attempt int    `json:"attempt"`
	Message string `json:"message"`
	// active while it goes, then done or error.
	Status     string        `json:"status"`
	Output     string        `json:"output"`
	Error      string        `json:"error"`
	Commits    []repoCommits `json:"commits"`
	StartedAt  time.Time     `json:"startedAt"`
	FinishedAt *time.Time    `json:"finishedAt"`
}

const maxFollowUpBytes = 100 << 10

func migrateFollowUps(db *sql.DB) error {
	if err := addColumn(db, "run_steps", "session", "JSON NULL"); err != nil {
		return err
	}
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS run_step_followups (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			step_id BIGINT NOT NULL,
			attempt INT NOT NULL,
			message TEXT NOT NULL,
			status VARCHAR(16) NOT NULL,
			output LONGTEXT NOT NULL,
			error TEXT NOT NULL,
			commits JSON NULL,
			started_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			finished_at TIMESTAMP(3) NULL,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE
		)`,
		// Follow-ups go on in the server; what was in flight when it went down can't continue.
		`UPDATE run_step_followups SET status = 'error', error = 'The server stopped before this finished.', finished_at = CURRENT_TIMESTAMP(3)
		WHERE status = 'active'`,
	} {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// The follow-ups of a run's steps, by step id.
func runFollowUps(ctx context.Context, db *sql.DB, runID int64) (map[int64][]followUp, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT f.step_id, f.id, f.attempt, f.message, f.status, f.output, f.error, f.commits, f.started_at, f.finished_at
		FROM run_step_followups f JOIN run_steps s ON s.id = f.step_id
		WHERE s.run_id = ? ORDER BY f.id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byStep := map[int64][]followUp{}
	for rows.Next() {
		var step int64
		var f followUp
		var commits []byte
		if err := rows.Scan(&step, &f.ID, &f.Attempt, &f.Message, &f.Status, &f.Output, &f.Error, &commits, &f.StartedAt, &f.FinishedAt); err != nil {
			return nil, err
		}
		f.Commits = []repoCommits{}
		if commits != nil {
			if err := json.Unmarshal(commits, &f.Commits); err != nil {
				return nil, err
			}
		}
		byStep[step] = append(byStep[step], f)
	}
	return byStep, rows.Err()
}

// Tells the agent who the message is from: the instructions it ran with,
// such as not to push, came before it.
func followUpPrompt(message string) string {
	return fmt.Sprintf("A person following this run sent you the message below. Act on it in the same workspace, on the same branch; "+
		"where it differs from the instructions you were given before, it takes precedence.\n\n<message>\n%s\n</message>\n", message)
}

// The follow-ups going, so they can be stopped.
type followUps struct {
	mu    sync.Mutex
	going map[int64]context.CancelFunc
}

func registerFollowUpRoutes(mux *http.ServeMux, db *sql.DB, workerURL string, mcp *mcpBroker) {
	fs := &followUps{going: map[int64]context.CancelFunc{}}

	// Carries on the conversation of an agent step that is over:
	// {"message": "..."}. The follow-up goes on in the server; this returns
	// it once it started.
	mux.HandleFunc("POST /api/runs/{run}/steps/{id}/followups", func(w http.ResponseWriter, r *http.Request) {
		runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
		stepID, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFollowUpBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		body.Message = strings.TrimSpace(body.Message)
		if body.Message == "" {
			http.Error(w, "message is required", http.StatusBadRequest)
			return
		}
		f, run, uses, err := startFollowUp(r.Context(), db, runID, stepID, body.Message)
		if errors.Is(err, errNotFound) {
			http.Error(w, "no agent step with that id in this run", http.StatusNotFound)
			return
		}
		var conflict conflictError
		if errors.As(err, &conflict) {
			http.Error(w, string(conflict), http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}

		who := requestUserID(r)
		ctx, cancel := context.WithCancel(context.Background())
		fs.mu.Lock()
		fs.going[f.ID] = cancel
		fs.mu.Unlock()
		go func() {
			defer func() {
				fs.mu.Lock()
				delete(fs.going, f.ID)
				fs.mu.Unlock()
				cancel()
			}()
			res := runFollowUp(ctx, db, workerURL, mcp, who, run, uses, stepID, f.Attempt)
			if err := finishFollowUp(db, f.ID, res); err != nil {
				log.Printf("finish follow-up %d of step %d: %v", f.ID, stepID, err)
			}
			// The run's workspace isn't needed again until the next one.
			if run.Workspace != nil {
				releaseIfOver(context.Background(), db, workerURL, run.Workspace.ID)
			}
		}()
		writeJSON(w, http.StatusCreated, f)
	})

	// Stops a follow-up still going; it fails.
	mux.HandleFunc("POST /api/runs/{run}/steps/{step}/followups/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		fs.mu.Lock()
		cancel, ok := fs.going[id]
		fs.mu.Unlock()
		if !ok {
			http.Error(w, "no follow-up going with that id", http.StatusConflict)
			return
		}
		cancel()
		w.WriteHeader(http.StatusNoContent)
	})
}

// Why a follow-up can't start now.
type conflictError string

func (e conflictError) Error() string { return string(e) }

// Records a follow-up as going, and returns it with what the worker runs and
// the MCP servers the step's process uses. A step carries on one follow-up at
// a time, and only once the run's steps are all over: they'd share its
// checkouts.
func startFollowUp(ctx context.Context, db *sql.DB, runID, stepID int64, message string) (followUp, agentRun, processSettings, error) {
	f := followUp{Message: message, Status: stepActive, Commits: []repoCommits{}}
	var run agentRun
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return f, run, processSettings{}, err
	}
	defer tx.Rollback()
	// Locking the run orders follow-ups of its steps against each other.
	var locked int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM runs WHERE id = ? FOR UPDATE", runID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return f, run, processSettings{}, errNotFound
	}
	if err != nil {
		return f, run, processSettings{}, err
	}
	var kind, status string
	var processID *int64
	var session []byte
	err = tx.QueryRowContext(ctx,
		"SELECT kind, status, process_id, process_name, model, thinking, session FROM run_steps WHERE id = ? AND run_id = ?", stepID, runID).
		Scan(&kind, &status, &processID, &run.ProcessName, &run.Model, &run.Thinking, &session)
	if errors.Is(err, sql.ErrNoRows) || err == nil && kind != kindAgent {
		return f, run, processSettings{}, errNotFound
	}
	if err != nil {
		return f, run, processSettings{}, err
	}
	if session == nil {
		return f, run, processSettings{}, conflictError("the step's agent kept no session to carry on: only agents that work on repositories do, in runs started since sessions are kept")
	}
	var s stepSession
	if err := json.Unmarshal(session, &s); err != nil {
		return f, run, processSettings{}, err
	}
	var going int
	err = tx.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM run_steps WHERE run_id = ? AND status = ?)
		+ (SELECT COUNT(*) FROM run_step_followups f JOIN run_steps s ON s.id = f.step_id WHERE s.run_id = ? AND f.status = ?)`,
		runID, stepActive, runID, stepActive).Scan(&going)
	if err != nil {
		return f, run, processSettings{}, err
	}
	if going > 0 {
		return f, run, processSettings{}, conflictError("the run has a step or a follow-up still going, which works on the same branch; wait until it is over")
	}
	// The next try of the step's conversation.
	err = tx.QueryRowContext(ctx,
		`SELECT GREATEST(
			COALESCE((SELECT MAX(attempt) FROM run_step_messages WHERE step_id = ?), 1),
			COALESCE((SELECT MAX(attempt) FROM run_step_followups WHERE step_id = ?), 1)) + 1`,
		stepID, stepID).Scan(&f.Attempt)
	if err != nil {
		return f, run, processSettings{}, err
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO run_step_followups (step_id, attempt, message, status, output, error) VALUES (?, ?, ?, ?, '', '')",
		stepID, f.Attempt, message, stepActive)
	if err != nil {
		return f, run, processSettings{}, err
	}
	if f.ID, err = res.LastInsertId(); err != nil {
		return f, run, processSettings{}, err
	}
	// The process's MCP servers, tools and skills as it has them now; one
	// since deleted has none.
	var uses processSettings
	if processID != nil {
		var flow int64
		err := tx.QueryRowContext(ctx, "SELECT flow_id, settings FROM processes WHERE id = ?", *processID).Scan(&flow, &uses)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return f, run, uses, err
		}
		if err == nil {
			var msg string
			if run.Skills, msg, err = stepSkills(ctx, tx, flow, uses.Skills); err != nil {
				return f, run, uses, err
			}
			if msg != "" {
				return f, run, uses, conflictError(msg)
			}
		}
		run.ProcessID = *processID
	}
	if err := tx.Commit(); err != nil {
		return f, run, processSettings{}, err
	}
	f.StartedAt = time.Now()
	run.Prompt = followUpPrompt(message)
	run.Workspace = s.Workspace
	run.SessionID = s.ID
	run.Resume = true
	return f, run, uses, nil
}

// Runs a follow-up in the worker for whoever asked for it, with their MCP
// connections, recording its conversation as the step's.
func runFollowUp(ctx context.Context, db *sql.DB, workerURL string, mcp *mcpBroker, who *int64, run agentRun, uses processSettings, stepID int64, attempt int) runResult {
	var msg string
	var err error
	if err = run.withModelAccess(ctx, db); err != nil {
		return runResult{Error: err.Error()}
	}
	run.MCPServers, run.MCPToken, msg, err = mcp.grant(ctx, who, uses)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	defer mcp.revoke(run.MCPToken)
	if msg != "" {
		return runResult{Error: msg}
	}
	return runAgent(ctx, db, workerURL, run, stepID, attempt)
}

func finishFollowUp(db *sql.DB, id int64, res runResult) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status := stepError
	if res.OK {
		status = stepDone
	}
	var commits []byte
	if len(res.Commits) > 0 {
		var err error
		if commits, err = json.Marshal(res.Commits); err != nil {
			return err
		}
	}
	_, err := db.ExecContext(ctx,
		"UPDATE run_step_followups SET status = ?, output = ?, error = ?, commits = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ?",
		status, res.Output, res.Error, commits, id)
	return err
}
