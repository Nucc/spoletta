package main

// Credentials: what scripts and agents call a service of their own with,
// such as a Jira token or an internal API's key, without ever seeing it.
// Admins keep them in the settings, under Credentials: each a name, the
// service's base URL, how its secret goes on each request (a header whose
// value a template such as Bearer {secret} makes, basic auth with a
// username, or a query parameter), and the secret, sealed with SECRET_KEY
// (secrets.go) and never sent back to the browser. Each is everyone's, or
// only the teams' it is given to.
//
// An agent or script process picks the credentials it uses
// (processSettings.Credentials). A flow uses only those its owner may, as
// it does skills (flowSkills): a person's flow, everyone's and those given
// a team of theirs; a team's flow, everyone's and those given the team; a
// global one, only everyone's. A step whose process picked one its flow may
// not use fails as it starts, saying so.
//
// The way the model proxy does it (llm.go), a step using credentials gets,
// for each, a variable holding the proxy's URL for it,
// CREDENTIAL_<NAME>_URL, and a token reaching them all, CREDENTIALS_TOKEN,
// granted at any replica (grants.go) and ending with the step, or a while
// after its timeout should the webapp lose it. The proxy, at
// /credentials/{id}/..., takes the token as a bearer token or as basic
// auth's password, checks the step's process picked the credential and its
// flow may still use it, and joins the path to the base URL, refusing one
// that would leave it or that some servers would read as leaving it. It
// passes on only the headers a client sends to say what it sends and
// wants, and those the credential names, never the step's token, puts the
// secret on the request as the credential says and passes it on,
// streaming the answer back with the secret blanked out of it, as sent or
// encoded, and any piece of it, as an echo cut short would hold; an answer
// in a charset the secret's forms aren't found in goes no further. Redirects
// aren't followed: one within the base URL is turned into the proxy's, and
// the secret is taken out of any other. Only the answer's headers saying
// what it is come back, never one handing the step the secret, or a
// session got with it.
// What is kept of the step, what it prints and says and logs, has its
// tokens blanked out too, so nobody reading it can call the services while
// the step goes. Every call goes in the run's log and the audit log by its
// method and path, never its query, the secret or the token.
//
// A stored secret never goes elsewhere than where it was given for:
// changing a credential's base URL, or where its secret goes, needs the
// secret again.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// How a credential's secret goes on each request.
const (
	// In a header, whose value its template makes.
	credentialHeader = "header"
	// As basic auth's password, with its username.
	credentialBasic = "basic"
	// As a query parameter.
	credentialQuery = "query"
)

// What a template stands for the secret with.
const secretPlaceholder = "{secret}"

