package main

// Model keys are the API keys agents reach their models' providers with.
// They are the instance's: an admin sets them in the settings, as they
// choose the models agents may use, and everyone's agents use them. pi reads
// each provider's key from an environment variable, which several providers
// may share, as a gateway's do; the worker says which variables the models
// it offers read, and which it has in its own environment (see the worker's
// keys.go). A key saved here goes to the worker for each agent step, over
// the worker's own, and isn't shown again: the settings show its last four
// characters. Keys are sealed with SECRET_KEY; see secrets.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The longest key taken.
const maxModelKey = 4096

func migrateModelKeys(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_keys (
		env VARCHAR(255) NOT NULL PRIMARY KEY,
		api_key TEXT NOT NULL,
		updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
	)`); err != nil {
		return err
	}
	// People once kept keys of their own beside the instance's, which were
	// the user 0's; only the instance's stay.
	personal, err := hasColumn(db, "model_keys", "user_id")
	if err != nil {
		return err
	}
	if personal {
		for _, q := range []string{
			"DELETE FROM model_keys WHERE user_id <> 0",
			"ALTER TABLE model_keys DROP PRIMARY KEY, DROP COLUMN user_id, ADD PRIMARY KEY (provider)",
		} {
			if _, err := db.Exec(q); err != nil {
				return err
			}
		}
	}
	// Keys were once kept by provider, from a list of the ones pi knows;
	// they are kept by the variable it reads them from now.
	byProvider, err := hasColumn(db, "model_keys", "provider")
	if err != nil || !byProvider {
		return err
	}
	for provider, env := range map[string]string{
		"anthropic": "ANTHROPIC_API_KEY", "openai": "OPENAI_API_KEY", "google": "GEMINI_API_KEY",
		"openrouter": "OPENROUTER_API_KEY", "mistral": "MISTRAL_API_KEY", "groq": "GROQ_API_KEY",
		"xai": "XAI_API_KEY", "cerebras": "CEREBRAS_API_KEY", "zai": "ZAI_API_KEY",
	} {
		if _, err := db.Exec("UPDATE model_keys SET provider = ? WHERE provider = ?", env, provider); err != nil {
			return err
		}
	}
	_, err = db.Exec("ALTER TABLE model_keys CHANGE provider env VARCHAR(255) NOT NULL")
	return err
}

// A variable the worker's models read a key from, as the worker says: the
// providers reading it, and whether the worker's environment has it. With
// no variable, the providers' keys are set up in the worker otherwise.
type workerKey struct {
	Env       string   `json:"env"`
	Providers []string `json:"providers"`
	Set       bool     `json:"set"`
}

// A key as the settings show it: what the worker says of its variable, and
// whether one is saved here, by its last four characters.
type modelKey struct {
	workerKey
	Saved     bool       `json:"saved"`
	Hint      string     `json:"hint,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// What pi takes as a variable's name.
var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,254}$`)

// The variables the worker's models read keys from.
func workerKeys(ctx context.Context, workerURL string) ([]workerKey, error) {
	var keys []workerKey
	err := callWorker(ctx, http.MethodGet, workerURL+"/keys", nil, &keys)
	return keys, err
}

// The keys the worker's models read, with those saved here; a key saved for
// a variable no model reads any more shows too, to be removed. The worker's
// models of a provider added in the settings aren't offered, so their
// providers' keys don't show. Says why the worker couldn't say, if it
// couldn't.
func listModelKeys(ctx context.Context, db *sql.DB, workerURL string) ([]modelKey, string, error) {
	keys := []modelKey{}
	reported, err := workerKeys(ctx, workerURL)
	workerError := ""
	if err != nil {
		workerError = "the worker didn't say which keys its models read: " + err.Error()
	}
	added, err := modelProviders(ctx, db)
	if err != nil {
		return nil, "", err
	}
	for _, k := range reported {
		k.Providers = slices.DeleteFunc(k.Providers, func(p string) bool {
			return slices.ContainsFunc(added, func(a modelProvider) bool { return a.ID == p })
		})
		if len(k.Providers) > 0 {
			keys = append(keys, modelKey{workerKey: k})
		}
	}
	err = scanAll(ctx, db, "SELECT env, api_key, updated_at FROM model_keys ORDER BY env", func(rows *sql.Rows) error {
		var env, stored string
		var at time.Time
		if err := rows.Scan(&env, &stored, &at); err != nil {
			return err
		}
		key, err := secrets.open(modelKeyPlace(env), stored)
		if err != nil {
			return err
		}
		i := 0
		for i < len(keys) && keys[i].Env != env {
			i++
		}
		if i == len(keys) {
			keys = append(keys, modelKey{workerKey: workerKey{Env: env, Providers: []string{}}})
		}
		keys[i].Saved, keys[i].UpdatedAt = true, &at
		if len(key) > 4 {
			keys[i].Hint = key[len(key)-4:]
		}
		return nil
	})
	return keys, workerError, err
}

// The keys saved here, by variable; nil without any.
func modelKeys(ctx context.Context, db *sql.DB) (map[string]string, error) {
	keys := map[string]string{}
	err := scanAll(ctx, db, "SELECT env, api_key FROM model_keys", func(rows *sql.Rows) error {
		var env, stored string
		if err := rows.Scan(&env, &stored); err != nil {
			return err
		}
		key, err := secrets.open(modelKeyPlace(env), stored)
		keys[env] = key
		return err
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

func registerModelKeyRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	writeKeys := func(w http.ResponseWriter, r *http.Request) {
		keys, workerError, err := listModelKeys(r.Context(), db, workerURL)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "workerError": workerError})
	}

	mux.HandleFunc("GET /api/model-keys", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		writeKeys(w, r)
	})

	// Only a variable one of the worker's models reads takes a key.
	mux.HandleFunc("PUT /api/model-keys/{env}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		env := r.PathValue("env")
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		body.Key = strings.TrimSpace(body.Key)
		switch {
		case !envNamePattern.MatchString(env):
			http.Error(w, "that isn't a variable's name", http.StatusBadRequest)
			return
		case body.Key == "":
			http.Error(w, "key is required", http.StatusBadRequest)
			return
		case len(body.Key) > maxModelKey || strings.ContainsAny(body.Key, "\x00\r\n"):
			http.Error(w, "that isn't a key", http.StatusBadRequest)
			return
		}
		reported, err := workerKeys(r.Context(), workerURL)
		if err != nil {
			http.Error(w, "the worker didn't say which keys its models read: "+err.Error(), http.StatusBadGateway)
			return
		}
		read := false
		for _, k := range reported {
			read = read || k.Env == env
		}
		if !read {
			http.Error(w, "no model the worker offers reads "+env, http.StatusBadRequest)
			return
		}
		stored, err := secrets.seal(modelKeyPlace(env), body.Key)
		if err != nil {
			serverError(w, err)
			return
		}
		if _, err := db.ExecContext(r.Context(),
			"INSERT INTO model_keys (env, api_key) VALUES (?, ?) ON DUPLICATE KEY UPDATE api_key = VALUES(api_key)",
			env, stored); err != nil {
			serverError(w, err)
			return
		}
		writeKeys(w, r)
	})

	mux.HandleFunc("DELETE /api/model-keys/{env}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM model_keys WHERE env = ?", r.PathValue("env")); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
