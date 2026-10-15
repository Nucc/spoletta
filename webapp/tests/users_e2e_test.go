package tests

// Users: people sign in through an identity provider, faked here, and runs
// and decisions record who they were.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const usersClientID, usersClientSecret = "spoletta", "secret"

// Someone the fake provider signs in.
type usersPerson struct {
	subject, email, name string
	// Whether the provider says it checked their email; nil says nothing.
	verified *bool
}

// An identity provider that signs in whoever the test says is next, and
// hands out ID tokens it doesn't sign, as the webapp doesn't check them.
type fakeProvider struct {
	t   *testing.T
	srv *httptest.Server

	mu   sync.Mutex
	next usersPerson
	// What the code sent back stands for: who signed in, the nonce and PKCE
	// challenge the webapp sent, and where it sent the browser back to.
	codes map[string]usersCode
	// Changes the ID token's claims, to test what the webapp refuses.
	tamper func(map[string]any)
}

type usersCode struct {
	person                       usersPerson
	nonce, challenge, redirectTo string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	p := &fakeProvider{t: t, codes: map[string]usersCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                 p.srv.URL,
			"authorization_endpoint": p.srv.URL + "/authorize",
			"token_endpoint":         p.srv.URL + "/token",
		})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != usersClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
			!strings.Contains(q.Get("scope"), "openid") {
			http.Error(w, "bad authorization request: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		code := fmt.Sprintf("code-%d", len(p.codes))
		p.codes[code] = usersCode{person: p.next, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectTo: q.Get("redirect_uri")}
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != usersClientID || secret != usersClientSecret {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
			return
		}
		r.ParseForm()
		p.mu.Lock()
		c, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		tamper := p.tamper
		p.mu.Unlock()
		challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(challenge[:]) != c.challenge || r.Form.Get("redirect_uri") != c.redirectTo {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
		claims := map[string]any{
			"iss": p.srv.URL, "aud": usersClientID, "exp": time.Now().Add(time.Hour).Unix(), "nonce": c.nonce,
			"sub": c.person.subject, "email": c.person.email, "name": c.person.name,
		}
		if c.person.verified != nil {
			claims["email_verified"] = *c.person.verified
		}
		if tamper != nil {
			tamper(claims)
		}
		payload, _ := json.Marshal(claims)
		token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "access", "token_type": "Bearer", "id_token": token})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

// A webapp people sign in to through the provider, the admin and anyone at
// example.org may.
func usersApp(t *testing.T) (*app, *fakeProvider) {
	t.Helper()
	p := newFakeProvider(t)
	fw := newFakeWorker(t)
	a := startApp(t, fw.url,
		"OIDC_ISSUER="+p.srv.URL,
		"OIDC_CLIENT_ID="+usersClientID,
		"OIDC_CLIENT_SECRET="+usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com")
	a.worker = fw
	return a, p
}

var usersAda = usersPerson{subject: "ada", email: "ada@example.com", name: "Ada Lovelace"}

// Signs the person in, and returns the client with their session, and the
// status and URL signing in ended on.
func usersTrySignIn(a *app, p *fakeProvider, who usersPerson) (*http.Client, int, string) {
	a.t.Helper()
	p.mu.Lock()
	p.next = who
	p.mu.Unlock()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Get(a.url + "/api/auth/login?next=" + url.QueryEscape("/flows/7"))
	if err != nil {
		a.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return c, resp.StatusCode, resp.Request.URL.Path
}

// Signs the person in, which must succeed, and makes the app's requests theirs.
func usersSignIn(a *app, p *fakeProvider, who usersPerson) {
	a.t.Helper()
	c, status, path := usersTrySignIn(a, p, who)
	// The page asked for, which the empty static directory hasn't.
	if path != "/flows/7" || status != http.StatusNotFound {
		a.t.Fatalf("signing in %s ended on %s with %d", who.email, path, status)
	}
	a.client = c
}

type usersMe struct {
	SignIn bool `json:"signIn"`
	User   *struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
	} `json:"user"`
}

// Without a session only signing in and the MCP proxy, which steps reach
// with tokens of their own, answer.
func TestUsersSignInNeeded(t *testing.T) {
	a, _ := usersApp(t)
	a.refused("GET", "/api/flows", nil, http.StatusUnauthorized, "sign in first")
	a.refused("GET", "/api/me", nil, http.StatusUnauthorized, "sign in first")
	a.refused("POST", "/api/flows", map[string]any{"name": "Nope"}, http.StatusUnauthorized, "sign in first")
	a.refused("GET", "/mcp/1", nil, http.StatusForbidden, "isn't available to this step")
	a.call("GET", "/health", nil, http.StatusOK, nil)

	// A session cookie the webapp didn't make is none.
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(a.url)
	jar.SetCookies(u, []*http.Cookie{{Name: "spoletta_session", Value: "made-up"}})
	a.client = &http.Client{Jar: jar}
	a.refused("GET", "/api/flows", nil, http.StatusUnauthorized, "sign in first")
}

// Signing in comes back to the page asked for, signed in; signing out ends it.
func TestUsersSignInAndOut(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	var me usersMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if !me.SignIn || me.User == nil || me.User.Email != "ada@example.com" || me.User.Name != "Ada Lovelace" || !me.User.Admin {
		t.Fatalf("me = %+v", me)
	}
	first := me.User.ID

	// Signing in again is the same user, with what the provider now says.
	usersSignIn(a, p, usersPerson{subject: "ada", email: "ada@example.com", name: "Ada King"})
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if me.User.ID != first || me.User.Name != "Ada King" {
		t.Errorf("signing in again = %+v, want user %d named Ada King", me.User, first)
	}

	a.call("POST", "/api/auth/logout", nil, http.StatusNoContent, nil)
	a.refused("GET", "/api/me", nil, http.StatusUnauthorized, "sign in first")

	// Someone else the provider gives Ada's email isn't Ada.
	client, status, _ := usersTrySignIn(a, p, usersPerson{subject: "not-ada", email: "ada@example.com", name: "Impostor"})
	if status != http.StatusForbidden {
		t.Errorf("another subject with ada's email ended with %d, want 403", status)
	}
	a.client = client
	a.refused("GET", "/api/me", nil, http.StatusUnauthorized, "sign in first")
}

// Only admins and people at an allowed domain sign in, with an email the
// provider doesn't say it didn't check.
func TestUsersWhoMaySignIn(t *testing.T) {
	a, p := usersApp(t)
	no := false
	for _, c := range []struct {
		name   string
		who    usersPerson
		status int
	}{
		{"an allowed domain", usersPerson{subject: "bob", email: "bob@example.org"}, http.StatusNotFound},
		{"another domain", usersPerson{subject: "eve", email: "eve@elsewhere.com"}, http.StatusForbidden},
		{"a domain ending like an allowed one", usersPerson{subject: "mal", email: "mal@notexample.org"}, http.StatusForbidden},
		{"an unverified email", usersPerson{subject: "carl", email: "carl@example.org", verified: &no}, http.StatusForbidden},
		{"no email", usersPerson{subject: "nobody"}, http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			a.t = t
			client, status, _ := usersTrySignIn(a, p, c.who)
			if status != c.status {
				t.Errorf("signing in ended with %d, want %d", status, c.status)
			}
			a.client = client
			if c.status == http.StatusForbidden {
				a.refused("GET", "/api/me", nil, http.StatusUnauthorized, "sign in first")
			} else {
				a.call("GET", "/api/me", nil, http.StatusOK, nil)
			}
		})
	}
}

// An ID token for another sign-in, client or issuer, or one expired, signs
// nobody in.
func TestUsersRefusedIDTokens(t *testing.T) {
	a, p := usersApp(t)
	for _, c := range []struct {
		name   string
		tamper func(map[string]any)
	}{
		{"another nonce", func(c map[string]any) { c["nonce"] = "other" }},
		{"another audience", func(c map[string]any) { c["aud"] = []string{"someone-else"} }},
		{"another issuer", func(c map[string]any) { c["iss"] = "https://evil.example" }},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			a.t = t
			p.mu.Lock()
			p.tamper = c.tamper
			p.mu.Unlock()
			client, status, _ := usersTrySignIn(a, p, usersAda)
			if status != http.StatusBadGateway {
				t.Errorf("signing in ended with %d, want %d", status, http.StatusBadGateway)
			}
			a.client = client
			a.refused("GET", "/api/me", nil, http.StatusUnauthorized, "sign in first")
		})
	}
	p.tamper = nil

	// A callback whose state isn't the sign-in's.
	a.t = t
	a.client = http.DefaultClient
	a.refused("GET", "/api/auth/callback?code=code-0&state=forged", nil, http.StatusBadRequest, "expired or was already used")
}

