package main

// Model providers: where agents' models come from, as admins add them in the
// settings beside the worker's own pi config. A provider pi knows, such as
// Anthropic, OpenAI or OpenRouter, gives agents the models picked of it,
// reached with its key; a custom one, such as a company's own AI gateway,
// is reached at its URL, speaking the API it says (OpenAI's, Anthropic's or
// Amazon Bedrock's), and serves the models listed by their IDs.
//
// Each reads its key from an environment variable. The key is saved here,
// sealed with SECRET_KEY like the model keys and shown only by its last four
// characters, under a variable of the provider's own,
// MODEL_PROVIDER_<ID>_KEY, so it can't be taken for what the worker's pi
// config reads from the provider's usual variable, as a gateway's providers
// may. Or it is kept in a vault that puts it in the webapp's environment,
// under the provider's usual variable or one the admin names, never coming
// here: the settings then show only the variable to set and whether the
// webapp has it.
//
// Every agent step gets the providers, without their keys, which the
// worker writes into pi's models.json over the host's entries for the same
// providers (see the worker's providers.go), pointed at the model proxy,
// which reaches them with their keys (see llm.go). The models offered are
// the providers' picked ones and the worker's own of other providers, of
// which Claude Code takes only those of Anthropic's API and Claude's on Bedrock. The
// default model, which a process without a model of its own uses, is kept
// here once a provider is added: the first provider's first model, until
// an admin picks another.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Where a provider's key comes from: saved here, or the webapp's environment.
const (
	keySaved = "saved"
	keyEnv   = "env"
)

// The APIs a custom provider may speak, as pi names them.
var customAPIs = []string{"openai-completions", "openai-responses", "anthropic-messages", "bedrock-converse-stream"}

// A provider as the settings show it.
type modelProvider struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	API       string   `json:"api,omitempty"`
	BaseURL   string   `json:"baseUrl,omitempty"`
	KeySource string   `json:"keySource"`
	Env       string   `json:"env"`
	Models    []string `json:"models"`
	// The saved key's last four characters.
	Hint string `json:"hint,omitempty"`
	// With the key in the webapp's environment: whether it has it, if the
	// worker said.
	EnvSet    *bool     `json:"envSet,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`

	key string
}

func (p modelProvider) custom() bool { return p.BaseURL != "" }

// A provider as the worker gets it, without its key.
type runProvider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	API     string   `json:"api,omitempty"`
	BaseURL string   `json:"baseUrl,omitempty"`
	Env     string   `json:"env"`
	Models  []string `json:"models"`
}

// The providers added, the first added first, with their saved keys.
func modelProviders(ctx context.Context, db rowsQuerier) ([]modelProvider, error) {
	providers := []modelProvider{}
	err := scanAll(ctx, db, "SELECT id, name, api, base_url, key_source, env, api_key, models, updated_at FROM model_providers ORDER BY created_at, id", func(rows *sql.Rows) error {
		var p modelProvider
		var stored string
		var models []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.API, &p.BaseURL, &p.KeySource, &p.Env, &stored, &models, &p.UpdatedAt); err != nil {
			return err
		}
		if err := json.Unmarshal(models, &p.Models); err != nil {
			return err
		}
		if stored != "" {
			key, err := secrets.open(modelProviderPlace(p.ID), stored)
			if err != nil {
				return err
			}
			p.key = key
			if len(key) > 4 {
				p.Hint = key[len(key)-4:]
			}
		}
		providers = append(providers, p)
		return nil
	})
	return providers, err
}

// The default model kept here; "" leaves it to the worker's pi config.
func defaultModel(ctx context.Context, db rowsQuerier) (string, error) {
	var model string
	err := scanAll(ctx, db, "SELECT model FROM model_default WHERE id = 1", func(rows *sql.Rows) error {
		return rows.Scan(&model)
	})
	return model, err
}

// The worker's own pi config's providers, and whether it could say them.
type hostProviders struct {
	providers []hostProvider
	known     bool
}

// The worker's own providers as the model proxy knows them, asked of the
// worker if it hasn't lately: before a transaction, never inside one, so no
// lock is held while the worker is asked.
func savedHostProviders(ctx context.Context) hostProviders {
	if llm == nil {
		return hostProviders{known: true}
	}
	host, err := llm.hostProviders(ctx)
	return hostProviders{providers: host, known: err == nil}
}

