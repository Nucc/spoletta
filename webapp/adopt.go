package main

// Runs taken over. A run executes in the replica that started it (see
// replicas.go); when that replica stops, as for a deploy or a crash, a
// replica still alive takes over each run it walked and carries it on, so a
// review waiting three days for a person outlives the next deploy. The run
// is claimed once, by a conditional update of runs.replica_id, with the runs
// of its flow processes and subflows still going; RESUME_RUNS=false ends
// them as interrupted instead, as before. Trials are ended so too, as whoever
// tried the process is watching it and can try it again; so are runs from
// before versions were recorded, which have no board of their own to walk.
//
// A run keeps nothing of its walk but what it stored, so the replica taking
// it over walks its version's board again from the start, as the replica
// that started it did, and takes up each step the run stored as it comes to
// it instead of running it: matched by process, in the order they started,
// one that took the same inputs first, as branches going at once may have
// started theirs in another order. That rebuilds every frontier the run had,
// on its parallel branches, items, flow processes' boards and subflows, with
// the passes, rounds and loops they went through, from the steps and object
// values already stored. What is recorded of a step taken up isn't recorded
// again, nor are people told again.
//
// Each step taken up goes as it went: one that ended ended so; a review or
// a loop's question still waiting waits again, the same step, with its
// reminders and time limit going on as they were (notify.go), and one
// decided while no replica walked the run is taken in as decided, from the
// decision its step kept. An agent, script or connector step that was going
// is lost, as the worker running it ends it once the replica that asked for
// it is gone: it fails, saying so, and goes again on what it took as a try of
// its own if its retries let it, or else fails as any step does, so a failure
// connection can handle it. Past what it stored, the walk goes on as if the
// run had never moved. A connector step that was going may have done what it
// does already, so going again may do it twice.
//
// The replica that stopped may only have been paused, as when it was taken
// for stopped after a long pause: until it notices and exits, what it still
// records of the run is refused (see errNotWalked in runs.go), so the steps
// the run goes on with here win.
//
// Each run taken over says so in its log, from which replica to which, and
// what it lost and tried again or failed, and keeps the move in run_moves,
// which its panel shows.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Whether runs a stopped replica walked are taken over, RESUME_RUNS, true
// unless it says false.
var resumeRuns = true

func resumingRuns() (bool, error) {
	switch v := strings.ToLower(env("RESUME_RUNS", "true")); v {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("RESUME_RUNS must be true or false; it is %q", v)
	}
}

// The engine taking over the runs of replicas that stopped, once this
// replica listens, as a step going again calls its proxies; until then
// they wait, as they would for any replica to take them. And whether it
// is taking them over now.
var (
	adopter  atomic.Pointer[engine]
	adopting atomic.Bool
)

// Takes over the runs of replicas that stopped as adoptStopped does, in
// the background and once at a time, so this replica saying it is alive
// never waits for it, however many runs there are; those it doesn't get
// to are taken the next time.
func (e *engine) adoptInBackground() {
	if !adopting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer adopting.Store(false)
		if err := e.adoptStopped(context.Background()); err != nil {
			log.Printf("WARNING: couldn't take over the runs of replicas that stopped: %v", err)
		}
	}()
}

// What a step goes on with when the replica walking its run stopped while
// it went.
const stepLost = "The server running it stopped before it finished, so what it did was lost."

// A step a run stored before it moved, as its walk, rebuilt, takes it up.
type pastStep struct {
	id, process  int64
	kind, status string
	inputs       []runInput
	cr           *changeRequest
	output       string
	err          string
	outcome      string
	instructions string
	// A person's decision not taken in yet; see the decide route.
	decision *decisionCommand
	// For a flow process's or subflow's step: the run it ran as.
	child *int64
	// What it generated, or the person decided.
	objects []pastObject
	taken   bool
}

type pastObject struct {
	object                       int64
	state, content, instructions string
}

// What the step recorded of the object, nil for nothing.
func (s *pastStep) object(o int64) *pastObject {
	for i := len(s.objects) - 1; i >= 0; i-- {
		if s.objects[i].object == o {
			return &s.objects[i]
		}
	}
	return nil
}

