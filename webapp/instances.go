package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// A persistent object remembers what runs generated it with: each run keeps
// one instance of it, its latest content and state. Attributes marked unique
// hold a value no two stored instances share, so a flow can tell it already
// handled something, such as an exception it processed before. A step
// generating a value another run stored is a duplicate, and its run ends as
// terminated.
//
// Only runs that end done keep their instances; a run that fails, is
// interrupted or terminated releases its values, so they can be processed
// again. Instances outlive the object, so deleting it from the board doesn't
// lose what was stored.
//
// One that only the processes taking it generate, as OPM's effect, is
// carried from run to run: a run of the flow starts with what the latest run
// to end done stored of it, so a script can keep a cursor, such as when it
// last fetched and from which offset, and move it on each run. A run that
// fails doesn't move it. Only a run of the whole flow carries it, from its
// top board; items of a list run alongside each other and would race over
// one value. Its attributes shouldn't be unique, or a run leaving it as it
// was would be a duplicate.

// A value a step generated that another run already stored.
type duplicate struct {
	Object    string `json:"object"`
	Attribute string `json:"attribute"`
	Value     string `json:"value"`
	// The run that stored it.
	Run int64 `json:"run"`
}

func (d duplicate) message() string {
	return fmt.Sprintf("%s with %s %s was already stored by run #%d.", d.Object, d.Attribute, d.Value, d.Run)
}

const (
	runTerminated = "terminated"
	stepDuplicate = "duplicate"
)

// Releases the values a run stored; their keys go with them.
func releaseRun(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, runID int64) error {
	_, err := db.ExecContext(ctx, "DELETE FROM object_instances WHERE run_id = ?", runID)
	return err
}

// Stores the persistent objects a step generated, as the run's board has
// them, replacing what its run stored of them before, so a revision after a
// review doesn't collide with the run's own earlier value. Returns the first
// unique value another run already holds; the caller rolls back what was
// stored then.
func storeInstances(ctx context.Context, tx *sql.Tx, b *runBoard, runID, stepID int64) (*duplicate, error) {
	type generated struct {
		object        int64
		name, content string
		state         string
	}
	var objects []generated
	err := scanAll(ctx, tx, "SELECT object_ref, name, content, state FROM run_objects WHERE step_id = ? ORDER BY object_ref, id",
		func(rows *sql.Rows) error {
			var g generated
			if err := rows.Scan(&g.object, &g.name, &g.content, &g.state); err != nil {
				return err
			}
			if o, ok := b.objects[g.object]; ok && o.Persistent {
				g.name = o.Name
				objects = append(objects, g)
			}
			return nil
		}, stepID)
	if err != nil {
		return nil, err
	}

	// Objects and attributes go in id order, so concurrent runs take the
	// keys' locks in the same order.
	for _, g := range objects {
		if dup, err := storeInstance(ctx, tx, runID, stepID, g.object, g.name, g.content, g.state, b.uniqueAttributes(g.object)); dup != nil || err != nil {
			return dup, err
		}
	}
	return nil, nil
}

