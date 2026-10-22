package main

// Model providers an admin adds in the webapp's settings, each giving agents
// the models picked of it: one pi knows, such as Anthropic or OpenRouter,
// or a custom one, such as a company's own AI gateway, by its URL, the API
// it speaks and its models' IDs. The webapp sends them with each agent step,
// never with their keys.
//
// pi takes a provider's endpoint and models only from models.json, so the
// worker writes the providers into the step's agent directory's models.json,
// over the host's entries for the same providers; a custom one has its URL,
// API and models, which get what pi's catalog knows of a model of the same
// ID (how much it takes in and gives out, whether it thinks, whether it
// sees images). Every provider the webapp reaches for the step, these and
// the host's (see host_providers.go), points at the webapp's proxy, with
// the step's token as its key; the proxy swaps it for the real one. pi
// takes a provider-level baseUrl over its own for one it knows, and reads a
// key that is neither a $variable nor a !command as it is, which the
// token, made of letters, digits, - and _, never is. A key models.json
// holds is dropped: no key is the worker's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// A provider as the webapp sends it. A custom one has a URL and an API.
type modelProvider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	API     string   `json:"api,omitempty"`
	BaseURL string   `json:"baseUrl,omitempty"`
	Env     string   `json:"env"`
	Models  []string `json:"models"`
}

func (p modelProvider) custom() bool { return p.BaseURL != "" }

// The providers pi knows that can be added, by the names people know them
// by, and the variable each reads its key from: amazon-bedrock takes a
// Bedrock API key, sent as a bearer token.
var knownProviders = []struct{ ID, Name string }{
	{"anthropic", "Anthropic"},
	{"openai", "OpenAI"},
	{"openrouter", "OpenRouter"},
	{"google", "Google Gemini"},
	{"amazon-bedrock", "Amazon Bedrock"},
	{"mistral", "Mistral"},
	{"groq", "Groq"},
	{"xai", "xAI"},
	{"cerebras", "Cerebras"},
	{"fireworks", "Fireworks"},
	{"zai", "Z.ai"},
}

func knownProviderEnv(id string) string {
	if id == "amazon-bedrock" {
		return "AWS_BEARER_TOKEN_BEDROCK"
	}
	return builtinKeys[id]
}

// The APIs a custom provider may speak, as pi names them.
var customAPIs = []string{"openai-completions", "openai-responses", "anthropic-messages", "bedrock-converse-stream"}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Checks a provider a run came with.
func (p modelProvider) check() error {
	if !providerIDPattern.MatchString(p.ID) {
		return fmt.Errorf("invalid model provider %q", p.ID)
	}
	if !envName.MatchString(p.Env) {
		return fmt.Errorf("invalid key variable for %s", p.ID)
	}
	if p.custom() {
		if err := checkProviderURL(p.BaseURL); err != nil {
			return fmt.Errorf("%s: %w", p.ID, err)
		}
		if !slices.Contains(customAPIs, p.API) {
			return fmt.Errorf("%s: unknown API %q", p.ID, p.API)
		}
	} else if knownProviderEnv(p.ID) == "" {
		return fmt.Errorf("%s isn't a provider pi knows; a custom one needs its URL", p.ID)
	}
	for _, m := range p.Models {
		if m == "" || len(m) > 255 || strings.HasPrefix(m, "-") || strings.ContainsFunc(m, func(r rune) bool { return r <= ' ' || r == 0x7f }) || thinkingSuffix.MatchString(m) {
			return fmt.Errorf("%s: invalid model %q", p.ID, m)
		}
	}
	return nil
}

func checkProviderURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the URL isn't an http or https URL")
	}
	return nil
}

// A model of pi's catalog, with what custom providers' models of its ID take.
type catalogModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	ContextWindow int64    `json:"contextWindow"`
	MaxTokens     int64    `json:"maxTokens"`
	// What it costs, in dollars a million tokens, as pi's catalog says.
	Cost *modelCost `json:"cost,omitempty"`
}

