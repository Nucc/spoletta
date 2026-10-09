package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
)

type process struct {
	ID int64 `json:"id"`
	// The flow it is in, on its top board or a flow process's. Versions
	// leave it out: they belong to the flow.
	Flow   int64  `json:"flow,omitempty"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
	Script string `json:"script"`
	// Model ("provider/id") and thinking level for agent processes; empty uses the pi default.
	Model    string  `json:"model"`
	Thinking string  `json:"thinking"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	// Set when resized; null draws the default width.
	Width *float64 `json:"width"`
	// The flow process whose board it is on; null on the top board.
	Parent *int64 `json:"parent"`
	// For flow processes: each time it runs is a run of its own.
	Recording bool            `json:"recording"`
	Settings  processSettings `json:"settings"`
}

// The settings of kinds that take more than a prompt or a script, by kind,
// so switching kinds back and forth loses none.
type processSettings struct {
	Sentry *sentrySettings `json:"sentry,omitempty"`
	// The MCP servers an agent process may use, by id; none by default. See mcp.go.
	MCPServers []int64 `json:"mcpServers,omitempty"`
	// The flow's repositories an agent or script process works on, by name;
	// all of them when absent, none when empty. See workspace.go.
	Repositories *[]string `json:"repositories,omitempty"`
	// The flow a subflow process runs, by id; see calls.go.
	Flow *int64 `json:"flow,omitempty"`
}

func (s processSettings) sentry() sentrySettings {
	if s.Sentry == nil {
		return defaultSentrySettings()
	}
	return *s.Sentry
}

// Why the settings can't be saved, or "".
func (s processSettings) problem() string {
	if s.Sentry != nil {
		return s.Sentry.problem()
	}
	return ""
}

// Stored as JSON; processes from before settings have none.
func (s *processSettings) Scan(v any) error {
	*s = processSettings{}
	switch b := v.(type) {
	case nil:
		return nil
	case []byte:
		return json.Unmarshal(b, s)
	case string:
		return json.Unmarshal([]byte(b), s)
	}
	return errors.New("settings: unexpected type")
}

func (s processSettings) Value() (driver.Value, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

// A process is either run by an agent from its prompt, executes its script,
// or is handled by a human, who reviews its inputs following its prompt and
// approves them or requests changes, and fills in the objects it generates. A flow process is made of processes
// itself, on a board of its own; see boards.go. A subflow process runs
// another flow, which other flows can reuse the same way; see calls.go. A
// Sentry process fetches issues from Sentry; see sentry.go.
const (
	kindAgent   = "agent"
	kindScript  = "script"
	kindHuman   = "human"
	kindFlow    = "flow"
	kindSubflow = "subflow"
	kindSentry  = "sentry"
)

const invalidKindMessage = "kind must be agent, script, human, flow, subflow or sentry"

func validKind(k string) bool {
	return k == kindAgent || k == kindScript || k == kindHuman || k == kindFlow || k == kindSubflow || k == kindSentry
}

const processColumns = "id, flow_id, name, kind, prompt, script, model, thinking, x, y, width, parent_id, recording, settings"

func scanProcess(row interface{ Scan(...any) error }, p *process) error {
	return row.Scan(&p.ID, &p.Flow, &p.Name, &p.Kind, &p.Prompt, &p.Script, &p.Model, &p.Thinking, &p.X, &p.Y, &p.Width, &p.Parent, &p.Recording, &p.Settings)
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

// Resized boxes stay between these widths, so a mistyped request can't make
// one vanish or swallow the board.
const (
	minWidth            = 80
	maxWidth            = 2000
	invalidWidthMessage = "width must be between 80 and 2000"
)

func validWidth(w *float64) bool {
	return w == nil || (*w >= minWidth && *w <= maxWidth)
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
		{"width", "DOUBLE NULL AFTER y"},
		{"settings", "JSON NULL"},
	} {
		if err := addColumn(db, "processes", c.name, c.def); err != nil {
			return err
		}
	}
	return migrateFlow(db)
}

// Tables created by older versions lack later columns; MySQL has no ADD COLUMN IF NOT EXISTS.
func addColumn(db *sql.DB, table, name, def string) error {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, name).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + def)
	return err
}

