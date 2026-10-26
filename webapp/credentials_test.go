package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCredentialVariables(t *testing.T) {
	for name, want := range map[string]string{
		"Jira":            "JIRA",
		"Internal API":    "INTERNAL_API",
		"billing-v2 prod": "BILLING_V2_PROD",
		"a__b--c":         "A_B_C",
	} {
		if got := credentialVariable(name); got != want {
			t.Errorf("%q is %q as a variable, want %q", name, got, want)
		}
	}
	if got := credentialEnv("JIRA"); got != "CREDENTIAL_JIRA_URL" {
		t.Errorf("a credential is reached at %s", got)
	}
}

// A credential's service is reached over https, or over http only at the
// webapp's own machine, at a URL with neither a username nor a query.
func TestCredentialURLs(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://jira.example.com":           true,
		"https://api.example.com/v1/":        true,
		"http://127.0.0.1:9000/api":          true,
		"http://localhost:9000":              true,
		"http://[::1]:9000":                  true,
		"http://jira.example.com":            false,
		"http://10.0.0.5":                    false,
		"ftp://jira.example.com":             false,
		"https://user:pass@jira.example.com": false,
		"https://jira.example.com/?key=x":    false,
		"https://jira.example.com/#top":      false,
		"https://":                           false,
		"jira.example.com":                   false,
		"https://jira.example.com/a b":       false,
		"":                                   false,
	} {
		if msg := checkCredentialURL(raw); (msg == "") != ok {
			t.Errorf("%q: %q", raw, msg)
		}
	}
}

func TestCredentialFields(t *testing.T) {
	f := credentialFields{Name: "  Jira   Cloud ", BaseURL: "https://jira.example.com", Auth: credentialHeader, Secret: " s3cret-token ", Everyone: true, Teams: []int64{4}}
	if msg := f.check(true); msg != "" {
		t.Fatal(msg)
	}
	if f.Name != "Jira Cloud" || f.Header != "Authorization" || f.Template != "Bearer {secret}" || f.Secret != "s3cret-token" || f.Teams != nil {
		t.Errorf("checked into %+v", f)
	}
	for _, bad := range []credentialFields{
		{Name: "1password", BaseURL: "https://x.example.com", Auth: credentialHeader, Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "http://x.example.com", Auth: credentialHeader, Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: "cookie", Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Header: "Host", Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Header: "X Key", Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Template: "Bearer", Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Template: "Bearer {secret}\r\nX: y", Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialBasic, Username: "a:b", Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialQuery, Param: "api key", Secret: "s", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Secret: "a\nb", Everyone: true},
		{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Secret: "s"},
	} {
		if msg := bad.check(true); msg == "" {
			t.Errorf("%+v was taken", bad)
		}
	}
	// A change keeps the secret it doesn't give.
	keep := credentialFields{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialQuery, Param: "key", Everyone: true}
	if msg := keep.check(false); msg != "" {
		t.Errorf("a change without a secret: %s", msg)
	}
}

// A path is joined to the base URL's, and one that would leave it, or hide
// a way out from a service decoding it again, isn't.
func TestCredentialTargets(t *testing.T) {
	for _, c := range []struct {
		base, rest, escaped, query, want string
	}{
		{"https://jira.example.com", "/rest/api/2/issue/X-1", "", "fields=summary", "https://jira.example.com/rest/api/2/issue/X-1?fields=summary"},
		{"https://gw.example.com/tenant/v1/", "/things", "", "", "https://gw.example.com/tenant/v1/things"},
		{"https://gw.example.com/tenant/v1", "", "", "", "https://gw.example.com/tenant/v1"},
		{"https://gw.example.com/tenant/v1", "/", "", "", "https://gw.example.com/tenant/v1/"},
		{"https://gw.example.com", "/a b/c", "/a%20b/c", "", "https://gw.example.com/a%20b/c"},
		{"https://gw.example.com", "/models/x:generate", "", "", "https://gw.example.com/models/x:generate"},
	} {
		escaped := c.escaped
		if escaped == "" {
			escaped = c.rest
		}
		u, ok := credentialTarget(c.base, c.rest, escaped, c.query)
		if !ok || u.String() != c.want {
			t.Errorf("%s + %s went to %v (%v), want %s", c.base, c.rest, u, ok, c.want)
		}
	}
	for _, c := range []struct{ rest, escaped string }{
		{"/../admin", "/../admin"},
		{"/a/../../admin", "/a/../../admin"},
		{"/./x", "/./x"},
		{"/a/b", "/a%2Fb"},
		{"/a/b", "/a%2fb"},
		{`/a\b`, "/a%5Cb"},
		{"/a%2f..%2fadmin", "/a%252f..%252fadmin"},
		{"/a?x=1", "/a%3Fx=1"},
		{"/a#x", "/a%23x"},
		{"//evil.example.com/x", "//evil.example.com/x"},
		{"/a//b", "/a//b"},
		{"/a\x00b", "/a%00b"},
		{"/a\nb", "/a%0Ab"},
		{"evil", "evil"},
		// What Tomcat, Jetty and Spring read as a step up, dropping what
		// follows a ;, IIS trimming trailing dots and spaces, and Unicode
		// normalising full-width dots.
		{"/..;/admin", "/..;/admin"},
		{"/..;/admin", "/%2e%2e;/admin"},
		{"/.;/x", "/.;/x"},
		{"/..;x=1/admin", "/..;x=1/admin"},
		{"/.. /admin", "/..%20/admin"},
		{"/. ./admin", "/.%20./admin"},
		{"/.../admin", "/.../admin"},
		{"/a;b", "/a;b"},
		{"/\uff0e\uff0e/admin", "/%EF%BC%8E%EF%BC%8E/admin"},
		{"/caf\u00e9", "/caf%C3%A9"},
	} {
		if u, ok := credentialTarget("https://gw.example.com/tenant/v1", c.rest, c.escaped, ""); ok {
			t.Errorf("%q (sent as %q) went to %s", c.rest, c.escaped, u)
		}
	}
}