// The default model for agent processes running with Claude Code, kept
// here beside the default model; "" takes the default model, when Claude
// Code can use it.
func claudeDefaultModel(ctx context.Context, db rowsQuerier) (string, error) {
	var model string
	err := scanAll(ctx, db, "SELECT claude_model FROM model_default WHERE id = 1", func(rows *sql.Rows) error {
		return rows.Scan(&model)
	})
	return model, err
}

// Keeps the default model one of the providers' after they changed, removed
// the provider removed: one of another kept, the worker's, stays; one of a
// provider gone or no longer offering it becomes the first provider's first
// model, as does none, once there is a provider. Claude Code's default goes
// the same way, but is never replaced: one gone, or of a provider Claude
// Code can no longer use, is cleared, so its processes take the default.
// Which providers those are takes the worker's own, read before the
// transaction (savedHostProviders); when the worker couldn't say, only one
// gone is cleared.
func fixDefaultModel(ctx context.Context, tx *sql.Tx, removed string, host hostProviders) error {
	providers, err := modelProviders(ctx, tx)
	if err != nil {
		return err
	}
	kept := func(model string) bool {
		provider, id, ok := strings.Cut(model, "/")
		if !ok {
			return false
		}
		i := slices.IndexFunc(providers, func(p modelProvider) bool { return p.ID == provider })
		return i >= 0 && slices.Contains(providers[i].Models, id) || i < 0 && provider != removed
	}
	claude, err := claudeDefaultModel(ctx, tx)
	if err != nil {
		return err
	}
	if claude != "" {
		if !kept(claude) || host.known && !claudeProvidersOf(providers, host.providers).takes(claude) {
			if _, err := tx.ExecContext(ctx, "UPDATE model_default SET claude_model = '' WHERE id = 1"); err != nil {
				return err
			}
		}
	}
	model, err := defaultModel(ctx, tx)
	if err != nil {
		return err
	}
	if kept(model) {
		return nil
	}
	model = ""
	if len(providers) > 0 && len(providers[0].Models) > 0 {
		model = providers[0].ID + "/" + providers[0].Models[0]
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO model_default (id, model) VALUES (1, ?) ON DUPLICATE KEY UPDATE model = VALUES(model)", model)
	return err
}

// The models offered for agent processes: the providers', and the worker's
// own of other providers, with the default and the providers' names, the
// coding agents the worker offers, and the models Claude Code may use. The
// worker's own are left out when it can't be asked, if there are providers.
func offeredModels(ctx context.Context, db *sql.DB, workerURL string) (agentModels, error) {
	var models agentModels
	providers, err := modelProviders(ctx, db)
	if err != nil {
		return models, err
	}
	workerErr := callWorker(ctx, http.MethodGet, workerURL+"/models", nil, &models)
	if workerErr != nil && len(providers) == 0 {
		return models, workerErr
	}
	if workerErr != nil {
		models = agentModels{}
	}
	def, err := defaultModel(ctx, db)
	if err != nil {
		return models, err
	}
	claudeDef, err := claudeDefaultModel(ctx, db)
	if err != nil {
		return models, err
	}
	var host []hostProvider
	if llm != nil {
		host, _ = llm.hostProviders(ctx)
	}
	models = offerModels(models, providers, host, def)
	models.ClaudeDefault = claudeDef
	return models, nil
}

// The models offered, from what the worker said of its own (nothing when
// it couldn't be asked), the providers added, the worker's pi config's
// providers, and the default the settings keep, if any.
func offerModels(models agentModels, providers []modelProvider, host []hostProvider, def string) agentModels {
	added := map[string]string{}
	for _, p := range providers {
		added[p.ID] = p.Name
	}
	offered := []string{}
	for _, m := range models.Models {
		if provider, _, _ := strings.Cut(m, "/"); added[provider] == "" {
			offered = append(offered, m)
		}
	}
	for _, p := range providers {
		for _, m := range p.Models {
			offered = append(offered, p.ID+"/"+m)
		}
	}
	models.Models = offered
	if len(providers) > 0 {
		models.Providers = added
	}
	if def != "" {
		models.Default = def
	}
	models.Harnesses = offeredHarnesses(models.Harnesses)
	claude := claudeProvidersOf(providers, host)
	models.Claude = nil
	for _, m := range offered {
		if claude.takes(m) {
			models.Claude = append(models.Claude, m)
		}
	}
	return models
}

// The coding agents the worker offers, as it says them; pi alone for a
// worker from before there was a choice.
func offeredHarnesses(said []string) []string {
	if len(said) == 0 {
		return []string{harnessPi}
	}
	return said
}

// Claude Code speaks Anthropic's Messages API, or Bedrock's, so the models
// it may use are those of the providers that do: Anthropic's own and those
// added in the settings or of the worker's pi config whose API is
// anthropic-messages, such as a company's gateway, any model of theirs, as
// a gateway's IDs are its own; and Amazon Bedrock, or a provider of its API
// such as a gateway in front of it, of whose models Claude Code runs only
// Claude's, whose IDs name anthropic (global.anthropic.claude-sonnet-5-5).
// Either is reached through the model proxy as any provider is.
const (
	anthropicMessages = "anthropic-messages"
	bedrockConverse   = "bedrock-converse-stream"
)

// What Claude Code speaks to a provider: Anthropic's API or Bedrock's.
const (
	claudeAnthropic = "anthropic"
	claudeBedrock   = "bedrock"
)

// The providers whose models Claude Code may use, by ID, with the API it
// speaks to each; "" for one it can't use.
type claudeProviders map[string]string

// The API Claude Code speaks to a provider of the ID, speaking the API
// models.json or the settings say, empty for one pi knows; "" for none.
func claudeAPIOf(id, api string) string {
	switch {
	case api == anthropicMessages, api == "" && id == "anthropic":
		return claudeAnthropic
	case api == bedrockConverse, api == "" && id == "amazon-bedrock":
		return claudeBedrock
	}
	return ""
}

// The providers of those added and the worker's own whose models Claude
// Code may use; one added goes over the worker's own of its ID, as the
// proxy routes them (llm.go).
func claudeProvidersOf(added []modelProvider, host []hostProvider) claudeProviders {
	c := claudeProviders{"anthropic": claudeAnthropic, "amazon-bedrock": claudeBedrock}
	for _, p := range host {
		c[p.ID] = claudeAPIOf(p.ID, p.API)
	}
	for _, p := range added {
		c[p.ID] = claudeAPIOf(p.ID, p.API)
	}
	return c
}

// Whether Claude Code may use the model, provider/id.
func (c claudeProviders) takes(model string) bool {
	provider, id, ok := strings.Cut(model, "/")
	if !ok || id == "" {
		return false
	}
	switch c[provider] {
	case claudeAnthropic:
		return true
	case claudeBedrock:
		return strings.Contains(id, "anthropic.")
	}
	return false
}

// The providers whose models Claude Code may use now: those added, and the
// worker's own as it said last.
func currentClaudeProviders(ctx context.Context, db rowsQuerier) (claudeProviders, error) {
	added, err := modelProviders(ctx, db)
	if err != nil {
		return nil, err
	}
	var host []hostProvider
	if llm != nil {
		host, _ = llm.hostProviders(ctx)
	}
	return claudeProvidersOf(added, host), nil
}

// Where a step's model came from: its process's own, the default for Claude
// Code, or the default model.
type modelSource int

const (
	fromProcess modelSource = iota
	fromClaudeDefault
	fromDefault
)

// Why a step running with Claude Code can't run on the model, or "".
func claudeModelProblem(c claudeProviders, model string, from modelSource) string {
	switch {
	case c.takes(model):
		return ""
	case from == fromProcess:
		return fmt.Sprintf("Claude Code can't use the model %s: it takes only models of a provider speaking Anthropic's API, or Claude's on Bedrock; pick another for the process", model)
	case from == fromClaudeDefault:
		return fmt.Sprintf("Claude Code's default model %s isn't one it can use any more; pick another in the settings, or one for the process", model)
	}
	return "the default model isn't one Claude Code can use; pick one for the process, or a default model for Claude Code in the settings"
}

// The default model of the worker's pi config, for a process that must
// name its model when the settings keep none; or why it can't be had.
func workerDefaultModel(ctx context.Context, workerURL string) (model, problem string) {
	var models agentModels
	if err := callWorker(ctx, http.MethodGet, workerURL+"/models", nil, &models); err != nil {
		if ctx.Err() != nil {
			return "", stopReason(ctx)
		}
		return "", "worker unavailable: " + err.Error()
	}
	return models.Default, ""
}

// The providers pi knows that can be added, with their models, as the
// worker's catalog has them.
type catalogProvider struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Env    string `json:"env"`
	Models []struct {
		ID string `json:"id"`
	} `json:"models"`
}

