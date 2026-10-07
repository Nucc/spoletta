package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A version is a saved snapshot of the whole board: its processes, objects
// with their states and attributes, links, connections and start node. Saving
// always adds a version on top, and jumping to one rewrites the board as it
// was, so no version is ever lost. The board keeps its rows' ids across
// jumps: runs refer to processes and objects by id, and persistent objects
// remember what runs stored by object and attribute id.
type version struct {
	ID int64 `json:"id"`
	// The version the board was on when this one was saved, if any; a save
	// after jumping back is based on an older version.
	BasedOn   *int64    `json:"basedOn"`
	CreatedAt time.Time `json:"createdAt"`
}

type snapshotObject struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Width      *float64 `json:"width"`
	Persistent bool     `json:"persistent"`
}

type snapshotLink struct {
	ID        int64  `json:"id"`
	Process   int64  `json:"process"`
	Object    int64  `json:"object"`
	Direction string `json:"direction"`
	// 0 when the link names no state, as stored.
	State int64           `json:"state"`
	Route json.RawMessage `json:"route"`
}

type snapshotLinkState struct {
	Link  int64 `json:"link"`
	State int64 `json:"state"`
}

type snapshot struct {
	Processes   []process           `json:"processes"`
	Objects     []snapshotObject    `json:"objects"`
	States      []objectState       `json:"states"`
	Attributes  []attribute         `json:"attributes"`
	Links       []snapshotLink      `json:"links"`
	LinkStates  []snapshotLinkState `json:"linkStates"`
	Connections []connection        `json:"connections"`
	Start       startNode           `json:"start"`
}

