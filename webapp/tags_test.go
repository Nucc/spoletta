package main

import (
	"slices"
	"strings"
	"testing"
)

// Tags are trimmed, their spaces made single, and each kept once whatever
// its case, in the order of their names; empty ones go.
func TestCleanTags(t *testing.T) {
	got, msg := cleanTags([]string{"  nightly ", "Billing", "billing", "", "  ", "team  payments", "API"})
	if want := []string{"API", "Billing", "nightly", "team payments"}; msg != "" || !slices.Equal(got, want) {
		t.Errorf("cleanTags = %q, %q, want %q", got, msg, want)
	}
	if got, msg := cleanTags(nil); msg != "" || got == nil || len(got) != 0 {
		t.Errorf("cleanTags(nil) = %#v, %q, want none", got, msg)
	}
	if _, msg := cleanTags([]string{strings.Repeat("é", maxTagLength)}); msg != "" {
		t.Errorf("a tag of %d characters is refused: %s", maxTagLength, msg)
	}
	if _, msg := cleanTags([]string{strings.Repeat("x", maxTagLength+1)}); !strings.Contains(msg, "longer than") {
		t.Errorf("a long tag = %q", msg)
	}
	many := make([]string, maxTags+1)
	for i := range many {
		many[i] = strings.Repeat("t", i+1)
	}
	if _, msg := cleanTags(many); !strings.Contains(msg, "at most") {
		t.Errorf("too many tags = %q", msg)
	}
	// The same twice over is one.
	if got, msg := cleanTags(append(many[:maxTags], many[0])); msg != "" || len(got) != maxTags {
		t.Errorf("%d tags and one again = %d, %q", maxTags, len(got), msg)
	}
}
