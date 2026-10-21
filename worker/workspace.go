package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
// share, and a workspace's checkout of it is a clone of the run's branch
// there, made when a step first needs it: only the latest commit is
// copied, never the history. The shared repository holds only the latest
// commit of each branch runs start from, without tags: a big repository's
// whole history would take long to fetch, and agents work on what is there
// now. The runs' branches live in the shared repository too, so one run's
// branch can start from another's.
//
// A checkout is a clone rather than a worktree of the shared repository so
// that a sandbox (see sandbox.go) can be given it alone: a worktree's git
// lives in the shared repository. Its origin is the repository's own URL,
// for agents to push to. Once a step is over, what it left uncommitted is
// committed in a sandbox too, and the worker brings the branch back into
// the shared repository by fetching from the checkout. The worker never
// runs any other git in a checkout a step had: its config, hooks and
// attributes are the step's to write, and git runs what they say, such as
// core.fsmonitor or a filter, for whoever runs it there; fetching from a
// repository runs none of them. What the worker knows of a workspace, its
// branch and its checkouts' repositories, it keeps in .meta, beside the
// workspaces, where no step reaches.
type workspaceSpec struct {
	// Names the workspace's directory, such as run-42.
	ID     string `json:"id"`
	Branch string `json:"branch"`
	// The run and step working in it, which the commits of what a step left
	// uncommitted name; see commitLeftovers.
	Run  int64 `json:"run,omitempty"`
	Step int64 `json:"step,omitempty"`
	// The repositories the step works on; others already in the workspace stay.
	Repositories []repository `json:"repositories"`
}

type repository struct {
	// The checkout's directory in the workspace.
	Name string `json:"name"`
	URL  string `json:"url"`
	// The branch the run's branch starts from; empty for the default branch.
	Base string `json:"base"`
	// For a run re-running another: the commit its branch starts from instead,
	// where the other run's branch was as the process it re-runs from started.
	// Runs' branches live in the shared repository, so it is there as long as
	// that run's branch is.
	Start string `json:"start,omitempty"`
}

// Where workspaces live, a volume of their own.
var workspacesRoot = envOr("WORKSPACES_DIR", "/data/workspaces")

// How long getting a workspace ready may take, apart from the step's own
// timeout: a first clone of a large repository takes a while.
const prepareTimeout = 15 * time.Minute

var (
	safeID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// A full commit id, SHA-1 or SHA-256.
	commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
)

// Per-path locks: a workspace is used by one step at a time, so agents
// sharing a branch don't trip over each other's edits and git's index lock;
// a shared repository fetches and adds worktrees one at a time.
var locks = struct {
	sync.Mutex
	m map[string]*sync.Mutex
}{m: map[string]*sync.Mutex{}}

func lockPath(p string) func() {
	l := pathLock(p)
	l.Lock()
	return l.Unlock
}

// Locks the path unless someone holds it already; false then.
func tryLockPath(p string) (func(), bool) {
	l := pathLock(p)
	return l.Unlock, l.TryLock()
}

func pathLock(p string) *sync.Mutex {
	locks.Lock()
	defer locks.Unlock()
	l, ok := locks.m[p]
	if !ok {
		l = &sync.Mutex{}
		locks.m[p] = l
	}
	return l
}

