package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MCP servers agents can use, such as the company MCP gateway's. An admin
// configures them once in the settings, and then everyone connects each for
// themselves there, by signing in through the browser (OAuth with PKCE), so
// an agent acts on a server as whoever its run is for; see runUser. An agent
// process uses only the servers it selects, none by default. Without
// sign-in, anyone configures them, and a connection is everyone's.
//
// The agent never sees the servers' tokens: the webapp hands each agent step
// a token of its own that reaches only the step's servers, through the proxy
// at /mcp/{id}, which puts the server's token on the request. The step's
// token stops working when the step ends. The servers' tokens are sealed
// with SECRET_KEY; see secrets.go.
//
// A server may instead take a token an admin sets, such as an API key: that
// one connection is everyone's, and nobody signs in. The connectors built
// into the webapp are such servers (see connectors.go): the proxy answers
// their requests itself, from the connector's operations, which call the
// connector's service with the token.
//
// An agent process may keep to some of a server's tools, which the proxy
// holds it to: tools/list leaves out the others, and calling one is refused.
// Unless it picks, it has all of an MCP server's tools, and of a connector
// only those that change nothing.

type mcpServer struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	// How it is connected: authOAuth by each person signing in, authToken
	// with the one token an admin sets.
	Auth string `json:"auth"`
	// The built-in connector it is, by name, if it is one; its URL is then
	// where the connector reaches its service's API.
	Connector string `json:"connector,omitempty"`
	// Whether whoever asked connected it, so their agents can use it; for
	// a server taking a token, whether its token is set.
	Connected bool `json:"connected"`
}

const (
	authOAuth = "oauth"
	authToken = "token"
)

