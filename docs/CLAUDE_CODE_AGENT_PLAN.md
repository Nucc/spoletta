# Plan: Claude Code as a second coding agent, beside pi

For the engineer (Opus 5.5) implementing it. Read `AGENTS.md` first, then the
files named in §2. Everything below was checked against the code on `main`
at `b487d9d` and against Claude Code 2.1.325 (`claude --help`) and its docs
(code.claude.com) on 2026-11-14. What could not be checked is listed in §4
as a spike to run before writing any production code.

## 1. Goal and scope

An agent process picks which coding agent ("harness") runs it: **pi** (as
today, the default) or **Claude Code**. Everything else about a step stays
as it is: the same prompt, inputs, `submit_output` contract, workspace and
branch, sandbox, model proxy and spending limits, MCP servers through the
webapp's proxy, skills, credentials, egress, follow-ups, re-runs, trials,
take-overs, the conversation in the step's Terminal, the logs.

In scope (v1):
- agent steps, their contract retry and their follow-ups, with Claude Code;
- models of providers that speak Anthropic's Messages API with a key the
  webapp holds: `anthropic`, and custom providers whose API is
  `anthropic-messages` (a gateway).

Out of scope (v1), listed in §10: the flows' assistant (stays pi),
Bedrock and Vertex, non-Claude models, WebSearch, Claude.ai sign-in.

### Constraints that shape the design (decided, don't reopen)

1. **Terms.** Anthropic's legal page: running Claude Code in a product
   "requires agreeing to our Commercial Terms"; "The Claude Code binary must
   not be modified"; "Customers may not pay for, resell, or intermediate
   Claude usage on their end users' behalf"; and no Claude.ai (Free, Pro,
   Max) credentials on behalf of users. The carve-out covers an organisation
   using keys it provisions for its own authorised users, billed to it. So:
   - Claude Code authenticates only with the step's proxy token standing in
     for an API key the instance's admin configured. Never
     `CLAUDE_CODE_OAUTH_TOKEN`, never `/login`, never a mounted
     `~/.claude` of a person.
   - Claude Code is **not installed by default**. The images take a build
     argument `CLAUDE_CODE_VERSION` (empty by default); an operator who
     accepts the terms sets it. The worker offers the harness only when it
     finds the binary. `docs/INSTALL.md` says so, quoting the conditions.
2. **Claude models only.** Anthropic doesn't support Claude Code on
   non-Claude models through a gateway. The UI offers Claude Code only the
   models of Anthropic-API providers; nothing checks the model's name.
3. **No keys in the worker**, as now: the step's token goes to the webapp's
   `/llm/{provider}/` proxy, which swaps it for the key.
4. **One conversation format.** The worker translates Claude Code's
   stream into pi's message shape, so `run_step_messages`, `messages.go`,
   `assistant_runs.go` and `StepTerminal.tsx` keep reading one format.

## 2. How pi is wired today (what to mirror)

