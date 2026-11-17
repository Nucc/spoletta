package main

// Model keys are the API keys agents reach the providers of the worker's
// own pi config with. They are a project's: its owners set them in its
// settings, and its agents use them. pi reads each provider's key from an
// environment variable, which several providers may share, as a gateway's
// do; the worker says which variables the providers of the models it
// offers read (see the worker's host_providers.go). A key saved here, or
// else the webapp's environment's for the same variable, is what the model
// proxy reaches those providers with (see llm.go); it never goes to the
// worker, and isn't shown again: the settings show its last four
// characters. Keys are sealed with SECRET_KEY; see secrets.go.
//
// The worker's providers are the instance's configuration, so only a
// project inheriting it (Legacy; see projects.go) has them and their keys;
// another has none to set, and gives its agents models through the
// providers its owners add (model_providers.go).

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The longest key taken.
const maxModelKey = 4096

// A variable the worker's models read a key from: the providers reading it,
// and whether the webapp's environment has it. With no variable, the
// providers' keys can't be had.
type workerKey struct {
	Env       string   `json:"env"`
	Providers []string `json:"providers"`
	Set       bool     `json:"set"`
}

// A key as the settings show it: its variable, and whether one is saved
// here, by its last four characters.
type modelKey struct {
	workerKey
	Saved     bool       `json:"saved"`
	Hint      string     `json:"hint,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// What pi takes as a variable's name.
var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,254}$`)

// The variables the worker's models read keys from, by the worker's
// providers, with whether the webapp's environment has each.
func workerKeys(ctx context.Context) ([]workerKey, error) {
	host, err := llm.hostProviders(ctx)
	if err != nil {
		return nil, err
	}
	var keys []workerKey
	for _, p := range host {
		i := slices.IndexFunc(keys, func(k workerKey) bool { return k.Env == p.Env })
		if i < 0 {
			keys = append(keys, workerKey{Env: p.Env, Providers: []string{}, Set: p.Env != "" && os.Getenv(p.Env) != ""})
			i = len(keys) - 1
		}
		keys[i].Providers = append(keys[i].Providers, p.ID)
	}
	slices.SortFunc(keys, func(a, b workerKey) int { return strings.Compare(a.Env, b.Env) })
	return keys, nil
}

// The keys the worker's models read, for a project inheriting them, with
// those the project saved; a key saved for a variable no model reads any
// more shows too, to be removed. The worker's models of a provider added in
// the project's settings aren't offered, so their providers' keys don't
// show. Says why the worker couldn't say, if it couldn't.
func listModelKeys(ctx context.Context, db *sql.DB, project int64) ([]modelKey, string, error) {
	keys := []modelKey{}
	inherits, err := inheritsInstance(ctx, db, project)
	if err != nil {
		return nil, "", err
	}
	var reported []workerKey
	workerError := ""
	if inherits {
		if reported, err = workerKeys(ctx); err != nil {
			workerError = "the worker didn't say which keys its models read: " + err.Error()
		}
	}
	added, err := modelProviders(ctx, db, project)
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
	err = scanAll(ctx, db, "SELECT env, api_key, updated_at FROM model_keys WHERE project_id = ? ORDER BY env", func(rows *sql.Rows) error {
		var env, stored string
		var at time.Time
		if err := rows.Scan(&env, &stored, &at); err != nil {
			return err
		}
		key, err := secrets.open(modelKeyPlace(project, env), stored)
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
	}, project)
	return keys, workerError, err
}

// The keys the project saved, by variable; nil without any.
func modelKeys(ctx context.Context, db *sql.DB, project int64) (map[string]string, error) {
	keys := map[string]string{}
	err := scanAll(ctx, db, "SELECT env, api_key FROM model_keys WHERE project_id = ?", func(rows *sql.Rows) error {
		var env, stored string
		if err := rows.Scan(&env, &stored); err != nil {
			return err
		}
		key, err := secrets.open(modelKeyPlace(project, env), stored)
		keys[env] = key
		return err
	}, project)
	if len(keys) == 0 {
		return nil, err
	}
	return keys, err
}

// Only an owner of the project the request is worked on in, or anyone
// without sign-in, sees and changes its keys and settings.
func refuseKeys(w http.ResponseWriter, r *http.Request) bool {
	return requestUser(r) != nil && refuseNonAdmin(w, r)
}

func registerModelKeyRoutes(mux *http.ServeMux, db *sql.DB) {
	writeKeys := func(w http.ResponseWriter, r *http.Request) {
		keys, workerError, err := listModelKeys(r.Context(), db, requestProject(r))
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

	// Only a variable one of the worker's models reads takes a key, in a
	// project inheriting them.
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
		project := requestProject(r)
		inherits, err := inheritsInstance(r.Context(), db, project)
		if err != nil {
			serverError(w, err)
			return
		}
		if !inherits {
			http.Error(w, "this project has none of the worker's own models, whose keys these are; add a provider with its key instead", http.StatusBadRequest)
			return
		}
		reported, err := workerKeys(r.Context())
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
		stored, err := secrets.seal(modelKeyPlace(project, env), body.Key)
		if err != nil {
			serverError(w, err)
			return
		}
		if _, err := db.ExecContext(r.Context(),
			"INSERT INTO model_keys (project_id, env, api_key) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE api_key = VALUES(api_key)",
			project, env, stored); err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "model_key.set", "the key %s reads", env)
		writeKeys(w, r)
	})

	mux.HandleFunc("DELETE /api/model-keys/{env}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM model_keys WHERE project_id = ? AND env = ?", requestProject(r), r.PathValue("env")); err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "model_key.remove", "the key %s reads", r.PathValue("env"))
		w.WriteHeader(http.StatusNoContent)
	})
}
