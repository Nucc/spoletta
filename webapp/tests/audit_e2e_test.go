package tests

// The audit log records what each step was handed, and what admins
// changed, never a token; only admins read it.

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

type auditLog struct {
	Events []struct {
		Kind   string `json:"kind"`
		Who    string `json:"who"`
		Run    *int64 `json:"run"`
		Step   *int64 `json:"step"`
		Detail string `json:"detail"`
	} `json:"events"`
	Matched int `json:"matched"`
}

// The events the query matches, once there are at least n: they are
// written in the background.
func (a *app) audit(query string, n int) auditLog {
	a.t.Helper()
	var log auditLog
	for deadline := time.Now().Add(5 * time.Second); ; {
		a.call("GET", "/api/audit?q="+url.QueryEscape(query), nil, http.StatusOK, &log)
		if len(log.Events) >= n || time.Now().After(deadline) {
			return log
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestTheAuditLogRecordsWhatStepsWereHanded(t *testing.T) {
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, "GIT_TOKEN=static-audit-token")
	a.worker = fw
	fixes, _ := gitTokensFlows(a, "https://github.com/acme/api")
	d := a.finish(a.startRun(fixes.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	fix := onlyStep(t, d, "Fix")
	log := a.audit(fmt.Sprintf("run:%d step:%d", d.ID, fix.ID), 5)
	var kinds []string
	for _, e := range log.Events {
		kinds = append(kinds, e.Kind)
		if strings.Contains(e.Detail, fw.runsOf("Fix")[0].LLM.Token) || strings.Contains(e.Detail, "static-audit-token") {
			t.Errorf("an event holds a token: %s %s", e.Kind, e.Detail)
		}
	}
	for _, want := range []string{"model.grant", "model.revoke", "internet.grant", "internet.revoke", "git.grant"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("the step's events are %v, without %s", kinds, want)
		}
	}
	if g := a.audit(fmt.Sprintf("kind:git.grant run:%d", d.ID), 2); len(g.Events) != 2 || !strings.Contains(g.Events[0].Detail, "the instance's GIT_TOKEN, for https://github.com/acme/api") {
		t.Errorf("the git tokens' events: %+v", g.Events)
	}

	// An admin's change is recorded too.
	a.call("POST", "/api/egress-hosts", map[string]any{"host": "audited.example.com"}, http.StatusOK, nil)
	t.Cleanup(func() { secretsDB(t).Exec("DELETE FROM egress_hosts WHERE host = 'audited.example.com'") })
	if c := a.audit("kind:internet.allow audited", 1); len(c.Events) != 1 || c.Events[0].Detail != "steps may reach audited.example.com" {
		t.Errorf("the change's events: %+v", c.Events)
	}
}

func TestOnlyAdminsReadTheAuditLog(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	a.call("PUT", "/api/limits", map[string]any{"runSpend": 3}, http.StatusOK, nil)
	t.Cleanup(func() { secretsDB(t).Exec("DELETE FROM limits") })
	if l := a.audit("kind:limits.set", 1); len(l.Events) == 0 || l.Events[0].Who != "Ada Lovelace" || !strings.HasPrefix(l.Events[0].Detail, "a run may spend $3.00") {
		t.Errorf("the limits' events: %+v", l.Events)
	}
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	a.refused("GET", "/api/audit", nil, http.StatusForbidden, "only an admin")
}
