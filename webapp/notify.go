package main

// People are told when a step starts waiting on them: a review or input
// they may decide, or a loop question they may answer (see decidesStep);
// again every time a review has waited as long again as its process's
// remindAfter says, until it is decided, stopped, or fails past its time
// limit, never at or after that; and, if they want, when a run they
// started fails. A review whose
// process has waitAtMost fails once it has waited that long, as a step
// running past its timeout does, so what handles its failure takes over.
// One whose process has escalateAfter is handed on once it has waited that
// long: the roles its escalateTo names may then decide it too, beside those
// who could before, who still may, and those it now waits on are told.
//
// Each message goes by email (mail.go), when the webapp has an SMTP server,
// and as a Slack direct message from the Slack connector's app, when one is
// installed with a token, finding the person by their email. Each person
// picks which in their account's settings, and whether they are told of
// their runs failing. Every message links straight to the review, or to
// the run, at PUBLIC_URL, or on this machine without it.
//
// Admins may decide any review, but only so that one nobody else may
// decide can still go on; so they are told only of those, unless they
// may decide it as anyone else would. A review no human object handles is
// anyone's who sees its flow, and everyone who does is told of it.
//
// Several replicas may notice the same thing, so each message is claimed
// first: a row in notifications for its step (or run), person and kind,
// which only one replica gets to add, and which records when it was sent.
// A replica that stops between claiming a message and sending it leaves it
// without a sent time, and another sends it once it finds the claiming
// replica gone; so a message goes once, or twice if a replica stopped just
// as it sent it, but never not at all. Deadlines are kept with their steps
// by the database's clock, and every replica looks for those due each
// second: a round of reminders is claimed by counting it and moving the
// step's next reminder on, a time limit by failing its step, which the
// replica walking the run is then told, as of a decision. Without sign-in nobody is anyone, so nobody is told anything,
// though reviews are still timed out.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// What a person is told of.
const (
	noticeWaiting  = "waiting"
	noticeReminder = "reminder"
	// A review handed on to more roles, told to those it now waits on too.
	noticeEscalated = "escalated"
	noticeFailed    = "failed"
)

// What a step waiting for a person waits for: a decision on a review, what
// they fill in, or whether a process goes on round a loop.
const (
	waitsReview = "review"
	waitsInput  = "input"
	waitsLoop   = "loop"
)

// How often every replica looks for reminders and time limits due, and for
// messages claimed by replicas that stopped.
const noticeInterval = time.Second

// How long telling one person of one thing may take, on every channel.
const noticeTimeout = 2 * time.Minute

type notifier struct {
	db *sql.DB
	// Whether people sign in; without, nobody is told anything.
	signIn bool
	// Nil without SMTP_HOST.
	mail *mailer
	// Where messages link to.
	base string
	// Fails a review that waited past its time limit.
	eng *engine
}

func newNotifier(db *sql.DB, signIn bool, mail *mailer, eng *engine) *notifier {
	base := strings.TrimRight(env("PUBLIC_URL", ""), "/")
	if base == "" {
		base = "http://localhost:" + env("PORT", "8080")
	}
	n := &notifier{db: db, signIn: signIn, mail: mail, base: base, eng: eng}
	switch {
	case !signIn:
		log.Printf("nobody is told what waits on them, as nobody signs in")
	case mail != nil:
		log.Printf("people are told what waits on them by email through %s:%s, and by Slack when its connector is installed; the messages link to %s", mail.host, mail.port, base)
	default:
		log.Printf("people are told what waits on them by Slack when its connector is installed, not by email as SMTP_HOST isn't set; the messages link to %s", base)
	}
	if signIn && env("PUBLIC_URL", "") == "" {
		log.Printf("WARNING: PUBLIC_URL isn't set, so the messages telling people what waits on them link to %s", base)
	}
	return n
}

