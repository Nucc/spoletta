package tests

import (
	"context"
	"strings"
	"testing"
)

// The webapp asks the worker with WORKER_TOKEN, which the worker checks on
// every request; a webapp with another token gets nothing run, and its
// steps fail saying why.
func TestWorkerTakesRequestsOnlyWithItsToken(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Asked with the token")
	draft := a.agent(f.ID, "Draft", "Write a ticket.")
	a.output(draft.ID, a.newObject(f.ID, "Ticket").ID)
	a.startAt(f.ID, draft.ID)
	a.worker.onAgent("Draft", handsIn(data("written")))
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("run %s, want done; steps %s", d.Status, describeSteps(d))
	}
	a.worker.mu.Lock()
	refused := a.worker.refused
	a.worker.mu.Unlock()
	if refused != 0 {
		t.Errorf("the worker refused %d requests from the webapp with its token", refused)
	}

	other := startApp(t, a.worker.url, "WORKER_TOKEN=not-the-workers")
	other.worker = a.worker
	g := other.newFlow("Asked with another token")
	p := other.agent(g.ID, "Draft", "Write a ticket.")
	other.output(p.ID, other.newObject(g.ID, "Ticket").ID)
	other.startAt(g.ID, p.ID)
	a.worker.onAgent("Draft", func(context.Context, agentRun) runResult {
		t.Error("the worker ran an agent for a webapp without its token")
		return runResult{OK: true}
	})
	d := other.finish(other.startRun(g.ID).ID)
	if s := onlyStep(t, d, "Draft"); s.Status != stepError || !strings.Contains(s.Error, "status 401") || !strings.Contains(s.Error, "WORKER_TOKEN") {
		t.Errorf("Draft step = %s %q, want refused by the worker", s.Status, s.Error)
	}
}
