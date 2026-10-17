package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
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
// Whether a struct's object is shared is its own, and each copy follows it,
// as with what else it has (see modules.go): the copy's store is the
// struct's object. A shared object isn't persistent too, which would store
// it in each flow's runs besides.

func migrateShared(db *sql.DB) error {
	// The shared object whose records this one reads and writes: a shared
	// struct's own object for itself, and for its copies.
	if err := addColumn(db, "objects", "store_id", "BIGINT NULL AFTER source_id"); err != nil {
		return err
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS shared_records (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			object_id BIGINT NOT NULL,
			content LONGTEXT NOT NULL,
			state VARCHAR(255) NOT NULL DEFAULT '',
			run_id BIGINT NULL,
			step_id BIGINT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			KEY object_updated (object_id, updated_at),
			KEY step (step_id),
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS shared_keys (
			object_id BIGINT NOT NULL,
			attribute_id BIGINT NOT NULL,
			value_hash CHAR(64) NOT NULL,
			record_id BIGINT NOT NULL,
			PRIMARY KEY (object_id, attribute_id, value_hash),
			FOREIGN KEY (record_id) REFERENCES shared_records(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

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

// Writes the records of shared objects a step generated into their stores.
// A copy of a struct deleted since writes none, having no unique attribute
// to tell them apart by; it still reads what was written.
func storeShared(ctx context.Context, tx *sql.Tx, runID, stepID int64) error {
	type generated struct {
		store          int64
		affects        bool
		content, state string
	}
	var objects []generated
	err := scanAll(ctx, tx,
		`SELECT o.store_id, ro.content, ro.state, EXISTS (
			SELECT 1 FROM object_links l JOIN run_steps s ON s.process_id = l.process_id
			WHERE s.id = ro.step_id AND l.object_id = ro.object_id AND l.direction = 'input')
		FROM run_objects ro JOIN objects o ON o.id = ro.object_id JOIN objects st ON st.id = o.store_id
		WHERE ro.step_id = ? ORDER BY o.store_id, o.id`,
		func(rows *sql.Rows) error {
			var g generated
			err := rows.Scan(&g.store, &g.content, &g.state, &g.affects)
			objects = append(objects, g)
			return err
		}, stepID)
	if err != nil {
		return err
	}
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
		q = "UPDATE shared_records SET content = ?, state = IF(? = '', state, ?), run_id = ?, step_id = ?, updated_at = CURRENT_TIMESTAMP(3) WHERE id = ?"
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

// Moves the records the step made its own into the state it came out in.
func recordSharedState(ctx context.Context, db *sql.DB, step, object int64, state string) error {
	if state == "" {
		return nil
	}
	_, err := db.ExecContext(ctx,
		"UPDATE shared_records r JOIN objects o ON o.store_id = r.object_id SET r.state = ? WHERE r.step_id = ? AND o.id = ?",
		state, step, object)
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
}

// A shared struct's records, the latest written first, with the fields its
// object has in the board's order: its attributes, then the objects it is
// made of, so a table of them has its columns before any record does.
type sharedRecords struct {
	Fields  []string       `json:"fields"`
	Records []sharedRecord `json:"records"`
	// More records than these are stored.
	More bool `json:"more"`
}

// How many records the struct's records list, the latest written first.
const maxListedRecords = 1000

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
		out := sharedRecords{Fields: []string{}, Records: []sharedRecord{}}
		err = scanAll(ctx, db, `SELECT name FROM (
				SELECT name, 0 AS kind, id FROM object_attributes WHERE object_id = ?
				UNION ALL SELECT name, 1, id FROM objects WHERE owner_id = ?
			) f ORDER BY kind, id`,
			func(rows *sql.Rows) error {
				var name string
				if err := rows.Scan(&name); err != nil {
					return err
				}
				out.Fields = append(out.Fields, name)
				return nil
			}, root, root)
		if err != nil {
			serverError(w, err)
			return
		}
		err = scanAll(ctx, db, `SELECT sr.id, sr.content, sr.state, sr.run_id, ru.flow_id, sr.updated_at
			FROM shared_records sr LEFT JOIN runs ru ON ru.id = sr.run_id
			WHERE sr.object_id = ? ORDER BY sr.updated_at DESC, sr.id DESC LIMIT ?`,
			func(rows *sql.Rows) error {
				var rec sharedRecord
				var content string
				if err := rows.Scan(&rec.ID, &content, &rec.State, &rec.Run, &rec.RunFlow, &rec.Updated); err != nil {
					return err
				}
				rec.Content = json.RawMessage(asItem(content))
				out.Records = append(out.Records, rec)
				return nil
			}, root, maxListedRecords+1)
		if err != nil {
			serverError(w, err)
			return
		}
		if len(out.Records) > maxListedRecords {
			out.Records, out.More = out.Records[:maxListedRecords], true
		}
		writeJSON(w, http.StatusOK, out)
	})
}
