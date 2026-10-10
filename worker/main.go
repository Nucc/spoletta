// Runs pi agent harnesses and scripts on behalf of the webapp. Each POST /run
// spawns one non-interactive pi process and streams its conversation back as
// it goes, ending with the outcome; each POST /script runs one bash script
// and replies once it exits. Either runs in its run's workspace, a directory
// of git checkouts on the run's branch (see workspace.go), or without one in
// a fresh scratch directory.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxBodyBytes = 1 << 20

// Settings that choose models. The rest (packages, extensions, UI) belongs
// to the interactive setup and isn't wanted in headless runs.
var modelSettings = []string{
	"defaultProvider",
	"defaultModel",
	"defaultThinkingLevel",
	"modelThinkingLevels",
	"thinkingBudgets",
	"enabledModels",
}

var thinkingSuffix = regexp.MustCompile(`:(off|minimal|low|medium|high|xhigh|max)$`)

// The models offered for agent processes: pi's enabledModels and its default.
type agentModels struct {
	Default string   `json:"default"`
	Models  []string `json:"models"`
}

type runRequest struct {
	// Identify the process in the logs.
	ProcessID   int64  `json:"processId"`
	ProcessName string `json:"processName"`
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	// The objects the process generates, which the agent hands in through
	// submit_output; see submit-output.ts. Absent when it generates none.
	Output *outputSpec `json:"output"`
	// Where the agent writes files; inside the files root.
	FilesDir string `json:"filesDir"`
	// The only MCP servers pi gets, which mcp-servers.ts registers; with
	// none, pi runs without MCP. The URLs are the webapp's proxy, which
	// takes the token.
	MCPServers []mcpServer `json:"mcpServers"`
	MCPToken   string      `json:"mcpToken"`
	// The workspace it works in, if the flow has repositories.
	Workspace *workspaceSpec `json:"workspace"`
	// In a workspace: the pi session the conversation is kept in, so a person
	// can carry it on later. Resume says the session must exist already,
	// which a step's first run creates.
	SessionID string `json:"sessionId"`
	Resume    bool   `json:"resume"`
}

type mcpServer struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// The interface of what a process generates, as submit-output.ts reads it.
type outputSpec struct {
	Shape   string          `json:"shape"`
	Objects json.RawMessage `json:"objects"`
	// The objects the agent decides the state of, if any.
	Decisions json.RawMessage `json:"decisions,omitempty"`
	// Set by the worker.
	FilesDir   string `json:"filesDir"`
	ResultFile string `json:"resultFile"`
}

// A script process's script, with the objects it takes.
type scriptRequest struct {
	// Identify the process in the logs.
	ProcessID   int64         `json:"processId"`
	ProcessName string        `json:"processName"`
	Script      string        `json:"script"`
	Inputs      []scriptInput `json:"inputs"`
	// Where the script writes files, which $OUTPUTS points to; inside the files root.
	FilesDir string `json:"filesDir"`
	// The workspace it works in, if the flow has repositories.
	Workspace *workspaceSpec `json:"workspace"`
}

type scriptInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Scripts' inputs and outputs can be lists of many items, so they may be
// larger than prompts.
const maxScriptBytes = 10 << 20

type runResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
	// For a run with an output spec: whether the agent handed in its output
	// through submit_output, which then is the output. If it didn't, the
	// output is what it printed.
	Submitted bool `json:"submitted"`
	// For a step in a workspace: what it did to each repository it worked on.
	Commits []repoCommits `json:"commits,omitempty"`
}

// Where files that runs generate are kept, a volume the webapp serves them from.
var filesRoot = envOr("FILES_DIR", "/data/files")

// The submit_output extension, which the image puts here.
var outputExtension = envOr("OUTPUT_EXTENSION", "/opt/flow/submit-output.ts")

// The extension that registers a run's MCP servers, which the image puts here.
var mcpExtension = envOr("MCP_EXTENSION", "/opt/flow/mcp-servers.ts")

// Creates a run's files directory, which must be inside the files root.
func makeFilesDir(dir string) error {
	rel, err := filepath.Rel(filesRoot, filepath.Clean(dir))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("files directory %q isn't inside %s", dir, filesRoot)
	}
	return os.MkdirAll(dir, 0o777)
}

