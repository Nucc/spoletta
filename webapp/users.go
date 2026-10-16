package main

// People sign in with the company's identity provider over OpenID Connect,
// and then work in the webapp as themselves: a run records who started it,
// and a human step who decided it, so a decision on the record says who made
// it. The webapp is the provider's client, set up by OIDC_ISSUER,
// OIDC_CLIENT_ID and OIDC_CLIENT_SECRET; the provider sends people back to
// /api/auth/callback on PUBLIC_URL, or on the address the browser reached the
// webapp by when that isn't set. Without OIDC_ISSUER nobody signs in and
// everyone can do everything, as before there were users, which suits a
// webapp only its developer reaches: it then listens only on this machine
// unless HOST says otherwise; see listenAddr in main.go.
//
// Not everyone the provider knows may sign in, as with a provider anyone can
// have an account at: only people whose email is in ADMIN_EMAILS, whose
// email's domain is in OIDC_ALLOWED_DOMAINS, or who an admin added. Those in
// ADMIN_EMAILS are admins.
//
// Signing in is the authorization code flow with PKCE. The ID token comes
// straight from the provider's token endpoint, so, as OpenID Connect allows,
// the connection vouches for it instead of its signature; its issuer,
// audience, expiry and nonce are still checked. A sign-in under way is kept
// in a cookie and a session in the database, so with several webapps any of
// them can finish a sign-in and serve its session.
//
// Every request to /api/ needs a session but those signing in and out. The
// MCP proxy at /mcp/ doesn't: the worker reaches it with an agent step's own
// token; see mcp.go.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Someone who signed in, or whom an admin added before they first did.
type user struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Admin bool   `json:"admin"`
	// Makes teams and says who is in them; see teams.go.
	TeamManager bool `json:"teamManager"`
}

// How long a session lasts, and a sign-in under way.
const (
	sessionLifetime = 30 * 24 * time.Hour
	signInLifetime  = 10 * time.Minute
)

const (
	sessionCookie = "spoletta_session"
	signInCookie  = "spoletta_sign_in"
)

// The label of the user with the id in column col, as a column of a query;
// empty without one.
func userLabelColumn(col string) string {
	return "COALESCE((SELECT COALESCE(NULLIF(u.name, ''), u.email) FROM users u WHERE u.id = " + col + "), '')"
}

