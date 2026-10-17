package main

import "testing"

func TestLinkQueryProblem(t *testing.T) {
	for _, c := range []struct {
		q    linkQuery
		want string
	}{
		{linkQuery{}, ""},
		{linkQuery{Where: `score >= 4 and kind == "bug"`, Limit: 100}, ""},
		{linkQuery{Where: "score"}, "the query's rule: "},
		{linkQuery{Limit: 101}, "a query takes at most 100 records"},
		{linkQuery{Limit: -1}, "a query takes at most 100 records"},
	} {
		got := c.q.problem()
		if (c.want == "") != (got == "") || len(got) < len(c.want) || got[:len(c.want)] != c.want {
			t.Errorf("%+v: problem = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestQueryPhrase(t *testing.T) {
	for q, want := range map[linkQuery]string{
		{}: "the latest 20 stored records of Review",
		{Where: "score >= 4", State: "approved", Limit: 1, Oldest: true}: "the earliest 1 stored records of Review in approved where score >= 4",
	} {
		if got := queryPhrase("Review", &q); got != want {
			t.Errorf("queryPhrase(%+v) = %q, want %q", q, got, want)
		}
	}
}
