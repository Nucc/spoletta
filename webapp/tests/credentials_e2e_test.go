package tests

// Credentials: admins keep a service's secret in the settings, a process
// picks the credentials it uses, and its step reaches each service through
// the webapp's proxy with a token of its own, which the proxy swaps for
// the secret; the step never sees it.

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type credentialsItem struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Variable  string   `json:"variable"`
	BaseURL   string   `json:"baseUrl"`
	Auth      string   `json:"auth"`
	Header    string   `json:"header"`
	Template  string   `json:"template"`
	Headers   []string `json:"headers"`
	SecretSet bool     `json:"secretSet"`
	Hint      string   `json:"hint"`
	Everyone  bool     `json:"everyone"`
	Teams     []struct {
		ID int64 `json:"id"`
	} `json:"teams"`
}

// A request a service got through the proxy.
type credentialsSeen struct {
	method, path, query, authorization, apiKey, cookie, body string
	header                                                   http.Header
}

// A service that records what it gets and answers with it; under
// /redirect it redirects to where its query's to says, under /echo it
// answers with the headers it got, a cookie, what looks like the
// credential and headers a browser would act on, and under /echo-body
// with the URL and credential it got, plainly, gzipped under /gzip, said
// to be encoded twice under /twice and /gzip-twice, and in an encoding the
// proxy can't read under /br; under /cut it echoes the URL it got cut
// short, and under /encoded its key parameter in the ways a service could
// write it.
type credentialsService struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []credentialsSeen
}

