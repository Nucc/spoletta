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

// A flow leaves the records it takes where storeShared moves them: the
// states an output link of a process also taking the object lists, else
// the one it names, else those of the object's states with a rule; a
// process only generating the object, a link naming no state of an object
// without rules, and another store's object leave them where they were.
func TestRecordLeaves(t *testing.T) {
	const store = 100
	shared, other := int64(store), int64(200)
	// The board's copy of the struct (1), another of it the flow only
	// generates (2), and another struct's (3).
	objects := []snapshotObject{{ID: 1, Store: &shared}, {ID: 2, Store: &shared}, {ID: 3, Store: &other}}
	states := []objectState{
		{ID: 11, Object: 1, Name: "new"}, {ID: 12, Object: 1, Name: "triaged"}, {ID: 13, Object: 1, Name: "fixed", Rule: `fix != ""`},
		{ID: 14, Object: 1, Name: "wontfix", Rule: "fix =="},
		{ID: 21, Object: 2, Name: "new"}, {ID: 22, Object: 2, Name: "closed"},
		{ID: 31, Object: 3, Name: "done"},
	}
	takes := func(id, process, object int64) snapshotLink {
		return snapshotLink{ID: id, Process: process, Object: object, Direction: directionInput}
	}
	gives := func(id, process, object, state int64) snapshotLink {
		return snapshotLink{ID: id, Process: process, Object: object, Direction: directionOutput, State: state}
	}
	for _, c := range []struct {
		name   string
		links  []snapshotLink
		listed []snapshotLinkState
		want   string
	}{
		{"the state it names", []snapshotLink{takes(1, 1, 1), gives(2, 1, 1, 12)}, nil, "triaged"},
		{"the states it decides between, in the board's order", []snapshotLink{takes(1, 1, 1), gives(2, 1, 1, 0)},
			[]snapshotLinkState{{2, 13}, {2, 11}}, "new, fixed"},
		{"states with rules that read", []snapshotLink{takes(1, 1, 1), gives(2, 1, 1, 0)}, nil, "fixed"},
		{"two processes", []snapshotLink{takes(1, 1, 1), gives(2, 1, 1, 12), takes(3, 2, 1), gives(4, 2, 1, 11)}, nil, "new, triaged"},
		{"only generating it", []snapshotLink{takes(1, 1, 3), gives(2, 1, 1, 12)}, nil, ""},
		{"no state and no rules", []snapshotLink{takes(1, 1, 2), gives(2, 1, 2, 0)}, nil, ""},
		{"taking another copy", []snapshotLink{takes(1, 1, 1), gives(2, 1, 2, 22)}, nil, ""},
		{"another store", []snapshotLink{takes(1, 1, 3), gives(2, 1, 3, 31)}, nil, ""},
	} {
		b := newRunBoard(1, snapshot{Objects: objects, States: states, Links: c.links, LinkStates: c.listed})
		got := recordLeaves(b, store)
		if got == nil || strings.Join(got, ", ") != c.want {
			t.Errorf("%s: recordLeaves = %#v, want %q", c.name, got, c.want)
		}
	}
}
