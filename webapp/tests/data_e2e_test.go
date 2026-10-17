package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type dataCell struct {
	Object  *int64 `json:"object"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Content string `json:"content"`
	Given   bool   `json:"given"`
}

type dataRow struct {
	run
	Cells []dataCell `json:"cells"`
}

type flowData struct {
	Objects []struct {
		ID         int64    `json:"id"`
		Name       string   `json:"name"`
		Many       bool     `json:"many"`
		Attributes []string `json:"attributes"`
	} `json:"objects"`
	Rows      []dataRow     `json:"rows"`
	Facets    []searchFacet `json:"facets"`
	Matched   int           `json:"matched"`
	Searched  int           `json:"searched"`
	Truncated bool          `json:"truncated"`
}

// A column rows are searched by, with its values' counts.
type searchFacet struct {
	Key    string `json:"key"`
	Object string `json:"object"`
	Kind   string `json:"kind"`
	Field  string `json:"field"`
	Values []struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	} `json:"values"`
	None int `json:"none"`
}

// The facet's values and their counts, as value=count joined by commas.
func facetCounts(facets []searchFacet, key string) string {
	for _, f := range facets {
		if f.Key == key {
			var out []string
			for _, v := range f.Values {
				out = append(out, fmt.Sprintf("%s=%d", v.Value, v.Count))
			}
			return strings.Join(out, ",")
		}
	}
	return "no facet " + key
}

func (a *app) data(f int64) flowData {
	a.t.Helper()
	return a.searchData(f, "", "")
}

func (a *app) searchData(f int64, q, sort string) flowData {
	a.t.Helper()
	var d flowData
	a.call("GET", fmt.Sprintf("/api/data?flow=%d&q=%s&sort=%s", f, url.QueryEscape(q), url.QueryEscape(sort)), nil, http.StatusOK, &d)
	return d
}

func cellOf(r dataRow, name string) *dataCell {
	for i := range r.Cells {
		if r.Cells[i].Name == name {
			return &r.Cells[i]
		}
	}
	return nil
}

// Each run is a row holding what it generated of each object, and the
// objects come with their attributes in the order they were added.
func TestDataHoldsWhatEachRunGenerated(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Triage")
	triage := a.agent(f.ID, "Triage", "Triage the crash.")
	issue := a.newObject(f.ID, "Issue")
	owner := a.newObject(f.ID, "Owner")
	for _, at := range []struct {
		object int64
		name   string
	}{{issue.ID, "title"}, {issue.ID, "severity"}, {owner.ID, "team"}} {
		a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", at.object), map[string]any{"name": at.name, "definition": "The " + at.name}, http.StatusCreated, nil)
	}
	a.output(triage.ID, issue.ID)
	a.output(triage.ID, owner.ID)
	a.startAt(f.ID, triage.ID)

	if d := a.data(f.ID); len(d.Rows) != 0 || len(d.Objects) != 2 {
		t.Fatalf("data before any run = %+v, want no rows and both objects", d)
	}

	titles := []string{"Login crashes", "Search is slow"}
	var runs []int64
	for _, title := range titles {
		a.worker.onAgent("Triage", handsIn(map[string]any{
			"Issue": map[string]any{"title": title, "severity": "high"},
			"Owner": map[string]any{"team": "auth"},
		}))
		rn := a.startRun(f.ID)
		if d := a.finish(rn.ID); d.Status != runDone {
			t.Fatalf("run %s; steps %s", d.Status, describeSteps(d))
		}
		runs = append(runs, rn.ID)
	}

	d := a.data(f.ID)
	if len(d.Objects) != 2 || d.Objects[0].Name != "Issue" || !slices.Equal(d.Objects[0].Attributes, []string{"title", "severity"}) {
		t.Errorf("objects = %+v, want Issue with title and severity first", d.Objects)
	}
	if len(d.Rows) != 2 || d.Rows[0].ID != runs[1] || d.Rows[1].ID != runs[0] {
		t.Fatalf("rows = %+v, want both runs, newest first", d.Rows)
	}
	for i, r := range d.Rows {
		want := titles[len(titles)-1-i]
		c := cellOf(r, "Issue")
		if c == nil || c.Object == nil || *c.Object != issue.ID || c.Given || !strings.Contains(c.Content, want) {
			t.Errorf("run %d's Issue = %+v, want it generated holding %q", r.ID, c, want)
		}
		if c := cellOf(r, "Owner"); c == nil || !strings.Contains(c.Content, "auth") || strings.Contains(c.Content, want) {
			t.Errorf("run %d's Owner = %+v, want only the owner", r.ID, c)
		}
	}
}

// A run of one item of a list holds the item it was given beside what its
// steps generated, and the outer run the lists.
func TestDataHoldsTheItemOfAnItemRun(t *testing.T) {
	a := newApp(t)
	n := newNestedLists(t, a, true, true)
	a.worker.onAgent("Find", handsIn(nestedTitles("A", "B")))
	rn := a.startRun(n.flow.ID)
	nestedFinish(t, a, rn.ID, runDone)

	d := a.data(n.flow.ID)
	if len(d.Rows) != 3 {
		t.Fatalf("rows = %+v, want the run and its two items' runs", d.Rows)
	}
	seen := map[string]bool{}
	for _, r := range d.Rows {
		if r.ID == rn.ID {
			if c := cellOf(r, "Issues"); c == nil || c.Given || !strings.Contains(c.Content, `"A"`) || !strings.Contains(c.Content, `"B"`) {
				t.Errorf("the run's Issues = %+v, want the list it generated", c)
			}
			if c := cellOf(r, "Fixes"); c == nil || !strings.Contains(c.Content, "fixed A") || !strings.Contains(c.Content, "fixed B") {
				t.Errorf("the run's Fixes = %+v, want what both items' runs fixed", c)
			}
			continue
		}
		if r.Parent == nil || *r.Parent != rn.ID {
			t.Errorf("row %+v is no item run of run %d", r.run, rn.ID)
			continue
		}
		item := cellOf(r, "Issues")
		if item == nil || !item.Given || item.Object == nil || *item.Object != n.issues.ID || !strings.Contains(item.Content, `"`+r.ItemKey+`"`) {
			t.Errorf("item run %d's Issues = %+v, want the item %s it was given", r.ID, item, r.ItemKey)
		}
		if c := cellOf(r, "Fixes"); c == nil || c.Given || !strings.Contains(c.Content, "fixed") {
			t.Errorf("item run %d's Fixes = %+v, want what Fix generated", r.ID, c)
		}
		seen[r.ItemKey] = true
	}
	if !seen["A"] || !seen["B"] {
		t.Errorf("item runs keyed %v, want A and B", seen)
	}
}

// The runs are searched on the server, over every run rather than the
// latest a page shows, by what they are and what they held of each object,
// and sorted by any of it. A facet's counts leave out what the query ticks
// of it.
func TestDataSearchesEveryRun(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Triage")
	triage := a.agent(f.ID, "Triage", "Triage the crash.")
	issue := a.newObject(f.ID, "Issue")
	for _, name := range []string{"title", "severity"} {
		a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", issue.ID), map[string]any{"name": name, "definition": "The " + name}, http.StatusCreated, nil)
	}
	a.output(triage.ID, issue.ID)
	a.startAt(f.ID, triage.ID)

	// The first run's issue is critical; the others alternate high and low.
	var n atomic.Int64
	a.worker.onAgent("Triage", func(context.Context, agentRun) runResult {
		i := n.Add(1)
		severity := map[bool]string{true: "high", false: "low"}[i%2 == 0]
		if i == 1 {
			severity = "critical"
		}
		return submitted(map[string]any{"title": fmt.Sprintf("Issue %d", i), "severity": severity})
	})
	const runs = 201
	var ids []int64
	for i := range runs {
		rn := a.startRun(f.ID)
		if d := a.finish(rn.ID); d.Status != runDone {
			t.Fatalf("run %d %s; steps %s", i+1, d.Status, describeSteps(d))
		}
		ids = append(ids, rn.ID)
	}
	first := ids[0]

	d := a.data(f.ID)
	if len(d.Rows) != 200 || d.Matched != runs || d.Searched != runs || d.Truncated {
		t.Fatalf("unsearched: %d rows, matched %d of %d, truncated %v; want the latest 200 of all %d", len(d.Rows), d.Matched, d.Searched, d.Truncated, runs)
	}
	if slices.ContainsFunc(d.Rows, func(r dataRow) bool { return r.ID == first }) {
		t.Fatalf("the first run is among the latest 200")
	}

	for _, c := range []struct {
		query   string
		matched int
	}{
		{"@Issue.severity:critical", 1},
		{"severity:CRITICAL", 1},
		{"-severity:critical", runs - 1},
		{"severity:(high OR low) -title:\"Issue 3\"", runs - 2},
		{`"Issue 17"`, 11},
		{"title:*7", 20},
		{"status:done severity:critical", 1},
		{"status:failed", 0},
		{"@Nothing.here:x", 0},
		{fmt.Sprintf("run:<=%d", ids[9]), 10},
		{"started:>now-1h severity:critical", 1},
		{"started:<now-1h", 0},
	} {
		got := a.searchData(f.ID, c.query, "")
		if got.Matched != c.matched || len(got.Rows) != min(c.matched, 200) {
			t.Errorf("%s matched %d, %d rows; want %d", c.query, got.Matched, len(got.Rows), c.matched)
		}
	}
	// Only a search on the server finds the first run, past the latest 200.
	if got := a.searchData(f.ID, "severity:critical", ""); len(got.Rows) != 1 || got.Rows[0].ID != first ||
		cellOf(got.Rows[0], "Issue") == nil || !strings.Contains(cellOf(got.Rows[0], "Issue").Content, "Issue 1\"") {
		t.Errorf("the critical issue's runs = %+v, want the first run with what it held", got.Rows)
	}

	// Sorting goes over every run too: oldest first by run, and by title,
	// with numbers in it as numbers.
	if got := a.searchData(f.ID, "", "run"); got.Rows[0].ID != first {
		t.Errorf("sorted by run, the first row is run %d, want %d", got.Rows[0].ID, first)
	}
	if got := a.searchData(f.ID, "", "-@Issue.title"); !strings.Contains(cellOf(got.Rows[0], "Issue").Content, "Issue 201") {
		t.Errorf("sorted by title the other way, the first row holds %s, want Issue 201", cellOf(got.Rows[0], "Issue").Content)
	}

	// Ticking a severity keeps the others' counts; other facets count only
	// the runs matched.
	got := a.searchData(f.ID, "severity:critical", "")
	if c := facetCounts(got.Facets, "@Issue.severity"); c != "low=100,high=100,critical=1" && c != "high=100,low=100,critical=1" {
		t.Errorf("severity counts with critical ticked = %s, want every severity", c)
	}
	if c := facetCounts(got.Facets, "status"); c != "done=1" {
		t.Errorf("status counts of the critical issue's runs = %s, want done=1", c)
	}
	for _, fc := range got.Facets {
		if fc.Key == "@Issue.title" && (fc.Object != fmt.Sprintf("#%d", issue.ID) || fc.Kind != "field" || fc.Field != "title") {
			t.Errorf("the title facet = %+v, want Issue's title field", fc)
		}
	}
}