// A run says who started it, and a review who decided it.
func TestUsersRecordWhoStartedAndDecided(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	if rn.StartedBy != "Ada Lovelace" {
		t.Errorf("started by %q", rn.StartedBy)
	}
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	if review.DecidedBy != "" {
		t.Errorf("a waiting review decided by %q", review.DecidedBy)
	}

	// Someone else, an editor, decides it.
	bob := usersNewPerson("Bob")
	bob.name = ""
	usersAdd(a, bob.email, "Editor")
	usersSignIn(a, p, bob)
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	d := a.finish(rn.ID)
	if r := onlyStep(t, d, "Review"); r.DecidedBy != bob.email {
		t.Errorf("decided by %q, want bob's email, as he has no name", r.DecidedBy)
	}
	var runs []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d", b.flow.ID), nil, http.StatusOK, &runs)
	if len(runs) != 1 || runs[0].StartedBy != "Ada Lovelace" {
		t.Errorf("runs = %+v", runs)
	}
}

// Without a provider nobody signs in, and everything answers as before.
func TestUsersNoSignIn(t *testing.T) {
	a := newApp(t)
	var me usersMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if me.SignIn || me.User != nil {
		t.Errorf("me = %+v", me)
	}
	a.refused("GET", "/api/auth/login", nil, http.StatusNotFound, "")
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	if d := a.finish(rn.ID); d.StartedBy != "" || onlyStep(t, d, "Review").DecidedBy != "" {
		t.Errorf("started by %q, decided by %q", d.StartedBy, onlyStep(t, d, "Review").DecidedBy)
	}
}