// A redirect within the base URL is the proxy's, so following it goes
// through the proxy again; one elsewhere loses the secret's parameter, and
// one still holding the secret is dropped.
func TestCredentialRedirects(t *testing.T) {
	b := newCredentialBroker(nil, "http://webapp:8080/")
	c := credential{ID: 7, BaseURL: "https://gw.example.com/tenant/v1", Auth: credentialQuery, Param: "key"}
	secret := "s3cret+token/x"
	forms := secretForms(c, secret)
	at, _ := url.Parse("https://gw.example.com/tenant/v1/things?key=" + url.QueryEscape(secret))
	for loc, want := range map[string]string{
		"/tenant/v1/things/2?page=2&key=" + url.QueryEscape(secret): "http://webapp:8080/credentials/7/things/2?page=2",
		"other":                            "http://webapp:8080/credentials/7/other",
		"https://gw.example.com/tenant/v1": "http://webapp:8080/credentials/7",
		"/tenant/v2/x":                     "https://gw.example.com/tenant/v2/x",
		"https://login.example.com/?key=" + url.QueryEscape(secret) + "&next=1": "https://login.example.com/?next=1",
	} {
		got, ok := b.location(c, at, loc, forms)
		if !ok || got != want {
			t.Errorf("%s became %q (%v), want %s", loc, got, ok, want)
		}
	}
	for _, loc := range []string{
		"https://login.example.com/?token=" + url.QueryEscape(secret),
		"https://login.example.com/" + url.PathEscape(secret),
		"https://login.example.com/#" + secret,
	} {
		if got, ok := b.location(c, at, loc, forms); ok {
			t.Errorf("%s was passed on as %s", loc, got)
		}
	}
}

func TestSecretForms(t *testing.T) {
	basic := secretForms(credential{Auth: credentialBasic, Username: "me@example.com"}, "tok en")
	for _, want := range []string{"tok en", "tok+en", "tok%20en", "bWVAZXhhbXBsZS5jb206dG9rIGVu"} {
		if !holdsSecret("x"+want+"y", basic) {
			t.Errorf("basic auth's forms miss %q: %v", want, basic)
		}
	}
	header := secretForms(credential{Auth: credentialHeader, Header: "Authorization", Template: "token {secret}"}, "abc")
	if !holdsSecret("token abc", header) || holdsSecret("token ab", header) {
		t.Errorf("a header's forms are %v", header)
	}
}

func TestAgentsAreToldTheirCredentials(t *testing.T) {
	if got := withCredentials("Do it.", nil); got != "Do it." {
		t.Errorf("with no credentials the prompt became %q", got)
	}
	got := withCredentials("Do it.", []credentialAccess{{Name: "Jira", BaseURL: "https://jira.example.com", Env: "CREDENTIAL_JIRA_URL", URL: "http://webapp/credentials/1"}})
	for _, want := range []string{"Authorization: Bearer $CREDENTIALS_TOKEN", "- Jira, https://jira.example.com: $CREDENTIAL_JIRA_URL", "never their values"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt doesn't say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "http://webapp/credentials/1") {
		t.Errorf("the prompt holds the proxy's URL itself:\n%s", got)
	}
}

// A secret is blanked out of what streams through, even split across
// pieces; what can't be the start of one goes on at once.
func TestRedactor(t *testing.T) {
	r := newRedactor("abcdefgh", "", "xyz12345", "abcdefgh")
	var out []string
	for _, p := range []string{"one abc", "def", "gh two xyz", "1234", "5 three ab"} {
		out = append(out, string(r.next([]byte(p))))
	}
	out = append(out, string(r.flush()))
	if got := strings.Join(out, ""); got != "one [redacted] two [redacted] three ab" {
		t.Errorf("came out as %q", got)
	}
	if out[0] != "one " || out[1] != "" {
		t.Errorf("held back %q", out)
	}
	if got := string(newRedactor("abcdefgh").next([]byte("plain text"))); got != "plain text" {
		t.Errorf("held back what can't be a secret: %q", got)
	}
	if got := string(newRedactor().next([]byte("abcdefgh"))); got != "abcdefgh" {
		t.Errorf("with nothing to blank out: %q", got)
	}
	if got := newRedactor("tok-123456").text("Bearer tok-123456 and tok-123456"); got != "Bearer [redacted] and [redacted]" {
		t.Errorf("the text became %q", got)
	}
}

