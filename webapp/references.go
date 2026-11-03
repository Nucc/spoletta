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
	"strings"
)

// Records of one shared struct can refer to another's: a pull request names
// the ticket it is for. A struct's attribute refers to another shared
// struct's records (object_attributes.ref_id, that struct's object), holding
// the unique value of the one it means, such as the ticket's key, so what
// agents, scripts, setup files and searches already say of a record says it
// here too. A write naming a record the store doesn't hold is refused: a
// step writing it fails, a person's edit is turned away; a value left empty
// refers to nothing.
//
// An object on a job flow's top board, a copy of the struct referred to, can
// follow such a reference from an object picking a record (objects.follows),
// standing for the record the picked one names: "the ticket of the pull
// request this run works on". As the run starts and takes the picked record
// (see picks.go) it follows the reference and takes the record it names too,
// recorded in taken_records marked followed, so every process linked to the
// object gets that record, a process taking and generating it writes it by
// id, failing if someone else moved or deleted it meanwhile, and a run taken
// over or re-run has it as it had. A followed record isn't held: several
// runs may follow one ticket at once, each pull request's, and the conflict
// check stops one undoing another; no pick passes it by, and a task board
// doesn't count the run as going on it.
//
// The other way round, a record's dialog lists the records referring to it
// (GET /api/structs/{id}/records/{record}/references), read from their
// content, so a reference written before its record was there counts once
// it is.

// The reference an object follows: Attribute of Object's record, Object an
// object picking a record on the same board.
type objectFollow struct {
	Object    int64  `json:"object" yaml:"-"`
	Attribute string `json:"attribute" yaml:"attribute"`
}

// The follow as stored: null for none.
func storedFollow(f *objectFollow) any {
	if f == nil {
		return nil
	}
	b, _ := json.Marshal(f)
	return string(b)
}

// A stored follow; nil for none.
func followOf(stored []byte) (*objectFollow, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	var f objectFollow
	return &f, json.Unmarshal(stored, &f)
}

// Whether a run takes a record for the object as it starts by following a
// reference.
func followsRecord(o snapshotObject) bool {
	return o.Follows != nil && o.Pick == nil && o.Store != nil && o.Parent == nil && o.Owner == nil && !o.Many
}

// Whether a run holds a record for the object from its start, picked or
// followed.
func holdsRecord(o snapshotObject) bool {
	return picksRecord(o) || followsRecord(o)
}

// What's wrong with an attribute referring to ref's records, if anything:
// ref must be a shared struct's object, and a reference isn't the record's
// own key.
func checkRef(ctx context.Context, db querier, u *user, ref *int64, unique bool) (string, error) {
	if ref == nil {
		return "", nil
	}
	if unique {
		return "an attribute referring to records can't be unique too", nil
	}
	var shared sql.NullBool
	var flow int64
	err := db.QueryRowContext(ctx, `SELECT o.store_id = o.id, f.id FROM objects o JOIN flows f ON f.id = o.flow_id
		WHERE o.id = ? AND f.is_struct AND f.deleted_at IS NULL AND o.parent_id IS NULL AND o.owner_id IS NULL`, *ref).Scan(&shared, &flow)
	if errors.Is(err, sql.ErrNoRows) {
		return "an attribute refers to the records of a struct", nil
	}
	if err != nil {
		return "", err
	}
	// A struct the person doesn't see is none to them.
	if access, err := flowAccess(ctx, db, u, flow); err != nil || access == "" {
		if err != nil {
			return "", err
		}
		return "an attribute refers to the records of a struct", nil
	}
	if !shared.Bool {
		return "the struct isn't shared, so it keeps no records to refer to", nil
	}
	return "", nil
}

// The attribute of a struct's object called name, if it is one referring to
// records: its id and the struct's object referred to.
func refAttribute(ctx context.Context, db querier, object int64, name string) (int64, int64, error) {
	var id int64
	var ref sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT id, ref_id FROM object_attributes WHERE object_id = ? AND name = ?", object, name).Scan(&id, &ref)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	return id, ref.Int64, err
}