// Records when a review is due to be reminded of first, to be handed on
// and to fail, from when it started waiting by the database's clock, as
// its process's settings say; none reminds or is handed on at or after it
// fails.
func keepDeadlines(ctx context.Context, db *sql.DB, step int64, s processSettings) error {
	before := func(at *int) *int {
		if at != nil && s.WaitAtMost != nil && *at >= *s.WaitAtMost {
			return nil
		}
		return at
	}
	remind, escalate := before(s.RemindAfter), before(s.EscalateAfter)
	if len(s.EscalateTo) == 0 {
		escalate = nil
	}
	if remind == nil && escalate == nil && s.WaitAtMost == nil {
		return nil
	}
	_, err := db.ExecContext(ctx,
		`UPDATE run_steps SET remind_at = started_at + INTERVAL ? SECOND, escalate_at = started_at + INTERVAL ? SECOND,
		expires_at = started_at + INTERVAL ? SECOND WHERE id = ?`,
		remind, escalate, s.WaitAtMost, step)
	return err
}

// The people told a step waits on them, by id, as above, the roles deciding
// it as its run's board has them, those it was handed on to too once it was.
func deciders(ctx context.Context, db *sql.DB, step int64) ([]int64, error) {
	var escalated bool
	if err := db.QueryRowContext(ctx, "SELECT escalated FROM run_steps WHERE id = ?", step).Scan(&escalated); err != nil {
		return nil, err
	}
	return decidersAs(ctx, db, step, escalated)
}

// The people told a step waits on them, as deciders, as if it was handed
// on, or not.
func decidersAs(ctx context.Context, db *sql.DB, step int64, escalated bool) ([]int64, error) {
	var kind string
	var process sql.NullInt64
	var run, flow int64
	err := db.QueryRowContext(ctx, "SELECT s.kind, s.process_ref, s.run_id, r.flow_id FROM run_steps s JOIN runs r ON r.id = s.run_id WHERE s.id = ?", step).
		Scan(&kind, &process, &run, &flow)
	if err != nil {
		return nil, err
	}
	var handledBy []string
	if kind == kindHuman && process.Valid {
		b, err := runBoardOf(ctx, db, run)
		if err != nil {
			return nil, err
		}
		handledBy = b.deciders(process.Int64, escalated)
	}
	var people []user
	err = scanAll(ctx, db, "SELECT id, email, name, admin, team_manager FROM users WHERE removed_at IS NULL ORDER BY id", func(rows *sql.Rows) error {
		var u user
		err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Admin, &u.TeamManager)
		people = append(people, u)
		return err
	})
	if err != nil {
		return nil, err
	}
	var told, admins []int64
	for _, u := range people {
		if u.Admin {
			admins = append(admins, u.ID)
		}
		// As anyone else would.
		as := u
		as.Admin = false
		roles, err := userRoles(ctx, db, u.ID)
		if err != nil {
			return nil, err
		}
		access, err := flowAccess(ctx, db, &as, flow)
		if err != nil {
			return nil, err
		}
		if decidesStep(&as, roles, access, kind, handledBy) {
			told = append(told, u.ID)
		}
	}
	if len(told) == 0 {
		return admins, nil
	}
	return told, nil
}

