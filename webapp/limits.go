package main

// What a project lets its runs and people use, set by its owners in the
// settings' Spending tab (Spending.tsx) and kept in limits, a row each by
// project and name, none or 0 for no limit of its own:
//
//   - run_spend: what a run may spend on its models, its subflows' and
//     items' runs and follow-ups included, in millionths of a dollar. A
//     flow may set a lower one (flowSettings.RunSpendLimit). Once a run has
//     spent it, the model proxy refuses its calls and the engine stops the
//     run, which fails saying so; see spendLimits in llm.go.
//   - user_month_spend: what a person's runs, follow-ups and assistant
//     replies in the project may spend in a calendar month (UTC), after
//     which the proxy refuses their calls there until the next.
//   - user_parallel_steps: how many agent, script and connector steps of
//     a person's runs in the project go at once, of the engine's
//     PARALLEL_STEPS, so one person's runs can't take every slot; the rest
//     wait their turn (engine.go's personSlot). Runs nobody started,
//     without sign-in, have no person.
//   - run_disk_mb: how much disk a run's workspaces, sessions, home and
//     files may take, in MB, which the worker checks after each of its
//     steps (its disk.go), failing a step that leaves the run over it.
//
// Above them the instance's admin sets ceilings no project may exceed, by
// the same names in instance_limits: an owner may set no more than a
// ceiling, and what the proxy, the engine and the worker hold a project's
// runs and people to is the least of its own limit and the ceiling (0
// being none), so a ceiling lowered holds at once over a project's limit
// set before.
//
// Limits and ceilings are read as the webapp starts and kept here by
// project, and read again every few seconds, so those saved at another
// replica reach this one soon after; saving them takes effect for steps
// started after.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"
)

// A set of limits: a project's own, a ceiling, or what holds of the two.
type limitSet struct {
	RunSpend       int64
	UserMonthSpend int64
	UserSteps      int64
	RunDiskMB      int64
}

// The most disk a run may take, in bytes.
func (l limitSet) diskBytes() int64 { return l.RunDiskMB << 20 }

var limits struct {
	mu sync.Mutex
	// Each project's own, by project.
	projects map[int64]limitSet
	ceilings limitSet
}

// What holds of the project's limits under the instance's ceilings: of
// each, the least of the two, 0 for neither.
func limitsIn(project int64) limitSet {
	limits.mu.Lock()
	defer limits.mu.Unlock()
	return limits.projects[project].under(limits.ceilings)
}

// The limits under the ceilings.
func (l limitSet) under(c limitSet) limitSet {
	return limitSet{
		RunSpend:       leastLimit(l.RunSpend, c.RunSpend),
		UserMonthSpend: leastLimit(l.UserMonthSpend, c.UserMonthSpend),
		UserSteps:      leastLimit(l.UserSteps, c.UserSteps),
		RunDiskMB:      leastLimit(l.RunDiskMB, c.RunDiskMB),
	}
}

// The project's own limits, as its owners set them.
func ownLimits(project int64) limitSet {
	limits.mu.Lock()
	defer limits.mu.Unlock()
	return limits.projects[project]
}

// The instance's ceilings.
func currentCeilings() limitSet {
	limits.mu.Lock()
	defer limits.mu.Unlock()
	return limits.ceilings
}

// The rows of limits by their names.
func (l *limitSet) fields() map[string]*int64 {
	return map[string]*int64{"run_spend": &l.RunSpend, "user_month_spend": &l.UserMonthSpend, "user_parallel_steps": &l.UserSteps, "run_disk_mb": &l.RunDiskMB}
}