func newCredentialsService(t *testing.T) *credentialsService {
	s := &credentialsService{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, credentialsSeen{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("Cookie"), string(body), r.Header.Clone()})
		s.mu.Unlock()
		echo := fmt.Sprintf(`{"self": %q, "auth": %q}`, r.URL.String(), r.Header.Get("Authorization"))
		switch {
		case strings.HasSuffix(r.URL.Path, "/echo-body"):
			fmt.Fprint(w, echo)
		case strings.HasSuffix(r.URL.Path, "/gzip"):
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			fmt.Fprint(gz, echo)
			gz.Close()
		case strings.HasSuffix(r.URL.Path, "/twice"):
			w.Header().Add("Content-Encoding", "identity")
			w.Header().Add("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			fmt.Fprint(gz, echo)
			gz.Close()
		case strings.HasSuffix(r.URL.Path, "/gzip-twice"):
			w.Header().Set("Content-Encoding", "gzip, gzip")
			gz := gzip.NewWriter(w)
			fmt.Fprint(gz, echo)
			gz.Close()
		case strings.HasSuffix(r.URL.Path, "/br"):
			w.Header().Set("Content-Encoding", "br")
			fmt.Fprint(w, echo)
		case strings.Contains(r.URL.Path, "/cut/"):
			self := r.URL.String()
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such page: " + self[:min(len(self), 80)]})
		case strings.HasSuffix(r.URL.Path, "/encoded"):
			key := r.URL.Query().Get("key")
			b64 := base64.StdEncoding.EncodeToString([]byte(key))
			fmt.Fprintf(w, "<p>%s</p> %s %s %s %s", html.EscapeString(key), strings.ToLower(url.QueryEscape(key)), b64, b64[:len(b64)/2], b64[len(b64)/2:])
		case strings.HasSuffix(r.URL.Path, "/redirect"):
			w.Header().Set("Location", r.URL.Query().Get("to"))
			w.WriteHeader(http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/echo"):
			w.Header().Set("Set-Cookie", "session=got-with-the-secret")
			w.Header().Set("X-Echo", r.Header.Get("Authorization"))
			w.Header().Set("Authorization", r.Header.Get("Authorization"))
			w.Header().Set("X-Request-Id", "req-1")
			w.Header().Set("X-RateLimit-Remaining", "41")
			w.Header().Set("X-Request-Echo", strings.TrimPrefix(r.Header.Get("Authorization"), "token ")[:12])
			for _, h := range []string{"Clear-Site-Data", "Refresh", "WWW-Authenticate", "Service-Worker-Allowed", "Access-Control-Allow-Origin",
				"Alt-Svc", "Digest", "X-Kept"} {
				w.Header().Set(h, "x")
			}
			w.Header().Add("Link", "<"+s.srv.URL+r.URL.Path+"?page=2>; rel=\"next\"")
			w.Header().Add("Link", "<https://docs.example.com/"+r.Header.Get("Authorization")[6:]+">; rel=\"help\"")
			fmt.Fprint(w, "echoed")
		default:
			writeJSON(w, http.StatusOK, map[string]string{"path": r.URL.Path})
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *credentialsService) requests() []credentialsSeen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// A name of its own, as the tests share a database, removed once the test
// is over.
func credentialsName(t *testing.T, name string) string {
	n := fmt.Sprintf("%s %d", name, time.Now().UnixNano())
	db := secretsDB(t)
	t.Cleanup(func() { db.Exec("DELETE FROM credentials WHERE name = ?", n) })
	return n
}

// Adds a credential, which must succeed, and returns it as the settings
// list it.
func credentialsAdd(a *app, fields map[string]any) credentialsItem {
	a.t.Helper()
	var list []credentialsItem
	a.call("POST", "/api/credentials", fields, http.StatusCreated, &list)
	for _, c := range list {
		if c.Name == fields["name"] {
			return c
		}
	}
	a.t.Fatalf("the credential %v isn't listed: %+v", fields["name"], list)
	return credentialsItem{}
}

// What a call through the proxy got back.
type credentialsAnswer struct {
	status int
	header http.Header
	body   string
}

// Calls the proxy at target as a step would, with the token as a bearer
// token, or with basic set as basic auth's password, and the headers in
// extra, "Name: value" each; never following a redirect.
func credentialsCall(t *testing.T, method, target, token string, basic bool, body string, extra ...string) credentialsAnswer {
	t.Helper()
	req, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if basic {
		req.SetBasicAuth("whoever", token)
	} else if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Api-Key", "the step's own")
	req.Header.Set("Cookie", "theirs=1")
	for _, h := range extra {
		k, v, _ := strings.Cut(h, ": ")
		req.Header.Set(k, v)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return credentialsAnswer{resp.StatusCode, resp.Header, string(b)}
}

// The URL of the step's credential named name.
func credentialsURL(t *testing.T, creds []credentialAccess, name string) string {
	t.Helper()
	for _, c := range creds {
		if c.Name == name {
			return c.URL
		}
	}
	t.Fatalf("the step has no credential %s: %+v", name, creds)
	return ""
}

// A script reaches its credentials' services through the proxy: each
// request gets its credential's secret as the credential says, in place of
// the step's token, on a path under its base URL; the secret is never
// listed, logged or audited, and the token ends with the step.
func TestCredentialsReachTheirServicesThroughTheProxy(t *testing.T) {
	a := newApp(t)
	svc := newCredentialsService(t)
	db := secretsDB(t)
	jiraSecret, basicSecret, querySecret := "jira-secret-0123456789", "basic-secret-0123456789", "query secret+0123456789"
	jira := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Jira"), "baseUrl": svc.srv.URL + "/jira/", "auth": "header",
		"template": "token {secret}", "secret": jiraSecret, "everyone": true, "description": "The tracker.", "headers": []string{"notion-version"}})
	basic := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Billing"), "baseUrl": svc.srv.URL + "/billing", "auth": "basic",
		"username": "me@example.com", "secret": basicSecret, "everyone": true})
	query := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Maps"), "baseUrl": svc.srv.URL + "/maps/v1", "auth": "query",
		"param": "key", "secret": querySecret, "everyone": true})
	unpicked := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Other"), "baseUrl": svc.srv.URL + "/other", "auth": "header",
		"header": "X-Api-Key", "template": "{secret}", "secret": "other-secret-0123456789", "everyone": true})
	if jira.Header != "Authorization" || jira.Template != "token {secret}" || !jira.SecretSet || jira.Hint != "6789" || !strings.HasPrefix(jira.Variable, "JIRA_") ||
		len(jira.Headers) != 1 || jira.Headers[0] != "Notion-Version" {
		t.Errorf("the settings show %+v", jira)
	}

	// The secret is kept sealed, and never sent back.
	if v := secretsStored(t, db, "SELECT secret FROM credentials WHERE id = ?", jira.ID); !strings.HasPrefix(v, "sealed:v1:") || strings.Contains(v, jiraSecret) {
		t.Errorf("the secret was kept as %q", v)
	}
	if _, body := a.do("GET", "/api/credentials", nil); strings.Contains(string(body), jiraSecret) || strings.Contains(string(body), "sealed:") {
		t.Errorf("the settings list the secret: %s", body)
	}
	a.refused("POST", "/api/credentials", map[string]any{"name": jira.Name, "baseUrl": "https://x.example.com", "auth": "header", "secret": "s", "everyone": true},
		http.StatusConflict, "named "+jira.Name+" already")
	a.refused("POST", "/api/credentials", map[string]any{"name": credentialsName(t, "Plain"), "baseUrl": "http://jira.example.com", "auth": "header", "secret": "s", "everyone": true},
		http.StatusBadRequest, "must be https")
	a.refused("POST", "/api/credentials", map[string]any{"name": credentialsName(t, "Forwarded"), "baseUrl": "https://x.example.com", "auth": "header", "secret": "s",
		"everyone": true, "headers": []string{"X_Forwarded_Host"}}, http.StatusBadRequest, "steps can't send X_forwarded_host")
	// A change without a secret keeps it, unless it would send it elsewhere:
	// to another base URL, even at the same origin, or in another place on
	// the request.
	a.call("PUT", fmt.Sprintf("/api/credentials/%d", jira.ID), map[string]any{"name": jira.Name, "baseUrl": svc.srv.URL + "/jira/", "auth": "header",
		"template": "token {secret}", "everyone": true, "headers": []string{"Notion-Version"}}, http.StatusOK, nil)
	other := map[string]any{"name": unpicked.Name, "baseUrl": svc.srv.URL + "/other", "auth": "header", "header": "X-Api-Key", "template": "{secret}", "everyone": true,
		"description": "Another."}
	a.call("PUT", fmt.Sprintf("/api/credentials/%d", unpicked.ID), other, http.StatusOK, nil)
	for _, change := range [][2]string{{"baseUrl", "http://localhost:1/other"}, {"baseUrl", svc.srv.URL + "/other/v2"}, {"header", "X-Echo"}, {"auth", "basic"}} {
		moved := maps.Clone(other)
		moved[change[0]] = change[1]
		a.refused("PUT", fmt.Sprintf("/api/credentials/%d", unpicked.ID), moved, http.StatusBadRequest, "give the secret again")
	}
	moved := maps.Clone(other)
	moved["baseUrl"], moved["secret"] = "http://localhost:1/other", "another-secret-0123456789"
	a.call("PUT", fmt.Sprintf("/api/credentials/%d", unpicked.ID), moved, http.StatusOK, nil)

	f := a.newFlow("Credentials")
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "curl \"$CREDENTIAL_JIRA_URL\"",
		"settings": map[string]any{"credentials": []int64{jira.ID, basic.ID, query.ID}}})
	a.startAt(f.ID, fetch.ID)
	before := len(svc.requests())
	var calls []credentialsAnswer
	var tricks []credentialsAnswer
	// What the script prints holds its token, as with set -x, a piece at a time.
	a.worker.mu.Lock()
	a.worker.scriptOutput["Fetch"] = [][2]string{{"stderr", "+ curl -H 'Authorization: Bearer {token-head}"}, {"stderr", "{token-tail}' \"$CREDENTIAL_JIRA_URL\"\n"},
		{"stdout", "the token is {token}\n"}, {"stderr", "> Authorization: Basic {token-basic}\n"}}
	a.worker.mu.Unlock()
	a.worker.onScript("Fetch", func(_ context.Context, run scriptRun) runResult {
		token := run.CredentialsToken
		jiraURL := credentialsURL(t, run.Credentials, jira.Name)
		mapsURL := credentialsURL(t, run.Credentials, query.Name)
		calls = append(calls,
			credentialsCall(t, "POST", jiraURL+"/rest/issue?fields=summary", token, false, `{"summary": "x"}`,
				"X-Original-URL: /admin", "X-Rewrite-URL: /admin", "X-Forwarded-Prefix: /admin", "X-HTTP-Method-Override: DELETE",
				"Range: bytes=0-3", "Accept-Encoding: gzip", "Prefer: return=minimal", "Notion-Version: 2022-06-28", "X-Custom: 1"),
			credentialsCall(t, "GET", credentialsURL(t, run.Credentials, basic.Name)+"/invoices", token, true, ""),
			credentialsCall(t, "GET", credentialsURL(t, run.Credentials, query.Name)+"/geocode?key=forged&q=Rome", token, false, ""),
			// What the process didn't pick, and a token that isn't one.
			credentialsCall(t, "GET", fmt.Sprintf("%s/credentials/%d/x", a.url, unpicked.ID), token, false, ""),
			credentialsCall(t, "GET", jiraURL+"/x", "not-a-token", false, ""),
			credentialsCall(t, "GET", jiraURL+"/x", "", false, ""),
			// Redirects within the base URL come back through the proxy;
			// elsewhere, without the secret.
			credentialsCall(t, "GET", jiraURL+"/redirect?to="+url.QueryEscape("/jira/next?page=2"), token, false, ""),
			credentialsCall(t, "GET", credentialsURL(t, run.Credentials, query.Name)+"/redirect?to="+
				url.QueryEscape("https://login.example.com/?key="+url.QueryEscape(querySecret)+"&next=1"), token, false, ""),
			credentialsCall(t, "GET", credentialsURL(t, run.Credentials, query.Name)+"/redirect?to="+
				url.QueryEscape("https://login.example.com/"+url.PathEscape(querySecret)), token, false, ""),
			credentialsCall(t, "GET", jiraURL+"/echo", token, false, ""),
			// What the service answers is read for the secret, uncompressed.
			credentialsCall(t, "GET", mapsURL+"/echo-body?page=2", token, false, ""),
			credentialsCall(t, "GET", jiraURL+"/echo-body", token, false, ""),
			credentialsCall(t, "GET", mapsURL+"/gzip", token, false, "", "Accept-Encoding: gzip"),
			credentialsCall(t, "GET", mapsURL+"/br", token, false, ""),
			// Said to be encoded more than once, it can't be read for the
			// secret either.
			credentialsCall(t, "GET", mapsURL+"/twice", token, false, ""),
			credentialsCall(t, "GET", mapsURL+"/gzip-twice", token, false, ""),
		)
		for _, path := range []string{"/a%2F..%2F..%2Fother", "/a%2fb", "/..%2F..%2Fother", "/%2e%2e/%2e%2e/other", "/a%252F..%252Fother", "/a%5Cb", "/../../other",
			"/..:/..:/other", "/..::$INDEX_ALLOCATION/..::$INDEX_ALLOCATION/other"} {
			tricks = append(tricks, credentialsCall(t, "GET", jiraURL+path, token, false, ""))
		}
		return runResult{OK: true, Output: "fetched with " + token, Log: []logLine{{Time: time.Now(), Level: "info", Message: "bash ran with " + token},
			{Time: time.Now(), Level: "info", Message: stepToken("bash printed Authorization: Basic {token-basic}", token)}}}
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	run := a.worker.scriptRunsSoFar()[0]
	if run.CredentialsToken == "" || len(run.Credentials) != 3 {
		t.Fatalf("the script got %+v", run)
	}
	for _, c := range run.Credentials {
		if !strings.HasPrefix(c.URL, a.url+"/credentials/") || !strings.HasPrefix(c.Env, "CREDENTIAL_") || !strings.HasSuffix(c.Env, "_URL") {
			t.Errorf("the script reaches %+v", c)
		}
	}
	if c := run.Credentials[0]; c.Env != "CREDENTIAL_"+jira.Variable+"_URL" || c.URL != fmt.Sprintf("%s/credentials/%d", a.url, jira.ID) {
		t.Errorf("the script reaches Jira as %+v", c)
	}

	seen := svc.requests()[before:]
	got := func(prefix string) credentialsSeen {
		t.Helper()
		for _, s := range seen {
			if strings.HasPrefix(s.path, prefix) {
				return s
			}
		}
		t.Fatalf("the service got nothing at %s: %+v", prefix, seen)
		return credentialsSeen{}
	}
	if s := got("/jira/rest/issue"); s.method != "POST" || s.query != "fields=summary" || s.authorization != "token "+jiraSecret ||
		s.body != `{"summary": "x"}` || s.apiKey != "" || s.cookie != "" {
		t.Errorf("Jira got %+v", s)
	} else {
		// Only the headers every service gets and those the credential
		// names go on; what could send the call elsewhere stays here.
		for k, want := range map[string]string{"Prefer": "return=minimal", "Notion-Version": "2022-06-28", "Accept-Encoding": "identity", "Content-Type": "",
			"X-Original-Url": "", "X-Rewrite-Url": "", "X-Forwarded-Prefix": "", "X-Http-Method-Override": "", "Range": "", "X-Custom": ""} {
			if got := s.header.Get(k); got != want {
				t.Errorf("Jira got %s: %q, want %q", k, got, want)
			}
		}
	}
	if s := got("/billing/invoices"); s.authorization != "Basic "+base64.StdEncoding.EncodeToString([]byte("me@example.com:"+basicSecret)) {
		t.Errorf("billing got %+v", s)
	}
	if s := got("/maps/v1/geocode"); s.authorization != "" || s.query != "key="+url.QueryEscape(querySecret)+"&q=Rome" {
		t.Errorf("maps got %+v", s)
	}
	for _, s := range seen {
		if !strings.HasPrefix(s.path, "/jira/") && !strings.HasPrefix(s.path, "/billing/") && !strings.HasPrefix(s.path, "/maps/v1/") {
			t.Errorf("the service got a call outside a base URL: %+v", s)
		}
	}
	for i, want := range []int{200, 200, 200, 403, 403, 401} {
		if calls[i].status != want {
			t.Errorf("call %d answered %d %s, want %d", i, calls[i].status, calls[i].body, want)
		}
	}
	if loc := calls[6].header.Get("Location"); calls[6].status != http.StatusFound || loc != fmt.Sprintf("%s/credentials/%d/next?page=2", a.url, jira.ID) {
		t.Errorf("a redirect within the base URL came back as %d %q", calls[6].status, loc)
	}
	if loc := calls[7].header.Get("Location"); loc != "https://login.example.com/?next=1" {
		t.Errorf("a redirect elsewhere came back to %q", loc)
	}
	if loc := calls[8].header.Get("Location"); calls[8].status != http.StatusFound || loc != "" {
		t.Errorf("a redirect holding the secret came back as %d %q", calls[8].status, loc)
	}
	// Only the answer's headers saying what it is, and how many calls are
	// left, come back, none holding a piece of the secret; its Link's pages
	// within the base URL come back as the proxy's.
	echo := calls[9]
	if echo.body != "echoed" || echo.header.Get("X-Request-Id") != "req-1" || echo.header.Get("X-Ratelimit-Remaining") != "41" {
		t.Errorf("the echo came back with %v", echo.header)
	}
	for _, h := range []string{"Set-Cookie", "X-Echo", "Authorization", "X-Request-Echo", "Clear-Site-Data", "Refresh", "Www-Authenticate",
		"Service-Worker-Allowed", "Access-Control-Allow-Origin", "Alt-Svc", "Digest", "X-Kept"} {
		if v := echo.header.Get(h); v != "" {
			t.Errorf("the echo came back with %s: %s", h, v)
		}
	}
	if links := echo.header.Values("Link"); len(links) != 1 || links[0] != fmt.Sprintf(`<%s/credentials/%d/echo?page=2>; rel="next"`, a.url, jira.ID) {
		t.Errorf("the echo's links came back as %q", links)
	}
	if echo.header.Get("Content-Security-Policy") != "sandbox" || echo.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("an answer came back without its sandbox: %v", echo.header)
	}
	for i, want := range map[int]string{
		10: `"self": "/maps/v1/echo-body?key=[redacted]&page=2"`,
		11: `"auth": "[redacted]"`,
		12: `"self": "/maps/v1/gzip?key=[redacted]"`,
	} {
		if c := calls[i]; c.status != http.StatusOK || !strings.Contains(c.body, want) || c.header.Get("Content-Encoding") != "" {
			t.Errorf("call %d came back %d %v: %s; want it to hold %s", i, c.status, c.header, c.body, want)
		}
	}
	for _, c := range calls[13:16] {
		if c.status != http.StatusBadGateway || strings.Contains(c.body, "query") {
			t.Errorf("an answer the proxy can't read came back %d: %s", c.status, c.body)
		}
	}
	for i, c := range tricks {
		if c.status == http.StatusOK {
			t.Errorf("trick %d answered %d %s", i, c.status, c.body)
		}
	}

	// Once the step is over, its token reaches nothing.
	if c := credentialsCall(t, "GET", run.Credentials[0].URL+"/x", run.CredentialsToken, false, ""); c.status != http.StatusForbidden {
		t.Errorf("after the step, its token got %d", c.status)
	}

	// Each call is in the run's log and the audit log, by its path without
	// its query; the secret and the token are in neither, nor in what the
	// webapp logs.
	lines := a.runLogs(d.ID, 0)
	logLineWith(t, lines, "the credential "+jira.Name+": POST /rest/issue, answered 200")
	logLineWith(t, lines, "the credential "+query.Name+": GET /geocode, answered 200")
	logLineWith(t, lines, "which the step's process didn't pick")
	a.auditEvent("kind:credential.call", func(_, detail string) bool {
		return detail == "the credential "+basic.Name+": GET /invoices, answered 200"
	})
	a.auditEvent("kind:credential.grant", func(_, detail string) bool { return strings.Contains(detail, jira.Name+", "+basic.Name) })
	a.auditEvent("kind:credential.revoke", func(_, detail string) bool { return detail == "the credential proxy's token ended with its step" })
	a.auditEvent("kind:credential.refused", func(_, detail string) bool { return strings.Contains(detail, "at a path outside its base URL") })
	// What the script printed, logged and handed in is kept without its token.
	var all []string
	var chunks []outputChunk
	step := onlyStep(t, d, "Fetch")
	a.call("GET", fmt.Sprintf("/api/runs/%d/steps/%d/output", d.ID, step.ID), nil, http.StatusOK, &chunks)
	var printed string
	for _, c := range chunks {
		printed += c.Text
	}
	if !strings.Contains(printed, "Authorization: Bearer [redacted]' ") || !strings.Contains(printed, "the token is [redacted]\n") ||
		!strings.Contains(printed, "[redacted]") || strings.Contains(printed, stepToken("{token-basic}", run.CredentialsToken)[12:40]) {
		t.Errorf("what the script printed was kept as %q", printed)
	}
	logLineWith(t, lines, "bash ran with [redacted]")
	logLineWith(t, lines, "bash printed Authorization: Basic ")
	all = append(all, printed, step.Output, step.Error)
	for _, l := range lines {
		all = append(all, l.Message)
	}
	for _, e := range a.audit("credential", 1).Events {
		all = append(all, e.Detail)
	}
	all = append(all, a.proc.log.String())
	for _, secret := range []string{jiraSecret, basicSecret, querySecret, url.QueryEscape(querySecret), run.CredentialsToken, run.CredentialsToken[:10], run.CredentialsToken[10:],
		stepToken("{token-basic}", run.CredentialsToken)[12:40]} {
		for _, s := range all {
			if strings.Contains(s, secret) {
				t.Errorf("%q holds a secret or the token", s)
			}
		}
	}

	// Removed, a credential fails the steps picking it.
	a.call("DELETE", fmt.Sprintf("/api/credentials/%d", basic.ID), nil, http.StatusNoContent, nil)
	d = a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Fetch"); s.Status != "error" || !strings.Contains(s.Error, "credential the process uses was removed") {
		t.Errorf("a step picking a removed credential: %s %s", s.Status, s.Error)
	}
}

