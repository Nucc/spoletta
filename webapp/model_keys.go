package main

// Model keys are the API keys agents reach their models' providers with,
// such as Anthropic's or OpenAI's. Everyone signed in sets their own in the
// settings; an admin sets the instance's, which those without one of their
// own use; the worker's environment has the last say for a provider neither
// has a key for. A key goes to the worker for each agent step, as the
// environment variable pi reads it from, and isn't shown again once saved:
// the settings show its last four characters.
//
// An agent step uses the keys of whoever its run is for; see runUser. Without
// sign-in there is only the instance's keys.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// A provider pi takes a key for, and the environment variable it reads it from.
type modelProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Env  string `json:"env"`
}

var modelProviders = []modelProvider{
	{"anthropic", "Anthropic", "ANTHROPIC_API_KEY"},
	{"openai", "OpenAI", "OPENAI_API_KEY"},
	{"google", "Google Gemini", "GEMINI_API_KEY"},
	{"openrouter", "OpenRouter", "OPENROUTER_API_KEY"},
	{"mistral", "Mistral", "MISTRAL_API_KEY"},
	{"groq", "Groq", "GROQ_API_KEY"},
	{"xai", "xAI", "XAI_API_KEY"},
	{"cerebras", "Cerebras", "CEREBRAS_API_KEY"},
	{"zai", "Z.ai", "ZAI_API_KEY"},
}

func providerByID(id string) (modelProvider, bool) {
	for _, p := range modelProviders {
		if p.ID == id {
			return p, true
		}
	}
	return modelProvider{}, false
}

// The instance's keys are kept as the user 0's, whom nobody is.
const instanceKeys = 0

// The longest key taken.
const maxModelKey = 4096

func migrateModelKeys(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_keys (
		user_id BIGINT NOT NULL,
		provider VARCHAR(64) NOT NULL,
		api_key TEXT NOT NULL,
		updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
		PRIMARY KEY (user_id, provider)
	)`)
	return err
}

// A key as the settings show it.
type modelKey struct {
	Provider  string    `json:"provider"`
	Hint      string    `json:"hint"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func listModelKeys(ctx context.Context, db *sql.DB, owner int64) ([]modelKey, error) {
	keys := []modelKey{}
	err := scanAll(ctx, db, "SELECT provider, api_key, updated_at FROM model_keys WHERE user_id = ? ORDER BY provider", func(rows *sql.Rows) error {
		var k modelKey
		var key string
		if err := rows.Scan(&k.Provider, &key, &k.UpdatedAt); err != nil {
			return err
		}
		if len(key) > 4 {
			k.Hint = key[len(key)-4:]
		}
		keys = append(keys, k)
		return nil
	}, owner)
	return keys, err
}

// The keys agents of the user's runs reach providers with, by environment
// variable: the instance's, overridden by their own.
func modelKeys(ctx context.Context, db *sql.DB, user *int64) (map[string]string, error) {
	owners := []any{instanceKeys}
	if user != nil {
		owners = append(owners, *user)
	}
	keys := map[string]string{}
	err := scanAll(ctx, db, "SELECT provider, api_key FROM model_keys WHERE user_id IN (?"+strings.Repeat(", ?", len(owners)-1)+") ORDER BY user_id", func(rows *sql.Rows) error {
		var provider, key string
		if err := rows.Scan(&provider, &key); err != nil {
			return err
		}
		if p, ok := providerByID(provider); ok {
			keys[p.Env] = key
		}
		return nil
	}, owners...)
	if len(keys) == 0 {
		return nil, err
	}
	return keys, err
}

// The user a run's agents work for: whoever started it, or the run it is
// part of, else whoever made the schedule that did, else the owner of the
// flow it runs; nil when none of them is anyone.
func runUser(ctx context.Context, db *sql.DB, run int64) (*int64, error) {
	for {
		var parent, who, owner sql.NullInt64
		err := db.QueryRowContext(ctx,
			`SELECT r.parent_run_id, COALESCE(r.started_by, s.created_by), f.owner_user_id FROM runs r
			JOIN flows f ON f.id = r.flow_id LEFT JOIN schedules s ON s.id = r.schedule_id WHERE r.id = ?`, run).Scan(&parent, &who, &owner)
		switch {
		case err != nil:
			return nil, err
		case who.Valid:
			return &who.Int64, nil
		case parent.Valid:
			run = parent.Int64
		case owner.Valid:
			return &owner.Int64, nil
		default:
			return nil, nil
		}
	}
}

// Whose keys a request is about: the instance's for scope "instance", which
// only an admin, or anyone without sign-in, sees and changes; the user's
// own for "me". Writes the error and returns false if neither.
func keysOwner(w http.ResponseWriter, r *http.Request) (int64, bool) {
	u := requestUser(r)
	switch r.PathValue("scope") {
	case "instance":
		if u != nil && !u.Admin {
			http.Error(w, "only an admin can do that", http.StatusForbidden)
			return 0, false
		}
		return instanceKeys, true
	case "me":
		if u == nil {
			http.Error(w, "nobody signs in, so there are only the instance's keys", http.StatusBadRequest)
			return 0, false
		}
		return u.ID, true
	}
	http.Error(w, "scope must be instance or me", http.StatusNotFound)
	return 0, false
}

func registerModelKeyRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/model-providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, modelProviders)
	})

	mux.HandleFunc("GET /api/model-keys/{scope}", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := keysOwner(w, r)
		if !ok {
			return
		}
		keys, err := listModelKeys(r.Context(), db, owner)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})

	mux.HandleFunc("PUT /api/model-keys/{scope}/{provider}", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := keysOwner(w, r)
		if !ok {
			return
		}
		p, ok := providerByID(r.PathValue("provider"))
		if !ok {
			http.Error(w, "no such provider", http.StatusNotFound)
			return
		}
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		body.Key = strings.TrimSpace(body.Key)
		switch {
		case body.Key == "":
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		case len(body.Key) > maxModelKey || strings.ContainsAny(body.Key, "\x00\r\n"):
			http.Error(w, "that isn't a key", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(),
			"INSERT INTO model_keys (user_id, provider, api_key) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE api_key = VALUES(api_key)",
			owner, p.ID, body.Key); err != nil {
			serverError(w, err)
			return
		}
		keys, err := listModelKeys(r.Context(), db, owner)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})

	mux.HandleFunc("DELETE /api/model-keys/{scope}/{provider}", func(w http.ResponseWriter, r *http.Request) {
		owner, ok := keysOwner(w, r)
		if !ok {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM model_keys WHERE user_id = ? AND provider = ?", owner, r.PathValue("provider")); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
