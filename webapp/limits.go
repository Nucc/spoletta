package main

// What the instance lets a run and a person use, set by admins in the
// settings' Spending tab (Spending.tsx) and kept in limits, a row each,
// none or 0 for no limit:
//
//   - run_spend: what a run may spend on its models, its subflows' and
//     items' runs and follow-ups included, in millionths of a dollar. A
//     flow may set a lower one (flowSettings.RunSpendLimit). Once a run has
//     spent it, the model proxy refuses its calls and the engine stops the
//     run, which fails saying so; see spendLimits in llm.go.
//   - user_month_spend: what a person's runs, follow-ups and assistant
//     replies may spend in a calendar month (UTC), after which the proxy
//     refuses their calls until the next.
//   - user_parallel_steps: how many agent, script, Sentry and connector
//     steps of a person's runs go at once, of the engine's PARALLEL_STEPS,
//     so one person's runs can't take every slot; the rest wait their turn
//     (engine.go's personSlot). Runs nobody started, without sign-in, have
//     no person.
//   - run_disk_mb: how much disk a run's workspaces, sessions, home and
//     files may take, in MB, which the worker checks after each of its
//     steps (its disk.go), failing a step that leaves the run over it.
//
// Limits are read as the webapp starts and kept here; saving them takes
// effect for steps started after.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
)

type instanceLimits struct {
	RunSpend       int64
	UserMonthSpend int64
	UserSteps      int64
	RunDiskMB      int64
}

// The most disk a run may take, in bytes.
func (l instanceLimits) diskBytes() int64 { return l.RunDiskMB << 20 }

var limits struct {
	mu sync.Mutex
	l  instanceLimits
}

func currentLimits() instanceLimits {
	limits.mu.Lock()
	defer limits.mu.Unlock()
	return limits.l
}

// The rows of limits by their names.
func (l *instanceLimits) fields() map[string]*int64 {
	return map[string]*int64{"run_spend": &l.RunSpend, "user_month_spend": &l.UserMonthSpend, "user_parallel_steps": &l.UserSteps, "run_disk_mb": &l.RunDiskMB}
}

func loadLimits(ctx context.Context, db *sql.DB) error {
	var l instanceLimits
	fields := l.fields()
	err := scanAll(ctx, db, "SELECT name, value FROM limits", func(r *sql.Rows) error {
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
	if err != nil {
		return err
	}
	limits.mu.Lock()
	limits.l = l
	limits.mu.Unlock()
	return nil
}

func saveLimits(ctx context.Context, db *sql.DB, l instanceLimits) error {
	for name, v := range l.fields() {
		if _, err := db.ExecContext(ctx, "INSERT INTO limits (name, value) VALUES (?, ?) ON DUPLICATE KEY UPDATE value = VALUES(value)", name, *v); err != nil {
			return err
		}
	}
	limits.mu.Lock()
	limits.l = l
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
// closer under a dime.
func formatDollars(micros int64) string {
	if micros < 100000 {
		return fmt.Sprintf("$%.4f", dollars(micros))
	}
	return fmt.Sprintf("$%.2f", dollars(micros))
}

// The limits as the settings show and send them: dollars, 0 for none.
type limitsJSON struct {
	RunSpend       float64 `json:"runSpend"`
	UserMonthSpend float64 `json:"userMonthSpend"`
	UserSteps      int64   `json:"userParallelSteps"`
	RunDiskMB      int64   `json:"runDiskMb"`
}

func (l instanceLimits) json() limitsJSON {
	return limitsJSON{RunSpend: dollars(l.RunSpend), UserMonthSpend: dollars(l.UserMonthSpend), UserSteps: l.UserSteps, RunDiskMB: l.RunDiskMB}
}

func registerLimitRoutes(mux *http.ServeMux, db *sql.DB) {
	// Everyone may see what they may use.
	mux.HandleFunc("GET /api/limits", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, currentLimits().json())
	})
	mux.HandleFunc("PUT /api/limits", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var body limitsJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		var l instanceLimits
		var err error
		if l.RunSpend, err = dollarMicros(body.RunSpend); err != nil {
			http.Error(w, "what a run may spend: "+err.Error(), http.StatusBadRequest)
			return
		}
		if l.UserMonthSpend, err = dollarMicros(body.UserMonthSpend); err != nil {
			http.Error(w, "what a person may spend a month: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.UserSteps < 0 || body.UserSteps > 10000 {
			http.Error(w, "how many steps a person's runs may go at once isn't a number of steps", http.StatusBadRequest)
			return
		}
		if body.RunDiskMB < 0 || body.RunDiskMB > 1<<30 {
			http.Error(w, "how much disk a run may take isn't a number of MB", http.StatusBadRequest)
			return
		}
		l.UserSteps, l.RunDiskMB = body.UserSteps, body.RunDiskMB
		if err := saveLimits(r.Context(), db, l); err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "limits.set", "a run may spend %s, a person %s a month, with %d steps at once, and a run take %d MB of disk (0 for no limit)",
			formatDollars(l.RunSpend), formatDollars(l.UserMonthSpend), l.UserSteps, l.RunDiskMB)
		writeJSON(w, http.StatusOK, l.json())
	})
}
