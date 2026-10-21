package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The step's token reaches the step's git, the worker's own and the
// script's, through the credential helper, and nothing else holds it.
func TestStepGitGetsItsToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	// Executable, as the image has it.
	b, err := os.ReadFile("git-credential.sh")
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "git-credential.sh")
	if err := os.WriteFile(helper, b, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCredentialHelper = helper
	t.Cleanup(func() { gitCredentialHelper = "/opt/flow/git-credential.sh" })
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dir := t.TempDir()
	ctx, env, err := writeGitCredentials(context.Background(), dir, &gitCredentials{Username: "x-access-token", Token: "step-git-token"})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "git-credentials")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the credentials file: %v, %v", fi, err)
	}
	// What git would send a host asking for credentials.
	fill := func(ctx context.Context, env []string) string {
		cmd := exec.CommandContext(ctx, "git", "credential", "fill")
		cmd.Env = childEnv(env...)
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
		out, _ := cmd.Output()
		return string(out)
	}
	if out := fill(ctx, gitCredentialEnv(ctx)); !strings.Contains(out, "password=step-git-token") {
		t.Errorf("the worker's own git got %q", out)
	}
	if !slices.Contains(env, "FLOW_GIT_CREDENTIALS="+filepath.Join(dir, "git-credentials")) {
		t.Errorf("the step's environment = %v", envNames(env))
	}
	if out := fill(context.Background(), nil); strings.Contains(out, "step-git-token") {
		t.Errorf("git without the step's environment got %q", out)
	}

	res := runScript(context.Background(), scriptRequest{Script: "git credential fill <<'EOF'\nprotocol=https\nhost=github.com\n\nEOF", Git: &gitCredentials{Token: "script-token"}}, time.Minute, newStepLogger("test", nil))
	if !strings.Contains(res.Output, "password=script-token") || !strings.Contains(res.Output, "username=x-access-token") {
		t.Errorf("the script's git got %q, %s", res.Output, res.Error)
	}

	if _, _, err := writeGitCredentials(context.Background(), dir, &gitCredentials{Token: "two\nlines"}); err == nil {
		t.Error("a token of two lines was written")
	}
}

// A step that would outlive its token is cut short, saying so.
func TestStepEndsBeforeItsToken(t *testing.T) {
	soon := time.Now().Add(time.Minute + 2*time.Second)
	res := runScript(context.Background(), scriptRequest{Script: "sleep 60", Git: &gitCredentials{Token: "t", Expires: &soon}}, 10*time.Minute, newStepLogger("test", nil))
	if res.OK || !strings.Contains(res.Error, "git token ends") {
		t.Errorf("result = %+v", res)
	}
	later := time.Now().Add(time.Hour)
	if d, short := untilTokenEnds(time.Minute, &gitCredentials{Expires: &later}); short || d != time.Minute {
		t.Errorf("a step ending before its token: %v, %v", d, short)
	}
}