// A service echoing the URL it got cut short, wherever a step pads the
// path to have it cut, hands back fewer than eight of the secret's
// characters in a row, the secret's parameter coming first in the query;
// and one echoing the secret as a service could write it, HTML-escaped,
// URL-encoded in lower case or in base64, whole or in pieces, hands back
// none of it.
func TestCredentialsBlankOutEchoesCutShort(t *testing.T) {
	a := newApp(t)
	svc := newCredentialsService(t)
	secret := `Zq8Lw2Xv9Kp4&Rt6<Ym1>Nc"Wd'5`
	escaped := url.QueryEscape(secret)
	echo := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Echo"), "baseUrl": svc.srv.URL + "/echo/v1", "auth": "query",
		"param": "key", "secret": secret, "everyone": true})
	f := a.newFlow("Echoes")
	call := a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindScript, "script": "curl \"$CREDENTIAL_ECHO_URL\"",
		"settings": map[string]any{"credentials": []int64{echo.ID}}})
	a.startAt(f.ID, call.ID)
	cut := map[int]credentialsAnswer{}
	var encoded credentialsAnswer
	a.worker.onScript("Call", func(_ context.Context, run scriptRun) runResult {
		at := credentialsURL(t, run.Credentials, echo.Name)
		// The service echoes 80 characters of /echo/v1/cut/<pad>?key=<secret>&a=1.
		for k := 1; k < len(escaped); k++ {
			pad := 80 - k - len("/echo/v1/cut/?key=")
			cut[k] = credentialsCall(t, "GET", at+"/cut/"+strings.Repeat("a", pad)+"?a=1&key=forged", run.CredentialsToken, false, "")
		}
		encoded = credentialsCall(t, "GET", at+"/encoded", run.CredentialsToken, false, "")
		return runResult{OK: true}
	})
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if len(cut) == 0 {
		t.Fatal("the script made no calls")
	}
	for k, c := range cut {
		if c.status != http.StatusNotFound || !strings.Contains(c.body, "no such page: /echo/v1/cut/aaa") {
			t.Errorf("cut %d characters into the secret came back %d: %s", k, c.status, c.body)
		}
		for i := 0; i+8 <= len(escaped); i++ {
			if strings.Contains(c.body, escaped[i:i+8]) {
				t.Errorf("cut %d characters into the secret came back holding %s: %s", k, escaped[i:i+8], c.body)
			}
		}
		if k >= 8 && !strings.Contains(c.body, "key=[redacted]") {
			t.Errorf("cut %d characters into the secret came back as %s", k, c.body)
		}
	}
	for _, s := range svc.requests() {
		if strings.HasPrefix(s.path, "/echo/v1/cut/") && s.query != "key="+escaped+"&a=1" {
			t.Errorf("the service got the query %s", s.query)
		}
	}
	if want := "<p>[redacted]</p> [redacted] [redacted] [redacted] [redacted]"; encoded.status != http.StatusOK || encoded.body != want {
		t.Errorf("the secret encoded came back %d as %s, want %s", encoded.status, encoded.body, want)
	}
}

