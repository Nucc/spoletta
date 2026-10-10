package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
)

// A module is a flow made to be reused: other flows run it through subflow
// processes, and only modules are offered to them. Other flows are the jobs
// that actually run, such as fixing exceptions. A flow a subflow process
// runs becomes a module, and stays one while any does.
//
// The outcomes a subflow process brings onto its board are copies of the
// module's objects (see calls.go), and each copy keeps the object it was
// copied from as its source, as do its states, attributes and the objects
// making it up. A copy follows its source: whenever the module's objects
// change, or its board goes back to a version, every copy of them is made
// over to match, and a copy's own states, attributes and parts can't be
// changed but in the module. Only where it goes and what this flow does with
// it, such as storing it, are the copy's own. A copy whose source is deleted
// becomes an object of its own.

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

func hasColumn(db *sql.DB, table, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, name).Scan(&n)
	return n > 0, err
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

// Whether an object has the states, attributes and parts of the one it
// would be a copy of, by name, and so do its parts.
func copiedAsIs(ctx context.Context, tx *sql.Tx, s structure, source, dup int64) (bool, error) {
	wantStates, err := sourcedRows(ctx, tx, "SELECT id, name, NULL FROM object_states WHERE object_id = ? ORDER BY id", source)
	if err != nil {
		return false, err
	}
	haveStates, err := sourcedRows(ctx, tx, "SELECT id, name, source_id FROM object_states WHERE object_id = ? ORDER BY id", dup)
	if err != nil {
		return false, err
	}
	haveAttrs, err := sourcedRows(ctx, tx, "SELECT id, name, source_id FROM object_attributes WHERE object_id = ? ORDER BY id", dup)
	if err != nil {
		return false, err
	}
	haveParts, err := sourcedRows(ctx, tx, "SELECT id, name, source_id FROM objects WHERE owner_id = ? ORDER BY id", dup)
	if err != nil {
		return false, err
	}
	var wantAttrs, wantParts []sourcedRow
	for _, a := range s.attributes(source) {
		wantAttrs = append(wantAttrs, sourcedRow{id: a.ID, name: a.Name})
	}
	for _, p := range partsOf(s, source) {
		wantParts = append(wantParts, sourcedRow{id: p.id, name: p.name})
	}
	if !sameNames(wantStates, haveStates) || !sameNames(wantAttrs, haveAttrs) || !sameNames(wantParts, haveParts) {
		return false, nil
	}
	pairs, _ := matchRows(wantParts, haveParts)
	for _, w := range wantParts {
		same, err := copiedAsIs(ctx, tx, s, w.id, pairs[w.id])
		if err != nil || !same {
			return false, err
		}
	}
	return true, nil
}

// Makes an object a copy of source, its states, attributes and parts copies
// of theirs by name, changing nothing else.
func adoptCopy(ctx context.Context, tx *sql.Tx, s structure, source, dup int64) error {
	if _, err := tx.ExecContext(ctx, "UPDATE objects SET source_id = ? WHERE id = ?", source, dup); err != nil {
		return err
	}
	var wantAttrs, wantParts []sourcedRow
	for _, a := range s.attributes(source) {
		wantAttrs = append(wantAttrs, sourcedRow{id: a.ID, name: a.Name})
	}
	parts := partsOf(s, source)
	for _, p := range parts {
		wantParts = append(wantParts, sourcedRow{id: p.id, name: p.name})
	}
	wantStates, err := sourcedRows(ctx, tx, "SELECT id, name, NULL FROM object_states WHERE object_id = ? ORDER BY id", source)
	if err != nil {
		return err
	}
	for _, t := range []struct {
		table, query string
		want         []sourcedRow
	}{
		{"object_states", "SELECT id, name, source_id FROM object_states WHERE object_id = ? ORDER BY id", wantStates},
		{"object_attributes", "SELECT id, name, source_id FROM object_attributes WHERE object_id = ? ORDER BY id", wantAttrs},
	} {
		have, err := sourcedRows(ctx, tx, t.query, dup)
		if err != nil {
			return err
		}
		pairs, _ := matchRows(t.want, have)
		for from, here := range pairs {
			if _, err := tx.ExecContext(ctx, "UPDATE "+t.table+" SET source_id = ? WHERE id = ?", from, here); err != nil {
				return err
			}
		}
	}
	have, err := sourcedRows(ctx, tx, "SELECT id, name, source_id FROM objects WHERE owner_id = ? ORDER BY id", dup)
	if err != nil {
		return err
	}
	pairs, _ := matchRows(wantParts, have)
	for _, p := range parts {
		if here := pairs[p.id]; here != 0 {
			if err := adoptCopy(ctx, tx, s, p.id, here); err != nil {
				return err
			}
		}
	}
	return nil
}

