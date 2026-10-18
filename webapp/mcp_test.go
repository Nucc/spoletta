package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// pi takes server names differing only in - and _, or case, for the same.
func TestMCPNameKey(t *testing.T) {
	for in, want := range map[string]string{
		"github":     "github",
		"GitHub":     "github",
		"my-server":  "my_server",
		"my_server":  "my_server",
		"My-Server_": "my_server_",
	} {
		if got := mcpNameKey(in); got != want {
			t.Errorf("mcpNameKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMCPServerProblem(t *testing.T) {
	for _, c := range []struct {
		s       mcpServer
		problem string
	}{
		{mcpServer{Name: "github", URL: "https://mcp.example.com/mcp"}, ""},
		{mcpServer{Name: "Local_1-a", URL: "http://localhost:3000"}, ""},
		{mcpServer{Name: strings.Repeat("a", 64), URL: "https://x.io", Description: strings.Repeat("d", 1024)}, ""},
		{mcpServer{Name: "", URL: "https://x.io"}, "the name"},
		{mcpServer{Name: strings.Repeat("a", 65), URL: "https://x.io"}, "the name"},
		{mcpServer{Name: "has space", URL: "https://x.io"}, "the name"},
		{mcpServer{Name: "dots.not", URL: "https://x.io"}, "the name"},
		{mcpServer{Name: "a", URL: ""}, "the URL"},
		{mcpServer{Name: "a", URL: "ftp://x.io"}, "the URL"},
		{mcpServer{Name: "a", URL: "file:///etc/passwd"}, "the URL"},
		{mcpServer{Name: "a", URL: "https://"}, "the URL"},
		{mcpServer{Name: "a", URL: "mcp.example.com"}, "the URL"},
		{mcpServer{Name: "a", URL: "https://x.io/" + strings.Repeat("p", 2048)}, "the URL"},
		{mcpServer{Name: "a", URL: "https://x.io", Description: strings.Repeat("d", 1025)}, "the description"},
		{mcpServer{Name: "a", URL: "https://x.io", Auth: authToken}, ""},
		{mcpServer{Name: "a", URL: "https://x.io", Auth: "basic"}, "auth must be"},
		// A connector takes its token, and its service's API unless told otherwise.
		{mcpServer{Name: "gh", Connector: "github"}, ""},
		{mcpServer{Name: "gh", Connector: "github", URL: "https://ghe.example.com/api/v3"}, ""},
		{mcpServer{Name: "gh", Connector: "github", Auth: authOAuth}, "takes a token"},
		{mcpServer{Name: "gh", Connector: "gitlab"}, "no connector"},
	} {
		c.s.defaults()
		msg := c.s.problem()
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("problem(%.80q, %.80q) = %q, want %q", c.s.Name, c.s.URL, msg, c.problem)
		}
	}
}

// The origin is the one the browser reached the webapp by, through a proxy
// too, which says so in X-Forwarded-Proto and X-Forwarded-Host.
func TestRequestOrigin(t *testing.T) {
	for _, c := range []struct {
		host, fwdHost, fwdProto string
		tls                     bool
		want                    string
	}{
		{"localhost:8080", "", "", false, "http://localhost:8080"},
		{"localhost:8080", "", "", true, "https://localhost:8080"},
		{"localhost:8080", "", "https", false, "https://localhost:8080"},
		{"localhost:8080", "", "http", false, "http://localhost:8080"},
		{"localhost:8080", "flows.example.com", "https", false, "https://flows.example.com"},
		{"localhost:5173", "localhost:8080", "", false, "http://localhost:8080"},
	} {
		r := httptest.NewRequest("GET", "/api/mcp-servers", nil)
		r.Host = c.host
		r.TLS = nil
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if c.fwdHost != "" {
			r.Header.Set("X-Forwarded-Host", c.fwdHost)
		}
		if c.fwdProto != "" {
			r.Header.Set("X-Forwarded-Proto", c.fwdProto)
		}
		if got := requestOrigin(r); got != c.want {
			t.Errorf("requestOrigin(%+v) = %q, want %q", c, got, c.want)
		}
		if got := callbackURL(r); got != c.want+"/api/mcp-oauth/callback" {
			t.Errorf("callbackURL(%+v) = %q", c, got)
		}
	}
}

func TestRandomToken(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		tok := randomToken()
		b, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil || len(b) != 32 {
			t.Fatalf("randomToken() = %q, not 32 bytes in URL-safe base64", tok)
		}
		if seen[tok] {
			t.Fatalf("randomToken() repeated %q", tok)
		}
		seen[tok] = true
	}
}

// A token without expires_in doesn't expire as far as the webapp knows.
func TestExpiresAt(t *testing.T) {
	for _, n := range []int64{0, -5} {
		if at := expiresAt(tokenResponse{ExpiresIn: n}); at != nil {
			t.Errorf("expiresAt(%d) = %v, want nil", n, at)
		}
	}
	before := time.Now()
	at := expiresAt(tokenResponse{ExpiresIn: 3600})
	if at == nil || at.Location() != time.UTC || at.Before(before.Add(time.Hour)) || at.After(time.Now().Add(time.Hour)) {
		t.Errorf("expiresAt(3600) = %v, want an hour from now in UTC", at)
	}
}

func TestGetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "accept", http.StatusNotAcceptable)
			return
		}
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"name": "x"}`))
		case "/bad":
			w.Write([]byte(`{"name": `))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var out struct{ Name string }
	if err := getJSON(context.Background(), srv.URL+"/ok", &out); err != nil || out.Name != "x" {
		t.Errorf("getJSON(/ok) = %v, %+v", err, out)
	}
	if err := getJSON(context.Background(), srv.URL+"/missing", &out); err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Errorf("getJSON(/missing) = %v, want status 404", err)
	}
	if err := getJSON(context.Background(), srv.URL+"/bad", &out); err == nil {
		t.Error("getJSON(/bad) took a broken body")
	}
}

// An authorization server for discoverOAuth: its routes answer JSON, the
// others not found.
type oauthFake struct {
	srv    *httptest.Server
	routes map[string]func(base string) any
	// What the MCP server's own address says when asked without a token.
	authenticate string
}

func newOAuthFake(t *testing.T) *oauthFake {
	f := &oauthFake{routes: map[string]func(string) any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if route, ok := f.routes[r.URL.Path]; ok {
			json.NewEncoder(w).Encode(route(f.srv.URL))
			return
		}
		if r.URL.Path == "/mcp" && f.authenticate != "" {
			w.Header().Set("WWW-Authenticate", f.authenticate)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func authServerMeta(issuer string) func(string) any {
	return func(base string) any {
		return map[string]string{
			"issuer":                 base + issuer,
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
			"registration_endpoint":  base + "/register",
		}
	}
}

// Discovery follows the MCP authorization spec: the protected resource's
// metadata, named by the server when it turns a request away or else at the
// well-known path for its path or its origin, names the authorization
// server, whose metadata names the endpoints. A server from before that is
// its own authorization server.
func TestDiscoverOAuth(t *testing.T) {
	t.Run("named by the server", func(t *testing.T) {
		f := newOAuthFake(t)
		f.authenticate = `Bearer realm="mcp", resource_metadata="` + f.srv.URL + `/meta"`
		f.routes["/meta"] = func(base string) any {
			return map[string]any{"resource": base + "/mcp-resource", "authorization_servers": []string{base + "/tenant"}, "scopes_supported": []string{"read", "write"}}
		}
		f.routes["/.well-known/oauth-authorization-server/tenant"] = authServerMeta("/tenant")
		c, err := discoverOAuth(context.Background(), f.srv.URL+"/mcp")
		if err != nil {
			t.Fatal(err)
		}
		want := oauthConfig{
			resource: f.srv.URL + "/mcp-resource", scopes: []string{"read", "write"},
			authorizationEndpoint: f.srv.URL + "/authorize", tokenEndpoint: f.srv.URL + "/token", registrationEndpoint: f.srv.URL + "/register",
		}
		if !reflect.DeepEqual(c, want) {
			t.Errorf("discoverOAuth = %+v, want %+v", c, want)
		}
	})

	t.Run("at the well-known path for the server's path", func(t *testing.T) {
		f := newOAuthFake(t)
		f.routes["/.well-known/oauth-protected-resource/mcp"] = func(base string) any {
			return map[string]any{"authorization_servers": []string{base}}
		}
		f.routes["/.well-known/openid-configuration"] = authServerMeta("")
		c, err := discoverOAuth(context.Background(), f.srv.URL+"/mcp/")
		if err != nil {
			t.Fatal(err)
		}
		if c.resource != f.srv.URL+"/mcp/" || c.tokenEndpoint != f.srv.URL+"/token" || c.scopes != nil {
			t.Errorf("discoverOAuth = %+v", c)
		}
	})

	t.Run("its own authorization server", func(t *testing.T) {
		f := newOAuthFake(t)
		f.routes["/.well-known/oauth-authorization-server"] = authServerMeta("")
		c, err := discoverOAuth(context.Background(), f.srv.URL+"/mcp")
		if err != nil {
			t.Fatal(err)
		}
		if c.resource != f.srv.URL+"/mcp" || c.authorizationEndpoint != f.srv.URL+"/authorize" || c.registrationEndpoint != f.srv.URL+"/register" {
			t.Errorf("discoverOAuth = %+v", c)
		}
	})

	t.Run("metadata naming another issuer", func(t *testing.T) {
		f := newOAuthFake(t)
		f.routes["/.well-known/oauth-authorization-server"] = func(base string) any {
			return map[string]string{"issuer": "https://evil.example.com", "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token"}
		}
		if _, err := discoverOAuth(context.Background(), f.srv.URL+"/mcp"); err == nil || !strings.Contains(err.Error(), "names the issuer") {
			t.Errorf("discoverOAuth = %v, want another issuer refused", err)
		}
	})

	t.Run("metadata without endpoints", func(t *testing.T) {
		f := newOAuthFake(t)
		f.routes["/.well-known/oauth-authorization-server"] = func(base string) any {
			return map[string]string{"issuer": base, "authorization_endpoint": base + "/authorize"}
		}
		if _, err := discoverOAuth(context.Background(), f.srv.URL+"/mcp"); err == nil || !strings.Contains(err.Error(), "no OAuth metadata") {
			t.Errorf("discoverOAuth = %v, want no metadata found", err)
		}
	})

	t.Run("no metadata", func(t *testing.T) {
		f := newOAuthFake(t)
		if _, err := discoverOAuth(context.Background(), f.srv.URL+"/mcp"); err == nil || !strings.Contains(err.Error(), "no OAuth metadata") {
			t.Errorf("discoverOAuth = %v, want no metadata found", err)
		}
	})
}

// The webapp registers as a public client for its callback, and takes the
// client id the authorization server gives it.
func TestRegisterClient(t *testing.T) {
	var got map[string]any
	status, answer := http.StatusCreated, `{"client_id": "c-1"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		w.Write([]byte(answer))
	}))
	defer srv.Close()

	id, err := registerClient(context.Background(), srv.URL, "https://flows.example.com/api/mcp-oauth/callback")
	if err != nil || id != "c-1" {
		t.Fatalf("registerClient = %q, %v", id, err)
	}
	if !reflect.DeepEqual(got["redirect_uris"], []any{"https://flows.example.com/api/mcp-oauth/callback"}) ||
		got["token_endpoint_auth_method"] != "none" ||
		!reflect.DeepEqual(got["grant_types"], []any{"authorization_code", "refresh_token"}) {
		t.Errorf("registered %v", got)
	}

	for _, c := range []struct {
		status          int
		answer, problem string
	}{
		{http.StatusOK, `{"client_id": "c-2"}`, ""},
		{http.StatusBadRequest, ` {"error": "invalid_redirect_uri"} `, `status 400: {"error": "invalid_redirect_uri"}`},
		{http.StatusCreated, `{}`, "no client id"},
		{http.StatusCreated, `not json`, "no client id"},
	} {
		status, answer = c.status, c.answer
		_, err := registerClient(context.Background(), srv.URL, "https://x/cb")
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("registerClient(%d %s) = %q, want %q", c.status, c.answer, msg, c.problem)
		}
	}
}