// A credential as the settings show it, without its secret.
type credential struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Its name as a variable's, which a step reaches it at,
	// CREDENTIAL_<Variable>_URL.
	Variable    string `json:"variable"`
	Description string `json:"description"`
	BaseURL     string `json:"baseUrl"`
	Auth        string `json:"auth"`
	// The header and its value's template, for credentialHeader.
	Header   string `json:"header,omitempty"`
	Template string `json:"template,omitempty"`
	// Basic auth's username, for credentialBasic.
	Username string `json:"username,omitempty"`
	// The query parameter, for credentialQuery.
	Param string `json:"param,omitempty"`
	// The headers steps may send the service besides those every service
	// gets (credentialPassedHeaders), such as an API's version header.
	Headers []string `json:"headers"`
	// Whether every flow may use it; else only those of the teams given it.
	Everyone bool      `json:"everyone"`
	Teams    []teamRef `json:"teams"`
	// Whether a secret is kept, with its last four characters when it is
	// long enough for them to say little.
	SecretSet bool      `json:"secretSet"`
	Hint      string    `json:"hint,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// A credential as a board offers it to a process: what it reaches, never
// how.
type credentialChoice struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Variable    string `json:"variable"`
	Description string `json:"description"`
	BaseURL     string `json:"baseUrl"`
}

// The limits on what a credential is.
const (
	maxCredentialName        = 64
	maxCredentialDescription = 1024
	maxCredentialSecret      = 4096
	// The most credentials a process picks.
	maxProcessCredentials = 32
)

var (
	credentialName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 _-]*$`)
	// A header's name, as HTTP has it.
	headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	queryParam = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,64}$`)
	notVarChar = regexp.MustCompile(`[^A-Z0-9]+`)
)

// Headers the secret can't go in: the request's own, which the proxy or
// HTTP sets.
var reservedHeaders = []string{"Host", "Content-Length", "Content-Type", "Transfer-Encoding", "Connection", "Cookie",
	"Te", "Trailer", "Upgrade", "Keep-Alive", "Proxy-Connection", "Proxy-Authorization"}

// A credential's name as a variable's: its letters and digits in capitals,
// with an underscore for whatever stands between them.
func credentialVariable(name string) string {
	return strings.Trim(notVarChar.ReplaceAllString(strings.ToUpper(name), "_"), "_")
}

// The variable a step reaches a credential at.
func credentialEnv(variable string) string { return "CREDENTIAL_" + variable + "_URL" }

// Why a base URL can't be a credential's, or "": an https URL, or http at
// the webapp's own machine, such as a sidecar's, where the secret never
// crosses a network; never with a username, a query or a fragment.
func checkCredentialURL(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case raw == "":
		return "the service's base URL is required"
	case len(raw) > 2048 || strings.ContainsAny(raw, "\x00\r\n\t "):
		return "that isn't a URL"
	case err != nil || u.Host == "" || u.Opaque != "":
		return "the base URL must be an https URL, such as https://jira.example.com"
	case u.Scheme == "http" && !loopbackHost(u.Hostname()):
		return "the base URL must be https, so the secret never crosses a network in the clear; only the webapp's own machine is reached over http"
	case u.Scheme != "https" && u.Scheme != "http":
		return "the base URL must be an https URL, such as https://jira.example.com"
	case u.User != nil:
		return "the secret goes in its own field, not the URL"
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "the base URL has no query; a secret going as a query parameter goes in its own field"
	}
	return ""
}

// Whether a host is the machine's own: localhost, or a loopback address.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// What a request makes or changes a credential to. A change without a
// secret keeps the one kept, unless it would send it elsewhere
// (sendsElsewhere).
type credentialFields struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	BaseURL     string   `json:"baseUrl"`
	Auth        string   `json:"auth"`
	Header      string   `json:"header"`
	Template    string   `json:"template"`
	Username    string   `json:"username"`
	Param       string   `json:"param"`
	Headers     []string `json:"headers"`
	Secret      string   `json:"secret"`
	Everyone    bool     `json:"everyone"`
	Teams       []int64  `json:"teams"`
}

// Trims what a request gives, fills in a header credential's defaults, and
// says what is wrong with it, if anything; creating needs a secret.
func (f *credentialFields) check(creating bool) string {
	f.Name = strings.Join(strings.Fields(f.Name), " ")
	f.Description = strings.TrimSpace(f.Description)
	f.BaseURL = strings.TrimSpace(f.BaseURL)
	f.Header, f.Template = strings.TrimSpace(f.Header), strings.TrimSpace(f.Template)
	f.Username, f.Param = strings.TrimSpace(f.Username), strings.TrimSpace(f.Param)
	f.Secret = strings.TrimSpace(f.Secret)
	if f.Auth == credentialHeader && f.Header == "" {
		f.Header = "Authorization"
	}
	if f.Auth == credentialHeader && f.Template == "" {
		f.Template = "Bearer " + secretPlaceholder
	}
	switch {
	case len(f.Name) > maxCredentialName || !credentialName.MatchString(f.Name):
		return fmt.Sprintf("a credential's name starts with a letter and has letters, digits, spaces, hyphens and underscores, at most %d", maxCredentialName)
	case utf8.RuneCountInString(f.Description) > maxCredentialDescription:
		return fmt.Sprintf("a description is at most %d characters", maxCredentialDescription)
	}
	if msg := checkCredentialURL(f.BaseURL); msg != "" {
		return msg
	}
	switch f.Auth {
	case credentialHeader:
		switch {
		case !headerName.MatchString(f.Header):
			return "that isn't a header's name"
		case slices.Contains(reservedHeaders, headerKey(f.Header)):
			return "the secret can't go in " + http.CanonicalHeaderKey(f.Header) + ", which HTTP itself sets"
		case !strings.Contains(f.Template, secretPlaceholder):
			return "the header's value says where the secret goes with " + secretPlaceholder + ", such as Bearer " + secretPlaceholder
		case len(f.Template) > 255 || strings.ContainsAny(f.Template, "\x00\r\n"):
			return "the header's value is one line of at most 255 characters"
		}
		f.Username, f.Param = "", ""
	case credentialBasic:
		if len(f.Username) > 255 || strings.ContainsAny(f.Username, ":\x00\r\n") {
			return "basic auth's username is at most 255 characters, without a colon"
		}
		f.Header, f.Template, f.Param = "", "", ""
	case credentialQuery:
		if !queryParam.MatchString(f.Param) {
			return "the query parameter's name is letters, digits and . _ ~ -, at most 64"
		}
		f.Header, f.Template, f.Username = "", "", ""
	default:
		return "a secret goes in a header, as basic auth's password, or as a query parameter: auth is header, basic or query"
	}
	var headers []string
	for _, h := range f.Headers {
		h = http.CanonicalHeaderKey(strings.TrimSpace(h))
		switch {
		case h == "" || slices.Contains(headers, h):
			continue
		case !headerName.MatchString(h):
			return fmt.Sprintf("%q isn't a header's name", h)
		case !passableHeader(h) || f.Auth == credentialHeader && headerKey(h) == headerKey(f.Header):
			return "steps can't send " + h + ": it carries their token, is the connection's, or could send the call elsewhere"
		}
		headers = append(headers, h)
	}
	if len(strings.Join(headers, ",")) > 1024 {
		return "the headers steps may send are at most 1 KB"
	}
	f.Headers = headers
	switch {
	case creating && f.Secret == "":
		return "the secret is required"
	case len(f.Secret) > maxCredentialSecret || strings.ContainsAny(f.Secret, "\x00\r\n"):
		return "that isn't a secret: one line of at most 4 KB"
	case !f.Everyone && len(f.Teams) == 0:
		return "give it to everyone, or to the teams whose flows may use it"
	}
	if f.Everyone {
		f.Teams = nil
	}
	return ""
}

// The columns of a credential c, for scanCredential.
const credentialColumns = "c.id, c.name, c.variable, c.description, c.base_url, c.auth, c.header, c.template, c.username, c.param, c.headers, c.secret, c.everyone, c.updated_at"

// A credential's row, with its secret as it is kept, and after it what
// more the query asks.
func scanCredential(rows interface{ Scan(...any) error }, more ...any) (credential, string, error) {
	var c credential
	var stored, headers string
	err := rows.Scan(append([]any{&c.ID, &c.Name, &c.Variable, &c.Description, &c.BaseURL, &c.Auth, &c.Header, &c.Template, &c.Username, &c.Param, &headers, &stored, &c.Everyone, &c.UpdatedAt}, more...)...)
	c.Headers = []string{}
	if headers != "" {
		c.Headers = strings.Split(headers, ",")
	}
	return c, stored, err
}

// Every credential, with the teams given each and whether its secret is
// kept, never the secret.
func listCredentials(ctx context.Context, db *sql.DB) ([]credential, error) {
	out := []credential{}
	err := scanAll(ctx, db, "SELECT "+credentialColumns+" FROM credentials c ORDER BY c.name", func(rows *sql.Rows) error {
		c, stored, err := scanCredential(rows)
		if err != nil {
			return err
		}
		if stored != "" {
			secret, err := secrets.open(credentialPlace(c.ID), stored)
			if err != nil {
				return err
			}
			c.SecretSet = true
			if len(secret) > 12 {
				c.Hint = secret[len(secret)-4:]
			}
		}
		c.Teams = []teamRef{}
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = scanAll(ctx, db, "SELECT ct.credential_id, t.id, t.name FROM credential_teams ct JOIN teams t ON t.id = ct.team_id ORDER BY t.name", func(rows *sql.Rows) error {
		var id int64
		var t teamRef
		if err := rows.Scan(&id, &t.ID, &t.Name); err != nil {
			return err
		}
		if i := slices.IndexFunc(out, func(c credential) bool { return c.ID == id }); i >= 0 {
			out[i].Teams = append(out[i].Teams, t)
		}
		return nil
	})
	return out, err
}

// The condition on credentials c that only those the flow may use meet, by
// its owner, as flowSkills has it for skills, and its arguments.
func flowCredentials(ctx context.Context, db querier, flow int64) (string, []any, error) {
	var person, team sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT owner_user_id, owner_team_id FROM flows WHERE id = ?", flow).Scan(&person, &team)
	if err != nil {
		return "", nil, err
	}
	switch {
	case person.Valid:
		return `(c.everyone OR c.id IN (SELECT ct.credential_id FROM credential_teams ct
			JOIN team_members tm ON tm.team_id = ct.team_id WHERE tm.user_id = ?))`, []any{person.Int64}, nil
	case team.Valid:
		return "(c.everyone OR c.id IN (SELECT credential_id FROM credential_teams WHERE team_id = ?))", []any{team.Int64}, nil
	}
	return "c.everyone", nil, nil
}

// The credentials the flow may use, as its board offers them.
func flowCredentialChoices(ctx context.Context, db dbQuerier, flow int64) ([]credentialChoice, error) {
	cond, args, err := flowCredentials(ctx, db, flow)
	if err != nil {
		return nil, err
	}
	out := []credentialChoice{}
	err = scanAll(ctx, db, "SELECT c.id, c.name, c.variable, c.description, c.base_url FROM credentials c WHERE "+cond+" ORDER BY c.name", func(rows *sql.Rows) error {
		var c credentialChoice
		err := rows.Scan(&c.ID, &c.Name, &c.Variable, &c.Description, &c.BaseURL)
		out = append(out, c)
		return err
	}, args...)
	return out, err
}

// A credential a step uses, as the worker gets it: the variable holding
// its URL at the proxy, and, for the agent's prompt and the worker's log,
// its name and the service's base URL.
type credentialAccess struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
	Env     string `json:"env"`
	URL     string `json:"url"`
	id      int64
}

// The credentials a process picked, for its step, if its flow may use them
// all and each has its secret; else why not.
func stepCredentials(ctx context.Context, db querier, flow int64, ids []int64) ([]credentialAccess, string, error) {
	if len(ids) == 0 {
		return nil, "", nil
	}
	cond, args, err := flowCredentials(ctx, db, flow)
	if err != nil {
		return nil, "", err
	}
	var out []credentialAccess
	for _, id := range ids {
		if slices.ContainsFunc(out, func(c credentialAccess) bool { return c.id == id }) {
			continue
		}
		c := credentialAccess{id: id}
		var variable string
		var secretSet, usable bool
		err := db.QueryRowContext(ctx, "SELECT c.name, c.variable, c.base_url, c.secret <> '', "+cond+" FROM credentials c WHERE c.id = ?",
			append(args, id)...).Scan(&c.Name, &variable, &c.BaseURL, &secretSet, &usable)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, "a credential the process uses was removed; pick its credentials again", nil
		case err != nil:
			return nil, "", err
		case !usable:
			// Not named: whoever reads the step may not see it.
			return nil, "a credential the process uses isn't one this flow may use: it is neither everyone's nor given to its owner's team; an admin gives it in Settings, under Credentials, or pick its credentials again", nil
		case !secretSet:
			return nil, fmt.Sprintf("the credential %s has no secret; an admin sets it in Settings, under Credentials", c.Name), nil
		}
		c.Env = credentialEnv(variable)
		out = append(out, c)
	}
	return out, "", nil
}

// Tells an agent the services it calls through the proxy, and how.
func withCredentials(prompt string, creds []credentialAccess) string {
	if len(creds) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	fmt.Fprintf(&b, "\n\nYou call these services through Spoletta's credential proxy, which signs each request with the service's credential, so you never see it. Reach each at the URL its variable holds in place of the service's own, sending the header `Authorization: Bearer $CREDENTIALS_TOKEN`, such as `curl -H \"Authorization: Bearer $CREDENTIALS_TOKEN\" \"$%s/...\"`. Always write the variables, never their values, which change with every step.\n", creds[0].Env)
	for _, c := range creds {
		fmt.Fprintf(&b, "- %s, %s: $%s\n", c.Name, c.BaseURL, c.Env)
	}
	return b.String()
}