func migrateMCP(db *sql.DB) error {
	stmts := []string{`CREATE TABLE IF NOT EXISTS mcp_servers (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(64) NOT NULL UNIQUE,
		url VARCHAR(2048) NOT NULL,
		description VARCHAR(1024) NOT NULL DEFAULT '',
		client_id VARCHAR(255) NOT NULL DEFAULT '',
		redirect_uri VARCHAR(2048) NOT NULL DEFAULT '',
		token_endpoint VARCHAR(2048) NOT NULL DEFAULT '',
		access_token TEXT NULL,
		refresh_token TEXT NULL,
		expires_at DATETIME NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
		// Each person's connection to a server; the user 0, whom nobody is,
		// holds the one everyone uses without sign-in.
		`CREATE TABLE IF NOT EXISTS mcp_tokens (
			server_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			access_token TEXT NULL,
			refresh_token TEXT NULL,
			expires_at DATETIME NULL,
			PRIMARY KEY (server_id, user_id),
			FOREIGN KEY (server_id) REFERENCES mcp_servers(id) ON DELETE CASCADE
		)`,
		// Servers held their one connection themselves before people
		// connected their own; it becomes the one without sign-in.
		`INSERT IGNORE INTO mcp_tokens (server_id, user_id, access_token, refresh_token, expires_at)
			SELECT id, 0, access_token, refresh_token, expires_at FROM mcp_servers WHERE access_token IS NOT NULL OR refresh_token IS NOT NULL`,
		`UPDATE mcp_servers SET access_token = NULL, refresh_token = NULL, expires_at = NULL WHERE access_token IS NOT NULL OR refresh_token IS NOT NULL`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, def string }{
		{"auth", "VARCHAR(16) NOT NULL DEFAULT 'oauth'"},
		{"connector", "VARCHAR(64) NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(db, "mcp_servers", c.name, c.def); err != nil {
			return err
		}
	}
	return nil
}

// A server's columns, for scanMCPServer, with whether the user whose id is
// the first argument connected it. The token of a server taking one is the
// connection of the user 0, whom nobody is.
const mcpServerColumns = "id, name, url, description, auth, connector, EXISTS (SELECT 1 FROM mcp_tokens t WHERE t.server_id = mcp_servers.id AND t.user_id = IF(mcp_servers.auth = 'token', 0, ?))"

// Whose connections a request is about: its user's, or without sign-in, everyone's.
func tokenOwner(r *http.Request) int64 {
	if u := requestUser(r); u != nil {
		return u.ID
	}
	return 0
}

// Turns away a request to configure servers from someone not an admin, when people sign in.
func refuseMCPConfig(w http.ResponseWriter, r *http.Request) bool {
	return requestUser(r) != nil && refuseNonAdmin(w, r)
}

func scanMCPServer(row interface{ Scan(...any) error }, s *mcpServer) error {
	return row.Scan(&s.ID, &s.Name, &s.URL, &s.Description, &s.Auth, &s.Connector, &s.Connected)
}

// pi names tools mcp__<server>__<tool>, and takes server names that differ
// only in - and _ for the same server.
var mcpNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func mcpNameKey(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
}

// Why the server can't be saved, or "".
func (s mcpServer) problem() string {
	if !mcpNamePattern.MatchString(s.Name) {
		return "the name may only have letters, digits, - and _, up to 64"
	}
	if s.Connector != "" && findConnector(s.Connector) == nil {
		return fmt.Sprintf("there is no connector named %s", quote(s.Connector))
	}
	if c := findConnector(s.Connector); c != nil && s.URL == "" {
		return fmt.Sprintf("%s needs the URL it is at, such as %s", c.Title, c.URLExample)
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(s.URL) > 2048 {
		return "the URL must be an http or https address"
	}
	if len(s.Description) > 1024 {
		return "the description may be up to 1024 characters"
	}
	if s.Auth != authOAuth && s.Auth != authToken {
		return "auth must be oauth or token"
	}
	if s.Connector != "" && s.Auth != authToken {
		return "a connector takes a token"
	}
	return ""
}

// Fills in what a server added without saying takes: signing in, and for a
// connector a token, its service's API and its description.
func (s *mcpServer) defaults() {
	if c := findConnector(s.Connector); c != nil {
		if s.URL == "" {
			s.URL = c.URL
		}
		if s.Description == "" {
			s.Description = c.Description
		}
		if s.Auth == "" {
			s.Auth = authToken
		}
	}
	if s.Auth == "" {
		s.Auth = authOAuth
	}
}

// Whether another server has a name pi takes for the same one.
func mcpNameTaken(ctx context.Context, db *sql.DB, name string, except int64) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM mcp_servers WHERE id <> ?", except)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var other string
		if err := rows.Scan(&other); err != nil {
			return false, err
		}
		if mcpNameKey(other) == mcpNameKey(name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Signs in to MCP servers, keeps their tokens fresh, and proxies agents' requests.
type mcpBroker struct {
	db *sql.DB
	// Where the worker reaches the proxy.
	proxyURL string
	// Whether people sign in, so connections are theirs.
	signIn bool

	mu sync.Mutex
	// Sign-ins under way, by OAuth state.
	pending map[string]mcpSignIn
	// The servers each agent step's token reaches, and whose connections it
	// reaches them with.
	grants map[string]mcpGrant
	// Held while refreshing a connection's token, so concurrent requests
	// don't spend a refresh token the server rotates.
	refreshing map[mcpConnection]*sync.Mutex
}

// Someone's connection to a server.
type mcpConnection struct {
	server, user int64
}

type mcpGrant struct {
	user int64
	// The tools the step has of each server it reaches; nil is all of them.
	servers map[int64]toolFilter
}

type mcpSignIn struct {
	server        int64
	user          int64
	verifier      string
	redirect      string
	clientID      string
	tokenEndpoint string
	resource      string
	expires       time.Time
}

func newMCPBroker(db *sql.DB, proxyURL string, signIn bool) *mcpBroker {
	return &mcpBroker{
		db:         db,
		proxyURL:   strings.TrimRight(proxyURL, "/"),
		signIn:     signIn,
		pending:    map[string]mcpSignIn{},
		grants:     map[string]mcpGrant{},
		refreshing: map[mcpConnection]*sync.Mutex{},
	}
}

var errMCPNotConnected = errors.New("not connected")

// Sign-in and token requests; MCP requests through the proxy have no timeout,
// since tool calls can take long and responses stream.
var oauthClient = &http.Client{Timeout: 30 * time.Second}

func registerMCPRoutes(mux *http.ServeMux, db *sql.DB, b *mcpBroker) {
	mux.HandleFunc("GET /api/mcp-servers", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT "+mcpServerColumns+" FROM mcp_servers ORDER BY name", tokenOwner(r))
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		servers := []mcpServer{}
		for rows.Next() {
			var s mcpServer
			if err := scanMCPServer(rows, &s); err != nil {
				serverError(w, err)
				return
			}
			servers = append(servers, s)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, servers)
	})

	mux.HandleFunc("POST /api/mcp-servers", func(w http.ResponseWriter, r *http.Request) {
		if refuseMCPConfig(w, r) {
			return
		}
		var s mcpServer
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		s.Name, s.URL, s.Description = strings.TrimSpace(s.Name), strings.TrimSpace(s.URL), strings.TrimSpace(s.Description)
		s.defaults()
		if msg := s.problem(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if taken, err := mcpNameTaken(r.Context(), db, s.Name, 0); err != nil {
			serverError(w, err)
			return
		} else if taken {
			http.Error(w, "another server has that name", http.StatusConflict)
			return
		}
		res, err := db.ExecContext(r.Context(), "INSERT INTO mcp_servers (name, url, description, auth, connector) VALUES (?, ?, ?, ?, ?)", s.Name, s.URL, s.Description, s.Auth, s.Connector)
		if err != nil {
			serverError(w, err)
			return
		}
		s.ID, _ = res.LastInsertId()
		s.Connected = false
		writeJSON(w, http.StatusCreated, s)
	})

	// Changing the URL signs everyone out, since their sign-ins were for the old one.
	mux.HandleFunc("PATCH /api/mcp-servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseMCPConfig(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var s mcpServer
		if err := scanMCPServer(db.QueryRowContext(r.Context(), "SELECT "+mcpServerColumns+" FROM mcp_servers WHERE id = ?", tokenOwner(r), id), &s); errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "server not found", http.StatusNotFound)
			return
		} else if err != nil {
			serverError(w, err)
			return
		}
		var patch struct {
			Name        *string `json:"name"`
			URL         *string `json:"url"`
			Description *string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		oldURL := s.URL
		if patch.Name != nil {
			s.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.URL != nil {
			s.URL = strings.TrimSpace(*patch.URL)
		}
		if patch.Description != nil {
			s.Description = strings.TrimSpace(*patch.Description)
		}
		if msg := s.problem(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if taken, err := mcpNameTaken(r.Context(), db, s.Name, id); err != nil {
			serverError(w, err)
			return
		} else if taken {
			http.Error(w, "another server has that name", http.StatusConflict)
			return
		}
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			q := "UPDATE mcp_servers SET name = ?, url = ?, description = ? WHERE id = ?"
			if s.URL != oldURL {
				q = "UPDATE mcp_servers SET name = ?, url = ?, description = ?, client_id = '', redirect_uri = '', token_endpoint = '' WHERE id = ?"
				s.Connected = false
				if _, err := tx.ExecContext(r.Context(), "DELETE FROM mcp_tokens WHERE server_id = ?", id); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(r.Context(), q, s.Name, s.URL, s.Description, id)
			return err
		})
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	// Processes that select it fail to run until it is removed from them.
	mux.HandleFunc("DELETE /api/mcp-servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseMCPConfig(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM mcp_servers WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Opened in a window of its own: sends the browser to the server's
	// sign-in page, which comes back to the callback below.
	mux.HandleFunc("GET /api/mcp-servers/{id}/connect", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		to, err := b.startSignIn(r.Context(), id, tokenOwner(r), callbackURL(r))
		if err != nil {
			log.Printf("mcp: sign in to server %d: %v", id, err)
			signInPage(w, http.StatusBadGateway, "Couldn't start signing in", err.Error())
			return
		}
		http.Redirect(w, r, to, http.StatusFound)
	})

	mux.HandleFunc("GET /api/mcp-oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		name, err := b.finishSignIn(r.Context(), r.URL.Query())
		if err != nil {
			log.Printf("mcp: sign-in callback: %v", err)
			signInPage(w, http.StatusBadRequest, "Signing in failed", err.Error())
			return
		}
		signInPage(w, http.StatusOK, "Connected", fmt.Sprintf("%s is connected. You can close this window.", name))
	})

	mux.HandleFunc("POST /api/mcp-servers/{id}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM mcp_tokens WHERE server_id = ? AND user_id = ?", id, tokenOwner(r)); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// A server taking a token has it set by an admin, as everyone's
	// connection; nobody sees it again.
	mux.HandleFunc("PUT /api/mcp-servers/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		if refuseMCPConfig(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		token := strings.TrimSpace(body.Token)
		if token == "" || len(token) > maxModelKey {
			http.Error(w, fmt.Sprintf("the token must be from 1 to %d characters", maxModelKey), http.StatusBadRequest)
			return
		}
		var auth, connector string
		if err := db.QueryRowContext(r.Context(), "SELECT auth, connector FROM mcp_servers WHERE id = ?", id).Scan(&auth, &connector); errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "server not found", http.StatusNotFound)
			return
		} else if err != nil {
			serverError(w, err)
			return
		}
		if auth != authToken {
			http.Error(w, "the server is connected by signing in to it, not with a token", http.StatusBadRequest)
			return
		}
		if c := findConnector(connector); c != nil && c.checkToken != nil {
			if err := c.checkToken(token); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		sealed, err := secrets.seal(mcpTokenPlace("access_token", id, 0), token)
		if err != nil {
			serverError(w, err)
			return
		}
		if _, err := db.ExecContext(r.Context(), `INSERT INTO mcp_tokens (server_id, user_id, access_token) VALUES (?, 0, ?)
			ON DUPLICATE KEY UPDATE access_token = VALUES(access_token), refresh_token = NULL, expires_at = NULL`, id, sealed); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /api/mcp-servers/{id}/token", func(w http.ResponseWriter, r *http.Request) {
		if refuseMCPConfig(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM mcp_tokens WHERE server_id = ? AND user_id = 0", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// The tools a server offers, for picking those an agent process uses.
	mux.HandleFunc("GET /api/mcp-servers/{id}/tools", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		tools, err := b.tools(r.Context(), id, tokenOwner(r))
		switch {
		case errors.Is(err, errNotFound):
			http.Error(w, "server not found", http.StatusNotFound)
		case errors.Is(err, errMCPNotConnected):
			http.Error(w, "connect the server to see its tools", http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			writeJSON(w, http.StatusOK, tools)
		}
	})

	// Streamable HTTP uses these three.
	for _, m := range []string{"GET", "POST", "DELETE"} {
		mux.HandleFunc(m+" /mcp/{id}", b.proxy)
	}
}

// The callback on the address the browser reached the webapp by, which the
// sign-in page sends it back to; through the dev server too, which proxies /api.
func callbackURL(r *http.Request) string {
	return requestOrigin(r) + "/api/mcp-oauth/callback"
}

// The scheme and host the browser reached the webapp by.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = h
	}
	return scheme + "://" + host
}

// The page the sign-in window ends on. It tells the window that opened it,
// which then refreshes the servers, and closes itself once connected.
func signInPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	closeScript := ""
	if status == http.StatusOK {
		closeScript = "setTimeout(() => window.close(), 1500)"
	}
	fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<title>%[1]s</title>
<style>body{font:15px system-ui,sans-serif;margin:3rem auto;max-width:32rem;padding:0 1rem;color:#222}</style>
<h1>%[1]s</h1>
<p>%[2]s</p>
<script>
if (window.opener) window.opener.postMessage({ type: "mcp-sign-in" }, location.origin)
%[3]s
</script>
`, html.EscapeString(title), html.EscapeString(message), closeScript)
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Where a server's sign-in happens, found as the MCP authorization spec
// describes: the protected resource's metadata names its authorization
// server, whose metadata names the endpoints.
type oauthConfig struct {
	resource              string
	scopes                []string
	authorizationEndpoint string
	tokenEndpoint         string
	registrationEndpoint  string
}

var resourceMetadataParam = regexp.MustCompile(`resource_metadata="([^"]+)"`)

func discoverOAuth(ctx context.Context, serverURL string) (oauthConfig, error) {
	c := oauthConfig{resource: serverURL}
	u, err := url.Parse(serverURL)
	if err != nil {
		return c, err
	}

	// The server names its metadata when it turns a request away; otherwise
	// it is at the well-known path, for the server's path first.
	var candidates []string
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
	if err != nil {
		return c, err
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if resp, err := oauthClient.Do(req); err == nil {
		resp.Body.Close()
		if m := resourceMetadataParam.FindStringSubmatch(resp.Header.Get("WWW-Authenticate")); m != nil {
			candidates = append(candidates, m[1])
		}
	}
	origin := u.Scheme + "://" + u.Host
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+p)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")

	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	issuer := ""
	for _, m := range candidates {
		if err := getJSON(ctx, m, &resource); err == nil && len(resource.AuthorizationServers) > 0 {
			issuer = resource.AuthorizationServers[0]
			c.scopes = resource.ScopesSupported
			if resource.Resource != "" {
				c.resource = resource.Resource
			}
			break
		}
	}
	// Servers from before protected resource metadata are their own authorization server.
	if issuer == "" {
		issuer = origin
	}

	iu, err := url.Parse(issuer)
	if err != nil {
		return c, fmt.Errorf("authorization server %q: %w", issuer, err)
	}
	ip := strings.TrimRight(iu.Path, "/")
	iorigin := iu.Scheme + "://" + iu.Host
	var meta struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		RegistrationEndpoint  string `json:"registration_endpoint"`
	}
	found := false
	for _, m := range []string{
		iorigin + "/.well-known/oauth-authorization-server" + ip,
		iorigin + "/.well-known/openid-configuration" + ip,
		strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration",
	} {
		if err := getJSON(ctx, m, &meta); err == nil && meta.AuthorizationEndpoint != "" && meta.TokenEndpoint != "" {
			found = true
			break
		}
	}
	if !found {
		return c, fmt.Errorf("no OAuth metadata found for the authorization server %s", issuer)
	}
	if meta.Issuer != "" && strings.TrimRight(meta.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return c, fmt.Errorf("the authorization server's metadata names the issuer %s instead of %s", meta.Issuer, issuer)
	}
	c.authorizationEndpoint = meta.AuthorizationEndpoint
	c.tokenEndpoint = meta.TokenEndpoint
	c.registrationEndpoint = meta.RegistrationEndpoint
	return c, nil
}

func getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Registers the webapp as a client of the server's authorization server, if
// it isn't for this redirect yet, and returns where to send the browser to
// connect the server for the user.
func (b *mcpBroker) startSignIn(ctx context.Context, id, user int64, redirect string) (string, error) {
	var serverURL, clientID, registeredRedirect, auth string
	err := b.db.QueryRowContext(ctx, "SELECT url, client_id, redirect_uri, auth FROM mcp_servers WHERE id = ?", id).
		Scan(&serverURL, &clientID, &registeredRedirect, &auth)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("the server doesn't exist")
	}
	if err != nil {
		return "", err
	}
	if auth == authToken {
		return "", errors.New("nobody signs in to this server: it takes the token an admin sets in Settings")
	}
	c, err := discoverOAuth(ctx, serverURL)
	if err != nil {
		return "", err
	}
	if clientID == "" || registeredRedirect != redirect {
		if c.registrationEndpoint == "" {
			return "", errors.New("the authorization server doesn't let clients register")
		}
		if clientID, err = registerClient(ctx, c.registrationEndpoint, redirect); err != nil {
			return "", err
		}
	}
	if _, err := b.db.ExecContext(ctx, "UPDATE mcp_servers SET client_id = ?, redirect_uri = ?, token_endpoint = ? WHERE id = ?",
		clientID, redirect, c.tokenEndpoint, id); err != nil {
		return "", err
	}

	state := randomToken()
	verifier := randomToken()
	challenge := sha256.Sum256([]byte(verifier))
	b.mu.Lock()
	now := time.Now()
	for s, p := range b.pending {
		if now.After(p.expires) {
			delete(b.pending, s)
		}
	}
	b.pending[state] = mcpSignIn{
		server: id, user: user, verifier: verifier, redirect: redirect, clientID: clientID,
		tokenEndpoint: c.tokenEndpoint, resource: c.resource, expires: now.Add(10 * time.Minute),
	}
	b.mu.Unlock()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"resource":              {c.resource},
	}
	if len(c.scopes) > 0 {
		q.Set("scope", strings.Join(c.scopes, " "))
	}
	sep := "?"
	if strings.Contains(c.authorizationEndpoint, "?") {
		sep = "&"
	}
	return c.authorizationEndpoint + sep + q.Encode(), nil
}

