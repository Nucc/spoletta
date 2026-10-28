package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
	// Running with a step waiting for a person: a review, or a loop's
	// question; see run_list.go.
	Waiting bool `json:"waiting"`
	// The version of the board the run ran on; null for runs from before
	// versions were recorded.
	Version *int64 `json:"version"`
	// For the run of a recording flow process: the run it ran in, and the
	// process, kept after it is deleted. Null for runs of the whole flow. The
	// run of a flow a subflow process ran has a parent in that process's
	// flow, and no process: it ran on its own flow's top board. A re-run of
	// such a run is another run of it in the same run.
	Parent      *int64 `json:"parent"`
	Process     *int64 `json:"process"`
	ProcessName string `json:"processName"`
	// For a run re-running another from one of its processes: that run, and
	// the process, kept after it is deleted; see reruns.go.
	RerunOf       *int64 `json:"rerunOf"`
	RerunFrom     *int64 `json:"rerunFrom"`
	RerunFromName string `json:"rerunFromName"`
	// For a run of a flow process for one item of a list: what tells the
	// item apart, its first unique attribute's value or its place in the list.
	ItemKey string `json:"itemKey"`
	// The schedule that started it, if one did; see schedules.go.
	Schedule *int64 `json:"schedule"`
	// Who started it, when someone did with everyone signing in; see users.go.
	StartedBy string `json:"startedBy"`
	// The name of the API key that started it, if one did; see api.go.
	APIKey string `json:"apiKey"`
	// The name of the webhook that started it, if one did, empty once it
	// is removed; null for a run no webhook started. See webhooks.go.
	Webhook *string `json:"webhook"`
	// The shared record a person started it on, if one did; see record_runs.go.
	Record *int64 `json:"record"`
	// For a trial: the process a person tried in it, kept after it is
	// deleted; see trials.go.
	TrialOf     *int64     `json:"trialOf"`
	TrialOfName string     `json:"trialOfName"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  *time.Time `json:"finishedAt"`
}

// What scanRun reads of a run, selected from runs unaliased, which runWaiting
// refers to.
var runColumns = "id, flow_id, status, status = '" + runRunning + "' AND " + runWaiting + ", version_id, parent_run_id, process_ref, process_name, rerun_of, rerun_from, rerun_from_name, item_key, schedule_id, " + userLabelColumn("started_by") + ", COALESCE((SELECT k.name FROM api_keys k WHERE k.id = api_key_id), ''), CASE WHEN webhook_id IS NOT NULL THEN COALESCE((SELECT h.name FROM webhooks h WHERE h.id = webhook_id), '') END, record_id, trial_of, trial_of_name, started_at, finished_at"

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
	return row.Scan(&rn.ID, &rn.Flow, &rn.Status, &rn.Waiting, &rn.Version, &rn.Parent, &rn.Process, &rn.ProcessName, &rn.RerunOf, &rn.RerunFrom, &rn.RerunFromName, &rn.ItemKey, &rn.Schedule, &rn.StartedBy, &rn.APIKey, &rn.Webhook, &rn.Record, &rn.TrialOf, &rn.TrialOfName, &rn.StartedAt, &rn.FinishedAt)
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
	// The prompt as sent to the agent, inputs and change request included;
	// for a loop step, what it asks; see loops.go.
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
	// the user asking may; see user_roles.go. Loop steps are answered by
	// those who may edit the flow.
	DecidedBy string   `json:"decidedBy"`
	HandledBy []string `json:"handledBy,omitempty"`
	CanDecide bool     `json:"canDecide"`
	// Whether a review was handed on to the roles its process escalates
	// to, which handledBy then includes; see notify.go.
	Escalated bool `json:"escalated,omitempty"`
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
	Usage *stepUsage `json:"usage,omitempty"`
	// For an agent, script or connector step: how long it waited for its
	// slots before it started, in milliseconds, and which try of its process
	// it is, from 1; null for other steps and those from before they were
	// recorded. A step waiting for its slots isn't recorded until it has them.
	SlotWaitMS *int64     `json:"slotWaitMs"`
	Try        *int64     `json:"try"`
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
	return s == stepActive || s == stepWaiting || s == stepDone || s == stepError || s == stepSkipped
}

// Starts a step of a run, copying the process's settings on the run's board
// into it.
func startStep(ctx context.Context, db *sql.DB, b *runBoard, runID, processID int64, status, prompt string, inputs []runInput, cr *changeRequest) (int64, error) {
	return startStepAs(ctx, db, b, runID, processID, "", status, prompt, inputs, cr)
}

// Starts a step as startStep does, of kind rather than its process's, unless
// empty. A process the board as it is no longer has, as when the run is on
// the version published, is referred to by process_ref alone.
func startStepAs(ctx context.Context, db *sql.DB, b *runBoard, runID, processID int64, kind, status, prompt string, inputs []runInput, cr *changeRequest) (int64, error) {
	p, err := b.process(processID)
	if err != nil {
		return 0, err
	}
	if kind == "" {
		kind = p.Kind
	}
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
	// Only into a run this replica walks: one another took over (see
	// resume.go) is that one's to go on with.
	res, err := db.ExecContext(ctx,
		`INSERT INTO run_steps (run_id, process_id, process_ref, process_name, kind, model, thinking, prompt, inputs, change_request, status, output, error, instructions)
		SELECT id, (SELECT id FROM processes WHERE id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '' FROM runs WHERE id = ? AND replica_id = ?`,
		p.ID, p.ID, p.Name, kind, p.Model, p.Thinking, prompt, in, crJSON, status, runID, replicaID)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err == nil {
			err = errNotWalked
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err == nil {
		logStepStarted(db, id, status)
	}
	return id, err
}

var errNotFound = errors.New("not found")

// What recording a step of a run this replica doesn't walk fails with:
// another replica took it over; see resume.go.
var errNotWalked = errors.New("another replica walks the run")

// Finishes a step. A successful step generates its process's output objects
// on the run's board, each in the state its link names; a human step those
// it fills in. Objects with attributes hold their part of the output, given
// in contents by object id; the others hold all of it.
//
// While the run goes on, persistent objects are stored too, as are the
// records of shared ones, unless the run is a trial. If one holds a unique
// value another run stored, nothing is stored, the step ends as a duplicate
// and its run as terminated; the duplicate is returned.
func finishStep(ctx context.Context, db *sql.DB, b *runBoard, stepID int64, status, output, errMsg string, contents map[int64]string) (*duplicate, error) {
	// Parallel steps storing values can still deadlock; the loser rolled
	// back everything, so it can go again.
	for attempt := 1; ; attempt++ {
		dup, err := finishStepOnce(ctx, db, b, stepID, status, output, errMsg, contents)
		if !mysqlErrorIs(err, mysqlErrDeadlock) || attempt == 3 {
			if err != nil {
				stepLog(db, stepID, logError, "step %d couldn't be recorded as ended: %v", stepID, err)
			} else {
				logStepFinished(db, stepID, false)
			}
			return dup, err
		}
	}
}

// Read committed takes no gap locks, which would make parallel runs
// replacing their own instances deadlock on the same empty range.
func finishStepOnce(ctx context.Context, db *sql.DB, b *runBoard, stepID int64, status, output, errMsg string, contents map[int64]string) (*duplicate, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Locking the run orders this against a parallel step terminating it.
	var runID int64
	var runStatus, kind string
	var process sql.NullInt64
	var trial bool
	var walker sql.NullString
	err = tx.QueryRowContext(ctx,
		"SELECT r.id, r.status, s.kind, s.process_ref, r.trial_of IS NOT NULL, r.replica_id FROM run_steps s JOIN runs r ON r.id = s.run_id WHERE s.id = ? FOR UPDATE", stepID).
		Scan(&runID, &runStatus, &kind, &process, &trial, &walker)
	if err != nil {
		return nil, err
	}
	// A step of a run another replica took over is that one's to end, as it
	// tries it again or fails it; what this one got of it is dropped. See
	// resume.go.
	if runStatus == runRunning && walker.String != replicaID {
		return nil, errNotWalked
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
	if process.Valid {
		for _, l := range b.outputLinks(process.Int64, kind) {
			content, ok := contents[l.Object]
			if !ok {
				content = output
			}
			if err := insertRunObject(ctx, tx, runID, stepID, l.Object, b.objects[l.Object].Name, b.stateName(l.State), content); err != nil {
				return nil, err
			}
		}
	}
	// A step finishing after its run ended stores nothing, nor does a flow
	// process's: the processes inside it stored what it generates. Nor does
	// a trial's, which real runs mustn't see; see trials.go.
	if runStatus != runRunning || kind == kindFlow || trial {
		return nil, tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, "SAVEPOINT store"); err != nil {
		return nil, err
	}
	dup, err := storeInstances(ctx, tx, b, runID, stepID)
	if err != nil {
		return nil, err
	}
	if dup == nil {
		if err := storeShared(ctx, tx, b, runID, stepID); err != nil {
			return nil, err
		}
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

// Records what a run holds of an object, as a step generated it or a person
// decided it. One the board as it is no longer has is referred to by
// object_ref alone.
func insertRunObject(ctx context.Context, db execer, run, step, object int64, name, state, content string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO run_objects (run_id, step_id, object_id, object_ref, name, state, content)
		VALUES (?, ?, (SELECT id FROM objects WHERE id = ?), ?, ?, ?, ?)`,
		run, step, object, object, name, state, content)
	return err
}

