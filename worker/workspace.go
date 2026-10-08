package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// A workspace is a directory of git checkouts a run's agents and scripts
// work in, all on the run's branch. It is kept on a volume, so it outlives
// the step that made it, a review waiting for days and the worker restarting.
// Each repository is fetched once into a bare repository all workspaces
// share, and a workspace's checkout of it is a worktree of that, added when a
// step first needs it: only the files are copied, never the history. The
// shared repository holds only the latest commit of each branch runs start
// from, without tags: a big repository's whole history would take long to
// fetch, and agents work on what is there now. The
// runs' branches live in the shared repository too, so one run's branch can
// start from another's.
type workspaceSpec struct {
	// Names the workspace's directory, such as run-42.
	ID     string `json:"id"`
	Branch string `json:"branch"`
	// The repositories the step works on; others already in the workspace stay.
	Repositories []repository `json:"repositories"`
}

type repository struct {
	// The checkout's directory in the workspace.
	Name string `json:"name"`
	URL  string `json:"url"`
	// The branch the run's branch starts from; empty for the default branch.
	Base string `json:"base"`
}

// Where workspaces live, a volume of their own.
var workspacesRoot = envOr("WORKSPACES_DIR", "/data/workspaces")

// How long getting a workspace ready may take, apart from the step's own
// timeout: a first clone of a large repository takes a while.
const prepareTimeout = 15 * time.Minute

var (
	safeID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Per-path locks: a workspace is used by one step at a time, so agents
// sharing a branch don't trip over each other's edits and git's index lock;
// a shared repository fetches and adds worktrees one at a time.
var locks = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func lockPath(p string) func() {
	locks.Lock()
	l, ok := locks.m[p]
	if !ok {
		l = &sync.Mutex{}
		locks.m[p] = l
	}
	locks.Unlock()
	l.Lock()
	return l.Unlock
}

// A workspace a step has taken, or its scratch directory.
type takenWorkspace struct {
	dir   string
	repos []repository
	// Each repository's commit as the step started, by name.
	before  map[string]string
	release func()
}

// What a step did to a repository it worked on.
type repoCommits struct {
	Name string `json:"name"`
	// The commit it started from and the one it left; the same if it committed nothing.
	Before string `json:"before"`
	After  string `json:"after"`
	// The commits it made, newest first, as "<short hash> <subject>"; the
	// latest maxListedCommits of them.
	Commits []string `json:"commits"`
	// Whether it left changes it didn't commit.
	Uncommitted bool `json:"uncommitted"`
}

const maxListedCommits = 20

// Gets the workspace ready for a step and takes it: each repository the step
// works on is checked out on the branch. The caller gives it back with done
// once the step is over.
func takeWorkspace(ctx context.Context, ws *workspaceSpec) (*takenWorkspace, error) {
	if !safeID.MatchString(ws.ID) {
		return nil, fmt.Errorf("invalid workspace %q", ws.ID)
	}
	if err := gitOK(ctx, "", "check-ref-format", "--branch", ws.Branch); err != nil {
		return nil, fmt.Errorf("invalid branch %q", ws.Branch)
	}
	for _, r := range ws.Repositories {
		if !safeName.MatchString(r.Name) {
			return nil, fmt.Errorf("invalid repository name %q", r.Name)
		}
		if !strings.HasPrefix(r.URL, "https://") {
			return nil, fmt.Errorf("repository %s: only https URLs are supported", r.Name)
		}
		if r.Base != "" {
			if err := gitOK(ctx, "", "check-ref-format", "--branch", r.Base); err != nil {
				return nil, fmt.Errorf("repository %s: invalid base branch %q", r.Name, r.Base)
			}
		}
	}
	dir := filepath.Join(workspacesRoot, ws.ID)
	unlock := lockPath(dir)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		unlock()
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	t := &takenWorkspace{dir: dir, repos: ws.Repositories, before: map[string]string{}, release: unlock}
	for _, r := range ws.Repositories {
		if err := checkout(pctx, dir, ws.Branch, r); err != nil {
			unlock()
			return nil, fmt.Errorf("repository %s: %w", r.Name, err)
		}
		head, err := gitOut(pctx, filepath.Join(dir, r.Name), "rev-parse", "HEAD")
		if err != nil {
			unlock()
			return nil, fmt.Errorf("repository %s: %w", r.Name, err)
		}
		t.before[r.Name] = head
	}
	return t, nil
}

// Records in res what the step did to each repository, and gives the
// workspace back. A repository git can't read any more, such as one the step
// deleted, is left out.
func (t *takenWorkspace) done(res *runResult) {
	defer t.release()
	// The step's context may be over; reading the repositories doesn't take long.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, r := range t.repos {
		dir := filepath.Join(t.dir, r.Name)
		c := repoCommits{Name: r.Name, Before: t.before[r.Name], Commits: []string{}}
		var err error
		if c.After, err = gitOut(ctx, dir, "rev-parse", "HEAD"); err != nil {
			continue
		}
		if c.After != c.Before {
			log, err := gitOut(ctx, dir, "log", "--format=%h %s", fmt.Sprintf("--max-count=%d", maxListedCommits), c.Before+".."+c.After)
			if err == nil && log != "" {
				c.Commits = strings.Split(log, "\n")
			}
		}
		status, err := gitOut(ctx, dir, "status", "--porcelain")
		c.Uncommitted = err == nil && status != ""
		res.Commits = append(res.Commits, c)
	}
}

