package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type process struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
	Script string `json:"script"`
	// Model ("provider/id") and thinking level for agent processes; empty uses the pi default.
	Model    string  `json:"model"`
	Thinking string  `json:"thinking"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

// A process is either run by an agent from its prompt, executes its script,
// or is handled by a human, who reviews its inputs following its prompt and
// approves them or requests changes.
const (
	kindAgent  = "agent"
	kindScript = "script"
	kindHuman  = "human"
)

const invalidKindMessage = "kind must be agent, script or human"

func validKind(k string) bool {
	return k == kindAgent || k == kindScript || k == kindHuman
}

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func validThinking(t string) bool {
	if t == "" {
		return true
	}
	for _, l := range thinkingLevels {
		if t == l {
			return true
		}
	}
	return false
}

// Model ids are passed to pi as arguments; a leading dash would read as an option.
func validModel(m string) bool {
	return !strings.HasPrefix(m, "-") && len(m) <= 255
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS processes (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		kind VARCHAR(16) NOT NULL DEFAULT 'agent',
		prompt TEXT NOT NULL,
		script TEXT NOT NULL,
		model VARCHAR(255) NOT NULL DEFAULT '',
		thinking VARCHAR(16) NOT NULL DEFAULT '',
		x DOUBLE NOT NULL DEFAULT 0,
		y DOUBLE NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return err
	}
	for _, c := range []struct{ name, def string }{
		{"prompt", "TEXT NOT NULL AFTER name"},
		{"kind", "VARCHAR(16) NOT NULL DEFAULT 'agent' AFTER name"},
		{"script", "TEXT NOT NULL AFTER prompt"},
		{"model", "VARCHAR(255) NOT NULL DEFAULT '' AFTER script"},
		{"thinking", "VARCHAR(16) NOT NULL DEFAULT '' AFTER model"},
	} {
		if err := addProcessColumn(db, c.name, c.def); err != nil {
			return err
		}
	}
	return migrateFlow(db)
}

// Tables created by older versions lack later columns; MySQL has no ADD COLUMN IF NOT EXISTS.
func addProcessColumn(db *sql.DB, name, def string) error {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'processes' AND COLUMN_NAME = ?`, name).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	_, err = db.Exec("ALTER TABLE processes ADD COLUMN " + name + " " + def)
	return err
}

func registerProcessRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/processes", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, name, kind, prompt, script, model, thinking, x, y FROM processes ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		ps := []process{}
		for rows.Next() {
			var p process
			if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Prompt, &p.Script, &p.Model, &p.Thinking, &p.X, &p.Y); err != nil {
				serverError(w, err)
				return
			}
			ps = append(ps, p)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, ps)
	})

	mux.HandleFunc("POST /api/processes", func(w http.ResponseWriter, r *http.Request) {
		var p process
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if p.Kind == "" {
			p.Kind = kindAgent
		}
		if !validKind(p.Kind) {
			http.Error(w, invalidKindMessage, http.StatusBadRequest)
			return
		}
		if !validModel(p.Model) || !validThinking(p.Thinking) {
			http.Error(w, "invalid model or thinking level", http.StatusBadRequest)
			return
		}

		res, err := db.ExecContext(r.Context(), "INSERT INTO processes (name, kind, prompt, script, model, thinking, x, y) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			p.Name, p.Kind, p.Prompt, p.Script, p.Model, p.Thinking, p.X, p.Y)
		if err != nil {
			serverError(w, err)
			return
		}
		if p.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})

	mux.HandleFunc("PATCH /api/processes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var patch struct {
			Name     *string  `json:"name"`
			Kind     *string  `json:"kind"`
			Prompt   *string  `json:"prompt"`
			Script   *string  `json:"script"`
			Model    *string  `json:"model"`
			Thinking *string  `json:"thinking"`
			X        *float64 `json:"x"`
			Y        *float64 `json:"y"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Name != nil {
			if *patch.Name = strings.TrimSpace(*patch.Name); *patch.Name == "" {
				http.Error(w, "name must not be empty", http.StatusBadRequest)
				return
			}
		}
		if patch.Kind != nil && !validKind(*patch.Kind) {
			http.Error(w, invalidKindMessage, http.StatusBadRequest)
			return
		}
		if (patch.Model != nil && !validModel(*patch.Model)) || (patch.Thinking != nil && !validThinking(*patch.Thinking)) {
			http.Error(w, "invalid model or thinking level", http.StatusBadRequest)
			return
		}

		// COALESCE keeps the stored value for fields absent from the patch.
		_, err = db.ExecContext(r.Context(),
			"UPDATE processes SET name = COALESCE(?, name), kind = COALESCE(?, kind), prompt = COALESCE(?, prompt), script = COALESCE(?, script), model = COALESCE(?, model), thinking = COALESCE(?, thinking), x = COALESCE(?, x), y = COALESCE(?, y) WHERE id = ?",
			patch.Name, patch.Kind, patch.Prompt, patch.Script, patch.Model, patch.Thinking, patch.X, patch.Y, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if patch.Kind != nil {
			if err := dropMismatchedEdges(r.Context(), db, id, *patch.Kind); err != nil {
				serverError(w, err)
				return
			}
		}

		var p process
		err = db.QueryRowContext(r.Context(), "SELECT id, name, kind, prompt, script, model, thinking, x, y FROM processes WHERE id = ?", id).
			Scan(&p.ID, &p.Name, &p.Kind, &p.Prompt, &p.Script, &p.Model, &p.Thinking, &p.X, &p.Y)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "process not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	})
}

// A human process leaves only through its routes, while other processes
// leave through the objects they generate and older direct connections.
// Changing kind deletes whichever of these the new kind doesn't have.
func dropMismatchedEdges(ctx context.Context, db *sql.DB, id int64, kind string) error {
	stmts := []string{"DELETE FROM routes WHERE process_id = ?"}
	if kind == kindHuman {
		stmts = []string{
			"DELETE FROM object_links WHERE process_id = ? AND direction = 'output'",
			"DELETE FROM connections WHERE source_id = ?",
		}
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s, id); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
