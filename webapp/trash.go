package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Deleting a flow, procedure or struct moves it to the trash, with when and
// who did it (flows.deleted_at, deleted_by), where those who may change it
// restore it or delete it for good, with its runs and versions. The trash
// empties itself of each after TRASH_KEEP, 30 days by default.
//
// In the trash a flow is no one's to see or reach, admins' included: it is
// out of every list, the flow list, the board's sidebar, the schedules, the
// Activity page, the inbox, and guardFlows turns away any request about it,
// or anything in it, as if it didn't exist; only this file's routes, under
// /api/trash, reach it. It starts no runs: newRun refuses it, so its
// schedules don't fire, which come back when it is restored, at their next
// time after then, and the API answers as for a flow that doesn't exist. Runs
// going on in it are stopped as it goes, which can't be undone, so moving
// one with runs going on asks first (stopRuns): its own runs are stopped as
// the Stop button stops them, and the runs of it other flows' runs started
// through their subflow processes fail, failing those processes' steps.
//
// The flows running a procedure in the trash keep their subflow processes,
// which fail to run it until it is restored, and the board shows the call
// broken. A struct's or procedure's object copied into other flows is the
// copies' own while it is in the trash, as when it is deleted for good, and
// they keep working on them; restoring it makes them follow it again. A
// shared struct's copies keep reading and writing its records, which stay.
//
// Names aren't unique, but setup files and the assistant refer to flows by
// them, so a flow in the trash leaves its name free, and comes back under
// another if a flow out of the trash has taken it.

// A run of a flow in the trash that was to start, which doesn't.
var errInTrash = errors.New("the flow is in the trash")

// How long the trash keeps what is moved to it, by default.
const defaultTrashKeep = "30d"

// How long the trash keeps a flow before it empties itself of it, from
// TRASH_KEEP: a number of days, such as 30d, or a duration such as 12h.
func trashKeep() (time.Duration, error) {
	v := env("TRASH_KEEP", defaultTrashKeep)
	d, err := parseKeep(v)
	if err != nil || d < time.Second {
		return 0, fmt.Errorf("TRASH_KEEP must be a number of days such as 30d, or a duration of a second or more such as 12h; it is %q", v)
	}
	return d, nil
}

