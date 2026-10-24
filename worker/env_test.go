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

	res := runScript(context.Background(), scriptRequest{Script: "env", Inputs: []scriptInput{{Name: "Ticket", Content: "x"}}}, time.Minute, newStepLogger("test", nil), nil)
	if !res.OK {
		t.Fatalf("script failed: %s", res.Error)
	}
	allowed := append(slices.Clone(childVars), "GIT_TERMINAL_PROMPT", "SHARED_TOKEN",
		"INPUTS", "SINCE", "LAST_RUN", "WORKSPACE", "BRANCH", "OUTPUTS", "PYTHONUNBUFFERED",
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
	if !slices.Contains(names, "SHARED_TOKEN") || !slices.Contains(names, "INPUTS") || !slices.Contains(names, "PYTHONUNBUFFERED") {
		t.Errorf("the script didn't get what it should: %v", names)
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
