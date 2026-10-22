package main

// What agents spend: each call the model proxy (llm.go) passes on is
// recorded in llm_usage with what it cost, worked out as it is recorded
// from the model's price, so a price changed later doesn't change what was
// spent. Prices are dollars a million tokens, as pi's catalog has them, for
// what a call took in, gave out, read from the provider's cache and wrote
// to it; costs are kept in millionths of a dollar.
//
// A model's price is the one the worker's models.json gives it, else pi's
// catalog's for the provider and model, else the catalog's for a model of
// the ID with any provider, which is how a gateway's or a custom
// provider's models are priced: their IDs are mostly a provider's own
// (claude-sonnet-4-6) or OpenRouter's (anthropic/claude-sonnet-4.6), or
// those with what comes before a slash left out. A model none of them
// prices is counted at unknownModelCost, high on purpose so a limit isn't
// passed unseen, and its rows say they weren't priced.

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// A model's price, in dollars a million tokens.
type modelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// What a model no price is known for is counted at: the most the largest
// models cost.
var unknownModelCost = modelCost{Input: 15, Output: 75, CacheRead: 1.5, CacheWrite: 18.75}

// What a call cost, in millionths of a dollar: a dollar a million tokens is
// a millionth a token.
func (c modelCost) micros(u llmUsage) int64 {
	return int64(math.Round(float64(u.input)*c.Input + float64(u.output)*c.Output +
		float64(u.cacheRead)*c.CacheRead + float64(u.cacheWrite)*c.CacheWrite))
}

// How long the catalog's prices are kept before the worker is asked again.
const pricesFor = 10 * time.Minute

// The prices the broker knows: the worker's models.json's by provider and
// model, then the catalog's.
type priceBook struct {
	host    map[string]map[string]modelCost
	catalog map[string]map[string]modelCost
}

func (b *llmBroker) prices(ctx context.Context) priceBook {
	p := priceBook{host: map[string]map[string]modelCost{}}
	if host, _ := b.hostProviders(ctx); host != nil {
		for _, h := range host {
			for _, m := range h.Models {
				if p.host[h.ID] == nil {
					p.host[h.ID] = map[string]modelCost{}
				}
				p.host[h.ID][m.ID] = m.Cost
			}
		}
	}
	b.mu.Lock()
	p.catalog = b.catalog
	stale := time.Since(b.catalogAt) >= pricesFor
	b.mu.Unlock()
	if stale {
		var c struct {
			Prices map[string]map[string]modelCost `json:"prices"`
		}
		// What it said last when it doesn't answer.
		if err := callWorker(ctx, http.MethodGet, b.workerURL+"/catalog", nil, &c); err == nil && c.Prices != nil {
			p.catalog = c.Prices
			b.mu.Lock()
			b.catalog, b.catalogAt = c.Prices, time.Now()
			b.mu.Unlock()
		}
	}
	return p
}

// A model's price, and whether one is known.
func (p priceBook) of(provider, model string) (modelCost, bool) {
	if c, ok := p.host[provider][model]; ok {
		return c, true
	}
	if c, ok := p.catalog[provider][model]; ok {
		return c, true
	}
	// Any provider's, the first by its ID, so the same is picked each time.
	ids := make([]string, 0, len(p.catalog))
	for id := range p.catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	names := []string{model}
	if i := strings.LastIndex(model, "/"); i >= 0 && i < len(model)-1 {
		names = append(names, model[i+1:])
	}
	for _, name := range names {
		for _, id := range ids {
			if c, ok := p.catalog[id][name]; ok {
				return c, true
			}
		}
	}
	return modelCost{}, false
}

// What a run, with its subflows' and items' runs, and a person this month
// have spent, kept while a token of theirs is alive and read again from
// llm_usage when the first is granted. A run's calls are checked against
// the least of the instance's limit and its flow's as they come to the
// proxy, and a person's against the instance's; the call that crosses a
// limit is let through, as what it costs is only known once it is over,
// and so are calls already going, so a run with steps running at once
// spends a little more than its limit.
type runSpend struct {
	micros int64
	tokens int
	// Over its limit, and stopped.
	over bool
}

type userSpend struct {
	month  string
	micros int64
	tokens int
}