func migrateUsers(db *sql.DB) error {
	stmts := []string{
		// Someone an admin added has no subject until they first sign in.
		`CREATE TABLE IF NOT EXISTS users (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			email VARCHAR(320) NOT NULL,
			issuer VARCHAR(255) NOT NULL DEFAULT '',
			subject VARCHAR(255) NULL,
			name VARCHAR(255) NOT NULL DEFAULT '',
			admin BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			signed_in_at TIMESTAMP(3) NULL,
			UNIQUE KEY users_email (email),
			UNIQUE KEY users_subject (issuer, subject)
		)`,
		// A session is known by its token's hash, so the table doesn't hold
		// what signs anyone in.
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash CHAR(64) PRIMARY KEY,
			user_id BIGINT NOT NULL,
			expires_at TIMESTAMP(3) NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			KEY sessions_expiry (expires_at),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	for _, c := range []struct{ table, name, def string }{
		{"runs", "started_by", "BIGINT NULL AFTER schedule_id"},
		{"run_steps", "decided_by", "BIGINT NULL AFTER instructions"},
		// Someone an admin removed; see user_roles.go.
		{"users", "removed_at", "TIMESTAMP(3) NULL"},
	} {
		if err := addColumn(db, c.table, c.name, c.def); err != nil {
			return err
		}
	}
	return nil
}

// Signing in, as the environment sets it up; nil when nobody signs in.
type auth struct {
	db                             *sql.DB
	issuer, clientID, clientSecret string
	publicURL                      string
	domains                        []string
	admins                         map[string]bool
}

func newAuth(db *sql.DB) *auth {
	issuer := strings.TrimRight(env("OIDC_ISSUER", ""), "/")
	if issuer == "" {
		log.Printf("WARNING: OIDC_ISSUER isn't set, so nobody signs in: anyone who reaches the webapp can do anything in it")
		return nil
	}
	// The ID token's signature isn't checked, as the connection to the
	// provider vouches for it; one in the clear vouches for nothing.
	if u, err := url.Parse(issuer); err != nil || (u.Scheme != "https" && !slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())) {
		log.Fatalf("OIDC_ISSUER must be an https address, or one on this machine: %q", issuer)
	}
	a := &auth{
		db:           db,
		issuer:       issuer,
		clientID:     env("OIDC_CLIENT_ID", ""),
		clientSecret: env("OIDC_CLIENT_SECRET", ""),
		publicURL:    strings.TrimRight(env("PUBLIC_URL", ""), "/"),
		admins:       map[string]bool{},
	}
	if a.clientID == "" {
		log.Fatal("OIDC_CLIENT_ID must be set with OIDC_ISSUER")
	}
	for _, d := range strings.Split(env("OIDC_ALLOWED_DOMAINS", ""), ",") {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			a.domains = append(a.domains, strings.TrimPrefix(d, "@"))
		}
	}
	for _, e := range strings.Split(env("ADMIN_EMAILS", ""), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			a.admins[e] = true
		}
	}
	if len(a.admins) == 0 && len(a.domains) == 0 {
		log.Printf("WARNING: neither ADMIN_EMAILS nor OIDC_ALLOWED_DOMAINS is set, so nobody can sign in")
	}
	return a
}

type userKey struct{}

// The user a request is from; nil when nobody signs in.
func requestUser(r *http.Request) *user {
	u, _ := r.Context().Value(userKey{}).(*user)
	return u
}

// The id of the user a request is from, for a column that records it.
func requestUserID(r *http.Request) *int64 {
	if u := requestUser(r); u != nil {
		return &u.ID
	}
	return nil
}

// Turns away a request to /api/ without a session, but those signing in and
// out, and passes the others on with their user.
func (a *auth) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a == nil || !strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/api/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		u, err := a.sessionUser(r)
		if err != nil {
			serverError(w, err)
			return
		}
		if u == nil {
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// The user of the request's session, or nil without one still going.
func (a *auth) sessionUser(r *http.Request) (*user, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	var u user
	err = a.db.QueryRowContext(r.Context(),
		`SELECT u.id, u.email, u.name, u.admin, u.team_manager FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > CURRENT_TIMESTAMP(3) AND u.removed_at IS NULL`, hashToken(c.Value)).
		Scan(&u.ID, &u.Email, &u.Name, &u.Admin, &u.TeamManager)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// Where the provider sends people back to.
func (a *auth) redirectURL(r *http.Request) string {
	base := a.publicURL
	if base == "" {
		base = requestOrigin(r)
	}
	return base + "/api/auth/callback"
}

// Cookies are Secure when the webapp is reached over https.
func (a *auth) secure(r *http.Request) bool {
	return strings.HasPrefix(a.redirectURL(r), "https://")
}

// Where the provider's endpoints are, from its discovery document.
type oidcProvider struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

func (a *auth) discover(ctx context.Context) (oidcProvider, error) {
	var p oidcProvider
	if err := getJSON(ctx, a.issuer+"/.well-known/openid-configuration", &p); err != nil {
		return p, fmt.Errorf("discover the identity provider: %w", err)
	}
	if strings.TrimRight(p.Issuer, "/") != a.issuer {
		return p, fmt.Errorf("the identity provider says its issuer is %q, not %q", p.Issuer, a.issuer)
	}
	if p.AuthorizationEndpoint == "" || p.TokenEndpoint == "" {
		return p, errors.New("the identity provider names no authorization or token endpoint")
	}
	return p, nil
}

// A sign-in under way, as its cookie keeps it.
type signIn struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
	// The page to come back to.
	Next string `json:"next"`
}

// Only a path of this webapp, so signing in can't send anyone elsewhere.
// Browsers drop tabs and newlines from a URL and read a backslash as a
// slash, so "/\t/evil.example" would be "//evil.example" to them: a path
// with either anywhere isn't taken.
func localPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsFunc(p, func(r rune) bool {
		return r < ' ' || r == 0x7f || r == '\\'
	}) {
		return "/"
	}
	return p
}

