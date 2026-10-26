package main

import (
	"database/sql"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestScanOwner(t *testing.T) {
	id := func(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }
	for _, c := range []struct {
		user, team sql.NullInt64
		want       flowOwner
	}{
		{sql.NullInt64{}, sql.NullInt64{}, flowOwner{Kind: ownerGlobal}},
		{id(3), sql.NullInt64{}, flowOwner{Kind: ownerUser, ID: 3, Name: "Ann"}},
		{sql.NullInt64{}, id(7), flowOwner{Kind: ownerTeam, ID: 7, Name: "Ann"}},
		// A flow is only ever someone's or a team's; someone's wins.
		{id(3), id(7), flowOwner{Kind: ownerUser, ID: 3, Name: "Ann"}},
	} {
		if got := scanOwner(c.user, c.team, "Ann"); got != c.want {
			t.Errorf("scanOwner(%v, %v) = %+v, want %+v", c.user, c.team, got, c.want)
		}
	}
}

// Nobody signed in and admins see and change every flow; anyone else only
// the global ones, their own and their teams', and sees the ones their roles
// decide reviews of besides.
func TestFlowConditions(t *testing.T) {
	for _, u := range []*user{nil, {ID: 1, Admin: true}} {
		for name, f := range map[string]func(*user) (string, []any){"editableFlows": editableFlows, "visibleFlows": visibleFlows} {
			if cond, args := f(u); cond != "TRUE" || args != nil {
				t.Errorf("%s(%+v) = %q, %v, want TRUE without arguments", name, u, cond, args)
			}
		}
	}
	u := &user{ID: 42}
	edit, editArgs := editableFlows(u)
	if !reflect.DeepEqual(editArgs, []any{int64(42), int64(42)}) || strings.Count(edit, "?") != len(editArgs) {
		t.Errorf("editableFlows = %q, %v", edit, editArgs)
	}
	for _, part := range []string{"f.owner_user_id IS NULL AND f.owner_team_id IS NULL", "f.owner_user_id = ?", "team_members"} {
		if !strings.Contains(edit, part) {
			t.Errorf("editableFlows lacks %q: %q", part, edit)
		}
	}
	see, seeArgs := visibleFlows(u)
	if !reflect.DeepEqual(seeArgs, []any{int64(42), int64(42), int64(42)}) || strings.Count(see, "?") != len(seeArgs) {
		t.Errorf("visibleFlows = %q, %v", see, seeArgs)
	}
	if !strings.Contains(see, edit) || !strings.Contains(see, "user_roles") || !strings.Contains(see, "o.human") {
		t.Errorf("visibleFlows isn't the editable ones or the ones with reviews the roles decide: %q", see)
	}
	// Or a version published, or one a run waiting on a person runs, names them.
	for _, part := range []string{"version_roles", "f.published_version_id", "s.status = 'waiting'"} {
		if !strings.Contains(see, part) {
			t.Errorf("visibleFlows lacks %q: %q", part, see)
		}
	}
}

// Reading needs only review access, and so do deciding a review and
// starring a flow; anything else changes the flow.
func TestNeededAccess(t *testing.T) {
	for _, c := range []struct {
		method, path, want string
	}{
		{"GET", "/api/flows/1", accessReview},
		{"HEAD", "/api/flows/1", accessReview},
		{"GET", "/api/runs/3/steps/4", accessReview},
		{"PATCH", "/api/runs/3/steps/4", accessReview},
		{"PATCH", "/api/runs/3/steps/4/", accessEdit},
		{"PATCH", "/api/runs/3/steps/4/files", accessEdit},
		{"PATCH", "/api/runs/3/steps/x", accessEdit},
		{"PATCH", "/api/runs/3", accessEdit},
		{"PATCH", "/api/flows/1", accessEdit},
		{"POST", "/api/runs/3/steps/4", accessEdit},
		{"DELETE", "/api/runs/3/steps/4", accessEdit},
		{"PUT", "/api/flows/1/owner", accessEdit},
		{"PUT", "/api/flows/1/tags", accessEdit},
		{"PUT", "/api/flows/1/favourite", accessReview},
		{"DELETE", "/api/flows/1/favourite", accessReview},
		{"DELETE", "/api/flows/1/favourite/x", accessEdit},
		{"POST", "/api/runs", accessEdit},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := neededAccess(r); got != c.want {
			t.Errorf("neededAccess(%s %s) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

// The guard finds the flows a body is about by the ids it names: a flow, a
// process or an object, one or many, sources and targets only where they are
// processes, and the flow a subflow process's settings run.
func TestBodyRefs(t *testing.T) {
	for _, c := range []struct {
		path, body string
		want       map[string][]int64
	}{
		{"/api/processes", `{"flow": 1, "name": "Fetch"}`, map[string][]int64{"flows": {1}}},
		{"/api/flows", `{"from": 2}`, map[string][]int64{"flows": {2}}},
		{"/api/objects", `{"parent": 5, "board": 6}`, map[string][]int64{"processes": {5, 6}}},
		{"/api/links", `{"process": 5, "object": 9}`, map[string][]int64{"processes": {5}, "objects": {9}}},
		{"/api/paste", `{"processes": [1, 2], "objects": [3]}`, map[string][]int64{"processes": {1, 2}, "objects": {3}}},
		{"/api/connections", `{"source": 1, "target": 2}`, map[string][]int64{"processes": {1, 2}}},
		{"/api/start", `{"source": 1}`, map[string][]int64{"processes": {1}}},
		{"/api/end", `{"target": 2}`, map[string][]int64{"processes": {2}}},
		// Elsewhere a source or target isn't a process.
		{"/api/links", `{"source": 1, "target": 2}`, map[string][]int64{}},
		{"/api/processes/4", `{"settings": {"flow": 8}}`, map[string][]int64{"flows": {8}}},
		{"/api/processes/4", `{"flow": 1, "settings": {"flow": 8}}`, map[string][]int64{"flows": {1, 8}}},
		{"/api/processes/4", `{"settings": {"model": "x"}}`, map[string][]int64{}},
		{"/api/processes/4", `{"settings": "flow"}`, map[string][]int64{}},
		// Values that aren't ids name nothing.
		{"/api/processes", `{"flow": "1", "objects": ["a"], "process": 1.5}`, map[string][]int64{}},
		{"/api/processes", `{}`, map[string][]int64{}},
		{"/api/processes", `[1, 2]`, nil},
		{"/api/processes", `not json`, nil},
		{"/api/processes", ``, nil},
	} {
		got := bodyRefs(c.path, []byte(c.body))
		// The fields are read in flowFields' order, a map's, so the ids
		// of a table come in any order; the guard checks each alike.
		for _, ids := range got {
			slices.Sort(ids)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("bodyRefs(%s, %s) = %v, want %v", c.path, c.body, got, c.want)
		}
	}
}

// Every table bodyRefs and the guard's query fields name has a query giving
// its flow, or the guard would look up nothing.
func TestFlowFieldsHaveQueries(t *testing.T) {
	for field, table := range flowFields {
		if flowOfTable[table] == "" {
			t.Errorf("field %q names table %q, which has no query", field, table)
		}
	}
	if flowOfTable["runs"] == "" {
		t.Error("runs have no query, which the files route needs")
	}
}

func TestFlowPaths(t *testing.T) {
	for _, c := range []struct {
		path  string
		match bool
		id    string
	}{
		{"/api/flows/12", true, "12"},
		{"/api/flows/12/owner", true, "12"},
		{"/api/runs/3/steps/4", true, "3"},
		{"/api/flows", false, ""},
		{"/api/flows/12x", false, ""},
		{"/api/flowsx/12", false, ""},
	} {
		matched := false
		for _, p := range flowPaths {
			if m := p.pattern.FindStringSubmatch(c.path); m != nil {
				matched = true
				if m[1] != c.id {
					t.Errorf("%s gives id %q, want %q", c.path, m[1], c.id)
				}
			}
		}
		if matched != c.match {
			t.Errorf("%s matched = %v, want %v", c.path, matched, c.match)
		}
	}
}
