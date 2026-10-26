package main

// Each person stars the flows, procedures and structs they come back to as
// their favourites, which the flow list puts first (FlowList.tsx). They are
// kept in favourite_flows, a row each; without sign-in they are everyone's,
// user 0's, as the connections to MCP servers are. Anyone who sees a flow
// may star it, as starring changes nothing in it; see neededAccess.

import (
	"context"
	"database/sql"
	"net/http"
	"regexp"
	"strconv"
)

// Starring a flow, or taking its star away.
var favouritePattern = regexp.MustCompile(`^/api/flows/\d+/favourite$`)

// Whose favourites a request is about: its user's, or without sign-in,
// everyone's.
func favouritesOf(r *http.Request) int64 {
	if u := requestUser(r); u != nil {
		return u.ID
	}
	return 0
}

// Which of the flows are the person's favourites.
func favouriteFlows(ctx context.Context, db rowsQuerier, person int64, ids []int64) (map[int64]bool, error) {
	starred := map[int64]bool{}
	if len(ids) == 0 {
		return starred, nil
	}
	err := scanAll(ctx, db, "SELECT flow_id FROM favourite_flows WHERE user_id = ? AND flow_id IN "+placeholders(len(ids)), func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		starred[id] = true
		return err
	}, append([]any{person}, int64Args(ids)...)...)
	return starred, err
}

func registerFavouriteRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("PUT /api/flows/{id}/favourite", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		_, err = db.ExecContext(r.Context(), "INSERT INTO favourite_flows (user_id, flow_id) VALUES (?, ?) ON DUPLICATE KEY UPDATE flow_id = flow_id", favouritesOf(r), id)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /api/flows/{id}/favourite", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM favourite_flows WHERE user_id = ? AND flow_id = ?", favouritesOf(r), id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
