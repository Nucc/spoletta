package main

import (
	"cmp"
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

// A person picks which record a flow works on: on a shared struct's Records
// page they start a run of a flow querying the struct's object on one
// record, or one run on each of several. The run is the flow's as a
// schedule starts it, on the version the flow published or its board as it
// is while it publishes none (see publish.go), but each of its query links on
// the record's store reads that record alone, whatever the query's state
// and rule, as a list of one; a query on another store, or on a persistent
// object's stored instances, reads as it would. So a flow implementing the
// Jira ticket a query gives it implements the one picked, and moves it on
// as it would any it queried (see shared.go). A re-run of the run reads the
// record again, as it is then; once the record is deleted, its queries fail.
//
// Nothing stops a person starting a run on a record its flow's query
// wouldn't select, or one another run is working on, as a person picked it,
// even one another run holds (see picks.go); the page says so before they
// do. An object of the flow picking a record of the store takes that one.

// How many records one request starts runs on at most.
const maxRecordRuns = 50

// What the run's query links on object read, if the run was started on a
// record of object's store: the record as a list of one, true. False for
// another store's object, or a run started on none.
func (fr *flowRun) pinnedRecord(ctx context.Context, db *sql.DB, object int64) (string, bool, error) {
	if fr.record == 0 {
		return "", false, nil
	}
	store := fr.rb.store(object)
	if store == 0 {
		return "", false, nil
	}
	var of int64
	var content string
	err := db.QueryRowContext(ctx, "SELECT object_id, content FROM shared_records WHERE id = ? AND deleted_at IS NULL", fr.record).Scan(&of, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("record %d, which the run was started on, was deleted", fr.record)
	}
	if err != nil || of != store {
		return "", false, err
	}
	if !json.Valid([]byte(content)) {
		return "", false, fmt.Errorf("record %d, which the run was started on, holds no JSON", fr.record)
	}
	b, err := json.Marshal([]json.RawMessage{json.RawMessage(content)})
	return string(b), true, err
}

// A flow a person may start on a store's records, with the queries it
// reads them by.
type recordFlow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	queries []linkQuery
	// The states its queries take records in, in their order, "" for a
	// query taking any, so a task board's column of a state can offer it.
	States []string `json:"states"`
	// The states it leaves the records it takes in (recordLeaves), so a
	// task board can draw where its records go from each state.
	Leaves []string `json:"leaves"`
	// Why its queries wouldn't select each record asked about, by record
	// id, for those they wouldn't.
	Off map[string]string `json:"off"`
}

// The states a run on the board leaves a record of the store in, by name in
// the order the board lists them, as storeShared moves it: a process's
// output link to an object of the store that the process also takes moves
// the record into the states the link lists to decide between, else into
// the one it names, else, naming none, into whichever of the object's
// states has a rule that may hold of it (see rules.go). A process only
// generating the object, or a link naming no state of an object without
// rules, leaves the record in the state it was in, so names none. A
// procedure a subflow process runs isn't looked into.
func recordLeaves(b *runBoard, store int64) []string {
	into := map[int64]bool{}
	for _, l := range b.Links {
		if l.Direction != directionOutput || b.store(l.Object) != store || !b.takes(l.Process, l.Object) {
			continue
		}
		switch listed := b.linkStates[l.ID]; {
		case len(listed) > 0:
			for _, st := range listed {
				into[st] = true
			}
		case l.State != 0:
			into[l.State] = true
		default:
			for _, st := range b.States {
				if r, err := parseRule(st.Rule); st.Object == l.Object && err == nil && len(r) > 0 {
					into[st.ID] = true
				}
			}
		}
	}
	leaves := []string{}
	for _, st := range b.States {
		if into[st.ID] && !slices.Contains(leaves, st.Name) {
			leaves = append(leaves, st.Name)
		}
	}
	return leaves
}

