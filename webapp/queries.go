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

// A query link takes a persistent object's stored instances rather than the
// object in this run: an agent or script process gets, as a list of that
// object, what the flow's runs that ended done stored of it, filtered by a
// rule on its fields (see rules.go) and a state, newest first or oldest
// first, at most a limit. So a process can look up what was handled before,
// such as the last approved review of a pull request. What the run itself
// has stored so far isn't among them.
//
// A query link doesn't wait: the process runs as if it took nothing by it,
// and reads the records once it starts. It neither names a state nor is a
// condition, can't consume, and is the process's only input link to its
// object, so the process takes the object one way only.
type linkQuery struct {
	// A rule the records' fields must hold, all of them when empty.
	Where string `json:"where,omitempty" yaml:"where,omitempty"`
	// The state they must be in, by name; any when empty.
	State string `json:"state,omitempty" yaml:"state,omitempty"`
	// At most this many; defaultQueryLimit when 0.
	Limit int `json:"limit,omitempty" yaml:"limit,omitempty"`
	// Oldest first rather than newest first.
	Oldest bool `json:"oldest,omitempty" yaml:"oldest,omitempty"`
}

const (
	defaultQueryLimit = 20
	// So an agent's prompt stays a prompt.
	maxQueryLimit = 100
)

func (q *linkQuery) limit() int {
	if q.Limit == 0 {
		return defaultQueryLimit
	}
	return q.Limit
}

// The query as stored: null for none.
func storedQuery(q *linkQuery) any {
	if q == nil {
		return nil
	}
	b, _ := json.Marshal(q)
	return string(b)
}

// A stored query; nil for none.
func queryOf(stored []byte) (*linkQuery, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	var q linkQuery
	return &q, json.Unmarshal(stored, &q)
}

// What's wrong with the query alone, if anything.
func (q *linkQuery) problem() string {
	if _, err := parseRule(q.Where); err != nil {
		return "the query's rule: " + err.Error()
	}
	if q.Limit < 0 || q.Limit > maxQueryLimit {
		return fmt.Sprintf("a query takes at most %d records", maxQueryLimit)
	}
	return ""
}

// What's wrong with the link as to queries, if anything: a query link is
// an agent's or script's input link to a persistent object, naming no state
// and not consuming it, and a process takes an object it queries by no
// other link.
func checkQuery(ctx context.Context, db *sql.DB, l link) (string, error) {
	if l.Direction != directionInput {
		if l.Query != nil {
			return "only an input link has a query", nil
		}
		return "", nil
	}
	var others, queried int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(query IS NOT NULL), 0) FROM object_links WHERE process_id = ? AND object_id = ? AND direction = 'input' AND id <> ?",
		l.Process, l.Object, l.ID).Scan(&others, &queried)
	if err != nil {
		return "", err
	}
	if queried > 0 {
		return "the process queries that object already; it takes it by its query alone", nil
	}
	if l.Query == nil {
		return "", nil
	}
	if others > 0 {
		return "the process takes that object already; it can't query it too", nil
	}
	if msg := l.Query.problem(); msg != "" {
		return msg, nil
	}
	switch {
	case l.State != nil || l.Condition || len(l.States) > 0:
		return "a query link names no state; the query says which records", nil
	case l.Role == roleConsumption:
		return "a query reads records; it can't consume them", nil
	}
	var kind string
	var persistent bool
	err = db.QueryRowContext(ctx, "SELECT p.kind, o.persistent OR o.store_id IS NOT NULL FROM processes p JOIN objects o ON o.id = ? WHERE p.id = ?", l.Object, l.Process).
		Scan(&kind, &persistent)
	if errors.Is(err, sql.ErrNoRows) {
		return "process or object not found", nil
	}
	if err != nil {
		return "", err
	}
	if kind != kindAgent && kind != kindScript {
		return "only an agent or a script queries records", nil
	}
	if !persistent {
		return "only a persistent or shared object has records to query", nil
	}
	if l.Query.State != "" {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_states WHERE object_id = ? AND name = ?", l.Object, l.Query.State).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return fmt.Sprintf("the object has no state %s", quote(l.Query.State)), nil
		}
	}
	return "", nil
}

// The records of the object the query selects, as a list of it: what
// runs other than run that ended done stored of it, or for a shared object
// its store's records; see shared.go.
func queryRecords(ctx context.Context, db *sql.DB, run, object, store int64, q *linkQuery) (string, error) {
	if store != 0 {
		return querySharedRecords(ctx, db, store, q)
	}
	where, err := parseRule(q.Where)
	if err != nil {
		return "", err
	}
	order := "DESC"
	if q.Oldest {
		order = "ASC"
	}
	args := []any{object, runDone, run}
	state := ""
	if q.State != "" {
		state = " AND i.state = ?"
		args = append(args, q.State)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT i.content FROM object_instances i JOIN runs r ON r.id = i.run_id
		WHERE i.object_id = ? AND r.status = ? AND r.id <> ?`+state+`
		ORDER BY r.finished_at `+order+`, r.id `+order, args...)
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

// The inputs a process's query links give it, in object order. A run
// started on a shared record reads that record alone of its store, whatever
// the query; see record_runs.go.
func (w *walk) queried(id int64) ([]runInput, error) {
	var ins []runInput
	for _, qe := range w.queries[id] {
		content, pinned, err := w.fr.pinnedRecord(w.ctx, w.e.db, qe.object)
		if err == nil && !pinned {
			content, err = queryRecords(w.ctx, w.e.db, w.run, qe.object, w.fr.rb.store(qe.object), qe.query)
		}
		if err != nil {
			return nil, err
		}
		w.mu.Lock()
		name := w.objects[qe.object]
		w.mu.Unlock()
		ins = append(ins, runInput{Name: name, Content: content, Role: inputRecords})
	}
	return ins, nil
}

type queryEnd struct {
	object int64
	query  *linkQuery
}

func registerQueryRoutes(mux *http.ServeMux, db *sql.DB) {
	// Sets an input link's query, or with null makes it a plain link again.
	mux.HandleFunc("PUT /api/links/{id}/query", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Query *linkQuery `json:"query"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		l := link{ID: id, Query: body.Query}
		var state int64
		err = db.QueryRowContext(r.Context(), "SELECT process_id, object_id, direction, state_id, role, is_condition FROM object_links WHERE id = ?", id).
			Scan(&l.Process, &l.Object, &l.Direction, &state, &l.Role, &l.Condition)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "link not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if state != 0 {
			l.State = &state
		}
		if msg, err := checkQuery(r.Context(), db, l); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_links SET query = ? WHERE id = ?", storedQuery(l.Query), id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
