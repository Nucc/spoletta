package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A flow's agents and scripts can work in a workspace: a checkout of each of
// the flow's repositories, all on a branch of the run's own. Every process of
// a run works on the same branch, picking up where the one before left off;
// each item of a list a flow process runs for gets a branch of its own, so
// items running at once don't meet. The worker keeps the workspaces; see
// worker/workspace.go.

type flowSettings struct {
	Repositories []repository `json:"repositories"`
	// Runs' branches are named <prefix>/run-<id>; empty uses flow/<the flow's name>.
	BranchPrefix string `json:"branchPrefix"`
	// The most, in dollars, a run of the flow may spend on its models, 0
	// for the instance's limit; the instance's is kept to if lower. See
	// limits.go.
	RunSpendLimit float64 `json:"runSpendLimit,omitempty"`
}

type repository struct {
	// Its checkout's directory in the workspace, and what processes pick it by.
	Name string `json:"name"`
	// Only https, which the worker's credentials cover.
	URL string `json:"url"`
	// The branch runs' branches start from; empty for the default branch.
	Base string `json:"base"`
	// Paths in it, directories or files, its checkouts leave out, such as a
	// cache of gems: not fetched into a step's checkout, nor there, yet kept
	// in the branch as they are. See sparse in worker/workspace.go.
	Exclude []string `json:"exclude,omitempty"`
}

// Stored as JSON; flows from before settings have none.
func (s *flowSettings) Scan(v any) error {
	*s = flowSettings{}
	switch b := v.(type) {
	case nil:
	case []byte:
		if err := json.Unmarshal(b, s); err != nil {
			return err
		}
	case string:
		if err := json.Unmarshal([]byte(b), s); err != nil {
			return err
		}
	default:
		return errors.New("flow settings: unexpected type")
	}
	if s.Repositories == nil {
		s.Repositories = []repository{}
	}
	return nil
}

func (s flowSettings) Value() (driver.Value, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

var (
	repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	// What git takes in a branch name, roughly; the worker checks it properly.
	branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

func validBranch(b string) bool {
	return branchName.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.HasSuffix(b, ".lock")
}

// The repository's name from its URL: the last part of its path, without .git.
func nameFromURL(url string) string {
	last := url[strings.LastIndex(strings.TrimRight(url, "/"), "/")+1:]
	return strings.TrimSuffix(strings.TrimRight(last, "/"), ".git")
}

// Tidies the settings, naming repositories after their URLs unless named,
// and says what is wrong with them, if anything.
func (s *flowSettings) check() string {
	if _, err := dollarMicros(s.RunSpendLimit); err != nil {
		return "what a run may spend: " + err.Error()
	}
	s.BranchPrefix = strings.Trim(strings.TrimSpace(s.BranchPrefix), "/")
	if s.BranchPrefix != "" && !validBranch(s.BranchPrefix) {
		return "the branch prefix isn't a valid branch name"
	}
	if s.Repositories == nil {
		s.Repositories = []repository{}
	}
	names := map[string]bool{}
	for i := range s.Repositories {
		r := &s.Repositories[i]
		r.URL, r.Base, r.Name = strings.TrimSpace(r.URL), strings.TrimSpace(r.Base), strings.TrimSpace(r.Name)
		if !strings.HasPrefix(r.URL, "https://") || strings.ContainsAny(r.URL, " \t\n") {
			return fmt.Sprintf("%q isn't an https URL", r.URL)
		}
		if r.Name == "" {
			r.Name = nameFromURL(r.URL)
		}
		if !repoName.MatchString(r.Name) {
			return fmt.Sprintf("%q isn't a valid repository name: letters, digits, ., - and _ only", r.Name)
		}
		if names[r.Name] {
			return fmt.Sprintf("two repositories are named %q", r.Name)
		}
		names[r.Name] = true
		if r.Base != "" && !validBranch(r.Base) {
			return fmt.Sprintf("%q isn't a valid branch name", r.Base)
		}
		if msg := checkExclude(r); msg != "" {
			return msg
		}
	}
	return ""
}

// The most paths a repository's checkouts may leave out.
const maxExcludes = 50

var excludeSegment = regexp.MustCompile(`^[A-Za-z0-9._@+-]+$`)

// Tidies the paths the repository's checkouts leave out, written with or
// without slashes around them, each once, and says what is wrong with them:
// plain paths in the repository, never patterns, as the worker checks too.
func checkExclude(r *repository) string {
	var paths []string
	for _, p := range r.Exclude {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" || slices.Contains(paths, p) {
			continue
		}
		ok := !strings.HasPrefix(p, "-")
		for _, s := range strings.Split(p, "/") {
			ok = ok && excludeSegment.MatchString(s) && s != "." && s != ".." && !strings.EqualFold(s, ".git")
		}
		if !ok {
			return fmt.Sprintf("%s: %q isn't a path to leave out: letters, digits, ., -, _, @ and + between slashes, no wildcards", r.Name, p)
		}
		paths = append(paths, p)
	}
	if len(paths) > maxExcludes {
		return fmt.Sprintf("%s: at most %d paths can be left out", r.Name, maxExcludes)
	}
	r.Exclude = paths
	return ""
}

// The prefix of the flow's runs' branches.
func (s flowSettings) prefix(flowName string) string {
	if s.BranchPrefix != "" {
		return s.BranchPrefix
	}
	slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(flowName), "-"), "-")
	if slug == "" {
		slug = "flow"
	}
	return "flow/" + slug
}

