package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
)

// An object marked many is a list of items, such as the exceptions a source
// found. On its own board it holds a JSON array: an agent generating it
// replies with one, and a flow process taking it runs once per item. Inside
// that flow process, it is the item; what the processes there generate of a
// list is an item of it, and the flow process collects them. A recording
// flow process runs each item as a run of its own, labelled by what tells
// the item apart.
//
// When the list is persistent, each item's run claims its item when it
// starts: it stores the item as its instance of the object, unique values
// included. An item another run already holds, done or still going, is
// skipped, so fetching the same items again processes only new ones; one
// whose run ends otherwise than done is released and runs again next time.

// Whether an object is a list on a board: on its own board, not inside a
// flow process it shows in.
func isList(o snapshotObject, board *int64) bool {
	return o.Many && (o.Parent == nil) == (board == nil) && (o.Parent == nil || *o.Parent == *board)
}

// The most items of one list a flow process runs for at once.
const maxItems = 50

// The items of a list's content, each as indented JSON.
func listItems(content string) ([]string, error) {
	raw, err := jsonArray(stripFence(content))
	if err != nil {
		return nil, err
	}
	items := make([]string, len(raw))
	for i, r := range raw {
		items[i] = indentJSON(string(r))
	}
	return items, nil
}

// An item's content as an element of a list: JSON as it is, other text as a
// JSON string.
func asItem(content string) string {
	if json.Valid([]byte(content)) {
		return content
	}
	return quote(content)
}

// What tells an item apart: the value of the list's first unique attribute
// it has, or else its place in the list.
func itemKey(ctx context.Context, db *sql.DB, list int64, content string, index int) (string, error) {
	fallback := "item " + strconv.Itoa(index+1)
	fields, err := jsonObject(content)
	if err != nil {
		return fallback, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT name FROM object_attributes WHERE object_id = ? AND is_unique ORDER BY id", list)
	if err != nil {
		return fallback, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fallback, err
		}
		if raw, ok := fields[name]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return s, nil
			}
			return compact(raw), nil
		}
	}
	return fallback, rows.Err()
}

// Stores an item as the run's instance of the list, so no other run takes
// it. Returns the claim of the run holding it already, if any; then nothing
// is stored.
func claimItem(ctx context.Context, db *sql.DB, run, stepID, list int64, name, content string) (*duplicate, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	dup, err := storeInstance(ctx, tx, run, stepID, list, name, content, "")
	if err != nil || dup != nil {
		return dup, err
	}
	return nil, tx.Commit()
}
