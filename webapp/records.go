package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// A shared struct's records are written by the flows using it, and
// corrected by hand: a person adds a record a flow missed, changes what an
// agent got wrong, its state too, adds a field or takes one out, and
// deletes records, one or many at once. What they write replaces the
// record whole, so a field taken out stays out, and stays told apart by
// the struct's unique attributes: a unique value another record holds is
// refused rather than merged, as a run would. A record edited is no longer
// the step's that last moved it, so the state that step comes out in
// doesn't undo the correction (recordSharedState); it keeps its run, and
// who edited it and when, until a run moves it on again.

// What a person writes of a record: its fields, as a JSON object, and its
// state, one of the object's or none.
type recordEdit struct {
	Content json.RawMessage `json:"content"`
	State   string          `json:"state"`
}

// How many records one request deletes at most.
const maxDeletedRecords = 1000

// The store of a shared struct's records, with why it has none.
func structStore(ctx context.Context, db querier, flow int64) (int64, string, error) {
	root, err := structRoot(ctx, db, flow)
	if err != nil {
		return 0, "", err
	}
	if root == 0 {
		return 0, "it is not a struct", nil
	}
	store, err := storeOf(ctx, db, root)
	if err != nil {
		return 0, "", err
	}
	if store != root {
		return 0, "the struct's object isn't shared, so it keeps no records", nil
	}
	return store, "", nil
}

// Checks what a person wrote of a record of the store, giving it back with
// its content compacted, or says what is wrong with it.
func checkRecordEdit(ctx context.Context, db querier, store int64, e recordEdit) (recordEdit, string, error) {
	fields, err := jsonObject(string(e.Content))
	if err != nil {
		return e, "a record holds its fields as a JSON object", nil
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return e, "", err
	}
	e.Content = b
	if e.State = strings.TrimSpace(e.State); e.State != "" {
		var n int
		err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_states WHERE object_id = ? AND name = ?", store, e.State).Scan(&n)
		if err != nil {
			return e, "", err
		}
		if n == 0 {
			return e, fmt.Sprintf("the object has no state %q", e.State), nil
		}
	}
	return e, "", nil
}

// Keys a record by the unique values content holds, refusing one another
// record of the store holds.
func keyRecord(ctx context.Context, tx *sql.Tx, store, record int64, attrs []attribute, content string) (string, error) {
	if _, err := tx.ExecContext(ctx, "DELETE FROM shared_keys WHERE record_id = ?", record); err != nil {
		return "", err
	}
	keys := recordKeys(attrs, content)
	for _, a := range attrs {
		hash, ok := keys[a.ID]
		if !ok {
			continue
		}
		var other int64
		err := tx.QueryRowContext(ctx, "SELECT record_id FROM shared_keys WHERE object_id = ? AND attribute_id = ? AND value_hash = ? FOR UPDATE",
			store, a.ID, hash).Scan(&other)
		if err == nil {
			return fmt.Sprintf("record %d holds that %s already", other, a.Name), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO shared_keys (object_id, attribute_id, value_hash, record_id) VALUES (?, ?, ?, ?)", store, a.ID, hash, record)
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			return fmt.Sprintf("another record holds that %s already", a.Name), nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", nil
}

func registerRecordEditRoutes(mux *http.ServeMux, db *sql.DB) {
	// The store of the struct the request is about, with the record it
	// names, if any, written as an edit; false once it has answered.
	prepare := func(w http.ResponseWriter, r *http.Request) (store int64, edit recordEdit, ok bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return 0, edit, false
		}
		store, msg, err := structStore(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return 0, edit, false
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return 0, edit, false
		}
		if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return 0, edit, false
		}
		return store, edit, true
	}

	// Writes a record of the store in a transaction: a new one with record
	// 0, keyed by its unique values.
	write := func(w http.ResponseWriter, r *http.Request, store, record int64, e recordEdit) {
		ctx := r.Context()
		e, msg, err := checkRecordEdit(ctx, db, store, e)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		status := http.StatusOK
		if record == 0 {
			res, err := tx.ExecContext(ctx, "INSERT INTO shared_records (object_id, content, state, edited_by, edited_at) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP(3))",
				store, string(e.Content), e.State, requestUserID(r))
			if err != nil {
				serverError(w, err)
				return
			}
			if record, err = res.LastInsertId(); err != nil {
				serverError(w, err)
				return
			}
			status = http.StatusCreated
		} else {
			res, err := tx.ExecContext(ctx, `UPDATE shared_records SET content = ?, state = ?, step_id = NULL, edited_by = ?,
					edited_at = CURRENT_TIMESTAMP(3), updated_at = CURRENT_TIMESTAMP(3)
				WHERE id = ? AND object_id = ?`, string(e.Content), e.State, requestUserID(r), record, store)
			if err != nil {
				serverError(w, err)
				return
			}
			if n, _ := res.RowsAffected(); n == 0 {
				var exists bool
				err := tx.QueryRowContext(ctx, "SELECT TRUE FROM shared_records WHERE id = ? AND object_id = ?", record, store).Scan(&exists)
				if errors.Is(err, sql.ErrNoRows) {
					http.Error(w, "no such record", http.StatusNotFound)
					return
				}
				if err != nil {
					serverError(w, err)
					return
				}
			}
		}
		attrs, err := uniqueAttributes(ctx, tx, store)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg, err := keyRecord(ctx, tx, store, record, attrs, string(e.Content)); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusConflict)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		rec, err := sharedRecordByID(ctx, db, record)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, status, rec)
	}

	mux.HandleFunc("POST /api/structs/{id}/records", func(w http.ResponseWriter, r *http.Request) {
		if store, e, ok := prepare(w, r); ok {
			write(w, r, store, 0, e)
		}
	})

	mux.HandleFunc("PUT /api/structs/{id}/records/{record}", func(w http.ResponseWriter, r *http.Request) {
		record, err := strconv.ParseInt(r.PathValue("record"), 10, 64)
		if err != nil || record <= 0 {
			http.Error(w, "invalid record", http.StatusBadRequest)
			return
		}
		if store, e, ok := prepare(w, r); ok {
			write(w, r, store, record, e)
		}
	})

	// Deletes the records named, one or many; their unique values are free
	// for others then. Records another struct keeps stay.
	mux.HandleFunc("POST /api/structs/{id}/records/delete", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			IDs []int64 `json:"ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.IDs) == 0 {
			http.Error(w, "name the records to delete", http.StatusBadRequest)
			return
		}
		if len(body.IDs) > maxDeletedRecords {
			http.Error(w, fmt.Sprintf("delete at most %d records at once", maxDeletedRecords), http.StatusBadRequest)
			return
		}
		store, msg, err := structStore(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		args := []any{store}
		for _, i := range body.IDs {
			args = append(args, i)
		}
		res, err := db.ExecContext(r.Context(), "DELETE FROM shared_records WHERE object_id = ? AND id IN (?"+strings.Repeat(", ?", len(body.IDs)-1)+")", args...)
		if err != nil {
			serverError(w, err)
			return
		}
		n, _ := res.RowsAffected()
		writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
	})
}
