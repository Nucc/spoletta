package main

// An agent step runs one coding agent, its harness: pi, the default, or
// Claude Code (see pi.go and claude.go). What every agent step does is
// here, whichever harness runs it: its private directory, its git token,
// the directory it works in and what it left there brought back, its
// credentials, its output spec, the prompt, the sandbox it runs in, its
// timeout, and how it ended. A harness says only how its program is
// started, what it is given beside that, and how to read what it prints:
// the conversation, in pi's message shape whichever harness it was, and the
// outcome.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type harness interface {
	// What the step's log and errors call it.
	name() string
	// The options keeping the step's conversation in its session, or
	// keeping none; checked before anything is made for the step, so
	// carrying on a session that is gone fails at once.
	session(run runRequest) ([]string, error)
	// How the program is started for the step, and what it is given beside
	// what every agent step is.
	prepare(st *agentStep) (agentCommand, error)
	// Reads what the program prints, sending each message of the
	// conversation to emit as it ends.
	stream(st *agentStep, emit func(json.RawMessage)) agentStream
}

// What a harness reads its program's output with: the conversation as it
// goes, and once it ended what it printed as text mode would, or why it
// failed though it exited fine.
type agentStream interface {
	io.Writer
	outcome() (output, failure string)
}

// An agent step, as the harness gets it ready.
type agentStep struct {
	run runRequest
	// The step's own directory, out of the agent's working directory, which
	// the sandbox mounts read-only; only the worker's user reads it.
	private string
	// Where the agent works: its run's workspace, or a scratch directory.
	cwd string
	// What the harness's session options are, from session.
	session []string
	// Where submit_output writes what the agent hands in, if the process
	// generates objects; its spec is spec.json in the private directory.
	resultFile string
	// What every agent step's environment holds beside the worker's
	// variables: its git token's and its credentials'.
	env []string
	// What goes on the program's stdin, which prepare may change.
	prompt string
	log    *stepLogger
}

// How a harness starts its program for a step.
type agentCommand struct {
	cmd []string
	// The program's whole environment.
	env []string
	// What it writes, beyond the working directory, the private directory
	// read-only, its output's directory, its files and the run's home.
	mounts []mount
	// The command as the step's log shows it.
	shown string
}

// The harness a request names: pi by default, or Claude Code when the
// worker has it.
func harnessFor(run runRequest) (harness, error) {
	switch run.Harness {
	case "", "pi":
		return piHarness{}, nil
	case "claude":
		if !claudeInstalled {
			return nil, errors.New("this worker doesn't have Claude Code: its images were built without CLAUDE_CODE_VERSION")
		}
		if run.NoTools {
			return nil, errors.New("Claude Code doesn't run without tools; the assistant runs with pi")
		}
		return claudeHarness{}, nil
	}
	return nil, fmt.Errorf("unknown harness %q", run.Harness)
}

