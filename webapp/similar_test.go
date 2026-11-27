package main

import (
	"slices"
	"testing"
)

// A record's words are what its fields hold, not their names, lower-cased,
// split at what isn't a letter or digit, without single letters and the
// commonest words.
func TestLikeWords(t *testing.T) {
	got := likeWords(contentText(`{"key": "PROJ-12", "summary": "The login page times out", "labels": ["auth", "UI"], "parts": {"steps": "Open it, wait 30s"}}`))
	for _, w := range []string{"proj", "12", "login", "page", "times", "out", "auth", "ui", "open", "wait", "30s"} {
		if !slices.Contains(got, w) {
			t.Errorf("the words %v lack %q", got, w)
		}
	}
	for _, w := range []string{"the", "it", "key", "summary", "labels"} {
		if slices.Contains(got, w) {
			t.Errorf("the words %v hold %q", got, w)
		}
	}
	if got := likeWords("Ünïcode café, naïve résumé"); !slices.Equal(got, []string{"ünïcode", "café", "naïve", "résumé"}) {
		t.Errorf("letters outside ASCII: %v", got)
	}
	if got := contentText("plain text, not JSON"); got != "plain text, not JSON" {
		t.Errorf("plain text = %q", got)
	}
}

// The records most like what is looked for come first, a rare word in
// common counting more than a common one; records sharing no word with it
// are left out.
func TestLikeOrder(t *testing.T) {
	texts := []string{
		"Checkout page shows a blank screen",         // 0: page
		"Login fails with a timeout on the SSO page", // 1: login, timeout, page
		"Export to CSV drops the last row",           // 2: nothing
		"Timeout when the login page loads slowly",   // 3: login, timeout, page
		"Search page is slow",                        // 4: page
		"Password reset email never arrives",         // 5: nothing
		"Login timeout",                              // 6: login, timeout, short
	}
	got := likeOrder(texts, "Users see a timeout on the login page")
	if len(got) != 5 {
		t.Fatalf("likeOrder = %v, want the five sharing a word", got)
	}
	// The short record saying both rare words is the most like; the two
	// longer ones saying them and page next; those saying page alone last,
	// the shorter first, as a word counts more in a shorter record.
	if got[0] != 6 || !slices.Equal(got[3:], []int{4, 0}) || !slices.Contains(got[1:3], 1) || !slices.Contains(got[1:3], 3) {
		t.Errorf("likeOrder = %v", got)
	}
	if got := likeOrder(texts, "the and of"); got != nil {
		t.Errorf("like of no word = %v, want none", got)
	}
	if got := likeOrder(nil, "timeout"); got != nil {
		t.Errorf("no texts = %v", got)
	}
	// Saying a word again counts, but less each time; records as like keep
	// their order.
	if got := likeOrder([]string{"login timeout", "timeout timeout", "timeout login"}, "timeout"); !slices.Equal(got, []int{1, 0, 2}) {
		t.Errorf("repeated words = %v", got)
	}
}