func TestRequestToken(t *testing.T) {
	var form url.Values
	status, answer := http.StatusOK, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(body))
		w.WriteHeader(status)
		w.Write([]byte(answer))
	}))
	defer srv.Close()

	for _, c := range []struct {
		status          int
		answer, problem string
		want            tokenResponse
	}{
		{http.StatusOK, `{"access_token": "a", "refresh_token": "r", "expires_in": 60}`, "", tokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: 60}},
		{http.StatusBadRequest, `{"error": "invalid_grant", "error_description": "code used"}`, "status 400: invalid_grant: code used", tokenResponse{Error: "invalid_grant", Description: "code used"}},
		{http.StatusUnauthorized, `{"error": "invalid_client"}`, "status 401: invalid_client", tokenResponse{Error: "invalid_client"}},
		// A token endpoint answering 200 without a token gave none.
		{http.StatusOK, `{"token_type": "bearer"}`, "status 200", tokenResponse{}},
		{http.StatusBadGateway, ` gateway down `, "status 502: gateway down", tokenResponse{}},
	} {
		status, answer = c.status, c.answer
		got, code, err := requestToken(context.Background(), srv.URL, url.Values{"grant_type": {"authorization_code"}, "code": {"xyz"}})
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("requestToken(%d %s) says %q, want %q", c.status, c.answer, msg, c.problem)
		}
		if code != c.status || got != c.want {
			t.Errorf("requestToken(%d %s) = %+v, %d", c.status, c.answer, got, code)
		}
		if form.Get("grant_type") != "authorization_code" || form.Get("code") != "xyz" {
			t.Errorf("the token endpoint got %v", form)
		}
	}
}
