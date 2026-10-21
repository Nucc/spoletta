package tests

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A line of a run's log, as the Logs window gets it.
type runLogLine struct {
	ID      int64     `json:"id"`
	Step    *int64    `json:"step"`
	Source  string    `json:"source"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

func (a *app) runLogs(run int64, after int64) []runLogLine {
	a.t.Helper()
	var lines []runLogLine
	a.call("GET", fmt.Sprintf("/api/runs/%d/logs?after=%d", run, after), nil, http.StatusOK, &lines)
	return lines
}

// The line of the log holding text, or fails.
func logLineWith(t *testing.T, lines []runLogLine, text string) runLogLine {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l.Message, text) {
			return l
		}
	}
	var all []string
	for _, l := range lines {
		all = append(all, l.Source+" "+l.Level+": "+l.Message)
	}
	t.Fatalf("no line of the log holds %q:\n%s", text, strings.Join(all, "\n"))
	return runLogLine{}
}

// A run's log says when it started and who started it, each step starting
// and ending, what the worker logged of each, what went wrong, and how it
// finished; its lines go in order, and after gives only the newer ones.
func TestRunLogsTellWhatHappened(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	f := a.newFlow("Logged")
	draft := a.agent(f.ID, "Draft", "Write a ticket.")
	ticket := a.newObject(f.ID, "Ticket")
	count := a.newProcess(f.ID, map[string]any{"name": "Count", "kind": kindScript, "script": "wc -c < \"$INPUTS/Ticket\""})
	a.output(draft.ID, ticket.ID)
	a.input(count.ID, ticket.ID)
	a.startAt(f.ID, draft.ID)
	a.worker.onAgent("Draft", handsIn(data("Fix the login")))
	a.worker.onScript("Count", func(context.Context, scriptRun) runResult {
		return runResult{Error: "wc: no such file\n(exit status 1)", Log: []logLine{{Time: time.Now(), Level: "error", Message: "the script printed to stderr:\nwc: no such file"}}}
	})

	rn := a.startRun(f.ID)
	d := a.finish(rn.ID)
	if d.Status != runFailed {
		t.Fatalf("run %s, want failed", d.Status)
	}
	lines := a.runLogs(rn.ID, 0)
	started := logLineWith(t, lines, `started, a run of "Logged"`)
	if !strings.Contains(started.Message, "by Ada Lovelace") || started.Source != "webapp" || started.Level != "info" || started.Step != nil {
		t.Errorf("the run's start logged %+v", started)
	}
	ds := onlyStep(t, d, "Draft")
	pi := logLineWith(t, lines, "fake pi runs Draft")
	if pi.Source != "worker" || pi.Step == nil || *pi.Step != ds.ID {
		t.Errorf("the worker's line of Draft = %+v, want of step %d", pi, ds.ID)
	}
	logLineWith(t, lines, `agent "Draft", started`)
	logLineWith(t, lines, `"Draft", ended done`)
	cs := onlyStep(t, d, "Count")
	bash := logLineWith(t, lines, "fake bash runs Count")
	if bash.Step == nil || *bash.Step != cs.ID {
		t.Errorf("the worker's line of Count = %+v", bash)
	}
	if stderr := logLineWith(t, lines, "wc: no such file"); stderr.Level != "error" {
		t.Errorf("the script's stderr logged as %s", stderr.Level)
	}
	failed := logLineWith(t, lines, `"Count", failed`)
	if failed.Level != "error" || !strings.Contains(failed.Message, "(exit status 1)") {
		t.Errorf("Count's failure logged %+v", failed)
	}
	finished := lines[len(lines)-1]
	if !strings.HasPrefix(finished.Message, "finished: failed after ") || finished.Level != "error" {
		t.Errorf("the last line = %+v, want how the run finished", finished)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i].ID <= lines[i-1].ID {
			t.Fatalf("lines out of order: %+v", lines)
		}
	}
	if newer := a.runLogs(rn.ID, failed.ID); len(newer) == 0 || newer[0].ID <= failed.ID {
		t.Errorf("after %d gave %+v", failed.ID, newer)
	}
	a.refused("GET", "/api/runs/999999999/logs", nil, http.StatusNotFound, "run not found")
}

// A run started without anyone signing in says so, and a re-run names the
// run it re-runs.
func TestRunLogsOfReruns(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Logged again")
	draft := a.agent(f.ID, "Draft", "Write a ticket.")
	a.output(draft.ID, a.newObject(f.ID, "Ticket").ID)
	a.startAt(f.ID, draft.ID)
	a.worker.onAgent("Draft", handsIn(data("Fix the login")))
	rn := a.startRun(f.ID)
	d := a.finish(rn.ID)
	logLineWith(t, a.runLogs(rn.ID, 0), "as nobody signs in")

	var again run
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/rerun", rn.ID, onlyStep(t, d, "Draft").ID), map[string]any{}, http.StatusCreated, &again)
	a.finish(again.ID)
	logLineWith(t, a.runLogs(again.ID, 0), fmt.Sprintf("a re-run of run %d from Draft", rn.ID))
}

// As it starts the webapp logs what it did: the database it works on, its
// migrations, the worker answering, and where it listens; and that the
// worker doesn't answer, as a warning, without failing to start.
func TestWebappLogsItsStart(t *testing.T) {
	if testDB.skip != "" {
		t.Skip(testDB.skip)
	}
	fw := newFakeWorker(t)
	w, err := startWebapp(fw.url, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// The worker is asked once the webapp answers; give it a moment.
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(w.log.String(), "the worker answers") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	out := w.stop()
	for _, want := range []string{
		"starting in the development environment",
		"connected to the database " + testDB.name,
		"the database is at version",
		"the worker answers, offering 1 models, by default fake/model",
		"started; listening on 127.0.0.1:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the webapp didn't log %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, testDB.password) && testDB.password != "" {
		t.Errorf("the webapp logged the database's password:\n%s", out)
	}

	w, err = startWebapp("http://127.0.0.1:1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(w.log.String(), "doesn't answer") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if out := w.stop(); !strings.Contains(out, "WARNING: the worker at http://127.0.0.1:1 doesn't answer") {
		t.Errorf("the webapp didn't warn of the worker:\n%s", out)
	}
}
