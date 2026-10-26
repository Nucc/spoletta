package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Four runs of a bug flow, with the facets a flow's data has.
func searchFixture() *searchTable {
	t := newSearchTable()
	run := t.facet("run", facet{Key: "run", Hidden: true})
	status := t.facet("status", facet{Key: "status"})
	started := t.facet("started", facet{Key: "started", Date: true, Hidden: true})
	title := t.facet("#1\x00.title", facet{Key: "@Bug.title"})
	amount := t.facet("#1\x00.amount", facet{Key: "@Bug.amount"})
	tags := t.facet("#1\x00.tags", facet{Key: "@Bug.tags"})
	add := func(id int, st, at, ti, am string, tg ...string) {
		r := t.row()
		r[run] = []string{fmt.Sprint(id)}
		r[status] = []string{st}
		r[started] = []string{at}
		if ti != "" {
			r[title] = []string{ti}
		}
		if am != "" {
			r[amount] = []string{am}
		}
		if len(tg) > 0 {
			r[tags] = tg
		}
	}
	add(1, "done", "2026-10-17T10:00:00Z", "Crash on login", "120", "a", "b")
	add(2, "failed", "2026-10-16T10:00:00Z", "Timeout in sync", "5")
	add(3, "interrupted", "2026-10-01T10:00:00Z", "Bug report", "30", "b")
	add(4, "done", time.Now().UTC().Format(time.RFC3339), "", "")
	return t
}

func TestSearchMatchesQueries(t *testing.T) {
	tb := searchFixture()
	for _, c := range []struct{ query, want string }{
		{"", "1,2,3,4"},
		{"status:done", "1,4"},
		{"status:Failed", "2"},
		{"-status:done", "2,3"},
		{"NOT status:done", "2,3"},
		{"status:(failed OR interrupted)", "2,3"},
		{"status:done OR status:failed", "1,2,4"},
		{"crash", "1"},
		{`"in sync"`, "2"},
		{"title:*bug*", "3"},
		{"@bug.title:*BUG*", "3"},
		{"Bug.amount:>10", "1,3"},
		{"amount:[5 TO 30]", "2,3"},
		{"amount:<=5", "2"},
		{"amount:30", "3"},
		{"tags:b", "1,3"},
		{"tags:*", "1,3"},
		{"-tags:*", "2,4"},
		{"started:>now-1h", "4"},
		{"started:2026-10-16", "2"},
		{"started:[2026-10-01 TO 2026-10-16T23:00]", "2,3"},
		{"nokey:x", ""},
		{"status:", "1,2,3,4"},
		{"status:done -", "1,4"},
		{"(status:done", "1,4"},
		{"status:done)", "1,4"},
		{"run:2", "2"},
		{"status:done (crash OR timeout)", "1"},
		{"time*sync", "2"},
		{"(tags:b OR -tags:*) -status:done", "2,3"},
	} {
		s := tb.search()
		var got []string
		for _, i := range s.run(parseSearch(c.query), "", false) {
			got = append(got, fmt.Sprint(i+1))
		}
		if g := strings.Join(got, ","); g != c.want {
			t.Errorf("%q matched runs %q, want %q", c.query, g, c.want)
		}
	}
}

func TestSearchSortsWithEmptyLast(t *testing.T) {
	tb := searchFixture()
	for _, c := range []struct {
		key  string
		desc bool
		want []int
	}{
		{"amount", false, []int{1, 2, 0, 3}},
		{"amount", true, []int{0, 2, 1, 3}},
		{"started", true, []int{3, 0, 1, 2}},
		{"title", false, []int{2, 0, 1, 3}},
	} {
		if got := tb.search().run(allRows, c.key, c.desc); !slices.Equal(got, c.want) {
			t.Errorf("sorted by %s desc=%v = %v, want %v", c.key, c.desc, got, c.want)
		}
	}
}

// A facet's counts leave out what the query ticks of it, so its other
// values still show, and count the rows with none.
func TestSearchCountsFacetsWithoutTheirOwnTicks(t *testing.T) {
	tb := searchFixture()
	s := tb.search()
	q := parseSearch("status:done -tags:a")
	s.count(q, s.run(q, "", false))
	status := tb.facets[1]
	if got := fmt.Sprint(status.Values); got != "[{done 1} {failed 1} {interrupted 1}]" {
		t.Errorf("status counts = %s, want every status among the runs without tag a", got)
	}
	tags := tb.facets[5]
	if got := fmt.Sprint(tags.Values); got != "[]" || tags.None != 1 || tags.Distinct != 2 {
		t.Errorf("tags = %s none %d distinct %d, want only run 4, with none", got, tags.None, tags.Distinct)
	}
	if !tb.facets[4].Numeric || tb.facets[3].Numeric {
		t.Errorf("amount numeric %v, title numeric %v; want only amount", tb.facets[4].Numeric, tb.facets[3].Numeric)
	}
}

// A term leaving out every row with a value of a facet, -tags:*, ticks
// none of its values, but its counts leave it out all the same.
func TestSearchCountsFacetsWithoutTheirOwnExclusions(t *testing.T) {
	tb := searchFixture()
	s := tb.search()
	q := parseSearch("-tags:*")
	if got := s.run(q, "", false); fmt.Sprint(got) != "[1 3]" {
		t.Fatalf("-tags:* matches rows %v, want the second and fourth", got)
	}
	s.count(q, s.run(q, "", false))
	if got := fmt.Sprint(tb.facets[5].Values); got != "[{b 2} {a 1}]" {
		t.Errorf("tags counts = %s, want those of every row", got)
	}
	if got := fmt.Sprint(tb.facets[1].Values); got != "[{done 1} {failed 1}]" {
		t.Errorf("status counts = %s, want those of the rows without tags", got)
	}
}

// Content reads as the board's cells read it: in a code fence, or with
// strings holding JSON again.
func TestContentValueUnwrapsTwiceEncodedJSON(t *testing.T) {
	for _, c := range []struct{ content, want string }{
		{`{"title":"A"}`, "map[title:A]"},
		{"```json\n{\"title\":\"A\"}\n```", "map[title:A]"},
		{`"{\"title\":\"A\"}"`, "<nil>"},
		{`{"bug":"{\"title\":\"A\",\"n\":2}"}`, "map[bug:map[n:2 title:A]]"},
		{`[1, "two"]`, "[1 two]"},
		{"plain text", "<nil>"},
		{`{"a":1} trailing`, "<nil>"},
	} {
		if got := fmt.Sprint(contentValue(c.content)); got != c.want {
			t.Errorf("contentValue(%q) = %s, want %s", c.content, got, c.want)
		}
	}
}
