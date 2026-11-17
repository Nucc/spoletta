package main

// pi, the default harness (npm @earendil-works/pi-coding-agent): run as
// `pi -p --mode json`, it prints its events as JSON, each message of the
// conversation in the shape the webapp stores and shows. Its models come
// from the step's own agent directory (see providers.go), its MCP servers
// and submit_output from the extensions the image keeps beside the worker
// (mcp-servers.ts, submit-output.ts), its skills from a SKILL.md each.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The submit_output extension, which the image puts here.
var outputExtension = envOr("OUTPUT_EXTENSION", "/opt/flow/submit-output.ts")

// The extension that registers a run's MCP servers, which the image puts here.
var mcpExtension = envOr("MCP_EXTENSION", "/opt/flow/mcp-servers.ts")

type piHarness struct{}

func (piHarness) name() string { return "pi" }

func (piHarness) session(run runRequest) ([]string, error) { return sessionArgs(run) }

func (piHarness) prepare(st *agentStep) (agentCommand, error) {
	run := st.run
	// An empty model or thinking level leaves the choice to the pi settings.
	// pi prints its events as JSON, so the conversation can be followed.
	args := append(append([]string{"-p", "--mode", "json", "--no-approve"}, st.session...), modelArgs(run.Model)...)
	if run.Thinking != "" {
		args = append(args, "--thinking", run.Thinking)
	}
	agentDir := filepath.Join(st.private, "agent")
	inherits := run.inherits()
	if err := writeAgentDir(agentDir, run.Providers, run.LLM, inherits); err != nil {
		return agentCommand{}, err
	}
	env := stepEnv(inherits, append(st.env, "PI_CODING_AGENT_DIR="+agentDir)...)
	// Bedrock without a key signs in with the worker's AWS credentials,
	// which are the instance's.
	if inherits {
		env = withCloudCredentials(env)
	}
	if len(run.MCPServers) > 0 {
		b, err := json.Marshal(map[string]any{"servers": run.MCPServers, "token": run.MCPToken})
		if err == nil {
			err = os.WriteFile(filepath.Join(st.private, "mcp.json"), b, 0o600)
		}
		if err != nil {
			return agentCommand{}, fmt.Errorf("write MCP servers: %w", err)
		}
		args = append(args, "-e", mcpExtension)
		env = append(env, "FLOW_MCP_SERVERS="+filepath.Join(st.private, "mcp.json"))
	}
	if run.Output != nil {
		args = append(args, "-e", outputExtension)
		env = append(env, "FLOW_OUTPUT_SPEC="+filepath.Join(st.private, "spec.json"))
	}
	if len(run.Skills) > 0 && !run.NoTools {
		skills, err := writeSkills(st.private, run.Skills)
		if err != nil {
			return agentCommand{}, fmt.Errorf("write skills: %w", err)
		}
		args = append(args, skills...)
	}
	if len(run.MCPServers) == 0 {
		args = append(args, "--no-mcp")
	}
	if run.NoTools {
		args = append(args, "--no-tools", "--no-extensions", "--no-skills", "--no-context-files", "--no-prompt-templates")
	}
	// pi's agent directory, and the session it keeps the conversation in.
	mounts := []mount{{Path: agentDir}}
	if run.SessionID != "" {
		mounts = append(mounts, mount{Path: sessionsDir(run.Workspace.ID)})
	}
	// The prompt goes on stdin and the token in models.json, so the
	// arguments hold neither.
	return agentCommand{cmd: append([]string{"pi"}, args...), env: env, mounts: mounts, shown: "pi " + strings.Join(args, " ")}, nil
}

func (piHarness) stream(_ *agentStep, emit func(json.RawMessage)) agentStream {
	return &eventStream{emit: emit}
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

// Runs an agent step with pi.
func runPi(ctx context.Context, run runRequest, timeout time.Duration, l *stepLogger, emit func(json.RawMessage)) runResult {
	return runAgent(ctx, piHarness{}, run, timeout, l, emit)
}
