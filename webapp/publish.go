package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// A flow can publish one of its versions (versions.go): the one the runs
// nobody starts by hand from the board take, so that a schedule, a request
// over the API or a run on records never runs a board someone is halfway
// through editing. The board is then the flow's draft. Publishing saves the
// board as a version if it changed since its last, and marks that version;
// any older version can be published again instead, which rolls back.
// Neither changes the board, and bringing the board back to another
// version, or undoing, leaves what is published as it is. A flow that never
// published one runs its board as it is, as every run did before.
//
// Which version each kind of run takes (see runVersion):
//   - the Run button, and a re-run from one of a run's processes, which
//     people start by hand from the board: the board as it is, saved as a
//     version first if it changed. A person can run the version published
//     by hand too.
//   - a schedule's run, one over the API (POST /v1/flows/{id}/runs), and a
//     run a person starts on records: the version published, or the board
//     as it is if there is none. What the API says a flow takes, and which
//     records a flow's queries select, are of that version too.
//   - the flow a subflow process runs (calls.go): in a run of a version
//     published, that flow's version published if it has one, else its
//     board as it is; in a run of a board as it is, the other's board as it
//     is too, so trying a draft tries the procedures' drafts with it.
//
// A setup file is one board: exporting carries no version and nothing of
// what is published, and importing over a flow changes its draft, leaving
// what it published.

// The flow's version published, if any, with its board.
func publishedBoard(ctx context.Context, tx *sql.Tx, flow int64) (*int64, snapshot, error) {
	var id *int64
	var body []byte
	var s snapshot
	err := tx.QueryRowContext(ctx, `SELECT f.published_version_id, v.snapshot FROM flows f
		LEFT JOIN flow_versions v ON v.id = f.published_version_id WHERE f.id = ?`, flow).Scan(&id, &body)
	if err != nil || id == nil {
		return nil, s, err
	}
	return id, s, json.Unmarshal(body, &s)
}

// The board a run nobody starts by hand would take now: the version
// published, or the board as it is.
func boardToRun(ctx context.Context, tx *sql.Tx, flow int64) (snapshot, error) {
	id, s, err := publishedBoard(ctx, tx, flow)
	if err != nil || id != nil {
		return s, err
	}
	return takeSnapshot(ctx, tx, flow)
}

// The version a run of the flow takes, and its board: with published, the
// version published if there is one; otherwise the board as it is, saved
// as a version first, as saved says, if it changed since its last. Says
// whether it took the version published.
func runVersion(ctx context.Context, tx *sql.Tx, flow int64, published bool, saved saving) (int64, snapshot, bool, error) {
	if published {
		id, s, err := publishedBoard(ctx, tx, flow)
		if err != nil || id != nil {
			return derefID(id), s, err == nil, err
		}
	}
	version, err := saveVersion(ctx, tx, flow, true, saved)
	if err != nil {
		return 0, snapshot{}, false, err
	}
	s, err := takeSnapshot(ctx, tx, flow)
	return version, s, false, err
}

func derefID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// Which version a flow published, as the Versions panel shows it: the
// version, when and by whom, and whether the board differs from it in more
// than where things sit.
type publishedState struct {
	Published   *int64     `json:"published"`
	PublishedAt *time.Time `json:"publishedAt"`
	PublishedBy string     `json:"publishedBy"`
	Draft       bool       `json:"draft"`
}

func publishedStateOf(ctx context.Context, tx *sql.Tx, flow int64) (publishedState, error) {
	var p publishedState
	err := tx.QueryRowContext(ctx, "SELECT published_version_id, published_at, "+userLabelColumn("published_by")+" FROM flows WHERE id = ?", flow).
		Scan(&p.Published, &p.PublishedAt, &p.PublishedBy)
	if err != nil || p.Published == nil {
		return p, err
	}
	_, published, err := publishedBoard(ctx, tx, flow)
	if err != nil {
		return p, err
	}
	board, err := takeSnapshot(ctx, tx, flow)
	if err != nil {
		return p, err
	}
	p.Draft = diffSnapshots(published, board).changed()
	return p, nil
}

// Marks a version of the flow as the one it published.
func publishVersion(ctx context.Context, tx *sql.Tx, flow, version int64, by *int64) error {
	_, err := tx.ExecContext(ctx, "UPDATE flows SET published_version_id = ?, published_at = CURRENT_TIMESTAMP(3), published_by = ? WHERE id = ?", version, by, flow)
	return err
}

func registerPublishRoutes(mux *http.ServeMux, db *sql.DB) {
	// Publishes one of the flow's versions, saved before, such as an older
	// one to roll back to; the board stays as it is.
	mux.HandleFunc("POST /api/versions/{id}/publish", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		var f int64
		var structFlow bool
		err = tx.QueryRowContext(ctx, "SELECT v.flow_id, f.is_struct FROM flow_versions v JOIN flows f ON f.id = v.flow_id WHERE v.id = ?", id).Scan(&f, &structFlow)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "version not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if structFlow {
			http.Error(w, notInStruct, http.StatusBadRequest)
			return
		}
		if err := publishVersion(ctx, tx, f, id, requestUserID(r)); err != nil {
			serverError(w, err)
			return
		}
		p, err := publishedStateOf(ctx, tx, f)
		if err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})

	// Stops publishing a version of the flow, whose runs take its board as
	// it is again.
	mux.HandleFunc("DELETE /api/versions/published", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		res, err := db.ExecContext(r.Context(), "UPDATE flows SET published_version_id = NULL, published_at = NULL, published_by = NULL WHERE id = ?", f)
		if err != nil {
			serverError(w, err)
			return
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			var exists int
			if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM flows WHERE id = ?", f).Scan(&exists); err != nil || exists == 0 {
				http.Error(w, "flow not found", http.StatusNotFound)
				return
			}
		}
		writeJSON(w, http.StatusOK, publishedState{})
	})
}
