package main

import (
	"strings"
	"testing"
	"time"
)

// A period's days end today where the person is, each starting at its
// midnight there: a day the clocks go back on is 25 hours long, and one
// whose midnight they skip starts when they go on, still named after it.
func TestPeriodOf(t *testing.T) {
	budapest, err := time.LoadLocation("Europe/Budapest")
	if err != nil {
		t.Fatal(err)
	}
	// The clocks went back an hour at 03:00 on Sunday 25 October 2026.
	p := periodOf(time.Date(2026, 10, 26, 0, 30, 0, 0, time.UTC), budapest, 7)
	wantDays := []string{"2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-24", "2026-10-25", "2026-10-26"}
	if strings.Join(p.days, " ") != strings.Join(wantDays, " ") || p.zone != "Europe/Budapest" {
		t.Errorf("days in %s = %v, want %v", p.zone, p.days, wantDays)
	}
	for i, want := range []time.Time{
		time.Date(2026, 10, 19, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC),
	} {
		got := p.starts[[]int{0, 5, 6}[i]]
		if !got.Equal(want) {
			t.Errorf("day %d starts at %v, want %v", []int{0, 5, 6}[i], got.UTC(), want)
		}
	}
	if got := p.starts[6].Sub(p.starts[5]); got != 25*time.Hour {
		t.Errorf("the day the clocks went back lasts %v", got)
	}

	// In Santiago the clocks went from 00:00 to 01:00 on 6 September 2026.
	santiago, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	p = periodOf(time.Date(2026, 9, 7, 15, 0, 0, 0, time.UTC), santiago, 3)
	if strings.Join(p.days, " ") != "2026-09-05 2026-09-06 2026-09-07" {
		t.Errorf("days in Santiago = %v", p.days)
	}
	for i := 1; i < len(p.starts); i++ {
		if !p.starts[i].After(p.starts[i-1]) {
			t.Errorf("day %d starts at %v, not after the day before, at %v", i, p.starts[i], p.starts[i-1])
		}
	}

	// UTC days start at midnight UTC, today's included.
	p = periodOf(time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC), time.UTC, 30)
	if len(p.days) != 30 || p.days[0] != "2025-12-03" || p.days[29] != "2026-01-01" || !p.from().Equal(time.Date(2025, 12, 3, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("30 days to 1 January = %v .. %v from %v", p.days[0], p.days[29], p.from())
	}
}

// The database counts the days' starts at or before a time, an argument
// each, as Unix seconds.
func TestPeriodDayOf(t *testing.T) {
	p := periodOf(time.Date(2026, 3, 4, 5, 0, 0, 0, time.UTC), time.UTC, 3)
	sql, args := p.dayOf("r.started_at")
	if sql != "INTERVAL(UNIX_TIMESTAMP(r.started_at), ?, ?, ?)" {
		t.Errorf("dayOf = %s", sql)
	}
	want := []int64{time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC).Unix()}
	for i, a := range args {
		if a != want[i] {
			t.Errorf("argument %d = %v, want %d", i, a, want[i])
		}
	}
}

// A time zone the browser names that the webapp doesn't know is UTC.
func TestActivityZone(t *testing.T) {
	for name, want := range map[string]string{"": "UTC", "Local": "UTC", "Nowhere/Atlantis": "UTC", "Asia/Kathmandu": "Asia/Kathmandu", "UTC": "UTC"} {
		if got := activityZone(name).String(); got != want {
			t.Errorf("activityZone(%q) = %s, want %s", name, got, want)
		}
	}
}
