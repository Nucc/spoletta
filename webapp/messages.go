package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// An agent step records its conversation as pi has it, a message at a time
// while it goes: the prompt, each reply of the model with its thinking and
// tool calls, and each tool's result. The messages are pi's own (see
// docs/message-types.md in pi), trimmed to what is worth keeping.
type stepMessage struct {
	ID int64 `json:"id"`
	// Which try of the step it belongs to, from 1; an agent that didn't hand
	// in its output gets a second.
	Attempt   int             `json:"attempt"`
	Message   json.RawMessage `json:"message"`
	CreatedAt time.Time       `json:"createdAt"`
}

const (
	// Each text, thinking or tool call's arguments are cut to this.
	maxMessageText = 100 << 10
	// A message still larger is recorded by its role only.
	maxMessageBytes = 1 << 20
)

func recordMessage(ctx context.Context, db *sql.DB, stepID int64, attempt int, raw json.RawMessage) error {
	m, err := trimMessage(raw)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "INSERT INTO run_step_messages (step_id, attempt, message) VALUES (?, ?, ?)", stepID, attempt, string(m))
	return err
}

// Drops what only the provider can read, such as signatures, and images,
// and cuts long texts.
func trimMessage(raw json.RawMessage) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if blocks, ok := m["content"].([]any); ok {
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok {
				continue
			}
			delete(block, "textSignature")
			delete(block, "thinkingSignature")
			delete(block, "thoughtSignature")
			switch block["type"] {
			case "image":
				delete(block, "data")
			case "text":
				block["text"] = clip(block["text"])
			case "thinking":
				block["thinking"] = clip(block["thinking"])
			case "toolCall":
				if args, err := json.Marshal(block["arguments"]); err == nil && len(args) > maxMessageText {
					block["arguments"] = map[string]any{"truncated": clip(string(args))}
				}
			}
		}
	}
	// Tool-specific details, such as a diff, can be large; the content says
	// what the model saw.
	if d, err := json.Marshal(m["details"]); err == nil && len(d) > maxMessageText {
		delete(m, "details")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if len(out) > maxMessageBytes {
		return json.Marshal(map[string]any{"role": m["role"], "omitted": len(out)})
	}
	return out, nil
}

func clip(v any) any {
	s, ok := v.(string)
	if !ok || len(s) <= maxMessageText {
		return v
	}
	return s[:maxMessageText] + fmt.Sprintf("\n… (%d more bytes)", len(s)-maxMessageText)
}

func registerMessageRoutes(mux *http.ServeMux, db *sql.DB) {
	// A step's conversation, in order; ?after= a message id gives only the
	// messages since, for following a step while it goes.
	mux.HandleFunc("GET /api/runs/{run}/steps/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
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
		rows, err := db.QueryContext(r.Context(),
			`SELECT m.id, m.attempt, m.message, m.created_at FROM run_step_messages m
			JOIN run_steps s ON s.id = m.step_id
			WHERE m.step_id = ? AND s.run_id = ? AND m.id > ? ORDER BY m.id`,
			id, runID, after)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		ms := []stepMessage{}
		for rows.Next() {
			var m stepMessage
			var msg []byte
			if err := rows.Scan(&m.ID, &m.Attempt, &msg, &m.CreatedAt); err != nil {
				serverError(w, err)
				return
			}
			m.Message = msg
			ms = append(ms, m)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ms)
	})
}