// A workspace a step has taken, or its scratch directory.
type takenWorkspace struct {
	dir   string
	spec  *workspaceSpec
	repos []repository
	// Each repository's commit as the step started, by name.
	before map[string]string
	// The repositories whose checkouts were made again from their branch, the
	// workspace having been released since the run last worked in it.
	recreated []string
	// What the step's commit of what it left uncommitted says it was: its
	// process and, for an agent, its pi session.
	process, session string
	release          func()
	// The step's log, for what goes wrong giving the workspace back.
	log *stepLogger
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
		if r.Start != "" && !commitID.MatchString(r.Start) {
			return nil, fmt.Errorf("repository %s: invalid start commit %q", r.Name, r.Start)
		}
	}
	dir := filepath.Join(workspacesRoot, ws.ID)
	unlock := lockPath(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		unlock()
		return nil, err
	}
	if err := ownForSandbox(dir, false); err != nil {
		unlock()
		return nil, err
	}
	meta, err := readMeta(ws.ID)
	if err == nil {
		meta.Branch = ws.Branch
		for _, r := range ws.Repositories {
			meta.Repositories[r.Name] = r.URL
		}
		err = writeMeta(ws.ID, meta)
	}
	if err != nil {
		unlock()
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	t := &takenWorkspace{dir: dir, spec: ws, repos: ws.Repositories, before: map[string]string{}, release: unlock}
	for _, r := range ws.Repositories {
		recreated, err := checkout(pctx, dir, ws.Branch, r)
		if err != nil {
			unlock()
			return nil, fmt.Errorf("repository %s: %w", r.Name, err)
		}
		if recreated {
			t.recreated = append(t.recreated, r.Name)
		}
		head, err := gitOut(pctx, sharedRepo(r.URL), "rev-parse", "refs/heads/"+ws.Branch)
		if err != nil {
			unlock()
			return nil, fmt.Errorf("repository %s: %w", r.Name, err)
		}
		t.before[r.Name] = head
	}
	return t, nil
}

// Records in res what the step did to each repository, and gives the
// workspace back: what the step left uncommitted is committed, and the
// branch brought back into the shared repository, where what it did is
// read. A repository whose checkout is gone, such as one the step deleted,
// is left out.
func (t *takenWorkspace) done(res *runResult) {
	defer t.release()
	// The step's context may be over; this doesn't take long.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, r := range t.repos {
		dest := filepath.Join(t.dir, r.Name)
		c := repoCommits{Name: r.Name, Before: t.before[r.Name], Commits: []string{}}
		subject := "Commit what " + t.process + " left uncommitted"
		if t.process == "" {
			subject = "Commit what the step left uncommitted"
		}
		uncommitted, err := commitLeftovers(ctx, dest, subject, t.trailers())
		if err != nil {
			t.log.errorf("commit what was left uncommitted in %s: %v", r.Name, err)
		}
		repo := sharedRepo(r.URL)
		if err := bringBack(ctx, dest, repo, t.spec.Branch); err != nil {
			if _, statErr := os.Lstat(dest); statErr == nil {
				t.log.errorf("bring back what the step did in %s: %v", r.Name, err)
			}
			continue
		}
		if c.After, err = gitOut(ctx, repo, "rev-parse", "refs/heads/"+t.spec.Branch); err != nil {
			continue
		}
		if c.After != c.Before {
			log, err := gitOut(ctx, repo, "log", "--format=%h %s", fmt.Sprintf("--max-count=%d", maxListedCommits), c.Before+".."+c.After)
			if err == nil && log != "" {
				c.Commits = strings.Split(log, "\n")
			}
		}
		c.Uncommitted = uncommitted
		res.Commits = append(res.Commits, c)
	}
}

// The trailers of a step's commit of what it left uncommitted, which tie it to
// the run, the step and the agent's session.
func (t *takenWorkspace) trailers() []string {
	var ts []string
	if t.spec != nil && t.spec.Run != 0 {
		ts = append(ts, fmt.Sprintf("Spoletta-Run: %d", t.spec.Run))
	}
	if t.spec != nil && t.spec.Step != 0 {
		ts = append(ts, fmt.Sprintf("Spoletta-Step: %d", t.spec.Step))
	}
	if t.session != "" {
		ts = append(ts, "Pi-Session: "+t.session)
	}
	return ts
}