// An agent is told the services it calls through the proxy, by their
// variables, and its request to the worker never holds a secret.
func TestAgentsAreToldTheirCredentials(t *testing.T) {
	a := newApp(t)
	svc := newCredentialsService(t)
	secret := "agent-secret-0123456789"
	jira := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Jira"), "baseUrl": svc.srv.URL, "auth": "header", "secret": secret, "everyone": true})
	f := a.newFlow("Agent credentials")
	use := a.newProcess(f.ID, map[string]any{"name": "Use", "kind": "agent", "prompt": "File the bug.", "settings": map[string]any{"credentials": []int64{jira.ID}}})
	a.startAt(f.ID, use.ID)
	var status int
	// What the agent says holds its token; what is kept of it doesn't.
	a.worker.onMessages("Use", map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "calling with Bearer {token}"}}})
	a.worker.onAgent("Use", func(_ context.Context, run agentRun) runResult {
		status = credentialsCall(t, "GET", run.Credentials[0].URL+"/rest/api/2/myself", run.CredentialsToken, false, "").status
		return runResult{OK: true, Output: "filed with " + run.CredentialsToken}
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone || status != http.StatusOK {
		t.Fatalf("run %s, the call answered %d; steps %s", d.Status, status, describeSteps(d))
	}
	run := a.worker.runsOf("Use")[0]
	env := "$CREDENTIAL_" + jira.Variable + "_URL"
	if !strings.Contains(run.Prompt, "Authorization: Bearer $CREDENTIALS_TOKEN") || !strings.Contains(run.Prompt, "- "+jira.Name+", "+svc.srv.URL+": "+env) {
		t.Errorf("the agent was told:\n%s", run.Prompt)
	}
	if strings.Contains(run.Raw, secret) || strings.Contains(run.Prompt, run.CredentialsToken) {
		t.Errorf("the worker was sent the secret, or the prompt the token:\n%s", run.Raw)
	}
	if s := svc.requests(); len(s) != 1 || s[0].authorization != "Bearer "+secret {
		t.Errorf("the service got %+v", s)
	}
	step := onlyStep(t, d, "Use")
	_, messages := a.do("GET", fmt.Sprintf("/api/runs/%d/steps/%d/messages", d.ID, step.ID), nil)
	if !strings.Contains(string(messages), "calling with Bearer [redacted]") || strings.Contains(string(messages), run.CredentialsToken) {
		t.Errorf("the conversation was kept as %s", messages)
	}
	if step.Output != "filed with [redacted]" {
		t.Errorf("the step's output was kept as %q", step.Output)
	}
}

// A grant made by the replica running the step reaches the service through
// another replica too, until the step ends.
func TestCredentialsReachEveryReplica(t *testing.T) {
	a := newApp(t)
	other := startApp(t, a.worker.url)
	svc := newCredentialsService(t)
	secret := "replica-secret-0123456789"
	c := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Shared"), "baseUrl": svc.srv.URL + "/api", "auth": "header", "secret": secret, "everyone": true})
	f := a.newFlow("Replicated credentials")
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "true", "settings": map[string]any{"credentials": []int64{c.ID}}})
	a.startAt(f.ID, fetch.ID)
	var status int
	a.worker.onScript("Fetch", func(_ context.Context, run scriptRun) runResult {
		status = credentialsCall(t, "GET", strings.Replace(run.Credentials[0].URL, a.url, other.url, 1)+"/things", run.CredentialsToken, false, "").status
		return runResult{OK: true, Output: "fetched"}
	})
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	if s := svc.requests(); status != http.StatusOK || len(s) != 1 || s[0].path != "/api/things" || s[0].authorization != "Bearer "+secret {
		t.Errorf("through the other replica, the call answered %d and the service got %+v", status, s)
	}
	run := a.worker.scriptRunsSoFar()[0]
	for _, at := range []*app{a, other} {
		target := strings.Replace(run.Credentials[0].URL, a.url, at.url, 1) + "/things"
		if c := credentialsCall(t, "GET", target, run.CredentialsToken, false, ""); c.status != http.StatusForbidden {
			t.Errorf("after the step, its token got %d at %s", c.status, at.url)
		}
	}
}

