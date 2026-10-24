package tests

// What a step changed in its repositories, read from the worker by the
// commits the step recorded; see changes.go.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type fileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
	Omitted   bool   `json:"omitted"`
}

type repoChanges struct {
	Name        string       `json:"name"`
	Before      string       `json:"before"`
	After       string       `json:"after"`
	Uncommitted bool         `json:"uncommitted"`
	Files       []fileChange `json:"files"`
	More        int          `json:"more"`
	Error       string       `json:"error"`
}

// The worker is asked for each repository's diff from where the step
// started to where its follow-up left off, in the step's workspace; a
// repository it committed nothing to isn't asked about, and what the worker
// can't read is told by repository.
func TestStepChanges(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Fixes")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}, {"url": "https://github.com/acme/docs"}, {"url": "https://github.com/acme/web"}},
	}}, http.StatusOK, nil)
	fix := a.agent(f.ID, "Fix", "Fix it.")
	a.output(fix.ID, a.newObject(f.ID, "Diff").ID)
	a.startAt(f.ID, fix.ID)
	commit := func(n int) string { return strings.Repeat(fmt.Sprint(n), 40) }
	a.worker.onAgent("Fix", func(_ context.Context, run agentRun) runResult {
		if run.Resume {
			return runResult{OK: true, Output: "done", Commits: []repoCommits{{Name: "api", Before: commit(2), After: commit(3), Commits: []string{}, Uncommitted: true}}}
		}
		out := submitted(data("fixed"))
		out.Commits = []repoCommits{
			{Name: "api", Before: commit(1), After: commit(2), Commits: []string{}},
			{Name: "docs", Before: commit(5), After: commit(5), Commits: []string{}},
			{Name: "web", Before: commit(6), After: commit(7), Commits: []string{}},
		}
		return out
	})
	a.worker.mu.Lock()
	a.worker.diffReply = func(req map[string]string) map[string]any {
		if req["repository"] == "web" {
			return map[string]any{"error": "commit 7777777 isn't in the repository any more"}
		}
		return map[string]any{"files": []map[string]any{
			{"path": "main.go", "status": "modified", "additions": 2, "deletions": 1, "patch": "diff --git a/main.go b/main.go\n"},
		}, "more": 0}
	}
	a.worker.mu.Unlock()

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	step := onlyStep(t, d, "Fix")
	path := fmt.Sprintf("/api/runs/%d/steps/%d/changes", d.ID, step.ID)
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, step.ID), map[string]any{"message": "Tidy up."}, http.StatusCreated, nil)
	a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
		s := onlyStep(t, d, "Fix")
		return len(s.FollowUps) == 1 && s.FollowUps[0].Status == stepDone
	})

	var changes []repoChanges
	a.call("GET", path, nil, http.StatusOK, &changes)
	if len(changes) != 3 {
		t.Fatalf("changes %+v, want api, docs and web", changes)
	}
	api, docs, web := changes[0], changes[1], changes[2]
	if api.Name != "api" || api.Before != commit(1) || api.After != commit(3) || !api.Uncommitted || len(api.Files) != 1 || api.Files[0].Path != "main.go" || api.Files[0].Additions != 2 {
		t.Errorf("api %+v, want from the step's start to the follow-up's end", api)
	}
	if docs.Name != "docs" || len(docs.Files) != 0 || docs.Error != "" {
		t.Errorf("docs %+v, want nothing changed", docs)
	}
	if web.Name != "web" || !strings.Contains(web.Error, "isn't in the repository") {
		t.Errorf("web %+v, want the worker's error", web)
	}
	a.worker.mu.Lock()
	diffs := a.worker.diffs
	a.worker.mu.Unlock()
	if len(diffs) != 2 {
		t.Fatalf("worker asked for %v, want api's and web's", diffs)
	}
	workspace := fmt.Sprintf("run-%d", d.ID)
	if want := (map[string]string{"workspace": workspace, "repository": "api", "before": commit(1), "after": commit(3)}); fmt.Sprint(diffs[0]) != fmt.Sprint(want) {
		t.Errorf("asked for %v, want %v", diffs[0], want)
	}

	a.refused("GET", fmt.Sprintf("/api/runs/%d/steps/%d/changes", d.ID+1, step.ID), nil, http.StatusNotFound, "not found")
}
