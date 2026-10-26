package main

import (
	"encoding/base64"
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
		{"https://gw.example.com", "/v1/:batchGet", "", "", "https://gw.example.com/v1/:batchGet"},
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
		// What Windows reads as the file before a :, a stream of it.
		{"/..:/admin", "/..:/admin"},
		{"/..::$INDEX_ALLOCATION/admin", "/..::$INDEX_ALLOCATION/admin"},
		{"/. .:x/admin", "/.%20.:x/admin"},
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
	blank := credentialRedactor(c, secret)
	at, _ := url.Parse("https://gw.example.com/tenant/v1/things?key=" + url.QueryEscape(secret))
	for loc, want := range map[string]string{
		"/tenant/v1/things/2?page=2&key=" + url.QueryEscape(secret): "http://webapp:8080/credentials/7/things/2?page=2",
		"other":                            "http://webapp:8080/credentials/7/other",
		"https://gw.example.com/tenant/v1": "http://webapp:8080/credentials/7",
		"/tenant/v2/x":                     "https://gw.example.com/tenant/v2/x",
		"https://login.example.com/?key=" + url.QueryEscape(secret) + "&next=1": "https://login.example.com/?next=1",
	} {
		got, ok := b.location(c, at, loc, blank)
		if !ok || got != want {
			t.Errorf("%s became %q (%v), want %s", loc, got, ok, want)
		}
	}
	for _, loc := range []string{
		"https://login.example.com/?token=" + url.QueryEscape(secret),
		"https://login.example.com/" + url.PathEscape(secret),
		"https://login.example.com/#" + secret,
		// Cut short, or in another case.
		"https://login.example.com/?token=" + url.QueryEscape(secret)[:9],
		"https://login.example.com/?token=" + strings.ToLower(url.QueryEscape(secret)),
	} {
		if got, ok := b.location(c, at, loc, blank); ok {
			t.Errorf("%s was passed on as %s", loc, got)
		}
	}
	// A Link header's pages within the base URL are the proxy's too.
	link := `<https://gw.example.com/tenant/v1/things?page=2&key=` + url.QueryEscape(secret) + `>; rel="next", <https://docs.example.com/paging>; rel="help"`
	if got, ok := b.link(c, at, link, blank); !ok || got != `<http://webapp:8080/credentials/7/things?page=2>; rel="next", <https://docs.example.com/paging>; rel="help"` {
		t.Errorf("the Link header became %q (%v)", got, ok)
	}
	if got, ok := b.link(c, at, `<https://docs.example.com/`+secret+`>; rel="help"`, blank); ok {
		t.Errorf("a Link header holding the secret was passed on as %s", got)
	}
}

// The secret's parameter goes first, without the step's own values of it.
func TestSecretFirst(t *testing.T) {
	for raw, want := range map[string]string{
		"":                       "key=s%2Bt",
		"a=aaaaaaaa&q=Rome":      "key=s%2Bt&a=aaaaaaaa&q=Rome",
		"key=forged&q=a%20b&key": "key=s%2Bt&q=a%20b",
		"k%65y=forged&x=1;key=2": "key=s%2Bt",
		"a=%zz&b=1":              "key=s%2Bt&b=1",
	} {
		if got := secretFirst("key", "s+t", raw); got != want {
			t.Errorf("%q became %q, want %q", raw, got, want)
		}
	}
}

func TestSecretForms(t *testing.T) {
	basic := credentialRedactor(credential{Auth: credentialBasic, Username: "me@example.com"}, "tok en")
	for _, want := range []string{"tok en", "tok+en", "tok%20en", "bWVAZXhhbXBsZS5jb206dG9rIGVu"} {
		if !basic.holds("x" + want + "y") {
			t.Errorf("basic auth's forms miss %q: %v", want, basic.secrets)
		}
	}
	header := credentialRedactor(credential{Auth: credentialHeader, Header: "Authorization", Template: "token {secret}"}, "abc")
	if !header.holds("token abc") || header.holds("token ab") {
		t.Errorf("a header's forms are %v", header.secrets)
	}
	// The template's own text is no piece of the secret's.
	long := credentialRedactor(credential{Auth: credentialHeader, Header: "Authorization", Template: "Bearer {secret}"}, "0123456789abcdef")
	if long.holds("Bearer another-token") || long.holds("Basic dXNlcjpvdGhlcg==") {
		t.Errorf("the template's text is taken for the secret")
	}
}

