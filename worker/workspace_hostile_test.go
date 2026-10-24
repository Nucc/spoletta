package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A sandbox that runs nothing, so a test sees only what the trusted side
// does; it records what it was asked to run.
type idleSandbox struct{ ran *[]sandboxSpec }

func (s idleSandbox) run(_ context.Context, spec sandboxSpec, _, _ io.Writer) error {
	*s.ran = append(*s.ran, spec)
	return nil
}

func gitSetUp(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	workspacesRoot = t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "Flow agent"}, {"GIT_AUTHOR_EMAIL", "a@localhost"}, {"GIT_COMMITTER_NAME", "Flow agent"}, {"GIT_COMMITTER_EMAIL", "a@localhost"}} {
		t.Setenv(kv[0], kv[1])
	}
}

// What a step leaves in its checkout's git for whoever runs git there next:
// a monitor, hooks, a filter, an upload-pack hook. Bringing the branch back
// and releasing the workspace runs none of them, and the step's commits
// still come back. The commit of what it left runs in the sandbox.
func TestWhatAStepLeavesInItsGitDoesntRunOutsideTheSandbox(t *testing.T) {
	gitSetUp(t)
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-9"
	sharedWithBranch(t, url, branch)
	spec := &workspaceSpec{ID: "run-9", Branch: branch, Run: 9, Step: 3, Repositories: []repository{{Name: "api", URL: url}}}
	ws, err := takeWorkspace(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(ws.dir, "api")
	if got := git(t, api, "remote", "get-url", "origin"); got != url {
		t.Errorf("the checkout's origin = %q, want the repository's", got)
	}
	// The step commits, then plants.
	os.WriteFile(filepath.Join(api, "fix.go"), []byte("package fix\n"), 0o644)
	git(t, api, "add", "--all")
	git(t, api, "commit", "--quiet", "-m", "Fix it")
	marker := t.TempDir()
	touch := func(name string) string { return "touch " + filepath.Join(marker, name) }
	for k, v := range map[string]string{
		"core.fsmonitor":             touch("fsmonitor") + "; false",
		"filter.evil.clean":          touch("clean"),
		"filter.evil.smudge":         touch("smudge"),
		"filter.evil.process":        touch("process"),
		"uploadpack.packObjectsHook": touch("packObjectsHook"),
		"core.sshCommand":            touch("ssh"),
		"core.pager":                 touch("pager"),
		"diff.external":              touch("diff"),
	} {
		git(t, api, "config", k, v)
	}
	os.WriteFile(filepath.Join(api, ".gitattributes"), []byte("* filter=evil\n"), 0o644)
	for _, h := range []string{"post-commit", "pre-commit", "reference-transaction", "post-upload-pack", "post-checkout"} {
		os.WriteFile(filepath.Join(api, ".git", "hooks", h), []byte("#!/bin/sh\n"+touch(h)+"\n"), 0o755)
	}
	os.WriteFile(filepath.Join(api, "left.go"), []byte("package fix\n"), 0o644)

	var ran []sandboxSpec
	steps = idleSandbox{&ran}
	t.Cleanup(func() { steps = processSandbox{} })
	var res runResult
	ws.done(&res)
	if len(ran) != 1 || ran[0].Dir != api || !ran[0].Offline || len(ran[0].Mounts) != 1 || ran[0].Mounts[0].Path != api {
		t.Errorf("the sandbox was asked to run %+v, want the commit of what was left, offline, seeing only the checkout", ran)
	}
	if len(res.Commits) != 1 || len(res.Commits[0].Commits) != 1 || !strings.HasSuffix(res.Commits[0].Commits[0], "Fix it") {
		t.Errorf("commits = %+v, want the step's", res.Commits)
	}
	if got := git(t, sharedRepo(url), "log", "-1", "--format=%s", "refs/heads/"+branch); got != "Fix it" {
		t.Errorf("the shared branch is at %q", got)
	}

	if released, err := releaseWorkspaces([]int64{9}); err != nil || len(released) != 1 {
		t.Errorf("released %v, %v", released, err)
	}
	if left, _ := os.ReadDir(marker); len(left) != 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("what the step planted ran outside the sandbox: %v", names)
	}
}

