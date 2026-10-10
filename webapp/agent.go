package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"maps"
	"net/http"
	"slices"
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
	// How the process takes it: as an instrument, consumed, or affected,
	// to hand in changed; empty when it just takes it.
	Role string `json:"role,omitempty"`
}

// The roles of a run's input, as the agent is told them.
const (
	inputInstrument = "instrument"
	inputConsumed   = "consumed"
	inputAffected   = "affected"
)

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
	// The workspace it works in, if it works on repositories.
	Workspace *workspaceSpec `json:"workspace,omitempty"`
	// In a workspace, the pi session the worker keeps its conversation in, so
	// a person can carry it on once the step is over; see followups.go.
	// Resume carries on one that exists.
	SessionID string `json:"sessionId,omitempty"`
	Resume    bool   `json:"resume,omitempty"`
	// How long the run may take, if the process has a timeout; the
	// worker's own limit otherwise.
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
}

// The pi session of a step that worked in a workspace, which follow-ups carry on.
type stepSession struct {
	ID        string         `json:"id"`
	Workspace *workspaceSpec `json:"workspace"`
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
	// What an agent deciding states decided, by object id; the worker
	// leaves it out.
	Decisions map[int64]decided `json:"-"`
	// Set when what the step generated holds a unique value another run
	// stored; its run is terminated.
	Duplicate *duplicate `json:"-"`
	// For a step in a workspace: what it did to each repository it worked
	// on, as the worker saw it.
	Commits []repoCommits `json:"commits,omitempty"`
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
func runAgentStep(ctx context.Context, db *sql.DB, workerURL string, mcp *mcpBroker, ws *runWorkspace, runID, id int64, inputs []runInput, cr *changeRequest, started func(int64)) (runResult, error) {
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
	run.TimeoutMS = settings.timeout().Milliseconds()
	switch {
	case strings.TrimSpace(run.Prompt) == "":
		res.Error = "the process has no prompt"
	case msg != "":
		res.Error = msg
	default:
		run.Workspace = ws.forProcess(settings)
		run.Prompt = withChangeRequest(withInputs(withWorkspace(run.Prompt, run.Workspace), inputs), cr)
	}

	stepID, err := startStep(ctx, db, runID, id, stepActive, run.Prompt, inputs, cr)
	if err != nil {
		return res, err
	}
	started(stepID)
	if err := recordBranch(ctx, db, stepID, run.Workspace); err != nil {
		return res, err
	}
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
	if res.Error == "" && run.Workspace != nil {
		if run.SessionID, err = newSessionID(); err != nil {
			return res, err
		}
		b, err := json.Marshal(stepSession{ID: run.SessionID, Workspace: run.Workspace})
		if err != nil {
			return res, err
		}
		if _, err := db.ExecContext(ctx, "UPDATE run_steps SET session = ? WHERE id = ?", b, stepID); err != nil {
			return res, err
		}
	}
	if res.Error == "" {
		res = runAgent(ctx, db, workerURL, run, stepID, 1)
		if contract != nil && res.OK {
			res = followContract(ctx, db, workerURL, run, contract, res, stepID)
		}
		if err := recordCommits(context.Background(), db, stepID, res.Commits); err != nil {
			log.Printf("record commits of step %d: %v", stepID, err)
		}
	}
	status := stepError
	if res.OK {
		status = stepDone
	}
	// An object it reviewed keeps what it was given.
	if contract != nil && res.OK {
		for _, d := range contract.decisions {
			if !d.review {
				continue
			}
			for _, in := range inputs {
				if in.Name == d.name {
					res.Objects[d.id] = in.Content
				}
			}
		}
	}
	// The run may have been stopped, but the step still has to end.
	if res.Duplicate, err = finishStep(context.Background(), db, stepID, status, res.Output, res.Error, res.Objects); err != nil {
		return res, err
	}
	return res, recordDecisions(context.Background(), db, stepID, res.Decisions)
}

// Records the state each object an agent decided on came out in, and as
// the step's outcome what it decided, as a human step's is.
func recordDecisions(ctx context.Context, db *sql.DB, stepID int64, decisions map[int64]decided) error {
	var outcomes, instructions []string
	for _, object := range slices.Sorted(maps.Keys(decisions)) {
		d := decisions[object]
		var name string
		if err := db.QueryRowContext(ctx, "SELECT name FROM object_states WHERE id = ?", d.state).Scan(&name); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "UPDATE run_objects SET state = ? WHERE step_id = ? AND object_id = ?", name, stepID, object); err != nil {
			return err
		}
		outcomes = append(outcomes, name)
		if d.instructions != "" {
			instructions = append(instructions, d.instructions)
		}
	}
	if len(outcomes) == 0 {
		return nil
	}
	_, err := db.ExecContext(ctx, "UPDATE run_steps SET outcome = ?, instructions = ? WHERE id = ?",
		strings.Join(outcomes, ", "), strings.Join(instructions, "\n\n"), stepID)
	return err
}