func migrateVersions(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS flow_versions (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			based_on BIGINT NULL,
			snapshot JSON NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
		)`,
		// The version the board was last saved as or jumped to.
		`CREATE TABLE IF NOT EXISTS flow_current (
			id TINYINT PRIMARY KEY,
			version_id BIGINT NULL,
			FOREIGN KEY (version_id) REFERENCES flow_versions(id)
		)`,
		"INSERT IGNORE INTO flow_current (id) VALUES (1)",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Scans every row of query with scan, which reads the current row.
func scanAll(ctx context.Context, tx *sql.Tx, query string, scan func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func takeSnapshot(ctx context.Context, tx *sql.Tx) (snapshot, error) {
	s := snapshot{
		Processes:   []process{},
		Objects:     []snapshotObject{},
		States:      []objectState{},
		Attributes:  []attribute{},
		Links:       []snapshotLink{},
		LinkStates:  []snapshotLinkState{},
		Connections: []connection{},
	}
	var startRoute []byte
	err := tx.QueryRowContext(ctx, "SELECT x, y, target_id, route FROM start_node WHERE id = 1").Scan(&s.Start.X, &s.Start.Y, &s.Start.Target, &startRoute)
	if err != nil {
		return s, err
	}
	s.Start.Route = routeJSON(startRoute)

	err = scanAll(ctx, tx, "SELECT id, name, kind, prompt, script, model, thinking, x, y, width FROM processes ORDER BY id", func(rows *sql.Rows) error {
		var p process
		err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Prompt, &p.Script, &p.Model, &p.Thinking, &p.X, &p.Y, &p.Width)
		s.Processes = append(s.Processes, p)
		return err
	})
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, "SELECT id, name, x, y, width, persistent FROM objects ORDER BY id", func(rows *sql.Rows) error {
		var o snapshotObject
		err := rows.Scan(&o.ID, &o.Name, &o.X, &o.Y, &o.Width, &o.Persistent)
		s.Objects = append(s.Objects, o)
		return err
	})
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, "SELECT id, object_id, name FROM object_states ORDER BY id", func(rows *sql.Rows) error {
		var st objectState
		err := rows.Scan(&st.ID, &st.Object, &st.Name)
		s.States = append(s.States, st)
		return err
	})
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, "SELECT id, object_id, name, definition, is_unique FROM object_attributes ORDER BY id", func(rows *sql.Rows) error {
		var a attribute
		err := rows.Scan(&a.ID, &a.Object, &a.Name, &a.Definition, &a.Unique)
		s.Attributes = append(s.Attributes, a)
		return err
	})
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, "SELECT id, process_id, object_id, direction, state_id, route FROM object_links ORDER BY id", func(rows *sql.Rows) error {
		var l snapshotLink
		var route []byte
		err := rows.Scan(&l.ID, &l.Process, &l.Object, &l.Direction, &l.State, &route)
		l.Route = routeJSON(route)
		s.Links = append(s.Links, l)
		return err
	})
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, "SELECT link_id, state_id FROM link_states ORDER BY link_id, state_id", func(rows *sql.Rows) error {
		var ls snapshotLinkState
		err := rows.Scan(&ls.Link, &ls.State)
		s.LinkStates = append(s.LinkStates, ls)
		return err
	})
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, "SELECT id, source_id, target_id FROM connections ORDER BY id", func(rows *sql.Rows) error {
		var c connection
		err := rows.Scan(&c.ID, &c.Source, &c.Target)
		s.Connections = append(s.Connections, c)
		return err
	})
	return s, err
}

// A route as stored: NULL for the default route.
func storedRoute(r json.RawMessage) any {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	return []byte(r)
}

// Deletes the rows of table whose id isn't among keep.
func deleteOthers(ctx context.Context, tx *sql.Tx, table string, keep []int64) error {
	if len(keep) == 0 {
		_, err := tx.ExecContext(ctx, "DELETE FROM "+table)
		return err
	}
	args := make([]any, len(keep))
	for i, id := range keep {
		args[i] = id
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE id NOT IN (?"+strings.Repeat(", ?", len(keep)-1)+")", args...)
	return err
}

// Rewrites the board as s has it. Processes and objects are updated in place
// rather than recreated, so the runs referring to them keep doing so. Rows
// nothing outside the board refers to are recreated with their saved ids.
func restoreSnapshot(ctx context.Context, tx *sql.Tx, s snapshot) error {
	// Cleared first so their unique keys can't collide while being rewritten.
	for _, t := range []string{"link_states", "object_links", "connections", "object_attributes", "object_states"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return err
		}
	}

	keep := make([]int64, len(s.Processes))
	for i, p := range s.Processes {
		keep[i] = p.ID
	}
	if err := deleteOthers(ctx, tx, "processes", keep); err != nil {
		return err
	}
	keep = make([]int64, len(s.Objects))
	for i, o := range s.Objects {
		keep[i] = o.ID
	}
	if err := deleteOthers(ctx, tx, "objects", keep); err != nil {
		return err
	}

	for _, p := range s.Processes {
		_, err := tx.ExecContext(ctx, `INSERT INTO processes (id, name, kind, prompt, script, model, thinking, x, y, width)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) AS saved
			ON DUPLICATE KEY UPDATE name = saved.name, kind = saved.kind, prompt = saved.prompt, script = saved.script,
				model = saved.model, thinking = saved.thinking, x = saved.x, y = saved.y, width = saved.width`,
			p.ID, p.Name, p.Kind, p.Prompt, p.Script, p.Model, p.Thinking, p.X, p.Y, p.Width)
		if err != nil {
			return err
		}
	}
	for _, o := range s.Objects {
		_, err := tx.ExecContext(ctx, `INSERT INTO objects (id, name, x, y, width, persistent)
			VALUES (?, ?, ?, ?, ?, ?) AS saved
			ON DUPLICATE KEY UPDATE name = saved.name, x = saved.x, y = saved.y, width = saved.width, persistent = saved.persistent`,
			o.ID, o.Name, o.X, o.Y, o.Width, o.Persistent)
		if err != nil {
			return err
		}
	}
	for _, st := range s.States {
		if _, err := tx.ExecContext(ctx, "INSERT INTO object_states (id, object_id, name) VALUES (?, ?, ?)", st.ID, st.Object, st.Name); err != nil {
			return err
		}
	}
	for _, a := range s.Attributes {
		_, err := tx.ExecContext(ctx, "INSERT INTO object_attributes (id, object_id, name, definition, is_unique) VALUES (?, ?, ?, ?, ?)",
			a.ID, a.Object, a.Name, a.Definition, a.Unique)
		if err != nil {
			return err
		}
	}
	for _, l := range s.Links {
		_, err := tx.ExecContext(ctx, "INSERT INTO object_links (id, process_id, object_id, direction, state_id, route) VALUES (?, ?, ?, ?, ?, ?)",
			l.ID, l.Process, l.Object, l.Direction, l.State, storedRoute(l.Route))
		if err != nil {
			return err
		}
	}
	for _, ls := range s.LinkStates {
		if _, err := tx.ExecContext(ctx, "INSERT INTO link_states (link_id, state_id) VALUES (?, ?)", ls.Link, ls.State); err != nil {
			return err
		}
	}
	for _, c := range s.Connections {
		if _, err := tx.ExecContext(ctx, "INSERT INTO connections (id, source_id, target_id) VALUES (?, ?, ?)", c.ID, c.Source, c.Target); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE start_node SET x = ?, y = ?, target_id = ?, route = ? WHERE id = 1",
		s.Start.X, s.Start.Y, s.Start.Target, storedRoute(s.Start.Route))
	return err
}

func registerVersionRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/versions", func(w http.ResponseWriter, r *http.Request) {
		res := struct {
			// The version the board was last saved as or jumped to; null before the first save.
			Current  *int64    `json:"current"`
			Versions []version `json:"versions"`
		}{Versions: []version{}}
		if err := db.QueryRowContext(r.Context(), "SELECT version_id FROM flow_current WHERE id = 1").Scan(&res.Current); err != nil {
			serverError(w, err)
			return
		}
		rows, err := db.QueryContext(r.Context(), "SELECT id, based_on, created_at FROM flow_versions ORDER BY id DESC")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var v version
			if err := rows.Scan(&v.ID, &v.BasedOn, &v.CreatedAt); err != nil {
				serverError(w, err)
				return
			}
			res.Versions = append(res.Versions, v)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("POST /api/versions", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()

		var v version
		// Locked so that two saves can't both be based on the same version unnoticed.
		if err := tx.QueryRowContext(ctx, "SELECT version_id FROM flow_current WHERE id = 1 FOR UPDATE").Scan(&v.BasedOn); err != nil {
			serverError(w, err)
			return
		}
		s, err := takeSnapshot(ctx, tx)
		if err != nil {
			serverError(w, err)
			return
		}
		body, err := json.Marshal(s)
		if err != nil {
			serverError(w, err)
			return
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO flow_versions (based_on, snapshot) VALUES (?, ?)", v.BasedOn, body)
		if err != nil {
			serverError(w, err)
			return
		}
		if v.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		if _, err := tx.ExecContext(ctx, "UPDATE flow_current SET version_id = ? WHERE id = 1", v.ID); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.QueryRowContext(ctx, "SELECT created_at FROM flow_versions WHERE id = ?", v.ID).Scan(&v.CreatedAt); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	})

	mux.HandleFunc("POST /api/versions/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()

		// A run follows the board as it goes; rewriting it underneath would derail the run.
		var running int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE status = 'running'").Scan(&running); err != nil {
			serverError(w, err)
			return
		}
		if running > 0 {
			http.Error(w, "can't switch versions while the flow is running", http.StatusConflict)
			return
		}

		var body []byte
		err = tx.QueryRowContext(ctx, "SELECT snapshot FROM flow_versions WHERE id = ?", id).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "version not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		var s snapshot
		if err := json.Unmarshal(body, &s); err != nil {
			serverError(w, err)
			return
		}
		if err := restoreSnapshot(ctx, tx, s); err != nil {
			serverError(w, err)
			return
		}
		if _, err := tx.ExecContext(ctx, "UPDATE flow_current SET version_id = ? WHERE id = 1", id); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
