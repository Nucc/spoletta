package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(id int64) *int64 { return &id }

// A person exhibiting a name and an address with a street and city, and
// aggregating phones; an employee specializing the person.
func people() structure {
	return structure{
		1: {id: 1, name: "Person", attrs: []attribute{{ID: 10, Object: 1, Name: "name"}}},
		2: {id: 2, name: "address", owner: ptr(1), relation: relationExhibition, attrs: []attribute{
			{ID: 11, Object: 2, Name: "street"}, {ID: 12, Object: 2, Name: "city"},
		}},
		3: {id: 3, name: "phones", owner: ptr(1), relation: relationAggregation, many: true, attrs: []attribute{
			{ID: 13, Object: 3, Name: "number"},
		}},
		4: {id: 4, name: "Employee", general: ptr(1), attrs: []attribute{{ID: 14, Object: 4, Name: "salary"}}},
	}
}

func names(fs []field) string {
	var b strings.Builder
	for i, f := range fs {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(f.name)
		if f.many {
			b.WriteString("*")
		}
		if len(f.fields) > 0 {
			b.WriteString("{" + names(f.fields) + "}")
		}
	}
	return b.String()
}

func TestFields(t *testing.T) {
	s := people()
	for id, want := range map[int64]string{
		1: "name address{street city} phones*{number}",
		// What it inherits comes first.
		4: "name address{street city} phones*{number} salary",
		2: "street city",
	} {
		fs, err := s.fields(id)
		if err != nil {
			t.Fatalf("fields(%d): %v", id, err)
		}
		if got := names(fs); got != want {
			t.Errorf("fields(%d) = %q, want %q", id, got, want)
		}
	}
	if msg := s.check(); msg != "" {
		t.Errorf("check() = %q, want none", msg)
	}
}

func TestCheckCycles(t *testing.T) {
	// The address specializing the person would hold the person, who holds it.
	s := people()
	s[2].general = ptr(1)
	if msg := s.check(); !strings.Contains(msg, "would hold itself") {
		t.Errorf("owned specializing its owner: check() = %q", msg)
	}

	s = people()
	s[1].general = ptr(4)
	if msg := s.check(); !strings.Contains(msg, "would hold itself") {
		t.Errorf("general loop: check() = %q", msg)
	}

	s = people()
	s[4].general = ptr(99)
	if msg := s.check(); !strings.Contains(msg, "of its flow") {
		t.Errorf("general elsewhere: check() = %q", msg)
	}
}

func TestCheckClashes(t *testing.T) {
	// An attribute named like a part.
	s := people()
	s[1].attrs = append(s[1].attrs, attribute{ID: 20, Object: 1, Name: "phones"})
	if msg := s.check(); !strings.Contains(msg, `two fields named "phones"`) {
		t.Errorf("attribute and part: check() = %q", msg)
	}

	// A specialization's own attribute named like an inherited one.
	s = people()
	s[4].attrs = append(s[4].attrs, attribute{ID: 21, Object: 4, Name: "name"})
	if msg := s.check(); !strings.Contains(msg, `"Employee" would have two fields named "name"`) {
		t.Errorf("inherited clash: check() = %q", msg)
	}

	// The same name at different depths is fine.
	s = people()
	s[2].attrs = append(s[2].attrs, attribute{ID: 22, Object: 2, Name: "name"})
	if msg := s.check(); msg != "" {
		t.Errorf("nested namesake: check() = %q", msg)
	}
}

func TestAttributesInherit(t *testing.T) {
	var got []string
	for _, a := range people().attributes(4) {
		got = append(got, a.Name)
	}
	if strings.Join(got, " ") != "name salary" {
		t.Errorf("attributes(4) = %v", got)
	}
}

func contractOf(t *testing.T, s structure, id int64, many bool) *outputContract {
	t.Helper()
	fs, err := s.fields(id)
	if err != nil {
		t.Fatal(err)
	}
	return &outputContract{shaped: []shapedObject{{id: id, name: s[id].name, many: many, fields: fs}}}
}