// What the ID token says of who signed in.
type idClaims struct {
	Issuer        string          `json:"iss"`
	Audience      json.RawMessage `json:"aud"`
	Expiry        float64         `json:"exp"`
	Nonce         string          `json:"nonce"`
	Subject       string          `json:"sub"`
	Email         string          `json:"email"`
	EmailVerified *bool           `json:"email_verified"`
	Name          string          `json:"name"`
}

// The claims of an ID token from the token endpoint, checked as being for
// this sign-in.
func (a *auth) idClaims(token, nonce string) (idClaims, error) {
	var c idClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("the ID token isn't a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return c, fmt.Errorf("the ID token: %w", err)
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, fmt.Errorf("the ID token: %w", err)
	}
	var audiences []string
	var one string
	if json.Unmarshal(c.Audience, &one) == nil {
		audiences = []string{one}
	} else if json.Unmarshal(c.Audience, &audiences) != nil {
		return c, errors.New("the ID token has no audience")
	}
	switch {
	case strings.TrimRight(c.Issuer, "/") != a.issuer:
		return c, fmt.Errorf("the ID token is from %q", c.Issuer)
	case !slices.Contains(audiences, a.clientID):
		return c, errors.New("the ID token isn't for this webapp")
	case time.Now().After(time.Unix(int64(c.Expiry), 0)):
		return c, errors.New("the ID token expired")
	case subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
		return c, errors.New("the ID token is for another sign-in")
	case c.Subject == "":
		return c, errors.New("the ID token names nobody")
	}
	return c, nil
}

// Fills in the email and name the ID token left out from the userinfo endpoint.
func (a *auth) fillFromUserinfo(ctx context.Context, p oidcProvider, accessToken string, c *idClaims) error {
	if (c.Email != "" && c.Name != "") || p.UserinfoEndpoint == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.UserinfoEndpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := oauthClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("userinfo: status %d", resp.StatusCode)
	}
	var info idClaims
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("userinfo: %w", err)
	}
	// Only about the same person.
	if info.Subject != c.Subject {
		return errors.New("userinfo is about someone else")
	}
	if c.Email == "" {
		c.Email, c.EmailVerified = info.Email, info.EmailVerified
	}
	if c.Name == "" {
		c.Name = info.Name
	}
	return nil
}

// Whether someone with this email may sign in without an admin adding them.
func (a *auth) admitted(email string) bool {
	if a.admins[email] {
		return true
	}
	_, domain, _ := strings.Cut(email, "@")
	return slices.Contains(a.domains, domain)
}

var (
	errNotAdmitted = errors.New("not admitted")
	errEmailTaken  = errors.New("the email is another account's")
)