// A checkout whose git, or which itself, became a link to another's isn't
// brought back: the run's branch stays as it was.
func TestALinkedCheckoutIsntBroughtBack(t *testing.T) {
	gitSetUp(t)
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-10"
	sharedWithBranch(t, url, branch)
	other := sharedRepo(url)
	before := git(t, other, "rev-parse", "refs/heads/"+branch)
	var ran []sandboxSpec
	steps = idleSandbox{&ran}
	t.Cleanup(func() { steps = processSandbox{} })

	for name, plant := range map[string]func(api string){
		"its .git a link": func(api string) {
			os.RemoveAll(filepath.Join(api, ".git"))
			os.Symlink(other, filepath.Join(api, ".git"))
		},
		"its .git a file": func(api string) {
			os.RemoveAll(filepath.Join(api, ".git"))
			os.WriteFile(filepath.Join(api, ".git"), []byte("gitdir: "+other+"\n"), 0o644)
		},
		"itself a link": func(api string) {
			os.RemoveAll(api)
			os.Symlink(other, api)
		},
	} {
		spec := &workspaceSpec{ID: "run-10", Branch: branch, Repositories: []repository{{Name: "api", URL: url}}}
		ws, err := takeWorkspace(context.Background(), spec, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		plant(filepath.Join(ws.dir, "api"))
		var res runResult
		ws.done(&res)
		if len(res.Commits) != 0 {
			t.Errorf("%s: brought back %+v", name, res.Commits)
		}
		if got := git(t, other, "rev-parse", "refs/heads/"+branch); got != before {
			t.Errorf("%s: the branch moved to %s", name, got)
		}
		os.RemoveAll(filepath.Join(ws.dir, "api"))
	}
}

// A step's commit comes back, but one it crafted malformed, here a commit
// whose author has no proper email, isn't: the fetch into the shared
// repository checks each object it takes, and the branch stays as it was.
func TestAMalformedCommitIsntBroughtBack(t *testing.T) {
	gitSetUp(t)
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-11"
	sharedWithBranch(t, url, branch)
	shared := sharedRepo(url)
	var ran []sandboxSpec
	steps = idleSandbox{&ran}
	t.Cleanup(func() { steps = processSandbox{} })
	spec := &workspaceSpec{ID: "run-11", Branch: branch, Repositories: []repository{{Name: "api", URL: url}}}

	ws, err := takeWorkspace(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(ws.dir, "api")
	os.WriteFile(filepath.Join(api, "fix.go"), []byte("package fix\n"), 0o644)
	git(t, api, "add", "--all")
	git(t, api, "commit", "--quiet", "-m", "Fix it")
	var res runResult
	ws.done(&res)
	good := git(t, shared, "rev-parse", "refs/heads/"+branch)
	if got := git(t, shared, "log", "-1", "--format=%s", good); got != "Fix it" {
		t.Fatalf("the step's commit didn't come back: the branch is at %q", got)
	}

	ws, err = takeWorkspace(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	api = filepath.Join(ws.dir, "api")
	tree, head := git(t, api, "rev-parse", "HEAD^{tree}"), git(t, api, "rev-parse", "HEAD")
	raw := filepath.Join(t.TempDir(), "commit")
	os.WriteFile(raw, []byte("tree "+tree+"\nparent "+head+"\nauthor Evil <evil 1700000000 +0000\ncommitter Evil <evil@x> 1700000000 +0000\n\nEvil\n"), 0o644)
	evil := git(t, api, "hash-object", "-t", "commit", "-w", "--literally", raw)
	git(t, api, "update-ref", "refs/heads/"+branch, evil)
	res = runResult{}
	ws.done(&res)
	if len(res.Commits) != 0 {
		t.Errorf("brought back %+v", res.Commits)
	}
	if got := git(t, shared, "rev-parse", "refs/heads/"+branch); got != good {
		t.Errorf("the branch moved to %s, the malformed commit", got)
	}
}

// A re-run's branch starts from the commit it is given, which another
// run's branch holds.
func TestReRunStartsFromItsCommit(t *testing.T) {
	gitSetUp(t)
	const url = "https://example.invalid/acme/api.git"
	sharedWithBranch(t, url, "flow/test/run-1")
	ctx := context.Background()
	ws, err := takeWorkspace(ctx, &workspaceSpec{ID: "run-1", Branch: "flow/test/run-1", Repositories: []repository{{Name: "api", URL: url}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(ws.dir, "api")
	os.WriteFile(filepath.Join(api, "one.go"), []byte("1"), 0o644)
	var res runResult
	ws.done(&res)
	start := res.Commits[0].After
	os.WriteFile(filepath.Join(workspacesRoot, "run-1", "api", "two.go"), []byte("2"), 0o644)
	ws, _ = takeWorkspace(ctx, &workspaceSpec{ID: "run-1", Branch: "flow/test/run-1", Repositories: []repository{{Name: "api", URL: url}}}, nil)
	ws.done(&res)

	again, err := takeWorkspace(ctx, &workspaceSpec{ID: "run-2", Branch: "flow/test/run-2", Repositories: []repository{{Name: "api", URL: url, Start: start}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer again.release()
	if got := git(t, filepath.Join(again.dir, "api"), "rev-parse", "HEAD"); got != start {
		t.Errorf("the re-run starts from %s, want %s", got, start)
	}
	if _, err := os.Stat(filepath.Join(again.dir, "api", "two.go")); !os.IsNotExist(err) {
		t.Errorf("the re-run has what came after its start: %v", err)
	}
}

// A worktree from before checkouts were clones is released as it was, and
// made again as a clone, with what it held.
func TestAWorktreeFromBeforeIsMadeAgainAsAClone(t *testing.T) {
	gitSetUp(t)
	const url, branch = "https://example.invalid/acme/api.git", "flow/test/run-11"
	sharedWithBranch(t, url, branch)
	repo := sharedRepo(url)
	dir := filepath.Join(workspacesRoot, "run-11")
	os.MkdirAll(dir, 0o700)
	git(t, repo, "worktree", "add", "--quiet", filepath.Join(dir, "api"), branch)
	os.WriteFile(filepath.Join(dir, "api", "old.go"), []byte("old"), 0o644)

	ws, err := takeWorkspace(context.Background(), &workspaceSpec{ID: "run-11", Branch: branch, Repositories: []repository{{Name: "api", URL: url}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.release()
	if fi, err := os.Lstat(filepath.Join(dir, "api", ".git")); err != nil || !fi.IsDir() {
		t.Errorf("the checkout's .git = %v, %v; want a clone's", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "api", "old.go")); err != nil || !bytes.Equal(b, []byte("old")) {
		t.Errorf("what the worktree held: %q, %v", b, err)
	}
}