func workerCatalog(ctx context.Context, workerURL string) ([]catalogProvider, error) {
	var c struct {
		Providers []catalogProvider `json:"providers"`
	}
	err := callWorker(ctx, http.MethodGet, workerURL+"/catalog", nil, &c)
	return c.Providers, err
}

// A provider as the settings send it: a known one by its ID, a custom one
// by its name, URL and API. With the key saved here and one saved already,
// no key keeps it.
type providerInput struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	API       string   `json:"api"`
	BaseURL   string   `json:"baseUrl"`
	KeySource string   `json:"keySource"`
	Env       string   `json:"env"`
	Key       string   `json:"key"`
	Models    []string `json:"models"`
}

var (
	providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	thinkingSuffix    = regexp.MustCompile(`:(off|minimal|low|medium|high|xhigh|max)$`)
	notSlug           = regexp.MustCompile(`[^a-z0-9]+`)
)

// A custom provider's ID, from its name: "Company gateway" is
// company-gateway.
func providerSlug(name string) string {
	slug := strings.Trim(notSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(slug) > 48 {
		slug = strings.TrimRight(slug[:48], "-")
	}
	return slug
}

// The variable a custom provider reads its key from by default.
func providerEnv(id string) string {
	return strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_API_KEY"
}

// The variable a provider's saved key goes to the worker in.
func savedKeyEnv(id string) string {
	return "MODEL_PROVIDER_" + strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_KEY"
}

// Says what is wrong with a model's ID, if anything.
func checkModelID(id string) string {
	switch {
	case id == "" || len(id) > 255:
		return "a model's ID is required, of at most 255 characters"
	case strings.HasPrefix(id, "-") || strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return fmt.Sprintf("%q isn't a model's ID", id)
	case thinkingSuffix.MatchString(id):
		return fmt.Sprintf("%q ends as a thinking level does; the process sets that", id)
	}
	return ""
}

// Says what is wrong with a custom provider's URL, if anything.
func checkProviderURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return "the provider's URL is required"
	case len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n\t "):
		return "that isn't a URL"
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return "the URL must be an http or https URL, such as https://gateway.example.com/v1"
	case u.User != nil:
		return "the key goes in its own field, not the URL"
	}
	return ""
}

