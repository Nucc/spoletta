package main

// Tags are words a flow, procedure or struct is given to be found by, such
// as "billing" or "nightly": the flow list shows them on each and narrows
// to one (FlowList.tsx). Those who may change a flow tag it. They are kept
// in flow_tags, a row each, and written with their flow in the setup file;
// they aren't on its board, so versions and undo leave them be, as they do
// its owner.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// How many tags a flow has at most, and how long each is.
	maxTags      = 20
	maxTagLength = 64
)

// The tags as given, each trimmed, its spaces made single, and once
// whatever its case, the first spelling kept, in the order of their names
// ignoring case; empty ones are dropped. Says what is wrong, if anything.
func cleanTags(given []string) ([]string, string) {
	tags := []string{}
	seen := map[string]bool{}
	for _, t := range given {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagLength {
			return nil, fmt.Sprintf("the tag %s is longer than %d characters", quote(t), maxTagLength)
		}
		seen[strings.ToLower(t)] = true
		tags = append(tags, t)
	}
	if len(tags) > maxTags {
		return nil, fmt.Sprintf("a flow has at most %d tags", maxTags)
	}
	slices.SortStableFunc(tags, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return tags, ""
}

// The tags of each of the flows, by id, in the order cleanTags puts them;
// none for a flow without.
func flowTags(ctx context.Context, db rowsQuerier, ids []int64) (map[int64][]string, error) {
	tags := map[int64][]string{}
	if len(ids) == 0 {
		return tags, nil
	}
	err := scanAll(ctx, db, "SELECT flow_id, tag FROM flow_tags WHERE flow_id IN "+placeholders(len(ids)), func(rows *sql.Rows) error {
		var id int64
		var t string
		err := rows.Scan(&id, &t)
		tags[id] = append(tags[id], t)
		return err
	}, int64Args(ids)...)
	for id, ts := range tags {
		tags[id], _ = cleanTags(ts)
	}
	return tags, err
}

// Makes the flow's tags these, which cleanTags has cleaned.
func setTags(ctx context.Context, tx execer, flow int64, tags []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM flow_tags WHERE flow_id = ?", flow); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := tx.ExecContext(ctx, "INSERT INTO flow_tags (flow_id, tag) VALUES (?, ?)", flow, t); err != nil {
			return err
		}
	}
	return nil
}

func registerTagRoutes(mux *http.ServeMux, db *sql.DB) {
	// Gives the flow these tags, in place of those it had.
	mux.HandleFunc("PUT /api/flows/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		tags, msg := cleanTags(body.Tags)
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		// Locking the flow orders this against another change of its tags.
		var found int64
		err = tx.QueryRowContext(r.Context(), "SELECT id FROM flows WHERE id = ? FOR UPDATE", id).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err == nil {
			err = setTags(r.Context(), tx, id, tags)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]string{"tags": tags})
	})
}