// Adds the repository to the workspace as a worktree on the branch, unless
// it is there. The branch starts from the base branch as just fetched, or
// stays as it is if it exists already. The worktree is added beside its
// place and moved in once ready, so one cut short leaves nothing that looks
// ready.
func checkout(ctx context.Context, dir, branch string, r repository) error {
	dest := filepath.Join(dir, r.Name)
	if _, err := os.Stat(dest); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	repo := sharedRepo(r.URL)
	defer lockPath(repo)()
	start, err := fetchShared(ctx, repo, r.URL, r.Base)
	if err != nil {
		return err
	}
	// Git names the worktree after this, so the shared repository says
	// whose it is: run-42-myrepo.
	adding := filepath.Join(dir, ".adding")
	tmp := filepath.Join(adding, filepath.Base(dir)+"-"+r.Name)
	// What an earlier try left behind.
	os.RemoveAll(tmp)
	if err := gitOK(ctx, repo, "worktree", "prune"); err != nil {
		return err
	}
	if err := os.MkdirAll(adding, 0o777); err != nil {
		return err
	}
	defer os.Remove(adding)
	args := []string{"worktree", "add", "--quiet"}
	if gitOK(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil {
		args = append(args, "--", tmp, branch)
	} else {
		args = append(args, "-b", branch, "--", tmp, start)
	}
	if err := gitOK(ctx, repo, args...); err != nil {
		return err
	}
	return gitOK(ctx, repo, "worktree", "move", tmp, dest)
}

// Where the repository behind url is shared from.
func sharedRepo(url string) string {
	sum := sha256.Sum256([]byte(url))
	return filepath.Join(workspacesRoot, ".repos", hex.EncodeToString(sum[:8])+".git")
}

// Brings the base branch, or else the default one, up to date in the shared
// repository, making it the first time, and returns where runs' branches
// start from. Its own branches are the runs'; the repository's are under
// origin/, so fetching never touches the runs' work. The caller holds its
// lock.
func fetchShared(ctx context.Context, repo, url, base string) (string, error) {
	if _, err := os.Stat(repo); errors.Is(err, fs.ErrNotExist) {
		if err := makeShared(ctx, repo, url); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if base == "" {
		// "ref: refs/heads/main\tHEAD" and the commit on the next line.
		out, err := gitOut(ctx, repo, "ls-remote", "--symref", "origin", "HEAD")
		if err != nil {
			return "", err
		}
		ref, _, _ := strings.Cut(strings.TrimPrefix(out, "ref: refs/heads/"), "\t")
		if ref == "" || strings.HasPrefix(ref, "ref:") {
			return "", errors.New("the repository has no default branch")
		}
		base = ref
	}
	// The branches fetched are the ones runs started from, each kept up to date.
	spec := "+refs/heads/" + base + ":refs/remotes/origin/" + base
	specs, _ := gitOut(ctx, repo, "config", "--get-all", "remote.origin.fetch")
	if !slices.Contains(strings.Split(specs, "\n"), spec) {
		if err := gitOK(ctx, repo, "config", "--add", "remote.origin.fetch", spec); err != nil {
			return "", err
		}
	}
	if err := gitOK(ctx, repo, "fetch", "--quiet", "--prune", "--no-tags", "--depth=1", "origin"); err != nil {
		return "", err
	}
	return "origin/" + base, nil
}

// Makes the shared repository, beside its place and moved in once ready.
func makeShared(ctx context.Context, repo, url string) error {
	if err := os.MkdirAll(filepath.Dir(repo), 0o777); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(repo), ".repo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, args := range [][]string{
		{"init", "--quiet", "--bare"},
		{"remote", "add", "--no-tags", "origin", "--", url},
		// No branches until a run starts from one; see fetchShared.
		{"config", "--unset-all", "remote.origin.fetch"},
		// Worktrees commit while others fetch; nothing collects garbage under them.
		{"config", "gc.auto", "0"},
	} {
		if err := gitOK(ctx, tmp, args...); err != nil {
			return err
		}
	}
	return os.Rename(tmp, repo)
}

// Runs git, failing with what it printed to stderr. It never asks for
// credentials: the token comes from the credential helper or not at all.
func gitOK(ctx context.Context, dir string, args ...string) error {
	_, err := gitOut(ctx, dir, args...)
	return err
}

// Runs git and returns what it printed, trimmed.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s", args[0], msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Deletes the workspaces of runs, run-<id> and the ones of its items,
// run-<id>-*, with their branches.
func deleteWorkspaces(runs []int64) error {
	for _, id := range runs {
		name := fmt.Sprintf("run-%d", id)
		matches, err := filepath.Glob(filepath.Join(workspacesRoot, name+"-*"))
		if err != nil {
			return err
		}
		for _, dir := range append(matches, filepath.Join(workspacesRoot, name)) {
			unlock := lockPath(dir)
			err := deleteWorkspace(dir)
			unlock()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Removes each worktree in the workspace from its shared repository, with its
// branch, then the workspace.
func deleteWorkspace(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		wt := filepath.Join(dir, e.Name())
		// A worktree's .git is a file pointing into its shared repository.
		if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
			continue
		}
		repo, err := gitOut(ctx, wt, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			continue
		}
		branch, _ := gitOut(ctx, wt, "branch", "--show-current")
		unlock := lockPath(repo)
		err = gitOK(ctx, repo, "worktree", "remove", "--force", wt)
		if err == nil && branch != "" {
			err = gitOK(ctx, repo, "branch", "--quiet", "-D", "--", branch)
		}
		unlock()
		if err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}
