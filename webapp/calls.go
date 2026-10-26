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
// version first if it changed, with its own repositories; in a run of a
// version published, it runs the version it published instead, if any (see
// publish.go).
//
// A flow can't run itself, however many subflows apart; see checkCall. One
// in the trash runs nowhere until it is restored (trash.go).

// Why a subflow process can't be given a flow in the trash to run.
const inTrashCall = "that procedure is in the trash; restore it, or pick another"

// What's wrong with a process of kind in flow running the flow its settings
// name, if anything: that flow must exist, out of the trash, and must not,
// directly or through its own subflow processes, run flow.
func checkCall(ctx context.Context, db *sql.DB, flow int64, kind string, s processSettings) (string, error) {
	if kind != kindSubflow || s.Flow == nil {
		return "", nil
	}
	var n int
	var st, trashed bool
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MAX(is_struct), FALSE), COALESCE(MAX(deleted_at IS NOT NULL), FALSE) FROM flows WHERE id = ?", *s.Flow).
		Scan(&n, &st, &trashed); err != nil {
		return "", err
	}
	if n == 0 {
		return "flow not found", nil
	}
	if trashed {
		return inTrashCall, nil
	}
	if st {
		return "a struct is an object, not a flow to run; drag it onto the board as an object", nil
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

// The flow a subflow process runs, as a run of it starts on: in a run of a
// version published, that flow's version published if it has one; else its
// board, saved as a version if it changed since its last (see publish.go).
// And the version's repositories. The run carries the flows it runs inside,
// after fr's. Says what is wrong, if anything.
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
	var trashed bool
	// Taken as moving it to the trash takes it, which then sees its run.
	err = tx.QueryRowContext(ctx, "SELECT name, settings, deleted_at IS NOT NULL FROM flows WHERE id = ? FOR UPDATE", *flow).Scan(&name, &settings, &trashed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "The flow it runs is gone.", nil
	}
	if err != nil {
		return nil, "", err
	}
	if trashed {
		return nil, "The procedure it runs, " + quote(name) + ", is in the trash; restore it, or pick another.", nil
	}
	version, board, _, err := runVersion(ctx, tx, *flow, fr.published, saving{reason: savedForRun})
	if err != nil {
		return nil, "", err
	}
	if board.Settings != nil {
		settings = *board.Settings
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	callee := newFlowRun(*flow, version, board, settings.Repositories, settings.prefix(name))
	callee.callers = append(slices.Clone(fr.callers), fr.flow)
	callee.published = fr.published
	return callee, "", nil
}

// The object of a name on the top board, and the state of a name of an object,
// 0 for none.
func (fr *flowRun) topObject(name string) int64 {
	for _, o := range fr.board.Objects {
		if o.Parent == nil && o.Owner == nil && o.Name == name {
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
// one with the same states, attributes and objects making it up, so what comes out of that flow has
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
	if err := markModule(ctx, tx, *s.Flow); err != nil {
		return err
	}
	type outcome struct {
		id   int64
		name string
		many bool
	}
	var outcomes []outcome
	err = scanAll(ctx, tx, `SELECT o.id, o.name, o.many FROM objects o
		WHERE o.flow_id = ? AND o.parent_id IS NULL AND o.`+onProcessBoard+` AND NOT o.human AND o.at_end
		ORDER BY o.x, o.id`, func(rows *sql.Rows) error {
		var o outcome
		err := rows.Scan(&o.id, &o.name, &o.many)
		outcomes = append(outcomes, o)
		return err
	}, *s.Flow)
	if err != nil {
		return err
	}
	// What the outcomes are made of, loaded once one needs copying.
	var made structure
	for i, o := range outcomes {
		var here int64
		// One it already generates first, such as one pasted along with it.
		err := tx.QueryRowContext(ctx, `SELECT o.id FROM objects o WHERE o.flow_id = ? AND o.parent_id <=> ? AND o.`+onProcessBoard+` AND o.name = ?
			ORDER BY EXISTS (SELECT 1 FROM object_links l WHERE l.process_id = ? AND l.object_id = o.id AND l.direction = 'output') DESC, o.id LIMIT 1`,
			flow, parent, o.name, process).Scan(&here)
		if errors.Is(err, sql.ErrNoRows) {
			if made == nil {
				if made, err = loadStructure(ctx, tx, *s.Flow); err != nil {
					return err
				}
			}
			at := &point{x + float64(i)*outcomeGapX, y + outcomeGapY}
			if here, err = copyStructure(ctx, tx, made, o.id, flow, parent, nil, at); err != nil {
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
