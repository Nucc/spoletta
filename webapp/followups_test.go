package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A follow-up's message goes to the agent tagged, and over what it was told before.
func TestFollowUpPrompt(t *testing.T) {
	got := followUpPrompt("Push it.")
	for _, want := range []string{"same workspace, on the same branch", "takes precedence", "\n\n<message>\nPush it.\n</message>\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("followUpPrompt = %q, want it to hold %q", got, want)
		}
	}
}

// Why a follow-up can't start is told apart from other errors, even wrapped.
func TestConflictError(t *testing.T) {
	err := fmt.Errorf("start: %w", conflictError("wait until it is over"))
	var conflict conflictError
	if !errors.As(err, &conflict) || string(conflict) != "wait until it is over" || conflict.Error() != "wait until it is over" {
		t.Errorf("errors.As(%v) = %q", err, conflict)
	}
	if errors.As(errNotFound, &conflict) {
		t.Error("errNotFound reads as a conflict")
	}
}
