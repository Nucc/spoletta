package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
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
//
// The runs can be searched and sorted (search.go) by what they are and by
// each column, over every run of the flow rather than only those shown:
// each object's state is a facet @Object.state, each of its attributes
// @Object.attribute, and all of it @Object where it has no fields, such as
// a list or text.

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

// How many runs' cells are read at once while searching.
const dataChunk = 500

// The runs a search found, at most dataRows of them, with the facets they
// were searched by.
type dataResult struct {
	Objects []dataObject `json:"objects"`
	Rows    []dataRow    `json:"rows"`
	Facets  []facet      `json:"facets"`
	// How many runs the search matched among how many it read, and whether
	// the flow has more runs than a search reads.
	Matched   int  `json:"matched"`
	Searched  int  `json:"searched"`
	Truncated bool `json:"truncated"`
	// How many of those read were trials the query left out, not naming the
	// trial facet; see trials.go.
	Trials int `json:"trials"`
}

// The flow's runs the query matches, newest first or as sorted, and what
// they held of each object.
func flowData(ctx context.Context, db *sql.DB, flow int64, query, sortBy string) (dataResult, error) {
	objects, err := dataObjects(ctx, db, flow)
	if err != nil {
		return dataResult{}, err
	}
	out := dataResult{Objects: objects, Rows: []dataRow{}, Facets: []facet{}}
	var runs []dataRow
	err = scanAll(ctx, db, "SELECT "+runColumns+" FROM runs WHERE flow_id = ? ORDER BY id DESC LIMIT ?",
		func(r *sql.Rows) error {
			var row dataRow
			if err := scanRun(r, &row.run); err != nil {
				return err
			}
			runs = append(runs, row)
			return nil
		}, flow, searchedRows+1)
	if err != nil {
		return out, err
	}
	if len(runs) > searchedRows {
		runs, out.Truncated = runs[:searchedRows], true
	}

	t := newSearchTable()
	runFacet := t.facet("run", facet{Key: "run", Label: "Run", Group: "Run", Hidden: true})
	status := t.facet("status", facet{Key: "status", Label: "Status", Group: "Run"})
	started := t.facet("started", facet{Key: "started", Label: "Started", Group: "Run", Date: true, Hidden: true})
	startedBy := t.facet("started_by", facet{Key: "started_by", Label: "Started by", Group: "Run"})
	item := t.facet("item", facet{Key: "item", Label: "Item", Group: "Run"})
	parent := t.facet("parent", facet{Key: "parent", Label: "In run", Group: "Run"})
	trial := t.facet("trial", facet{Key: "trial", Label: "Trial of", Group: "Run"})
	names := map[string]string{}
	// The board's objects first, so their facets go in its order.
	for _, o := range objects {
		k := cellKey(&o.ID, o.Name)
		names[k] = o.Name
		base := "@" + slug(o.Name)
		t.facet(k+"\x00state", facet{Key: base + ".state", Label: "state", Group: o.Name, Object: k, Kind: "state"})
		for _, a := range o.Attributes {
			t.facet(k+"\x00."+a, facet{Key: base + "." + slug(a), Label: a, Group: o.Name, Object: k, Kind: "field", Field: a})
		}
	}
	for start := 0; start < len(runs); start += dataChunk {
		chunk := runs[start:min(start+dataChunk, len(runs))]
		if err := runCells(ctx, db, objects, chunk); err != nil {
			return out, err
		}
		for i := range chunk {
			rn := &chunk[i]
			r := t.row()
			r[runFacet] = []string{strconv.FormatInt(rn.ID, 10)}
			r[status] = []string{rn.Status}
			r[started] = []string{rn.StartedAt.UTC().Format(time.RFC3339)}
			r[startedBy] = scalars(rn.StartedBy)
			r[item] = scalars(rn.ItemKey)
			if rn.Parent != nil {
				r[parent] = []string{strconv.FormatInt(*rn.Parent, 10)}
			}
			if rn.TrialOf != nil {
				r[trial] = []string{rn.TrialOfName}
			}
			for _, c := range rn.Cells {
				cellFacets(t, r, names, c)
			}
			// Only the rows shown keep their cells, read again below.
			rn.Cells = nil
		}
	}

	s := t.search()
	q := parseSearch(query)
	if !s.names(q, s.idOf("trial")) {
		q = withoutTrials(q)
		for _, rn := range runs {
			if rn.TrialOf != nil {
				out.Trials++
			}
		}
	}
	key, desc := sortParam(sortBy)
	matched := s.run(q, key, desc)
	s.count(q, matched)
	out.Facets, out.Matched, out.Searched = t.list(), len(matched), len(runs)
	for _, i := range matched[:min(len(matched), dataRows)] {
		out.Rows = append(out.Rows, runs[i])
	}
	return out, runCells(ctx, db, objects, out.Rows)
}