// Runs the agent in the worker, recording each message of its conversation
// as the step's as it comes in, so what the agent does can be followed while
// it goes. attempt counts the tries of the step, from 1.
func runAgent(ctx context.Context, db *sql.DB, workerURL string, run agentRun, stepID int64, attempt int) runResult {
	failed := func(err error) runResult {
		if ctx.Err() != nil {
			return runResult{Error: stopReason(ctx)}
		}
		return runResult{Error: "worker unavailable: " + err.Error()}
	}
	resp, err := requestWorker(ctx, http.MethodPost, workerURL+"/run", run)
	if err != nil {
		return failed(err)
	}
	defer resp.Body.Close()
	// A record per line: each message as it ends, then the result.
	dec := json.NewDecoder(resp.Body)
	for {
		var rec struct {
			Message json.RawMessage `json:"message"`
			Result  *runResult      `json:"result"`
		}
		if err := dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("it ended the run without a result")
			}
			return failed(err)
		}
		if rec.Result != nil {
			return *rec.Result
		}
		if rec.Message != nil {
			if err := recordMessage(ctx, db, stepID, attempt, rec.Message); err != nil && ctx.Err() == nil {
				log.Printf("record message of step %d: %v", stepID, err)
			}
		}
	}
}

// Checks what an agent handed in against the interface of the objects it
// generates. The worker's submit_output tool has the agent fix what breaks
// it, so this catches an agent that never handed anything in. That gets one
// more try, and the step records the prompt of that try, which holds the
// rejected reply. An agent with a session carries it on, which holds the
// prompt and the reply already, so it is only told what went wrong.
func followContract(ctx context.Context, db *sql.DB, workerURL string, run agentRun, c *outputContract, res runResult, stepID int64) runResult {
	out, objects, decisions, err := c.handedIn(res)
	if err != nil {
		switch {
		case run.SessionID != "":
			run.Resume = true
			run.Prompt = fmt.Sprintf("Your reply wasn't handed in: %s. Hand in what you generate by calling submit_output now.", err)
		case stepID != 0:
			run.Prompt = withRetry(run.Prompt, res.Output, err)
			if _, err := db.ExecContext(ctx, "UPDATE run_steps SET prompt = ? WHERE id = ?", run.Prompt, stepID); err != nil {
				log.Printf("record retry of step %d: %v", stepID, err)
			}
		default:
			run.Prompt = withRetry(run.Prompt, res.Output, err)
		}
		first := res.Commits
		res = runAgent(ctx, db, workerURL, run, stepID, 2)
		res.Commits = mergeCommits(first, res.Commits)
		if !res.OK {
			return res
		}
		if out, objects, decisions, err = c.handedIn(res); err != nil {
			return runResult{Output: res.Output, Error: "The output didn't follow the interface of what the process generates twice. The last time " + err.Error() + ".", Commits: res.Commits}
		}
	}
	return runResult{OK: true, Output: out, Objects: objects, Decisions: decisions, Commits: res.Commits}
}

// What an agent handed in, checked against the contract, and what it decided.
func (c *outputContract) handedIn(res runResult) (string, map[int64]string, map[int64]decided, error) {
	if !res.Submitted {
		return "", nil, nil, errors.New("it wasn't handed in by calling submit_output")
	}
	return c.parseDecisions(res.Output)
}

// Appends each input object to the prompt, tagged with its name so the agent
// can tell them apart and match them to the prompt's wording.
func withInputs(prompt string, inputs []runInput) string {
	if len(inputs) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nYour inputs follow, one <input> element per object.")
	roles := map[string]bool{}
	for _, in := range inputs {
		roles[in.Role] = true
	}
	if roles[inputInstrument] {
		fmt.Fprintf(&b, " One whose role is %s is a reference to work with: use it, but don't change it.", inputInstrument)
	}
	if roles[inputAffected] {
		fmt.Fprintf(&b, " One whose role is %s is yours to change: what you hand in for it is its new version.", inputAffected)
	}
	b.WriteString("\n")
	for _, in := range inputs {
		fmt.Fprintf(&b, "\n<input name=\"%s\"%s%s>\n%s\n</input>\n", html.EscapeString(in.Name), stateAttr(in.State), roleAttr(in.Role), in.Content)
	}
	return b.String()
}

func roleAttr(role string) string {
	if role == "" || role == inputConsumed {
		return ""
	}
	return fmt.Sprintf(" role=\"%s\"", role)
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

func callWorker(ctx context.Context, method, url string, in, out any) error {
	resp, err := requestWorker(ctx, method, url, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Sends a request to the worker; the reply is the caller's to read and close
// if it succeeded. No client timeout: agent runs can take minutes, and the
// worker enforces its own limit.
func requestWorker(ctx context.Context, method, url string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return resp, nil
}
