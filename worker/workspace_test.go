package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOut(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A shared repository for url with the run's branch already in it, as a run
// that worked once leaves it; nothing needs fetching.
func sharedWithBranch(t *testing.T, url, branch string) {
	t.Helper()
	src := t.TempDir()
	git(t, src, "init", "--quiet")
	os.WriteFile(filepath.Join(src, "README"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(src, ".gitignore"), []byte("node_modules/\n"), 0o644)
	git(t, src, "add", "--all")
	git(t, src, "commit", "--quiet", "-m", "Start")
	repo := sharedRepo(url)
	if err := os.MkdirAll(repo, 0o777); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "--quiet", "--bare")
	git(t, src, "push", "--quiet", repo, "HEAD:refs/heads/"+branch)
}

// What a step leaves uncommitted is committed, naming its run, step and
// session; releasing the workspace keeps the branch, so taking it again
// makes the checkout again with everything committed, but not what git
// ignores. A workspace in use isn't released.
func TestReleasedWorkspaceIsMadeAgainFromItsBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	workspacesRoot = t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "Flow agent"}, {"GIT_AUTHOR_EMAIL", "a@localhost"}, {"GIT_COMMITTER_NAME", "Flow agent"}, {"GIT_COMMITTER_EMAIL", "a@localhost"}} {
		t.Setenv(kv[0], kv[1])
	}
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-7"
	sharedWithBranch(t, url, branch)
	spec := &workspaceSpec{ID: "run-7", Branch: branch, Run: 7, Step: 12, Repositories: []repository{{Name: "api", URL: url}}}
	ctx := context.Background()

	ws, err := takeWorkspace(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.process, ws.session = "Fix", "s-1"
	api := filepath.Join(ws.dir, "api")
	os.WriteFile(filepath.Join(api, "fix.go"), []byte("package fix\n"), 0o644)
	os.MkdirAll(filepath.Join(api, "node_modules"), 0o777)
	os.WriteFile(filepath.Join(api, "node_modules", "dep.js"), []byte("x"), 0o644)
	var res runResult
	ws.done(&res)
	if len(res.Commits) != 1 || res.Commits[0].Before == res.Commits[0].After || res.Commits[0].Uncommitted {
		t.Fatalf("commits = %+v, want the step's leftovers committed", res.Commits)
	}
	msg := git(t, api, "log", "-1", "--format=%B")
	for _, want := range []string{"Commit what Fix left uncommitted", "Spoletta-Run: 7", "Spoletta-Step: 12", "Pi-Session: s-1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("commit message %q lacks %q", msg, want)
		}
	}

	// A step that ended without getting to commit leaves something behind.
	os.WriteFile(filepath.Join(api, "half.go"), []byte("package fix\n"), 0o644)
	unlock := lockPath(filepath.Join(workspacesRoot, "run-7"))
	if released, err := releaseWorkspaces([]int64{7}); err != nil || len(released) != 0 {
		t.Errorf("released %v (%v) while in use", released, err)
	}
	unlock()
	if released, err := releaseWorkspaces([]int64{7, 8}); err != nil || !slices.Equal(released, []string{"run-7"}) {
		t.Fatalf("released %v (%v), want run-7", released, err)
	}
	if _, err := os.Stat(filepath.Join(workspacesRoot, "run-7")); !os.IsNotExist(err) {
		t.Errorf("the workspace is still there: %v", err)
	}

	ws, err = takeWorkspace(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.release()
	if !slices.Equal(ws.recreated, []string{"api"}) {
		t.Errorf("recreated = %v, want api", ws.recreated)
	}
	for _, f := range []string{"fix.go", "half.go"} {
		if _, err := os.Stat(filepath.Join(api, f)); err != nil {
			t.Errorf("%s didn't come back: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(api, "node_modules")); !os.IsNotExist(err) {
		t.Errorf("ignored files came back: %v", err)
	}
}