// Claims telling the person of the step of the run, 0 for the run itself,
// as kind says, in the round of reminders given, 0 for anything else: the
// claim's id, or 0 if one was claimed already, here or at another replica.
func claimNotice(ctx context.Context, db execer, run, step, person int64, kind string, round int) (int64, error) {
	res, err := db.ExecContext(ctx, "INSERT INTO notifications (run_id, step_id, user_id, kind, round, replica_id) VALUES (?, ?, ?, ?, ?, ?)",
		run, step, person, kind, round, replicaID)
	if mysqlErrorIs(err, mysqlErrDuplicate) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Tells those who may decide the step of the run that it waits on them.
func (n *notifier) waiting(run, step int64) {
	if n == nil || !n.signIn {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()
	people, err := deciders(ctx, n.db, step)
	if err != nil {
		runLog(n.db, run, step, logError, "who to tell that step %d waits on them couldn't be found: %v", step, err)
		return
	}
	var claims []int64
	for _, p := range people {
		id, err := claimNotice(ctx, n.db, run, step, p, noticeWaiting, 0)
		if err != nil {
			runLog(n.db, run, step, logError, "telling user %d that step %d waits on them couldn't be claimed: %v", p, step, err)
			continue
		}
		if id != 0 {
			claims = append(claims, id)
		}
	}
	for _, id := range claims {
		n.deliver(id)
	}
}

// Tells whoever started the run, if they want, that it failed; never of a
// trial, which its maker is watching from the process's panel.
func (n *notifier) runFailed(run int64) {
	if n == nil || !n.signIn {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()
	var who sql.NullInt64
	var trial bool
	err := n.db.QueryRowContext(ctx,
		"SELECT COALESCE(r.started_by, s.created_by), r.trial_of IS NOT NULL FROM runs r LEFT JOIN schedules s ON s.id = r.schedule_id WHERE r.id = ?", run).Scan(&who, &trial)
	if err != nil {
		runLog(n.db, run, 0, logError, "who started it, to tell them it failed, couldn't be found: %v", err)
		return
	}
	if !who.Valid || trial {
		return
	}
	id, err := claimNotice(ctx, n.db, run, 0, who.Int64, noticeFailed, 0)
	if err != nil {
		runLog(n.db, run, 0, logError, "telling user %d that it failed couldn't be claimed: %v", who.Int64, err)
		return
	}
	if id != 0 {
		n.deliver(id)
	}
}

// Fails the reviews waiting past their time limits, reminds of those due,
// and sends what replicas that stopped claimed but never sent, every
// interval, until ctx ends.
func (n *notifier) watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, f := range []struct {
			what string
			do   func(context.Context) error
		}{{"fail the reviews past their time limits", n.expire}, {"remind of the reviews due", n.remind},
			{"hand on the reviews due", n.escalate}, {"send what stopped replicas claimed", n.resend}} {
			if err := f.do(ctx); err != nil && ctx.Err() == nil {
				log.Printf("WARNING: couldn't %s: %v", f.what, err)
			}
		}
	}
}

// Fails each review waiting past its time limit, which only one replica
// gets to do, as only one gets to decide it; the replica walking its run
// then handles its failure.
func (n *notifier) expire(ctx context.Context) error {
	type due struct {
		step, run int64
		limit     int
	}
	var steps []due
	err := scanAll(ctx, n.db, `SELECT id, run_id, TIMESTAMPDIFF(SECOND, started_at, expires_at) FROM run_steps
		WHERE status = ? AND expires_at <= CURRENT_TIMESTAMP(3) ORDER BY expires_at LIMIT 100`, func(rows *sql.Rows) error {
		var d due
		err := rows.Scan(&d.step, &d.run, &d.limit)
		steps = append(steps, d)
		return err
	}, stepWaiting)
	if err != nil {
		return err
	}
	for _, d := range steps {
		msg := fmt.Sprintf("It waited longer than its time limit, %s, with nobody deciding.", spokenDuration(d.limit))
		res, err := n.db.ExecContext(ctx,
			"UPDATE run_steps SET status = ?, error = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ? AND status = ?",
			stepError, msg, d.step, stepWaiting)
		if err != nil {
			return err
		}
		if k, err := res.RowsAffected(); err != nil || k == 0 {
			continue
		}
		logStepFinished(n.db, d.step, false)
		n.eng.decide(d.run, d.step, decision{expired: msg})
	}
	return nil
}

// Reminds those who may decide each review due to be reminded of, every
// remindAfter of its process as its run's board has it. Counting the round
// and moving the next on, from now, so a replica that was down doesn't send
// the rounds it missed all at once, claims it, along with the claims for
// each person, so a replica stopping in between leaves both or neither. A
// round falling at or after the review's time limit isn't kept, and none is
// claimed once the review has passed it.
func (n *notifier) remind(ctx context.Context) error {
	type due struct {
		step, run, process int64
		reminded           int
	}
	var steps []due
	err := scanAll(ctx, n.db, `SELECT id, run_id, COALESCE(process_ref, 0), reminded FROM run_steps
		WHERE status = ? AND remind_at <= CURRENT_TIMESTAMP(3) ORDER BY remind_at LIMIT 100`, func(rows *sql.Rows) error {
		var d due
		err := rows.Scan(&d.step, &d.run, &d.process, &d.reminded)
		steps = append(steps, d)
		return err
	}, stepWaiting)
	if err != nil {
		return err
	}
	boards := map[int64]*runBoard{}
	for _, d := range steps {
		b, ok := boards[d.run]
		if !ok {
			if b, err = runBoardOf(ctx, n.db, d.run); err != nil {
				runLog(n.db, d.run, d.step, logError, "how often step %d reminds couldn't be read: %v", d.step, err)
				continue
			}
			boards[d.run] = b
		}
		// Without one, as a version from before a setting went, it reminds no more.
		every := b.processes[d.process].Settings.RemindAfter
		round := d.reminded + 1
		var claims []int64
		err := inTx(ctx, n.db, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE run_steps SET reminded = reminded + 1,
				remind_at = IF(? IS NULL OR (expires_at IS NOT NULL AND CURRENT_TIMESTAMP(3) + INTERVAL ? SECOND >= expires_at), NULL, CURRENT_TIMESTAMP(3) + INTERVAL ? SECOND)
				WHERE id = ? AND status = ? AND reminded = ? AND remind_at IS NOT NULL AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP(3))`,
				every, every, every, d.step, stepWaiting, d.reminded)
			if err != nil {
				return err
			}
			if k, err := res.RowsAffected(); err != nil || k == 0 || !n.signIn {
				return err
			}
			people, err := deciders(ctx, n.db, d.step)
			if err != nil {
				return err
			}
			for _, p := range people {
				id, err := claimNotice(ctx, tx, d.run, d.step, p, noticeReminder, round)
				if err != nil {
					return err
				}
				if id != 0 {
					claims = append(claims, id)
				}
			}
			return nil
		})
		if err != nil {
			runLog(n.db, d.run, d.step, logError, "the reminder of step %d couldn't be claimed: %v", d.step, err)
			continue
		}
		go func() {
			for _, id := range claims {
				n.deliver(id)
			}
		}()
	}
	return nil
}

// Hands on each review due to be, as its process's escalateAfter says,
// to the roles its escalateTo names, who may then decide it too. Marking
// it handed on claims it, along with the claims telling those who may now
// decide it and couldn't before, so a replica stopping in between leaves
// both or neither, and only one replica hands it on. One past its time
// limit isn't handed on.
func (n *notifier) escalate(ctx context.Context) error {
	type due struct {
		step, run, process int64
		waited             int
	}
	var steps []due
	err := scanAll(ctx, n.db, `SELECT id, run_id, COALESCE(process_ref, 0), TIMESTAMPDIFF(SECOND, started_at, escalate_at) FROM run_steps
		WHERE status = ? AND escalate_at <= CURRENT_TIMESTAMP(3) ORDER BY escalate_at LIMIT 100`, func(rows *sql.Rows) error {
		var d due
		err := rows.Scan(&d.step, &d.run, &d.process, &d.waited)
		steps = append(steps, d)
		return err
	}, stepWaiting)
	if err != nil {
		return err
	}
	for _, d := range steps {
		var claims []int64
		handed := false
		err := inTx(ctx, n.db, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE run_steps SET escalated = TRUE, escalate_at = NULL
				WHERE id = ? AND status = ? AND escalate_at IS NOT NULL AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP(3))`,
				d.step, stepWaiting)
			if err != nil {
				return err
			}
			if k, err := res.RowsAffected(); err != nil || k == 0 {
				return err
			}
			handed = true
			if !n.signIn {
				return nil
			}
			before, err := decidersAs(ctx, n.db, d.step, false)
			if err != nil {
				return err
			}
			after, err := decidersAs(ctx, n.db, d.step, true)
			if err != nil {
				return err
			}
			for _, p := range after {
				if slices.Contains(before, p) {
					continue
				}
				id, err := claimNotice(ctx, tx, d.run, d.step, p, noticeEscalated, 0)
				if err != nil {
					return err
				}
				if id != 0 {
					claims = append(claims, id)
				}
			}
			return nil
		})
		if err != nil {
			runLog(n.db, d.run, d.step, logError, "handing step %d on couldn't be claimed: %v", d.step, err)
			continue
		}
		if handed {
			roles := "the roles it escalates to"
			if b, err := runBoardOf(ctx, n.db, d.run); err == nil && len(b.processes[d.process].Settings.EscalateTo) > 0 {
				roles = quotedList(b.processes[d.process].Settings.EscalateTo)
			}
			runLog(n.db, d.run, d.step, logInfo, "handed on to %s too, as nobody decided it in %s", roles, spokenDuration(d.waited))
		}
		go func() {
			for _, id := range claims {
				n.deliver(id)
			}
		}()
	}
	return nil
}

