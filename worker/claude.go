package main

// Claude Code, the second harness (npm @anthropic-ai/claude-code), which
// the images hold only when an operator who accepts Anthropic's terms built
// them with CLAUDE_CODE_VERSION; the worker offers it when it finds the
// binary as it starts. It runs as `claude -p --output-format stream-json`,
// printing its events as JSON, which claude_stream.go turns into pi's
// message shape, so the webapp keeps one conversation format.
//
// It reaches its model only through the webapp's proxy, with the step's
// token standing in for an API key the instance's admin configured: never a
// Claude.ai sign-in, an OAuth token, or a person's ~/.claude. So it takes
// only models of a provider the proxy reaches for the step that speaks
// Anthropic's Messages API, the token as its API key, or Claude's models on
// Amazon Bedrock (or a provider of Bedrock's API, such as a gateway), the
// token as a Bedrock API key; on Bedrock it also lists the inference
// profiles and calls two models of its own once with a token each as it
// starts.
//
// What it reads of its own setup is kept to what the worker writes for the
// step: no user, project or local settings (--setting-sources ""), so a
// repository's .claude/settings.json hooks never run and its CLAUDE.md,
// skills and agents aren't loaded (the prompt tells the agent to read
// AGENTS.md or CLAUDE.md itself, as pi's), no MCP servers but the step's
// (--strict-mcp-config), no auto-memory, no traffic beyond its model's.
// --bare would do much of this too, but it also leaves the agent only
// Bash, Read and Edit. Its tools are an allowlist: those that work in a
// step that runs once, unattended, with no subagents, which this version
// runs in the background, nor web search, which runs at Anthropic outside
// the step's egress allowlist and is billed in a way the webapp can't
// price. It runs with bypassPermissions, as nobody answers its prompts:
// the sandbox is the boundary.
//
// Its config directory keeps its transcripts and its own state, never a
// credential: in a workspace, beside pi's sessions, one per session, so a
// follow-up or the contract retry carries the conversation on and no
// session's state reaches another's; without one, in the step's private
// directory, gone with it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Whether the worker found Claude Code as it started; see claudeInstalled.
var claudeInstalled bool

// Finds Claude Code, which both images have or neither, as they are built
// with the same CLAUDE_CODE_VERSION.
func findClaude() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// The MCP server serving submit_output to Claude Code, which the image puts
// here, and the name it goes by: a name the webapp keeps its own MCP servers
// from, so a tool of theirs can't stand in for it.
var outputMCPServer = envOr("OUTPUT_MCP_SERVER", "/opt/flow/submit-output-mcp.ts")

const outputServerName = "flow_output"

// The worker's own pi config's providers, as read as it started, which say
// the API a provider speaks when the run's added providers don't, for a
// step inheriting the instance's configuration.
var ownProviders []hostProvider

// Whether Claude Code speaks Bedrock's API to the provider: Amazon Bedrock,
// or a provider of its API; else Anthropic's.
func claudeBedrock(run runRequest, provider string) bool {
	api, found := "", false
	for _, p := range run.Providers {
		if p.ID == provider {
			api, found = p.API, true
		}
	}
	if run.inherits() {
		for _, p := range ownProviders {
			if p.ID == provider && !found {
				api = p.API
			}
		}
	}
	return api == "bedrock-converse-stream" || api == "" && provider == "amazon-bedrock"
}

// The region Claude Code is told Bedrock is in, which it needs though it
// reaches it through the proxy: the worker's AWS_REGION, for a step
// inheriting the instance's configuration.
func bedrockRegion(inherits bool) string {
	if !inherits {
		return "us-east-1"
	}
	return envOr("AWS_REGION", "us-east-1")
}

// What Claude Code's tools are named for MCP servers: theirs and the
// webapp's names agree, so the proxy's tool filter holds either harness.
var claudeMCPName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// The tools a step's Claude Code has: what a coding agent works with in a
// step that runs once and unattended. Skill joins them when the step has
// skills.
var claudeTools = []string{"Bash", "Read", "Edit", "Write", "WebFetch", "NotebookEdit"}

// pi's thinking levels as Claude Code's efforts: it has no off or minimal.
var claudeEfforts = map[string]string{
	"off": "low", "minimal": "low", "low": "low",
	"medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max",
}

// What Claude Code is told beside its own system prompt.
const claudeHeadless = "You run headless as one step of an automated workflow; nobody can answer questions, so decide and go on."

func claudeOutputNote() string {
	return " When the prompt says to call submit_output, the tool is mcp__" + outputServerName + "__submit_output: " +
		"give your result only by calling it, as your last action; if it reports a problem, fix it and call it again."
}

type claudeHarness struct{}

func (claudeHarness) name() string { return "Claude Code" }

// Claude Code takes a UUID as a session's id, and the webapp's are 32 hex
// characters, which are written as one: Claude Code checks only the form.
func claudeSessionID(id string) (string, bool) {
	if len(id) != 32 || strings.Trim(strings.ToLower(id), "0123456789abcdef") != "" {
		return "", false
	}
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:], true
}