// The user who signed in, made or updated from what the provider says of
// them; errNotAdmitted if they may not sign in.
func (a *auth) signedIn(ctx context.Context, c idClaims) (int64, error) {
	email := strings.ToLower(strings.TrimSpace(c.Email))
	// A provider that says the email isn't verified could be told any.
	if email == "" || (c.EmailVerified != nil && !*c.EmailVerified) {
		return 0, errNotAdmitted
	}
	admin := a.admins[email]
	var id int64
	err := inTx(ctx, a.db, func(tx *sql.Tx) error {
		// Known by the provider's subject, which is who they are to it.
		// Else by their email: when an admin added them and they sign in the
		// first time, or when the provider changed. Someone else the same
		// provider gives a known email, such as whoever gets the mailbox of
		// someone who left, isn't that user.
		err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE issuer = ? AND subject = ? FOR UPDATE", a.issuer, c.Subject).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			var issuer string
			var subject sql.NullString
			err = tx.QueryRowContext(ctx, "SELECT id, issuer, subject FROM users WHERE email = ? FOR UPDATE", email).Scan(&id, &issuer, &subject)
			if err == nil && subject.Valid && issuer == a.issuer {
				return errEmailTaken
			}
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if !a.admitted(email) {
				return errNotAdmitted
			}
			res, err := tx.ExecContext(ctx, "INSERT INTO users (email, issuer, subject, name, admin, signed_in_at) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP(3))",
				email, a.issuer, c.Subject, c.Name, admin)
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		case err != nil:
			return err
		}
		// Someone an admin removed stays out, even if ADMIN_EMAILS or their
		// domain would let them in, until an admin adds them back.
		var removed bool
		if err := tx.QueryRowContext(ctx, "SELECT removed_at IS NOT NULL FROM users WHERE id = ?", id).Scan(&removed); err != nil {
			return err
		}
		if removed {
			return errNotAdmitted
		}
		// ADMIN_EMAILS makes admins, but taking someone off it doesn't undo
		// that: an admin can.
		_, err = tx.ExecContext(ctx,
			"UPDATE users SET email = ?, issuer = ?, subject = ?, name = COALESCE(NULLIF(?, ''), name), admin = admin OR ?, signed_in_at = CURRENT_TIMESTAMP(3) WHERE id = ?",
			email, a.issuer, c.Subject, c.Name, admin, id)
		return err
	})
	return id, err
}

func registerAuthRoutes(mux *http.ServeMux, db *sql.DB, a *auth) {
	// Who is signed in, with their roles, and whether anyone signs in at all.
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		me := map[string]any{"signIn": a != nil, "user": nil}
		if u := requestUser(r); u != nil {
			p, err := getPerson(r.Context(), db, u.ID)
			if err != nil {
				serverError(w, err)
				return
			}
			me["user"] = p
		}
		writeJSON(w, http.StatusOK, me)
	})
	if a == nil {
		return
	}

	// Sends the browser to the provider to sign in, coming back to ?next.
	mux.HandleFunc("GET /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		p, err := a.discover(r.Context())
		if err != nil {
			log.Printf("auth: %v", err)
			authPage(w, http.StatusBadGateway, "Couldn't start signing in", err.Error())
			return
		}
		s := signIn{State: randomToken(), Verifier: randomToken(), Nonce: randomToken(), Next: localPath(r.URL.Query().Get("next"))}
		raw, _ := json.Marshal(s)
		http.SetCookie(w, &http.Cookie{
			Name: signInCookie, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/api/auth/",
			MaxAge: int(signInLifetime / time.Second), HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
		})
		challenge := sha256.Sum256([]byte(s.Verifier))
		q := url.Values{
			"response_type":         {"code"},
			"client_id":             {a.clientID},
			"redirect_uri":          {a.redirectURL(r)},
			"scope":                 {"openid email profile"},
			"state":                 {s.State},
			"nonce":                 {s.Nonce},
			"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
			"code_challenge_method": {"S256"},
		}
		sep := "?"
		if strings.Contains(p.AuthorizationEndpoint, "?") {
			sep = "&"
		}
		http.Redirect(w, r, p.AuthorizationEndpoint+sep+q.Encode(), http.StatusFound)
	})

	mux.HandleFunc("GET /api/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		// The sign-in is used once, whatever comes of it.
		http.SetCookie(w, &http.Cookie{Name: signInCookie, Path: "/api/auth/", MaxAge: -1, HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
		var s signIn
		c, err := r.Cookie(signInCookie)
		if err == nil {
			var raw []byte
			if raw, err = base64.RawURLEncoding.DecodeString(c.Value); err == nil {
				err = json.Unmarshal(raw, &s)
			}
		}
		q := r.URL.Query()
		if err != nil || s.State == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(s.State)) != 1 {
			authPage(w, http.StatusBadRequest, "Signing in failed", "This sign-in expired or was already used. Sign in again.")
			return
		}
		if e := q.Get("error"); e != "" {
			if d := q.Get("error_description"); d != "" {
				e += ": " + d
			}
			authPage(w, http.StatusBadRequest, "Signing in failed", e)
			return
		}
		id, err := a.finish(r, s, q.Get("code"))
		if errors.Is(err, errNotAdmitted) {
			authPage(w, http.StatusForbidden, "You can't sign in yet", "Ask an admin to add you, then sign in again.")
			return
		}
		if errors.Is(err, errEmailTaken) {
			authPage(w, http.StatusForbidden, "You can't sign in", "Your email belongs to another account here. Ask an admin.")
			return
		}
		if err != nil {
			log.Printf("auth: sign-in callback: %v", err)
			authPage(w, http.StatusBadGateway, "Signing in failed", err.Error())
			return
		}
		token := randomToken()
		if _, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE expires_at <= CURRENT_TIMESTAMP(3)"); err != nil {
			serverError(w, err)
			return
		}
		if _, err := a.db.ExecContext(r.Context(), "INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
			hashToken(token), id, time.Now().Add(sessionLifetime).UTC()); err != nil {
			serverError(w, err)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionLifetime / time.Second),
			HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, s.Next, http.StatusFound)
	})

	mux.HandleFunc("GET /api/auth/signed-out", func(w http.ResponseWriter, r *http.Request) {
		authPage(w, http.StatusOK, "Signed out", "You signed out of Spoletta.")
	})

	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			if _, err := a.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash = ?", hashToken(c.Value)); err != nil {
				serverError(w, err)
				return
			}
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
		w.WriteHeader(http.StatusNoContent)
	})
}

