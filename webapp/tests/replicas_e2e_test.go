package tests

// Several replicas of the webapp work on one database: whichever of them a
// step's or a person's request reaches, it is as if it reached the one
// running the step.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A step's tokens, granted by the replica running it, reach its models,
// its MCP servers and the internet through another replica too, as long
// as the step goes.
func TestTokensReachEveryReplica(t *testing.T) {
	a := newApp(t)
	other := startApp(t, a.worker.url)
	db := secretsDB(t)
	t.Cleanup(func() { db.Exec("DELETE FROM model_keys WHERE env = 'OPENAI_API_KEY'") })
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-replicated"}, http.StatusOK, nil)

	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		w.Header().Set("Content-Type", "text/event-stream")
		result := map[string]any{"authorization": r.Header.Get("Authorization")}
		if m.Method == "tools/list" {
			result["tools"] = []map[string]any{{"name": "search"}, {"name": "delete_everything"}}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", out)
	}))
	t.Cleanup(mcp.Close)
	// One whose tools the process doesn't pick, so it has them all, and
	// one it picks one tool of.
	var all, some struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "rp_all", "url": mcp.URL, "auth": "token"}, http.StatusCreated, &all)
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "rp_some", "url": mcp.URL, "auth": "token"}, http.StatusCreated, &some)
	for _, id := range []int64{all.ID, some.ID} {
		a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", id), map[string]any{"token": "mcp-key"}, http.StatusNoContent, nil)
	}

	f := a.newFlow("Replicated")
	use := a.agent(f.ID, "Use", "Use it all.")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", use.ID), map[string]any{"settings": map[string]any{
		"mcpServers": []int64{all.ID, some.ID}, "mcpTools": map[string][]string{fmt.Sprint(some.ID): {"search"}},
	}}, http.StatusOK, nil)
	a.startAt(f.ID, use.ID)
	var model, internet int
	var key string
	listed := map[int64]string{}
	a.worker.onAgent("Use", func(ctx context.Context, run agentRun) runResult {
		model, key = reachModel(&llmAccess{URL: other.url + "/llm", Token: run.LLM.Token}, "openai")
		for _, id := range []int64{all.ID, some.ID} {
			listed[id] = replicasMCP(ctx, other, id, run.MCPToken)
		}
		if run.Egress != nil {
			internet = throughEgress(t, other, run.Egress, "http://example.com/")
		}
		return submitted(data("used"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if model != http.StatusOK || key != "sk-replicated" {
		t.Errorf("through the other replica, the model answered %d with the key %q", model, key)
	}
	if s := listed[all.ID]; !strings.HasPrefix(s, "200 ") || !strings.Contains(s, "delete_everything") || !strings.Contains(s, "Bearer mcp-key") {
		t.Errorf("the MCP server whose tools weren't picked listed %s", s)
	}
	if s := listed[some.ID]; !strings.HasPrefix(s, "200 ") || !strings.Contains(s, `"search"`) || strings.Contains(s, "delete_everything") {
		t.Errorf("the MCP server with a tool picked listed %s", s)
	}
	// Refused for the host, which isn't allowed, not for the token.
	if internet != http.StatusForbidden {
		t.Errorf("through the other replica, the egress proxy answered %d", internet)
	}

	// Once the step is over, its tokens reach nothing at either.
	run := a.worker.runsOf("Use")[0]
	for _, at := range []*app{a, other} {
		if status, _ := reachModel(&llmAccess{URL: at.url + "/llm", Token: run.LLM.Token}, "openai"); status != http.StatusForbidden {
			t.Errorf("after the step, its model token got %d", status)
		}
		if s := replicasMCP(context.Background(), at, all.ID, run.MCPToken); !strings.HasPrefix(s, "403 ") {
			t.Errorf("after the step, its MCP token got %s", s)
		}
		if status := throughEgress(t, at, run.Egress, "http://example.com/"); status != http.StatusProxyAuthRequired {
			t.Errorf("after the step, its egress token got %d", status)
		}
	}
}

// A flow's assistant replies with its model reached through whichever
// replica its calls come to, not only the one it was asked at.
func TestAssistantReachesItsModelThroughEveryReplica(t *testing.T) {
	a := newApp(t)
	other := startApp(t, a.worker.url)
	db := secretsDB(t)
	t.Cleanup(func() { db.Exec("DELETE FROM model_keys WHERE env = 'OPENAI_API_KEY'") })
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-assistant"}, http.StatusOK, nil)
	f := a.newFlow("Assisted on replicas")
	a.worker.onAgent("Assistant", func(_ context.Context, run agentRun) runResult {
		status, key := reachModel(&llmAccess{URL: other.url + "/llm", Token: run.LLM.Token}, "openai")
		return runResult{OK: true, Output: fmt.Sprintf("the model answered %d with %s", status, key)}
	})
	askAssistant(a, f.ID, "Hello")
	if r := assistantReply(a, f.ID); r.Status != "done" || r.Content != "the model answered 200 with sk-assistant" {
		t.Errorf("the reply: %+v", r)
	}
}

// Lists an MCP server's tools through the replica's proxy with the token.
func replicasMCP(ctx context.Context, at *app, server int64, token string) string {
	req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/mcp/%d", at.url, server), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("%d %s", resp.StatusCode, out)
}
