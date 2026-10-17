package tests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// A persistent object only the process taking it generates is carried from
// run to run, as a cursor: each run starts with what the last run to end
// done stored of it, and before any did, the process runs without it. A
// process taking it after the one moving it on gets the new value.
func TestRunsCarryACursorOver(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Cursor")
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "cat $INPUTS/Cursor"})
	report := a.newProcess(f.ID, map[string]any{"name": "Report", "kind": kindScript, "script": "cat $INPUTS/Cursor"})
	cursor := a.newObject(f.ID, "Cursor")
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", cursor.ID), map[string]any{"persistent": true}, http.StatusOK, nil)
	a.input(fetch.ID, cursor.ID)
	a.output(fetch.ID, cursor.ID)
	a.input(report.ID, cursor.ID)
	a.output(report.ID, a.newObject(f.ID, "Summary").ID)
	a.startAt(f.ID, fetch.ID)

	var mu sync.Mutex
	var fetched, reported [][]scriptInput
	a.worker.onScript("Fetch", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		fetched = append(fetched, run.Inputs)
		if len(fetched) == 3 {
			return runResult{Error: "GitHub is down"}
		}
		return runResult{OK: true, Output: fmt.Sprintf(`{"data": "offset %d"}`, len(fetched))}
	})
	a.worker.onScript("Report", func(_ context.Context, run scriptRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, run.Inputs)
		return runResult{OK: true, Output: `{"data": "reported"}`}
	})

	for i, want := range []string{runDone, runDone, runFailed, runDone} {
		if d := a.finish(a.startRun(f.ID).ID); d.Status != want {
			t.Fatalf("run %d %s, want %s; steps %s", i+1, d.Status, want, describeSteps(d))
		}
	}

	mu.Lock()
	defer mu.Unlock()
	// The first run has nothing to carry over, and the failed third doesn't
	// move the cursor on.
	for i, want := range []string{"", "offset 1", "offset 2", "offset 2"} {
		got := fetched[i]
		switch {
		case want == "" && len(got) != 0:
			t.Errorf("run %d's Fetch was given %+v, want nothing", i+1, got)
		case want != "" && (len(got) != 1 || got[0].Name != "Cursor" || !strings.Contains(got[0].Content, want)):
			t.Errorf("run %d's Fetch was given %+v, want the cursor at %s", i+1, got, want)
		}
	}
	// Report runs after Fetch, on what it moved the cursor to.
	for i, want := range []string{"offset 1", "offset 2", "offset 4"} {
		if got := reported[i]; len(got) != 1 || !strings.Contains(got[0].Content, want) {
			t.Errorf("Report's run %d was given %+v, want the cursor at %s", i+1, got, want)
		}
	}
	if len(reported) != 3 {
		t.Errorf("Report ran %d times, want 3: not in the failed run", len(reported))
	}
}