// The config directory a session's Claude Code keeps its transcript and
// state in, beside pi's sessions of the workspace.
func claudeSessionDir(workspaceID, uuid string) string {
	return filepath.Join(sessionsDir(workspaceID), "claude", uuid)
}

// The options keeping the step's conversation in its session, or keeping
// none. Carrying on a session that isn't there fails, rather than starting
// one that knows nothing.
func (claudeHarness) session(run runRequest) ([]string, error) {
	if run.SessionID == "" {
		if run.Resume {
			return nil, errors.New("there is no session to carry on")
		}
		return []string{"--no-session-persistence"}, nil
	}
	if run.Workspace == nil || !safeID.MatchString(run.Workspace.ID) {
		return nil, errors.New("a session needs a workspace")
	}
	uuid, ok := claudeSessionID(run.SessionID)
	if !ok || !safeSessionID.MatchString(run.SessionID) {
		return nil, fmt.Errorf("invalid session %q", run.SessionID)
	}
	dir := claudeSessionDir(run.Workspace.ID, uuid)
	if run.Resume {
		// Claude Code keeps a session as projects/<its directory, dashed>/<id>.jsonl.
		matches, err := filepath.Glob(filepath.Join(dir, "projects", "*", uuid+".jsonl"))
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, errors.New("the agent's session is gone, so it can't be carried on")
		}
		return []string{"--resume", uuid}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return []string{"--session-id", uuid}, nil
}

func (claudeHarness) prepare(st *agentStep) (agentCommand, error) {
	run := st.run
	provider, id, bedrock, err := claudeModel(run)
	if err != nil {
		return agentCommand{}, err
	}
	dir := filepath.Join(st.private, "claude")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return agentCommand{}, fmt.Errorf("create the private directory: %w", err)
	}

	servers := map[string]any{}
	for _, s := range run.MCPServers {
		if !claudeMCPName.MatchString(s.Name) || strings.EqualFold(s.Name, outputServerName) {
			return agentCommand{}, fmt.Errorf("the MCP server %q can't be given to Claude Code by that name", s.Name)
		}
		servers[s.Name] = map[string]any{"type": "http", "url": s.URL, "headers": map[string]string{"Authorization": "Bearer " + run.MCPToken}}
	}
	if run.Output != nil {
		servers[outputServerName] = map[string]any{"type": "stdio", "command": "node", "args": []string{outputMCPServer},
			"env": map[string]string{"FLOW_OUTPUT_SPEC": filepath.Join(st.private, "spec.json")}}
	}
	// The MCP servers' token is in it, as pi's mcp.json holds it.
	mcpConfig := filepath.Join(dir, "mcp.json")
	if err := writeJSON(mcpConfig, map[string]any{"mcpServers": servers}); err != nil {
		return agentCommand{}, fmt.Errorf("write MCP servers: %w", err)
	}
	// Transcripts are kept as long as the workspace, which deletes them;
	// Claude Code would delete them after 30 days, ending follow-ups. No
	// co-author lines in the agent's commits, which are the run's, with the
	// step in their trailers, as pi's are.
	settings := filepath.Join(dir, "settings.json")
	if err := writeJSON(settings, map[string]any{
		"cleanupPeriodDays":     36500,
		"skipWebFetchPreflight": true,
		"attribution":           map[string]string{"commit": "", "pr": ""},
	}); err != nil {
		return agentCommand{}, fmt.Errorf("write settings: %w", err)
	}

	tools := slices.Clone(claudeTools)
	system := claudeHeadless
	if run.Output != nil {
		system += claudeOutputNote()
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose",
		"--setting-sources", "",
		"--permission-mode", "bypassPermissions",
		"--model", id,
	}
	if effort := claudeEfforts[run.Thinking]; effort != "" {
		args = append(args, "--effort", effort)
	}
	if len(run.Skills) > 0 {
		plugin := filepath.Join(dir, "plugin")
		if err := writeSkillsPlugin(plugin, run.Skills); err != nil {
			return agentCommand{}, fmt.Errorf("write skills: %w", err)
		}
		args = append(args, "--plugin-dir", plugin)
		tools = append(tools, "Skill")
	}
	args = append(args, "--tools", strings.Join(tools, ","), "--disallowedTools", "WebSearch",
		"--strict-mcp-config", "--mcp-config", mcpConfig,
		"--settings", settings,
		"--append-system-prompt", system)
	args = append(args, st.session...)

	// Its config directory: the session's, or the step's own.
	config := filepath.Join(dir, "config")
	if run.SessionID != "" {
		uuid, _ := claudeSessionID(run.SessionID)
		config = claudeSessionDir(run.Workspace.ID, uuid)
	} else if err := os.Mkdir(config, 0o700); err != nil {
		return agentCommand{}, fmt.Errorf("create the private directory: %w", err)
	}

	small := id
	if m := os.Getenv("CLAUDE_SMALL_MODEL"); m != "" && run.inherits() {
		small = m
	}
	base := strings.TrimSuffix(run.LLM.URL, "/") + "/" + provider
	signIn := []string{
		"ANTHROPIC_BASE_URL=" + base,
		// Sent as x-api-key, which the proxy swaps in place; a bearer token
		// (ANTHROPIC_AUTH_TOKEN) is what Anthropic's API refuses an API key as.
		"ANTHROPIC_API_KEY=" + run.LLM.Token,
	}
	if bedrock {
		signIn = []string{
			"CLAUDE_CODE_USE_BEDROCK=1",
			"ANTHROPIC_BEDROCK_BASE_URL=" + base,
			// A Bedrock API key, sent as a bearer token, which the proxy
			// swaps in place.
			"AWS_BEARER_TOKEN_BEDROCK=" + run.LLM.Token,
			"AWS_REGION=" + bedrockRegion(run.inherits()),
		}
	}
	vars := append(signIn,
		// What it calls a small, fast model for, such as summing up, goes to
		// the model picked, which the provider surely has and the price
		// book knows, unless the worker says otherwise.
		"ANTHROPIC_DEFAULT_HAIKU_MODEL="+small,
		"CLAUDE_CONFIG_DIR="+config,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_UPDATES=1",
		"ENABLE_CLAUDEAI_MCP_SERVERS=false",
		// Otherwise MCP tools, submit_output among them, are only found by
		// searching for them, which the agent may not do.
		"ENABLE_TOOL_SEARCH=false",
	)
	env := claudeEnv(stepEnv(run.inherits(), append(slices.Clone(st.env), vars...)...), vars, bedrock)

	// Claude Code reads a prompt starting with /name as one of its skills
	// or commands. The webapp spelled out the flow's commands already, so
	// one left is meant as written, which a line before it keeps.
	if strings.HasPrefix(st.prompt, "/") {
		st.prompt = "\n" + st.prompt
	}
	return agentCommand{
		cmd: append([]string{"claude"}, args...), env: env,
		mounts: []mount{{Path: config}},
		shown:  "claude " + shownArgs(args),
	}, nil
}