// Sends what replicas that stopped claimed but never sent, each claimed
// again first, so only one replica sends it.
func (n *notifier) resend(ctx context.Context) error {
	orphaned := "sent_at IS NULL AND replica_id NOT IN (SELECT id FROM replicas)"
	var ids []int64
	err := scanAll(ctx, n.db, "SELECT id FROM notifications WHERE "+orphaned+" ORDER BY id LIMIT 100", func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		ids = append(ids, id)
		return err
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		res, err := n.db.ExecContext(ctx, "UPDATE notifications SET replica_id = ?, claimed_at = CURRENT_TIMESTAMP(3) WHERE id = ? AND "+orphaned, replicaID, id)
		if err != nil {
			return err
		}
		if k, err := res.RowsAffected(); err == nil && k > 0 {
			go n.deliver(id)
		}
	}
	return nil
}

// Whom a message is to, and on what they want to be told.
type noticePerson struct {
	email, name         string
	byEmail, bySlack    bool
	ofFailures, removed bool
}

func (p noticePerson) label() string {
	if p.name != "" {
		return p.name
	}
	return p.email
}

// What a message says: its subject and text, for an email, and the same
// in Slack's mrkdwn; what it is about, for the run's log.
type notice struct {
	subject, text, slack string
	about                string
}

