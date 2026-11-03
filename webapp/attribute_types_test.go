package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckAttributeType(t *testing.T) {
	ref := int64(7)
	for _, c := range []struct {
		in        attribute
		wantType  string
		wantPat   string
		wantError string
	}{
		{in: attribute{}, wantType: ""},
		{in: attribute{Type: " integer "}, wantType: "integer"},
		{in: attribute{Type: "text", Pattern: " [A-Z]+-\\d+ "}, wantType: "text", wantPat: `[A-Z]+-\d+`},
		// A pattern alone makes it text.
		{in: attribute{Pattern: `\d+`}, wantType: "text", wantPat: `\d+`},
		{in: attribute{Type: "string"}, wantError: `"string" isn't a type of attribute: it is one of text, integer, number, boolean, datetime`},
		{in: attribute{Type: "integer", Pattern: `\d+`}, wantError: "only text has a pattern"},
		{in: attribute{Type: "text", Pattern: "[A-Z"}, wantError: "the pattern isn't a regular expression: missing closing ]: `[A-Z`"},
		{in: attribute{Type: "text", Pattern: strings.Repeat("a", maxPatternLength+1)}, wantError: "a pattern is at most 1000 characters"},
		{in: attribute{Type: "number", File: true}, wantError: "a file attribute holds the path of a file, so it has no type"},
		{in: attribute{Type: "text", Ref: &ref}, wantError: "an attribute referring to records holds their keys, so it has no type"},
		{in: attribute{File: true}, wantType: ""},
		{in: attribute{Ref: &ref}, wantType: ""},
	} {
		a := c.in
		msg := checkAttributeType(&a)
		if msg != c.wantError {
			t.Errorf("checkAttributeType(%+v) = %q, want %q", c.in, msg, c.wantError)
			continue
		}
		if msg == "" && (a.Type != c.wantType || a.Pattern != c.wantPat) {
			t.Errorf("checkAttributeType(%+v) left type %q, pattern %q; want %q, %q", c.in, a.Type, a.Pattern, c.wantType, c.wantPat)
		}
	}
}

func TestCheckTyped(t *testing.T) {
	ok := []struct{ kind, pattern, value string }{
		{"", "", `{"any": ["thing"]}`},
		{"", "", `null`},
		{"text", "", `"anything"`},
		{"text", "", `""`},
		{"text", `[A-Z]+-\d+`, `"TALK-123"`},
		// The pattern is matched whole, but may say otherwise itself.
		{"text", `.*TALK.*`, `"see TALK-1"`},
		{"text", `TALK|PROJ`, `"PROJ"`},
		{"integer", "", `3`},
		{"integer", "", `-12`},
		{"integer", "", `3.0`},
		{"integer", "", `1e3`},
		{"integer", "", `12345678901234567890`},
		{"number", "", `0.8`},
		{"number", "", `-2`},
		{"boolean", "", `true`},
		{"boolean", "", ` false `},
		{"datetime", "", `"2026-11-03"`},
		{"datetime", "", `"2026-11-03T09:00:00Z"`},
		{"datetime", "", `"2026-11-03T09:00:00.5+01:00"`},
	}
	for _, c := range ok {
		if err := checkTyped(c.kind, c.pattern, json.RawMessage(c.value)); err != nil {
			t.Errorf("checkTyped(%q, %q, %s) = %v", c.kind, c.pattern, c.value, err)
		}
	}
	bad := []struct{ kind, pattern, value, want string }{
		{"text", "", `3`, "it must be text, a JSON string"},
		{"text", "", `null`, "it must be text, a JSON string"},
		{"text", `[A-Z]+-\d+`, `"TALK-123 again"`, `"TALK-123 again" doesn't match its pattern [A-Z]+-\d+`},
		{"text", `TALK|PROJ`, `"PROJECT"`, `"PROJECT" doesn't match its pattern TALK|PROJ`},
		{"integer", "", `"3"`, "it must be an integer, a JSON number such as 3"},
		{"integer", "", `3.5`, "it must be an integer, a JSON number such as 3"},
		{"integer", "", `null`, "it must be an integer, a JSON number such as 3"},
		{"number", "", `"0.8"`, "it must be a number, a JSON number such as 0.8"},
		{"number", "", `true`, "it must be a number, a JSON number such as 0.8"},
		{"boolean", "", `"true"`, "it must be true or false"},
		{"boolean", "", `null`, "it must be true or false"},
		{"boolean", "", `1`, "it must be true or false"},
		{"datetime", "", `"tomorrow"`, `it must be a date such as "2026-11-03", or a date and time in RFC 3339 such as "2026-11-03T09:00:00Z"`},
		{"datetime", "", `"2026-11-03 09:00"`, `it must be a date such as "2026-11-03", or a date and time in RFC 3339 such as "2026-11-03T09:00:00Z"`},
		{"datetime", "", `20261103`, `it must be a date such as "2026-11-03", or a date and time in RFC 3339 such as "2026-11-03T09:00:00Z"`},
	}
	for _, c := range bad {
		if err := checkTyped(c.kind, c.pattern, json.RawMessage(c.value)); err == nil || err.Error() != c.want {
			t.Errorf("checkTyped(%q, %q, %s) = %v, want %q", c.kind, c.pattern, c.value, err, c.want)
		}
	}
}

