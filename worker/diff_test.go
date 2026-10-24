package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A step's changes come from the shared repository by the workspace's kept
// meta: added, modified, deleted, renamed and binary files, each with its
// own part of the patch, and odd names as they are.
func TestDiffOfAStepsCommits(t *testing.T) {
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
	repo := sharedRepo(url)
	before := git(t, repo, "rev-parse", "refs/heads/"+branch)

	src := t.TempDir()
	git(t, src, "clone", "--quiet", "--branch", branch, repo, ".")
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README", "hello\nworld\n")
	write("new file.txt", "one\ntwo\n")
	write("logo.bin", "\x00\x01\x02")
	git(t, src, "mv", ".gitignore", "ignore")
	git(t, src, "add", "--all")
	git(t, src, "commit", "--quiet", "-m", "Change things")
	git(t, src, "rm", "--quiet", "new file.txt")
	write("tab\tname", "x\n")
	git(t, src, "add", "--all")
	git(t, src, "commit", "--quiet", "-m", "Change more")
	git(t, src, "push", "--quiet", repo, "HEAD:refs/heads/"+branch)
	after := git(t, repo, "rev-parse", "refs/heads/"+branch)
	if err := writeMeta("run-7", workspaceMeta{Branch: branch, Repositories: map[string]string{"api": url}}); err != nil {
		t.Fatal(err)
	}

	d, err := diffWorkspace(context.Background(), diffRequest{Workspace: "run-7", Repository: "api", Before: before, After: after})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]fileChange{}
	for _, f := range d.Files {
		byPath[f.Path] = f
	}
	if len(d.Files) != 4 || d.More != 0 {
		t.Fatalf("files %+v, want 4", d.Files)
	}
	if f := byPath["README"]; f.Status != "modified" || f.Additions != 1 || f.Deletions != 0 || !strings.HasPrefix(f.Patch, "diff --git a/README b/README\n") || !strings.Contains(f.Patch, "\n+world\n") {
		t.Errorf("README %+v", f)
	}
	if f := byPath["ignore"]; f.Status != "renamed" || f.OldPath != ".gitignore" {
		t.Errorf("renamed %+v", f)
	}
	if f := byPath["logo.bin"]; f.Status != "added" || !f.Binary || strings.Contains(f.Patch, "README") {
		t.Errorf("binary %+v", f)
	}
	if f := byPath["tab\tname"]; f.Status != "added" || f.Additions != 1 {
		t.Errorf("odd name %+v", f)
	}
	if _, ok := byPath["new file.txt"]; ok {
		t.Errorf("a file added and removed again is listed")
	}

	for _, req := range []diffRequest{
		{Workspace: "run-7", Repository: "api", Before: "HEAD~1", After: after},
		{Workspace: "../run-7", Repository: "api", Before: before, After: after},
		{Workspace: "run-7", Repository: "api", Before: strings.Repeat("a", 40), After: after},
	} {
		if _, err := diffWorkspace(context.Background(), req); err == nil {
			t.Errorf("%+v diffed", req)
		}
	}
	if _, err := diffWorkspace(context.Background(), diffRequest{Workspace: "run-8", Repository: "api", Before: before, After: after}); !errors.Is(err, errUnknownWorkspace) {
		t.Errorf("an unknown workspace: %v", err)
	}
}

// A patch is cut at each file's diff --git line, never at a line of content
// that reads like one.
func TestDiffSplitsThePatchByFile(t *testing.T) {
	parts := splitPatch([]byte("diff --git a/x b/x\n+diff --git not a header\n diff --git neither\ndiff --git a/y b/y\n-y\n"))
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "diff --git a/y") || !strings.Contains(parts[0], "+diff --git not") {
		t.Errorf("parts %q", parts)
	}
}