// A secret is found in an answer as a service could write it: URL-encoded
// in either case, HTML-escaped, in base64 alone or within something
// longer, and in JSON.
func TestSecretFormsEncoded(t *testing.T) {
	secret := `k<e>y&"1'/2+3 ab`
	blank := credentialRedactor(credential{Auth: credentialQuery, Param: "key"}, secret)
	for _, want := range []string{
		strings.ToLower(url.QueryEscape(secret)),
		strings.ToLower(url.PathEscape(secret)),
		"k&lt;e&gt;y&amp;&#34;1&#39;/2+3 ab",
		"k&lt;e&gt;y&amp;&quot;1&#x27;/2+3 ab",
		`k\u003ce\u003ey\u0026\"1'/2+3 ab`,
		`k\u003Ce\u003Ey\u0026\"1'\/2+3 ab`,
		`k<e>y&\"1'\/2+3 ab`,
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawURLEncoding.EncodeToString([]byte(secret)),
	} {
		if got := blank.text(`{"echo": "` + want + `"}`); got != `{"echo": "[redacted]"}` {
			t.Errorf("%s came out as %s", want, got)
		}
	}
	// Within what else is encoded with it, wherever it starts in a group,
	// but for the groups it shares with what is around it.
	for _, before := range []string{"", "u:", "us:", "use:"} {
		enc := base64.StdEncoding.EncodeToString([]byte(before + secret + "!"))
		if got := blank.text(enc); !strings.Contains(got, redacted) || len(got)-len(redacted) > (len(before)+2)/3*4+4 {
			t.Errorf("base64 of %q came out as %s", before+secret, got)
		}
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
	if got := newRedactor("tok-123456").text("Bearer tok-123456 and TOK-123456"); got != "Bearer [redacted] and [redacted]" {
		t.Errorf("the text became %q", got)
	}
	if got := newRedactor("tok-123456").text("tok-123456tok-123456"); got != "[redacted]" {
		t.Errorf("secrets in a row became %q", got)
	}
}

// An echo cut short keeps fewer than secretPiece of the secret's
// characters in a row, wherever it is cut, at either end, in whatever
// pieces it streams through, and with nothing more after them.
func TestRedactorBlanksPiecesOfSecrets(t *testing.T) {
	secret := "Zq8Lw2Xv9Kp4Rt6Ym1Nc"
	for k := 1; k < len(secret); k++ {
		for _, echo := range []string{
			`{"error": "no such path /aaaa?key=` + secret[:k] + `", "x": 1}`,
			`{"error": "no such path ` + secret[len(secret)-k:] + `&a=1"}`,
			"...key=" + secret[:k],
		} {
			whole := credentialRedactor(credential{Auth: credentialQuery, Param: "key"}, secret).text(echo)
			for split := 0; split <= len(echo); split++ {
				r := credentialRedactor(credential{Auth: credentialQuery, Param: "key"}, secret)
				got := string(r.next([]byte(echo[:split]))) + string(r.next([]byte(echo[split:]))) + string(r.flush())
				if got != whole {
					t.Fatalf("%q split at %d came out as %q, but whole as %q", echo, split, got, whole)
				}
			}
			for i := 0; i+secretPiece <= len(secret); i++ {
				if strings.Contains(whole, secret[i:i+secretPiece]) {
					t.Errorf("%q came out as %q, holding %s", echo, whole, secret[i:i+secretPiece])
				}
			}
		}
	}
	// What only looks like it, in a few characters, goes on.
	r := credentialRedactor(credential{Auth: credentialQuery, Param: "key"}, secret)
	if got := r.text("Zq8Lw2X and Zq8 and 1Nc"); got != "Zq8Lw2X and Zq8 and 1Nc" {
		t.Errorf("fewer than a piece came out as %q", got)
	}
}

// A step's tokens are blanked out as they are, in base64 within something
// longer, and as basic auth's user:token for the usernames steps get.
func TestTokenForms(t *testing.T) {
	token := "Qm9ndXMtdG9rZW4tZm9yLXRlc3RzLW9ubHktMDEyMzQ1"
	blank := newRedactor(tokenForms([]string{token, ""}, egressUser, "x-access-token")...)
	for user, whole := range map[string]bool{"step": true, "x-access-token": true, "whoever-they-say": false, "ab": false, "": false} {
		got := blank.text(base64.StdEncoding.EncodeToString([]byte(user + ":" + token)))
		// But for the groups holding the username and the token's first and
		// last bytes, which share them.
		if left := len(got) - len(redacted); !strings.Contains(got, redacted) || whole && left != 0 || left > (len(user)+1+2)/3*4+4 {
			t.Errorf("basic auth as %q came out as %s", user, got)
		}
	}
	if got := blank.text("Bearer " + token); got != "Bearer [redacted]" {
		t.Errorf("the token came out as %s", got)
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
		"Cookie": "a=1", "X-Api-Key": "the step's own", "X-Custom": "1", "Host": "evil.example.com", "X_Api_Key": "theirs",
		"Notion_Version": "2022-06-28",
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
	for _, bad := range []string{"X-Original-URL", "Authorization", "x-forwarded-prefix", "Range", "Accept-Encoding", "Proxy-Foo", "X-Http-Method-Override",
		"X-Original-URI", "X-Host", "X-Original-Host", "X-Forwarded-For", "X_Forwarded_Host", "X_Original_URL", "x_http_method_override", "proxy_authorization"} {
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
	f.Headers[3] = "X_Api_Key"
	if msg := f.check(true); msg == "" {
		t.Errorf("a credential lets steps send the header its secret goes in, with _ for -")
	}
	if f := (credentialFields{Name: "Jira", BaseURL: "https://x.example.com", Auth: credentialHeader, Header: "Content_Length", Secret: "s", Everyone: true}); f.check(true) == "" {
		t.Errorf("the secret may go in Content_Length")
	}
	f.Headers = f.Headers[:3]
	if msg := f.check(true); msg != "" || len(f.Headers) != 1 || f.Headers[0] != "Notion-Version" {
		t.Errorf("headers checked into %v: %s", f.Headers, msg)
	}
}

// Changing where a kept secret goes needs it again: another base URL, even
// another path at the same origin, or another header, parameter or way;
// another template, or the origin written another way, doesn't.
func TestCredentialChangesSendingTheSecretElsewhere(t *testing.T) {
	was := credential{BaseURL: "https://jira.example.com/rest", Auth: credentialHeader, Header: "Authorization", Template: "Bearer {secret}"}
	same := credentialFields{BaseURL: "https://JIRA.example.com:443/rest", Auth: credentialHeader, Header: "authorization", Template: "token {secret}"}
	if sendsElsewhere(was, same) {
		t.Errorf("the same base URL written another way needs the secret again")
	}
	if credentialBase("https://x.example.com") != credentialBase("https://x.example.com/") {
		t.Errorf("a base URL without a path isn't the same as one with /")
	}
	for _, f := range []credentialFields{
		{BaseURL: "https://jira.example.com/other", Auth: credentialHeader, Header: "Authorization"},
		{BaseURL: "https://jira.example.com/rest/", Auth: credentialHeader, Header: "Authorization"},
		{BaseURL: "https://jira.example.com/Rest", Auth: credentialHeader, Header: "Authorization"},
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
	blank := credentialRedactor(credential{Auth: credentialQuery, Param: "key"}, `a/b"c`)
	for _, want := range []string{`a/b\"c`, `a\/b\"c`, "a%2Fb%22c"} {
		if !blank.holds(`{"echo": "` + want + `"}`) {
			t.Errorf("the forms miss %s: %v", want, blank.secrets)
		}
	}
}

// Only the answer's headers saying what it is, and how many calls are
// left, come back; never the credential's own, nor what would have a
// browser act on the webapp's origin.
func TestAnswerHeaders(t *testing.T) {
	c := credential{Auth: credentialHeader, Header: "X-Request-Id"}
	for _, k := range []string{"Content-Type", "Etag", "Retry-After", "X-Ratelimit-Remaining", "Ratelimit-Policy", "Ratelimit", "X-Total-Count"} {
		if !answerHeader(c, k) {
			t.Errorf("%s doesn't come back", k)
		}
	}
	for _, k := range []string{"X-Request-Id", "Set-Cookie", "Www-Authenticate", "Clear-Site-Data", "Refresh", "Service-Worker-Allowed",
		"Access-Control-Allow-Origin", "Alt-Svc", "Content-Length", "Content-Encoding", "Digest", "Content-Digest", "Content-Md5",
		"Location", "Link", "X-Kept"} {
		if answerHeader(c, k) {
			t.Errorf("%s comes back", k)
		}
	}
}