// Only admins keep credentials. A flow uses those given everyone, or a
// team of its owner's, which its board is offered; a step picking another
// fails, and a credential taken from a team no longer reaches its service
// for the steps going.
func TestCredentialsAreForTheTeamsGivenThem(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	svc := newCredentialsService(t)
	bob := usersNewPerson("Bob")
	bobRow := usersAdd(a, bob.email)
	var mine, theirs teamsTeam
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Billing", bobRow.ID), "members": []int64{bobRow.ID}}, http.StatusCreated, &mine)
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Others", bobRow.ID), "members": []int64{}}, http.StatusCreated, &theirs)
	given := map[string]any{"name": credentialsName(t, "Ledger"), "baseUrl": svc.srv.URL, "auth": "basic", "username": "svc", "secret": "ledger-secret-0123",
		"everyone": false, "teams": []int64{mine.ID}}
	ledger := credentialsAdd(a, given)
	other := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Vault"), "baseUrl": svc.srv.URL, "auth": "header", "secret": "vault-secret-0123",
		"everyone": false, "teams": []int64{theirs.ID}})
	if ledger.Everyone || len(ledger.Teams) != 1 || ledger.Teams[0].ID != mine.ID {
		t.Errorf("given %+v", ledger)
	}
	a.refused("POST", "/api/credentials", map[string]any{"name": credentialsName(t, "Nobody"), "baseUrl": svc.srv.URL, "auth": "header", "secret": "s"},
		http.StatusBadRequest, "give it to everyone, or to the teams")

	usersSignIn(a, p, bob)
	a.refused("GET", "/api/credentials", nil, http.StatusForbidden, "only an admin")
	a.refused("POST", "/api/credentials", map[string]any{"name": credentialsName(t, "Mine"), "baseUrl": svc.srv.URL, "auth": "header", "secret": "s", "everyone": true},
		http.StatusForbidden, "only an admin")
	f := a.newFlow("Ledger")
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "user", "id": bobRow.ID}, http.StatusOK, nil)
	var offered []credentialsItem
	a.call("GET", fmt.Sprintf("/api/credentials?flow=%d", f.ID), nil, http.StatusOK, &offered)
	if !slices.ContainsFunc(offered, func(c credentialsItem) bool { return c.ID == ledger.ID }) || slices.ContainsFunc(offered, func(c credentialsItem) bool { return c.ID == other.ID }) {
		t.Errorf("bob's flow is offered %+v", offered)
	}
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "true", "settings": map[string]any{"credentials": []int64{ledger.ID}}})
	a.startAt(f.ID, fetch.ID)
	release := make(chan struct{})
	reached := make(chan int, 2)
	a.worker.onScript("Fetch", func(ctx context.Context, run scriptRun) runResult {
		target := run.Credentials[0].URL + "/entries"
		reached <- credentialsCall(t, "GET", target, run.CredentialsToken, false, "").status
		select {
		case <-release:
		case <-ctx.Done():
		}
		reached <- credentialsCall(t, "GET", target, run.CredentialsToken, false, "").status
		return runResult{OK: true, Output: "fetched"}
	})
	id := a.startRun(f.ID).ID
	if status := receive(t, reached, "the first call"); status != http.StatusOK {
		t.Errorf("bob's flow reached its team's credential with %d", status)
	}
	// Taken from the team while the step goes, it reaches nothing more.
	usersSignIn(a, p, usersAda)
	given["teams"] = []int64{theirs.ID}
	a.call("PUT", fmt.Sprintf("/api/credentials/%d", ledger.ID), given, http.StatusOK, nil)
	close(release)
	if status := receive(t, reached, "the second call"); status != http.StatusForbidden {
		t.Errorf("once taken from the team, the step's call answered %d", status)
	}
	a.finish(id)

	// Now its owner's team may not use it, so the step fails as it starts.
	d := a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Fetch"); s.Status != "error" || !strings.Contains(s.Error, "a credential the process uses isn't one this flow may use") || strings.Contains(s.Error, ledger.Name) {
		t.Errorf("a step picking a credential its flow may not use: %s %s", s.Status, s.Error)
	}
	// A global flow uses only everyone's.
	given["teams"], given["everyone"] = []int64{mine.ID}, false
	a.call("PUT", fmt.Sprintf("/api/credentials/%d", ledger.ID), given, http.StatusOK, nil)
	usersShare(a, f.ID)
	a.call("GET", fmt.Sprintf("/api/credentials?flow=%d", f.ID), nil, http.StatusOK, &offered)
	if slices.ContainsFunc(offered, func(c credentialsItem) bool { return c.ID == ledger.ID }) {
		t.Errorf("a global flow is offered a team's credential: %+v", offered)
	}
	if s := onlyStep(t, a.finish(a.startRun(f.ID).ID), "Fetch"); s.Status != "error" {
		t.Errorf("a global flow picking a team's credential: %s", s.Status)
	}
	a.call("DELETE", fmt.Sprintf("/api/teams/%d", theirs.ID), nil, http.StatusNoContent, nil)
}