// Someone at the allowed domain no other test has signed in.
func usersNewPerson(name string) usersPerson {
	id := fmt.Sprintf("%s%d", strings.ToLower(name), time.Now().UnixNano())
	return usersPerson{subject: id, email: id + "@example.org", name: name}
}

type usersPersonRow struct {
	ID    int64    `json:"id"`
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Admin bool     `json:"admin"`
	Roles []string `json:"roles"`
}

// Makes the flow global, so everyone sees it, not only its maker.
func usersShare(a *app, f int64) {
	a.t.Helper()
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f), map[string]any{"kind": "global"}, http.StatusOK, nil)
}

// The admin adds someone, before they first sign in.
func usersAdd(a *app, email string, roles ...string) usersPersonRow {
	a.t.Helper()
	var p usersPersonRow
	a.call("POST", "/api/users", map[string]any{"email": email, "roles": roles}, http.StatusCreated, &p)
	return p
}

// Only someone holding the role a human object names decides the reviews of
// the process it handles; an admin decides any.
func TestUsersRolesDecideReviews(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	b := humanNewReviewBoard(a)
	usersShare(a, b.flow.ID)
	bob := usersNewPerson("Bob")
	row := usersAdd(a, bob.email)
	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	if !review.CanDecide || !slices.Equal(review.HandledBy, []string{"Editor"}) {
		t.Errorf("the admin sees the review handled by %v, can decide %v", review.HandledBy, review.CanDecide)
	}

	usersSignIn(a, p, bob)
	path := fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, review.ID)
	if s := onlyStep(t, a.getRun(rn.ID), "Review"); s.CanDecide {
		t.Errorf("bob, without the role, can decide")
	}
	a.refused("PATCH", path, map[string]any{"state": b.approved.ID}, http.StatusForbidden, "only Editor decides this review")
	a.refused("PATCH", "/api/users/"+fmt.Sprint(row.ID), map[string]any{"roles": []string{"Editor"}}, http.StatusForbidden, "only an admin")

	// The role matches ignoring case and spaces.
	usersSignIn(a, p, usersAda)
	a.call("PATCH", fmt.Sprintf("/api/users/%d", row.ID), map[string]any{"roles": []string{"  editor ", "", "EDITOR", "Finance"}}, http.StatusOK, &row)
	if !slices.Equal(row.Roles, []string{"editor", "Finance"}) {
		t.Errorf("roles = %v", row.Roles)
	}
	usersSignIn(a, p, bob)
	if s := onlyStep(t, a.getRun(rn.ID), "Review"); !s.CanDecide {
		t.Errorf("bob, with the role, can't decide")
	}
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	if r := onlyStep(t, a.finish(rn.ID), "Review"); r.DecidedBy != "Bob" {
		t.Errorf("decided by %q", r.DecidedBy)
	}

	// The admin decides without the role.
	usersSignIn(a, p, usersAda)
	rn = a.startRun(b.flow.ID)
	review = a.waitStep(rn.ID, "Review", stepWaiting)
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	if r := onlyStep(t, a.finish(rn.ID), "Review"); r.DecidedBy != "Ada Lovelace" {
		t.Errorf("decided by %q", r.DecidedBy)
	}
}