// Sends the message claimed, on the channels its person wants, logging what
// went wrong in the run's log and the server's, and records it sent; one
// about a step no longer waiting goes nowhere.
func (n *notifier) deliver(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()
	var run, step, person int64
	var kind string
	if err := n.db.QueryRowContext(ctx, "SELECT run_id, step_id, user_id, kind FROM notifications WHERE id = ?", id).Scan(&run, &step, &person, &kind); err != nil {
		log.Printf("WARNING: couldn't read message %d to send it: %v", id, err)
		return
	}
	defer func() {
		if _, err := n.db.ExecContext(context.Background(), "UPDATE notifications SET sent_at = CURRENT_TIMESTAMP(3) WHERE id = ?", id); err != nil {
			log.Printf("WARNING: couldn't record message %d as sent, so it may go again: %v", id, err)
		}
	}()
	var p noticePerson
	err := n.db.QueryRowContext(ctx, "SELECT email, name, notify_email, notify_slack, notify_failures, removed_at IS NOT NULL FROM users WHERE id = ?", person).
		Scan(&p.email, &p.name, &p.byEmail, &p.bySlack, &p.ofFailures, &p.removed)
	if errors.Is(err, sql.ErrNoRows) || p.removed || (kind == noticeFailed && !p.ofFailures) {
		return
	}
	if err != nil {
		runLog(n.db, run, step, logError, "who to tell couldn't be read: %v", err)
		return
	}
	var m notice
	var ok bool
	if kind == noticeFailed {
		m, err = n.failedNotice(ctx, run)
		ok = err == nil
	} else {
		m, ok, err = n.waitingNotice(ctx, step, kind)
	}
	if err != nil {
		runLog(n.db, run, step, logError, "what to tell %s couldn't be put together: %v", p.label(), err)
		return
	}
	if !ok {
		return
	}
	var told []string
	if p.byEmail && n.mail != nil {
		if err := n.mail.send(ctx, p.email, m.subject, m.text); err != nil {
			runLog(n.db, run, step, logError, "couldn't email %s that %s: %v", p.label(), m.about, err)
		} else {
			told = append(told, "email")
		}
	}
	if p.bySlack {
		c, installed, err := slackNotifier(ctx, n.db)
		if err == nil && installed {
			err = slackDirect(ctx, c, p.email, m.slack)
		}
		if err != nil {
			runLog(n.db, run, step, logError, "couldn't tell %s on Slack that %s: %v", p.label(), m.about, err)
		} else if installed {
			told = append(told, "Slack")
		}
	}
	if len(told) > 0 {
		runLog(n.db, run, step, logInfo, "told %s by %s that %s", p.label(), strings.Join(told, " and "), m.about)
	}
}