// Stores what a run holds of a persistent object, replacing what it stored
// of it before, and its unique values, those of attrs. Returns the first
// unique value another run already holds; the caller rolls back what was
// stored then.
func storeInstance(ctx context.Context, tx *sql.Tx, runID, stepID, object int64, name, content, state string, attrs []attribute) (*duplicate, error) {
	if _, err := tx.ExecContext(ctx, "DELETE FROM object_instances WHERE object_id = ? AND run_id = ?", object, runID); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO object_instances (object_id, run_id, step_id, content, state) VALUES (?, ?, ?, ?, ?)",
		object, runID, stepID, content, state)
	if err != nil {
		return nil, err
	}
	instance, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	// Content that isn't a JSON object, such as an object without
	// attributes, has no attribute values to keep unique.
	fields, err := jsonObject(content)
	if err != nil {
		return nil, nil
	}
	for _, a := range attrs {
		hash, ok := keyOf(fields[a.Name])
		if !ok {
			continue
		}
		value := compact(fields[a.Name])
		_, err := tx.ExecContext(ctx,
			"INSERT INTO instance_keys (object_id, attribute_id, value_hash, value, instance_id) VALUES (?, ?, ?, ?, ?)",
			object, a.ID, hash, value, instance)
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			d := &duplicate{Object: name, Attribute: a.Name, Value: value}
			err := tx.QueryRowContext(ctx,
				`SELECT i.run_id FROM instance_keys k JOIN object_instances i ON i.id = k.instance_id
				WHERE k.object_id = ? AND k.attribute_id = ? AND k.value_hash = ?`,
				object, a.ID, hash).Scan(&d.Run)
			return d, err
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// The object's unique attributes as the board has them now, its generals'
// first, since it has those too; a shared object's store keeps to its
// struct's as they are.
func uniqueAttributes(ctx context.Context, tx *sql.Tx, object int64) ([]attribute, error) {
	var attrs []attribute
	for seen := map[int64]bool{}; !seen[object]; {
		seen[object] = true
		var own []attribute
		err := scanAll(ctx, tx, "SELECT id, object_id, name, definition, is_unique FROM object_attributes WHERE object_id = ? AND is_unique ORDER BY id",
			func(rows *sql.Rows) error {
				var a attribute
				err := rows.Scan(&a.ID, &a.Object, &a.Name, &a.Definition, &a.Unique)
				own = append(own, a)
				return err
			}, object)
		if err != nil {
			return nil, err
		}
		attrs = append(own, attrs...)
		var general *int64
		if err := tx.QueryRowContext(ctx, "SELECT general_id FROM objects WHERE id = ?", object).Scan(&general); err != nil || general == nil {
			return attrs, err
		}
		object = *general
	}
	return attrs, nil
}

// The hash a unique value is stored by. Agents word the same value
// differently from run to run, so strings count the same whatever their
// case and spacing, and objects whatever their key order. Missing, null and
// empty values hold nothing, so they never collide.
func keyOf(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	v = normalize(v)
	if v == nil || v == "" {
		return "", false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

func normalize(v any) any {
	switch x := v.(type) {
	case string:
		return strings.ToLower(strings.Join(strings.Fields(x), " "))
	case []any:
		for i := range x {
			x[i] = normalize(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = normalize(x[k])
		}
	}
	return v
}

// A value as one line, cut short if long, to say what collided.
func compact(raw json.RawMessage) string {
	const limit = 200
	s := strings.Join(strings.Fields(string(raw)), " ")
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit]) + "…"
	}
	return s
}

// Records the state an object a step generated came out in, by name, once
// the step is over, such as by its states' rules or an agent's decision:
// in what the step generated, and in what the run stored of it, if it is
// persistent, so its records are in the state the run left them in, and in
// the shared records the step moved on; see shared.go.
func recordState(ctx context.Context, db *sql.DB, b *runBoard, step, object int64, state string) error {
	if _, err := db.ExecContext(ctx, "UPDATE run_objects SET state = ? WHERE step_id = ? AND object_ref = ?", state, step, object); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "UPDATE object_instances SET state = ? WHERE step_id = ? AND object_id = ?", state, step, object); err != nil {
		return err
	}
	return recordSharedState(ctx, db, step, b.store(object), state)
}

// Starts the walk of a run's top board with the persistent objects carried
// over: what the latest run to end done stored of each, or, before any did,
// an object existing but holding nothing, so the processes taking it run.
// Like an object no process generates, it isn't followed from the start:
// the process the start points at takes it, and one waiting only for it runs
// once the process moving it on did, on the new value. One that also waits
// for something ready sooner may run on the old one.
func (w *walk) carry() error {
	for _, o := range w.fr.board.Objects {
		// One given from outside holds what it was given.
		if o.Parent != nil || o.Owner != nil || !o.Persistent || o.Many || !w.generated[o.ID] || !w.onlyAffected(o.ID) || slices.Contains(w.given, o.ID) {
			continue
		}
		var content, state string
		err := w.e.db.QueryRowContext(w.ctx,
			`SELECT i.content, i.state FROM object_instances i JOIN runs r ON r.id = i.run_id
			WHERE i.object_id = ? AND r.status = ? ORDER BY r.finished_at DESC, r.id DESC LIMIT 1`, o.ID, runDone).
			Scan(&content, &state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		w.mu.Lock()
		if err != nil {
			w.values[o.ID] = value{}
			w.unstored[o.ID] = true
		} else {
			w.values[o.ID] = value{content: content, state: w.stateNamed(o.ID, state)}
		}
		w.mu.Unlock()
	}
	return nil
}

// Whether every process generating the object takes it too.
func (w *walk) onlyAffected(o int64) bool {
	for p, ends := range w.outputs {
		if slices.ContainsFunc(ends, func(e linkEnd) bool { return e.object == o }) &&
			!slices.ContainsFunc(w.inputs[p], func(e linkEnd) bool { return e.object == o }) {
			return false
		}
	}
	return true
}

// The object's state of that name, or none, such as for a state renamed or
// deleted since it was stored.
func (w *walk) stateNamed(o int64, name string) int64 {
	for id, n := range w.stateNames {
		if n == name && w.stateObjects[id] == o {
			return id
		}
	}
	return 0
}
