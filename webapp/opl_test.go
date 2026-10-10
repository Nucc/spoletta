package main

import (
	"strings"
	"testing"
)

func TestOPL(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	three, two, ninety := 3, 2, 90
	s := snapshot{
		Processes: []process{
			{ID: 1, Name: "Triage", Kind: kindAgent, Y: 0},
			{ID: 2, Name: "Fix", Kind: kindAgent, Y: 100, Settings: processSettings{Timeout: &ninety, Retries: &two}},
			{ID: 3, Name: "Critic", Kind: kindAgent, Y: 200, Settings: processSettings{Rounds: &three}},
			{ID: 4, Name: "Review", Kind: kindHuman, Y: 300},
			{ID: 5, Name: "Escalate", Kind: kindAgent, Y: 400},
			{ID: 6, Name: "Merge", Kind: kindScript, Y: 500, Settings: processSettings{Quorum: &two}},
			{ID: 7, Name: "Release", Kind: kindFlow, Y: 600},
			{ID: 8, Name: "Tag", Kind: kindScript, Parent: id(7)},
			{ID: 9, Name: "Deploy", Kind: kindSubflow, Y: 700, Settings: processSettings{Flow: id(42)}},
		},
		Objects: []snapshotObject{
			{ID: 10, Name: "Ticket", Persistent: true, End: true},
			{ID: 11, Name: "Guide"},
			{ID: 12, Name: "Patch"},
			{ID: 13, Name: "Editor", Human: true},
			{ID: 14, Name: "Queue", Many: true, Min: 1},
			{ID: 15, Name: "Author", Owner: id(10), Relation: relationAggregation},
			{ID: 16, Name: "Tag name", Parent: id(7)},
		},
		States: []objectState{
			{ID: 20, Object: 10, Name: "bug", Rule: `kind == "bug"`},
			{ID: 21, Object: 10, Name: "feature"},
			{ID: 22, Object: 12, Name: "good"},
			{ID: 23, Object: 12, Name: "weak"},
		},
		Attributes: []attribute{{ID: 30, Object: 10, Name: "title"}, {ID: 31, Object: 15, Name: "email"}},
		Links: []snapshotLink{
			{ID: 40, Process: 1, Object: 10, Direction: directionOutput},
			{ID: 41, Process: 2, Object: 10, Direction: directionInput, State: 20, Condition: true},
			{ID: 42, Process: 2, Object: 11, Direction: directionInput, Role: roleInstrument},
			{ID: 43, Process: 2, Object: 12, Direction: directionOutput},
			{ID: 44, Process: 3, Object: 12, Direction: directionInput},
			{ID: 45, Process: 3, Object: 12, Direction: directionOutput},
			{ID: 46, Process: 4, Object: 13, Direction: directionInput},
			{ID: 47, Process: 4, Object: 12, Direction: directionInput, State: 22},
			{ID: 48, Process: 6, Object: 14, Direction: directionInput, Role: roleConsumption},
			{ID: 49, Process: 8, Object: 16, Direction: directionOutput},
		},
		LinkStates:  []snapshotLinkState{{Link: 40, State: 20}, {Link: 40, State: 21}, {Link: 45, State: 22}, {Link: 45, State: 23}},
		Connections: []connection{{ID: 50, Source: 2, Target: 5, Kind: connectionFailure}, {ID: 51, Source: 6, Target: 7}},
		Start:       startNode{Target: id(1)},
		Boards:      []innerBoard{{Process: 7, Start: startNode{Target: id(8)}}},
	}
	got := opl(s, func(id int64) string {
		if id == 42 {
			return "Deploy prod"
		}
		return ""
	})
	for _, want := range []string{
		`"Ticket" is stored, so each run keeps what it generated of it.`,
		`"Ticket" can be "bug" or "feature".`,
		`"Ticket" is "bug" when kind == "bug".`,
		`"Ticket" exhibits "title".`,
		`"Ticket" consists of "Author".`,
		`"Author" exhibits "email".`,
		`"Editor" is a person.`,
		`"Queue" is a list of items, at least 1.`,
		`"Triage" is an agent process, which an AI agent carries out.`,
		`"Triage" yields "Ticket" as "bug" or "feature", deciding which.`,
		`"Fix" occurs if "Ticket" is "bug", and is skipped otherwise.`,
		`"Fix" requires "Guide", which it doesn't change.`,
		`"Fix" yields "Patch".`,
		`"Fix" fails after 90 seconds.`,
		`"Fix" is tried again up to 2 times when it fails.`,
		`"Escalate" handles a failure of "Fix".`,
		`"Critic" decides whether "Patch" is "good" or "weak".`,
		`"Critic" runs at most 3 rounds.`,
		`"Editor" handles "Review".`,
		`"Patch" entering "good" triggers "Review".`,
		`"Merge" consumes "Queue".`,
		`"Merge" needs 2 of what it takes.`,
		`"Release" follows "Merge".`,
		`"Deploy" is a procedure, which runs the flow "Deploy prod".`,
		`The flow starts with "Triage".`,
		`The flow ends with "Ticket".`,
		"\"Release\" zooms into:\n  \"Tag\" is a script process.",
		`  "Tag" yields "Tag name".`,
		`  "Release" starts with "Tag".`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("OPL lacks %q:\n%s", want, got)
		}
	}
	// What the flow process's board has isn't said on the top board.
	if strings.Contains(got, "\n\"Tag\" is") {
		t.Errorf("an inner process is on the top board:\n%s", got)
	}
	if strings.Contains(got, `"Critic" takes "Patch"`) || strings.Contains(got, `"Critic" yields "Patch"`) {
		t.Errorf("a review reads as taking or yielding:\n%s", got)
	}
}

func TestOPLEffect(t *testing.T) {
	s := snapshot{
		Processes: []process{{ID: 1, Name: "Edit", Kind: kindAgent}},
		Objects:   []snapshotObject{{ID: 2, Name: "Doc"}},
		Links: []snapshotLink{
			{ID: 3, Process: 1, Object: 2, Direction: directionInput},
			{ID: 4, Process: 1, Object: 2, Direction: directionOutput},
		},
	}
	got := opl(s, nil)
	if !strings.Contains(got, `"Edit" affects "Doc".`) || strings.Contains(got, "takes") || strings.Contains(got, "yields") {
		t.Errorf("OPL of an effect:\n%s", got)
	}
}
