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
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
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

// An agent that works until its step is stopped, or released.
func replicasSlow(release <-chan struct{}) func(context.Context, agentRun) runResult {
	return func(ctx context.Context, _ agentRun) runResult {
		select {
		case <-ctx.Done():
			return runResult{Error: "stopped"}
		case <-release:
			return submitted(data("done"))
		}
	}
}

// A replica starting, as in a rolling update, leaves the runs another walks
// going.
func TestStartingAReplicaLeavesAnothersRunsGoing(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Rolled over")
	a.startAt(f.ID, a.agent(f.ID, "Work", "Work.").ID)
	release := make(chan struct{})
	a.worker.onAgent("Work", replicasSlow(release))
	rn := a.startRun(f.ID)
	a.waitStep(rn.ID, "Work", stepActive)

	other := startApp(t, a.worker.url)
	if d := other.getRun(rn.ID); d.Status != runRunning {
		t.Fatalf("after another replica started, the run is %s; steps %s", d.Status, describeSteps(d))
	}
	close(release)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Errorf("run %s; steps %s", d.Status, describeSteps(d))
	}
}

// What a replica ran ends once it stops: at once when it is told to, as
// another replica finds it left, and once it hasn't said it is alive for
// the timeout when it is killed.
func TestAStoppedReplicasRunsEnd(t *testing.T) {
	const timeout = "REPLICA_TIMEOUT=3s"
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, timeout)
	a.worker = fw
	f := a.newFlow("Left behind")
	a.startAt(f.ID, a.agent(f.ID, "Work", "Work.").ID)
	fw.onAgent("Work", replicasSlow(nil))

	// Killed: the run ends once the timeout is over since the replica last
	// said it was alive, a sixth of it before at most, not before.
	other := startApp(t, fw.url, timeout)
	rn := a.startRun(f.ID)
	a.waitStep(rn.ID, "Work", stepActive)
	killed := time.Now()
	a.proc.kill()
	if d := other.getRun(rn.ID); d.Status != runRunning {
		t.Errorf("as its replica was killed, the run is %s", d.Status)
	}
	d := other.waitRun(rn.ID, "it to be ended", func(d runDetail) bool { return d.Status != runRunning })
	if took := time.Since(killed); d.Status != runInterrupted || took < 2500*time.Millisecond {
		t.Errorf("run %s after %s; steps %s", d.Status, took, describeSteps(d))
	}
	if s := onlyStep(t, d, "Work"); s.Status != stepError || !strings.Contains(s.Error, "The run ended before this step finished") {
		t.Errorf("its step: %s %q", s.Status, s.Error)
	}

	// Told to stop: it leaves, so another replica ends its run long
	// before a timeout of a minute.
	third := startApp(t, fw.url, "REPLICA_TIMEOUT=1m")
	rn = third.startRun(f.ID)
	third.waitStep(rn.ID, "Work", stepActive)
	third.proc.stop()
	d = other.waitRun(rn.ID, "it to be ended", func(d runDetail) bool { return d.Status != runRunning })
	if d.Status != runInterrupted {
		t.Errorf("run %s; steps %s", d.Status, describeSteps(d))
	}
}

