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