func registerClient(ctx context.Context, endpoint, redirect string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "Flow Builder",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("register client: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("register client: status %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" {
		return "", errors.New("register client: no client id in the response")
	}
	return out.ClientID, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func requestToken(ctx context.Context, endpoint string, form url.Values) (tokenResponse, int, error) {
	var t tokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return t, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient.Do(req)
	if err != nil {
		return t, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(raw, &t); err != nil {
		return t, resp.StatusCode, fmt.Errorf("token endpoint: status %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}
	if resp.StatusCode != http.StatusOK || t.AccessToken == "" {
		msg := t.Error
		if t.Description != "" {
			msg += ": " + t.Description
		}
		return t, resp.StatusCode, fmt.Errorf("token endpoint: status %d: %s", resp.StatusCode, msg)
	}
	return t, resp.StatusCode, nil
}

func expiresAt(t tokenResponse) *time.Time {
	if t.ExpiresIn <= 0 {
		return nil
	}
	at := time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).UTC()
	return &at
}

// Exchanges the code the sign-in page came back with for tokens, and returns
// the server's name.
func (b *mcpBroker) finishSignIn(ctx context.Context, q url.Values) (string, error) {
	b.mu.Lock()
	p, ok := b.pending[q.Get("state")]
	delete(b.pending, q.Get("state"))
	b.mu.Unlock()
	if !ok || time.Now().After(p.expires) {
		return "", errors.New("this sign-in expired or was already used; start it again from the settings")
	}
	if e := q.Get("error"); e != "" {
		if d := q.Get("error_description"); d != "" {
			e += ": " + d
		}
		return "", errors.New(e)
	}
	t, _, err := requestToken(ctx, p.tokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {q.Get("code")},
		"redirect_uri":  {p.redirect},
		"client_id":     {p.clientID},
		"code_verifier": {p.verifier},
		"resource":      {p.resource},
	})
	if err != nil {
		return "", err
	}
	access, err := secrets.seal(mcpTokenPlace("access_token", p.server, p.user), t.AccessToken)
	if err != nil {
		return "", err
	}
	var refresh *string
	if t.RefreshToken != "" {
		r, err := secrets.seal(mcpTokenPlace("refresh_token", p.server, p.user), t.RefreshToken)
		if err != nil {
			return "", err
		}
		refresh = &r
	}
	if _, err := b.db.ExecContext(ctx,
		`INSERT INTO mcp_tokens (server_id, user_id, access_token, refresh_token, expires_at) VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE access_token = VALUES(access_token), refresh_token = VALUES(refresh_token), expires_at = VALUES(expires_at)`,
		p.server, p.user, access, refresh, expiresAt(t)); err != nil {
		return "", err
	}
	var name string
	err = b.db.QueryRowContext(ctx, "SELECT name FROM mcp_servers WHERE id = ?", p.server).Scan(&name)
	return name, err
}

// The access token of the user's connection to a server, refreshed first
// when it has expired or, with rejected set, when the server turned that
// token away.
func (b *mcpBroker) accessToken(ctx context.Context, id, user int64, rejected string) (string, error) {
	var auth string
	err := b.db.QueryRowContext(ctx, "SELECT auth FROM mcp_servers WHERE id = ?", id).Scan(&auth)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("the server was removed")
	}
	if err != nil {
		return "", err
	}
	if auth == authToken {
		return adminToken(ctx, b.db, id, rejected)
	}
	c := mcpConnection{id, user}
	b.mu.Lock()
	lock, ok := b.refreshing[c]
	if !ok {
		lock = &sync.Mutex{}
		b.refreshing[c] = lock
	}
	b.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	var access, refresh sql.NullString
	var expires sql.NullTime
	var serverURL, clientID, tokenEndpoint string
	err = b.db.QueryRowContext(ctx, "SELECT url, client_id, token_endpoint FROM mcp_servers WHERE id = ?", id).
		Scan(&serverURL, &clientID, &tokenEndpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("the server was removed")
	}
	if err != nil {
		return "", err
	}
	err = b.db.QueryRowContext(ctx, "SELECT access_token, refresh_token, expires_at FROM mcp_tokens WHERE server_id = ? AND user_id = ?", id, user).
		Scan(&access, &refresh, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errMCPNotConnected
	}
	if err != nil {
		return "", err
	}
	for column, token := range map[string]*sql.NullString{"access_token": &access, "refresh_token": &refresh} {
		if token.Valid {
			if token.String, err = secrets.open(mcpTokenPlace(column, id, user), token.String); err != nil {
				return "", err
			}
		}
	}
	fresh := !expires.Valid || time.Now().Add(30*time.Second).Before(expires.Time)
	// Another request may have refreshed it while this one waited.
	if access.Valid && fresh && (rejected == "" || access.String != rejected) {
		return access.String, nil
	}
	if !refresh.Valid || tokenEndpoint == "" {
		return "", errMCPNotConnected
	}
	t, status, err := requestToken(ctx, tokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh.String},
		"client_id":     {clientID},
		"resource":      {serverURL},
	})
	if err != nil {
		// A refresh token the server no longer takes means signing in again.
		if status == http.StatusBadRequest || status == http.StatusUnauthorized {
			log.Printf("mcp: refresh for server %d of user %d: %v", id, user, err)
			if _, err := b.db.ExecContext(context.Background(), "DELETE FROM mcp_tokens WHERE server_id = ? AND user_id = ?", id, user); err != nil {
				log.Printf("mcp: sign out server %d of user %d: %v", id, user, err)
			}
			return "", errMCPNotConnected
		}
		return "", err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = refresh.String
	}
	sealedAccess, err := secrets.seal(mcpTokenPlace("access_token", id, user), t.AccessToken)
	if err != nil {
		return "", err
	}
	sealedRefresh, err := secrets.seal(mcpTokenPlace("refresh_token", id, user), t.RefreshToken)
	if err != nil {
		return "", err
	}
	if _, err := b.db.ExecContext(ctx, "UPDATE mcp_tokens SET access_token = ?, refresh_token = ?, expires_at = ? WHERE server_id = ? AND user_id = ?",
		sealedAccess, sealedRefresh, expiresAt(t), id, user); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// The token an admin set for a server taking one. It can't be refreshed, so