// Only the headers a client sends to say what it sends and wants, and
// those the credential names, go on; never one that could send the call
// elsewhere, carries the step's token, or the connection names.
func TestPassedHeaders(t *testing.T) {
	c := credential{Auth: credentialHeader, Header: "X-Api-Key", Headers: []string{"Notion-Version"}}
	h := http.Header{}
	for k, v := range map[string]string{
		"Accept": "application/json", "Content-Type": "application/json", "If-None-Match": `"x"`, "Notion-Version": "2022-06-28",
		"Prefer": "return=minimal", "Connection": "Prefer, close", "X-Original-Url": "/admin", "X-Rewrite-Url": "/admin",
		"X-Forwarded-Prefix": "/admin", "X-Forwarded-Host": "evil.example.com", "X-Http-Method-Override": "DELETE",
		"Range": "bytes=0-3", "If-Range": `"x"`, "Accept-Encoding": "gzip", "Authorization": "Bearer step-token",
		"Cookie": "a=1", "X-Api-Key": "the step's own", "X-Custom": "1", "Host": "evil.example.com",
	} {
		h.Set(k, v)
	}
	got := passedHeaders(c, h)
	want := []string{"Accept", "Content-Type", "If-None-Match", "Notion-Version"}
	if len(got) != len(want) {
		t.Errorf("passed %v, want %v", got, want)
	}
	for _, k := range want {
		if got.Get(k) == "" {
			t.Errorf("%s didn't go on: %v", k, got)
		}
	}
	for _, bad := range []string{"X-Original-URL", "Authorization", "x-forwarded-prefix", "Range", "Accept-Encoding", "Proxy-Foo", "X-Http-Method-Override"} {
		f := credentialFields{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Secret: "s", Everyone: true, Headers: []string{bad}}
		if msg := f.check(true); msg == "" {
			t.Errorf("a credential lets steps send %s", bad)
		}
	}
	f := credentialFields{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Header: "X-Api-Key", Secret: "s", Everyone: true,
		Headers: []string{" notion-version ", "Notion-Version", "", "x-api-key"}}
	if msg := f.check(true); msg == "" {
		t.Errorf("a credential lets steps send the header its secret goes in")
	}
	f.Headers = f.Headers[:3]
	if msg := f.check(true); msg != "" || len(f.Headers) != 1 || f.Headers[0] != "Notion-Version" {
		t.Errorf("headers checked into %v: %s", f.Headers, msg)
	}
}

// Changing where a kept secret goes needs it again: another origin, or
// another header, parameter or way; another path at the same origin, or
// another template, doesn't.
func TestCredentialChangesSendingTheSecretElsewhere(t *testing.T) {
	was := credential{BaseURL: "https://jira.example.com/rest", Auth: credentialHeader, Header: "Authorization", Template: "Bearer {secret}"}
	same := credentialFields{BaseURL: "https://JIRA.example.com:443/other", Auth: credentialHeader, Header: "authorization", Template: "token {secret}"}
	if sendsElsewhere(was, same) {
		t.Errorf("another path at the same origin needs the secret again")
	}
	for _, f := range []credentialFields{
		{BaseURL: "https://evil.example.com/rest", Auth: credentialHeader, Header: "Authorization"},
		{BaseURL: "https://jira.example.com:8443/rest", Auth: credentialHeader, Header: "Authorization"},
		{BaseURL: "http://localhost/rest", Auth: credentialHeader, Header: "Authorization"},
		{BaseURL: "https://jira.example.com/rest", Auth: credentialHeader, Header: "X-Echo"},
		{BaseURL: "https://jira.example.com/rest", Auth: credentialQuery, Param: "key"},
		{BaseURL: "https://jira.example.com/rest", Auth: credentialBasic},
	} {
		if !sendsElsewhere(was, f) {
			t.Errorf("%+v keeps the secret", f)
		}
	}
}

// The machine's own addresses are dialled only for a base URL naming them.
func TestCredentialClientsDialLoopbackOnlyWhenAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if _, err := credentialClient(false).Get(srv.URL); err == nil || !strings.Contains(err.Error(), "isn't an address steps may reach") {
		t.Errorf("the client for other hosts dialled the loopback: %v", err)
	}
	resp, err := credentialClient(true).Get(srv.URL)
	if err != nil {
		t.Fatalf("the client for the machine's own: %v", err)
	}
	resp.Body.Close()
}

// A secret is found in an answer as JSON would write it, too.
func TestSecretFormsInJSON(t *testing.T) {
	forms := secretForms(credential{Auth: credentialQuery, Param: "key"}, `a/b"c`)
	for _, want := range []string{`a/b\"c`, `a\/b\"c`, "a%2Fb%22c"} {
		if !holdsSecret(`{"echo": "`+want+`"}`, forms) {
			t.Errorf("the forms miss %s: %v", want, forms)
		}
	}
}
