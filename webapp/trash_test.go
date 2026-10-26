package main

import (
	"reflect"
	"testing"
	"time"
)

// TRASH_KEEP is a number of days, or a duration.
func TestParseKeep(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"30d", 30 * 24 * time.Hour, true},
		{" 7d ", 7 * 24 * time.Hour, true},
		{"0.5d", 12 * time.Hour, true},
		{"12h", 12 * time.Hour, true},
		{"2s", 2 * time.Second, true},
		{"0d", 0, false},
		{"-1d", 0, false},
		{"d", 0, false},
		{"30", 0, false},
		{"forever", 0, false},
		{"9999d", 0, false},
	} {
		got, err := parseKeep(c.in)
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Errorf("parseKeep(%q) = %v, %v; want %v, ok %v", c.in, got, err, c.want, c.ok)
		}
	}
}

func TestKeepLabel(t *testing.T) {
	for keep, want := range map[time.Duration]string{30 * 24 * time.Hour: "30 days", 24 * time.Hour: "a day", 36 * time.Hour: "36h0m0s", 2 * time.Second: "2s"} {
		if got := keepLabel(keep); got != want {
			t.Errorf("keepLabel(%v) = %q, want %q", keep, got, want)
		}
	}
}

// The trash is swept a tenth as often as it keeps what is in it, between a
// second and a minute.
func TestTrashSweepEvery(t *testing.T) {
	for keep, want := range map[time.Duration]time.Duration{
		30 * 24 * time.Hour: time.Minute,
		5 * time.Minute:     30 * time.Second,
		2 * time.Second:     time.Second,
	} {
		if got := trashSweepEvery(keep); got != want {
			t.Errorf("trashSweepEvery(%v) = %v, want %v", keep, got, want)
		}
	}
}

// Someone seeing every flow is held to those out of the trash by leaving
// those in it out; anyone else by those they see.
func TestOfFlows(t *testing.T) {
	for _, c := range []struct {
		seen flowSet
		cond string
		args []any
	}{
		{flowSet{}, "TRUE", nil},
		{flowSet{trashed: []int64{}}, "TRUE", nil},
		{flowSet{trashed: []int64{4, 5}}, "r.flow_id NOT IN (?, ?)", []any{int64(4), int64(5)}},
		{flowSet{ids: []int64{1}}, "r.flow_id IN (?)", []any{int64(1)}},
		{flowSet{ids: []int64{}}, "r.flow_id IN (SELECT NULL FROM DUAL WHERE FALSE)", []any{}},
	} {
		cond, args := ofFlows("r.flow_id", c.seen)
		if cond != c.cond || (len(args) > 0 || len(c.args) > 0) && !reflect.DeepEqual(args, c.args) {
			t.Errorf("ofFlows(%+v) = %q, %v; want %q, %v", c.seen, cond, args, c.cond, c.args)
		}
	}
}
