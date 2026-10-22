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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	// The run the step is of, whose home every step of it gets; see runHome.
	Run int64 `json:"run"`
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
	// The most disk the run may take, in bytes, 0 for no limit; see disk.go.
	DiskLimit int64 `json:"diskLimit,omitempty"`
	// The token its git reaches the workspace's repositories with, if any;
	// see git_credentials.go.
	Git *gitCredentials `json:"git"`
	// In a workspace: the pi session the conversation is kept in, so a person
	// can carry it on later. Resume says the session must exist already,
	// which a step's first run creates.
	SessionID string `json:"sessionId"`
	Resume    bool   `json:"resume"`
	// How long the run may take, if not the worker's own limit.
	TimeoutMS int64 `json:"timeoutMs"`
	// The agent only writes a reply: pi runs without tools, and without the
	// extensions, skills and context files that could bring some back, so
	// it can't read the worker's files or secrets, or run anything.
	NoTools bool `json:"noTools"`
	// The model providers an admin added, which go over the host's
	// models.json; see providers.go.
	Providers []modelProvider `json:"providers"`
	// Where the step reaches its models' providers, the webapp's proxy,
	// with its token; the worker has no keys of its own.
	LLM *llmAccess `json:"llm"`
	// Where a step in a container reaches the internet hosts it may: the
	// webapp's proxy, with the step's token as its credentials.
	Egress *egressAccess `json:"egress"`
	// Put the guide to authoring flows before the prompt, for an agent that
	// can't read it itself.
	Guide bool `json:"guide"`
	// The skills pi is given, each written as a SKILL.md of its own; see
	// writeSkills.
	Skills []skill `json:"skills"`
}

// A skill as the webapp sends it.
type skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

// pi's rule for a skill's name, which is also its directory's.
var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// The guide to authoring flows, docs/FLOWS.md, where the image keeps it.
const flowsGuide = "/opt/flow/docs/FLOWS.md"

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
	// The run the step is of, whose home every step of it gets.
	Run int64 `json:"run"`
	// Identify the process in the logs.
	ProcessID   int64         `json:"processId"`
	ProcessName string        `json:"processName"`
	Script      string        `json:"script"`
	Inputs      []scriptInput `json:"inputs"`
	// Where the script writes files, which $OUTPUTS points to; inside the files root.
	FilesDir string `json:"filesDir"`
	// The workspace it works in, if the flow has repositories.
	Workspace *workspaceSpec `json:"workspace"`
	// The most disk the run may take, in bytes, 0 for no limit; see disk.go.
	DiskLimit int64 `json:"diskLimit,omitempty"`
	// The token its git reaches the workspace's repositories with, if any.
	Git *gitCredentials `json:"git"`
	// Where a script in a container reaches the internet hosts it may.
	Egress *egressAccess `json:"egress"`
	// How long the script may take, if not the worker's own limit.
	TimeoutMS int64 `json:"timeoutMs"`
	// When its schedule's last run that got through started, in seconds
	// since 1970, which $SINCE and $LAST_RUN hold; nil for none.
	Since *int64 `json:"since"`
}

// The run's own limit, if it has one, or the worker's.
func limit(ms int64, fallback time.Duration) time.Duration {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return fallback
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
	// For a script: what the worker logged of running it; see logs.go. An
	// agent's lines are streamed instead.
	Log []logLine `json:"log,omitempty"`
}

// Where files that runs generate are kept, a volume the webapp serves them from.
var filesRoot = envOr("FILES_DIR", "/data/files")

// Where steps' private and scratch directories are made: a volume of its
// own when steps run in containers, which must see them; see sandbox.go.
var stepsRoot = envOr("STEPS_DIR", os.TempDir())

// The prefixes of the directories steps get under stepsRoot.
var stepDirPrefixes = []string{"private-", "script-private-", "run-", "script-"}

// Removes the directories of steps a worker that stopped left behind, which
// may hold their git tokens; steps remove their own as they end. Only in a
// STEPS_DIR of the worker's own, never the system's temporary directory.
func sweepStepDirs() {
	if os.Getenv("STEPS_DIR") == "" {
		return
	}
	entries, _ := os.ReadDir(stepsRoot)
	for _, e := range entries {
		for _, p := range stepDirPrefixes {
			if e.IsDir() && strings.HasPrefix(e.Name(), p) {
				os.RemoveAll(filepath.Join(stepsRoot, e.Name()))
				break
			}
		}
	}
}

