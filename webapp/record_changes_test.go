package main

import (
	"encoding/json"
	"testing"
)

// A change lists the fields that differ from the change before: changed,
// added and taken out, the struct's own in its order first and the others
// by name, leaving out those alike as JSON whatever their spacing or order.
// A record's first change lists all its fields as new, and content that
// isn't fields changes as a whole.
func TestFieldChanges(t *testing.T) {
	str := func(s string) *string { return &s }
	order := []string{"id", "title", "severity"}
	say := func(cs []fieldChange) []string {
		out := []string{}
		for _, c := range cs {
			b, a := "-", "-"
			if c.Before != nil {
				b = string(c.Before)
			}
			if c.After != nil {
				a = string(c.After)
			}
			out = append(out, c.Name+":"+b+">"+a)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		before *string
		after  string
		want   []string
	}{
		{"first", nil, `{"severity":"low","id":"B-1","extra":1}`,
			[]string{`id:->"B-1"`, `severity:->"low"`, `extra:->1`}},
		{"changed and added", str(`{"id":"B-1","title":"a"}`), `{"id":"B-1","title":"b","zeta":true,"alpha":null}`,
			[]string{`title:"a">"b"`, `alpha:->null`, `zeta:->true`}},
		{"taken out", str(`{"id":"B-1","severity":"low","note":"x"}`), `{"id":"B-1"}`,
			[]string{`severity:"low">-`, `note:"x">-`}},
		{"alike in another order and spacing", str(`{"id":"B-1","tags":{"a":1,"b":2}}`), `{ "tags": {"b": 2, "a": 1}, "id": "B-1" }`,
			[]string{}},
		{"text changes whole", str(`Login fails`), `Login fails after a reset`,
			[]string{`:"Login fails">"Login fails after a reset"`}},
		{"text alike", str(`same`), `same`, []string{}},
		{"text first", nil, `plain`, []string{`:->"plain"`}},
		{"fields to text", str(`{"id":"B-1"}`), `gone`, []string{`:{"id":"B-1"}>"gone"`}},
	} {
		got := say(fieldChanges(tc.before, tc.after, order))
		w, _ := json.Marshal(tc.want)
		g, _ := json.Marshal(got)
		if string(w) != string(g) {
			t.Errorf("%s: changes = %s, want %s", tc.name, g, w)
		}
	}
}

// Contents hold the same when alike as JSON, or as text when either isn't
// JSON, so two texts that aren't JSON never pass for each other.
func TestSameContent(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, `{"b":[1,2],"a":1}`, true},
		{`{"a":1}`, `{"a":2}`, false},
		{`one`, `two`, false},
		{`one`, `one`, true},
		{`{"a":1}`, `not json`, false},
	} {
		if got := sameContent(tc.a, tc.b); got != tc.want {
			t.Errorf("sameContent(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
