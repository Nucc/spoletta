package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// A flow's data is what its runs held of each object, a run per row, so a
// person can see at a glance what the flow processed, such as the exceptions
// it fixed. A run holds the latest content and state its steps gave an
// object: a revision after a review replaces the first draft, and a
// person's decision the state. The run of one item of a list, or any run
// of a recording flow process, holds too what it was given, the item
// among it, unless its own steps generated that object again.
//
// The board says what attributes its objects have now; older runs may hold
// others, and objects deleted since, so the table's columns come from what
// the runs held, in the board's order where it has them.

// What a run held of an object.
type dataCell struct {
	// Kept after the object is deleted; null for an object deleted before
	// ids were kept, or a given object the flow has no object named as.
	Object  *int64 `json:"object"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Content string `json:"content"`
	// Given to the run rather than generated in it.
	Given bool `json:"given"`
}

type dataRow struct {
	run
	Cells []dataCell `json:"cells"`
}

// An object on the flow's boards and its attributes in the order they were
// added, which its content's fields follow.
type dataObject struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Many       bool     `json:"many"`
	Attributes []string `json:"attributes"`
}

// As many runs as the runs list shows.
const dataRows = 200

func flowData(ctx context.Context, db *sql.DB, flow int64) ([]dataObject, []dataRow, error) {
	objects := []dataObject{}
	byID := map[int64]int{}
	// Fields and parts an object is made of are in its content already.
	err := scanAll(ctx, db, "SELECT id, name, many FROM objects WHERE flow_id = ? AND owner_id IS NULL ORDER BY id",
		func(rows *sql.Rows) error {
			o := dataObject{Attributes: []string{}}
			if err := rows.Scan(&o.ID, &o.Name, &o.Many); err != nil {
				return err
			}
			byID[o.ID] = len(objects)
			objects = append(objects, o)
			return nil
		}, flow)
	if err != nil {
		return nil, nil, err
	}
	err = scanAll(ctx, db,
		`SELECT a.object_id, a.name FROM object_attributes a JOIN objects o ON o.id = a.object_id
		WHERE o.flow_id = ? ORDER BY a.id`,
		func(rows *sql.Rows) error {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			if i, ok := byID[id]; ok {
				objects[i].Attributes = append(objects[i].Attributes, name)
			}
			return nil
		}, flow)
	if err != nil {
		return nil, nil, err
	}

	rows := []dataRow{}
	at := map[int64]int{}
	err = scanAll(ctx, db, "SELECT "+runColumns+" FROM runs WHERE flow_id = ? ORDER BY id DESC LIMIT ?",
		func(r *sql.Rows) error {
			var row dataRow
			if err := scanRun(r, &row.run); err != nil {
				return err
			}
			row.Cells = []dataCell{}
			at[row.ID] = len(rows)
			rows = append(rows, row)
			return nil
		}, flow, dataRows)
	if err != nil || len(rows) == 0 {
		return objects, rows, err
	}
	ids := make([]any, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}

	// The latest of each object in each run; rows come oldest first, so a
	// later one replaces what came before it.
	type key struct {
		run    int64
		object string
	}
	latest := map[key]int{}
	keyOf := func(object *int64, name string) string {
		if object != nil {
			return "#" + strconv.FormatInt(*object, 10)
		}
		return name
	}
	put := func(run int64, c dataCell) {
		row := &rows[at[run]]
		k := key{run, keyOf(c.Object, c.Name)}
		if i, ok := latest[k]; ok {
			row.Cells[i] = c
			return
		}
		latest[k] = len(row.Cells)
		row.Cells = append(row.Cells, c)
	}
	// A flow process running once per item records each item's step with
	// what that item came out as, never the list it gathers: the items a
	// flow process's steps generated of a list since anything else did are
	// gathered back into it.
	items := map[key][]string{}
	err = scanAll(ctx, db,
		`SELECT ro.run_id, COALESCE(ro.object_ref, ro.object_id), ro.name, ro.state, ro.content, s.kind, COALESCE(o.many, FALSE)
		FROM run_objects ro JOIN run_steps s ON s.id = ro.step_id
		LEFT JOIN objects o ON o.id = COALESCE(ro.object_ref, ro.object_id)
		WHERE ro.run_id IN `+placeholders(len(ids))+" ORDER BY ro.id",
		func(r *sql.Rows) error {
			var run int64
			var c dataCell
			var kind string
			var many bool
			if err := r.Scan(&run, &c.Object, &c.Name, &c.State, &c.Content, &kind, &many); err != nil {
				return err
			}
			k := key{run, keyOf(c.Object, c.Name)}
			if _, err := jsonArray(stripFence(c.Content)); kind == kindFlow && many && err != nil {
				items[k] = append(items[k], asItem(c.Content))
			} else {
				delete(items, k)
			}
			put(run, c)
			return nil
		}, ids...)
	if err != nil {
		return nil, nil, err
	}
	for k, list := range items {
		rows[at[k.run]].Cells[latest[k]].Content = indentJSON("[" + strings.Join(list, ",") + "]")
	}

	// What a run of a recording flow process was given is the inputs of
	// the step in the outer run that started it. Inputs carry only the
	// object's name; the flow's object of that name is it.
	var parents []any
	for _, r := range rows {
		if r.Parent != nil {
			parents = append(parents, *r.Parent)
		}
	}
	if len(parents) == 0 {
		return objects, rows, nil
	}
	named := map[string]int64{}
	for _, o := range objects {
		if _, ok := named[o.Name]; !ok {
			named[o.Name] = o.ID
		}
	}
	// By the outer runs, whose steps the run_id index finds.
	err = scanAll(ctx, db,
		"SELECT child_run_id, inputs FROM run_steps WHERE run_id IN "+placeholders(len(parents))+" AND child_run_id IN "+placeholders(len(ids)),
		func(r *sql.Rows) error {
			var run int64
			var raw []byte
			if err := r.Scan(&run, &raw); err != nil {
				return err
			}
			var inputs []runInput
			if err := json.Unmarshal(raw, &inputs); err != nil {
				return err
			}
			for _, in := range inputs {
				c := dataCell{Name: in.Name, State: in.State, Content: in.Content, Given: true}
				if id, ok := named[in.Name]; ok {
					c.Object = &id
				}
				if _, ok := latest[key{run, keyOf(c.Object, c.Name)}]; !ok {
					put(run, c)
				}
			}
			return nil
		}, append(parents, ids...)...)
	return objects, rows, err
}

func registerDataRoutes(mux *http.ServeMux, db *sql.DB) {
	// The flow's objects and what its latest runs held of them; see flowData.
	mux.HandleFunc("GET /api/data", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		objects, rows, err := flowData(r.Context(), db, f)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Objects []dataObject `json:"objects"`
			Rows    []dataRow    `json:"rows"`
		}{objects, rows})
	})
}