func main() {
	log.SetFlags(0)
	port := envOr("PORT", "8090")
	timeout := 10 * time.Minute
	if v := os.Getenv("RUN_TIMEOUT_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil {
			log.Fatalf("worker: invalid RUN_TIMEOUT_MS: %v", err)
		}
		timeout = time.Duration(ms) * time.Millisecond
	}
	// The user's pi config, mounted read-only. Only models.json and
	// settings.json are used; credentials come from the environment.
	hostConfigDir := envOr("PI_HOST_CONFIG_DIR", "/pi-host")
	// pi writes to its agent directory, so it gets a writable one built from the host config.
	agentDir := envOr("PI_CODING_AGENT_DIR", "/root/.pi/agent")

	models, err := prepareAgentDir(hostConfigDir, agentDir)
	if err != nil {
		log.Fatalf("worker: %v", err)
	}
	def := models.Default
	if def == "" {
		def = "(pi default)"
	}
	log.Printf("worker: %d enabled models, default %s", len(models.Models), def)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, models)
	})
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var run runRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&run); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if run.Prompt == "" {
			reply(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
			return
		}
		for field, v := range map[string]string{"model": run.Model, "thinking": run.Thinking} {
			if strings.HasPrefix(v, "-") {
				reply(w, http.StatusBadRequest, map[string]string{"error": "invalid " + field})
				return
			}
		}

		if run.FilesDir != "" {
			if err := makeFilesDir(run.FilesDir); err != nil {
				reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}

		// The reply is a JSON record per line: {"message": ...} for each message
		// of the conversation as it ends, then {"result": ...} with the outcome.
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		var mu sync.Mutex
		send := func(record any) {
			mu.Lock()
			defer mu.Unlock()
			if err := json.NewEncoder(w).Encode(record); err == nil {
				rc.Flush()
			}
		}

		started := time.Now()
		res := runPi(r.Context(), run, timeout, func(m json.RawMessage) {
			send(map[string]json.RawMessage{"message": m})
		})
		logRun(run, res, time.Since(started))
		if r.Context().Err() == nil {
			send(map[string]runResult{"result": res})
		}
	})

	mux.HandleFunc("POST /script", func(w http.ResponseWriter, r *http.Request) {
		var run scriptRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxScriptBytes)).Decode(&run); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if strings.TrimSpace(run.Script) == "" {
			reply(w, http.StatusBadRequest, map[string]string{"error": "script is required"})
			return
		}

		if run.FilesDir != "" {
			if err := makeFilesDir(run.FilesDir); err != nil {
				reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}

		started := time.Now()
		res := runScript(r.Context(), run, timeout)
		logOutcome(processLabel(run.ProcessID, run.ProcessName), "bash", res, time.Since(started))
		if r.Context().Err() == nil {
			reply(w, http.StatusOK, res)
		}
	})

	// Deletes the workspaces of runs: {"runs": [ids]}.
	mux.HandleFunc("POST /workspaces/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Runs []int64 `json:"runs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&body); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		if err := deleteWorkspaces(body.Runs); err != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, map[string]bool{"ok": true})
	})

	log.Printf("worker: listening on %s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// enabledModels may hold patterns (anthropic/*, sonnet:high); only exact
// provider/id entries can be offered as choices. Ids like bedrock's ...-v1:0
// contain a colon too, so only thinking-level suffixes count as patterns.
func isExactModel(m string) bool {
	return strings.Index(m, "/") > 0 && !strings.ContainsAny(m, "*?") && !thinkingSuffix.MatchString(m)
}

// Reads a JSON file from the host config, returning nil if it doesn't exist.
func readHostJSON(dir, name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("%s: invalid JSON", name)
	}
	return b, nil
}

