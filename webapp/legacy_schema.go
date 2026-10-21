package main

// How the database was kept up to date before migrations (migrations.go):
// each domain made its tables if they were missing and added the columns,
// keys and data later versions needed, checking first what was there, every
// time the webapp started. It is now migration 00001, run once on a database
// from before then to bring it to where migrations took over; a new one gets
// the same schema whole from 00002. Nothing here changes any more: a change
// to the schema is a new migration.

import (
	"context"
	"database/sql"
	"errors"
)

func migrateProcesses(db *sql.DB) error {
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

func migrateFlow(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS connections (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			source_id BIGINT NOT NULL,
			target_id BIGINT NOT NULL,
			UNIQUE KEY source_target (source_id, target_id),
			FOREIGN KEY (source_id) REFERENCES processes(id) ON DELETE CASCADE,
			FOREIGN KEY (target_id) REFERENCES processes(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	if err := addColumn(db, "connections", "kind", "VARCHAR(16) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := migrateObjects(db); err != nil {
		return err
	}
	if err := migrateBoards(db); err != nil {
		return err
	}
	if err := migrateStructure(db); err != nil {
		return err
	}
	if err := migrateRoutes(db); err != nil {
		return err
	}
	if err := migrateDecisionLinks(db); err != nil {
		return err
	}
	if err := migrateRuns(db); err != nil {
		return err
	}
	if err := migrateInstances(db); err != nil {
		return err
	}
	if err := migrateItems(db); err != nil {
		return err
	}
	if err := migrateQueries(db); err != nil {
		return err
	}
	if err := migrateVersions(db); err != nil {
		return err
	}
	if err := migrateFlows(db); err != nil {
		return err
	}
	if err := migrateModules(db); err != nil {
		return err
	}
	if err := migrateStructs(db); err != nil {
		return err
	}
	if err := migrateShared(db); err != nil {
		return err
	}
	if err := migrateRecordEdits(db); err != nil {
		return err
	}
	if err := migrateHistory(db); err != nil {
		return err
	}
	return migrateAgent(db)
}

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

func migrateStates(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS object_states (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		object_id BIGINT NOT NULL,
		name VARCHAR(255) NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE CASCADE
	)`)
	if err != nil {
		return err
	}
	return addColumn(db, "object_states", "rule", "TEXT NULL")
}

func migrateAttributes(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS object_attributes (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		object_id BIGINT NOT NULL,
		name VARCHAR(255) NOT NULL,
		definition TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE KEY object_name (object_id, name),
		FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE CASCADE
	)`)
	if err != nil {
		return err
	}
	return addColumn(db, "object_attributes", "is_file", "BOOLEAN NOT NULL DEFAULT FALSE")
}

func migrateBoards(db *sql.DB) error {
	for _, t := range []string{"processes", "objects"} {
		if err := addColumn(db, t, "parent_id", "BIGINT NULL"); err != nil {
			return err
		}
		ok, err := hasConstraint(db, t, t+"_parent")
		if err != nil {
			return err
		}
		if !ok {
			_, err := db.Exec("ALTER TABLE " + t + " ADD CONSTRAINT " + t + "_parent FOREIGN KEY (parent_id) REFERENCES processes(id) ON DELETE CASCADE")
			if err != nil {
				return err
			}
		}
	}
	if err := addColumn(db, "processes", "recording", "BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS inner_boards (
		process_id BIGINT PRIMARY KEY,
		x DOUBLE NOT NULL DEFAULT 0,
		y DOUBLE NOT NULL DEFAULT 0,
		target_id BIGINT NULL,
		route JSON NULL,
		end_x DOUBLE NOT NULL DEFAULT 0,
		end_y DOUBLE NOT NULL DEFAULT 0,
		FOREIGN KEY (process_id) REFERENCES processes(id) ON DELETE CASCADE,
		FOREIGN KEY (target_id) REFERENCES processes(id) ON DELETE SET NULL
	)`)
	return err
}

func migrateStructure(db *sql.DB) error {
	for _, c := range []struct{ name, def string }{
		{"owner_id", "BIGINT NULL"},
		{"general_id", "BIGINT NULL"},
		// How an owned object makes up its owner: an attribute it exhibits or a
		// part it aggregates. Empty for other objects.
		{"relation", "VARCHAR(16) NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(db, "objects", c.name, c.def); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, def string }{
		{"objects_owner", "FOREIGN KEY (owner_id) REFERENCES objects(id) ON DELETE CASCADE"},
		{"objects_general", "FOREIGN KEY (general_id) REFERENCES objects(id) ON DELETE SET NULL"},
	} {
		ok, err := hasConstraint(db, "objects", c.name)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := db.Exec("ALTER TABLE objects ADD CONSTRAINT " + c.name + " " + c.def); err != nil {
				return err
			}
		}
	}
	return nil
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

// What older versions' human routes become: the reviewed object gets these
// states, the human sets them, and the routes' targets take the object in them.
var routeStateNames = map[string]string{"approved": "approved", "changes": "changes requested"}

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

// Processes and objects can be deleted later; their history stays.
func migrateRuns(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS runs (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			status VARCHAR(16) NOT NULL,
			started_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			finished_at TIMESTAMP(3) NULL
		)`,
		`CREATE TABLE IF NOT EXISTS run_steps (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			run_id BIGINT NOT NULL,
			process_id BIGINT NULL,
			process_name VARCHAR(255) NOT NULL,
			kind VARCHAR(16) NOT NULL,
			model VARCHAR(255) NOT NULL DEFAULT '',
			thinking VARCHAR(16) NOT NULL DEFAULT '',
			prompt LONGTEXT NOT NULL,
			inputs JSON NOT NULL,
			change_request JSON NULL,
			status VARCHAR(16) NOT NULL,
			output LONGTEXT NOT NULL,
			error TEXT NOT NULL,
			outcome VARCHAR(255) NOT NULL DEFAULT '',
			instructions TEXT NOT NULL,
			started_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			finished_at TIMESTAMP(3) NULL,
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
			FOREIGN KEY (process_id) REFERENCES processes(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS run_objects (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			run_id BIGINT NOT NULL,
			step_id BIGINT NOT NULL,
			object_id BIGINT NULL,
			name VARCHAR(255) NOT NULL,
			state VARCHAR(255) NOT NULL DEFAULT '',
			content LONGTEXT NOT NULL,
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE,
			FOREIGN KEY (object_id) REFERENCES objects(id) ON DELETE SET NULL
		)`,
		// The conversation of an agent step, a message per row; see messages.go.
		`CREATE TABLE IF NOT EXISTS run_step_messages (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			step_id BIGINT NOT NULL,
			attempt INT NOT NULL,
			message JSON NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE
		)`,
		// Outcomes used to be approved or changes; now they name states.
		"ALTER TABLE run_steps MODIFY outcome VARCHAR(255) NOT NULL DEFAULT ''",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	for _, c := range []struct{ table, name, def string }{
		{"run_objects", "state", "VARCHAR(255) NOT NULL DEFAULT '' AFTER name"},
		{"runs", "version_id", "BIGINT NULL AFTER status"},
		// Ids that outlive the process or object: deleting it, or jumping to a
		// version without it, nulls the foreign keys, yet the run's version
		// still has it.
		{"run_steps", "process_ref", "BIGINT NULL AFTER process_id"},
		{"run_objects", "object_ref", "BIGINT NULL AFTER object_id"},
		{"runs", "parent_run_id", "BIGINT NULL AFTER version_id"},
		{"runs", "process_ref", "BIGINT NULL AFTER parent_run_id"},
		{"runs", "process_name", "VARCHAR(255) NOT NULL DEFAULT '' AFTER process_ref"},
		{"runs", "item_key", "VARCHAR(255) NOT NULL DEFAULT '' AFTER process_name"},
		{"run_steps", "child_run_id", "BIGINT NULL AFTER instructions"},
		{"runs", "schedule_id", "BIGINT NULL AFTER item_key"},
		{"runs", "rerun_of", "BIGINT NULL AFTER process_name"},
		{"runs", "rerun_from", "BIGINT NULL AFTER rerun_of"},
		{"runs", "rerun_from_name", "VARCHAR(255) NOT NULL DEFAULT '' AFTER rerun_from"},
		// For a re-run: the commit each repository's branch started from.
		{"runs", "starts", "JSON NULL AFTER rerun_from_name"},
	} {
		if err := addColumn(db, c.table, c.name, c.def); err != nil {
			return err
		}
	}
	for _, s := range []string{
		"UPDATE run_steps SET process_ref = process_id WHERE process_ref IS NULL AND process_id IS NOT NULL",
		"UPDATE run_objects SET object_ref = object_id WHERE object_ref IS NULL AND object_id IS NOT NULL",
	} {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Unique values are stored as a hash in instance_keys, whose primary key
// lets only one instance hold each; the value itself is kept to say what
// collided.
func migrateInstances(db *sql.DB) error {
	if err := addColumn(db, "objects", "persistent", "BOOLEAN NOT NULL DEFAULT FALSE AFTER width"); err != nil {
		return err
	}
	if err := addColumn(db, "objects", "many", "BOOLEAN NOT NULL DEFAULT FALSE AFTER persistent"); err != nil {
		return err
	}
	if err := addColumn(db, "object_attributes", "is_unique", "BOOLEAN NOT NULL DEFAULT FALSE AFTER definition"); err != nil {
		return err
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS object_instances (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			object_id BIGINT NOT NULL,
			run_id BIGINT NOT NULL,
			step_id BIGINT NOT NULL,
			content LONGTEXT NOT NULL,
			state VARCHAR(255) NOT NULL DEFAULT '',
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			UNIQUE KEY object_run (object_id, run_id),
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS instance_keys (
			object_id BIGINT NOT NULL,
			attribute_id BIGINT NOT NULL,
			value_hash CHAR(64) NOT NULL,
			value TEXT NOT NULL,
			instance_id BIGINT NOT NULL,
			PRIMARY KEY (object_id, attribute_id, value_hash),
			FOREIGN KEY (instance_id) REFERENCES object_instances(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrateItems(db *sql.DB) error {
	if err := addColumn(db, "objects", "min_items", "INT NOT NULL DEFAULT 0 AFTER many"); err != nil {
		return err
	}
	// Null for no most.
	return addColumn(db, "objects", "max_items", "INT NULL AFTER min_items")
}

func migrateQueries(db *sql.DB) error {
	return addColumn(db, "object_links", "query", "JSON NULL")
}

func migrateVersions(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS flow_versions (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			based_on BIGINT NULL,
			snapshot JSON NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Before flows there was a single board, whose start lived in the one row of
// start_node and whose current version in the one row of flow_current. It
// becomes the first flow.
func migrateFlows(db *sql.DB) error {
	stmts := []string{
		// version_id is the version the board was last saved as or jumped to.
		`CREATE TABLE IF NOT EXISTS flows (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			version_id BIGINT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			FOREIGN KEY (version_id) REFERENCES flow_versions(id) ON DELETE SET NULL
		)`,
		// The start of a flow's top board, with the columns of inner_boards' start.
		`CREATE TABLE IF NOT EXISTS flow_starts (
			flow_id BIGINT PRIMARY KEY,
			x DOUBLE NOT NULL DEFAULT 0,
			y DOUBLE NOT NULL DEFAULT 0,
			target_id BIGINT NULL,
			route JSON NULL,
			FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE,
			FOREIGN KEY (target_id) REFERENCES processes(id) ON DELETE SET NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	for _, t := range flowTables {
		if err := addColumn(db, t, "flow_id", "BIGINT NULL"); err != nil {
			return err
		}
	}
	if err := addColumn(db, "flows", "settings", "JSON NULL"); err != nil {
		return err
	}
	// The end of a flow's top board, with the columns of inner_boards' end.
	// Null until moved: the board places it below what is on it.
	for _, c := range []string{"end_x", "end_y"} {
		if err := addColumn(db, "flow_starts", c, "DOUBLE NULL"); err != nil {
			return err
		}
	}
	if err := addColumn(db, "run_steps", "branch", "VARCHAR(255) NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := addColumn(db, "run_steps", "commits", "JSON NULL"); err != nil {
		return err
	}
	if err := migrateFollowUps(db); err != nil {
		return err
	}

	legacy, err := hasTable(db, "start_node")
	if err != nil {
		return err
	}
	if legacy {
		var flows int
		if err := db.QueryRow("SELECT COUNT(*) FROM flows").Scan(&flows); err != nil {
			return err
		}
		// A start up that went down before dropping the old tables already moved them.
		if flows == 0 {
			if err := adoptBoard(db); err != nil {
				return err
			}
		}
		for _, t := range []string{"flow_current", "start_node"} {
			if _, err := db.Exec("DROP TABLE IF EXISTS " + t); err != nil {
				return err
			}
		}
	}

	for _, t := range flowTables {
		var nullable string
		err := db.QueryRow(`SELECT IS_NULLABLE FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'flow_id'`, t).Scan(&nullable)
		if err != nil {
			return err
		}
		if nullable == "YES" {
			if _, err := db.Exec("ALTER TABLE " + t + " MODIFY flow_id BIGINT NOT NULL"); err != nil {
				return err
			}
		}
		ok, err := hasConstraint(db, t, t+"_flow")
		if err != nil {
			return err
		}
		if !ok {
			// Deleting a flow deletes what is in it first; see deleteFlow.
			if _, err := db.Exec("ALTER TABLE " + t + " ADD CONSTRAINT " + t + "_flow FOREIGN KEY (flow_id) REFERENCES flows(id)"); err != nil {
				return err
			}
		}
	}
	return nil
}

// The tables whose rows belong to a flow, by flow_id.
var flowTables = []string{"processes", "objects", "runs", "flow_versions"}

func migrateFollowUps(db *sql.DB) error {
	if err := addColumn(db, "run_steps", "session", "JSON NULL"); err != nil {
		return err
	}
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS run_step_followups (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			step_id BIGINT NOT NULL,
			attempt INT NOT NULL,
			message TEXT NOT NULL,
			status VARCHAR(16) NOT NULL,
			output LONGTEXT NOT NULL,
			error TEXT NOT NULL,
			commits JSON NULL,
			started_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			finished_at TIMESTAMP(3) NULL,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE CASCADE
		)`,
	} {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// Makes the single board from before flows the first flow.
func adoptBoard(db *sql.DB) error {
	if err := addColumn(db, "start_node", "route", "JSON NULL"); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version *int64
	hasCurrent, err := hasTable(db, "flow_current")
	if err != nil {
		return err
	}
	if hasCurrent {
		err := tx.QueryRow("SELECT version_id FROM flow_current WHERE id = 1").Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	res, err := tx.Exec("INSERT INTO flows (name, version_id) VALUES ('Flow', ?)", version)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO flow_starts (flow_id, x, y, target_id, route) SELECT ?, x, y, target_id, route FROM start_node WHERE id = 1", id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT IGNORE INTO flow_starts (flow_id, x, y) VALUES (?, 40, 40)", id); err != nil {
		return err
	}
	for _, t := range flowTables {
		if _, err := tx.Exec("UPDATE "+t+" SET flow_id = ? WHERE flow_id IS NULL", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migrateModules(db *sql.DB) error {
	had, err := hasColumn(db, "flows", "module")
	if err != nil {
		return err
	}
	if err := addColumn(db, "flows", "module", "BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
		return err
	}
	// The flows already run by others were the modules all along.
	if !had {
		_, err := db.Exec(`UPDATE flows SET module = TRUE WHERE id IN (
			SELECT CAST(JSON_EXTRACT(settings, '$.flow') AS SIGNED) FROM processes
			WHERE kind = ? AND JSON_EXTRACT(settings, '$.flow') IS NOT NULL)`, kindSubflow)
		if err != nil {
			return err
		}
	}

	// A state's or attribute's source has no foreign key: going back to a
	// version rewrites them under the same ids, which a key would forget.
	hadSources, err := hasColumn(db, "objects", "source_id")
	if err != nil {
		return err
	}
	for _, t := range []string{"objects", "object_states", "object_attributes"} {
		if err := addColumn(db, t, "source_id", "BIGINT NULL"); err != nil {
			return err
		}
	}
	ok, err := hasConstraint(db, "objects", "objects_source")
	if err != nil {
		return err
	}
	if !ok {
		if _, err := db.Exec("ALTER TABLE objects ADD CONSTRAINT objects_source FOREIGN KEY (source_id) REFERENCES objects(id) ON DELETE SET NULL"); err != nil {
			return err
		}
	}
	if !hadSources {
		return adoptCopies(db)
	}
	return nil
}

// Outcomes subflow processes brought in before copies kept their source get
// it, when they are still as they were copied: the same states, attributes
// and parts by name as the module's object. One changed since stays as it is.
// Only their sources are set, so nothing on a board changes until the module
// does.
func adoptCopies(db *sql.DB) error {
	ctx := context.Background()
	return inTx(ctx, db, func(tx *sql.Tx) error {
		type candidate struct{ copy, source, module int64 }
		var cs []candidate
		err := scanAll(ctx, tx, `SELECT DISTINCT o.id, src.id, src.flow_id FROM object_links l
			JOIN processes p ON p.id = l.process_id
			JOIN objects o ON o.id = l.object_id
			JOIN objects src ON src.flow_id = CAST(JSON_EXTRACT(p.settings, '$.flow') AS SIGNED)
				AND src.name = o.name AND src.parent_id IS NULL AND src.owner_id IS NULL AND src.at_end
			WHERE p.kind = ? AND l.direction = 'output' AND o.owner_id IS NULL AND o.source_id IS NULL
			ORDER BY o.id`, func(rows *sql.Rows) error {
			var c candidate
			err := rows.Scan(&c.copy, &c.source, &c.module)
			cs = append(cs, c)
			return err
		}, kindSubflow)
		if err != nil {
			return err
		}
		modules := map[int64]structure{}
		for _, c := range cs {
			s := modules[c.module]
			if s == nil {
				if s, err = loadStructure(ctx, tx, c.module); err != nil {
					return err
				}
				modules[c.module] = s
			}
			same, err := copiedAsIs(ctx, tx, s, c.source, c.copy)
			if err != nil {
				return err
			}
			if same {
				if err := adoptCopy(ctx, tx, s, c.source, c.copy); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func migrateStructs(db *sql.DB) error {
	return addColumn(db, "flows", "is_struct", "BOOLEAN NOT NULL DEFAULT FALSE")
}

func migrateShared(db *sql.DB) error {
	// The shared object whose records this one reads and writes: a shared
	// struct's own object for itself, and for its copies.
	if err := addColumn(db, "objects", "store_id", "BIGINT NULL AFTER source_id"); err != nil {
		return err
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS shared_records (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			object_id BIGINT NOT NULL,
			content LONGTEXT NOT NULL,
			state VARCHAR(255) NOT NULL DEFAULT '',
			run_id BIGINT NULL,
			step_id BIGINT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			KEY object_updated (object_id, updated_at),
			KEY step (step_id),
			FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL,
			FOREIGN KEY (step_id) REFERENCES run_steps(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS shared_keys (
			object_id BIGINT NOT NULL,
			attribute_id BIGINT NOT NULL,
			value_hash CHAR(64) NOT NULL,
			record_id BIGINT NOT NULL,
			PRIMARY KEY (object_id, attribute_id, value_hash),
			FOREIGN KEY (record_id) REFERENCES shared_records(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrateRecordEdits(db *sql.DB) error {
	if err := addColumn(db, "shared_records", "edited_by", "BIGINT NULL"); err != nil {
		return err
	}
	return addColumn(db, "shared_records", "edited_at", "TIMESTAMP(3) NULL")
}

func migrateHistory(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS flow_history (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		flow_id BIGINT NOT NULL,
		snapshot JSON NOT NULL,
		undone BOOLEAN NOT NULL DEFAULT FALSE,
		INDEX flow_history_steps (flow_id, undone, id),
		FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE
	)`)
	return err
}

func migrateAgent(db *sql.DB) error {
	// Agent settings used to live here, API key included; pi's config replaced them.
	_, err := db.Exec("DROP TABLE IF EXISTS agent_settings")
	return err
}

func migrateMCP(db *sql.DB) error {
	stmts := []string{`CREATE TABLE IF NOT EXISTS mcp_servers (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(64) NOT NULL UNIQUE,
		url VARCHAR(2048) NOT NULL,
		description VARCHAR(1024) NOT NULL DEFAULT '',
		client_id VARCHAR(255) NOT NULL DEFAULT '',
		redirect_uri VARCHAR(2048) NOT NULL DEFAULT '',
		token_endpoint VARCHAR(2048) NOT NULL DEFAULT '',
		access_token TEXT NULL,
		refresh_token TEXT NULL,
		expires_at DATETIME NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
		// Each person's connection to a server; the user 0, whom nobody is,
		// holds the one everyone uses without sign-in.
		`CREATE TABLE IF NOT EXISTS mcp_tokens (
			server_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			access_token TEXT NULL,
			refresh_token TEXT NULL,
			expires_at DATETIME NULL,
			PRIMARY KEY (server_id, user_id),
			FOREIGN KEY (server_id) REFERENCES mcp_servers(id) ON DELETE CASCADE
		)`,
		// Servers held their one connection themselves before people
		// connected their own; it becomes the one without sign-in.
		`INSERT IGNORE INTO mcp_tokens (server_id, user_id, access_token, refresh_token, expires_at)
			SELECT id, 0, access_token, refresh_token, expires_at FROM mcp_servers WHERE access_token IS NOT NULL OR refresh_token IS NOT NULL`,
		`UPDATE mcp_servers SET access_token = NULL, refresh_token = NULL, expires_at = NULL WHERE access_token IS NOT NULL OR refresh_token IS NOT NULL`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, def string }{
		{"auth", "VARCHAR(16) NOT NULL DEFAULT 'oauth'"},
		{"connector", "VARCHAR(64) NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(db, "mcp_servers", c.name, c.def); err != nil {
			return err
		}
	}
	return nil
}

// Times are kept as UTC, in DATETIME so MySQL's session time zone doesn't
// move them.
func migrateSchedules(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schedules (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			flow_id BIGINT NOT NULL,
			cron VARCHAR(255) NOT NULL,
			timezone VARCHAR(64) NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			next_run_at DATETIME(3) NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE,
			INDEX schedules_due (enabled, next_run_at)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	// Each run started by a schedule says which one.
	has, err := hasIndex(db, "runs", "runs_schedule")
	if err != nil || has {
		return err
	}
	_, err = db.Exec("ALTER TABLE runs ADD CONSTRAINT runs_schedule FOREIGN KEY (schedule_id) REFERENCES schedules(id) ON DELETE SET NULL")
	return err
}

func migrateAssistant(db *sql.DB) error {
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS assistant_messages (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			flow_id BIGINT NOT NULL,
			role VARCHAR(16) NOT NULL,
			content LONGTEXT NOT NULL,
			status VARCHAR(16) NOT NULL,
			error TEXT NOT NULL,
			applied BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			finished_at TIMESTAMP(3) NULL,
			FOREIGN KEY (flow_id) REFERENCES flows(id) ON DELETE CASCADE
		)`,
	} {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrateUsers(db *sql.DB) error {
	stmts := []string{
		// Someone an admin added has no subject until they first sign in.
		`CREATE TABLE IF NOT EXISTS users (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			email VARCHAR(320) NOT NULL,
			issuer VARCHAR(255) NOT NULL DEFAULT '',
			subject VARCHAR(255) NULL,
			name VARCHAR(255) NOT NULL DEFAULT '',
			admin BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			signed_in_at TIMESTAMP(3) NULL,
			UNIQUE KEY users_email (email),
			UNIQUE KEY users_subject (issuer, subject)
		)`,
		// A session is known by its token's hash, so the table doesn't hold
		// what signs anyone in.
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash CHAR(64) PRIMARY KEY,
			user_id BIGINT NOT NULL,
			expires_at TIMESTAMP(3) NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			KEY sessions_expiry (expires_at),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	for _, c := range []struct{ table, name, def string }{
		{"runs", "started_by", "BIGINT NULL AFTER schedule_id"},
		{"run_steps", "decided_by", "BIGINT NULL AFTER instructions"},
		// Someone an admin removed; see user_roles.go.
		{"users", "removed_at", "TIMESTAMP(3) NULL"},
	} {
		if err := addColumn(db, c.table, c.name, c.def); err != nil {
			return err
		}
	}
	// Who a user is to each provider they signed in with. Users once had one
	// provider, in their issuer and subject, which are now only read here.
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS user_identities (
			issuer VARCHAR(255) NOT NULL,
			subject VARCHAR(255) NOT NULL,
			user_id BIGINT NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			PRIMARY KEY (issuer, subject),
			KEY user_identities_user (user_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`INSERT IGNORE INTO user_identities (issuer, subject, user_id)
			SELECT issuer, subject, id FROM users WHERE subject IS NOT NULL AND issuer <> ''`,
	} {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrateUserRoles(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS user_roles (
		user_id BIGINT NOT NULL,
		name VARCHAR(255) NOT NULL,
		PRIMARY KEY (user_id, name),
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`)
	return err
}

func migrateTeams(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS teams (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			UNIQUE KEY teams_name (name)
		)`,
		`CREATE TABLE IF NOT EXISTS team_members (
			team_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			PRIMARY KEY (team_id, user_id),
			KEY team_members_user (user_id),
			FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return addColumn(db, "users", "team_manager", "BOOLEAN NOT NULL DEFAULT FALSE AFTER admin")
}

func migrateOwners(db *sql.DB) error {
	for _, c := range []struct{ table, name, def string }{
		{"flows", "owner_user_id", "BIGINT NULL"},
		{"flows", "owner_team_id", "BIGINT NULL"},
		// Whose schedule it is: its runs use their model keys and MCP
		// connections; see runUser.
		{"schedules", "created_by", "BIGINT NULL"},
	} {
		if err := addColumn(db, c.table, c.name, c.def); err != nil {
			return err
		}
	}
	// A team or someone that owns flows can't go without them going elsewhere
	// first; see teams.go and user_roles.go.
	for _, c := range []struct{ name, def string }{
		{"flows_owner_user", "FOREIGN KEY (owner_user_id) REFERENCES users(id)"},
		{"flows_owner_team", "FOREIGN KEY (owner_team_id) REFERENCES teams(id)"},
	} {
		ok, err := hasConstraint(db, "flows", c.name)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := db.Exec("ALTER TABLE flows ADD CONSTRAINT " + c.name + " " + c.def); err != nil {
				return err
			}
		}
	}
	return nil
}

func migrateModelKeys(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS model_keys (
		env VARCHAR(255) NOT NULL PRIMARY KEY,
		api_key TEXT NOT NULL,
		updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
	)`); err != nil {
		return err
	}
	// People once kept keys of their own beside the instance's, which were
	// the user 0's; only the instance's stay.
	personal, err := hasColumn(db, "model_keys", "user_id")
	if err != nil {
		return err
	}
	if personal {
		for _, q := range []string{
			"DELETE FROM model_keys WHERE user_id <> 0",
			"ALTER TABLE model_keys DROP PRIMARY KEY, DROP COLUMN user_id, ADD PRIMARY KEY (provider)",
		} {
			if _, err := db.Exec(q); err != nil {
				return err
			}
		}
	}
	// Keys were once kept by provider, from a list of the ones pi knows;
	// they are kept by the variable it reads them from now.
	byProvider, err := hasColumn(db, "model_keys", "provider")
	if err != nil || !byProvider {
		return err
	}
	for provider, env := range map[string]string{
		"anthropic": "ANTHROPIC_API_KEY", "openai": "OPENAI_API_KEY", "google": "GEMINI_API_KEY",
		"openrouter": "OPENROUTER_API_KEY", "mistral": "MISTRAL_API_KEY", "groq": "GROQ_API_KEY",
		"xai": "XAI_API_KEY", "cerebras": "CEREBRAS_API_KEY", "zai": "ZAI_API_KEY",
	} {
		if _, err := db.Exec("UPDATE model_keys SET provider = ? WHERE provider = ?", env, provider); err != nil {
			return err
		}
	}
	_, err = db.Exec("ALTER TABLE model_keys CHANGE provider env VARCHAR(255) NOT NULL")
	return err
}

func migrateModelProviders(db *sql.DB) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS model_providers (
			id VARCHAR(64) NOT NULL PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			api VARCHAR(64) NOT NULL DEFAULT '',
			base_url TEXT NOT NULL,
			key_source VARCHAR(8) NOT NULL,
			env VARCHAR(255) NOT NULL,
			api_key TEXT NOT NULL,
			models JSON NOT NULL,
			created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
			updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
		)`,
		`CREATE TABLE IF NOT EXISTS model_default (
			id TINYINT NOT NULL PRIMARY KEY,
			model VARCHAR(512) NOT NULL
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func migrateAPIKeys(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT NULL,
		name VARCHAR(255) NOT NULL,
		prefix VARCHAR(16) NOT NULL,
		token_hash CHAR(64) NOT NULL,
		created_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
		last_used_at TIMESTAMP(3) NULL,
		UNIQUE KEY api_keys_token (token_hash),
		FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
	)`); err != nil {
		return err
	}
	// The key that started a run, and what its caller called the request,
	// so a request sent again starts no second run.
	for _, c := range []struct{ name, def string }{
		{"api_key_id", "BIGINT NULL"},
		{"idempotency_key", "VARCHAR(255) NULL"},
	} {
		if err := addColumn(db, "runs", c.name, c.def); err != nil {
			return err
		}
	}
	ok, err := hasIndex(db, "runs", "runs_idempotency")
	if err != nil || ok {
		return err
	}
	_, err = db.Exec("CREATE UNIQUE INDEX runs_idempotency ON runs (api_key_id, idempotency_key)")
	return err
}

func migrateSkills(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS skills (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		kind VARCHAR(16) NOT NULL,
		name VARCHAR(64) NOT NULL,
		description TEXT NOT NULL,
		argument_hint VARCHAR(255) NOT NULL DEFAULT '',
		content MEDIUMTEXT NOT NULL,
		owner_user_id BIGINT NULL,
		owner_team_id BIGINT NULL,
		updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
		UNIQUE KEY skills_name (kind, name),
		FOREIGN KEY (owner_user_id) REFERENCES users(id),
		FOREIGN KEY (owner_team_id) REFERENCES teams(id)
	)`)
	return err
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

func hasColumn(db *sql.DB, table, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, name).Scan(&n)
	return n > 0, err
}

func hasTable(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, name).Scan(&n)
	return n > 0, err
}

func hasConstraint(db *sql.DB, table, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = ?`, table, name).Scan(&n)
	return n > 0, err
}
