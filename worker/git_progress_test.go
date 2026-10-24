package main

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// A phase's first line, its done line and one every few seconds are
// logged, git's \r rewrites taken apart; what isn't progress is logged as
// it comes, but what went wrong only kept, for the error.
func TestGitProgressLogsEachPhaseThrottled(t *testing.T) {
	var said []string
	p := newGitProgress(func(line string) { said = append(said, line) })
	clock := time.Unix(0, 0)
	p.now = func() time.Time { return clock }
	write := func(s string) { p.Write([]byte(s)) }

	write("Cloning into '/data/workspaces/.adding/run-1-api'...\n")
	write("remote: Enumerating objects: 12, done.\n")
	write("remote: Counting objects:  50% (6/12)\rremote: Counting objects: 100% (12/12), done.\n")
	write("Receiving objects:   1% (1/100)\rReceiving objects:  10% (10/100)\r")
	clock = clock.Add(3 * time.Second)
	write("Receiving objects:  40% (40/100), 1.0")
	write("0 MiB | 1.00 MiB/s\rReceiving objects:  60% (60/100)\r")
	write("warning: redirecting to https://example.com/acme/api.git/\n")
	write("fatal: the remote end hung up unexpectedly\n")
	p.flush()

	want := []string{
		"remote: Enumerating objects: 12, done.",
		"remote: Counting objects:  50% (6/12)",
		"remote: Counting objects: 100% (12/12), done.",
		"Receiving objects:   1% (1/100)",
		"Receiving objects:  40% (40/100), 1.00 MiB | 1.00 MiB/s",
		"Receiving objects:  60% (60/100)",
		"warning: redirecting to https://example.com/acme/api.git/",
	}
	if !slices.Equal(said, want) {
		t.Fatalf("said\n%s\nwant\n%s", strings.Join(said, "\n"), strings.Join(want, "\n"))
	}
	if p.said[0] != "warning: redirecting to https://example.com/acme/api.git/" || p.said[len(p.said)-1] != "fatal: the remote end hung up unexpectedly" {
		t.Fatalf("kept %q", p.said)
	}
}

func TestShownURLLeavesOutCredentials(t *testing.T) {
	if got := shownURL("https://bot:s3cret@github.com/acme/api.git"); got != "https://github.com/acme/api.git" {
		t.Fatalf("shown as %q", got)
	}
}

// Getting a workspace ready logs each repository's checkout as it goes, and
// a step waiting for the workspace says so.
func TestTakingAWorkspaceLogsWhatItDoes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	workspacesRoot = t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "Flow agent"}, {"GIT_AUTHOR_EMAIL", "a@localhost"}, {"GIT_COMMITTER_NAME", "Flow agent"}, {"GIT_COMMITTER_EMAIL", "a@localhost"}} {
		t.Setenv(kv[0], kv[1])
	}
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-3"
	sharedWithBranch(t, url, branch)
	spec := &workspaceSpec{ID: "run-3", Branch: branch, Repositories: []repository{{Name: "api", URL: url}}}
	ctx := context.Background()

	l := newStepLogger("process 1", nil)
	ws, err := takeWorkspace(ctx, spec, l)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, line := range l.kept() {
		said = append(said, line.Message)
	}
	all := strings.Join(said, "\n")
	if !strings.Contains(all, "api: checking out the branch "+branch) || !strings.Contains(all, "api: ready in ") || strings.Contains(all, "Cloning into") {
		t.Fatalf("logged\n%s", all)
	}

	waiting := newStepLogger("process 2", nil)
	took := make(chan error)
	go func() {
		second, err := takeWorkspace(ctx, spec, waiting)
		if err == nil {
			second.release()
		}
		took <- err
	}()
	for len(waiting.kept()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if msg := waiting.kept()[0].Message; msg != "run-3: waiting for another step of the run working in it" {
		t.Fatalf("logged %q", msg)
	}
	ws.release()
	if err := <-took; err != nil {
		t.Fatal(err)
	}
}
