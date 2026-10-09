package main

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// An object is made of more than the attributes it exhibits, as OPM unfolds
// an object into its own diagram: it can exhibit attributes that are objects
// of their own, such as a person's address with its street and city, and
// aggregate parts, such as a car's wheels. Each such object belongs to the
// object it makes up, its owner, and lives on the owner's board rather than
// on a process's; it can be unfolded in turn. An object can also specialize
// another, its general, as a car is a vehicle: it has everything the general
// has, and what it adds.
//
// What an object holds is a JSON object with a key per field: the general's
// fields first, then its own attributes, then the objects it owns, each in
// the order they were added. An owned object's field holds a JSON object of
// its own fields, or for a list an array of them; one without fields holds
// any JSON value.

const (
	relationExhibition  = "exhibition"
	relationAggregation = "aggregation"
)

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

// One object as its structure needs it.
type structObject struct {
	id       int64
	name     string
	many     bool
	quantity quantity
	owner    *int64
	general  *int64
	relation string
	attrs    []attribute
}

// The objects of a flow, by id.
type structure map[int64]*structObject

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadStructure(ctx context.Context, db rowsQuerier, flow int64) (structure, error) {
	s := structure{}
	rows, err := db.QueryContext(ctx, "SELECT id, name, many, min_items, max_items, owner_id, general_id, relation FROM objects WHERE flow_id = ? ORDER BY id", flow)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		o := &structObject{}
		if err := rows.Scan(&o.id, &o.name, &o.many, &o.quantity.min, &o.quantity.max, &o.owner, &o.general, &o.relation); err != nil {
			rows.Close()
			return nil, err
		}
		s[o.id] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT a.id, a.object_id, a.name, a.definition, a.is_unique, a.is_file
		FROM object_attributes a JOIN objects o ON o.id = a.object_id WHERE o.flow_id = ? ORDER BY a.id`, flow)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a attribute
		if err := rows.Scan(&a.ID, &a.Object, &a.Name, &a.Definition, &a.Unique, &a.File); err != nil {
			return nil, err
		}
		if o := s[a.Object]; o != nil {
			o.attrs = append(o.attrs, a)
		}
	}
	return s, rows.Err()
}

// The structure of the flow an object is in.
func structureOf(ctx context.Context, db *sql.DB, object int64) (structure, int64, error) {
	var flow int64
	if err := db.QueryRowContext(ctx, "SELECT flow_id FROM objects WHERE id = ?", object).Scan(&flow); err != nil {
		return nil, 0, err
	}
	s, err := loadStructure(ctx, db, flow)
	return s, flow, err
}

// A key of what an object holds: one of its attributes, or an object it
// owns, with that object's fields.
type field struct {
	name       string
	definition string
	file       bool
	// An owned object: a list of its items, each holding fields, as many
	// as quantity says.
	many     bool
	quantity quantity
	// The owned object's fields; none for an attribute, or an owned object
	// holding any JSON value.
	fields []field
	// The object it is, for an owned object.
	object int64
}

// The objects an object owns, in the order they were added.
func (s structure) owned(id int64) []*structObject {
	var os []*structObject
	for _, o := range s {
		if o.owner != nil && *o.owner == id {
			os = append(os, o)
		}
	}
	slices.SortFunc(os, func(a, b *structObject) int { return int(a.id - b.id) })
	return os
}

// The objects specializing an object, directly.
func (s structure) specializations(id int64) []*structObject {
	var os []*structObject
	for _, o := range s {
		if o.general != nil && *o.general == id {
			os = append(os, o)
		}
	}
	slices.SortFunc(os, func(a, b *structObject) int { return int(a.id - b.id) })
	return os
}

// The fields of what an object holds, or what makes them endless.
func (s structure) fields(id int64) ([]field, error) {
	return s.fieldsOf(id, map[int64]bool{})
}

// Through generals and owned objects, an object can't come back to itself:
// what it holds would never end. Seen holds the objects on the way there.
func (s structure) fieldsOf(id int64, seen map[int64]bool) ([]field, error) {
	o := s[id]
	if o == nil {
		return nil, fmt.Errorf("object %d isn't in the flow", id)
	}
	if seen[id] {
		return nil, fmt.Errorf("%s would hold itself, through what it specializes or what it is made of", quote(o.name))
	}
	seen[id] = true
	defer delete(seen, id)

	var fs []field
	if o.general != nil {
		inherited, err := s.fieldsOf(*o.general, seen)
		if err != nil {
			return nil, err
		}
		fs = append(fs, inherited...)
	}
	for _, a := range o.attrs {
		fs = append(fs, field{name: a.Name, definition: a.Definition, file: a.File})
	}
	for _, c := range s.owned(id) {
		inner, err := s.fieldsOf(c.id, seen)
		if err != nil {
			return nil, err
		}
		fs = append(fs, field{name: c.name, many: c.many, quantity: c.quantity, fields: inner, object: c.id})
	}
	return fs, nil
}

// The leaf attributes an object has, its general's first.
func (s structure) attributes(id int64) []attribute {
	var as []attribute
	for o, seen := s[id], map[int64]bool{}; o != nil && !seen[o.id]; {
		seen[o.id] = true
		as = append(slices.Clone(o.attrs), as...)
		if o.general == nil {
			break
		}
		o = s[*o.general]
	}
	return as
}

// Says what is wrong with the structure, if anything: an object holding
// itself, a general in another flow, or two fields of an object sharing a name.
func (s structure) check() string {
	for _, o := range s {
		if o.general != nil && s[*o.general] == nil {
			return quote(o.name) + " can only specialize an object of its flow"
		}
	}
	ids := make([]int64, 0, len(s))
	for id := range s {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		fs, err := s.fields(id)
		if err != nil {
			return err.Error()
		}
		names := map[string]bool{}
		for _, f := range fs {
			if names[f.name] {
				return fmt.Sprintf("%s would have two fields named %s; each attribute, part and inherited field needs a name of its own", quote(s[id].name), quote(f.name))
			}
			names[f.name] = true
		}
	}
	return ""
}

// Says what is wrong with the flow's structure once change is made to it,
// if anything.
func checkStructure(ctx context.Context, db *sql.DB, flow int64, change func(structure)) (string, error) {
	s, err := loadStructure(ctx, db, flow)
	if err != nil {
		return "", err
	}
	change(s)
	return s.check(), nil
}

// Copies an object of another flow into flow, with what makes it up: its
// states, its attributes and the objects it owns, each copied in turn. What
// it inherits becomes its own, since its general is in the other flow. The
// copy keeps the object as its source, and follows it; see modules.go.
// The copy goes at, or where the object is without it. Returns the copy's id.
func copyStructure(ctx context.Context, tx *sql.Tx, s structure, object, flow int64, parent, owner *int64, at *point) (int64, error) {
	if s[object] == nil {
		return 0, fmt.Errorf("object %d isn't in the flow", object)
	}
	var x, y *float64
	if at != nil {
		x, y = &at.X, &at.Y
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO objects (flow_id, name, x, y, width, parent_id, owner_id, relation, source_id)
		SELECT ?, name, COALESCE(?, x), COALESCE(?, y), width, ?, ?, relation, id FROM objects WHERE id = ?`,
		flow, x, y, parent, owner, object)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, syncCopy(ctx, tx, s, object, id)
}
