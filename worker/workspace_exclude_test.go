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

// A repository's paths left out aren't in its checkout, nor are their files
// fetched into it, yet stay in the branch as they were: a step's commit,
// releasing the workspace and making the checkout again leave them be, and
// what a step writes in one is left out as an ignored file is.
func TestCheckoutLeavesOutPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	workspacesRoot = t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "Flow agent"}, {"GIT_AUTHOR_EMAIL", "a@localhost"}, {"GIT_COMMITTER_NAME", "Flow agent"}, {"GIT_COMMITTER_EMAIL", "a@localhost"}} {
		t.Setenv(kv[0], kv[1])
	}
	// Nothing missing may be fetched from the repository's own URL, which
	// is no host, so a checkout that needed it would fail.
	const url, branch = "https://example.invalid/acme/shop.git", "flow/test/run-9"
	src := t.TempDir()
	git(t, src, "init", "--quiet")
	for f, s := range map[string]string{"README": "hello\n", "app/a.rb": "a\n", "vendor/cache/big.gem": strings.Repeat("gem", 100000), "vendor/cache/old/x.gem": "x", "docs/big.pdf": "pdf"} {
		os.MkdirAll(filepath.Join(src, filepath.Dir(f)), 0o777)
		os.WriteFile(filepath.Join(src, f), []byte(s), 0o644)
	}
	git(t, src, "add", "--all")
	git(t, src, "commit", "--quiet", "-m", "Start")
	big := git(t, src, "rev-parse", "HEAD:vendor/cache/big.gem")
	repo := sharedRepo(url)
	os.MkdirAll(repo, 0o777)
	git(t, repo, "init", "--quiet", "--bare")
	git(t, src, "push", "--quiet", repo, "HEAD:refs/heads/"+branch)

	spec := &workspaceSpec{ID: "run-9", Branch: branch, Run: 9, Step: 3, Repositories: []repository{{Name: "shop", URL: url, Exclude: []string{"vendor/cache", "docs/big.pdf"}}}}
	ctx := context.Background()
	ws, err := takeWorkspace(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.process = "Fix"
	shop := filepath.Join(ws.dir, "shop")
	for f, want := range map[string]bool{"README": true, "app/a.rb": true, "vendor/cache": false, "docs/big.pdf": false, "docs": false} {
		if _, err := os.Stat(filepath.Join(shop, f)); (err == nil) != want {
			t.Errorf("%s in the checkout: %v, want %v", f, err == nil, want)
		}
	}
	missing := git(t, shop, "rev-list", "--objects", "--missing=print", "HEAD")
	if !strings.Contains(missing, "?"+big) {
		t.Errorf("the big gem was fetched into the checkout:\n%s", missing)
	}

	os.WriteFile(filepath.Join(shop, "app/a.rb"), []byte("b\n"), 0o644)
	os.Remove(filepath.Join(shop, "README"))
	os.MkdirAll(filepath.Join(shop, "vendor/cache"), 0o777)
	os.WriteFile(filepath.Join(shop, "vendor/cache/new.gem"), []byte("n"), 0o644)
	var res runResult
	ws.done(&res)
	if res.Error != "" || len(res.Commits) != 1 || res.Commits[0].Before == res.Commits[0].After || res.Commits[0].Uncommitted {
		t.Fatalf("result = %+v, want the step's leftovers committed", res)
	}
	tree := strings.Split(git(t, repo, "ls-tree", "-r", "--name-only", branch), "\n")
	if want := []string{"app/a.rb", "docs/big.pdf", "vendor/cache/big.gem", "vendor/cache/old/x.gem"}; !slices.Equal(tree, want) {
		t.Errorf("the branch holds %v, want %v", tree, want)
	}

	if released, err := releaseWorkspaces([]int64{9}); err != nil || !slices.Equal(released, []string{"run-9"}) {
		t.Fatalf("released %v (%v), want run-9", released, err)
	}
	ws, err = takeWorkspace(ctx, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.release()
	if b, err := os.ReadFile(filepath.Join(shop, "app/a.rb")); err != nil || string(b) != "b\n" {
		t.Errorf("app/a.rb came back as %q (%v)", b, err)
	}
	if _, err := os.Stat(filepath.Join(shop, "vendor/cache")); !os.IsNotExist(err) {
		t.Errorf("the checkout made again holds vendor/cache: %v", err)
	}
}

func TestValidExcludes(t *testing.T) {
	for _, p := range []string{"vendor/cache", "a", "docs/big.pdf", "@types/x", ".bundle", "x/.cache"} {
		if err := validExcludes([]string{p}); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	for _, p := range []string{"", "/vendor", "vendor/", "../x", "a/../b", "a/./b", ".git", "a/.git", "*.gem", "!x", "-x", "a b", "a\nb", "#x", "a//b"} {
		if validExcludes([]string{p}) == nil {
			t.Errorf("%q taken", p)
		}
	}
}