func (claudeHarness) stream(st *agentStep, emit func(json.RawMessage)) agentStream {
	// It says it signed in with the API key, or on Bedrock with none of
	// Anthropic's.
	signIn := "ANTHROPIC_API_KEY"
	if provider, _, _ := strings.Cut(st.run.Model, "/"); claudeBedrock(st.run, provider) {
		signIn = "none"
	}
	return &claudeStream{emit: emit, log: st.log, prompt: strings.TrimPrefix(st.prompt, "\n"), names: map[string]string{}, signIn: signIn}
}

// The model's provider and its ID with that provider: a provider the
// model proxy reaches for the step, which Claude Code's base URL points at,
// and whether it speaks Bedrock's API to it, on which it runs only Claude.
func claudeModel(run runRequest) (provider, id string, bedrock bool, err error) {
	if run.Model == "" {
		return "", "", false, errors.New("Claude Code needs a model: pick one for the process")
	}
	provider, id, ok := strings.Cut(run.Model, "/")
	if !ok || provider == "" || id == "" || run.LLM == nil || !slices.Contains(run.LLM.Providers, provider) {
		return "", "", false, errors.New("Claude Code needs a model of a provider the model proxy reaches")
	}
	if err := run.LLM.check(); err != nil {
		return "", "", false, err
	}
	if strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", "", false, fmt.Errorf("invalid model %q", run.Model)
	}
	bedrock = claudeBedrock(run, provider)
	if bedrock && !strings.Contains(id, "anthropic.") {
		return "", "", false, fmt.Errorf("Claude Code runs only Claude's models on Bedrock, which %s isn't", run.Model)
	}
	return provider, id, bedrock, nil
}

// The environment without what could sign Claude Code in otherwise than
// with the step's token, should the worker's CHILD_ENV pass one on: every
// ANTHROPIC_ and CLAUDE_ variable but those set for the step, and on
// Bedrock every AWS_ one too, so it signs in with the step's Bedrock API
// key and no AWS credentials.
func claudeEnv(env, ours []string, bedrock bool) []string {
	set := map[string]bool{}
	for _, kv := range ours {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	return slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		theirs := strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_") || bedrock && strings.HasPrefix(name, "AWS_")
		return theirs && !set[name]
	})
}

// Writes the skills as a plugin of Claude Code's, named flow, whose skills
// the agent sees as flow:<name>.
func writeSkillsPlugin(dir string, skills []skill) error {
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o700); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, ".claude-plugin", "plugin.json"), map[string]string{"name": "flow"}); err != nil {
		return err
	}
	_, err := writeSkills(dir, skills)
	return err
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// The arguments as the step's log shows them: the system prompt's text
// left out, an empty one quoted.
func shownArgs(args []string) string {
	shown := make([]string, len(args))
	for i, a := range args {
		switch {
		case i > 0 && args[i-1] == "--append-system-prompt":
			shown[i] = "<…>"
		case a == "":
			shown[i] = `""`
		default:
			shown[i] = a
		}
	}
	return strings.Join(shown, " ")
}
