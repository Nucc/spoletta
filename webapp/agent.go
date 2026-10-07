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

// An object a process takes as input, with the output of the process that
// generated it and the state it is in, if any.
type runInput struct {
	Name    string `json:"name"`
	State   string `json:"state,omitempty"`
	Content string `json:"content"`
}

// Sent when a human process set an object to a state this process takes it
// in, and left instructions: the work the human reviewed, the state they set
// it to and what they want done. Requests from before states have no state.
type changeRequest struct {
	Reviewer     string     `json:"reviewer"`
	State        string     `json:"state,omitempty"`
	Instructions string     `json:"instructions"`
	Reviewed     []runInput `json:"reviewed"`
}

// What the worker runs.
type agentRun struct {
	// Identify the process in the worker's logs.
	ProcessID   int64  `json:"processId"`
	ProcessName string `json:"processName"`
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
}

// Inputs are capped so a runaway output can't grow every prompt downstream without bound.
const maxRunRequestBytes = 10 << 20

type runResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
	// What each output object with attributes holds, by object id, when the
	// process generates objects with attributes; the others hold the output.
	// The worker leaves it out.
	Objects map[int64]string `json:"objects,omitempty"`
	// Set when what the step generated holds a unique value another run
	// stored; its run is terminated.
	Duplicate *duplicate `json:"duplicate,omitempty"`
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
		run := agentRun{ProcessID: id}
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
		contract, msg, err := contractFor(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		switch {
		case strings.TrimSpace(run.Prompt) == "":
			res.Error = "the process has no prompt"
		case msg != "":
			res.Error = msg
		default:
			run.Prompt = withChangeRequest(withInputs(run.Prompt, body.Inputs), body.ChangeRequest)
			if contract != nil {
				run.Prompt = contract.withContract(run.Prompt)
			}
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
			res = runAgent(r.Context(), workerURL, run)
			if contract != nil && res.OK {
				res = followContract(r.Context(), db, workerURL, run, contract, res, stepID)
			}
		}
		if stepID != 0 {
			status := stepError
			if res.OK {
				status = stepDone
			}
			// The request may be gone, but the step still has to end.
			dup, err := finishStep(context.Background(), db, stepID, status, res.Output, res.Error, res.Objects)
			if err != nil {
				log.Printf("record step %d: %v", stepID, err)
			}
			res.Duplicate = dup
		}
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

func runAgent(ctx context.Context, workerURL string, run agentRun) runResult {
	var res runResult
	if err := callWorker(ctx, http.MethodPost, workerURL+"/run", run, &res); err != nil {
		if ctx.Err() != nil {
			return runResult{Error: "the run was cancelled"}
		}
		return runResult{Error: "worker unavailable: " + err.Error()}
	}
	return res
}

// Checks an agent's reply against the attributes of the objects it
// generates. A reply that breaks the contract gets one more try, and the
// step records the prompt of that try, which holds the rejected reply.
func followContract(ctx context.Context, db *sql.DB, workerURL string, run agentRun, c *outputContract, res runResult, stepID int64) runResult {
	out, objects, err := c.parse(res.Output)
	if err != nil {
		run.Prompt = withRetry(run.Prompt, res.Output, err)
		if stepID != 0 {
			if _, err := db.ExecContext(ctx, "UPDATE run_steps SET prompt = ? WHERE id = ?", run.Prompt, stepID); err != nil {
				log.Printf("record retry of step %d: %v", stepID, err)
			}
		}
		if res = runAgent(ctx, workerURL, run); !res.OK {
			return res
		}
		if out, objects, err = c.parse(res.Output); err != nil {
			return runResult{Output: res.Output, Error: "The reply didn't follow the output format twice. The last time " + err.Error() + "."}
		}
	}
	return runResult{OK: true, Output: out, Objects: objects}
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
		fmt.Fprintf(&b, "\n<input name=\"%s\"%s>\n%s\n</input>\n", html.EscapeString(in.Name), stateAttr(in.State), in.Content)
	}
	return b.String()
}

func stateAttr(state string) string {
	if state == "" {
		return ""
	}
	return fmt.Sprintf(" state=\"%s\"", html.EscapeString(state))
}

// Appends the work a human reviewed and their instructions, so the agent
// works on that rather than starting over.
func withChangeRequest(prompt string, cr *changeRequest) string {
	if cr == nil {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	if cr.State == "" {
		fmt.Fprintf(&b, "\n\nThe reviewer %q requested changes. Revise the work below following their instructions.\n",
			cr.Reviewer)
	} else {
		fmt.Fprintf(&b, "\n\nThe reviewer %q marked the work below as %q. Act on it following their instructions.\n",
			cr.Reviewer, cr.State)
	}
	for _, in := range cr.Reviewed {
		fmt.Fprintf(&b, "\n<reviewed name=\"%s\"%s>\n%s\n</reviewed>\n", html.EscapeString(in.Name), stateAttr(in.State), in.Content)
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
