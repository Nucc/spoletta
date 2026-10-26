package main

// Webhooks: other systems' events starting a flow. Services sending events,
// GitHub, Slack, a monitoring alert, can't call the API (api.go): they send
// payloads of their own, signed their own way, without an API key. So those
// who may change a flow add webhooks to it, in its settings: each a URL of
// its own, /hooks/<token>, outside /api/, with a token nobody can guess, and
// a secret, generated or pasted (Slack's signing secret), sealed with
// SECRET_KEY (secrets.go) and never sent back but as it is made. Its kind
// says how a delivery proves it holds the secret:
//   - github: X-Hub-Signature-256, an HMAC-SHA256 of the body;
//   - slack: X-Slack-Signature, an HMAC-SHA256 of the request's timestamp
//     and body, refused when the timestamp is more than five minutes off,
//     so one sent again later isn't taken;
//   - generic: an HMAC-SHA256 of the body, in hex or base64, in a header it
//     names, or the secret itself in one, such as Authorization.
//
// The URL alone starts nothing, since every webhook has a secret, compared
// in constant time; what goes wrong never says more than that it didn't
// match. A GitHub webhook takes the events it names, each alone (issues) or
// with an action (issues.labeled); a Slack one answers Slack's URL
// verification and takes the Events API's callbacks, those it names by
// type if it names any.
//
// A delivery starts a run as the API does (startGivenRun), so it runs the
// version the flow published, if any, and what it gives is checked as a
// request's is: each object the flow starts from (flowInputs) is taken from
// the payload as the webhook's mapping says, from a dotted path into it
// (issue.title, pull_request.number, labels.0.name), the whole of it, or
// for an object with attributes a path for each. It is recorded first as a
// step of kind input, so re-runs and unique values work as for any run. A
// delivery is taken once: one sent again with the same id, GitHub's
// X-GitHub-Delivery, Slack's event id, or the header a generic webhook
// names, gets the run the first started, through the idempotency key the
// API's requests use, which the database keeps for every replica. One
// refused takes nothing, so it can be sent again once what refused it is
// put right. GitHub's and a generic HMAC sign the body alone, not the id,
// so whoever captures a signed delivery could send it again under another
// id; only Slack's timestamp bounds that. A flow that must not handle the
// same event twice gives what it starts from a unique attribute, which ends
// a second run as a duplicate (see api.go).
//
// A webhook's runs are for whoever added it (runUser), so their limits and
// access apply: once they are removed, or may no longer run the flow, its
// deliveries are refused. Without sign-in it is nobody's, and once people
// sign in it stops working, as API keys do.
//
// Each webhook keeps its latest deliveries, when, which event, what came of
// it and why, and the run it started; and the payload of the latest
// signed one, to write the mapping from. Every delivery goes in the audit
// log, never what it carried or the secret. The answer comes as soon as the
// run is recorded, before it goes on.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The kinds of webhook, by who sends its deliveries.
const (
	webhookGitHub  = "github"
	webhookSlack   = "slack"
	webhookGeneric = "generic"
)

// How a generic webhook's deliveries prove they hold the secret.
const (
	// An HMAC-SHA256 of the body, keyed with it.
	verifyHMAC = "hmac"
	// The secret itself.
	verifyToken = "token"
)

// What came of a delivery.
const (
	deliveryStarted = "started"
	// The same delivery came again, and got the run it started.
	deliveryRepeat  = "repeat"
	deliveryIgnored = "ignored"
	deliveryRefused = "refused"
)

// The limits on a webhook and its deliveries.
const (
	maxWebhookBody   = 1 << 20
	maxWebhookName   = 255
	maxWebhookEvents = 32
	maxWebhookInputs = 64
	maxWebhookPath   = 512
	// The shortest secret taken, so it can't be guessed either.
	minWebhookSecret = 16
	// How many deliveries each keeps, and the list shows.
	webhookDeliveriesKept = 100
	// How far a Slack request's timestamp may be from now.
	slackTolerance = 5 * time.Minute
)

// What a generated secret starts with, so it can be told apart from other
// secrets, as an API key is by its own.
const webhookSecretPrefix = "spwh_"

var (
	webhookEvent = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}(\.[A-Za-z0-9_-]{1,64})?$`)
	webhookToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Where an object the flow starts from is taken from in a delivery's
// payload.
type webhookInput struct {
	// A dotted path into it, such as issue.title; empty for the whole of it.
	Path string `json:"path"`
	// For an object with attributes, the path each is taken from instead,
	// by attribute.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// What each object the flow starts from is taken from, by name.
type webhookMapping map[string]webhookInput

func (m *webhookMapping) Scan(src any) error {
	b, ok := src.([]byte)
	if !ok {
		return fmt.Errorf("webhookMapping: unexpected %T", src)
	}
	return json.Unmarshal(b, m)
}

// A webhook as the flow's settings show it; its secret only as it is made
// or given a new one.
type webhook struct {
	ID   int64  `json:"id"`
	Flow int64  `json:"flow"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Where its deliveries go.
	URL string `json:"url"`
	// For a generic webhook: how a delivery proves it holds the secret, in
	// which header, and the header holding its id, if any.
	Verify   string `json:"verify,omitempty"`
	Header   string `json:"header,omitempty"`
	IDHeader string `json:"idHeader,omitempty"`
	// The events it takes; none for every one, but on GitHub.
	Events  []string       `json:"events"`
	Mapping webhookMapping `json:"mapping"`
	Enabled bool           `json:"enabled"`
	// Whom its runs are for.
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	// The secret's last four characters, when it is long enough for them
	// to say little.
	Hint string `json:"hint,omitempty"`
	// What keeps its deliveries from starting a run, or leaves part of its
	// mapping unused, as the flow stands.
	Problems     []string         `json:"problems"`
	LastDelivery *webhookDelivery `json:"lastDelivery"`
	Secret       string           `json:"secret,omitempty"`
}