// How the run of a flow process's step that ended had ended.
func (s *pastStep) ended() string {
	switch {
	case s.status == stepDone:
		return runDone
	case s.status == stepDuplicate:
		return runTerminated
	case strings.HasSuffix(s.err, stoppedInside):
		return runInterrupted
	}
	return runFailed
}

// What ends the error of a flow process's step whose board was stopped.
const stoppedInside = " was stopped."

// The steps of one run stored before it moved, in the order they started.
type pastSteps struct {
	mu    sync.Mutex
	steps []*pastStep
}

// Takes up the first step of the process not taken up yet that fits,
// preferring one that took inputs; nil for none, as for a run walked here
// from its start.
func (ps *pastSteps) take(process int64, fits func(*pastStep) bool, inputs []runInput) *pastStep {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var first *pastStep
	for _, s := range ps.steps {
		if s.taken || s.process != process || !fits(s) {
			continue
		}
		if slices.Equal(s.inputs, inputs) {
			s.taken = true
			return s
		}
		if first == nil {
			first = s
		}
	}
	if first != nil {
		first.taken = true
	}
	return first
}

// The step take would take up first, without taking it.
func (ps *pastSteps) peek(process int64, fits func(*pastStep) bool) *pastStep {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, s := range ps.steps {
		if !s.taken && s.process == process && fits(s) {
			return s
		}
	}
	return nil
}