// The workspace a walk's agents and scripts work in.
type runWorkspace struct {
	// Names its directory in the worker: run-<id> for a run, run-<id>-step-<id>
	// for an item of a list that runs without a run of its own.
	id string
	// The run working in it: the run it is named after, or for an item
	// without a run of its own, the run the item is in.
	run    int64
	branch string
	repos  []repository
	// For a re-run: the commit each repository's branch starts from, by
	// name; see reruns.go.
	starts map[string]string
}

// What the worker takes for a step: the workspace with the repositories the
// process works on.
type workspaceSpec struct {
	ID     string `json:"id"`
	Branch string `json:"branch"`
	// The run and step working in it, which the worker's commit of what the
	// step left uncommitted names.
	Run          int64           `json:"run,omitempty"`
	Step         int64           `json:"step,omitempty"`
	Repositories []workspaceRepo `json:"repositories"`
}

// A repository as the worker takes it: for a re-run, with the commit its
// branch starts from instead of its base.
type workspaceRepo struct {
	repository
	Start string `json:"start,omitempty"`
}

// The workspace for a process with these settings, or nil if it works on no
// repository.
func (ws *runWorkspace) forProcess(s processSettings) *workspaceSpec {
	if ws == nil {
		return nil
	}
	var repos []workspaceRepo
	for _, r := range ws.repos {
		if s.Repositories == nil || contains(*s.Repositories, r.Name) {
			repos = append(repos, workspaceRepo{r, ws.starts[r.Name]})
		}
	}
	if len(repos) == 0 {
		return nil
	}
	return &workspaceSpec{ID: ws.id, Branch: ws.branch, Run: ws.run, Repositories: repos}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Tells the agent where its repositories are and which branch it is on.
func withWorkspace(prompt string, ws *workspaceSpec) string {
	if ws == nil {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	fmt.Fprintf(&b, "\n\nYou work in a workspace: your working directory holds a checkout of each repository below, on the branch %q.\n", ws.Branch)
	for _, r := range ws.Repositories {
		base := r.Base
		if base == "" {
			base = "its default branch"
		}
		if r.Start != "" {
			base = "commit " + r.Start[:min(len(r.Start), 12)] + ", where the run this one re-runs had got to"
		}
		fmt.Fprintf(&b, "- %s/: %s, branched from %s\n", r.Name, r.URL, base)
		if len(r.Exclude) > 0 {
			fmt.Fprintf(&b, "  Left out of the checkout, though still in the branch: %s. They aren't deleted, so don't restore or commit them.\n", strings.Join(r.Exclude, ", "))
		}
	}
	b.WriteString("The checkouts hold only the latest commit of the branch they started from, with what this run committed on top: " +
		"older history and tags aren't there, so don't look for them.\n")
	// pi loads context files from the working directory up, which the checkouts are below.
	b.WriteString("Before working in a repository, read its AGENTS.md or CLAUDE.md if it has one, else its README, for how to work in it. " +
		"Other processes of this run work on the same branch, before and after you, so what they committed is there. " +
		"Commit your changes to this branch as you go; whatever you leave uncommitted is committed for you once you are done. " +
		"Don't switch branches, and don't push.\n")
	return b.String()
}

// What a step did to a repository it worked on.
type repoCommits struct {
	Name string `json:"name"`
	// The commit it started from and the one it left; the same if it committed nothing.
	Before string `json:"before"`
	After  string `json:"after"`
	// The commits it made, newest first, as "<short hash> <subject>"; only
	// the latest ones of many.
	Commits []string `json:"commits"`
	// Whether it left changes it didn't commit.
	Uncommitted bool `json:"uncommitted"`
}

// What two tries of a step did together: from where the first started to
// where the second left off.
func mergeCommits(first, second []repoCommits) []repoCommits {
	byName := map[string]repoCommits{}
	for _, c := range first {
		byName[c.Name] = c
	}
	var merged []repoCommits
	for _, c := range second {
		if f, ok := byName[c.Name]; ok {
			c.Before = f.Before
			c.Commits = append(c.Commits, f.Commits...)
		}
		merged = append(merged, c)
	}
	return merged
}

// Records what a step did to its repositories, if it worked on any.
func recordCommits(ctx context.Context, db execer, stepID int64, commits []repoCommits) error {
	if len(commits) == 0 {
		return nil
	}
	b, err := json.Marshal(commits)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "UPDATE run_steps SET commits = ? WHERE id = ?", b, stepID)
	return err
}

// Records the branch a step works on.
func recordBranch(ctx context.Context, db execer, stepID int64, ws *workspaceSpec) error {
	if ws == nil {
		return nil
	}
	_, err := db.ExecContext(ctx, "UPDATE run_steps SET branch = ? WHERE id = ?", ws.Branch, stepID)
	return err
}

// Has the worker release the workspaces of runs that are over, run-<id> and
// run-<id>-*, to save their space: it commits what they hold uncommitted
// and removes the checkouts, keeping the branches and the agents' sessions,
// so a follow-up or a re-run makes them again; see worker/workspace.go. A
// workspace a step or follow-up is using stays, and is released once that
// is over. A worker that can't be reached keeps them; the next start of the
// webapp releases them.
func releaseWorkspaces(workerURL string, runs []int64) {
	if len(runs) == 0 {
		return
	}
	go func() {
		if err := askRelease(workerURL, runs); err != nil {
			log.Printf("release workspaces of %d runs: %v", len(runs), err)
		}
	}()
}

func askRelease(workerURL string, runs []int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var res struct {
		Error string `json:"error"`
	}
	return callWorker(ctx, http.MethodPost, workerURL+"/workspaces/release", map[string][]int64{"runs": runs}, &res)
}

// The run a workspace is named after: 42 for run-42 and run-42-step-7.
var workspaceRun = regexp.MustCompile(`^run-(\d+)(?:-step-\d+)?$`)

// Releases the workspaces of the run whose workspace this is, once that run
// is over, such as after a follow-up in it.
func releaseIfOver(ctx context.Context, db *sql.DB, workerURL, workspace string) {
	m := workspaceRun.FindStringSubmatch(workspace)
	if m == nil {
		return
	}
	id, _ := strconv.ParseInt(m[1], 10, 64)
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM runs WHERE id = ?", id).Scan(&status); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("release the workspace %s: %v", workspace, err)
		}
		return
	}
	if status != runRunning {
		releaseWorkspaces(workerURL, []int64{id})
	}
}

