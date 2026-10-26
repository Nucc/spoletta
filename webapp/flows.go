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
	"time"
)

// A flow is a board of its own with everything on it: its processes and
// objects, the boards of its flow processes, its versions and its runs. Each
// flow does one job, such as fixing exceptions or bumping dependencies, or
// is a module, a part of such jobs that other flows reuse; see modules.go,
// or a struct, a single object they use; see structs.go.
type flow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Offered to other flows to run; every flow another runs is one.
	Module bool `json:"module"`
	// Holds a single object, which other flows use; see structs.go.
	Struct bool `json:"struct"`
	// How many other flows run it, or for a struct, use its object.
	UsedBy int `json:"usedBy"`
	// The repositories its agents and scripts work on, and how its runs'
	// branches are named; see workspace.go.
	Settings  flowSettings `json:"settings"`
	CreatedAt time.Time    `json:"createdAt"`
	// Who sees it and may change it, and how far whoever asked may go with
	// it: accessEdit or accessReview; see owners.go.
	Owner  flowOwner `json:"owner"`
	Access string    `json:"access"`
	// Its latest run of the whole flow, if any.
	LastRun *run `json:"lastRun"`
	// What the flow list shows of it besides: its tags (tags.go), whether
	// whoever asked starred it (favourites.go), and how its runs go of late,
	// none for a struct, which never runs.
	Tags      []string    `json:"tags"`
	Favourite bool        `json:"favourite"`
	Health    *flowHealth `json:"health"`
}

// How a flow's runs of its own go: how many started in the last healthFor
// and how many of those failed, and how many are going on now.
type flowHealth struct {
	Runs    int `json:"runs"`
	Failed  int `json:"failed"`
	Running int `json:"running"`
}

const healthFor = 7 * 24 * time.Hour