func parseKeep(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(strings.TrimSpace(v), "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil || n <= 0 || n > 3650 {
			return 0, errors.New("not a number of days")
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(v)
}

// How often each replica looks for what the trash keeps no longer: a tenth
// of how long it keeps it, between a second and a minute.
func trashSweepEvery(keep time.Duration) time.Duration {
	return min(max(keep/10, time.Second), time.Minute)
}

// A flow in the trash as the trash lists it.
type trashedFlow struct {
	ID     int64     `json:"id"`
	Name   string    `json:"name"`
	Module bool      `json:"module"`
	Struct bool      `json:"struct"`
	Owner  flowOwner `json:"owner"`
	// When it was moved there, and by whom; empty for nobody signed in.
	DeletedAt time.Time `json:"deletedAt"`
	DeletedBy string    `json:"deletedBy"`
	// When the trash empties itself of it.
	GoesAt time.Time `json:"goesAt"`
	// Whether a flow out of the trash has its name, so it comes back under
	// another.
	NameTaken bool `json:"nameTaken"`
	// How many flows out of the trash run it, or for a struct, have copies
	// of its object: they fail to run it, or have the copies as their own,
	// until it comes back.
	UsedBy int `json:"usedBy"`
}

// Whether the user may restore the flow, or delete it for good: those who
// may change it, as if it weren't in the trash. False for a flow that isn't
// in the trash.
func mayEmpty(ctx context.Context, db querier, u *user, flow int64) (bool, error) {
	edit, args := editableFlows(u)
	var may bool
	err := db.QueryRowContext(ctx, "SELECT "+edit+" FROM flows f WHERE f.id = ? AND f.deleted_at IS NOT NULL", append(args, flow)...).Scan(&may)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return may, err
}

// The flows in the trash the user may restore, the latest moved there first.
func listTrash(ctx context.Context, db *sql.DB, u *user, keep time.Duration) ([]trashedFlow, error) {
	edit, args := editableFlows(u)
	fs := []trashedFlow{}
	err := scanAll(ctx, db, `SELECT f.id, f.name, f.module, f.is_struct, `+ownerColumns+`, f.deleted_at, `+userLabelColumn("f.deleted_by")+`,
			f.deleted_at + INTERVAL ? MICROSECOND,
			EXISTS (SELECT 1 FROM flows o WHERE o.name = f.name AND o.deleted_at IS NULL)
		FROM flows f WHERE f.deleted_at IS NOT NULL AND `+edit+` ORDER BY f.deleted_at DESC, f.id DESC`, func(rows *sql.Rows) error {
		var t trashedFlow
		var ownerUser, ownerTeam sql.NullInt64
		var ownerName string
		err := rows.Scan(&t.ID, &t.Name, &t.Module, &t.Struct, &ownerUser, &ownerTeam, &ownerName, &t.DeletedAt, &t.DeletedBy, &t.GoesAt, &t.NameTaken)
		t.Owner = scanOwner(ownerUser, ownerTeam, ownerName)
		fs = append(fs, t)
		return err
	}, append([]any{keep.Microseconds()}, args...)...)
	if err != nil {
		return nil, err
	}
	for i := range fs {
		t := &fs[i]
		if t.Struct {
			t.UsedBy, err = structUsers(ctx, db, t.ID)
		} else {
			t.UsedBy, err = usersOf(ctx, db, t.ID)
		}
		if err != nil {
			return nil, err
		}
	}
	return fs, nil
}

// A run going on in a flow, as moving it to the trash stops it.
type runInTrash struct {
	id int64
	// Started by another flow's run, through a subflow process; nil for a
	// run of the flow's own.
	caller *int64
}

// The runs going on in the flow that stopping it stops: its own, trials
// too, and those other flows' runs started of it. The runs of its flow
// processes and items go with the run they are in.
func runsGoingOn(ctx context.Context, db rowsQuerier, flow int64) ([]runInTrash, error) {
	var rs []runInTrash
	err := scanAll(ctx, db, `SELECT r.id, r.parent_run_id FROM runs r LEFT JOIN runs p ON p.id = r.parent_run_id
		WHERE r.flow_id = ? AND r.status = ? AND (r.parent_run_id IS NULL OR p.flow_id <> r.flow_id) ORDER BY r.id`, func(rows *sql.Rows) error {
		var rn runInTrash
		err := rows.Scan(&rn.id, &rn.caller)
		rs = append(rs, rn)
		return err
	}, flow, runRunning)
	return rs, err
}

// Moves the flow to the trash for the user, unless runs are going on in it
// and stopRuns doesn't say to stop them; then it says how many there are.
// sql.ErrNoRows without the flow, or with it in the trash already.
func trashFlow(ctx context.Context, db *sql.DB, eng *engine, u *user, flow int64, stopRuns bool) (int, error) {
	var name string
	err := inTx(ctx, db, func(tx *sql.Tx) error {
		// Taken as runs starting take it (newRun), so none starts unseen.
		err := tx.QueryRowContext(ctx, "SELECT name FROM flows WHERE id = ? AND deleted_at IS NULL FOR UPDATE", flow).Scan(&name)
		if err != nil {
			return err
		}
		going, err := runsGoingOn(ctx, tx, flow)
		if err != nil {
			return err
		}
		if len(going) > 0 && !stopRuns {
			return runsGoing(len(going))
		}
		var by *int64
		if u != nil {
			by = &u.ID
		}
		_, err = tx.ExecContext(ctx, "UPDATE flows SET deleted_at = CURRENT_TIMESTAMP(3), deleted_by = ? WHERE id = ?", by, flow)
		return err
	})
	var going runsGoing
	if errors.As(err, &going) {
		return int(going), nil
	}
	if err != nil {
		return 0, err
	}
	who := "someone"
	if u != nil {
		who = userLabel(ctx, db, u.ID)
	}
	log.Printf("%s moved flow %d, %s, to the trash", who, flow, quote(name))
	// Read again now that none can start, so none is missed.
	runs, err := runsGoingOn(ctx, db, flow)
	if err != nil {
		return 0, err
	}
	for _, rn := range runs {
		if rn.caller == nil {
			runLog(db, rn.id, 0, logInfo, "stopped: %s moved the flow %s to the trash", who, quote(name))
			if _, err := endRun(context.Background(), db, rn.id, runInterrupted); err != nil {
				return 0, err
			}
			eng.stop(rn.id)
			continue
		}
		why := fmt.Sprintf("%s moved the procedure %s it runs to the trash", who, quote(name))
		runLog(db, *rn.caller, 0, logError, "run #%d stopped: %s", rn.id, why)
		eng.fail(rn.id, why)
	}
	return 0, nil
}

// How many runs are going on in a flow that moving it to the trash would
// stop, which it doesn't without being told to.
type runsGoing int

func (n runsGoing) Error() string {
	return fmt.Sprintf("%d runs are going on in it", int(n))
}

// Why a flow in the trash can't come back under a name, if it can't: a flow
// out of it has the name, or for a struct, its object would clash with
// another a process takes or generates. Empty for a name it can have.
func refuseRestoreName(ctx context.Context, db *sql.DB, flow int64, isStruct bool, name string) (string, error) {
	var taken bool
	err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM flows WHERE name = ? AND deleted_at IS NULL AND id <> ?)", name, flow).Scan(&taken)
	if err != nil || taken {
		return "a flow named " + quote(name) + " is out of the trash already; restore it under another name", err
	}
	if !isStruct {
		return "", nil
	}
	object, err := structRoot(ctx, db, flow)
	if err != nil || object == 0 {
		return "", err
	}
	return renameClash(ctx, db, object, name)
}

