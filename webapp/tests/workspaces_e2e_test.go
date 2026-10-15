package tests

// Workspaces are released once their run is over, and again after each
// follow-up, so the worker keeps only the branches; see workspace.go.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

// Waits for the webapp to ask the worker to release run's workspaces as
// often as want.
func waitReleased(t *testing.T, a *app, run int64, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.worker.mu.Lock()
		n := 0
		for _, r := range a.worker.released {
			if r == run {
				n++
			}
		}
		a.worker.mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d's workspaces were released %d times, want %d", run, n, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A run working on repositories has its workspace released once it ends,
// and again after a follow-up, which works in it under the step's name. A
// run without repositories has none to release.
func TestWorkspacesAreReleasedOnceOver(t *testing.T) {
	a := newApp(t)
	plain := a.newFlow("Plain")
	note := a.agent(plain.ID, "Note", "Write a note.")
	a.output(note.ID, a.newObject(plain.ID, "Note text").ID)
	a.startAt(plain.ID, note.ID)
	a.worker.onAgent("Note", handsIn(data("noted")))
	noRepos := a.finish(a.startRun(plain.ID).ID)

	f := a.newFlow("Fixes")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
	}}, http.StatusOK, nil)
	fix := a.agent(f.ID, "Fix", "Fix it.")
	a.output(fix.ID, a.newObject(f.ID, "Diff").ID)
	a.startAt(f.ID, fix.ID)
	a.worker.onAgent("Fix", func(_ context.Context, run agentRun) runResult {
		if run.Resume {
			return runResult{OK: true, Output: "pushed"}
		}
		return submitted(data("fixed"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	step := onlyStep(t, d, "Fix")
	if ws := a.worker.runsOf("Fix")[0].Workspace; ws == nil || ws.Run != d.ID || ws.Step != step.ID {
		t.Errorf("Fix worked in %+v, want one naming run %d and step %d", ws, d.ID, step.ID)
	}
	waitReleased(t, a, d.ID, 1)

	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, step.ID), map[string]any{"message": "Push it."}, http.StatusCreated, nil)
	a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
		s := onlyStep(t, d, "Fix")
		return len(s.FollowUps) == 1 && s.FollowUps[0].Status == stepDone
	})
	waitReleased(t, a, d.ID, 2)
	runs := a.worker.runsOf("Fix")
	if last := runs[len(runs)-1]; !last.Resume || last.Workspace == nil || last.Workspace.Step != step.ID {
		t.Errorf("the follow-up worked in %+v, want the step's workspace", last.Workspace)
	}

	a.worker.mu.Lock()
	defer a.worker.mu.Unlock()
	if slices.Contains(a.worker.released, noRepos.ID) {
		t.Errorf("released run %d's workspaces, which it has none of", noRepos.ID)
	}
}
