package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// A shared record's history: every change of it, which run's step made it
// or which person by hand, with the record as it stood after it, its fields
// and state, so each change says what the record became and, against the
// change before it, which state it came from and which fields changed. A
// record keeps only its latest self (shared.go); its history keeps the rest,
// shown newest first in the record's dialog, on the Records page and from a
// task board's card.
//
// Every write of a record notes its change in the same transaction, holding
// the record's row, so the history is in the order the record changed and
// goes back with the write when it fails (a step's duplicate, a conflict on
// a record its run took, a deadlock retried). A step's writing a record and
// the state its rules or an agent's decision then give it are one change,
// the step's; a write that changes nothing, such as a flow fetching the same
// data again, is none. Records there were before the history began start it
// as they stood then (found). A record deleted keeps its history, which
// ends noting who deleted it (records.go), though nobody reads it then.

// What kind of change a record had: added by a step or a person, written by
// a step, edited by a person by hand, found as it stood when its history
// began, or deleted by a person.
const (
	changeAdded   = "added"
	changeWritten = "written"
	changeEdited  = "edited"
	changeFound   = "found"
	changeDeleted = "deleted"
)

// How many changes a record's history lists, the latest first.
const maxRecordChanges = 500

// Who or what changed a record: a run's step, or a person by hand with step
// 0. The step's run, the run at its top, which a person, a schedule, a board
// or the API started, its process and kind, and the person deciding it, if a
// person did.
type recordChanger struct {
	run, top, step int64
	process, kind  string
	user           int64
}

// The step that changes a record, as a recordChanger.
func stepChanger(ctx context.Context, db querier, step int64) (recordChanger, error) {
	by := recordChanger{step: step}
	var user sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT run_id, process_name, kind, decided_by FROM run_steps WHERE id = ?", step).
		Scan(&by.run, &by.process, &by.kind, &user)
	if err != nil {
		return by, err
	}
	by.user = user.Int64
	by.top, err = topRun(ctx, db, by.run)
	return by, err
}

// The run at the top of run: run itself unless it is a subflow's, a flow
// process's item's or a follow-up's run inside another.
func topRun(ctx context.Context, db querier, run int64) (int64, error) {
	for seen := map[int64]bool{}; !seen[run]; {
		seen[run] = true
		var parent sql.NullInt64
		err := db.QueryRowContext(ctx, "SELECT parent_run_id FROM runs WHERE id = ?", run).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !parent.Valid) {
			return run, nil
		}
		if err != nil {
			return 0, err
		}
		run = parent.Int64
	}
	return run, nil
}

