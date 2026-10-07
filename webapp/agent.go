package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Agent settings configure the pi harness that runs agent processes. They
// live in a one-row table. The API key is write-only: responses only say
// whether one is stored and how it ends.
type agentSettings struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Thinking  string `json:"thinking"`
	HasAPIKey bool   `json:"hasApiKey"`
	KeyHint   string `json:"keyHint"`
}

// Keep in sync with API_KEY_ENV in worker/server.mjs.
var agentProviders = []string{"anthropic", "openai", "google", "openrouter", "mistral", "groq", "xai", "deepseek"}

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

type runResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

func migrateAgent(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_settings (
			id TINYINT PRIMARY KEY,
			provider VARCHAR(64) NOT NULL DEFAULT 'anthropic',
			model VARCHAR(255) NOT NULL DEFAULT '',
			thinking VARCHAR(16) NOT NULL DEFAULT 'medium',
			api_key TEXT NOT NULL
		)`,
		"INSERT IGNORE INTO agent_settings (id, api_key) VALUES (1, '')",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func keyHint(key string) string {
	if len(key) < 8 {
		return ""
	}
	return key[len(key)-4:]
}

func registerAgentRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	getSettings := func(w http.ResponseWriter, r *http.Request) {
		var s agentSettings
		var key string
		err := db.QueryRowContext(r.Context(), "SELECT provider, model, thinking, api_key FROM agent_settings WHERE id = 1").
			Scan(&s.Provider, &s.Model, &s.Thinking, &key)
		if err != nil {
			serverError(w, err)
			return
		}
		s.HasAPIKey = key != ""
		s.KeyHint = keyHint(key)
		writeJSON(w, http.StatusOK, s)
	}

	mux.HandleFunc("GET /api/agent-settings", getSettings)

	mux.HandleFunc("PATCH /api/agent-settings", func(w http.ResponseWriter, r *http.Request) {
		var patch struct {
			Provider *string `json:"provider"`
			Model    *string `json:"model"`
			Thinking *string `json:"thinking"`
			// An empty key keeps the stored one; clearing goes through clearApiKey.
			APIKey      *string `json:"apiKey"`
			ClearAPIKey bool    `json:"clearApiKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Provider != nil && !contains(agentProviders, *patch.Provider) {
			http.Error(w, "unsupported provider", http.StatusBadRequest)
			return
		}
		if patch.Thinking != nil && !contains(thinkingLevels, *patch.Thinking) {
			http.Error(w, "invalid thinking level", http.StatusBadRequest)
			return
		}
		if patch.Model != nil {
			*patch.Model = strings.TrimSpace(*patch.Model)
		}
		if patch.APIKey != nil {
			if *patch.APIKey = strings.TrimSpace(*patch.APIKey); *patch.APIKey == "" {
				patch.APIKey = nil
			}
		}
		if patch.ClearAPIKey {
			empty := ""
			patch.APIKey = &empty
		}

		_, err := db.ExecContext(r.Context(),
			"UPDATE agent_settings SET provider = COALESCE(?, provider), model = COALESCE(?, model), thinking = COALESCE(?, thinking), api_key = COALESCE(?, api_key) WHERE id = 1",
			patch.Provider, patch.Model, patch.Thinking, patch.APIKey)
		if err != nil {
			serverError(w, err)
			return
		}
		getSettings(w, r)
	})

	// Runs an agent process's prompt in the worker and waits for the result.
	// Closing the request cancels the run.
	mux.HandleFunc("POST /api/processes/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var kind, prompt string
		err = db.QueryRowContext(r.Context(), "SELECT kind, prompt FROM processes WHERE id = ?", id).Scan(&kind, &prompt)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "process not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if kind != kindAgent {
			http.Error(w, "only agent processes can be run", http.StatusBadRequest)
			return
		}

		var run struct {
			Prompt   string `json:"prompt"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Thinking string `json:"thinking"`
			APIKey   string `json:"apiKey"`
		}
		err = db.QueryRowContext(r.Context(), "SELECT provider, model, thinking, api_key FROM agent_settings WHERE id = 1").
			Scan(&run.Provider, &run.Model, &run.Thinking, &run.APIKey)
		if err != nil {
			serverError(w, err)
			return
		}
		// Configuration problems are run failures, so they show up on the node like any other.
		run.Prompt = prompt
		switch {
		case strings.TrimSpace(prompt) == "":
			writeJSON(w, http.StatusOK, runResult{Error: "the process has no prompt"})
			return
		case run.Model == "" || run.APIKey == "":
			writeJSON(w, http.StatusOK, runResult{Error: "the agent is not configured; set a model and API key in settings"})
			return
		}

		res, err := callWorker(r, workerURL, run)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			writeJSON(w, http.StatusOK, runResult{Error: "worker unavailable: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

func callWorker(r *http.Request, workerURL string, run any) (runResult, error) {
	body, err := json.Marshal(run)
	if err != nil {
		return runResult{}, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, workerURL+"/run", bytes.NewReader(body))
	if err != nil {
		return runResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	// No client timeout: agent runs can take minutes, and the worker enforces its own limit.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return runResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return runResult{}, fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	var res runResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return runResult{}, fmt.Errorf("decode response: %w", err)
	}
	return res, nil
}