// The states each flow publishing no version leaves the store's records in
// on its board as it is, by flow: what recordLeaves reads of a board, its
// objects of the store with their links and states, read for every such
// flow at once.
func unpublishedLeaves(ctx context.Context, db *sql.DB, store int64) (map[int64][]string, error) {
	of := func(object string) string {
		return ` JOIN objects o ON o.id = ` + object + ` JOIN flows f ON f.id = o.flow_id
			WHERE o.store_id = ? AND ` + liveFlows + ` AND NOT f.is_struct AND NOT f.module AND f.published_version_id IS NULL`
	}
	boards := map[int64]*snapshot{}
	board := func(flow int64) *snapshot {
		if boards[flow] == nil {
			boards[flow] = &snapshot{}
		}
		return boards[flow]
	}
	err := scanAll(ctx, db, `SELECT l.id, l.process_id, l.object_id, l.direction, l.state_id, o.flow_id FROM object_links l`+of("l.object_id"),
		func(rows *sql.Rows) error {
			var l snapshotLink
			var flow int64
			if err := rows.Scan(&l.ID, &l.Process, &l.Object, &l.Direction, &l.State, &flow); err != nil {
				return err
			}
			s := board(flow)
			if !slices.ContainsFunc(s.Objects, func(o snapshotObject) bool { return o.ID == l.Object }) {
				s.Objects = append(s.Objects, snapshotObject{ID: l.Object, Store: &store})
			}
			s.Links = append(s.Links, l)
			return nil
		}, store)
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT ls.link_id, ls.state_id, o.flow_id FROM link_states ls JOIN object_links l ON l.id = ls.link_id`+of("l.object_id"),
		func(rows *sql.Rows) error {
			var ls snapshotLinkState
			var flow int64
			if err := rows.Scan(&ls.Link, &ls.State, &flow); err != nil {
				return err
			}
			board(flow).LinkStates = append(board(flow).LinkStates, ls)
			return nil
		}, store)
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT st.id, st.object_id, st.name, COALESCE(st.rule, ''), o.flow_id FROM object_states st`+of("st.object_id")+` ORDER BY st.id`,
		func(rows *sql.Rows) error {
			var st objectState
			var flow int64
			if err := rows.Scan(&st.ID, &st.Object, &st.Name, &st.Rule, &flow); err != nil {
				return err
			}
			board(flow).States = append(board(flow).States, st)
			return nil
		}, store)
	if err != nil {
		return nil, err
	}
	out := map[int64][]string{}
	for flow, s := range boards {
		out[flow] = recordLeaves(newRunBoard(flow, *s), store)
	}
	return out, nil
}

// The flows querying the store's records, a job flow's own boards, or
// picking one (see picks.go), which the user may start, by name, with a
// pick read as the query taking the one record it selects. A flow that
// published a version runs that on records (see publish.go), so its queries
// and picks are that version's.
func recordFlows(ctx context.Context, db *sql.DB, u *user, store int64) ([]*recordFlow, error) {
	var fs []*recordFlow
	err := scanAll(ctx, db, `SELECT f.id, f.name, v.snapshot FROM flows f JOIN flow_versions v ON v.id = f.published_version_id
		WHERE `+liveFlows+` AND NOT f.is_struct AND NOT f.module AND JSON_CONTAINS(JSON_EXTRACT(v.snapshot, '$.objects[*].store'), CAST(? AS JSON))`,
		func(rows *sql.Rows) error {
			f := &recordFlow{Off: map[string]string{}}
			var body []byte
			if err := rows.Scan(&f.ID, &f.Name, &body); err != nil {
				return err
			}
			var s snapshot
			if err := json.Unmarshal(body, &s); err != nil {
				return err
			}
			b := newRunBoard(f.ID, s)
			for _, l := range s.Links {
				if l.Direction == directionInput && l.Query != nil && b.store(l.Object) == store {
					f.queries = append(f.queries, *l.Query)
				}
			}
			for _, o := range s.Objects {
				if picksRecord(o) && *o.Store == store {
					f.queries = append(f.queries, o.Pick.query())
				}
			}
			if len(f.queries) > 0 {
				f.Leaves = recordLeaves(b, store)
				fs = append(fs, f)
			}
			return nil
		}, strconv.FormatInt(store, 10))
	if err != nil {
		return nil, err
	}
	unpublished := map[int64]*recordFlow{}
	err = scanAll(ctx, db, `SELECT f.id, f.name, l.query, NULL FROM objects o
			JOIN object_links l ON l.object_id = o.id AND l.direction = 'input' AND l.query IS NOT NULL
			JOIN flows f ON f.id = o.flow_id
		WHERE o.store_id = ? AND `+liveFlows+` AND NOT f.is_struct AND NOT f.module AND f.published_version_id IS NULL
		UNION ALL SELECT f.id, f.name, NULL, o.pick FROM objects o JOIN flows f ON f.id = o.flow_id
		WHERE o.store_id = ? AND o.pick IS NOT NULL AND o.parent_id IS NULL AND o.owner_id IS NULL AND NOT o.many
			AND `+liveFlows+` AND NOT f.is_struct AND NOT f.module AND f.published_version_id IS NULL`, func(rows *sql.Rows) error {
		var id int64
		var name string
		var query, pick []byte
		if err := rows.Scan(&id, &name, &query, &pick); err != nil {
			return err
		}
		var q linkQuery
		if query != nil {
			if err := json.Unmarshal(query, &q); err != nil {
				return err
			}
		} else {
			p, err := pickOf(pick)
			if err != nil {
				return err
			}
			q = p.query()
		}
		f := unpublished[id]
		if f == nil {
			f = &recordFlow{ID: id, Name: name, Off: map[string]string{}}
			unpublished[id] = f
			fs = append(fs, f)
		}
		f.queries = append(f.queries, q)
		return nil
	}, store, store)
	if err != nil {
		return nil, err
	}
	if len(unpublished) > 0 {
		leaves, err := unpublishedLeaves(ctx, db, store)
		if err != nil {
			return nil, err
		}
		for id, f := range unpublished {
			f.Leaves = leaves[id]
		}
	}
	slices.SortStableFunc(fs, func(x, y *recordFlow) int {
		return cmp.Or(cmp.Compare(x.Name, y.Name), cmp.Compare(x.ID, y.ID))
	})
	var may []*recordFlow
	for _, f := range fs {
		access, err := flowAccess(ctx, db, u, f.ID)
		if err != nil {
			return nil, err
		}
		if access != accessEdit {
			continue
		}
		f.States = []string{}
		for _, q := range f.queries {
			if !slices.Contains(f.States, q.State) {
				f.States = append(f.States, q.State)
			}
		}
		if f.Leaves == nil {
			f.Leaves = []string{}
		}
		may = append(may, f)
	}
	return may, nil
}