// Exchanges the code the provider came back with for who signed in, and
// returns their user's id.
func (a *auth) finish(r *http.Request, s signIn, code string) (int64, error) {
	ctx := r.Context()
	p, err := a.discover(ctx)
	if err != nil {
		return 0, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {a.redirectURL(r)},
		"code_verifier": {s.Verifier},
	}
	// A public client says who it is in the form.
	if a.clientSecret == "" {
		form.Set("client_id", a.clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if a.clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(a.clientID), url.QueryEscape(a.clientSecret))
	}
	resp, err := oauthClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	var t struct {
		tokenResponse
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return 0, fmt.Errorf("token endpoint: status %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || t.IDToken == "" {
		msg := t.Error
		if t.Description != "" {
			msg += ": " + t.Description
		}
		return 0, fmt.Errorf("token endpoint: status %d: %s", resp.StatusCode, msg)
	}
	claims, err := a.idClaims(t.IDToken, s.Nonce)
	if err != nil {
		return 0, err
	}
	if err := a.fillFromUserinfo(ctx, p, t.AccessToken, &claims); err != nil {
		return 0, err
	}
	return a.signedIn(ctx, claims)
}

// The page a sign-in that can't go on ends on.
func authPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html>
<meta charset="utf-8">
<title>%[1]s</title>
<style>body{font:15px system-ui,sans-serif;margin:3rem auto;max-width:32rem;padding:0 1rem;color:#222}</style>
<h1>%[1]s</h1>
<p>%[2]s</p>
<p><a href="/api/auth/login">Sign in again</a></p>
`, html.EscapeString(title), html.EscapeString(message))
}

// The name of the user, or their email without one; "someone" for a user
// that can't be found.
func userLabel(ctx context.Context, db querier, id int64) string {
	var label string
	if err := db.QueryRowContext(ctx, "SELECT "+userLabelColumn("?"), id).Scan(&label); err != nil || label == "" {
		return "someone"
	}
	return label
}
