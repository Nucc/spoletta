package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUserLabelColumn(t *testing.T) {
	got := userLabelColumn("r.started_by")
	if !strings.Contains(got, "u.id = r.started_by)") || !strings.HasPrefix(got, "COALESCE(") || !strings.HasSuffix(got, ", '')") {
		t.Errorf("userLabelColumn = %q", got)
	}
}

// A session is known by its token's hash, which is sha256 in hex.
func TestHashToken(t *testing.T) {
	if got := hashToken(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("hashToken(\"\") = %q", got)
	}
	a, b := hashToken("one"), hashToken("two")
	if len(a) != 64 || a == b || a != hashToken("one") {
		t.Errorf("hashToken gives %q and %q", a, b)
	}
}

// Signing in comes back only to a path of this webapp, never to another
// site, however the next page is written.
func TestLocalPath(t *testing.T) {
	for in, want := range map[string]string{
		"/":                    "/",
		"/#/flows/3":           "/#/flows/3",
		"/flows?x=1":           "/flows?x=1",
		"/a//b":                "/a//b",
		"":                     "/",
		"flows":                "/",
		"https://evil.example": "/",
		"http://evil.example/": "/",
		"//evil.example":       "/",
		"//evil.example/path":  "/",
		"/\\evil.example":      "/",
		"\\\\evil.example":     "/",
		"\\/evil.example":      "/",
		"javascript:alert(1)":  "/",
		" /flows":              "/",
		"evil.example/flows":   "/",
		"///evil.example":      "/",
	} {
		if got := localPath(in); got != want {
			t.Errorf("localPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestUser(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/flows", nil)
	if requestUser(r) != nil || requestUserID(r) != nil {
		t.Error("a request without a session has a user")
	}
	u := &user{ID: 9, Email: "ann@example.com"}
	r = r.WithContext(context.WithValue(r.Context(), userKey{}, u))
	if requestUser(r) != u {
		t.Error("the request's user isn't the one in its context")
	}
	if id := requestUserID(r); id == nil || *id != 9 {
		t.Errorf("requestUserID = %v, want 9", id)
	}
}

// The provider sends people back to PUBLIC_URL when it is set, else to the
// address the browser reached the webapp by; cookies are Secure over https.
func TestAuthRedirectURL(t *testing.T) {
	for _, c := range []struct {
		publicURL, host, proto string
		tls                    bool
		want                   string
		secure                 bool
	}{
		{"https://flows.example.com", "localhost:8080", "", false, "https://flows.example.com/api/auth/callback", true},
		{"http://flows.internal", "localhost:8080", "https", true, "http://flows.internal/api/auth/callback", false},
		{"", "localhost:8080", "", false, "http://localhost:8080/api/auth/callback", false},
		{"", "flows.example.com", "https", false, "https://flows.example.com/api/auth/callback", true},
		{"", "flows.example.com", "", true, "https://flows.example.com/api/auth/callback", true},
	} {
		a := &auth{publicURL: c.publicURL}
		r := httptest.NewRequest("GET", "/api/auth/login", nil)
		r.Host = c.host
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		} else {
			r.TLS = nil
		}
		if got := a.redirectURL(r); got != c.want {
			t.Errorf("redirectURL(%+v) = %q, want %q", c, got, c.want)
		}
		if got := a.secure(r); got != c.secure {
			t.Errorf("secure(%+v) = %v, want %v", c, got, c.secure)
		}
	}
}

// Without an admin adding them, only those in ADMIN_EMAILS or with an email
// at one of OIDC_ALLOWED_DOMAINS sign in; a domain is matched whole.
func TestAuthAdmitted(t *testing.T) {
	a := &auth{admins: map[string]bool{"boss@elsewhere.com": true}, domains: []string{"example.com"}}
	for email, want := range map[string]bool{
		"boss@elsewhere.com":      true,
		"ann@example.com":         true,
		"other@elsewhere.com":     false,
		"ann@sub.example.com":     false,
		"ann@example.com.evil.io": false,
		"ann@evilexample.com":     false,
		"example.com":             false,
		"ann@b@example.com":       false,
		"":                        false,
	} {
		if got := a.admitted(email); got != want {
			t.Errorf("admitted(%q) = %v, want %v", email, got, want)
		}
	}
	if (&auth{admins: map[string]bool{}}).admitted("ann@example.com") {
		t.Error("someone is admitted with neither admins nor domains")
	}
}

func idToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".c2lnbmF0dXJl"
}

// The ID token's signature isn't checked, as it comes straight from the
// provider, but it must be from the issuer, for this webapp, not expired,
// for this sign-in, and name someone.
func TestAuthIDClaims(t *testing.T) {
	a := &auth{issuer: "https://id.example.com", clientID: "spoletta"}
	later := float64(time.Now().Add(time.Hour).Unix())
	valid := func() map[string]any {
		return map[string]any{
			"iss": "https://id.example.com", "aud": "spoletta", "exp": later, "nonce": "n1",
			"sub": "u-1", "email": "ann@example.com", "email_verified": true, "name": "Ann",
		}
	}
	with := func(k string, v any) string {
		c := valid()
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return idToken(t, c)
	}
	for _, c := range []struct {
		name, token, nonce, problem string
	}{
		{"valid", idToken(t, valid()), "n1", ""},
		{"audience list", with("aud", []string{"other", "spoletta"}), "n1", ""},
		{"issuer with slash", with("iss", "https://id.example.com/"), "n1", ""},
		{"padded payload", idToken(t, valid()), "n1", ""},
		{"other issuer", with("iss", "https://evil.example.com"), "n1", "is from"},
		{"other audience", with("aud", "other"), "n1", "isn't for this webapp"},
		{"other audiences", with("aud", []string{"a", "b"}), "n1", "isn't for this webapp"},
		{"no audience", with("aud", nil), "n1", "no audience"},
		{"audience a number", with("aud", 5), "n1", "no audience"},
		{"expired", with("exp", float64(time.Now().Add(-time.Minute).Unix())), "n1", "expired"},
		{"no expiry", with("exp", nil), "n1", "expired"},
		{"other nonce", idToken(t, valid()), "n2", "another sign-in"},
		{"no nonce", with("nonce", nil), "n1", "another sign-in"},
		{"nobody", with("sub", ""), "n1", "names nobody"},
		{"two parts", "a.b", "n1", "isn't a JWT"},
		{"four parts", "a.b.c.d", "n1", "isn't a JWT"},
		{"bad base64", "a.!!!.c", "n1", "the ID token"},
		{"not JSON", "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c", "n1", "the ID token"},
	} {
		token := c.token
		if c.name == "padded payload" {
			parts := strings.Split(token, ".")
			parts[1] = base64.URLEncoding.EncodeToString(mustDecode(t, parts[1]))
			token = strings.Join(parts, ".")
		}
		got, err := a.idClaims(token, c.nonce)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("%s: idClaims says %q, want %q", c.name, msg, c.problem)
			continue
		}
		if c.problem == "" && (got.Subject != "u-1" || got.Email != "ann@example.com" || got.Name != "Ann" || got.EmailVerified == nil || !*got.EmailVerified) {
			t.Errorf("%s: idClaims = %+v", c.name, got)
		}
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
