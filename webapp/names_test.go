package main

import (
	"strings"
	"testing"
)

func TestInputFileName(t *testing.T) {
	for name, want := range map[string]string{
		"Jira Ticket":   "Jira_Ticket",
		"Jira  Ticket":  "Jira_Ticket",
		"Jira_Ticket":   "Jira_Ticket",
		"Jira Ticket.":  "Jira_Ticket",
		"Jira-Ticket":   "Jira-Ticket",
		"Pull request?": "Pull_request",
		"***":           "input",
	} {
		if got := inputFileName(name); got != want {
			t.Errorf("inputFileName(%q) = %q, want %q", name, got, want)
		}
	}
}

// A board saved before names were checked still runs, but a step taking two
// objects whose files would be the same fails rather than losing one.
func TestInputClash(t *testing.T) {
	if msg := inputClash("Fetch", []runInput{{Name: "Ticket"}, {Name: "Cursor"}}); msg != "" {
		t.Errorf("distinct inputs clash: %s", msg)
	}
	msg := inputClash("Fetch", []runInput{{Name: "Jira Ticket"}, {Name: "Jira_Ticket"}})
	if !strings.Contains(msg, `"Fetch" takes two objects named "Jira_Ticket"`) {
		t.Errorf("inputClash = %q, want it to name the process and the object", msg)
	}
}
