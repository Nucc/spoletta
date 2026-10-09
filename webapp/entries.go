package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
)

// A human process fills in the objects its output links name without listing
// states to decide between, unless it takes them too, as a review does: a
// person carrying it out, linked to it by an agent link, enters what each
// holds, such as which Jira ticket a flow is to process. What they enter follows the same contract as what an agent
// generates: a JSON object with exactly the object's attributes, or for a
// list an array of them; an object without attributes holds the text entered.
// A file can't be entered by hand, since none was written in the step.

// Whether a human process's output link l is to an object it fills in.
const filledIn = `NOT EXISTS (SELECT 1 FROM link_states ls WHERE ls.link_id = l.id)
	AND NOT EXISTS (SELECT 1 FROM object_links i
		WHERE i.process_id = l.process_id AND i.object_id = l.object_id AND i.direction = 'input')`

// The objects a human process fills in, in id order. Says what is wrong if
// what they hold never ends.
func entryObjects(ctx context.Context, db *sql.DB, process int64) ([]shapedObject, string, error) {
	return shapedObjects(ctx, db, process,
		`SELECT DISTINCT o.id, o.name, o.many AND o.parent_id <=> p.parent_id
		FROM object_links l JOIN objects o ON o.id = l.object_id JOIN processes p ON p.id = l.process_id
		WHERE l.process_id = ? AND l.direction = 'output' AND p.kind = ?
			AND `+filledIn+`
		ORDER BY o.id`, process, kindHuman)
}

// The name of the first field holding a file, at any depth; empty if none does.
func fileField(fs []field) string {
	for _, f := range fs {
		if f.file {
			return f.name
		}
		if inner := fileField(f.fields); inner != "" {
			return inner
		}
	}
	return ""
}

// Checks what a person entered, by object id, against the objects their
// process fills in. Returns it as the step's output, with the attributes in
// their order, and what each object holds; or says what is wrong with it.
func checkEntries(objects []shapedObject, entries map[string]json.RawMessage) (string, map[int64]string, error) {
	keys := make([]string, len(objects))
	for i, o := range objects {
		keys[i] = strconv.FormatInt(o.id, 10)
	}
	if err := exactKeys(entries, keys); err != nil {
		return "", nil, fmt.Errorf("the objects entered don't match the process's: %w", err)
	}
	c := &outputContract{}
	contents := map[int64]string{}
	names := make([]string, len(objects))
	parts := make([]json.RawMessage, len(objects))
	for i, o := range objects {
		names[i] = o.name
		entry := entries[keys[i]]
		if o.data {
			var text string
			if err := json.Unmarshal(entry, &text); err != nil {
				return "", nil, fmt.Errorf("%s: it must be text", quote(o.name))
			}
			contents[o.id] = text
			parts[i] = json.RawMessage(quote(text))
			continue
		}
		if f := fileField(o.fields); f != "" {
			return "", nil, fmt.Errorf("%s can't be filled in by hand: %s holds a file", quote(o.name), quote(f))
		}
		out, err := c.shapeObject(o, string(entry))
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", quote(o.name), err)
		}
		contents[o.id] = out
		parts[i] = json.RawMessage(out)
	}
	if len(objects) == 1 {
		return contents[objects[0].id], contents, nil
	}
	return ordered(names, parts), contents, nil
}