// once the server turns it away, the admin sets a new one.
func adminToken(ctx context.Context, db *sql.DB, id int64, rejected string) (string, error) {
	var access sql.NullString
	err := db.QueryRowContext(ctx, "SELECT access_token FROM mcp_tokens WHERE server_id = ? AND user_id = 0", id).Scan(&access)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !access.Valid) {
		return "", errMCPNotConnected
	}
	if err != nil {
		return "", err
	}
	token, err := secrets.open(mcpTokenPlace("access_token", id, 0), access.String)
	if err != nil {
		return "", err
	}
	if rejected != "" && token == rejected {
		return "", errMCPTokenRejected
	}
	return token, nil
}

var errMCPTokenRejected = errors.New("the server turned its token away; an admin sets a new one in Settings, under MCP servers")

// The user a run's agents work for, with their MCP connections: whoever
// started it, or the run it is part of, else whoever made the schedule that
// did, else the owner of the flow it runs; nil when none of them is anyone.
func runUser(ctx context.Context, db *sql.DB, run int64) (*int64, error) {
	for {
		var parent, who, owner sql.NullInt64
		err := db.QueryRowContext(ctx,
			`SELECT r.parent_run_id, COALESCE(r.started_by, s.created_by), f.owner_user_id FROM runs r
			JOIN flows f ON f.id = r.flow_id LEFT JOIN schedules s ON s.id = r.schedule_id WHERE r.id = ?`, run).Scan(&parent, &who, &owner)
		switch {
		case err != nil:
			return nil, err
		case who.Valid:
			return &who.Int64, nil
		case parent.Valid:
			run = parent.Int64
		case owner.Valid:
			return &owner.Int64, nil
		default:
			return nil, nil
		}
	}
}

