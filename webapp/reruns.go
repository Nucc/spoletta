package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
)

// A run can be re-run from one of its processes, to try that process again
// after fixing what it took or the process itself. The re-run is a new run
// on the board as it is now, even of a run of the version published, since
// a person starts it by hand from the board as they do with the Run button
// (see publish.go). What the processes before it generated is
// copied from the run, as a step of its own, and the re-run starts at the
// process, taking what it took then, or what the person re-running it put
// in its place. The process and what follows it run anew: the objects they
// generate, or decide the state of, aren't copied, so the processes taking
// them wait for them as in any run. Processes on branches of their own, not
// following it, don't run again; what they generated is copied.
//
// A run can be re-run once it ended, from a process on the board it walks.
// For the run of a recording flow process, or of the flow a subflow process
// ran, that is the flow process's board, or the other flow's top board: the
// re-run is a new run of it in the same run, given what the run it re-runs
// was, but what comes out of it goes nowhere, as the run it ran in has gone
// on already. The re-run takes over the values the run stored (see
// instances.go): the run releases them, and the re-run stores what it copied
// of them, so regenerating the same unique value isn't a duplicate. So do
// the runs of flow processes the re-run runs again, as their boards run anew. It
// works on a branch of its own, which starts where the run's branch was as
// the process started, so it has what came before committed and nothing
// after; the run's branch stays as it is.

// Steps that hold what a re-run copied from the run it re-runs; they have no
// process.
const kindCopied = "copied"

// What re-running a run from a process takes and copies.
type rerunPlan struct {
	process int64
	name    string
	// The board the process is on, nil for the top board, and what the run
	// walking it was given from outside, by object.
	board *int64
	given map[int64]value
	// The objects the process takes, in the order it takes them.
	inputs []int64
	// What the re-run starts with, by object: what the run copies, and what
	// the process took when it ran.
	values map[int64]value
	// The objects the process and what follows it generate, which the re-run
	// generates anew.
	after map[int64]bool
	// The commit each repository's branch was at in the run as the process
	// started, by name, which the re-run's branches start from.
	starts map[string]string
	w      *walk
}

