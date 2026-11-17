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
//
// The company's provider may say which groups someone is in, in the claim
// OIDC_GROUPS_CLAIM names: a string, one group, or a list of strings, in the
// ID token or, failing that, in what its userinfo endpoint says. A group a
// project is given to lets its people in (projects.go), so each sign-in
// replaces the person's groups (user_groups) with those it carries; one
// without the variable, or with Google, carries none. Someone taken out of a
// group at the provider loses what it gave them as their groups are read
// again: every replica looks, every few seconds, for sessions of people with
// groups read longer ago than an hour (or half GROUPS_MAX_AGE, if that is
// shorter) and reads them again from the provider with the session's
// refresh token, or its access token while it lasts, kept sealed with
// SECRET_KEY (secrets.go). A session the provider refuses to refresh ends.
// Where they can't be read again, as the provider gave no refresh token,
// a session of someone with groups read longer ago than GROUPS_MAX_AGE (24
// hours unless set) is over, so they sign in again. The sign-in asks for
// offline_access, which gets a refresh token from most providers, when
// OIDC_GROUPS_CLAIM is set and the provider's discovery document lists it.
// When groups were last read is kept with the person (groups_read_at), and
// each session's next reading with it, claimed by moving it on, so nothing
// of it lives only in one replica's memory.

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
	"io"
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
	// For a request's user, an owner of the project it is worked on in (see
	// projects.go), who sees and changes everything there; elsewhere, as
	// people are listed or a run's user is read, the instance's admin.
	Admin bool `json:"admin"`
	// The instance's admin, users.admin, who makes projects, enters any as
	// its owner, and manages people.
	Super bool `json:"-"`
	// The project they are in as this user: the request's, or the one
	// something is done in on their behalf (userInProject); none for a user
	// read from a row. What they own, see and may change is then kept to it
	// (ownedBy, visibleFlows).
	Project int64 `json:"-"`
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

// Signing in, as the environment sets it up; nil when nobody signs in.
type auth struct {
	db        *sql.DB
	providers []*provider
	publicURL string
	domains   []string
	admins    map[string]bool
	// How long someone's groups hold once read, past which a session of
	// theirs is over; and how often a live session reads them again.
	groupsMaxAge, groupsEvery time.Duration
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
	// The claim saying which groups someone is in; none for a provider
	// whose sign-ins carry no groups.
	groupsClaim string
}

// Google's issuer; GOOGLE_ISSUER stands in for it in tests.
const googleIssuer = "https://accounts.google.com"

// How long groups hold unless GROUPS_MAX_AGE says, and how often at most a
// live session reads them again; see the top of this file.
const (
	defaultGroupsMaxAge = 24 * time.Hour
	groupsReadEvery     = time.Hour
)

// How often every replica looks for sessions whose groups are due to be
// read again, and how long one that couldn't be waits to be tried again.
const (
	groupsSweep = 5 * time.Second
	groupsRetry = 5 * time.Minute
)