// Records the state a human step set its object to; the object keeps its content.
func recordDecision(ctx context.Context, db *sql.DB, b *runBoard, stepID, stateID int64, content string) error {
	st, ok := b.states[stateID]
	if !ok {
		return nil
	}
	var run int64
	if err := db.QueryRowContext(ctx, "SELECT run_id FROM run_steps WHERE id = ?", stepID).Scan(&run); err != nil {
		return err
	}
	return insertRunObject(ctx, db, run, stepID, st.Object, b.objects[st.Object].Name, st.Name, content)
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
	return endedRun(ctx, db, id)
}

// Ends a run as endRun does once its walk here is over, unless another
// replica took it over meanwhile, as when this one was taken for stopped:
// that one goes on with it, so it is left as it is, with errNotWalked.
func endWalkedRun(ctx context.Context, db *sql.DB, id int64, status string) (run, error) {
	res, err := db.ExecContext(ctx,
		"UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ? AND status = ? AND replica_id = ?",
		status, id, runRunning, replicaID)
	if err != nil {
		return run{}, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return run{}, err
		}
		var walker sql.NullString
		var going bool
		if err := db.QueryRowContext(ctx, "SELECT replica_id, status = ? FROM runs WHERE id = ?", runRunning, id).Scan(&walker, &going); err != nil {
			return run{}, err
		}
		if going && walker.String != replicaID {
			return run{}, errNotWalked
		}
	}
	return endedRun(ctx, db, id)
}