// The setup file names a process's credentials, never the credentials
// themselves; importing one naming a credential the app hasn't is
// refused, and the assistant is told those its flow may use.
func TestCredentialsInTheSetupFile(t *testing.T) {
	a := setupApp(t)
	svc := newCredentialsService(t)
	prefix := fmt.Sprintf("creds%d", time.Now().UnixNano())
	credentialsAdd(a, map[string]any{"name": "Issue Tracker", "baseUrl": svc.srv.URL + "/jira", "auth": "header", "secret": "setup-secret-0123",
		"everyone": true, "description": "Where bugs are filed."})
	file := fmt.Sprintf(`spoletta: 1
flows:
  - name: %s Bugs
    processes:
      - name: File
        kind: script
        script: curl "$CREDENTIAL_ISSUE_TRACKER_URL/rest/api/2/issue"
        credentials: [issue tracker]
`, prefix)
	setupImportOK(a, file)
	out := setupExport(a, prefix)
	if !strings.Contains(out, "credentials:\n") || !strings.Contains(out, "- Issue Tracker") || strings.Contains(out, "setup-secret") || strings.Contains(out, svc.srv.URL) {
		t.Errorf("the export:\n%s", out)
	}
	status, body := setupImport(a, strings.Replace(file, "issue tracker", "No Such", 1))
	if status != http.StatusBadRequest || !strings.Contains(string(body), `no credential named "No Such"`) {
		t.Errorf("importing an unknown credential = %d %s", status, body)
	}
	status, body = setupImport(a, strings.Replace(file, "kind: script", "kind: human", 1))
	if status != http.StatusBadRequest || !strings.Contains(string(body), "only an agent or script process uses credentials") {
		t.Errorf("importing a human process with credentials = %d %s", status, body)
	}

	var flows []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
	var f int64
	for _, fl := range flows {
		if fl.Name == prefix+" Bugs" {
			f = fl.ID
		}
	}
	a.worker.onAgent("Assistant", func(_ context.Context, run agentRun) runResult {
		if want := "<credentials>\n- Issue Tracker, " + svc.srv.URL + "/jira, at $CREDENTIAL_ISSUE_TRACKER_URL: Where bugs are filed.\n"; !strings.Contains(run.Prompt, want) {
			t.Errorf("the assistant's prompt lacks %q", want)
		}
		if strings.Contains(run.Prompt, "setup-secret") {
			t.Errorf("the assistant was told the secret")
		}
		return runResult{OK: true, Output: "Nothing to change."}
	})
	askAssistant(a, f, "Which services can it call?")
	if reply := assistantReply(a, f); reply.Status != "done" {
		t.Errorf("the reply = %+v", reply)
	}
}