// Stopping a run, one of its steps, an assistant's reply or a follow-up,
// and deciding a review, take effect at the replica running it whichever
// replica the request reaches.
func TestReplicasActOnWhatAnotherRuns(t *testing.T) {
	a := newApp(t)
	other := startApp(t, a.worker.url)
	cancelled := make(chan string, 4)
	slow := func(name string) func(context.Context, agentRun) runResult {
		return func(ctx context.Context, _ agentRun) runResult {
			select {
			case <-ctx.Done():
				cancelled <- name
			case <-time.After(10 * time.Second):
				cancelled <- "not " + name
			}
			return runResult{Error: "stopped"}
		}
	}

	// A step.
	f := a.newFlow("Stopped elsewhere")
	a.startAt(f.ID, a.agent(f.ID, "Step", "Work.").ID)
	a.worker.onAgent("Step", slow("step"))
	rn := a.startRun(f.ID)
	s := a.waitStep(rn.ID, "Step", stepActive)
	other.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/stop", rn.ID, s.ID+1000), nil, http.StatusConflict, "no step going")
	other.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/stop", rn.ID, s.ID), nil, http.StatusNoContent, nil)
	if got := <-cancelled; got != "step" {
		t.Errorf("stopping the step at another replica: %s cancelled", got)
	}
	if d := a.finish(rn.ID); onlyStep(t, d, "Step").Status != stepError {
		t.Errorf("the step stopped: %s", describeSteps(d))
	}

	// A run.
	g := a.newFlow("Ended elsewhere")
	a.startAt(g.ID, a.agent(g.ID, "Run", "Work.").ID)
	a.worker.onAgent("Run", slow("run"))
	rn = a.startRun(g.ID)
	a.waitStep(rn.ID, "Run", stepActive)
	other.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
	if got := <-cancelled; got != "run" {
		t.Errorf("stopping the run at another replica: %s cancelled", got)
	}

	// A review.
	h := a.newFlow("Decided elsewhere")
	kick := a.agent(h.ID, "Kick", "Start.")
	signal := a.newObject(h.ID, "Signal")
	review := a.newProcess(h.ID, map[string]any{"name": "Review", "kind": kindHuman})
	a.output(kick.ID, signal.ID)
	a.input(review.ID, signal.ID)
	a.startAt(h.ID, kick.ID)
	a.worker.onAgent("Kick", handsIn(data("go")))
	rn = a.startRun(h.ID)
	r := a.waitStep(rn.ID, "Review", stepWaiting)
	other.call("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, r.ID), map[string]any{"instructions": "Fine."}, http.StatusNoContent, nil)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Errorf("after the review was decided at another replica, run %s; steps %s", d.Status, describeSteps(d))
	}

	// A follow-up, of a step working on a repository, so it kept its
	// session.
	k := a.newFlow("Followed up elsewhere")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", k.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
	}}, http.StatusOK, nil)
	a.startAt(k.ID, a.agent(k.ID, "Answer", "Answer.").ID)
	a.worker.onAgent("Answer", func(ctx context.Context, run agentRun) runResult {
		if run.Resume {
			return slow("follow-up")(ctx, run)
		}
		return submitted(data("answered"))
	})
	d := a.finish(a.startRun(k.ID).ID)
	step := onlyStep(t, d, "Answer")
	var fu struct {
		ID int64 `json:"id"`
	}
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, step.ID), map[string]any{"message": "More."}, http.StatusCreated, &fu)
	other.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups/%d/stop", d.ID, step.ID, fu.ID), nil, http.StatusNoContent, nil)
	if got := <-cancelled; got != "follow-up" {
		t.Errorf("stopping the follow-up at another replica: %s cancelled", got)
	}

	// An assistant's reply.
	a.worker.onAgent("Assistant", slow("reply"))
	askAssistant(a, k.ID, "Hello")
	other.call("POST", fmt.Sprintf("/api/flows/%d/assistant/stop", k.ID), nil, http.StatusNoContent, nil)
	if got := <-cancelled; got != "reply" {
		t.Errorf("stopping the reply at another replica: %s cancelled", got)
	}
	if r := assistantReply(a, k.ID); r.Status != "error" {
		t.Errorf("the reply stopped: %+v", r)
	}
	other.refused("POST", fmt.Sprintf("/api/flows/%d/assistant/stop", k.ID), nil, http.StatusConflict, "isn't replying")
}

