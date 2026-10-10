package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	// The container has no zone database; schedules name their time zone.
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// A schedule runs a flow on its own, whenever its cron expression says, as
// the Run button would: on the board as it is. The server checks every
// second for schedules due, and each one due starts a run unless its last
// run is still going, such as one waiting on a person; that time is skipped.
// Times missed while the server was down are skipped too: a schedule only
// carries on from its next time.
//
// Every server checks, so with several of them each time is claimed by
// moving the schedule's next time on, which only one of them gets to do.

type schedule struct {
	ID       int64  `json:"id"`
	Flow     int64  `json:"flow"`
	FlowName string `json:"flowName"`
	// Five fields, minute to day of the week, or a descriptor such as
	// @hourly or @every 90m; a minute apart at the least.
	Cron string `json:"cron"`
	// The IANA zone the cron expression's times are in, such as Europe/London.
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`
	// When it runs next; null while disabled.
	NextRunAt *time.Time `json:"nextRunAt"`
	// The latest run it started, if any; while it is running, the times due
	// are skipped.
	LastRun   *run      `json:"lastRun"`
	CreatedAt time.Time `json:"createdAt"`
}

// Times are kept as UTC, in DATETIME so MySQL's session time zone doesn't
// move them.
func migrateSchedules(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schedules (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			flow_id BIGINT NOT NULL,
			cron VARCHAR(255) NOT NULL,
			timezone VARCHAR(64) NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			next_run_at DATETIME(3) NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE,
			INDEX schedules_due (enabled, next_run_at)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	// Each run started by a schedule says which one.
	has, err := hasIndex(db, "runs", "runs_schedule")
	if err != nil || has {
		return err
	}
	_, err = db.Exec("ALTER TABLE runs ADD CONSTRAINT runs_schedule FOREIGN KEY (schedule_id) REFERENCES schedules(id) ON DELETE SET NULL")
	return err
}

// Schedules run a minute apart at the least. The tests' webapps set
// SCHEDULE_SECONDS, which takes a sixth field, for seconds, first, so their
// schedules come round in seconds rather than minutes.
var (
	scheduleSeconds = os.Getenv("SCHEDULE_SECONDS") != ""
	cronFields      = cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor
	cronParser      = cron.NewParser(cronFields)
)

func init() {
	if scheduleSeconds {
		cronParser = cron.NewParser(cron.SecondOptional | cronFields)
	}
}

// The times a cron expression in a time zone gives, or why it gives none.
func parseSchedule(expr, zone string) (cron.Schedule, *time.Location, string) {
	if strings.Contains(expr, "TZ=") {
		return nil, nil, "set the time zone on its own, not in the cron expression"
	}
	if zone == "" {
		return nil, nil, "a time zone is required, such as UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, nil, "unknown time zone " + strconv.Quote(zone)
	}
	s, err := cronParser.Parse(expr)
	if err != nil {
		return nil, nil, "invalid cron expression: " + err.Error()
	}
	if every, ok := s.(cron.ConstantDelaySchedule); ok && every.Delay < time.Minute && !scheduleSeconds {
		return nil, nil, "a schedule runs a minute apart at the least"
	}
	if s.Next(time.Now().In(loc)).IsZero() {
		return nil, nil, "the cron expression never comes round"
	}
	return s, loc, ""
}

// The first n times after t, in UTC, to the millisecond as kept.
func upcoming(s cron.Schedule, loc *time.Location, t time.Time, n int) []time.Time {
	var ts []time.Time
	for t = t.In(loc); len(ts) < n; {
		t = s.Next(t)
		if t.IsZero() {
			break
		}
		ts = append(ts, t.UTC().Truncate(time.Millisecond))
	}
	return ts
}

// When a schedule runs next after t; nil while it is disabled, or if its
// expression no longer parses.
func nextRun(expr, zone string, enabled bool, t time.Time) *time.Time {
	if !enabled {
		return nil
	}
	s, loc, msg := parseSchedule(expr, zone)
	if msg != "" {
		return nil
	}
	ts := upcoming(s, loc, t, 1)
	if len(ts) == 0 {
		return nil
	}
	return &ts[0]
}

const scheduleColumns = "s.id, s.flow_id, f.name, s.cron, s.timezone, s.enabled, s.next_run_at, s.created_at"

func scanSchedule(row interface{ Scan(...any) error }, s *schedule) error {
	return row.Scan(&s.ID, &s.Flow, &s.FlowName, &s.Cron, &s.Timezone, &s.Enabled, &s.NextRunAt, &s.CreatedAt)
}

// Fills in the schedule's latest run.
func withLastRun(ctx context.Context, db *sql.DB, s *schedule) error {
	var rn run
	err := scanRun(db.QueryRowContext(ctx, "SELECT "+runColumns+" FROM runs WHERE schedule_id = ? ORDER BY id DESC LIMIT 1", s.ID), &rn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err == nil {
		s.LastRun = &rn
	}
	return err
}

func getSchedule(ctx context.Context, db *sql.DB, id int64) (schedule, error) {
	var s schedule
	err := scanSchedule(db.QueryRowContext(ctx, "SELECT "+scheduleColumns+" FROM schedules s JOIN flows f ON f.id = s.flow_id WHERE s.id = ?", id), &s)
	if err == nil {
		err = withLastRun(ctx, db, &s)
	}
	return s, err
}

// Starts the runs of the schedules due, every tick, until ctx is done.
func runSchedules(ctx context.Context, db *sql.DB, eng *engine, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := fireDue(ctx, db, eng, time.Now()); err != nil && ctx.Err() == nil {
				log.Printf("schedules: %v", err)
			}
		}
	}
}

// Starts a run for each schedule due at now that this server claims.
func fireDue(ctx context.Context, db *sql.DB, eng *engine, now time.Time) error {
	type due struct {
		id, flow   int64
		expr, zone string
		next       time.Time
	}
	var dues []due
	rows, err := db.QueryContext(ctx, "SELECT id, flow_id, cron, timezone, next_run_at FROM schedules WHERE enabled AND next_run_at <= ?", now.UTC())
	if err != nil {
		return err
	}
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.flow, &d.expr, &d.zone, &d.next); err != nil {
			rows.Close()
			return err
		}
		dues = append(dues, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range dues {
		// Its next time after now, not after the time due, so times missed
		// are skipped rather than run all at once.
		res, err := db.ExecContext(ctx, "UPDATE schedules SET next_run_at = ? WHERE id = ? AND enabled AND next_run_at = ?",
			nextRun(d.expr, d.zone, true, now), d.id, d.next)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			// Another server claimed it, or it changed since.
			continue
		}
		var running int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE schedule_id = ? AND status = ?", d.id, runRunning).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			log.Printf("schedule %d: skipped, its last run is still going", d.id)
			continue
		}
		id := d.id
		if _, err := startRun(ctx, db, eng, d.flow, &id); err != nil {
			log.Printf("schedule %d: start a run of flow %d: %v", d.id, d.flow, err)
		}
	}
	return nil
}

func registerScheduleRoutes(mux *http.ServeMux, db *sql.DB) {
	// Every schedule, or with flow, those of that flow, by flow then id.
	mux.HandleFunc("GET /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		q := "SELECT " + scheduleColumns + " FROM schedules s JOIN flows f ON f.id = s.flow_id"
		var args []any
		if r.URL.Query().Has("flow") {
			f, ok := flowParam(w, r)
			if !ok {
				return
			}
			q += " WHERE s.flow_id = ?"
			args = append(args, f)
		}
		rows, err := db.QueryContext(r.Context(), q+" ORDER BY f.name, s.id", args...)
		if err != nil {
			serverError(w, err)
			return
		}
		ss := []schedule{}
		for rows.Next() {
			var s schedule
			if err := scanSchedule(rows, &s); err != nil {
				rows.Close()
				serverError(w, err)
				return
			}
			ss = append(ss, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		for i := range ss {
			if err := withLastRun(r.Context(), db, &ss[i]); err != nil {
				serverError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, ss)
	})

	// The next times a cron expression gives in a time zone, to check it
	// before saving.
	mux.HandleFunc("GET /api/schedules/preview", func(w http.ResponseWriter, r *http.Request) {
		s, loc, msg := parseSchedule(strings.TrimSpace(r.URL.Query().Get("cron")), r.URL.Query().Get("timezone"))
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]time.Time{"upcoming": upcoming(s, loc, time.Now(), 5)})
	})

	mux.HandleFunc("POST /api/schedules", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Flow     int64  `json:"flow"`
			Cron     string `json:"cron"`
			Timezone string `json:"timezone"`
			Enabled  *bool  `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		body.Cron = strings.TrimSpace(body.Cron)
		if _, _, msg := parseSchedule(body.Cron, body.Timezone); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		var n int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM flows WHERE id = ?", body.Flow).Scan(&n); err != nil {
			serverError(w, err)
			return
		}
		if n == 0 {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if refuseInStruct(w, r, db, body.Flow) {
			return
		}
		enabled := body.Enabled == nil || *body.Enabled
		res, err := db.ExecContext(r.Context(), "INSERT INTO schedules (flow_id, cron, timezone, enabled, next_run_at) VALUES (?, ?, ?, ?, ?)",
			body.Flow, body.Cron, body.Timezone, enabled, nextRun(body.Cron, body.Timezone, enabled, time.Now()))
		if err != nil {
			serverError(w, err)
			return
		}
		id, err := res.LastInsertId()
		if err != nil {
			serverError(w, err)
			return
		}
		s, err := getSchedule(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s)
	})

	// Changing when it runs, or enabling it, counts its next time from now.
	mux.HandleFunc("PATCH /api/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		s, err := getSchedule(r.Context(), db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		var patch struct {
			Flow     *int64  `json:"flow"`
			Cron     *string `json:"cron"`
			Timezone *string `json:"timezone"`
			Enabled  *bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		was := s
		if patch.Flow != nil {
			s.Flow = *patch.Flow
		}
		if patch.Cron != nil {
			s.Cron = strings.TrimSpace(*patch.Cron)
		}
		if patch.Timezone != nil {
			s.Timezone = *patch.Timezone
		}
		if patch.Enabled != nil {
			s.Enabled = *patch.Enabled
		}
		if _, _, msg := parseSchedule(s.Cron, s.Timezone); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if s.Flow != was.Flow {
			var n int
			if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM flows WHERE id = ?", s.Flow).Scan(&n); err != nil {
				serverError(w, err)
				return
			}
			if n == 0 {
				http.Error(w, "flow not found", http.StatusNotFound)
				return
			}
			if refuseInStruct(w, r, db, s.Flow) {
				return
			}
		}
		next := s.NextRunAt
		if s.Cron != was.Cron || s.Timezone != was.Timezone || s.Enabled != was.Enabled || next == nil {
			next = nextRun(s.Cron, s.Timezone, s.Enabled, time.Now())
		}
		_, err = db.ExecContext(r.Context(), "UPDATE schedules SET flow_id = ?, cron = ?, timezone = ?, enabled = ?, next_run_at = ? WHERE id = ?",
			s.Flow, s.Cron, s.Timezone, s.Enabled, next, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if s, err = getSchedule(r.Context(), db, id); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	// Its runs stay, as runs no schedule started.
	mux.HandleFunc("DELETE /api/schedules/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		res, err := db.ExecContext(r.Context(), "DELETE FROM schedules WHERE id = ?", id)
		if err != nil {
			serverError(w, err)
			return
		}
		if n, err := res.RowsAffected(); err != nil {
			serverError(w, err)
			return
		} else if n == 0 {
			http.Error(w, "schedule not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