// Builds the agent directory once at startup; restart the worker to pick up config changes.
func prepareAgentDir(hostDir, agentDir string) (agentModels, error) {
	var models agentModels
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return models, err
	}

	raw, err := readHostJSON(hostDir, "settings.json")
	if err != nil {
		return models, err
	}
	settings := map[string]json.RawMessage{}
	if raw != nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return models, fmt.Errorf("settings.json: %w", err)
		}
	}
	trimmed := map[string]json.RawMessage{}
	for _, k := range modelSettings {
		if v, ok := settings[k]; ok {
			trimmed[k] = v
		}
	}
	out, err := json.MarshalIndent(trimmed, "", "  ")
	if err != nil {
		return models, err
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), out, 0o644); err != nil {
		return models, err
	}

	modelsJSON, err := readHostJSON(hostDir, "models.json")
	if err != nil {
		return models, err
	}
	if modelsJSON != nil {
		err = os.WriteFile(filepath.Join(agentDir, "models.json"), modelsJSON, 0o644)
	} else {
		err = os.Remove(filepath.Join(agentDir, "models.json"))
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	}
	if err != nil {
		return models, err
	}

	var provider, model string
	var enabled []any
	// Values of the wrong type are treated as unset.
	_ = json.Unmarshal(trimmed["defaultProvider"], &provider)
	_ = json.Unmarshal(trimmed["defaultModel"], &model)
	_ = json.Unmarshal(trimmed["enabledModels"], &enabled)
	if provider != "" && model != "" {
		models.Default = provider + "/" + model
	}
	models.Models = []string{}
	for _, e := range enabled {
		if m, ok := e.(string); ok && isExactModel(m) {
			models.Models = append(models.Models, m)
		}
	}
	return models, nil
}

// Model ids may contain slashes and colons (fireworks/accounts/..., bedrock's
// ...-v1:0), so the provider is split off and passed separately rather than
// letting pi parse a provider/id[:thinking] pattern.
func modelArgs(model string) []string {
	if model == "" {
		return nil
	}
	if provider, id, ok := strings.Cut(model, "/"); ok && provider != "" {
		return []string{"--provider", provider, "--model", id}
	}
	return []string{"--model", model}
}

// Returns the run outcome; a client that disconnects (ctx is cancelled) or a
// run that exceeds the timeout kills pi. emit gets each message of the
// conversation as it ends.
func runPi(ctx context.Context, run runRequest, timeout time.Duration, emit func(json.RawMessage)) (res runResult) {
	session, err := sessionArgs(run)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	cwd, ws, err := workDir(ctx, run.Workspace, "run-")
	if err != nil {
		return runResult{Error: err.Error()}
	}
	defer ws.done(&res)

	// An empty model or thinking level leaves the choice to the pi settings.
	// pi prints its events as JSON, so the conversation can be followed.
	args := append(append([]string{"-p", "--mode", "json", "--no-approve"}, session...), modelArgs(run.Model)...)
	if run.Thinking != "" {
		args = append(args, "--thinking", run.Thinking)
	}

	// The specs and the result live outside the agent's working directory.
	var resultFile string
	env := os.Environ()
	if len(run.MCPServers) > 0 || run.Output != nil {
		private, err := os.MkdirTemp("", "private-")
		if err != nil {
			return runResult{Error: "create private directory: " + err.Error()}
		}
		defer os.RemoveAll(private)
		if len(run.MCPServers) > 0 {
			b, err := json.Marshal(map[string]any{"servers": run.MCPServers, "token": run.MCPToken})
			if err == nil {
				err = os.WriteFile(filepath.Join(private, "mcp.json"), b, 0o600)
			}
			if err != nil {
				return runResult{Error: "write MCP servers: " + err.Error()}
			}
			args = append(args, "-e", mcpExtension)
			env = append(env, "FLOW_MCP_SERVERS="+filepath.Join(private, "mcp.json"))
		}
		if run.Output != nil {
			if resultFile, err = writeOutputSpec(private, run); err != nil {
				return runResult{Error: "write output spec: " + err.Error()}
			}
			args = append(args, "-e", outputExtension)
			env = append(env, "FLOW_OUTPUT_SPEC="+filepath.Join(private, "spec.json"))
		}
	}
	if len(run.MCPServers) == 0 {
		args = append(args, "--no-mcp")
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)

	defer cancel()
	cmd := exec.CommandContext(runCtx, "pi", args...)
	cmd.Dir = cwd
	cmd.Env = env
	// pi gets its own process group so stopping it also stops the tools it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	// Don't wait forever on a pi that ignores SIGTERM or leaves children holding its output.
	cmd.WaitDelay = 10 * time.Second
	// The prompt goes in on stdin: as an argument, a leading @ or - would be
	// parsed as a file reference or an option. pi may exit before reading it,
	// e.g. on a bad model; that's reported by its exit status.
	cmd.Stdin = strings.NewReader(run.Prompt)
	events := &eventStream{emit: emit}
	var stderr bytes.Buffer
	cmd.Stdout = events
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return runResult{Error: "failed to start pi: " + err.Error()}
	}
	err = cmd.Wait()
	output, failure := events.outcome()
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return runResult{Output: output, Error: "timed out"}
	case ctx.Err() != nil:
		return runResult{Output: output, Error: "cancelled"}
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "pi failed: " + err.Error()
		}
		return runResult{Output: output, Error: msg}
	case failure != "":
		// In JSON mode pi exits fine even when the model's request failed.
		return runResult{Output: output, Error: failure}
	}
	if resultFile != "" {
		// Missing if the agent never called submit_output, or the extension failed to load.
		if b, err := os.ReadFile(resultFile); err == nil {
			return runResult{OK: true, Output: string(b), Submitted: true}
		}
	}
	return runResult{OK: true, Output: output}
}