// A review nobody handles is anyone's; one on a board inside a flow process
// is its human object's.
func TestUsersRolesWhereReviewsAre(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	f := a.newFlow("Nested review").ID
	write := a.agent(f, "Write", "Write a ticket.")
	ticket := a.newObject(f, "Ticket")
	approved := a.newState(ticket.ID, "approved")
	outer := nestedFlowProcess(a, f, "Outer", false)
	result := a.newObject(f, "Result")
	a.output(write.ID, ticket.ID)
	a.input(outer.ID, ticket.ID)
	a.output(outer.ID, result.ID)
	a.startAt(f, write.ID)
	approve := nestedProcess(a, f, outer.ID, "Approve", kindHuman)
	a.input(approve.ID, ticket.ID)
	finance := nestedObject(a, outer.ID, "Finance")
	nestedPatchObject(a, finance.ID, map[string]any{"human": true})
	a.input(approve.ID, finance.ID)
	humanDecides(a, approve.ID, ticket.ID, approved.ID)
	pay := nestedAgent(a, f, outer.ID, "Pay")
	humanTakesIn(a, pay.ID, ticket.ID, approved.ID)
	a.output(pay.ID, result.ID)
	nestedStartAt(a, outer.ID, approve.ID)
	a.worker.onAgent("Write", handsIn(data("the ticket")))
	a.worker.onAgent("Pay", handsIn(data("paid")))

	// A review no person handles, on its own flow.
	free := humanHuman(a, a.newFlow("Free review").ID, "Look")
	freeFlow := free.Flow
	freeDoc := a.newObject(freeFlow, "Doc")
	seen := a.newState(freeDoc.ID, "seen")
	draft := a.agent(freeFlow, "Draft", "Draft it.")
	a.output(draft.ID, freeDoc.ID)
	a.input(free.ID, freeDoc.ID)
	humanDecides(a, free.ID, freeDoc.ID, seen.ID)
	a.startAt(freeFlow, draft.ID)
	a.worker.onAgent("Draft", handsIn(data("a doc")))
	usersShare(a, f)
	usersShare(a, freeFlow)

	rn := a.startRun(f)
	freeRun := a.startRun(freeFlow)
	nested := a.waitStep(rn.ID, "Approve", stepWaiting)
	look := a.waitStep(freeRun.ID, "Look", stepWaiting)

	usersSignIn(a, p, usersNewPerson("Carl"))
	if !look.CanDecide || len(look.HandledBy) != 0 {
		t.Errorf("the free review is handled by %v", look.HandledBy)
	}
	if s := onlyStep(t, a.getRun(rn.ID), "Approve"); s.CanDecide || !slices.Equal(s.HandledBy, []string{"Finance"}) {
		t.Errorf("carl sees the nested review handled by %v, can decide %v", s.HandledBy, s.CanDecide)
	}
	a.refused("PATCH", fmt.Sprintf("/api/runs/%d/steps/%d", rn.ID, nested.ID), map[string]any{"state": approved.ID}, http.StatusForbidden, "only Finance")
	humanMustDecide(a, freeRun.ID, look.ID, map[string]any{"state": seen.ID})
	if d := a.finish(freeRun.ID); d.Status != runDone {
		t.Errorf("the free run %s", d.Status)
	}
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
}