// Turns what the settings sent into the provider to keep, given the one kept
// already, if any, and the others; says what is wrong instead, if anything.
func (in providerInput) provider(catalog []catalogProvider, existing *modelProvider, others []modelProvider) (modelProvider, string) {
	p := modelProvider{ID: in.ID, KeySource: in.KeySource}
	known := slices.IndexFunc(catalog, func(c catalogProvider) bool { return c.ID == in.ID })
	switch {
	case existing != nil && !existing.custom() || existing == nil && in.BaseURL == "" && in.API == "":
		if known < 0 {
			return p, "pi doesn't know the provider " + in.ID
		}
		p.Name, p.Env = catalog[known].Name, catalog[known].Env
	default:
		p.Name, p.API, p.BaseURL = strings.TrimSpace(in.Name), in.API, strings.TrimSpace(in.BaseURL)
		if existing == nil {
			p.ID = providerSlug(p.Name)
		}
		known = slices.IndexFunc(catalog, func(c catalogProvider) bool { return c.ID == p.ID })
		switch {
		case p.Name == "" || len(p.Name) > 255:
			return p, "the provider's name is required, of at most 255 characters"
		case !providerIDPattern.MatchString(p.ID):
			return p, "the provider's name needs a letter or digit"
		case existing == nil && known >= 0:
			return p, p.ID + " is a provider pi knows; add it as that, or name this one otherwise"
		case !slices.Contains(customAPIs, p.API):
			return p, "pick the API the provider speaks"
		}
		if msg := checkProviderURL(p.BaseURL); msg != "" {
			return p, msg
		}
		p.Env = providerEnv(p.ID)
	}
	if existing == nil && slices.ContainsFunc(others, func(o modelProvider) bool { return o.ID == p.ID }) {
		return p, "there is a provider " + p.ID + " already"
	}
	switch in.KeySource {
	case keySaved:
		p.Env = savedKeyEnv(p.ID)
		p.key = strings.TrimSpace(in.Key)
		if p.key == "" && existing != nil && existing.KeySource == keySaved {
			p.key = existing.key
		}
		if p.key == "" {
			return p, "the key is required"
		}
		if len(p.key) > maxModelKey || strings.ContainsAny(p.key, "\x00\r\n") {
			return p, "that isn't a key"
		}
	case keyEnv:
		// A vault names its variables its own way.
		if env := strings.TrimSpace(in.Env); env != "" {
			p.Env = env
		}
		if !envNamePattern.MatchString(p.Env) {
			return p, "the variable's name takes capital letters, digits and _, such as ANTHROPIC_API_KEY"
		}
	default:
		return p, "say where the key comes from"
	}
	// A key saved for a variable goes over the environment's, so two
	// providers can't read one saved and another from the vault.
	for _, o := range others {
		if o.ID != p.ID && o.Env == p.Env && (o.KeySource == keySaved || p.KeySource == keySaved) {
			return p, fmt.Sprintf("%s reads its key from %s already", o.Name, p.Env)
		}
	}
	p.Models = []string{}
	for _, m := range in.Models {
		m = strings.TrimSpace(m)
		if m == "" || slices.Contains(p.Models, m) {
			continue
		}
		if msg := checkModelID(m); msg != "" {
			return p, msg
		}
		p.Models = append(p.Models, m)
	}
	if len(p.Models) == 0 {
		return p, "pick at least one model"
	}
	return p, ""
}

func registerModelProviderRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	writeProviders := func(w http.ResponseWriter, r *http.Request) {
		providers, err := modelProviders(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		def, err := defaultModel(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		for i, p := range providers {
			if p.KeySource == keyEnv {
				set := os.Getenv(p.Env) != ""
				providers[i].EnvSet = &set
			}
		}
		claudeDef, err := claudeDefaultModel(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		workerError := ""
		writeJSON(w, http.StatusOK, map[string]any{"providers": providers, "default": def, "claudeDefault": claudeDef, "workerError": workerError})
	}

	mux.HandleFunc("GET /api/model-providers", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		writeProviders(w, r)
	})

	mux.HandleFunc("GET /api/model-providers/catalog", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var c json.RawMessage
		if err := callWorker(r.Context(), http.MethodGet, workerURL+"/catalog", nil, &c); err != nil {
			http.Error(w, "the worker didn't say which providers pi knows: "+workerMessage(err), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})

	// Adds a provider, or changes the one at {id}.
	save := func(w http.ResponseWriter, r *http.Request, id string) {
		if refuseKeys(w, r) {
			return
		}
		var in providerInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if id != "" {
			in.ID = id
		}
		catalog, err := workerCatalog(r.Context(), workerURL)
		if err != nil {
			http.Error(w, "the worker didn't say which providers pi knows: "+workerMessage(err), http.StatusBadGateway)
			return
		}
		host := savedHostProviders(r.Context())
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		// Saves are one at a time, so two can't take one ID or variable.
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO model_default (id, model) VALUES (1, '') ON DUPLICATE KEY UPDATE id = id"); err != nil {
			serverError(w, err)
			return
		}
		var locked string
		if err := tx.QueryRowContext(r.Context(), "SELECT model FROM model_default WHERE id = 1 FOR UPDATE").Scan(&locked); err != nil {
			serverError(w, err)
			return
		}
		others, err := modelProviders(r.Context(), tx)
		if err != nil {
			serverError(w, err)
			return
		}
		var existing *modelProvider
		if id != "" {
			i := slices.IndexFunc(others, func(p modelProvider) bool { return p.ID == id })
			if i < 0 {
				http.Error(w, "no such provider", http.StatusNotFound)
				return
			}
			existing = &others[i]
		}
		p, msg := in.provider(catalog, existing, others)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		stored := ""
		if p.KeySource == keySaved {
			if stored, err = secrets.seal(modelProviderPlace(p.ID), p.key); err != nil {
				serverError(w, err)
				return
			}
		}
		models, _ := json.Marshal(p.Models)
		if existing == nil {
			_, err = tx.ExecContext(r.Context(),
				"INSERT INTO model_providers (id, name, api, base_url, key_source, env, api_key, models) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
				p.ID, p.Name, p.API, p.BaseURL, p.KeySource, p.Env, stored, models)
		} else {
			_, err = tx.ExecContext(r.Context(),
				"UPDATE model_providers SET name = ?, api = ?, base_url = ?, key_source = ?, env = ?, api_key = ?, models = ? WHERE id = ?",
				p.Name, p.API, p.BaseURL, p.KeySource, p.Env, stored, models, p.ID)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if err := fixDefaultModel(r.Context(), tx, "", host); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeProviders(w, r)
	}
	mux.HandleFunc("POST /api/model-providers", func(w http.ResponseWriter, r *http.Request) { save(w, r, "") })
	mux.HandleFunc("PUT /api/model-providers/{id}", func(w http.ResponseWriter, r *http.Request) { save(w, r, r.PathValue("id")) })

	mux.HandleFunc("DELETE /api/model-providers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		id := r.PathValue("id")
		host := savedHostProviders(r.Context())
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM model_providers WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		if err := fixDefaultModel(r.Context(), tx, id, host); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeProviders(w, r)
	})

	// Picks the default model among those offered.
	mux.HandleFunc("PUT /api/model-default", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		offered, err := offeredModels(r.Context(), db, workerURL)
		if err != nil {
			http.Error(w, "the worker didn't say which models it offers: "+workerMessage(err), http.StatusBadGateway)
			return
		}
		if !slices.Contains(offered.Models, body.Model) {
			http.Error(w, "no model offered is "+body.Model, http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "INSERT INTO model_default (id, model) VALUES (1, ?) ON DUPLICATE KEY UPDATE model = VALUES(model)", body.Model); err != nil {
			serverError(w, err)
			return
		}
		writeProviders(w, r)
	})

	// Picks the default model for Claude Code among those it may use, or
	// none, so its processes take the default model.
	mux.HandleFunc("PUT /api/model-default/claude", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Model != "" {
			offered, err := offeredModels(r.Context(), db, workerURL)
			if err != nil {
				http.Error(w, "the worker didn't say which models it offers: "+workerMessage(err), http.StatusBadGateway)
				return
			}
			switch {
			case !slices.Contains(offered.Models, body.Model):
				http.Error(w, "no model offered is "+body.Model, http.StatusBadRequest)
				return
			case !slices.Contains(offered.Claude, body.Model):
				http.Error(w, "Claude Code can't use the model "+body.Model+": it takes only models of a provider speaking Anthropic's API, or Claude's on Bedrock", http.StatusBadRequest)
				return
			}
		}
		if _, err := db.ExecContext(r.Context(), "INSERT INTO model_default (id, model, claude_model) VALUES (1, '', ?) ON DUPLICATE KEY UPDATE claude_model = VALUES(claude_model)", body.Model); err != nil {
			serverError(w, err)
			return
		}
		writeProviders(w, r)
	})

	// The models a custom provider says it serves, asked with the key typed,
	// the one saved for it, or the one in the webapp's environment.
	mux.HandleFunc("POST /api/model-providers/models", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var in providerInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		in.BaseURL = strings.TrimSpace(in.BaseURL)
		if msg := checkProviderURL(in.BaseURL); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		key := strings.TrimSpace(in.Key)
		switch {
		case in.KeySource == keyEnv:
			env := strings.TrimSpace(in.Env)
			if !envNamePattern.MatchString(env) {
				http.Error(w, "that isn't a variable's name", http.StatusBadRequest)
				return
			}
			key = os.Getenv(env)
		case key == "" && in.ID != "":
			providers, err := modelProviders(r.Context(), db)
			if err != nil {
				serverError(w, err)
				return
			}
			if i := slices.IndexFunc(providers, func(p modelProvider) bool { return p.ID == in.ID }); i >= 0 {
				key = providers[i].key
			}
		}
		models, err := providerModels(r.Context(), in.BaseURL, in.API, key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]string{"models": models})
	})
}

// The models a custom provider says it serves, by their IDs, from its
// models endpoint, asked with its key: OpenAI's and Anthropic's APIs have
// one.
func providerModels(ctx context.Context, baseURL, api, key string) ([]string, error) {
	if strings.ContainsAny(key, "\x00\r\n") {
		return nil, errors.New("invalid key")
	}
	base := strings.TrimSuffix(baseURL, "/")
	var target string
	header := http.Header{}
	switch api {
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

// What the worker said went wrong: its error, without how it said it.
func workerMessage(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, "{"); i >= 0 {
		var body struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(msg[i:]), &body) == nil && body.Error != "" {
			return body.Error
		}
	}
	return msg
}