// Makes a directory for a step, which only the worker's user reads.
func makeStepDir(prefix string) (string, error) {
	if err := os.MkdirAll(stepsRoot, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(stepsRoot, prefix)
}

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
	return os.MkdirAll(dir, 0o755)
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
	// settings.json are used; credentials come from the environment. Each
	// agent step gets an agent directory of its own made from them; see
	// writeAgentDir.
	hostConfigDir := envOr("PI_HOST_CONFIG_DIR", "/pi-host")

	models, err := readAgentConfig(hostConfigDir)
	if err != nil {
		log.Fatalf("worker: %v", err)
	}
	def := models.Default
	if def == "" {
		def = "(pi default)"
	}
	log.Printf("worker: %d enabled models, default %s", len(models.Models), def)
	hostProviders, err := hostProvidersOf(hostConfigDir, models)
	if err != nil {
		log.Fatalf("worker: %v", err)
	}

	sweepStepDirs()
	if steps, err = newSandbox(os.Getenv("SANDBOX")); err != nil {
		log.Fatalf("worker: %v", err)
	}
	token, err := workerToken()
	if err != nil {
		log.Fatalf("worker: %v", err)
	}
	if token == "" {
		log.Printf("worker: WARNING: WORKER_TOKEN isn't set, so anyone reaching port %s can run bash here", port)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, models)
	})
	// The providers of the worker's pi config, which the webapp reaches for
	// agents; see host_providers.go.
	mux.HandleFunc("GET /host-providers", func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, hostProviders)
	})
	// The providers pi knows that the webapp's settings can add, with their
	// models.
	mux.HandleFunc("GET /catalog", func(w http.ResponseWriter, r *http.Request) {
		c, err := loadCatalog()
		if err != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		reply(w, http.StatusOK, c)
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
		// of the conversation as it ends, {"log": ...} for each line the worker
		// logs of the run, then {"result": ...} with the outcome.
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
		l := newStepLogger(processLabel(run.ProcessID, run.ProcessName), func(line logLine) {
			send(map[string]logLine{"log": line})
		})
		res := runPi(r.Context(), run, limit(run.TimeoutMS, timeout), l, func(m json.RawMessage) {
			send(map[string]json.RawMessage{"message": m})
		})
		logRun(l, run, res, time.Since(started))
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
		l := newStepLogger(processLabel(run.ProcessID, run.ProcessName), nil)
		res := runScript(r.Context(), run, limit(run.TimeoutMS, timeout), l)
		logOutcome(l, "bash", res, time.Since(started))
		res.Log = l.kept()
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

	// Releases the workspaces of runs that are over, keeping their branches
	// and sessions: {"runs": [ids]}; see releaseWorkspaces.
	mux.HandleFunc("POST /workspaces/release", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Runs []int64 `json:"runs"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&body); err != nil {
			reply(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
			return
		}
		released, err := releaseWorkspaces(body.Runs)
		if err != nil {
			reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if released == nil {
			released = []string{}
		}
		reply(w, http.StatusOK, map[string][]string{"released": released})
	})

	log.Printf("worker: listening on %s", port)
	log.Fatal(http.ListenAndServe(":"+port, requireToken(token, mux)))
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

// What every step's agent directory starts from: the host's settings,
// trimmed to those choosing models, and its models.json, if any. Read once
// at startup; restart the worker to pick up config changes.
var agentConfig struct {
	settings, models []byte
}

// Reads the host's pi config into agentConfig, returning the models offered.
func readAgentConfig(hostDir string) (agentModels, error) {
	var models agentModels
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
	if agentConfig.settings, err = json.MarshalIndent(trimmed, "", "  "); err != nil {
		return models, err
	}
	if agentConfig.models, err = readHostJSON(hostDir, "models.json"); err != nil {
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
func runPi(ctx context.Context, run runRequest, timeout time.Duration, l *stepLogger, emit func(json.RawMessage)) (res runResult) {
	session, err := sessionArgs(run)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	// The specs, the result, the git token and pi's agent directory live
	// outside the agent's working directory, in a directory of the step's
	// own, which only the worker's user reads, as every file in it.
	private, err := makeStepDir("private-")
	if err != nil {
		return runResult{Error: "create private directory: " + err.Error()}
	}
	defer os.RemoveAll(private)
	ctx, gitEnv, err := writeGitCredentials(ctx, private, run.Git)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	cwd, ws, err := workDir(ctx, run.Workspace, "run-", l)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	ws.process, ws.session = run.ProcessName, run.SessionID
	// Once its work is brought back.
	defer checkDisk(run.Run, run.DiskLimit, &res, l)
	defer ws.done(&res)

	// An empty model or thinking level leaves the choice to the pi settings.
	// pi prints its events as JSON, so the conversation can be followed.
	args := append(append([]string{"-p", "--mode", "json", "--no-approve"}, session...), modelArgs(run.Model)...)
	if run.Thinking != "" {
		args = append(args, "--thinking", run.Thinking)
	}

	var resultFile string
	for _, p := range run.Providers {
		if err := p.check(); err != nil {
			return runResult{Error: err.Error()}
		}
	}
	agentDir := filepath.Join(private, "agent")
	if err := os.Mkdir(filepath.Join(private, "out"), 0o700); err != nil {
		return runResult{Error: "create private directory: " + err.Error()}
	}
	if err := writeAgentDir(agentDir, run.Providers, run.LLM); err != nil {
		return runResult{Error: err.Error()}
	}
	env := withCloudCredentials(childEnv(append(gitEnv, "PI_CODING_AGENT_DIR="+agentDir)...))
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
	if len(run.Skills) > 0 && !run.NoTools {
		skills, err := writeSkills(private, run.Skills)
		if err != nil {
			return runResult{Error: "write skills: " + err.Error()}
		}
		args = append(args, skills...)
	}
	if len(run.MCPServers) == 0 {
		args = append(args, "--no-mcp")
	}
	if run.NoTools {
		args = append(args, "--no-tools", "--no-extensions", "--no-skills", "--no-context-files", "--no-prompt-templates")
	}
	prompt := run.Prompt
	if run.Resume && len(ws.recreated) > 0 {
		prompt = recreatedNote(ws.recreated) + prompt
	}
	if run.Guide {
		guide, err := os.ReadFile(flowsGuide)
		if err != nil {
			return runResult{Error: "read the guide to authoring flows: " + err.Error()}
		}
		prompt = "<flows_guide>\n" + string(guide) + "\n</flows_guide>\n\n" + prompt
	}

	// The prompt goes in on stdin: as an argument, a leading @ or - would be
	// parsed as a file reference or an option. pi may exit before reading it,
	// e.g. on a bad model; that's reported by its exit status.
	promptFile := filepath.Join(private, "prompt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return runResult{Error: "write the prompt: " + err.Error()}
	}
	spec := sandboxSpec{
		Cmd: append([]string{"pi"}, args...), Stdin: promptFile, Dir: cwd, Env: env, Graceful: true, Egress: run.Egress.proxy(),
		// What the agent writes, besides its working directory: pi's agent
		// directory, its result, its files and its session.
		Mounts: []mount{{Path: cwd}, {Path: private, ReadOnly: true}, {Path: agentDir}, {Path: filepath.Join(private, "out")}},
	}
	if run.FilesDir != "" {
		spec.Mounts = append(spec.Mounts, mount{Path: run.FilesDir})
	}
	if run.SessionID != "" {
		spec.Mounts = append(spec.Mounts, mount{Path: sessionsDir(run.Workspace.ID)})
	}
	if err := spec.withHome(run.Run); err != nil {
		return runResult{Error: "prepare the run's home: " + err.Error()}
	}

	timeout, short := untilTokenEnds(timeout, run.Git)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	events := &eventStream{emit: emit}
	var stderr bytes.Buffer
	// The prompt goes on stdin and the token in models.json, so the
	// arguments hold neither.
	l.infof("pi %s", strings.Join(args, " "))
	err = steps.run(runCtx, spec, events, &stderr)
	if notStarted(err) && runCtx.Err() == nil {
		return runResult{Error: "failed to start pi: " + err.Error()}
	}
	output, failure := events.outcome()
	// A failure's error is what it printed to stderr already.
	if err == nil {
		l.stderr("pi", stderr.String())
	}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return runResult{Output: output, Error: timedOut(timeout, short, run.Git)}
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
		// Missing if the agent never called submit_output, or the extension
		// failed to load. The agent could have left a link to another file.
		if b, err := readNoFollow(resultFile); err == nil {
			return runResult{OK: true, Output: string(b), Submitted: true}
		}
	}
	return runResult{OK: true, Output: output}
}

// Writes each skill as pi reads one, a directory of its name holding its
// SKILL.md, under dir, and returns the arguments giving them to pi. The
// frontmatter's values are JSON strings, which YAML reads as they are, so a
// description holding a colon or a line break can't break it.
func writeSkills(dir string, skills []skill) ([]string, error) {
	var args []string
	for _, s := range skills {
		if !skillName.MatchString(s.Name) {
			return nil, fmt.Errorf("invalid skill name %q", s.Name)
		}
		d := filepath.Join(dir, "skills", s.Name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		name, _ := json.Marshal(s.Name)
		description, _ := json.Marshal(s.Description)
		md := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, description, s.Content)
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(md), 0o600); err != nil {
			return nil, err
		}
		args = append(args, "--skill", d)
	}
	return args, nil
}

// Writes the output spec submit-output.ts reads, returning where its result goes.
func writeOutputSpec(dir string, run runRequest) (string, error) {
	spec := *run.Output
	spec.FilesDir = run.FilesDir
	spec.ResultFile = filepath.Join(dir, "out", "result.json")
	b, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	return spec.ResultFile, os.WriteFile(filepath.Join(dir, "spec.json"), b, 0o600)
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

// Tells an agent carrying on its conversation that its checkouts were made
// again since, so it doesn't count on what only the old ones held.
func recreatedNote(repos []string) string {
	return fmt.Sprintf("Note: since you last worked here, the checkouts of %s were removed to save space and made again from the branch. "+
		"Everything committed is there, including what was left uncommitted, which was committed for you; "+
		"but files git ignores, such as installed dependencies and build output, are gone, as is anything you left outside the repositories. "+
		"Set them up again if you need them.\n\n", strings.Join(repos, ", "))
}

// The directory a step works in, and what gives it back once the step is
// done: its run's workspace, ready and taken, or a scratch directory deleted
// afterwards. Getting the workspace ready doesn't count towards the step's
// timeout.
func workDir(ctx context.Context, ws *workspaceSpec, scratch string, l *stepLogger) (string, *takenWorkspace, error) {
	if ws != nil {
		started := time.Now()
		t, err := takeWorkspace(ctx, ws)
		if err != nil {
			return "", nil, errors.New("prepare workspace: " + err.Error())
		}
		t.log = l
		names := make([]string, len(t.repos))
		for i, r := range t.repos {
			names[i] = r.Name
		}
		msg := fmt.Sprintf("working in the workspace %s on the branch %s, with %s, ready in %dms", ws.ID, ws.Branch, strings.Join(names, ", "), time.Since(started).Milliseconds())
		if len(t.recreated) > 0 {
			msg += "; the checkouts of " + strings.Join(t.recreated, ", ") + " were made again from the branch"
		}
		l.infof("%s", msg)
		return t.dir, t, nil
	}
	dir, err := makeStepDir(scratch)
	if err != nil {
		return "", nil, errors.New("create scratch directory: " + err.Error())
	}
	l.infof("working in a scratch directory")
	return dir, &takenWorkspace{release: func() { os.RemoveAll(dir) }}, nil
}

// Characters kept in an input's file name; others become underscores.
var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Runs a script with bash in its run's workspace, which $WORKSPACE points to
// and whose checkouts are on the branch $BRANCH names, or else in a fresh
// scratch directory. Each object it takes is a file in the directory $INPUTS
// points to, named after the object: spaces and other characters unsafe in a
// file name become underscores, so Jira Ticket is $INPUTS/Jira_Ticket. Files
// it generates go in the directory $OUTPUTS points to, which is kept. In a
// scheduled run, $SINCE and its alias $LAST_RUN hold when the schedule's
// last run that got through started, as a Unix timestamp, and are empty
// when there was none, so the script can fetch only what changed since. What
// the script prints is its output; a non-zero exit fails it, with what it
// printed to stderr as the error. Like pi, it runs in a process group of its own,
// which a cancelled request or the timeout kills.
func runScript(ctx context.Context, run scriptRequest, timeout time.Duration, l *stepLogger) (res runResult) {
	// The inputs, the script and the git token stay out of the working
	// directory, which a workspace shares with other steps, readable only
	// by the worker's user.
	private, err := makeStepDir("script-private-")
	if err != nil {
		return runResult{Error: "create private directory: " + err.Error()}
	}
	defer os.RemoveAll(private)
	ctx, gitEnv, err := writeGitCredentials(ctx, private, run.Git)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	cwd, ws, err := workDir(ctx, run.Workspace, "script-", l)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	ws.process = run.ProcessName
	defer checkDisk(run.Run, run.DiskLimit, &res, l)
	defer ws.done(&res)
	inputs := filepath.Join(private, "inputs")
	if err := os.Mkdir(inputs, 0o700); err != nil {
		return runResult{Error: "create inputs directory: " + err.Error()}
	}
	for _, in := range run.Inputs {
		name := strings.Trim(unsafeName.ReplaceAllString(in.Name, "_"), "._")
		if name == "" {
			name = "input"
		}
		if err := os.WriteFile(filepath.Join(inputs, name), []byte(in.Content), 0o600); err != nil {
			return runResult{Error: "write input " + in.Name + ": " + err.Error()}
		}
	}
	script := filepath.Join(private, "script")
	if err := os.WriteFile(script, []byte(run.Script), 0o600); err != nil {
		return runResult{Error: "write script: " + err.Error()}
	}

	// Set even when empty, so nothing else can stand in.
	since := ""
	if run.Since != nil {
		since = strconv.FormatInt(*run.Since, 10)
	}
	spec := sandboxSpec{Cmd: []string{"bash", script}, Dir: cwd, Mounts: []mount{{Path: cwd}, {Path: private, ReadOnly: true}}, Egress: run.Egress.proxy()}
	spec.Env = childEnv(append(gitEnv, "INPUTS="+inputs, "SINCE="+since, "LAST_RUN="+since)...)
	if run.Workspace != nil {
		spec.Env = append(spec.Env, "WORKSPACE="+cwd, "BRANCH="+run.Workspace.Branch)
	}
	if run.FilesDir != "" {
		spec.Env = append(spec.Env, "OUTPUTS="+run.FilesDir)
		spec.Mounts = append(spec.Mounts, mount{Path: run.FilesDir})
	}
	if err := spec.withHome(run.Run); err != nil {
		return runResult{Error: "prepare the run's home: " + err.Error()}
	}
	stdout := &cappedBuffer{max: maxScriptBytes}
	stderr := &cappedBuffer{max: 64 << 10}

	timeout, short := untilTokenEnds(timeout, run.Git)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if len(run.Inputs) > 0 {
		names := make([]string, len(run.Inputs))
		for i, in := range run.Inputs {
			names[i] = in.Name
		}
		l.infof("bash, given %s", strings.Join(names, ", "))
	} else {
		l.infof("bash, given nothing")
	}
	err = steps.run(runCtx, spec, stdout, stderr)
	if notStarted(err) && runCtx.Err() == nil {
		return runResult{Error: "failed to start bash: " + err.Error()}
	}
	output := strings.TrimSpace(stdout.String())
	// A failure's error is what it printed to stderr already.
	if err == nil {
		l.stderr("the script", stderr.String())
	}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return runResult{Output: output, Error: timedOut(timeout, short, run.Git)}
	case ctx.Err() != nil:
		return runResult{Output: output, Error: "cancelled"}
	case stdout.over:
		return runResult{Error: fmt.Sprintf("the script printed more than %d MB", maxScriptBytes>>20)}
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "the script failed: " + err.Error()
		} else if exit, ok := err.(exitError); ok {
			msg += fmt.Sprintf("\n(exit status %d)", int(exit))
		}
		return runResult{Output: output, Error: msg}
	}
	return runResult{OK: true, Output: output}
}

// Why a step that ran out of time failed: its timeout, or its git token
// ending first.
func timedOut(timeout time.Duration, short bool, git *gitCredentials) string {
	if !short {
		return "timed out after " + timeout.String()
	}
	return fmt.Sprintf("stopped after %s, as its git token ends at %s and the step would have outlived it", timeout.Round(time.Second), git.Expires.Format(time.RFC3339))
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
func logRun(l *stepLogger, run runRequest, res runResult, d time.Duration) {
	model := run.Model
	if model == "" {
		model = "default model"
	}
	logOutcome(l, model, res, d)
}

// The outcome, with what went wrong and what the step committed, goes in the
// step's log too; its output, which the step keeps anyway, only here.
func logOutcome(l *stepLogger, with string, res runResult, d time.Duration) {
	for _, c := range res.Commits {
		switch {
		case len(c.Commits) > 0:
			l.infof("%s: %d commits, %s", c.Name, len(c.Commits), strings.Join(c.Commits, "; "))
		case c.Uncommitted:
			l.infof("%s: nothing committed, changes left uncommitted", c.Name)
		}
	}
	if res.OK {
		l.infof("%s succeeded in %dms", with, d.Milliseconds())
	} else {
		l.errorf("%s failed in %dms: %s", with, d.Milliseconds(), res.Error)
	}
	for _, line := range strings.Split(res.Output, "\n") {
		if res.Output != "" {
			log.Printf("worker: %s output| %s", l.label, line)
		}
	}
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