// Admins add people, who then sign in from any domain, make others admins,
// and see the roles boards name; nobody else does.
func TestUsersAdminsManagePeople(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	f := a.newFlow("Roles")
	humanPerson(a, f.ID, humanHuman(a, f.ID, "Sign off").ID, "  Legal ")

	carl := usersPerson{subject: fmt.Sprint("carl", time.Now().UnixNano()), email: fmt.Sprintf("carl%d@elsewhere.com", time.Now().UnixNano()), name: "Carl"}
	row := usersAdd(a, strings.ToUpper(carl.email), "Legal")
	if row.Email != carl.email || row.Admin || !slices.Equal(row.Roles, []string{"Legal"}) {
		t.Errorf("added %+v", row)
	}
	a.refused("POST", "/api/users", map[string]any{"email": carl.email}, http.StatusConflict, "already added")
	a.refused("POST", "/api/users", map[string]any{"email": "not-an-email"}, http.StatusBadRequest, "email address")
	a.refused("PATCH", "/api/users/1099511627776", map[string]any{"admin": true}, http.StatusNotFound, "no one")
	var me usersMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	a.refused("PATCH", fmt.Sprintf("/api/users/%d", me.User.ID), map[string]any{"admin": false}, http.StatusBadRequest, "yourself")
	var roles []string
	a.call("GET", "/api/roles", nil, http.StatusOK, &roles)
	if !slices.Contains(roles, "Legal") {
		t.Errorf("roles = %v", roles)
	}

	// Carl, at a domain not allowed, signs in as the person added.
	usersSignIn(a, p, carl)
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if me.User.ID != row.ID || me.User.Name != "Carl" {
		t.Errorf("carl signed in as %+v, want %d", me.User, row.ID)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/users"}, {"POST", "/api/users"}, {"GET", "/api/roles"}, {"PATCH", fmt.Sprintf("/api/users/%d", row.ID)},
	} {
		a.refused(c.method, c.path, map[string]any{"email": "x@example.org", "admin": true}, http.StatusForbidden, "only an admin")
	}

	usersSignIn(a, p, usersAda)
	a.call("PATCH", fmt.Sprintf("/api/users/%d", row.ID), map[string]any{"admin": true}, http.StatusOK, &row)
	usersSignIn(a, p, carl)
	var people []usersPersonRow
	a.call("GET", "/api/users", nil, http.StatusOK, &people)
	if !slices.ContainsFunc(people, func(p usersPersonRow) bool { return p.ID == row.ID && p.Admin }) {
		t.Errorf("people = %+v", people)
	}
}
