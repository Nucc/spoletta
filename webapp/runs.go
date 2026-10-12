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

// A run is one execution of the flow from the start node. The engine walks
// the flow and records each process it executes as a step; see engine.go.
type run struct {
	ID int64 `json:"id"`
	// The flow it is a run of.
	Flow   int64  `json:"flow"`
	Status string `json:"status"`
	// The version of the board the run ran on; null for runs from before
	// versions were recorded.
	Version *int64 `json:"version"`
	// For the run of a recording flow process: the run it ran in, and the
	// process, kept after it is deleted. Null for runs of the whole flow. The
	// run of a flow a subflow process ran has a parent in that process's
	// flow, and no process: it ran on its own flow's top board.
	Parent      *int64 `json:"parent"`
	Process     *int64 `json:"process"`
	ProcessName string `json:"processName"`
	// For a run of a flow process for one item of a list: what tells the
	// item apart, its first unique attribute's value or its place in the list.
	ItemKey string `json:"itemKey"`
	// The schedule that started it, if one did; see schedules.go.
	Schedule *int64 `json:"schedule"`
	// Who started it, when someone did with everyone signing in; see users.go.
	StartedBy  string     `json:"startedBy"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

var runColumns = "id, flow_id, status, version_id, parent_run_id, process_ref, process_name, item_key, schedule_id, " + userLabelColumn("started_by") + ", started_at, finished_at"

// The tokens a step's model calls took in and gave out, and what they cost
// at the models' catalog prices, in dollars.
type stepUsage struct {
	Input  int64   `json:"input"`
	Output int64   `json:"output"`
	Cost   float64 `json:"cost"`
}

// The usage of each agent step of a run, by step id, summed over the
// replies of its model.
func runUsage(ctx context.Context, db *sql.DB, run int64) (map[int64]*stepUsage, error) {
	usage := map[int64]*stepUsage{}
	err := scanAll(ctx, db, `SELECT m.step_id,
			COALESCE(SUM(CAST(JSON_EXTRACT(m.message, '$.usage.input') AS DOUBLE)), 0),
			COALESCE(SUM(CAST(JSON_EXTRACT(m.message, '$.usage.output') AS DOUBLE)), 0),
			COALESCE(SUM(CAST(JSON_EXTRACT(m.message, '$.usage.cost.total') AS DOUBLE)), 0)
		FROM run_step_messages m JOIN run_steps s ON s.id = m.step_id
		WHERE s.run_id = ? AND JSON_EXTRACT(m.message, '$.usage') IS NOT NULL
		GROUP BY m.step_id`, func(rows *sql.Rows) error {
		var id int64
		var in, out float64
		u := &stepUsage{}
		if err := rows.Scan(&id, &in, &out, &u.Cost); err != nil {
			return err
		}
		u.Input, u.Output = int64(in), int64(out)
		usage[id] = u
		return nil
	}, run)
	return usage, err
}

func scanRun(row interface{ Scan(...any) error }, rn *run) error {
	return row.Scan(&rn.ID, &rn.Flow, &rn.Status, &rn.Version, &rn.Parent, &rn.Process, &rn.ProcessName, &rn.ItemKey, &rn.Schedule, &rn.StartedBy, &rn.StartedAt, &rn.FinishedAt)
}

// A step is one execution of a process in a run. A process can run several
// times in one run when a human's decision leads back to it. The process's
// name and settings are copied, so the history survives later edits.
type step struct {
	ID int64 `json:"id"`
	// The process's id, kept after it is deleted, since the run's version
	// still has it. Null only for steps whose process was deleted before ids
	// were kept.
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
	// For human steps: the name of the state the human set, and their
	// instructions. Steps from before states hold approved or changes.
	Outcome      string `json:"outcome"`
	Instructions string `json:"instructions"`
	// Who decided it, with everyone signing in; see users.go. For human
	// steps, the roles that decide them, none when anyone does, and whether
	// the user asking may; see user_roles.go.
	DecidedBy string   `json:"decidedBy"`
	HandledBy []string `json:"handledBy,omitempty"`
	CanDecide bool     `json:"canDecide"`
	// For a recording flow process: the run its board ran as.
	ChildRun *int64 `json:"childRun"`
	// The branch it worked on in its run's workspace, if it worked in one,
	// and what it did to each repository there.
	Branch  string        `json:"branch"`
	Commits []repoCommits `json:"commits"`
	// For an agent step that kept its session: the pi session's id, which a
	// person can carry the conversation on in, and the follow-ups they sent;
	// see followups.go.
	Session     string     `json:"session"`
	CanFollowUp bool       `json:"canFollowUp"`
	FollowUps   []followUp `json:"followUps"`
	// What an agent step's model calls used, follow-ups included, as pi
	// reports it; absent for other steps.
	Usage      *stepUsage `json:"usage,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// The content and state an object got in a run from the step that generated
// it, or the state a human step set it to.
type runObject struct {
	ID   int64 `json:"id"`
	Step int64 `json:"step"`
	// Kept after the object is deleted, as a step's process is.
	Object  *int64 `json:"object"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Content string `json:"content"`
	// The generated files its content names, which /api/files serves.
	Files []string `json:"files"`
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
	// A process whose condition didn't hold, so it didn't run; see engine.go.
	stepSkipped = "skipped"
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
			outcome VARCHAR(255) NOT NULL DEFAULT '',
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
			state VARCHAR(255) NOT NULL DEFAULT '',
			content LONGTEXT NOT NULL,
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE,
			FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE SET NULL
		)`,
		// The conversation of an agent step, a message per row; see messages.go.
		`CREATE TABLE IF NOT EXISTS run_step_messages (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			step_id BIGINT NOT NULL,
			attempt INT NOT NULL,
			message JSON NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE
		)`,
		// Outcomes used to be approved or changes; now they name states.
		"ALTER TABLE run_steps MODIFY outcome VARCHAR(255) NOT NULL DEFAULT ''",
		// Runs execute in the server; whatever was in flight when it went down
		// can't continue. Their open steps end first, while the runs
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
	for _, c := range []struct{ table, name, def string }{
		{"run_objects", "state", "VARCHAR(255) NOT NULL DEFAULT '' AFTER name"},
		{"runs", "version_id", "BIGINT NULL AFTER status"},
		// Ids that outlive the process or object: deleting it, or jumping to a
		// version without it, nulls the foreign keys, yet the run's version
		// still has it.
		{"run_steps", "process_ref", "BIGINT NULL AFTER process_id"},
		{"run_objects", "object_ref", "BIGINT NULL AFTER object_id"},
		{"runs", "parent_run_id", "BIGINT NULL AFTER version_id"},
		{"runs", "process_ref", "BIGINT NULL AFTER parent_run_id"},
		{"runs", "process_name", "VARCHAR(255) NOT NULL DEFAULT '' AFTER process_ref"},
		{"runs", "item_key", "VARCHAR(255) NOT NULL DEFAULT '' AFTER process_name"},
		{"run_steps", "child_run_id", "BIGINT NULL AFTER instructions"},
		{"runs", "schedule_id", "BIGINT NULL AFTER item_key"},
	} {
		if err := addColumn(db, c.table, c.name, c.def); err != nil {
			return err
		}
	}
	for _, s := range []string{
		"UPDATE run_steps SET process_ref = process_id WHERE process_ref IS NULL AND process_id IS NOT NULL",
		"UPDATE run_objects SET object_ref = object_id WHERE object_ref IS NULL AND object_id IS NOT NULL",
	} {
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
		`INSERT INTO run_steps (run_id, process_id, process_ref, process_name, kind, model, thinking, prompt, inputs, change_request, status, output, error, instructions)
		SELECT ?, id, id, name, kind, model, thinking, ?, ?, ?, ?, '', '', '' FROM processes WHERE id = ?`,
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
// each in the state its link names; a human step those it fills in. Objects with attributes hold their part
// of the output, given in contents by object id; the others hold all of it.
//
// While the run goes on, persistent objects are stored too. If one holds a
// unique value another run stored, nothing is stored, the step ends as a
// duplicate and its run as terminated; the duplicate is returned.
func finishStep(ctx context.Context, db *sql.DB, stepID int64, status, output, errMsg string, contents map[int64]string) (*duplicate, error) {
	// Parallel steps storing values can still deadlock; the loser rolled
	// back everything, so it can go again.
	for attempt := 1; ; attempt++ {
		dup, err := finishStepOnce(ctx, db, stepID, status, output, errMsg, contents)
		if !mysqlErrorIs(err, mysqlErrDeadlock) || attempt == 3 {
			return dup, err
		}
	}
}

// Read committed takes no gap locks, which would make parallel runs
// replacing their own instances deadlock on the same empty range.
func finishStepOnce(ctx context.Context, db *sql.DB, stepID int64, status, output, errMsg string, contents map[int64]string) (*duplicate, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Locking the run orders this against a parallel step terminating it.
	var runID int64
	var runStatus, kind string
	err = tx.QueryRowContext(ctx,
		"SELECT r.id, r.status, s.kind FROM run_steps s JOIN runs r ON r.id = s.run_id WHERE s.id = ? FOR UPDATE", stepID).
		Scan(&runID, &runStatus, &kind)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE run_steps SET status = ?, output = ?, error = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ?",
		status, output, errMsg, stepID)
	if err != nil {
		return nil, err
	}
	if status != stepDone {
		return nil, tx.Commit()
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO run_objects (run_id, step_id, object_id, object_ref, name, state, content)
		SELECT s.run_id, s.id, o.id, o.id, o.name, COALESCE(st.name, ''), ? FROM run_steps s
		JOIN object_links l ON l.process_id = s.process_id AND l.direction = 'output'
		JOIN objects o ON o.id = l.object_id
		LEFT JOIN object_states st ON st.id = l.state_id
		WHERE s.id = ? AND (s.kind <> 'human' OR (`+filledIn+`))
		ORDER BY o.id`,
		output, stepID)
	if err != nil {
		return nil, err
	}
	for object, content := range contents {
		_, err := tx.ExecContext(ctx, "UPDATE run_objects SET content = ? WHERE step_id = ? AND object_id = ?", content, stepID, object)
		if err != nil {
			return nil, err
		}
	}
	// A step finishing after its run ended stores nothing, nor does a flow
	// process's: the processes inside it stored what it generates.
	if runStatus != runRunning || kind == kindFlow {
		return nil, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, "SAVEPOINT store"); err != nil {
		return nil, err
	}
	dup, err := storeInstances(ctx, tx, runID, stepID)
	if err != nil {
		return nil, err
	}
	if dup != nil {
		// The step keeps what it generated, for the history.
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT store"); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE run_steps SET status = ?, error = ? WHERE id = ?", stepDuplicate, dup.message(), stepID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ?", runTerminated, runID); err != nil {
			return nil, err
		}
		if err := releaseRun(ctx, tx, runID); err != nil {
			return nil, err
		}
	}
	return dup, tx.Commit()
}

// The name of a state a human step can decide on: one its process's output
// links list. Empty if the state isn't one of them.
func decisionState(ctx context.Context, db *sql.DB, stepID, stateID int64) (string, error) {
	var name string
	err := db.QueryRowContext(ctx,
		`SELECT st.name FROM run_steps s
		JOIN object_links l ON l.process_id = s.process_id AND l.direction = 'output'
		JOIN link_states ls ON ls.link_id = l.id AND ls.state_id = ?
		JOIN object_states st ON st.id = ls.state_id
		WHERE s.id = ? AND s.kind = 'human'`,
		stateID, stepID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// Records the state a human step set its object to; the object keeps its content.
func recordDecision(ctx context.Context, db *sql.DB, stepID, stateID int64, content string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO run_objects (run_id, step_id, object_id, object_ref, name, state, content)
		SELECT s.run_id, s.id, o.id, o.id, o.name, st.name, ?
		FROM run_steps s
		JOIN object_states st ON st.id = ?
		JOIN objects o ON o.id = st.object_id
		WHERE s.id = ?`,
		content, stateID, stepID)
	return err
}

func getRun(ctx context.Context, db *sql.DB, id int64) (run, error) {
	var rn run
	err := scanRun(db.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE id = ?", id), &rn)
	return rn, err
}

// Ends a run. Only a running run can end, so a run that ended otherwise
// already, such as terminated, keeps how it ended.
func endRun(ctx context.Context, db *sql.DB, id int64, status string) (run, error) {
	_, err := db.ExecContext(ctx,
		"UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ? AND status = ?",
		status, id, runRunning)
	if err != nil {
		return run{}, err
	}
	// Steps left open, such as a review nobody decided, ended with the run.
	_, err = db.ExecContext(ctx,
		"UPDATE run_steps SET status = ?, error = 'The run ended before this step finished.', finished_at = CURRENT_TIMESTAMP(3) WHERE run_id = ? AND status IN (?, ?)",
		stepError, id, stepActive, stepWaiting)
	if err != nil {
		return run{}, err
	}
	rn, err := getRun(ctx, db, id)
	if err != nil {
		return rn, err
	}
	// Only a run that ended done keeps the values it stored.
	if rn.Status != runDone && rn.Status != runRunning {
		err = releaseRun(ctx, db, id)
	}
	return rn, err
}

// Starts a run of the whole flow on its board as it is, for schedule if one
// started it, or the user who did: a board changed since its version is saved
// as a new one first, so the run's version shows what it ran. The run goes on in the server;
// this returns its id once it started, or sql.ErrNoRows without the flow.
func startRun(ctx context.Context, db *sql.DB, eng *engine, flow int64, schedule, startedBy *int64) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var name string
	var settings flowSettings
	if err := tx.QueryRowContext(ctx, "SELECT name, settings FROM flows WHERE id = ?", flow).Scan(&name, &settings); err != nil {
		return 0, err
	}
	version, err := saveVersion(ctx, tx, flow, true)
	if err != nil {
		return 0, err
	}
	board, err := takeSnapshot(ctx, tx, flow)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO runs (flow_id, status, version_id, schedule_id, started_by) VALUES (?, ?, ?, ?, ?)", flow, runRunning, version, schedule, startedBy)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	eng.start(id, flow, version, board, settings.Repositories, settings.prefix(name))
	return id, nil
}

func registerRunRoutes(mux *http.ServeMux, db *sql.DB, eng *engine) {
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		rows, err := db.QueryContext(r.Context(), "SELECT "+runColumns+" FROM runs WHERE flow_id = ? ORDER BY id DESC LIMIT 200", f)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		rs := []run{}
		for rows.Next() {
			var rn run
			if err := scanRun(rows, &rn); err != nil {
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

	// Starts a run on the board as it is; see startRun.
	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok || refuseInStruct(w, r, db, f) {
			return
		}
		id, err := startRun(r.Context(), db, eng, f, nil, requestUserID(r))
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		rn, err := getRun(r.Context(), db, id)
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
		rn, err := getRun(r.Context(), db, id)
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
			`SELECT id, COALESCE(process_ref, process_id), process_name, kind, model, thinking, prompt, inputs, change_request,
				status, output, error, outcome, instructions, `+userLabelColumn("decided_by")+`, child_run_id, branch, commits, COALESCE(JSON_UNQUOTE(JSON_EXTRACT(session, '$.id')), ''), started_at, finished_at
			FROM run_steps WHERE run_id = ? ORDER BY id`, id)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		followUps, err := runFollowUps(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		usage, err := runUsage(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		var roles []string
		if u := requestUser(r); u != nil {
			if roles, err = userRoles(r.Context(), db, u.ID); err != nil {
				serverError(w, err)
				return
			}
		}
		for rows.Next() {
			var s step
			var inputs []byte
			var cr, commits []byte
			if err := rows.Scan(&s.ID, &s.Process, &s.ProcessName, &s.Kind, &s.Model, &s.Thinking, &s.Prompt, &inputs, &cr,
				&s.Status, &s.Output, &s.Error, &s.Outcome, &s.Instructions, &s.DecidedBy, &s.ChildRun, &s.Branch, &commits, &s.Session, &s.StartedAt, &s.FinishedAt); err != nil {
				serverError(w, err)
				return
			}
			s.CanFollowUp = s.Session != ""
			s.Usage = usage[s.ID]
			if s.FollowUps = followUps[s.ID]; s.FollowUps == nil {
				s.FollowUps = []followUp{}
			}
			if err := json.Unmarshal(inputs, &s.Inputs); err != nil {
				serverError(w, err)
				return
			}
			s.Commits = []repoCommits{}
			if commits != nil {
				if err := json.Unmarshal(commits, &s.Commits); err != nil {
					serverError(w, err)
					return
				}
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
		rows.Close()
		for i, s := range detail.Steps {
			if s.Kind != kindHuman {
				continue
			}
			if s.Process != nil {
				if detail.Steps[i].HandledBy, err = handlers(r.Context(), db, *s.Process); err != nil {
					serverError(w, err)
					return
				}
			}
			detail.Steps[i].CanDecide = mayDecide(requestUser(r), roles, detail.Steps[i].HandledBy)
		}

		orows, err := db.QueryContext(r.Context(),
			"SELECT id, step_id, COALESCE(object_ref, object_id), name, state, content FROM run_objects WHERE run_id = ? ORDER BY id", id)
		if err != nil {
			serverError(w, err)
			return
		}
		defer orows.Close()
		for orows.Next() {
			var o runObject
			if err := orows.Scan(&o.ID, &o.Step, &o.Object, &o.Name, &o.State, &o.Content); err != nil {
				serverError(w, err)
				return
			}
			if o.Files = filesIn(o.Content); o.Files == nil {
				o.Files = []string{}
			}
			detail.Objects = append(detail.Objects, o)
		}
		if err := orows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	})

	// Stops a run: {"status": "interrupted"}. The run ends where it is, and
	// steps still going are cancelled.
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
		if patch.Status != runInterrupted {
			http.Error(w, "status must be interrupted", http.StatusBadRequest)
			return
		}
		// Ended first, so what the cancelled steps still record finds the run over.
		rn, err := endRun(r.Context(), db, id, runInterrupted)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		eng.stop(id)
		writeJSON(w, http.StatusOK, rn)
	})

	// Stops an agent, script or Sentry step still going. It fails, which ends
	// its branch; the rest of the run goes on.
	mux.HandleFunc("POST /api/runs/{run}/steps/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
		id, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if !eng.stopStep(runID, id) {
			http.Error(w, "no step going with that id in this run", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Decides a human step waiting for its review: records the state the
	// human set, if any, their instructions, and what they entered of the
	// objects the step fills in, by object id; see entries.go. Then the run
	// goes on.
	mux.HandleFunc("PATCH /api/runs/{run}/steps/{id}", func(w http.ResponseWriter, r *http.Request) {
		runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
		id, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			State        *int64                     `json:"state"`
			Instructions string                     `json:"instructions"`
			Entries      map[string]json.RawMessage `json:"entries"`
		}
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&patch)
		if err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		var outcome string
		if patch.State != nil {
			if outcome, err = decisionState(r.Context(), db, id, *patch.State); err != nil {
				serverError(w, err)
				return
			}
			if outcome == "" {
				http.Error(w, "the step's process can't decide on that state", http.StatusBadRequest)
				return
			}
		}
		// A step whose process was deleted before ids were kept fills in nothing.
		var process sql.NullInt64
		err = db.QueryRowContext(r.Context(), "SELECT process_ref FROM run_steps WHERE id = ? AND run_id = ?", id, runID).Scan(&process)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no review waiting with that id in this run", http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if msg, err := refuseDecider(r.Context(), db, r, process.Int64); err != nil || msg != "" {
			if err != nil {
				serverError(w, err)
			} else {
				http.Error(w, msg, http.StatusForbidden)
			}
			return
		}
		objects, msg, err := entryObjects(r.Context(), db, process.Int64)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		var output string
		var contents map[int64]string
		if len(objects) > 0 || patch.Entries != nil {
			if output, contents, err = checkEntries(objects, patch.Entries); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		// Only a review of this run still waiting; deciding one twice would
		// let the run go on twice.
		res, err := db.ExecContext(r.Context(),
			`UPDATE run_steps SET status = ?, outcome = ?, instructions = ?, decided_by = ?, finished_at = CURRENT_TIMESTAMP(3)
			WHERE id = ? AND run_id = ? AND kind = ? AND status = ?`,
			stepDone, outcome, patch.Instructions, requestUserID(r), id, runID, kindHuman, stepWaiting)
		if err != nil {
			serverError(w, err)
			return
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				serverError(w, err)
				return
			}
			http.Error(w, "no review waiting with that id in this run", http.StatusConflict)
			return
		}
		// The run records the object in the state set, with the content it has there.
		eng.decide(runID, id, decision{state: patch.State, instructions: patch.Instructions, output: output, entries: contents})
		w.WriteHeader(http.StatusNoContent)
	})
}
