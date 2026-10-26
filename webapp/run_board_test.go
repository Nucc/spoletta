package main

import (
	"reflect"
	"testing"
)

// Write drafts an article from the brief, which an editor reviews, approving
// or rejecting it, or filling in a note; the article is a persistent object
// with a unique slug, specializing a page with a unique path.
func articleBoard() snapshot {
	return snapshot{
		Processes: []process{
			{ID: 1, Name: "Write", Kind: kindAgent, Prompt: "Write it."},
			{ID: 2, Name: "Review", Kind: kindHuman},
		},
		Objects: []snapshotObject{
			{ID: 10, Name: "Brief"},
			{ID: 11, Name: "Article", Persistent: true, General: versionID(13)},
			{ID: 12, Name: "Editor", Human: true},
			{ID: 13, Name: "Page"},
			{ID: 14, Name: "Note"},
		},
		States: []objectState{{ID: 20, Object: 11, Name: "approved"}, {ID: 21, Object: 11, Name: "rejected"}},
		Attributes: []attribute{
			{ID: 30, Object: 11, Name: "slug", Unique: true},
			{ID: 31, Object: 11, Name: "body"},
			{ID: 32, Object: 13, Name: "path", Unique: true},
		},
		Links: []snapshotLink{
			{ID: 40, Process: 1, Object: 10, Direction: directionInput},
			{ID: 41, Process: 1, Object: 11, Direction: directionOutput},
			{ID: 42, Process: 2, Object: 11, Direction: directionInput},
			{ID: 43, Process: 2, Object: 11, Direction: directionOutput},
			{ID: 44, Process: 2, Object: 12, Direction: directionInput},
			{ID: 45, Process: 2, Object: 14, Direction: directionOutput},
		},
		LinkStates: []snapshotLinkState{{Link: 43, State: 21}, {Link: 43, State: 20}},
	}
}

// A run's board answers what its steps used to read from the board as it
// is: who decides a review, what it decides between and fills in, and what
// a process generates.
func TestRunBoardLookups(t *testing.T) {
	b := newRunBoard(1, articleBoard())

	if got := b.handlers(2); !reflect.DeepEqual(got, []string{"Editor"}) {
		t.Errorf("Review is handled by %v", got)
	}
	if got := b.handlers(1); len(got) != 0 {
		t.Errorf("Write is handled by %v", got)
	}
	if got := b.decisionState(2, 20); got != "approved" {
		t.Errorf("Review decides state 20 as %q", got)
	}
	if got := b.decisionState(1, 20); got != "" {
		t.Errorf("Write, an agent, decides %q", got)
	}
	if got := b.decisionState(2, 99); got != "" {
		t.Errorf("Review decides an unknown state as %q", got)
	}

	// Review fills in the note, not the article it decides on.
	entries, msg := b.entryObjects(2)
	if msg != "" || len(entries) != 1 || entries[0].name != "Note" || !entries[0].data {
		t.Errorf("Review fills in %+v (%q)", entries, msg)
	}
	var outputs []int64
	for _, l := range b.outputLinks(2, kindHuman) {
		outputs = append(outputs, l.Object)
	}
	if !reflect.DeepEqual(outputs, []int64{14}) {
		t.Errorf("a human step of Review generates %v", outputs)
	}

	c, msg := b.contractFor(1, true)
	if msg != "" || c == nil || len(c.shaped) != 1 || c.shaped[0].name != "Article" {
		t.Fatalf("Write's contract = %+v (%q)", c, msg)
	}
	var fields []string
	for _, f := range c.shaped[0].fields {
		fields = append(fields, f.name)
	}
	if !reflect.DeepEqual(fields, []string{"path", "slug", "body"}) {
		t.Errorf("the article's fields = %v, its general's first", fields)
	}

	var unique []string
	for _, a := range b.uniqueAttributes(11) {
		unique = append(unique, a.Name)
	}
	if !reflect.DeepEqual(unique, []string{"path", "slug"}) {
		t.Errorf("the article's unique attributes = %v", unique)
	}
	if got := itemKey(b, 11, `{"slug": "hello", "body": "x"}`, 0); got != "hello" {
		t.Errorf("the item key = %q", got)
	}
	if got := itemKey(b, 11, "not JSON", 2); got != "item 3" {
		t.Errorf("the item key of text = %q", got)
	}
	if _, err := b.process(99); err != errNotFound {
		t.Errorf("an unknown process = %v", err)
	}
}

// An agent deciding states of what it takes reviews it: the contract lists
// the decision and not the object.
func TestRunBoardContractOfAReviewer(t *testing.T) {
	s := articleBoard()
	s.Processes[1].Kind = kindAgent
	b := newRunBoard(1, s)
	c, msg := b.contractFor(2, true)
	if msg != "" || c == nil {
		t.Fatalf("the contract = %+v (%q)", c, msg)
	}
	if len(c.decisions) != 1 || !c.decisions[0].review || len(c.decisions[0].states) != 2 || c.decisions[0].states[0].Name != "approved" {
		t.Errorf("the decisions = %+v", c.decisions)
	}
	for _, o := range c.shaped {
		if o.name == "Article" {
			t.Errorf("the reviewer generates the article it reviews: %+v", c.shaped)
		}
	}
}