// A typed attribute's values are checked wherever a process hands one in,
// at any depth, and refused with the field named.
func TestParseTyped(t *testing.T) {
	c := &outputContract{shaped: []shapedObject{{id: 1, name: "Ticket", fields: []field{
		{name: "key", kind: typeText, pattern: `[A-Z]+-\d+`},
		{name: "points", kind: typeInteger},
		{name: "notes"},
		{name: "estimates", many: true, fields: []field{{name: "days", kind: typeNumber}, {name: "sure", kind: typeBoolean}}},
	}}}}
	out, _, err := c.parse(`{"key": "TALK-1", "points": 3, "notes": {"free": true}, "estimates": [{"days": 1.5, "sure": false}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := squeezed(out); got != `{"key":"TALK-1","points":3,"notes":{"free":true},"estimates":[{"days":1.5,"sure":false}]}` {
		t.Errorf("parse = %s", got)
	}
	for reply, want := range map[string]string{
		`{"key": "talk-1", "points": 3, "notes": 1, "estimates": []}`:                            `"key": "talk-1" doesn't match its pattern [A-Z]+-\d+`,
		`{"key": "TALK-1", "points": "3", "notes": 1, "estimates": []}`:                          `"points": it must be an integer, a JSON number such as 3`,
		`{"key": "TALK-1", "points": 3, "notes": 1, "estimates": [{"days": "1", "sure": true}]}`: `"estimates": item 1: "days": it must be a number, a JSON number such as 0.8`,
		`{"key": "TALK-1", "points": 3, "notes": 1, "estimates": [{"days": 1, "sure": "yes"}]}`:  `"estimates": item 1: "sure": it must be true or false`,
	} {
		if _, _, err := c.parse(reply); err == nil || err.Error() != want {
			t.Errorf("parse(%s) = %v, want %q", reply, err, want)
		}
	}
}

// An agent is told each value's type, in its prompt and in the tool it
// hands its output in with, a pattern anchored as it is matched.
func TestSpecTyped(t *testing.T) {
	c := &outputContract{shaped: []shapedObject{{id: 1, name: "Ticket", fields: []field{
		{name: "key", definition: "Its key", kind: typeText, pattern: `[A-Z]+-\d+`},
		{name: "points", kind: typeInteger},
		{name: "notes", definition: "Anything"},
	}}}}
	as := c.spec().Objects[0].Attributes
	if as[0].Type != "text" || as[0].Pattern != `^(?:[A-Z]+-\d+)$` || as[0].Description != `Text matching the regular expression ^(?:[A-Z]+-\d+)$. Its key` {
		t.Errorf("text attribute = %+v", as[0])
	}
	if as[1].Type != "integer" || as[1].Pattern != "" || as[1].Description != "An integer." {
		t.Errorf("integer attribute = %+v", as[1])
	}
	if as[2].Type != "" || as[2].Description != "Anything" {
		t.Errorf("attribute = %+v", as[2])
	}
	text := c.withContract("Do it.")
	for _, want := range []string{
		`- "key": text matching the regular expression ^(?:[A-Z]+-\d+)$. Its key`,
		`- "points": an integer.`,
		`- "notes": Anything`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("contract lacks %q:\n%s", want, text)
		}
	}
}

// What a request starting a run gives is described with each value's type.
func TestFieldsSchemaTyped(t *testing.T) {
	s := fieldsSchema([]field{
		{name: "key", kind: typeText, pattern: `[A-Z]+-\d+`, definition: "Its key"},
		{name: "points", kind: typeInteger},
		{name: "due", kind: typeDatetime},
		{name: "notes"},
	})
	b, err := json.Marshal(s["properties"])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"due":{"anyOf":[{"format":"date"},{"format":"date-time"}],"type":"string"},"key":{"description":"Its key","pattern":"^(?:[A-Z]+-\\d+)$","type":"string"},"notes":{},"points":{"type":"integer"}}`
	if string(b) != want {
		t.Errorf("properties = %s\nwant %s", b, want)
	}
}

// A hand edit of a record may leave a typed field out or empty, but not
// give it a value of another type, at any depth.
func TestTypedProblems(t *testing.T) {
	fs := []field{
		{name: "key", kind: typeText, pattern: `[A-Z]+-\d+`},
		{name: "points", kind: typeInteger},
		{name: "notes"},
		{name: "steps", many: true, fields: []field{{name: "done", kind: typeBoolean}}},
	}
	for content, want := range map[string]struct {
		msg    string
		asJSON bool
	}{
		`{}`: {},
		`{"key": "", "points": null, "notes": 3}`:                       {},
		`{"key": "TALK-1", "points": 2, "steps": [{"done": true}, {}]}`: {},
		`{"key": "talk"}`: {msg: `"key": "talk" doesn't match its pattern [A-Z]+-\d+`},
		`{"points": "2"}`: {msg: `"points": it must be an integer, a JSON number such as 3`, asJSON: true},
		`{"steps": [{"done": true}, {"done": "yes"}]}`: {msg: `"steps": item 2: "done": it must be true or false`, asJSON: true},
	} {
		fields, err := jsonObject(content)
		if err != nil {
			t.Fatal(err)
		}
		msg, asJSON := typedProblems(fs, fields)
		if msg != want.msg || asJSON != want.asJSON {
			t.Errorf("typedProblems(%s) = %q, %v; want %q, %v", content, msg, asJSON, want.msg, want.asJSON)
		}
	}
}