// Notes the change by of record, as it stands now, in its history; the
// caller holds the record's row in tx. The step's change it noted already,
// when it is the latest, takes this one in, such as the state the step's
// rules gave the record after it wrote it, and goes once the record is as it
// was before the step. Nothing is noted when the record is as its latest
// change left it, or was deleted.
func noteRecordChange(ctx context.Context, tx *sql.Tx, record int64, by recordChanger, kind string) error {
	var content, state string
	err := tx.QueryRowContext(ctx, "SELECT content, state FROM shared_records WHERE id = ? AND deleted_at IS NULL FOR UPDATE", record).Scan(&content, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	type change struct {
		id             int64
		content, state string
		step           sql.NullInt64
	}
	var last []change
	err = scanAll(ctx, tx, "SELECT id, content, state, step_id FROM record_changes WHERE record_id = ? ORDER BY id DESC LIMIT 2",
		func(rows *sql.Rows) error {
			var c change
			err := rows.Scan(&c.id, &c.content, &c.state, &c.step)
			last = append(last, c)
			return err
		}, record)
	if err != nil {
		return err
	}
	same := func(c change) bool { return c.state == state && sameContent(c.content, content) }
	if len(last) > 0 && by.step != 0 && last[0].step.Valid && last[0].step.Int64 == by.step {
		if len(last) > 1 && same(last[1]) {
			_, err := tx.ExecContext(ctx, "DELETE FROM record_changes WHERE id = ?", last[0].id)
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE record_changes SET content = ?, state = ?, changed_at = CURRENT_TIMESTAMP(3) WHERE id = ?",
			content, state, last[0].id)
		return err
	}
	if len(last) > 0 && same(last[0]) {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO record_changes (record_id, content, state, kind, run_id, top_run_id, step_id, process_name, step_kind, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record, content, state, kind, nullID(by.run), nullID(by.top), nullID(by.step), by.process, by.kind, nullID(by.user))
	return err
}

// Whether two contents of a record hold the same: alike as JSON, whatever
// their spacing and order of fields, or as text when either isn't JSON.
func sameContent(a, b string) bool {
	if a == b {
		return true
	}
	if !json.Valid([]byte(a)) || !json.Valid([]byte(b)) {
		return false
	}
	return sameJSON(json.RawMessage(a), json.RawMessage(b))
}

// A field a change changed: its value before, absent if the record didn't
// have it, and after, absent if the change took it out. Content that isn't
// fields changes as a whole, as a field with no name.
type fieldChange struct {
	Name   string          `json:"name"`
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
}

// The fields after changed from before, nil for a record's first change,
// whose fields are all new: the struct's own in order first, then the others
// by name.
func fieldChanges(before *string, after string, order []string) []fieldChange {
	changes := []fieldChange{}
	now, err := jsonObject(after)
	var was map[string]json.RawMessage
	if err == nil && before != nil {
		was, err = jsonObject(*before)
	}
	if err != nil {
		if before != nil && sameContent(*before, after) {
			return changes
		}
		c := fieldChange{After: json.RawMessage(asItem(after))}
		if before != nil {
			c.Before = json.RawMessage(asItem(*before))
		}
		return append(changes, c)
	}
	names := []string{}
	seen := map[string]bool{}
	for _, f := range order {
		_, has := now[f]
		_, had := was[f]
		if (has || had) && !seen[f] {
			names, seen[f] = append(names, f), true
		}
	}
	var rest []string
	for _, fields := range []map[string]json.RawMessage{was, now} {
		for f := range fields {
			if !seen[f] {
				rest, seen[f] = append(rest, f), true
			}
		}
	}
	slices.Sort(rest)
	for _, f := range append(names, rest...) {
		b, had := was[f]
		a, has := now[f]
		if had && has && sameJSON(b, a) {
			continue
		}
		changes = append(changes, fieldChange{Name: f, Before: b, After: a})
	}
	return changes
}

// A change of a record as its history lists it.
type recordChange struct {
	ID   int64     `json:"id"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// The state it came out in, and the one it was in before, null for its
	// first change.
	State string  `json:"state"`
	From  *string `json:"from"`
	// The fields that changed from the change before, all for its first.
	Changed []fieldChange `json:"changed"`
	// The record as it stood after the change.
	Content json.RawMessage `json:"content"`
	// The run that changed it, the one a person, a schedule, a board or the
	// API started, with its flow, null once the run is deleted or when the
	// person doesn't see the flow (hidden).
	Run      *int64 `json:"run"`
	Flow     *int64 `json:"flow"`
	FlowName string `json:"flowName"`
	Hidden   bool   `json:"hidden"`
	// The run inside it the step went in, when a subflow's or a flow
	// process's item's run made the change, with its flow.
	Inner         *int64 `json:"inner"`
	InnerFlow     *int64 `json:"innerFlow"`
	InnerFlowName string `json:"innerFlowName"`
	// The step's process and its kind (agent, script, connector, human),
	// empty when the person doesn't see the flow it ran in.
	Process     string `json:"process"`
	ProcessKind string `json:"processKind"`
	// The person who edited it by hand or decided the step, by name; empty
	// without sign-in, or when the person doesn't see the flow the step ran
	// in.
	By string `json:"by"`
}

// A record's history: the record as it stands now, the fields its struct
// has in the board's order, and its changes, the latest first; more are
// kept than these.
type recordChanges struct {
	Record  sharedRecord   `json:"record"`
	Fields  []string       `json:"fields"`
	Changes []recordChange `json:"changes"`
	More    bool           `json:"more"`
}

// The history of record, of store, as u sees it.
func loadRecordChanges(ctx context.Context, db *sql.DB, u *user, store, record int64) (recordChanges, error) {
	out := recordChanges{Changes: []recordChange{}}
	var err error
	if out.Record, err = sharedRecordByID(ctx, db, record); err != nil {
		return out, err
	}
	if out.Fields, err = recordFields(ctx, db, store); err != nil {
		return out, err
	}
	type row struct {
		change                       recordChange
		content                      string
		top, run, topFlow, innerFlow sql.NullInt64
	}
	var rows []row
	err = scanAll(ctx, db, `SELECT c.id, c.changed_at, c.kind, c.content, c.state, c.top_run_id, t.flow_id, COALESCE(tf.name, ''),
			c.run_id, r.flow_id, COALESCE(rf.name, ''), c.process_name, c.step_kind, COALESCE(NULLIF(u.name, ''), u.email, '')
		FROM record_changes c
		LEFT JOIN runs t ON t.id = c.top_run_id LEFT JOIN flows tf ON tf.id = t.flow_id
		LEFT JOIN runs r ON r.id = c.run_id LEFT JOIN flows rf ON rf.id = r.flow_id
		LEFT JOIN users u ON u.id = c.user_id
		WHERE c.record_id = ? ORDER BY c.id DESC LIMIT ?`,
		func(rs *sql.Rows) error {
			var x row
			c := &x.change
			err := rs.Scan(&c.ID, &c.At, &c.Kind, &x.content, &c.State, &x.top, &x.topFlow, &c.FlowName,
				&x.run, &x.innerFlow, &c.InnerFlowName, &c.Process, &c.ProcessKind, &c.By)
			rows = append(rows, x)
			return err
		}, record, maxRecordChanges+1)
	if err != nil {
		return out, err
	}
	// Whether the person sees each flow the runs went in.
	seen := map[int64]bool{}
	sees := func(flow sql.NullInt64) (bool, error) {
		if !flow.Valid {
			return false, nil
		}
		ok, known := seen[flow.Int64]
		if !known {
			access, err := flowAccess(ctx, db, u, flow.Int64)
			if err != nil {
				return false, err
			}
			ok = access != ""
			seen[flow.Int64] = ok
		}
		return ok, nil
	}
	ptr := func(n sql.NullInt64) *int64 {
		if !n.Valid {
			return nil
		}
		return &n.Int64
	}
	out.More = len(rows) > maxRecordChanges
	for i := range rows[:min(len(rows), maxRecordChanges)] {
		x := rows[i]
		c := x.change
		c.Content = json.RawMessage(asItem(x.content))
		var before *string
		if i+1 < len(rows) {
			before = &rows[i+1].content
			c.From = &rows[i+1].change.State
		}
		c.Changed = fieldChanges(before, x.content, out.Fields)
		top := x.top
		if !top.Valid {
			top, x.topFlow = x.run, x.innerFlow
		}
		c.Run = ptr(top)
		topSeen, err := sees(x.topFlow)
		if err != nil {
			return out, err
		}
		if topSeen {
			c.Flow = ptr(x.topFlow)
		} else {
			c.FlowName, c.Hidden = "", x.topFlow.Valid
		}
		// The step went in the top run, or in a run inside it.
		stepFlow := x.topFlow
		if x.run.Valid && x.run != top {
			c.Inner, stepFlow = ptr(x.run), x.innerFlow
			innerSeen, err := sees(x.innerFlow)
			if err != nil {
				return out, err
			}
			if innerSeen {
				c.InnerFlow = ptr(x.innerFlow)
			} else {
				c.InnerFlowName = ""
			}
		} else {
			c.InnerFlowName = ""
		}
		if ok, err := sees(stepFlow); err != nil {
			return out, err
		} else if !ok && stepFlow.Valid {
			c.Process, c.ProcessKind, c.By = "", "", ""
		}
		out.Changes = append(out.Changes, c)
	}
	return out, nil
}

func registerRecordChangeRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/structs/{id}/records/{record}/changes", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		record, err := strconv.ParseInt(r.PathValue("record"), 10, 64)
		if err != nil || record <= 0 {
			http.Error(w, "invalid record", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		store, msg, err := structStore(ctx, db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM shared_records WHERE id = ? AND object_id = ? AND deleted_at IS NULL", record, store).Scan(&n); err != nil {
			serverError(w, err)
			return
		}
		if n == 0 {
			http.Error(w, "no such record", http.StatusNotFound)
			return
		}
		out, err := loadRecordChanges(ctx, db, requestUser(r), store, record)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