| Concern | Where | What happens |
|---|---|---|
| Launch | `worker/main.go` `runPi` | `pi -p --mode json --no-approve …`, prompt on stdin, through `steps.run(sandboxSpec)` |
| Request | `worker/main.go` `runRequest`, `webapp/agent.go` `agentRun` | one JSON shape, sent to `POST /run`, answered as NDJSON `{"message"}`, `{"log"}`, `{"result"}` |
| Models | `worker/providers.go` `writeAgentDir`, `providersModels` | per-step `models.json`, every provider's `baseUrl` = `<llm.URL>/<provider>`, `apiKey` = the token |
| Proxy | `webapp/llm.go` | finds the token in `Authorization: Bearer`, `X-Api-Key` or `X-Goog-Api-Key`, swaps the key in the same header, forwards any path under the provider's base, records usage from the answer |
| Output | `worker/submit-output.ts` | pi extension registering `submit_output`; checks files, patterns, dates; writes `private/out/result.json`; `runResult.Submitted` |
| MCP | `worker/mcp-servers.ts` | registers each server with `Authorization: Bearer <MCPToken>`; pi names tools `mcp__<server>__<tool>` |
| Skills | `worker/main.go` `writeSkills` | a `SKILL.md` per skill, `--skill <dir>` |
| Session | `worker/workspace.go` `sessionArgs`, `sessionsDir` | `--session-dir /data/workspaces/.sessions/<ws> --session-id <32 hex>`; resume globs `*_<id>.jsonl` |
| Stream | `worker/main.go` `eventStream` | keeps each `message_end`'s message, emits it raw; `outcome()` reads the last assistant message |
| Stored | `webapp/messages.go` `trimMessage` | pi's blocks: `text`, `thinking`, `toolCall{id,name,arguments}`, `image`; roles `user`, `assistant`, `toolResult{toolCallId,toolName,isError,details}` |
| Shown | `webapp/frontend/src/StepTerminal.tsx` | `callLine` knows `bash{command}`, `read{path,offset,limit}`, `edit{path,oldText,newText,edits}`, `write{path,content}`, `ls`, `grep`, `find`; `liveChanges` reads `edit` and `write` |
| Retry | `webapp/agent.go` `followContract` | not handed in: resume the session with "call submit_output now" |
| Follow-up | `webapp/followups.go` | `stepSession{ID, Workspace}` from `run_steps.session`, `Resume: true` |
| Env | `worker/env.go` `childEnv` | allowlist; `worker/sandbox_engine.go` sets the egress proxy variables and `NO_PROXY` for the webapp |
| Images | `worker/Containerfile`, `worker/sandbox.Containerfile` | Alpine, Node 22, `npm install -g --ignore-scripts @earendil-works/pi-coding-agent@1.0.4`; steps are uid 1000, read-only root, home is the run's |

## 3. Design

### 3.1 Picking the harness

- `processSettings.Harness string` (`json:"harness,omitempty"`), `""` or
  `"pi"` meaning pi, `"claude"` Claude Code. It lives in `settings`, so
  versions, history, paste and `runBoard` carry it with no schema change.
  Normalise `"pi"` to `""` on write so diffs don't flag it.
- `agentRun.Harness` / `runRequest.Harness` (`json:"harness,omitempty"`).
- `stepSession.Harness`, so a follow-up resumes with the harness that made
  the session, whatever the board says now. A session without it is pi's.
- Setup files: `harness: claude` on an agent process (`setup_export.go`,
  `setup_import.go`, `webapp/SETUP.md`, `docs/FLOWS.md`). The assistant's
  setup file shows it, so it can set it.
- `diff.go` lists it as a field of the process ("runs with"), and
  `stepSettings.ts` writes it out.

### 3.2 Worker: a harness behind one seam

Split `runPi` into what every agent step does and what pi does, without
changing pi's behaviour (the existing tests must pass untouched after the
split, before Claude Code is added; make that its own commit).

- `worker/harness.go`: the common part, `runAgent(ctx, run, timeout, l,
  emit)`: session checks, private directory, git credentials, `workDir`,
  disk check, output spec, credentials' variables, prompt (recreated note,
  guide), `sandboxSpec`, timeout, and the outcome switch. It asks a
  ```go
  type harness interface {
      name() string                                  // "pi", "claude", for logs and errors
      prepare(st *agentStep) (cmd []string, env []string, mounts []mount, err error)
      stream(emit func(json.RawMessage), prompt string) agentStream // io.Writer + outcome()
  }
  ```
  where `agentStep` holds the request, `private`, `cwd`, the result file.
  `POST /run` picks by `run.Harness`; an unknown one, or `claude` when the
  binary is missing, is a 400 saying so.
- `worker/pi.go`: today's pi specifics moved (args, `writeAgentDir`,
  extensions, `--skill`, `eventStream`).
