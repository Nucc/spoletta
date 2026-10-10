package main

import "testing"

func TestRules(t *testing.T) {
	content := `{"confidence": 0.9, "kind": "bug", "title": "Login crashes", "labels": ["auth", "p1"], "author": {"name": "Ada"}, "done": false}`
	for _, c := range []struct {
		rule string
		want bool
	}{
		{"confidence >= 0.8", true},
		{"confidence > 0.9", false},
		{"confidence < 1 and kind == \"bug\"", true},
		{"kind == bug", true},
		{"kind != \"bug\"", false},
		{"title contains \"crash\"", true},
		{"labels contains \"auth\"", true},
		{"labels contains \"p2\"", false},
		{"author.name == \"Ada\"", true},
		{"done == false", true},
		{"missing == 1", false},
		{"missing != 1", true},
		{"kind >= \"a\"", true},
		{"confidence >= \"high\"", false},
		{"", false},
	} {
		r, err := parseRule(c.rule)
		if err != nil {
			t.Errorf("parseRule(%q): %v", c.rule, err)
			continue
		}
		if got := r.holds(content); got != c.want {
			t.Errorf("%q holds = %v, want %v", c.rule, got, c.want)
		}
	}
	for _, bad := range []string{"confidence", "== 1", "a. == 1", "flag > true", "x >"} {
		if _, err := parseRule(bad); err == nil {
			t.Errorf("parseRule(%q) took it", bad)
		}
	}
	if r, _ := parseRule("data == \"yes\""); !r.holds(`{"data": "yes"}`) || r.holds("not json") {
		t.Error("data rule")
	}
}
