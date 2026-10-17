package main

// People sign in over OpenID Connect, with the company's identity provider,
// with Google, or with either, and then work in the webapp as themselves: a
// run records who started it, and a human step who decided it, so a
// decision on the record says who made it. The webapp is each provider's
// client: the company's is set up by OIDC_ISSUER, OIDC_CLIENT_ID and
// OIDC_CLIENT_SECRET, and named on the sign-in page by OIDC_NAME; Google by
// GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET. Both send people back to
// /api/auth/callback on PUBLIC_URL, or on the address the browser reached the
// webapp by when that isn't set. With both, signing in first asks which.
// With neither nobody signs in and everyone can do everything, as before
// there were users, which suits a webapp only its developer reaches: it then
// listens only on this machine unless HOST says otherwise; see listenAddr in
// main.go.
//
// Not everyone a provider knows may sign in, as with a provider anyone can
// have an account at: only people whose email is in ADMIN_EMAILS, whose
// email's domain is in OIDC_ALLOWED_DOMAINS, or who an admin added. Those in
// ADMIN_EMAILS are admins. Anyone can make a Google account with any email,
// and keep it after leaving the company, so Google is taken at its word only
// for a Gmail address, or for an account of the Google Workspace at the
// email's domain.
//
// A user is known to each provider they signed in with by its subject for
// them, kept in user_identities, and found by their email the first time
// they sign in with one; so signing in with either is the same user.
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
	// Who a user is to each provider they signed in with. Users once had one
	// provider, in their issuer and subject, which are now only read here.
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS user_identities (
			issuer VARCHAR(255) NOT NULL,
			subject VARCHAR(255) NOT NULL,
			user_id BIGINT NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			PRIMARY KEY (issuer, subject),
			KEY user_identities_user (user_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`INSERT IGNORE INTO user_identities (issuer, subject, user_id)
			SELECT issuer, subject, id FROM users WHERE subject IS NOT NULL AND issuer <> ''`,
	} {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Signing in, as the environment sets it up; nil when nobody signs in.
type auth struct {
	db        *sql.DB
	providers []*provider
	publicURL string
	domains   []string
	admins    map[string]bool
}

// An identity provider people sign in with.
type provider struct {
	// Names it in a sign-in under way and in ?provider; label on the page
	// asking which to sign in with.
	key, label                     string
	issuer, clientID, clientSecret string
	// How else its ID tokens may write its issuer.
	issuerAliases []string
	// Whether it vouches for an email only at one of mailDomains, or for an
	// account of the email's domain's own, as the ID token's hd claim says.
	hostedDomains bool
	mailDomains   []string
}

// Google's issuer; GOOGLE_ISSUER stands in for it in tests.
const googleIssuer = "https://accounts.google.com"

func newAuth(db *sql.DB) *auth {
	a := &auth{
		db:        db,
		publicURL: strings.TrimRight(env("PUBLIC_URL", ""), "/"),
		admins:    map[string]bool{},
	}
	if issuer := strings.TrimRight(env("OIDC_ISSUER", ""), "/"); issuer != "" {
		p := &provider{key: "oidc", issuer: issuer, clientID: env("OIDC_CLIENT_ID", ""), clientSecret: env("OIDC_CLIENT_SECRET", "")}
		if p.clientID == "" {
			log.Fatal("OIDC_CLIENT_ID must be set with OIDC_ISSUER")
		}
		p.label = env("OIDC_NAME", "")
		if p.label == "" {
			u, _ := url.Parse(issuer)
			p.label = u.Hostname()
		}
		a.providers = append(a.providers, p)
	}
	if id := env("GOOGLE_CLIENT_ID", ""); id != "" {
		p := &provider{key: "google", label: "Google", issuer: strings.TrimRight(env("GOOGLE_ISSUER", googleIssuer), "/"),
			clientID: id, clientSecret: env("GOOGLE_CLIENT_SECRET", ""),
			hostedDomains: true, mailDomains: []string{"gmail.com", "googlemail.com"}}
		if p.clientSecret == "" {
			log.Fatal("GOOGLE_CLIENT_SECRET must be set with GOOGLE_CLIENT_ID")
		}
		// Google's ID tokens may leave the scheme out of its issuer.
		p.issuerAliases = []string{strings.TrimPrefix(p.issuer, "https://")}
		a.providers = append(a.providers, p)
	}
	if len(a.providers) == 0 {
		log.Printf("WARNING: neither OIDC_ISSUER nor GOOGLE_CLIENT_ID is set, so nobody signs in: anyone who reaches the webapp can do anything in it")
		return nil
	}
	for _, p := range a.providers {
		// The ID token's signature isn't checked, as the connection to the
		// provider vouches for it; one in the clear vouches for nothing.
		if u, err := url.Parse(p.issuer); err != nil || (u.Scheme != "https" && !slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())) {
			log.Fatalf("the issuer of %s must be an https address, or one on this machine: %q", p.label, p.issuer)
		}
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

// The provider the key names, or the only one without a key; nil if none.
func (a *auth) provider(key string) *provider {
	if key == "" && len(a.providers) == 1 {
		return a.providers[0]
	}
	for _, p := range a.providers {
		if p.key == key {
			return p
		}
	}
	return nil
}

func (pr *provider) discover(ctx context.Context) (oidcProvider, error) {
	var p oidcProvider
	if err := getJSON(ctx, pr.issuer+"/.well-known/openid-configuration", &p); err != nil {
		return p, fmt.Errorf("discover the identity provider: %w", err)
	}
	if strings.TrimRight(p.Issuer, "/") != pr.issuer {
		return p, fmt.Errorf("the identity provider says its issuer is %q, not %q", p.Issuer, pr.issuer)
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
	// The key of the provider signing in with.
	Provider string `json:"provider"`
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
	// The Google Workspace domain whose account it is; see provider.
	HostedDomain string `json:"hd"`
}

// The claims of an ID token from the token endpoint, checked as being for
// this sign-in.
func (p *provider) idClaims(token, nonce string) (idClaims, error) {
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
	case strings.TrimRight(c.Issuer, "/") != p.issuer && !slices.Contains(p.issuerAliases, c.Issuer):
		return c, fmt.Errorf("the ID token is from %q", c.Issuer)
	case !slices.Contains(audiences, p.clientID):
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
func fillFromUserinfo(ctx context.Context, p oidcProvider, accessToken string, c *idClaims) error {
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

// Whether the provider's word that an account has the email is taken; hd
// is the domain it says the account is of.
func (p *provider) vouches(email, hd string) bool {
	if !p.hostedDomains {
		return true
	}
	_, domain, _ := strings.Cut(email, "@")
	return slices.Contains(p.mailDomains, domain) || (domain != "" && strings.EqualFold(hd, domain))
}

var (
	errNotAdmitted = errors.New("not admitted")
	errNotVouched  = errors.New("the provider doesn't vouch for the email")
	errEmailTaken  = errors.New("the email is another account's")
)

// The user who signed in with the provider, made or updated from what it
// says of them; errNotAdmitted if they may not sign in.
func (a *auth) signedIn(ctx context.Context, p *provider, c idClaims) (int64, error) {
	email := strings.ToLower(strings.TrimSpace(c.Email))
	// A provider that says the email isn't verified could be told any.
	if email == "" || (c.EmailVerified != nil && !*c.EmailVerified) {
		return 0, errNotAdmitted
	}
	if !p.vouches(email, c.HostedDomain) {
		return 0, errNotVouched
	}
	admin := a.admins[email]
	var id int64
	err := inTx(ctx, a.db, func(tx *sql.Tx) error {
		// Known by the provider's subject, which is who they are to it.
		// Else by their email: when an admin added them and they sign in the
		// first time, or the first time with this provider. Someone else the
		// same provider gives a known email, such as whoever gets the
		// mailbox of someone who left, isn't that user.
		err := tx.QueryRowContext(ctx, "SELECT user_id FROM user_identities WHERE issuer = ? AND subject = ? FOR UPDATE", p.issuer, c.Subject).Scan(&id)
		known := err == nil
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email = ? FOR UPDATE", email).Scan(&id)
			if err == nil {
				var others int
				if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_identities WHERE user_id = ? AND issuer = ?", id, p.issuer).Scan(&others); err != nil {
					return err
				}
				if others > 0 {
					return errEmailTaken
				}
			}
		}
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if !a.admitted(email) {
				return errNotAdmitted
			}
			res, err := tx.ExecContext(ctx, "INSERT INTO users (email, name, admin, signed_in_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP(3))",
				email, c.Name, admin)
			if err != nil {
				return err
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO user_identities (issuer, subject, user_id) VALUES (?, ?, ?)", p.issuer, c.Subject, id)
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
		if !known {
			if _, err := tx.ExecContext(ctx, "INSERT INTO user_identities (issuer, subject, user_id) VALUES (?, ?, ?)", p.issuer, c.Subject, id); err != nil {
				return err
			}
		}
		// ADMIN_EMAILS makes admins, but taking someone off it doesn't undo
		// that: an admin can.
		_, err = tx.ExecContext(ctx,
			"UPDATE users SET email = ?, name = COALESCE(NULLIF(?, ''), name), admin = admin OR ?, signed_in_at = CURRENT_TIMESTAMP(3) WHERE id = ?",
			email, c.Name, admin, id)
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

	// Sends the browser to ?provider to sign in, coming back to ?next; with
	// several and none named, asks which.
	mux.HandleFunc("GET /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		next := localPath(r.URL.Query().Get("next"))
		key := r.URL.Query().Get("provider")
		if key == "" && len(a.providers) > 1 {
			choosePage(w, a.providers, next)
			return
		}
		pr := a.provider(key)
		if pr == nil {
			authPage(w, http.StatusBadRequest, "Signing in failed", "Nobody signs in with "+key+" here.")
			return
		}
		p, err := pr.discover(r.Context())
		if err != nil {
			log.Printf("auth: %v", err)
			authPage(w, http.StatusBadGateway, "Couldn't start signing in", err.Error())
			return
		}
		s := signIn{State: randomToken(), Verifier: randomToken(), Nonce: randomToken(), Provider: pr.key, Next: next}
		raw, _ := json.Marshal(s)
		http.SetCookie(w, &http.Cookie{
			Name: signInCookie, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/api/auth/",
			MaxAge: int(signInLifetime / time.Second), HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode,
		})
		challenge := sha256.Sum256([]byte(s.Verifier))
		q := url.Values{
			"response_type":         {"code"},
			"client_id":             {pr.clientID},
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
		pr := a.provider(s.Provider)
		if pr == nil {
			authPage(w, http.StatusBadRequest, "Signing in failed", "This sign-in was with a provider nobody signs in with here any more. Sign in again.")
			return
		}
		id, err := a.finish(r, pr, s, q.Get("code"))
		if errors.Is(err, errNotAdmitted) {
			authPage(w, http.StatusForbidden, "You can't sign in yet", "Ask an admin to add you, then sign in again.")
			return
		}
		if errors.Is(err, errNotVouched) {
			authPage(w, http.StatusForbidden, "You can't sign in with this account",
				pr.label+" vouches here only for its own addresses and for its Workspace accounts at their email's domain, and this account is neither. Sign in another way, or with your Workspace account.")
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
func (a *auth) finish(r *http.Request, pr *provider, s signIn, code string) (int64, error) {
	ctx := r.Context()
	p, err := pr.discover(ctx)
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
	if pr.clientSecret == "" {
		form.Set("client_id", pr.clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if pr.clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(pr.clientID), url.QueryEscape(pr.clientSecret))
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
	claims, err := pr.idClaims(t.IDToken, s.Nonce)
	if err != nil {
		return 0, err
	}
	if err := fillFromUserinfo(ctx, p, t.AccessToken, &claims); err != nil {
		return 0, err
	}
	return a.signedIn(ctx, pr, claims)
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

// The page asking which provider to sign in with, coming back to next.
func choosePage(w http.ResponseWriter, providers []*provider, next string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html>
<meta charset="utf-8">
<title>Sign in</title>
<style>body{font:15px system-ui,sans-serif;margin:3rem auto;max-width:32rem;padding:0 1rem;color:#222}li{margin:.5rem 0}</style>
<h1>Sign in to Spoletta</h1>
<ul>
`)
	for _, p := range providers {
		href := "/api/auth/login?" + url.Values{"provider": {p.key}, "next": {next}}.Encode()
		fmt.Fprintf(w, "<li><a href=\"%s\">Sign in with %s</a></li>\n", html.EscapeString(href), html.EscapeString(p.label))
	}
	fmt.Fprint(w, "</ul>\n")
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
