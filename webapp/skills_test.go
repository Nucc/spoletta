package main

import (
	"slices"
	"testing"
)

// A command is spelled out where it stands as one, at the start of a line
// or after a space, taking the rest of its line when it has placeholders; a
// /word naming none stays.
func TestExpandLine(t *testing.T) {
	commands := map[string]string{
		"review": "Review the change. Focus on ${1:-correctness}.\n",
		"tone":   "Write plainly.",
		"greet":  "Hello $1 and ${@:2}; all: $@ $ARGUMENTS ${@:2:1} $3",
	}
	for _, c := range []struct{ line, want string }{
		{"/review", "Review the change. Focus on correctness."},
		{"/review security", "Review the change. Focus on security."},
		{`Then /review "API compatibility" please`, "Then Review the change. Focus on API compatibility."},
		{"/tone Then go on.", "Write plainly. Then go on."},
		{"Mind the /tone.", "Mind the Write plainly.."},
		{"/tone /tone", "Write plainly. Write plainly."},
		{"/greet ann bob 'carl d'", "Hello ann and bob carl d; all: ann bob carl d ann bob carl d bob carl d"},
		{"Read /etc/hosts and /usr/bin", "Read /etc/hosts and /usr/bin"},
		{"https://example.com/review and and/or /review-x", "https://example.com/review and and/or /review-x"},
		{"/Review /tones", "/Review /tones"},
		{"no commands", "no commands"},
		{"Run /secret, then /tmp.", "Run /secret, then /tmp."},
	} {
		if got := expandLine(c.line, commands); got != c.want {
			t.Errorf("%q gave %q, want %q", c.line, got, c.want)
		}
	}
}

// Arguments split at spaces outside quotes, and placeholders follow pi's.
func TestSubstituteArgs(t *testing.T) {
	args := commandArguments(` a "b c"  'd' `)
	if !slices.Equal(args, []string{"a", "b c", "d"}) {
		t.Fatalf("args = %q", args)
	}
	for _, c := range []struct{ content, want string }{
		{"$1|$2|$3|$4", "a|b c|d|"},
		{"${4:-none} ${1:-none}", "none a"},
		{"${@:2} ${@:0:1} ${@:9}", "b c d a "},
		{"${@:-x}", "a b c d"},
	} {
		if got := substituteArgs(c.content, args); got != c.want {
			t.Errorf("%q gave %q, want %q", c.content, got, c.want)
		}
	}
	if got := substituteArgs("${@:-all} ${ARGUMENTS:-all}", nil); got != "all all" {
		t.Errorf("without arguments: %q", got)
	}
}