// What a credential grant reaches, kept with it: the credentials its
// step's process picked, and the flow whose owner must still be allowed
// them.
type credentialReaches struct {
	Credentials []int64 `json:"credentials"`
	Flow        int64   `json:"flow"`
}

type credentialBroker struct {
	db       *sql.DB
	proxyURL string
	grants   grantStore
	// Calls services; the one dialling the machine's own addresses only
	// for a credential whose base URL names them.
	client, loopback *http.Client

	mu sync.Mutex
	// Ends each token this replica granted as it expires.
	expiries map[string]*time.Timer
}

// The broker steps' credentials go through; set as the webapp starts.
var credentialProxy *credentialBroker

func newCredentialBroker(db *sql.DB, proxyURL string) *credentialBroker {
	return &credentialBroker{db: db, proxyURL: strings.TrimSuffix(proxyURL, "/"), grants: grantsIn(db),
		client: credentialClient(false), loopback: credentialClient(true), expiries: map[string]*time.Timer{}}
}

// How long a call through the proxy may take, and wait for the service's
// answer to start; the most a request may send.
const (
	credentialCallTimeout   = 10 * time.Minute
	credentialAnswerTimeout = 2 * time.Minute
	maxCredentialRequest    = 64 << 20
)

// The client the proxy calls services with: it never follows a redirect,
// never dials a link-local or multicast address, such as the cloud's
// metadata service, whatever a name resolves to, nor the machine's own
// unless loopback, for a base URL naming it; and never asks for
// compression of its own, so what comes back can be read for the secret.
func credentialClient(loopback bool) *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, Control: refusePrivate(true, loopback)}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: credentialAnswerTimeout,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          100,
			ForceAttemptHTTP2:     true,
			DisableCompression:    true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Grants the step a token reaching the credentials, with their URLs at the
// proxy filled in, for ttl at most; none for none. The flow is the
// process's, whose owner must be allowed each as each call is made.
func (b *credentialBroker) grant(ctx context.Context, who *int64, run, step, flow int64, creds []credentialAccess, ttl time.Duration) (string, error) {
	if len(creds) == 0 {
		return "", nil
	}
	reaches := credentialReaches{Flow: flow}
	names := make([]string, len(creds))
	for i := range creds {
		creds[i].URL = fmt.Sprintf("%s/credentials/%d", b.proxyURL, creds[i].id)
		reaches.Credentials = append(reaches.Credentials, creds[i].id)
		names[i] = creds[i].Name
	}
	if ttl <= 0 {
		ttl = llmDefaultTTL
	}
	ttl += llmGrantMargin
	token := randomToken()
	if err := b.grants.put(ctx, grantCredential, token, proxyGrant{user: who, run: run, step: step}, reaches, ttl); err != nil {
		return "", err
	}
	b.mu.Lock()
	b.expiries[token] = time.AfterFunc(ttl, func() { b.revokeToken(token, "expired") })
	b.mu.Unlock()
	audit("credential.grant", who, run, step, "the credential proxy, reaching %s, for %s at most", strings.Join(names, ", "), ttl.Round(time.Second))
	return token, nil
}

// Ends the step's token; nothing reaches a service with it after.
func (b *credentialBroker) revoke(token string) {
	if token != "" {
		b.revokeToken(token, "ended with its step")
	}
}

func (b *credentialBroker) revokeToken(token, why string) {
	b.mu.Lock()
	if t, ok := b.expiries[token]; ok {
		t.Stop()
		delete(b.expiries, token)
	}
	b.mu.Unlock()
	if g, ok := b.grants.remove(grantCredential, token); ok {
		auditGrantEnd(grantCredential, g, why)
	}
}

// Grants the step of a run its process's credentials, which the caller
// revokes once it is over; the credentials with their URLs, and the token.
func grantCredentials(ctx context.Context, who *int64, run, step, flow int64, creds []credentialAccess, ttl time.Duration) ([]credentialAccess, string, error) {
	if credentialProxy == nil || len(creds) == 0 {
		return nil, "", nil
	}
	token, err := credentialProxy.grant(ctx, who, run, step, flow, creds, ttl)
	return creds, token, err
}

// The methods the proxy passes on. CONNECT never reaches it: the egress
// proxy takes it.
var credentialMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

func registerCredentialProxy(mux *http.ServeMux, b *credentialBroker) {
	for _, m := range credentialMethods {
		mux.HandleFunc(m+" /credentials/{id}", b.proxy)
		mux.HandleFunc(m+" /credentials/{id}/{path...}", b.proxy)
	}
}

// The token a call to the proxy carries: as a bearer token, or as basic
// auth's password, whatever its username.
func credentialTokenOf(r *http.Request) string {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	if _, pass, ok := r.BasicAuth(); ok {
		return pass
	}
	return ""
}

// Headers of the step's request that go on to every service: what says
// what the request sends and what it takes, conditions on what is there,
// and what many APIs ask a write for. The rest stay here unless the
// credential names them: the step's token, its cookies, the connection's,
// what says where the call is meant for or which method it is, and Range,
// which would cut the secret apart where the answer is read for it, and
// Accept-Charset, which would have it written in bytes its forms aren't
// (as would an Accept's charset, which passedHeaders takes out).
var credentialPassedHeaders = []string{"Accept", "Accept-Language", "Cache-Control",
	"Content-Disposition", "Content-Encoding", "Content-Language", "Content-Type", "Idempotency-Key",
	"If-Match", "If-Modified-Since", "If-None-Match", "If-Unmodified-Since", "Pragma", "Prefer", "User-Agent"}