// A model's price: dollars a million tokens taken in, given out, read from
// the provider's cache and written to it.
type modelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// A provider that can be added, with its models.
type catalogProvider struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Env    string         `json:"env"`
	Models []catalogModel `json:"models"`
}

type modelCatalog struct {
	Providers []catalogProvider `json:"providers"`
	// The price of every model of every provider pi knows, by provider and
	// model, which the webapp counts what agents spend by (see its
	// spend.go), the providers that can't be added too: a gateway's models
	// are often theirs.
	Prices map[string]map[string]modelCost `json:"prices"`
	// The models of every provider pi knows, by ID; the first of an ID,
	// by the providers' names, when several have it.
	byID map[string]catalogModel
}

// pi's catalog, read once when first asked for.
var catalog struct {
	once sync.Once
	c    *modelCatalog
	err  error
}

func loadCatalog() (*modelCatalog, error) {
	catalog.once.Do(func() {
		dir, err := catalogDir()
		if err == nil {
			catalog.c, err = readCatalog(dir)
		}
		if err != nil {
			catalog.err = fmt.Errorf("pi's model catalog: %w", err)
			log.Printf("worker: %v", catalog.err)
		}
	})
	return catalog.c, catalog.err
}

// Where pi keeps the catalog it comes with: PI_MODEL_CATALOG, or in the pi
// installed globally, where npm puts the package of the catalog either
// beside pi's or inside it.
func catalogDir() (string, error) {
	if dir := os.Getenv("PI_MODEL_CATALOG"); dir != "" {
		return dir, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "npm", "root", "-g").Output()
	if err != nil {
		return "", fmt.Errorf("find the global packages: %w", err)
	}
	root := strings.TrimSpace(string(out))
	const data = "@earendil-works/pi-ai/dist/providers/data"
	for _, dir := range []string{
		filepath.Join(root, "@earendil-works/pi-coding-agent/node_modules", data),
		filepath.Join(root, data),
	} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
	}
	return "", errors.New("not found in " + root)
}

// Reads the catalog: a file per provider, holding its models by API and
// then by kind and ID, such as "chat:claude-sonnet-4-6".
func readCatalog(dir string) (*modelCatalog, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	byProvider := map[string][]catalogModel{}
	c := &modelCatalog{byID: map[string]catalogModel{}, Prices: map[string]map[string]modelCost{}}
	for _, f := range files {
		// Beside the providers' files is a manifest of them.
		if strings.HasPrefix(filepath.Base(f), ".") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var apis map[string]map[string]json.RawMessage
		if err := json.Unmarshal(raw, &apis); err != nil {
			log.Printf("worker: pi's model catalog: %s isn't a provider's models: %v", filepath.Base(f), err)
			continue
		}
		provider := strings.TrimSuffix(filepath.Base(f), ".json")
		for _, models := range apis {
			for key, raw := range models {
				if !strings.HasPrefix(key, "chat:") {
					continue
				}
				var m catalogModel
				if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" {
					continue
				}
				byProvider[provider] = append(byProvider[provider], m)
				if m.Cost != nil {
					if c.Prices[provider] == nil {
						c.Prices[provider] = map[string]modelCost{}
					}
					c.Prices[provider][m.ID] = *m.Cost
				}
				if _, ok := c.byID[m.ID]; !ok {
					c.byID[m.ID] = m
				}
			}
		}
	}
	for _, p := range knownProviders {
		models := byProvider[p.ID]
		slices.SortFunc(models, func(a, b catalogModel) int { return strings.Compare(a.ID, b.ID) })
		c.Providers = append(c.Providers, catalogProvider{ID: p.ID, Name: p.Name, Env: knownProviderEnv(p.ID), Models: models})
	}
	return c, nil
}

