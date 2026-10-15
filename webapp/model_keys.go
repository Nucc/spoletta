package main

// Model keys are the API keys agents reach their models' providers with,
// such as Anthropic's or OpenAI's. They are the instance's: an admin sets
// them in the settings, as they choose the models agents may use, and
// everyone's agents use them; the worker's environment has the last say for
// a provider without one. A key goes to the worker for each agent step, as
// the environment variable pi reads it from, and isn't shown again once
// saved: the settings show its last four characters.

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

// The longest key taken.
const maxModelKey = 4096

func migrateModelKeys(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_keys (
		provider VARCHAR(64) NOT NULL PRIMARY KEY,
		api_key TEXT NOT NULL,
		updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
	)`); err != nil {
		return err
	}
	// People once kept keys of their own beside the instance's, which were
	// the user 0's; only the instance's stay.
	personal, err := hasColumn(db, "model_keys", "user_id")
	if err != nil || !personal {
		return err
	}
	for _, q := range []string{
		"DELETE FROM model_keys WHERE user_id <> 0",
		"ALTER TABLE model_keys DROP PRIMARY KEY, DROP COLUMN user_id, ADD PRIMARY KEY (provider)",
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// A key as the settings show it.
type modelKey struct {
	Provider  string    `json:"provider"`
	Hint      string    `json:"hint"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func listModelKeys(ctx context.Context, db *sql.DB) ([]modelKey, error) {
	keys := []modelKey{}
	err := scanAll(ctx, db, "SELECT provider, api_key, updated_at FROM model_keys ORDER BY provider", func(rows *sql.Rows) error {
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
	})
	return keys, err
}

// The keys agents reach providers with, by environment variable; nil
// without any.
func modelKeys(ctx context.Context, db *sql.DB) (map[string]string, error) {
	keys := map[string]string{}
	err := scanAll(ctx, db, "SELECT provider, api_key FROM model_keys", func(rows *sql.Rows) error {
		var provider, key string
		if err := rows.Scan(&provider, &key); err != nil {
			return err
		}
		if p, ok := providerByID(provider); ok {
			keys[p.Env] = key
		}
		return nil
	})
	if len(keys) == 0 {
		return nil, err
	}
	return keys, err
}

// Only an admin, or anyone without sign-in, sees and changes the keys.
func refuseKeys(w http.ResponseWriter, r *http.Request) bool {
	return requestUser(r) != nil && refuseNonAdmin(w, r)
}

func registerModelKeyRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/model-providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, modelProviders)
	})

	mux.HandleFunc("GET /api/model-keys", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		keys, err := listModelKeys(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})

	mux.HandleFunc("PUT /api/model-keys/{provider}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
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
			"INSERT INTO model_keys (provider, api_key) VALUES (?, ?) ON DUPLICATE KEY UPDATE api_key = VALUES(api_key)",
			p.ID, body.Key); err != nil {
			serverError(w, err)
			return
		}
		keys, err := listModelKeys(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})

	mux.HandleFunc("DELETE /api/model-keys/{provider}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM model_keys WHERE provider = ?", r.PathValue("provider")); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
