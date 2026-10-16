package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A setup file is YAML or JSON, starts with its format's version, and
// turns down fields the format doesn't have, so a misspelt one doesn't
// quietly go missing; links and states read as just a name too.
func TestParseSetup(t *testing.T) {
	for _, c := range []struct {
		name, body, problem string
	}{
		{"empty", "", "the file is empty"},
		{"no version", "flows: []\n", "starts with spoletta: 1"},
		{"other version", "spoletta: 2\n", "starts with spoletta: 1"},
		{"yaml", "spoletta: 1\nflows:\n  - name: Triage\n", ""},
		{"json", `{"spoletta": 1, "flows": [{"name": "Triage"}]}`, ""},
		{"unknown field", "spoletta: 1\nflow: []\n", "invalid setup file"},
		{"unknown field on a process", "spoletta: 1\nflows:\n  - name: F\n    processes:\n      - name: P\n        promt: hi\n", "invalid setup file"},
		{"unknown field on a link", "spoletta: 1\nflows:\n  - name: F\n    processes:\n      - name: P\n        takes:\n          - object: O\n            stat: open\n", "invalid setup file"},
		{"unknown field on a state", "spoletta: 1\nstructs:\n  - name: S\n    states:\n      - name: open\n        rul: x\n", "invalid setup file"},
		{"unknown field in a link's place", "spoletta: 1\nflows:\n  - name: F\n    processes:\n      - name: P\n        takes:\n          - object: O\n            at: {x: 1, y: 2, z: 3}\n", "invalid setup file"},
		{"not yaml", "spoletta: [1\n", "invalid setup file"},
		{"json unknown field", `{"spoletta": 1, "flows": [{"name": "F", "nmae": "x"}]}`, "invalid setup file"},
	} {
		_, err := parseSetup([]byte(c.body))
		got := ""
		if err != nil {
			got = err.Error()
		}
		if (c.problem == "") != (got == "") || !strings.Contains(got, c.problem) {
			t.Errorf("%s: parseSetup = %q, want %q", c.name, got, c.problem)
		}
	}
}