// The query with trials left out, as a query naming the trial facet
// doesn't leave them: -trial:* among its terms, which the facet's own
// counts leave out, as they do any term ticking its values, so they still
// say how many trials there are.
func withoutTrials(q *searchNode) *searchNode {
	none := &searchNode{kind: "not", nodes: []*searchNode{{kind: "term", key: "trial", hasKey: true, test: searchTest{op: "exists"}}}}
	return &searchNode{kind: "and", nodes: append(slices.Clone(clauses(q)), none)}
}

// What tells a run's objects apart: their id, or for one deleted before ids
// were kept or a given one the flow has none of, its name.
func cellKey(object *int64, name string) string {
	if object != nil {
		return "#" + strconv.FormatInt(*object, 10)
	}
	return name
}

// The facets of what a run held of an object, named as the board names it,
// or the newest run holding it for one deleted since.
func cellFacets(t *searchTable, r map[int][]string, names map[string]string, c dataCell) {
	k := cellKey(c.Object, c.Name)
	name, ok := names[k]
	if !ok {
		name = c.Name
		names[k] = name
	}
	base := "@" + slug(name)
	if c.State != "" {
		r[t.facet(k+"\x00state", facet{Key: base + ".state", Label: "state", Group: name, Object: k, Kind: "state"})] = []string{c.State}
	}
	switch v := contentValue(c.Content).(type) {
	case map[string]any:
		fields := make([]string, 0, len(v))
		for f := range v {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		for _, f := range fields {
			i := t.facet(k+"\x00."+f, facet{Key: base + "." + slug(f), Label: f, Group: name, Object: k, Kind: "field", Field: f})
			r[i] = scalars(v[f])
		}
	case []any:
		r[t.facet(k+"\x00whole", facet{Key: base, Label: "items", Group: name, Object: k, Kind: "whole"})] = scalars(v)
	default:
		r[t.facet(k+"\x00whole", facet{Key: base, Label: "value", Group: name, Object: k, Kind: "whole"})] = scalars(c.Content)
	}
}

// The flow's objects on its boards, with their attributes.
func dataObjects(ctx context.Context, db *sql.DB, flow int64) ([]dataObject, error) {
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
		return nil, err
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
	return objects, err
}

// Fills in what each of the runs held of each object.
func runCells(ctx context.Context, db *sql.DB, objects []dataObject, rows []dataRow) error {
	if len(rows) == 0 {
		return nil
	}
	at := map[int64]int{}
	ids := make([]any, len(rows))
	for i := range rows {
		rows[i].Cells = []dataCell{}
		at[rows[i].ID] = i
		ids[i] = rows[i].ID
	}

	// The latest of each object in each run; rows come oldest first, so a
	// later one replaces what came before it.
	type key struct {
		run    int64
		object string
	}
	latest := map[key]int{}
	put := func(run int64, c dataCell) {
		row := &rows[at[run]]
		k := key{run, cellKey(c.Object, c.Name)}
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
	err := scanAll(ctx, db,
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
			k := key{run, cellKey(c.Object, c.Name)}
			if _, err := jsonArray(stripFence(c.Content)); kind == kindFlow && many && err != nil {
				items[k] = append(items[k], asItem(c.Content))
			} else {
				delete(items, k)
			}
			put(run, c)
			return nil
		}, ids...)
	if err != nil {
		return err
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
		return nil
	}
	named := map[string]int64{}
	for _, o := range objects {
		if _, ok := named[o.Name]; !ok {
			named[o.Name] = o.ID
		}
	}
	// By the outer runs, whose steps the run_id index finds.
	return scanAll(ctx, db,
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
				if _, ok := latest[key{run, cellKey(c.Object, c.Name)}]; !ok {
					put(run, c)
				}
			}
			return nil
		}, append(parents, ids...)...)
}

func registerDataRoutes(mux *http.ServeMux, db *sql.DB) {
	// The flow's objects and what its latest runs held of them, those the
	// query q matches, sorted by sort; see flowData.
	mux.HandleFunc("GET /api/data", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		out, err := flowData(r.Context(), db, f, r.URL.Query().Get("q"), r.URL.Query().Get("sort"))
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
