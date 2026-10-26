package main

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestParseRunFilter(t *testing.T) {
	f, msg := parseRunFilter(url.Values{"status": {"failed,waiting", "failed"}, "started": {"schedule", " api "}, "before": {"40"}, "limit": {"5"}})
	if msg != "" {
		t.Fatal(msg)
	}
	want := runFilter{statuses: []string{"failed", "waiting"}, origins: []string{"schedule", "api"}, before: 40, limit: 5}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("parsed %+v, want %+v", f, want)
	}
	if f, msg := parseRunFilter(url.Values{}); msg != "" || f.limit != runPage || f.statuses != nil || f.before != 0 {
		t.Errorf("no filter parsed as %+v (%s)", f, msg)
	}
	for _, q := range []url.Values{
		{"status": {"stopped"}},
		{"started": {"person,cron"}},
		{"before": {"x"}},
		{"before": {"0"}},
		{"limit": {"0"}},
		{"limit": {"201"}},
	} {
		if _, msg := parseRunFilter(q); msg == "" {
			t.Errorf("%v parsed without complaint", q)
		}
	}
}

func TestRunFilterQuery(t *testing.T) {
	where, args := runFilter{limit: runPage}.query(7)
	if where != " WHERE flow_id = ? ORDER BY id DESC LIMIT ?" || !reflect.DeepEqual(args, []any{int64(7), runPage}) {
		t.Errorf("unfiltered: %q %v", where, args)
	}

	where, args = runFilter{statuses: []string{"waiting", "failed", "done"}, origins: []string{"rerun"}, before: 9, limit: 3}.query(7)
	for _, part := range []string{
		"flow_id = ?",
		"((status = 'running' AND " + runWaiting + ") OR status IN (?, ?))",
		"(" + runOrigin + ") IN (?)",
		"id < ?",
	} {
		if !strings.Contains(where, part) {
			t.Errorf("%q lacks %q", where, part)
		}
	}
	if want := []any{int64(7), "failed", "done", "rerun", int64(9), 3}; !reflect.DeepEqual(args, want) {
		t.Errorf("args %v, want %v", args, want)
	}

	// Running picks the runs going that wait for nobody.
	where, args = runFilter{statuses: []string{"running"}, limit: 1}.query(7)
	if !strings.Contains(where, "((status = 'running' AND NOT "+runWaiting+"))") || len(args) != 2 {
		t.Errorf("running: %q %v", where, args)
	}
}
