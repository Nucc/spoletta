package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// An object can pick a shared record: a copy of a shared struct's object on
// a flow's top board stands for the one record each run works on, such as
// "the pull request this run reviews". As a run starts it takes that record,
// the one it was started on (a person's Run… on a record, a task board), or
// else the one the pick selects of the store: in a state, whose fields hold
// a rule, the newest or the oldest. The record is then the object's value in
// the run, as an object given from outside is (see api.go), so every process
// linked to it gets the same record as it was when the run took it, however
// the store changes meanwhile, and a query of the store isn't needed.
//
// The run holds the record while it goes: no other run picks it, and a task
// board counts the run as going on it (see taskboard_play.go). Taking it is
// claimed under a lock of the record's row, so two runs starting at once
// take two records. A run finding none free isn't started.
//
// A process taking the object and generating it, OPM's effect, changes that
// record, whatever its unique attribute says (see shared.go): fields the step
// left as it was handed keep what the store holds now, so another's edit to
// them meanwhile stays, and a state someone else moved it to meanwhile fails
// the step rather than being moved back. A process only generating the
// object would change it blind, so the board refuses that (see problems.go);
// a flow adding new records generates another copy of the struct.
//
// Picking is a job flow's: a procedure's or a struct's object picks nothing,
// nor does an object inside a flow process or making up another. A run of a
// flow process or a subflow is the run it is in's, whose record its steps
// change too.

// How a run picks its record: in State, any when empty, whose fields hold
// Where, the latest written first unless Oldest.
type objectPick struct {
	State  string `json:"state,omitempty" yaml:"state,omitempty"`
	Where  string `json:"where,omitempty" yaml:"where,omitempty"`
	Oldest bool   `json:"oldest,omitempty" yaml:"oldest,omitempty"`
}

