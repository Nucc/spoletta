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
	"strings"
	"sync"
	"time"
)

// A workspace is a directory of git checkouts a run's agents and scripts
// work in, all on the run's branch. It is kept on a volume, so it outlives
// the step that made it, a review waiting for days and the worker restarting.
// Each repository is cloned when a step first needs it, from a mirror of it
// that all workspaces share, so cloning one many times stays cheap.
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
// a mirror is fetched by one clone at a time.
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

// Gets the workspace ready for a step and takes it: each repository the step
// works on is checked out on the branch. Returns the workspace's directory
// and the function giving it back, which the caller calls once the step is
// done.
func takeWorkspace(ctx context.Context, ws *workspaceSpec) (string, func(), error) {
	if !safeID.MatchString(ws.ID) {
		return "", nil, fmt.Errorf("invalid workspace %q", ws.ID)
	}
	if err := gitOK(ctx, "", "check-ref-format", "--branch", ws.Branch); err != nil {
		return "", nil, fmt.Errorf("invalid branch %q", ws.Branch)
	}
	for _, r := range ws.Repositories {
		if !safeName.MatchString(r.Name) {
			return "", nil, fmt.Errorf("invalid repository name %q", r.Name)
		}
		if !strings.HasPrefix(r.URL, "https://") {
			return "", nil, fmt.Errorf("repository %s: only https URLs are supported", r.Name)
		}
		if r.Base != "" {
			if err := gitOK(ctx, "", "check-ref-format", "--branch", r.Base); err != nil {
				return "", nil, fmt.Errorf("repository %s: invalid base branch %q", r.Name, r.Base)
			}
		}
	}
	dir := filepath.Join(workspacesRoot, ws.ID)
	unlock := lockPath(dir)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		unlock()
		return "", nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	for _, r := range ws.Repositories {
		if err := checkout(pctx, dir, ws.Branch, r); err != nil {
			unlock()
			return "", nil, fmt.Errorf("repository %s: %w", r.Name, err)
		}
	}
	return dir, unlock, nil
}

// Clones the repository into the workspace on the branch, unless it is there.
// It is cloned beside its place and moved in once ready, so a clone cut short
// leaves nothing that looks ready.
func checkout(ctx context.Context, dir, branch string, r repository) error {
	dest := filepath.Join(dir, r.Name)
	if _, err := os.Stat(dest); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	mirror, err := updateMirror(ctx, r.URL)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, ".clone-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// Cloned from the mirror, then pointed at the repository itself, so the
	// checkout fetches, and someday pushes, where it came from.
	args := []string{"clone", "--quiet", "--no-hardlinks"}
	if r.Base != "" {
		args = append(args, "--branch", r.Base)
	}
	args = append(args, "--", mirror, tmp)
	if err := gitOK(ctx, "", args...); err != nil {
		return err
	}
	for _, a := range [][]string{
		{"remote", "set-url", "origin", "--", r.URL},
		{"checkout", "--quiet", "-B", branch},
	} {
		if err := gitOK(ctx, tmp, a...); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dest)
}

// Brings the shared mirror of a repository up to date, cloning it the first
// time, and returns where it is.
func updateMirror(ctx context.Context, url string) (string, error) {
	sum := sha256.Sum256([]byte(url))
	mirror := filepath.Join(workspacesRoot, ".mirrors", hex.EncodeToString(sum[:8])+".git")
	defer lockPath(mirror)()
	if _, err := os.Stat(mirror); err == nil {
		return mirror, gitOK(ctx, mirror, "fetch", "--quiet", "--prune", "origin")
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o777); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(mirror), ".mirror-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	// Only branches and tags: a mirror would fetch every pull request too,
	// which on a busy GitHub repository is most of it.
	if err := gitOK(ctx, "", "clone", "--quiet", "--bare", "--", url, tmp); err != nil {
		return "", err
	}
	if err := gitOK(ctx, tmp, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*"); err != nil {
		return "", err
	}
	return mirror, os.Rename(tmp, mirror)
}

// Runs git, failing with what it printed to stderr. It never asks for
// credentials: the token comes from the credential helper or not at all.
func gitOK(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("git %s: %s", args[0], msg)
		}
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	return nil
}

// Deletes the workspaces of runs: run-<id> and the ones of its items,
// run-<id>-*.
func deleteWorkspaces(runs []int64) error {
	for _, id := range runs {
		name := fmt.Sprintf("run-%d", id)
		matches, err := filepath.Glob(filepath.Join(workspacesRoot, name+"-*"))
		if err != nil {
			return err
		}
		for _, dir := range append(matches, filepath.Join(workspacesRoot, name)) {
			unlock := lockPath(dir)
			err := os.RemoveAll(dir)
			unlock()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