// An object the process re-run from takes, as it took it in the run.
type rerunInput struct {
	Object  int64  `json:"object"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Content string `json:"content"`
	// False if the run had nothing of it when the process ran.
	Present bool `json:"present"`
}

// Works out how to re-run run src from the process of its step on board, the
// board as it is of the flow src ran. Says why it can't be, if it can't.
func planRerun(ctx context.Context, tx *sql.Tx, src run, stepID int64, board snapshot) (rerunPlan, string, error) {
	var p rerunPlan
	if src.Status == runRunning {
		return p, fmt.Sprintf("run #%d is still going; stop it or wait for it to end", src.ID), nil
	}
	// A re-run would go on past the process, as a real run; see trials.go.
	if src.TrialOf != nil {
		return p, fmt.Sprintf("run #%d is a trial of %s; try it again instead", src.ID, src.TrialOfName), nil
	}
	// The run of a recording flow process walks its board; any other run,
	// such as that of the flow a subflow process ran, its flow's top board.
	on := src.Process
	if on != nil && !slices.ContainsFunc(board.Processes, func(pr process) bool { return pr.ID == *on && pr.Kind == kindFlow }) {
		return p, strconv.Quote(src.ProcessName) + " is no longer a flow process on the board", nil
	}
	var ref sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT process_ref, process_name FROM run_steps WHERE id = ? AND run_id = ?", stepID, src.ID).Scan(&ref, &p.name)
	if errors.Is(err, sql.ErrNoRows) {
		return p, "no step with that id in this run", nil
	}
	if err != nil {
		return p, "", err
	}
	i := slices.IndexFunc(board.Processes, func(pr process) bool { return ref.Valid && pr.ID == ref.Int64 })
	switch {
	case i < 0:
		return p, strconv.Quote(p.name) + " is no longer on the flow's board", nil
	case !sameBoard(board.Processes[i].Parent, on):
		return p, strconv.Quote(p.name) + " is inside a flow process; re-run from the flow process", nil
	}
	p.process, p.name, p.board = ref.Int64, board.Processes[i].Name, on

	states := map[int64]map[string]int64{}
	for _, st := range board.States {
		if states[st.Object] == nil {
			states[st.Object] = map[string]int64{}
		}
		states[st.Object][st.Name] = st.ID
	}
	if p.given, err = givenTo(ctx, tx, src, board, states); err != nil {
		return p, "", err
	}
	p.w = newWalk(ctx, nil, 0, nil, newFlowRun(src.Flow, 0, board, nil, ""), on, p.given, nil)
	p.after = p.w.after(p.process)
	for _, in := range p.w.inputs[p.process] {
		// Objects nothing generates hold nothing a process takes.
		if (p.w.generated[in.object] || in.state != 0) && !slices.Contains(p.inputs, in.object) {
			p.inputs = append(p.inputs, in.object)
		}
	}
	// The process its board starts from takes what came from outside too.
	if p.w.target != nil && *p.w.target == p.process {
		for _, o := range p.w.given {
			if !slices.Contains(p.inputs, o) {
				p.inputs = append(p.inputs, o)
			}
		}
	}

	// What each object last held in the run, and before the step started.
	last, before := map[int64]value{}, map[int64]value{}
	err = scanAll(ctx, tx, "SELECT step_id, COALESCE(object_ref, object_id), state, content FROM run_objects WHERE run_id = ? ORDER BY id",
		func(rows *sql.Rows) error {
			var step int64
			var object sql.NullInt64
			var state, content string
			if err := rows.Scan(&step, &object, &state, &content); err != nil {
				return err
			}
			// Objects deleted since have nothing to hold.
			if _, ok := p.w.objects[object.Int64]; !object.Valid || !ok {
				return nil
			}
			v := value{content: content, state: states[object.Int64][state]}
			last[object.Int64] = v
			if step < stepID {
				before[object.Int64] = v
			}
			return nil
		}, src.ID)
	if err != nil {
		return p, "", err
	}
	p.values = map[int64]value{}
	for o, v := range p.given {
		p.values[o] = v
	}
	for o, v := range last {
		if !p.after[o] {
			p.values[o] = v
		}
	}
	for _, o := range p.inputs {
		if v, ok := before[o]; ok {
			p.values[o] = v
		} else if v, ok := p.given[o]; ok {
			p.values[o] = v
		} else {
			delete(p.values, o)
		}
	}
	p.starts, err = startCommits(ctx, tx, src, stepID)
	return p, "", err
}

// What the run of a recording flow process, or of the flow a subflow process
// ran, was given from outside, by object: what the step in the run it ran in
// took. Each is the object of its name, preferably on the board the flow
// process is on, or for another flow, its top board. A re-run of such a run
// was given what the run it re-runs was; what it copied of it, as changed by
// whoever re-ran it, the caller has. Nothing for a run of the whole flow.
func givenTo(ctx context.Context, tx *sql.Tx, src run, board snapshot, states map[int64]map[string]int64) (map[int64]value, error) {
	given := map[int64]value{}
	if src.Parent == nil {
		return given, nil
	}
	var on *int64
	if src.Process != nil {
		for _, pr := range board.Processes {
			if pr.ID == *src.Process {
				on = pr.Parent
			}
		}
	}
	var inputs []byte
	for r := src; ; {
		err := tx.QueryRowContext(ctx, "SELECT inputs FROM run_steps WHERE child_run_id = ? ORDER BY id DESC LIMIT 1", r.ID).Scan(&inputs)
		if err == nil || !errors.Is(err, sql.ErrNoRows) {
			if err != nil {
				return nil, err
			}
			break
		}
		if r.RerunOf == nil {
			return given, nil
		}
		if err := scanRun(tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE id = ?", *r.RerunOf), &r); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return given, nil
			}
			return nil, err
		}
	}
	var ins []runInput
	if err := json.Unmarshal(inputs, &ins); err != nil {
		return nil, err
	}
	for _, in := range ins {
		o := objectNamed(board, in.Name, on)
		if o == 0 {
			continue
		}
		given[o] = value{content: in.Content, state: states[o][in.State]}
	}
	return given, nil
}

// The object named name on board, nil for the top board, or else anywhere;
// 0 for none.
func objectNamed(board snapshot, name string, on *int64) int64 {
	var found int64
	for _, o := range board.Objects {
		if o.Name != name {
			continue
		}
		if sameBoard(o.Parent, on) && o.Owner == nil {
			return o.ID
		}
		if found == 0 {
			found = o.ID
		}
	}
	return found
}

// The workspace a run's processes worked in: a run's own, or for the run of
// a recording flow process for no item, the run's it ran in.
func workspaceOwner(ctx context.Context, tx *sql.Tx, r run) (run, error) {
	for r.Parent != nil && r.Process != nil && r.ItemKey == "" && r.RerunOf == nil {
		if err := scanRun(tx.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE id = ?", *r.Parent), &r); err != nil {
			return r, err
		}
	}
	return r, nil
}

// Where each repository's branch of run was as the step started, by name:
// where the step itself started, for one it worked on, else where the last
// step before it on the branch left it, or for a re-run, where its branch
// started. Repositories nothing worked on yet aren't there; their branches
// start from their base.
func startCommits(ctx context.Context, tx *sql.Tx, src run, stepID int64) (map[string]string, error) {
	owner, err := workspaceOwner(ctx, tx, src)
	if err != nil {
		return nil, err
	}
	starts := map[string]string{}
	var stored []byte
	if err := tx.QueryRowContext(ctx, "SELECT starts FROM runs WHERE id = ?", owner.ID).Scan(&stored); err != nil {
		return nil, err
	}
	if stored != nil {
		if err := json.Unmarshal(stored, &starts); err != nil {
			return nil, err
		}
	}
	// Items of a list work on branches of their own; only the run's counts,
	// whichever run's steps worked on it.
	suffix := "/run-" + strconv.FormatInt(owner.ID, 10)
	err = scanAll(ctx, tx,
		"SELECT id, commits FROM run_steps WHERE id <= ? AND commits IS NOT NULL AND RIGHT(branch, ?) = ? ORDER BY id",
		func(rows *sql.Rows) error {
			var id int64
			var b []byte
			if err := rows.Scan(&id, &b); err != nil {
				return err
			}
			var commits []repoCommits
			if err := json.Unmarshal(b, &commits); err != nil {
				return err
			}
			for _, c := range commits {
				if id == stepID {
					starts[c.Name] = c.Before
				} else {
					starts[c.Name] = c.After
				}
			}
			return nil
		}, stepID, len(suffix), suffix)
	return starts, err
}

// The objects process p takes, as it took them in the run.
func (p rerunPlan) takes() []rerunInput {
	ins := []rerunInput{}
	for _, o := range p.inputs {
		v, ok := p.values[o]
		ins = append(ins, rerunInput{Object: o, Name: p.w.objects[o], State: p.w.stateNames[v.state], Content: v.content, Present: ok})
	}
	return ins
}

// The names of the objects the re-run copies besides what the process
// takes, and of those it generates anew, each in id order.
func (p rerunPlan) copied() (copied, anew []string) {
	copied, anew = []string{}, []string{}
	for _, o := range sortedKeys(p.values) {
		if !slices.Contains(p.inputs, o) {
			copied = append(copied, p.w.objects[o])
		}
	}
	for _, o := range sortedKeys(p.after) {
		if !slices.Contains(p.inputs, o) {
			anew = append(anew, p.w.objects[o])
		}
	}
	return copied, anew
}

func sortedKeys[V any](m map[int64]V) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// The objects that process p and the processes following it generate, or
// decide the state of: those taking what it generates, those it connects
// to, and those handling its failure, and so on.
func (w *walk) after(p int64) map[int64]bool {
	objects := map[int64]bool{}
	seen := map[int64]bool{p: true}
	for queue := []int64{p}; len(queue) > 0; queue = queue[1:] {
		q := queue[0]
		next := append(slices.Clone(w.successors[q]), w.handlers[q]...)
		for _, end := range w.outputs[q] {
			objects[end.object] = true
			for _, t := range w.takers[end.object] {
				next = append(next, t.process)
			}
		}
		for _, n := range next {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}
	return objects
}

// Records what the re-run copied as its first step, and stores the
// persistent objects among it as the re-run's, taking them over from the run
// it re-runs, from the step on. Returns a unique value another run holds, which stops the
// re-run.
func recordCopied(ctx context.Context, tx *sql.Tx, src, stepID, rerun int64, p rerunPlan) (*duplicate, error) {
	if err := releaseRun(ctx, tx, src); err != nil {
		return nil, err
	}
	// The runs of recording flow processes and subflow processes the step's
	// process and those after it started are redone too, as are theirs.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM object_instances WHERE run_id IN (
			WITH RECURSIVE redone (id) AS (
				SELECT child_run_id FROM run_steps WHERE run_id = ? AND id >= ? AND child_run_id IS NOT NULL
				UNION
				SELECT s.child_run_id FROM run_steps s JOIN redone r ON s.run_id = r.id WHERE s.child_run_id IS NOT NULL)
			SELECT id FROM redone)`, src, stepID); err != nil {
		return nil, err
	}
	if len(p.values) == 0 {
		return nil, nil
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO run_steps (run_id, process_name, kind, prompt, inputs, status, output, error, instructions, finished_at)
		VALUES (?, ?, ?, '', '[]', ?, '', '', '', CURRENT_TIMESTAMP(3))`,
		rerun, "Copied from run #"+strconv.FormatInt(src, 10), kindCopied, stepDone)
	if err != nil {
		return nil, err
	}
	copied, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, o := range sortedKeys(p.values) {
		v := p.values[o]
		if err := insertRunObject(ctx, tx, rerun, copied, o, p.w.objects[o], p.w.stateNames[v.state], v.content); err != nil {
			return nil, err
		}
	}
	return storeInstances(ctx, tx, p.w.fr.rb, rerun, copied)
}

// The run and step a re-run route is about, or false once it answered.
func rerunTarget(w http.ResponseWriter, r *http.Request, db *sql.DB) (run, int64, bool) {
	runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
	stepID, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return run{}, 0, false
	}
	src, err := getRun(r.Context(), db, runID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "run not found", http.StatusNotFound)
		return run{}, 0, false
	}
	if err != nil {
		serverError(w, err)
		return run{}, 0, false
	}
	return src, stepID, true
}

func registerRerunRoutes(mux *http.ServeMux, db *sql.DB, eng *engine) {
	// What re-running the run from the step's process takes: the objects the
	// process took, which the person re-running it can change, and what the
	// re-run copies and generates anew.
	mux.HandleFunc("GET /api/runs/{run}/steps/{id}/rerun", func(w http.ResponseWriter, r *http.Request) {
		src, stepID, ok := rerunTarget(w, r, db)
		if !ok {
			return
		}
		tx, err := db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		board, err := takeSnapshot(r.Context(), tx, src.Flow)
		if err != nil {
			serverError(w, err)
			return
		}
		p, msg, err := planRerun(r.Context(), tx, src, stepID, board)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusConflict)
			return
		}
		copied, anew := p.copied()
		writeJSON(w, http.StatusOK, struct {
			Process     int64        `json:"process"`
			ProcessName string       `json:"processName"`
			Inputs      []rerunInput `json:"inputs"`
			Copied      []string     `json:"copied"`
			Anew        []string     `json:"anew"`
		}{p.process, p.name, p.takes(), copied, anew})
	})

	// Re-runs the run from the step's process, as a new run on the board as
	// it is: {"inputs": {"<object id>": "content"}} puts content in place of
	// what the process took of each object given; the others it takes as it
	// did.
	mux.HandleFunc("POST /api/runs/{run}/steps/{id}/rerun", func(w http.ResponseWriter, r *http.Request) {
		src, stepID, ok := rerunTarget(w, r, db)
		if !ok {
			return
		}
		var body struct {
			Inputs map[string]string `json:"inputs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if refuseInStruct(w, r, db, src.Flow) {
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		nr, err := newRun(r.Context(), tx, src.Flow, nil, requestUserID(r), false)
		if err != nil {
			serverError(w, err)
			return
		}
		p, msg, err := planRerun(r.Context(), tx, src, stepID, nr.board)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusConflict)
			return
		}
		for key, content := range body.Inputs {
			o, err := strconv.ParseInt(key, 10, 64)
			if err != nil || !slices.Contains(p.inputs, o) {
				http.Error(w, fmt.Sprintf("%s takes no object %s", strconv.Quote(p.name), key), http.StatusBadRequest)
				return
			}
			p.values[o] = value{content: content, state: p.values[o].state}
		}
		// A re-run of a flow process's run is another run of it in the same run.
		starts, err := json.Marshal(p.starts)
		if err != nil {
			serverError(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE runs SET parent_run_id = ?, process_ref = ?, process_name = ?, item_key = ?,
				rerun_of = ?, rerun_from = ?, rerun_from_name = ?, starts = ?, record_id = ? WHERE id = ?`,
			src.Parent, src.Process, src.ProcessName, src.ItemKey, src.ID, p.process, p.name, starts, src.Record, nr.id); err != nil {
			serverError(w, err)
			return
		}
		// A re-run of a run started on a record reads that record again.
		if src.Record != nil {
			nr.record = *src.Record
		}
		dup, err := recordCopied(r.Context(), tx, src.ID, stepID, nr.id, p)
		if err != nil {
			serverError(w, err)
			return
		}
		if dup != nil {
			http.Error(w, dup.message(), http.StatusConflict)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		eng.rerun(nr, src.Flow, p.board, p.given, p.process, p.values, p.starts)
		rn, err := getRun(r.Context(), db, nr.id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rn)
	})
}