// Brings the flow back out of the trash, under name if given: its copies
// follow it again, and its schedules come round at their next time.
// sql.ErrNoRows without the flow in the trash.
func restoreFlow(ctx context.Context, db *sql.DB, flow int64, name string) (string, error) {
	var was string
	var isStruct bool
	err := db.QueryRowContext(ctx, "SELECT name, is_struct FROM flows WHERE id = ? AND deleted_at IS NOT NULL", flow).Scan(&was, &isStruct)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = was
	}
	if msg, err := refuseRestoreName(ctx, db, flow, isStruct, name); msg != "" || err != nil {
		return msg, err
	}
	return "", inTx(ctx, db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT is_struct FROM flows WHERE id = ? AND deleted_at IS NOT NULL FOR UPDATE", flow).Scan(&isStruct)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE flows SET deleted_at = NULL, deleted_by = NULL, name = ? WHERE id = ?", name, flow); err != nil {
			return err
		}
		if isStruct {
			if _, err := tx.ExecContext(ctx, "UPDATE objects SET name = ? WHERE flow_id = ? AND parent_id IS NULL AND owner_id IS NULL", name, flow); err != nil {
				return err
			}
		}
		// Times missed while it was in the trash are skipped, as times
		// missed while the server was down are.
		type sched struct {
			id         int64
			expr, zone string
		}
		var scheds []sched
		err = scanAll(ctx, tx, "SELECT id, cron, timezone FROM schedules WHERE flow_id = ? AND enabled", func(rows *sql.Rows) error {
			var s sched
			err := rows.Scan(&s.id, &s.expr, &s.zone)
			scheds = append(scheds, s)
			return err
		}, flow)
		if err != nil {
			return err
		}
		now := time.Now()
		for _, s := range scheds {
			if _, err := tx.ExecContext(ctx, "UPDATE schedules SET next_run_at = ? WHERE id = ?", nextRun(s.expr, s.zone, true, now), s.id); err != nil {
				return err
			}
		}
		return syncCopies(ctx, tx, flow, true)
	})
}

// Deletes a flow in the trash for good, with its runs, whose workspaces go
// too; with due, only once it has been there that long, as the trash
// empties itself, passing it by while another replica deletes it. Says
// whether it deleted it, and if not, whether runs stopped as it went to the
// trash are still ending.
func deleteForGood(ctx context.Context, db *sql.DB, workerURL string, flow int64, due *time.Duration) (deleted, ending bool, err error) {
	var runs []int64
	err = inTx(ctx, db, func(tx *sql.Tx) error {
		q := "SELECT id FROM flows WHERE id = ? AND deleted_at IS NOT NULL FOR UPDATE"
		args := []any{flow}
		if due != nil {
			q = "SELECT id FROM flows WHERE id = ? AND deleted_at <= CURRENT_TIMESTAMP(3) - INTERVAL ? MICROSECOND FOR UPDATE SKIP LOCKED"
			args = append(args, due.Microseconds())
		}
		if err := tx.QueryRowContext(ctx, q, args...).Scan(&flow); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		var running int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE flow_id = ? AND status = ?", flow, runRunning).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			ending = true
			return nil
		}
		runs, err = deleteFlow(ctx, tx, flow)
		deleted = err == nil
		return err
	})
	if err != nil {
		return false, false, err
	}
	if deleted {
		dropWorkspaces(workerURL, runs)
	}
	return deleted, ending, nil
}

