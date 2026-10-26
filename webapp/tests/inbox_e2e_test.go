package tests

// The inbox lists what waits on each person, and people are told of it by
// email and on Slack, each message once across replicas; reviews remind
// and time out as their processes say.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type inboxItem struct {
	Step        int64      `json:"step"`
	Run         int64      `json:"run"`
	Flow        int64      `json:"flow"`
	FlowName    string     `json:"flowName"`
	Parent      *int64     `json:"parent"`
	Process     *int64     `json:"process"`
	ProcessName string     `json:"processName"`
	Waits       string     `json:"waits"`
	Detail      string     `json:"detail"`
	HandledBy   []string   `json:"handledBy"`
	StartedBy   string     `json:"startedBy"`
	Since       time.Time  `json:"since"`
	Expires     *time.Time `json:"expires"`
}

type inboxSettings struct {
	Email    bool `json:"email"`
	Slack    bool `json:"slack"`
	Failures bool `json:"failures"`
	CanEmail bool `json:"canEmail"`
	CanSlack bool `json:"canSlack"`
}

// What waits on whoever the app's requests are from, in the flows given.
func inboxOf(a *app, flows ...int64) []inboxItem {
	a.t.Helper()
	var all []inboxItem
	a.call("GET", "/api/inbox", nil, http.StatusOK, &all)
	items := []inboxItem{}
	for _, it := range all {
		if slices.Contains(flows, it.Flow) {
			items = append(items, it)
		}
	}
	return items
}

// The app as the person sees it, signed in through the provider.
func inboxAs(a *app, p *fakeProvider, who usersPerson) *app {
	a.t.Helper()
	c, status, path := usersTrySignIn(a, p, who)
	if path != "/flows/7" || status != http.StatusNotFound {
		a.t.Fatalf("signing in %s ended on %s with %d", who.email, path, status)
	}
	as := *a
	as.client = c
	return &as
}

// A webapp people sign in to, as usersApp's, with the environment variables
// in extra, and its worker at workerURL, a fake of its own if empty.
func inboxApp(t *testing.T, p *fakeProvider, workerURL string, extra ...string) *app {
	t.Helper()
	var fw *fakeWorker
	if workerURL == "" {
		fw = newFakeWorker(t)
		workerURL = fw.url
	}
	a := startApp(t, workerURL, append([]string{
		"OIDC_ISSUER=" + p.srv.URL,
		"OIDC_CLIENT_ID=" + usersClientID,
		"OIDC_CLIENT_SECRET=" + usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com",
	}, extra...)...)
	a.worker = fw
	return a
}

