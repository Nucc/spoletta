package tests

// Attributes with a type, what each of their values must be: set and
// refused as they are edited, carried by the setup file, told to whoever
// starts a run with inputs, and checked wherever a value is handed in: what
// a script prints, what a run is given and a record written by hand.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// An attribute's type and pattern are set as it is added or changed, a
// pattern alone making it text, and what can't go together is refused.
func TestAttributeTypesAreEdited(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Typed tickets")
	ticket := a.newObject(f.ID, "Ticket")
	attrs := fmt.Sprintf("/api/objects/%d/attributes", ticket.ID)

	var key attribute
	a.call("POST", attrs, map[string]any{"name": "key", "pattern": `[A-Z]+-\d+`}, http.StatusCreated, &key)
	if key.Type != "text" || key.Pattern != `[A-Z]+-\d+` {
		t.Errorf("a pattern alone made %+v", key)
	}
	a.refused("POST", attrs, map[string]any{"name": "size", "type": "string"}, http.StatusBadRequest, `"string" isn't a type of attribute`)
	a.refused("POST", attrs, map[string]any{"name": "size", "type": "text", "pattern": "[A-Z"}, http.StatusBadRequest,
		"the pattern isn't a regular expression: missing closing ]")
	a.refused("POST", attrs, map[string]any{"name": "size", "type": "number", "file": true}, http.StatusBadRequest, "so it has no type")

	var points attribute
	a.call("POST", attrs, map[string]any{"name": "points", "type": "integer"}, http.StatusCreated, &points)
	path := fmt.Sprintf("/api/attributes/%d", points.ID)
	a.refused("PATCH", path, map[string]any{"pattern": `\d+`}, http.StatusBadRequest, "only text has a pattern")
	a.refused("PATCH", path, map[string]any{"file": true}, http.StatusBadRequest, "a file attribute holds the path of a file, so it has no type")
	// Each reply goes into an attribute of its own, as an empty type or
	// pattern is left out of it.
	var file attribute
	a.call("PATCH", path, map[string]any{"type": "", "file": true}, http.StatusOK, &file)
	if file.Type != "" || !file.File {
		t.Errorf("a file of any value is %+v", file)
	}

	// A type other than text drops the pattern; none lets it hold any value.
	keyPath := fmt.Sprintf("/api/attributes/%d", key.ID)
	var integer, text attribute
	a.call("PATCH", keyPath, map[string]any{"type": "integer"}, http.StatusOK, &integer)
	if integer.Type != "integer" || integer.Pattern != "" {
		t.Errorf("an integer is %+v", integer)
	}
	a.call("PATCH", keyPath, map[string]any{"type": "text", "pattern": "T-.*"}, http.StatusOK, nil)
	a.call("PATCH", keyPath, map[string]any{"definition": "Its key"}, http.StatusOK, &text)
	if text.Type != "text" || text.Pattern != "T-.*" {
		t.Errorf("changing its definition left %+v", text)
	}

	// The board's objects carry them.
	var objs []struct {
		ID         int64       `json:"id"`
		Attributes []attribute `json:"attributes"`
	}
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f.ID), nil, http.StatusOK, &objs)
	if len(objs) != 1 || len(objs[0].Attributes) != 2 || objs[0].Attributes[0].Pattern != "T-.*" {
		t.Errorf("the board's objects are %+v", objs)
	}
}

// A script printing a value of another type fails its step, saying which
// and why; one printing values of their types goes on.
func TestTypedOutputsAreChecked(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Typed estimates")
	estimate := a.newProcess(f.ID, map[string]any{"name": "Estimate", "kind": kindScript, "script": "echo"})
	out := a.newObject(f.ID, "Estimate")
	attrs := fmt.Sprintf("/api/objects/%d/attributes", out.ID)
	a.call("POST", attrs, map[string]any{"name": "key", "pattern": `[A-Z]+-\d+`}, http.StatusCreated, nil)
	a.call("POST", attrs, map[string]any{"name": "points", "type": "integer"}, http.StatusCreated, nil)
	a.call("POST", attrs, map[string]any{"name": "due", "type": "datetime"}, http.StatusCreated, nil)
	a.output(estimate.ID, out.ID)
	a.startAt(f.ID, estimate.ID)
	var right atomic.Bool
	a.worker.onScript("Estimate", func(context.Context, scriptRun) runResult {
		if right.Load() {
			return runResult{OK: true, Output: `{"key": "TALK-1", "points": 3, "due": "2026-11-03"}`}
		}
		return runResult{OK: true, Output: `{"key": "TALK-1", "points": "3", "due": "2026-11-03"}`}
	})

	d := a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Estimate"); d.Status != runFailed || !strings.Contains(s.Error, `"points": it must be an integer, a JSON number such as 3`) {
		t.Errorf("a string for an integer ended the run %s, the step saying %q", d.Status, s.Error)
	}
	right.Store(true)
	d = a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("values of their types ended the run %s: %s", d.Status, describeSteps(d))
	}
	if got := contentsOf(d, "Estimate"); len(got) != 1 || strings.Join(strings.Fields(got[0]), "") != `{"key":"TALK-1","points":3,"due":"2026-11-03"}` {
		t.Errorf("Estimate held %q", got)
	}
}

