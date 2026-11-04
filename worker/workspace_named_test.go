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

// An origin for url, a bare repository on disk that git reaches through
// insteadOf, holding main and the branch feature/PROJ-1 a commit ahead of
// it, whose commits it returns.
func namedOrigin(t *testing.T, url string) (origin, mainCommit, featureCommit string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	workspacesRoot = t.TempDir()
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "Flow agent"}, {"GIT_AUTHOR_EMAIL", "a@localhost"}, {"GIT_COMMITTER_NAME", "Flow agent"}, {"GIT_COMMITTER_EMAIL", "a@localhost"}} {
		t.Setenv(kv[0], kv[1])
	}
	origin = filepath.Join(t.TempDir(), "origin.git")
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte("[url \"file://"+origin+"\"]\n\tinsteadOf = "+url+"\n[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	git(t, "", "init", "--quiet", "--bare", origin)
	src := t.TempDir()
	git(t, src, "init", "--quiet", "--initial-branch=main")
	os.WriteFile(filepath.Join(src, "README"), []byte("hello\n"), 0o644)
	git(t, src, "add", "--all")
	git(t, src, "commit", "--quiet", "-m", "Start")
	mainCommit = git(t, src, "rev-parse", "HEAD")
	git(t, src, "checkout", "--quiet", "-b", "feature/PROJ-1")
	os.WriteFile(filepath.Join(src, "feature.go"), []byte("package feature\n"), 0o644)
	git(t, src, "add", "--all")
	git(t, src, "commit", "--quiet", "-m", "Begin PROJ-1")
	featureCommit = git(t, src, "rev-parse", "HEAD")
	git(t, src, "push", "--quiet", origin, "main", "feature/PROJ-1")
	return origin, mainCommit, featureCommit
}

