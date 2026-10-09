package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
//
// A list can say how many items it holds, as OPM writes a link's
// multiplicity at the object's end: exactly 4, between 2 and 5, at least 1,
// or any number. What a process generates of it, or a person enters, must
// have that many; a list a flow process collects from its items' runs has
// as many as ran.

func migrateItems(db *sql.DB) error {
	if err := addColumn(db, "objects", "min_items", "INT NOT NULL DEFAULT 0 AFTER many"); err != nil {
		return err
	}
	// Null for no most.
	return addColumn(db, "objects", "max_items", "INT NULL AFTER min_items")
}

// How many items a list holds: at least min, and at most max unless it is nil.
type quantity struct {
	min int
	max *int
}

// Says what is wrong with how many items an object holds, if anything. One
// that isn't a list holds one object, so its bounds stay unset; a list
// holding at most one would read as that.
func checkQuantity(many bool, q quantity) string {
	switch {
	case !many && (q.min != 0 || q.max != nil):
		return "only a list of items says how many it holds"
	case q.min < 0:
		return "a list can't hold fewer than 0 items"
	case q.max != nil && *q.max < 2:
		return "a list holding at most one item is no list; make it hold one object instead"
	case q.max != nil && *q.max < q.min:
		return "a list can't hold fewer items at most than at least"
	}
	return ""
}

// The quantity as its links write it: 1, 4, 2..5, 1..* or * for any number.
func (q quantity) text(many bool) string {
	switch {
	case !many:
		return "1"
	case q.max == nil && q.min == 0:
		return "*"
	case q.max == nil:
		return strconv.Itoa(q.min) + "..*"
	case *q.max == q.min:
		return strconv.Itoa(q.min)
	}
	return strconv.Itoa(q.min) + ".." + strconv.Itoa(*q.max)
}

// How many items a list must have, in words, or empty for any number.
func (q quantity) words() string {
	switch {
	case q.max == nil && q.min == 0:
		return ""
	case q.max == nil:
		return "at least " + strconv.Itoa(q.min)
	case *q.max == q.min:
		return "exactly " + strconv.Itoa(q.min)
	case q.min == 0:
		return "at most " + strconv.Itoa(*q.max)
	}
	return "between " + strconv.Itoa(q.min) + " and " + strconv.Itoa(*q.max)
}

// Says what is wrong with a list of n items, if anything.
func (q quantity) check(n int) error {
	if n >= q.min && (q.max == nil || n <= *q.max) {
		return nil
	}
	items := "items"
	if n == 1 {
		items = "item"
	}
	return fmt.Errorf("it has %d %s; it must have %s", n, items, q.words())
}

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