// Returns the run outcome; a client that disconnects (ctx is cancelled) or a
// run that exceeds the timeout stops the agent. emit gets each message of
// the conversation as it ends.
func runAgent(ctx context.Context, h harness, run runRequest, timeout time.Duration, l *stepLogger, emit func(json.RawMessage)) (res runResult) {
	session, err := h.session(run)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	// The specs, the result, the git token and the harness's own files live
	// outside the agent's working directory, in a directory of the step's
	// own, which only the worker's user reads, as every file in it.
	private, err := makeStepDir("private-")
	if err != nil {
		return runResult{Error: "create private directory: " + err.Error()}
	}
	defer os.RemoveAll(private)
	ctx, gitEnv, err := writeGitCredentials(withInheritance(ctx, run.inherits()), private, run.Git)
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

	for _, p := range run.Providers {
		if err := p.check(); err != nil {
			return runResult{Error: err.Error()}
		}
	}
	out := filepath.Join(private, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		return runResult{Error: "create private directory: " + err.Error()}
	}
	credVars, err := credentialVars(run.Credentials, run.CredentialsToken)
	if err != nil {
		return runResult{Error: err.Error()}
	}
	if len(credVars) > 0 {
		l.infof("calling %s through the webapp's credential proxy", credentialNames(run.Credentials))
	}
	st := &agentStep{run: run, private: private, cwd: cwd, session: session, env: append(gitEnv, credVars...), log: l}
	if run.Output != nil {
		if st.resultFile, err = writeOutputSpec(private, run); err != nil {
			return runResult{Error: "write output spec: " + err.Error()}
		}
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
	st.prompt = prompt
	c, err := h.prepare(st)
	if err != nil {
		return runResult{Error: err.Error()}
	}

	// The prompt goes in on stdin: as an argument, a leading @ or - would be
	// parsed as a file reference or an option. The agent may exit before
	// reading it, e.g. on a bad model; that's reported by its exit status.
	promptFile := filepath.Join(private, "prompt")
	if err := os.WriteFile(promptFile, []byte(st.prompt), 0o600); err != nil {
		return runResult{Error: "write the prompt: " + err.Error()}
	}
	spec := sandboxSpec{
		Cmd: c.cmd, Stdin: promptFile, Dir: cwd, Env: c.env, Graceful: true, Egress: run.Egress.proxy(),
		// What the agent writes, besides its working directory: its result,
		// its files, and what its harness keeps, such as its session.
		Mounts: append([]mount{{Path: cwd}, {Path: private, ReadOnly: true}, {Path: out}}, c.mounts...),
	}
	if run.FilesDir != "" {
		spec.Mounts = append(spec.Mounts, mount{Path: run.FilesDir})
	}
	if err := spec.withHome(run.Run); err != nil {
		return runResult{Error: "prepare the run's home: " + err.Error()}
	}

	timeout, short := untilTokenEnds(timeout, run.Git)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	events := h.stream(st, emit)
	var stderr bytes.Buffer
	l.infof("%s", c.shown)
	err = steps.run(runCtx, spec, events, &stderr)
	if notStarted(err) && runCtx.Err() == nil {
		return runResult{Error: "failed to start " + h.name() + ": " + err.Error()}
	}
	output, failure := events.outcome()
	// A failure's error is what it printed to stderr already.
	if err == nil {
		l.stderr(h.name(), stderr.String())
	}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return runResult{Output: output, Error: timedOut(timeout, short, run.Git)}
	case ctx.Err() != nil:
		return runResult{Output: output, Error: "cancelled"}
	case err != nil:
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = h.name() + " failed: " + err.Error()
		}
		return runResult{Output: output, Error: msg}
	case failure != "":
		// An agent may exit fine even when the model's request failed.
		return runResult{Output: output, Error: failure}
	}
	if st.resultFile != "" {
		// Missing if the agent never called submit_output, or its tool
		// failed to load. The agent could have left a link to another file.
		if b, err := readNoFollow(st.resultFile); err == nil {
			return runResult{OK: true, Output: string(b), Submitted: true}
		}
	}
	return runResult{OK: true, Output: output}
}

// Writes each skill as a directory of its name holding its SKILL.md, under
// dir/skills, which is how pi and Claude Code's plugins both read one, and
// returns the arguments giving them to pi. The frontmatter's values are
// JSON strings, which YAML reads as they are, so a description holding a
// colon or a line break can't break it.
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

// Writes the output spec submit_output reads, returning where its result goes.
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

// Tells an agent carrying on its conversation that its checkouts were made
// again since, so it doesn't count on what only the old ones held.
func recreatedNote(repos []string) string {
	return fmt.Sprintf("Note: since you last worked here, the checkouts of %s were removed to save space and made again from the branch. "+
		"Everything committed is there, including what was left uncommitted, which was committed for you; "+
		"but files git ignores, such as installed dependencies and build output, are gone, as is anything you left outside the repositories. "+
		"Set them up again if you need them.\n\n", strings.Join(repos, ", "))
}
