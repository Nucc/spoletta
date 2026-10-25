package tests

// Each step working on repositories gets a git token for them, beside its
// workspace and never kept with it: a GitHub App's, made for the step and
// reaching only the run's repositories, or the instance's GIT_TOKEN.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A flow whose agent Fix works on the repository at url and hands a Diff to
// the script Check, which works on it too; a plain agent Note works on none.
func gitTokensFlows(a *app, url string) (fixes, plain flow) {
	fixes = a.newFlow("Fixes")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", fixes.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": url}},
	}}, http.StatusOK, nil)
	fix := a.agent(fixes.ID, "Fix", "Fix it.")
	diff := a.newObject(fixes.ID, "Diff")
	a.output(fix.ID, diff.ID)
	check := a.newProcess(fixes.ID, map[string]any{"name": "Check", "kind": kindScript, "script": "git diff"})
	a.input(check.ID, diff.ID)
	a.startAt(fixes.ID, fix.ID)
	a.worker.onAgent("Fix", handsIn(data("fixed")))
	a.worker.onScript("Check", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: "checked"} })

	plain = a.newFlow("Plain")
	a.startAt(plain.ID, a.agent(plain.ID, "Note", "Write a note.").ID)
	a.worker.onAgent("Note", handsIn(data("noted")))
	return fixes, plain
}

func TestStepsGetTheInstancesGitToken(t *testing.T) {
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, "GIT_TOKEN=static-e2e-token")
	a.worker = fw
	fixes, plain := gitTokensFlows(a, "https://github.com/acme/api")
	d := a.finish(a.startRun(fixes.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	// Each step says which run it is of, whose home it gets.
	if fw.runsOf("Fix")[0].Run != d.ID || fw.scriptRunsSoFar()[0].Run != d.ID {
		t.Errorf("the steps were of runs %d and %d, want %d", fw.runsOf("Fix")[0].Run, fw.scriptRunsSoFar()[0].Run, d.ID)
	}
	a.finish(a.startRun(plain.ID).ID)
	for name, git := range map[string]*gitCredentials{"Fix": fw.runsOf("Fix")[0].Git, "Check": fw.scriptRunsSoFar()[0].Git} {
		if git == nil || git.Token != "static-e2e-token" || git.Username != "x-access-token" || git.Expires != nil {
			t.Errorf("%s's git got %+v", name, git)
		}
	}
	if git := fw.runsOf("Note")[0].Git; git != nil {
		t.Errorf("a step working on no repository got %+v", git)
	}
	db := secretsDB(t)
	var kept int
	db.QueryRow("SELECT COUNT(*) FROM run_steps WHERE session LIKE '%static-e2e-token%' OR prompt LIKE '%static-e2e-token%'").Scan(&kept)
	if kept != 0 {
		t.Errorf("%d steps kept the token", kept)
	}
	var access map[string]any
	a.call("GET", "/api/git-access", nil, http.StatusOK, &access)
	if access["source"] != "token" {
		t.Errorf("git access = %v", access)
	}
}

func TestStepsGetAGitHubAppsTokenForTheirRepositories(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked [][]string
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/77/access_tokens" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			http.Error(w, `{"message": "Not Found"}`, http.StatusNotFound)
			return
		}
		var body struct {
			Repositories []string `json:"repositories"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		asked = append(asked, body.Repositories)
		n := len(asked)
		mu.Unlock()
		writeJSON(w, http.StatusCreated, map[string]any{"token": fmt.Sprintf("ghs_step_%d", n), "expires_at": "2099-01-01T00:00:00Z"})
	}))
	defer github.Close()
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, "GITHUB_APP_ID=12", "GITHUB_APP_INSTALLATION_ID=77", "GITHUB_APP_PRIVATE_KEY="+string(pemKey), "GITHUB_API_URL="+github.URL)
	a.worker = fw
	host := strings.TrimPrefix(github.URL, "http://")
	fixes, _ := gitTokensFlows(a, "https://"+host+"/acme/api.git")
	if d := a.finish(a.startRun(fixes.ID).ID); d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	fix, check := fw.runsOf("Fix")[0].Git, fw.scriptRunsSoFar()[0].Git
	if fix == nil || check == nil || fix.Token == check.Token || !strings.HasPrefix(fix.Token, "ghs_step_") || fix.Expires == nil || fix.Expires.Year() != 2099 {
		t.Errorf("Fix's git got %+v, Check's %+v; want a token each", fix, check)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 || !slices.Equal(asked[0], []string{"api"}) {
		t.Errorf("GitHub was asked for %v", asked)
	}
}
