package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
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

// An object a process takes as input, with the output of the process that generated it.
type runInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Sent when a human process requested changes and routed back to this one:
// the work the human reviewed and what they want changed.
type changeRequest struct {
	Reviewer     string     `json:"reviewer"`
	Instructions string     `json:"instructions"`
	Reviewed     []runInput `json:"reviewed"`
}

// Inputs are capped so a runaway output can't grow every prompt downstream without bound.
const maxRunRequestBytes = 10 << 20

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
	// The body may list the process's input objects and a human's change
	// request, which are appended to the prompt. Closing the request cancels
	// the run.
	mux.HandleFunc("POST /api/processes/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var body struct {
			// The flow run this is a step of, if any; the step is recorded in it.
			Run           *int64         `json:"run"`
			Inputs        []runInput     `json:"inputs"`
			ChangeRequest *changeRequest `json:"changeRequest"`
		}
		err = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&body)
		if err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
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
		var res runResult
		if strings.TrimSpace(run.Prompt) == "" {
			res.Error = "the process has no prompt"
		} else {
			run.Prompt = withChangeRequest(withInputs(run.Prompt, body.Inputs), body.ChangeRequest)
		}

		var stepID int64
		if body.Run != nil {
			stepID, err = startStep(r.Context(), db, *body.Run, id, stepActive, run.Prompt, body.Inputs, body.ChangeRequest)
			if mysqlErrorIs(err, mysqlErrForeignKey) {
				http.Error(w, "run not found", http.StatusBadRequest)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
		}

		if res.Error == "" {
			if err := callWorker(r.Context(), http.MethodPost, workerURL+"/run", run, &res); err != nil {
				if r.Context().Err() != nil {
					res = runResult{Error: "the run was cancelled"}
				} else {
					res = runResult{Error: "worker unavailable: " + err.Error()}
				}
			}
		}
		if stepID != 0 {
			status := stepError
			if res.OK {
				status = stepDone
			}
			// The request may be gone, but the step still has to end.
			if err := finishStep(context.Background(), db, stepID, status, res.Output, res.Error); err != nil {
				log.Printf("record step %d: %v", stepID, err)
			}
		}
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// Appends each input object to the prompt, tagged with its name so the agent
// can tell them apart and match them to the prompt's wording.
func withInputs(prompt string, inputs []runInput) string {
	if len(inputs) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nYour inputs follow, one <input> element per object.\n")
	for _, in := range inputs {
		fmt.Fprintf(&b, "\n<input name=\"%s\">\n%s\n</input>\n", html.EscapeString(in.Name), in.Content)
	}
	return b.String()
}

// Appends the work a human reviewed and their instructions, so the agent
// revises that work rather than starting over.
func withChangeRequest(prompt string, cr *changeRequest) string {
	if cr == nil {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	fmt.Fprintf(&b, "\n\nThe reviewer %q requested changes. Revise the work below following their instructions.\n",
		cr.Reviewer)
	for _, in := range cr.Reviewed {
		fmt.Fprintf(&b, "\n<reviewed name=\"%s\">\n%s\n</reviewed>\n", html.EscapeString(in.Name), in.Content)
	}
	fmt.Fprintf(&b, "\n<change_request>\n%s\n</change_request>\n", cr.Instructions)
	return b.String()
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
