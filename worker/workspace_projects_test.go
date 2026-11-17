package main

// Projects are tenants: each shares its repositories among its own runs
// alone, so nothing a step of one fetched, nor a branch of its runs, reaches
// a step of another, even when both name the same URL and the same branch.
// A spec naming no project is Legacy's, whose repositories stay where they
// were before projects.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Two projects naming the same URL and the same branch get shared
// repositories of their own: the second's checkout starts from origin, not
// from what the first committed, its repository lacks the first's commits,
// a re-run of it can't start from one, and the first's workspace can't be
// taken as the second's. Released and deleted together, each workspace goes
// back to its own project's repository.
func TestProjectsKeepTheirOwnSharedRepositories(t *testing.T) {
	const url = "https://example.invalid/acme/api.git"
	_, main, _ := namedOrigin(t, url)
	ctx := context.Background()
	const branch = "flow/fix/run-20"

	var specA workspaceSpec
	body := `{"id": "run-20", "project": 2, "branch": "` + branch + `", "run": 20, "repositories": [{"name": "api", "url": "` + url + `"}]}`
	if err := json.Unmarshal([]byte(body), &specA); err != nil || specA.Project != 2 {
		t.Fatalf("the webapp's spec read as %+v (%v)", specA, err)
	}
	ws, err := takeWorkspace(ctx, &specA, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ws.dir, "api", "secret.go"), []byte("package secret\n"), 0o644)
	var res runResult
	ws.done(&res)
	if len(res.Commits) != 1 || res.Commits[0].After == main {
		t.Fatalf("commits = %+v, want project 2's step's commit", res.Commits)
	}
	theirs := res.Commits[0].After

	repoA, repoB := sharedRepo(2, url), sharedRepo(3, url)
	if repoA == repoB {
		t.Fatalf("both projects share %s", repoA)
	}
	if got := git(t, repoA, "rev-parse", "refs/heads/"+branch); got != theirs {
		t.Errorf("project 2's branch is at %s, want %s", got, theirs)
	}
	if _, err := os.Stat(sharedRepo(0, url)); !os.IsNotExist(err) {
		t.Errorf("Legacy's shared repository was made for project 2: %v", err)
	}

	specB := &workspaceSpec{ID: "run-21", Project: 3, Branch: branch, Run: 21, Repositories: []repository{{Name: "api", URL: url}}}
	ws, err = takeWorkspace(ctx, specB, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(ws.dir, "api")
	if got := git(t, api, "rev-parse", "HEAD"); got != main {
		t.Errorf("project 3's checkout starts from %s, want origin's main at %s", got, main)
	}
	if _, err := os.Stat(filepath.Join(api, "secret.go")); !os.IsNotExist(err) {
		t.Errorf("project 3's checkout has project 2's file: %v", err)
	}
	os.WriteFile(filepath.Join(api, "mine.go"), []byte("package mine\n"), 0o644)
	res = runResult{}
	ws.done(&res)
	if len(res.Commits) != 1 || res.Commits[0].Before != main {
		t.Fatalf("commits = %+v, want project 3's step's commit on main", res.Commits)
	}
	mine := res.Commits[0].After
	if gitOK(ctx, repoB, "cat-file", "-e", theirs+"^{commit}") == nil {
		t.Error("project 3's shared repository has project 2's commit")
	}
	if gitOK(ctx, repoA, "cat-file", "-e", mine+"^{commit}") == nil {
		t.Error("project 2's shared repository has project 3's commit")
	}
	if got := git(t, repoA, "rev-parse", "refs/heads/"+branch); got != theirs {
		t.Errorf("project 3's step moved project 2's branch to %s", got)
	}

	rerun := &workspaceSpec{ID: "run-22", Project: 3, Branch: "flow/fix/run-22", Repositories: []repository{{Name: "api", URL: url, Start: theirs}}}
	if _, err := takeWorkspace(ctx, rerun, nil); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Errorf("a re-run in project 3 starting from project 2's commit: %v, want it refused", err)
	}

	for _, p := range []int64{0, 1, 3} {
		spec := &workspaceSpec{ID: "run-20", Project: p, Branch: branch, Repositories: []repository{{Name: "api", URL: url}}}
		if _, err := takeWorkspace(ctx, spec, nil); err == nil || !strings.Contains(err.Error(), "another project's") {
			t.Errorf("project 2's workspace taken as project %d's: %v", p, err)
		}
	}
	if _, err := takeWorkspace(ctx, &workspaceSpec{ID: "run-23", Project: -1, Branch: "flow/fix/run-23"}, nil); err == nil {
		t.Error("a negative project was taken")
	}

	if released, err := releaseWorkspaces([]int64{20, 21}); err != nil || len(released) != 2 {
		t.Fatalf("released %v (%v), want both", released, err)
	}
	if got := git(t, repoA, "rev-parse", "refs/heads/"+branch); got != theirs {
		t.Errorf("after releasing, project 2's branch is at %s, want %s", got, theirs)
	}
	if got := git(t, repoB, "rev-parse", "refs/heads/"+branch); got != mine {
		t.Errorf("after releasing, project 3's branch is at %s, want %s", got, mine)
	}
	if err := deleteWorkspaces([]int64{21}); err != nil {
		t.Fatal(err)
	}
	if gitOK(ctx, repoB, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil {
		t.Error("deleting project 3's run left its branch")
	}
	if got := git(t, repoA, "rev-parse", "refs/heads/"+branch); got != theirs {
		t.Errorf("deleting project 3's run moved project 2's branch to %s", got)
	}
}