// The pick as stored: null for none.
func storedPick(p *objectPick) any {
	if p == nil {
		return nil
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// A stored pick; nil for none.
func pickOf(stored []byte) (*objectPick, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	var p objectPick
	return &p, json.Unmarshal(stored, &p)
}

// The pick as a query selecting the one record it takes, so the Records
// page, task boards and Run… on a record read it as they read queries.
func (p *objectPick) query() linkQuery {
	return linkQuery{State: p.State, Where: p.Where, Limit: 1, Oldest: p.Oldest}
}

// What's wrong with the pick alone, if anything.
func (p *objectPick) problem() string {
	if _, err := parseRule(p.Where); err != nil {
		return "the pick's rule: " + err.Error()
	}
	return ""
}

// What's wrong with the object picking, if anything: only a shared struct's
// copy on a job flow's top board picks, in one of its states.
func checkPick(ctx context.Context, db querier, object int64, p *objectPick) (string, error) {
	if p == nil {
		return "", nil
	}
	if msg := p.problem(); msg != "" {
		return msg, nil
	}
	var shared, top, many, structOrModule bool
	err := db.QueryRowContext(ctx, `SELECT o.store_id IS NOT NULL, o.parent_id IS NULL AND o.owner_id IS NULL, o.many, f.is_struct OR f.module
		FROM objects o JOIN flows f ON f.id = o.flow_id WHERE o.id = ?`, object).Scan(&shared, &top, &many, &structOrModule)
	if errors.Is(err, sql.ErrNoRows) {
		return "object not found", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case !shared:
		return "only a shared struct's object picks a record", nil
	case structOrModule:
		return "only a job flow's object picks a record; a procedure's runs are given what they take", nil
	case !top:
		return "only an object on the flow's top board picks a record", nil
	case many:
		return "a list picks no record; each run works on one", nil
	}
	if p.State != "" {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_states WHERE object_id = ? AND name = ?", object, p.State).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return fmt.Sprintf("the object has no state %s", quote(p.State)), nil
		}
	}
	return "", nil
}

// The sentence saying which record each run works on, such as the earliest
// stored record of "Ticket" in open.
func pickPhrase(object string, p *objectPick) string {
	which := "latest"
	if p.Oldest {
		which = "earliest"
	}
	s := fmt.Sprintf("Each run works on the %s stored record of %s", which, object)
	if p.State != "" {
		s += " in " + p.State
	}
	if p.Where != "" {
		s += " where " + p.Where
	}
	return s + ", which no other run takes meanwhile."
}

func registerPickRoutes(mux *http.ServeMux, db *sql.DB) {
	// Sets which record each run of the object's flow works on, or with null
	// none.
	mux.HandleFunc("PUT /api/objects/{id}/pick", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Pick *objectPick `json:"pick"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if msg, err := checkPick(r.Context(), db, id, body.Pick); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE objects SET pick = ? WHERE id = ?", storedPick(body.Pick), id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// What a step holding what a run took is called; see takeRecords.
const takenStep = "Taken from the records"

// None of an object's records free for a run to work on: none the pick
// selects, or held says each is held by a run going on.
type nothingToTake struct {
	object string
	pick   *objectPick
	held   bool
}

func (n *nothingToTake) Error() string {
	which := quote(n.object) + " record"
	if n.pick.State != "" {
		which += " in " + n.pick.State
	}
	if n.pick.Where != "" {
		which += " where " + n.pick.Where
	}
	if n.held {
		return fmt.Sprintf("every %s has a run working on it", which)
	}
	return fmt.Sprintf("no %s is there to work on", which)
}

// Takes the records a run of the flow works on, as it starts: for each
// object on its top board picking one, the record it was started on if that
// is of the object's store, else the first the pick selects that no run
// going on holds, claimed under a lock of its row. Records them as the
// run's, and as a step of what it was given, so a run taken over or re-run
// has them as it had; and gives them to nr to give its walk. The caller's
// transaction is read committed, so a record another run took before the
// lock was granted shows as held. A *nothingToTake if an object has none.
func takeRecords(ctx context.Context, tx *sql.Tx, flow int64, nr *pendingRun) error {
	var picking []snapshotObject
	for _, o := range nr.board.Objects {
		if picksRecord(o) {
			picking = append(picking, o)
		}
	}
	if len(picking) == 0 {
		return nil
	}
	var module bool
	if err := tx.QueryRowContext(ctx, "SELECT module FROM flows WHERE id = ?", flow).Scan(&module); err != nil || module {
		return err
	}
	type took struct {
		object       snapshotObject
		record       int64
		state, value string
	}
	var all []took
	for _, o := range picking {
		record, state, content, err := takeRecord(ctx, tx, nr.record, o)
		if err != nil {
			return err
		}
		all = append(all, took{object: o, record: record, state: state, value: content})
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO run_steps (run_id, process_name, kind, prompt, inputs, status, output, error, instructions, finished_at)
		VALUES (?, ?, ?, '', '[]', ?, '', '', '', CURRENT_TIMESTAMP(3))`,
		nr.id, takenStep, kindInput, stepDone)
	if err != nil {
		return err
	}
	step, err := res.LastInsertId()
	if err != nil {
		return err
	}
	nr.taken = map[int64]value{}
	for _, t := range all {
		if _, err := tx.ExecContext(ctx, "INSERT INTO taken_records (run_id, object_ref, record_id, state) VALUES (?, ?, ?, ?)",
			nr.id, t.object.ID, t.record, t.state); err != nil {
			return err
		}
		if err := insertRunObject(ctx, tx, nr.id, step, t.object.ID, t.object.Name, t.state, t.value); err != nil {
			return err
		}
		v := value{content: t.value}
		for _, st := range nr.board.States {
			if st.Object == t.object.ID && st.Name == t.state {
				v.state = st.ID
			}
		}
		nr.taken[t.object.ID] = v
	}
	return nil
}

// Whether a run takes a record for the object as it starts.
func picksRecord(o snapshotObject) bool {
	return o.Pick != nil && o.Store != nil && o.Parent == nil && o.Owner == nil && !o.Many
}

// The record a run takes for the object picking one: the one it was
// started on, if that is of the object's store, else the first the pick
// selects that no run going on holds; its id, state and content. A run
// started on a record takes it whoever else holds it, as a person picked
// it.
func takeRecord(ctx context.Context, tx *sql.Tx, on int64, o snapshotObject) (int64, string, string, error) {
	store := *o.Store
	if on != 0 {
		var of int64
		var state, content string
		err := tx.QueryRowContext(ctx, "SELECT object_id, state, content FROM shared_records WHERE id = ? FOR UPDATE", on).Scan(&of, &state, &content)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", "", fmt.Errorf("record %d, which the run was started on, was deleted", on)
		}
		if err != nil {
			return 0, "", "", err
		}
		if of == store {
			return on, state, content, nil
		}
	}
	where, err := parseRule(o.Pick.Where)
	if err != nil {
		return 0, "", "", fmt.Errorf("%s picks by a rule that doesn't read: %v", quote(o.Name), err)
	}
	order := "DESC"
	if o.Pick.Oldest {
		order = "ASC"
	}
	q := "SELECT id, content FROM shared_records WHERE object_id = ?"
	args := []any{store}
	if o.Pick.State != "" {
		q += " AND state = ?"
		args = append(args, o.Pick.State)
	}
	q += " ORDER BY updated_at " + order + ", id " + order
	selects := func(state, content string) bool {
		return (o.Pick.State == "" || state == o.Pick.State) && json.Valid([]byte(content)) && (len(where) == 0 || where.holds(content))
	}
	var candidates []int64
	err = scanAll(ctx, tx, q, func(rows *sql.Rows) error {
		var id int64
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return err
		}
		if selects(o.Pick.State, content) {
			candidates = append(candidates, id)
		}
		return nil
	}, args...)
	if err != nil {
		return 0, "", "", err
	}
	// Only the first that looks free is locked, and looked at again once
	// it is, so runs going through the records in different orders seldom
	// wait on each other.
	held := false
	for _, id := range candidates {
		if h, err := recordHeld(ctx, tx, id); err != nil || h {
			if err != nil {
				return 0, "", "", err
			}
			held = true
			continue
		}
		var state, content string
		err := tx.QueryRowContext(ctx, "SELECT state, content FROM shared_records WHERE id = ? FOR UPDATE", id).Scan(&state, &content)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, "", "", err
		}
		if !selects(state, content) {
			continue
		}
		h, err := recordHeld(ctx, tx, id)
		if err != nil {
			return 0, "", "", err
		}
		if h {
			held = true
			continue
		}
		return id, state, content, nil
	}
	return 0, "", "", &nothingToTake{object: o.Name, pick: o.Pick, held: held}
}