// Each person's inbox lists the reviews they may decide by their roles and
// the loop questions of flows they may edit, newest first, and nothing of
// flows they don't see; an admin's lists everything.
func TestInboxListsWhatWaitsOnEachPerson(t *testing.T) {
	p := newFakeProvider(t)
	a := inboxApp(t, p, "")
	usersSignIn(a, p, usersAda)
	b := humanNewReviewBoard(a)
	usersShare(a, b.flow.ID)
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", b.review.ID), map[string]any{"prompt": "Check the notes read well."}, http.StatusOK, nil)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email, "Editor")
	usersAdd(a, carl.email)
	// A loop question of Ada's own flow, which only she sees.
	loops, _ := loopsFailing(a, 1, handsIn(data("reworked")))

	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	loopRun := a.startRun(loops.ID)
	asked := a.waitRun(loopRun.ID, "it to ask", func(d runDetail) bool { return len(loopsAsked(d, "Fix")) > 0 })
	question := loopsAsked(asked, "Fix")[0]

	got := inboxOf(a, b.flow.ID, loops.ID)
	if len(got) != 2 || got[0].Step != question.ID || got[1].Step != review.ID {
		t.Fatalf("Ada's inbox: %+v", got)
	}
	if q := got[0]; q.Waits != "loop" || q.Run != loopRun.ID || q.FlowName != "Failure loop" || q.ProcessName != "Fix" || !strings.Contains(q.Detail, "Should it go on?") {
		t.Errorf("the loop question: %+v", q)
	}
	r := got[1]
	if r.Waits != "review" || r.Run != rn.ID || r.Flow != b.flow.ID || r.ProcessName != "Review" || r.Detail != "Check the notes read well." ||
		!slices.Equal(r.HandledBy, []string{"Editor"}) || r.StartedBy != "Ada Lovelace" || r.Since.IsZero() || r.Expires != nil {
		t.Errorf("the review: %+v", r)
	}

	asBob, asCarl := inboxAs(a, p, bob), inboxAs(a, p, carl)
	if got := inboxOf(asBob, b.flow.ID, loops.ID); len(got) != 1 || got[0].Step != review.ID {
		t.Errorf("Bob's inbox: %+v", got)
	}
	if got := inboxOf(asCarl, b.flow.ID, loops.ID); len(got) != 0 {
		t.Errorf("Carl's inbox: %+v", got)
	}

	// Deciding takes it out of the inbox.
	humanMustDecide(asBob, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	if got := inboxOf(asBob, b.flow.ID); len(got) != 0 {
		t.Errorf("Bob's inbox once he decided: %+v", got)
	}
	loopsAnswer(a, loopRun.ID, question.ID, false)
	if got := inboxOf(a, b.flow.ID, loops.ID); len(got) != 0 {
		t.Errorf("Ada's inbox once all is decided: %+v", got)
	}
}

// Without sign-in everyone decides everything, so the inbox lists every
// step waiting, and an input says it is one.
func TestInboxWithoutSignIn(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Ask for a ticket")
	ask := humanHuman(a, f.ID, "Ask")
	ticket := a.newObject(f.ID, "Ticket")
	a.output(ask.ID, ticket.ID)
	a.startAt(f.ID, ask.ID)
	rn := a.startRun(f.ID)
	s := a.waitStep(rn.ID, "Ask", stepWaiting)
	if got := inboxOf(a, f.ID); len(got) != 1 || got[0].Step != s.ID || got[0].Waits != "input" || got[0].StartedBy != "" {
		t.Errorf("inbox: %+v", got)
	}
	a.refused("GET", "/api/me/notifications", nil, http.StatusNotFound, "")
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
}

// An SMTP server on this machine that takes every email, and keeps them.
type inboxSMTP struct {
	addr  string
	mu    sync.Mutex
	mails []inboxMail
}

type inboxMail struct {
	to, subject, text string
}

func newInboxSMTP(t *testing.T) *inboxSMTP {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	s := &inboxSMTP{addr: l.Addr().String()}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *inboxSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { io.WriteString(c, line+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch verb := strings.ToUpper(strings.Fields(line + " x")[0]); verb {
		case "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			msg, err := mail.ReadMessage(strings.NewReader(b.String()))
			if err == nil {
				subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
				text, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
				to, _ := mail.ParseAddress(msg.Header.Get("To"))
				s.mu.Lock()
				s.mails = append(s.mails, inboxMail{to: to.Address, subject: subject, text: strings.ReplaceAll(string(text), "\r\n", "\n")})
				s.mu.Unlock()
			}
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// The emails to the address.
func (s *inboxSMTP) to(address string) []inboxMail {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ms []inboxMail
	for _, m := range s.mails {
		if m.to == address {
			ms = append(ms, m)
		}
	}
	return ms
}

// The variables a webapp emails through the server with. Without
// PUBLIC_URL, which signing in goes back to, messages link to the webapp
// on this machine.
func (s *inboxSMTP) env() []string {
	host, port, _ := net.SplitHostPort(s.addr)
	return []string{"SMTP_HOST=" + host, "SMTP_PORT=" + port, "SMTP_FROM=Spoletta <spoletta@example.com>"}
}

// Where messages link to on the app: its own port on this machine.
func inboxLink(a *app, path string) string {
	a.t.Helper()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(a.url, "http://"))
	return "http://localhost:" + port + path
}

// A Slack that knows everyone at example.org, by a user id made of their
// email, and keeps the direct messages its app sends.
type inboxSlack struct {
	srv *httptest.Server
	mu  sync.Mutex
	dms map[string][]string
}

func newInboxSlack(t *testing.T) *inboxSlack {
	s := &inboxSlack{dms: map[string][]string{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-inbox" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		switch r.URL.Path {
		case "/users.lookupByEmail":
			email := r.URL.Query().Get("email")
			if !strings.HasSuffix(email, "@example.org") {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "users_not_found"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": map[string]any{"id": "U-" + email}})
		case "/chat.postMessage":
			var msg struct {
				Channel string `json:"channel"`
				Text    string `json:"text"`
			}
			json.NewDecoder(r.Body).Decode(&msg)
			s.mu.Lock()
			s.dms[strings.TrimPrefix(msg.Channel, "U-")] = append(s.dms[strings.TrimPrefix(msg.Channel, "U-")], msg.Text)
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": msg.Channel, "ts": "1730000000.000100"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *inboxSlack) to(email string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dms[email])
}

// Installs the Slack connector at the fake, with its token; the admin's
// app does.
func (s *inboxSlack) install(a *app, name string) {
	a.t.Helper()
	forgetMCPServers(a.t, name)
	var server struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/mcp-servers", map[string]any{"name": name, "connector": "slack", "url": s.srv.URL}, http.StatusCreated, &server)
	a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", server.ID), map[string]any{"token": "xoxb-inbox"}, http.StatusNoContent, nil)
}

// Waits until cond holds, failing the test if it doesn't soon.
func inboxEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Whoever may decide a review is told it waits on them, by email and on
// Slack, once, with a link to it; an admin only when nobody else may decide
// it. Each person picks how they are told, and whether of their runs
// failing.
func TestNotifyWhoMayDecide(t *testing.T) {
	p := newFakeProvider(t)
	smtp, slack := newInboxSMTP(t), newInboxSlack(t)
	a := inboxApp(t, p, "", smtp.env()...)
	usersSignIn(a, p, usersAda)
	slack.install(a, "inbox_slack")
	b := humanNewReviewBoard(a)
	usersShare(a, b.flow.ID)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email, "Editor")
	usersAdd(a, carl.email)
	asBob := inboxAs(a, p, bob)

	var s inboxSettings
	asBob.call("GET", "/api/me/notifications", nil, http.StatusOK, &s)
	if s != (inboxSettings{Email: true, Slack: true, Failures: false, CanEmail: true, CanSlack: true}) {
		t.Errorf("Bob's settings at first: %+v", s)
	}

	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	link := inboxLink(a, fmt.Sprintf("/#/flows/%d/runs/%d/steps/%d", b.flow.ID, rn.ID, review.ID))
	inboxEventually(t, "Bob's email and Slack message", func() bool { return len(smtp.to(bob.email)) > 0 && len(slack.to(bob.email)) > 0 })
	m := smtp.to(bob.email)[0]
	if m.subject != fmt.Sprintf(`"Review" waits for your review, in run %d of "Review"`, rn.ID) || !strings.Contains(m.text, "Open the review: "+link) {
		t.Errorf("Bob's email: %+v", m)
	}
	if dm := slack.to(bob.email)[0]; !strings.Contains(dm, "<"+link+"|Open the review>") {
		t.Errorf("Bob's Slack message: %s", dm)
	}
	logLineWith(t, a.runLogs(rn.ID, 0), "told Bob by email and Slack that \"Review\" waits for your review")

	// Bob turns email off; the next review comes on Slack alone.
	asBob.call("PATCH", "/api/me/notifications", map[string]any{"email": false, "failures": true}, http.StatusOK, &s)
	if s.Email || !s.Slack || !s.Failures {
		t.Errorf("Bob's settings: %+v", s)
	}
	humanMustDecide(asBob, rn.ID, review.ID, map[string]any{"state": b.changes.ID})
	again := a.waitStep(rn.ID, "Review", stepWaiting, review.ID)
	inboxEventually(t, "Bob's second Slack message", func() bool { return len(slack.to(bob.email)) == 2 })
	humanMustDecide(asBob, rn.ID, again.ID, map[string]any{"state": b.approved.ID})
	a.finish(rn.ID)

	// A run Bob starts failing tells him, as he asked.
	f := a.newFlow("Breaks")
	usersShare(a, f.ID)
	breaks := a.agent(f.ID, "Break", "Break.")
	a.output(breaks.ID, a.newObject(f.ID, "Shards").ID)
	a.startAt(f.ID, breaks.ID)
	a.worker.onAgent("Break", func(_ context.Context, _ agentRun) runResult { return runResult{Error: "it broke"} })
	failed := asBob.startRun(f.ID)
	if d := a.finish(failed.ID); d.Status != runFailed {
		t.Fatalf("the breaking run %s", d.Status)
	}
	inboxEventually(t, "Bob being told his run failed", func() bool { return len(slack.to(bob.email)) == 3 })
	if dm := slack.to(bob.email)[2]; !strings.Contains(dm, fmt.Sprintf("Run %d of", failed.ID)) ||
		!strings.Contains(dm, inboxLink(a, fmt.Sprintf("/#/flows/%d/runs/%d|Open the run", f.ID, failed.ID))) {
		t.Errorf("Bob's failure message: %s", dm)
	}

	// A trial of the process failing tells him nothing: he is watching it.
	trial := tryProcess(asBob, breaks.ID)
	if d := a.finish(trial.ID); d.Status != runFailed {
		t.Fatalf("the breaking trial %s", d.Status)
	}

	// Each went once; nobody else may decide them, so Ada and Carl heard
	// nothing.
	time.Sleep(1500 * time.Millisecond)
	if n, k := len(smtp.to(bob.email)), len(slack.to(bob.email)); n != 1 || k != 3 {
		t.Errorf("Bob got %d emails and %d Slack messages, want 1 and 3", n, k)
	}
	for _, who := range []string{usersAda.email, carl.email} {
		for _, m := range smtp.to(who) {
			if strings.Contains(m.text, fmt.Sprintf("run %d of", rn.ID)) {
				t.Errorf("%s was told: %+v", who, m)
			}
		}
	}
	if len(slack.to(carl.email)) != 0 {
		t.Errorf("Carl was told on Slack: %q", slack.to(carl.email))
	}

	// A review nobody but admins may decide goes to them.
	g := a.newFlow("Unheld")
	usersShare(a, g.ID)
	write := a.agent(g.ID, "Write", "Write.")
	note := a.newObject(g.ID, "Note")
	seen := a.newState(note.ID, "seen")
	sign := humanHuman(a, g.ID, "Sign")
	humanPerson(a, g.ID, sign.ID, "Nobody holds this")
	a.output(write.ID, note.ID)
	a.input(sign.ID, note.ID)
	humanDecides(a, sign.ID, note.ID, seen.ID)
	a.startAt(g.ID, write.ID)
	a.worker.onAgent("Write", handsIn(data("a note")))
	rn = a.startRun(g.ID)
	a.waitStep(rn.ID, "Sign", stepWaiting)
	inboxEventually(t, "Ada being told", func() bool {
		return slices.ContainsFunc(smtp.to(usersAda.email), func(m inboxMail) bool { return strings.Contains(m.subject, fmt.Sprintf("run %d of", rn.ID)) })
	})
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
}

// A review reminds those who may decide it every remindAfter while it
// waits, each round once though two replicas watch, and no more once it is
// decided; past its time limit it fails, once, never reminding at or after
// it, and a failure connection handles it.
func TestReviewRemindsAndTimesOutOnce(t *testing.T) {
	p := newFakeProvider(t)
	smtp := newInboxSMTP(t)
	a := inboxApp(t, p, "", smtp.env()...)
	other := inboxApp(t, p, a.worker.url, smtp.env()...)
	other.worker = a.worker
	usersSignIn(a, p, usersAda)
	// Sessions are kept in the database, so Ada is signed in at both.
	other.client = a.client
	bob := usersNewPerson("Bob")
	row := usersAdd(a, bob.email, "Approver")
	db := secretsDB(t)

	f := a.newFlow("Timed review")
	usersShare(a, f.ID)
	draft := a.agent(f.ID, "Draft", "Draft it.")
	doc := a.newObject(f.ID, "Doc")
	ok := a.newState(doc.ID, "ok")
	review := humanHuman(a, f.ID, "Approve")
	humanPerson(a, f.ID, review.ID, "Approver")
	handle := a.agent(f.ID, "Chase", "Chase it up.")
	a.output(draft.ID, doc.ID)
	a.input(review.ID, doc.ID)
	humanDecides(a, review.ID, doc.ID, ok.ID)
	a.output(handle.ID, a.newObject(f.ID, "Chased").ID)
	a.startAt(f.ID, draft.ID)
	failuresHandles(a, review.ID, handle.ID)
	failuresSettings(a, review.ID, processSettings{RemindAfter: ptr(1), WaitAtMost: ptr(6)})
	a.worker.onAgent("Draft", handsIn(data("a doc")))
	a.worker.onAgent("Chase", handsIn(data("chased")))

	var opl struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil, http.StatusOK, &opl)
	for _, want := range []string{`"Approve" reminds whoever decides it every 1 second.`, `"Approve" fails after waiting 6 seconds.`, `"Chase" handles a failure of "Approve".`} {
		if !strings.Contains(opl.Text, want) {
			t.Errorf("OPL lacks %s:\n%s", want, opl.Text)
		}
	}

	// What Bob was emailed about a run, by kind.
	mailed := func(run int64) (waiting, reminders int) {
		for _, m := range smtp.to(bob.email) {
			switch {
			case !strings.Contains(m.subject, fmt.Sprintf("in run %d of", run)):
			case strings.HasPrefix(m.subject, `Reminder: "Approve" still waits for your review`):
				reminders++
			case strings.HasPrefix(m.subject, `"Approve" waits for your review`):
				waiting++
			}
		}
		return waiting, reminders
	}
	// The rounds Bob was reminded of a step in, claimed once each.
	rounds := func(step int64) []int {
		rows, err := db.Query("SELECT round FROM notifications WHERE step_id = ? AND user_id = ? AND kind = 'reminder' ORDER BY round", step, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var rs []int
		for rows.Next() {
			var r int
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			rs = append(rs, r)
		}
		return rs
	}

	decided, timed := other.startRun(f.ID), other.startRun(f.ID)
	s := a.waitStep(decided.ID, "Approve", stepWaiting)
	// Both wait before the inbox is read, or it may list the first alone.
	a.waitStep(timed.ID, "Approve", stepWaiting)
	if got := inboxOf(inboxAs(a, p, bob), f.ID); len(got) != 2 || got[0].Expires == nil || got[0].Expires.Sub(got[0].Since) != 6*time.Second {
		t.Errorf("Bob's inbox: %+v", got)
	}

	// Reminded twice, Bob decides; no more come.
	inboxEventually(t, "two reminders", func() bool { _, n := mailed(decided.ID); return n >= 2 })
	humanMustDecide(a, decided.ID, s.ID, map[string]any{"state": ok.ID})
	if d := a.finish(decided.ID); d.Status != runDone {
		t.Fatalf("the decided run %s: %s", d.Status, describeSteps(d))
	}
	_, before := mailed(decided.ID)
	time.Sleep(2500 * time.Millisecond)
	if _, after := mailed(decided.ID); after != before {
		t.Errorf("reminded %d times once decided, after %d before", after-before, before)
	}

	// The other waits out its time limit.
	d := a.finish(timed.ID)
	if d.Status != runDone {
		t.Fatalf("the run %s: %s", d.Status, describeSteps(d))
	}
	approve := onlyStep(t, d, "Approve")
	want := "It waited longer than its time limit, 6 seconds, with nobody deciding."
	if approve.Status != stepError || approve.Error != want {
		t.Errorf("the review: %s %q", approve.Status, approve.Error)
	}
	chase := onlyStep(t, d, "Chase")
	if !slices.ContainsFunc(chase.Inputs, func(in runInput) bool { return in.Name == "Failure of Approve" && in.Content == want }) {
		t.Errorf("Chase took %+v", chase.Inputs)
	}
	time.Sleep(1500 * time.Millisecond)
	for _, c := range []struct {
		run  int64
		step int64
	}{{decided.ID, s.ID}, {timed.ID, approve.ID}} {
		waiting, reminders := mailed(c.run)
		rs := rounds(c.step)
		if waiting != 1 || reminders < 2 || reminders != len(rs) {
			t.Errorf("run %d: Bob got %d emails saying it waits and %d reminders, rounds claimed %v", c.run, waiting, reminders, rs)
		}
		for i, r := range rs {
			if r != i+1 {
				t.Errorf("run %d: rounds claimed %v, want 1, 2, …", c.run, rs)
				break
			}
		}
	}
	var late int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notifications n JOIN run_steps s ON s.id = n.step_id
		WHERE n.step_id = ? AND n.kind = 'reminder' AND n.claimed_at >= s.expires_at`, approve.ID).Scan(&late); err != nil || late != 0 {
		t.Errorf("%d reminders claimed at or after the time limit (%v)", late, err)
	}

	// What can't be set.
	for _, st := range []processSettings{{RemindAfter: ptr(0)}, {WaitAtMost: ptr(maxWait + 1)}} {
		a.refused("PATCH", fmt.Sprintf("/api/processes/%d", review.ID), map[string]any{"settings": st}, http.StatusBadRequest, "must be from 1 to")
	}
}

// A message a replica claimed but didn't send before it stopped is sent by
// another; one sent isn't sent again.
func TestNotifySendsWhatAStoppedReplicaClaimed(t *testing.T) {
	p := newFakeProvider(t)
	smtp := newInboxSMTP(t)
	a := inboxApp(t, p, "", smtp.env()...)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	row := usersAdd(a, bob.email, "Editor")
	b := humanNewReviewBoard(a)
	usersShare(a, b.flow.ID)
	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	inboxEventually(t, "Bob's email", func() bool { return len(smtp.to(bob.email)) == 1 })

	db := secretsDB(t)
	for _, c := range []struct {
		kind string
		sent any
	}{{"reminder", nil}, {"lost", time.Now()}} {
		if _, err := db.Exec("INSERT INTO notifications (run_id, step_id, user_id, kind, replica_id, sent_at) VALUES (?, ?, ?, ?, ?, ?)",
			rn.ID, review.ID, row.ID, c.kind, "0123456789abcdef0123456789abcdef", c.sent); err != nil {
			t.Fatal(err)
		}
	}
	inboxEventually(t, "the reminder claimed by a replica gone", func() bool { return len(smtp.to(bob.email)) == 2 })
	if m := smtp.to(bob.email)[1]; !strings.HasPrefix(m.subject, "Reminder: ") {
		t.Errorf("the second email: %+v", m)
	}
	time.Sleep(1500 * time.Millisecond)
	if n := len(smtp.to(bob.email)); n != 2 {
		t.Errorf("Bob got %d emails, want 2", n)
	}
	var unsent int
	if err := db.QueryRow("SELECT COUNT(*) FROM notifications WHERE run_id = ? AND sent_at IS NULL", rn.ID).Scan(&unsent); err != nil || unsent != 0 {
		t.Errorf("%d unsent (%v)", unsent, err)
	}
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)
}

// A process becoming human keeps what handles its failure, as it can fail
// past its time limit, but not what runs right after it.
func TestHumanKeepsItsFailureConnections(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Becoming human")
	check := a.agent(f.ID, "Check", "Check it.")
	chase := a.agent(f.ID, "Chase", "Chase.")
	next := a.agent(f.ID, "Next", "Next.")
	failuresHandles(a, check.ID, chase.ID)
	a.call("POST", "/api/connections", map[string]any{"source": check.ID, "target": next.ID}, http.StatusCreated, nil)
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", check.ID), map[string]any{"kind": kindHuman}, http.StatusOK, nil)
	var cs []connection
	a.call("GET", fmt.Sprintf("/api/connections?flow=%d", f.ID), nil, http.StatusOK, &cs)
	if len(cs) != 1 || cs[0].Target != chase.ID || cs[0].Kind != connectionFailure {
		t.Errorf("connections: %+v", cs)
	}
}

// A review's time limit and instructions are its run's version's: a run of
// the version published keeps them while the draft has dropped them.
func TestReviewWaitsAsItsVersionSays(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Versioned review")
	draft := a.agent(f.ID, "Draft", "Draft it.")
	doc := a.newObject(f.ID, "Doc")
	ok := a.newState(doc.ID, "ok")
	review := humanHuman(a, f.ID, "Approve")
	chase := a.agent(f.ID, "Chase", "Chase it up.")
	a.output(draft.ID, doc.ID)
	a.input(review.ID, doc.ID)
	humanDecides(a, review.ID, doc.ID, ok.ID)
	a.output(chase.ID, a.newObject(f.ID, "Chased").ID)
	a.startAt(f.ID, draft.ID)
	failuresHandles(a, review.ID, chase.ID)
	a.worker.onAgent("Draft", handsIn(data("a doc")))
	a.worker.onAgent("Chase", handsIn(data("chased")))
	path := fmt.Sprintf("/api/processes/%d", review.ID)
	a.call("PATCH", path, map[string]any{"prompt": "Check what was published.", "settings": processSettings{WaitAtMost: ptr(2)}}, http.StatusOK, nil)
	pubPublish(a, f.ID, "with a time limit")
	a.call("PATCH", path, map[string]any{"prompt": "Check the draft.", "settings": processSettings{}}, http.StatusOK, nil)

	var rn run
	a.call("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), map[string]any{"published": true}, http.StatusCreated, &rn)
	s := a.waitStep(rn.ID, "Approve", stepWaiting)
	if got := inboxOf(a, f.ID); len(got) != 1 || got[0].Step != s.ID || got[0].Detail != "Check what was published." || got[0].Expires == nil {
		t.Errorf("inbox: %+v", got)
	}
	d := a.finish(rn.ID)
	if approve := onlyStep(t, d, "Approve"); d.Status != runDone || approve.Error != "It waited longer than its time limit, 2 seconds, with nobody deciding." {
		t.Errorf("the run %s, the review %s %q", d.Status, approve.Status, approve.Error)
	}
	onlyStep(t, d, "Chase")
}