// A delivery as the webhook lists it.
type webhookDelivery struct {
	ID         int64     `json:"id"`
	ReceivedAt time.Time `json:"receivedAt"`
	// Its id, as its sender gave it.
	Delivery string `json:"delivery"`
	Event    string `json:"event"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason"`
	Run      *int64 `json:"run"`
}

// An object the flow starts from, as the mapping is written for it.
type webhookFlowInput struct {
	Name       string   `json:"name"`
	Many       bool     `json:"many"`
	Attributes []string `json:"attributes"`
}

// What a request makes or changes a webhook to.
type webhookFields struct {
	Flow     int64          `json:"flow"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Verify   string         `json:"verify"`
	Header   string         `json:"header"`
	IDHeader string         `json:"idHeader"`
	Events   []string       `json:"events"`
	Mapping  webhookMapping `json:"mapping"`
	Enabled  *bool          `json:"enabled"`
	// A secret given, such as Slack's signing secret; none keeps the one
	// kept, or makes one for a new webhook or with NewSecret.
	Secret    string `json:"secret"`
	NewSecret bool   `json:"newSecret"`
}

// Tidies what a request gives and says what is wrong with it, if anything.
func (f *webhookFields) check(creating bool) string {
	f.Name = strings.TrimSpace(f.Name)
	f.Secret = strings.TrimSpace(f.Secret)
	f.Header, f.IDHeader = strings.TrimSpace(f.Header), strings.TrimSpace(f.IDHeader)
	switch {
	case f.Name == "" || len(f.Name) > maxWebhookName:
		return fmt.Sprintf("a webhook needs a name, of at most %d characters, saying what sends it", maxWebhookName)
	case f.Kind != webhookGitHub && f.Kind != webhookSlack && f.Kind != webhookGeneric:
		return "a webhook's kind is github, slack or generic"
	case f.Secret != "" && f.NewSecret:
		return "give a secret or ask for a new one, not both"
	case f.Secret != "" && (len(f.Secret) < minWebhookSecret || len(f.Secret) > maxCredentialSecret || strings.ContainsAny(f.Secret, "\r\n")):
		return fmt.Sprintf("the secret must be one line of %d to %d characters", minWebhookSecret, maxCredentialSecret)
	case creating && f.Kind == webhookSlack && f.Secret == "":
		return "a Slack webhook takes the app's signing secret, from its Basic Information page"
	}
	if f.Kind == webhookGeneric {
		switch {
		case f.Verify != verifyHMAC && f.Verify != verifyToken:
			return "a generic webhook checks an HMAC of the body (hmac) or the secret itself (token) in a header"
		case !headerName.MatchString(f.Header) || slices.ContainsFunc(reservedHeaders, func(h string) bool { return strings.EqualFold(h, f.Header) }):
			return "name the header the signature or the secret comes in, such as X-Signature"
		case f.IDHeader != "" && !headerName.MatchString(f.IDHeader):
			return "the header holding a delivery's id isn't a header's name"
		}
	} else {
		f.Verify, f.Header, f.IDHeader = "", "", ""
	}
	var events []string
	for _, e := range f.Events {
		if e = strings.TrimSpace(e); e == "" || slices.Contains(events, e) {
			continue
		}
		if !webhookEvent.MatchString(e) {
			return fmt.Sprintf("%s isn't an event, such as issues or issues.labeled", quote(e))
		}
		events = append(events, e)
	}
	f.Events = events
	switch {
	case len(f.Events) > maxWebhookEvents:
		return fmt.Sprintf("a webhook takes at most %d events", maxWebhookEvents)
	case f.Kind == webhookGitHub && len(f.Events) == 0:
		return "name the events it takes, such as issues or pull_request.opened, as GitHub sends every one it is asked for"
	case f.Kind == webhookGeneric && len(f.Events) > 0:
		return "a generic webhook takes every delivery; it names no events"
	}
	if f.Mapping == nil {
		f.Mapping = webhookMapping{}
	}
	if len(f.Mapping) > maxWebhookInputs {
		return fmt.Sprintf("a mapping gives at most %d objects", maxWebhookInputs)
	}
	for name, in := range f.Mapping {
		if strings.TrimSpace(name) == "" || len(name) > maxWebhookName {
			return "each part of the mapping names an object the flow starts from"
		}
		if msg := checkPayloadPath(in.Path); msg != "" {
			return fmt.Sprintf("%s: %s", quote(name), msg)
		}
		if len(in.Attributes) > maxWebhookInputs {
			return fmt.Sprintf("%s: at most %d attributes", quote(name), maxWebhookInputs)
		}
		if len(in.Attributes) > 0 && in.Path != "" {
			return fmt.Sprintf("%s is taken from a path or from a path for each attribute, not both", quote(name))
		}
		for attr, p := range in.Attributes {
			if msg := checkPayloadPath(p); attr == "" || p == "" || msg != "" {
				return fmt.Sprintf("%s: each attribute is taken from a path into the payload, such as issue.title", quote(name))
			}
		}
	}
	return ""
}

// Why a path into a payload isn't one, or "": dotted keys, or an array's
// item's index, none empty; empty for the whole payload.
func checkPayloadPath(p string) string {
	if p == "" {
		return ""
	}
	if len(p) > maxWebhookPath || strings.ContainsAny(p, "\x00\r\n") || slices.Contains(strings.Split(p, "."), "") {
		return fmt.Sprintf("%s isn't a path into the payload, such as issue.title", quote(p))
	}
	return ""
}

// The value at a dotted path into a payload, each part a key of an object
// or the index of an array's item; the whole payload for an empty path.
func payloadAt(payload any, path string) (any, bool) {
	if path == "" {
		return payload, true
	}
	v := payload
	for _, part := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			next, ok := x[part]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// What a request would give of the objects the flow starts from, taken
// from the payload as the mapping says, or what keeps it from being made.
// Objects the flow doesn't start from are left out; one it does that the
// mapping doesn't give, or a path the payload doesn't have, refuses it.
// givenValues checks the rest, as it does a request's.
func (m webhookMapping) request(in []givenObject, payload any) (apiRunRequest, error) {
	req := apiRunRequest{Inputs: map[string]json.RawMessage{}}
	at := func(path string) (json.RawMessage, error) {
		v, ok := payloadAt(payload, path)
		if !ok {
			return nil, fmt.Errorf("the payload has no %s", path)
		}
		return json.Marshal(v)
	}
	for _, g := range in {
		src, ok := m[g.name]
		if !ok {
			return req, fmt.Errorf("the webhook's mapping gives nothing for %s, which the flow starts from", quote(g.name))
		}
		if len(src.Attributes) == 0 {
			raw, err := at(src.Path)
			if err != nil {
				return req, fmt.Errorf("%s: %w", quote(g.name), err)
			}
			req.Inputs[g.name] = raw
			continue
		}
		fields := map[string]json.RawMessage{}
		for attr, path := range src.Attributes {
			raw, err := at(path)
			if err != nil {
				return req, fmt.Errorf("%s's %s: %w", quote(g.name), attr, err)
			}
			fields[attr] = raw
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return req, err
		}
		req.Inputs[g.name] = raw
	}
	return req, nil
}

// What keeps a delivery from starting a run, or leaves part of the mapping
// unused, as the flow starts from in.
func (m webhookMapping) problems(in []givenObject) []string {
	var out []string
	for _, g := range in {
		src, ok := m[g.name]
		if !ok {
			out = append(out, fmt.Sprintf("the mapping gives nothing for %s, which the flow starts from, so deliveries are refused", quote(g.name)))
			continue
		}
		if len(src.Attributes) == 0 {
			continue
		}
		if g.many || len(g.fields) == 0 {
			what := "an object without attributes"
			if g.many {
				what = "a list"
			}
			out = append(out, fmt.Sprintf("%s is given a path for each attribute, but it is %s, taken from one path, so deliveries are refused", quote(g.name), what))
			continue
		}
		for _, f := range g.fields {
			if _, ok := src.Attributes[f.name]; !ok {
				out = append(out, fmt.Sprintf("the mapping gives %s no %s, so deliveries are refused", quote(g.name), quote(f.name)))
			}
		}
		for attr := range src.Attributes {
			if !slices.ContainsFunc(g.fields, func(f field) bool { return f.name == attr }) {
				out = append(out, fmt.Sprintf("%s has no attribute %s, so deliveries are refused", quote(g.name), quote(attr)))
			}
		}
	}
	var unused []string
	for name := range m {
		if !slices.ContainsFunc(in, func(g givenObject) bool { return g.name == name }) {
			unused = append(unused, quote(name))
		}
	}
	slices.Sort(unused)
	if len(unused) > 0 {
		out = append(out, "the flow doesn't start from "+strings.Join(unused, ", ")+", so that part of the mapping goes unused")
	}
	return out
}

func hmacSHA256(key string, parts ...[]byte) []byte {
	h := hmac.New(sha256.New, []byte(key))
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// A digest a header gives, as hex or base64, after a sha256= or v0=
// prefix, if any; nil if it is neither.
func headerDigest(v string) []byte {
	v = strings.TrimSpace(v)
	for _, p := range []string{"sha256=", "v0="} {
		v = strings.TrimPrefix(v, p)
	}
	if b, err := hex.DecodeString(v); err == nil && len(b) == sha256.Size {
		return b
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(v); err == nil && len(b) == sha256.Size {
			return b
		}
	}
	return nil
}

// What a webhook checks a delivery's signature by.
type webhookSigner struct {
	kind, verify, header, secret string
}

// Why the delivery doesn't prove it holds the secret, or "". Comparisons
// take the same time however much of a signature matches.
func (s webhookSigner) refuse(h http.Header, body []byte, now time.Time) string {
	const mismatch = "its signature doesn't match the webhook's secret"
	switch s.kind {
	case webhookGitHub:
		sig := h.Get("X-Hub-Signature-256")
		if sig == "" {
			return "it has no X-Hub-Signature-256 signature; give the webhook's secret to GitHub"
		}
		got, ok := strings.CutPrefix(sig, "sha256=")
		b, err := hex.DecodeString(got)
		if !ok || err != nil || !hmac.Equal(b, hmacSHA256(s.secret, body)) {
			return mismatch
		}
	case webhookSlack:
		ts, sig := h.Get("X-Slack-Request-Timestamp"), h.Get("X-Slack-Signature")
		if ts == "" || sig == "" {
			return "it has no X-Slack-Signature or X-Slack-Request-Timestamp; it doesn't come from Slack"
		}
		got, ok := strings.CutPrefix(sig, "v0=")
		b, err := hex.DecodeString(got)
		if !ok || err != nil || !hmac.Equal(b, hmacSHA256(s.secret, []byte("v0:"+ts+":"), body)) {
			return mismatch
		}
		sent, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return "its timestamp isn't one"
		}
		if d := now.Sub(time.Unix(sent, 0)); d > slackTolerance || d < -slackTolerance {
			return "its timestamp is more than 5 minutes off, so it may be an old one sent again"
		}
	case webhookGeneric:
		v := h.Get(s.header)
		if v == "" {
			return fmt.Sprintf("it has no %s header", s.header)
		}
		if s.verify == verifyToken {
			v = strings.TrimPrefix(v, "Bearer ")
			// Digests, so the comparison doesn't tell the secret's length.
			a, b := sha256.Sum256([]byte(v)), sha256.Sum256([]byte(s.secret))
			if !hmac.Equal(a[:], b[:]) {
				return fmt.Sprintf("its %s header doesn't hold the webhook's secret", s.header)
			}
			return ""
		}
		got := headerDigest(v)
		if got == nil || !hmac.Equal(got, hmacSHA256(s.secret, body)) {
			return mismatch
		}
	default:
		return "the webhook is of no kind known"
	}
	return ""
}

// What a delivery carries, read from its body and headers once its
// signature is checked.
type webhookPayload struct {
	// The payload, decoded, numbers kept as they were written.
	value any
	// The payload as it is shown to write the mapping from.
	text string
	// Which event it is, and its id as its sender gave it.
	event, delivery string
	// For an event GitHub sends with an action, the action.
	action string
	// What to answer it with instead of starting a run, for Slack's URL
	// verification and GitHub's ping.
	answer any
	// Why it is ignored, if it is.
	ignored string
}

// Decodes JSON, keeping numbers as they were written.
func decodePayload(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("it has more after the JSON")
	}
	return v, nil
}

// What the delivery carries, for a webhook of the kind taking events; or
// why it is refused. GitHub may send its JSON as a form's payload field.
func readPayload(kind, idHeader string, events []string, h http.Header, body []byte) (webhookPayload, string) {
	var p webhookPayload
	data := body
	if ct, _, _ := mime.ParseMediaType(h.Get("Content-Type")); ct == "application/x-www-form-urlencoded" && kind == webhookGitHub {
		form, err := url.ParseQuery(string(body))
		if err != nil || !form.Has("payload") {
			return p, "it is a form without a payload; set the webhook's content type to application/json in GitHub"
		}
		data = []byte(form.Get("payload"))
	}
	v, err := decodePayload(data)
	switch {
	case err == nil:
		p.value, p.text = v, string(data)
	case kind == webhookGeneric:
		// What isn't JSON is its text, which only the whole payload gives.
		text := strings.ToValidUTF8(string(body), "\uFFFD")
		p.value, p.text = text, text
	default:
		return p, "its payload isn't JSON"
	}
	fields, _ := p.value.(map[string]any)
	str := func(m map[string]any, key string) string {
		s, _ := m[key].(string)
		return s
	}
	switch kind {
	case webhookGitHub:
		p.event, p.delivery, p.action = h.Get("X-GitHub-Event"), h.Get("X-GitHub-Delivery"), str(fields, "action")
		switch {
		case p.event == "":
			return p, "it has no X-GitHub-Event; it doesn't come from GitHub"
		case p.event == "ping":
			p.answer, p.ignored = map[string]string{"answer": "pong"}, "GitHub's ping, answered"
		case !slices.Contains(events, p.event) && !slices.Contains(events, p.event+"."+p.action):
			p.ignored = fmt.Sprintf("it takes %s, not %s", strings.Join(events, ", "), p.eventName())
		}
	case webhookSlack:
		switch t := str(fields, "type"); t {
		case "url_verification":
			p.event = t
			p.answer, p.ignored = map[string]string{"challenge": str(fields, "challenge")}, "Slack's URL verification, answered"
		case "event_callback":
			event, _ := fields["event"].(map[string]any)
			p.event, p.delivery = str(event, "type"), str(fields, "event_id")
			if len(events) > 0 && !slices.Contains(events, p.event) {
				p.ignored = fmt.Sprintf("it takes %s, not %s", strings.Join(events, ", "), p.event)
			}
		default:
			p.event, p.ignored = t, "it takes the Events API's events, not "+quote(t)
		}
	case webhookGeneric:
		if idHeader != "" {
			p.delivery = h.Get(idHeader)
		}
	}
	if len(p.delivery) > 255 {
		return p, "its id is longer than 255 characters"
	}
	return p, ""
}

// The event as the deliveries list names it: with its action, if any.
func (p webhookPayload) eventName() string {
	if p.action != "" {
		return p.event + "." + p.action
	}
	return p.event
}

// A webhook as a delivery needs it.
type webhookRow struct {
	id, flow   int64
	name, kind string
	signer     webhookSigner
	idHeader   string
	events     []string
	mapping    webhookMapping
	enabled    bool
	createdBy  sql.NullInt64
}

// At most n bytes of what a sender gave, as text a column takes.
func clipText(s string, n int) string {
	return strings.ToValidUTF8(truncate(strings.ToValidUTF8(s, ""), n), "")
}

func splitEvents(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// Whom the webhook's runs are for, or why it starts none: whoever added it,
// while they may run the flow; nobody without sign-in.
func webhookRunner(ctx context.Context, db *sql.DB, signIn bool, h webhookRow) (*int64, string, error) {
	if !signIn {
		return nil, "", nil
	}
	if !h.createdBy.Valid {
		return nil, "it was added before people signed in, so it is nobody's; whoever may change the flow adds it again", nil
	}
	u := user{ID: h.createdBy.Int64}
	var removed bool
	err := db.QueryRowContext(ctx, "SELECT email, name, admin, team_manager, removed_at IS NOT NULL FROM users WHERE id = ?", u.ID).
		Scan(&u.Email, &u.Name, &u.Admin, &u.TeamManager, &removed)
	if errors.Is(err, sql.ErrNoRows) || removed {
		return nil, "whoever added it was removed; whoever may change the flow adds it again", nil
	}
	if err != nil {
		return nil, "", err
	}
	access, err := flowAccess(ctx, db, &u, h.flow)
	if err != nil {
		return nil, "", err
	}
	if access != accessEdit {
		return nil, "whoever added it may no longer run the flow", nil
	}
	return &u.ID, "", nil
}

// Keeps a delivery among the webhook's latest, and logs it in the audit log.
func recordDelivery(ctx context.Context, db *sql.DB, h webhookRow, d webhookDelivery) {
	_, err := db.ExecContext(ctx, "INSERT INTO webhook_deliveries (webhook_id, delivery, event, outcome, reason, run_id) VALUES (?, ?, ?, ?, ?, ?)",
		h.id, clipText(d.Delivery, 255), clipText(d.Event, 255), d.Outcome, clipText(d.Reason, 1024), d.Run)
	if err == nil {
		// The newest are kept; a derived table, as MySQL limits no subquery.
		_, err = db.ExecContext(ctx, `DELETE FROM webhook_deliveries WHERE webhook_id = ? AND id <= (SELECT id FROM
			(SELECT id FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?) AS oldest)`, h.id, h.id, webhookDeliveriesKept)
	}
	if err != nil {
		log.Printf("record a delivery of webhook %d: %v", h.id, err)
	}
	var who *int64
	if h.createdBy.Valid {
		who = &h.createdBy.Int64
	}
	var run int64
	if d.Run != nil {
		run = *d.Run
	}
	detail := fmt.Sprintf("the webhook %s of flow %d", quote(h.name), h.flow)
	if d.Event != "" {
		detail += ", " + d.Event
	}
	if d.Delivery != "" {
		detail += ", delivery " + quote(d.Delivery)
	}
	detail += ": " + d.Outcome
	if d.Reason != "" {
		detail += ", " + d.Reason
	}
	audit("webhook.delivery", who, run, 0, "%s", detail)
}

// Takes a delivery to a webhook: checks it holds the secret, answers what
// asks for an answer, refuses what it can't take, and starts a run for the
// rest, answering once it is recorded.
func receiveWebhook(db *sql.DB, eng *engine, signIn bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		token := r.PathValue("token")
		var h webhookRow
		var stored, events string
		err := sql.ErrNoRows
		if webhookToken.MatchString(token) {
			err = db.QueryRowContext(ctx, `SELECT id, flow_id, name, kind, secret, verify, header, id_header, events, mapping, enabled, created_by
				FROM webhooks WHERE token = ?`, token).
				Scan(&h.id, &h.flow, &h.name, &h.kind, &stored, &h.signer.verify, &h.signer.header, &h.idHeader, &events, &h.mapping, &h.enabled, &h.createdBy)
		}
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such webhook", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		h.signer.kind, h.events = h.kind, splitEvents(events)
		if h.signer.secret, err = secrets.open(webhookPlace(h.id), stored); err != nil {
			serverError(w, fmt.Errorf("open the secret of webhook %d: %w", h.id, err))
			return
		}
		d := webhookDelivery{}
		refuse := func(status int, reason string) {
			d.Outcome, d.Reason = deliveryRefused, reason
			recordDelivery(context.WithoutCancel(ctx), db, h, d)
			http.Error(w, reason, status)
		}
		// Which delivery its headers say it is, to list one refused by.
		switch h.kind {
		case webhookGitHub:
			d.Event, d.Delivery = clipText(r.Header.Get("X-GitHub-Event"), 255), clipText(r.Header.Get("X-GitHub-Delivery"), 255)
		case webhookGeneric:
			if h.idHeader != "" {
				d.Delivery = clipText(r.Header.Get(h.idHeader), 255)
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("its payload is larger than %d MB", maxWebhookBody>>20))
				return
			}
			http.Error(w, "couldn't read the delivery", http.StatusBadRequest)
			return
		}
		if reason := h.signer.refuse(r.Header, body, time.Now()); reason != "" {
			refuse(http.StatusUnauthorized, reason)
			return
		}
		p, reason := readPayload(h.kind, h.idHeader, h.events, r.Header, body)
		if p.event != "" {
			d.Event, d.Delivery = p.eventName(), p.delivery
		}
		if reason != "" {
			refuse(http.StatusBadRequest, reason)
			return
		}
		if p.answer != nil {
			d.Outcome, d.Reason = deliveryIgnored, p.ignored
			recordDelivery(context.WithoutCancel(ctx), db, h, d)
			writeJSON(w, http.StatusOK, p.answer)
			return
		}
		// What it carried, to write the mapping from.
		if _, err := db.ExecContext(ctx, "UPDATE webhooks SET last_payload = ?, last_event = ?, last_payload_at = CURRENT_TIMESTAMP(3) WHERE id = ?",
			strings.ToValidUTF8(p.text, "\uFFFD"), clipText(d.Event, 255), h.id); err != nil {
			serverError(w, err)
			return
		}
		src := runSource{column: "webhook_id", id: h.id, given: "Given by the webhook " + h.name}
		answer := func(status int, run int64) {
			d.Run = &run
			recordDelivery(context.WithoutCancel(ctx), db, h, d)
			writeJSON(w, status, map[string]any{"run": run, "url": fmt.Sprintf("%s/#/flows/%d/runs/%d", publicOrigin(r), h.flow, run)})
		}
		// One sent again gets its run, however the webhook changed since.
		if p.delivery != "" {
			run, _, err := idempotentRun(ctx, db, h.flow, src, p.delivery)
			if err != nil {
				serverError(w, err)
				return
			}
			if run != 0 {
				d.Outcome = deliveryRepeat
				answer(http.StatusOK, run)
				return
			}
		}
		if !h.enabled {
			d.Outcome, d.Reason = deliveryIgnored, "the webhook is turned off"
			recordDelivery(context.WithoutCancel(ctx), db, h, d)
			writeJSON(w, http.StatusOK, map[string]string{"ignored": d.Reason})
			return
		}
		who, reason, err := webhookRunner(ctx, db, signIn, h)
		if err != nil {
			serverError(w, err)
			return
		}
		if reason != "" {
			refuse(http.StatusForbidden, reason)
			return
		}
		if p.ignored != "" {
			d.Outcome, d.Reason = deliveryIgnored, p.ignored
			recordDelivery(context.WithoutCancel(ctx), db, h, d)
			writeJSON(w, http.StatusOK, map[string]string{"ignored": d.Reason})
			return
		}
		run, created, refused, err := startGivenRun(ctx, db, eng, h.flow, src, who, p.delivery, func(in []givenObject) (apiRunRequest, error) {
			return h.mapping.request(in, p.value)
		})
		if err != nil {
			serverError(w, err)
			return
		}
		if refused != nil {
			refuse(refused.status, refused.msg)
			return
		}
		if !created {
			d.Outcome = deliveryRepeat
			answer(http.StatusOK, run)
			return
		}
		d.Outcome = deliveryStarted
		var status, message string
		if err := db.QueryRowContext(ctx, "SELECT status, COALESCE((SELECT error FROM run_steps WHERE run_id = runs.id AND status = ? LIMIT 1), '') FROM runs WHERE id = ?",
			stepDuplicate, run).Scan(&status, &message); err == nil && status == runTerminated {
			// What it gave another run handled already.
			d.Reason = message
		}
		answer(http.StatusAccepted, run)
	}
}

// The columns listWebhooks reads, of webhooks h.
const webhookColumns = `h.id, h.flow_id, h.name, h.kind, h.token, h.verify, h.header, h.id_header, h.events, h.mapping, h.enabled,
	h.created_by, ` + "COALESCE((SELECT COALESCE(NULLIF(u.name, ''), u.email) FROM users u WHERE u.id = h.created_by), '')" + `, h.created_at, h.secret`

// The flow's webhooks, or the one with id, never with their secrets; what
// the flow starts from, which their mappings are written for; and why it
// can't be started with inputs given, if it can't. Each says what keeps its
// deliveries from starting a run, as the version they would run stands.
func listWebhooks(ctx context.Context, db *sql.DB, r *http.Request, signIn bool, flow, id int64) ([]webhook, []webhookFlowInput, string, error) {
	out := []webhook{}
	q := "SELECT " + webhookColumns + " FROM webhooks h WHERE h.flow_id = ?"
	args := []any{flow}
	if id != 0 {
		q += " AND h.id = ?"
		args = append(args, id)
	}
	err := scanAll(ctx, db, q+" ORDER BY h.id", func(rows *sql.Rows) error {
		var h webhook
		var token, events, stored string
		var createdBy sql.NullInt64
		if err := rows.Scan(&h.ID, &h.Flow, &h.Name, &h.Kind, &token, &h.Verify, &h.Header, &h.IDHeader, &events, &h.Mapping, &h.Enabled,
			&createdBy, &h.CreatedBy, &h.CreatedAt, &stored); err != nil {
			return err
		}
		secret, err := secrets.open(webhookPlace(h.ID), stored)
		if err != nil {
			return err
		}
		if len(secret) > 12 {
			h.Hint = secret[len(secret)-4:]
		}
		h.URL, h.Events, h.Problems = publicOrigin(r)+"/hooks/"+token, splitEvents(events), []string{}
		if signIn && !createdBy.Valid {
			h.Problems = append(h.Problems, "it was added before people signed in, so it is nobody's and its deliveries are refused; add it again")
		}
		out = append(out, h)
		return nil
	}, args...)
	if err != nil {
		return nil, nil, "", err
	}
	// What its runs start from is what the version they take starts from.
	var in []givenObject
	var problem string
	err = inTx(ctx, db, func(tx *sql.Tx) error {
		board, err := boardToRun(ctx, tx, flow)
		if err != nil {
			return err
		}
		in, problem = flowInputs(ctx, flow, board)
		return nil
	})
	if err != nil {
		return nil, nil, "", err
	}
	inputs := []webhookFlowInput{}
	for _, g := range in {
		i := webhookFlowInput{Name: g.name, Many: g.many, Attributes: []string{}}
		for _, f := range g.fields {
			i.Attributes = append(i.Attributes, f.name)
		}
		inputs = append(inputs, i)
	}
	for i := range out {
		if problem != "" {
			out[i].Problems = append(out[i].Problems, "the flow can't be given what it starts from: "+problem)
		} else {
			out[i].Problems = append(out[i].Problems, out[i].Mapping.problems(in)...)
		}
		var d webhookDelivery
		err := db.QueryRowContext(ctx, "SELECT "+deliveryColumns+" FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT 1", out[i].ID).
			Scan(scanDelivery(&d)...)
		switch {
		case err == nil:
			out[i].LastDelivery = &d
		case !errors.Is(err, sql.ErrNoRows):
			return nil, nil, "", err
		}
	}
	return out, inputs, problem, nil
}

// The columns of a delivery scanDelivery reads.
const deliveryColumns = "id, received_at, delivery, event, outcome, reason, run_id"

func scanDelivery(d *webhookDelivery) []any {
	return []any{&d.ID, &d.ReceivedAt, &d.Delivery, &d.Event, &d.Outcome, &d.Reason, &d.Run}
}

// The payload of a webhook's latest signed delivery, to write the mapping
// from.
type webhookPayloadShown struct {
	Event      string    `json:"event"`
	ReceivedAt time.Time `json:"receivedAt"`
	Payload    string    `json:"payload"`
}

func registerWebhookRoutes(mux *http.ServeMux, db *sql.DB, eng *engine, signIn bool) {
	// Whether whoever asks may change the flow, writing why not if not: a
	// webhook starts runs as whoever added it, and its payloads are the
	// flow's, so those only deciding its reviews see none.
	mayEdit := func(w http.ResponseWriter, r *http.Request, flow int64) bool {
		access, err := flowAccess(r.Context(), db, requestUser(r), flow)
		switch {
		case err != nil:
			serverError(w, err)
			return false
		case access == "":
			http.Error(w, "flow not found", http.StatusNotFound)
			return false
		case access != accessEdit:
			http.Error(w, "only those who may change the flow see and change its webhooks", http.StatusForbidden)
			return false
		}
		return true
	}
	// The webhook the path names, and its flow, which whoever asks may change.
	named := func(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return 0, 0, false
		}
		var flow int64
		err = db.QueryRowContext(r.Context(), "SELECT flow_id FROM webhooks WHERE id = ?", id).Scan(&flow)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "webhook not found", http.StatusNotFound)
			return 0, 0, false
		}
		if err != nil {
			serverError(w, err)
			return 0, 0, false
		}
		return id, flow, mayEdit(w, r, flow)
	}
	decode := func(w http.ResponseWriter, r *http.Request, creating bool) (webhookFields, bool) {
		var f webhookFields
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&f); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return f, false
		}
		if msg := f.check(creating); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return f, false
		}
		return f, true
	}
	// Seals the secret where the webhook keeps it.
	keep := func(ctx context.Context, tx *sql.Tx, id int64, secret string) error {
		stored, err := secrets.seal(webhookPlace(id), secret)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE webhooks SET secret = ? WHERE id = ?", stored, id)
		}
		return err
	}
	// Writes the webhook, with its secret when it was made just now.
	writeOne := func(w http.ResponseWriter, r *http.Request, status int, flow, id int64, secret string) {
		hs, _, _, err := listWebhooks(r.Context(), db, r, signIn, flow, id)
		if err != nil || len(hs) == 0 {
			serverError(w, fmt.Errorf("read webhook %d back: %v", id, err))
			return
		}
		hs[0].Secret = secret
		writeJSON(w, status, hs[0])
	}

	// The flow's webhooks, what the flow starts from, and why it can't be
	// given that, if it can't.
	mux.HandleFunc("GET /api/webhooks", func(w http.ResponseWriter, r *http.Request) {
		flow, ok := flowParam(w, r)
		if !ok || !mayEdit(w, r, flow) {
			return
		}
		hs, inputs, problem, err := listWebhooks(r.Context(), db, r, signIn, flow, 0)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"webhooks": hs, "inputs": inputs, "problem": problem})
	})

	// Adds one to a flow: {"flow", "name", "kind", "verify", "header",
	// "idHeader", "events", "mapping", "enabled", "secret"}. Without a
	// secret it makes one, which the reply holds, never shown again. Its
	// runs are for whoever adds it.
	mux.HandleFunc("POST /api/webhooks", func(w http.ResponseWriter, r *http.Request) {
		f, ok := decode(w, r, true)
		if !ok {
			return
		}
		var n int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM flows WHERE id = ?", f.Flow).Scan(&n); err != nil {
			serverError(w, err)
			return
		}
		if n == 0 {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if !mayEdit(w, r, f.Flow) || refuseInStruct(w, r, db, f.Flow) {
			return
		}
		secret, made := f.Secret, ""
		if secret == "" {
			secret = webhookSecretPrefix + randomToken()
			made = secret
		}
		mapping, err := json.Marshal(f.Mapping)
		if err != nil {
			serverError(w, err)
			return
		}
		var id int64
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(r.Context(),
				`INSERT INTO webhooks (token, flow_id, name, kind, secret, verify, header, id_header, events, mapping, enabled, created_by)
				VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`,
				randomToken(), f.Flow, f.Name, f.Kind, f.Verify, f.Header, f.IDHeader, strings.Join(f.Events, ","), mapping,
				f.Enabled == nil || *f.Enabled, requestUserID(r))
			if err == nil {
				id, err = res.LastInsertId()
			}
			if err != nil {
				return err
			}
			// Sealed where it is kept, which the row's id says.
			return keep(r.Context(), tx, id, secret)
		})
		if err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "webhook.add", "the %s webhook %s of flow %d", f.Kind, quote(f.Name), f.Flow)
		writeOne(w, r, http.StatusCreated, f.Flow, id, made)
	})

	// Changes one as a request to add it would make it, keeping its secret
	// unless given another, or with {"newSecret": true} making one, which
	// the reply holds. Its runs stay for whoever added it.
	mux.HandleFunc("PUT /api/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, flow, ok := named(w, r)
		if !ok {
			return
		}
		f, ok := decode(w, r, false)
		if !ok {
			return
		}
		mapping, err := json.Marshal(f.Mapping)
		if err != nil {
			serverError(w, err)
			return
		}
		secret, made := f.Secret, ""
		if f.NewSecret {
			secret = webhookSecretPrefix + randomToken()
			made = secret
		}
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(),
				`UPDATE webhooks SET name = ?, kind = ?, verify = ?, header = ?, id_header = ?, events = ?, mapping = ?, enabled = COALESCE(?, enabled)
				WHERE id = ?`,
				f.Name, f.Kind, f.Verify, f.Header, f.IDHeader, strings.Join(f.Events, ","), mapping, f.Enabled, id)
			if err != nil || secret == "" {
				return err
			}
			return keep(r.Context(), tx, id, secret)
		})
		if err != nil {
			serverError(w, err)
			return
		}
		with := ""
		if secret != "" {
			with = ", with a new secret"
		}
		auditChange(r, "webhook.change", "the %s webhook %s of flow %d%s", f.Kind, quote(f.Name), flow, with)
		writeOne(w, r, http.StatusOK, flow, id, made)
	})

	// Removes one; deliveries to it are turned away from then on, and its
	// runs stay.
	mux.HandleFunc("DELETE /api/webhooks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, flow, ok := named(w, r)
		if !ok {
			return
		}
		var name string
		err := db.QueryRowContext(r.Context(), "SELECT name FROM webhooks WHERE id = ?", id).Scan(&name)
		if err == nil {
			_, err = db.ExecContext(r.Context(), "DELETE FROM webhooks WHERE id = ?", id)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "webhook.remove", "the webhook %s of flow %d", quote(name), flow)
		w.WriteHeader(http.StatusNoContent)
	})

	// Its latest deliveries, newest first, and the payload of the latest
	// signed one.
	mux.HandleFunc("GET /api/webhooks/{id}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		id, _, ok := named(w, r)
		if !ok {
			return
		}
		out := struct {
			Deliveries []webhookDelivery    `json:"deliveries"`
			Payload    *webhookPayloadShown `json:"payload"`
		}{Deliveries: []webhookDelivery{}}
		err := scanAll(r.Context(), db, "SELECT "+deliveryColumns+" FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT ?",
			func(rows *sql.Rows) error {
				var d webhookDelivery
				err := rows.Scan(scanDelivery(&d)...)
				out.Deliveries = append(out.Deliveries, d)
				return err
			}, id, webhookDeliveriesKept)
		if err != nil {
			serverError(w, err)
			return
		}
		var p webhookPayloadShown
		var text sql.NullString
		var at sql.NullTime
		err = db.QueryRowContext(r.Context(), "SELECT last_event, last_payload_at, last_payload FROM webhooks WHERE id = ?", id).Scan(&p.Event, &at, &text)
		if err != nil {
			serverError(w, err)
			return
		}
		if text.Valid && at.Valid {
			p.ReceivedAt, p.Payload = at.Time, text.String
			out.Payload = &p
		}
		writeJSON(w, http.StatusOK, out)
	})

	// Where other systems deliver their events; see receiveWebhook.
	mux.HandleFunc("POST /hooks/{token}", receiveWebhook(db, eng, signIn))
}
