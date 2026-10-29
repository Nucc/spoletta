package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"
)

// A struct's object can be shared: every copy of it, in whatever flow,
// reads and writes one store of records, as a table every flow using it
// has. A flow fetching exceptions from Sentry adds them, and another flow
// queries those in a state, works on them and moves them on, such as to
// completed; which ones get worked on, or worked on again, is up to the
// query and the states the processes leave them in.
//
// A record is told apart by the struct's unique attributes: what a step
// generates holding a unique value a record holds is that record, and what
// holds none is a record of its own. A process that only generates the
// object adds the records that are new and refreshes the fields of those
// that aren't, leaving their state alone, so fetching an exception again
// doesn't undo how far it got. A process that also takes it, by any input
// link or a query, moves the records it generates into the state it comes
// out in. A list holds a record per item, all in the state the list comes
// out in.
//
// Records belong to the struct, not to the runs writing them: they are
// written once the step writing them is done and stay whatever its run
// does later, and no run claims them, so two runs querying the same state
// at once may both work on one record.
//
// People correct records by hand as well, adding, changing and deleting
// them (records.go).
//
// Whether a struct's object is shared is its own, and each copy follows it,
// as with what else it has (see modules.go): the copy's store is the
// struct's object. A shared object isn't persistent too, which would store
// it in each flow's runs besides.

// What's wrong with sharing a struct's object, if anything: records are
// told apart by a unique attribute, and a copy already stored in its runs
// would be stored twice.
func checkShared(ctx context.Context, db *sql.DB, object int64) (string, error) {
	var unique, stored int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_attributes WHERE object_id = ? AND is_unique", object).Scan(&unique)
	if err != nil {
		return "", err
	}
	if unique == 0 {
		return "records are told apart by a unique attribute; mark one unique before sharing the object", nil
	}
	// Copies of it in a procedure have copies of their own in turn.
	err = db.QueryRowContext(ctx, `WITH RECURSIVE copies (id) AS (
			SELECT id FROM objects WHERE source_id = ?
			UNION SELECT o.id FROM objects o JOIN copies c ON o.source_id = c.id)
		SELECT COUNT(*) FROM objects o JOIN copies c ON c.id = o.id WHERE o.persistent`, object).Scan(&stored)
	if err != nil {
		return "", err
	}
	if stored > 0 {
		return "a copy of the object is stored in its flow's runs; turn that off before sharing it", nil
	}
	return "", nil
}

// Writes the records of shared objects a step generated, as the run's board
// has them, into their stores. A copy of a struct deleted since writes none,
// having no unique attribute to tell them apart by; it still reads what was
// written.
func storeShared(ctx context.Context, tx *sql.Tx, b *runBoard, runID, stepID int64) error {
	type generated struct {
		store          int64
		affects        bool
		content, state string
	}
	var objects []generated
	var process sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT process_ref FROM run_steps WHERE id = ?", stepID).Scan(&process); err != nil {
		return err
	}
	err := scanAll(ctx, tx, "SELECT object_ref, content, state FROM run_objects WHERE step_id = ? ORDER BY object_ref, id",
		func(rows *sql.Rows) error {
			var object int64
			var g generated
			if err := rows.Scan(&object, &g.content, &g.state); err != nil {
				return err
			}
			if g.store = b.store(object); g.store != 0 {
				g.affects = process.Valid && b.takes(process.Int64, object)
				objects = append(objects, g)
			}
			return nil
		}, stepID)
	if err != nil {
		return err
	}
	// A store whose struct is gone keeps no records.
	kept := objects[:0]
	for _, g := range objects {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM objects WHERE id = ?", g.store).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			kept = append(kept, g)
		}
	}
	objects = kept
	slices.SortStableFunc(objects, func(x, y generated) int { return cmp.Compare(x.store, y.store) })
	for _, g := range objects {
		attrs, err := uniqueAttributes(ctx, tx, g.store)
		if err != nil {
			return err
		}
		items := []string{stripFence(g.content)}
		// A list on its own board holds its items; inside a flow process
		// running for each, it is the item. An agent reviewing records it
		// queried hands them back as they came, a list too.
		if raw, err := jsonArray(items[0]); err == nil {
			items = items[:0]
			for _, r := range raw {
				items = append(items, string(r))
			}
		}
		for _, item := range items {
			if err := writeRecord(ctx, tx, g.store, runID, stepID, attrs, item, g.state, g.affects); err != nil {
				return err
			}
		}
	}
	return nil
}