// Headers a credential can't let steps send: those that carry the step's
// token or are HTTP's own, those having the answer encoded or written in
// a charset the secret can't be found in, and those some servers and
// gateways take as another URL, host, method or client to answer for, as
// do those starting with Proxy-, X-Forwarded-, X-Original- and X-Envoy-.
var unpassableHeaders = []string{"Authorization", "Cookie", "Host", "Connection", "Keep-Alive", "Te", "Trailer",
	"Transfer-Encoding", "Upgrade", "Content-Length", "Range", "If-Range", "Accept-Encoding", "Accept-Charset", "Forwarded",
	"X-Host", "X-Http-Host", "X-Real-Ip", "X-Rewrite-Url", "X-Http-Method-Override", "X-Http-Method", "X-Method-Override",
	"X-Client-Ip", "True-Client-Ip", "Client-Ip", "X-Cluster-Client-Ip", "Cf-Connecting-Ip", "Fastly-Client-Ip"}

// A header's name as it is compared: canonical, with _ read as -, as nginx
// and CGI read it.
func headerKey(h string) string { return http.CanonicalHeaderKey(strings.ReplaceAll(h, "_", "-")) }

// Whether a credential may let steps send a header.
func passableHeader(h string) bool {
	h = headerKey(h)
	return !slices.Contains(unpassableHeaders, h) && !slices.ContainsFunc([]string{"Proxy-", "X-Forwarded-", "X-Original-", "X-Envoy-"},
		func(p string) bool { return strings.HasPrefix(h, p) })
}

// The headers of the step's request that go on to the credential's service:
// those every service gets and those the credential names, but for any the
// request's Connection names, which are its connection's.
func passedHeaders(c credential, h http.Header) http.Header {
	hop := map[string]bool{}
	for _, v := range h.Values("Connection") {
		for _, name := range strings.Split(v, ",") {
			hop[headerKey(strings.TrimSpace(name))] = true
		}
	}
	out := http.Header{}
	for k, v := range h {
		k = http.CanonicalHeaderKey(k)
		if hop[headerKey(k)] || !passableHeader(k) || c.Auth == credentialHeader && headerKey(k) == headerKey(c.Header) {
			continue
		}
		if slices.Contains(credentialPassedHeaders, k) || slices.Contains(c.Headers, k) {
			out[k] = v
		}
	}
	if v, ok := out["Accept"]; ok {
		out["Accept"] = withoutCharset(v)
	}
	return out
}

// An Accept header's values without the charset any media range asks for,
// which some services answer in (Spring and Jackson do), keeping the rest
// of each range's parameters.
func withoutCharset(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		ranges := strings.Split(v, ",")
		for j, media := range ranges {
			params := strings.Split(media, ";")
			params = append(params[:1], slices.DeleteFunc(params[1:], func(p string) bool {
				name, _, _ := strings.Cut(p, "=")
				return strings.EqualFold(strings.TrimSpace(name), "charset")
			})...)
			ranges[j] = strings.Join(params, ";")
		}
		out[i] = strings.Join(ranges, ",")
	}
	return out
}

// Headers of the service's answer that come back to the step: what says
// what the answer is, how long it may be kept and whether it changed,
// which methods are allowed, when to call again and how many calls are
// left, ids to tell the service's people about a call, and pages' counts;
// Location and Link come back as location makes them. The rest stay here:
// what could carry a credential, or a session the step could go on with
// once its token ended (Set-Cookie, WWW-Authenticate); what has a browser
// opening the answer act on the webapp's origin (Clear-Site-Data, Refresh,
// Service-Worker-Allowed, Access-Control-*, Alt-Svc); the connection's;
// and what says how long the answer is, how it is encoded or what its
// digest is, which reading it uncompressed and blanking out the secret
// change.
var credentialAnswerHeaders = []string{"Accept-Patch", "Age", "Allow", "Cache-Control", "Content-Disposition",
	"Content-Language", "Content-Type", "Date", "Deprecation", "Etag", "Expires", "Last-Modified", "Pragma", "Request-Id",
	"Retry-After", "Sunset", "Vary", "X-Correlation-Id", "X-Next-Page", "X-Page", "X-Per-Page", "X-Prev-Page",
	"X-Request-Id", "X-Total", "X-Total-Count", "X-Total-Pages"}

// Whether a header of the service's answer comes back to the step, by its
// canonical name: one of credentialAnswerHeaders, or saying how many calls
// are left (RateLimit-*, X-RateLimit-*), but never the credential's own.
func answerHeader(c credential, k string) bool {
	if c.Auth == credentialHeader && headerKey(k) == headerKey(c.Header) {
		return false
	}
	return slices.Contains(credentialAnswerHeaders, k) || strings.HasPrefix(k, "Ratelimit") || strings.HasPrefix(k, "X-Ratelimit-")
}

// The forms a secret goes on a request in, and those a service echoing it
// could write it in (secretEncodings), which an answer must never hold.
func secretForms(c credential, secret string) []string {
	forms := secretEncodings(secret)
	switch c.Auth {
	case credentialHeader:
		forms = append(forms, strings.ReplaceAll(c.Template, secretPlaceholder, secret))
	case credentialBasic:
		forms = append(forms, base64.StdEncoding.EncodeToString([]byte(c.Username+":"+secret)))
	}
	slices.Sort(forms)
	return slices.Compact(forms)
}

// HTML's escapes as some write them: Go's html.EscapeString writes &#34;
// and &#39;, others &quot; and &#x27;.
var htmlQuoted = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")

// The ways a service could write a secret alone: as it is, URL-encoded
// either way, in a JSON string with HTML's characters escaped or not and
// slashes escaped or not, HTML-escaped, and in base64, either alphabet,
// on its own or within something longer (base64Within). Each is matched
// in any case (redactor), so %2f and < are too.
func secretEncodings(secret string) []string {
	forms := []string{secret, url.QueryEscape(secret), url.PathEscape(secret), html.EscapeString(secret), htmlQuoted.Replace(secret)}
	for _, escapeHTML := range []bool{true, false} {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(escapeHTML)
		enc.Encode(secret)
		inJSON := strings.TrimSuffix(b.String(), "\n")
		inJSON = inJSON[1 : len(inJSON)-1]
		forms = append(forms, inJSON, strings.ReplaceAll(inJSON, "/", `\/`))
		if !escapeHTML {
			forms = append(forms, nonASCII(secret, inJSON)...)
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
		forms = append(forms, enc.EncodeToString([]byte(secret)))
	}
	forms = append(forms, base64Within(secret)...)
	slices.Sort(forms)
	return slices.Compact(forms)
}

// The ways a secret with characters outside ASCII could be written but as
// UTF-8: in ISO-8859-1, an answer's charset the proxy takes, if each of
// them is in it, and in a JSON string (inJSON, as Go writes it) escaping
// each as \uXXXX, as Python's json does.
func nonASCII(secret, inJSON string) []string {
	if !strings.ContainsFunc(secret, func(r rune) bool { return r >= 0x80 }) {
		return nil
	}
	var forms []string
	if !strings.ContainsFunc(secret, func(r rune) bool { return r > 0xff }) {
		latin1 := make([]byte, 0, len(secret))
		for _, r := range secret {
			latin1 = append(latin1, byte(r))
		}
		forms = append(forms, string(latin1))
	}
	var escaped strings.Builder
	for _, r := range inJSON {
		switch {
		case r < 0x80:
			escaped.WriteRune(r)
		case r > 0xffff:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&escaped, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(&escaped, `\u%04x`, r)
		}
	}
	return append(forms, escaped.String())
}

// What of s's base64 shows wherever s is encoded, whatever comes before or
// after it, as in basic auth's user:secret: for each of the three places
// s can start at in a group of three bytes, the characters its own whole
// groups make, in either alphabet; none shorter than a piece
// (secretPiece), which could be anybody's.
func base64Within(s string) []string {
	var out []string
	for skip := 0; skip < 3 && skip < len(s); skip++ {
		rest := s[skip:]
		rest = rest[:len(rest)/3*3]
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			if e := enc.EncodeToString([]byte(rest)); len(e) >= secretPiece {
				out = append(out, e)
			}
		}
	}
	return out
}

