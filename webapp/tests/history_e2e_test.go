package tests

import (
	"fmt"
	"net/http"
	"testing"
)

// What a flow's history holds, and whether the board changed just now.
type history struct {
	Undo    bool `json:"undo"`
	Redo    bool `json:"redo"`
	Changed bool `json:"changed"`
}

func histCall(a *app, f int64, what string) history {
	a.t.Helper()
	var h history
	a.call("POST", fmt.Sprintf("/api/history%s?flow=%d", what, f), nil, http.StatusOK, &h)
	return h
}

func histRecord(a *app, f int64) history { a.t.Helper(); return histCall(a, f, "") }
func histUndo(a *app, f int64) history   { a.t.Helper(); return histCall(a, f, "/undo") }
func histRedo(a *app, f int64) history   { a.t.Helper(); return histCall(a, f, "/redo") }

// Undoing goes back through the changes one by one, ids and all, and
// redoing forward again, until a change drops what was undone.
func TestUndoAndRedo(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Undo")
	other := a.newFlow("Other")
	a.agent(other.ID, "Elsewhere", "Stay.")
	otherBoard := verDump(a, other.ID)

	if h := histRecord(a, f.ID); h.Undo || h.Redo {
		t.Errorf("a new flow's history = %+v", h)
	}
	if h := histUndo(a, f.ID); h.Changed {
		t.Errorf("undoing with nothing to undo = %+v", h)
	}
	empty := verDump(a, f.ID)

	p := a.agent(f.ID, "Draft", "Draft it.")
	histRecord(a, f.ID)
	drafted := verDump(a, f.ID)

	o := a.newObject(f.ID, "Draft text")
	a.output(p.ID, o.ID)
	// Recording an unchanged board adds no step.
	histRecord(a, f.ID)
	histRecord(a, f.ID)
	linked := verDump(a, f.ID)

	// A change not recorded yet is the one undone.
	verPatch(a, fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"name": "Write"})
	renamed := verDump(a, f.ID)

	if h := histUndo(a, f.ID); !h.Changed || !h.Undo || !h.Redo {
		t.Errorf("first undo = %+v", h)
	}
	verSameBoard(t, linked, verDump(a, f.ID))
	histUndo(a, f.ID)
	verSameBoard(t, drafted, verDump(a, f.ID))
	if h := histUndo(a, f.ID); !h.Changed || h.Undo || !h.Redo {
		t.Errorf("undo back to the start = %+v", h)
	}
	verSameBoard(t, empty, verDump(a, f.ID))
	if h := histUndo(a, f.ID); h.Changed {
		t.Errorf("undoing past the start = %+v", h)
	}

	// Redoing brings back what was removed, with the same ids.
	histRedo(a, f.ID)
	verSameBoard(t, drafted, verDump(a, f.ID))
	histRedo(a, f.ID)
	verSameBoard(t, linked, verDump(a, f.ID))
	if h := histRedo(a, f.ID); !h.Changed || h.Redo {
		t.Errorf("last redo = %+v", h)
	}
	verSameBoard(t, renamed, verDump(a, f.ID))
	if h := histRedo(a, f.ID); h.Changed {
		t.Errorf("redoing past the end = %+v", h)
	}

	// A change after undoing drops what was undone.
	histUndo(a, f.ID)
	histUndo(a, f.ID)
	a.agent(f.ID, "Review", "Review it.")
	if h := histRedo(a, f.ID); h.Changed || h.Redo {
		t.Errorf("redo after a change = %+v", h)
	}
	if ps := verProcesses(a, f.ID); len(ps) != 2 {
		t.Errorf("after the change, processes %+v", ps)
	}
	histUndo(a, f.ID)
	verSameBoard(t, drafted, verDump(a, f.ID))

	verSameBoard(t, otherBoard, verDump(a, other.ID))
	a.refused("POST", "/api/history/undo", nil, http.StatusBadRequest, "flow is required")
	a.refused("POST", fmt.Sprintf("/api/history/undo?flow=%d", int64(1<<40)), nil, http.StatusNotFound, "flow not found")

	// A flow goes with its history.
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusNoContent, nil)
}