- `worker/claude.go`, `worker/claude_stream.go`: below.
- `run.NoTools` with `claude` is refused (the assistant stays pi).
- `GET /models` gains `"harnesses": ["pi", "claude"]`: `claude` when
  `exec.LookPath("claude")` finds it as the worker starts (both images are
  built with the same argument), which the worker logs.

### 3.3 Claude Code's command

```
claude -p --output-format stream-json --verbose
  --bare
  --permission-mode bypassPermissions
  --model <id>                         # the part after "provider/"
  [--effort low|medium|high|xhigh|max]
  --strict-mcp-config --mcp-config <private>/claude/mcp.json
  --settings <private>/claude/settings.json
  --disallowedTools WebSearch
  --append-system-prompt <text below>
  [--plugin-dir <private>/claude/plugin]         # skills, §3.7
  --session-id <uuid> | --resume <uuid> | --no-session-persistence
```

Prompt on stdin, as pi's, and for the same reason. Notes on each choice:

- `--bare`: no CLAUDE.md discovery, no auto-memory, no hooks, plugins, or
  `.mcp.json` of the repository, no keychain or OAuth; auth is strictly
  `ANTHROPIC_API_KEY`. That matches pi here, which loads no context files
  (the prompt from `withWorkspace` already tells the agent to read
  `AGENTS.md` or `CLAUDE.md`), and keeps a hostile repository's
  `.claude/settings.json` hooks from running. The docs say it will become
  `-p`'s default.
- `bypassPermissions`: nobody answers prompts; the sandbox is the boundary.
  It refuses to run as root; steps are uid 1000 in every sandbox, and the
  worker drops to `flow` under `SANDBOX=process`. Since 2.1.302 a `-p` run
  without a mode denies whatever would prompt, so the mode is required.
- `--disallowedTools WebSearch`: it runs at Anthropic, outside the egress
  allowlist, and is billed per search, which `spend.go` can't price.
  WebFetch stays: it fetches from the step, through the egress proxy.
- `--append-system-prompt`: "You run headless as one step of an automated
  workflow; nobody can answer questions, so decide and go on. When the
  prompt says to call submit_output, the tool is
  `mcp__<output server>__submit_output`: give your result only by calling
  it, as your last action; if it reports a problem, fix it and call it
  again." (pi gets the same from the extension's `promptGuidelines`.) Only
  the second half when the step has an output spec.
- Thinking: `run.Thinking` maps to `--effort`: `off`, `minimal`, `low` to
  `low`; `medium`, `high`, `xhigh`, `max` as they are; empty passes none.

`settings.json` (written per step):
```json
{ "cleanupPeriodDays": 36500, "skipWebFetchPreflight": true }
```
`cleanupPeriodDays` matters: Claude Code deletes transcripts after 30 days
by default, which would silently end follow-ups; the worker deletes them
with the workspace instead (`deleteWorkspaces` already removes
`sessionsDir`).

Environment, over `childEnv(...)` (no `withCloudCredentials`: no AWS here):

