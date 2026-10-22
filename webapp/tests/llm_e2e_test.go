package tests

// Agents reach their models through the webapp's proxy with a token of
// their step's, which the provider never sees, and which ends with the step.

import (
	"context"
	"net/http"
	"testing"
)

func TestAgentsReachTheirModelsThroughTheProxy(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	t.Cleanup(func() { db.Exec("DELETE FROM model_keys WHERE env = 'OPENAI_API_KEY'") })
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-proxied"}, http.StatusOK, nil)
	f := a.newFlow("Proxied")
	a.startAt(f.ID, a.agent(f.ID, "Write", "Write.").ID)
	var during int
	a.worker.onAgent("Write", func(_ context.Context, run agentRun) runResult {
		// As the agent works, its token reaches the providers granted, not others.
		during, _ = reachModel(run.LLM, "mistral")
		return submitted(data("done"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	run := a.worker.runsOf("Write")[0]
	if run.LLM == nil || run.LLM.URL != a.url+"/llm" || run.LLM.Token == "" {
		t.Fatalf("the agent got %+v", run.LLM)
	}
	if run.Reached["openai"] != "sk-proxied" || run.Reached["fireworks"] != "gw-from-env" {
		t.Errorf("the agent reached %v", run.Reached)
	}
	if during != http.StatusForbidden {
		t.Errorf("a provider the step wasn't granted answered %d", during)
	}

	// Once the step is over, its token reaches nothing.
	if status, key := reachModel(run.LLM, "openai"); status != http.StatusForbidden || key != "" {
		t.Errorf("after the step, its token got %d %q", status, key)
	}

	// What each call took and cost is recorded as the step's: the model's
	// price is the catalog's, $3 a million taken in, $15 given out, $0.30
	// read from the cache and $3.75 written to it.
	step := onlyStep(t, d, "Write")
	var calls, in, out, read, written, cost, priced int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cache_read_tokens), 0),
		COALESCE(SUM(cache_write_tokens), 0), COALESCE(SUM(cost_micros), 0), COALESCE(SUM(priced), 0) FROM llm_usage WHERE step_id = ? AND run_id = ?`,
		step.ID, d.ID).Scan(&calls, &in, &out, &read, &written, &cost, &priced); err != nil {
		t.Fatal(err)
	}
	if want := int64(len(run.Reached)); calls != want || in != 30*want || out != 7*want || read != 1000*want || written != 200*want || priced != want {
		t.Errorf("recorded %d calls taking %d, giving %d, reading %d and writing %d, %d priced; want %d", calls, in, out, read, written, priced, want)
	}
	if want := int64(len(run.Reached)) * (30*3 + 7*15 + 1000*3/10 + 200*375/100); cost != want {
		t.Errorf("the calls cost %d millionths of a dollar, want %d", cost, want)
	}
}