// What a run starts from is described with each value's type, and a value
// of another type is refused before the run starts.
func TestTypedInputsAreChecked(t *testing.T) {
	a := newApp(t)
	tf := newAPITicketFlow(a)
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", tf.id.ID), map[string]any{"type": "text", "pattern": `T-\d+`}, http.StatusOK, nil)

	in := handInputsOf(a, tf.flow.ID, false)
	props, _ := in.Inputs[0].Schema["properties"].(map[string]any)
	id, _ := props["id"].(map[string]any)
	if id["type"] != "string" || id["pattern"] != `^(?:T-\d+)$` {
		t.Errorf("the ticket's id is described as %v", id)
	}

	path := fmt.Sprintf("/api/runs?flow=%d", tf.flow.ID)
	a.refused("POST", path, map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"id": "X-1", "subject": "s"}, "Note": "n"}},
		http.StatusBadRequest, `"id": "X-1" doesn't match its pattern T-\d+`)
	var rn run
	a.call("POST", path, map[string]any{"inputs": map[string]any{"Ticket": map[string]any{"id": "T-1", "subject": "s"}, "Note": "n"}},
		http.StatusCreated, &rn)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Errorf("a ticket of its type ended the run %s: %s", d.Status, describeSteps(d))
	}
}

// A record written by hand may leave a typed field empty, but not give it
// a value of another type; a number written as text is refused, saying to
// write it as JSON.
func TestTypedRecordsWrittenByHand(t *testing.T) {
	a := newApp(t)
	var s flow
	a.call("POST", "/api/flows", map[string]any{"name": "Typed story", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	attrs := fmt.Sprintf("/api/objects/%d/attributes", root.ID)
	a.call("POST", attrs, map[string]any{"name": "id", "unique": true, "pattern": `S-\d+`}, http.StatusCreated, nil)
	a.call("POST", attrs, map[string]any{"name": "points", "type": "integer"}, http.StatusCreated, nil)
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)

	records := fmt.Sprintf("/api/structs/%d/records", s.ID)
	a.refused("POST", records, map[string]any{"content": map[string]any{"id": "S-1", "points": "3"}}, http.StatusBadRequest,
		`"points": it must be an integer, a JSON number such as 3; tick JSON beside it to write one`)
	a.refused("POST", records, map[string]any{"content": map[string]any{"id": "story 1", "points": 3}}, http.StatusBadRequest,
		`"id": "story 1" doesn't match its pattern S-\d+`)
	a.call("POST", records, map[string]any{"content": map[string]any{"id": "S-1", "points": 3}}, http.StatusCreated, nil)
	a.call("POST", records, map[string]any{"content": map[string]any{"id": "S-2", "points": ""}}, http.StatusCreated, nil)
}

// The setup file carries an attribute's type and pattern, a pattern alone
// making it text, and refuses a type there isn't.
func TestSetupCarriesAttributeTypes(t *testing.T) {
	a := setupApp(t)
	prefix := fmt.Sprintf("typed%d", time.Now().UnixNano())
	file := strings.ReplaceAll(`spoletta: 1
flows:
  - name: PFX Flow
    objects:
      - name: Ticket
        attributes:
          - {name: key, pattern: "[A-Z]+-\\d+"}
          - {name: points, type: integer}
          - {name: notes}
`, "PFX", prefix)
	setupImportOK(a, file)
	out := setupExport(a, prefix)
	for _, want := range []string{"type: text", `[A-Z]+-\d+`, "type: integer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the export lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "type:") != 2 {
		t.Errorf("an attribute of any value was given a type:\n%s", out)
	}
	status, body := setupImport(a, strings.Replace(file, "type: integer", "type: whole", 1))
	if status != http.StatusBadRequest || !strings.Contains(string(body), `attribute "points": "whole" isn't a type of attribute`) {
		t.Errorf("a type there isn't = %d %s", status, body)
	}
}

// A struct's copy in a flow has its attributes' types, as it has the rest of
// them, and follows the struct when one changes, so runs on the copy check
// what the struct says.
func TestStructCopiesFollowAttributeTypes(t *testing.T) {
	a := newApp(t)
	var s flow
	a.call("POST", "/api/flows", map[string]any{"name": "Typed bug", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	var key attribute
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "key", "pattern": `B-\d+`}, http.StatusCreated, &key)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "points", "type": "integer"}, http.StatusCreated, nil)
	f := a.newFlow("Fix typed bugs")
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f.ID, "x": 0, "y": 0}, http.StatusCreated, nil)

	copied := func() []attribute {
		t.Helper()
		var objs []struct {
			Name       string      `json:"name"`
			Attributes []attribute `json:"attributes"`
		}
		a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f.ID), nil, http.StatusOK, &objs)
		if len(objs) != 1 || objs[0].Name != "Typed bug" {
			t.Fatalf("the flow holds %+v", objs)
		}
		return objs[0].Attributes
	}
	if as := copied(); len(as) != 2 || as[0].Type != "text" || as[0].Pattern != `B-\d+` || as[1].Type != "integer" {
		t.Errorf("the copy's attributes are %+v", as)
	}
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", key.ID), map[string]any{"pattern": `BUG-\d+`}, http.StatusOK, nil)
	if as := copied(); as[0].Pattern != `BUG-\d+` {
		t.Errorf("the copy didn't follow the struct's pattern: %+v", as[0])
	}
}
