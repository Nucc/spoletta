package main

import (
	"fmt"
	"strings"
	"testing"
)

// The Data page leaves trials out unless its query names their facet,
// whatever else it asks; the facet's counts still say how many there are.
func TestDataLeavesOutTrialsUnlessAsked(t *testing.T) {
	tb := newSearchTable()
	run := tb.facet("run", facet{Key: "run", Hidden: true})
	status := tb.facet("status", facet{Key: "status"})
	trial := tb.facet("trial", facet{Key: "trial", Label: "Trial of"})
	for i, row := range []struct{ status, trial string }{{"done", ""}, {"done", "Count"}, {"failed", "Count"}, {"failed", ""}} {
		r := tb.row()
		r[run] = []string{fmt.Sprint(i + 1)}
		r[status] = []string{row.status}
		if row.trial != "" {
			r[trial] = []string{row.trial}
		}
	}
	for _, c := range []struct {
		query, want string
	}{
		{"", "1 4"},
		{"status:done", "1"},
		{"timeout OR status:failed", "4"},
		{"trial:*", "2 3"},
		{"trial:count status:failed", "3"},
		{"-trial:Count", "1 4"},
		{"status:done OR trial:*", "1 2 3"},
		{"TRIAL:*", "2 3"},
	} {
		s := tb.search()
		q := parseSearch(c.query)
		if !s.names(q, s.idOf("trial")) {
			q = withoutTrials(q)
		}
		var got []string
		for _, i := range s.run(q, "", false) {
			got = append(got, fmt.Sprint(i+1))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%q matches runs %v, want %s", c.query, got, c.want)
		}
		if c.query == "" {
			s.count(q, s.run(q, "", false))
			if got := fmt.Sprint(tb.facets[trial].Values); got != "[{Count 2}]" {
				t.Errorf("trial counts = %s, want both trials, though they are left out", got)
			}
		}
	}
}

// A sample's inputs keep through the database as they went in, and none
// go in as an empty list.
func TestSampleInputsStore(t *testing.T) {
	in := sampleInputs{{Object: 3, State: 7, Content: "a ticket"}, {Object: 4, Content: ""}}
	v, err := in.Value()
	if err != nil {
		t.Fatal(err)
	}
	var out sampleInputs
	if err := out.Scan([]byte(v.(string))); err != nil || fmt.Sprint(out) != fmt.Sprint(in) {
		t.Errorf("stored %v and read back %v, %v", in, out, err)
	}
	if v, _ := sampleInputs(nil).Value(); v != "[]" {
		t.Errorf("no inputs stored as %v, want []", v)
	}
	for name, want := range map[string]string{"": "needs a name", "Long": "", strings.Repeat("é", 256): "at most 255"} {
		if got := sampleNameProblem(name); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("sampleNameProblem(%.10q) = %q, want %q", name, got, want)
		}
	}
}
