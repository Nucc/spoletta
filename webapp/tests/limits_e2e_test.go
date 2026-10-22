package tests

// What runs and people spend on models, and the limits admins set: a run
// that spends what it may is stopped and fails saying so, and a person who
// spent what they may this month has their agents' calls refused.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Each call to the test provider takes 30 tokens, gives 7, reads 1000 from
// the cache and writes 200 to it, at the catalog's price of the model:
// 90 + 105 + 300 + 750 millionths of a dollar.
const callMicros = 1245

type limitsBody struct {
	RunSpend       float64 `json:"runSpend"`
	UserMonthSpend float64 `json:"userMonthSpend"`
}

func setLimits(t *testing.T, a *app, l limitsBody) {
	t.Helper()
	a.call("PUT", "/api/limits", l, http.StatusOK, nil)
	t.Cleanup(func() { secretsDB(t).Exec("DELETE FROM limits") })
}

// Calls the provider through the proxy, returning the status and what a
// refusal said.
func callModel(llm *llmAccess, provider string) (int, string) {
	req, _ := http.NewRequest("POST", llm.URL+"/"+provider+"/v1/messages", strings.NewReader(`{"model": "fake-model"}`))
	req.Header.Set("X-Api-Key", llm.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestARunThatSpendsWhatItMayIsStopped(t *testing.T) {
	a := newApp(t)
	a.worker.quiet = true
	// Two calls' worth, less a little: the second call passes the limit.
	setLimits(t, a, limitsBody{RunSpend: float64(2*callMicros-100) / 1e6})
	f := a.newFlow("Spender")
	a.startAt(f.ID, a.agent(f.ID, "Spend", "Spend.").ID)
	var statuses []int
	stopped := false
	a.worker.onAgent("Spend", func(ctx context.Context, run agentRun) runResult {
		for range 2 {
			s, _ := callModel(run.LLM, "fireworks")
			statuses = append(statuses, s)
		}
		// The engine stops the step, as it would a pi still going.
		select {
		case <-ctx.Done():
			stopped = true
		case <-time.After(20 * time.Second):
		}
		after, _ := callModel(run.LLM, "fireworks")
		statuses = append(statuses, after)
		return submitted(data("spent"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runFailed {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if !stopped || fmt.Sprint(statuses) != "[200 200 403]" {
		t.Errorf("stopped %v; the calls answered %v", stopped, statuses)
	}
	logLineWith(t, a.runLogs(d.ID, 0), "stopped: it spent $0.0025 on its models, and may spend $0.0024")
	if e := onlyStep(t, d, "Spend").Error; e != "it was stopped, as its run spent $0.0025 on its models, and may spend $0.0024" {
		t.Errorf("the step failed with %q", e)
	}
	if l := a.audit(fmt.Sprintf("kind:run.overspent run:%d", d.ID), 1); len(l.Events) != 1 {
		t.Errorf("the audit log has %+v", l.Events)
	}

	// What the run spent shows in its panel, with what it may.
	var detail struct {
		Spent      float64 `json:"spent"`
		SpendLimit float64 `json:"spendLimit"`
	}
	a.call("GET", fmt.Sprintf("/api/runs/%d", d.ID), nil, http.StatusOK, &detail)
	if detail.Spent != 2*callMicros/1e6 || detail.SpendLimit != float64(2*callMicros-100)/1e6 {
		t.Errorf("the run's panel says it spent $%v of $%v", detail.Spent, detail.SpendLimit)
	}

	// And the settings list the month's costliest runs, the costliest first.
	var spending struct {
		Runs []struct {
			Run   int64   `json:"run"`
			Spent float64 `json:"spent"`
		} `json:"runs"`
	}
	a.call("GET", "/api/spending", nil, http.StatusOK, &spending)
	if len(spending.Runs) == 0 || spending.Runs[0].Spent < 2*callMicros/1e6 {
		t.Errorf("the month's runs: %+v", spending.Runs)
	}
	for i := 1; i < len(spending.Runs); i++ {
		if spending.Runs[i].Spent > spending.Runs[i-1].Spent {
			t.Errorf("the month's runs aren't the costliest first: %+v", spending.Runs)
		}
	}
}

// A flow may hold its runs to less than the instance does, and a run's
// follow-ups count toward it; a call when nothing is left is refused
// saying the run is out of budget, which pi doesn't try again.
func TestAFlowsLimitAndRefusedCalls(t *testing.T) {
	a := newApp(t)
	a.worker.quiet = true
	setLimits(t, a, limitsBody{RunSpend: 1})
	f := a.newFlow("Thrifty")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories":  []map[string]any{{"url": "https://github.com/acme/api"}},
		"runSpendLimit": float64(2*callMicros-100) / 1e6,
	}}, http.StatusOK, nil)
	a.refused("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{"runSpendLimit": -1}}, http.StatusBadRequest, "isn't an amount of dollars")
	a.startAt(f.ID, a.agent(f.ID, "Spend", "Spend.").ID)
	var calls []string
	a.worker.onAgent("Spend", func(ctx context.Context, run agentRun) runResult {
		status, body := callModel(run.LLM, "fireworks")
		calls = append(calls, fmt.Sprintf("%d %s", status, body))
		if run.Resume {
			status, body = callModel(run.LLM, "fireworks")
			calls = append(calls, fmt.Sprintf("%d %s", status, body))
			return runResult{OK: true, Output: "again"}
		}
		return submitted(data("spent"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}

	// A follow-up spends the rest: its call passes the flow's limit, the
	// run's tokens end, and the next call reaches nothing.
	step := onlyStep(t, d, "Spend")
	followUp := func(n int) {
		a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, step.ID), map[string]any{"message": "Again."}, http.StatusCreated, nil)
		a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
			s := onlyStep(t, d, "Spend")
			return len(s.FollowUps) == n && s.FollowUps[n-1].Status != stepActive
		})
	}
	followUp(1)
	logLineWith(t, a.runLogs(d.ID, 0), "stopped: it spent $0.0025 on its models, and may spend $0.0024")
	// Then one more has nothing left to spend.
	followUp(2)
	if len(calls) != 5 || !strings.HasPrefix(calls[1], "200 ") || !strings.HasPrefix(calls[2], "403 ") {
		t.Fatalf("the calls answered %q", calls)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	refusal := strings.TrimPrefix(calls[3], "403 ")
	json.Unmarshal([]byte(refusal), &body)
	if body.Error.Type != "out_of_budget" || body.Error.Message != "out of budget: the run has spent $0.0025 on its models, and may spend $0.0024" {
		t.Errorf("the second follow-up's call got %s", calls[3])
	}
	logLineWith(t, a.runLogs(d.ID, 0), "a call to the model provider fireworks was refused: out of budget")
}

// Someone who spent what they may this month has their calls refused, and
// sees what they spent; others' spending is for admins.
func TestAPersonsMonthlyLimit(t *testing.T) {
	a, p := usersApp(t)
	a.worker.quiet = true
	usersSignIn(a, p, usersAda)
	setLimits(t, a, limitsBody{UserMonthSpend: float64(2*callMicros-100) / 1e6})
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	a.refused("PUT", "/api/limits", limitsBody{}, http.StatusForbidden, "only an admin")
	f := a.newFlow("Bob's")
	a.startAt(f.ID, a.agent(f.ID, "Spend", "Spend.").ID)
	var statuses []int
	var refusal string
	a.worker.onAgent("Spend", func(ctx context.Context, run agentRun) runResult {
		for range 3 {
			s, body := callModel(run.LLM, "fireworks")
			statuses, refusal = append(statuses, s), body
		}
		return submitted(data("spent"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if fmt.Sprint(statuses) != "[200 200 403]" || !strings.Contains(refusal, "out of budget: its person has spent $0.0025 on models this month, and may spend $0.0024") {
		t.Errorf("the calls answered %v, the last %s", statuses, refusal)
	}

	var spending struct {
		People []struct {
			Name  string  `json:"name"`
			Spent float64 `json:"spent"`
			Calls int64   `json:"calls"`
		} `json:"people"`
	}
	a.call("GET", "/api/spending", nil, http.StatusOK, &spending)
	if len(spending.People) != 1 || spending.People[0].Name != "Bob" || spending.People[0].Calls != 2 || spending.People[0].Spent != 2*callMicros/1e6 {
		t.Errorf("Bob sees %+v", spending.People)
	}
}

// A person's runs have no more steps going at once than an admin lets
// them; the rest wait their turn, taking none of the engine's slots.
func TestAPersonsStepsAtOnce(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	a.call("PUT", "/api/limits", map[string]any{"userParallelSteps": 1}, http.StatusOK, nil)
	t.Cleanup(func() { secretsDB(t).Exec("DELETE FROM limits") })
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	f := a.newFlow("Bob's two")
	seed := a.agent(f.ID, "Seed", "Seed.")
	s := a.newObject(f.ID, "S")
	a.output(seed.ID, s.ID)
	a.startAt(f.ID, seed.ID)
	a.worker.onAgent("Seed", handsIn(data("seed")))
	var mu sync.Mutex
	going, most := 0, 0
	for _, name := range []string{"W0", "W1"} {
		w := a.agent(f.ID, name, "Work.")
		a.input(w.ID, s.ID)
		a.worker.onAgent(name, func(ctx context.Context, _ agentRun) runResult {
			mu.Lock()
			going++
			most = max(most, going)
			mu.Unlock()
			time.Sleep(200 * time.Millisecond)
			mu.Lock()
			going--
			mu.Unlock()
			return runResult{OK: true, Output: "done"}
		})
	}
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone || len(d.Steps) != 3 || most != 1 {
		t.Fatalf("run %s with %d steps, at most %d at once; want 1: %s", d.Status, len(d.Steps), most, describeSteps(d))
	}
	logLineWith(t, a.runLogs(d.ID, 0), " waits: its person's runs have as many steps going as they may at once, 1")
}

// The worker is told the most disk a run may take, which it checks after
// each step; a step that leaves its run over it fails saying so.
func TestTheWorkerIsToldARunsDiskLimit(t *testing.T) {
	a := newApp(t)
	a.call("PUT", "/api/limits", map[string]any{"runDiskMb": 5}, http.StatusOK, nil)
	t.Cleanup(func() { secretsDB(t).Exec("DELETE FROM limits") })
	a.refused("PUT", "/api/limits", map[string]any{"runDiskMb": -1}, http.StatusBadRequest, "isn't a number of MB")
	fixes, _ := gitTokensFlows(a, "https://github.com/acme/api")
	tooBig := "the run takes 6.0 MB of disk, more than the 5.0 MB it may"
	a.worker.onScript("Check", func(context.Context, scriptRun) runResult { return runResult{Error: tooBig} })
	d := a.finish(a.startRun(fixes.ID).ID)
	if d.Status != runFailed || onlyStep(t, d, "Check").Error != tooBig {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if fix, check := a.worker.runsOf("Fix")[0].DiskLimit, a.worker.scriptRuns[0].DiskLimit; fix != 5<<20 || check != 5<<20 {
		t.Errorf("the steps may take %d and %d bytes", fix, check)
	}
}