// An MCP server as the worker registers it with pi for one agent step.
type mcpRunServer struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// Grants an agent step the servers its process selects, with the
// connections of the user it works for: nil is nobody, with no connections
// when people sign in, and everyone's without. A server taking a token has
// the one an admin set whoever the run is for. Checks each is connected and
// returns them with the token that reaches them through the proxy, which
// revoke ends, and which reaches only the tools the process keeps to. A
// server that is missing or signed out fails the step with a message
// saying what to do; that is msg, err is for anything else.
func (b *mcpBroker) grant(ctx context.Context, who *int64, uses processSettings) (servers []mcpRunServer, token, msg string, err error) {
	if len(uses.MCPServers) == 0 {
		return nil, "", "", nil
	}
	var user int64
	if who != nil {
		user = *who
	}
	allowed := map[int64]toolFilter{}
	for _, id := range uses.MCPServers {
		if _, ok := allowed[id]; ok {
			continue
		}
		var s mcpServer
		err := scanMCPServer(b.db.QueryRowContext(ctx, "SELECT "+mcpServerColumns+" FROM mcp_servers WHERE id = ?", user, id), &s)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", "An MCP server the process uses was removed; pick its MCP servers again.", nil
		}
		if err != nil {
			return nil, "", "", err
		}
		if s.Auth == authOAuth && who == nil && b.signIn {
			return nil, "", "This run is for nobody, so it has no MCP connections: start it yourself, or have the schedule made again by someone who connected its MCP servers.", nil
		}
		if _, err := b.accessToken(ctx, id, user, ""); errors.Is(err, errMCPNotConnected) {
			switch {
			case s.Auth == authToken:
				return nil, "", fmt.Sprintf("The MCP server %s has no token. An admin sets it in Settings, under MCP servers, then run again.", s.Name), nil
			case b.signIn:
				return nil, "", fmt.Sprintf("The MCP server %s isn't connected for %s. Connect it in Settings, under MCP servers, then run again.", s.Name, userLabel(ctx, b.db, user)), nil
			}
			return nil, "", fmt.Sprintf("The MCP server %s isn't connected. Connect it in Settings, under MCP servers, then run again.", s.Name), nil
		} else if err != nil {
			return nil, "", fmt.Sprintf("Couldn't get a token for the MCP server %s: %v", s.Name, err), nil
		}
		allowed[id] = filterFor(s, uses.MCPTools[id])
		servers = append(servers, mcpRunServer{
			Name:        s.Name,
			URL:         fmt.Sprintf("%s/mcp/%d", b.proxyURL, id),
			Description: s.Description,
		})
	}
	token = randomToken()
	b.mu.Lock()
	b.grants[token] = mcpGrant{user: user, servers: allowed}
	b.mu.Unlock()
	return servers, token, "", nil
}