// Writes the output spec submit-output.ts reads, returning where its result goes.
func writeOutputSpec(dir string, run runRequest) (string, error) {
	spec := *run.Output
	spec.FilesDir = run.FilesDir
	spec.ResultFile = filepath.Join(dir, "result.json")
	b, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return spec.ResultFile, os.WriteFile(filepath.Join(dir, "spec.json"), b, 0o644)
}

// Reads pi's JSON event stream (docs/json.md in pi): a JSON record per line,
// split on LF only. Of the events, only each message's end is kept, which
// holds the whole message: the prompt, each reply of the model with its
// thinking and tool calls, and each tool's result.
type eventStream struct {
	emit func(json.RawMessage)
	buf  []byte
	last piMessage
}

type piMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

func (s *eventStream) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		s.record(bytes.TrimSuffix(s.buf[:i], []byte("\r")))
		s.buf = s.buf[i+1:]
	}
}

func (s *eventStream) record(line []byte) {
	var ev struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Type != "message_end" {
		return
	}
	// A user message's content can be a plain string, which doesn't fit
	// piMessage; only the role matters then.
	var m piMessage
	if err := json.Unmarshal(ev.Message, &m); err != nil {
		m = piMessage{}
		var role struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(ev.Message, &role)
		m.Role = role.Role
	}
	s.last = m
	if s.emit != nil {
		s.emit(ev.Message)
	}
}

// What the run printed, as text mode would: the text of the last message if
// the model wrote it. A failed or aborted request instead gives its error.
func (s *eventStream) outcome() (output, failure string) {
	m := s.last
	if m.Role != "assistant" {
		return "", ""
	}
	if m.StopReason == "error" || m.StopReason == "aborted" {
		if m.ErrorMessage == "" {
			return "", "Request " + m.StopReason
		}
		return "", m.ErrorMessage
	}
	var texts []string
	for _, c := range m.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), ""
}

// The directory a step works in, and what gives it back once the step is
// done: its run's workspace, ready and taken, or a scratch directory deleted
// afterwards. Getting the workspace ready doesn't count towards the step's
// timeout.
func workDir(ctx context.Context, ws *workspaceSpec, scratch string) (string, *takenWorkspace, error) {
	if ws != nil {
		t, err := takeWorkspace(ctx, ws)
		if err != nil {
			return "", nil, errors.New("prepare workspace: " + err.Error())
		}
		return t.dir, t, nil
	}
	dir, err := os.MkdirTemp("", scratch)
	if err != nil {
		return "", nil, errors.New("create scratch directory: " + err.Error())
	}
	return dir, &takenWorkspace{release: func() { os.RemoveAll(dir) }}, nil
}

