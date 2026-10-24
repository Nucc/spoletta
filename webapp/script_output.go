package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// What a script step prints, stdout and stderr in the order they came, is
// kept a chunk at a time as the worker streams it (see worker/output.go),
// so a person follows the script in its step's terminal window as it runs,
// whichever replica runs it. Its stdout is still its output, read from its
// result once it ends; this is for reading.
type outputChunk struct {
	ID        int64     `json:"id"`
	Stream    string    `json:"stream"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

// Runs the script in the worker, which streams what it prints to onOutput
// and the lines it logs to onLog as they come, then its result. A worker
// that replies with the result alone, its log in it, is taken as well.
func streamScript(ctx context.Context, workerURL string, run scriptRun, onOutput func(stream, text string), onLog func(workerLogLine)) (runResult, error) {
	req, err := workerRequest(ctx, http.MethodPost, workerURL+"/script", run)
	if err != nil {
		return runResult{}, err
	}
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := doWorker(req)
	if err != nil {
		return runResult{}, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-ndjson") {
		var res runResult
		if err := dec.Decode(&res); err != nil {
			return runResult{}, errors.New("decode response: " + err.Error())
		}
		for _, l := range res.Log {
			onLog(l)
		}
		res.Log = nil
		return res, nil
	}
	for {
		var rec struct {
			Output *struct {
				Stream string `json:"stream"`
				Text   string `json:"text"`
			} `json:"output"`
			Log    *workerLogLine `json:"log"`
			Result *runResult     `json:"result"`
		}
		if err := dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("it ended the script without a result")
			}
			return runResult{}, err
		}
		if rec.Result != nil {
			return *rec.Result, nil
		}
		if rec.Output != nil {
			onOutput(rec.Output.Stream, rec.Output.Text)
		}
		if rec.Log != nil {
			onLog(*rec.Log)
		}
	}
}

func recordOutput(db *sql.DB, step int64, stream, text string) {
	if stream != "stderr" {
		stream = "stdout"
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO run_step_output (step_id, stream, text) VALUES (?, ?, ?)", step, stream, text); err != nil {
		log.Printf("record what step %d printed: %v", step, err)
	}
}

func registerScriptOutputRoutes(mux *http.ServeMux, db *sql.DB) {
	// What the script step printed, in order; ?after= a chunk's id gives only
	// those since, for following a step while it goes.
	mux.HandleFunc("GET /api/runs/{run}/steps/{id}/output", func(w http.ResponseWriter, r *http.Request) {
		runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
		id, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var after int64
		if v := r.URL.Query().Get("after"); v != "" {
			if after, err1 = strconv.ParseInt(v, 10, 64); err1 != nil {
				http.Error(w, "invalid after", http.StatusBadRequest)
				return
			}
		}
		chunks := []outputChunk{}
		err := scanAll(r.Context(), db,
			`SELECT o.id, o.stream, o.text, o.created_at FROM run_step_output o
			JOIN run_steps s ON s.id = o.step_id
			WHERE o.step_id = ? AND s.run_id = ? AND o.id > ? ORDER BY o.id LIMIT 1000`,
			func(rows *sql.Rows) error {
				var c outputChunk
				if err := rows.Scan(&c.ID, &c.Stream, &c.Text, &c.CreatedAt); err != nil {
					return err
				}
				chunks = append(chunks, c)
				return nil
			}, id, runID, after)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, chunks)
	})
}
