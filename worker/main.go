// Runs pi agent harnesses on behalf of the webapp. Each POST /run spawns one
// non-interactive pi process in a fresh scratch directory and replies once it exits.
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
}

type runResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
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

		started := time.Now()
		res := runPi(r.Context(), run, timeout)
		logRun(run, res, time.Since(started))
		if r.Context().Err() == nil {
			reply(w, http.StatusOK, res)
		}
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
// run that exceeds the timeout kills pi.
func runPi(ctx context.Context, run runRequest, timeout time.Duration) runResult {
	cwd, err := os.MkdirTemp("", "run-")
	if err != nil {
		return runResult{Error: "create scratch directory: " + err.Error()}
	}
	defer os.RemoveAll(cwd)

	// An empty model or thinking level leaves the choice to the pi settings.
	args := append([]string{"-p", "--no-session", "--no-approve"}, modelArgs(run.Model)...)
	if run.Thinking != "" {
		args = append(args, "--thinking", run.Thinking)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "pi", args...)
	cmd.Dir = cwd
	// pi gets its own process group so stopping it also stops the tools it started.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	// Don't wait forever on a pi that ignores SIGTERM or leaves children holding its output.
	cmd.WaitDelay = 10 * time.Second
	// The prompt goes in on stdin: as an argument, a leading @ or - would be
	// parsed as a file reference or an option. pi may exit before reading it,
	// e.g. on a bad model; that's reported by its exit status.
	cmd.Stdin = strings.NewReader(run.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return runResult{Error: "failed to start pi: " + err.Error()}
	}
	err = cmd.Wait()
	output := strings.TrimSpace(stdout.String())
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
	}
	return runResult{OK: true, Output: output}
}

// Logs the outcome followed by the agent's output (or error), each line
// prefixed so multi-line output stays attributable when runs interleave.
func logRun(run runRequest, res runResult, d time.Duration) {
	label := "run"
	if run.ProcessID != 0 {
		label = fmt.Sprintf("process %d", run.ProcessID)
		if run.ProcessName != "" {
			label += fmt.Sprintf(" %q", run.ProcessName)
		}
	}
	model := run.Model
	if model == "" {
		model = "default model"
	}
	outcome := "failed"
	if res.OK {
		outcome = "succeeded"
	}
	log.Printf("worker: %s with %s %s in %dms", label, model, outcome, d.Milliseconds())
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