func hasIndex(db *sql.DB, table, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?`, table, name).Scan(&n)
	return n > 0, err
}

func registerProcessRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/processes", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		rows, err := db.QueryContext(r.Context(), "SELECT "+processColumns+" FROM processes WHERE flow_id = ? ORDER BY id", f)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		ps := []process{}
		for rows.Next() {
			var p process
			if err := scanProcess(rows, &p); err != nil {
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

		if !validWidth(p.Width) {
			http.Error(w, invalidWidthMessage, http.StatusBadRequest)
			return
		}
		if msg := p.Settings.problem(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if msg, err := checkParent(r.Context(), db, p.Parent); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		var msg string
		var err error
		if p.Flow, msg, err = flowFor(r.Context(), db, p.Flow, p.Parent); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if msg, err := checkCall(r.Context(), db, p.Flow, p.Kind, p.Settings); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(r.Context(), "INSERT INTO processes (flow_id, name, kind, prompt, script, model, thinking, x, y, width, parent_id, recording, settings) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			p.Flow, p.Name, p.Kind, p.Prompt, p.Script, p.Model, p.Thinking, p.X, p.Y, p.Width, p.Parent, p.Recording, p.Settings)
		if err != nil {
			serverError(w, err)
			return
		}
		if p.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		if p.Kind == kindFlow {
			if err := ensureBoard(r.Context(), tx, p.ID); err != nil {
				serverError(w, err)
				return
			}
		}
		if err := bringOutcomes(r.Context(), tx, p.ID); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
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
			Width    *float64 `json:"width"`
			// Turning it off keeps the runs it recorded.
			Recording *bool `json:"recording"`
			// Replaces all of them.
			Settings *processSettings `json:"settings"`
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
		if !validWidth(patch.Width) {
			http.Error(w, invalidWidthMessage, http.StatusBadRequest)
			return
		}
		if patch.Settings != nil {
			if msg := patch.Settings.problem(); msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
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

		var oldKind string
		var flow int64
		var oldSettings processSettings
		err = db.QueryRowContext(r.Context(), "SELECT kind, flow_id, settings FROM processes WHERE id = ?", id).Scan(&oldKind, &flow, &oldSettings)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			serverError(w, err)
			return
		}
		if oldKind != "" && (patch.Kind != nil || patch.Settings != nil) {
			kind, settings := oldKind, oldSettings
			if patch.Kind != nil {
				kind = *patch.Kind
			}
			if patch.Settings != nil {
				settings = *patch.Settings
			}
			if msg, err := checkCall(r.Context(), db, flow, kind, settings); err != nil {
				serverError(w, err)
				return
			} else if msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}

		// COALESCE keeps the stored value for fields absent from the patch.
		_, err = db.ExecContext(r.Context(),
			"UPDATE processes SET name = COALESCE(?, name), kind = COALESCE(?, kind), prompt = COALESCE(?, prompt), script = COALESCE(?, script), model = COALESCE(?, model), thinking = COALESCE(?, thinking), x = COALESCE(?, x), y = COALESCE(?, y), width = COALESCE(?, width), recording = COALESCE(?, recording), settings = COALESCE(?, settings) WHERE id = ?",
			patch.Name, patch.Kind, patch.Prompt, patch.Script, patch.Model, patch.Thinking, patch.X, patch.Y, patch.Width, patch.Recording, patch.Settings, id)
		if err != nil {
			serverError(w, err)
			return
		}
		// Becoming a flow gives it a board; ceasing to be one keeps the board,
		// so switching back loses nothing.
		if patch.Kind != nil && *patch.Kind == kindFlow && oldKind != "" {
			if err := ensureBoard(r.Context(), db, id); err != nil {
				serverError(w, err)
				return
			}
		}
		if patch.Kind != nil && *patch.Kind != oldKind && (oldKind == kindHuman || *patch.Kind == kindHuman) {
			if err := dropMismatchedEdges(r.Context(), db, id, *patch.Kind); err != nil {
				serverError(w, err)
				return
			}
		}
		// Picking the flow a subflow process runs brings its outcomes in.
		if (patch.Kind != nil && *patch.Kind != oldKind) || (patch.Settings != nil && !sameFlow(patch.Settings.Flow, oldSettings.Flow)) {
			if err := inTx(r.Context(), db, func(tx *sql.Tx) error { return bringOutcomes(r.Context(), tx, id) }); err != nil {
				serverError(w, err)
				return
			}
		}

		var p process
		err = scanProcess(db.QueryRowContext(r.Context(), "SELECT "+processColumns+" FROM processes WHERE id = ?", id), &p)
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

	// Its links and connections go with it, and a flow process's board with
	// all on it; runs keep their steps.
	mux.HandleFunc("DELETE /api/processes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM processes WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// A human process leaves only through the states it can decide on, while
// other processes leave through the objects they generate and older direct
// connections. Becoming or ceasing to be human deletes the outputs the new
// kind can't have: a human's decisions don't say what another process
// generates, nor the other way round. Ceasing to be human also deletes the
// agent links of the people who carried it out.
func dropMismatchedEdges(ctx context.Context, db *sql.DB, id int64, kind string) error {
	stmts := []string{"DELETE FROM object_links WHERE process_id = ? AND direction = 'output'"}
	if kind == kindHuman {
		stmts = append(stmts, "DELETE FROM connections WHERE source_id = ?")
	} else {
		stmts = append(stmts, `DELETE l FROM object_links l JOIN objects o ON o.id = l.object_id
			WHERE l.process_id = ? AND l.direction = 'input' AND o.human`)
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

func sameFlow(a, b *int64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Runs fn in a transaction, committed if it succeeds.
func inTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
