package main

// Model providers an admin adds in the webapp's settings, each giving agents
// the models picked of it: one pi knows, such as Anthropic or OpenRouter,
// reached with its key, or a custom one, such as a company's own AI gateway,
// by its URL, the API it speaks and its models' IDs. The webapp sends them
// with each agent step without their keys: a key saved in the webapp goes
// with the model keys, by the variable its provider reads it from, and one
// kept in a vault is in the worker's environment under that variable.
//
// pi takes a provider's endpoint and models only from models.json, so the
// worker writes the providers into the step's agent directory's models.json, over
// the host's entries for the same providers: each reads its key from its
// variable, and a custom one has its URL, API and models, which get what
// pi's catalog knows of a model of the same ID (how much it takes in and
// gives out, whether it thinks, whether it sees images). Each agent step
// gets a models.json of its own, in its own agent directory.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
	c := &modelCatalog{byID: map[string]catalogModel{}}
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

// The host's models.json with the providers over its entries for the same
// ones; the host's own without any.
func providersModels(host []byte, providers []modelProvider, known map[string]catalogModel) ([]byte, error) {
	if len(providers) == 0 {
		return host, nil
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
	for _, p := range providers {
		entry := map[string]any{"apiKey": "${" + p.Env + "}"}
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
				}
				models = append(models, m)
			}
			entry["models"] = models
		}
		entries[p.ID] = entry
	}
	return json.MarshalIndent(config, "", "  ")
}

// Makes a step's agent directory, which pi keeps what it writes in, such
// as credentials it is given and the models it finds, so no step sees
// another's: the host's settings, and its models.json with the run's
// providers over its entries for the same ones.
func writeAgentDir(dir string, providers []modelProvider) error {
	var known map[string]catalogModel
	if len(providers) > 0 {
		// Without the catalog, custom providers' models take pi's defaults.
		if c, _ := loadCatalog(); c != nil {
			known = c.byID
		}
	}
	models, err := providersModels(agentConfig.models, providers, known)
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

// Whether the run's providers read a key from the variable.
func (run runRequest) providerReads(env string) bool {
	return slices.ContainsFunc(run.Providers, func(p modelProvider) bool { return p.Env == env })
}

// Which of the variables the worker's environment has.
func envSet(names []string) (map[string]bool, error) {
	set := map[string]bool{}
	for _, name := range names {
		if !envName.MatchString(name) {
			return nil, fmt.Errorf("invalid variable %q", name)
		}
		set[name] = os.Getenv(name) != ""
	}
	return set, nil
}

// What asks a custom provider which models it serves: its URL and API, and
// its key, or the variable in the worker's environment holding it.
type providerModelsRequest struct {
	BaseURL string `json:"baseUrl"`
	API     string `json:"api"`
	Env     string `json:"env"`
	Key     string `json:"key"`
}

// The models a custom provider says it serves, by their IDs, from its
// models endpoint: OpenAI's and Anthropic's APIs have one.
func providerModels(ctx context.Context, req providerModelsRequest) ([]string, error) {
	if err := checkProviderURL(req.BaseURL); err != nil {
		return nil, err
	}
	key := req.Key
	if key == "" && req.Env != "" {
		if !envName.MatchString(req.Env) {
			return nil, fmt.Errorf("invalid variable %q", req.Env)
		}
		key = os.Getenv(req.Env)
	}
	if strings.ContainsAny(key, "\x00\r\n") {
		return nil, errors.New("invalid key")
	}
	base := strings.TrimSuffix(req.BaseURL, "/")
	var target string
	header := http.Header{}
	switch req.API {
	case "openai-completions", "openai-responses":
		target = base + "/models"
		if key != "" {
			header.Set("Authorization", "Bearer "+key)
		}
	case "anthropic-messages":
		target = base + "/v1/models"
		header.Set("anthropic-version", "2023-06-01")
		if key != "" {
			header.Set("x-api-key", key)
		}
	default:
		return nil, errors.New("this API doesn't list its models; enter their IDs")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	r.Header = header
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		return nil, fmt.Errorf("ask the provider for its models: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("the provider answered %s: %s", res.Status, msg)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, errors.New("the provider's answer isn't a list of models")
	}
	ids := []string{}
	for _, m := range list.Data {
		if m.ID != "" && !slices.Contains(ids, m.ID) {
			ids = append(ids, m.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}