// Characters kept in an input's file name; others become underscores.
var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Runs a script with bash in its run's workspace, which $WORKSPACE points to
// and whose checkouts are on the branch $BRANCH names, or else in a fresh
// scratch directory. Each object it takes is a file in the directory $INPUTS
// points to, named after the object: spaces and other characters unsafe in a
// file name become underscores, so Jira Ticket is $INPUTS/Jira_Ticket. Files
// it generates go in the directory $OUTPUTS points to, which is kept. What
// the script prints is its output; a non-zero exit fails it, with what it
// printed to stderr as the error. Like pi, it runs in a process group of its own,
// which a cancelled request or the timeout kills.
func runScript(ctx context.Context, run scriptRequest, timeout time.Duration) (res runResult) {
	cwd, ws, err := workDir(ctx, run.Workspace, "script-")
	if err != nil {
		return runResult{Error: err.Error()}
	}
	defer ws.done(&res)
	// The inputs and the script stay out of the working directory, which a
	// workspace shares with other steps.
	private, err := os.MkdirTemp("", "script-private-")
	if err != nil {
		return runResult{Error: "create private directory: " + err.Error()}
	}
	defer os.RemoveAll(private)
	inputs := filepath.Join(private, "inputs")
	if err := os.Mkdir(inputs, 0o755); err != nil {
		return runResult{Error: "create inputs directory: " + err.Error()}
	}
	for _, in := range run.Inputs {
		name := strings.Trim(unsafeName.ReplaceAllString(in.Name, "_"), "._")
		if name == "" {
			name = "input"
		}
		if err := os.WriteFile(filepath.Join(inputs, name), []byte(in.Content), 0o644); err != nil {
			return runResult{Error: "write input " + in.Name + ": " + err.Error()}
		}
	}
	script := filepath.Join(private, "script")
	if err := os.WriteFile(script, []byte(run.Script), 0o644); err != nil {
		return runResult{Error: "write script: " + err.Error()}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "bash", script)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "INPUTS="+inputs)
	if run.Workspace != nil {
		cmd.Env = append(cmd.Env, "WORKSPACE="+cwd, "BRANCH="+run.Workspace.Branch)
	}
	if run.FilesDir != "" {
		cmd.Env = append(cmd.Env, "OUTPUTS="+run.FilesDir)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 10 * time.Second
	stdout := &cappedBuffer{max: maxScriptBytes}
	stderr := &cappedBuffer{max: 64 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return runResult{Error: "failed to start bash: " + err.Error()}
	}
	err = cmd.Wait()
	output := strings.TrimSpace(stdout.String())
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return runResult{Output: output, Error: "timed out"}
	case ctx.Err() != nil:
		return runResult{Output: output, Error: "cancelled"}
	case stdout.over:
		return runResult{Error: fmt.Sprintf("the script printed more than %d MB", maxScriptBytes>>20)}
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "the script failed: " + err.Error()
		} else if exit, ok := err.(*exec.ExitError); ok {
			msg += fmt.Sprintf("\n(exit status %d)", exit.ExitCode())
		}
		return runResult{Output: output, Error: msg}
	}
	return runResult{OK: true, Output: output}
}

// Keeps the last max bytes written to it, which for errors hold the lines
// that matter; output past the cap fails the script.
type cappedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.Buffer.Write(p)
	if b.Len() > b.max {
		b.over = true
		b.Next(b.Len() - b.max)
	}
	return len(p), nil
}

func processLabel(id int64, name string) string {
	if id == 0 {
		return "run"
	}
	label := fmt.Sprintf("process %d", id)
	if name != "" {
		label += fmt.Sprintf(" %q", name)
	}
	return label
}

// Logs the outcome followed by the agent's output (or error), each line
// prefixed so multi-line output stays attributable when runs interleave.
func logRun(run runRequest, res runResult, d time.Duration) {
	model := run.Model
	if model == "" {
		model = "default model"
	}
	logOutcome(processLabel(run.ProcessID, run.ProcessName), model, res, d)
}

func logOutcome(label, with string, res runResult, d time.Duration) {
	outcome := "failed"
	if res.OK {
		outcome = "succeeded"
	}
	log.Printf("worker: %s with %s %s in %dms", label, with, outcome, d.Milliseconds())
	for _, s := range []struct{ name, text string }{{"output", res.Output}, {"error", res.Error}} {
		if s.text == "" {
			continue
		}
		for _, line := range strings.Split(s.text, "\n") {
			log.Printf("worker: %s %s| %s", label, s.name, line)
		}
	}
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