| Variable | Value |
|---|---|
| `ANTHROPIC_BASE_URL` | `<llm.URL>/<provider>` (Claude Code appends `/v1/messages`) |
| `ANTHROPIC_API_KEY` | the step's model token (sent as `X-Api-Key`, which the proxy swaps in place; **not** `ANTHROPIC_AUTH_TOKEN`, whose `Bearer` Anthropic's API refuses for API keys) |
| `ANTHROPIC_DEFAULT_HAIKU_MODEL` | the picked model id, so background calls use a model the provider surely has and the price book knows (`CLAUDE_SMALL_MODEL` in the worker's environment overrides it) |
| `CLAUDE_CONFIG_DIR` | see §3.5 |
| `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` | `1` |
| `DISABLE_TELEMETRY`, `DISABLE_ERROR_REPORTING`, `DISABLE_UPDATES` | `1` |
| `ENABLE_CLAUDEAI_MCP_SERVERS` | `false` |
| `CLAUDE_CODE_DISABLE_AUTO_MEMORY` | `1` |

The step fails before starting when `run.LLM` is nil, the model has no
`provider/` part, or the provider isn't in `run.LLM.Providers` ("Claude
Code needs a model of a provider the model proxy reaches").

The worker's log line is `claude <args>` with the system prompt's text cut
out; the token is in the environment and the prompt on stdin, so the
arguments hold no secret. Add the token forms to `hiding` as now
(`run.tokens()` already covers them).

### 3.4 The model proxy (`webapp/llm.go`)

It should work unchanged; prove it with tests rather than assume:
- `POST /llm/anthropic/v1/messages?beta=true` with `X-Api-Key: <token>`:
  key swapped in `X-Api-Key`, `anthropic-beta` and `anthropic-version`
  passed on verbatim, query kept, usage recorded from the SSE.
- `POST …/v1/messages/count_tokens` answers `{"input_tokens": N}` at the top
  level: confirm `usageIn` records nothing for it (it reads only `usage`
  objects). Add a test pinning that, or a path check if it does.
- `HEAD /llm/anthropic/api/hello` and `GET …/v1/models` (Claude Code's
  start-up probes): Go's mux serves HEAD on the GET pattern; confirm they
  pass and record nothing.
- An `out_of_budget` 403: confirm Claude Code ends with an error result
  rather than retrying for minutes (spike, §4); if it retries, also answer
  with `x-should-retry: false`.
- Update the file's header comment: it says "pi" throughout.

Which models Claude Code may use (`webapp/model_providers.go`): a model
`p/id` qualifies when `p` is `anthropic`, or an added provider whose `API`
is `anthropic-messages`, or a host provider (`hostProvider.API`) of that
API. `offeredModels` returns them as `agentModels.Claude []string`
(`json:"claude,omitempty"`) and passes on the worker's `harnesses`.

Default model: a Claude Code process without a model takes the default if
it qualifies; otherwise the step fails with "the default model isn't one
Claude Code can use; pick one for the process", and `problems.go` warns of
it beforehand (§3.9). No second default setting in v1.

### 3.5 Sessions and state

- `CLAUDE_CONFIG_DIR`:
  - with a session: `sessionsDir(ws.ID)/claude`, mounted read-write (the
    parent already is). It holds only transcripts and Claude Code's own
    state; no credential is ever written there.
  - without: `<private>/claude/config`, mounted read-write as pi's agent
    directory is, removed with the step.
- Session id: `--session-id` takes only a UUID, and the webapp's
  `newSessionID` gives 32 hex characters. The worker formats them
  8-4-4-4-12 (`claudeSessionID`); if the spike shows the version and
  variant bits are checked, set them deterministically from the same hex.
  No webapp change, and `safeSessionID` keeps guarding the input.
- Transcripts are at `$CLAUDE_CONFIG_DIR/projects/<cwd, every
  non-alphanumeric a dash>/<uuid>.jsonl`. Resume globs
  `projects/*/<uuid>.jsonl`; none is "the agent's session is gone, so it
  can't be carried on", as pi's. A workspace's directory keeps its path
  when its checkouts are made again, so resume finds the project.
- A step's own run passes `--session-id`; the contract retry and
  follow-ups pass `--resume`. A workspace is taken by one step at a time
  (`takeWorkspace`), so two never write one config directory.
- `docs/SANDBOX.md`'s mount table gains the row.

### 3.6 `submit_output` as an MCP server

Claude Code has `--json-schema`, but it can't check files, patterns or
dates, nor tell the agent which attribute is wrong in the board's words. So
the same tool, served over MCP on stdio:

- `worker/output-spec.ts`: what both share, moved out of
  `submit-output.ts` unchanged: the types, `parts`, `output`,
  `typeProblems`, `fileProblems`, `datetime`, `regExp`. No imports beyond
  `node:`.
- `worker/submit-output.ts`: pi's extension, now importing those; its
  typebox schema building stays as it is.
- `worker/submit-output-mcp.ts`: a dependency-free MCP server (Node 22
  runs erasable TypeScript as it is; no build step, no npm package):
  newline-delimited JSON-RPC on stdin and stdout answering `initialize`,
  `notifications/initialized`, `ping`, `tools/list` and `tools/call`. One
  tool, `submit_output`, description as pi's, `inputSchema` the same
  schema as `toolParameters` built as plain JSON Schema. Claude Code does
  not check arguments against it, so the server does, with a small
  validator for the subset used (object with `properties`, `required`,
  `additionalProperties: false`; array with `minItems`, `maxItems`;
  string, integer, number, boolean; `const`/`anyOf` for states; any), its
  messages naming the path as pi's do. Then `fileProblems` and
  `typeProblems`; problems come back as `isError: true` with the text;
  success writes `resultFile` and answers "Output submitted. You are
  done; reply with one short line and stop." A later call overwrites.
- In `mcp.json`:
  `{"command": "node", "args": ["/opt/flow/submit-output-mcp.ts"], "env": {"FLOW_OUTPUT_SPEC": "<private>/spec.json"}}`
  under a server name the webapp's MCP server names can never be (see the
  name rule in `MCPSettings.tsx` and `mcp.go`; if `flow_output` can
  collide, pick one that can't and reserve it in the webapp's check).
