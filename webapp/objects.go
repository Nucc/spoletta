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
	ID int64 `json:"id"`
	// The flow it is in, on its top board or a flow process's.
	Flow int64   `json:"flow"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	// Set when resized; null draws the default width.
	Width *float64 `json:"width"`
	// Runs store what they generate it with; see instances.go.
	Persistent bool `json:"persistent"`
	// A list of items: an agent generating it replies with a list, and a
	// flow process taking it runs once per item; see items.go.
	Many bool `json:"many"`
	// How many items the list holds: at least min, and at most max unless
	// it is null; see items.go.
	Min int  `json:"min"`
	Max *int `json:"max"`
	// A person, such as a requester: in OPM a human object, whose input link
	// is an agent link, saying the person carries out the process. Only a
	// human process takes it.
	Human bool `json:"human"`
	// Leads into the end of the flow's top board, as one of what the flow
	// leaves: an object of its top board a person connected to the end.
	// Flows running it take these as its outcomes; see calls.go.
	End bool `json:"end"`
	// The flow process whose board it is on; null on the top board. It also
	// shows inside the flow processes linked to it; see boards.go.
	Parent *int64 `json:"parent"`
	// The object it makes up, as an attribute it exhibits or a part it
	// aggregates, as relation says; it is on that object's board rather than
	// a process's. See structure.go.
	Owner    *int64 `json:"owner"`
	Relation string `json:"relation"`
	// The object it specializes, whose fields it has too.
	General *int64 `json:"general"`
	// The module's object it is a copy of, and the module, which the copy
	// follows; see modules.go.
	Source     *int64        `json:"source"`
	SourceFlow *int64        `json:"sourceFlow"`
	States     []objectState `json:"states"`
	Attributes []attribute   `json:"attributes"`
}

// A link ties an object to a process: the process generates it (output) or
// takes it as input. A link naming one of the object's states generates the
// object in that state, or takes it once it enters that state. A human
// process's output link names no state; it lists the states the human
// decides between instead. An agent's output link can list states too, for
// the agent to decide between.
//
// An input link has a role, as in OPM: the process takes the object and
// leaves it for others (none), uses it as an instrument, a reference it
// doesn't change, or consumes it, so it is gone once taken. A process
// taking an object and generating it too affects it, as OPM's effect link:
// it hands in the object changed.
//
// An input link naming a state is an event link: the object entering that
// state makes its process run. One marked a condition, OPM's condition link,
// instead lets its process run only if the object is in that state once it
// is ready to; otherwise the process is skipped.
type link struct {
	ID        int64   `json:"id"`
	Process   int64   `json:"process"`
	Object    int64   `json:"object"`
	Direction string  `json:"direction"`
	State     *int64  `json:"state"`
	States    []int64 `json:"states"`
	Role      string  `json:"role"`
	Condition bool    `json:"condition"`
	// Where the link's drawn segments run, if moved by hand; see route.ts.
	Route json.RawMessage `json:"route"`
	// Where the object shows on the process's own board, if moved there by
	// hand; only a flow process has one.
	At *point `json:"at"`
}

type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// A point stored in nullable columns, nil when there is none.
func pointOf(x, y sql.NullFloat64) *point {
	if !x.Valid || !y.Valid {
		return nil
	}
	return &point{x.Float64, y.Float64}
}

const (
	directionOutput = "output"
	directionInput  = "input"
)

// The roles of an input link.
const (
	roleNone        = ""
	roleInstrument  = "instrument"
	roleConsumption = "consumption"
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
	if err := addColumn(db, "objects", "human", "BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
		return err
	}
	if err := addColumn(db, "objects", "at_end", "BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
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
	if err := addColumn(db, "object_links", "at_x", "DOUBLE NULL"); err != nil {
		return err
	}
	if err := addColumn(db, "object_links", "at_y", "DOUBLE NULL"); err != nil {
		return err
	}
	if err := addColumn(db, "object_links", "role", "VARCHAR(16) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn(db, "object_links", "is_condition", "BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
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

const objectColumns = "id, flow_id, name, x, y, width, persistent, many, min_items, max_items, human, at_end, parent_id, owner_id, relation, general_id, source_id, " +
	"(SELECT src.flow_id FROM objects src WHERE src.id = objects.source_id)"

// Whether an object is on a board of processes, rather than owned by
// another object.
const onProcessBoard = "owner_id IS NULL"

func getObject(ctx context.Context, db *sql.DB, id int64) (object, error) {
	o := object{States: []objectState{}, Attributes: []attribute{}}
	err := db.QueryRowContext(ctx, "SELECT "+objectColumns+" FROM objects WHERE id = ?", id).Scan(&o.ID, &o.Flow, &o.Name, &o.X, &o.Y, &o.Width, &o.Persistent, &o.Many, &o.Min, &o.Max, &o.Human, &o.End, &o.Parent, &o.Owner, &o.Relation, &o.General, &o.Source, &o.SourceFlow)
	if err != nil {
		return o, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id, object_id, name, source_id FROM object_states WHERE object_id = ? ORDER BY id", id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var s objectState
		if err := rows.Scan(&s.ID, &s.Object, &s.Name, &s.Source); err != nil {
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
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		rows, err := db.QueryContext(r.Context(), "SELECT "+objectColumns+" FROM objects WHERE flow_id = ? ORDER BY id", f)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()

		objs := []object{}
		for rows.Next() {
			var o object
			if err := rows.Scan(&o.ID, &o.Flow, &o.Name, &o.X, &o.Y, &o.Width, &o.Persistent, &o.Many, &o.Min, &o.Max, &o.Human, &o.End, &o.Parent, &o.Owner, &o.Relation, &o.General, &o.Source, &o.SourceFlow); err != nil {
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
		var msg string
		var err error
		if o.Owner != nil {
			if !ownObject(w, r, db, *o.Owner) {
				return
			}
			msg, err = checkOwned(r.Context(), db, &o)
		} else {
			o.Relation = ""
			o.General = nil
			if msg, err = checkParent(r.Context(), db, o.Parent); err == nil && msg == "" {
				o.Flow, msg, err = flowFor(r.Context(), db, o.Flow, o.Parent)
			}
		}
		if err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		// A struct holds its object alone, and the objects making it up.
		if o.Owner == nil && refuseInStruct(w, r, db, o.Flow) {
			return
		}
		res, err := db.ExecContext(r.Context(), "INSERT INTO objects (flow_id, name, x, y, width, human, parent_id, owner_id, relation) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			o.Flow, o.Name, o.X, o.Y, o.Width, o.Human, o.Parent, o.Owner, o.Relation)
		if err != nil {
			serverError(w, err)
			return
		}
		o.States = []objectState{}
		o.Attributes = []attribute{}
		o.Source, o.SourceFlow = nil, nil
		if o.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		if o.Owner != nil && !propagateFrom(w, r, db, o.ID, false) {
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
			Many       *bool `json:"many"`
			// How many items a list holds; raw so that an explicit null max,
			// no most, is told apart from an absent one. Checked with what
			// the object has once patched.
			Min   *int            `json:"min"`
			Max   json.RawMessage `json:"max"`
			Human *bool           `json:"human"`
			// Only an object of the top board other than a person can end it.
			End *bool `json:"end"`
			// Only an owned object has one.
			Relation *string `json:"relation"`
			// Raw so that an explicit null, specializing nothing, is told apart
			// from an absent field.
			General json.RawMessage `json:"general"`
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

		var owned, many bool
		var flow int64
		var q quantity
		err = db.QueryRowContext(r.Context(), "SELECT owner_id IS NOT NULL, flow_id, many, min_items, max_items FROM objects WHERE id = ?", id).
			Scan(&owned, &flow, &many, &q.min, &q.max)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "object not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		// Where a copy goes, and what this flow does with it, are its own.
		if (patch.Name != nil || patch.Many != nil || patch.Min != nil || len(patch.Max) > 0 || patch.Relation != nil || len(patch.General) > 0) && !ownObject(w, r, db, id) {
			return
		}
		root, err := isStructRoot(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if root && (isTrue(patch.Human) || isTrue(patch.Persistent) || isTrue(patch.End)) {
			http.Error(w, "a struct's object is what flows use; whether it is a person, stored or leads into an end is up to each copy of it", http.StatusBadRequest)
			return
		}
		if owned && (isTrue(patch.Human) || isTrue(patch.Persistent) || isTrue(patch.End)) {
			http.Error(w, "an object making up another is part of what that one holds: it can't be a person, stored or lead into an end on its own", http.StatusBadRequest)
			return
		}
		if patch.Relation != nil && (!owned || (*patch.Relation != relationExhibition && *patch.Relation != relationAggregation)) {
			http.Error(w, "only an object making up another has a relation, exhibition or aggregation", http.StatusBadRequest)
			return
		}
		if patch.Many != nil {
			many = *patch.Many
		}
		if patch.Min != nil {
			q.min = *patch.Min
		}
		if len(patch.Max) > 0 {
			if err := json.Unmarshal(patch.Max, &q.max); err != nil {
				http.Error(w, "invalid max: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if !many && patch.Min == nil && len(patch.Max) == 0 {
			// One object holds no bounds of a list it was.
			q = quantity{}
		}
		if msg := checkQuantity(many, q); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}

		var general *int64
		setGeneral := len(patch.General) > 0
		if setGeneral {
			if err := json.Unmarshal(patch.General, &general); err != nil {
				http.Error(w, "invalid general: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if setGeneral || patch.Name != nil {
			msg, err := checkStructure(r.Context(), db, flow, func(s structure) {
				if o := s[id]; o != nil {
					if setGeneral {
						o.general = general
					}
					if patch.Name != nil {
						o.name = *patch.Name
					}
				}
			})
			if err != nil {
				serverError(w, err)
				return
			}
			if msg != "" {
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}

		if patch.Human != nil && *patch.Human {
			var n int
			err := db.QueryRowContext(r.Context(),
				`SELECT COUNT(*) FROM object_links l JOIN processes p ON p.id = l.process_id
				WHERE l.object_id = ? AND l.direction = 'input' AND p.kind <> ?`, id, kindHuman).Scan(&n)
			if err != nil {
				serverError(w, err)
				return
			}
			if n > 0 {
				http.Error(w, agentLinkMessage, http.StatusBadRequest)
				return
			}
		}

		if patch.End != nil && *patch.End {
			var n int
			err := db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM objects WHERE id = ? AND parent_id IS NULL AND "+onProcessBoard+" AND NOT human", id).Scan(&n)
			if err != nil {
				serverError(w, err)
				return
			}
			if n == 0 {
				http.Error(w, "only an object of the top board, not a person, can lead into its end", http.StatusBadRequest)
				return
			}
		}

		_, err = db.ExecContext(r.Context(),
			"UPDATE objects SET name = COALESCE(?, name), x = COALESCE(?, x), y = COALESCE(?, y), width = COALESCE(?, width), persistent = COALESCE(?, persistent), many = ?, min_items = ?, max_items = ?, human = COALESCE(?, human), at_end = COALESCE(?, at_end), relation = COALESCE(?, relation) WHERE id = ?",
			patch.Name, patch.X, patch.Y, patch.Width, patch.Persistent, many, q.min, q.max, patch.Human, patch.End, patch.Relation, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if setGeneral {
			if _, err := db.ExecContext(r.Context(), "UPDATE objects SET general_id = ? WHERE id = ?", general, id); err != nil {
				serverError(w, err)
				return
			}
		}
		// A struct is named after its object.
		if root && patch.Name != nil {
			if _, err := db.ExecContext(r.Context(), "UPDATE flows SET name = ? WHERE id = ?", *patch.Name, flow); err != nil {
				serverError(w, err)
				return
			}
		}
		// Only what copies have changes them; a new outcome comes out of the
		// subflow processes running the flow.
		if patch.Name != nil || patch.Many != nil || patch.Min != nil || len(patch.Max) > 0 || patch.Relation != nil || setGeneral || isTrue(patch.End) {
			if err := propagate(r.Context(), db, flow, isTrue(patch.End)); err != nil {
				serverError(w, err)
				return
			}
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
		// A part of a copy goes only when it goes from its source.
		var flow int64
		var owner *int64
		err = db.QueryRowContext(r.Context(), "SELECT flow_id, owner_id FROM objects WHERE id = ?", id).Scan(&flow, &owner)
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if owner != nil && !ownObject(w, r, db, *owner) {
			return
		}
		if root, err := isStructRoot(r.Context(), db, id); err != nil {
			serverError(w, err)
			return
		} else if root {
			http.Error(w, "a struct is its object; delete the struct instead", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM objects WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		if err := propagate(r.Context(), db, flow, false); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/links", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		rows, err := db.QueryContext(r.Context(),
			`SELECT l.id, l.process_id, l.object_id, l.direction, l.state_id, l.route, l.at_x, l.at_y, l.role, l.is_condition
			FROM object_links l JOIN processes p ON p.id = l.process_id WHERE p.flow_id = ? ORDER BY l.id`, f)
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
			var atX, atY sql.NullFloat64
			if err := rows.Scan(&l.ID, &l.Process, &l.Object, &l.Direction, &state, &route, &atX, &atY, &l.Role, &l.Condition); err != nil {
				serverError(w, err)
				return
			}
			if state != 0 {
				l.State = &state
			}
			l.Route = routeJSON(route)
			l.At = pointOf(atX, atY)
			ls = append(ls, l)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}

		srows, err := db.QueryContext(r.Context(),
			`SELECT ls.link_id, ls.state_id FROM link_states ls
			JOIN object_links l ON l.id = ls.link_id JOIN processes p ON p.id = l.process_id
			WHERE p.flow_id = ? ORDER BY ls.state_id`, f)
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
		res, err := tx.ExecContext(r.Context(), "INSERT INTO object_links (process_id, object_id, direction, state_id, role, is_condition) VALUES (?, ?, ?, ?, ?, ?)",
			l.Process, l.Object, l.Direction, state, l.Role, l.Condition)
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
		l.At = nil
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

	// Places the object on the process's own board, or with null goes back to
	// the default place.
	mux.HandleFunc("PUT /api/links/{id}/at", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var at *point
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&at); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		var x, y sql.NullFloat64
		if at != nil {
			x, y = sql.NullFloat64{Float64: at.X, Valid: true}, sql.NullFloat64{Float64: at.Y, Valid: true}
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_links SET at_x = ?, at_y = ? WHERE id = ?", x, y, id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Makes an input link naming a state a condition, or an event again.
	mux.HandleFunc("PUT /api/links/{id}/condition", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Condition bool `json:"condition"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		var l link
		var state int64
		err = db.QueryRowContext(r.Context(), "SELECT direction, state_id FROM object_links WHERE id = ?", id).Scan(&l.Direction, &state)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "link not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if state != 0 {
			l.State = &state
		}
		l.Condition = body.Condition
		if msg := checkCondition(l); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_links SET is_condition = ? WHERE id = ?", l.Condition, id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Sets the role of an input link.
	mux.HandleFunc("PUT /api/links/{id}/role", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Role string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		var l link
		err = db.QueryRowContext(r.Context(), "SELECT id, process_id, object_id, direction FROM object_links WHERE id = ?", id).
			Scan(&l.ID, &l.Process, &l.Object, &l.Direction)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "link not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		l.Role = body.Role
		if msg, err := checkRole(r.Context(), db, l); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_links SET role = ? WHERE id = ?", l.Role, id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Replaces the states a human's or an agent's output link decides between.
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
			WHERE l.id = ? AND l.direction = 'output' AND p.kind IN (?, ?) AND l.state_id = 0`, id, kindHuman, kindAgent).Scan(&object)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no human or agent output link naming no state with that id", http.StatusNotFound)
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

func isTrue(b *bool) bool { return b != nil && *b }

// Says what is wrong with a link being a condition, if it is one.
func checkCondition(l link) string {
	if l.Condition && (l.Direction != directionInput || l.State == nil) {
		return "only an input link naming a state is a condition"
	}
	return ""
}

// Says what is wrong with a link's role, if anything: only an input link
// has one, a person is neither an instrument nor consumed, and only one
// process consumes an object, so no two take it from each other.
func checkRole(ctx context.Context, db *sql.DB, l link) (string, error) {
	switch l.Role {
	case roleNone:
		return "", nil
	case roleInstrument, roleConsumption:
	default:
		return "role must be empty, instrument or consumption", nil
	}
	if l.Direction != directionInput {
		return "only an input link has a role", nil
	}
	var human bool
	if err := db.QueryRowContext(ctx, "SELECT human FROM objects WHERE id = ?", l.Object).Scan(&human); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if human {
		return "a person carries out a process; they are neither its instrument nor consumed", nil
	}
	if l.Role != roleConsumption {
		return "", nil
	}
	var n int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_links WHERE object_id = ? AND role = ? AND id <> ?", l.Object, roleConsumption, l.ID).Scan(&n)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "another process consumes the object already; only one can", nil
	}
	return "", nil
}

// Fills in what a new object making up o.Owner takes from it, and says what
// is wrong with it, if anything: it is on its owner's board, in its flow,
// and its name is a field of its owner's.
func checkOwned(ctx context.Context, db *sql.DB, o *object) (string, error) {
	if o.Human {
		return "a person can't make up an object", nil
	}
	if o.Relation == "" {
		o.Relation = relationExhibition
	}
	if o.Relation != relationExhibition && o.Relation != relationAggregation {
		return "relation must be exhibition or aggregation", nil
	}
	err := db.QueryRowContext(ctx, "SELECT flow_id FROM objects WHERE id = ?", *o.Owner).Scan(&o.Flow)
	if errors.Is(err, sql.ErrNoRows) {
		return "owner not found", nil
	}
	if err != nil {
		return "", err
	}
	o.Parent, o.General = nil, nil
	return checkStructure(ctx, db, o.Flow, func(s structure) {
		// Ids only grow, so the new object comes last, as it will.
		s[1<<62] = &structObject{id: 1 << 62, name: o.Name, owner: o.Owner, relation: o.Relation}
	})
}

const agentLinkMessage = "a person carries out only a human process; link them to one"

const decisionLinkMessage = "only a human's or an agent's output link lists states"

// Says what is wrong with a new link, if anything. A named state must be one
// of the object's. A human process decides by setting its object to one of
// the states its output link lists, and an agent can decide between the
// states its output link lists; only they list states. Any process other
// than a human generates an object once, so it has at most one output link
// to it.
// A human object's input link is an agent link, so it goes into a human
// process.
func checkLink(ctx context.Context, db *sql.DB, l link) (string, error) {
	if ok, err := onBoardOf(ctx, db, l.Process, l.Object); err != nil || !ok {
		return "the object isn't on the process's board", err
	}
	if msg, err := checkRole(ctx, db, l); err != nil || msg != "" {
		return msg, err
	}
	if msg := checkCondition(l); msg != "" {
		return msg, nil
	}
	var kind string
	err := db.QueryRowContext(ctx, "SELECT kind FROM processes WHERE id = ?", l.Process).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "process not found", nil
	}
	if err != nil {
		return "", err
	}
	if l.Direction == directionInput && kind != kindHuman {
		var human bool
		if err := db.QueryRowContext(ctx, "SELECT human FROM objects WHERE id = ?", l.Object).Scan(&human); err != nil {
			return "", err
		}
		if human {
			return agentLinkMessage, nil
		}
	}
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
			return decisionLinkMessage, nil
		}
		return "", nil
	}

	if kind == kindHuman {
		if l.State != nil {
			return "a human process links to the object and lists the states it decides between", nil
		}
		return "", nil
	}
	switch {
	case len(l.States) > 0 && kind != kindAgent:
		return decisionLinkMessage, nil
	case len(l.States) > 0 && l.State != nil:
		return "an agent's link to an object either names the state it generates it in or lists the states it decides between", nil
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
