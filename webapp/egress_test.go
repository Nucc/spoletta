package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestEgressMatches(t *testing.T) {
	for _, c := range []struct {
		allowed, host, port string
		ok                  bool
	}{
		{"registry.npmjs.org", "registry.npmjs.org", "443", true},
		{"registry.npmjs.org", "registry.npmjs.org", "80", true},
		{"registry.npmjs.org", "registry.npmjs.org", "22", false},
		{"registry.npmjs.org", "evil.npmjs.org", "443", false},
		{"git.example.com:2222", "git.example.com", "2222", true},
		{"git.example.com:2222", "git.example.com", "443", false},
		{"*.example.com", "api.example.com", "443", true},
		{"*.example.com", "example.com", "443", false},
		{"*.example.com", "api.example.com.evil.net", "443", false},
	} {
		if got := egressMatches(c.allowed, c.host, c.port); got != c.ok {
			t.Errorf("%s takes %s:%s = %v", c.allowed, c.host, c.port, got)
		}
	}
	for addr, listed := range map[string]bool{"127.0.0.1:443": true, "169.254.169.254:80": true, "10.0.0.5:443": false, "[::1]:443": true} {
		if refusePrivate(listed, false)("tcp", addr, nil) == nil {
			t.Errorf("%s was dialled (listed %v)", addr, listed)
		}
	}
	if refusePrivate(true, false)("tcp", "10.0.0.5:443", nil) != nil || refusePrivate(false, false)("tcp", "93.184.216.34:443", nil) != nil {
		t.Error("a private address of a listed host, or a public one, was refused")
	}
}

// A step's token reaches the hosts allowed and its repositories' through
// the proxy, plain or tunnelled; without it, or to another host, nothing.
func TestEgressProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "reached "+r.URL.Path)
	}))
	defer target.Close()
	tlsTarget := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "tunnelled "+r.URL.Path)
	}))
	defer tlsTarget.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "http://"))
	_, tlsPort, _ := net.SplitHostPort(strings.TrimPrefix(tlsTarget.URL, "https://"))

	b := newEgressBroker(nil, "http://webapp:8080")
	b.loopback = true
	b.hosts = []egressHost{{Host: "127.0.0.1:" + port}}
	proxy := httptest.NewServer(b.wrap(http.NotFoundHandler()))
	defer proxy.Close()
	access := b.grant(nil, 0, 7, &workspaceSpec{Repositories: []workspaceRepo{{repository: repository{URL: "https://127.0.0.1:" + tlsPort + "/acme/api"}}}}, 0)
	via := func(creds *url.Userinfo) *http.Client {
		u, _ := url.Parse(proxy.URL)
		u.User = creds
		tr := tlsTarget.Client().Transport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(u)
		return &http.Client{Transport: tr}
	}
	granted, _ := url.Parse(access.URL)
	if granted.Host != "webapp:8080" || granted.User.Username() != "step" {
		t.Errorf("the step reaches the proxy at %s", access.URL)
	}
	get := func(c *http.Client, u string) (int, string) {
		resp, err := c.Get(u)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	step := via(granted.User)
	if status, body := get(step, target.URL+"/allowed"); status != 200 || body != "reached /allowed" {
		t.Errorf("an allowed host: %d %q", status, body)
	}
	if status, body := get(step, tlsTarget.URL+"/repo"); status != 200 || body != "tunnelled /repo" {
		t.Errorf("the step's repository's host, tunnelled: %d %q", status, body)
	}
	if status, _ := get(via(url.UserPassword("step", "guessed")), target.URL+"/x"); status != http.StatusProxyAuthRequired {
		t.Errorf("another token: %d", status)
	}
	b.hosts = []egressHost{{Host: "example.com"}}
	if status, _ := get(step, target.URL+"/x"); status != http.StatusForbidden {
		t.Errorf("a host no longer allowed: %d", status)
	}
	b.revoke(access)
	if status, _ := get(step, tlsTarget.URL+"/repo"); status == 200 {
		t.Errorf("a revoked token reached the repository's host")
	}

	// Requests that aren't the proxy's go on to the webapp.
	if resp, err := http.Get(proxy.URL + "/api/flows"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("a request of the webapp's: %v, %v", resp, err)
	}
}