// Writes one record of a store: the record holding one of its unique
// values, or a new one. A step that took the object moves the record into
// state and makes it the step's, so the state the step comes out in later
// reaches it too; see recordState.
func writeRecord(ctx context.Context, tx *sql.Tx, store, runID, stepID int64, attrs []attribute, content, state string, affects bool) error {
	// Another step adding the same new record first makes this one find it.
	for attempt := 1; ; attempt++ {
		record, err := recordHolding(ctx, tx, store, attrs, content)
		if err != nil {
			return err
		}
		if record != 0 {
			return updateRecord(ctx, tx, store, record, runID, stepID, attrs, content, state, affects)
		}
		if _, err := tx.ExecContext(ctx, "SAVEPOINT shared_record"); err != nil {
			return err
		}
		err = insertRecord(ctx, tx, store, runID, stepID, attrs, content, state)
		if !mysqlErrorIs(err, mysqlErrDuplicate) || attempt == 2 {
			return err
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT shared_record"); err != nil {
			return err
		}
	}
}

// The unique values content holds, by attribute, hashed as keyOf does.
func recordKeys(attrs []attribute, content string) map[int64]string {
	keys := map[int64]string{}
	fields, err := jsonObject(content)
	if err != nil {
		return keys
	}
	for _, a := range attrs {
		if hash, ok := keyOf(fields[a.Name]); ok {
			keys[a.ID] = hash
		}
	}
	return keys
}

