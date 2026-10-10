package main

import (
	"strings"
	"testing"
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
