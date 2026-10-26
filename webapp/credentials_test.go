package main

import (
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
