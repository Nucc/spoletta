package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
// the same names, and the processes a start points at in start_targets, each
// on the board whose start that is. A top board's end has no place until it
// is moved: the board places it below what is on it.

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

type rowQuerier interface {
	querier
	rowsQuerier
}

func getStart(ctx context.Context, db rowQuerier, flow int64, board *int64) (startNode, error) {
	var s startNode
	table, cond, args := startRow(flow, board)
	if err := db.QueryRowContext(ctx, "SELECT x, y FROM "+table+" "+cond, args...).Scan(&s.X, &s.Y); err != nil {
		return s, err
	}
	targets, err := startTargets(ctx, db, flow)
	s.Targets = targets[boardKey(board)]
	return s, err
}

// What the starts of the flow's boards point at, by board: 0 for the top
// board, else the flow process's id.
func startTargets(ctx context.Context, db rowsQuerier, flow int64) (map[int64][]startTarget, error) {
	targets := map[int64][]startTarget{}
	err := scanAll(ctx, db, `SELECT p.id, COALESCE(p.parent_id, 0), t.route FROM start_targets t JOIN processes p ON p.id = t.process_id
		WHERE p.flow_id = ? ORDER BY p.id`, func(rows *sql.Rows) error {
		var t startTarget
		var board int64
		var route []byte
		if err := rows.Scan(&t.Process, &board, &route); err != nil {
			return err
		}
		t.Route = routeJSON(route)
		targets[board] = append(targets[board], t)
		return nil
	}, flow)
	return targets, err
}

// A board as startTargets keys it.
func boardKey(board *int64) int64 {
	if board == nil {
		return 0
	}
	return *board
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
		targets, err := startTargets(r.Context(), db, f)
		if err != nil {
			serverError(w, err)
			return
		}
		bs := []innerBoard{}
		err = scanAll(r.Context(), db, `SELECT b.process_id, b.x, b.y, b.end_x, b.end_y
			FROM inner_boards b JOIN processes p ON p.id = b.process_id WHERE p.flow_id = ? ORDER BY b.process_id`, func(rows *sql.Rows) error {
			var b innerBoard
			if err := rows.Scan(&b.Process, &b.Start.X, &b.Start.Y, &b.End.X, &b.End.Y); err != nil {
				return err
			}
			b.Start.Targets = targets[b.Process]
			bs = append(bs, b)
			return nil
		}, f)
		if err != nil {
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
		// target points the start at that process alone, or with null at
		// none; route is where the link to its one process runs. Pointing
		// it at several is a process's own, PUT /api/processes/{id}/start.
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
			err := inTx(r.Context(), db, func(tx *sql.Tx) error {
				if _, err := tx.ExecContext(r.Context(), `DELETE t FROM start_targets t JOIN processes p ON p.id = t.process_id
					WHERE p.flow_id = ? AND p.parent_id <=> ?`, f, board); err != nil {
					return err
				}
				if target == nil {
					return nil
				}
				_, err := tx.ExecContext(r.Context(), "INSERT INTO start_targets (process_id) VALUES (?)", *target)
				return err
			})
			if err != nil {
				serverError(w, err)
				return
			}
		}
		if len(patch.Route) > 0 {
			stored, ok := routeOf(patch.Route)
			if !ok {
				http.Error(w, "invalid route", http.StatusBadRequest)
				return
			}
			_, err := db.ExecContext(r.Context(), `UPDATE start_targets t JOIN processes p ON p.id = t.process_id SET t.route = ?
				WHERE p.flow_id = ? AND p.parent_id <=> ?`, stored, f, board)
			if err != nil {
				serverError(w, err)
				return
			}
		}
		writeStart(w, r, f, board)
	})

	// Points the start of the process's board at it too, so a run begins
	// with it beside the others it points at, or with a route, moves the
	// link to it; DELETE points the start away from it.
	setStartTarget := func(w http.ResponseWriter, r *http.Request, connect bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Route json.RawMessage `json:"route"`
		}
		if connect && r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		stored, ok := routeOf(body.Route)
		if !ok {
			http.Error(w, "invalid route", http.StatusBadRequest)
			return
		}
		var flow int64
		var board *int64
		err = db.QueryRowContext(r.Context(), "SELECT flow_id, parent_id FROM processes WHERE id = ?", id).Scan(&flow, &board)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "process not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if connect {
			_, err = db.ExecContext(r.Context(), "INSERT INTO start_targets (process_id, route) VALUES (?, ?) AS new ON DUPLICATE KEY UPDATE route = new.route", id, stored)
		} else {
			_, err = db.ExecContext(r.Context(), "DELETE FROM start_targets WHERE process_id = ?", id)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeStart(w, r, flow, board)
	}
	mux.HandleFunc("PUT /api/processes/{id}/start", func(w http.ResponseWriter, r *http.Request) { setStartTarget(w, r, true) })
	mux.HandleFunc("DELETE /api/processes/{id}/start", func(w http.ResponseWriter, r *http.Request) { setStartTarget(w, r, false) })

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

	// The start of the flow process's board points at those of them the
	// start pointed at, as moving them there made it, on new routes; the
	// start they leave points at the flow process instead.
	var started int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM start_targets WHERE process_id IN "+placeholders(len(processes)), procArgs...).Scan(&started); err != nil || started == 0 {
		return p, "", err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE start_targets SET route = NULL WHERE process_id IN "+placeholders(len(processes)), procArgs...); err != nil {
		return p, "", err
	}
	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO start_targets (process_id) VALUES (?)", p.ID); err != nil {
		return p, "", err
	}
	return p, "", nil
}