- Parity: `worker/scripts/output-schema.ts` builds both schemas for a set
  of fixture specs (object, items, objects, decisions, nested parts,
  typed attributes, bounds) and compares them as JSON, run by a Go test
  when Node is installed, as `search_grammar_test.go` runs its script. It
  also drives the MCP server over a pipe: a bad call returns the
  problems, a good one writes the file.
- Both Containerfiles copy the two new files to `/opt/flow/`.

`runResult.Submitted` is set as now, from the result file; the webapp's
`followContract` is untouched.

### 3.7 MCP servers and skills

- MCP: each `run.MCPServers` entry becomes
  `{"type": "http", "url": <url>, "headers": {"Authorization": "Bearer <MCPToken>"}}`
  in the same `mcp.json` (0600, in the private directory, as pi's
  `mcp.json` holds the token today). Tools are named
  `mcp__<server>__<tool>` by both harnesses, and the proxy's tool filter
  (`mcp_tools.go`) holds either. `init`'s `mcp_servers` statuses go to the
  step's log; one that failed to connect is an error line naming it.
- Skills: `writeSkills`' directories, placed as a plugin:
  `<private>/claude/plugin/.claude-plugin/plugin.json` (`{"name": "flow"}`)
  and `skills/<name>/SKILL.md`, given with `--plugin-dir`. The spike
  confirms the model sees and uses them under `--bare`; the fallback is
  `--add-dir <dir>` holding `.claude/skills/`.
- Commands are spelled out by the webapp before the worker sees the prompt
  (`expandCommands`), so nothing to do; but Claude Code expands a `/name`
  that **starts** a `-p` prompt as one of its own. A `/name` the flow may
  not use stays as written, so guard it: when the prompt's first
  character is `/`, the worker puts a line before it (spike: confirm a
  leading newline is enough, else a short "Your task:" line).

### 3.8 The conversation, translated (`worker/claude_stream.go`)

One JSON event per line on stdout. Emit pi-shaped messages:

| Claude Code event | Emitted |
|---|---|
| (before any) | `{"role":"user","content":<the prompt>}`, as pi echoes it |
| `system` / `init` | nothing; log the model, the version and the MCP servers' statuses |
| `system` / `api_retry` | a log line (attempt, status, error) |
| `assistant` | `{"role":"assistant","model":…,"stopReason":…,"content":[…]}`: `text` as it is; `thinking` to `{"type":"thinking","thinking":…}`; `redacted_thinking` to `{"type":"thinking","thinking":"","redacted":true}`; `tool_use` to `{"type":"toolCall","id":…,"name":…,"arguments":…}`, normalised below |
| `user` with `tool_result` blocks | one `{"role":"toolResult","toolCallId":…,"toolName":…,"isError":…,"content":[{"type":"text","text":…}]}` each; the name from the call remembered by id; image blocks as `{"type":"image","mimeType":…}` |
| events with `parent_tool_use_id` | dropped (a subagent's inner turns; its result still arrives as the Agent call's tool result) |
| `result` | not a message: the outcome |