// The health of each of the flows that ran, by id.
func flowHealths(ctx context.Context, db rowsQuerier, ids []int64) (map[int64]*flowHealth, error) {
	health := map[int64]*flowHealth{}
	if len(ids) == 0 {
		return health, nil
	}
	of := func(id int64) *flowHealth {
		if health[id] == nil {
			health[id] = &flowHealth{}
		}
		return health[id]
	}
	in := placeholders(len(ids))
	err := scanAll(ctx, db, `SELECT flow_id, COUNT(*), SUM(status = ?) FROM runs
		WHERE parent_run_id IS NULL AND started_at >= ? AND flow_id IN `+in+` GROUP BY flow_id`, func(rows *sql.Rows) error {
		var id int64
		var h flowHealth
		err := rows.Scan(&id, &h.Runs, &h.Failed)
		of(id).Runs, of(id).Failed = h.Runs, h.Failed
		return err
	}, append([]any{runFailed, time.Now().Add(-healthFor)}, int64Args(ids)...)...)
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT flow_id, COUNT(*) FROM runs
		WHERE status = ? AND parent_run_id IS NULL AND flow_id IN `+in+` GROUP BY flow_id`, func(rows *sql.Rows) error {
		var id int64
		var n int
		err := rows.Scan(&id, &n)
		of(id).Running = n
		return err
	}, append([]any{runRunning}, int64Args(ids)...)...)
	return health, err
}

// Fills in what the flow list shows of each flow besides the flow itself,
// for whoever asked, a query for all of them each.
func describeFlows(ctx context.Context, db *sql.DB, r *http.Request, fs []flow) error {
	ids := make([]int64, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	tags, err := flowTags(ctx, db, ids)
	if err != nil {
		return err
	}
	starred, err := favouriteFlows(ctx, db, favouritesOf(r), ids)
	if err != nil {
		return err
	}
	health, err := flowHealths(ctx, db, ids)
	if err != nil {
		return err
	}
	for i := range fs {
		f := &fs[i]
		f.Tags, f.Favourite = tags[f.ID], starred[f.ID]
		if f.Tags == nil {
			f.Tags = []string{}
		}
		if !f.Struct {
			f.Health = health[f.ID]
			if f.Health == nil {
				f.Health = &flowHealth{}
			}
		}
	}
	return nil
}

// The flow with what describeFlows fills in.
func describeFlow(ctx context.Context, db *sql.DB, r *http.Request, f flow) (flow, error) {
	fs := []flow{f}
	err := describeFlows(ctx, db, r, fs)
	return fs[0], err
}

// The flow a request is about, from ?flow=<id>. Writes the error and returns
// false if it is missing or invalid.
func flowParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get("flow"), 10, 64)
	if err != nil {
		http.Error(w, "flow is required", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// The flow a process is in.
func flowOf(ctx context.Context, db querier, process int64) (int64, error) {
	var f int64
	err := db.QueryRowContext(ctx, "SELECT flow_id FROM processes WHERE id = ?", process).Scan(&f)
	return f, err
}

// The flow something new on a board goes in: its parent's, or for the top
// board the one given. Says what is wrong, if anything.
func flowFor(ctx context.Context, db *sql.DB, given int64, parent *int64) (int64, string, error) {
	if parent != nil {
		f, err := flowOf(ctx, db, *parent)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "parent must be a flow process", nil
		}
		return f, "", err
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM flows WHERE id = ?", given).Scan(&n); err != nil {
		return 0, "", err
	}
	if n == 0 {
		return 0, "flow not found", nil
	}
	return given, "", nil
}

func getFlow(ctx context.Context, db *sql.DB, id int64) (flow, error) {
	var f flow
	var ownerUser, ownerTeam sql.NullInt64
	var ownerName string
	err := db.QueryRowContext(ctx, "SELECT f.id, f.name, f.module, f.is_struct, f.settings, f.created_at, "+ownerColumns+" FROM flows f WHERE f.id = ?", id).
		Scan(&f.ID, &f.Name, &f.Module, &f.Struct, &f.Settings, &f.CreatedAt, &ownerUser, &ownerTeam, &ownerName)
	if err != nil {
		return f, err
	}
	f.Owner = scanOwner(ownerUser, ownerTeam, ownerName)
	if f.Struct {
		f.UsedBy, err = structUsers(ctx, db, id)
	} else {
		f.UsedBy, err = usersOf(ctx, db, id)
	}
	if err != nil {
		return f, err
	}
	var rn run
	err = scanRun(db.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE flow_id = ? AND parent_run_id IS NULL ORDER BY id DESC LIMIT 1", id), &rn)
	if err == nil {
		f.LastRun = &rn
	} else if !errors.Is(err, sql.ErrNoRows) {
		return f, err
	}
	return f, nil
}

// Every flow the user sees, each as getFlow has it, with how far they may
// go with it, by name: a query for all of them each, rather than some for
// each, so a long list comes as quickly as a short one.
func listFlows(ctx context.Context, db *sql.DB, u *user) ([]flow, error) {
	see, seeArgs := visibleFlows(u)
	edit, editArgs := editableFlows(u)
	fs := []flow{}
	err := scanAll(ctx, db, "SELECT f.id, f.name, f.module, f.is_struct, f.settings, f.created_at, "+ownerColumns+", "+edit+
		" FROM flows f WHERE "+see+" ORDER BY f.name, f.id", func(rows *sql.Rows) error {
		var f flow
		var ownerUser, ownerTeam sql.NullInt64
		var ownerName string
		var editable bool
		err := rows.Scan(&f.ID, &f.Name, &f.Module, &f.Struct, &f.Settings, &f.CreatedAt, &ownerUser, &ownerTeam, &ownerName, &editable)
		f.Owner = scanOwner(ownerUser, ownerTeam, ownerName)
		f.Access = accessReview
		if editable {
			f.Access = accessEdit
		}
		fs = append(fs, f)
		return err
	}, append(editArgs, seeArgs...)...)
	if err != nil {
		return nil, err
	}
	// How many other flows run each procedure, as usersOf counts them, and
	// use each struct's object, as structUsers does.
	called, used := map[int64]int{}, map[int64]int{}
	err = scanAll(ctx, db, `SELECT CAST(JSON_EXTRACT(settings, '$.flow') AS SIGNED) AS called, COUNT(DISTINCT flow_id) FROM processes
		WHERE kind = ? AND flow_id <> CAST(JSON_EXTRACT(settings, '$.flow') AS SIGNED) GROUP BY called`, func(rows *sql.Rows) error {
		var id int64
		var n int
		err := rows.Scan(&id, &n)
		called[id] = n
		return err
	}, kindSubflow)
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, `SELECT src.flow_id, COUNT(DISTINCT o.flow_id) FROM objects o JOIN objects src ON src.id = o.source_id
		WHERE o.flow_id <> src.flow_id GROUP BY src.flow_id`, func(rows *sql.Rows) error {
		var id int64
		var n int
		err := rows.Scan(&id, &n)
		used[id] = n
		return err
	})
	if err != nil {
		return nil, err
	}
	last := map[int64]*run{}
	err = scanAll(ctx, db, "SELECT "+runColumns+" FROM (SELECT MAX(id) AS last_id FROM runs WHERE parent_run_id IS NULL GROUP BY flow_id) l JOIN runs ON runs.id = l.last_id", func(rows *sql.Rows) error {
		var rn run
		err := scanRun(rows, &rn)
		last[rn.Flow] = &rn
		return err
	})
	if err != nil {
		return nil, err
	}
	for i := range fs {
		f := &fs[i]
		if f.Struct {
			f.UsedBy = used[f.ID]
		} else {
			f.UsedBy = called[f.ID]
		}
		f.LastRun = last[f.ID]
	}
	return fs, nil
}

// Deletes a flow with everything in it and every run of it, and returns
// the runs, whose workspaces are to go too.
func deleteFlow(ctx context.Context, tx *sql.Tx, id int64) ([]int64, error) {
	var runs []int64
	rows, err := tx.QueryContext(ctx, "SELECT id FROM runs WHERE flow_id = ?", id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r int64
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stmts := []string{
		"UPDATE flows SET version_id = NULL WHERE id = ?",
		"DELETE FROM runs WHERE flow_id = ?",
		// The processes inside flow processes, and the objects on their boards, go with them.
		"DELETE FROM processes WHERE flow_id = ? AND parent_id IS NULL",
		"DELETE FROM processes WHERE flow_id = ?",
		"DELETE FROM objects WHERE flow_id = ?",
		"DELETE FROM flow_versions WHERE flow_id = ?",
		"DELETE FROM flow_history WHERE flow_id = ?",
		"DELETE FROM flows WHERE id = ?",
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s, id); err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func registerFlowListRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	// The flows the user sees; see owners.go.
	mux.HandleFunc("GET /api/flows", func(w http.ResponseWriter, r *http.Request) {
		fs, err := listFlows(r.Context(), db, requestUser(r))
		if err != nil {
			serverError(w, err)
			return
		}
		if err := describeFlows(r.Context(), db, r, fs); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fs)
	})

	mux.HandleFunc("GET /api/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if err == nil {
			f.Access, err = flowAccess(r.Context(), db, requestUser(r), id)
		}
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err == nil {
			f, err = describeFlow(r.Context(), db, r, f)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	})

	mux.HandleFunc("POST /api/flows", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name   string `json:"name"`
			Module bool   `json:"module"`
			// Made with its object, named after it.
			Struct bool `json:"struct"`
			// Who it belongs to; see newOwner.
			Owner *flowOwner `json:"owner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Name = strings.TrimSpace(body.Name); body.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if body.Module && body.Struct {
			http.Error(w, "a struct holds an object, not processes to run, so it can't be a procedure", http.StatusBadRequest)
			return
		}
		ownerUser, ownerTeam, msg, err := newOwner(r.Context(), db, requestUser(r), body.Owner)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(r.Context(), "INSERT INTO flows (name, module, is_struct, owner_user_id, owner_team_id) VALUES (?, ?, ?, ?, ?)",
			body.Name, body.Module, body.Struct, ownerUser, ownerTeam)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "no such owner", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		id, err := res.LastInsertId()
		if err != nil {
			serverError(w, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO flow_starts (flow_id, x, y) VALUES (?, 40, 40)", id); err != nil {
			serverError(w, err)
			return
		}
		if body.Struct {
			if _, err := tx.ExecContext(r.Context(), "INSERT INTO objects (flow_id, name, x, y) VALUES (?, ?, 40, 40)", id, body.Name); err != nil {
				serverError(w, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if err == nil {
			f, err = describeFlow(r.Context(), db, r, f)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		f.Access = accessEdit
		writeJSON(w, http.StatusCreated, f)
	})

	mux.HandleFunc("PATCH /api/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			Name *string `json:"name"`
			// A flow others run stays a module.
			Module *bool `json:"module"`
			// Replaces them all; runs going on keep the ones they started with.
			Settings *flowSettings `json:"settings"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Name != nil {
			if *patch.Name = strings.TrimSpace(*patch.Name); *patch.Name == "" {
				http.Error(w, "name must not be empty", http.StatusBadRequest)
				return
			}
		}
		if patch.Settings != nil {
			if msg := patch.Settings.check(); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}
		s, err := isStruct(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if s && isTrue(patch.Module) {
			http.Error(w, "a struct holds an object, not processes to run, so it can't be a procedure", http.StatusBadRequest)
			return
		}
		if s && patch.Name != nil {
			var object int64
			err := db.QueryRowContext(r.Context(), "SELECT id FROM objects WHERE flow_id = ? AND parent_id IS NULL AND owner_id IS NULL", id).Scan(&object)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				serverError(w, err)
				return
			}
			if err == nil {
				if msg, err := renameClash(r.Context(), db, object, *patch.Name); err != nil {
					serverError(w, err)
					return
				} else if msg != "" {
					http.Error(w, msg, http.StatusBadRequest)
					return
				}
			}
			if err := renameStruct(r.Context(), db, id, *patch.Name); err != nil {
				serverError(w, err)
				return
			}
		}
		if patch.Module != nil && !*patch.Module {
			n, err := usersOf(r.Context(), db, id)
			if err != nil {
				serverError(w, err)
				return
			}
			if n > 0 {
				http.Error(w, fmt.Sprintf("%d other flows run this procedure; take it out of them first", n), http.StatusConflict)
				return
			}
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE flows SET name = COALESCE(?, name), module = COALESCE(?, module), settings = COALESCE(?, settings) WHERE id = ?",
			patch.Name, patch.Module, patch.Settings, id); err != nil {
			serverError(w, err)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err == nil {
			f, err = describeFlow(r.Context(), db, r, f)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		f.Access = accessEdit
		writeJSON(w, http.StatusOK, f)
	})

	mux.HandleFunc("DELETE /api/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		var running int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE flow_id = ? AND status = 'running'", id).Scan(&running); err != nil {
			serverError(w, err)
			return
		}
		if running > 0 {
			http.Error(w, "can't delete a flow while it is running", http.StatusConflict)
			return
		}
		runs, err := deleteFlow(ctx, tx, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		dropWorkspaces(workerURL, runs)
		w.WriteHeader(http.StatusNoContent)
	})
}
