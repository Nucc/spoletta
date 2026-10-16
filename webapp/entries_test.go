package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFileField(t *testing.T) {
	for _, c := range []struct {
		fs   []field
		want string
	}{
		{nil, ""},
		{[]field{{name: "a"}, {name: "b"}}, ""},
		{[]field{{name: "a"}, {name: "pdf", file: true}, {name: "png", file: true}}, "pdf"},
		{[]field{{name: "address", fields: []field{{name: "street"}, {name: "photo", file: true}}}}, "photo"},
	} {
		if got := fileField(c.fs); got != c.want {
			t.Errorf("fileField(%+v) = %q, want %q", c.fs, got, c.want)
		}
	}
}

// What a person enters comes by object id; an object without attributes
// holds the text entered as it is.
func TestCheckEntries(t *testing.T) {
	ticket := shapedObject{id: 1, name: "Ticket", fields: []field{{name: "key"}, {name: "summary"}}}
	note := shapedObject{id: 2, name: "Note", data: true}

	out, contents, err := checkEntries([]shapedObject{note}, map[string]json.RawMessage{"2": json.RawMessage(`"Call back on Monday"`)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Call back on Monday" || contents[2] != out {
		t.Errorf("one text entry = %q, %v", out, contents)
	}

	out, contents, err = checkEntries([]shapedObject{ticket, note}, map[string]json.RawMessage{
		"1": json.RawMessage(`{"summary": "Broken", "key": "OPS-1"}`),
		"2": json.RawMessage(`"first\nsecond"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := squeezed(out); got != `{"Ticket":{"key":"OPS-1","summary":"Broken"},"Note":"first\nsecond"}` {
		t.Errorf("checkEntries = %s", got)
	}
	if squeezed(contents[1]) != `{"key":"OPS-1","summary":"Broken"}` || contents[2] != "first\nsecond" {
		t.Errorf("contents = %v", contents)
	}

	report := shapedObject{id: 3, name: "Report", fields: []field{{name: "title"}, {name: "pdf", file: true}}}
	for _, c := range []struct {
		objects []shapedObject
		entries string
		problem string
	}{
		{[]shapedObject{ticket}, `{}`, `the objects entered don't match the process's: it lacks "1"`},
		{[]shapedObject{ticket}, `{"1": {"key": "a", "summary": "b"}, "9": 1}`, `the objects entered don't match the process's: it has keys not asked for: "9"`},
		{[]shapedObject{note}, `{"2": {"text": "x"}}`, `"Note": it must be text`},
		{[]shapedObject{ticket}, `{"1": {"key": "a"}}`, `"Ticket": it lacks "summary"`},
		{[]shapedObject{ticket}, `{"1": "OPS-1"}`, `"Ticket": it is not a JSON object`},
		// No file was written in a human step, so none can be named.
		{[]shapedObject{report}, `{"3": {"title": "t", "pdf": "/tmp/x.pdf"}}`, `"Report" can't be filled in by hand: "pdf" holds a file`},
	} {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.entries), &entries); err != nil {
			t.Fatal(err)
		}
		if _, _, err := checkEntries(c.objects, entries); err == nil || err.Error() != c.problem {
			t.Errorf("checkEntries(%s) = %v, want %q", c.entries, err, c.problem)
		}
	}

	tickets := ticket
	tickets.many = true
	out, _, err = checkEntries([]shapedObject{tickets}, map[string]json.RawMessage{"1": json.RawMessage(`[{"summary": "s", "key": "k"}]`)})
	if err != nil || !strings.HasPrefix(squeezed(out), `[{"key":"k"`) {
		t.Errorf("a list entered = %s, %v", out, err)
	}
}
