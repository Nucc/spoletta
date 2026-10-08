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
	// What the process generates, which the agent hands in through the
	// worker's submit_output tool, whose parameters it shapes.
	Output *outputSpec `json:"output,omitempty"`
	// Where the agent writes the files it generates.
	FilesDir string `json:"filesDir,omitempty"`
	// The MCP servers the process uses, reached through the webapp's proxy
	// with the step's token; pi gets no MCP servers without them.
	MCPServers []mcpRunServer `json:"mcpServers,omitempty"`
	MCPToken   string         `json:"mcpToken,omitempty"`
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
	// Set by the worker when the agent handed in its output through
	// submit_output, which checked it against the output's interface.
	Submitted bool `json:"submitted,omitempty"`
	// Set when what the step generated holds a unique value another run
	// stored; its run is terminated.
	Duplicate *duplicate `json:"-"`
}

func migrateAgent(db *sql.DB) error {
	// Agent settings used to live here, API key included; pi's config replaced them.
	_, err := db.Exec("DROP TABLE IF EXISTS agent_settings")
	return err
}

func registerAgentRoutes(mux *http.ServeMux, workerURL string) {
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		var models agentModels
		if err := callWorker(r.Context(), http.MethodGet, workerURL+"/models", nil, &models); err != nil {
			http.Error(w, "worker unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, models)
	})
}

// Runs an agent process's prompt in the worker as a step of a run and waits
// for the result. Its input objects and a human's change request are
// appended to the prompt. Problems with the process itself are step
// failures, so they show up on the node like any other; the error is for
// recording the step. started gets the step's id once it is recorded.
func runAgentStep(ctx context.Context, db *sql.DB, workerURL string, mcp *mcpBroker, runID, id int64, inputs []runInput, cr *changeRequest, started func(int64)) (runResult, error) {
	var res runResult
	run := agentRun{ProcessID: id}
	var settings processSettings
	err := db.QueryRowContext(ctx, "SELECT name, prompt, model, thinking, settings FROM processes WHERE id = ?", id).
		Scan(&run.ProcessName, &run.Prompt, &run.Model, &run.Thinking, &settings)
	if errors.Is(err, sql.ErrNoRows) {
		return res, errNotFound
	}
	if err != nil {
		return res, err
	}
	contract, msg, err := contractFor(ctx, db, id, true)
	if err != nil {
		return res, err
	}
	switch {
	case strings.TrimSpace(run.Prompt) == "":
		res.Error = "the process has no prompt"
	case msg != "":
		res.Error = msg
	default:
		run.Prompt = withChangeRequest(withInputs(run.Prompt, inputs), cr)
	}

	stepID, err := startStep(ctx, db, runID, id, stepActive, run.Prompt, inputs, cr)
	if err != nil {
		return res, err
	}
	started(stepID)
	// The files directory is the step's, so the contract follows the step.
	if res.Error == "" && contract != nil {
		contract.filesDir = stepFilesDir(runID, stepID)
		run.Prompt = contract.withContract(run.Prompt)
		run.Output = contract.spec()
		run.FilesDir = contract.filesDir
		if _, err := db.ExecContext(ctx, "UPDATE run_steps SET prompt = ? WHERE id = ?", run.Prompt, stepID); err != nil {
			return res, err
		}
	}
	// The step's token reaches its MCP servers until the step ends, retry included.
	if res.Error == "" {
		var msg string
		run.MCPServers, run.MCPToken, msg, err = mcp.grant(ctx, settings.MCPServers)
		if err != nil {
			return res, err
		}
		defer mcp.revoke(run.MCPToken)
		res.Error = msg
	}
	if res.Error == "" {
		res = runAgent(ctx, workerURL, run)
		if contract != nil && res.OK {
			res = followContract(ctx, db, workerURL, run, contract, res, stepID)
		}
	}
	status := stepError
	if res.OK {
		status = stepDone
	}
	// The run may have been stopped, but the step still has to end.
	res.Duplicate, err = finishStep(context.Background(), db, stepID, status, res.Output, res.Error, res.Objects)
	return res, err
}

func runAgent(ctx context.Context, workerURL string, run agentRun) runResult {
	var res runResult
	if err := callWorker(ctx, http.MethodPost, workerURL+"/run", run, &res); err != nil {
		if ctx.Err() != nil {
			return runResult{Error: "it was stopped"}
		}
		return runResult{Error: "worker unavailable: " + err.Error()}
	}
	return res
}

// Checks what an agent handed in against the interface of the objects it
// generates. The worker's submit_output tool has the agent fix what breaks
// it, so this catches an agent that never handed anything in. That gets one
// more try, and the step records the prompt of that try, which holds the
// rejected reply.
func followContract(ctx context.Context, db *sql.DB, workerURL string, run agentRun, c *outputContract, res runResult, stepID int64) runResult {
	out, objects, err := c.handedIn(res)
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
		if out, objects, err = c.handedIn(res); err != nil {
			return runResult{Output: res.Output, Error: "The output didn't follow the interface of what the process generates twice. The last time " + err.Error() + "."}
		}
	}
	return runResult{OK: true, Output: out, Objects: objects}
}

// What an agent handed in, checked against the contract.
func (c *outputContract) handedIn(res runResult) (string, map[int64]string, error) {
	if !res.Submitted {
		return "", nil, errors.New("it wasn't handed in by calling submit_output")
	}
	return c.parse(res.Output)
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
