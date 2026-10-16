package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The output with all whitespace dropped, to compare JSON regardless of
// indentation.
func squeezed(s string) string { return strings.Join(strings.Fields(s), "") }

func TestStripFence(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1}`:                  `{"a": 1}`,
		"  {\"a\": 1}\n":            `{"a": 1}`,
		"```json\n{\"a\": 1}\n```":  `{"a": 1}`,
		"```\n{\"a\": 1}\n```":      `{"a": 1}`,
		"\n```json\n[1, 2]\n```\n":  `[1, 2]`,
		"```{\"a\": 1}```":          `{"a": 1}`,
		"``````":                    "",
		"```":                       "```",
		"```json\n{\"a\": 1}":       "```json\n{\"a\": 1}",
		"Here it is:\n```\n{}\n```": "Here it is:\n```\n{}\n```",
	} {
		if got := stripFence(in); got != want {
			t.Errorf("stripFence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJSONObject(t *testing.T) {
	fields, err := jsonObject(` {"a": 1, "b": {"c": [true]}} `)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || string(fields["a"]) != "1" || squeezed(string(fields["b"])) != `{"c":[true]}` {
		t.Errorf("jsonObject = %v", fields)
	}
	if fields, err := jsonObject(`{}`); err != nil || len(fields) != 0 {
		t.Errorf("jsonObject({}) = %v, %v", fields, err)
	}
	for in, problem := range map[string]string{
		``:                  "it is not a JSON object",
		`null`:              "it is not a JSON object",
		`[1]`:               "it is not a JSON object",
		`"text"`:            "it is not a JSON object",
		`{"a": 1`:           "it is not a JSON object",
		`{"a": 1} {"b": 2}`: "it has text after the JSON object",
		`{"a": 1} and more`: "it has text after the JSON object",
	} {
		if _, err := jsonObject(in); err == nil || err.Error() != problem {
			t.Errorf("jsonObject(%q) = %v, want %q", in, err, problem)
		}
	}
}

func TestJSONArray(t *testing.T) {
	items, err := jsonArray(`[1, "two", {"three": 3}]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || string(items[1]) != `"two"` {
		t.Errorf("jsonArray = %v", items)
	}
	// An empty array is a list of no items, not a missing one.
	if items, err := jsonArray(`[]`); err != nil || items == nil || len(items) != 0 {
		t.Errorf("jsonArray([]) = %v, %v", items, err)
	}
	for in, problem := range map[string]string{
		``:          "it is not a JSON array",
		`null`:      "it is not a JSON array",
		`{"a": 1}`:  "it is not a JSON array",
		`[1, 2`:     "it is not a JSON array",
		`[1] [2]`:   "it has text after the JSON array",
		`[1] done.`: "it has text after the JSON array",
	} {
		if _, err := jsonArray(in); err == nil || err.Error() != problem {
			t.Errorf("jsonArray(%q) = %v, want %q", in, err, problem)
		}
	}
}

func TestExactKeys(t *testing.T) {
	obj := func(keys ...string) map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for _, k := range keys {
			m[k] = json.RawMessage("1")
		}
		return m
	}
	for _, c := range []struct {
		fields  map[string]json.RawMessage
		keys    []string
		problem string
	}{
		{obj(), nil, ""},
		{obj("a", "b"), []string{"b", "a"}, ""},
		// Missing keys are named in the order asked for, extra ones sorted.
		{obj("a"), []string{"c", "a", "b"}, `it lacks "c", "b"`},
		{obj("a", "z", "y"), []string{"a"}, `it has keys not asked for: "y", "z"`},
		{obj("x"), []string{"a"}, `it lacks "a"; it has keys not asked for: "x"`},
	} {
		err := exactKeys(c.fields, c.keys)
		if (c.problem == "") != (err == nil) || (err != nil && err.Error() != c.problem) {
			t.Errorf("exactKeys(%v, %v) = %v, want %q", c.fields, c.keys, err, c.problem)
		}
	}
}

func TestOrdered(t *testing.T) {
	// Keys keep the order given, which json.Marshal would sort.
	got := ordered([]string{"z", "a", `say "hi"`}, []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`{"b":[2]}`), json.RawMessage(`"x"`)})
	want := "{\n  \"z\": 1,\n  \"a\": {\n    \"b\": [\n      2\n    ]\n  },\n  \"say \\\"hi\\\"\": \"x\"\n}"
	if got != want {
		t.Errorf("ordered = %s, want %s", got, want)
	}
	if got := ordered(nil, nil); got != "{}" {
		t.Errorf("ordered() = %q", got)
	}
	// A value that isn't JSON leaves the object unindented rather than lost.
	if got := ordered([]string{"a"}, []json.RawMessage{json.RawMessage(`nope`)}); got != `{"a":nope}` {
		t.Errorf("ordered with a bad value = %q", got)
	}
}

func TestIndentJSON(t *testing.T) {
	if got := indentJSON(`[1,{"a":2}]`); got != "[\n  1,\n  {\n    \"a\": 2\n  }\n]" {
		t.Errorf("indentJSON = %q", got)
	}
	if got := indentJSON(`not json`); got != "not json" {
		t.Errorf("indentJSON(not json) = %q", got)
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"name":       `"name"`,
		`say "hi"`:   `"say \"hi\""`,
		"two\nlines": `"two\nlines"`,
		"":           `""`,
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestItemsCount(t *testing.T) {
	for _, c := range []struct {
		q    quantity
		want string
	}{
		{quantity{}, ""},
		{quantity{min: 2}, " (at least 2)"},
		{quantity{max: count(3)}, " (at most 3)"},
		{quantity{min: 4, max: count(4)}, " (exactly 4)"},
		{quantity{min: 1, max: count(3)}, " (between 1 and 3)"},
	} {
		if got := itemsCount(c.q); got != c.want {
			t.Errorf("itemsCount(%+v) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestWithRetry(t *testing.T) {
	got := withRetry("Summarize the ticket.", `{"summary": 1, "x": 2}`, errors.New(`it has keys not asked for: "x"`))
	if !strings.HasPrefix(got, "Summarize the ticket.\n\n") {
		t.Errorf("the prompt doesn't come first:\n%s", got)
	}
	for _, want := range []string{
		`did not follow <output_format>: it has keys not asked for: "x".`,
		"<rejected_reply>\n{\"summary\": 1, \"x\": 2}\n</rejected_reply>\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("withRetry lacks %q:\n%s", want, got)
		}
	}
}

// A ticket with a subject and a body, and a review deciding whether a draft
// is approved or rejected.
func ticketObject() shapedObject {
	return shapedObject{id: 1, name: "Ticket", fields: []field{{name: "subject", definition: "What it is about"}, {name: "body"}}}
}

func draftDecision(review bool) decidedObject {
	return decidedObject{id: 2, name: "Draft", review: review, states: []objectState{
		{ID: 20, Object: 2, Name: "approved"}, {ID: 21, Object: 2, Name: "rejected"},
	}}
}

func TestSpecShapes(t *testing.T) {
	ticket := ticketObject()
	tickets := ticketObject()
	tickets.many = true
	notes := shapedObject{id: 3, name: "Notes", fields: []field{{name: dataAttribute}}}
	for _, c := range []struct {
		name  string
		c     outputContract
		shape string
	}{
		{"one object", outputContract{shaped: []shapedObject{ticket}}, "object"},
		{"one list", outputContract{shaped: []shapedObject{tickets}}, "items"},
		{"several objects", outputContract{shaped: []shapedObject{ticket, notes}}, "objects"},
		{"only decisions", outputContract{decisions: []decidedObject{draftDecision(true)}}, "decisions"},
		{"an object and decisions", outputContract{shaped: []shapedObject{ticket}, decisions: []decidedObject{draftDecision(false)}}, "object"},
	} {
		s := c.c.spec()
		if s.Shape != c.shape || len(s.Objects) != len(c.c.shaped) || len(s.Decisions) != len(c.c.decisions) {
			t.Errorf("%s: spec = %+v", c.name, s)
		}
	}

	s := (&outputContract{decisions: []decidedObject{draftDecision(true)}}).spec()
	if d := s.Decisions[0]; d.Name != "Draft" || strings.Join(d.States, ",") != "approved,rejected" || !d.Review {
		t.Errorf("spec decision = %+v", d)
	}
	// Objects is an empty array, never null, for the worker's tool.
	if b, _ := json.Marshal(s); !strings.Contains(string(b), `"objects":[]`) {
		t.Errorf("spec = %s", b)
	}
	if b, _ := json.Marshal((&outputContract{shaped: []shapedObject{ticket}}).spec()); strings.Contains(string(b), "decisions") {
		t.Errorf("spec without decisions = %s", b)
	}
}

// A file attribute's description says where to write it, before what it is.
func TestSpecFileDescription(t *testing.T) {
	c := &outputContract{filesDir: "/data/files/runs/1/steps/2", shaped: []shapedObject{{id: 1, name: "Report", fields: []field{
		{name: "pdf", definition: " The rendered report ", file: true},
		{name: "title", definition: " Its title "},
	}}}}
	as := c.spec().Objects[0].Attributes
	if as[0].Description != "The path of a file you wrote in /data/files/runs/1/steps/2. The rendered report" || !as[0].File {
		t.Errorf("file attribute = %+v", as[0])
	}
	if as[1].Description != "Its title" || as[1].File {
		t.Errorf("attribute = %+v", as[1])
	}
	text := c.withContract("Do it.")
	for _, want := range []string{
		`- "pdf": the path of a file you wrote. The rendered report`,
		"Write the files you generate in /data/files/runs/1/steps/2, and give their paths.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("contract lacks %q:\n%s", want, text)
		}
	}
}

func TestWithContractShapes(t *testing.T) {
	ticket := ticketObject()
	tickets := ticketObject()
	tickets.many = true
	notes := shapedObject{id: 3, name: "Notes", fields: []field{{name: dataAttribute}}}
	for _, c := range []struct {
		name      string
		c         outputContract
		want, not []string
	}{
		{"one object", outputContract{shaped: []shapedObject{ticket}}, []string{
			"Its arguments are exactly the keys below.\n",
			"\n\"Ticket\":\n  - \"subject\": What it is about\n  - \"body\"\n",
		}, []string{"decisions", "Write the files"}},
		{"one list", outputContract{shaped: []shapedObject{tickets}}, []string{
			"Its argument items is an array with one JSON object per item, each with exactly the keys below; an empty array if there are no items.\n",
			"\"Ticket\", a list of items, each with:",
		}, nil},
		{"several objects", outputContract{shaped: []shapedObject{ticket, notes}}, []string{
			"Its arguments hold one key per object below",
			"\n\"Notes\":\n  - \"data\"\n",
		}, nil},
		{"only decisions", outputContract{decisions: []decidedObject{draftDecision(true)}}, []string{
			"Its argument is exactly decisions.\n",
			`- "Draft": one of "approved", "rejected"; you review it as you were given it, so it keeps what it holds`,
		}, []string{"Its arguments are exactly output"}},
		{"an object and decisions", outputContract{shaped: []shapedObject{ticket}, decisions: []decidedObject{draftDecision(false)}}, []string{
			"Its arguments are exactly output and decisions. output is a JSON object with exactly the keys below.\n",
			"decisions holds one key per object below, each a JSON object with \"state\"",
			"- \"Draft\": one of \"approved\", \"rejected\"\n",
		}, []string{"you review it"}},
		{"a list and decisions", outputContract{shaped: []shapedObject{tickets}, decisions: []decidedObject{draftDecision(false)}}, []string{
			"output is an array with one JSON object per item, each with exactly the keys below; an empty array if there are no items.\n",
		}, nil},
	} {
		text := c.c.withContract("Do it.")
		if !strings.HasPrefix(text, "Do it.\n\n<output_format>\n") || !strings.HasSuffix(text, "</output_format>\n") {
			t.Errorf("%s: the contract doesn't follow the prompt:\n%s", c.name, text)
		}
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: contract lacks %q:\n%s", c.name, want, text)
			}
		}
		for _, not := range c.not {
			if strings.Contains(text, not) {
				t.Errorf("%s: contract has %q:\n%s", c.name, not, text)
			}
		}
	}
}

// With several objects, the reply holds each under its name, and comes back
// with them in the contract's order.
func TestParseSeveral(t *testing.T) {
	notes := shapedObject{id: 3, name: "Notes", data: true, fields: []field{{name: dataAttribute}}}
	c := &outputContract{shaped: []shapedObject{ticketObject(), notes}}
	out, contents, err := c.parse("```json\n" + `{"Notes": {"data": [1, 2]}, "Ticket": {"body": "b", "subject": "s"}}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if got := squeezed(out); got != `{"Ticket":{"subject":"s","body":"b"},"Notes":{"data":[1,2]}}` {
		t.Errorf("parse = %s", got)
	}
	if squeezed(contents[1]) != `{"subject":"s","body":"b"}` || squeezed(contents[3]) != `{"data":[1,2]}` {
		t.Errorf("contents = %v", contents)
	}
	for reply, problem := range map[string]string{
		`[]`: "it is not a JSON object",
		`{"Ticket": {"subject": "s", "body": "b"}}`:                                   `it lacks "Notes"`,
		`{"Ticket": {"subject": "s", "body": "b"}, "Notes": {"data": 1}, "Other": 1}`: `it has keys not asked for: "Other"`,
		`{"Ticket": {"subject": "s"}, "Notes": {"data": 1}}`:                          `"Ticket": it lacks "body"`,
		`{"Ticket": {"subject": "s", "body": "b"}, "Notes": 1}`:                       `"Notes": it is not a JSON object`,
	} {
		if _, _, err := c.parse(reply); err == nil || err.Error() != problem {
			t.Errorf("parse(%s) = %v, want %q", reply, err, problem)
		}
	}
}

// An attribute's value can be any JSON; only objects and lists are shaped.
func TestParseAnyValue(t *testing.T) {
	c := &outputContract{shaped: []shapedObject{ticketObject()}}
	out, _, err := c.parse(`{"subject": {"free": ["form"]}, "body": null}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := squeezed(out); got != `{"subject":{"free":["form"]},"body":null}` {
		t.Errorf("parse = %s", got)
	}
	if _, _, err := c.parse(`{"subject": "s", "body": "b"} trailing`); err == nil || err.Error() != "it has text after the JSON object" {
		t.Errorf("err = %v", err)
	}
}

func TestParseDecisions(t *testing.T) {
	// A review hands in only its decisions.
	c := &outputContract{decisions: []decidedObject{draftDecision(true)}}
	out, contents, decisions, err := c.parseDecisions(`{"decisions": {"Draft": {"state": "rejected", "instructions": "  Shorten it.  "}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := squeezed(out); got != `{"decisions":{"Draft":{"state":"rejected","instructions":"Shortenit."}}}` {
		t.Errorf("parseDecisions = %s", got)
	}
	if len(contents) != 0 || decisions[2] != (decided{state: 21, instructions: "Shorten it."}) {
		t.Errorf("contents = %v, decisions = %v", contents, decisions)
	}

	// Generating an object and deciding its state, the output comes first.
	c = &outputContract{shaped: []shapedObject{ticketObject()}, decisions: []decidedObject{draftDecision(false)}}
	out, contents, decisions, err = c.parseDecisions(`{"decisions": {"Draft": {"state": "approved"}}, "output": {"body": "b", "subject": "s"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := squeezed(out); got != `{"output":{"subject":"s","body":"b"},"decisions":{"Draft":{"state":"approved"}}}` {
		t.Errorf("parseDecisions = %s", got)
	}
	if squeezed(contents[1]) != `{"subject":"s","body":"b"}` || decisions[2] != (decided{state: 20}) {
		t.Errorf("contents = %v, decisions = %v", contents, decisions)
	}

	for reply, problem := range map[string]string{
		`not json`: "it is not a JSON object",
		`{"output": {"subject": "s", "body": "b"}}`:                                                                   `it lacks "decisions"`,
		`{"output": {"subject": "s"}, "decisions": {"Draft": {"state": "approved"}}}`:                                 `"output": it lacks "body"`,
		`{"output": {"subject": "s", "body": "b"}, "decisions": []}`:                                                  `"decisions": it is not a JSON object`,
		`{"output": {"subject": "s", "body": "b"}, "decisions": {}}`:                                                  `"decisions": it lacks "Draft"`,
		`{"output": {"subject": "s", "body": "b"}, "decisions": {"Draft": {"state": "lost"}}}`:                        `"decisions" "Draft": "state" is "lost", which isn't one of "approved", "rejected"`,
		`{"output": {"subject": "s", "body": "b"}, "decisions": {"Draft": {"state": 1}}}`:                             `"decisions" "Draft": "state" must be the name of a state, as a string`,
		`{"output": {"subject": "s", "body": "b"}, "decisions": {"Draft": {}}}`:                                       `"decisions" "Draft": it lacks "state"`,
		`{"output": {"subject": "s", "body": "b"}, "decisions": {"Draft": {"state": "approved", "x": 1}}}`:            `"decisions" "Draft": it has keys not asked for: "x"`,
		`{"output": {"subject": "s", "body": "b"}, "decisions": {"Draft": {"state": "approved", "instructions": 3}}}`: `"decisions" "Draft": "instructions" must be a string`,
	} {
		if _, _, _, err := c.parseDecisions(reply); err == nil || err.Error() != problem {
			t.Errorf("parseDecisions(%s) = %v, want %q", reply, err, problem)
		}
	}

	// A review's reply holding an output has a key not asked for.
	c = &outputContract{decisions: []decidedObject{draftDecision(true)}}
	if _, _, _, err := c.parseDecisions(`{"output": {}, "decisions": {"Draft": {"state": "approved"}}}`); err == nil || err.Error() != `it has keys not asked for: "output"` {
		t.Errorf("err = %v", err)
	}

	// Deciding nothing, the reply is checked as by parse.
	c = &outputContract{shaped: []shapedObject{ticketObject()}}
	out, contents, decisions, err = c.parseDecisions(`{"subject": "s", "body": "b"}`)
	if err != nil || decisions != nil || contents[1] != out {
		t.Errorf("parseDecisions without decisions = %q, %v, %v, %v", out, contents, decisions, err)
	}
}

func TestCheckFile(t *testing.T) {
	dir := t.TempDir()
	steps := filepath.Join(dir, "step")
	for _, d := range []string{steps, filepath.Join(steps, "sub"), filepath.Join(dir, "other")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(steps, "a.txt"), filepath.Join(steps, "sub", "b.txt"), filepath.Join(dir, "other", "secret")} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A symlink an agent left pointing outside the step's directory.
	if err := os.Symlink(filepath.Join(dir, "other", "secret"), filepath.Join(steps, "link")); err != nil {
		t.Fatal(err)
	}
	c := &outputContract{filesDir: steps}
	for _, ok := range []struct{ value, path string }{
		{`"a.txt"`, filepath.Join(steps, "a.txt")},
		{`"sub/b.txt"`, filepath.Join(steps, "sub", "b.txt")},
		{`"./sub/../a.txt"`, filepath.Join(steps, "a.txt")},
		{quote(filepath.Join(steps, "a.txt")), filepath.Join(steps, "a.txt")},
	} {
		if got, err := c.checkFile(json.RawMessage(ok.value)); err != nil || got != ok.path {
			t.Errorf("checkFile(%s) = %q, %v, want %q", ok.value, got, err, ok.path)
		}
	}
	for value, problem := range map[string]string{
		`""`:                "it must be the path of a file, as a string",
		`3`:                 "it must be the path of a file, as a string",
		`["a.txt"]`:         "it must be the path of a file, as a string",
		`"missing.txt"`:     filepath.Join(steps, "missing.txt") + " doesn't exist",
		`"../other/secret"`: filepath.Join(steps, "../other/secret") + " isn't in " + steps,
		quote(filepath.Join(dir, "other", "secret")): filepath.Join(dir, "other", "secret") + " isn't in " + steps,
		`"link"`: filepath.Join(steps, "link") + " isn't in " + steps,
		`"sub"`:  filepath.Join(steps, "sub") + " isn't a file",
		`"."`:    steps + " isn't in " + steps,
	} {
		if _, err := c.checkFile(json.RawMessage(value)); err == nil || err.Error() != problem {
			t.Errorf("checkFile(%s) = %v, want %q", value, err, problem)
		}
	}
	// Without a files directory, any path is kept as it is.
	if got, err := (&outputContract{}).checkFile(json.RawMessage(`"../anywhere"`)); err != nil || got != "../anywhere" {
		t.Errorf("checkFile unchecked = %q, %v", got, err)
	}
	// A step whose directory was never made has no files.
	gone := &outputContract{filesDir: filepath.Join(dir, "never")}
	if _, err := gone.checkFile(json.RawMessage(`"a.txt"`)); err == nil || !strings.HasSuffix(err.Error(), "doesn't exist") {
		t.Errorf("checkFile in a missing directory = %v", err)
	}
}

func TestHasFile(t *testing.T) {
	for _, c := range []struct {
		fs   []field
		want bool
	}{
		{nil, false},
		{[]field{{name: "a"}, {name: "b"}}, false},
		{[]field{{name: "a"}, {name: "f", file: true}}, true},
		{[]field{{name: "o", fields: []field{{name: "p", fields: []field{{name: "f", file: true}}}}}}, true},
	} {
		if got := hasFile(c.fs); got != c.want {
			t.Errorf("hasFile(%+v) = %v", c.fs, got)
		}
	}
}

func TestStepFilesDir(t *testing.T) {
	if got, want := stepFilesDir(7, 42), filepath.Join(filesRoot, "runs", "7", "steps", "42"); got != want {
		t.Errorf("stepFilesDir = %q, want %q", got, want)
	}
}