// Commits whatever the checkout holds that isn't committed, ignored files
// aside, so the branch has all of a step's work and the checkout can be
// removed and made again from it without losing any. Nothing to commit is
// no commit. Hooks don't run: the checkout is the run's, not a person's.
// It runs in a sandbox of its own, offline, seeing only the checkout, as
// the step's own git would; says whether something is still left
// uncommitted, as a change git can't commit.
func commitLeftovers(ctx context.Context, dir, subject string, trailers []string) (bool, error) {
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return false, fmt.Errorf("%s isn't a checkout any more", filepath.Base(dir))
	}
	const script = `subject=$1; shift
n=$#
for t; do set -- "$@" --trailer "$t"; done
shift $n
if [ -n "$(git status --porcelain)" ]; then
	git add --all && git -c commit.gpgsign=false commit --quiet --no-verify -m "$subject" "$@" || exit 1
fi
[ -z "$(git status --porcelain)" ] || echo uncommitted`
	var stdout, stderr bytes.Buffer
	spec := sandboxSpec{
		Cmd: append([]string{"sh", "-c", script, "sh", subject}, trailers...),
		Dir: dir, Env: childEnv(), Mounts: []mount{{Path: dir}}, Offline: true,
	}
	err := steps.run(ctx, spec, &stdout, &stderr)
	uncommitted := strings.Contains(stdout.String(), "uncommitted")
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return uncommitted, errors.New(msg)
		}
	}
	return uncommitted, err
}

