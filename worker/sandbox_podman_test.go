package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Splitting podman's stream of a container's output.
func TestDemux(t *testing.T) {
	frame := func(stream byte, s string) []byte {
		return append([]byte{stream, 0, 0, 0, 0, 0, 0, byte(len(s))}, s...)
	}
	in := bytes.Join([][]byte{frame(1, "out "), frame(2, "err"), frame(1, "more")}, nil)
	var out, errOut bytes.Buffer
	if err := demux(bytes.NewReader(in), &out, &errOut); err != nil || out.String() != "out more" || errOut.String() != "err" {
		t.Errorf("demux = %q, %q, %v", out.String(), errOut.String(), err)
	}
}

// A step's container sees only the directories it is given, as its own
// user, with a read-only root; offline it has no network. It runs only in
// a container of the worker's, with podman's socket and the worker's
// volumes: SANDBOX_TEST=podman; see docs/SANDBOX.md.
func TestPodmanSandbox(t *testing.T) {
	if os.Getenv("SANDBOX_TEST") != "podman" {
		t.Skip("SANDBOX_TEST isn't podman")
	}
	ctx := context.Background()
	s, err := newPodmanSandbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sandboxOwner = -1 })
	mine, err := makeStepDir("private-")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := makeStepDir("private-")
	defer os.RemoveAll(mine)
	defer os.RemoveAll(other)
	os.WriteFile(filepath.Join(mine, "prompt"), []byte("from stdin"), 0o600)
	os.WriteFile(filepath.Join(other, "git-credentials"), []byte("password=someone-elses"), 0o600)
	ws := filepath.Join(workspacesRoot, "run-1")
	os.MkdirAll(ws, 0o700)
	os.MkdirAll(filepath.Join(workspacesRoot, "run-2"), 0o700)
	os.WriteFile(filepath.Join(workspacesRoot, "run-2", "theirs"), []byte("x"), 0o600)
	ownForSandbox(ws, false)

	script := `cat; echo; id -u; ls ` + stepsRoot + `; ls ` + workspacesRoot + `; cat ` + other + `/git-credentials 2>&1; touch /usr/x 2>&1 || true
touch ` + ws + `/made && echo wrote-workspace; touch ` + mine + `/x 2>/dev/null || echo private-read-only; exit 7`
	var out, errOut bytes.Buffer
	err = s.run(ctx, sandboxSpec{Cmd: []string{"sh", "-c", script}, Stdin: filepath.Join(mine, "prompt"), Dir: ws, Env: []string{"GREETING=hi", "HTTPS_PROXY=http://elsewhere"},
		Mounts: []mount{{Path: ws}, {Path: mine, ReadOnly: true}}}, &out, &errOut)
	got := out.String() + errOut.String()
	var exit exitError
	if !errors.As(err, &exit) || exit != 7 {
		t.Errorf("exited with %v", err)
	}
	for _, want := range []string{"from stdin", "\n1000\n", filepath.Base(mine), "run-1", "wrote-workspace", "private-read-only", "Read-only file system"} {
		if !strings.Contains(got, want) {
			t.Errorf("the container printed %q, lacking %q", got, want)
		}
	}
	if listed := strings.Split(got, "\n"); strings.Contains(strings.Join(listed[2:4], " "), filepath.Base(other)) {
		t.Errorf("the container listed another step's directory: %q", got)
	}
	for _, not := range []string{"run-2", "someone-elses"} {
		if strings.Contains(got, not) {
			t.Errorf("the container saw %q: %q", not, got)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, "made")); err != nil {
		t.Errorf("what it wrote in its workspace: %v", err)
	}

	out.Reset()
	s.run(ctx, sandboxSpec{Cmd: []string{"sh", "-c", "curl -s -m 5 -o /dev/null https://1.1.1.1 || echo offline"}, Dir: "/tmp", Offline: true}, &out, &out)
	if !strings.Contains(out.String(), "offline") {
		t.Errorf("offline it printed %q", out.String())
	}

	// Stopping the step stops its container.
	stop, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	s.run(stop, sandboxSpec{Cmd: []string{"sleep", "120"}, Dir: "/tmp"}, &out, &out)
	if time.Since(started) > 30*time.Second {
		t.Errorf("stopping took %v", time.Since(started))
	}
}

// A script step in its run's workspace, in a container: it sees its own
// workspace and no other run's, its git works there, and what it leaves
// uncommitted is committed in a sandbox and brought back to the branch.
func TestPodmanScriptStepInItsWorkspace(t *testing.T) {
	if os.Getenv("SANDBOX_TEST") != "podman" {
		t.Skip("SANDBOX_TEST isn't podman")
	}
	s, err := newPodmanSandbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	steps = s
	t.Cleanup(func() { steps, sandboxOwner = processSandbox{}, -1 })
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "Flow agent"}, {"GIT_AUTHOR_EMAIL", "a@localhost"}, {"GIT_COMMITTER_NAME", "Flow agent"}, {"GIT_COMMITTER_EMAIL", "a@localhost"}} {
		t.Setenv(kv[0], kv[1])
	}
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-31"
	sharedWithBranch(t, url, branch)
	os.MkdirAll(filepath.Join(workspacesRoot, "run-32", "api"), 0o700)

	res := runScript(context.Background(), scriptRequest{
		Run:       31,
		Script:    "echo cached > $HOME/cache; ls " + workspacesRoot + "; cd api && git log --oneline | wc -l && echo new > new.txt",
		Workspace: &workspaceSpec{ID: "run-31", Branch: branch, Run: 31, Step: 4, Repositories: []repository{{Name: "api", URL: url}}},
	}, time.Minute, newStepLogger("test", nil))
	if !res.OK {
		t.Fatalf("the script failed: %s", res.Error)
	}
	if lines := strings.Fields(res.Output); len(lines) != 2 || lines[0] != "run-31" || lines[1] != "1" {
		t.Errorf("the script printed %q, want only its own workspace and its branch's commit", res.Output)
	}
	if len(res.Commits) != 1 || len(res.Commits[0].Commits) != 1 || !strings.Contains(res.Commits[0].Commits[0], "left uncommitted") {
		t.Errorf("commits = %+v", res.Commits)
	}
	if b, err := os.ReadFile(filepath.Join(runHome(31), "cache")); err != nil || string(b) != "cached\n" {
		t.Errorf("the run's home holds %q, %v", b, err)
	}
}