// Reads every project's limits and the ceilings.
func loadLimits(ctx context.Context, db *sql.DB) error {
	projects := map[int64]limitSet{}
	err := scanAll(ctx, db, "SELECT project_id, name, value FROM limits", func(r *sql.Rows) error {
		var project int64
		var name string
		var v int64
		if err := r.Scan(&project, &name, &v); err != nil {
			return err
		}
		l := projects[project]
		if f, ok := l.fields()[name]; ok {
			*f = v
		}
		projects[project] = l
		return nil
	})
	if err != nil {
		return err
	}
	ceilings, err := readCeilings(ctx, db)
	if err != nil {
		return err
	}
	limits.mu.Lock()
	limits.projects, limits.ceilings = projects, ceilings
	limits.mu.Unlock()
	return nil
}

// The ceilings as the database has them now.
func readCeilings(ctx context.Context, db rowsQuerier) (limitSet, error) {
	var c limitSet
	fields := c.fields()
	err := scanAll(ctx, db, "SELECT name, value FROM instance_limits", func(r *sql.Rows) error {
		var name string
		var v int64
		if err := r.Scan(&name, &v); err != nil {
			return err
		}
		if f, ok := fields[name]; ok {
			*f = v
		}
		return nil
	})
	return c, err
}

// How often the limits are read again.
const limitsFor = 5 * time.Second

// Reads the limits again every limitsFor, until ctx ends.
func reloadLimits(ctx context.Context, db *sql.DB) {
	t := time.NewTicker(limitsFor)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := loadLimits(ctx, db); err != nil {
			log.Printf("WARNING: couldn't read the limits again: %v", err)
		}
	}
}

// Saves the project's own limits.
func saveLimits(ctx context.Context, db *sql.DB, project int64, l limitSet) error {
	for name, v := range l.fields() {
		if _, err := db.ExecContext(ctx, "INSERT INTO limits (project_id, name, value) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)", project, name, *v); err != nil {
			return err
		}
	}
	limits.mu.Lock()
	if limits.projects == nil {
		limits.projects = map[int64]limitSet{}
	}
	limits.projects[project] = l
	limits.mu.Unlock()
	return nil
}

// Saves the instance's ceilings.
func saveCeilings(ctx context.Context, db *sql.DB, c limitSet) error {
	for name, v := range c.fields() {
		if _, err := db.ExecContext(ctx, "INSERT INTO instance_limits (name, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)", name, *v); err != nil {
			return err
		}
	}
	limits.mu.Lock()
	limits.ceilings = c
	limits.mu.Unlock()
	return nil
}

// Dollars as millionths of one, refusing what isn't an amount.
func dollarMicros(d float64) (int64, error) {
	if math.IsNaN(d) || math.IsInf(d, 0) || d < 0 || d > 1e9 {
		return 0, fmt.Errorf("%v isn't an amount of dollars", d)
	}
	return int64(math.Round(d * 1e6)), nil
}

func dollars(micros int64) float64 { return float64(micros) / 1e6 }

// An amount of millionths of a dollar as people read it, to the cent, or
// closer under a dime, and to the millionth under a hundredth of a cent.
func formatDollars(micros int64) string {
	if micros < 100 {
		return fmt.Sprintf("$%.6f", dollars(micros))
	}
	if micros < 100000 {
		return fmt.Sprintf("$%.4f", dollars(micros))
	}
	return fmt.Sprintf("$%.2f", dollars(micros))
}

// Limits as the settings show and send them: dollars, 0 for none.
type limitsJSON struct {
	RunSpend       float64 `json:"runSpend"`
	UserMonthSpend float64 `json:"userMonthSpend"`
	UserSteps      int64   `json:"userParallelSteps"`
	RunDiskMB      int64   `json:"runDiskMb"`
}

func (l limitSet) json() limitsJSON {
	return limitsJSON{RunSpend: dollars(l.RunSpend), UserMonthSpend: dollars(l.UserMonthSpend), UserSteps: l.UserSteps, RunDiskMB: l.RunDiskMB}
}