// The month spend is counted by, as YYYY-MM in UTC.
func spendMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// The run whose spending a run's counts toward: the run its subflow or
// item ran in, and so on to the top, with the most its flow lets it spend.
func rootRun(ctx context.Context, db *sql.DB, run int64) (int64, int64, error) {
	for {
		var parent sql.NullInt64
		var settings flowSettings
		err := db.QueryRowContext(ctx, "SELECT r.parent_run_id, f.settings FROM runs r JOIN flows f ON f.id = r.flow_id WHERE r.id = ?", run).
			Scan(&parent, &settings)
		if err != nil {
			return 0, 0, err
		}
		if !parent.Valid {
			limit, _ := dollarMicros(settings.RunSpendLimit)
			return run, limit, nil
		}
		run = parent.Int64
	}
}

// The least of two limits, 0 being none.
func leastLimit(a, b int64) int64 {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

// Counts a grant's spending from what was spent before it, and sets the
// limits it is held to.
func (b *llmBroker) trackSpend(ctx context.Context, g *llmGrant) error {
	l := currentLimits()
	g.userLimit = l.UserMonthSpend
	if g.run != 0 {
		root, flowLimit, err := rootRun(ctx, b.db, g.run)
		if err != nil {
			return err
		}
		g.root, g.runLimit = root, leastLimit(l.RunSpend, flowLimit)
	}
	now := time.Now().UTC()
	month := spendMonth(now)
	var runSpent, userSpent int64
	if g.root != 0 {
		if err := b.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_micros), 0) FROM llm_usage WHERE root_run_id = ?", g.root).Scan(&runSpent); err != nil {
			return err
		}
	}
	if g.user != nil {
		if err := b.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_micros), 0) FROM llm_usage WHERE user_id = ? AND at >= ?",
			*g.user, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)).Scan(&userSpent); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if g.root != 0 {
		rs := b.runSpend[g.root]
		if rs == nil {
			rs = &runSpend{micros: runSpent}
			b.runSpend[g.root] = rs
		}
		rs.tokens++
	}
	if g.user != nil {
		us := b.userSpend[*g.user]
		if us == nil || us.month != month {
			tokens := 0
			if us != nil {
				tokens = us.tokens
			}
			us = &userSpend{month: month, micros: userSpent, tokens: tokens}
			b.userSpend[*g.user] = us
		}
		us.tokens++
	}
	return nil
}

// Stops counting a grant's spending; b.mu is held.
func (b *llmBroker) untrackSpend(g *llmGrant) {
	if rs := b.runSpend[g.root]; rs != nil {
		if rs.tokens--; rs.tokens <= 0 {
			delete(b.runSpend, g.root)
		}
	}
	if g.user != nil {
		if us := b.userSpend[*g.user]; us != nil {
			if us.tokens--; us.tokens <= 0 {
				delete(b.userSpend, *g.user)
			}
		}
	}
}

// Why a grant's call is refused, if it is: its run or its person has spent
// what they may. pi doesn't try again a call refused for being out of
// budget, so the step fails saying why. b.mu is held.
func (b *llmBroker) outOfBudget(g *llmGrant) string {
	if rs := b.runSpend[g.root]; rs != nil && g.runLimit > 0 && rs.micros >= g.runLimit {
		return fmt.Sprintf("out of budget: the run has spent %s on its models, and may spend %s", formatDollars(rs.micros), formatDollars(g.runLimit))
	}
	if g.user != nil && g.userLimit > 0 {
		if us := b.userSpend[*g.user]; us != nil && us.month == spendMonth(time.Now()) && us.micros >= g.userLimit {
			return fmt.Sprintf("out of budget: its person has spent %s on models this month, and may spend %s", formatDollars(us.micros), formatDollars(g.userLimit))
		}
	}
	return ""
}

// Adds what a call cost to its run's and person's spending. A run it takes
// past its limit is stopped: its tokens are revoked, and the engine fails
// the run saying why.
func (b *llmBroker) spent(g *llmGrant, micros int64) {
	b.mu.Lock()
	var stop string
	if us := b.userSpend[derefUser(g.user)]; g.user != nil && us != nil {
		if month := spendMonth(time.Now()); us.month != month {
			us.month, us.micros = month, 0
		}
		us.micros += micros
	}
	rs := b.runSpend[g.root]
	if rs != nil {
		rs.micros += micros
		if g.runLimit > 0 && rs.micros >= g.runLimit && !rs.over {
			rs.over = true
			stop = fmt.Sprintf("it spent %s on its models, and may spend %s", formatDollars(rs.micros), formatDollars(g.runLimit))
			for token, other := range b.grants {
				if other.root == g.root {
					other.expire.Stop()
					delete(b.grants, token)
					b.untrackSpend(other)
					audit("model.revoke", other.user, other.run, other.step, "the model proxy's token ended, as its run spent what it may")
				}
			}
		}
	}
	b.mu.Unlock()
	if stop != "" {
		audit("run.overspent", g.user, g.root, 0, "stopped: %s", stop)
		if b.overspent != nil {
			b.overspent(g.root, stop)
		}
	}
}

