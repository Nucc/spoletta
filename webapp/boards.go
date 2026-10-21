package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// A flow process is made of processes on a board of its own, as OPM zooms
// into a process. The top board is the flow itself; every other board
// belongs to a flow process, and its processes and objects name that process
// as their parent.
//
// The objects linked to a flow process are its interface: zoomed in, the
// ones it takes show at its board's start and the ones it generates at its
// end. They are the same objects, so the processes inside link to them
// directly; nothing is declared twice.
//
// Each board has a start and an end. A flow's top board's start and end live
// in flow_starts, the others' in inner_boards, which has their columns under
// the same names. A top board's end has no place until it is moved: the
// board places it below what is on it.

// Where a board ends.
type boardEnd struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// A flow process's board as versions keep it.
type innerBoard struct {
	Process int64     `json:"process"`
	Start   startNode `json:"start"`
	End     boardEnd  `json:"end"`
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Gives a flow process its board, unless it has one.
func ensureBoard(ctx context.Context, db execer, process int64) error {
	_, err := db.ExecContext(ctx, "INSERT IGNORE INTO inner_boards (process_id, x, y, end_x, end_y) VALUES (?, 40, 40, 40, 480)", process)
	return err
}

// What's wrong with putting something on the board of parent, if anything:
// only flow processes have boards.
func checkParent(ctx context.Context, db *sql.DB, parent *int64) (string, error) {
	if parent == nil {
		return "", nil
	}
	var kind string
	err := db.QueryRowContext(ctx, "SELECT kind FROM processes WHERE id = ?", *parent).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && kind != kindFlow) {
		return "parent must be a flow process", nil
	}
	return "", err
}

