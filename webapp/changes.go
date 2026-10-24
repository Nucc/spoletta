package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
	"time"
)

// What a step changed in its repositories, for a person to look over beside
// its conversation: each file between the commit it started from and the
// one it left, with its patch, which the worker reads from where it keeps
// the runs' branches (worker/diff.go). A step's follow-ups carry on from
// where it left off, so what they did is counted with it; another step
// committing on the branch in between splits it in two, as then the span
// would show that step's work too.
type repoChanges struct {
	Name        string `json:"name"`
	Before      string `json:"before"`
	After       string `json:"after"`
	Uncommitted bool   `json:"uncommitted"`
	// Empty when nothing was committed.
	Files []fileChange `json:"files"`
	// How many files changed past the ones listed.
	More int `json:"more"`
	// Why the changes couldn't be read, such as the run's branches having
	// been deleted.
	Error string `json:"error,omitempty"`
}

// A file a step changed, as the worker gives it.
type fileChange struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	// added, modified, deleted, renamed, copied or type changed.
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	// Its part of the diff; empty with omitted set when too large to show.
	Patch   string `json:"patch"`
	Omitted bool   `json:"omitted,omitempty"`
}

// How long the worker may take over a step's diffs.
const changesTimeout = time.Minute

// What the step and its ended follow-ups did to each repository, in the
// order they did it, spans that follow on from each other joined.
func spanCommits(tries ...[]repoCommits) []repoChanges {
	var spans []repoChanges
	last := map[string]int{}
	for _, commits := range tries {
		for _, c := range commits {
			if i, ok := last[c.Name]; ok && spans[i].After == c.Before {
				spans[i].After, spans[i].Uncommitted = c.After, c.Uncommitted
				continue
			}
			last[c.Name] = len(spans)
			spans = append(spans, repoChanges{Name: c.Name, Before: c.Before, After: c.After, Uncommitted: c.Uncommitted, Files: []fileChange{}})
		}
	}
	return spans
}

func registerChangeRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	// The step's changes, by repository; none while it hasn't ended.
	mux.HandleFunc("GET /api/runs/{run}/steps/{id}/changes", func(w http.ResponseWriter, r *http.Request) {
		runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
		id, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var branch string
		var commits []byte
		err := db.QueryRowContext(r.Context(), "SELECT branch, commits FROM run_steps WHERE id = ? AND run_id = ?", id, runID).Scan(&branch, &commits)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "step not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		tries, err := stepTries(r.Context(), db, id, commits)
		if err != nil {
			serverError(w, err)
			return
		}
		spans := spanCommits(tries...)
		if spans == nil {
			spans = []repoChanges{}
		}
		ctx, cancel := context.WithTimeout(r.Context(), changesTimeout)
		defer cancel()
		for i := range spans {
			s := &spans[i]
			if s.Before == s.After || s.After == "" {
				continue
			}
			var res struct {
				Files []fileChange `json:"files"`
				More  int          `json:"more"`
				Error string       `json:"error"`
			}
			// The workspace is named after the last part of its branch; see
			// flowRun.workspace.
			err := callWorker(ctx, http.MethodPost, workerURL+"/workspaces/diff", map[string]string{
				"workspace": path.Base(branch), "repository": s.Name, "before": s.Before, "after": s.After,
			}, &res)
			switch {
			case err != nil:
				s.Error = "the worker couldn't be asked: " + err.Error()
			case res.Error != "":
				s.Error = res.Error
			default:
				if res.Files != nil {
					s.Files = res.Files
				}
				s.More = res.More
			}
		}
		writeJSON(w, http.StatusOK, spans)
	})
}

// What the step's tries did, its own and then each follow-up's that ended,
// in order.
func stepTries(ctx context.Context, db *sql.DB, stepID int64, commits []byte) ([][]repoCommits, error) {
	var own []repoCommits
	if commits != nil {
		if err := json.Unmarshal(commits, &own); err != nil {
			return nil, err
		}
	}
	tries := [][]repoCommits{own}
	rows, err := db.QueryContext(ctx,
		"SELECT commits FROM run_step_followups WHERE step_id = ? AND status <> ? AND commits IS NOT NULL ORDER BY attempt", stepID, stepActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var cs []repoCommits
		if err := json.Unmarshal(b, &cs); err != nil {
			return nil, err
		}
		tries = append(tries, cs)
	}
	return tries, rows.Err()
}
