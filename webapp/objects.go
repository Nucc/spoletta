package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// An object is an OPM object: a named thing that processes generate and take
// as input. Its attributes say what an agent generating it must reply with.
type object struct {
	ID   int64   `json:"id"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	// Set when resized; null draws the default width.
	Width *float64 `json:"width"`
	// Runs store what they generate it with; see instances.go.
	Persistent bool          `json:"persistent"`
	States     []objectState `json:"states"`
	Attributes []attribute   `json:"attributes"`
}

// A link ties an object to a process: the process generates it (output) or
// takes it as input. A link naming one of the object's states generates the
// object in that state, or takes it once it enters that state. A human
// process's output link names no state; it lists the states the human
// decides between instead.
type link struct {
	ID        int64   `json:"id"`
	Process   int64   `json:"process"`
	Object    int64   `json:"object"`
	Direction string  `json:"direction"`
	State     *int64  `json:"state"`
	States    []int64 `json:"states"`
	// Where the link's drawn segments run, if moved by hand; see route.ts.
	Route json.RawMessage `json:"route"`
}

const (
	directionOutput = "output"
	directionInput  = "input"
)

// A link's state_id is 0 when it names no state; a NULL would let the unique
// key take the same link twice.
func migrateObjects(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS objects (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		x DOUBLE NOT NULL DEFAULT 0,
		y DOUBLE NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return err
	}
	if err := addColumn(db, "objects", "width", "DOUBLE NULL AFTER y"); err != nil {
		return err
	}
	if err := migrateStates(db); err != nil {
		return err
	}
	if err := migrateAttributes(db); err != nil {
		return err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS object_links (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		process_id BIGINT NOT NULL,
		object_id BIGINT NOT NULL,
		direction VARCHAR(8) NOT NULL,
		state_id BIGINT NOT NULL DEFAULT 0,
		UNIQUE KEY process_object_direction_state (process_id, object_id, direction, state_id),
		FOREIGN KEY (process_id) REFERENCES processes(id) ON DELETE CASCADE,
		FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE CASCADE
	)`)
	if err != nil {
		return err
	}

	// Links from before states: the new unique key goes in before the old one
	// goes, since the process foreign key needs an index starting with it.
	if err := addColumn(db, "object_links", "state_id", "BIGINT NOT NULL DEFAULT 0 AFTER direction"); err != nil {
		return err
	}
	if ok, err := hasIndex(db, "object_links", "process_object_direction_state"); err != nil {
		return err
	} else if !ok {
		_, err := db.Exec("ALTER TABLE object_links ADD UNIQUE KEY process_object_direction_state (process_id, object_id, direction, state_id)")
		if err != nil {
			return err
		}
	}
	if ok, err := hasIndex(db, "object_links", "process_object_direction"); err != nil {
		return err
	} else if ok {
		if _, err := db.Exec("ALTER TABLE object_links DROP INDEX process_object_direction"); err != nil {
			return err
		}
	}

	if err := addColumn(db, "object_links", "route", "JSON NULL"); err != nil {
		return err
	}

	// The states a human's output link decides between.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS link_states (
		link_id BIGINT NOT NULL,
		state_id BIGINT NOT NULL,
		PRIMARY KEY (link_id, state_id),
		FOREIGN KEY (link_id) REFERENCES object_links(id) ON DELETE CASCADE,
		FOREIGN KEY (state_id) REFERENCES object_states(id) ON DELETE CASCADE
	)`)
	return err
}

func getObject(ctx context.Context, db *sql.DB, id int64) (object, error) {
	o := object{States: []objectState{}, Attributes: []attribute{}}
	err := db.QueryRowContext(ctx, "SELECT id, name, x, y, width, persistent FROM objects WHERE id = ?", id).Scan(&o.ID, &o.Name, &o.X, &o.Y, &o.Width, &o.Persistent)
	if err != nil {
		return o, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id, object_id, name FROM object_states WHERE object_id = ? ORDER BY id", id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var s objectState
		if err := rows.Scan(&s.ID, &s.Object, &s.Name); err != nil {
			return o, err
		}
		o.States = append(o.States, s)
	}
	if err := rows.Err(); err != nil {
		return o, err
	}
	attrs, err := listAttributes(ctx, db, id)
	if err != nil {
		return o, err
	}
	if attrs[id] != nil {
		o.Attributes = attrs[id]
	}
	return o, nil
}

func registerObjectRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/objects", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, name, x, y, width, persistent FROM objects ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		objs := []object{}
		for rows.Next() {
			var o object
			if err := rows.Scan(&o.ID, &o.Name, &o.X, &o.Y, &o.Width, &o.Persistent); err != nil {
				serverError(w, err)
				return
			}
			objs = append(objs, o)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		states, err := listStates(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		attrs, err := listAttributes(r.Context(), db, 0)
		if err != nil {
			serverError(w, err)
			return
		}
		for i := range objs {
			if objs[i].States = states[objs[i].ID]; objs[i].States == nil {
				objs[i].States = []objectState{}
			}
			if objs[i].Attributes = attrs[objs[i].ID]; objs[i].Attributes == nil {
				objs[i].Attributes = []attribute{}
			}
		}
		writeJSON(w, http.StatusOK, objs)
	})

	mux.HandleFunc("POST /api/objects", func(w http.ResponseWriter, r *http.Request) {
		var o object
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		o.Name = strings.TrimSpace(o.Name)
		if o.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		if !validWidth(o.Width) {
			http.Error(w, invalidWidthMessage, http.StatusBadRequest)
			return
		}
		res, err := db.ExecContext(r.Context(), "INSERT INTO objects (name, x, y, width) VALUES (?, ?, ?, ?)", o.Name, o.X, o.Y, o.Width)
		if err != nil {
			serverError(w, err)
			return
		}
		o.States = []objectState{}
		o.Attributes = []attribute{}
		if o.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, o)
	})

	mux.HandleFunc("PATCH /api/objects/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}

		var patch struct {
			Name  *string  `json:"name"`
			X     *float64 `json:"x"`
			Width *float64 `json:"width"`
			Y     *float64 `json:"y"`
			// Turning it off keeps what was stored.
			Persistent *bool `json:"persistent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if !validWidth(patch.Width) {
			http.Error(w, invalidWidthMessage, http.StatusBadRequest)
			return
		}
		if patch.Name != nil {
			if *patch.Name = strings.TrimSpace(*patch.Name); *patch.Name == "" {
				http.Error(w, "name must not be empty", http.StatusBadRequest)
				return
			}
		}

		_, err = db.ExecContext(r.Context(),
			"UPDATE objects SET name = COALESCE(?, name), x = COALESCE(?, x), y = COALESCE(?, y), width = COALESCE(?, width), persistent = COALESCE(?, persistent) WHERE id = ?",
			patch.Name, patch.X, patch.Y, patch.Width, patch.Persistent, id)
		if err != nil {
			serverError(w, err)
			return
		}

		o, err := getObject(r.Context(), db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "object not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, o)
	})

	// Its links go with it; runs keep what it held, and stored instances stay.
	mux.HandleFunc("DELETE /api/objects/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM objects WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/links", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.QueryContext(r.Context(), "SELECT id, process_id, object_id, direction, state_id, route FROM object_links ORDER BY id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		ls := []link{}
		for rows.Next() {
			l := link{States: []int64{}}
			var state int64
			var route []byte
			if err := rows.Scan(&l.ID, &l.Process, &l.Object, &l.Direction, &state, &route); err != nil {
				serverError(w, err)
				return
			}
			if state != 0 {
				l.State = &state
			}
			l.Route = routeJSON(route)
			ls = append(ls, l)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}

		srows, err := db.QueryContext(r.Context(), "SELECT link_id, state_id FROM link_states ORDER BY state_id")
		if err != nil {
			serverError(w, err)
			return
		}
		defer srows.Close()
		states := map[int64][]int64{}
		for srows.Next() {
			var linkID, stateID int64
			if err := srows.Scan(&linkID, &stateID); err != nil {
				serverError(w, err)
				return
			}
			states[linkID] = append(states[linkID], stateID)
		}
		if err := srows.Err(); err != nil {
			serverError(w, err)
			return
		}
		for i := range ls {
			if ss, ok := states[ls[i].ID]; ok {
				ls[i].States = ss
			}
		}
		writeJSON(w, http.StatusOK, ls)
	})

	mux.HandleFunc("POST /api/links", func(w http.ResponseWriter, r *http.Request) {
		var l link
		if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if l.Direction != directionOutput && l.Direction != directionInput {
			http.Error(w, "direction must be output or input", http.StatusBadRequest)
			return
		}
		if msg, err := checkLink(r.Context(), db, l); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		var state int64
		if l.State != nil {
			state = *l.State
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(r.Context(), "INSERT INTO object_links (process_id, object_id, direction, state_id) VALUES (?, ?, ?, ?)",
			l.Process, l.Object, l.Direction, state)
		switch {
		case mysqlErrorIs(err, mysqlErrDuplicate):
			http.Error(w, "link already exists", http.StatusConflict)
			return
		case mysqlErrorIs(err, mysqlErrForeignKey):
			http.Error(w, "process or object not found", http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		if l.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		if l.States == nil {
			l.States = []int64{}
		}
		l.Route = routeJSON(nil)
		if msg, err := setDecisionStates(r.Context(), tx, l.ID, l.Object, l.States); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, l)
	})

	// Sets where the link's segments run, or with null goes back to the default route.
	mux.HandleFunc("PATCH /api/links/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		route, ok := decodeRoute(w, r)
		if !ok {
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_links SET route = ? WHERE id = ?", route, id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Replaces the states a human's output link decides between.
	mux.HandleFunc("PUT /api/links/{id}/states", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			States []int64 `json:"states"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		var object int64
		err = db.QueryRowContext(r.Context(),
			`SELECT l.object_id FROM object_links l JOIN processes p ON p.id = l.process_id
			WHERE l.id = ? AND l.direction = 'output' AND p.kind = 'human'`, id).Scan(&object)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no human output link with that id", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM link_states WHERE link_id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		if msg, err := setDecisionStates(r.Context(), tx, id, object, body.States); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("DELETE /api/links/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM object_links WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Adds states a link decides between; they must be the object's. Says what
// is wrong, if anything.
func setDecisionStates(ctx context.Context, tx *sql.Tx, linkID, object int64, states []int64) (string, error) {
	for _, s := range states {
		var owner int64
		err := tx.QueryRowContext(ctx, "SELECT object_id FROM object_states WHERE id = ?", s).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != object) {
			return "state not found on that object", nil
		}
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO link_states (link_id, state_id) VALUES (?, ?)", linkID, s); err != nil {
			return "", err
		}
	}
	return "", nil
}

// Says what is wrong with a new link, if anything. A named state must be one
// of the object's. A human process decides by setting its object to one of
// the states its output link lists, and only it lists states; any other
// process generates an object once, so it has at most one output link to it.
func checkLink(ctx context.Context, db *sql.DB, l link) (string, error) {
	if l.State != nil {
		var object int64
		err := db.QueryRowContext(ctx, "SELECT object_id FROM object_states WHERE id = ?", *l.State).Scan(&object)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && object != l.Object) {
			return "state not found on that object", nil
		}
		if err != nil {
			return "", err
		}
	}
	if l.Direction != directionOutput {
		if len(l.States) > 0 {
			return "only a human's output link lists states", nil
		}
		return "", nil
	}

	var kind string
	err := db.QueryRowContext(ctx, "SELECT kind FROM processes WHERE id = ?", l.Process).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "process not found", nil
	}
	if err != nil {
		return "", err
	}
	if kind == kindHuman {
		if l.State != nil {
			return "a human process links to the object and lists the states it decides between", nil
		}
		return "", nil
	}
	if len(l.States) > 0 {
		return "only a human's output link lists states", nil
	}
	var n int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_links WHERE process_id = ? AND object_id = ? AND direction = 'output'",
		l.Process, l.Object).Scan(&n)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "the process already generates that object", nil
	}
	return "", nil
}
