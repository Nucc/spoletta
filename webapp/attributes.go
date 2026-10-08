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

		res, err := db.ExecContext(r.Context(), "INSERT INTO object_attributes (object_id, name, definition) VALUES (?, ?, ?)",
			a.Object, a.Name, a.Definition)
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
		if _, err := db.ExecContext(r.Context(), "DELETE FROM object_attributes WHERE id = ?", id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// The attributes of every object, or of one if object isn't 0, by object id.
func listAttributes(ctx context.Context, db *sql.DB, object int64) (map[int64][]attribute, error) {
	rows, err := db.QueryContext(ctx,
		"SELECT id, object_id, name, definition, is_unique, is_file FROM object_attributes WHERE ? = 0 OR object_id = ? ORDER BY id", object, object)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attrs := map[int64][]attribute{}
	for rows.Next() {
		var a attribute
		if err := rows.Scan(&a.ID, &a.Object, &a.Name, &a.Definition, &a.Unique, &a.File); err != nil {
			return nil, err
		}
		attrs[a.Object] = append(attrs[a.Object], a)
	}
	return attrs, rows.Err()
}

// The interface of what a process generates, which its output always
// follows: a JSON object with the attributes of the object it generates, or
// {"data": ...} when the object has none; for a list of items a JSON array
// of such objects. When it generates several objects, the output holds one
// such JSON object or array per generated object, under the object's name.
type outputContract struct {
	shaped []shapedObject
	// The step's files directory, which file attributes must point inside;
	// empty to leave them unchecked.
	filesDir string
}

type shapedObject struct {
	id         int64
	name       string
	many       bool
	attributes []attribute
	// The object has no attributes, so it holds its value under data.
	data bool
}

// The attribute an object without attributes holds its value in.
const dataAttribute = "data"

// The contract for what a process generates, or nil if it generates
// nothing. With all false, only objects with attributes are in it, and nil
// means none has any. Says what is wrong if the objects can't be told apart.
func contractFor(ctx context.Context, db *sql.DB, process int64, all bool) (*outputContract, string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT o.id, o.name, o.many AND o.parent_id <=> p.parent_id
		FROM object_links l JOIN objects o ON o.id = l.object_id JOIN processes p ON p.id = l.process_id
		WHERE l.process_id = ? AND l.direction = 'output' ORDER BY o.id`, process)
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
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	attrs, err := listAttributes(ctx, db, 0)
	if err != nil {
		return nil, "", err
	}
	c := &outputContract{}
	names := map[string]bool{}
	for _, o := range objects {
		if o.attributes = attrs[o.id]; len(o.attributes) == 0 {
			if !all {
				continue
			}
			o.data = true
			o.attributes = []attribute{{Name: dataAttribute, Definition: fmt.Sprintf("What %s holds, as any JSON value", quote(o.name))}}
		}
		if names[o.name] {
			return nil, fmt.Sprintf("the process generates two objects named %q; rename one so its output can tell them apart", o.name), nil
		}
		names[o.name] = true
		c.shaped = append(c.shaped, o)
	}
	if len(c.shaped) == 0 {
		return nil, "", nil
	}
	return c, "", nil
}

// Where a step keeps the files it generates, in the volume the worker
// writes them to.
func stepFilesDir(run, step int64) string {
	return filepath.Join(filesRoot, "runs", strconv.FormatInt(run, 10), "steps", strconv.FormatInt(step, 10))
}

// The contract as the worker's submit_output tool takes it.
type outputSpec struct {
	Shape   string       `json:"shape"`
	Objects []specObject `json:"objects"`
}

type specObject struct {
	Name       string          `json:"name"`
	Many       bool            `json:"many"`
	Attributes []specAttribute `json:"attributes"`
}

type specAttribute struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        bool   `json:"file"`
}

// Tool arguments must be a JSON object, so a single list goes under items.
func (c *outputContract) spec() *outputSpec {
	s := &outputSpec{Shape: "objects"}
	if len(c.shaped) == 1 {
		s.Shape = "object"
		if c.shaped[0].many {
			s.Shape = "items"
		}
	}
	for _, o := range c.shaped {
		so := specObject{Name: o.name, Many: o.many}
		for _, a := range o.attributes {
			desc := strings.TrimSpace(a.Definition)
			if a.File {
				desc = strings.TrimSpace("The path of a file you wrote in " + c.filesDir + ". " + desc)
			}
			so.Attributes = append(so.Attributes, specAttribute{Name: a.Name, Description: desc, File: a.File})
		}
		s.Objects = append(s.Objects, so)
	}
	return s
}

func (c *outputContract) hasFiles() bool {
	for _, o := range c.shaped {
		for _, a := range o.attributes {
			if a.File {
				return true
			}
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
	switch {
	case len(c.shaped) > 1:
		b.WriteString("Its arguments hold one key per object below, each a JSON object with exactly that object's keys, or for a list an array of such objects, one per item.\n")
	case c.shaped[0].many:
		b.WriteString("Its argument items is an array with one JSON object per item, each with exactly the keys below; an empty array if there are no items.\n")
	default:
		b.WriteString("Its arguments are exactly the keys below.\n")
	}
	for _, o := range c.shaped {
		switch {
		case o.many:
			fmt.Fprintf(&b, "\n%q, a list of items each with:\n", o.name)
		default:
			fmt.Fprintf(&b, "\n%q:\n", o.name)
		}
		writeAttributes(&b, o.attributes, "  ")
	}
	if c.hasFiles() {
		fmt.Fprintf(&b, "\nWrite the files you generate in %s, and give their paths.\n", c.filesDir)
	}
	b.WriteString("Keys not listed are not allowed. Each value can be any JSON value that fits its definition.\n</output_format>\n")
	return b.String()
}

func writeAttributes(b *strings.Builder, attrs []attribute, indent string) {
	for _, a := range attrs {
		def := strings.TrimSpace(a.Definition)
		if a.File {
			def = strings.TrimSpace("the path of a file you wrote. " + def)
		}
		if def == "" {
			fmt.Fprintf(b, "%s- %s\n", indent, quote(a.Name))
		} else {
			fmt.Fprintf(b, "%s- %s: %s\n", indent, quote(a.Name), strings.ReplaceAll(def, "\n", "\n"+indent+"  "))
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
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

// The object's part of a reply, checked and with its attributes in order:
// a JSON object, or for a list a JSON array of them.
func (c *outputContract) shapeObject(o shapedObject, s string) (string, error) {
	if !o.many {
		fields, err := jsonObject(s)
		if err != nil {
			return "", err
		}
		return c.shape(fields, o.attributes)
	}
	items, err := jsonArray(s)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(items))
	for i, item := range items {
		fields, err := jsonObject(string(item))
		if err != nil {
			return "", fmt.Errorf("item %d: %w", i+1, err)
		}
		if parts[i], err = c.shape(fields, o.attributes); err != nil {
			return "", fmt.Errorf("item %d: %w", i+1, err)
		}
	}
	return indentJSON("[" + strings.Join(parts, ",") + "]"), nil
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

// The object's attributes as indented JSON, in their order.
func (c *outputContract) shape(fields map[string]json.RawMessage, attrs []attribute) (string, error) {
	keys := make([]string, len(attrs))
	for i, a := range attrs {
		keys[i] = a.Name
	}
	if err := exactKeys(fields, keys); err != nil {
		return "", err
	}
	// A file's path is kept whole, so it means the same to every process.
	for _, a := range attrs {
		if a.File {
			path, err := c.checkFile(fields[a.Name])
			if err != nil {
				return "", fmt.Errorf("%s: %w", quote(a.Name), err)
			}
			fields[a.Name] = json.RawMessage(quote(path))
		}
	}
	values := make([]json.RawMessage, len(keys))
	for i, k := range keys {
		values[i] = fields[k]
	}
	return ordered(keys, values), nil
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