// Whether a process can link to an object: one on its board, or one its
// board's flow process is linked to. An object making up another is on that
// object's board instead.
func onBoardOf(ctx context.Context, db *sql.DB, process, object int64) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM processes p JOIN objects o ON o.id = ? AND o.flow_id = p.flow_id
		WHERE p.id = ? AND o.`+onProcessBoard+` AND (o.parent_id <=> p.parent_id
			OR EXISTS (SELECT 1 FROM object_links l WHERE l.process_id = p.parent_id AND l.object_id = o.id))`,
		object, process).Scan(&n)
	return n > 0, err
}

// The board a request is about, from ?board=<flow process id>, and the flow
// it is in; for a flow's top board, from ?flow=<id> with a nil board. Writes
// the error and returns false if it is invalid.
func boardParam(w http.ResponseWriter, r *http.Request, db *sql.DB) (int64, *int64, bool) {
	v := r.URL.Query().Get("board")
	if v == "" {
		f, ok := flowParam(w, r)
		return f, nil, ok
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		http.Error(w, "invalid board", http.StatusBadRequest)
		return 0, nil, false
	}
	f, err := flowOf(r.Context(), db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "board not found", http.StatusNotFound)
		return 0, nil, false
	}
	if err != nil {
		serverError(w, err)
		return 0, nil, false
	}
	return f, &id, true
}

// The table and condition of the row holding a board's start and end.
func startRow(flow int64, board *int64) (string, string, []any) {
	if board == nil {
		return "flow_starts", "WHERE flow_id = ?", []any{flow}
	}
	return "inner_boards", "WHERE process_id = ?", []any{*board}
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getStart(ctx context.Context, db querier, flow int64, board *int64) (startNode, error) {
	var s startNode
	var route []byte
	table, cond, args := startRow(flow, board)
	err := db.QueryRowContext(ctx, "SELECT x, y, target_id, route FROM "+table+" "+cond, args...).Scan(&s.X, &s.Y, &s.Target, &route)
	s.Route = routeJSON(route)
	return s, err
}

func registerBoardRoutes(mux *http.ServeMux, db *sql.DB) {
	writeStart := func(w http.ResponseWriter, r *http.Request, flow int64, board *int64) {
		s, err := getStart(r.Context(), db, flow, board)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "board not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}

	// Every board of a flow's flow processes: where it starts and ends.
	mux.HandleFunc("GET /api/boards", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		rows, err := db.QueryContext(r.Context(),
			`SELECT b.process_id, b.x, b.y, b.target_id, b.route, b.end_x, b.end_y
			FROM inner_boards b JOIN processes p ON p.id = b.process_id WHERE p.flow_id = ? ORDER BY b.process_id`, f)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		bs := []innerBoard{}
		for rows.Next() {
			var b innerBoard
			var route []byte
			if err := rows.Scan(&b.Process, &b.Start.X, &b.Start.Y, &b.Start.Target, &route, &b.End.X, &b.End.Y); err != nil {
				serverError(w, err)
				return
			}
			b.Start.Route = routeJSON(route)
			bs = append(bs, b)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, bs)
	})

	mux.HandleFunc("GET /api/start", func(w http.ResponseWriter, r *http.Request) {
		if f, board, ok := boardParam(w, r, db); ok {
			writeStart(w, r, f, board)
		}
	})

	mux.HandleFunc("PATCH /api/start", func(w http.ResponseWriter, r *http.Request) {
		f, board, ok := boardParam(w, r, db)
		if !ok {
			return
		}
		var patch struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
			// Raw so that an explicit null (disconnect, default route) is distinguishable from an absent field.
			Target json.RawMessage `json:"target"`
			Route  json.RawMessage `json:"route"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		table, cond, args := startRow(f, board)

		_, err := db.ExecContext(r.Context(), "UPDATE "+table+" SET x = COALESCE(?, x), y = COALESCE(?, y) "+cond,
			append([]any{patch.X, patch.Y}, args...)...)
		if err != nil {
			serverError(w, err)
			return
		}

		if len(patch.Target) > 0 {
			var target *int64
			if err := json.Unmarshal(patch.Target, &target); err != nil {
				http.Error(w, "invalid target: "+err.Error(), http.StatusBadRequest)
				return
			}
			if target != nil {
				var n int
				err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM processes WHERE id = ? AND parent_id <=> ? AND flow_id = ?", *target, board, f).Scan(&n)
				if err != nil {
					serverError(w, err)
					return
				}
				if n == 0 {
					http.Error(w, "target process not found on that board", http.StatusBadRequest)
					return
				}
			}
			// A new target needs a new route.
			_, err := db.ExecContext(r.Context(), "UPDATE "+table+" SET target_id = ?, route = NULL "+cond,
				append([]any{target}, args...)...)
			if err != nil {
				serverError(w, err)
				return
			}
		}
		if len(patch.Route) > 0 {
			var lanes route
			if err := json.Unmarshal(patch.Route, &lanes); err != nil || len(lanes) > maxRouteLanes {
				http.Error(w, "invalid route", http.StatusBadRequest)
				return
			}
			var stored []byte
			if lanes != nil {
				stored = patch.Route
			}
			_, err := db.ExecContext(r.Context(), "UPDATE "+table+" SET route = ? "+cond,
				append([]any{stored}, args...)...)
			if err != nil {
				serverError(w, err)
				return
			}
		}
		writeStart(w, r, f, board)
	})

	// A top board's end is null until it is moved.
	writeEnd := func(w http.ResponseWriter, r *http.Request, flow int64, board *int64) {
		var x, y *float64
		table, cond, args := startRow(flow, board)
		err := db.QueryRowContext(r.Context(), "SELECT end_x, end_y FROM "+table+" "+cond, args...).Scan(&x, &y)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "board not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if x == nil || y == nil {
			writeJSON(w, http.StatusOK, nil)
			return
		}
		writeJSON(w, http.StatusOK, boardEnd{X: *x, Y: *y})
	}

	mux.HandleFunc("GET /api/end", func(w http.ResponseWriter, r *http.Request) {
		if f, board, ok := boardParam(w, r, db); ok {
			writeEnd(w, r, f, board)
		}
	})

	mux.HandleFunc("PATCH /api/end", func(w http.ResponseWriter, r *http.Request) {
		f, board, ok := boardParam(w, r, db)
		if !ok {
			return
		}
		var patch struct {
			X *float64 `json:"x"`
			Y *float64 `json:"y"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		table, cond, args := startRow(f, board)
		_, err := db.ExecContext(r.Context(), "UPDATE "+table+" SET end_x = COALESCE(?, end_x), end_y = COALESCE(?, end_y) "+cond,
			append([]any{patch.X, patch.Y}, args...)...)
		if err != nil {
			serverError(w, err)
			return
		}
		writeEnd(w, r, f, board)
	})

	// Moves processes, and the objects only they link to, into a new flow
	// process in their place.
	mux.HandleFunc("POST /api/processes/wrap", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name      string  `json:"name"`
			Processes []int64 `json:"processes"`
			Objects   []int64 `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Name = strings.TrimSpace(body.Name); body.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if len(body.Processes) == 0 {
			http.Error(w, "select at least one process", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		p, msg, err := wrap(ctx, tx, body.Name, body.Processes, body.Objects)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})
}

// Where the new flow process's board starts and ends, around what moves in.
const (
	wrapMargin = 120
	// Roughly a process's height, so the end sits below the lowest one.
	wrapNodeHeight = 64
)

// "(?, ?, ...)" for n ids. For none, a list nothing is in, so "IN" holds of
// nothing and "NOT IN" of everything, where "()" would be an SQL error.
func placeholders(n int) string {
	if n == 0 {
		return "(SELECT NULL FROM DUAL WHERE FALSE)"
	}
	return "(?" + strings.Repeat(", ?", n-1) + ")"
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// The processes must share a board; the new flow process takes their place
// on it. Selected objects move in too, unless a process staying outside
// links to them: those stay, as the flow process's interface. Links from the
// processes moving in to objects staying outside become the flow process's
// own links, and direct connections across the boundary go. A start pointing
// at a process moving in points at the flow process, whose own start points
// at that process.
func wrap(ctx context.Context, tx *sql.Tx, name string, processes, objects []int64) (process, string, error) {
	var p process
	inSet := map[int64]bool{}
	for _, id := range processes {
		inSet[id] = true
	}

	// They must exist and share a board.
	var boards, found int
	var minX, minY, maxX, maxY = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	rows, err := tx.QueryContext(ctx, "SELECT flow_id, parent_id, x, y FROM processes WHERE id IN "+placeholders(len(processes)), int64Args(processes)...)
	if err != nil {
		return p, "", err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var parent *int64
		var x, y float64
		if err := rows.Scan(&p.Flow, &parent, &x, &y); err != nil {
			rows.Close()
			return p, "", err
		}
		key := "flow " + strconv.FormatInt(p.Flow, 10)
		if parent != nil {
			key = strconv.FormatInt(*parent, 10)
			p.Parent = parent
		}
		if !seen[key] {
			seen[key] = true
			boards++
		}
		found++
		minX, minY, maxX, maxY = math.Min(minX, x), math.Min(minY, y), math.Max(maxX, x), math.Max(maxY, y)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return p, "", err
	}
	if found != len(inSet) {
		return p, "process not found", nil
	}
	if boards != 1 {
		return p, "the processes must be on the same board", nil
	}

	// Objects move in only if nothing staying outside links to them, and
	// only from the same board.
	var moving []int64
	if len(objects) > 0 {
		args := append(int64Args(objects), p.Parent, p.Flow)
		args = append(args, int64Args(processes)...)
		rows, err := tx.QueryContext(ctx,
			`SELECT o.id FROM objects o WHERE o.id IN `+placeholders(len(objects))+` AND o.parent_id <=> ? AND o.flow_id = ? AND o.`+onProcessBoard+`
			AND NOT EXISTS (SELECT 1 FROM object_links l WHERE l.object_id = o.id AND l.process_id NOT IN `+placeholders(len(processes))+`)`,
			args...)
		if err != nil {
			return p, "", err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return p, "", err
			}
			moving = append(moving, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return p, "", err
		}
	}

	p.Name, p.Kind = name, kindFlow
	p.X, p.Y = (minX+maxX)/2, (minY+maxY)/2
	res, err := tx.ExecContext(ctx, "INSERT INTO processes (flow_id, name, kind, prompt, script, x, y, parent_id) VALUES (?, ?, ?, '', '', ?, ?, ?)",
		p.Flow, p.Name, p.Kind, p.X, p.Y, p.Parent)
	if err != nil {
		return p, "", err
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return p, "", err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO inner_boards (process_id, x, y, end_x, end_y) VALUES (?, ?, ?, ?, ?)",
		p.ID, minX, minY-wrapMargin, minX, maxY+wrapNodeHeight+wrapMargin)
	if err != nil {
		return p, "", err
	}

	procArgs := int64Args(processes)
	if _, err := tx.ExecContext(ctx, "UPDATE processes SET parent_id = ? WHERE id IN "+placeholders(len(processes)), append([]any{p.ID}, procArgs...)...); err != nil {
		return p, "", err
	}
	if len(moving) > 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE objects SET parent_id = ? WHERE id IN "+placeholders(len(moving)), append([]any{p.ID}, int64Args(moving)...)...); err != nil {
			return p, "", err
		}
	}

	// The flow process takes and generates each such object once, in no
	// state: the states processes inside take it in are theirs to trigger.
	args := append([]any{p.ID}, procArgs...)
	_, err = tx.ExecContext(ctx,
		`INSERT IGNORE INTO object_links (process_id, object_id, direction, state_id)
		SELECT DISTINCT ?, l.object_id, l.direction, 0
		FROM object_links l JOIN objects o ON o.id = l.object_id
		WHERE l.process_id IN `+placeholders(len(processes))+` AND NOT (o.parent_id <=> `+strconv.FormatInt(p.ID, 10)+`)`,
		args...)
	if err != nil {
		return p, "", err
	}

	_, err = tx.ExecContext(ctx,
		"DELETE FROM connections WHERE (source_id IN "+placeholders(len(processes))+") <> (target_id IN "+placeholders(len(processes))+")",
		append(procArgs, procArgs...)...)
	if err != nil {
		return p, "", err
	}

	start, err := getStart(ctx, tx, p.Flow, p.Parent)
	if err != nil {
		return p, "", err
	}
	if start.Target != nil && inSet[*start.Target] {
		table, cond, wargs := startRow(p.Flow, p.Parent)
		if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET target_id = ?, route = NULL "+cond, append([]any{p.ID}, wargs...)...); err != nil {
			return p, "", err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE inner_boards SET target_id = ? WHERE process_id = ?", *start.Target, p.ID); err != nil {
			return p, "", err
		}
	}
	return p, "", nil
}
