package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestSpokenDuration(t *testing.T) {
	for in, want := range map[int]string{
		0:                     "0 seconds",
		1:                     "1 second",
		90:                    "1 minute and 30 seconds",
		3600:                  "1 hour",
		4 * 3600:              "4 hours",
		86400:                 "1 day",
		2*86400 + 4*3600 + 60: "2 days, 4 hours and 1 minute",
	} {
		if got := spokenDuration(in); got != want {
			t.Errorf("spokenDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCutShort(t *testing.T) {
	if got := cutShort("short", 10); got != "short" {
		t.Errorf("cutShort = %q", got)
	}
	// Never in the middle of a character.
	if got := cutShort("ab“cd", 3); got != "ab…" {
		t.Errorf("cutShort = %q", got)
	}
}

// What people are told of a step waiting on them names the process, the
// run and its flow, says what it waits for and since when, and links to it.
func TestWaitingNotice(t *testing.T) {
	since := time.Date(2026, 10, 26, 10, 4, 0, 0, time.UTC)
	expires := since.Add(48 * time.Hour)
	w := waitingStep{run: 42, flow: 7, flowName: "Payments", process: "Approve <payment>", kind: kindHuman, waits: waitsReview,
		instructions: "Check the amount.", since: since, expires: &expires}
	m := w.notice("https://spoletta.example.com/#/flows/7/runs/42/steps/9", "https://spoletta.example.com/#/settings/account", false)
	if m.subject != `"Approve <payment>" waits for your review, in run 42 of "Payments"` {
		t.Errorf("subject = %q", m.subject)
	}
	for _, want := range []string{"since 26 Oct 2026 10:04 UTC", "by 28 Oct 2026 10:04 UTC, it fails", "Check the amount.",
		"Open the review: https://spoletta.example.com/#/flows/7/runs/42/steps/9", "#/settings/account"} {
		if !strings.Contains(m.text, want) {
			t.Errorf("text lacks %q:\n%s", want, m.text)
		}
	}
	if !strings.Contains(m.slack, `&lt;payment&gt;`) || !strings.Contains(m.slack, "<https://spoletta.example.com/#/flows/7/runs/42/steps/9|Open the review>") {
		t.Errorf("slack = %q", m.slack)
	}

	w.waits, w.question, w.expires = waitsLoop, `"Fix" has run 5 times round a loop. Should it go on?`, nil
	m = w.notice("https://x/#/flows/7/runs/42/steps/9", "https://x/#/settings/account", true)
	if !strings.HasPrefix(m.subject, `Reminder: "Approve <payment>" still asks whether it goes on round a loop`) {
		t.Errorf("reminder subject = %q", m.subject)
	}
	if !strings.Contains(m.text, "Should it go on?") || strings.Contains(m.text, "Check the amount.") || strings.Contains(m.text, "it fails") {
		t.Errorf("loop text:\n%s", m.text)
	}

	w.waits = waitsInput
	if m := w.notice("l", "s", false); !strings.Contains(m.subject, "waits for you to fill it in") {
		t.Errorf("input subject = %q", m.subject)
	}

	// Those a review is handed on to are told so.
	w.waits, w.handedTo = waitsReview, []string{"Lead", "Head of finance"}
	m = w.notice("l", "s", false)
	if !strings.HasPrefix(m.subject, `Handed on to you: "Approve <payment>" waits for your review`) || !strings.HasPrefix(m.slack, "Handed on to you: ") {
		t.Errorf("handed on subject = %q, slack = %q", m.subject, m.slack)
	}
	if !strings.Contains(m.text, `handed on to "Lead" and "Head of finance" too`) {
		t.Errorf("handed on text:\n%s", m.text)
	}
}

func TestFailedRunNotice(t *testing.T) {
	at := sql.NullTime{Time: time.Date(2026, 10, 26, 10, 30, 0, 0, time.UTC), Valid: true}
	m := failedRunNotice(42, "Payments", at, []string{`"Fix" failed: it broke`}, "https://x/#/flows/7/runs/42", "https://x/#/settings/account")
	if m.subject != `Run 42 of "Payments" failed` {
		t.Errorf("subject = %q", m.subject)
	}
	for _, want := range []string{"which you started, failed at 26 Oct 2026 10:30 UTC", `"Fix" failed: it broke`, "Open the run: https://x/#/flows/7/runs/42"} {
		if !strings.Contains(m.text, want) {
			t.Errorf("text lacks %q:\n%s", want, m.text)
		}
	}
}

// Messages link to PUBLIC_URL, or to the webapp on this machine without it.
func TestNotifierLinksToThePublicURL(t *testing.T) {
	t.Setenv("PORT", "8123")
	t.Setenv("PUBLIC_URL", "")
	if n := newNotifier(nil, true, nil, nil); n.base != "http://localhost:8123" {
		t.Errorf("without PUBLIC_URL, links to %q", n.base)
	}
	t.Setenv("PUBLIC_URL", "https://spoletta.example.com/")
	if n := newNotifier(nil, true, nil, nil); n.base != "https://spoletta.example.com" {
		t.Errorf("links to %q", n.base)
	}
}
