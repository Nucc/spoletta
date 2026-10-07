package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Agent processes run in the worker's pi harness, which takes its models,
// defaults and credentials from the user's pi setup.

// The models offered for agent processes: pi's enabledModels and its default.
type agentModels struct {
	Default string   `json:"default"`
	Models  []string `json:"models"`
}

type runResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

func migrateAgent(db *sql.DB) error {
	// Agent settings used to live here, API key included; pi's config replaced them.
	_, err := db.Exec("DROP TABLE IF EXISTS agent_settings")
	return err
}

func registerAgentRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		var models agentModels
		if err := callWorker(r.Context(), http.MethodGet, workerURL+"/models", nil, &models); err != nil {
			http.Error(w, "worker unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, models)
	})

	// Runs an agent process's prompt in the worker and waits for the result.
	// Closing the request cancels the run.
	mux.HandleFunc("POST /api/processes/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var kind string
		run := struct {
			// Identify the process in the worker's logs.
			ProcessID   int64  `json:"processId"`
			ProcessName string `json:"processName"`
			Prompt      string `json:"prompt"`
			Model       string `json:"model"`
			Thinking    string `json:"thinking"`
		}{ProcessID: id}
		err = db.QueryRowContext(r.Context(), "SELECT name, kind, prompt, model, thinking FROM processes WHERE id = ?", id).
			Scan(&run.ProcessName, &kind, &run.Prompt, &run.Model, &run.Thinking)
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
		// Problems with the process itself are run failures, so they show up on the node like any other.
		if strings.TrimSpace(run.Prompt) == "" {
			writeJSON(w, http.StatusOK, runResult{Error: "the process has no prompt"})
			return
		}

		var res runResult
		if err := callWorker(r.Context(), http.MethodPost, workerURL+"/run", run, &res); err != nil {
			if r.Context().Err() != nil {
				return
			}
			res = runResult{Error: "worker unavailable: " + err.Error()}
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// No client timeout: agent runs can take minutes, and the worker enforces its own limit.
func callWorker(ctx context.Context, method, url string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