// A spec naming no project, as every one from before projects, is Legacy's,
// and so is a workspace whose meta names none: its repositories are where
// every shared repository was, so its branches and re-runs go on as they
// did, and no other project can take it.
func TestASpecWithoutAProjectIsLegacys(t *testing.T) {
	gitSetUp(t)
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-7"
	sum := sha256.Sum256([]byte(url))
	legacy := filepath.Join(workspacesRoot, ".repos", hex.EncodeToString(sum[:8])+".git")
	for _, p := range []int64{0, 1} {
		if got := sharedRepo(p, url); got != legacy {
			t.Errorf("project %d's shared repository is %s, want %s", p, got, legacy)
		}
	}
	if got := sharedRepo(2, url); got == legacy || !strings.HasPrefix(got, filepath.Join(workspacesRoot, ".projects", "2")+"/") {
		t.Errorf("project 2's shared repository is %s, want one in a directory of its own", got)
	}

	sharedWithBranch(t, url, branch)
	start := git(t, legacy, "rev-parse", "refs/heads/"+branch)
	if err := os.MkdirAll(filepath.Dir(metaPath("run-7")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath("run-7"), []byte(`{"branch": "`+branch+`", "repositories": {"api": "`+url+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range []int64{0, 1} {
		spec := &workspaceSpec{ID: "run-7", Project: p, Branch: branch, Repositories: []repository{{Name: "api", URL: url}}}
		ws, err := takeWorkspace(ctx, spec, nil)
		if err != nil {
			t.Fatalf("a workspace from before projects taken as project %d's: %v", p, err)
		}
		if ws.before["api"] != start {
			t.Errorf("project %d's checkout starts from %s, want Legacy's branch at %s", p, ws.before["api"], start)
		}
		ws.release()
	}
	if meta, err := readMeta("run-7"); err != nil || meta.project() != legacyProject {
		t.Errorf("the workspace's meta is %+v (%v), want Legacy's", meta, err)
	}
	spec := &workspaceSpec{ID: "run-7", Project: 2, Branch: branch, Repositories: []repository{{Name: "api", URL: url}}}
	if _, err := takeWorkspace(ctx, spec, nil); err == nil || !strings.Contains(err.Error(), "another project's") {
		t.Errorf("Legacy's workspace taken as project 2's: %v", err)
	}
	if released, err := releaseWorkspaces([]int64{7}); err != nil || !slices.Equal(released, []string{"run-7"}) {
		t.Errorf("released %v (%v), want run-7", released, err)
	}
	if got := git(t, legacy, "rev-parse", "refs/heads/"+branch); got != start {
		t.Errorf("Legacy's branch is at %s after releasing, want %s", got, start)
	}
}

// A diff reads the shared repository of its workspace's project, as its meta
// keeps it: a commit only another project has isn't found through it, and
// a request naming another project than the workspace's is refused.
func TestDiffReadsItsWorkspacesProject(t *testing.T) {
	gitSetUp(t)
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-30"
	before2 := sharedWithBranchOf(t, 2, url, branch)
	before3 := sharedWithBranchOf(t, 3, url, branch)
	repo2 := sharedRepo(2, url)
	src := t.TempDir()
	git(t, src, "clone", "--quiet", "--branch", branch, repo2, ".")
	os.WriteFile(filepath.Join(src, "secret.go"), []byte("package secret\n"), 0o644)
	git(t, src, "add", "--all")
	git(t, src, "commit", "--quiet", "-m", "Only project 2's")
	git(t, src, "push", "--quiet", repo2, "HEAD:refs/heads/"+branch)
	after2 := git(t, repo2, "rev-parse", "refs/heads/"+branch)
	for id, p := range map[string]int64{"run-30": 2, "run-31": 3} {
		if err := writeMeta(id, workspaceMeta{Project: p, Branch: branch, Repositories: map[string]string{"api": url}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	var req diffRequest
	body := `{"project": 2, "workspace": "run-30", "repository": "api", "before": "` + before2 + `", "after": "` + after2 + `"}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	d, err := diffWorkspace(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "secret.go" || d.Files[0].Status != "added" {
		t.Errorf("project 2's diff lists %+v, want secret.go added", d.Files)
	}

	_, err = diffWorkspace(ctx, diffRequest{Project: 3, Workspace: "run-31", Repository: "api", Before: before3, After: after2})
	if err == nil || !strings.Contains(err.Error(), "isn't in the repository") {
		t.Errorf("project 2's commit through project 3's workspace: %v, want it not found", err)
	}

	for _, p := range []int64{0, 1, 3} {
		_, err := diffWorkspace(ctx, diffRequest{Project: p, Workspace: "run-30", Repository: "api", Before: before2, After: after2})
		if !errors.Is(err, errOtherProject) {
			t.Errorf("project 2's workspace asked about as project %d's: %v, want it refused", p, err)
		}
	}
	if _, err := diffWorkspace(ctx, diffRequest{Project: -2, Workspace: "run-30", Repository: "api", Before: before2, After: after2}); err == nil {
		t.Error("a negative project diffed")
	}
}

// The worker's own git config reaches a diff only for a project inheriting
// the instance's configuration, as it reaches only such a project's steps
// (env.go): for another, or a request naming its project and saying
// nothing, git reads the diff as if the worker had none.
func TestDiffWithholdsTheWorkersGitConfig(t *testing.T) {
	gitSetUp(t)
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-40"
	before := sharedWithBranchOf(t, 2, url, branch)
	repo := sharedRepo(2, url)
	src := t.TempDir()
	git(t, src, "clone", "--quiet", "--branch", branch, repo, ".")
	os.WriteFile(filepath.Join(src, "README"), []byte("hello\nworld\n"), 0o644)
	git(t, src, "commit", "--quiet", "--all", "-m", "Greet the world")
	git(t, src, "push", "--quiet", repo, "HEAD:refs/heads/"+branch)
	after := git(t, repo, "rev-parse", "refs/heads/"+branch)
	if err := writeMeta("run-40", workspaceMeta{Project: 2, Branch: branch, Repositories: map[string]string{"api": url}}); err != nil {
		t.Fatal(err)
	}
	// The worker's own config shows no lines around a change.
	config := filepath.Join(t.TempDir(), "instance.gitconfig")
	if err := os.WriteFile(config, []byte("[diff]\n\tcontext = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	yes, no := true, false
	for _, c := range []struct {
		name     string
		inherits *bool
		context  bool
	}{{"inheriting", &yes, false}, {"not inheriting", &no, true}, {"saying nothing", nil, true}} {
		d, err := diffWorkspace(context.Background(), diffRequest{Project: 2, InheritsInstance: c.inherits, Workspace: "run-40", Repository: "api", Before: before, After: after})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(d.Files) != 1 || !strings.Contains(d.Files[0].Patch, "+world") {
			t.Fatalf("%s: the diff lists %+v", c.name, d.Files)
		}
		if got := strings.Contains(d.Files[0].Patch, "\n hello\n"); got != c.context {
			t.Errorf("%s: the patch shows the line around the change %v, want %v:\n%s", c.name, got, c.context, d.Files[0].Patch)
		}
	}
}