// A run whose calls through another replica spend what it may is stopped
// at the replica running it, once.
func TestARunOverspentThroughAnotherReplicaIsStopped(t *testing.T) {
	a := newApp(t)
	a.worker.quiet = true
	other := startApp(t, a.worker.url)
	setLimits(t, a, limitsBody{RunSpend: float64(2*callMicros-100) / 1e6})
	f := a.newFlow("Spent elsewhere")
	a.startAt(f.ID, a.agent(f.ID, "Spend", "Spend.").ID)
	var statuses []int
	stopped := false
	a.worker.onAgent("Spend", func(ctx context.Context, run agentRun) runResult {
		elsewhere := &llmAccess{URL: other.url + "/llm", Token: run.LLM.Token}
		for range 2 {
			s, _ := callModel(elsewhere, "fireworks")
			statuses = append(statuses, s)
		}
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
	if n := strings.Count(fmt.Sprint(a.runLogs(d.ID, 0)), "stopped: it spent"); n != 1 {
		t.Errorf("the run was stopped %d times", n)
	}
}

// A person's runs have as many steps going at once as they may across
// every replica, not at each.
func TestAPersonsStepsAtOnceAcrossReplicas(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	a.call("PUT", "/api/limits", map[string]any{"userParallelSteps": 1}, http.StatusOK, nil)
	t.Cleanup(func() { secretsDB(t).Exec("DELETE FROM limits") })
	other := startApp(t, a.worker.url,
		"OIDC_ISSUER="+p.srv.URL,
		"OIDC_CLIENT_ID="+usersClientID,
		"OIDC_CLIENT_SECRET="+usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com")
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	// Sessions are kept in the database, and cookies go to either port.
	other.client = a.client
	f := a.newFlow("Bob's on two replicas")
	a.startAt(f.ID, a.agent(f.ID, "Work", "Work.").ID)
	var mu sync.Mutex
	going, most := 0, 0
	release := make(chan struct{})
	a.worker.onAgent("Work", func(ctx context.Context, _ agentRun) runResult {
		mu.Lock()
		going++
		most = max(most, going)
		first := going == 1 && most == 1
		mu.Unlock()
		if first {
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		mu.Lock()
		going--
		mu.Unlock()
		return runResult{OK: true, Output: "done"}
	})
	first := a.startRun(f.ID)
	a.waitStep(first.ID, "Work", stepActive)
	second := other.startRun(f.ID)
	other.waitRun(second.ID, "its step to wait", func(runDetail) bool {
		return strings.Contains(fmt.Sprint(other.runLogs(second.ID, 0)), "waits: its person's runs have as many steps going as they may at once, 1")
	})
	close(release)
	for _, d := range []runDetail{a.finish(first.ID), other.finish(second.ID)} {
		if d.Status != runDone {
			t.Errorf("run %s; steps %s", d.Status, describeSteps(d))
		}
	}
	if most != 1 {
		t.Errorf("Bob had %d steps going at once, may have 1", most)
	}
}

// The replica a person's browser comes back to from an MCP server's
// sign-in finishes it, whichever replica started it.
func TestAnMCPSignInFinishesAtAnotherReplica(t *testing.T) {
	a, p := usersApp(t)
	other := startApp(t, a.worker.url,
		"OIDC_ISSUER="+p.srv.URL,
		"OIDC_CLIENT_ID="+usersClientID,
		"OIDC_CLIENT_SECRET="+usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com")
	m := newSettingsMCP(t)
	usersSignIn(a, p, usersAda)
	var server settingsServer
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "rp_tracker", "url": m.srv.URL + "/mcp"}, http.StatusCreated, &server)

	// The browser is sent back to the other replica.
	at, _ := url.Parse(other.url)
	browser := *a.client
	browser.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if req.URL.Path == "/api/mcp-oauth/callback" {
			req.URL.Host = at.Host
		}
		return nil
	}
	m.mu.Lock()
	m.next = "ada-token"
	m.mu.Unlock()
	resp, err := browser.Get(fmt.Sprintf("%s/api/mcp-servers/%d/connect", a.url, server.ID))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "is connected") || resp.Request.URL.Host != at.Host {
		t.Fatalf("connecting ended at %s with %d: %s", resp.Request.URL.Host, resp.StatusCode, body)
	}
	if !settingsConnected(a, server.ID) {
		t.Errorf("the server isn't connected")
	}
}

// Limits an admin saves at one replica reach the others soon after.
func TestLimitsReachEveryReplica(t *testing.T) {
	a := newApp(t)
	other := startApp(t, a.worker.url)
	setLimits(t, a, limitsBody{RunSpend: 1.5})
	deadline := time.Now().Add(10 * time.Second)
	for {
		var l limitsBody
		other.call("GET", "/api/limits", nil, http.StatusOK, &l)
		if l.RunSpend == 1.5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the other replica's limits are still %+v", l)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Two replicas refreshing a connection's expired token at once refresh it
// once, so a server rotating refresh tokens, which takes each once, keeps
// the person connected.
func TestReplicasRefreshAConnectionOnce(t *testing.T) {
	a, p := usersApp(t)
	other := startApp(t, a.worker.url,
		"OIDC_ISSUER="+p.srv.URL,
		"OIDC_CLIENT_ID="+usersClientID,
		"OIDC_CLIENT_SECRET="+usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com")
	var mu sync.Mutex
	refreshed, refused := 0, 0
	current := "refresh-0"
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint": srv.URL + "/token", "registration_endpoint": srv.URL + "/register"})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"client_id": "spoletta"})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {"code"}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		mu.Lock()
		defer mu.Unlock()
		if r.Form.Get("grant_type") == "authorization_code" {
			// Expired at once, so the first calls refresh it.
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access-0", "refresh_token": current, "expires_in": 1})
			return
		}
		// Slow, so the other replica's refresh comes while this one goes.
		time.Sleep(300 * time.Millisecond)
		if r.Form.Get("refresh_token") != current {
			refused++
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		refreshed++
		current = fmt.Sprint("refresh-", refreshed)
		json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprint("access-", refreshed), "refresh_token": current, "expires_in": 3600})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"authorization": r.Header.Get("Authorization")})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	usersSignIn(a, p, usersAda)
	other.client = a.client
	var server settingsServer
	a.call("POST", "/api/mcp-servers", map[string]any{"name": "rp_rotating", "url": srv.URL + "/mcp"}, http.StatusCreated, &server)
	resp, err := a.client.Get(fmt.Sprintf("%s/api/mcp-servers/%d/connect", a.url, server.ID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(1100 * time.Millisecond)

	f := a.newFlow("Rotating")
	use := a.agent(f.ID, "Use", "Use it.")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", use.ID), map[string]any{"settings": map[string]any{"mcpServers": []int64{server.ID}}}, http.StatusOK, nil)
	a.startAt(f.ID, use.ID)
	// Both steps call once both are going.
	var wg sync.WaitGroup
	wg.Add(2)
	reached := make(chan string, 2)
	a.worker.onAgent("Use", func(ctx context.Context, run agentRun) runResult {
		wg.Done()
		wg.Wait()
		req, _ := http.NewRequestWithContext(ctx, "POST", run.MCPServers[0].URL, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+run.MCPToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			reached <- err.Error()
			return submitted(data("used"))
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		reached <- fmt.Sprintf("%d %s", resp.StatusCode, body)
		return submitted(data("used"))
	})
	first, second := a.startRun(f.ID), other.startRun(f.ID)
	a.finish(first.ID)
	other.finish(second.ID)
	for range 2 {
		if got := <-reached; !strings.Contains(got, "Bearer access-1") {
			t.Errorf("a step reached the server with %s", got)
		}
	}
	if refreshed != 1 || refused != 0 {
		t.Errorf("refreshed %d times, %d refused", refreshed, refused)
	}
	if !settingsConnected(a, server.ID) {
		t.Errorf("the connection was lost")
	}
}