// What blanks a credential's secret out of its service's answer, headers
// and body: each of its forms whole, and any piece of one written alone
// (secretEncodings), so an echo cut short, at either end, keeps fewer than
// secretPiece of its characters in a row.
func credentialRedactor(c credential, secret string) *redactor {
	return newRedactor(secretForms(c, secret)...).withPieces(secretEncodings(secret)...)
}

// The forms a step's tokens could be printed in: as they are, in base64
// within something longer (a curl -v printing Proxy-Authorization: Basic
// with a username of the step's making), and as basic auth's user:token
// for each username steps are given.
func tokenForms(tokens []string, usernames ...string) []string {
	var forms []string
	for _, t := range tokens {
		if t == "" {
			continue
		}
		forms = append(forms, t)
		forms = append(forms, base64Within(t)...)
		for _, u := range usernames {
			forms = append(forms, base64.StdEncoding.EncodeToString([]byte(u+":"+t)))
		}
	}
	return forms
}

// What stands for a secret or token taken out of what a step gets or
// what is kept of it.
const redacted = "[redacted]"

// The fewest characters in a row of a secret's that are blanked out where
// they show up alone, as at the cut of an echo cut short: fewer say little
// of a long secret, and more would let more through.
const secretPiece = 8

// Takes secrets out of what streams through it, a piece at a time, as
// they are or in any case of their ASCII letters: each piece goes on with
// every secret in it blanked out, but for a tail that could be the start
// of one, held back until the next piece says, so a secret split across
// pieces is caught too. Given the forms a service's secret could be
// written in alone (withPieces), it blanks out any secretPiece characters
// in a row of them as well. A run of what is blanked out is written once.
type redactor struct {
	// Each in lower case.
	secrets [][]byte
	longest int
	pieces  map[[secretPiece]byte]bool
	// What a piece starts with, shorter than one, which a tail waits on.
	starts map[string]bool

	held []byte
	// How many of held's first bytes are blanked out already, by a secret
	// or piece starting before them; and whether what went on last was.
	heldBlank int
	endsBlank bool
}

func newRedactor(secrets ...string) *redactor {
	r := &redactor{}
	for _, s := range secrets {
		f := lowerASCII([]byte(s))
		if len(f) > 0 && !slices.ContainsFunc(r.secrets, func(b []byte) bool { return bytes.Equal(b, f) }) {
			r.secrets = append(r.secrets, f)
			r.longest = max(r.longest, len(f))
		}
	}
	return r
}

// Has the redactor blank out any secretPiece characters in a row of the
// forms too.
func (r *redactor) withPieces(forms ...string) *redactor {
	for _, f := range forms {
		b := lowerASCII([]byte(f))
		for i := 0; i+secretPiece <= len(b); i++ {
			if r.pieces == nil {
				r.pieces, r.starts = map[[secretPiece]byte]bool{}, map[string]bool{}
			}
			r.pieces[[secretPiece]byte(b[i:i+secretPiece])] = true
			for k := 1; k < secretPiece; k++ {
				r.starts[string(b[i:i+k])] = true
			}
		}
	}
	return r
}