// What's wrong with the object following f, if anything: a shared struct's
// copy on a job flow's top board follows a reference to its struct, held by
// another object on that board picking a record.
func checkFollow(ctx context.Context, db querier, object int64, f *objectFollow) (string, error) {
	if f == nil {
		return "", nil
	}
	if strings.TrimSpace(f.Attribute) == "" {
		return "name the attribute holding the reference", nil
	}
	var flow int64
	var store, pick sql.NullInt64
	var top, many, structOrModule, picks bool
	err := db.QueryRowContext(ctx, `SELECT o.flow_id, o.store_id, o.parent_id IS NULL AND o.owner_id IS NULL, o.many, f.is_struct OR f.module, o.pick IS NOT NULL
		FROM objects o JOIN flows f ON f.id = o.flow_id WHERE o.id = ?`, object).Scan(&flow, &store, &top, &many, &structOrModule, &picks)
	if errors.Is(err, sql.ErrNoRows) {
		return "object not found", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case !store.Valid:
		return "only a shared struct's object follows a reference", nil
	case structOrModule:
		return "only a job flow's object follows a reference; a procedure's runs are given what they take", nil
	case !top:
		return "only an object on the flow's top board follows a reference", nil
	case many:
		return "a list follows no reference; it names one record", nil
	case picks:
		return "an object picking a record follows no reference too; stop it picking first", nil
	case f.Object == object:
		return "an object follows a reference another object holds", nil
	}
	var ofFlow int64
	var ofTop, ofPicks bool
	err = db.QueryRowContext(ctx, "SELECT flow_id, parent_id IS NULL AND owner_id IS NULL, pick IS NOT NULL, store_id FROM objects WHERE id = ?", f.Object).
		Scan(&ofFlow, &ofTop, &ofPicks, &pick)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && ofFlow != flow) {
		return "the object holding the reference isn't on the flow's board", nil
	}
	if err != nil {
		return "", err
	}
	if !ofTop || !ofPicks {
		return "follow a reference of an object picking the record each run works on", nil
	}
	_, ref, err := refAttribute(ctx, db, pick.Int64, f.Attribute)
	if err != nil {
		return "", err
	}
	if ref != store.Int64 {
		return fmt.Sprintf("the object holding it has no attribute %s referring to this struct's records", quote(f.Attribute)), nil
	}
	return "", nil
}

// The sentence saying which record the object stands for, such as Ticket is
// the Jira Ticket record that Pull Request's ticket names.
func followPhrase(object, of string, f *objectFollow) string {
	return fmt.Sprintf("%s is the record that %s's %s names.", object, of, f.Attribute)
}