// What a review, or a loop question, waiting tells those who may decide it,
// reminds them of, or tells those it is handed on to, as kind says, with
// the process as its run's board has it; false if it no longer waits.
func (n *notifier) waitingNotice(ctx context.Context, step int64, kind string) (notice, bool, error) {
	var w waitingStep
	var status string
	var process sql.NullInt64
	err := n.db.QueryRowContext(ctx,
		`SELECT s.status, s.run_id, r.flow_id, f.name, s.process_ref, s.process_name, s.kind, s.prompt, s.started_at, s.expires_at
		FROM run_steps s JOIN runs r ON r.id = s.run_id JOIN flows f ON f.id = r.flow_id
		WHERE s.id = ?`, step).
		Scan(&status, &w.run, &w.flow, &w.flowName, &process, &w.process, &w.kind, &w.question, &w.since, &w.expires)
	if err != nil || status != stepWaiting {
		return notice{}, false, err
	}
	w.waits = waitsLoop
	if w.kind == kindHuman {
		b, err := runBoardOf(ctx, n.db, w.run)
		if err != nil {
			return notice{}, false, err
		}
		w.waits, w.instructions = reviewWaits(b, process.Int64), b.processes[process.Int64].Prompt
		if kind == noticeEscalated {
			w.handedTo = b.processes[process.Int64].Settings.EscalateTo
		}
	}
	return w.notice(n.base+reviewPath(w.flow, w.run, step), n.base+"/#/settings/account", kind == noticeReminder), true, nil
}

// Where the board opens on a run with a step's review showing.
func reviewPath(flow, run, step int64) string {
	return fmt.Sprintf("/#/flows/%d/runs/%d/steps/%d", flow, run, step)
}

// A step waiting for a person, as they are told of it.
type waitingStep struct {
	run, flow               int64
	flowName, process, kind string
	waits                   string
	// The loop step's question, and the human process's instructions.
	question, instructions string
	since                  time.Time
	expires                *time.Time
	// The roles it was handed on to, telling those holding them; none for
	// any other message.
	handedTo []string
}

// What a review of the process, as the board has it, waits for: a decision
// between states, or only what the person fills in.
func reviewWaits(b *runBoard, process int64) string {
	fills := false
	for _, l := range b.links[process] {
		if l.Direction != directionOutput {
			continue
		}
		if len(b.linkStates[l.ID]) > 0 {
			return waitsReview
		}
		fills = fills || b.fillsIn(l)
	}
	if fills {
		return waitsInput
	}
	return waitsReview
}

