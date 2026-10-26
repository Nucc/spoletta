package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Presence is who else has a flow's board open: each browser tab showing a
// board says so every few seconds (PUT /api/flows/{id}/presence), keeping a
// row of board_viewers by flow, person and tab, and is answered with the
// others there and whether someone else changed the board since it last
// loaded. Without sign-in there are no people, so nobody is shown, but a
// tab is still told of changes made in another.
//
// A board changes through many routes, but each change is kept as a step of
// its history (history.go), which the board asks for once its changes are
// answered, a setup import or the assistant makes, and undoing or redoing
// moves along. So each step added or gone to is a row of board_changes,
// with who made it and from which tab: a tab that sees a change from
// another loads the board again, and says who made it. Someone is editing
// while they changed the board in the last minute.
//
// Everything is kept in the database, by its clock, so that a replica
// answers for tabs another heard from; see docs/REPLICAS.md. A tab not
// heard from for presenceTimeout is gone, as it is at once when it says it
// leaves; rows older than a minute go as the board's tabs are heard from,
// and changes older than an hour as the board changes again.

// How long a tab not heard from still counts as having its board open; the
// board says so every few seconds.
const presenceTimeout = "20 SECOND"

// How long after their last change to the board someone counts as editing it.
const editingFor = "60 SECOND"

// Saying a board is open, or leaving it, changes nothing on it, so seeing
// the flow is enough; see neededAccess.
var presencePattern = regexp.MustCompile(`^/api/flows/\d+/presence$`)

// How long a tab's own id may be: the board makes up one of its own.
const maxClient = 64

// Who changed a board, and from which tab; neither for a change nobody
// signing in made, or that no tab did, such as the assistant's.
type boardChanger struct {
	user   *int64
	client string
}

// The changer a request is.
func requestChanger(r *http.Request) boardChanger {
	return boardChanger{user: requestUserID(r), client: clientParam(r)}
}

func clientParam(r *http.Request) string {
	c := strings.TrimSpace(r.URL.Query().Get("client"))
	if len(c) > maxClient {
		return ""
	}
	return c
}

// Says the flow's board changed, as by says, and lets go of the changes
// made over an hour ago.
func noteBoardChange(ctx context.Context, tx *sql.Tx, flow int64, by boardChanger) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO board_changes (flow_id, user_id, client) VALUES (?, ?, ?)", flow, by.user, by.client); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM board_changes WHERE flow_id = ? AND changed_at < NOW(3) - INTERVAL 1 HOUR", flow)
	return err
}

// Someone else with the board open.
type boardViewer struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Editing bool   `json:"editing"`
}

// What a tab with the board open is told: the latest change to the board,
// as rev, which it gives as since next time; whether another tab changed
// it since the one given, and who, if it was someone else; and the others
// with it open, without whoever asks.
type presence struct {
	Rev       int64         `json:"rev"`
	Changed   bool          `json:"changed"`
	ChangedBy string        `json:"changedBy"`
	People    []boardViewer `json:"people"`
}

func registerPresenceRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("PUT /api/flows/{id}/presence", func(w http.ResponseWriter, r *http.Request) {
		flow, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Client string `json:"client"`
			// The latest change the tab has loaded, as rev told it; with
			// none, as before it heard, it is told of none.
			Since *int64 `json:"since"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Client == "" || len(body.Client) > maxClient {
			http.Error(w, "client is required, at most 64 characters", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		u := requestUser(r)
		res := presence{People: []boardViewer{}}
		if u != nil {
			_, err := db.ExecContext(ctx, `INSERT INTO board_viewers (flow_id, user_id, client, seen_at) VALUES (?, ?, ?, NOW(3))
				ON DUPLICATE KEY UPDATE seen_at = NOW(3)`, flow, u.ID, body.Client)
			if mysqlErrorIs(err, mysqlErrForeignKey) {
				http.Error(w, "flow not found", http.StatusNotFound)
				return
			}
			if err == nil {
				_, err = db.ExecContext(ctx, "DELETE FROM board_viewers WHERE flow_id = ? AND seen_at < NOW(3) - INTERVAL 1 MINUTE", flow)
			}
			if err == nil {
				err = scanAll(ctx, db, `SELECT DISTINCT v.user_id, COALESCE(NULLIF(u.name, ''), u.email),
						EXISTS (SELECT 1 FROM board_changes c WHERE c.flow_id = ? AND c.user_id = v.user_id AND c.changed_at > NOW(3) - INTERVAL `+editingFor+`)
					FROM board_viewers v JOIN users u ON u.id = v.user_id
					WHERE v.flow_id = ? AND v.user_id <> ? AND v.seen_at > NOW(3) - INTERVAL `+presenceTimeout+` AND u.removed_at IS NULL
					ORDER BY 2, 1`, func(rows *sql.Rows) error {
					var p boardViewer
					err := rows.Scan(&p.ID, &p.Name, &p.Editing)
					res.People = append(res.People, p)
					return err
				}, flow, flow, u.ID)
			}
			if err != nil {
				serverError(w, err)
				return
			}
		}
		if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM board_changes WHERE flow_id = ?", flow).Scan(&res.Rev); err != nil {
			serverError(w, err)
			return
		}
		if body.Since != nil && res.Rev > *body.Since {
			var by *int64
			var name string
			err := db.QueryRowContext(ctx, "SELECT user_id, "+userLabelColumn("user_id")+" FROM board_changes WHERE flow_id = ? AND id > ? AND client <> ? ORDER BY id DESC LIMIT 1",
				flow, *body.Since, body.Client).Scan(&by, &name)
			switch {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				serverError(w, err)
				return
			default:
				res.Changed = true
				if by != nil && (u == nil || *by != u.ID) {
					res.ChangedBy = name
				}
			}
		}
		writeJSON(w, http.StatusOK, res)
	})

	// The tab leaves the board, as it closes or shows another page.
	mux.HandleFunc("DELETE /api/flows/{id}/presence", func(w http.ResponseWriter, r *http.Request) {
		flow, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if u := requestUser(r); u != nil {
			if _, err := db.ExecContext(r.Context(), "DELETE FROM board_viewers WHERE flow_id = ? AND user_id = ? AND client = ?", flow, u.ID, clientParam(r)); err != nil {
				serverError(w, err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
