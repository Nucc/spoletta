package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

// A flow's history is the boards it had as it was changed, for undoing and
// redoing changes. The board calls for a step after changing, and each step
// keeps the whole board as a version does. The newest step not undone is
// the board as it is; undoing goes back a step and redoing forward again,
// rewriting the board as versions do. A change made after undoing drops the
// steps undone, so they can't be redone.

// How many steps a flow's history keeps.
const historySteps = 100

// Whether there is a change to undo or to redo, whether the board changed
// just now, and whether a run of it holds it, refusing undo and redo until
// it ends (lockBoard).
type historyState struct {
	Undo    bool `json:"undo"`
	Redo    bool `json:"redo"`
	Changed bool `json:"changed"`
	Held    bool `json:"held"`
}

// The step the board is on, the newest not undone; 0 with none.
func currentStep(ctx context.Context, tx *sql.Tx, flow int64) (int64, []byte, error) {
	var id int64
	var body []byte
	err := tx.QueryRowContext(ctx, "SELECT id, snapshot FROM flow_history WHERE flow_id = ? AND NOT undone ORDER BY id DESC LIMIT 1", flow).Scan(&id, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, nil
	}
	return id, body, err
}

// The board as a step keeps it: without the flow's settings, which undoing
// leaves as they are.
func boardStep(ctx context.Context, tx *sql.Tx, flow int64) (snapshot, error) {
	s, err := takeSnapshot(ctx, tx, flow)
	s.Settings = nil
	return s, err
}

// Adds the board as it is as a step, unless the board is on the step it
// already is; a step added drops the steps undone. Says whether it added one.
func recordStep(ctx context.Context, tx *sql.Tx, flow int64) (bool, error) {
	s, err := boardStep(ctx, tx, flow)
	if err != nil {
		return false, err
	}
	body, err := json.Marshal(s)
	if err != nil {
		return false, err
	}
	current, saved, err := currentStep(ctx, tx, flow)
	if err != nil {
		return false, err
	}
	if current != 0 {
		if same, err := sameSnapshot(body, saved); err != nil || same {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM flow_history WHERE flow_id = ? AND undone", flow); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO flow_history (flow_id, snapshot) VALUES (?, ?)", flow, body); err != nil {
		return false, err
	}
	var oldest int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM flow_history WHERE flow_id = ? ORDER BY id DESC LIMIT 1 OFFSET ?", flow, historySteps-1).Scan(&oldest)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM flow_history WHERE flow_id = ? AND id < ?", flow, oldest)
	return true, err
}

// Rewrites the board as step has it. The step then keeps the board as it
// came out, with copies following what they copy as it is now, so that the
// board is on it.
func goToStep(ctx context.Context, tx *sql.Tx, flow, step int64) error {
	var body []byte
	if err := tx.QueryRowContext(ctx, "SELECT snapshot FROM flow_history WHERE id = ?", step).Scan(&body); err != nil {
		return err
	}
	var s snapshot
	if err := json.Unmarshal(body, &s); err != nil {
		return err
	}
	if err := rewriteBoard(ctx, tx, flow, s); err != nil {
		return err
	}
	if s, err := boardStep(ctx, tx, flow); err != nil {
		return err
	} else if body, err = json.Marshal(s); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE flow_history SET snapshot = ? WHERE id = ?", body, step)
	return err
}

// Goes back a step, if there is one before the board's.
func undoStep(ctx context.Context, tx *sql.Tx, flow int64) (bool, error) {
	// A change not called for yet is the step undone.
	if _, err := recordStep(ctx, tx, flow); err != nil {
		return false, err
	}
	current, _, err := currentStep(ctx, tx, flow)
	if err != nil || current == 0 {
		return false, err
	}
	var previous int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM flow_history WHERE flow_id = ? AND NOT undone AND id < ? ORDER BY id DESC LIMIT 1", flow, current).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE flow_history SET undone = TRUE WHERE id = ?", current); err != nil {
		return false, err
	}
	return true, goToStep(ctx, tx, flow, previous)
}

// Goes forward to the step undone last, if any.
func redoStep(ctx context.Context, tx *sql.Tx, flow int64) (bool, error) {
	// A change since undoing drops the steps undone.
	if recorded, err := recordStep(ctx, tx, flow); err != nil || recorded {
		return false, err
	}
	var next int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM flow_history WHERE flow_id = ? AND undone ORDER BY id LIMIT 1", flow).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE flow_history SET undone = FALSE WHERE id = ?", next); err != nil {
		return false, err
	}
	return true, goToStep(ctx, tx, flow, next)
}

// Whether the flow's history has a step to undo, and one to redo.
func historyOf(ctx context.Context, tx *sql.Tx, flow int64) (historyState, error) {
	var h historyState
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(NOT undone), 0) > 1, COALESCE(SUM(undone), 0) > 0
		FROM flow_history WHERE flow_id = ?`, flow).Scan(&h.Undo, &h.Redo)
	return h, err
}

func registerHistoryRoutes(mux *http.ServeMux, db *sql.DB) {
	// Each changes the board's history, and says what it holds then.
	for path, step := range map[string]func(context.Context, *sql.Tx, int64) (bool, error){
		"POST /api/history":      recordStep,
		"POST /api/history/undo": undoStep,
		"POST /api/history/redo": redoStep,
	} {
		rewrites := path != "POST /api/history"
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			f, ok := flowParam(w, r)
			if !ok {
				return
			}
			ctx := r.Context()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				serverError(w, err)
				return
			}
			defer tx.Rollback()

			running, err := lockBoard(ctx, tx, f)
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "flow not found", http.StatusNotFound)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
			if running && rewrites {
				http.Error(w, "can't undo or redo while the flow is running", http.StatusConflict)
				return
			}
			changed, err := step(ctx, tx, f)
			if err != nil {
				serverError(w, err)
				return
			}
			h, err := historyOf(ctx, tx, f)
			if err != nil {
				serverError(w, err)
				return
			}
			h.Changed, h.Held = changed && rewrites, running
			if err := tx.Commit(); err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, h)
		})
	}
}