// Where a step reaches its providers: the webapp's proxy, URL/<provider>
// for each provider it reaches, with the step's token.
type llmAccess struct {
	URL       string   `json:"url"`
	Token     string   `json:"token"`
	Providers []string `json:"providers"`
}

// What a token can be, so pi takes it as it is.
var llmToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,}$`)

func (a *llmAccess) check() error {
	if a == nil {
		return nil
	}
	if !llmToken.MatchString(a.Token) {
		return errors.New("invalid model proxy token")
	}
	if err := checkProviderURL(a.URL); err != nil {
		return fmt.Errorf("model proxy: %w", err)
	}
	for _, id := range a.Providers {
		if !providerIDPattern.MatchString(id) {
			return fmt.Errorf("invalid model provider %q", id)
		}
	}
	return nil
}

// The host's models.json with the providers over its entries for the same
// ones, and every provider the step reaches pointed at the proxy. Keys the
// host's holds go.
func providersModels(host []byte, providers []modelProvider, known map[string]catalogModel, llm *llmAccess) ([]byte, error) {
	if len(providers) == 0 && llm == nil && host == nil {
		return nil, nil
	}
	config := map[string]any{}
	if host != nil {
		if err := json.Unmarshal(host, &config); err != nil {
			return nil, fmt.Errorf("models.json: %w", err)
		}
	}
	entries, _ := config["providers"].(map[string]any)
	if entries == nil {
		entries = map[string]any{}
		config["providers"] = entries
	}
	for _, e := range entries {
		if entry, ok := e.(map[string]any); ok {
			delete(entry, "apiKey")
		}
	}
	for _, p := range providers {
		entry := map[string]any{}
		if p.custom() {
			entry["name"], entry["baseUrl"], entry["api"] = p.Name, p.BaseURL, p.API
			models := []any{}
			for _, id := range p.Models {
				m := map[string]any{"id": id}
				// Only what holds whatever API serves it: an API's own
				// settings would break another's.
				if k, ok := known[id]; ok {
					m["name"], m["reasoning"] = k.Name, k.Reasoning
					if len(k.Input) > 0 {
						m["input"] = k.Input
					}
					if k.ContextWindow > 0 {
						m["contextWindow"] = k.ContextWindow
					}
					if k.MaxTokens > 0 {
						m["maxTokens"] = k.MaxTokens
					}
					if k.Cost != nil {
						m["cost"] = k.Cost
					}
				}
				models = append(models, m)
			}
			entry["models"] = models
		}
		entries[p.ID] = entry
	}
	if llm != nil {
		for _, id := range llm.Providers {
			entry, _ := entries[id].(map[string]any)
			if entry == nil {
				entry = map[string]any{}
				entries[id] = entry
			}
			entry["baseUrl"] = strings.TrimSuffix(llm.URL, "/") + "/" + id
			entry["apiKey"] = llm.Token
		}
	}
	return json.MarshalIndent(config, "", "  ")
}

// Makes a step's agent directory, which pi keeps what it writes in, such
// as credentials it is given and the models it finds, so no step sees
// another's: the host's settings, and its models.json with the run's
// providers over its entries for the same ones.
func writeAgentDir(dir string, providers []modelProvider, llm *llmAccess) error {
	if err := llm.check(); err != nil {
		return err
	}
	var known map[string]catalogModel
	if len(providers) > 0 {
		// Without the catalog, custom providers' models take pi's defaults.
		if c, _ := loadCatalog(); c != nil {
			known = c.byID
		}
	}
	models, err := providersModels(agentConfig.models, providers, known, llm)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create the agent directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), agentConfig.settings, 0o600); err != nil {
		return fmt.Errorf("write settings.json: %w", err)
	}
	if models != nil {
		if err := os.WriteFile(filepath.Join(dir, "models.json"), models, 0o600); err != nil {
			return fmt.Errorf("write models.json: %w", err)
		}
	}
	return nil
}