func sameNames(a, b []sourcedRow) bool {
	names := func(rs []sourcedRow) []string {
		var ns []string
		for _, r := range rs {
			ns = append(ns, r.name)
		}
		slices.Sort(ns)
		return ns
	}
	return slices.Equal(names(a), names(b))
}

// Makes a flow a module, once a subflow process runs it.
func markModule(ctx context.Context, tx *sql.Tx, flow int64) error {
	_, err := tx.ExecContext(ctx, "UPDATE flows SET module = TRUE WHERE id = ?", flow)
	return err
}

// How many other flows run a flow through their subflow processes.
func usersOf(ctx context.Context, db querier, flow int64) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT flow_id) FROM processes
		WHERE kind = ? AND CAST(JSON_EXTRACT(settings, '$.flow') AS SIGNED) = ? AND flow_id <> ?`, kindSubflow, flow, flow).Scan(&n)
	return n, err
}

// Why an object can't be changed but where it comes from: it is a copy of a
// module's or a struct's, whose name the reason gives. Empty for an object of
// its own.
func copyReason(ctx context.Context, db querier, object int64) (string, error) {
	var name, module string
	var isStruct bool
	err := db.QueryRowContext(ctx, `SELECT src.name, f.name, f.is_struct FROM objects o
		JOIN objects src ON src.id = o.source_id JOIN flows f ON f.id = src.flow_id WHERE o.id = ?`, object).Scan(&name, &module, &isStruct)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	from := "procedure"
	if isStruct {
		from = "struct"
	}
	return quote(name) + " comes from the " + from + " " + quote(module) + "; change it there, and it changes here too", nil
}

// The flow an object is in, 0 if it is gone.
func flowOfObject(ctx context.Context, db querier, object int64) (int64, error) {
	return idBy(ctx, db, "SELECT flow_id FROM objects WHERE id = ?", object)
}

// The id a query finds by another, 0 for none.
func idBy(ctx context.Context, db querier, query string, id int64) (int64, error) {
	var f int64
	err := db.QueryRowContext(ctx, query, id).Scan(&f)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return f, err
}

// Carries a change to a flow's objects over to wherever they are copied,
// and makes the flow's own copies match their sources; see syncCopies.
func propagate(ctx context.Context, db *sql.DB, flow int64, outcomes bool) error {
	if flow == 0 {
		return nil
	}
	return inTx(ctx, db, func(tx *sql.Tx) error { return syncCopies(ctx, tx, flow, outcomes) })
}

// Makes every copy of the flow's objects, and every copy on its boards,
// match its source again. With outcomes, the subflow processes running the
// flow bring in its outcomes too, for those it gained. A module the copies
// are in passes the change on to the copies of them in turn, as a ticket
// copied into a module, which a flow runs, is copied into that flow.
func syncCopies(ctx context.Context, tx *sql.Tx, flow int64, outcomes bool) error {
	return syncCopiesFrom(ctx, tx, flow, outcomes, map[int64]bool{})
}

func syncCopiesFrom(ctx context.Context, tx *sql.Tx, flow int64, outcomes bool, seen map[int64]bool) error {
	seen[flow] = true
	if outcomes {
		var callers []int64
		err := scanAll(ctx, tx, `SELECT id FROM processes WHERE kind = ? AND CAST(JSON_EXTRACT(settings, '$.flow') AS SIGNED) = ? ORDER BY id`,
			func(rows *sql.Rows) error {
				var p int64
				err := rows.Scan(&p)
				callers = append(callers, p)
				return err
			}, kindSubflow, flow)
		if err != nil {
			return err
		}
		for _, p := range callers {
			if err := bringOutcomes(ctx, tx, p); err != nil {
				return err
			}
		}
	}
	type pair struct{ copy, source, module, in int64 }
	var pairs []pair
	err := scanAll(ctx, tx, `SELECT o.id, src.id, src.flow_id, o.flow_id FROM objects o JOIN objects src ON src.id = o.source_id
		WHERE o.owner_id IS NULL AND (src.flow_id = ? OR o.flow_id = ?) ORDER BY o.id`, func(rows *sql.Rows) error {
		var p pair
		err := rows.Scan(&p.copy, &p.source, &p.module, &p.in)
		pairs = append(pairs, p)
		return err
	}, flow, flow)
	if err != nil {
		return err
	}
	modules := map[int64]structure{}
	for _, p := range pairs {
		s := modules[p.module]
		if s == nil {
			if s, err = loadStructure(ctx, tx, p.module); err != nil {
				return err
			}
			modules[p.module] = s
		}
		if err := syncCopy(ctx, tx, s, p.source, p.copy); err != nil {
			return err
		}
	}
	for _, p := range pairs {
		if !seen[p.in] {
			if err := syncCopiesFrom(ctx, tx, p.in, false, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// A row copied from another, by its id, its name and its source's id.
type sourcedRow struct {
	id     int64
	name   string
	source *int64
}

func sourcedRows(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]sourcedRow, error) {
	var rs []sourcedRow
	err := scanAll(ctx, tx, query, func(rows *sql.Rows) error {
		var r sourcedRow
		err := rows.Scan(&r.id, &r.name, &r.source)
		rs = append(rs, r)
		return err
	}, args...)
	return rs, err
}

// Pairs each wanted row with the row there is copied from it, or else with
// one of the same name copied from none of them, and returns the pairs, by
// wanted id, and the rows there are left over.
func matchRows(want, have []sourcedRow) (map[int64]int64, []int64) {
	wanted := map[int64]bool{}
	for _, w := range want {
		wanted[w.id] = true
	}
	pairs := map[int64]int64{}
	taken := map[int64]bool{}
	for _, h := range have {
		if h.source != nil && wanted[*h.source] && pairs[*h.source] == 0 {
			pairs[*h.source] = h.id
			taken[h.id] = true
		}
	}
	for _, w := range want {
		if pairs[w.id] != 0 {
			continue
		}
		for _, h := range have {
			if !taken[h.id] && h.name == w.name && (h.source == nil || !wanted[*h.source]) {
				pairs[w.id] = h.id
				taken[h.id] = true
				break
			}
		}
	}
	var left []int64
	for _, h := range have {
		if !taken[h.id] {
			left = append(left, h.id)
		}
	}
	return pairs, left
}

// The objects making up an object as copied: what it owns and what its
// generals own, generals' first, since the copy has no general.
func partsOf(s structure, object int64) []*structObject {
	var chain []int64
	for g, seen := s[object], map[int64]bool{}; g != nil && !seen[g.id]; {
		seen[g.id] = true
		chain = append([]int64{g.id}, chain...)
		if g.general == nil {
			break
		}
		g = s[*g.general]
	}
	var parts []*structObject
	for _, g := range chain {
		parts = append(parts, s.owned(g)...)
	}
	return parts
}

// Makes a copy match its source in the module whose structure s is: its
// name and how many it is, its states, the attributes it has (its generals'
// too), and the objects making it up, each matched in turn. What the copy
// has that its source no longer does goes, the links naming its states too.
func syncCopy(ctx context.Context, tx *sql.Tx, s structure, source, dup int64) error {
	o := s[source]
	if o == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, "UPDATE objects SET name = ?, many = ?, min_items = ?, max_items = ?, relation = ?, source_id = ? WHERE id = ?",
		o.name, o.many, o.quantity.min, o.quantity.max, o.relation, source, dup)
	if err != nil {
		return err
	}

	want, err := sourcedRows(ctx, tx, "SELECT id, name, NULL FROM object_states WHERE object_id = ? ORDER BY id", source)
	if err != nil {
		return err
	}
	have, err := sourcedRows(ctx, tx, "SELECT id, name, source_id FROM object_states WHERE object_id = ? ORDER BY id", dup)
	if err != nil {
		return err
	}
	pairs, left := matchRows(want, have)
	for _, st := range left {
		if _, err := tx.ExecContext(ctx, "DELETE FROM object_links WHERE state_id = ?", st); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM object_states WHERE id = ?", st); err != nil {
			return err
		}
	}
	for _, w := range want {
		if here := pairs[w.id]; here != 0 {
			_, err = tx.ExecContext(ctx, "UPDATE object_states SET name = ?, source_id = ? WHERE id = ?", w.name, w.id, here)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO object_states (object_id, name, source_id) VALUES (?, ?, ?)", dup, w.name, w.id)
		}
		if err != nil {
			return err
		}
	}
	// A copy's states keep the rules of theirs.
	if _, err := tx.ExecContext(ctx,
		"UPDATE object_states d JOIN object_states s ON s.id = d.source_id SET d.rule = s.rule WHERE d.object_id = ?", dup); err != nil {
		return err
	}

	attrs := s.attributes(source)
	want = want[:0]
	for _, a := range attrs {
		want = append(want, sourcedRow{id: a.ID, name: a.Name})
	}
	if have, err = sourcedRows(ctx, tx, "SELECT id, name, source_id FROM object_attributes WHERE object_id = ? ORDER BY id", dup); err != nil {
		return err
	}
	pairs, left = matchRows(want, have)
	for _, a := range left {
		if _, err := tx.ExecContext(ctx, "DELETE FROM object_attributes WHERE id = ?", a); err != nil {
			return err
		}
	}
	// Renamed out of the way first, so two swapping names don't collide.
	for _, a := range attrs {
		if here := pairs[a.ID]; here != 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE object_attributes SET name = CONCAT('\x00', id) WHERE id = ?", here); err != nil {
				return err
			}
		}
	}
	for _, a := range attrs {
		if here := pairs[a.ID]; here != 0 {
			_, err = tx.ExecContext(ctx, "UPDATE object_attributes SET name = ?, definition = ?, is_unique = ?, is_file = ?, source_id = ? WHERE id = ?",
				a.Name, a.Definition, a.Unique, a.File, a.ID, here)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO object_attributes (object_id, name, definition, is_unique, is_file, source_id) VALUES (?, ?, ?, ?, ?, ?)",
				dup, a.Name, a.Definition, a.Unique, a.File, a.ID)
		}
		if err != nil {
			return err
		}
	}

	parts := partsOf(s, source)
	want = want[:0]
	for _, p := range parts {
		want = append(want, sourcedRow{id: p.id, name: p.name})
	}
	if have, err = sourcedRows(ctx, tx, "SELECT id, name, source_id FROM objects WHERE owner_id = ? ORDER BY id", dup); err != nil {
		return err
	}
	pairs, left = matchRows(want, have)
	for _, p := range left {
		if _, err := tx.ExecContext(ctx, "DELETE FROM objects WHERE id = ?", p); err != nil {
			return err
		}
	}
	var flow int64
	if err := tx.QueryRowContext(ctx, "SELECT flow_id FROM objects WHERE id = ?", dup).Scan(&flow); err != nil {
		return err
	}
	for _, p := range parts {
		if here := pairs[p.id]; here != 0 {
			err = syncCopy(ctx, tx, s, p.id, here)
		} else {
			_, err = copyStructure(ctx, tx, s, p.id, flow, nil, &dup, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Whether what an object has may change here: not for a copy of a module's
// object, which follows it. Writes why not, or the error, when it may not.
func ownObject(w http.ResponseWriter, r *http.Request, db *sql.DB, object int64) bool {
	msg, err := copyReason(r.Context(), db, object)
	if err != nil {
		serverError(w, err)
		return false
	}
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return false
	}
	return true
}

// The object a state or attribute is of, 0 if it is gone.
func objectOfState(ctx context.Context, db querier, state int64) (int64, error) {
	return idBy(ctx, db, "SELECT object_id FROM object_states WHERE id = ?", state)
}

func objectOfAttribute(ctx context.Context, db querier, attr int64) (int64, error) {
	return idBy(ctx, db, "SELECT object_id FROM object_attributes WHERE id = ?", attr)
}

// Carries a change to an object over to its copies, as propagate does for
// its flow. Writes the error and returns false if that failed.
func propagateFrom(w http.ResponseWriter, r *http.Request, db *sql.DB, object int64, outcomes bool) bool {
	flow, err := flowOfObject(r.Context(), db, object)
	if err == nil {
		err = propagate(r.Context(), db, flow, outcomes)
	}
	if err != nil {
		serverError(w, err)
		return false
	}
	return true
}
