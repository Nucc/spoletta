package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// A person building a flow can try one of its agent, script or connector
// processes on inputs they give it, rather than run the whole flow to see
// whether it works. A trial is a run of the flow on the board as it is in
// which only that process runs, once, on what the person gave each object
// it takes, in the state they gave it, whatever its conditions say: nothing
// before or after it runs, and its failure isn't handled. Its step is a step
// as any other, with its conversation, what it printed, what it changed and
// what it generated, and its run says what it cost.
//
// It really runs: what the process does outside the flow, such as a
// connector posting to Slack or an agent pushing a branch, it does. It works
// on a branch of its own, as any run does; spending limits hold it and its
// spending counts, and the audit log records what it was handed. What real
// runs see stays as it was, though: its steps store no persistent object's
// values, so nothing it generated is taken as handled already, and no value
// carried from run to run moves on (see instances.go); they write no shared
// struct's records (shared.go); and no schedule started it, so no schedule's
// $SINCE moves (schedules.go). It is marked as a trial, by the process it
// tried: the runs list says so, the Data page leaves it out unless its
// trial facet asks for trials (rundata.go), and the flow's last run is the
// last that isn't one. It isn't re-run; the process is tried again.
//
// What a process is tried on can be saved with it as samples, several of
// them, each under a name of its own. They belong to the process as its
// links do, so they are part of the board: versions and history keep them,
// pasting copies them along with the objects they give, and setup files
// have them. Trying a process starts from its first sample, or else from
// the latest of the flow's runs, trials or not, it can start from: one
// that reached it, giving what it took, or one holding what it takes,
// giving that, such as a trial of the process before it; so each process
// can be tried on what the one before it really generated, without running
// what came before again.

// The most samples a process keeps.
const maxSamples = 50

// The most runs trying a process offers to start from, and how many of the
// flow's latest runs it looks through for them.
const (
	maxTrialPast = 20
	trialPastOf  = 500
)

// What a sample gives an object the process takes: what it holds, and the
// state it is in, 0 for none.
type sampleInput struct {
	Object  int64  `json:"object"`
	State   int64  `json:"state,omitempty"`
	Content string `json:"content"`
}

// Stored as JSON.
type sampleInputs []sampleInput

func (s *sampleInputs) Scan(v any) error {
	*s = sampleInputs{}
	switch b := v.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(b, s)
	case string:
		return json.Unmarshal([]byte(b), s)
	}
	return errors.New("sample inputs: unexpected type")
}