// Whether a run going on holds the record: one that took it, or was started
// on it.
func recordHeld(ctx context.Context, db querier, record int64) (bool, error) {
	var held bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM taken_records t JOIN runs r ON r.id = t.run_id WHERE t.record_id = ? AND r.status = ?)
		OR EXISTS (SELECT 1 FROM runs WHERE record_id = ? AND status = ?)`, record, runRunning, record, runRunning).Scan(&held)
	return held, err
}

// The record a run took for the object, if it or a run it is in did: the
// run that took it, the record and the state that run last left it in.
func takenBy(ctx context.Context, db querier, run, object int64) (t takenRecord, ok bool, err error) {
	for {
		err = db.QueryRowContext(ctx, "SELECT run_id, record_id, state FROM taken_records WHERE run_id = ? AND object_ref = ?", run, object).
			Scan(&t.run, &t.record, &t.state)
		if err == nil {
			return t, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return t, false, err
		}
		var parent sql.NullInt64
		if err := db.QueryRowContext(ctx, "SELECT parent_run_id FROM runs WHERE id = ?", run).Scan(&parent); err != nil || !parent.Valid {
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
			return t, false, err
		}
		run = parent.Int64
	}
}

// A record a run took.
type takenRecord struct {
	run, record int64
	state       string
}

// A record a step would change that changed under its run: deleted, or
// moved on by someone else. The step fails, saying so, and writes nothing.
type recordConflict struct{ msg string }

func (c *recordConflict) Error() string { return c.msg }

// Writes what a step changed of the record its run took for the object:
// the fields it changed from what it was handed, over what the record holds
// now, so another's edit to the others stays, moving it into state unless
// that is none. A *recordConflict if the record was deleted, or is no longer
// in the state the run last left it in.
func writeTaken(ctx context.Context, tx *sql.Tx, b *runBoard, t takenRecord, object, runID, stepID int64, attrs []attribute, content, state string) error {
	name := quote(b.objects[object].Name)
	store := b.store(object)
	var now, at string
	err := tx.QueryRowContext(ctx, "SELECT content, state FROM shared_records WHERE id = ? AND object_id = ? FOR UPDATE", t.record, store).Scan(&now, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return &recordConflict{fmt.Sprintf("The %s record this run works on (#%d) was deleted meanwhile, so what the step changed of it wasn't written.", name, t.record)}
	}
	if err != nil {
		return err
	}
	if at != t.state {
		return &recordConflict{fmt.Sprintf("The %s record this run works on (#%d) was moved from %s to %s by someone else meanwhile, so the step didn't write it.",
			name, t.record, stateWords(t.state), stateWords(at))}
	}
	var handed string
	var inputs []byte
	if err := tx.QueryRowContext(ctx, "SELECT inputs FROM run_steps WHERE id = ?", stepID).Scan(&inputs); err != nil {
		return err
	}
	var ins []runInput
	if json.Unmarshal(inputs, &ins) == nil {
		for _, in := range ins {
			if in.Name == b.objects[object].Name {
				handed = in.Content
			}
		}
	}
	if err := updateRecord(ctx, tx, store, t.record, runID, stepID, attrs, changedFields(handed, now, content), state, true); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE taken_records t JOIN shared_records sr ON sr.id = t.record_id SET t.state = sr.state WHERE t.run_id = ? AND t.object_ref = ?",
		t.run, object)
	return err
}

// A state's name as a sentence says it.
func stateWords(state string) string {
	if state == "" {
		return "no state"
	}
	return state
}

// What content changed of handed, over now: the fields content holds a
// value of other than handed's, with now's for the others. content itself
// unless all three are JSON objects.
func changedFields(handed, now, content string) string {
	was, err := jsonObject(handed)
	if err != nil {
		return content
	}
	is, err := jsonObject(now)
	if err != nil {
		return content
	}
	will, err := jsonObject(content)
	if err != nil {
		return content
	}
	for k, v := range will {
		if old, ok := was[k]; !ok || !sameJSON(old, v) {
			is[k] = v
		}
	}
	b, err := json.Marshal(is)
	if err != nil {
		return content
	}
	return string(b)
}

// Moves the record the step's run took for the object, if it took one, into
// state, as a person decided; see recordDecision.
func moveTaken(ctx context.Context, db *sql.DB, run, step, object int64, state string) error {
	t, ok, err := takenBy(ctx, db, run, object)
	if err != nil || !ok || state == "" {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE shared_records SET state = ?, run_id = ?, step_id = ?, edited_by = NULL, edited_at = NULL,
		updated_at = CURRENT_TIMESTAMP(3) WHERE id = ?`, state, run, step, t.record); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "UPDATE taken_records SET state = ? WHERE run_id = ? AND object_ref = ?", state, t.run, object)
	return err
}

// Keeps the state the step left the record its run took for the object in,
// once it is over, as the run's; see recordState.
func noteTaken(ctx context.Context, db *sql.DB, step, object int64) error {
	var run int64
	if err := db.QueryRowContext(ctx, "SELECT run_id FROM run_steps WHERE id = ?", step).Scan(&run); err != nil {
		return err
	}
	t, ok, err := takenBy(ctx, db, run, object)
	if err != nil || !ok {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE taken_records t JOIN shared_records sr ON sr.id = t.record_id SET t.state = sr.state
		WHERE t.run_id = ? AND t.object_ref = ? AND sr.step_id = ?`, t.run, object, step)
	return err
}

// A step's result as finishing it went: failed, saying why, if a record its
// run took changed meanwhile, as a step's own failure is.
func conflicted(res runResult, err error) (runResult, error) {
	var c *recordConflict
	if errors.As(err, &c) {
		res.OK, res.Error = false, c.msg
		return res, nil
	}
	return res, err
}