Tool names and arguments are normalised to pi's, so the Terminal's
`callLine` and `liveChanges` work as they are:

| Claude Code | pi shape |
|---|---|
| `Bash{command}` | `bash{command}` |
| `Read{file_path, offset, limit}` | `read{path, offset, limit}` |
| `Edit{file_path, old_string, new_string}` | `edit{path, oldText, newText}` |
| `Write{file_path, content}` | `write{path, content}` |
| `Grep{pattern, path}` | `grep{pattern, path}` |
| `Glob{pattern, path}` | `find{pattern, path}` |
| anything else | as it is |

Outcome from `result`: `subtype: "success"` and not `is_error` gives its
`result` text as the output. Otherwise the failure is `errors` joined, else
`result`, else by subtype ("it ran out of turns", "it failed while
running"). `permission_denials`, if any, are warning lines. Log one line
with `num_turns`, `duration_ms` and `total_cost_usd`, called an estimate:
spending is what the proxy recorded, never this. No `result` at all with
exit status 0 is "Claude Code ended without a result"; a non-zero exit
gives stderr, as pi's. SIGTERM (cancel, timeout) leaves no result, which
the existing switch already reports as cancelled or timed out.

`trimMessage` in the webapp needs no change; add a test feeding it a
translated conversation.

### 3.9 Webapp

- `processes.go`: `validHarness` on POST and PATCH; `claude` only on an
  agent process.
- `agent.go` `runAgentStep`: `run.Harness = settings.Harness`; the default
  model rule of §3.4; `stepSession.Harness`. Fix the comments saying pi.
- `followups.go`: the follow-up's `agentRun.Harness` from its step's
  session.
- `problems.go` (warnings, since they are set up outside the board): a
  Claude Code process whose model, or the default it would take, isn't
  one Claude Code can use; one when the worker doesn't offer `claude`
  (`problemFacts` gains the harnesses and the qualifying models; when the
  worker can't be asked, no warning).
- The step's JSON for the run's panel gains `harness` (from the session,
  else from the run's version's settings), so the panel can say "Claude
  Code session" where `Runs.tsx:840` says "pi session".
- `assistant.go`: unchanged; it never sets a harness.
- Trials, re-runs, take-overs (`adopt.go`) go through `runAgentStep` and
  need nothing; check `stepLost` retries keep the harness (they read the
  version's settings).

### 3.10 Frontend

- `api.ts`: `ProcessSettings.harness`, `AgentModels.harnesses`,
  `AgentModels.claude`, the step's `harness`.
- `ProcessPanel.tsx`: for an agent process, "Runs with" pi or Claude Code,
  shown only when the worker offers both, with a `?` help line (`useHelp`)
  saying what differs: Claude Code takes Anthropic-API models only and
  has no web search here. Switching to Claude Code with a model it can't
  use clears the model.
- `ModelPicker.tsx`: for Claude Code, only `models.claude`; the empty
  choice reads "the default" with the default's name, or says the default
  can't be used.
- Thinking picker: for Claude Code the levels are `low`, `medium`, `high`,
  `xhigh`, `max`, labelled "Effort".
- `stepSettings.ts`, `Diff.tsx`, the palette and hints: name the harness
  where the model is named.
- `StepTerminal.tsx`: no change expected; check a recorded Claude Code
  conversation renders (calls, patches under edits, live files).
- Desktop only; statuses keep the `--status-*` colours; errors through
  `ErrorBanner`.

### 3.11 Images and sandbox

- Both Containerfiles:
  ```
  ARG CLAUDE_CODE_VERSION=
  RUN if [ -n "$CLAUDE_CODE_VERSION" ]; then npm install -g @anthropic-ai/claude-code@"$CLAUDE_CODE_VERSION"; fi
  ```
  with the exact form settled by the spike (whether `--ignore-scripts`
  works, whether Alpine needs `libgcc` and `libstdc++`). The package is a
  native binary per platform, musl included; it must not be patched.
  `compose.yaml` and `compose.sandbox.yaml` pass the argument through from
  `.env`, commented with the terms.
- `sandbox_engine.go` and `sandbox_kubernetes.go`: confirm the new
  variables reach the container (the engine filters some names) and that
  the `NO_PROXY` it sets covers the webapp's host, so model calls go
  direct while WebFetch goes through the egress proxy.
- `docs/SANDBOX.md`: the threat model's paragraph on what the agent's own
  process reaches (only the webapp, with the traffic switches above), the
  mounts, and that `bypassPermissions` relies on the container.

## 4. Spike first (half a day, throwaway; record the answers in the PR)

Build the sandbox image with `CLAUDE_CODE_VERSION` set, run it as uid 1000
with a read-only root, home on a tmpfs, and a small local HTTP server that
logs requests standing in for the proxy (then the real Anthropic API with
a key, by hand). Answer:

1. Install: does `npm install -g --ignore-scripts` leave a working
   `claude` on Alpine? Extra packages? Is `USE_BUILTIN_RIPGREP=0` needed?
2. Does it run with a read-only root, writing only `CLAUDE_CONFIG_DIR`,
   `HOME` and `/tmp`? What does it write to `HOME`?
3. With `--bare` and the environment of §3.3, which requests does it make,
   to which paths and hosts, with which auth header? Anything not to the
   base URL?
4. `stream-json`: record a run with Bash, Read, Edit, Write, Grep, Glob, a
   thinking block, an MCP call and a subagent. Check §3.8's table against
   it (is one assistant message split over several events; is the prompt
   echoed; the exact `result` fields). Keep the recording as
   `worker/testdata/claude_stream.jsonl` for the translator's test.
5. `--session-id` with the hex formatted as a UUID: accepted? Then
   `--resume` from the same working directory in a fresh container with
   the same config directory: carried on? Where is the transcript?
6. The stdio MCP server: listed in `init`, called as
   `mcp__<name>__submit_output`, an `isError` answer fixed and retried,
   the run ending soon after success.
7. HTTP MCP with the bearer header against the webapp's `/mcp/{id}` proxy.
8. Skills by `--plugin-dir` under `--bare`: seen and used? How are they
   named?
9. A prompt starting with `/`: what happens, and what guards it.
10. A 403 `out_of_budget` from the proxy: how fast does it give up, with
    which `result`?
11. `--disallowedTools WebSearch` holds under `-p`; WebFetch works through
    `HTTPS_PROXY` with `skipWebFetchPreflight`.
12. SIGTERM mid-run: exits promptly, child processes too, transcript
    resumable.

Where an answer contradicts §3, change the design in the smallest way and
note it; stop and ask only if `--bare`, the proxy routing or resume can't
be made to work.

## 5. Order of work (a commit each, building and passing on its own)

1. Worker: split `runPi` into the common part and pi's (`harness.go`,
   `pi.go`), no behaviour change.
2. Worker: `output-spec.ts` shared, `submit-output-mcp.ts`, the parity
   script and its Go test.
3. Worker: Claude Code (`claude.go`, `claude_stream.go`), `harnesses` in
   `GET /models`, tests (§6); Containerfiles and compose.
4. Webapp: the setting, `agentRun.Harness`, sessions and follow-ups,
   `offeredModels`, problems, setup files, diff; proxy tests.
5. Frontend: the picker, models, effort, the step's label.
6. Docs: `AGENTS.md` (Layout's worker and sandbox lines, `llm.go`,
   `model_keys.go`, `skills.go`), `docs/SANDBOX.md`, `docs/INSTALL.md`,
   `docs/FLOWS.md`, `webapp/SETUP.md`.

Commit subjects as `AGENTS.md` says (one imperative sentence, no prefix),
and no co-author or generator line in any commit.

## 6. Tests

- Worker, Go: a fake `claude` on `PATH` (a shell script printing the
  recorded stream, writing its arguments and environment to a file), as
  the existing tests fake pi. Cover: arguments and environment for a
  first run, a resume, no session; refused without a proxied provider;
  `mcp.json` and `settings.json` contents; the session glob; the
  translator against the recording (each row of both tables, the outcome
  for success, each error subtype, no result); tokens never in the log.
- Worker, Node: §3.6's parity and server script.
- Webapp unit: harness validation, `offeredModels`' `claude`, the problems,
  setup round trip, diff, `trimMessage` on a translated conversation, the
  proxy cases of §3.4.
- E2E (`webapp/tests`): the fake worker records `agentRun.harness` and
  answers `harnesses`; a flow whose process runs with Claude Code sends
  it, with a model of an Anthropic-API provider; its follow-up carries the
  harness and `resume`; a run of a published version keeps the harness it
  was published with after the board switches back; the default-model
  failure reads as written.
- By hand, once: `podman compose` with the sandbox, a real key, a flow
  with a repository: a Claude Code step that edits, commits and hands in
  typed output; a follow-up; a spending limit hit; the same flow with pi
  beside it in one run.

## 7. Acceptance

- A flow can run pi and Claude Code processes in one run, on one branch.
- With `CLAUDE_CODE_VERSION` unset nothing changes anywhere: no picker, no
  new traffic, pi's tests untouched.
- A Claude Code step's spend appears in its panel and counts toward the
  run's and the person's limits, from the proxy's records.
- Its container reaches only the webapp (verify with the egress and audit
  logs); no key, OAuth token or person's config is in it.
- Its conversation, live file changes, log, changes, follow-ups, re-run and
  trial behave as a pi step's.

## 8. Risks

- **Stream format drift.** It is documented only through the Agent SDK's
  types. Pin the version, keep the recording as a test, and have the
  translator pass unknown events by rather than fail.
- **`--bare` changes.** If a release moves what it skips, skills or the MCP
  config could go missing; the tests of step 3 run the real binary in CI
  only if the image is built with it, so add one CI job that builds the
  sandbox image with the pinned version and runs `claude --version` and
  the stdio MCP handshake offline.
- **Background model calls** raising cost on gateways: watched through
  `llm_usage` by model in the by-hand test; `CLAUDE_SMALL_MODEL` is the
  lever.
- **Terms.** The opt-in build argument and API-key-only auth are the
  mitigation; whether a hosted, multi-customer offering of Spoletta may
  include it is the owner's to settle with Anthropic, not the code's.

## 9. What not to do

- Don't wrap the Agent SDK in a Node script: it spawns the same binary and
  adds an npm dependency for nothing the CLI lacks here.
- Don't store Claude Code's messages in their own format, or teach the
  frontend a second one.
- Don't give Claude Code the worker's AWS credentials, a person's
  `~/.claude`, or any Anthropic key.
- Don't turn on Claude Code's own bubblewrap sandbox inside the step's
  container.
- Don't add a plugin system for harnesses: two, behind one Go interface.

## 10. Later, separately

- Bedrock with a Bedrock API key: `CLAUDE_CODE_USE_BEDROCK=1`,
  `ANTHROPIC_BEDROCK_BASE_URL=<llm.URL>/amazon-bedrock`,
  `AWS_BEARER_TOKEN_BEDROCK=<token>` (a bearer the proxy already swaps).
- The assistant on Claude Code (`--tools ""`, the guide in the prompt).
- WebSearch, once its price can be recorded.
- A default model per harness.