func (s sampleInputs) Value() (driver.Value, error) {
	if s == nil {
		s = sampleInputs{}
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// A process's sample as the board keeps it.
type processSample struct {
	ID      int64        `json:"id"`
	Process int64        `json:"process"`
	Name    string       `json:"name"`
	Inputs  sampleInputs `json:"inputs"`
}

// An object the process tried takes: the states it can be given in, and
// the one it is given in unless someone says otherwise, the state its link
// waits for or the first its conditions name, "" for none.
type trialObject struct {
	Object int64    `json:"object"`
	Name   string   `json:"name"`
	States []string `json:"states"`
	State  string   `json:"state"`
}

// What an object is given in a trial, or a sample gives it: its state by
// name, "" for none, and what it holds.
type trialValue struct {
	Object  int64  `json:"object"`
	State   string `json:"state"`
	Content string `json:"content"`
}

type trialSample struct {
	ID     int64        `json:"id"`
	Name   string       `json:"name"`
	Inputs []trialValue `json:"inputs"`
}

// A run of the flow trying the process may start from: one that reached
// it, or else holds an object it takes; and how it went.
type trialPast struct {
	Run   int64 `json:"run"`
	Trial bool  `json:"trial"`
	// The process a trial tried, by name.
	TrialOf   string    `json:"trialOf"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"startedAt"`
	Reached   bool      `json:"reached"`
}

// What trying a process takes, and what it may start from: its samples,
// and the latest runs it may start from, the latest first, whose values
// are read one run at a time.
type trialPlan struct {
	Process     int64         `json:"process"`
	ProcessName string        `json:"processName"`
	Kind        string        `json:"kind"`
	Takes       []trialObject `json:"takes"`
	Samples     []trialSample `json:"samples"`
	Past        []trialPast   `json:"past"`
	// What it does outside the flow, which it does in a trial too.
	Effects []string `json:"effects"`
}

// A process to try, on the board as it is: where it is and what it takes.
type trialTarget struct {
	process process
	flow    int64
	board   snapshot
	w       *walk
	takes   []trialObject
	// Each object's states by name.
	states map[int64]map[string]int64
}

// The process to try on board, the board of flow as it is. Says why it
// can't be tried, if it can't.
func trialTargetOf(ctx context.Context, flow, id int64, board snapshot) (trialTarget, string) {
	t := trialTarget{flow: flow, board: board, states: map[int64]map[string]int64{}}
	i := slices.IndexFunc(board.Processes, func(p process) bool { return p.ID == id })
	if i < 0 {
		return t, "no such process on the flow's board"
	}
	t.process = board.Processes[i]
	if !failingKind(t.process.Kind) {
		return t, "only an agent, script or connector process can be tried on its own"
	}
	for _, st := range board.States {
		if t.states[st.Object] == nil {
			t.states[st.Object] = map[string]int64{}
		}
		t.states[st.Object][st.Name] = st.ID
	}
	t.w = newWalk(ctx, nil, 0, nil, newFlowRun(flow, 0, board, nil, ""), t.process.Parent, nil, nil)
	t.takes = []trialObject{}
	for _, in := range t.w.inputs[id] {
		if slices.ContainsFunc(t.takes, func(o trialObject) bool { return o.Object == in.object }) {
			continue
		}
		o := trialObject{Object: in.object, Name: t.w.objects[in.object], States: []string{}}
		for _, st := range board.States {
			if st.Object == in.object {
				o.States = append(o.States, st.Name)
			}
		}
		switch conds := t.w.conditions[id][in.object]; {
		case in.state != 0:
			o.State = t.w.stateNames[in.state]
		case len(conds) > 0:
			o.State = t.w.stateNames[conds[0]]
		}
		t.takes = append(t.takes, o)
	}
	return t, ""
}

// What values give the objects the process takes, checked: each one it
// takes, in one of its states or none. Says what is wrong, if anything.
func (t trialTarget) values(given []trialValue) (map[int64]value, string) {
	values := map[int64]value{}
	for _, g := range given {
		if !slices.ContainsFunc(t.takes, func(o trialObject) bool { return o.Object == g.Object }) {
			return nil, fmt.Sprintf("%s takes no object %d", strconv.Quote(t.process.Name), g.Object)
		}
		v := value{content: g.Content}
		if g.State != "" {
			if v.state = t.states[g.Object][g.State]; v.state == 0 {
				return nil, fmt.Sprintf("%s has no state %s", strconv.Quote(t.w.objects[g.Object]), strconv.Quote(g.State))
			}
		}
		values[g.Object] = v
	}
	return values, ""
}

// What a sample gives the objects the process takes now, by state name.
func (t trialTarget) sampleValues(inputs sampleInputs) []trialValue {
	names := map[int64]string{}
	for _, st := range t.board.States {
		names[st.ID] = st.Name
	}
	out := []trialValue{}
	for _, in := range inputs {
		if slices.ContainsFunc(t.takes, func(o trialObject) bool { return o.Object == in.Object }) {
			out = append(out, trialValue{Object: in.Object, State: names[in.State], Content: in.Content})
		}
	}
	return out
}

// The process's samples, in the order they were saved.
func (t trialTarget) samples(ctx context.Context, db *sql.Tx) ([]trialSample, error) {
	samples := []trialSample{}
	err := scanAll(ctx, db, "SELECT id, name, inputs FROM process_samples WHERE process_id = ? ORDER BY id", func(rows *sql.Rows) error {
		var s trialSample
		var inputs sampleInputs
		if err := rows.Scan(&s.ID, &s.Name, &inputs); err != nil {
			return err
		}
		s.Inputs = t.sampleValues(inputs)
		samples = append(samples, s)
		return nil
	}, t.process.ID)
	return samples, err
}

// A step of run s that took something, as it took it: neither one a
// re-run copied, which another run's step took first, nor a question of a
// loop's; its arguments are tookArgs'. One that reached the process is
// also of it, with the process's id after them.
const (
	tookStep    = "s.kind NOT IN (?, ?) AND JSON_LENGTH(s.inputs) > 0"
	reachedStep = tookStep + " AND s.process_id = ?"
)

func tookArgs() []any { return []any{kindCopied, kindLoop} }

func (t trialTarget) reachedArgs() []any { return append(tookArgs(), t.process.ID) }

// The latest runs of the flow trying the process may start from, the latest
// first, among the flow's latest trialPastOf: those that reached it, and
// those holding an object it takes, as a step generated it or as one took
// it, given it by a person trying it.
func (t trialTarget) past(ctx context.Context, db rowsQuerier) ([]trialPast, error) {
	reached := "EXISTS (SELECT 1 FROM run_steps s WHERE s.run_id = r.id AND " + reachedStep + ")"
	held, heldArgs := "FALSE", []any{}
	if len(t.takes) > 0 {
		var objects []any
		var names []string
		for _, o := range t.takes {
			objects = append(objects, o.Object)
			names = append(names, "JSON_CONTAINS(s.inputs, JSON_OBJECT('name', ?))")
		}
		held = "EXISTS (SELECT 1 FROM run_objects ro WHERE ro.run_id = r.id AND ro.object_id IN " + placeholders(len(objects)) + ")" +
			" OR EXISTS (SELECT 1 FROM run_steps s WHERE s.run_id = r.id AND " + tookStep + " AND (" + strings.Join(names, " OR ") + "))"
		heldArgs = append(append(objects, tookArgs()...), t.takenNames()...)
	}
	past := []trialPast{}
	args := append(append(append(append(t.reachedArgs(), t.flow, trialPastOf), t.reachedArgs()...), heldArgs...), maxTrialPast)
	err := scanAll(ctx, db, `SELECT r.id, r.trial_of IS NOT NULL, r.trial_of_name, r.status, r.started_at, `+reached+`
		FROM (SELECT id, trial_of, trial_of_name, status, started_at FROM runs WHERE flow_id = ? ORDER BY id DESC LIMIT ?) r
		WHERE `+reached+` OR `+held+` ORDER BY r.id DESC LIMIT ?`, func(rows *sql.Rows) error {
		var p trialPast
		err := rows.Scan(&p.Run, &p.Trial, &p.TrialOf, &p.Status, &p.StartedAt, &p.Reached)
		past = append(past, p)
		return err
	}, args...)
	return past, err
}

// The names of the objects the process takes, as steps' inputs name them.
func (t trialTarget) takenNames() []any {
	names := make([]any, len(t.takes))
	for i, o := range t.takes {
		names[i] = o.Name
	}
	return names
}

// What the run gives the objects the process takes now: what the process
// took the last time the run reached it; or else what the run held of each
// last, as a step generated it, such as the process before it, or else as
// a step took it, given it by a person trying it. What it took that it no
// longer takes, such as a failure it handled or records it queried, is
// left out, and so is a state the object no longer has.
func (t trialTarget) runValues(ctx context.Context, db querier, run int64) ([]trialValue, error) {
	values := map[int64]trialValue{}
	// Gives each object the process takes, and isn't given yet, what the
	// run's latest step on cond took of it; false without such a step.
	took := func(cond string, args ...any) (bool, error) {
		var inputs []byte
		err := db.QueryRowContext(ctx, "SELECT s.inputs FROM run_steps s WHERE s.run_id = ? AND "+cond+" ORDER BY s.id DESC LIMIT 1",
			append([]any{run}, args...)...).Scan(&inputs)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var in []runInput
		if err := json.Unmarshal(inputs, &in); err != nil {
			return false, err
		}
		for _, o := range t.takes {
			i := slices.IndexFunc(in, func(in runInput) bool { return in.Name == o.Name })
			if _, given := values[o.Object]; i >= 0 && !given {
				values[o.Object] = trialValue{Object: o.Object, State: in[i].State, Content: in[i].Content}
			}
		}
		return true, nil
	}
	reached, err := took(reachedStep, t.reachedArgs()...)
	if err != nil {
		return nil, err
	}
	for _, o := range t.takes {
		if reached {
			break
		}
		// What a step generated comes before what one was given.
		v := trialValue{Object: o.Object}
		err := db.QueryRowContext(ctx, "SELECT state, content FROM run_objects WHERE run_id = ? AND object_id = ? ORDER BY id DESC LIMIT 1", run, o.Object).
			Scan(&v.State, &v.Content)
		switch {
		case err == nil:
			values[o.Object] = v
		case errors.Is(err, sql.ErrNoRows):
			_, err = took(tookStep+" AND JSON_CONTAINS(s.inputs, JSON_OBJECT('name', ?))", append(tookArgs(), o.Name)...)
		}
		if err != nil {
			return nil, err
		}
	}
	out := []trialValue{}
	for _, o := range t.takes {
		if v, ok := values[o.Object]; ok {
			if t.states[o.Object][v.State] == 0 {
				v.State = ""
			}
			out = append(out, v)
		}
	}
	return out, nil
}

// What the process does outside the flow, as a person trying it should
// know: the operation a connector carries out, the MCP servers an agent
// may use, the repositories it works on.
func (t trialTarget) effects(ctx context.Context, db *sql.Tx) ([]string, error) {
	effects := []string{}
	s := t.process.Settings
	switch t.process.Kind {
	case kindConnector:
		if s.Connector == nil {
			break
		}
		var name, kind string
		err := db.QueryRowContext(ctx, "SELECT name, connector FROM mcp_servers WHERE id = ?", s.Connector.Server).Scan(&name, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if c := findConnector(kind); c != nil {
			if op := c.operation(s.Connector.Operation); op != nil {
				if op.Write {
					effects = append(effects, fmt.Sprintf("It carries out %s on %s, which changes what is there.", op.Title, name))
				} else {
					effects = append(effects, fmt.Sprintf("It carries out %s on %s, which only reads.", op.Title, name))
				}
			}
		}
	case kindAgent:
		if len(s.MCPServers) > 0 {
			var names []string
			err := scanAll(ctx, db, "SELECT name FROM mcp_servers WHERE id IN "+placeholders(len(s.MCPServers))+" ORDER BY name",
				func(rows *sql.Rows) error {
					var n string
					err := rows.Scan(&n)
					names = append(names, n)
					return err
				}, int64Args(s.MCPServers)...)
			if err != nil {
				return nil, err
			}
			if len(names) > 0 {
				effects = append(effects, fmt.Sprintf("It may use the tools of %s, which can change what is there, such as by posting or commenting.", list(names, "and")))
			}
		}
	case kindScript:
		effects = append(effects, "Its script runs as it is, and reaches the internet hosts it may.")
	}
	if t.process.Kind != kindConnector {
		var settings flowSettings
		if err := db.QueryRowContext(ctx, "SELECT settings FROM flows WHERE id = ?", t.flow).Scan(&settings); err != nil {
			return nil, err
		}
		var repos []string
		for _, r := range settings.Repositories {
			if s.Repositories == nil || contains(*s.Repositories, r.Name) {
				repos = append(repos, r.Name)
			}
		}
		if len(repos) > 0 {
			effects = append(effects, fmt.Sprintf("It works on %s on a branch of its own; what it commits there, and pushes, is real.", list(repos, "and")))
		}
	}
	return effects, nil
}

// Executes a trial of a process on board, nil for the top board, as start
// does a run: only the process runs, on what values give the objects it
// takes.
func (e *engine) try(nr pendingRun, flow int64, board *int64, process int64, values map[int64]value) {
	ctx, cancel := context.WithCancelCause(context.Background())
	lr := e.register(nr.id, func() { cancel(nil) })
	lr.failWith = cancel
	go func() {
		logRunStarted(e.db, nr.id)
		fr := newFlowRun(flow, nr.version, nr.board, nr.repos, nr.prefix)
		w := newWalk(ctx, e, nr.id, lr, fr, board, nil, fr.workspace(nr.id, "run-"+strconv.FormatInt(nr.id, 10)))
		w.try(process, values)
		e.finish(ctx, nr.id, w)
	}()
}

// Runs process id alone, on what values give the objects it takes, with
// the records its queries select: retried as its retries say, but neither
// held to its conditions nor followed by anything, its failure handled by
// nothing. Returns once it is over.
func (w *walk) try(id int64, values map[int64]value) {
	if w.stopped() {
		return
	}
	w.mu.Lock()
	given := []runInput{}
	seen := map[int64]bool{}
	for _, i := range w.inputs[id] {
		v, ok := values[i.object]
		if !ok || seen[i.object] {
			continue
		}
		seen[i.object] = true
		w.values[i.object] = v
		given = append(given, w.inputOf(id, i.object))
	}
	w.mu.Unlock()
	records, err := w.queried(id)
	if err != nil {
		runLog(w.e.db, w.run, 0, logError, "the records for process %d couldn't be queried: %v", id, err)
		w.note(id, stepError, "Its query couldn't read the records.")
		w.fail()
		return
	}
	given = append(given, records...)
	res, ok, _, stepID := w.attempt(id, given, nil)
	if !ok {
		w.fail()
		return
	}
	w.outcome(id, stepID, res)
}

// The process a trial route is about, on its flow's board as it is, or
// false once it answered.
func trialTargetFor(w http.ResponseWriter, r *http.Request, tx *sql.Tx, board *snapshot) (trialTarget, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return trialTarget{}, false
	}
	var flow int64
	err = tx.QueryRowContext(r.Context(), "SELECT flow_id FROM processes WHERE id = ?", id).Scan(&flow)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "process not found", http.StatusNotFound)
		return trialTarget{}, false
	}
	if err != nil {
		serverError(w, err)
		return trialTarget{}, false
	}
	if board == nil {
		s, err := takeSnapshot(r.Context(), tx, flow)
		if err != nil {
			serverError(w, err)
			return trialTarget{}, false
		}
		board = &s
	}
	t, msg := trialTargetOf(r.Context(), flow, id, *board)
	if msg != "" {
		http.Error(w, msg, http.StatusConflict)
		return t, false
	}
	return t, true
}

