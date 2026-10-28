package main

// Comments on a review: while a human process's step waits, those who may
// decide it and those who may edit its flow say what they think of it to
// one another, beside its form, before somebody decides it, such as a
// reviewer asking another to look at the amount. Each comment is a person's
// and when they wrote it; none is edited or deleted. They stay with the
// step once it is decided, stopped or failed, shown in the run's panel,
// though none can be added then; nobody is told of one, and no run, agent
// or OPL reads them.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The longest comment, and the most a review keeps.
const (
	maxCommentRunes   = 4000
	maxReviewComments = 200
)

// Commenting on a review, which those holding its role may do.
var commentPattern = regexp.MustCompile(`^/api/runs/\d+/steps/\d+/comments$`)

// A comment on a review: who wrote it, empty without sign-in, and when.
type reviewComment struct {
	ID     int64     `json:"id"`
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// The comments on the run's reviews, by step, oldest first.
func reviewComments(ctx context.Context, db rowsQuerier, run int64) (map[int64][]reviewComment, error) {
	comments := map[int64][]reviewComment{}
	err := scanAll(ctx, db, "SELECT id, step_id, "+userLabelColumn("user_id")+", text, created_at FROM review_comments WHERE run_id = ? ORDER BY id",
		func(rows *sql.Rows) error {
			var c reviewComment
			var step int64
			err := rows.Scan(&c.ID, &step, &c.Author, &c.Text, &c.At)
			comments[step] = append(comments[step], c)
			return err
		}, run)
	return comments, err
}

func registerCommentRoutes(mux *http.ServeMux, db *sql.DB) {
	// Comments on the review waiting: {"text": "..."}. Returns its comments.
	mux.HandleFunc("POST /api/runs/{run}/steps/{id}/comments", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		text := strings.TrimSpace(body.Text)
		switch n := utf8.RuneCountInString(text); {
		case n == 0:
			http.Error(w, "a comment says something", http.StatusBadRequest)
			return
		case n > maxCommentRunes:
			http.Error(w, fmt.Sprintf("a comment is at most %d characters", maxCommentRunes), http.StatusBadRequest)
			return
		}
		run, err := strconv.ParseInt(r.PathValue("run"), 10, 64)
		if err != nil {
			http.Error(w, "invalid run", http.StatusBadRequest)
			return
		}
		step, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid step", http.StatusBadRequest)
			return
		}
		var kind, status, runStatus string
		var process sql.NullInt64
		var escalated bool
		var flow int64
		err = db.QueryRowContext(r.Context(),
			`SELECT s.kind, s.status, s.process_ref, s.escalated, r.status, r.flow_id FROM run_steps s JOIN runs r ON r.id = s.run_id
			WHERE s.id = ? AND s.run_id = ?`, step, run).Scan(&kind, &status, &process, &escalated, &runStatus, &flow)
		switch {
		case errors.Is(err, sql.ErrNoRows) || (err == nil && kind != kindHuman):
			http.Error(w, "no review with that id in this run", http.StatusNotFound)
			return
		case err != nil:
			serverError(w, err)
			return
		case status != stepWaiting || runStatus != runRunning:
			http.Error(w, "the review no longer waits, so it takes no more comments", http.StatusConflict)
			return
		}
		// Those who may decide it, as its run's board has them, or edit
		// its flow.
		u := requestUser(r)
		access, err := flowAccess(r.Context(), db, u, flow)
		if err != nil {
			serverError(w, err)
			return
		}
		if access != accessEdit {
			b, err := runBoardOf(r.Context(), db, run)
			if err != nil {
				serverError(w, err)
				return
			}
			var roles []string
			if u != nil {
				if roles, err = userRoles(r.Context(), db, u.ID); err != nil {
					serverError(w, err)
					return
				}
			}
			if !decidesStep(u, roles, access, kind, b.deciders(process.Int64, escalated)) {
				http.Error(w, "only those who may decide this review, or edit its flow, comment on it", http.StatusForbidden)
				return
			}
		}
		var n int
		if err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM review_comments WHERE step_id = ?", step).Scan(&n); err != nil {
			serverError(w, err)
			return
		}
		if n >= maxReviewComments {
			http.Error(w, fmt.Sprintf("a review keeps at most %d comments", maxReviewComments), http.StatusConflict)
			return
		}
		if _, err := db.ExecContext(r.Context(), "INSERT INTO review_comments (run_id, step_id, user_id, text) VALUES (?, ?, ?, ?)",
			run, step, requestUserID(r), text); err != nil {
			serverError(w, err)
			return
		}
		comments, err := reviewComments(r.Context(), db, run)
		if err != nil {
			serverError(w, err)
			return
		}
		out := comments[step]
		if out == nil {
			out = []reviewComment{}
		}
		writeJSON(w, http.StatusCreated, out)
	})
}
