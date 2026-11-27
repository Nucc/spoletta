package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

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
		{linkQuery{Search: "state:open @created:>now-7d", Like: "{@Incoming.summary} {@Incoming}"}, ""},
		{linkQuery{Search: "key:{x}"}, "the query's search: write each value as {@Object.attribute}"},
		{linkQuery{Like: "{@}"}, "the query's like: \"{@}\" names no object"},
		{linkQuery{Like: strings.Repeat("x", maxQueryText+1)}, "the query's like is longer than"},
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
		{Where: "score >= 4", State: "approved", Limit: 1, Oldest: true}:    "the earliest stored record of Review in approved where score >= 4",
		{Search: "@created:>now-7d", Like: "{@Incoming.summary}", Limit: 5}: `the 5 stored records of Review most like "{@Incoming.summary}" found by "@created:>now-7d"`,
	} {
		if got := queryPhrase("Review", &q); got != want {
			t.Errorf("queryPhrase(%+v) = %q, want %q", q, got, want)
		}
	}
}

// A query takes records in its state, else in those its search names; a
// value a search names stands for any.
func TestLinkQueryStates(t *testing.T) {
	for _, c := range []struct {
		q    linkQuery
		want []string
	}{
		{linkQuery{}, nil},
		{linkQuery{State: "open", Search: "state:done"}, []string{"open"}},
		{linkQuery{Search: "state:(new OR reopened) @created:>now-7d"}, []string{"new", "reopened"}},
		{linkQuery{Search: "-state:done"}, nil},
		{linkQuery{Search: "state:{@Incoming.state}"}, nil},
	} {
		if got := c.q.states(); !slices.Equal(got, c.want) {
			t.Errorf("%+v: states = %v, want %v", c.q, got, c.want)
		}
	}
	if (&linkQuery{Like: "  "}).lookup() || !(&linkQuery{Like: "{@Incoming}"}).lookup() {
		t.Error("only a query with like looks records up")
	}
}

// What a process takes fills in a query's values: into a search quoted,
// whatever it holds staying one value, into like as the text it holds.
func TestFillQuery(t *testing.T) {
	given := []runInput{
		{Name: "Incoming ticket", Content: `{"summary": "Login \"times\" out\\ OR state:done *", "Component": "auth", "count": 3, "labels": ["sso", "ui"], "empty": ""}`},
		{Name: "Note", Content: "plain words here"},
	}
	search, err := fillQuery(`state:open @component:{@Incoming_ticket.component} {@incoming ticket.summary} @count:>{@Incoming ticket.count}`, given, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := `state:open @component:"auth" "Login \"times\" out\\ OR state:done *" @count:>"3"`; search != want {
		t.Errorf("search = %s, want %s", search, want)
	}
	// It parses as the terms written, the summary one literal term.
	n := parseSearch(search)
	if n.kind != "and" || len(n.nodes) != 4 {
		t.Fatalf("the search parses as %+v", n)
	}
	if s := n.nodes[2]; s.kind != "term" || s.hasKey || !s.test.literal || s.test.value != `Login "times" out\ OR state:done *` {
		t.Errorf("the summary term = %+v", s)
	}
	if c := n.nodes[1]; c.key != "@component" || c.test.value != "auth" || !c.test.literal {
		t.Errorf("the component term = %+v", c)
	}

	like, err := fillQuery("{@Incoming ticket.labels} {@Note}", given, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(likeWords(like), []string{"sso", "ui", "plain", "words", "here"}) {
		t.Errorf("like = %q", like)
	}
	if whole, _ := fillQuery("{@Incoming ticket}", given, false); !slices.Contains(likeWords(whole), "auth") || slices.Contains(likeWords(whole), "component") {
		t.Errorf("the whole object's text = %q", whole)
	}
	if missing, err := fillQuery("{@Incoming ticket.nothing}", given, false); err != nil || missing != "" {
		t.Errorf("a field it lacks = %q, %v, want nothing to compare", missing, err)
	}

	for _, c := range []struct {
		text string
		want string
	}{
		{"{@Bug.summary}", `Its query's search names "Bug", which it wasn't given.`},
		{"@x:{@Incoming ticket.empty}", `Its query's search names "Incoming ticket"'s "empty", which is empty.`},
		{"@x:{@Incoming ticket.labels}", `which isn't text or a number`},
	} {
		_, err := fillQuery(c.text, given, true)
		var p *queryProblem
		if !errors.As(err, &p) || !strings.Contains(p.msg, c.want) {
			t.Errorf("%s: %v, want %q", c.text, err, c.want)
		}
	}
}

// A query's records say how many it was cut from only when they are fewer
// than it selected, or it read no further.
func TestRecordsCutFrom(t *testing.T) {
	for _, c := range []struct {
		in   runInput
		want string
	}{
		{runInput{Role: inputRecords, Content: `[{}, {}]`, Matched: 2}, ""},
		{runInput{Role: inputRecords, Content: `[{}, {}]`, Matched: 340}, "340"},
		{runInput{Role: inputRecords, Content: `[{}]`, Matched: 1, MatchedMore: true}, "1+"},
		{runInput{Role: inputRecords, Content: `[]`}, ""},
		{runInput{Content: `[{}]`, Matched: 5}, ""},
	} {
		if got := c.in.cutFrom(); got != c.want {
			t.Errorf("%+v: cutFrom = %q, want %q", c.in, got, c.want)
		}
	}
	prompt := withInputs("Check it.", []runInput{{Name: "Ticket", Role: inputRecords, Content: `[{}]`, Matched: 12}, {Name: "Incoming", Content: `{}`}})
	if !strings.Contains(prompt, `<input name="Ticket" role="records" matched="12">`) || !strings.Contains(prompt, "One with a matched attribute") {
		t.Errorf("the prompt doesn't say the records were cut:\n%s", prompt)
	}
	if prompt := withInputs("Check it.", []runInput{{Name: "Ticket", Role: inputRecords, Content: `[{}]`, Matched: 1}}); strings.Contains(prompt, "matched") {
		t.Errorf("the prompt says records were cut when none were:\n%s", prompt)
	}
}