// Closes what a run that ended left open, and releases what it stored
// unless it ended done.
func endedRun(ctx context.Context, db *sql.DB, id int64) (run, error) {
	// Steps left open, such as a review nobody decided, ended with the run.
	_, err := db.ExecContext(ctx,
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

// Starts a run of the whole flow, for schedule if one started it, or the
// user who did, on its board as it is, or with published on the version it
// published if any; see newRun. The run goes on in the server; this returns
// its id once it started, or sql.ErrNoRows without the flow.
func startRun(ctx context.Context, db *sql.DB, eng *engine, flow int64, schedule, startedBy *int64, published bool) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	nr, err := newRun(ctx, tx, flow, schedule, startedBy, published)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	eng.start(nr, flow, nil)
	return nr.id, nil
}

// A run recorded, and what the engine needs to execute it.
type pendingRun struct {
	id, version int64
	board       snapshot
	repos       []repository
	prefix      string
	// The shared record it is started on, 0 for none; see record_runs.go.
	record int64
	// Whether it runs the version the flow published; see publish.go.
	published bool
}

// Records a run of the flow on its board as it is, or with published on the
// version the flow published if it has one; see runVersion. A board changed
// since its version is saved as a new one first, so the run's version shows
// what it ran. The flow's settings, its repositories, are the version's.
// sql.ErrNoRows without the flow, or with it in the trash, whose moving
// there waits for the run to be recorded, to stop it (see trash.go).
func newRun(ctx context.Context, tx *sql.Tx, flow int64, schedule, startedBy *int64, published bool) (pendingRun, error) {
	var name string
	var settings flowSettings
	if err := tx.QueryRowContext(ctx, "SELECT name, settings FROM flows WHERE id = ? AND deleted_at IS NULL FOR UPDATE", flow).Scan(&name, &settings); err != nil {
		return pendingRun{}, err
	}
	version, board, onPublished, err := runVersion(ctx, tx, flow, published, saving{by: startedBy, reason: savedForRun})
	if err != nil {
		return pendingRun{}, err
	}
	if board.Settings != nil {
		settings = *board.Settings
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO runs (flow_id, status, version_id, schedule_id, started_by, replica_id, published) VALUES (?, ?, ?, ?, ?, ?, ?)",
		flow, runRunning, version, schedule, startedBy, replicaID, onPublished)
	if err != nil {
		return pendingRun{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return pendingRun{}, err
	}
	return pendingRun{id: id, version: version, board: board, repos: settings.Repositories, prefix: settings.prefix(name), published: onPublished}, nil
}

func registerRunRoutes(mux *http.ServeMux, db *sql.DB, eng *engine) {
	// The flow's runs, newest first, a page at a time, narrowed by status and
	// how they started; see run_list.go.
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		filter, msg := parseRunFilter(r.URL.Query())
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		where, args := filter.query(f)
		rows, err := db.QueryContext(r.Context(), "SELECT "+runColumns+" FROM runs"+where, args...)
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

	// Starts a run on the board as it is, or with {"published": true} on the
	// version the flow published, if any; see startRun. With inputs, it is
	// given what it starts from, as a run over the API is (startGivenRun).
	// One with errors is refused, saying them.
	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok || refuseInStruct(w, r, db, f) {
			return
		}
		var body struct {
			Published bool `json:"published"`
			// What the flow starts from, by name, and the states of any of
			// it, given by hand from Run's menu as a request to /v1 gives
			// them; see api.go. Without either the run starts from nothing.
			Inputs map[string]json.RawMessage `json:"inputs"`
			States map[string]string          `json:"states"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBody)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		// A board whose errors a run can't get past isn't run; see problems.go.
		msg, err := refuseRun(r.Context(), db, r, f, body.Published)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusConflict)
			return
		}
		var id int64
		if body.Inputs != nil || body.States != nil {
			var refused *apiRefusal
			id, _, refused, err = startGivenRun(r.Context(), db, eng, f, handSource, requestUserID(r), "", body.Published,
				func([]givenObject) (apiRunRequest, error) {
					return apiRunRequest{Inputs: body.Inputs, States: body.States}, nil
				})
			if err == nil && refused != nil {
				http.Error(w, refused.msg, refused.status)
				return
			}
		} else {
			id, err = startRun(r.Context(), db, eng, f, nil, requestUserID(r), body.Published)
		}
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
			// What it spent on its models through the model proxy, its
			// subflows' and items' runs with it, and the most it may, 0
			// for no limit, in dollars; see spend.go.
			Spent      float64 `json:"spent"`
			SpendLimit float64 `json:"spendLimit"`
			// Each time it moved to another replica, as the one walking
			// it stopped; see adopt.go.
			Moves []runMove `json:"moves"`
		}{run: rn, Steps: []step{}, Objects: []runObject{}}
		if detail.Spent, detail.SpendLimit, err = runSpent(r.Context(), db, id); err != nil {
			serverError(w, err)
			return
		}
		if detail.Moves, err = runMoves(r.Context(), db, id); err != nil {
			serverError(w, err)
			return
		}

		rows, err := db.QueryContext(r.Context(),
			`SELECT id, COALESCE(process_ref, process_id), process_name, kind, model, thinking, prompt, inputs, change_request,
				status, output, error, outcome, instructions, `+userLabelColumn("decided_by")+`, child_run_id, branch, commits, COALESCE(JSON_UNQUOTE(JSON_EXTRACT(session, '$.id')), ''), slot_wait_ms, try, started_at, finished_at, escalated
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
				&s.Status, &s.Output, &s.Error, &s.Outcome, &s.Instructions, &s.DecidedBy, &s.ChildRun, &s.Branch, &commits, &s.Session, &s.SlotWaitMS, &s.Try, &s.StartedAt, &s.FinishedAt, &s.Escalated); err != nil {
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
		// Who decides a review is the run's board's to say.
		var b *runBoard
		access, err := flowAccess(r.Context(), db, requestUser(r), rn.Flow)
		if err != nil {
			serverError(w, err)
			return
		}
		for i, s := range detail.Steps {
			if s.Kind != kindHuman && s.Kind != kindLoop {
				continue
			}
			if s.Kind == kindHuman && s.Process != nil {
				if b == nil {
					if b, err = runBoardOf(r.Context(), db, id); err != nil {
						serverError(w, err)
						return
					}
				}
				detail.Steps[i].HandledBy = b.deciders(*s.Process, s.Escalated)
			}
			detail.Steps[i].CanDecide = decidesStep(requestUser(r), roles, access, s.Kind, detail.Steps[i].HandledBy)
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

	// Stops an agent, script or connector step still going. It fails, which ends
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

		// A step whose process was deleted before ids were kept fills in nothing.
		var process sql.NullInt64
		var escalated bool
		err = db.QueryRowContext(r.Context(), "SELECT process_ref, escalated FROM run_steps WHERE id = ? AND run_id = ?", id, runID).Scan(&process, &escalated)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no review waiting with that id in this run", http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		// The review is of the process as the run's board has it.
		b, err := runBoardOf(r.Context(), db, runID)
		if err != nil {
			serverError(w, err)
			return
		}
		var outcome string
		if patch.State != nil {
			if outcome = b.decisionState(process.Int64, *patch.State); outcome == "" {
				http.Error(w, "the step's process can't decide on that state", http.StatusBadRequest)
				return
			}
		}
		if msg, err := refuseDecider(r.Context(), db, r, b.deciders(process.Int64, escalated)); err != nil || msg != "" {
			if err != nil {
				serverError(w, err)
			} else {
				http.Error(w, msg, http.StatusForbidden)
			}
			return
		}
		objects, msg := b.entryObjects(process.Int64)
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
		// The step keeps the decision whole until the run's walk recorded
		// what follows from it, so a replica taking the run over carries it
		// on even if the one walking it stopped before it got it; see
		// resume.go.
		d := decision{state: patch.State, instructions: patch.Instructions, output: output, entries: contents}
		kept, err := json.Marshal(d.command())
		if err != nil {
			serverError(w, err)
			return
		}
		// Only a review of this run still waiting; deciding one twice would
		// let the run go on twice.
		res, err := db.ExecContext(r.Context(),
			`UPDATE run_steps SET status = ?, outcome = ?, instructions = ?, decided_by = ?, decision = ?, finished_at = CURRENT_TIMESTAMP(3)
			WHERE id = ? AND run_id = ? AND kind = ? AND status = ?`,
			stepDone, outcome, patch.Instructions, requestUserID(r), kept, id, runID, kindHuman, stepWaiting)
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
		logStepFinished(db, id, true)
		// The run records the object in the state set, with the content it has there.
		eng.decide(runID, id, d)
		w.WriteHeader(http.StatusNoContent)
	})
}