func TestParseSetupReadsTheFile(t *testing.T) {
	f, err := parseSetup([]byte(`spoletta: 1
mcpServers:
  - name: Linear
    url: https://mcp.linear.app/sse
structs:
  - name: Ticket
    states: [open, {name: closed, key: done, rule: "status == 'closed'"}]
    attributes:
      - name: id
        unique: true
procedures:
  - name: Review
flows:
  - name: Triage
    repositories:
      - name: app
        url: https://example.com/app.git
    schedules:
      - cron: "0 9 * * 1-5"
        timezone: UTC
        enabled: false
    start: {next: Fetch}
    end: {x: 10, y: 20}
    processes:
      - name: Fetch
        kind: script
        script: echo hi
        yields: [Ticket]
        next: [Decide]
      - name: Decide
        takes:
          - Ticket
          - {object: Ticket, states: [open, done], role: instrument, at: {x: 1, y: 2}}
        onFailure: [Fetch]
    objects:
      - name: Ticket
        copyOf: {flow: Ticket}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.MCPServers) != 1 || f.MCPServers[0].URL != "https://mcp.linear.app/sse" {
		t.Errorf("mcpServers = %+v", f.MCPServers)
	}
	wantStates := []setupState{{Name: "open"}, {Name: "closed", Key: "done", Rule: "status == 'closed'"}}
	if len(f.Structs) != 1 || !reflect.DeepEqual(f.Structs[0].States, wantStates) {
		t.Errorf("struct states = %+v, want %+v", f.Structs, wantStates)
	}
	if len(f.Procedures) != 1 || f.Procedures[0].Name != "Review" {
		t.Errorf("procedures = %+v", f.Procedures)
	}
	fl := f.Flows[0]
	if fl.Start == nil || fl.Start.Next != "Fetch" || fl.End == nil || *fl.End != (setupPoint{10, 20}) {
		t.Errorf("start, end = %+v, %+v", fl.Start, fl.End)
	}
	if len(fl.Schedules) != 1 || fl.Schedules[0].Enabled == nil || *fl.Schedules[0].Enabled {
		t.Errorf("schedules = %+v", fl.Schedules)
	}
	if len(fl.Processes) != 2 || fl.Processes[0].Kind != "script" || !reflect.DeepEqual(fl.Processes[0].Yields, []setupLink{{Object: "Ticket"}}) {
		t.Fatalf("processes = %+v", fl.Processes)
	}
	wantTakes := []setupLink{{Object: "Ticket"}, {Object: "Ticket", States: []string{"open", "done"}, Role: "instrument", At: &setupPoint{1, 2}}}
	if !reflect.DeepEqual(fl.Processes[1].Takes, wantTakes) {
		t.Errorf("takes = %+v, want %+v", fl.Processes[1].Takes, wantTakes)
	}
	if !reflect.DeepEqual(fl.Processes[1].OnFailure, []string{"Fetch"}) {
		t.Errorf("onFailure = %v", fl.Processes[1].OnFailure)
	}
	if len(fl.Objects) != 1 || fl.Objects[0].CopyOf == nil || fl.Objects[0].CopyOf.Flow != "Ticket" {
		t.Errorf("objects = %+v", fl.Objects)
	}
}

// A link or a state that says nothing but its name is written as the name,
// and read back as it was either way.
func TestSetupLinkAndStateYAML(t *testing.T) {
	type holder struct {
		Links  []setupLink  `yaml:"links,omitempty"`
		States []setupState `yaml:"states,omitempty"`
	}
	for _, c := range []struct {
		name string
		in   holder
		yaml string
	}{
		{"bare", holder{[]setupLink{{Object: "Ticket"}}, []setupState{{Name: "open"}}},
			"links:\n    - Ticket\nstates:\n    - open\n"},
		{"with a state", holder{[]setupLink{{Object: "Ticket", State: "open"}}, nil},
			"links:\n    - object: Ticket\n      state: open\n"},
		{"with a key", holder{nil, []setupState{{Name: "open", Key: "open (2)"}}},
			"states:\n    - name: open\n      key: open (2)\n"},
		{"with a rule", holder{nil, []setupState{{Name: "big", Rule: "size > 10"}}},
			"states:\n    - name: big\n      rule: size > 10\n"},
		{"condition", holder{[]setupLink{{Object: "Ticket", Condition: true}}, nil},
			"links:\n    - object: Ticket\n      condition: true\n"},
		{"role and place", holder{[]setupLink{{Object: "Ticket", Role: "instrument", At: &setupPoint{1, 2}}}, nil},
			"links:\n    - object: Ticket\n      role: instrument\n      at:\n        x: 1\n        \"y\": 2\n"},
	} {
		b, err := yaml.Marshal(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(b) != c.yaml {
			t.Errorf("%s: marshalled\n%s\nwant\n%s", c.name, b, c.yaml)
		}
		var out holder
		if err := yaml.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(out, c.in) {
			t.Errorf("%s: read back %+v, want %+v", c.name, out, c.in)
		}
	}
}

// A link's route is kept whatever its shape, and makes the link a mapping.
func TestSetupLinkRouteRoundTrip(t *testing.T) {
	in := setupLink{Object: "Ticket", Route: []any{map[string]any{"x": 1, "y": 2}}}
	b, err := yaml.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out setupLink
	if err := yaml.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	got, err := storedSetupRoute(out.Route)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `[{"x":1,"y":2}]` {
		t.Errorf("route = %s", got)
	}
}

func TestDecodeKnown(t *testing.T) {
	type pair struct {
		A string `yaml:"a"`
		B int    `yaml:"b"`
	}
	for _, c := range []struct {
		yaml string
		want pair
		ok   bool
	}{
		{"a: x\nb: 2\n", pair{"x", 2}, true},
		{"a: x\n", pair{A: "x"}, true},
		{"a: x\nc: 3\n", pair{}, false},
		{"b: not a number\n", pair{}, false},
	} {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(c.yaml), &n); err != nil {
			t.Fatal(err)
		}
		var got pair
		err := decodeKnown(n.Content[0], &got)
		if (err == nil) != c.ok {
			t.Errorf("decodeKnown(%q) error = %v, want ok %v", c.yaml, err, c.ok)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("decodeKnown(%q) = %+v, want %+v", c.yaml, got, c.want)
		}
	}
}

// The first of a name goes by the name, the ones after it by the name with
// a number, skipping numbers another name already has.
func TestSetupKeys(t *testing.T) {
	for _, c := range []struct {
		names, keys []string
	}{
		{nil, []string{}},
		{[]string{"a", "b"}, []string{"", ""}},
		{[]string{"a", "a", "a"}, []string{"", "a (2)", "a (3)"}},
		{[]string{"a", "b", "a", "b"}, []string{"", "", "a (2)", "b (2)"}},
		{[]string{"a", "a", "a (2)"}, []string{"", "a (3)", ""}},
		{[]string{"a (2)", "a", "a"}, []string{"", "", "a (3)"}},
		{[]string{"", ""}, []string{"", " (2)"}},
	} {
		if got := setupKeys(c.names); !reflect.DeepEqual(got, c.keys) {
			t.Errorf("setupKeys(%q) = %q, want %q", c.names, got, c.keys)
		}
	}
}

func TestIdent(t *testing.T) {
	for _, c := range []struct{ name, key, want string }{
		{"Ticket", "", "Ticket"},
		{"Ticket", "Ticket (2)", "Ticket (2)"},
	} {
		if got := ident(c.name, c.key); got != c.want {
			t.Errorf("ident(%q, %q) = %q, want %q", c.name, c.key, got, c.want)
		}
	}
}

// A route the file has is decoded JSON, nil for the default route, and goes
// back to JSON as stored.
func TestSetupRoute(t *testing.T) {
	for _, c := range []struct {
		raw    string
		want   any
		stored string
		ok     bool
	}{
		{"", nil, "", true},
		{"null", nil, "", true},
		{`[{"x":1,"y":2}]`, []any{map[string]any{"x": 1.0, "y": 2.0}}, `[{"x":1,"y":2}]`, true},
		{`{"a":[1,2]}`, map[string]any{"a": []any{1.0, 2.0}}, `{"a":[1,2]}`, true},
		{`[1,`, nil, "", false},
	} {
		v, err := setupRoute([]byte(c.raw))
		if (err == nil) != c.ok {
			t.Errorf("setupRoute(%q) error = %v, want ok %v", c.raw, err, c.ok)
			continue
		}
		if !c.ok {
			continue
		}
		if !reflect.DeepEqual(v, c.want) {
			t.Errorf("setupRoute(%q) = %#v, want %#v", c.raw, v, c.want)
		}
		stored, err := storedSetupRoute(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(stored) != c.stored || (c.stored == "" && stored != nil) {
			t.Errorf("storedSetupRoute(%#v) = %q, want %q", v, stored, c.stored)
		}
	}
	if _, err := storedSetupRoute(func() {}); err == nil {
		t.Error("storedSetupRoute took a route JSON can't hold")
	}
}

func TestProblemf(t *testing.T) {
	err := problemf("%s needs a name", quote("flow"))
	var p setupProblem
	if !errors.As(err, &p) {
		t.Fatalf("problemf gave %T, want a setupProblem", err)
	}
	if err.Error() != `"flow" needs a name` || p.msg != err.Error() {
		t.Errorf("problemf = %q", err.Error())
	}
}