// The record of the store holding one of content's unique values, 0 if none.
func recordHolding(ctx context.Context, tx *sql.Tx, store int64, attrs []attribute, content string) (int64, error) {
	keys := recordKeys(attrs, content)
	for _, a := range attrs {
		hash, ok := keys[a.ID]
		if !ok {
			continue
		}
		var record int64
		err := tx.QueryRowContext(ctx, "SELECT record_id FROM shared_keys WHERE object_id = ? AND attribute_id = ? AND value_hash = ? FOR UPDATE",
			store, a.ID, hash).Scan(&record)
		if err == nil {
			return record, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	return 0, nil
}

func insertRecord(ctx context.Context, tx *sql.Tx, store, runID, stepID int64, attrs []attribute, content, state string) error {
	res, err := tx.ExecContext(ctx, "INSERT INTO shared_records (object_id, content, state, run_id, step_id) VALUES (?, ?, ?, ?, ?)",
		store, content, state, runID, stepID)
	if err != nil {
		return err
	}
	record, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, a := range attrs {
		if hash, ok := recordKeys(attrs, content)[a.ID]; ok {
			_, err := tx.ExecContext(ctx, "INSERT INTO shared_keys (object_id, attribute_id, value_hash, record_id) VALUES (?, ?, ?, ?)", store, a.ID, hash, record)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Refreshes a record with the fields content has, keeping those it leaves
// out, and with affects moves it into state, unless that is none.
func updateRecord(ctx context.Context, tx *sql.Tx, store, record, runID, stepID int64, attrs []attribute, content, state string, affects bool) error {
	var old string
	if err := tx.QueryRowContext(ctx, "SELECT content FROM shared_records WHERE id = ? FOR UPDATE", record).Scan(&old); err != nil {
		return err
	}
	content = mergeFields(old, content)
	q := "UPDATE shared_records SET content = ?, updated_at = CURRENT_TIMESTAMP(3) WHERE id = ?"
	args := []any{content, record}
	if affects {
		q = `UPDATE shared_records SET content = ?, state = IF(? = '', state, ?), run_id = ?, step_id = ?, edited_by = NULL, edited_at = NULL,
			updated_at = CURRENT_TIMESTAMP(3) WHERE id = ?`
		args = []any{content, state, state, runID, stepID, record}
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	// A unique value another record holds stays that record's.
	if _, err := tx.ExecContext(ctx, "DELETE FROM shared_keys WHERE record_id = ?", record); err != nil {
		return err
	}
	for id, hash := range recordKeys(attrs, content) {
		_, err := tx.ExecContext(ctx, "INSERT IGNORE INTO shared_keys (object_id, attribute_id, value_hash, record_id) VALUES (?, ?, ?, ?)", store, id, hash, record)
		if err != nil {
			return err
		}
	}
	return nil
}

// A record's fields with those of content over them; content itself
// unless both are JSON objects.
func mergeFields(old, content string) string {
	was, err := jsonObject(old)
	if err != nil {
		return content
	}
	now, err := jsonObject(content)
	if err != nil {
		return content
	}
	for k, v := range now {
		was[k] = v
	}
	b, err := json.Marshal(was)
	if err != nil {
		return content
	}
	return string(b)
}

// Moves the records of store the step made its own into the state it came
// out in.
func recordSharedState(ctx context.Context, db *sql.DB, step, store int64, state string) error {
	if state == "" || store == 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, "UPDATE shared_records SET state = ? WHERE step_id = ? AND object_id = ?", state, step, store)
	return err
}

// The store an object reads and writes, 0 if it isn't shared.
func storeOf(ctx context.Context, db querier, object int64) (int64, error) {
	var store sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT store_id FROM objects WHERE id = ?", object).Scan(&store)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return store.Int64, err
}

// The records of a store the query selects, as a list of it, the latest
// written first, or the earliest with oldest.
func querySharedRecords(ctx context.Context, db *sql.DB, store int64, q *linkQuery) (string, error) {
	where, err := parseRule(q.Where)
	if err != nil {
		return "", err
	}
	order := "DESC"
	if q.Oldest {
		order = "ASC"
	}
	args := []any{store}
	state := ""
	if q.State != "" {
		state = " AND state = ?"
		args = append(args, q.State)
	}
	rows, err := db.QueryContext(ctx,
		"SELECT content FROM shared_records WHERE object_id = ?"+state+" ORDER BY updated_at "+order+", id "+order, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	records := []json.RawMessage{}
	for len(records) < q.limit() && rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return "", err
		}
		if !json.Valid([]byte(content)) || (len(where) > 0 && !where.holds(content)) {
			continue
		}
		records = append(records, json.RawMessage(content))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	b, err := json.Marshal(records)
	return string(b), err
}

// A record as the struct's records list it.
type sharedRecord struct {
	ID      int64           `json:"id"`
	Content json.RawMessage `json:"content"`
	State   string          `json:"state"`
	// The run that last moved it on, or added it, and that run's flow; null
	// once that run is deleted.
	Run     *int64    `json:"run"`
	RunFlow *int64    `json:"runFlow"`
	Updated time.Time `json:"updated"`
	// Whether a person corrected it since a run last moved it on, and who,
	// by name, empty without sign-in; see records.go.
	Edited   bool   `json:"edited"`
	EditedBy string `json:"editedBy"`
}

// The columns sharedRecordFrom scans.
const sharedRecordColumns = `sr.id, sr.content, sr.state, sr.run_id, ru.flow_id, sr.updated_at, sr.edited_at IS NOT NULL, COALESCE(NULLIF(eu.name, ''), eu.email, '')
	FROM shared_records sr LEFT JOIN runs ru ON ru.id = sr.run_id LEFT JOIN users eu ON eu.id = sr.edited_by`

// A record as sharedRecordColumns read it, with its content as stored.
func sharedRecordFrom(rows interface{ Scan(...any) error }) (sharedRecord, string, error) {
	var rec sharedRecord
	var content string
	err := rows.Scan(&rec.ID, &content, &rec.State, &rec.Run, &rec.RunFlow, &rec.Updated, &rec.Edited, &rec.EditedBy)
	rec.Content = json.RawMessage(asItem(content))
	return rec, content, err
}

func sharedRecordByID(ctx context.Context, db querier, id int64) (sharedRecord, error) {
	rec, _, err := sharedRecordFrom(db.QueryRowContext(ctx, "SELECT "+sharedRecordColumns+" WHERE sr.id = ?", id))
	return rec, err
}

// A shared struct's records, the latest written first or as sorted, with
// the fields its object has in the board's order: its attributes, then the
// objects it is made of, so a table of them has its columns before any
// record does. A search (search.go) picks which, over every stored record:
// by id, state, updated and run, and each field as @field.
type sharedRecords struct {
	Fields []string `json:"fields"`
	// The states a record can be in, in the board's order.
	States  []string       `json:"states"`
	Records []sharedRecord `json:"records"`
	// More records than these match.
	More bool `json:"more"`
	// The facets the records were searched by; how many records the search
	// matched among how many it read; and whether more are stored than a
	// search reads.
	Facets    []facet `json:"facets"`
	Matched   int     `json:"matched"`
	Searched  int     `json:"searched"`
	Truncated bool    `json:"truncated"`
}

// How many records the struct's records list, the latest written first.
const maxListedRecords = 1000

// A shared struct's records ready to be searched: the fields and states its
// object has, in the board's order, the latest records it stores, at most
// searchedRows of them, and what each can be searched by. One is read for
// as many searches of the struct as need it, such as a task board's
// columns (taskboards.go).
type recordTable struct {
	fields, states []string
	records        []sharedRecord
	table          *searchTable
	// More records are stored than these.
	truncated bool
}

// The fields the records of the struct whose object is root have, in the
// board's order: its attributes, then the objects it is made of.
func recordFields(ctx context.Context, db rowsQuerier, root int64) ([]string, error) {
	fields := []string{}
	err := scanAll(ctx, db, `SELECT name FROM (
			SELECT name, 0 AS kind, id FROM object_attributes WHERE object_id = ?
			UNION ALL SELECT name, 1, id FROM objects WHERE owner_id = ?
		) f ORDER BY kind, id`,
		func(rows *sql.Rows) error {
			var name string
			err := rows.Scan(&name)
			fields = append(fields, name)
			return err
		}, root, root)
	return fields, err
}

// The records of the struct whose object is root, to search.
func readRecordTable(ctx context.Context, db *sql.DB, root int64) (*recordTable, error) {
	rt := &recordTable{states: []string{}, table: newSearchTable()}
	err := scanAll(ctx, db, "SELECT name FROM object_states WHERE object_id = ? ORDER BY id",
		func(rows *sql.Rows) error {
			var name string
			err := rows.Scan(&name)
			rt.states = append(rt.states, name)
			return err
		}, root)
	if err != nil {
		return nil, err
	}
	if rt.fields, err = recordFields(ctx, db, root); err != nil {
		return nil, err
	}
	t := rt.table
	idFacet := t.facet("id", facet{Key: "id", Label: "Record", Group: "Record", Hidden: true})
	state := t.facet("state", facet{Key: "state", Label: "State", Group: "Record"})
	updated := t.facet("updated", facet{Key: "updated", Label: "Updated", Group: "Record", Date: true, Hidden: true})
	runFacet := t.facet("run", facet{Key: "run", Label: "Run", Group: "Record"})
	field := func(f string) int {
		return t.facet("."+f, facet{Key: "@" + slug(f), Label: f, Group: "Fields", Kind: "field", Field: f})
	}
	for _, f := range rt.fields {
		field(f)
	}
	err = scanAll(ctx, db, "SELECT "+sharedRecordColumns+" WHERE sr.object_id = ? ORDER BY sr.updated_at DESC, sr.id DESC LIMIT ?",
		func(rows *sql.Rows) error {
			rec, content, err := sharedRecordFrom(rows)
			if err != nil {
				return err
			}
			rt.records = append(rt.records, rec)
			if len(rt.records) > searchedRows {
				return nil
			}
			row := t.row()
			row[idFacet] = []string{strconv.FormatInt(rec.ID, 10)}
			row[state] = scalars(rec.State)
			row[updated] = []string{rec.Updated.UTC().Format(time.RFC3339)}
			if rec.Run != nil {
				row[runFacet] = []string{strconv.FormatInt(*rec.Run, 10)}
			}
			if fields, ok := contentValue(content).(map[string]any); ok {
				names := make([]string, 0, len(fields))
				for f := range fields {
					names = append(names, f)
				}
				sort.Strings(names)
				for _, f := range names {
					row[field(f)] = scalars(fields[f])
				}
			}
			return nil
		}, root, searchedRows+1)
	if err != nil {
		return nil, err
	}
	if len(rt.records) > searchedRows {
		rt.records, rt.truncated = rt.records[:searchedRows], true
	}
	return rt, nil
}

func registerSharedRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/structs/{id}/records", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		root, err := structRoot(ctx, db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		rt, err := readRecordTable(ctx, db, root)
		if err != nil {
			serverError(w, err)
			return
		}
		out := sharedRecords{Fields: rt.fields, States: rt.states, Records: []sharedRecord{}, Truncated: rt.truncated}
		s := rt.table.search()
		q := parseSearch(r.URL.Query().Get("q"))
		key, desc := sortParam(r.URL.Query().Get("sort"))
		matched := s.run(q, key, desc)
		s.count(q, matched)
		out.Facets, out.Matched, out.Searched = rt.table.list(), len(matched), len(rt.records)
		for _, i := range matched[:min(len(matched), maxListedRecords)] {
			out.Records = append(out.Records, rt.records[i])
		}
		out.More = len(matched) > maxListedRecords
		writeJSON(w, http.StatusOK, out)
	})
}