// Empties the trash of what it has kept long enough, at every replica, each
// flow deleted by whichever claims it first.
func emptyTrash(ctx context.Context, db *sql.DB, workerURL string, keep time.Duration) {
	t := time.NewTicker(trashSweepEvery(keep))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := sweepTrash(ctx, db, workerURL, keep); err != nil && ctx.Err() == nil {
				log.Printf("trash: %v", err)
			}
		}
	}
}

// How many flows a sweep of the trash deletes at most; the next sweep goes on.
const trashSweepFlows = 20

func sweepTrash(ctx context.Context, db *sql.DB, workerURL string, keep time.Duration) error {
	var due []int64
	err := scanAll(ctx, db, "SELECT id FROM flows WHERE deleted_at <= CURRENT_TIMESTAMP(3) - INTERVAL ? MICROSECOND ORDER BY deleted_at LIMIT ?", func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		due = append(due, id)
		return err
	}, keep.Microseconds(), trashSweepFlows)
	if err != nil {
		return err
	}
	for _, id := range due {
		deleted, _, err := deleteForGood(ctx, db, workerURL, id, &keep)
		if err != nil {
			return err
		}
		if deleted {
			log.Printf("emptied flow %d out of the trash for good", id)
		}
	}
	return nil
}

func registerTrashRoutes(mux *http.ServeMux, db *sql.DB, eng *engine, workerURL string, keep time.Duration) {
	// Moves the flow to the trash. With runs going on in it, it answers
	// 409 with how many, {"running": n}, unless {"stopRuns": true} says to
	// stop them.
	mux.HandleFunc("DELETE /api/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			StopRuns bool `json:"stopRuns"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		running, err := trashFlow(r.Context(), db, eng, requestUser(r), id, body.StopRuns)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "flow not found", http.StatusNotFound)
		case err != nil:
			serverError(w, err)
		case running > 0:
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   fmt.Sprintf("%d runs are going on in it, which moving it to the trash stops", running),
				"running": running,
			})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	// The flows in the trash whoever asks may restore.
	mux.HandleFunc("GET /api/trash", func(w http.ResponseWriter, r *http.Request) {
		fs, err := listTrash(r.Context(), db, requestUser(r), keep)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, fs)
	})

	// The flow in the trash a request names, if whoever asks may restore it
	// or delete it for good; writes why not if not.
	trashed := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return 0, false
		}
		may, err := mayEmpty(r.Context(), db, requestUser(r), id)
		if err != nil {
			serverError(w, err)
			return 0, false
		}
		if !may {
			http.Error(w, "nothing of that id is in the trash", http.StatusNotFound)
			return 0, false
		}
		return id, true
	}

	// Brings the flow back, under its name, or {"name": "..."} if given.
	mux.HandleFunc("POST /api/trash/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id, ok := trashed(w, r)
		if !ok {
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		msg, err := restoreFlow(r.Context(), db, id, strings.TrimSpace(body.Name))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "nothing of that id is in the trash", http.StatusNotFound)
			return
		case err != nil:
			serverError(w, err)
			return
		case msg != "":
			http.Error(w, msg, http.StatusConflict)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if err == nil {
			f.Access, err = flowAccess(r.Context(), db, requestUser(r), id)
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

	// Deletes the flow for good, with everything on its boards, its versions
	// and its runs.
	mux.HandleFunc("DELETE /api/trash/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := trashed(w, r)
		if !ok {
			return
		}
		deleted, ending, err := deleteForGood(r.Context(), db, workerURL, id, nil)
		switch {
		case err != nil:
			serverError(w, err)
		case ending:
			http.Error(w, "its runs are still stopping; try again in a moment", http.StatusConflict)
		case !deleted:
			http.Error(w, "nothing of that id is in the trash", http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
