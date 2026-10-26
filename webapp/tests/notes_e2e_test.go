package tests

// Notes on a board say what the board doesn't: they are board data that
// versions, history, pasting and setup files keep and diffs list, but no
// run reads them, and like layout they don't make a draft differ from what
// it published.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type boardNote struct {
	ID      int64   `json:"id"`
	Parent  *int64  `json:"parent"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	Text    string  `json:"text"`
	Colour  string  `json:"colour"`
	Process *int64  `json:"process"`
	Object  *int64  `json:"object"`
}

func notesNew(a *app, n map[string]any) boardNote {
	a.t.Helper()
	var out boardNote
	a.call("POST", "/api/notes", n, http.StatusCreated, &out)
	return out
}

func notesOf(a *app, f int64) []boardNote {
	a.t.Helper()
	var ns []boardNote
	a.call("GET", fmt.Sprintf("/api/notes?flow=%d", f), nil, http.StatusOK, &ns)
	return ns
}

func notesTexts(ns []boardNote) []string {
	out := []string{}
	for _, n := range ns {
		out = append(out, n.Text)
	}
	return out
}

// A note goes on a board, sized and coloured, pointing at a process or an
// object of its flow or at nothing; what it points at going leaves it
// pointing at nothing, and a flow process going takes the notes on its
// board with it.
func TestNotesOnBoards(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Board notes")
	fetch := a.agent(f.ID, "Fetch", "Fetch it.")
	ticket := a.newObject(f.ID, "Ticket")
	outer := editFlowProcess(a, f.ID, "Outer")

	n := notesNew(a, map[string]any{"flow": f.ID, "x": 10, "y": 20, "text": "Why we **fetch** first", "process": fetch.ID})
	if n.Colour != "yellow" || n.Width != 240 || n.Height != 140 || n.Parent != nil || n.Process == nil || *n.Process != fetch.ID {
		t.Errorf("a new note = %+v, want a yellow one of the default size pointing at Fetch", n)
	}
	inner := notesNew(a, map[string]any{"parent": outer.ID, "text": "Unfinished", "colour": "pink", "width": 300, "height": 200})
	if inner.Parent == nil || *inner.Parent != outer.ID || inner.Colour != "pink" || inner.Width != 300 {
		t.Errorf("a note inside Outer = %+v", inner)
	}

	var changed boardNote
	a.call("PATCH", fmt.Sprintf("/api/notes/%d", n.ID), map[string]any{"text": "Look here", "colour": "blue", "object": ticket.ID, "width": 400}, http.StatusOK, &changed)
	if changed.Text != "Look here" || changed.Colour != "blue" || changed.Process != nil || changed.Object == nil || *changed.Object != ticket.ID || changed.Width != 400 || changed.X != 10 {
		t.Errorf("the note changed = %+v, want it pointing at Ticket instead, the rest kept", changed)
	}
	a.call("PATCH", fmt.Sprintf("/api/notes/%d", n.ID), map[string]any{"x": 50}, http.StatusOK, &changed)
	if changed.Object == nil || changed.X != 50 {
		t.Errorf("moving the note = %+v, want it still pointing at Ticket", changed)
	}

	other := a.newFlow("Board notes elsewhere")
	elsewhere := a.agent(other.ID, "Far", "Far away.")
	var strct flow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprint("Ledger", time.Now().UnixNano()), "struct": true}, http.StatusCreated, &strct)
	a.refused("POST", "/api/notes", map[string]any{"flow": strct.ID, "text": "x"}, http.StatusBadRequest, "a struct holds a single object")
	a.refused("POST", "/api/notes", map[string]any{"flow": f.ID, "text": "x", "colour": "orange"}, http.StatusBadRequest, "yellow, blue")
	a.refused("POST", "/api/notes", map[string]any{"flow": f.ID, "text": "x", "process": elsewhere.ID}, http.StatusBadRequest, "of its own flow")
	a.refused("POST", "/api/notes", map[string]any{"flow": f.ID, "text": "x", "process": fetch.ID, "object": ticket.ID}, http.StatusBadRequest, "not both")
	a.refused("POST", "/api/notes", map[string]any{"parent": fetch.ID, "text": "x"}, http.StatusBadRequest, "parent must be a flow process")
	a.refused("POST", "/api/notes", map[string]any{"flow": f.ID, "text": strings.Repeat("x", 10001)}, http.StatusBadRequest, "at most 10000")
	a.refused("PATCH", fmt.Sprintf("/api/notes/%d", n.ID), map[string]any{"height": 10}, http.StatusBadRequest, "at least 120 wide and 60 high")
	a.refused("PATCH", fmt.Sprintf("/api/notes/%d", n.ID), map[string]any{"object": elsewhere.ID}, http.StatusBadRequest, "of its own flow")
	a.refused("PATCH", fmt.Sprintf("/api/notes/%d", int64(1<<40)), map[string]any{"x": 1}, http.StatusNotFound, "note not found")

	// Notes say nothing to agents.
	status, body := a.do("GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil)
	if status != http.StatusOK || strings.Contains(string(body), "Look here") || strings.Contains(string(body), "Unfinished") {
		t.Errorf("the flow's OPL = %d %s, want no notes in it", status, body)
	}

	a.call("DELETE", fmt.Sprintf("/api/objects/%d", ticket.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", outer.ID), nil, http.StatusNoContent, nil)
	ns := notesOf(a, f.ID)
	if len(ns) != 1 || ns[0].ID != n.ID || ns[0].Object != nil || ns[0].Process != nil {
		t.Errorf("after deleting Ticket and Outer, notes = %+v, want the top board's pointing at nothing", ns)
	}
	a.call("DELETE", fmt.Sprintf("/api/notes/%d", n.ID), nil, http.StatusNoContent, nil)
	if ns := notesOf(a, f.ID); len(ns) != 0 {
		t.Errorf("after deleting the note, notes = %+v", ns)
	}
}

// Versions and undo keep notes as the rest of the board, ids and all.
func TestNotesInVersionsAndHistory(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Board notes history")
	fetch := a.agent(f.ID, "Fetch", "Fetch it.")
	histRecord(a, f.ID)
	n := notesNew(a, map[string]any{"flow": f.ID, "text": "First", "process": fetch.ID})
	histRecord(a, f.ID)
	v := verSave(a, f.ID)

	verPatch(a, fmt.Sprintf("/api/notes/%d", n.ID), map[string]any{"text": "Second"})
	histRecord(a, f.ID)
	if h := histUndo(a, f.ID); !h.Changed {
		t.Fatalf("undoing the note's change = %+v", h)
	}
	if ns := notesOf(a, f.ID); len(ns) != 1 || ns[0].ID != n.ID || ns[0].Text != "First" || ns[0].Process == nil || *ns[0].Process != fetch.ID {
		t.Errorf("after undoing, notes = %+v, want the first text back on the same note", ns)
	}
	histRedo(a, f.ID)
	if ns := notesOf(a, f.ID); len(ns) != 1 || ns[0].Text != "Second" {
		t.Errorf("after redoing, notes = %+v", ns)
	}
	// Undoing the note's adding takes it away.
	histUndo(a, f.ID)
	histUndo(a, f.ID)
	if ns := notesOf(a, f.ID); len(ns) != 0 {
		t.Errorf("after undoing the note's adding, notes = %+v", ns)
	}

	verRestore(a, v.ID)
	if ns := notesOf(a, f.ID); len(ns) != 1 || ns[0].ID != n.ID || ns[0].Text != "First" {
		t.Errorf("after restoring the version, notes = %+v", ns)
	}
	var board struct {
		Notes []boardNote `json:"notes"`
	}
	a.call("GET", fmt.Sprintf("/api/versions/%d", v.ID), nil, http.StatusOK, &board)
	if len(board.Notes) != 1 || board.Notes[0].Text != "First" {
		t.Errorf("the version's board has notes %+v", board.Notes)
	}

	// A flow with no notes saves versions as before them, so the board
	// unchanged is the version it was saved as.
	plain := a.newFlow("Board without notes")
	a.agent(plain.ID, "Only", "Only this.")
	saved := verSave(a, plain.ID)
	if l := pubPublish(a, plain.ID, ""); l.ID != saved.ID {
		t.Errorf("publishing the unchanged board saved version %d, want %d kept", l.ID, saved.ID)
	}
}

// Notes copied go with what is copied, pointing at what they pointed at if
// that went along, and the notes on a flow process's board go with it.
func TestNotesPasted(t *testing.T) {
	a := newApp(t)
	src := a.newFlow("Board notes from")
	fetch := a.agent(src.ID, "Fetch", "Fetch it.")
	ticket := a.newObject(src.ID, "Ticket")
	outer := editFlowProcess(a, src.ID, "Outer")
	about := notesNew(a, map[string]any{"flow": src.ID, "x": 100, "y": 100, "text": "About Fetch", "process": fetch.ID, "colour": "green"})
	behind := notesNew(a, map[string]any{"flow": src.ID, "x": 0, "y": 0, "text": "About Ticket", "object": ticket.ID})
	notesNew(a, map[string]any{"parent": outer.ID, "x": 5, "y": 6, "text": "Inside"})

	dst := a.newFlow("Board notes into")
	var res struct {
		Processes []int64 `json:"processes"`
		Notes     []int64 `json:"notes"`
	}
	a.call("POST", "/api/paste", map[string]any{"from": src.ID, "processes": []int64{fetch.ID, outer.ID}, "notes": []int64{about.ID, behind.ID},
		"flow": dst.ID, "dx": 10, "dy": 20}, http.StatusCreated, &res)
	if len(res.Notes) != 2 || len(res.Processes) != 2 {
		t.Fatalf("pasted %+v, want both notes and both processes", res)
	}
	ps := editProcesses(a, dst.ID)
	byText := map[string]boardNote{}
	for _, n := range notesOf(a, dst.ID) {
		byText[n.Text] = n
	}
	if n := byText["About Fetch"]; n.Process == nil || *n.Process != ps["Fetch"].ID || n.X != 110 || n.Y != 120 || n.Colour != "green" || n.Parent != nil {
		t.Errorf("About Fetch pasted = %+v, want pointing at Fetch's copy, moved with it", n)
	}
	if n := byText["About Ticket"]; n.Object != nil || n.Process != nil {
		t.Errorf("About Ticket pasted = %+v, want pointing at nothing, Ticket left behind", n)
	}
	if n := byText["Inside"]; n.Parent == nil || *n.Parent != ps["Outer"].ID || n.X != 5 || n.Y != 6 {
		t.Errorf("Inside pasted = %+v, want on Outer's copy where it was", n)
	}

	// Notes alone paste too, but only from one board.
	a.call("POST", "/api/paste", map[string]any{"from": src.ID, "notes": []int64{about.ID}, "flow": dst.ID, "dx": 0, "dy": 300}, http.StatusCreated, &res)
	if len(res.Notes) != 1 || len(res.Processes) != 0 {
		t.Errorf("pasting a note alone = %+v", res)
	}
	inner := notesOf(a, src.ID)
	var insideID int64
	for _, n := range inner {
		if n.Text == "Inside" {
			insideID = n.ID
		}
	}
	a.refused("POST", "/api/paste", map[string]any{"from": src.ID, "notes": []int64{about.ID, insideID}, "flow": dst.ID}, http.StatusBadRequest, "same board")
}

// A setup file carries a board's notes, naming what they point at; one
// leaving them out keeps those the board has, and an empty list clears
// them. A note keeping its text keeps its id.
func TestNotesInSetupFiles(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprint("Board notes setup ", time.Now().UnixNano())
	f := a.newFlow(name)
	fetch := a.agent(f.ID, "Fetch", "Fetch it.")
	outer := editFlowProcess(a, f.ID, "Outer")
	ticket := a.newObject(f.ID, "Ticket")
	kept := notesNew(a, map[string]any{"flow": f.ID, "x": 1, "y": 2, "width": 300, "height": 100, "text": "Why", "process": fetch.ID, "colour": "purple"})
	notesNew(a, map[string]any{"parent": outer.ID, "text": "Inside", "object": ticket.ID})

	var export struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", f.ID), nil, http.StatusOK, &export)
	var file struct {
		Flows []struct {
			Name      string `yaml:"name"`
			Notes     []map[string]any
			Processes []struct {
				Name  string `yaml:"name"`
				Board *struct {
					Notes []map[string]any `yaml:"notes"`
				} `yaml:"board"`
			} `yaml:"processes"`
		} `yaml:"flows"`
	}
	if err := yaml.Unmarshal([]byte(export.Text), &file); err != nil || len(file.Flows) != 1 {
		t.Fatalf("the export %s: %v", export.Text, err)
	}
	top := file.Flows[0].Notes
	if len(top) != 1 || top[0]["text"] != "Why" || top[0]["process"] != "Fetch" || top[0]["colour"] != "purple" || top[0]["width"] != 300 {
		t.Errorf("the top board's notes exported = %+v", top)
	}
	for _, p := range file.Flows[0].Processes {
		if p.Name == "Outer" && (p.Board == nil || len(p.Board.Notes) != 1 || p.Board.Notes[0]["object"] != "Ticket" || p.Board.Notes[0]["colour"] != nil) {
			t.Errorf("Outer's board exported = %+v", p.Board)
		}
	}

	// The file's notes replace the board's; one with the same text keeps its id.
	setupImportOK(a, fmt.Sprintf(`spoletta: 1
