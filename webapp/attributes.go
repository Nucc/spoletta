package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// An attribute is something an OPM object exhibits, such as a ticket's
// subject. Its definition says what it holds. An agent process generating an
// object with attributes must reply with a JSON object holding exactly those
// attributes, so what it generates has a known shape.
type attribute struct {
	ID         int64  `json:"id"`
	Object     int64  `json:"object"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
	// No two values a persistent object stores share this attribute's value.
	Unique bool `json:"unique"`
	// Holds the path of a file the process wrote in its step's files
	// directory, which must exist.
	File bool `json:"file"`
	// The attribute of a module's object it is a copy of; see modules.go.
	Source *int64 `json:"source,omitempty"`
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

func registerAttributeRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("POST /api/objects/{id}/attributes", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var a attribute
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		a.Object = id
		if a.Name = strings.TrimSpace(a.Name); a.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		if !ownObject(w, r, db, id) {
			return
		}
		if msg, err := checkAttribute(r.Context(), db, id, func(o *structObject) {
			o.attrs = append(o.attrs, attribute{ID: 1 << 62, Object: id, Name: a.Name})
		}); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "object not found", http.StatusBadRequest)
				return
			}
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if a.Unique {
			var owned bool
			if err := db.QueryRowContext(r.Context(), "SELECT owner_id IS NOT NULL FROM objects WHERE id = ?", id).Scan(&owned); err != nil {
				serverError(w, err)
				return
			}
			if owned {
				http.Error(w, "only an object on a board is stored, so only its own attributes can be unique", http.StatusBadRequest)
				return
			}
		}

		res, err := db.ExecContext(r.Context(), "INSERT INTO object_attributes (object_id, name, definition, is_unique, is_file) VALUES (?, ?, ?, ?, ?)",
			a.Object, a.Name, a.Definition, a.Unique, a.File)
		switch {
		case mysqlErrorIs(err, mysqlErrDuplicate):
			http.Error(w, "the object already has an attribute named "+a.Name, http.StatusConflict)
			return
		case mysqlErrorIs(err, mysqlErrForeignKey):
			http.Error(w, "object not found", http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		if a.ID, err = res.LastInsertId(); err != nil {
			serverError(w, err)
			return
		}
		if !propagateFrom(w, r, db, id, false) {
			return
		}
		writeJSON(w, http.StatusCreated, a)
	})

	mux.HandleFunc("PATCH /api/attributes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var patch struct {
			Name       *string `json:"name"`
			Definition *string `json:"definition"`
			Unique     *bool   `json:"unique"`
			File       *bool   `json:"file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if patch.Name != nil {
			if *patch.Name = strings.TrimSpace(*patch.Name); *patch.Name == "" {
				http.Error(w, "name must not be empty", http.StatusBadRequest)
				return
			}
		}
		object, err := objectOfAttribute(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if !ownObject(w, r, db, object) {
			return
		}
		if patch.Name != nil || isTrue(patch.Unique) {
			var object int64
			var owned bool
			err := db.QueryRowContext(r.Context(), "SELECT a.object_id, o.owner_id IS NOT NULL FROM object_attributes a JOIN objects o ON o.id = a.object_id WHERE a.id = ?", id).
				Scan(&object, &owned)
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "attribute not found", http.StatusNotFound)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
			if owned && isTrue(patch.Unique) {
				http.Error(w, "only an object on a board is stored, so only its own attributes can be unique", http.StatusBadRequest)
				return
			}
			if patch.Name != nil {
				msg, err := checkAttribute(r.Context(), db, object, func(o *structObject) {
					for i := range o.attrs {
						if o.attrs[i].ID == id {
							o.attrs[i].Name = *patch.Name
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
		}
		_, err = db.ExecContext(r.Context(),
			"UPDATE object_attributes SET name = COALESCE(?, name), definition = COALESCE(?, definition), is_unique = COALESCE(?, is_unique), is_file = COALESCE(?, is_file) WHERE id = ?",
			patch.Name, patch.Definition, patch.Unique, patch.File, id)
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			http.Error(w, "the object already has an attribute named "+*patch.Name, http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if !propagateFrom(w, r, db, object, false) {
			return
		}

		var a attribute
		err = db.QueryRowContext(r.Context(), "SELECT id, object_id, name, definition, is_unique, is_file FROM object_attributes WHERE id = ?", id).
			Scan(&a.ID, &a.Object, &a.Name, &a.Definition, &a.Unique, &a.File)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "attribute not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, a)
	})

	mux.HandleFunc("DELETE /api/attributes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		object, err := objectOfAttribute(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		if !ownObject(w, r, db, object) {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM object_attributes WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		if !propagateFrom(w, r, db, object, false) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Says what is wrong with the structure of the object's flow once change is
// made to the object, if anything.
func checkAttribute(ctx context.Context, db *sql.DB, object int64, change func(*structObject)) (string, error) {
	s, _, err := structureOf(ctx, db, object)
	if err != nil {
		return "", err
	}
	if o := s[object]; o != nil {
		change(o)
	}
	return s.check(), nil
}

// The attributes of every object, or of one if object isn't 0, by object id.
func listAttributes(ctx context.Context, db *sql.DB, object int64) (map[int64][]attribute, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, object_id, name, definition, is_unique, is_file, source_id FROM object_attributes WHERE ? = 0 OR object_id = ? ORDER BY id", object, object)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attrs := map[int64][]attribute{}
	for rows.Next() {
		var a attribute
		if err := rows.Scan(&a.ID, &a.Object, &a.Name, &a.Definition, &a.Unique, &a.File, &a.Source); err != nil {
			return nil, err
		}
		attrs[a.Object] = append(attrs[a.Object], a)
	}
	return attrs, rows.Err()
}

// The interface of what a process generates, which its output always
// follows: a JSON object with the fields of the object it generates, its
// attributes and the objects it is made of (see structure.go), or
// {"data": ...} when the object has none; for a list of items a JSON array
// of such objects. When it generates several objects, the output holds one
// such JSON object or array per generated object, under the object's name.
//
// An agent can decide which of the states its output link lists an object
// comes out in. Then what it hands in is a JSON object with that output and
// its decisions: for each such object, the state, and instructions for the
// process taking it in that state, if any. An object it takes too it
// reviews, as a human process does: it keeps its content, so the output
// holds no part of it; one it doesn't take it generates in the state it
// decides.
type outputContract struct {
	shaped    []shapedObject
	decisions []decidedObject
	// The step's files directory, which file attributes must point inside;
	// empty to leave them unchecked.
	filesDir string
}

type shapedObject struct {
	id       int64
	name     string
	many     bool
	quantity quantity
	fields   []field
	// The object has no fields, so it holds its value under data.
	data bool
}

// An object a process decides the state of, and the states it decides between.
type decidedObject struct {
	id     int64
	name   string
	states []objectState
	// The process takes it too, so it reviews it rather than generating it.
	review bool
}

// What a process decided of an object: one of its link's states, and its
// instructions for the processes taking the object in that state, if any.
type decided struct {
	state        int64
	instructions string
}

// The attribute an object without attributes holds its value in.
const dataAttribute = "data"

// The keys of what a process deciding states hands in.
const (
	outputKey    = "output"
	decisionsKey = "decisions"
	stateKey     = "state"
	// Instructions for the process taking the object in the state decided.
	instructionsKey = "instructions"
)

// The contract for what a process generates, or nil if it generates
// nothing. With all false, only objects with fields are in it, and nil
// means none has any. Says what is wrong if the objects can't be told apart.
func contractFor(ctx context.Context, db *sql.DB, process int64, all bool) (*outputContract, string, error) {
	decisions, err := decidedObjects(ctx, db, process)
	if err != nil {
		return nil, "", err
	}
	reviewed := map[int64]bool{}
	for _, d := range decisions {
		reviewed[d.id] = d.review
	}
	objects, msg, err := shapedObjects(ctx, db, process,
		`SELECT DISTINCT o.id, o.name, o.many AND o.parent_id <=> p.parent_id
		FROM object_links l JOIN objects o ON o.id = l.object_id JOIN processes p ON p.id = l.process_id
		WHERE l.process_id = ? AND l.direction = 'output' ORDER BY o.id`, process)
	if err != nil || msg != "" {
		return nil, msg, err
	}

	c := &outputContract{decisions: decisions}
	names := map[string]bool{}
	for _, o := range objects {
		if reviewed[o.id] {
			continue
		}
		if o.data {
			if !all {
				continue
			}
			o.fields = []field{{name: dataAttribute, definition: fmt.Sprintf("What %s holds, as any JSON value", quote(o.name))}}
		}
		if names[o.name] {
			return nil, fmt.Sprintf("the process generates two objects named %q; rename one so its output can tell them apart", o.name), nil
		}
		names[o.name] = true
		c.shaped = append(c.shaped, o)
	}
	for _, d := range decisions {
		if d.review && names[d.name] {
			return nil, fmt.Sprintf("the process generates and decides on two objects named %q; rename one so its output can tell them apart", d.name), nil
		}
	}
	if len(c.shaped) == 0 && len(c.decisions) == 0 {
		return nil, "", nil
	}
	return c, "", nil
}

// The objects a process decides the state of, in id order: those its output
// links list states for, which only an agent's do among the processes with
// contracts.
func decidedObjects(ctx context.Context, db *sql.DB, process int64) ([]decidedObject, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT o.id, o.name, st.id, st.name,
			EXISTS (SELECT 1 FROM object_links i WHERE i.process_id = l.process_id AND i.object_id = l.object_id AND i.direction = 'input')
		FROM object_links l JOIN link_states ls ON ls.link_id = l.id
		JOIN object_states st ON st.id = ls.state_id JOIN objects o ON o.id = l.object_id
		WHERE l.process_id = ? AND l.direction = 'output' ORDER BY o.id, st.id`, process)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ds []decidedObject
	for rows.Next() {
		var d decidedObject
		var st objectState
		if err := rows.Scan(&d.id, &d.name, &st.ID, &st.Name, &d.review); err != nil {
			return nil, err
		}
		st.Object = d.id
		if n := len(ds); n > 0 && ds[n-1].id == d.id {
			ds[n-1].states = append(ds[n-1].states, st)
			continue
		}
		d.states = []objectState{st}
		ds = append(ds, d)
	}
	return ds, rows.Err()
}

// The objects query finds, each as id, name and whether a list, with the
// fields of what it holds; the query's objects are all in the process's
// flow. Says what is wrong if their fields never end.
func shapedObjects(ctx context.Context, db *sql.DB, process int64, query string, args ...any) ([]shapedObject, string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var objects []shapedObject
	for rows.Next() {
		var o shapedObject
		if err := rows.Scan(&o.id, &o.name, &o.many); err != nil {
			return nil, "", err
		}
		objects = append(objects, o)
	}
	if err := rows.Err(); err != nil || len(objects) == 0 {
		return nil, "", err
	}
	var flow int64
	if err := db.QueryRowContext(ctx, "SELECT flow_id FROM processes WHERE id = ?", process).Scan(&flow); err != nil {
		return nil, "", err
	}
	s, err := loadStructure(ctx, db, flow)
	if err != nil {
		return nil, "", err
	}
	for i := range objects {
		fs, err := s.fields(objects[i].id)
		if err != nil {
			return nil, err.Error(), nil
		}
		objects[i].fields = fs
		objects[i].data = len(fs) == 0
		// Inside a flow process a list is one item of it.
		if objects[i].many {
			objects[i].quantity = s[objects[i].id].quantity
		}
	}
	return objects, "", nil
}

// Where a step keeps the files it generates, in the volume the worker
// writes them to.
func stepFilesDir(run, step int64) string {
	return filepath.Join(filesRoot, "runs", strconv.FormatInt(run, 10), "steps", strconv.FormatInt(step, 10))
}

// The contract as the worker's submit_output tool takes it. Shape is
// "decisions" when the process only decides on objects it reviews.
type outputSpec struct {
	Shape   string       `json:"shape"`
	Objects []specObject `json:"objects"`
	// The objects the process decides the state of; with any, the output
	// goes under output, next to the decisions.
	Decisions []specDecision `json:"decisions,omitempty"`
}

// An object the agent decides the state of, as one of states.
type specDecision struct {
	Name   string   `json:"name"`
	States []string `json:"states"`
	Review bool     `json:"review"`
}

type specObject struct {
	Name       string          `json:"name"`
	Many       bool            `json:"many"`
	MinItems   int             `json:"minItems,omitempty"`
	MaxItems   *int            `json:"maxItems,omitempty"`
	Attributes []specAttribute `json:"attributes"`
}

// An attribute, or an object the object is made of: a JSON object with
// its own attributes, or for a list an array of them.
type specAttribute struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	File        bool            `json:"file"`
	Many        bool            `json:"many,omitempty"`
	MinItems    int             `json:"minItems,omitempty"`
	MaxItems    *int            `json:"maxItems,omitempty"`
	Attributes  []specAttribute `json:"attributes,omitempty"`
}

// Tool arguments must be a JSON object, so a single list goes under items.
func (c *outputContract) spec() *outputSpec {
	s := &outputSpec{Shape: "objects", Objects: []specObject{}}
	for _, d := range c.decisions {
		s.Decisions = append(s.Decisions, specDecision{Name: d.name, States: d.stateNames(), Review: d.review})
	}
	switch len(c.shaped) {
	case 0:
		s.Shape = "decisions"
	case 1:
		s.Shape = "object"
		if c.shaped[0].many {
			s.Shape = "items"
		}
	}
	for _, o := range c.shaped {
		s.Objects = append(s.Objects, specObject{Name: o.name, Many: o.many, MinItems: o.quantity.min, MaxItems: o.quantity.max, Attributes: c.specFields(o.fields)})
	}
	return s
}

func (d decidedObject) stateNames() []string {
	names := make([]string, len(d.states))
	for i, st := range d.states {
		names[i] = st.Name
	}
	return names
}

func (c *outputContract) specFields(fs []field) []specAttribute {
	var as []specAttribute
	for _, f := range fs {
		desc := strings.TrimSpace(f.definition)
		if f.file {
			desc = strings.TrimSpace("The path of a file you wrote in " + c.filesDir + ". " + desc)
		}
		as = append(as, specAttribute{Name: f.name, Description: desc, File: f.file, Many: f.many, MinItems: f.quantity.min, MaxItems: f.quantity.max, Attributes: c.specFields(f.fields)})
	}
	return as
}

func (c *outputContract) hasFiles() bool {
	for _, o := range c.shaped {
		if hasFile(o.fields) {
			return true
		}
	}
	return false
}

func hasFile(fs []field) bool {
	for _, f := range fs {
		if f.file || hasFile(f.fields) {
			return true
		}
	}
	return false
}

// Appends what the agent hands in, through the submit_output tool, to the
// prompt. It goes last, so it binds a revision asked for in a change request
// too.
func (c *outputContract) withContract(prompt string) string {
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n<output_format>\n")
	b.WriteString("Hand in what you generate by calling the submit_output tool once, as your last action; a reply in text is not your result. ")
	// With decisions, the output is one argument of two.
	decides := len(c.decisions) > 0
	switch {
	case decides && len(c.shaped) == 0:
		b.WriteString("Its argument is exactly decisions.\n")
	case decides:
		fmt.Fprintf(&b, "Its arguments are exactly %s and %s. %s is ", outputKey, decisionsKey, outputKey)
	}
	switch {
	case len(c.shaped) == 0:
	case len(c.shaped) > 1 && decides:
		b.WriteString("a JSON object with one key per object below, each a JSON object with exactly that object's keys, or for a list an array of such objects, one per item.\n")
	case len(c.shaped) > 1:
		b.WriteString("Its arguments hold one key per object below, each a JSON object with exactly that object's keys, or for a list an array of such objects, one per item.\n")
	case c.shaped[0].many && decides:
		b.WriteString("an array with one JSON object per item, each with exactly the keys below")
		if c.shaped[0].quantity.min == 0 {
			b.WriteString("; an empty array if there are no items")
		}
		b.WriteString(".\n")
	case c.shaped[0].many && c.shaped[0].quantity.min == 0:
		b.WriteString("Its argument items is an array with one JSON object per item, each with exactly the keys below; an empty array if there are no items.\n")
	case c.shaped[0].many:
		b.WriteString("Its argument items is an array with one JSON object per item, each with exactly the keys below.\n")
	case decides:
		b.WriteString("a JSON object with exactly the keys below.\n")
	default:
		b.WriteString("Its arguments are exactly the keys below.\n")
	}
	for _, o := range c.shaped {
		switch {
		case o.many:
			fmt.Fprintf(&b, "\n%q, a list of items%s, each with:\n", o.name, itemsCount(o.quantity))
		default:
			fmt.Fprintf(&b, "\n%q:\n", o.name)
		}
		writeFields(&b, o.fields, "  ")
	}
	if decides {
		fmt.Fprintf(&b, "\n%s holds one key per object below, each a JSON object with %s, the state you decide the object is in, which must be one of those listed, and optionally %s, what the process taking it in that state is to do, such as what to change:\n",
			decisionsKey, quote(stateKey), quote(instructionsKey))
		for _, d := range c.decisions {
			fmt.Fprintf(&b, "- %s: one of %s", quote(d.name), strings.Join(d.quotedStates(), ", "))
			if d.review {
				b.WriteString("; you review it as you were given it, so it keeps what it holds")
			}
			b.WriteString("\n")
		}
	}
	if c.hasFiles() {
		fmt.Fprintf(&b, "\nWrite the files you generate in %s, and give their paths.\n", c.filesDir)
	}
	b.WriteString("Keys not listed are not allowed, at any depth. Each value without keys of its own can be any JSON value that fits its definition.\n</output_format>\n")
	return b.String()
}

// Each field, and the keys of a field holding an object, indented below it.
func writeFields(b *strings.Builder, fs []field, indent string) {
	for _, f := range fs {
		def := strings.TrimSpace(f.definition)
		if f.file {
			def = strings.TrimSpace("the path of a file you wrote. " + def)
		}
		switch {
		case len(f.fields) > 0 && f.many:
			def = "a list of JSON objects" + itemsCount(f.quantity) + ", one per item, each with:"
		case len(f.fields) > 0:
			def = "a JSON object with:"
		case f.many:
			def = "a JSON array of any values" + itemsCount(f.quantity)
		}
		if def == "" {
			fmt.Fprintf(b, "%s- %s\n", indent, quote(f.name))
		} else {
			fmt.Fprintf(b, "%s- %s: %s\n", indent, quote(f.name), strings.ReplaceAll(def, "\n", "\n"+indent+"  "))
		}
		writeFields(b, f.fields, indent+"  ")
	}
}

// How many items a list must have, in brackets after a space, or nothing
// for any number.
func itemsCount(q quantity) string {
	if w := q.words(); w != "" {
		return " (" + w + ")"
	}
	return ""
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Checks a reply of a process deciding states against the contract. Returns
// the reply as indented JSON in its order, what each shaped object holds and
// what it decided of each object; or says what is wrong with it. A reply of
// a process deciding nothing is checked by parse.
func (c *outputContract) parseDecisions(reply string) (string, map[int64]string, map[int64]decided, error) {
	if len(c.decisions) == 0 {
		out, contents, err := c.parse(reply)
		return out, contents, nil, err
	}
	fields, err := jsonObject(stripFence(reply))
	if err != nil {
		return "", nil, nil, err
	}
	var keys []string
	if len(c.shaped) > 0 {
		keys = append(keys, outputKey)
	}
	keys = append(keys, decisionsKey)
	if err := exactKeys(fields, keys); err != nil {
		return "", nil, nil, err
	}
	var parts []json.RawMessage
	contents := map[int64]string{}
	if len(c.shaped) > 0 {
		var out string
		if out, contents, err = c.parse(string(fields[outputKey])); err != nil {
			return "", nil, nil, fmt.Errorf("%s: %w", quote(outputKey), err)
		}
		parts = append(parts, json.RawMessage(out))
	}
	ds, err := jsonObject(string(fields[decisionsKey]))
	if err != nil {
		return "", nil, nil, fmt.Errorf("%s: %w", quote(decisionsKey), err)
	}
	names := make([]string, len(c.decisions))
	for i, d := range c.decisions {
		names[i] = d.name
	}
	if err := exactKeys(ds, names); err != nil {
		return "", nil, nil, fmt.Errorf("%s: %w", quote(decisionsKey), err)
	}
	decisions := map[int64]decided{}
	shown := make([]json.RawMessage, len(c.decisions))
	for i, d := range c.decisions {
		got, shape, err := d.parse(ds[d.name])
		if err != nil {
			return "", nil, nil, fmt.Errorf("%s %s: %w", quote(decisionsKey), quote(d.name), err)
		}
		decisions[d.id] = got
		shown[i] = shape
	}
	parts = append(parts, json.RawMessage(ordered(names, shown)))
	return ordered(keys, parts), contents, decisions, nil
}

// One decision: a JSON object with one of the object's states, and maybe
// instructions. Returns it, and it as indented JSON.
func (d decidedObject) parse(v json.RawMessage) (decided, json.RawMessage, error) {
	fields, err := jsonObject(string(v))
	if err != nil {
		return decided{}, nil, err
	}
	keys := []string{stateKey}
	if _, ok := fields[instructionsKey]; ok {
		keys = append(keys, instructionsKey)
	}
	if err := exactKeys(fields, keys); err != nil {
		return decided{}, nil, err
	}
	var name string
	if err := json.Unmarshal(fields[stateKey], &name); err != nil {
		return decided{}, nil, fmt.Errorf("%s must be the name of a state, as a string", quote(stateKey))
	}
	var got decided
	for _, st := range d.states {
		if st.Name == name {
			got.state = st.ID
		}
	}
	if got.state == 0 {
		return decided{}, nil, fmt.Errorf("%s is %s, which isn't one of %s", quote(stateKey), quote(name), strings.Join(d.quotedStates(), ", "))
	}
	if raw, ok := fields[instructionsKey]; ok {
		if err := json.Unmarshal(raw, &got.instructions); err != nil {
			return decided{}, nil, fmt.Errorf("%s must be a string", quote(instructionsKey))
		}
		got.instructions = strings.TrimSpace(got.instructions)
	}
	values := []json.RawMessage{json.RawMessage(quote(name))}
	if len(keys) > 1 {
		values = append(values, json.RawMessage(quote(got.instructions)))
	}
	return got, json.RawMessage(ordered(keys, values)), nil
}

func (d decidedObject) quotedStates() []string {
	states := make([]string, len(d.states))
	for i, st := range d.states {
		states[i] = quote(st.Name)
	}
	return states
}

// Checks a reply against the contract. Returns the reply as indented JSON
// with the attributes in their order, and what each shaped object holds; or
// says what is wrong with it.
func (c *outputContract) parse(reply string) (string, map[int64]string, error) {
	contents := map[int64]string{}
	if len(c.shaped) == 1 {
		o := c.shaped[0]
		out, err := c.shapeObject(o, stripFence(reply))
		if err != nil {
			return "", nil, err
		}
		contents[o.id] = out
		return out, contents, nil
	}
	fields, err := jsonObject(stripFence(reply))
	if err != nil {
		return "", nil, err
	}

	keys := make([]string, len(c.shaped))
	for i, o := range c.shaped {
		keys[i] = o.name
	}
	if err := exactKeys(fields, keys); err != nil {
		return "", nil, err
	}
	parts := make([]json.RawMessage, len(c.shaped))
	for i, o := range c.shaped {
		out, err := c.shapeObject(o, string(fields[o.name]))
		if err != nil {
			return "", nil, fmt.Errorf("%q: %w", o.name, err)
		}
		contents[o.id] = out
		parts[i] = json.RawMessage(out)
	}
	return ordered(keys, parts), contents, nil
}

// The object's part of a reply, checked and with its fields in order: a
// JSON object, or for a list a JSON array of them.
func (c *outputContract) shapeObject(o shapedObject, s string) (string, error) {
	out, err := c.shapeValue(field{name: o.name, many: o.many, quantity: o.quantity, fields: o.fields}, json.RawMessage(s), true)
	return string(out), err
}

// A field's value, checked: for a list a JSON array of its items, each a
// JSON object with its fields in order if it has any. Whole, an object
// without fields is a JSON object too, holding data.
func (c *outputContract) shapeValue(f field, v json.RawMessage, whole bool) (json.RawMessage, error) {
	if !f.many {
		return c.shapeItem(f, v, whole)
	}
	items, err := jsonArray(string(v))
	if err != nil {
		return nil, err
	}
	if err := f.quantity.check(len(items)); err != nil {
		return nil, err
	}
	parts := make([]string, len(items))
	for i, item := range items {
		out, err := c.shapeItem(f, item, whole)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i+1, err)
		}
		parts[i] = string(out)
	}
	return json.RawMessage(indentJSON("[" + strings.Join(parts, ",") + "]")), nil
}

func (c *outputContract) shapeItem(f field, v json.RawMessage, whole bool) (json.RawMessage, error) {
	switch {
	case f.file:
		// A file's path is kept whole, so it means the same to every process.
		path, err := c.checkFile(v)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(quote(path)), nil
	case len(f.fields) > 0 || whole:
		fields, err := jsonObject(string(v))
		if err != nil {
			return nil, err
		}
		out, err := c.shape(fields, f.fields)
		return json.RawMessage(out), err
	default:
		return v, nil
	}
}

func jsonArray(s string) ([]json.RawMessage, error) {
	var items []json.RawMessage
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&items); err != nil || items == nil {
		return nil, errors.New("it is not a JSON array")
	}
	if dec.More() {
		return nil, errors.New("it has text after the JSON array")
	}
	return items, nil
}

func indentJSON(s string) string {
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(s), "", "  "); err != nil {
		return s
	}
	return out.String()
}

// A reply may come wrapped in a Markdown code fence.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}
	s = strings.TrimSuffix(s[3:], "```")
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	}
	return strings.TrimSpace(s)
}

func jsonObject(s string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, errors.New("it is not a JSON object")
	}
	if dec.More() {
		return nil, errors.New("it has text after the JSON object")
	}
	return fields, nil
}

// The fields as indented JSON, in their order, each checked.
func (c *outputContract) shape(values map[string]json.RawMessage, fs []field) (string, error) {
	keys := make([]string, len(fs))
	for i, f := range fs {
		keys[i] = f.name
	}
	if err := exactKeys(values, keys); err != nil {
		return "", err
	}
	out := make([]json.RawMessage, len(fs))
	for i, f := range fs {
		v, err := c.shapeValue(f, values[f.name], false)
		if err != nil {
			return "", fmt.Errorf("%s: %w", quote(f.name), err)
		}
		out[i] = v
	}
	return ordered(keys, out), nil
}

// Checks that a file attribute's value is the path of a file in the step's
// files directory, and returns the path, made absolute if it was relative to
// that directory.
func (c *outputContract) checkFile(value json.RawMessage) (string, error) {
	var path string
	if err := json.Unmarshal(value, &path); err != nil || path == "" {
		return "", errors.New("it must be the path of a file, as a string")
	}
	if c.filesDir == "" {
		return path, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.filesDir, path)
	}
	root, err := filepath.EvalSymlinks(c.filesDir)
	if err != nil {
		return "", fmt.Errorf("%s doesn't exist", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s doesn't exist", path)
	}
	if rel, err := filepath.Rel(root, real); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("%s isn't in %s", path, c.filesDir)
	}
	if info, err := os.Stat(real); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s isn't a file", path)
	}
	return filepath.Clean(path), nil
}

func exactKeys(fields map[string]json.RawMessage, keys []string) error {
	want := map[string]bool{}
	var missing, extra []string
	for _, k := range keys {
		want[k] = true
		if _, ok := fields[k]; !ok {
			missing = append(missing, quote(k))
		}
	}
	for k := range fields {
		if !want[k] {
			extra = append(extra, quote(k))
		}
	}
	slices.Sort(extra)
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "it lacks "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		problems = append(problems, "it has keys not asked for: "+strings.Join(extra, ", "))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// A JSON object with the keys in the given order; json.Marshal would sort them.
func ordered(keys []string, values []json.RawMessage) string {
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(quote(k))
		b.WriteString(":")
		b.Write(values[i])
	}
	b.WriteString("}")
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return b.String()
	}
	return out.String()
}

// Asks again after a reply that broke the contract. Runs don't keep a
// session, so the prompt goes again with the rejected reply and why.
func withRetry(prompt, reply string, err error) string {
	return fmt.Sprintf("%s\n\nYou already replied to this, but your reply did not follow <output_format>: %s. Reply again following it exactly.\n\n<rejected_reply>\n%s\n</rejected_reply>\n",
		prompt, err, reply)
}