// Why none of the queries would select a record in state holding content,
// or "" if one would.
func offQuery(queries []linkQuery, content, state string) string {
	var why string
	for _, q := range queries {
		where, err := parseRule(q.Where)
		switch {
		case err != nil:
			why = "its rule doesn't read"
		case q.State != "" && q.State != state:
			why = fmt.Sprintf("it takes records in state %s", quote(q.State))
		case len(where) > 0 && !where.holds(content):
			why = fmt.Sprintf("it takes records where %s", strings.TrimSpace(q.Where))
		default:
			return ""
		}
	}
	return why
}

// A run going on a record: one started on it, one that took it (see
// picks.go), or the one that last moved it on, still running.
type recordRun struct {
	Run  int64 `json:"run"`
	Flow int64 `json:"flow"`
}

// The runs going on each of the store's records, by record id.
func recordsGoing(ctx context.Context, db *sql.DB, store int64, records []int64) (map[string][]recordRun, error) {
	going := map[string][]recordRun{}
	if len(records) == 0 {
		return going, nil
	}
	in := "?" + strings.Repeat(", ?", len(records)-1)
	var args []any
	for range 3 {
		args = append(args, store)
		for _, r := range records {
			args = append(args, r)
		}
		args = append(args, runRunning)
	}
	err := scanAll(ctx, db, `SELECT sr.id, r.id, r.flow_id FROM shared_records sr JOIN runs r ON r.record_id = sr.id
			WHERE sr.object_id = ? AND sr.id IN (`+in+`) AND sr.deleted_at IS NULL AND r.status = ?
		UNION SELECT sr.id, r.id, r.flow_id FROM shared_records sr JOIN runs r ON r.id = sr.run_id
			WHERE sr.object_id = ? AND sr.id IN (`+in+`) AND sr.deleted_at IS NULL AND r.status = ?
		UNION SELECT sr.id, r.id, r.flow_id FROM shared_records sr JOIN taken_records t ON t.record_id = sr.id AND NOT t.followed JOIN runs r ON r.id = t.run_id
			WHERE sr.object_id = ? AND sr.id IN (`+in+`) AND sr.deleted_at IS NULL AND r.status = ?
		ORDER BY 1, 2`, func(rows *sql.Rows) error {
		var record int64
		var rr recordRun
		if err := rows.Scan(&record, &rr.Run, &rr.Flow); err != nil {
			return err
		}
		key := strconv.FormatInt(record, 10)
		going[key] = append(going[key], rr)
		return nil
	}, args...)
	return going, err
}

// The store's records named, their content and state by id; an id not of
// the store is left out.
func storeRecords(ctx context.Context, db *sql.DB, store int64, ids []int64) (map[int64][2]string, error) {
	recs := map[int64][2]string{}
	if len(ids) == 0 {
		return recs, nil
	}
	args := []any{store}
	for _, i := range ids {
		args = append(args, i)
	}
	err := scanAll(ctx, db, "SELECT id, content, state FROM shared_records WHERE object_id = ? AND deleted_at IS NULL AND id IN (?"+strings.Repeat(", ?", len(ids)-1)+")",
		func(rows *sql.Rows) error {
			var id int64
			var content, state string
			if err := rows.Scan(&id, &content, &state); err != nil {
				return err
			}
			recs[id] = [2]string{content, state}
			return nil
		}, args...)
	return recs, err
}

