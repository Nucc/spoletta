package main

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
)

// A subflow process reuses another flow: running it runs that flow's top
// board as a run of that flow, from inside this one, as a recording flow
// process runs its board. Zoomed in, it is that flow.
//
// The two flows share no objects, so the subflow process's links name its
// interface by name: an object it takes is given to the object of the same
// name on the other flow's top board, in the state of the same name, and an
// object it generates is what the other flow's run left of the object of the
// same name. The other flow runs as it is when the step starts, saved as a
// version first if it changed, with its own repositories.
//
// A flow can't run itself, however many subflows apart; see checkCall.

// What's wrong with a process of kind in flow running the flow its settings
// name, if anything: that flow must exist, and must not, directly or through
// its own subflow processes, run flow.
func checkCall(ctx context.Context, db *sql.DB, flow int64, kind string, s processSettings) (string, error) {
	if kind != kindSubflow || s.Flow == nil {
		return "", nil
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM flows WHERE id = ?", *s.Flow).Scan(&n); err != nil {
		return "", err
	}
	if n == 0 {
		return "flow not found", nil
	}
	seen := map[int64]bool{}
	next := []int64{*s.Flow}
	for len(next) > 0 {
		f := next[0]
		next = next[1:]
		if f == flow {
			return "a flow can't run itself", nil
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		called, err := calledFlows(ctx, db, f)
		if err != nil {
			return "", err
		}
		next = append(next, called...)
	}
	return "", nil
}

// The flows a flow's subflow processes run.
func calledFlows(ctx context.Context, db *sql.DB, flow int64) ([]int64, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT CAST(JSON_EXTRACT(settings, '$.flow') AS SIGNED) FROM processes
		WHERE flow_id = ? AND kind = ? AND JSON_EXTRACT(settings, '$.flow') IS NOT NULL`, flow, kindSubflow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []int64
	for rows.Next() {
		var f int64
		if err := rows.Scan(&f); err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	return fs, rows.Err()
}

// The flow a subflow process runs, as a run of it starts on: its board saved
// as a version if it changed since its last, and its repositories. The run
// carries the flows it runs inside, after fr's. Says what is wrong, if anything.
func (fr *flowRun) call(ctx context.Context, db *sql.DB, flow *int64) (*flowRun, string, error) {
	if flow == nil {
		return nil, "It runs no flow; pick one in its settings.", nil
	}
	if *flow == fr.flow || slices.Contains(fr.callers, *flow) {
		return nil, "It runs flow #" + strconv.FormatInt(*flow, 10) + ", which it is already running inside.", nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var name string
	var settings flowSettings
	err = tx.QueryRowContext(ctx, "SELECT name, settings FROM flows WHERE id = ?", *flow).Scan(&name, &settings)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "The flow it runs is gone.", nil
	}
	if err != nil {
		return nil, "", err
	}
	version, err := saveVersion(ctx, tx, *flow, true)
	if err != nil {
		return nil, "", err
	}
	board, err := takeSnapshot(ctx, tx, *flow)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	callee := newFlowRun(*flow, version, board, settings.Repositories, settings.prefix(name))
	callee.callers = append(slices.Clone(fr.callers), fr.flow)
	return callee, "", nil
}

// The object of a name on the top board, and the state of a name of an object,
// 0 for none.
func (fr *flowRun) topObject(name string) int64 {
	for _, o := range fr.board.Objects {
		if o.Parent == nil && o.Name == name {
			return o.ID
		}
	}
	return 0
}

func (fr *flowRun) stateOf(object int64, name string) int64 {
	for _, st := range fr.board.States {
		if st.Object == object && st.Name == name {
			return st.ID
		}
	}
	return 0
}

// The objects given to a subflow process, as given to the flow it runs: each
// to its namesake there, in its state's namesake. Those without one aren't
// given.
func (w *walk) toCallee(callee *flowRun, given map[int64]value) map[int64]value {
	out := map[int64]value{}
	for o, v := range given {
		to := callee.topObject(w.objects[o])
		if to == 0 {
			continue
		}
		v.state = callee.stateOf(to, w.stateNames[v.state])
		out[to] = v
	}
	return out
}

// What the flow a subflow process ran left of the objects it generates: each
// object's namesake there, if the run generated it, in its state's namesake
// here, or else in the state the link names.
func (w *walk) fromCallee(id int64, callee *flowRun, inner *walk) map[int64]value {
	out := map[int64]value{}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	for _, end := range w.outputs[id] {
		from := callee.topObject(w.objects[end.object])
		v, ok := inner.values[from]
		if from == 0 || !ok || !inner.generated[from] {
			continue
		}
		v.state = w.fr.stateOf(end.object, inner.stateNames[v.state])
		if v.state == 0 {
			v.state = end.state
		}
		out[end.object] = v
	}
	return out
}

// Where a subflow process's outcomes go when it brings them in: below it, in
// a row, a little apart.
const (
	outcomeGapY = 140
	outcomeGapX = 240
)

// Brings the outcomes of the flow a subflow process runs onto its board, as
// objects it generates: the objects connected to its top board's end. Each
// is the object of the same name on the board if there is one, or else a new
// one with the same states and attributes, so what comes out of that flow has
// somewhere to go here.
// Does nothing for other processes, or one that runs no flow.
func bringOutcomes(ctx context.Context, tx *sql.Tx, process int64) error {
	var flow int64
	var parent *int64
	var x, y float64
	var kind string
	var s processSettings
	err := tx.QueryRowContext(ctx, "SELECT flow_id, parent_id, x, y, kind, settings FROM processes WHERE id = ?", process).
		Scan(&flow, &parent, &x, &y, &kind, &s)
	if err != nil || kind != kindSubflow || s.Flow == nil {
		return err
	}
	type outcome struct {
		id   int64
		name string
		many bool
	}
	var outcomes []outcome
	err = scanAll(ctx, tx, `SELECT o.id, o.name, o.many FROM objects o
		WHERE o.flow_id = ? AND o.parent_id IS NULL AND NOT o.human AND o.at_end
		ORDER BY o.x, o.id`, func(rows *sql.Rows) error {
		var o outcome
		err := rows.Scan(&o.id, &o.name, &o.many)
		outcomes = append(outcomes, o)
		return err
	}, *s.Flow)
	if err != nil {
		return err
	}
	for i, o := range outcomes {
		var here int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM objects WHERE flow_id = ? AND parent_id <=> ? AND name = ? ORDER BY id LIMIT 1", flow, parent, o.name).Scan(&here)
		if errors.Is(err, sql.ErrNoRows) {
			if here, err = copyObject(ctx, tx, o.id, flow, parent, x+float64(i)*outcomeGapX, y+outcomeGapY); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO object_links (process_id, object_id, direction, state_id) VALUES (?, ?, 'output', 0)", process, here); err != nil {
			return err
		}
	}
	return nil
}

// Copies an object of another flow onto a board of flow, with its states and
// attributes, and returns the copy's id. Whether to store it is this flow's
// choice, so the copy isn't persistent.
func copyObject(ctx context.Context, tx *sql.Tx, object, flow int64, parent *int64, x, y float64) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO objects (flow_id, name, x, y, many, parent_id)
		SELECT ?, name, ?, ?, many, ? FROM objects WHERE id = ?`, flow, x, y, parent, object)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	stmts := []string{
		"INSERT INTO object_states (object_id, name) SELECT ?, name FROM object_states WHERE object_id = ? ORDER BY id",
		`INSERT INTO object_attributes (object_id, name, definition, is_unique, is_file)
		SELECT ?, name, definition, is_unique, is_file FROM object_attributes WHERE object_id = ? ORDER BY id`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, id, object); err != nil {
			return 0, err
		}
	}
	return id, nil
}