// Brings the branch as the checkout has it into the shared repository, by
// fetching from it, which runs nothing the checkout's config or hooks say.
// The checkout and its git must be the directories they were, not links
// leading to another's, and it may borrow no objects from elsewhere. The
// caller isn't holding the shared repository's lock.
func bringBack(ctx context.Context, dest, repo, branch string) error {
	gitDir := filepath.Join(dest, ".git")
	for _, p := range []string{dest, gitDir, filepath.Join(gitDir, "objects"), filepath.Join(gitDir, "objects", "info")} {
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) && p != dest && p != gitDir {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s isn't a directory", strings.TrimPrefix(p, filepath.Dir(dest)+"/"))
		}
	}
	for _, f := range []string{"alternates", "http-alternates"} {
		if err := os.Remove(filepath.Join(gitDir, "objects", "info", f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	defer lockPath(repo)()
	return gitOK(ctx, repo, "fetch", "--quiet", "--no-tags", "file://"+dest, "+refs/heads/"+branch+":refs/heads/"+branch)
}

// Clones the run's branch of the repository into the workspace, unless it
// is there. The branch starts from the base branch as just fetched, or the
// repository's start commit if it has one, or stays as it is if it exists
// already: then nothing is fetched, as the checkout is only made again, the
// workspace having been released. Says whether it was. The clone is made
// outside the workspace, which steps write, and moved in once ready, so one
// cut short leaves nothing that looks ready. A worktree from before
// checkouts were clones is released first, and made again as one.
func checkout(ctx context.Context, dir, branch string, r repository) (bool, error) {
	dest := filepath.Join(dir, r.Name)
	if fi, err := os.Lstat(dest); err == nil {
		switch {
		case fi.IsDir() && legacyWorktree(dest):
			if err := releaseLegacy(ctx, dest); err != nil {
				return false, err
			}
		case fi.IsDir():
			return false, nil
		default:
			// Not a checkout, whatever a step left in its place.
			if err := os.RemoveAll(dest); err != nil {
				return false, err
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	repo := sharedRepo(r.URL)
	unlock := lockPath(repo)
	exists := false
	if _, err := os.Stat(repo); err == nil {
		exists = gitOK(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil
	}
	if !exists {
		start := r.Start
		if start != "" {
			if gitOK(ctx, repo, "rev-parse", "--verify", "--quiet", r.Start+"^{commit}") != nil {
				unlock()
				return false, fmt.Errorf("the commit %s the run starts from is gone, along with the branch of the run it re-runs", r.Start[:7])
			}
		} else {
			var err error
			if start, err = fetchShared(ctx, repo, r.URL, r.Base); err != nil {
				unlock()
				return false, err
			}
		}
		if err := gitOK(ctx, repo, "branch", "--quiet", branch, start); err != nil {
			unlock()
			return false, err
		}
	}
	unlock()
	adding := filepath.Join(workspacesRoot, ".adding")
	if err := os.MkdirAll(adding, 0o700); err != nil {
		return false, err
	}
	tmp := filepath.Join(adding, filepath.Base(dir)+"-"+r.Name)
	// What an earlier try left behind.
	os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	if err := gitOK(ctx, "", "clone", "--quiet", "--depth=1", "--no-tags", "--single-branch", "--branch", branch, "--", "file://"+repo, tmp); err != nil {
		return false, err
	}
	if err := gitOK(ctx, tmp, "remote", "set-url", "origin", r.URL); err != nil {
		return false, err
	}
	if err := ownForSandbox(tmp, true); err != nil {
		return false, err
	}
	return exists, os.Rename(tmp, dest)
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
	if err := os.MkdirAll(filepath.Dir(repo), 0o700); err != nil {
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
// credentials: the step's token comes from the credential helper, if ctx
// carries one (see git_credentials.go), or not at all.
func gitOK(ctx context.Context, dir string, args ...string) error {
	_, err := gitOut(ctx, dir, args...)
	return err
}

// Runs git and returns what it printed, trimmed.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = childEnv(gitCredentialEnv(ctx)...)
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

// The pi sessions of a workspace's steps are kept beside it, out of the
// agents' way, as long as it is: pi finds a session again only from the
// directory it was started in.
func sessionsDir(workspaceID string) string {
	return filepath.Join(workspacesRoot, ".sessions", workspaceID)
}

// What pi takes as a session id.
var safeSessionID = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

// The pi options that keep the run's conversation in its session, or keep
// none. Carrying on a session that isn't there fails, rather than starting
// one that knows nothing, which pi would.
func sessionArgs(run runRequest) ([]string, error) {
	if run.SessionID == "" {
		if run.Resume {
			return nil, errors.New("there is no session to carry on")
		}
		return []string{"--no-session"}, nil
	}
	if run.Workspace == nil || !safeID.MatchString(run.Workspace.ID) {
		return nil, errors.New("a session needs a workspace")
	}
	if !safeSessionID.MatchString(run.SessionID) {
		return nil, fmt.Errorf("invalid session %q", run.SessionID)
	}
	dir := sessionsDir(run.Workspace.ID)
	if run.Resume {
		// pi names a session's file <time>_<id>.jsonl.
		matches, err := filepath.Glob(filepath.Join(dir, "*_"+run.SessionID+".jsonl"))
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, errors.New("the agent's session is gone, so it can't be carried on")
		}
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return []string{"--session-dir", dir, "--session-id", run.SessionID}, nil
}

// Releases the workspaces of runs that are over, run-<id> and the ones of its
// items, run-<id>-*, to save their space: what each checkout holds
// uncommitted is committed, the branch brought back into its shared
// repository, and the checkouts are removed, but the branches stay, and the
// pi sessions beside them, so a follow-up or a re-run makes the checkouts
// again from the branches; see checkout. A workspace a step is using is
// left as it is; what asked for the release asks again once it is over.
// Returns the workspaces released.
func releaseWorkspaces(runs []int64) ([]string, error) {
	var released []string
	for _, id := range runs {
		for _, dir := range workspacesOf(id) {
			unlock, ok := tryLockPath(dir)
			if !ok {
				continue
			}
			ok, err := releaseWorkspace(dir)
			unlock()
			if err != nil {
				return released, err
			}
			if ok {
				released = append(released, filepath.Base(dir))
			}
		}
	}
	return released, nil
}

// The workspaces of a run: run-<id> and its items', run-<id>-*.
func workspacesOf(run int64) []string {
	name := fmt.Sprintf("run-%d", run)
	matches, _ := filepath.Glob(filepath.Join(workspacesRoot, name+"-*"))
	return append(matches, filepath.Join(workspacesRoot, name))
}

// Commits what each checkout in the workspace holds uncommitted, brings its
// branch back and removes it, then the workspace, unless a checkout
// couldn't be: it then stays, with the workspace. False if there was
// nothing to release.
func releaseWorkspace(dir string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	meta, err := readMeta(filepath.Base(dir))
	if err != nil {
		return false, err
	}
	kept := false
	for _, e := range entries {
		wt := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			continue
		}
		if legacyWorktree(wt) {
			if err := releaseLegacy(ctx, wt); err != nil {
				log.Printf("release %s: %v; keeping it", wt, err)
				kept = true
			}
			continue
		}
		url, ok := meta.Repositories[e.Name()]
		if !ok {
			continue
		}
		_, err := commitLeftovers(ctx, wt, "Commit what was left uncommitted as the workspace was released", nil)
		if err == nil {
			err = bringBack(ctx, wt, sharedRepo(url), meta.Branch)
		}
		if err != nil {
			log.Printf("release %s: %v; keeping it", wt, err)
			kept = true
			continue
		}
		if err := os.RemoveAll(wt); err != nil {
			return false, err
		}
	}
	if kept {
		return false, nil
	}
	return true, os.RemoveAll(dir)
}

// Deletes the workspaces of runs, run-<id> and the ones of its items,
// run-<id>-*, with their branches and sessions.
func deleteWorkspaces(runs []int64) error {
	for _, id := range runs {
		for _, dir := range workspacesOf(id) {
			unlock := lockPath(dir)
			err := deleteWorkspace(dir)
			if err == nil {
				err = os.RemoveAll(sessionsDir(filepath.Base(dir)))
			}
			unlock()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Deletes the workspace's branch from each repository it had a checkout
// of, then the workspace and what the worker kept of it.
func deleteWorkspace(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	id := filepath.Base(dir)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if wt := filepath.Join(dir, e.Name()); e.IsDir() && legacyWorktree(wt) {
				if err := deleteLegacy(ctx, wt); err != nil {
					return err
				}
			}
		}
	}
	meta, err := readMeta(id)
	if err != nil {
		return err
	}
	if meta.Branch != "" {
		for _, url := range meta.Repositories {
			repo := sharedRepo(url)
			unlock := lockPath(repo)
			if gitOK(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+meta.Branch) == nil {
				err = gitOK(ctx, repo, "branch", "--quiet", "-D", "--", meta.Branch)
			}
			unlock()
			if err != nil {
				return err
			}
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Remove(metaPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// What the worker keeps of a workspace, outside it: the branch its
// checkouts are on, and each one's repository, by name.
type workspaceMeta struct {
	Branch       string            `json:"branch"`
	Repositories map[string]string `json:"repositories"`
}

func metaPath(id string) string {
	return filepath.Join(workspacesRoot, ".meta", id+".json")
}

// What the worker keeps of the workspace; nothing for one it never kept.
func readMeta(id string) (workspaceMeta, error) {
	m := workspaceMeta{Repositories: map[string]string{}}
	b, err := os.ReadFile(metaPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if m.Repositories == nil {
		m.Repositories = map[string]string{}
	}
	return m, err
}

// Keeps what the worker knows of the workspace, written beside its place
// and moved in.
func writeMeta(id string, m workspaceMeta) error {
	if err := os.MkdirAll(filepath.Dir(metaPath(id)), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := metaPath(id) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, metaPath(id))
}

// Whether the checkout is a worktree of its shared repository, as
// checkouts were before they were clones: its .git is a file pointing there.
func legacyWorktree(wt string) bool {
	fi, err := os.Lstat(filepath.Join(wt, ".git"))
	return err == nil && fi.Mode().IsRegular()
}

// Commits what a worktree from before holds uncommitted and removes it,
// keeping its branch. These were written by steps that ran as the worker
// itself, before sandboxes, so the worker's git runs in them.
func releaseLegacy(ctx context.Context, wt string) error {
	repo, err := gitOut(ctx, wt, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	if status, err := gitOut(ctx, wt, "status", "--porcelain"); err != nil {
		return err
	} else if status != "" {
		if err := gitOK(ctx, wt, "add", "--all"); err != nil {
			return err
		}
		if err := gitOK(ctx, wt, "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", "Commit what was left uncommitted as the workspace was released"); err != nil {
			return err
		}
	}
	defer lockPath(repo)()
	return gitOK(ctx, repo, "worktree", "remove", "--force", wt)
}

// Removes a worktree from before from its shared repository, with its branch.
func deleteLegacy(ctx context.Context, wt string) error {
	repo, err := gitOut(ctx, wt, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil
	}
	branch, _ := gitOut(ctx, wt, "branch", "--show-current")
	defer lockPath(repo)()
	if err := gitOK(ctx, repo, "worktree", "remove", "--force", wt); err != nil {
		return err
	}
	if branch != "" {
		return gitOK(ctx, repo, "branch", "--quiet", "-D", "--", branch)
	}
	return nil
}
