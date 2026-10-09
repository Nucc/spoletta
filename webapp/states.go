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

// A state is one of the situations an OPM object can be in, such as a draft
// being approved or having changes requested. Links can name a state: an
// output link generates its object in that state, and an input link is a
// condition link that runs its process when the object enters the state. A
// human process decides by setting its object to one of the states its
// output link lists.
type objectState struct {
	ID     int64  `json:"id"`
	Object int64  `json:"object"`
	Name   string `json:"name"`
}

// What older versions' human routes become: the reviewed object gets these
// states, the human sets them, and the routes' targets take the object in them.
var routeStateNames = map[string]string{"approved": "approved", "changes": "changes requested"}

func migrateStates(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS object_states (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		object_id BIGINT NOT NULL,
		name VARCHAR(255) NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE CASCADE
	)`)
	return err
}

// Older versions sent a human process's decision along a route per outcome,
// straight to the next process. Each route becomes a state of the object the
// human reviews (the first it takes as input, or a new one if it takes none),
// an output link from the human to that state and a condition link from the
// state to the route's target. Then the routes table goes, and
// migrateDecisionLinks merges the human's links to states into one.
func migrateRoutes(db *sql.DB) error {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'routes'`).Scan(&n)
	if err != nil || n == 0 {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type route struct {
		process, target int64
		outcome, name   string
		x, y            float64
	}
	rows, err := tx.Query(`SELECT r.process_id, r.target_id, r.outcome, p.name, p.x, p.y
		FROM routes r JOIN processes p ON p.id = r.process_id ORDER BY r.id`)
	if err != nil {
		return err
	}
	var routes []route
	for rows.Next() {
		var r route
		if err := rows.Scan(&r.process, &r.target, &r.outcome, &r.name, &r.x, &r.y); err != nil {
			rows.Close()
			return err
		}
		routes = append(routes, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	reviewed := map[int64]int64{}
	for _, r := range routes {
		object, ok := reviewed[r.process]
		if !ok {
			var first sql.NullInt64
			err := tx.QueryRow("SELECT MIN(object_id) FROM object_links WHERE process_id = ? AND direction = 'input'", r.process).Scan(&first)
			if err != nil {
				return err
			}
			object = first.Int64
			if !first.Valid {
				res, err := tx.Exec("INSERT INTO objects (name, x, y) VALUES (?, ?, ?)", r.name+" decision", r.x, r.y+140)
				if err != nil {
					return err
				}
				if object, err = res.LastInsertId(); err != nil {
					return err
				}
			}
			reviewed[r.process] = object
		}

		name, ok := routeStateNames[r.outcome]
		if !ok {
			name = r.outcome
		}
		var state int64
		err := tx.QueryRow("SELECT id FROM object_states WHERE object_id = ? AND name = ?", object, name).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			var res sql.Result
			if res, err = tx.Exec("INSERT INTO object_states (object_id, name) VALUES (?, ?)", object, name); err != nil {
				return err
			}
			state, err = res.LastInsertId()
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT IGNORE INTO object_links (process_id, object_id, direction, state_id)
			VALUES (?, ?, 'output', ?), (?, ?, 'input', ?)`, r.process, object, state, r.target, object, state)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.Exec("DROP TABLE routes")
	return err
}

// A human used to have an output link per state it could set. Each human's
// links to one object's states become a single link to the object, listing
// those states.
func migrateDecisionLinks(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`INSERT IGNORE INTO link_states (link_id, state_id)
		SELECT k.keep_id, l.state_id FROM object_links l
		JOIN processes p ON p.id = l.process_id AND p.kind = 'human'
		JOIN (SELECT process_id, object_id, MIN(id) AS keep_id FROM object_links
			WHERE direction = 'output' GROUP BY process_id, object_id) k
			ON k.process_id = l.process_id AND k.object_id = l.object_id
		WHERE l.direction = 'output' AND l.state_id <> 0`,
		`DELETE l FROM object_links l
		JOIN processes p ON p.id = l.process_id AND p.kind = 'human'
		JOIN (SELECT process_id, object_id, MIN(id) AS keep_id FROM object_links
			WHERE direction = 'output' GROUP BY process_id, object_id) k
			ON k.process_id = l.process_id AND k.object_id = l.object_id
		WHERE l.direction = 'output' AND l.id <> k.keep_id`,
		`UPDATE object_links l JOIN processes p ON p.id = l.process_id AND p.kind = 'human'
		SET l.state_id = 0 WHERE l.direction = 'output' AND l.state_id <> 0`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func registerStateRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("POST /api/objects/{id}/states", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var s objectState
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.Object = id
		if s.Name = strings.TrimSpace(s.Name); s.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}

		// Processes set an object's states, and only objects on a board meet them.
		var owned bool
		if err := db.QueryRowContext(r.Context(), "SELECT owner_id IS NOT NULL FROM objects WHERE id = ?", s.Object).Scan(&owned); err == nil && owned {
			http.Error(w, "an object making up another has no states of its own; give them to the object on the board", http.StatusBadRequest)
			return
		}
		res, err := db.ExecContext(r.Context(), "INSERT INTO object_states (object_id, name) VALUES (?, ?)", s.Object, s.Name)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "object not found", http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if s.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s)
	})

	mux.HandleFunc("PATCH /api/states/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Name = strings.TrimSpace(patch.Name); patch.Name == "" {
			http.Error(w, "name must not be empty", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_states SET name = ? WHERE id = ?", patch.Name, id); err != nil {
			serverError(w, err)
			return
		}

		var s objectState
		err = db.QueryRowContext(r.Context(), "SELECT id, object_id, name FROM object_states WHERE id = ?", id).Scan(&s.ID, &s.Object, &s.Name)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "state not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	// The links naming the state go with it.
	mux.HandleFunc("DELETE /api/states/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if err := deleteState(r.Context(), db, id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// object_links.state_id has no foreign key: 0 stands for no state, which
// keeps it in the links' unique key, so its links are deleted here.
func deleteState(ctx context.Context, db *sql.DB, id int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM object_links WHERE state_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM object_states WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// The states of every object, by object id.
func listStates(ctx context.Context, db *sql.DB) (map[int64][]objectState, error) {
	rows, err := db.QueryContext(ctx, "SELECT id, object_id, name FROM object_states ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[int64][]objectState{}
	for rows.Next() {
		var s objectState
		if err := rows.Scan(&s.ID, &s.Object, &s.Name); err != nil {
			return nil, err
		}
		states[s.Object] = append(states[s.Object], s)
	}
	return states, rows.Err()
}