// The limits a request sent, or what is wrong with them.
func (body limitsJSON) limits() (limitSet, string) {
	var l limitSet
	var err error
	if l.RunSpend, err = dollarMicros(body.RunSpend); err != nil {
		return l, "what a run may spend: " + err.Error()
	}
	if l.UserMonthSpend, err = dollarMicros(body.UserMonthSpend); err != nil {
		return l, "what a person may spend a month: " + err.Error()
	}
	if body.UserSteps < 0 || body.UserSteps > 10000 {
		return l, "how many steps a person's runs may go at once isn't a number of steps"
	}
	if body.RunDiskMB < 0 || body.RunDiskMB > 1<<30 {
		return l, "how much disk a run may take isn't a number of MB"
	}
	l.UserSteps, l.RunDiskMB = body.UserSteps, body.RunDiskMB
	return l, ""
}

// Why a project's limits go over the ceilings, if they do: a limit of its
// own above a ceiling. None of its own leaves the ceiling to hold.
func (l limitSet) overCeilings(c limitSet) string {
	over := func(own, ceiling int64) bool { return ceiling > 0 && own > ceiling }
	switch {
	case over(l.RunSpend, c.RunSpend):
		return fmt.Sprintf("a run may spend at most %s, the instance's ceiling", formatDollars(c.RunSpend))
	case over(l.UserMonthSpend, c.UserMonthSpend):
		return fmt.Sprintf("a person may spend at most %s a month, the instance's ceiling", formatDollars(c.UserMonthSpend))
	case over(l.UserSteps, c.UserSteps):
		return fmt.Sprintf("a person's runs may have at most %d steps going at once, the instance's ceiling", c.UserSteps)
	case over(l.RunDiskMB, c.RunDiskMB):
		return fmt.Sprintf("a run may take at most %d MB of disk, the instance's ceiling", c.RunDiskMB)
	}
	return ""
}

// A project's limits as the settings show them: its own, 0 for none of
// its own, the ceilings above them, and what holds of the two.
type projectLimitsJSON struct {
	limitsJSON
	Ceilings limitsJSON `json:"ceilings"`
	Holding  limitsJSON `json:"holding"`
}

func projectLimitsOf(project int64) projectLimitsJSON {
	return projectLimitsJSON{limitsJSON: ownLimits(project).json(), Ceilings: currentCeilings().json(), Holding: limitsIn(project).json()}
}

func registerLimitRoutes(mux *http.ServeMux, db *sql.DB) {
	// Everyone in the project may see what they may use there.
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, projectLimitsOf(requestProject(r)))
	})
	// The project's own limits, which its owners set under the ceilings.
	mux.HandleFunc("PUT /api/limits", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var body limitsJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		l, msg := body.limits()
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		// The ceilings as they are now, not as this replica last read them.
		ceilings, err := readCeilings(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg := l.overCeilings(ceilings); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		project := requestProject(r)
		if err := saveLimits(r.Context(), db, project, l); err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "limits.set", "a run may spend %s, a person %s a month, with %d steps at once, and a run take %d MB of disk (0 for no limit)",
			formatDollars(l.RunSpend), formatDollars(l.UserMonthSpend), l.UserSteps, l.RunDiskMB)
		writeJSON(w, http.StatusOK, projectLimitsOf(project))
	})

	// The ceilings no project's limits may exceed; for super admins.
	mux.HandleFunc("GET /api/instance/limits", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonSuper(w, r) {
			return
		}
		writeJSON(w, http.StatusOK, currentCeilings().json())
	})
	mux.HandleFunc("PUT /api/instance/limits", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonSuper(w, r) {
			return
		}
		var body limitsJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		c, msg := body.limits()
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := saveCeilings(r.Context(), db, c); err != nil {
			serverError(w, err)
			return
		}
		auditInstance("limits.ceilings", requestUserID(r), "no project's run may spend more than %s, a person %s a month, with %d steps at once, nor a run take more than %d MB of disk (0 for no ceiling)",
			formatDollars(c.RunSpend), formatDollars(c.UserMonthSpend), c.UserSteps, c.RunDiskMB)
		writeJSON(w, http.StatusOK, c.json())
	})
}