// A follow-up of an agent step gets its process's credentials anew, as its
// run's version picked them, with a token of its own that ends with it.
func TestFollowUpsGetTheirProcessesCredentials(t *testing.T) {
	a := newApp(t)
	svc := newCredentialsService(t)
	secret := "followup-secret-0123456789"
	c := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Tracker"), "baseUrl": svc.srv.URL + "/api", "auth": "header", "secret": secret, "everyone": true})
	f := a.newFlow("Followed up")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
	}}, http.StatusOK, nil)
	fix := a.newProcess(f.ID, map[string]any{"name": "Fix", "kind": "agent", "prompt": "Fix it.", "settings": map[string]any{"credentials": []int64{c.ID}}})
	a.output(fix.ID, a.newObject(f.ID, "Diff").ID)
	a.startAt(f.ID, fix.ID)
	var status int
	a.worker.onAgent("Fix", func(_ context.Context, run agentRun) runResult {
		if run.Resume {
			status = credentialsCall(t, "GET", run.Credentials[0].URL+"/issues/7", run.CredentialsToken, false, "").status
			return runResult{OK: true, Output: "commented"}
		}
		return submitted(data("fixed"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
	}
	step := onlyStep(t, d, "Fix")
	// The draft dropping the credential leaves the run's version as it was.
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", fix.ID), map[string]any{"settings": map[string]any{}}, http.StatusOK, nil)
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, step.ID), map[string]any{"message": "Comment on the issue."}, http.StatusCreated, nil)
	a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
		s := onlyStep(t, d, "Fix")
		return len(s.FollowUps) == 1 && s.FollowUps[0].Status == stepDone
	})
	runs := a.worker.runsOf("Fix")
	first, followUp := runs[0], runs[len(runs)-1]
	if !followUp.Resume || len(followUp.Credentials) != 1 || followUp.Credentials[0].URL != fmt.Sprintf("%s/credentials/%d", a.url, c.ID) ||
		followUp.CredentialsToken == "" || followUp.CredentialsToken == first.CredentialsToken {
		t.Fatalf("the follow-up got %+v with the token %q, the step %q", followUp.Credentials, followUp.CredentialsToken, first.CredentialsToken)
	}
	if s := svc.requests(); status != http.StatusOK || len(s) != 1 || s[0].path != "/api/issues/7" || s[0].authorization != "Bearer "+secret {
		t.Errorf("the follow-up's call answered %d, and the service got %+v", status, s)
	}
	if got := credentialsCall(t, "GET", followUp.Credentials[0].URL+"/issues/7", followUp.CredentialsToken, false, ""); got.status != http.StatusForbidden {
		t.Errorf("after the follow-up, its token got %d", got.status)
	}
}