func (b *mcpBroker) revoke(token string) {
	if token == "" {
		return
	}
	b.mu.Lock()
	delete(b.grants, token)
	b.mu.Unlock()
}

// Request headers streamable HTTP needs passed on; the rest, the step's
// token above all, stay here.
var mcpRequestHeaders = []string{"Accept", "Content-Type", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-ID"}

// JSON-RPC requests are small; this only bounds a misbehaving client.
const maxMCPRequestBytes = 10 << 20

// Passes an agent's request on to the MCP server with the server's token,
// streaming the response back, or answers it from the connector the server
// is. A request the server turns away gets one retry with a refreshed
// token; nothing else is retried, since a tool call may already have taken
// effect. A call of a tool the step doesn't have is refused without
// reaching the server, and the tools it lists leave those out.
func (b *mcpBroker) proxy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.Lock()
	g, granted := b.grants[token]
	filter, allowed := g.servers[id]
	b.mu.Unlock()
	if !ok || !granted || !allowed {
		http.Error(w, "this MCP server isn't available to this step", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPRequestBytes))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var serverURL, name, connector string
	if err := b.db.QueryRowContext(r.Context(), "SELECT url, name, connector FROM mcp_servers WHERE id = ?", id).Scan(&serverURL, &name, &connector); err != nil {
		http.Error(w, "the MCP server was removed", http.StatusNotFound)
		return
	}
	if c := findConnector(connector); c != nil {
		access, err := b.accessToken(r.Context(), id, g.user, "")
		if err != nil {
			http.Error(w, "token for "+name+": "+err.Error(), http.StatusBadGateway)
			return
		}
		serveConnector(w, r, c, c.client(serverURL, access), filter, body)
		return
	}
	messages, _ := parseRPC(body)
	if refusal := filter.refuse(messages); refusal != nil {
		writeJSON(w, http.StatusOK, refusal)
		return
	}
	if r.URL.RawQuery != "" {
		if strings.Contains(serverURL, "?") {
			serverURL += "&" + r.URL.RawQuery
		} else {
			serverURL += "?" + r.URL.RawQuery
		}
	}

	var resp *http.Response
	rejected := ""
	for attempt := 0; attempt < 2; attempt++ {
		access, err := b.accessToken(r.Context(), id, g.user, rejected)
		if errors.Is(err, errMCPNotConnected) {
			http.Error(w, fmt.Sprintf("the MCP server %s isn't connected; connect it in Settings, under MCP servers", name), http.StatusBadGateway)
			return
		}
		if err != nil {
			http.Error(w, "token for "+name+": "+err.Error(), http.StatusBadGateway)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, serverURL, bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		for _, h := range mcpRequestHeaders {
			if v := r.Header.Values(h); len(v) > 0 {
				req.Header[h] = v
			}
		}
		req.Header.Set("Authorization", "Bearer "+access)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, "reach "+name+": "+err.Error(), http.StatusBadGateway)
			return
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt == 1 {
			break
		}
		resp.Body.Close()
		rejected = access
	}
	defer resp.Body.Close()

	// pi would take a challenge for its own sign-in, which can't happen here.
	for k, v := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Www-Authenticate", "Set-Cookie", "Connection", "Transfer-Encoding", "Content-Length":
			continue
		}
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	if filter != nil && listsTools(messages) {
		filter.filterResponse(w, resp)
		return
	}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