func TestParseNested(t *testing.T) {
	c := contractOf(t, people(), 1, false)
	out, contents, err := c.parse(`{"phones": [{"number": "1"}, {"number": 2}], "address": {"city": "Oslo", "street": "Main"}, "name": "Ann"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Keys come back in their order, at every depth.
	compact := strings.Join(strings.Fields(out), "")
	want := `{"name":"Ann","address":{"street":"Main","city":"Oslo"},"phones":[{"number":"1"},{"number":2}]}`
	if compact != want {
		t.Errorf("parse = %s, want %s", compact, want)
	}
	if contents[1] != out {
		t.Errorf("contents[1] = %q", contents[1])
	}

	for reply, problem := range map[string]string{
		`{"name": "Ann", "address": {"street": "Main"}, "phones": []}`:                                    `"address": it lacks "city"`,
		`{"name": "Ann", "address": {"street": "Main", "city": "Oslo", "zip": 1}, "phones": []}`:          `"address": it has keys not asked for: "zip"`,
		`{"name": "Ann", "address": "Main 1, Oslo", "phones": []}`:                                        `"address": it is not a JSON object`,
		`{"name": "Ann", "address": {"street": "Main", "city": "Oslo"}, "phones": {"number": "1"}}`:       `"phones": it is not a JSON array`,
		`{"name": "Ann", "address": {"street": "Main", "city": "Oslo"}, "phones": [{"number": "1"}, {}]}`: `"phones": item 2: it lacks "number"`,
	} {
		if _, _, err := c.parse(reply); err == nil || err.Error() != problem {
			t.Errorf("parse(%s) = %v, want %q", reply, err, problem)
		}
	}
}

func TestParseNestedList(t *testing.T) {
	c := contractOf(t, people(), 1, true)
	if _, _, err := c.parse(`[{"name": "Ann", "address": {"street": "Main", "city": "Oslo"}, "phones": []}]`); err != nil {
		t.Fatal(err)
	}
	_, _, err := c.parse(`[{"name": "Ann", "address": {"street": "Main"}, "phones": []}]`)
	if err == nil || err.Error() != `item 1: "address": it lacks "city"` {
		t.Errorf("err = %v", err)
	}
}

func TestParseNestedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cv.pdf"), []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := people()
	s[2].attrs = append(s[2].attrs, attribute{ID: 30, Object: 2, Name: "photo", File: true})
	c := contractOf(t, s, 1, false)
	c.filesDir = dir
	if !c.hasFiles() {
		t.Error("hasFiles() = false for a file inside an owned object")
	}
	out, _, err := c.parse(`{"name": "Ann", "address": {"street": "Main", "city": "Oslo", "photo": "cv.pdf"}, "phones": []}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Join(dir, "cv.pdf")) {
		t.Errorf("the nested file's path isn't made whole: %s", out)
	}
	_, _, err = c.parse(`{"name": "Ann", "address": {"street": "Main", "city": "Oslo", "photo": "../x"}, "phones": []}`)
	if err == nil || !strings.HasPrefix(err.Error(), `"address": "photo": `) {
		t.Errorf("a file outside the step's directory: err = %v", err)
	}
}

func TestContractText(t *testing.T) {
	c := contractOf(t, people(), 4, false)
	text := c.withContract("Do it.")
	for _, want := range []string{
		`- "address": a JSON object with:`,
		`    - "street"`,
		`- "phones": a list of JSON objects, one per item, each with:`,
		`    - "number"`,
		`- "salary"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("contract lacks %q:\n%s", want, text)
		}
	}
	spec := c.spec()
	phones := spec.Objects[0].Attributes[2]
	if phones.Name != "phones" || !phones.Many || len(phones.Attributes) != 1 {
		t.Errorf("spec phones = %+v", phones)
	}
}

func TestCheckEntriesNested(t *testing.T) {
	fs, err := people().fields(1)
	if err != nil {
		t.Fatal(err)
	}
	objects := []shapedObject{{id: 1, name: "Person", fields: fs}}
	_, _, err = checkEntries(objects, map[string]json.RawMessage{
		"1": json.RawMessage(`{"name": "Ann", "address": {"street": "Main", "city": "Oslo"}, "phones": [{"number": "1"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}