// Record ids as a request's query lists them, comma separated.
func recordIDs(s string) ([]int64, error) {
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid record %q", part)
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxRecordRuns {
		return nil, fmt.Errorf("start runs on at most %d records at once", maxRecordRuns)
	}
	return ids, nil
}

// Starts a run of the flow on the record, for the user who did, on the
// version the flow published, if any; see startRun.
func startRecordRun(ctx context.Context, db *sql.DB, eng *engine, flow, record int64, startedBy *int64) (int64, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	nr, err := newRun(ctx, tx, flow, nil, startedBy, true)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET record_id = ? WHERE id = ?", record, nr.id); err != nil {
		return 0, err
	}
	nr.record = record
	if err := takeRecords(ctx, tx, flow, &nr); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	eng.start(nr, flow, nil)
	return nr.id, nil
}

func registerRecordRunRoutes(mux *http.ServeMux, db *sql.DB, eng *engine) {
	// The store of the struct the path names; false once it has answered.
	storeFor := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return 0, false
		}
		store, msg, err := structStore(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return 0, false
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return 0, false
		}
		return store, true
	}

	// The flows the user may start on the struct's records, and for the
	// records ?records=1,2 names, which of them each flow's queries wouldn't
	// select, and the runs going on each.
	mux.HandleFunc("GET /api/structs/{id}/records/flows", func(w http.ResponseWriter, r *http.Request) {
		store, ok := storeFor(w, r)
		if !ok {
			return
		}
		ids, err := recordIDs(r.URL.Query().Get("records"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		fs, err := recordFlows(ctx, db, requestUser(r), store)
		if err != nil {
			serverError(w, err)
			return
		}
		recs, err := storeRecords(ctx, db, store, ids)
		if err != nil {
			serverError(w, err)
			return
		}
		for _, f := range fs {
			for id, rec := range recs {
				if why := offQuery(f.queries, rec[0], rec[1]); why != "" {
					f.Off[strconv.FormatInt(id, 10)] = why
				}
			}
		}
		going, err := recordsGoing(ctx, db, store, ids)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Flows []*recordFlow          `json:"flows"`
			Going map[string][]recordRun `json:"going"`
		}{append([]*recordFlow{}, fs...), going})
	})

	// Starts a run of the flow on each record named, {"flow": 1, "records":
	// [2, 3]}, returning the runs in the records' order.
	mux.HandleFunc("POST /api/structs/{id}/records/runs", func(w http.ResponseWriter, r *http.Request) {
		store, ok := storeFor(w, r)
		if !ok {
			return
		}
		var body struct {
			Flow    int64   `json:"flow"`
			Records []int64 `json:"records"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.Records) == 0 {
			http.Error(w, "name the records to start runs on", http.StatusBadRequest)
			return
		}
		if len(body.Records) > maxRecordRuns {
			http.Error(w, fmt.Sprintf("start runs on at most %d records at once", maxRecordRuns), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		fs, err := recordFlows(ctx, db, requestUser(r), store)
		if err != nil {
			serverError(w, err)
			return
		}
		if !slices.ContainsFunc(fs, func(f *recordFlow) bool { return f.ID == body.Flow }) {
			http.Error(w, "that flow doesn't query these records, or isn't yours to run", http.StatusBadRequest)
			return
		}
		recs, err := storeRecords(ctx, db, store, body.Records)
		if err != nil {
			serverError(w, err)
			return
		}
		for _, id := range body.Records {
			if _, ok := recs[id]; !ok {
				http.Error(w, fmt.Sprintf("no record %d", id), http.StatusNotFound)
				return
			}
		}
		runs := []run{}
		var started []int64
		for _, id := range body.Records {
			if slices.Contains(started, id) {
				continue
			}
			started = append(started, id)
			rid, err := startRecordRun(ctx, db, eng, body.Flow, id, requestUserID(r))
			var none *nothingToTake
			if errors.As(err, &none) {
				http.Error(w, "nothing to run on: "+none.Error(), http.StatusConflict)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
			rn, err := getRun(ctx, db, rid)
			if err != nil {
				serverError(w, err)
				return
			}
			runs = append(runs, rn)
		}
		writeJSON(w, http.StatusCreated, runs)
	})
}
