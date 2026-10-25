package main

import (
	"strconv"
	"strings"
	"testing"
)

// A record a flow's queries wouldn't select says why, from the last query
// that passed it over; one any of them selects says nothing.
func TestOffQuery(t *testing.T) {
	open := linkQuery{State: "open"}
	urgent := linkQuery{Where: `priority == "high"`}
	for _, c := range []struct {
		name    string
		queries []linkQuery
		content string
		state   string
		want    string
	}{
		{"any record", []linkQuery{{}}, `{"id": "A-1"}`, "", ""},
		{"in the state", []linkQuery{open}, `{"id": "A-1"}`, "open", ""},
		{"in another state", []linkQuery{open}, `{"id": "A-1"}`, "done", `it takes records in state "open"`},
		{"in no state", []linkQuery{open}, `{"id": "A-1"}`, "", `it takes records in state "open"`},
		{"the rule holds", []linkQuery{urgent}, `{"priority": "high"}`, "", ""},
		{"the rule doesn't hold", []linkQuery{urgent}, `{"priority": "low"}`, "", `it takes records where priority == "high"`},
		{"one query of two", []linkQuery{open, urgent}, `{"priority": "high"}`, "done", ""},
		{"neither query", []linkQuery{urgent, open}, `{"priority": "low"}`, "done", `it takes records in state "open"`},
		{"a rule that doesn't read", []linkQuery{{Where: "priority =="}}, `{}`, "", "its rule doesn't read"},
	} {
		if got := offQuery(c.queries, c.content, c.state); got != c.want {
			t.Errorf("%s: offQuery = %q, want %q", c.name, got, c.want)
		}
	}
}

// Record ids come comma separated, each once, positive, at most
// maxRecordRuns of them.
func TestRecordIDs(t *testing.T) {
	ids, err := recordIDs(" 3, 1,,3 ,2")
	if err != nil || len(ids) != 3 || ids[0] != 3 || ids[1] != 1 || ids[2] != 2 {
		t.Errorf("recordIDs = %v, %v; want [3 1 2]", ids, err)
	}
	if ids, err := recordIDs(""); err != nil || len(ids) != 0 {
		t.Errorf("no records = %v, %v", ids, err)
	}
	for _, bad := range []string{"1,x", "0", "-4", "1.5"} {
		if _, err := recordIDs(bad); err == nil {
			t.Errorf("recordIDs(%q) took it", bad)
		}
	}
	var many []string
	for i := 1; i <= maxRecordRuns+1; i++ {
		many = append(many, strconv.Itoa(i))
	}
	if _, err := recordIDs(strings.Join(many, ",")); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("%d records: %v, want refused", maxRecordRuns+1, err)
	}
}