// b with its ASCII letters in lower case, the same length.
func lowerASCII(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

func (r *redactor) blanks() bool { return len(r.secrets) > 0 || len(r.pieces) > 0 }

// Which bytes of a text, given in lower case, are a secret's or a piece of
// one; the first pre are already.
func (r *redactor) marks(lower []byte, pre int) []bool {
	m := make([]bool, len(lower))
	for i := range min(pre, len(lower)) {
		m[i] = true
	}
	mark := func(from, to int) {
		for i := from; i < to; i++ {
			m[i] = true
		}
	}
	for _, s := range r.secrets {
		for at := 0; ; {
			i := bytes.Index(lower[at:], s)
			if i < 0 {
				break
			}
			mark(at+i, at+i+len(s))
			at += i + 1
		}
	}
	if len(r.pieces) > 0 {
		for i := 0; i+secretPiece <= len(lower); i++ {
			if r.pieces[[secretPiece]byte(lower[i:i+secretPiece])] {
				mark(i, i+secretPiece)
			}
		}
	}
	return m
}

// b as it goes on, each run of what m marks written as redacted once; one
// going on from what went on before it, which continuing says ended
// blanked out, not again. Also whether it ends blanked out.
func blankOut(b []byte, m []bool, continuing bool) ([]byte, bool) {
	out := make([]byte, 0, len(b))
	ends := continuing
	for i := 0; i < len(b); i++ {
		if !m[i] {
			out = append(out, b[i])
			ends = false
			continue
		}
		if !ends {
			out = append(out, redacted...)
		}
		ends = true
	}
	return out, ends
}

// How many of the last bytes of a text, given in lower case, more could
// make part of a secret, or of a piece of one, so they wait for it: the
// longest tail that starts one. Any other goes on at once, so what streams
// through, such as the blank line ending an event, isn't kept waiting for
// the next.
func (r *redactor) tail(lower []byte) int {
	keep := 0
	for k := min(secretPiece-1, len(lower)); k > 0 && len(r.starts) > 0; k-- {
		if r.starts[string(lower[len(lower)-k:])] {
			keep = k
			break
		}
	}
	for k := min(r.longest-1, len(lower)); k > keep; k-- {
		if slices.ContainsFunc(r.secrets, func(s []byte) bool { return bytes.HasPrefix(s, lower[len(lower)-k:]) }) {
			return k
		}
	}
	return keep
}

// What of p, after what was held back, may go on.
func (r *redactor) next(p []byte) []byte {
	if !r.blanks() {
		return p
	}
	buf := append(r.held, p...)
	lower := lowerASCII(buf)
	m := r.marks(lower, r.heldBlank)
	cut := len(buf) - r.tail(lower)
	out, ends := blankOut(buf[:cut], m[:cut], r.endsBlank)
	r.endsBlank = ends
	r.heldBlank = 0
	for cut+r.heldBlank < len(buf) && m[cut+r.heldBlank] {
		r.heldBlank++
	}
	r.held = slices.Clone(buf[cut:])
	return out
}

// What was held back, once nothing more comes: never a whole secret, nor
// a piece of one.
func (r *redactor) flush() []byte {
	m := r.marks(lowerASCII(r.held), r.heldBlank)
	out, _ := blankOut(r.held, m, r.endsBlank)
	r.held, r.heldBlank, r.endsBlank = nil, 0, false
	return out
}

// The text with every secret, and piece of one, blanked out.
func (r *redactor) text(s string) string {
	if !r.blanks() {
		return s
	}
	b := []byte(s)
	out, _ := blankOut(b, r.marks(lowerASCII(b), 0), false)
	return string(out)
}

// Whether s holds a secret, or a piece of one.
func (r *redactor) holds(s string) bool {
	return slices.Contains(r.marks(lowerASCII([]byte(s)), 0), true)
}

// The URL a call at rest, what follows /credentials/{id} in its path,
// goes to: rest under the base URL's path, with the query; false when rest
// would leave it. rest comes decoded, so a step's %2F..%2F would step up
// out of the base URL, %3F start a query of its making, and %25 hide
// either from a service that decodes once more; escaped is the path as
// the step sent it, to find encoded slashes in. Some servers read more as
// a step up: Tomcat, Jetty and Spring drop what follows a ; in a segment
// (..;x=1), IIS trims a segment's trailing dots and spaces (".. "), and
// normalising Unicode makes full-width dots (．．) plain ones, and Windows
// reads what follows a : as a stream of the file before it (..:x). So no ;
// or non-ASCII character is taken, nor a segment of only dots and spaces
// before its first :, if any.
func credentialTarget(base, rest, escaped, query string) (*url.URL, bool) {
	lower := strings.ToLower(escaped)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return nil, false
	}
	if (rest != "" && !strings.HasPrefix(rest, "/")) || strings.ContainsAny(rest, `%?#\;`) || strings.Contains(rest, "//") {
		return nil, false
	}
	for _, r := range rest {
		if r < 0x20 || r >= 0x7f {
			return nil, false
		}
	}
	for _, segment := range strings.Split(rest, "/") {
		if head, _, _ := strings.Cut(segment, ":"); head != "" && strings.Trim(head, ". ") == "" {
			return nil, false
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, false
	}
	if rest != "" {
		// Set as a path, never as text read as a URL again.
		u.Path = strings.TrimSuffix(u.Path, "/") + rest
		u.RawPath = ""
	}
	u.RawQuery = query
	return u, true
}

// The Location of a service's redirect, or a URL of its Link header, as
// the step gets it: within the base URL, the proxy's URL for it, so
// following it goes through the proxy again; elsewhere, the service's,
// without the secret's query parameter. False when it would still hold
// the secret, or a piece of it, so it is dropped.
func (b *credentialBroker) location(c credential, at *url.URL, loc string, blank *redactor) (string, bool) {
	u, err := at.Parse(loc)
	if err != nil {
		return "", false
	}
	if c.Auth == credentialQuery {
		if q := u.Query(); q.Has(c.Param) {
			q.Del(c.Param)
			u.RawQuery = q.Encode()
		}
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return "", false
	}
	basePath := strings.TrimSuffix(base.Path, "/")
	if strings.EqualFold(u.Scheme, base.Scheme) && strings.EqualFold(u.Host, base.Host) &&
		(u.Path == basePath || strings.HasPrefix(u.Path, basePath+"/")) {
		rest := &url.URL{Path: strings.TrimPrefix(u.Path, basePath)}
		out := fmt.Sprintf("%s/credentials/%d%s", b.proxyURL, c.ID, rest.EscapedPath())
		if u.RawQuery != "" {
			out += "?" + u.RawQuery
		}
		if u.Fragment != "" {
			out += "#" + u.EscapedFragment()
		}
		loc = out
	} else {
		loc = u.String()
	}
	if decoded, err := url.PathUnescape(loc); blank.holds(loc) || err == nil && blank.holds(decoded) {
		return "", false
	}
	return loc, true
}

// The URLs in a Link header's value, such as the next page's.
var linkURL = regexp.MustCompile(`<[^>]*>`)

// A Link header's value as the step gets it, each URL in it as location
// makes it; false when it would still hold the secret, so it is dropped.
func (b *credentialBroker) link(c credential, at *url.URL, v string, blank *redactor) (string, bool) {
	ok := true
	out := linkURL.ReplaceAllStringFunc(v, func(m string) string {
		u, fine := b.location(c, at, m[1:len(m)-1], blank)
		ok = ok && fine
		return "<" + u + ">"
	})
	return out, ok && !blank.holds(out)
}

// The step's query with the credential's parameter, the secret, put first,
// so a step can't pad what comes before it in the query to have a service
// echoing it cut short cut the secret; what a step pads the path with
// before it, the redactor keeps to a few characters. The step's own
// values of the parameter are left out, as are pairs that don't parse,
// which a service might read for it.
func secretFirst(param, secret, raw string) string {
	out := url.QueryEscape(param) + "=" + url.QueryEscape(secret)
	for _, pair := range strings.Split(raw, "&") {
		if q, err := url.ParseQuery(pair); pair != "" && err == nil && !q.Has(param) {
			out += "&" + pair
		}
	}
	return out
}

// A call refused, which is audited, and logged in the run's log when its
// grant is known.
func (b *credentialBroker) refuse(w http.ResponseWriter, g *proxyGrant, status int, msg, format string, args ...any) {
	if g != nil {
		audit("credential.refused", g.user, g.run, g.step, format, args...)
		if g.run != 0 && b.db != nil {
			runLog(b.db, g.run, g.step, logError, "a call through the credential proxy was refused: "+format, args...)
		}
	} else {
		audit("credential.refused", nil, 0, 0, format, args...)
	}
	http.Error(w, msg, status)
}

// Passes a step's call on to the credential's service with its secret.
func (b *credentialBroker) proxy(w http.ResponseWriter, r *http.Request) {
	notAvailable := "this credential isn't available to this step"
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, notAvailable, http.StatusForbidden)
		return
	}
	token := credentialTokenOf(r)
	var reaches credentialReaches
	pg, granted, err := b.grants.lookup(r.Context(), grantCredential, token, &reaches)
	if err != nil {
		serverError(w, err)
		return
	}
	if !granted {
		if token != "" {
			b.refuse(w, nil, http.StatusForbidden, notAvailable, "a call to the credential %d with a token that isn't granted, or ended", id)
		} else {
			http.Error(w, notAvailable+": send the step's $CREDENTIALS_TOKEN as Authorization: Bearer", http.StatusUnauthorized)
		}
		return
	}
	g := &pg
	if !slices.Contains(reaches.Credentials, id) {
		b.refuse(w, g, http.StatusForbidden, notAvailable, "a call to the credential %d, which the step's process didn't pick", id)
		return
	}
	cond, args, err := flowCredentials(r.Context(), b.db, reaches.Flow)
	if errors.Is(err, sql.ErrNoRows) {
		b.refuse(w, g, http.StatusForbidden, notAvailable, "a call to the credential %d from a flow since deleted", id)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	var usable bool
	c, stored, err := scanCredential(b.db.QueryRowContext(r.Context(), "SELECT "+credentialColumns+", "+cond+" FROM credentials c WHERE c.id = ?", append(args, id)...), &usable)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		b.refuse(w, g, http.StatusForbidden, notAvailable, "a call to the credential %d, which was removed", id)
		return
	case err != nil:
		serverError(w, err)
		return
	case !usable:
		b.refuse(w, g, http.StatusForbidden, notAvailable, "a call to the credential %s, which its flow may no longer use", c.Name)
		return
	}
	secret, err := secrets.open(credentialPlace(c.ID), stored)
	if err != nil {
		serverError(w, err)
		return
	}
	if secret == "" {
		b.refuse(w, g, http.StatusForbidden, "the credential "+c.Name+" has no secret", "a call to the credential %s, which has no secret", c.Name)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/credentials/"+r.PathValue("id"))
	query := r.URL.RawQuery
	if c.Auth == credentialQuery {
		query = secretFirst(c.Param, secret, query)
	}
	target, ok := credentialTarget(c.BaseURL, rest, r.URL.EscapedPath(), query)
	if !ok {
		b.refuse(w, g, http.StatusBadRequest, "the path isn't one under the service's base URL", "a call to the credential %s at a path outside its base URL", c.Name)
		return
	}
	logPath := truncate(rest, 500)
	if logPath == "" {
		logPath = "/"
	}
	if r.ContentLength > maxCredentialRequest {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var body io.Reader = http.NoBody
	if r.ContentLength != 0 {
		body = http.MaxBytesReader(w, r.Body, maxCredentialRequest)
	}
	ctx, cancel := context.WithTimeout(r.Context(), credentialCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), body)
	if err != nil {
		http.Error(w, "the service's URL: "+errorWithoutURL(err), http.StatusBadGateway)
		return
	}
	req.ContentLength = r.ContentLength
	req.Header = passedHeaders(c, r.Header)
	// What comes back is read for the secret, so it must come as it is.
	req.Header.Set("Accept-Encoding", "identity")
	switch c.Auth {
	case credentialHeader:
		req.Header.Set(c.Header, strings.ReplaceAll(c.Template, secretPlaceholder, secret))
	case credentialBasic:
		req.SetBasicAuth(c.Username, secret)
	}
	client := b.client
	if loopbackHost(target.Hostname()) {
		client = b.loopback
	}
	blank := credentialRedactor(c, secret)
	resp, err := client.Do(req)
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		b.logCall(g, c, "%s %s sent more than %d MB, so it went no further", r.Method, logPath, maxCredentialRequest>>20)
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		// Its URL may hold the secret, as a query parameter.
		why := errorWithoutURL(err)
		if blank.holds(why) {
			why = "the call failed"
		}
		b.logCall(g, c, "%s %s couldn't reach the service: %s", r.Method, logPath, why)
		http.Error(w, "couldn't reach the service of the credential "+c.Name+": "+why, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	// A service compressing what it was asked for as it is is read
	// uncompressed, if gzipped once; what else it is encoded in, however
	// many of the header's values say it, can't be read for the secret.
	var codings []string
	for _, v := range resp.Header.Values("Content-Encoding") {
		for _, enc := range strings.Split(v, ",") {
			if enc = strings.ToLower(strings.TrimSpace(enc)); enc != "" {
				codings = append(codings, enc)
			}
		}
	}
	answer := io.Reader(resp.Body)
	switch enc := strings.Join(codings, ", "); enc {
	case "", "identity":
	case "gzip", "x-gzip":
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			b.logCall(g, c, "%s %s answered %d, compressed so it couldn't be read", r.Method, logPath, resp.StatusCode)
			http.Error(w, "the service's answer couldn't be read: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer gz.Close()
		answer = &cappedReader{r: io.LimitReader(gz, maxCredentialAnswer+1), max: maxCredentialAnswer}
	default:
		enc = plainName(enc, "an encoding of its own", blank)
		b.logCall(g, c, "%s %s answered %d in %s, which the proxy can't read for the secret, so it went no further", r.Method, logPath, resp.StatusCode, enc)
		http.Error(w, "the service answered in "+enc+", which the proxy can't read for the credential's secret", http.StatusBadGateway)
		return
	}
	// In another charset, the secret is written in bytes none of its forms
	// are; so is an answer starting with UTF-16's or UTF-32's byte order
	// mark, whatever it says it is.
	head := bufio.NewReader(answer)
	answer = head
	charset, ok := answerCharset(resp.Header.Values("Content-Type"))
	if ok && startsWithBOM(head) {
		charset, ok = "UTF-16 or UTF-32", false
	}
	if !ok {
		charset = plainName(charset, "a charset of its own", blank)
		b.logCall(g, c, "%s %s answered %d in %s, which the proxy can't read for the secret, so it went no further", r.Method, logPath, resp.StatusCode, charset)
		http.Error(w, "the service answered in "+charset+", which the proxy can't read for the credential's secret; only UTF-8, US-ASCII and ISO-8859-1 are", http.StatusBadGateway)
		return
	}
	for k, v := range resp.Header {
		if k = http.CanonicalHeaderKey(k); answerHeader(c, k) && !slices.ContainsFunc(v, blank.holds) {
			w.Header()[k] = v
		}
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		if out, ok := b.location(c, target, loc, blank); ok {
			w.Header().Set("Location", out)
		}
	}
	for _, v := range resp.Header.Values("Link") {
		if out, ok := b.link(c, target, v, blank); ok {
			w.Header().Add("Link", out)
		}
	}
	// What a service answers may be HTML; sandboxed, it can't act as the app.
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(resp.StatusCode)
	b.logCall(g, c, "%s %s, answered %d", r.Method, logPath, resp.StatusCode)
	if err := streamAnswer(w, answer, blank); err != nil {
		why := "couldn't be read to its end"
		if errors.Is(err, errAnswerTooLarge) {
			why = fmt.Sprintf("came to more than %d MB uncompressed", maxCredentialAnswer>>20)
		}
		b.logCall(g, c, "%s %s: its answer %s, so the rest went no further", r.Method, logPath, why)
		// What went on of it ends broken, so the step can't take it for
		// the whole answer.
		panic(http.ErrAbortHandler)
	}
}

// The most a gzipped answer comes to uncompressed, past which the call
// fails: four times what a request may send.
const maxCredentialAnswer = 4 * maxCredentialRequest

var errAnswerTooLarge = errors.New("the answer is too large")

// Reads r, which a LimitReader keeps to one byte past max, failing past
// max.
type cappedReader struct {
	r         io.Reader
	read, max int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if c.read += int64(n); c.read > c.max {
		return 0, errAnswerTooLarge
	}
	return n, err
}

// Streams an answer on to the step with the secret blanked out of it,
// each piece as it comes; an error if it couldn't be read to its end,
// once the step has what went on of it, but for what was held back.
func streamAnswer(w io.Writer, answer io.Reader, blank *redactor) error {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := answer.Read(buf)
		if n > 0 {
			if _, werr := w.Write(blank.next(buf[:n])); werr != nil {
				// The step went away.
				return nil
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if errors.Is(err, io.EOF) {
			w.Write(blank.flush())
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// A name a service gave, such as an encoding's or a charset's, as the step
// is told it: as it is when plainly a name, else fallback.
func plainName(name, fallback string, blank *redactor) string {
	if name == "" || len(name) > 32 || blank.holds(name) || strings.ContainsFunc(name, func(r rune) bool { return r > 0x7e || r < 0x20 }) {
		return fallback
	}
	return name
}

// The charsets an answer may be written in, which the secret's forms are
// found in: UTF-8, and US-ASCII and ISO-8859-1, which write ASCII as it
// does (a secret outside ASCII has a form in ISO-8859-1 too).
var answerCharsets = []string{"utf-8", "us-ascii", "iso-8859-1"}

// The charset an answer's Content-Type says that isn't one of
// answerCharsets, if any, and false; a Content-Type naming a charset that
// can't be read is taken as one.
func answerCharset(types []string) (string, bool) {
	for _, t := range types {
		_, params, err := mime.ParseMediaType(t)
		if err != nil {
			if strings.Contains(strings.ToLower(t), "charset") {
				return "", false
			}
			continue
		}
		if cs, ok := params["charset"]; ok && !slices.Contains(answerCharsets, strings.ToLower(strings.TrimSpace(cs))) {
			return cs, false
		}
	}
	return "", true
}

// The byte order marks of UTF-16, big- and little-endian, and of UTF-32
// big-endian; UTF-32 little-endian's starts with UTF-16's.
var byteOrderMarks = [][]byte{{0xfe, 0xff}, {0xff, 0xfe}, {0x00, 0x00, 0xfe, 0xff}}

// Whether an answer starts with a byte order mark of UTF-16 or UTF-32,
// reading only as far as it still could, so an answer starting otherwise,
// such as an event, isn't kept waiting for more.
func startsWithBOM(r *bufio.Reader) bool {
	for n := 1; ; n++ {
		head, _ := r.Peek(n)
		if len(head) < n {
			return false
		}
		could := false
		for _, m := range byteOrderMarks {
			if bytes.HasPrefix(m, head) {
				if len(m) == n {
					return true
				}
				could = true
			}
		}
		if !could {
			return false
		}
	}
}

// Notes a call in the step's run's log, and the audit log.
func (b *credentialBroker) logCall(g *proxyGrant, c credential, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	audit("credential.call", g.user, g.run, g.step, "the credential %s: %s", c.Name, detail)
	if g.run == 0 || b.db == nil {
		log.Printf("credential %s of step %d: %s", c.Name, g.step, detail)
		return
	}
	runLog(b.db, g.run, g.step, logInfo, "the credential %s: %s", c.Name, detail)
}

// Whether a change would send the credential's kept secret elsewhere than
// it was given for: to another base URL, even at the same origin, which
// may be another's (script.google.com/macros/s/<id>), or in another header
// or parameter, or as basic auth where it was a header's.
func sendsElsewhere(was credential, f credentialFields) bool {
	return credentialBase(was.BaseURL) != credentialBase(f.BaseURL) || was.Auth != f.Auth ||
		!strings.EqualFold(was.Header, f.Header) || was.Param != f.Param
}

// A base URL as it is compared: its origin (credentialOrigin), then its
// path as written, / for none.
func credentialBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return credentialOrigin(raw) + path
}

// A URL's scheme, host and port, its port said even when it is the
// scheme's own.
func credentialOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// Gives the credential to the teams, in place of those it had.
func setCredentialTeams(ctx context.Context, tx *sql.Tx, id int64, teams []int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM credential_teams WHERE credential_id = ?", id); err != nil {
		return err
	}
	for _, t := range teams {
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO credential_teams (credential_id, team_id) VALUES (?, ?)", id, t); err != nil {
			return err
		}
	}
	return nil
}

// Who may use a credential, as the audit log says it.
func credentialUsers(f credentialFields) string {
	if f.Everyone {
		return "everyone"
	}
	return fmt.Sprintf("%d teams", len(f.Teams))
}

func registerCredentialRoutes(mux *http.ServeMux, db *sql.DB) {
	writeList := func(w http.ResponseWriter, r *http.Request, status int) {
		out, err := listCredentials(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, status, out)
	}
	taken := func(w http.ResponseWriter, name string) {
		http.Error(w, fmt.Sprintf("there is a credential named %s already, or one whose variable would be %s; name it apart", name, credentialEnv(credentialVariable(name))), http.StatusConflict)
	}
	// Saves what a request gives a credential, as a new one for id 0, in a
	// transaction, and says what went wrong with it that is the request's.
	save := func(w http.ResponseWriter, r *http.Request, id int64, f credentialFields) (int64, bool) {
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return 0, false
		}
		defer tx.Rollback()
		variable, headers := credentialVariable(f.Name), strings.Join(f.Headers, ",")
		if id == 0 {
			res, err := tx.ExecContext(r.Context(),
				`INSERT INTO credentials (name, variable, description, base_url, auth, header, template, username, param, headers, secret, everyone)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
				f.Name, variable, f.Description, f.BaseURL, f.Auth, f.Header, f.Template, f.Username, f.Param, headers, f.Everyone)
			if mysqlErrorIs(err, mysqlErrDuplicate) {
				taken(w, f.Name)
				return 0, false
			}
			if err == nil {
				id, err = res.LastInsertId()
			}
			if err != nil {
				serverError(w, err)
				return 0, false
			}
		} else {
			was, _, err := scanCredential(tx.QueryRowContext(r.Context(), "SELECT "+credentialColumns+" FROM credentials c WHERE c.id = ? FOR UPDATE", id))
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "credential not found", http.StatusNotFound)
				return 0, false
			}
			if err == nil && f.Secret == "" && sendsElsewhere(was, f) {
				http.Error(w, "give the secret again: it goes to another base URL, or another place on its requests, than it was given for", http.StatusBadRequest)
				return 0, false
			}
			if err == nil {
				_, err = tx.ExecContext(r.Context(),
					`UPDATE credentials SET name = ?, variable = ?, description = ?, base_url = ?, auth = ?, header = ?, template = ?, username = ?, param = ?, headers = ?, everyone = ?
					WHERE id = ?`,
					f.Name, variable, f.Description, f.BaseURL, f.Auth, f.Header, f.Template, f.Username, f.Param, headers, f.Everyone, id)
			}
			if mysqlErrorIs(err, mysqlErrDuplicate) {
				taken(w, f.Name)
				return 0, false
			}
			if err != nil {
				serverError(w, err)
				return 0, false
			}
		}
		// Sealed where it is kept, which the row's id says.
		if f.Secret != "" {
			stored, err := secrets.seal(credentialPlace(id), f.Secret)
			if err == nil {
				_, err = tx.ExecContext(r.Context(), "UPDATE credentials SET secret = ? WHERE id = ?", stored, id)
			}
			if err != nil {
				serverError(w, err)
				return 0, false
			}
		}
		err = setCredentialTeams(r.Context(), tx, id, f.Teams)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "no such team", http.StatusBadRequest)
			return 0, false
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			serverError(w, err)
			return 0, false
		}
		return id, true
	}
	decode := func(w http.ResponseWriter, r *http.Request, creating bool) (credentialFields, bool) {
		var f credentialFields
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return f, false
		}
		if msg := f.check(creating); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return f, false
		}
		return f, true
	}

	// Every credential, for admins; with ?flow=, the ones the flow may use,
	// which its board offers, for whoever sees the flow.
	mux.HandleFunc("GET /api/credentials", func(w http.ResponseWriter, r *http.Request) {
		if v := r.URL.Query().Get("flow"); v != "" {
			flow, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				http.Error(w, "invalid flow", http.StatusBadRequest)
				return
			}
			out, err := flowCredentialChoices(r.Context(), db, flow)
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "flow not found", http.StatusNotFound)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		if refuseKeys(w, r) {
			return
		}
		writeList(w, r, http.StatusOK)
	})

	// Adds one: {"name", "description", "baseUrl", "auth", "header",
	// "template", "username", "param", "secret", "everyone", "teams"}.
	mux.HandleFunc("POST /api/credentials", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		f, ok := decode(w, r, true)
		if !ok {
			return
		}
		if _, ok := save(w, r, 0, f); !ok {
			return
		}
		auditChange(r, "credential.add", "the credential %s, at %s, for %s", f.Name, f.BaseURL, credentialUsers(f))
		writeList(w, r, http.StatusCreated)
	})

	// Changes one as a request to add it would make it, keeping its secret
	// unless given another.
	mux.HandleFunc("PUT /api/credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		f, ok := decode(w, r, false)
		if !ok {
			return
		}
		if _, ok := save(w, r, id, f); !ok {
			return
		}
		secret := ""
		if f.Secret != "" {
			secret = ", with a new secret"
		}
		auditChange(r, "credential.change", "the credential %s, at %s, for %s%s", f.Name, f.BaseURL, credentialUsers(f), secret)
		writeList(w, r, http.StatusOK)
	})

	// Removes one. The processes picking it fail to run until they no
	// longer do, and steps going have their next calls refused.
	mux.HandleFunc("DELETE /api/credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var name string
		err := db.QueryRowContext(r.Context(), "SELECT name FROM credentials WHERE id = ?", r.PathValue("id")).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "credential not found", http.StatusNotFound)
			return
		}
		if err == nil {
			_, err = db.ExecContext(r.Context(), "DELETE FROM credentials WHERE id = ?", r.PathValue("id"))
		}
		if err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "credential.remove", "the credential %s", name)
		w.WriteHeader(http.StatusNoContent)
	})
}
