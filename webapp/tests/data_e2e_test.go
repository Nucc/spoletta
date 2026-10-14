package tests

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
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
	Rows []dataRow `json:"rows"`
}

func (a *app) data(f int64) flowData {
	a.t.Helper()
	var d flowData
	a.call("GET", fmt.Sprintf("/api/data?flow=%d", f), nil, http.StatusOK, &d)
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