// The value a record's field holds as a reference, "" for none.
func refValue(content, field string) string {
	fields, err := jsonObject(content)
	if err != nil {
		return ""
	}
	raw, ok := fields[field]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// The record of store a reference names: the one holding value as one of
// its unique values; 0 if none does.
func referredRecord(ctx context.Context, db rowQuerier, store int64, value string, lock bool) (int64, error) {
	attrs, err := uniqueAttributes(ctx, db, store)
	if err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(value)
	hash, ok := keyOf(raw)
	if !ok {
		return 0, nil
	}
	q := "SELECT k.record_id FROM shared_keys k JOIN shared_records sr ON sr.id = k.record_id AND sr.deleted_at IS NULL WHERE k.object_id = ? AND k.attribute_id = ? AND k.value_hash = ?"
	if lock {
		q += " FOR UPDATE"
	}
	for _, a := range attrs {
		var record int64
		err := db.QueryRowContext(ctx, q, store, a.ID, hash).Scan(&record)
		if err == nil {
			return record, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	// A number written as one names the record holding it as a number.
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		if hash, ok := keyOf(json.RawMessage(value)); ok {
			for _, a := range attrs {
				var record int64
				err := db.QueryRowContext(ctx, q, store, a.ID, hash).Scan(&record)
				if err == nil {
					return record, nil
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return 0, err
				}
			}
		}
	}
	return 0, nil
}

// The attributes of a store referring to records, each with the store it
// refers to.
type refField struct {
	name  string
	store int64
}

func refFields(ctx context.Context, db rowQuerier, store int64) ([]refField, error) {
	var fields []refField
	err := scanAll(ctx, db, "SELECT name, ref_id FROM object_attributes WHERE object_id = ? AND ref_id IS NOT NULL ORDER BY id", func(rows *sql.Rows) error {
		var f refField
		err := rows.Scan(&f.name, &f.store)
		fields = append(fields, f)
		return err
	}, store)
	return fields, err
}

// Why content can't be written to store, if a reference it holds names no
// record: "" when each names one or is empty.
func danglingRefs(ctx context.Context, db rowQuerier, store int64, content string) (string, error) {
	fields, err := refFields(ctx, db, store)
	if err != nil || len(fields) == 0 {
		return "", err
	}
	for _, f := range fields {
		v := refValue(content, f.name)
		if v == "" {
			continue
		}
		record, err := referredRecord(ctx, db, f.store, v, false)
		if err != nil {
			return "", err
		}
		if record == 0 {
			var name string
			if err := db.QueryRowContext(ctx, "SELECT name FROM objects WHERE id = ?", f.store).Scan(&name); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return "", err
			}
			return fmt.Sprintf("%s names %s, but no %s record holds it", f.name, quote(v), quote(name)), nil
		}
	}
	return "", nil
}

// A *recordConflict failing the step writing content to store if a
// reference it holds, or an item of it does, names no record.
func refusedRefs(ctx context.Context, db rowQuerier, store int64, content string) error {
	items := []string{content}
	if raw, err := jsonArray(content); err == nil {
		items = items[:0]
		for _, r := range raw {
			items = append(items, string(r))
		}
	}
	for _, item := range items {
		msg, err := danglingRefs(ctx, db, store, item)
		if err != nil {
			return err
		}
		if msg != "" {
			return &recordConflict{"The step's records weren't written: " + msg + "."}
		}
	}
	return nil
}

// A followed reference that names no record a run can take: empty, or
// naming a record the store doesn't hold.
func noFollowed(object, of string, f *objectFollow, value string) error {
	if value == "" {
		return &nothingToTake{msg: fmt.Sprintf("the %s record this run works on names no %s in %s, so %s has no record to follow", quote(of), quote(object), f.Attribute, quote(object))}
	}
	return &nothingToTake{msg: fmt.Sprintf("the %s record this run works on names %s in %s, which no %s record holds", quote(of), quote(value), f.Attribute, quote(object))}
}

// Takes the records the objects following a reference stand for, once the
// records the run picked are taken: for each, the record the picked one's
// attribute names, locked while its row is read, but held by no one. A
// *nothingToTake if one names none.
func followRecords(ctx context.Context, tx *sql.Tx, nr *pendingRun, picked map[int64]string, step int64) error {
	for _, o := range nr.board.Objects {
		if !followsRecord(o) {
			continue
		}
		content, ok := picked[o.Follows.Object]
		if !ok {
			return &nothingToTake{msg: fmt.Sprintf("%s follows a reference of an object that picks no record, so the run has none to follow", quote(o.Name))}
		}
		var of string
		for _, p := range nr.board.Objects {
			if p.ID == o.Follows.Object {
				of = p.Name
			}
		}
		key := refValue(content, o.Follows.Attribute)
		if key == "" {
			return noFollowed(o.Name, of, o.Follows, key)
		}
		record, err := referredRecord(ctx, tx, *o.Store, key, true)
		if err != nil {
			return err
		}
		if record == 0 {
			return noFollowed(o.Name, of, o.Follows, key)
		}
		var state, got string
		if err := tx.QueryRowContext(ctx, "SELECT state, content FROM shared_records WHERE id = ?", record).Scan(&state, &got); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO taken_records (run_id, object_ref, record_id, state, followed) VALUES (?, ?, ?, ?, TRUE)",
			nr.id, o.ID, record, state); err != nil {
			return err
		}
		if err := insertRunObject(ctx, tx, nr.id, step, o.ID, o.Name, state, got); err != nil {
			return err
		}
		v := value{content: got}
		for _, st := range nr.board.States {
			if st.Object == o.ID && st.Name == state {
				v.state = st.ID
			}
		}
		nr.taken[o.ID] = v
	}
	return nil
}

// A record referring to another, as its dialog lists them.
type referringRecord struct {
	ID      int64           `json:"id"`
	Content json.RawMessage `json:"content"`
	State   string          `json:"state"`
	// Whether a run is going on it.
	Going bool `json:"going"`
}

// The records of one struct referring to a record by one attribute.
type referringGroup struct {
	Struct    int64  `json:"struct"`
	Name      string `json:"name"`
	Attribute string `json:"attribute"`
	// The value they hold naming the record, to search for them by.
	Key     string            `json:"key"`
	Records []referringRecord `json:"records"`
	// Whether more refer to it than are listed.
	More bool `json:"more"`
}

// How many referring records of each kind a dialog lists at most.
const referringShown = 200