// What a run spent on its models, with its subflows' and items' runs when
// it is the run at their top, and the most the run their spending counts
// toward may, in dollars.
func runSpent(ctx context.Context, db *sql.DB, run int64) (float64, float64, error) {
	var micros int64
	err := db.QueryRowContext(ctx, "SELECT COALESCE(SUM(cost_micros), 0) FROM llm_usage WHERE root_run_id = ? OR (run_id = ? AND root_run_id <> ?)",
		run, run, run).Scan(&micros)
	if err != nil {
		return 0, 0, err
	}
	_, flowLimit, err := rootRun(ctx, db, run)
	if err != nil {
		return 0, 0, err
	}
	return dollars(micros), dollars(leastLimit(currentLimits().RunSpend, flowLimit)), nil
}

func derefUser(u *int64) int64 {
	if u == nil {
		return 0
	}
	return *u
}

// What a person spent in a month, and on how many calls, of which how
// many were to models without a known price.
type personSpending struct {
	User     *int64  `json:"user"`
	Name     string  `json:"name"`
	Spent    float64 `json:"spent"`
	Calls    int64   `json:"calls"`
	Unpriced int64   `json:"unpriced"`
}

// A run that spent the most in the month, by the run its spending counts
// toward.
type runSpending struct {
	Run   int64   `json:"run"`
	Flow  int64   `json:"flow"`
	Name  string  `json:"name"`
	Spent float64 `json:"spent"`
}

// How many of the month's costliest runs the settings list.
const costliestRuns = 20

func registerSpendingRoutes(mux *http.ServeMux, db *sql.DB) {
	// What was spent on models this month (UTC), or in month=YYYY-MM: by
	// everyone and on the costliest runs for an admin, else only by and
	// for whoever asks.
	mux.HandleFunc("GET /api/spending", func(w http.ResponseWriter, r *http.Request) {
		month := r.URL.Query().Get("month")
		if month == "" {
			month = spendMonth(time.Now())
		}
		from, err := time.Parse("2006-01", month)
		if err != nil {
			http.Error(w, "month isn't YYYY-MM", http.StatusBadRequest)
			return
		}
		to := from.AddDate(0, 1, 0)
		where, args := "u.at >= ? AND u.at < ?", []any{from, to}
		if me := requestUser(r); me != nil && !me.Admin {
			where, args = where+" AND u.user_id = ?", append(args, me.ID)
		}
		out := struct {
			Month  string           `json:"month"`
			People []personSpending `json:"people"`
			Runs   []runSpending    `json:"runs"`
		}{Month: month, People: []personSpending{}, Runs: []runSpending{}}
		err = scanAll(r.Context(), db, `SELECT u.user_id, COALESCE(MAX(p.name), ''), COALESCE(MAX(p.email), ''), SUM(u.cost_micros), COUNT(*), SUM(1 - u.priced)
			FROM llm_usage u LEFT JOIN users p ON p.id = u.user_id WHERE `+where+` GROUP BY u.user_id ORDER BY SUM(u.cost_micros) DESC`,
			func(rows *sql.Rows) error {
				var p personSpending
				var user sql.NullInt64
				var name, email string
				var micros int64
				if err := rows.Scan(&user, &name, &email, &micros, &p.Calls, &p.Unpriced); err != nil {
					return err
				}
				if user.Valid {
					p.User = &user.Int64
				}
				if p.Name = name; p.Name == "" {
					p.Name = email
				}
				p.Spent = dollars(micros)
				out.People = append(out.People, p)
				return nil
			}, args...)
		if err != nil {
			serverError(w, err)
			return
		}
		err = scanAll(r.Context(), db, `SELECT u.root_run_id, f.id, f.name, SUM(u.cost_micros) FROM llm_usage u
			JOIN runs rn ON rn.id = u.root_run_id JOIN flows f ON f.id = rn.flow_id
			WHERE `+where+` GROUP BY u.root_run_id, f.id, f.name ORDER BY SUM(u.cost_micros) DESC LIMIT ?`,
			func(rows *sql.Rows) error {
				var rs runSpending
				var micros int64
				if err := rows.Scan(&rs.Run, &rs.Flow, &rs.Name, &micros); err != nil {
					return err
				}
				rs.Spent = dollars(micros)
				out.Runs = append(out.Runs, rs)
				return nil
			}, append(args, costliestRuns)...)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