// Releases the workspaces of every run that is over, as the webapp starts:
// those that ended while it couldn't reach the worker, or before workspaces
// were released. The worker may be starting too, so it is asked a few times.
func releaseEnded(db *sql.DB, workerURL string) {
	ctx := context.Background()
	ids := map[int64]bool{}
	err := scanAll(ctx, db, "SELECT DISTINCT SUBSTRING_INDEX(branch, '/', -1) FROM run_steps WHERE branch <> ''", func(rows *sql.Rows) error {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if m := workspaceRun.FindStringSubmatch(name); m != nil {
			id, _ := strconv.ParseInt(m[1], 10, 64)
			ids[id] = true
		}
		return nil
	})
	if err != nil {
		log.Printf("find the workspaces to release: %v", err)
		return
	}
	var ended []int64
	err = scanAll(ctx, db, "SELECT id FROM runs WHERE status <> ?", func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if ids[id] {
			ended = append(ended, id)
		}
		return nil
	}, runRunning)
	if err != nil || len(ended) == 0 {
		if err != nil {
			log.Printf("find the workspaces to release: %v", err)
		}
		return
	}
	for try := 0; ; try++ {
		if err = askRelease(workerURL, ended); err == nil || try == 5 {
			break
		}
		time.Sleep(30 * time.Second)
	}
	if err != nil {
		log.Printf("release the workspaces of %d runs that are over: %v", len(ended), err)
	}
}

// Has the worker delete the workspaces of runs. The runs are gone either way,
// so a worker that can't be reached only leaves the checkouts behind.
func dropWorkspaces(workerURL string, runs []int64) {
	if len(runs) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var res struct {
			Error string `json:"error"`
		}
		if err := callWorker(ctx, http.MethodPost, workerURL+"/workspaces/delete", map[string][]int64{"runs": runs}, &res); err != nil {
			log.Printf("delete workspaces of %d runs: %v", len(runs), err)
		}
	}()
}