// A run whose flow names its branch works on it: its checkout is on the
// name, starting from origin's latest commit of it, and a plain push
// updates it there. The worker keeps the run's own branch, never the name,
// so runs on the same name don't meet in it, and a checkout made again
// after the workspace was released is on the name again.
func TestCheckoutIsOnTheNamedBranch(t *testing.T) {
	const url = "https://example.invalid/acme/api.git"
	origin, _, feature := namedOrigin(t, url)
	ctx := context.Background()
	spec := &workspaceSpec{ID: "run-7", Branch: "flow/fix/run-7", Named: "feature/PROJ-1", Run: 7, Step: 12, Repositories: []repository{{Name: "api", URL: url}}}

	ws, err := takeWorkspace(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(ws.dir, "api")
	if got := git(t, api, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/PROJ-1" {
		t.Errorf("the checkout is on %s, want feature/PROJ-1", got)
	}
	if got := git(t, api, "rev-parse", "HEAD"); got != feature {
		t.Errorf("the checkout starts from %s, want origin's feature/PROJ-1 at %s", got, feature)
	}
	if refs := git(t, api, "for-each-ref", "--format=%(refname)"); strings.Contains(refs, "run-7") {
		t.Errorf("the checkout names the run's branch: %s", refs)
	}
	os.WriteFile(filepath.Join(api, "fix.go"), []byte("package fix\n"), 0o644)
	var res runResult
	ws.done(&res)
	if len(res.Commits) != 1 || res.Commits[0].Before != feature || res.Commits[0].After == feature {
		t.Fatalf("commits = %+v, want the step's commit on top of %s", res.Commits, feature)
	}
	repo := sharedRepo(url)
	if got := git(t, repo, "rev-parse", "refs/heads/flow/fix/run-7"); got != res.Commits[0].After {
		t.Errorf("the run's branch is at %s, want %s", got, res.Commits[0].After)
	}
	if err := gitOK(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/feature/PROJ-1"); err == nil {
		t.Error("the shared repository keeps the name as a branch")
	}
	if specs := git(t, repo, "config", "--get-all", "remote.origin.fetch"); strings.Contains(specs, "PROJ-1") {
		t.Errorf("the shared repository fetches the name from now on: %s", specs)
	}

	git(t, api, "push", "--quiet", "origin")
	if got := git(t, origin, "rev-parse", "refs/heads/feature/PROJ-1"); got != res.Commits[0].After {
		t.Errorf("a push left origin's feature/PROJ-1 at %s, want %s", got, res.Commits[0].After)
	}

	if released, err := releaseWorkspaces([]int64{7}); err != nil || !slices.Equal(released, []string{"run-7"}) {
		t.Fatalf("released %v (%v), want run-7", released, err)
	}
	ws, err = takeWorkspace(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.release()
	if got := git(t, api, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/PROJ-1" {
		t.Errorf("the checkout made again is on %s, want feature/PROJ-1", got)
	}
	if _, err := os.Stat(filepath.Join(api, "fix.go")); err != nil {
		t.Errorf("the step's work didn't come back: %v", err)
	}
}

// A name origin doesn't have yet starts from the base branch, for a push to
// make it; a name that is the branch runs start from is refused.
func TestNamedBranchOriginLacksStartsFromTheBase(t *testing.T) {
	const url = "https://example.invalid/acme/web.git"
	origin, main, _ := namedOrigin(t, url)
	ctx := context.Background()

	spec := &workspaceSpec{ID: "run-8", Branch: "flow/fix/run-8", Named: "feature/PROJ-2", Repositories: []repository{{Name: "web", URL: url}}}
	ws, err := takeWorkspace(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	web := filepath.Join(ws.dir, "web")
	if got := git(t, web, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/PROJ-2" {
		t.Errorf("the checkout is on %s, want feature/PROJ-2", got)
	}
	if got := git(t, web, "rev-parse", "HEAD"); got != main {
		t.Errorf("the checkout starts from %s, want main at %s", got, main)
	}
	git(t, web, "push", "--quiet", "origin")
	if got := git(t, origin, "rev-parse", "refs/heads/feature/PROJ-2"); got != main {
		t.Errorf("a push left origin's feature/PROJ-2 at %q, want %s", got, main)
	}
	ws.release()

	for _, c := range []struct {
		id, base string
	}{{"run-9", ""}, {"run-10", "feature/PROJ-1"}} {
		named := "main"
		if c.base != "" {
			named = c.base
		}
		spec := &workspaceSpec{ID: c.id, Branch: "flow/fix/" + c.id, Named: named, Repositories: []repository{{Name: "web", URL: url, Base: c.base}}}
		if _, err := takeWorkspace(ctx, spec, nil); err == nil || !strings.Contains(err.Error(), "runs start from") {
			t.Errorf("a run named %s on base %q: %v, want it refused", named, c.base, err)
		}
	}
}

// A run whose flow names no branch for it, its value being blank, works on
// its own branch from the base, and its clone says why as it makes it.
func TestUnnamedBranchIsSaidAsTheCloneIsMade(t *testing.T) {
	const url = "https://example.invalid/acme/docs.git"
	_, main, _ := namedOrigin(t, url)
	ctx := context.Background()
	why := `the branch feature/{@Ticket.key} couldn't be named: "Ticket"'s "key" is blank`
	spec := &workspaceSpec{ID: "run-11", Branch: "flow/fix/run-11", Unnamed: why, Repositories: []repository{{Name: "docs", URL: url}}}
	var lines []string
	l := newStepLogger("test", func(line logLine) { lines = append(lines, line.Message) })

	ws, err := takeWorkspace(ctx, spec, l)
	if err != nil {
		t.Fatal(err)
	}
	docs := filepath.Join(ws.dir, "docs")
	if got := git(t, docs, "rev-parse", "--abbrev-ref", "HEAD"); got != "flow/fix/run-11" {
		t.Errorf("the checkout is on %s, want the run's own branch", got)
	}
	if got := git(t, docs, "rev-parse", "HEAD"); got != main {
		t.Errorf("the checkout starts from %s, want main at %s", got, main)
	}
	ws.release()
	want := "docs: " + why + ", so the run works on flow/fix/run-11, a branch of its own"
	if !slices.Contains(lines, want) {
		t.Errorf("the log lacks %q:\n%s", want, strings.Join(lines, "\n"))
	}

	// Taken again, the branch is there, and nothing is said of it.
	lines = nil
	ws, err = takeWorkspace(ctx, spec, l)
	if err != nil {
		t.Fatal(err)
	}
	ws.release()
	for _, line := range lines {
		if strings.Contains(line, "a branch of its own") {
			t.Errorf("said again: %s", line)
		}
	}
}
