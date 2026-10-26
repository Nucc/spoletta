package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"unicode/utf8"
)

// A note is a box of Markdown text on a board, the flow's top board or a
// flow process's, saying what the board itself doesn't: why a flow is built
// so, what is unfinished, what someone should look at. It is placed and
// sized anywhere, coloured from a small set, and may point at a process or
// an object of its flow, drawn as a thin line to it while both are on the
// board shown. Notes are part of the board: versions, history, copying and
// pasting and setup files keep them as they keep processes, and diffs list
// them. But they change nothing of how the flow runs: no run reads them,
// OPL leaves them out, and like layout, a change to notes alone doesn't make
// a draft differ from what it published (diff.go).

type note struct {
	ID int64 `json:"id"`
	// The flow process whose board it is on; null for the top board.
	Parent *int64  `json:"parent"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Text   string  `json:"text"`
	Colour string  `json:"colour"`
	// What it points at, if anything: a process or an object of its flow.
	Process *int64 `json:"process"`
	Object  *int64 `json:"object"`
}

// The colours a note comes in, the first a new one's.
var noteColours = []string{"yellow", "blue", "green", "pink", "purple", "grey"}

const (
	// How long a note's text may be, in characters.
	maxNoteText = 10000
	// How small and large a note may be drawn, and how large a new one is.
	minNoteWidth, minNoteHeight = 120.0, 60.0
	maxNoteSize                 = 2000.0
	newNoteWidth, newNoteHeight = 240.0, 140.0
)

const noteColumns = "id, parent_id, x, y, width, height, text, colour, process_id, object_id"

func scanNote(row interface{ Scan(...any) error }, n *note) error {
	return row.Scan(&n.ID, &n.Parent, &n.X, &n.Y, &n.Width, &n.Height, &n.Text, &n.Colour, &n.Process, &n.Object)
}

// What is wrong with a note's text, colour and size, if anything.
func (n note) problem() string {
	switch {
	case utf8.RuneCountInString(n.Text) > maxNoteText:
		return "a note holds at most " + strconv.Itoa(maxNoteText) + " characters"
	case !validNoteColour(n.Colour):
		return "a note is " + list(noteColours, "or")
	case n.Width < minNoteWidth || n.Width > maxNoteSize || n.Height < minNoteHeight || n.Height > maxNoteSize:
		return "a note is at least 120 wide and 60 high, and at most 2000 either way"
	case n.Process != nil && n.Object != nil:
		return "a note points at a process or an object, not both"
	}
	return ""
}

func validNoteColour(c string) bool {
	return slices.Contains(noteColours, c)
}

// What is wrong with what a note of the flow points at, if anything: it
// must be of the same flow. It may be on another board, as after wrapping
// what it points at in a flow process; the line is drawn only while both
// are on the board shown.
func checkNoteTarget(ctx context.Context, db querier, flow int64, n note) (string, error) {
	check := func(query string, id *int64, what string) (string, error) {
		if id == nil {
			return "", nil
		}
		var of int64
		err := db.QueryRowContext(ctx, query, *id).Scan(&of)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && of != flow) {
			return "a note points at a " + what + " of its own flow", nil
		}
		return "", err
	}
	if msg, err := check("SELECT flow_id FROM processes WHERE id = ?", n.Process, "process"); msg != "" || err != nil {
		return msg, err
	}
	return check("SELECT flow_id FROM objects WHERE id = ?", n.Object, "object")
}

func getNote(ctx context.Context, db querier, id int64) (note, int64, error) {
	var n note
	var flow int64
	row := db.QueryRowContext(ctx, "SELECT flow_id, "+noteColumns+" FROM notes WHERE id = ?", id)
	err := row.Scan(&flow, &n.ID, &n.Parent, &n.X, &n.Y, &n.Width, &n.Height, &n.Text, &n.Colour, &n.Process, &n.Object)
	return n, flow, err
}

func registerNoteRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/notes", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		ns := []note{}
		err := scanAll(r.Context(), db, "SELECT "+noteColumns+" FROM notes WHERE flow_id = ? ORDER BY id", func(rows *sql.Rows) error {
			var n note
			err := scanNote(rows, &n)
			ns = append(ns, n)
			return err
		}, f)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ns)
	})

	// A new note on the flow's top board, or on the board of the flow
	// process its parent names; without a size or colour, a new note's.
	mux.HandleFunc("POST /api/notes", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			note
			Flow int64 `json:"flow"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		n := body.note
		if n.Width == 0 && n.Height == 0 {
			n.Width, n.Height = newNoteWidth, newNoteHeight
		}
		if n.Colour == "" {
			n.Colour = noteColours[0]
		}
		if msg := n.problem(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		msg, err := checkParent(ctx, db, n.Parent)
		var flow int64
		if err == nil && msg == "" {
			flow, msg, err = flowFor(ctx, db, body.Flow, n.Parent)
		}
		if err == nil && msg == "" {
			msg, err = checkNoteTarget(ctx, db, flow, n)
		}
		if err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if refuseInStruct(w, r, db, flow) {
			return
		}
		res, err := db.ExecContext(ctx, "INSERT INTO notes (flow_id, parent_id, x, y, width, height, text, colour, process_id, object_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			flow, n.Parent, n.X, n.Y, n.Width, n.Height, n.Text, n.Colour, n.Process, n.Object)
		if err != nil {
			serverError(w, err)
			return
		}
		if n.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, n)
	})

	// Changes what the body gives of the note. Giving process or object
	// points it at what is given, or with null at nothing.
	mux.HandleFunc("PATCH /api/notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			X       *float64        `json:"x"`
			Y       *float64        `json:"y"`
			Width   *float64        `json:"width"`
			Height  *float64        `json:"height"`
			Text    *string         `json:"text"`
			Colour  *string         `json:"colour"`
			Process json.RawMessage `json:"process"`
			Object  json.RawMessage `json:"object"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		n, flow, err := getNote(ctx, db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "note not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		for _, f := range []struct {
			to   *float64
			from *float64
		}{{&n.X, patch.X}, {&n.Y, patch.Y}, {&n.Width, patch.Width}, {&n.Height, patch.Height}} {
			if f.from != nil {
				*f.to = *f.from
			}
		}
		if patch.Text != nil {
			n.Text = *patch.Text
		}
		if patch.Colour != nil {
			n.Colour = *patch.Colour
		}
		if patch.Process != nil || patch.Object != nil {
			n.Process, n.Object = nil, nil
			for _, t := range []struct {
				raw json.RawMessage
				to  **int64
			}{{patch.Process, &n.Process}, {patch.Object, &n.Object}} {
				if t.raw != nil {
					if err := json.Unmarshal(t.raw, t.to); err != nil {
						http.Error(w, "a note points at a process or an object by id", http.StatusBadRequest)
						return
					}
				}
			}
		}
		if msg := n.problem(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if msg, err := checkNoteTarget(ctx, db, flow, n); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		_, err = db.ExecContext(ctx, "UPDATE notes SET x = ?, y = ?, width = ?, height = ?, text = ?, colour = ?, process_id = ?, object_id = ? WHERE id = ?",
			n.X, n.Y, n.Width, n.Height, n.Text, n.Colour, n.Process, n.Object, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, n)
	})

	mux.HandleFunc("DELETE /api/notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM notes WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Writes the notes s has on the flow's boards, cleared first by
// restoreSnapshot: with their saved ids, or for one new to the board, as
// from a setup file, an id of its own. A note on a board s hasn't goes to the
// top board, and one pointing at what s hasn't points at nothing.
func restoreNotes(ctx context.Context, tx *sql.Tx, flow int64, s snapshot) error {
	procs, objs := map[int64]bool{}, map[int64]bool{}
	for _, p := range s.Processes {
		procs[p.ID] = true
	}
	for _, o := range s.Objects {
		objs[o.ID] = true
	}
	known := func(id *int64, in map[int64]bool) *int64 {
		if id == nil || !in[*id] {
			return nil
		}
		return id
	}
	for _, n := range s.Notes {
		var id any
		if n.ID != 0 {
			id = n.ID
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO notes (id, flow_id, parent_id, x, y, width, height, text, colour, process_id, object_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			id, flow, known(n.Parent, procs), n.X, n.Y, n.Width, n.Height, n.Text, n.Colour, known(n.Process, procs), known(n.Object, objs))
		if err != nil {
			return err
		}
	}
	return nil
}