func newAuth(db *sql.DB) *auth {
	a := &auth{
		db:           db,
		publicURL:    strings.TrimRight(env("PUBLIC_URL", ""), "/"),
		admins:       map[string]bool{},
		groupsMaxAge: defaultGroupsMaxAge,
	}
	if v := env("GROUPS_MAX_AGE", ""); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			log.Fatalf("GROUPS_MAX_AGE must be a duration such as 24h or 90m: %q", v)
		}
		a.groupsMaxAge = d
	}
	a.groupsEvery = min(groupsReadEvery, a.groupsMaxAge/2)
	if issuer := strings.TrimRight(env("OIDC_ISSUER", ""), "/"); issuer != "" {
		p := &provider{key: "oidc", issuer: issuer, clientID: env("OIDC_CLIENT_ID", ""), clientSecret: env("OIDC_CLIENT_SECRET", ""),
			groupsClaim: strings.TrimSpace(env("OIDC_GROUPS_CLAIM", ""))}
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

// The condition on users u that their groups were read longer ago than the
// microseconds its argument gives; never NULL, and false for groups put
// there otherwise than by signing in, which have no time they were read.
const staleGroups = "COALESCE(u.groups_read_at < CURRENT_TIMESTAMP(3) - INTERVAL ? MICROSECOND, FALSE)"

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// The user of the request's session, or nil without one still going: one
// of someone whose groups were read longer ago than GROUPS_MAX_AGE is over,
// as they may have left them since (see the top of this file). Whether
// they are an owner of the project the request is about is worked out next
// (enterProject).
func (a *auth) sessionUser(r *http.Request) (*user, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	var u user
	err = a.db.QueryRowContext(r.Context(),
		`SELECT u.id, u.email, u.name, u.admin, u.team_manager FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > CURRENT_TIMESTAMP(3) AND u.removed_at IS NULL
			AND NOT (`+staleGroups+` AND EXISTS (SELECT 1 FROM user_groups g WHERE g.user_id = u.id))`,
		hashToken(c.Value), a.groupsMaxAge.Microseconds()).
		Scan(&u.ID, &u.Email, &u.Name, &u.Super, &u.TeamManager)
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
	// The scopes it says it supports, if it says; see the login route.
	ScopesSupported []string `json:"scopes_supported"`
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
	// Every claim as it came, for the one naming the groups.
	raw map[string]json.RawMessage
}

// The claims of an ID token from the token endpoint, checked as being for
// this sign-in.
func (p *provider) idClaims(token, nonce string) (idClaims, error) {
	c, err := p.checkedClaims(token)
	if err != nil {
		return c, err
	}
	if subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1 {
		return c, errors.New("the ID token is for another sign-in")
	}
	return c, nil
}

// The claims of an ID token the token endpoint gave as a session's tokens
// were refreshed, which has no nonce, checked as being from the provider,
// for this webapp, and about the same person, who is subject to it.
func (p *provider) refreshedClaims(token, subject string) (idClaims, error) {
	c, err := p.checkedClaims(token)
	if err == nil && c.Subject != subject {
		err = errors.New("the refreshed ID token is about someone else")
	}
	return c, err
}

// The claims of an ID token from the token endpoint, checked as being from
// the provider, for this webapp, still good and naming someone.
func (p *provider) checkedClaims(token string) (idClaims, error) {
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
	if err := json.Unmarshal(payload, &c.raw); err != nil {
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
	case c.Subject == "":
		return c, errors.New("the ID token names nobody")
	}
	return c, nil
}

// What the userinfo endpoint says of whoever the access token is for, and
// the status it answered with.
func userinfo(ctx context.Context, endpoint, accessToken string) (idClaims, int, error) {
	var info idClaims
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return info, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := oauthClient.Do(req)
	if err != nil {
		return info, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, resp.StatusCode, fmt.Errorf("userinfo: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err == nil {
		err = json.Unmarshal(body, &info)
	}
	if err == nil {
		err = json.Unmarshal(body, &info.raw)
	}
	if err != nil {
		return info, resp.StatusCode, fmt.Errorf("userinfo: %w", err)
	}
	return info, resp.StatusCode, nil
}

// Where a sign-in found the groups.
const (
	groupsInIDToken  = "id_token"
	groupsInUserinfo = "userinfo"
)

// The groups the provider's claim names among the claims given, and
// whether it is there at all: a string is one group, a list of strings
// several, null none. Names longer than a project's group may have are left
// out, as no project could be given them.
func (p *provider) groupsIn(claims map[string]json.RawMessage) ([]string, bool, error) {
	raw, ok := claims[p.groupsClaim]
	if p.groupsClaim == "" || !ok {
		return nil, false, nil
	}
	var names []string
	var one *string
	if err := json.Unmarshal(raw, &one); err == nil {
		if one != nil {
			names = []string{*one}
		}
	} else if err := json.Unmarshal(raw, &names); err != nil {
		return nil, true, fmt.Errorf("the claim %s, which OIDC_GROUPS_CLAIM names, is neither a string nor a list of strings", p.groupsClaim)
	}
	groups := []string{}
	for _, n := range names {
		if n != "" && len(n) <= maxGroupName && !slices.Contains(groups, n) {
			groups = append(groups, n)
		}
	}
	return groups, true, nil
}

// Replaces the person's groups with those given, read from the provider
// now.
func setGroups(ctx context.Context, tx *sql.Tx, user int64, groups []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_groups WHERE user_id = ?", user); err != nil {
		return err
	}
	for _, g := range groups {
		if _, err := tx.ExecContext(ctx, "INSERT INTO user_groups (user_id, group_name) VALUES (?, ?)", user, g); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE users SET groups_read_at = CURRENT_TIMESTAMP(3) WHERE id = ?", user)
	return err
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
// says of them, their groups now those given; errNotAdmitted if they may
// not sign in.
func (a *auth) signedIn(ctx context.Context, p *provider, c idClaims, groups []string) (int64, error) {
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
			if _, err = tx.ExecContext(ctx, "INSERT INTO user_identities (issuer, subject, user_id) VALUES (?, ?, ?)", p.issuer, c.Subject, id); err != nil {
				return err
			}
			if err := joinLegacy(ctx, tx, id, admin); err != nil {
				return err
			}
			return setGroups(ctx, tx, id, groups)
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
		if err != nil {
			return err
		}
		return setGroups(ctx, tx, id, groups)
	})
	return id, err
}

func registerAuthRoutes(mux *http.ServeMux, db *sql.DB, a *auth) {
	// Who is signed in, with their roles and teams in the project the
	// request names, none if it names none, and whether anyone signs in at
	// all; whether they are the instance's admin (super), and the projects
	// they may enter, each with their level there. Their admin is whether
	// they own the project the request names (see projects.go). Someone who
	// may enter none is told whom to ask: the instance's admins, by name
	// (admins). Without sign-in everyone may do anything, in any project.
	mux.HandleFunc("GET /api/me", func(w http.ResponseWriter, r *http.Request) {
		u := requestUser(r)
		me := map[string]any{"signIn": a != nil, "user": nil, "super": u == nil || u.Super}
		if u != nil {
			project, _ := requestProjectIf(r)
			p, err := getPerson(r.Context(), db, u.ID, project)
			if err != nil {
				serverError(w, err)
				return
			}
			p.Admin = u.Admin
			me["user"] = p
		}
		projects, err := enterableProjects(r.Context(), db, u)
		if err != nil {
			serverError(w, err)
			return
		}
		me["projects"] = projects
		if u != nil && len(projects) == 0 {
			admins := []string{}
			err := scanAll(r.Context(), db, "SELECT COALESCE(NULLIF(name, ''), email) FROM users WHERE admin AND removed_at IS NULL ORDER BY 1", func(rows *sql.Rows) error {
				var n string
				err := rows.Scan(&n)
				admins = append(admins, n)
				return err
			})
			if err != nil {
				serverError(w, err)
				return
			}
			me["admins"] = admins
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
		// A refresh token, to read someone's groups again while their
		// session lives, where the provider says it gives one this way.
		scope := "openid email profile"
		if pr.groupsClaim != "" && slices.Contains(p.ScopesSupported, "offline_access") {
			scope += " offline_access"
		}
		q := url.Values{
			"response_type":         {"code"},
			"client_id":             {pr.clientID},
			"redirect_uri":          {a.redirectURL(r)},
			"scope":                 {scope},
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
		as, err := a.finish(r, pr, s, q.Get("code"))
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
		if err := a.startSession(r.Context(), token, pr, as); err != nil {
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

// What the token endpoint gives.
type oidcTokens struct {
	tokenResponse
	IDToken string `json:"id_token"`
}

// Asks the provider's token endpoint for tokens, as its client, and says
// what it gave, the status it answered with, and what went wrong.
func (pr *provider) tokens(ctx context.Context, endpoint string, form url.Values) (oidcTokens, int, error) {
	var t oidcTokens
	// A public client says who it is in the form.
	if pr.clientSecret == "" {
		form.Set("client_id", pr.clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return t, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if pr.clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(pr.clientID), url.QueryEscape(pr.clientSecret))
	}
	resp, err := oauthClient.Do(req)
	if err != nil {
		return t, 0, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return t, resp.StatusCode, fmt.Errorf("token endpoint: status %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := t.Error
		if t.Description != "" {
			msg += ": " + t.Description
		}
		return t, resp.StatusCode, fmt.Errorf("token endpoint: status %d: %s", resp.StatusCode, msg)
	}
	return t, resp.StatusCode, nil
}

// What signing in came to: who signed in, and what their session keeps to
// read their groups again.
type signedInAs struct {
	user int64
	// The groups they are in, and where the sign-in found them
	// (groupsInIDToken or groupsInUserinfo); none found, none.
	groups     []string
	groupsFrom string
	// The provider's tokens, and how many seconds the access token lasts,
	// if it says.
	refreshToken, accessToken string
	expiresIn                 int64
}

// Exchanges the code the provider came back with for who signed in, and
// returns their user and what their session keeps.
func (a *auth) finish(r *http.Request, pr *provider, s signIn, code string) (signedInAs, error) {
	ctx := r.Context()
	var as signedInAs
	p, err := pr.discover(ctx)
	if err != nil {
		return as, err
	}
	t, _, err := pr.tokens(ctx, p.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {a.redirectURL(r)},
		"code_verifier": {s.Verifier},
	})
	if err == nil && t.IDToken == "" {
		err = errors.New("token endpoint: no ID token")
	}
	if err != nil {
		return as, err
	}
	claims, err := pr.idClaims(t.IDToken, s.Nonce)
	if err != nil {
		return as, err
	}
	groups, found, err := pr.groupsIn(claims.raw)
	if err != nil {
		return as, err
	}
	if found {
		as.groupsFrom = groupsInIDToken
	}
	// The userinfo endpoint fills in the email and name the ID token left
	// out, and the groups.
	if (claims.Email == "" || claims.Name == "" || (pr.groupsClaim != "" && !found)) && p.UserinfoEndpoint != "" {
		info, _, err := userinfo(ctx, p.UserinfoEndpoint, t.AccessToken)
		if err != nil {
			return as, err
		}
		// Only about the same person.
		if info.Subject != claims.Subject {
			return as, errors.New("userinfo is about someone else")
		}
		if claims.Email == "" {
			claims.Email, claims.EmailVerified = info.Email, info.EmailVerified
		}
		if claims.Name == "" {
			claims.Name = info.Name
		}
		if !found {
			if groups, found, err = pr.groupsIn(info.raw); err != nil {
				return as, err
			}
			if found {
				as.groupsFrom = groupsInUserinfo
			}
		}
	}
	as.groups, as.refreshToken, as.accessToken, as.expiresIn = groups, t.RefreshToken, t.AccessToken, t.ExpiresIn
	as.user, err = a.signedIn(ctx, pr, claims, groups)
	return as, err
}

// Where a session's refresh or access token, as column says, is kept.
func sessionTokenPlace(column, hash string) string { return "sessions/" + column + "/" + hash }

// A token as a session keeps it: sealed, or NULL for none.
func sessionToken(column, hash, token string) (sql.NullString, error) {
	if token == "" {
		return sql.NullString{}, nil
	}
	v, err := secrets.seal(sessionTokenPlace(column, hash), token)
	return sql.NullString{String: v, Valid: err == nil}, err
}

// Starts the session of whoever signed in, under the token given. Someone
// with groups from a provider that carries them keeps its tokens, to read
// them again, as long as one is left to read them with.
func (a *auth) startSession(ctx context.Context, token string, pr *provider, as signedInAs) error {
	hash := hashToken(token)
	var refresh, access sql.NullString
	var expiresIn *int64
	var checkIn *int64
	if pr.groupsClaim != "" && len(as.groups) > 0 && (as.refreshToken != "" || as.accessToken != "") {
		var err error
		if refresh, err = sessionToken("refresh_token", hash, as.refreshToken); err != nil {
			return err
		}
		if access, err = sessionToken("access_token", hash, as.accessToken); err != nil {
			return err
		}
		if as.expiresIn > 0 {
			expiresIn = &as.expiresIn
		}
		every := a.groupsEvery.Microseconds()
		checkIn = &every
	}
	_, err := a.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at, issuer, refresh_token, access_token, access_expires_at, groups_from, groups_check_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP(3) + INTERVAL ? SECOND, ?, CURRENT_TIMESTAMP(3) + INTERVAL ? MICROSECOND)`,
		hash, as.user, time.Now().Add(sessionLifetime).UTC(), pr.issuer, refresh, access, expiresIn, as.groupsFrom, checkIn)
	return err
}

// Reads again, every groupsSweep until ctx ends, the groups of people with
// a live session whose groups are due to be; see the top of this file.
func (a *auth) watchGroups(ctx context.Context) {
	if a == nil {
		return
	}
	t := time.NewTicker(groupsSweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := a.readGroupsDue(ctx); err != nil && ctx.Err() == nil {
			log.Printf("WARNING: couldn't read people's groups again: %v", err)
		}
	}
}

// Reads again the groups of the people with groups read longer ago than
// groupsEvery, each through one of their sessions due, which only one
// replica claims at a time by moving its next reading on. Only sessions of
// the providers this replica signs in with are looked at, so one started
// with other providers, as in a deploy changing them, leaves the others'
// sessions be.
func (a *auth) readGroupsDue(ctx context.Context) error {
	var issuers []any
	for _, p := range a.providers {
		if p.groupsClaim != "" {
			issuers = append(issuers, p.issuer)
		}
	}
	if len(issuers) == 0 {
		return nil
	}
	var due []string
	err := scanAll(ctx, a.db, `SELECT s.token_hash FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.groups_check_at <= CURRENT_TIMESTAMP(3) AND s.expires_at > CURRENT_TIMESTAMP(3) AND s.issuer IN `+placeholders(len(issuers))+`
			AND u.removed_at IS NULL AND `+staleGroups+` AND EXISTS (SELECT 1 FROM user_groups g WHERE g.user_id = u.id)
		ORDER BY s.groups_check_at LIMIT 20`, func(rows *sql.Rows) error {
		var hash string
		err := rows.Scan(&hash)
		due = append(due, hash)
		return err
	}, append(issuers, a.groupsEvery.Microseconds())...)
	if err != nil {
		return err
	}
	for _, hash := range due {
		res, err := a.db.ExecContext(ctx, `UPDATE sessions s JOIN users u ON u.id = s.user_id
			SET s.groups_check_at = CURRENT_TIMESTAMP(3) + INTERVAL ? MICROSECOND
			WHERE s.token_hash = ? AND s.groups_check_at <= CURRENT_TIMESTAMP(3) AND `+staleGroups,
			min(groupsRetry, a.groupsEvery).Microseconds(), hash, a.groupsEvery.Microseconds())
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			continue
		}
		if err := a.readGroupsAgain(ctx, hash); err != nil && ctx.Err() == nil {
			log.Printf("WARNING: couldn't read the groups of a session's person again, so they are tried again in %s: %v", min(groupsRetry, a.groupsEvery), err)
		}
	}
	return nil
}

// Why a session's groups can't be read again, whatever is tried.
var errGroupsUnreadable = errors.New("the groups can't be read again")

// Reads the groups of the session's person again from the provider, with
// the session's refresh token, or its access token while it lasts, and
// keeps the tokens the provider gives back. A session the provider refuses
// to refresh ends. One whose groups can't be read again, as it has no token
// left that works, or neither a new ID token nor userinfo carries the
// claim where the sign-in found it, keeps its tokens no more; it ends once
// its person's groups are older than GROUPS_MAX_AGE. Any other error is
// returned, to be tried again.
func (a *auth) readGroupsAgain(ctx context.Context, hash string) error {
	err := a.refreshGroups(ctx, hash)
	switch {
	case errors.Is(err, errGroupsUnreadable):
		log.Printf("a session's groups can't be read again (%v), so it ends once they are older than %s", err, a.groupsMaxAge)
		_, err = a.db.ExecContext(ctx, `UPDATE sessions SET refresh_token = NULL, access_token = NULL, access_expires_at = NULL, groups_check_at = NULL
			WHERE token_hash = ?`, hash)
	case errors.Is(err, errRefreshRefused):
		_, err = a.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", hash)
	}
	return err
}

// The provider refused to refresh a session's tokens: whoever it was for
// may no longer sign in with it.
var errRefreshRefused = errors.New("the provider refused to refresh the session")

func (a *auth) refreshGroups(ctx context.Context, hash string) error {
	var user int64
	var issuer, from string
	var refreshSealed, accessSealed sql.NullString
	var accessGone bool
	err := a.db.QueryRowContext(ctx, `SELECT user_id, issuer, groups_from, refresh_token, access_token,
			COALESCE(access_expires_at <= CURRENT_TIMESTAMP(3), FALSE) FROM sessions WHERE token_hash = ?`, hash).
		Scan(&user, &issuer, &from, &refreshSealed, &accessSealed, &accessGone)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var pr *provider
	for _, p := range a.providers {
		if p.issuer == issuer && p.groupsClaim != "" {
			pr = p
		}
	}
	if pr == nil {
		return nil
	}
	var subject string
	err = a.db.QueryRowContext(ctx, "SELECT subject FROM user_identities WHERE issuer = ? AND user_id = ?", issuer, user).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: they are no longer known to the provider", errGroupsUnreadable)
	}
	if err != nil {
		return err
	}
	open := func(column string, stored sql.NullString) (string, error) {
		if !stored.Valid {
			return "", nil
		}
		return secrets.open(sessionTokenPlace(column, hash), stored.String)
	}
	refresh, err := open("refresh_token", refreshSealed)
	if err != nil {
		return err
	}
	access, err := open("access_token", accessSealed)
	if err != nil {
		return err
	}
	if refresh == "" && (access == "" || accessGone) {
		return fmt.Errorf("%w: the provider gave no refresh token, and the access token expired", errGroupsUnreadable)
	}
	p, err := pr.discover(ctx)
	if err != nil {
		return err
	}
	var expiresIn *int64
	var refreshed map[string]json.RawMessage
	if refresh != "" {
		t, status, err := pr.tokens(ctx, p.TokenEndpoint, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
		if t.Error == "invalid_grant" {
			return errRefreshRefused
		}
		if err == nil && t.AccessToken == "" {
			err = fmt.Errorf("token endpoint: status %d: no access token", status)
		}
		if err != nil {
			return err
		}
		access = t.AccessToken
		if t.RefreshToken != "" {
			refresh = t.RefreshToken
		}
		if t.ExpiresIn > 0 {
			expiresIn = &t.ExpiresIn
		}
		if t.IDToken != "" {
			c, err := pr.refreshedClaims(t.IDToken, subject)
			if err != nil {
				return fmt.Errorf("%w: %v", errGroupsUnreadable, err)
			}
			refreshed = c.raw
		}
	}
	groups, found, err := pr.groupsIn(refreshed)
	if err != nil {
		return fmt.Errorf("%w: %v", errGroupsUnreadable, err)
	}
	asked := false
	if !found && p.UserinfoEndpoint != "" {
		info, status, err := userinfo(ctx, p.UserinfoEndpoint, access)
		if status == http.StatusUnauthorized && refresh == "" {
			return fmt.Errorf("%w: the access token is no longer taken", errGroupsUnreadable)
		}
		if err != nil {
			return err
		}
		if info.Subject != subject {
			return fmt.Errorf("%w: userinfo is about someone else", errGroupsUnreadable)
		}
		if groups, found, err = pr.groupsIn(info.raw); err != nil {
			return fmt.Errorf("%w: %v", errGroupsUnreadable, err)
		}
		asked = true
	}
	// The claim left out where the sign-in found it says they are in no
	// group; left out elsewhere it says nothing.
	if !found && !((from == groupsInIDToken && refreshed != nil) || (from == groupsInUserinfo && asked)) {
		return fmt.Errorf("%w: neither a refreshed ID token nor userinfo carries the claim %s", errGroupsUnreadable, pr.groupsClaim)
	}
	sealedRefresh, err := sessionToken("refresh_token", hash, refresh)
	if err != nil {
		return err
	}
	sealedAccess, err := sessionToken("access_token", hash, access)
	if err != nil {
		return err
	}
	return inTx(ctx, a.db, func(tx *sql.Tx) error {
		if err := setGroups(ctx, tx, user, groups); err != nil {
			return err
		}
		// A new access token lasts as long as the provider said, if it
		// said; the one kept, as long as it did.
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET refresh_token = ?, access_token = ?,
				access_expires_at = IF(?, CURRENT_TIMESTAMP(3) + INTERVAL ? SECOND, access_expires_at),
				groups_check_at = CURRENT_TIMESTAMP(3) + INTERVAL ? MICROSECOND WHERE token_hash = ?`,
			sealedRefresh, sealedAccess, refresh != "", expiresIn, a.groupsEvery.Microseconds(), hash)
		return err
	})
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
