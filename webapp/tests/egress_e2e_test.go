package tests

// Steps in containers of their own reach the internet only through the
// webapp's egress proxy, with a token of the step's, to the hosts an admin
// allowed and their repositories' hosts.

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Asks the webapp's proxy for target with the step's egress access. Fake
// agents call it off the test's goroutine, so what goes wrong is reported
// with t.Error, never t.Fatal, and answers 0.
func throughEgress(t *testing.T, a *app, access *egressAccess, target string) int {
	t.Helper()
	granted, err := url.Parse(access.URL)
	if err != nil {
		t.Error(err)
		return 0
	}
	proxy, _ := url.Parse(a.url)
	proxy.User = granted.User
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	resp, err := c.Get(target)
	if err != nil {
		t.Error(err)
		return 0
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestStepsReachOnlyTheHostsAllowed(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	var hosts []map[string]any
	a.call("POST", "/api/egress-hosts", map[string]any{"host": "Registry.NPMjs.org", "note": "packages"}, http.StatusOK, &hosts)
	if len(hosts) != 1 || hosts[0]["host"] != "registry.npmjs.org" || hosts[0]["note"] != "packages" {
		t.Errorf("hosts = %v", hosts)
	}
	for _, bad := range []string{"https://example.com", "exa mple.com", "*.", ""} {
		a.refused("POST", "/api/egress-hosts", map[string]any{"host": bad}, http.StatusBadRequest, "a host is a name")
	}

	f := a.newFlow("Fetches")
	usersShare(a, f.ID)
	a.startAt(f.ID, a.agent(f.ID, "Fetch", "Fetch it.").ID)
	var during int
	a.worker.onAgent("Fetch", func(_ context.Context, run agentRun) runResult {
		if run.Egress != nil {
			// Not allowed: refused before anything is dialled.
			during = throughEgress(t, a, run.Egress, "http://example.com/")
		}
		return submitted(data("fetched"))
	})
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	a.refused("GET", "/api/egress-hosts", nil, http.StatusForbidden, "only an admin")
	a.refused("POST", "/api/egress-hosts", map[string]any{"host": "evil.example"}, http.StatusForbidden, "only an admin")
	a.finish(a.startRun(f.ID).ID)
	run := a.worker.runsOf("Fetch")[0]
	if run.Egress == nil || !strings.HasPrefix(run.Egress.URL, "http://step:") {
		t.Fatalf("the step got %+v", run.Egress)
	}
	if during != http.StatusForbidden {
		t.Errorf("a host not allowed answered %d", during)
	}
	// Once the step is over, its token reaches nothing.
	if status := throughEgress(t, a, run.Egress, "http://registry.npmjs.org/"); status != http.StatusProxyAuthRequired {
		t.Errorf("after the step, its token got %d", status)
	}

	usersSignIn(a, p, usersAda)
	a.call("DELETE", "/api/egress-hosts/registry.npmjs.org", nil, http.StatusNoContent, nil)
	a.call("GET", "/api/egress-hosts", nil, http.StatusOK, &hosts)
	if len(hosts) != 0 {
		t.Errorf("after removing it: %v", hosts)
	}
}