// A step takes the credentials its run's version picked: a run of the
// version published keeps its credential while the draft picks another,
// which a run of the board as it is takes, and the version's diff names
// the change.
func TestCredentialsComeFromTheRunsVersion(t *testing.T) {
	a := newApp(t)
	svc := newCredentialsService(t)
	published := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Published"), "baseUrl": svc.srv.URL + "/a", "auth": "header", "secret": "published-secret-0123", "everyone": true})
	draft := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Draft"), "baseUrl": svc.srv.URL + "/b", "auth": "header", "secret": "draft-secret-0123", "everyone": true})
	f := a.newFlow("Versioned credentials")
	fetch := a.newProcess(f.ID, map[string]any{"name": "Fetch", "kind": kindScript, "script": "true", "settings": map[string]any{"credentials": []int64{published.ID}}})
	a.startAt(f.ID, fetch.ID)
	v := pubPublish(a, f.ID, "with the published credential")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", fetch.ID), map[string]any{"settings": map[string]any{"credentials": []int64{draft.ID}}}, http.StatusOK, nil)
	a.worker.onScript("Fetch", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: "fetched"} })

	var onVersion run
	a.call("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), map[string]any{"published": true}, http.StatusCreated, &onVersion)
	if d := a.finish(onVersion.ID); d.Status != runDone {
		t.Fatalf("the published run %s; steps %s", d.Status, describeSteps(d))
	}
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("the draft's run %s; steps %s", d.Status, describeSteps(d))
	}
	runs := a.worker.scriptRunsSoFar()
	if len(runs) != 2 || len(runs[0].Credentials) != 1 || runs[0].Credentials[0].Name != published.Name || len(runs[1].Credentials) != 1 || runs[1].Credentials[0].Name != draft.Name {
		t.Errorf("the runs got %+v and %+v", runs[0].Credentials, runs[1].Credentials)
	}

	d := pubDiffOf(a, fmt.Sprintf("/api/versions/%d/diff", v.ID))
	if len(d.Processes) != 1 || len(d.Processes[0].Fields) != 1 || d.Processes[0].Fields[0].Field != "credentials" ||
		d.Processes[0].Fields[0].Before != fmt.Sprintf("[%d]", published.ID) || d.Processes[0].Fields[0].After != fmt.Sprintf("[%d]", draft.ID) {
		t.Errorf("the draft against the version published: %+v", d.Processes)
	}
}
