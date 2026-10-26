package main

import (
	"strings"
	"testing"
)

// Every template kept in the webapp reads, holds one flow alone, and is
// told apart from the others by its id, title and flow.
func TestTemplatesRead(t *testing.T) {
	ts, err := readTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) < 5 || len(ts) > 7 {
		t.Errorf("%d templates", len(ts))
	}
	seen := map[string]bool{}
	for _, tp := range ts {
		for _, k := range []string{"id " + tp.ID, "title " + tp.Title, "flow " + tp.Flow} {
			if seen[k] {
				t.Errorf("two templates have the %s", k)
			}
			seen[k] = true
		}
		if strings.ContainsAny(tp.ID, " /.") || len(tp.Steps) == 0 {
			t.Errorf("template %+v", tp)
		}
		for _, s := range tp.Steps {
			if s.Kind == "sentry" || !validKind(s.Kind) {
				t.Errorf("%s: %s is of kind %q", tp.ID, s.Name, s.Kind)
			}
		}
	}
}

// What a template may not hold: what importing it would make over in the
// app, more than one flow, repositories of its own, or a connector named
// otherwise than by the connector.
func TestTemplatesRefused(t *testing.T) {
	head := "title: T\ndescription: D.\nsetup:\n  spoletta: 1\n"
	flow := "  flows:\n    - name: F\n      processes:\n        - name: P\n"
	cases := map[string]string{
		"no title":         "description: D.\nsetup:\n  spoletta: 1\n" + flow,
		"an MCP server":    head + "  mcpServers:\n    - name: Slack\n      url: https://example.com\n" + flow,
		"a struct":         head + "  structs:\n    - name: Ticket\n" + flow,
		"a procedure":      head + "  procedures:\n    - name: Q\n" + flow,
		"a skill":          head + "  skills:\n    - name: S\n      content: x\n" + flow,
		"two flows":        head + flow + "    - name: G\n",
		"no flow":          head,
		"its repositories": head + "  flows:\n    - name: F\n      repositories:\n        - name: app\n          url: https://example.com/app.git\n",
		"a server's name": head + "  flows:\n    - name: F\n      processes:\n        - name: P\n          kind: connector\n" +
			"          connector:\n            server: Our Slack\n            operation: post_message\n",
		"an operation it lacks": head + "  flows:\n    - name: F\n      processes:\n        - name: P\n          kind: connector\n" +
			"          connector:\n            server: slack\n            operation: fly\n",
		"a field it lacks": head + "author: me\n" + flow,
	}
	for name, body := range cases {
		if _, err := parseTemplate("01-x.yaml", []byte(body)); err == nil {
			t.Errorf("a template with %s reads", name)
		}
	}
	tp, err := parseTemplate("07-ok-then.yaml", []byte(head+flow))
	if err != nil || tp.ID != "ok-then" || tp.Flow != "F" || len(tp.Needs) != 0 {
		t.Errorf("parseTemplate = %+v, %v", tp, err)
	}
}

func TestEscapeLike(t *testing.T) {
	if got := escapeLike(`50%_off\x`); got != `50\%\_off\\x` {
		t.Errorf("escapeLike = %q", got)
	}
}