// The flow a process is in, 0 without the process.
func processFlow(ctx context.Context, db querier, id string) (int64, error) {
	var flow int64
	err := db.QueryRowContext(ctx, "SELECT flow_id FROM processes WHERE id = ?", id).Scan(&flow)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return flow, err
}

// Why a sample's name can't be saved, or "".
func sampleNameProblem(name string) string {
	switch {
	case name == "":
		return "a sample needs a name"
	case utf8.RuneCountInString(name) > 255:
		return "a sample's name is at most 255 characters"
	}
	return ""
}

func registerTrialRoutes(mux *http.ServeMux, db *sql.DB, eng *engine) {
	// What trying the process takes, its samples, the runs it may start
	// from, and what it does outside the flow.
	mux.HandleFunc("GET /api/processes/{id}/trial", func(w http.ResponseWriter, r *http.Request) {
		tx, err := db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		t, ok := trialTargetFor(w, r, tx, nil)
		if !ok {
			return
		}
		plan := trialPlan{Process: t.process.ID, ProcessName: t.process.Name, Kind: t.process.Kind, Takes: t.takes}
		if plan.Samples, err = t.samples(r.Context(), tx); err == nil {
			if plan.Past, err = t.past(r.Context(), tx); err == nil {
				plan.Effects, err = t.effects(r.Context(), tx)
			}
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
	})

	// What a run of the process's flow gives the objects it takes, to try it
	// on: what it took there, or what the run held. The guard sees only the
	// process, so the run is checked to be of its flow here.
	mux.HandleFunc("GET /api/processes/{id}/trial/runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		run, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
		if err != nil {
			http.Error(w, "invalid run", http.StatusBadRequest)
			return
		}
		tx, err := db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		t, ok := trialTargetFor(w, r, tx, nil)
		if !ok {
			return
		}
		var flow int64
		err = tx.QueryRowContext(r.Context(), "SELECT flow_id FROM runs WHERE id = ?", run).Scan(&flow)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && flow != t.flow) {
			http.Error(w, "no such run of the process's flow", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		values, err := t.runValues(r.Context(), tx, run)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, values)
	})

	// Tries the process, as a trial on the board as it is: {"inputs":
	// [{"object": id, "state": "name", "content": "..."}]} gives the objects
	// it takes; it takes none of the others.
	mux.HandleFunc("POST /api/processes/{id}/trial", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Inputs []trialValue `json:"inputs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		flow, err := processFlow(r.Context(), db, r.PathValue("id"))
		if err != nil {
			serverError(w, err)
			return
		}
		if flow != 0 && refuseInStruct(w, r, db, flow) {
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		// A trial is of the board as it is, a draft or not, never the
		// version the flow published; see publish.go.
		nr, err := newRun(r.Context(), tx, flow, nil, requestUserID(r), false)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "process not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		t, ok := trialTargetFor(w, r, tx, &nr.board)
		if !ok {
			return
		}
		values, msg := t.values(body.Inputs)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE runs SET trial_of = ?, trial_of_name = ? WHERE id = ?", t.process.ID, t.process.Name, nr.id); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		eng.try(nr, flow, t.process.Parent, t.process.ID, values)
		rn, err := getRun(r.Context(), db, nr.id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rn)
	})

	// Saves inputs to try the process on as a sample: {"name": "...",
	// "inputs": [...]}, as a trial takes them, replacing the sample of that
	// name if it has one. Returns its samples.
	mux.HandleFunc("PUT /api/processes/{id}/samples", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string       `json:"name"`
			Inputs []trialValue `json:"inputs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if msg := sampleNameProblem(body.Name); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		t, ok := trialTargetFor(w, r, tx, nil)
		if !ok {
			return
		}
		values, msg := t.values(body.Inputs)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		inputs := sampleInputs{}
		for _, o := range t.takes {
			if v, ok := values[o.Object]; ok {
				inputs = append(inputs, sampleInput{Object: o.Object, State: v.state, Content: v.content})
			}
		}
		var n int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM process_samples WHERE process_id = ? AND name <> ?", t.process.ID, body.Name).Scan(&n); err != nil {
			serverError(w, err)
			return
		}
		if n >= maxSamples {
			http.Error(w, fmt.Sprintf("a process keeps at most %d samples; delete one first", maxSamples), http.StatusConflict)
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			"INSERT INTO process_samples (process_id, name, inputs) VALUES (?, ?, ?) AS saved ON DUPLICATE KEY UPDATE inputs = saved.inputs",
			t.process.ID, body.Name, inputs); err != nil {
			serverError(w, err)
			return
		}
		samples, err := t.samples(r.Context(), tx)
		if err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, samples)
	})

	mux.HandleFunc("DELETE /api/processes/{id}/samples/{sample}", func(w http.ResponseWriter, r *http.Request) {
		res, err := db.ExecContext(r.Context(), "DELETE FROM process_samples WHERE id = ? AND process_id = ?", r.PathValue("sample"), r.PathValue("id"))
		if err != nil {
			serverError(w, err)
			return
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				serverError(w, err)
				return
			}
			http.Error(w, "no such sample of the process", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