// The message telling someone the step waits on them, or reminding them,
// linking to link, and to settings to turn such messages off.
func (w waitingStep) notice(link, settings string, reminder bool) notice {
	name, flow := strconv.Quote(w.process), strconv.Quote(w.flowName)
	var what, open string
	switch w.waits {
	case waitsLoop:
		what, open = "asks whether it goes on round a loop", "Answer it"
	case waitsInput:
		what, open = "waits for you to fill it in", "Fill it in"
	default:
		what, open = "waits for your review", "Open the review"
	}
	if reminder {
		what = "still " + what
		if w.waits == waitsLoop {
			what = "still asks whether it goes on round a loop"
		}
	}
	where := fmt.Sprintf("in run %d of %s", w.run, flow)
	m := notice{subject: fmt.Sprintf("%s %s, %s", name, what, where), about: fmt.Sprintf("%s %s", name, what)}
	switch {
	case reminder:
		m.subject = "Reminder: " + m.subject
	case len(w.handedTo) > 0:
		m.subject = "Handed on to you: " + m.subject
		m.about += ", handed on"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s, %s, since %s.\n", name, what, where, spokenTime(w.since))
	if len(w.handedTo) > 0 {
		fmt.Fprintf(&b, "Nobody decided it in time, so it is handed on to %s too.\n", quotedList(w.handedTo))
	}
	if w.expires != nil {
		fmt.Fprintf(&b, "If nobody decides it by %s, it fails.\n", spokenTime(*w.expires))
	}
	if w.waits == waitsLoop && w.question != "" {
		fmt.Fprintf(&b, "\n%s\n", w.question)
	} else if s := strings.TrimSpace(w.instructions); s != "" {
		fmt.Fprintf(&b, "\nWhat to do:\n%s\n", cutShort(s, 2000))
	}
	fmt.Fprintf(&b, "\n%s: %s\n\nYou are told as you may decide it. Say what you are told of, and how, in your settings: %s\n", open, link, settings)
	m.text = b.String()
	m.slack = fmt.Sprintf("*%s* %s, %s. <%s|%s>", slackEscape(name), slackEscape(what), slackEscape(where), link, open)
	if len(w.handedTo) > 0 {
		m.slack = "Handed on to you: " + m.slack
	}
	return m
}

// What a run failing tells whoever started it.
func (n *notifier) failedNotice(ctx context.Context, run int64) (notice, error) {
	var flow int64
	var name string
	var finished sql.NullTime
	err := n.db.QueryRowContext(ctx, "SELECT r.flow_id, f.name, r.finished_at FROM runs r JOIN flows f ON f.id = r.flow_id WHERE r.id = ?", run).
		Scan(&flow, &name, &finished)
	if err != nil {
		return notice{}, err
	}
	var failures []string
	err = scanAll(ctx, n.db, "SELECT process_name, error FROM run_steps WHERE run_id = ? AND status = ? ORDER BY id LIMIT 3", func(rows *sql.Rows) error {
		var p, e string
		if err := rows.Scan(&p, &e); err != nil {
			return err
		}
		line := strconv.Quote(p) + " failed"
		if e = strings.TrimSpace(e); e != "" {
			line += ": " + cutShort(e, 500)
		}
		failures = append(failures, line)
		return nil
	}, run, stepError)
	if err != nil {
		return notice{}, err
	}
	return failedRunNotice(run, name, finished, failures, n.base+fmt.Sprintf("/#/flows/%d/runs/%d", flow, run), n.base+"/#/settings/account"), nil
}

func failedRunNotice(run int64, flowName string, finished sql.NullTime, failures []string, link, settings string) notice {
	what := fmt.Sprintf("run %d of %s failed", run, strconv.Quote(flowName))
	m := notice{subject: "Run " + strings.TrimPrefix(what, "run "), about: what}
	var b strings.Builder
	fmt.Fprintf(&b, "Run %d of %s, which you started, failed", run, strconv.Quote(flowName))
	if finished.Valid {
		fmt.Fprintf(&b, " at %s", spokenTime(finished.Time))
	}
	b.WriteString(".\n")
	if len(failures) > 0 {
		b.WriteString("\n" + strings.Join(failures, "\n") + "\n")
	}
	fmt.Fprintf(&b, "\nOpen the run: %s\n\nYou are told as you asked to be of your runs failing. Say what you are told of, and how, in your settings: %s\n", link, settings)
	m.text = b.String()
	m.slack = fmt.Sprintf("Run %d of *%s*, which you started, failed. <%s|Open the run>", run, slackEscape(strconv.Quote(flowName)), link)
	return m
}

// The text cut down to at most n bytes, and a character, where it is longer,
// saying so.
func cutShort(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// A time as messages give it, in UTC.
func spokenTime(t time.Time) string {
	return t.UTC().Format("2 Jan 2006 15:04 UTC")
}

// A number of seconds in days, hours, minutes and seconds, such as "1 day
// and 4 hours".
func spokenDuration(seconds int) string {
	var parts []string
	for _, u := range []struct {
		size int
		name string
	}{{24 * 60 * 60, "day"}, {60 * 60, "hour"}, {60, "minute"}, {1, "second"}} {
		if q := seconds / u.size; q > 0 {
			part := fmt.Sprintf("%d %s", q, u.name)
			if q > 1 {
				part += "s"
			}
			parts = append(parts, part)
			seconds -= q * u.size
		}
	}
	switch len(parts) {
	case 0:
		return "0 seconds"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// Whether people are told by email, and on Slack, as an admin set them up.
func (n *notifier) channels(ctx context.Context) (email, slack bool, err error) {
	if n == nil {
		return false, false, nil
	}
	_, slack, err = slackNotifier(ctx, n.db)
	return n.mail != nil, slack, err
}
