package main

import (
	"strings"
	"testing"
)

// A rule is checked as the branch it names with a value standing for each
// value it takes.
func TestBranchRulesAreChecked(t *testing.T) {
	for rule, want := range map[string]string{
		"":                             "",
		"feature/{@Ticket.key}":        "",
		"  {@Pull request.branch}  ":   "",
		"fix/{@Ticket.key}-{@Summary}": "",
		"feature/{@}":                  "names no object",
		"feature/{Ticket.key}":         "write each value as",
		"feature/{@Ticket.key":         "write each value as",
		"feature//{@Ticket.key}":       "isn't a valid branch name",
		"feature/{@Ticket.key}.lock":   "isn't a valid branch name",
		"feature/.{@Ticket.key}":       "isn't a valid branch name",
		"feature {@Ticket.key}":        "isn't a valid branch name",
	} {
		r := rule
		if got := checkBranchRule(&r); want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%q: %q, want %q", rule, got, want)
		}
	}
}

// A rule takes each value from what the run holds as it starts, by the
// object's name as a search facet writes it, else names no branch, saying
// why.
func TestBranchRulesNameFromWhatRunsHold(t *testing.T) {
	board := snapshot{
		Objects: []snapshotObject{
			{ID: 1, Name: "Ticket"},
			{ID: 2, Name: "Pull request"},
			{ID: 3, Name: "Key"},
			{ID: 4, Name: "Tickets", Many: true},
		},
		Attributes: []attribute{
			{ID: 10, Object: 1, Name: "key"},
			{ID: 11, Object: 1, Name: "points"},
			{ID: 12, Object: 2, Name: "branch"},
			{ID: 13, Object: 1, Name: "title"},
			{ID: 14, Object: 4, Name: "key"},
			{ID: 15, Object: 1, Name: "odd"},
			{ID: 16, Object: 1, Name: "bang"},
		},
	}
	given := map[int64]value{
		1: {content: `{"key": "PROJ-123", "points": 3, "title": "Fix the login", "odd": "-.fix//a..b/.c/.lock", "bang": "!?*"}`},
		2: {content: `{"branch": "fix/login"}`},
		3: {content: "PROJ-7"},
		4: {content: `[{"key": "A-1"}]`},
	}
	for _, c := range []struct{ rule, want, why string }{
		{"feature/{@Ticket.key}", "feature/PROJ-123", ""},
		{"feature/{@ticket.KEY}-{@Ticket.points}", "feature/PROJ-123-3", ""},
		{"{@Pull_request.branch}", "fix/login", ""},
		{"{@Pull request.branch}", "fix/login", ""},
		{"feature/{@Key}", "feature/PROJ-7", ""},
		{"feature/{@Bug.key}", "", `no object "Bug"`},
		{"feature/{@Ticket.owner}", "", `"Ticket" has no attribute "owner"`},
		{"feature/{@Ticket}", "", "isn't text or a number"},
		{"feature/{@Ticket.title}", "feature/Fix_the_login", ""},
		{"{@Ticket.odd}", "fix/a.b/c/lock", ""},
		{"feature/{@Ticket.bang}", "", `"Ticket"'s "bang" holds nothing a branch name takes`},
		{"feature/{@Tickets.key}", "", "isn't a JSON object"},
	} {
		got, why := ruleBranch(c.rule, board, given)
		if got != c.want || c.why == "" && why != "" || c.why != "" && !strings.Contains(why, c.why) {
			t.Errorf("%s: %q (%q), want %q (%q)", c.rule, got, why, c.want, c.why)
		}
	}
	if got, why := ruleBranch("feature/{@Ticket.key}", board, map[int64]value{2: given[2]}); got != "" || !strings.Contains(why, `wasn't given "Ticket"`) {
		t.Errorf("without the ticket: %q (%q)", got, why)
	}
	for content, want := range map[string]string{`{"key": ""}`: "is empty", `{"key": null}`: "is empty", `{}`: "is empty", `{"key": "  "}`: `"Ticket"'s "key" is blank`} {
		if got, why := ruleBranch("feature/{@Ticket.key}", board, map[int64]value{1: {content: content}}); got != "" || !strings.Contains(why, want) {
			t.Errorf("%s: %q (%q), want none as it %s", content, got, why, want)
		}
	}
	if got, why := ruleBranch("feature/{@Key}", board, map[int64]value{3: {content: " \n "}}); got != "" || !strings.Contains(why, `"Key" is empty`) {
		t.Errorf("a blank plain value: %q (%q)", got, why)
	}
}

// The agent is told the branch its checkouts are on, where it started from
// and how to push it, the run's own branch staying the worker's.
func TestAgentsAreToldTheNamedBranch(t *testing.T) {
	ws := &workspaceSpec{ID: "run-4", Branch: "flow/fix/run-4", Named: "feature/PROJ-1", Repositories: []workspaceRepo{{repository: repository{Name: "api", URL: "https://github.com/acme/api", Base: "main"}}}}
	got := withWorkspace("Fix it.", ws)
	for _, want := range []string{`on the branch "feature/PROJ-1"`, "on origin's feature/PROJ-1 as the run first checked it out, or if origin didn't have it then, branched from main", "git push origin feature/PROJ-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "run-4") {
		t.Errorf("the prompt names the worker's branch:\n%s", got)
	}
}

// A value is made a branch name: spaces as underscores, what git doesn't
// take left out, the whole tidied as git would have it.
func TestValuesAreMadeBranchSafe(t *testing.T) {
	for in, want := range map[string]string{
		"Fix: the login (urgent)!": "Fix_the_login_urgent",
		"  two   spaces ":          "two_spaces",
		"Ünnep ~^:?*[\\ day":       "nnep_day",
		"PROJ-123":                 "PROJ-123",
		"feature/x.y_z":            "feature/x.y_z",
	} {
		if got := branchSafe(in); got != want {
			t.Errorf("branchSafe(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"feature/PROJ-1":       "feature/PROJ-1",
		"feature//x":           "feature/x",
		"feature/.hidden/../x": "feature/hidden/x",
		"-x/y.":                "x/y",
		"-./x":                 "x",
		"x/y.lock":             "x/y",
		"x/y.lock/":            "x/y",
		"_x":                   "x",
	} {
		if got := tidyBranch(in); got != want || !gitBranch(got) {
			t.Errorf("tidyBranch(%q) = %q, want %q", in, got, want)
		}
	}
	if got := tidyBranch("feature/" + strings.Repeat("a", 300)); len(got) != 200 || !gitBranch(got) {
		t.Errorf("a long name is cut to %d characters: %q", len(got), got)
	}
}
