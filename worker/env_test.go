package main

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// A script sees only the variables every step gets and its own, never what
// else the worker's environment holds, such as the git token or a model's key.
func TestScriptsGetOnlyTheirEnvironment(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	t.Setenv("GIT_TOKEN", "secret-git")
	t.Setenv("ANTHROPIC_API_KEY", "secret-model")
	t.Setenv("SENTRY_AUTH_TOKEN", "secret-sentry")
	t.Setenv("JIRA_API_TOKEN", "secret-jira")
	t.Setenv("SHARED_TOKEN", "meant-for-scripts")
	t.Setenv("CHILD_ENV", "SHARED_TOKEN, not a name")
	keyNames["ANTHROPIC_API_KEY"] = true
	t.Cleanup(func() { delete(keyNames, "ANTHROPIC_API_KEY") })

	res := runScript(context.Background(), scriptRequest{Script: "env", Inputs: []scriptInput{{Name: "Ticket", Content: "x"}}}, time.Minute, newStepLogger("test", nil))
	if !res.OK {
		t.Fatalf("script failed: %s", res.Error)
	}
	allowed := append(slices.Clone(childVars), "GIT_TERMINAL_PROMPT", "SHARED_TOKEN",
		"INPUTS", "SINCE", "LAST_RUN", "WORKSPACE", "BRANCH", "OUTPUTS",
		// What bash sets itself.
		"PWD", "OLDPWD", "SHLVL", "_")
	var names []string
	for _, line := range strings.Split(res.Output, "\n") {
		name, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		names = append(names, name)
		if !slices.Contains(allowed, name) {
			t.Errorf("the script got %s", name)
		}
	}
	if !slices.Contains(names, "SHARED_TOKEN") || !slices.Contains(names, "INPUTS") {
		t.Errorf("the script didn't get what it should: %v", names)
	}
}

// pi gets the model keys besides, but the git token only goes to the
// worker's own git.
func TestAgentsGetModelKeysButNotTheGitToken(t *testing.T) {
	t.Setenv("GIT_TOKEN", "secret-git")
	t.Setenv("ANTHROPIC_API_KEY", "from-env")
	t.Setenv("AWS_REGION", "eu-central-1")
	keyNames["ANTHROPIC_API_KEY"] = true
	t.Cleanup(func() { delete(keyNames, "ANTHROPIC_API_KEY") })
	env, err := runRequest{}.withKeys(childEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "ANTHROPIC_API_KEY=from-env") || !slices.Contains(env, "AWS_REGION=eu-central-1") || slices.Contains(env, "GIT_TOKEN=secret-git") {
		t.Errorf("pi's environment = %v", envNames(env))
	}
	if git := childEnv(gitCredentials()...); !slices.Contains(git, "GIT_TOKEN=secret-git") {
		t.Errorf("git's environment = %v", envNames(git))
	}
}

// The names of an environment's variables, which a failing test prints
// rather than their values: the worker's environment may hold real keys.
func envNames(env []string) []string {
	names := make([]string, len(env))
	for i, kv := range env {
		names[i], _, _ = strings.Cut(kv, "=")
	}
	return names
}