// The steps going or waiting not taken up, by id.
func (ps *pastSteps) open() []int64 {
	if ps == nil {
		return nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var open []int64
	for _, s := range ps.steps {
		if !s.taken && (s.status == stepActive || s.status == stepWaiting) {
			open = append(open, s.id)
		}
	}
	return open
}

// Which steps each part of the walk takes up: an agent's, script's or
// connector's that ran or was going; a flow process's or subflow's; a
// review or a loop's question; a step saying why a process ran nothing.
func ranStep(s *pastStep) bool {
	return (s.kind == kindAgent || s.kind == kindScript || s.kind == kindConnector) && s.status != stepSkipped && s.status != stepWaiting
}

func flowStep(s *pastStep) bool {
	return (s.kind == kindFlow || s.kind == kindSubflow) && s.status != stepSkipped
}

func reviewed(loop bool) func(*pastStep) bool {
	return func(s *pastStep) bool {
		if loop {
			return s.kind == kindLoop
		}
		return s.kind == kindHuman && (s.status == stepWaiting || s.status == stepDone || s.status == stepError)
	}
}

func noted(status string) func(*pastStep) bool {
	return func(s *pastStep) bool { return s.kind != kindLoop && s.status == status }
}

// The steps a walk of the run takes up, nil for none.
func (w *walk) past() *pastSteps {
	if w.lr == nil {
		return nil
	}
	return w.lr.past
}

const pastColumns = "id, process_ref, kind, status, inputs, change_request, output, error, outcome, instructions, decision, child_run_id"

func scanPast(row interface{ Scan(...any) error }) (*pastStep, error) {
	s := &pastStep{}
	var inputs, cr, decided []byte
	if err := row.Scan(&s.id, &s.process, &s.kind, &s.status, &inputs, &cr, &s.output, &s.err, &s.outcome, &s.instructions, &decided, &s.child); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(inputs, &s.inputs); err != nil {
		return nil, err
	}
	if s.inputs == nil {
		s.inputs = []runInput{}
	}
	if cr != nil {
		if err := json.Unmarshal(cr, &s.cr); err != nil {
			return nil, err
		}
	}
	if decided != nil {
		if err := json.Unmarshal(decided, &s.decision); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// The steps the run stored of its processes, with what they generated; the
// steps holding what it was given or copied aren't a process's.
func loadPast(ctx context.Context, db *sql.DB, run int64) (*pastSteps, error) {
	ps := &pastSteps{}
	byID := map[int64]*pastStep{}
	err := scanAll(ctx, db, "SELECT "+pastColumns+" FROM run_steps WHERE run_id = ? AND process_ref IS NOT NULL AND kind NOT IN (?, ?) ORDER BY id",
		func(rows *sql.Rows) error {
			s, err := scanPast(rows)
			if err == nil {
				ps.steps = append(ps.steps, s)
				byID[s.id] = s
			}
			return err
		}, run, kindCopied, kindInput)
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, "SELECT step_id, object_ref, state, content, COALESCE(instructions, '') FROM run_objects WHERE run_id = ? AND object_ref IS NOT NULL ORDER BY id",
		func(rows *sql.Rows) error {
			var step int64
			var o pastObject
			if err := rows.Scan(&step, &o.object, &o.state, &o.content, &o.instructions); err != nil {
				return err
			}
			if s := byID[step]; s != nil {
				s.objects = append(s.objects, o)
			}
			return nil
		}, run)
	return ps, err
}

// One step as it is stored now, with what it generated.
func loadStep(ctx context.Context, db *sql.DB, id int64) (*pastStep, error) {
	s, err := scanPast(db.QueryRowContext(ctx, "SELECT "+pastColumns+" FROM run_steps WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, "SELECT object_ref, state, content, COALESCE(instructions, '') FROM run_objects WHERE step_id = ? AND object_ref IS NOT NULL ORDER BY id",
		func(rows *sql.Rows) error {
			var o pastObject
			err := rows.Scan(&o.object, &o.state, &o.content, &o.instructions)
			s.objects = append(s.objects, o)
			return err
		}, id)
	return s, err
}

// How an agent, script or connector step taken up went: done or a
// duplicate, with what it generated; failed, or stopped by a person; or,
// going when the run moved, lost, and tried again if retry says so.
func (w *walk) again(s *pastStep, retry bool) (runResult, bool, bool, int64) {
	switch s.status {
	case stepDone:
		return runResult{OK: true, Output: s.output, past: s}, true, false, s.id
	case stepDuplicate:
		return runResult{OK: true, Output: s.output, past: s, Duplicate: &duplicate{}}, true, false, s.id
	case stepActive:
		return runResult{Error: w.lose(s, retry)}, false, false, s.id
	}
	return runResult{Error: s.err}, false, strings.HasPrefix(s.err, "it was stopped"), s.id
}

// What an agent, script or connector step taken up generated, each object
// in the state it came out in, with the instructions an agent left with
// what it decided on.
func (w *walk) pastValues(id int64, s *pastStep) map[int64]value {
	out := map[int64]value{}
	for _, end := range w.outputs[id] {
		v := value{content: s.output, state: end.state}
		if o := s.object(end.object); o != nil {
			v.content, v.state = o.content, w.stateNamed(end.object, o.state)
			if o.instructions != "" {
				v.review = &review{reviewer: w.names[id], instructions: o.instructions}
			}
		}
		out[end.object] = v
	}
	return out
}

// Ends a step that was going when the replica walking its run stopped as
// failed, saying so, and says whether its process goes again; returns why
// it failed.
func (w *walk) lose(s *pastStep, retry bool) string {
	ctx := context.Background()
	db := w.e.db
	if _, err := db.ExecContext(ctx, "UPDATE run_steps SET status = ?, error = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ? AND status = ?",
		stepError, stepLost, s.id, stepActive); err != nil {
		log.Printf("record step %d as lost: %v", s.id, err)
	}
	logStepFinished(db, s.id, false)
	name := strconv.Quote(w.names[s.process])
	if retry {
		runLog(db, w.run, s.id, logInfo, "%s was lost, going as the server running it stopped; it goes again", name)
	} else {
		runLog(db, w.run, s.id, logError, "%s was lost, going as the server running it stopped; it has no tries left, so it failed", name)
	}
	retried := "false"
	if retry {
		retried = "true"
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE run_moves SET lost = JSON_ARRAY_APPEND(COALESCE(lost, JSON_ARRAY()), '$', JSON_OBJECT('step', ?, 'process', ?, 'retried', CAST(? AS JSON)))
		WHERE run_id = ? ORDER BY id DESC LIMIT 1`, s.id, w.names[s.process], retried, w.run); err != nil {
		log.Printf("record step %d as lost in run %d's move: %v", s.id, w.run, err)
	}
	return stepLost
}

// Waits for a decision of the process, as await does: a review, or with a
// question, a loop step asking it. In a run taken over, one it stored is
// taken up instead.
func (w *walk) review(id int64, given []runInput, question string) (int64, decision, bool) {
	if s := w.past().take(id, reviewed(question != ""), given); s != nil {
		return w.reviewAgain(id, s)
	}
	if question != "" {
		runLog(w.e.db, w.run, 0, logInfo, "%s", question)
	}
	return w.e.await(w.reviews, w.lr, w.fr.rb, w.run, id, given, question)
}

// A review, or a loop's question, the run stored before it moved: still
// waiting, it waits again, for whoever decides it now; decided or failed
// meanwhile, as it was. Its step is decided wherever the person is, so it
// is read again once this replica listens for its decision, which another
// may have recorded while nobody walked the run.
func (w *walk) reviewAgain(id int64, s *pastStep) (int64, decision, bool) {
	if s.status == stepWaiting {
		ch := make(chan decision, 1)
		w.e.mu.Lock()
		w.lr.reviews[s.id] = ch
		w.e.mu.Unlock()
		now, err := loadStep(context.Background(), w.e.db, s.id)
		if err != nil {
			runLog(w.e.db, w.run, s.id, logError, "step %d couldn't be read again: %v", s.id, err)
		}
		if err == nil && now.status != stepWaiting {
			w.e.mu.Lock()
			mine := w.lr.reviews[s.id] == ch
			if mine {
				delete(w.lr.reviews, s.id)
			}
			w.e.mu.Unlock()
			// Otherwise its decision came here as it was read.
			if mine {
				return s.id, w.decided(id, now), true
			}
		} else if err == nil {
			// Telling people again is claimed once, so only those the
			// replica that stopped hadn't told yet are told.
			go w.e.notify.waiting(w.run, s.id)
		}
		select {
		case d := <-ch:
			return s.id, d, true
		case <-w.reviews.Done():
			return s.id, decision{}, false
		}
	}
	return s.id, w.decided(id, s), true
}

// The decision of a review or a loop's question that ended: one a person
// made that the walk hadn't taken in comes as they made it, and what
// follows from it is recorded now; otherwise as it was taken in, with what
// followed from it as recorded then.
func (w *walk) decided(id int64, s *pastStep) decision {
	switch {
	case s.kind == kindLoop:
		return decision{goOn: s.status == stepDone}
	case s.status == stepError:
		return decision{expired: orElse(s.err, "It failed.")}
	case s.decision != nil:
		return s.decision.decision()
	}
	d := decision{instructions: s.instructions, output: s.output, past: s}
	for _, end := range w.outputs[id] {
		if end.state != 0 && s.outcome != "" && w.stateNames[end.state] == s.outcome {
			st := end.state
			d.state = &st
			break
		}
	}
	for _, end := range w.outputs[id] {
		if o := s.object(end.object); end.state == 0 && o != nil {
			if d.entries == nil {
				d.entries = map[int64]string{}
			}
			d.entries[end.object] = o.content
		}
	}
	return d
}

// The string, or otherwise when it is empty.
func orElse(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// The processes that run with what a person entered on a review the run
// took in before it moved, each object in the state it came out in.
func (w *walk) enterPast(p *pass, d decision) []func() {
	if w.stopped() {
		return nil
	}
	objects := sortedKeys(d.entries)
	entered := map[int64]value{}
	for _, o := range objects {
		v := value{content: d.entries[o]}
		if po := d.past.object(o); po != nil {
			v.state = w.stateNamed(o, po.state)
		}
		entered[o] = v
	}
	return w.goOnWith(p, objects, entered)
}

// Registers the run a flow process or subflow ran as, in a run taken
// over, with the steps it stored, as register does a run starting; and how
// it ended, if it had before the run moved, "" while it goes. One that
// ended is walked again only to rebuild what came of it.
func (e *engine) registerAgain(run int64, cancel context.CancelFunc) (*liveRun, string, error) {
	ctx := context.Background()
	var status string
	if err := e.db.QueryRowContext(ctx, "SELECT status FROM runs WHERE id = ?", run).Scan(&status); err != nil {
		return nil, "", err
	}
	past, err := loadPast(ctx, e.db, run)
	if err != nil {
		return nil, "", err
	}
	lr := e.register(run, cancel)
	lr.past = past
	if status == runRunning {
		status = ""
	}
	return lr, status, nil
}

// Drops a run registered only to walk it again.
func (e *engine) forget(run int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.runs, run)
}

// How the run walks: its flow, on the version it took, with that version's
// repositories and the branches' prefix it started with.
func flowRunOf(ctx context.Context, db *sql.DB, id int64) (*flowRun, error) {
	var flow, version int64
	var name string
	var settings flowSettings
	var body []byte
	var published bool
	err := db.QueryRowContext(ctx, `SELECT r.flow_id, v.id, f.name, f.settings, v.snapshot, r.published FROM runs r
		JOIN flows f ON f.id = r.flow_id JOIN flow_versions v ON v.id = r.version_id WHERE r.id = ?`, id).
		Scan(&flow, &version, &name, &settings, &body, &published)
	if err != nil {
		return nil, err
	}
	var board snapshot
	if err := json.Unmarshal(body, &board); err != nil {
		return nil, err
	}
	if board.Settings != nil {
		settings = *board.Settings
	}
	// The flow may have been renamed since, which names its branches by
	// default; the run's go on as they started.
	prefix := settings.prefix(name)
	suffix := "/run-" + strconv.FormatInt(id, 10)
	var branch string
	err = db.QueryRowContext(ctx, "SELECT branch FROM run_steps WHERE run_id = ? AND RIGHT(branch, ?) = ? ORDER BY id LIMIT 1", id, len(suffix), suffix).Scan(&branch)
	if err == nil {
		prefix = strings.TrimSuffix(branch, suffix)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	fr := newFlowRun(flow, version, board, settings.Repositories, prefix)
	fr.published = published
	return fr, nil
}

// The flow a subflow process ran as the run child, on the version that run
// took, as call made it.
func (fr *flowRun) calledAs(ctx context.Context, db *sql.DB, child int64) (*flowRun, error) {
	callee, err := flowRunOf(ctx, db, child)
	if err != nil {
		return nil, err
	}
	callee.callers = append(slices.Clone(fr.callers), fr.flow)
	callee.published = fr.published
	return callee, nil
}

// A persistent object a run carried in, as runs.carried keeps it: what the
// latest run to end done stored of it then, or nil before any did.
type carriedValue struct {
	Content string `json:"content"`
	State   string `json:"state"`
}

// Starts the walk with what the run carried in before it moved, or for a
// run from before that was kept, as carry does.
func (w *walk) carryAgain(carried []byte) error {
	if carried == nil {
		return w.carry()
	}
	var values map[int64]*carriedValue
	if err := json.Unmarshal(carried, &values); err != nil {
		return err
	}
	w.carryIn(values)
	return nil
}

// Takes over, once, each run walked by a replica that stopped, with the
// runs going inside it, and carries it on; see the top of the file.
func (e *engine) adoptStopped(ctx context.Context) error {
	var roots []int64
	err := scanAll(ctx, e.db, `SELECT id FROM runs WHERE status = ? AND `+orphaned("replica_id")+`
		AND (parent_run_id IS NULL OR rerun_of IS NOT NULL) AND trial_of IS NULL AND version_id IS NOT NULL ORDER BY id`,
		func(rows *sql.Rows) error {
			var id int64
			err := rows.Scan(&id)
			roots = append(roots, id)
			return err
		}, runRunning)
	if err != nil {
		return err
	}
	for _, id := range roots {
		claimed, err := claimRun(ctx, e.db, id)
		if err != nil {
			return err
		}
		if claimed {
			e.carryOn(id)
		}
	}
	return nil
}

// Claims a run going that no replica alive walks, with the runs going
// inside it, for this one, and records each moving here; false if another
// replica claimed it first.
func claimRun(ctx context.Context, db *sql.DB, id int64) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var from sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT replica_id FROM runs WHERE id = ? FOR UPDATE", id).Scan(&from); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, "UPDATE runs SET replica_id = ? WHERE id = ? AND status = ? AND "+orphaned("replica_id"), replicaID, id, runRunning)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	inside, err := runsInside(ctx, tx, id)
	if err != nil {
		return false, err
	}
	runs := append([]int64{id}, inside...)
	if len(runs) > 1 {
		if _, err := tx.ExecContext(ctx, "UPDATE runs SET replica_id = ? WHERE id IN "+placeholders(len(runs)-1),
			append([]any{replicaID}, int64Args(runs[1:])...)...); err != nil {
			return false, err
		}
	}
	for _, r := range runs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO run_moves (run_id, from_replica, to_replica, host) VALUES (?, ?, ?, ?)",
			r, from, replicaID, truncate(replicaHost, 255)); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	was := "a replica from before replicas had ids"
	if from.Valid {
		was = "replica " + from.String
	}
	for _, r := range runs {
		runLog(db, r, 0, logInfo, "taken over by replica %s on %s, as %s walking it stopped; it goes on from what it stored", replicaID, replicaHost, was)
	}
	log.Printf("took over run %d, and %d runs inside it, from %s, which stopped", id, len(runs)-1, was)
	return true, nil
}

// The runs going inside the run: those of its flow processes and subflows,
// theirs and so on; a re-run of one of them is walked on its own.
func runsInside(ctx context.Context, db rowsQuerier, id int64) ([]int64, error) {
	var runs []int64
	for next := []int64{id}; len(next) > 0; {
		var found []int64
		err := scanAll(ctx, db, "SELECT id FROM runs WHERE parent_run_id IN "+placeholders(len(next))+" AND rerun_of IS NULL AND status = ?",
			func(rows *sql.Rows) error {
				var c int64
				err := rows.Scan(&c)
				found = append(found, c)
				return err
			}, append(int64Args(next), runRunning)...)
		if err != nil {
			return nil, err
		}
		runs, next = append(runs, found...), found
	}
	return runs, nil
}

// Carries on a run of the whole flow, or a re-run, taken over: walks its
// version's board again, as start or rerun did, taking up what it stored,
// in the background, and ends it once nothing in it is left to run.
func (e *engine) carryOn(id int64) {
	go e.walkAgain(id)
}

func (e *engine) walkAgain(id int64) {
	ctx := context.Background()
	// With the runs going inside it, claimed with it.
	end := func() {
		runs, err := runsInside(ctx, e.db, id)
		if err != nil {
			log.Printf("find the runs inside run %d: %v", id, err)
		}
		for _, r := range append(runs, id) {
			if _, err := endRun(ctx, e.db, r, runInterrupted); err != nil {
				log.Printf("interrupt run %d: %v", r, err)
			}
		}
	}
	fail := func(err error) {
		runLog(e.db, id, 0, logError, "couldn't be taken over, so it is interrupted: %v", err)
		end()
	}
	rn, err := getRun(ctx, e.db, id)
	if err != nil {
		fail(err)
		return
	}
	fr, err := flowRunOf(ctx, e.db, id)
	if err != nil {
		fail(err)
		return
	}
	if rn.Record != nil {
		fr.record = *rn.Record
	}
	var carried, starts []byte
	if err := e.db.QueryRowContext(ctx, "SELECT carried, starts FROM runs WHERE id = ?", id).Scan(&carried, &starts); err != nil {
		fail(err)
		return
	}
	if starts != nil {
		if err := json.Unmarshal(starts, &fr.starts); err != nil {
			fail(err)
			return
		}
	}
	past, err := loadPast(ctx, e.db, id)
	if err != nil {
		fail(err)
		return
	}
	// What the run was given from outside, or for a re-run, what it copied
	// and where it starts.
	var board *int64
	var given, values map[int64]value
	if rn.RerunOf != nil {
		board, given, values, err = rerunAgain(ctx, e.db, rn, fr)
	} else {
		given, err = stepValues(ctx, e.db, fr, id, kindInput)
	}
	if err != nil {
		fail(err)
		return
	}
	walking, cancel := context.WithCancelCause(context.Background())
	lr := e.register(id, func() { cancel(nil) })
	lr.failWith = cancel
	lr.past = past
	// A person may have stopped it since it was claimed, which nothing
	// walking it heard of; once registered, stopping it reaches the walk.
	var status string
	if err := e.db.QueryRowContext(ctx, "SELECT status FROM runs WHERE id = ?", id).Scan(&status); err != nil || status != runRunning {
		e.forget(id)
		cancel(nil)
		if err != nil {
			fail(err)
		} else {
			end()
		}
		return
	}
	w := newWalk(walking, e, id, lr, fr, board, given, fr.workspace(id, "run-"+strconv.FormatInt(id, 10)))
	switch {
	case rn.RerunOf != nil && rn.RerunFrom != nil:
		w.resume(*rn.RerunFrom, values)
	case rn.RerunOf != nil:
		w.fail()
	default:
		if err := w.carryAgain(carried); err != nil {
			runLog(e.db, id, 0, logError, "the objects it carries couldn't be carried in: %v", err)
			w.fail()
		} else {
			w.enter()
		}
	}
	e.finish(walking, id, w)
}

// What the run's steps of kind recorded of each object, by object on fr's
// board, each in the state of its name there.
func stepValues(ctx context.Context, db *sql.DB, fr *flowRun, run int64, kind string) (map[int64]value, error) {
	values := map[int64]value{}
	err := scanAll(ctx, db, `SELECT o.object_ref, o.state, o.content FROM run_objects o JOIN run_steps s ON s.id = o.step_id
		WHERE o.run_id = ? AND s.kind = ? AND o.object_ref IS NOT NULL ORDER BY o.id`, func(rows *sql.Rows) error {
		var o int64
		var state, content string
		if err := rows.Scan(&o, &state, &content); err != nil {
			return err
		}
		if _, ok := fr.objects[o]; ok {
			values[o] = value{content: content, state: fr.stateOf(o, state)}
		}
		return nil
	}, run, kind)
	return values, err
}

// What a re-run taken over walks, as its route planned it: the board the
// process it starts from is on, what the run it re-runs was given from
// outside, and what it copied of that run, as the person re-running it put
// it.
func rerunAgain(ctx context.Context, db *sql.DB, rn run, fr *flowRun) (*int64, map[int64]value, map[int64]value, error) {
	values, err := stepValues(ctx, db, fr, rn.ID, kindCopied)
	if err != nil {
		return nil, nil, nil, err
	}
	given := map[int64]value{}
	err = inTx(ctx, db, func(tx *sql.Tx) error {
		var src run
		if err := scanRun(tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE id = ?", *rn.RerunOf), &src); err != nil {
			return err
		}
		states := map[int64]map[string]int64{}
		for _, st := range fr.board.States {
			if states[st.Object] == nil {
				states[st.Object] = map[string]int64{}
			}
			states[st.Object][st.Name] = st.ID
		}
		from, err := givenTo(ctx, tx, src, fr.board, states)
		// What it copied is what it was given, as the person put it.
		for o := range from {
			if v, ok := values[o]; ok {
				given[o] = v
			} else {
				given[o] = from[o]
			}
		}
		return err
	})
	return rn.Process, given, values, err
}

// A time a run moved to another replica, as its panel shows it: from which
// replica to which, on which host, and the steps it lost then, each tried
// again or not.
type runMove struct {
	From    string     `json:"from"`
	To      string     `json:"to"`
	Host    string     `json:"host"`
	Lost    []lostStep `json:"lost"`
	MovedAt time.Time  `json:"movedAt"`
}

type lostStep struct {
	Step    int64  `json:"step"`
	Process string `json:"process"`
	Retried bool   `json:"retried"`
}

// The times the run moved, first first.
func runMoves(ctx context.Context, db *sql.DB, run int64) ([]runMove, error) {
	moves := []runMove{}
	err := scanAll(ctx, db, "SELECT COALESCE(from_replica, ''), to_replica, host, lost, moved_at FROM run_moves WHERE run_id = ? ORDER BY id",
		func(rows *sql.Rows) error {
			var m runMove
			var lost []byte
			if err := rows.Scan(&m.From, &m.To, &m.Host, &lost, &m.MovedAt); err != nil {
				return err
			}
			m.Lost = []lostStep{}
			if lost != nil {
				if err := json.Unmarshal(lost, &m.Lost); err != nil {
					return err
				}
			}
			moves = append(moves, m)
			return nil
		}, run)
	return moves, err
}
