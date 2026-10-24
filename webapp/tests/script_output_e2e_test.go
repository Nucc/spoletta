package tests

// What a script prints is kept as the worker streams it, readable while the
// script still runs; see script_output.go.

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type outputChunk struct {
	ID     int64  `json:"id"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// A running script's output is there before it ends, stdout and stderr in
// the order printed, and ?after= gives only what came since, and only under
// its own run; its log lines arrive as they come too, and its stdout is
// still its output.
func TestScriptOutputWhileItRuns(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Printing")
	count := a.newProcess(f.ID, map[string]any{"name": "Count", "kind": kindScript, "script": "echo counting; wc -c"})
	a.output(count.ID, a.newObject(f.ID, "Size").ID)
	a.startAt(f.ID, count.ID)
	a.worker.mu.Lock()
	a.worker.scriptOutput["Count"] = [][2]string{{"stdout", "counting\n"}, {"stderr", "warning: slow\n"}}
	a.worker.mu.Unlock()
	release := make(chan struct{})
	a.worker.onScript("Count", func(ctx context.Context, _ scriptRun) runResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return runResult{OK: true, Output: `{"data": 13}`}
	})

	rn := a.startRun(f.ID)
	step := a.waitStep(rn.ID, "Count", stepActive)
	path := fmt.Sprintf("/api/runs/%d/steps/%d/output", rn.ID, step.ID)
	var chunks []outputChunk
	deadline := time.Now().Add(10 * time.Second)
	for len(chunks) < 2 && time.Now().Before(deadline) {
		a.call("GET", path, nil, http.StatusOK, &chunks)
		time.Sleep(20 * time.Millisecond)
	}
	if len(chunks) != 2 || chunks[0].Stream != "stdout" || chunks[0].Text != "counting\n" || chunks[1].Stream != "stderr" || chunks[1].Text != "warning: slow\n" {
		t.Fatalf("while it runs, output %+v", chunks)
	}
	logLineWith(t, a.runLogs(rn.ID, 0), "fake bash runs Count")
	var since []outputChunk
	a.call("GET", fmt.Sprintf("%s?after=%d", path, chunks[0].ID), nil, http.StatusOK, &since)
	if len(since) != 1 || since[0].ID != chunks[1].ID {
		t.Errorf("after the first, output %+v", since)
	}
	close(release)

	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if got := contentsOf(d, "Size"); len(got) != 1 {
		t.Errorf("Size %v, want what the script printed", got)
	}
	var other []outputChunk
	a.call("GET", fmt.Sprintf("/api/runs/%d/steps/%d/output", rn.ID+1, step.ID), nil, http.StatusOK, &other)
	if len(other) != 0 {
		t.Errorf("the step's output read as another run's: %+v", other)
	}
}