flows:
  - name: %s
    processes:
      - name: Fetch
      - name: Outer
        kind: flow
    objects:
      - name: Ticket
    notes:
      - text: Why
        object: Ticket
      - text: "New, and **bold**"
        colour: green
`, name))
	// Outer's board said nothing of notes, so it kept its own.
	ns := notesOf(a, f.ID)
	if got := strings.Join(notesTexts(ns), ", "); got != "Why, Inside, New, and **bold**" {
		t.Fatalf("after importing notes, notes are %q", got)
	}
	if n := ns[0]; n.ID != kept.ID || n.Object == nil || *n.Object != ticket.ID || n.Process != nil || n.X != 1 || n.Width != 300 || n.Colour != "yellow" {
		t.Errorf("Why imported = %+v, want it kept, its place and size too, yellow, pointing at Ticket", n)
	}
	if n := ns[2]; n.Colour != "green" || n.Width != 240 || n.Parent != nil {
		t.Errorf("the new note imported = %+v", n)
	}

	// Leaving notes out keeps them; an empty list clears them.
	setupImportOK(a, fmt.Sprintf("spoletta: 1\nflows:\n  - name: %s\n    processes: [{name: Fetch}, {name: Outer, kind: flow}]\n    objects: [{name: Ticket}]\n", name))
	if got := notesTexts(notesOf(a, f.ID)); len(got) != 3 {
		t.Errorf("after importing a file without notes, notes = %v, want them kept", got)
	}
	setupImportOK(a, fmt.Sprintf("spoletta: 1\nflows:\n  - name: %s\n    processes: [{name: Fetch}]\n    notes: []\n", name))
	if got := notesOf(a, f.ID); len(got) != 0 {
		t.Errorf("after importing notes: [], notes = %+v", got)
	}

	status, body := setupImport(a, fmt.Sprintf("spoletta: 1\nflows:\n  - name: %s\n    notes: [{text: x, process: Nobody}]\n", name))
	if status != http.StatusBadRequest || !strings.Contains(string(body), "isn't a process of the flow") {
		t.Errorf("importing a note pointing at nothing there = %d %s", status, body)
	}
}

// A note added, changed or removed shows in the diff, but a change to notes
// alone leaves the board what it published.
func TestNotesInDiffs(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Board notes diff")
	fetch := a.agent(f.ID, "Fetch", "Fetch it.")
	gone := notesNew(a, map[string]any{"flow": f.ID, "text": "Going"})
	changed := notesNew(a, map[string]any{"flow": f.ID, "text": "# Heading\nand more"})
	v := pubPublish(a, f.ID, "")

	a.call("DELETE", fmt.Sprintf("/api/notes/%d", gone.ID), nil, http.StatusNoContent, nil)
	verPatch(a, fmt.Sprintf("/api/notes/%d", changed.ID), map[string]any{"colour": "blue", "process": fetch.ID, "x": 300})
	notesNew(a, map[string]any{"flow": f.ID, "text": "Arriving"})
	if l := pubList(a, f.ID); l.Draft {
		t.Errorf("after changing only notes, versions = %+v, want no draft", l)
	}
	var d struct {
		Notes     []pubItem `json:"notes"`
		Processes []pubItem `json:"processes"`
		Layout    bool      `json:"layout"`
	}
	a.call("GET", fmt.Sprintf("/api/versions/%d/diff", v.ID), nil, http.StatusOK, &d)
	got := strings.Join(pubNames(d.Notes), "; ")
	if got != "changed Heading; added Arriving; removed Going" || !d.Layout || len(d.Processes) != 0 {
		t.Errorf("the diff's notes = %q, layout %v, processes %v", got, d.Layout, d.Processes)
	}
	for _, it := range d.Notes {
		if it.Change != "changed" {
			continue
		}
		fields := map[string]string{}
		for _, fd := range it.Fields {
			fields[fd.Field] = fd.Before + " → " + fd.After
		}
		if fields["colour"] != "yellow → blue" || fields["points at"] != " → Fetch" || len(fields) != 2 {
			t.Errorf("the changed note's fields = %v", fields)
		}
	}
}
