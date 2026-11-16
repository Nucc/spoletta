package main

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Schedules run a minute apart at the least: neither a seconds field nor
// @every under a minute is taken, unless SCHEDULE_SECONDS is set.
func TestScheduleMinuteAtTheLeast(t *testing.T) {
	for _, c := range []struct {
		cron, problem string
	}{
		{"* * * * *", ""},
		{"*/5 9-17 * * 1-5", ""},
		{"@hourly", ""},
		{"@every 1m", ""},
		{"@every 90m", ""},
		{"* * * * * *", "invalid cron expression"},
		{"30 * * * * *", "invalid cron expression"},
		{"@every 30s", "a minute apart at the least"},
	} {
		_, _, msg := parseSchedule(c.cron, "UTC")
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("parseSchedule(%q) = %q, want %q", c.cron, msg, c.problem)
		}
	}
}

// A schedule runs at its times in its own time zone, through changes to
// daylight saving time; the times are kept in UTC.
func TestUpcoming(t *testing.T) {
	s, loc, msg := parseSchedule("0 9 * * *", "Europe/Budapest")
	if msg != "" {
		t.Fatal(msg)
	}
	from := time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC)
	want := []time.Time{
		time.Date(2026, 3, 28, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 30, 7, 0, 0, 0, time.UTC),
	}
	got := upcoming(s, loc, from, 3)
	if !slices.Equal(got, want) {
		t.Errorf("upcoming = %v, want %v", got, want)
	}
	for _, ts := range got {
		if ts.Location() != time.UTC {
			t.Errorf("%v is not in UTC", ts)
		}
	}
	if got := upcoming(s, loc, from, 0); len(got) != 0 {
		t.Errorf("upcoming of none = %v", got)
	}
}

// A schedule runs next at its first time after now, unless it is disabled
// or its expression or time zone no longer parse.
func TestNextRun(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC)
	at := func(h, m int) *time.Time {
		ts := time.Date(2026, 10, 16, h, m, 0, 0, time.UTC)
		return &ts
	}
	for _, c := range []struct {
		cron, zone string
		enabled    bool
		want       *time.Time
	}{
		{"*/15 * * * *", "UTC", true, at(9, 45)},
		{"@hourly", "UTC", true, at(10, 0)},
		{"0 12 * * *", "America/New_York", true, at(16, 0)},
		{"*/15 * * * *", "UTC", false, nil},
		{"not cron", "UTC", true, nil},
		{"*/15 * * * *", "Nowhere/Atlantis", true, nil},
		{"*/15 * * * *", "", true, nil},
		{"0 0 30 2 *", "UTC", true, nil},
	} {
		got := nextRun(c.cron, c.zone, c.enabled, now)
		if (got == nil) != (c.want == nil) || got != nil && !got.Equal(*c.want) {
			t.Errorf("nextRun(%q, %q, %v) = %v, want %v", c.cron, c.zone, c.enabled, got, c.want)
		}
	}
}

// An agent's prompt names $SINCE and $LAST_RUN bare, as ${SINCE} or as
// {$SINCE}, and has them filled in as a script gets them: the Unix
// timestamp, or nothing in a run no schedule got through before. A longer
// name, and a command's placeholders, stay as written.
func TestWithSince(t *testing.T) {
	since := int64(1731772800)
	for _, c := range []struct {
		prompt string
		since  *int64
		want   string
	}{
		{"Fetch the latest message since {$LAST_RUN}.", &since, "Fetch the latest message since 1731772800."},
		{"Since {$SINCE}, ${SINCE}, ${LAST_RUN}, $SINCE and $LAST_RUN", &since,
			"Since 1731772800, 1731772800, 1731772800, 1731772800 and 1731772800"},
		{"oldest=$SINCE&limit=20", &since, "oldest=1731772800&limit=20"},
		{"Fetch the latest message since {$LAST_RUN}, or the latest 20 if none.", nil, "Fetch the latest message since , or the latest 20 if none."},
		{"$SINCE_DAYS, $LAST_RUNS, ${SINCE_X}, $since, $1, ${1:-x} and $@", &since, "$SINCE_DAYS, $LAST_RUNS, ${SINCE_X}, $since, $1, ${1:-x} and $@"},
		{"No variables here.", &since, "No variables here."},
	} {
		if got := withSince(c.prompt, c.since); got != c.want {
			t.Errorf("withSince(%q) = %q, want %q", c.prompt, got, c.want)
		}
	}
}
