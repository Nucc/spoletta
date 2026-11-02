package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-sql-driver/mysql"
)

// The start node is a board's single UML initial node. It points at the
// processes a run of the board begins with, all at once, or none; where it
// is, a flow's top board keeps in flow_starts and a flow process's board in
// inner_boards, and what it points at start_targets, a row per process.
type startNode struct {
	X float64
	Y float64
	// In the order of the processes' ids.
	Targets []startTarget
}

// A process a start points at, and where the link to it runs, if moved by
// hand.
type startTarget struct {
	Process int64           `json:"process"`
	Route   json.RawMessage `json:"route"`
}

// As JSON a start has its targets, and the first again as target with its
// route, as versions from before a start pointed at several had it, so an
// older webapp still reads where its board starts.
type startJSON struct {
	X       float64         `json:"x"`
	Y       float64         `json:"y"`
	Targets []startTarget   `json:"targets"`
	Target  *int64          `json:"target"`
	Route   json.RawMessage `json:"route"`
}

func (s startNode) MarshalJSON() ([]byte, error) {
	out := startJSON{X: s.X, Y: s.Y, Targets: s.Targets}
	if out.Targets == nil {
		out.Targets = []startTarget{}
	}
	if len(s.Targets) > 0 {
		out.Target, out.Route = &s.Targets[0].Process, s.Targets[0].Route
	}
	return json.Marshal(out)
}

func (s *startNode) UnmarshalJSON(b []byte) error {
	var in startJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	s.X, s.Y, s.Targets = in.X, in.Y, in.Targets
	if in.Targets == nil && in.Target != nil {
		s.Targets = []startTarget{{Process: *in.Target, Route: in.Route}}
	}
	return nil
}

// The processes the start points at.
func (s startNode) processes() []int64 {
	ps := make([]int64, len(s.Targets))
	for i, t := range s.Targets {
		ps[i] = t.Process
	}
	return ps
}

// A connection is a directed edge between two processes: the target runs
// right after the source, or for a failure connection, OPM's exception link,
// instead of it once it fails, which handles the failure; see engine.go.
type connection struct {
	ID     int64  `json:"id"`
	Source int64  `json:"source"`
	Target int64  `json:"target"`
	Kind   string `json:"kind"`
}

const (
	connectionNext    = ""
	connectionFailure = "failure"
)

const (
	mysqlErrDuplicate  = 1062
	mysqlErrForeignKey = 1452
	mysqlErrDeadlock   = 1213
)

func registerFlowRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/connections", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		rows, err := db.QueryContext(r.Context(),
			"SELECT c.id, c.source_id, c.target_id, c.kind FROM connections c JOIN processes p ON p.id = c.source_id WHERE p.flow_id = ? ORDER BY c.id", f)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		cs := []connection{}
		for rows.Next() {
			var c connection
			if err := rows.Scan(&c.ID, &c.Source, &c.Target, &c.Kind); err != nil {
				serverError(w, err)
				return
			}
			cs = append(cs, c)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cs)
	})

	mux.HandleFunc("POST /api/connections", func(w http.ResponseWriter, r *http.Request) {
		var c connection
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if c.Source == c.Target {
			http.Error(w, "a process can't connect to itself", http.StatusBadRequest)
			return
		}
		if c.Kind != connectionNext && c.Kind != connectionFailure {
			http.Error(w, "kind must be empty or failure", http.StatusBadRequest)
			return
		}
		if c.Kind == connectionFailure {
			var kind string
			err := db.QueryRowContext(r.Context(), "SELECT kind FROM processes WHERE id = ?", c.Source).Scan(&kind)
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "process not found", http.StatusBadRequest)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
			if !failingKind(kind) && kind != kindHuman {
				http.Error(w, failureSourceMessage, http.StatusBadRequest)
				return
			}
		}
		var flows int
		err := db.QueryRowContext(r.Context(), "SELECT COUNT(DISTINCT flow_id) FROM processes WHERE id IN (?, ?)", c.Source, c.Target).Scan(&flows)
		if err != nil {
			serverError(w, err)
			return
		}
		if flows > 1 {
			http.Error(w, "the processes are in different flows", http.StatusBadRequest)
			return
		}

		res, err := db.ExecContext(r.Context(), "INSERT INTO connections (source_id, target_id, kind) VALUES (?, ?, ?)", c.Source, c.Target, c.Kind)
		switch {
		case mysqlErrorIs(err, mysqlErrDuplicate):
			http.Error(w, "connection already exists", http.StatusConflict)
			return
		case mysqlErrorIs(err, mysqlErrForeignKey):
			http.Error(w, "process not found", http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, c)
	})

	mux.HandleFunc("DELETE /api/connections/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM connections WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

const failureSourceMessage = "only an agent, script or connector process fails, or a human one waiting past its time limit, so only one of those leads to what handles its failure"

func mysqlErrorIs(err error, number uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == number
}