// The records referring to the record of store, by struct and attribute,
// of the structs the person sees.
func referringRecords(ctx context.Context, db *sql.DB, u *user, store, record int64) ([]referringGroup, error) {
	var keys []string
	var content string
	if err := db.QueryRowContext(ctx, "SELECT content FROM shared_records WHERE id = ?", record).Scan(&content); err != nil {
		return nil, err
	}
	attrs, err := uniqueAttributes(ctx, db, store)
	if err != nil {
		return nil, err
	}
	for _, a := range attrs {
		if v := refValue(content, a.Name); v != "" && !slices.Contains(keys, v) {
			keys = append(keys, v)
		}
	}
	groups := []referringGroup{}
	if len(keys) == 0 {
		return groups, nil
	}
	see, args := visibleFlows(u)
	type field struct {
		flow, object int64
		name, attr   string
	}
	var fields []field
	err = scanAll(ctx, db, `SELECT f.id, o.id, f.name, a.name FROM object_attributes a JOIN objects o ON o.id = a.object_id AND o.store_id = o.id
		JOIN flows f ON f.id = o.flow_id AND f.is_struct WHERE a.ref_id = ? AND `+see+` ORDER BY f.name, a.id`, func(rows *sql.Rows) error {
		var x field
		err := rows.Scan(&x.flow, &x.object, &x.name, &x.attr)
		fields = append(fields, x)
		return err
	}, append([]any{store}, args...)...)
	if err != nil {
		return nil, err
	}
	for _, x := range fields {
		g := referringGroup{Struct: x.flow, Name: x.name, Attribute: x.attr, Key: keys[0], Records: []referringRecord{}}
		path := "$." + strconv.Quote(x.attr)
		in := placeholders(len(keys))
		q := `SELECT sr.id, sr.content, sr.state,
				EXISTS (SELECT 1 FROM runs r WHERE r.record_id = sr.id AND r.status = ?)
				OR EXISTS (SELECT 1 FROM taken_records t JOIN runs r ON r.id = t.run_id WHERE t.record_id = sr.id AND NOT t.followed AND r.status = ?)
			FROM shared_records sr WHERE sr.object_id = ? AND sr.deleted_at IS NULL AND JSON_VALID(sr.content)
				AND CAST(JSON_UNQUOTE(JSON_EXTRACT(sr.content, ?)) AS CHAR) IN ` + in + `
			ORDER BY sr.updated_at DESC, sr.id DESC LIMIT ?`
		qargs := []any{runRunning, runRunning, x.object, path}
		for _, k := range keys {
			qargs = append(qargs, k)
		}
		qargs = append(qargs, referringShown+1)
		err := scanAll(ctx, db, q, func(rows *sql.Rows) error {
			var rr referringRecord
			var c string
			if err := rows.Scan(&rr.ID, &c, &rr.State, &rr.Going); err != nil {
				return err
			}
			rr.Content = json.RawMessage(c)
			g.Records = append(g.Records, rr)
			return nil
		}, qargs...)
		if err != nil {
			return nil, err
		}
		if len(g.Records) > referringShown {
			g.Records, g.More = g.Records[:referringShown], true
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func registerReferenceRoutes(mux *http.ServeMux, db *sql.DB) {
	// Sets the reference the object follows, or with null none.
	mux.HandleFunc("PUT /api/objects/{id}/follows", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Follows *objectFollow `json:"follows"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if msg, err := checkFollow(r.Context(), db, id, body.Follows); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE objects SET follows = ? WHERE id = ?", storedFollow(body.Follows), id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// The records referring to a record, by struct and attribute.
	mux.HandleFunc("GET /api/structs/{id}/records/{record}/references", func(w http.ResponseWriter, r *http.Request) {
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
		groups, err := referringRecords(ctx, db, requestUser(r), store, record)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, groups)
	})
}

// The fields of a store referring to another struct's records, each with
// that struct's id.
func refStructs(ctx context.Context, db rowQuerier, store int64) (map[string]int64, error) {
	refs := map[string]int64{}
	err := scanAll(ctx, db, `SELECT a.name, o.flow_id FROM object_attributes a JOIN objects o ON o.id = a.ref_id
		WHERE a.object_id = ? ORDER BY a.id`, func(rows *sql.Rows) error {
		var name string
		var flow int64
		err := rows.Scan(&name, &flow)
		refs[name] = flow
		return err
	}, store)
	return refs, err
}

// The records a field refers to, by each of their unique values, for a
// search to reach through it.
type referredTable struct {
	field   string
	records map[string]referred
}

type referred struct {
	state  string
	fields map[string]any
}

// The tables of records the fields of store refer to, read once per field.
func referredTables(ctx context.Context, db *sql.DB, store int64) ([]referredTable, error) {
	fields, err := refFields(ctx, db, store)
	if err != nil {
		return nil, err
	}
	var out []referredTable
	for _, f := range fields {
		attrs, err := uniqueAttributes(ctx, db, f.store)
		if err != nil {
			return nil, err
		}
		rt := referredTable{field: f.name, records: map[string]referred{}}
		err = scanAll(ctx, db, "SELECT content, state FROM shared_records WHERE object_id = ? AND deleted_at IS NULL ORDER BY updated_at DESC, id DESC LIMIT ?",
			func(rows *sql.Rows) error {
				var content, state string
				if err := rows.Scan(&content, &state); err != nil {
					return err
				}
				fields, _ := contentValue(content).(map[string]any)
				for _, a := range attrs {
					if v := refValue(content, a.Name); v != "" {
						if _, seen := rt.records[v]; !seen {
							rt.records[v] = referred{state: state, fields: fields}
						}
					}
				}
				return nil
			}, f.store, searchedRows)
		if err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, nil
}

// How many records of one struct refer to a record by one attribute, in
// all and in each state, as a task board's card counts them.
type referringCount struct {
	Name      string       `json:"name"`
	Attribute string       `json:"attribute"`
	Total     int          `json:"total"`
	States    []stateCount `json:"states"`
}

type stateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// The records referring to those of a store, counted by the value naming
// each, for every struct the person sees referring to it.
type referringCounts struct {
	// The store's unique attributes, whose values name a record.
	keys   []string
	groups []referringCountGroup
}

type referringCountGroup struct {
	name, attribute string
	byKey           map[string]*referringCount
}

// Counts what refers to the records of store, a query per struct and
// attribute referring to it.
func countReferring(ctx context.Context, db *sql.DB, u *user, store int64) (*referringCounts, error) {
	rc := &referringCounts{}
	attrs, err := uniqueAttributes(ctx, db, store)
	if err != nil {
		return nil, err
	}
	for _, a := range attrs {
		rc.keys = append(rc.keys, a.Name)
	}
	see, args := visibleFlows(u)
	type field struct {
		object     int64
		name, attr string
	}
	var fields []field
	err = scanAll(ctx, db, `SELECT o.id, f.name, a.name FROM object_attributes a JOIN objects o ON o.id = a.object_id AND o.store_id = o.id
		JOIN flows f ON f.id = o.flow_id AND f.is_struct WHERE a.ref_id = ? AND `+see+` ORDER BY f.name, a.id`, func(rows *sql.Rows) error {
		var x field
		err := rows.Scan(&x.object, &x.name, &x.attr)
		fields = append(fields, x)
		return err
	}, append([]any{store}, args...)...)
	if err != nil {
		return nil, err
	}
	for _, x := range fields {
		g := referringCountGroup{name: x.name, attribute: x.attr, byKey: map[string]*referringCount{}}
		err := scanAll(ctx, db, `SELECT k, state, COUNT(*) FROM (
				SELECT CAST(JSON_UNQUOTE(JSON_EXTRACT(content, ?)) AS CHAR) AS k, state FROM shared_records
				WHERE object_id = ? AND deleted_at IS NULL AND JSON_VALID(content)) refs
			WHERE k IS NOT NULL AND k <> '' GROUP BY k, state ORDER BY k, state`, func(rows *sql.Rows) error {
			var key, state string
			var n int
			if err := rows.Scan(&key, &state, &n); err != nil {
				return err
			}
			key = strings.TrimSpace(key)
			c := g.byKey[key]
			if c == nil {
				c = &referringCount{Name: x.name, Attribute: x.attr, States: []stateCount{}}
				g.byKey[key] = c
			}
			c.Total += n
			c.States = append(c.States, stateCount{State: state, Count: n})
			return nil
		}, "$."+strconv.Quote(x.attr), x.object)
		if err != nil {
			return nil, err
		}
		rc.groups = append(rc.groups, g)
	}
	return rc, nil
}

// What refers to the record with content, by struct and attribute.
func (rc *referringCounts) of(content json.RawMessage) []referringCount {
	if rc == nil {
		return nil
	}
	var out []referringCount
	for _, g := range rc.groups {
		for _, k := range rc.keys {
			if c, ok := g.byKey[refValue(string(content), k)]; ok {
				out = append(out, *c)
				break
			}
		}
	}
	return out
}
