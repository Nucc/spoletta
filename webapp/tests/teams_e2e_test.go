package tests

// Teams and owners: people make teams, flows belong to someone, a team or
// everyone, and only those they belong to see them.

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
)

type teamsTeam struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Members []struct {
		ID int64 `json:"id"`
	} `json:"members"`
}

func (t teamsTeam) has(id int64) bool {
	return slices.ContainsFunc(t.Members, func(m struct {
		ID int64 `json:"id"`
	}) bool {
		return m.ID == id
	})
}

func teamsFlows(a *app) []int64 {
	a.t.Helper()
	var fs []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &fs)
	var ids []int64
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	return ids
}

// Admins make team managers, who make teams; anyone sees the teams, but
// nobody else makes or changes them.
func TestTeamsManagersMakeTeams(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	bobRow, carlRow := usersAdd(a, bob.email), usersAdd(a, carl.email)
	a.call("PATCH", fmt.Sprintf("/api/users/%d", bobRow.ID), map[string]any{"teamManager": true}, http.StatusOK, nil)

	usersSignIn(a, p, bob)
	var tm teamsTeam
	a.call("POST", "/api/teams", map[string]any{"name": " Payments ", "members": []int64{bobRow.ID, carlRow.ID, carlRow.ID}}, http.StatusCreated, &tm)
	if tm.Name != "Payments" || len(tm.Members) != 2 || !tm.has(carlRow.ID) {
		t.Errorf("made %+v", tm)
	}
	a.refused("POST", "/api/teams", map[string]any{"name": "Payments"}, http.StatusConflict, "another team")
	a.refused("POST", "/api/teams", map[string]any{"name": " "}, http.StatusBadRequest, "name is required")
	a.refused("POST", "/api/teams", map[string]any{"name": "Ghosts", "members": []int64{1 << 40}}, http.StatusBadRequest, "isn't anyone")
	a.call("PATCH", fmt.Sprintf("/api/teams/%d", tm.ID), map[string]any{"members": []int64{bobRow.ID}}, http.StatusOK, &tm)
	if len(tm.Members) != 1 || tm.has(carlRow.ID) {
		t.Errorf("changed to %+v", tm)
	}
	// Managers see people, to pick members from.
	a.call("GET", "/api/users", nil, http.StatusOK, nil)

	usersSignIn(a, p, carl)
	var teams []teamsTeam
	a.call("GET", "/api/teams", nil, http.StatusOK, &teams)
	if !slices.ContainsFunc(teams, func(x teamsTeam) bool { return x.ID == tm.ID }) {
		t.Errorf("carl sees %+v", teams)
	}
	a.refused("POST", "/api/teams", map[string]any{"name": "Mine"}, http.StatusForbidden, "team manager")
	a.refused("PATCH", fmt.Sprintf("/api/teams/%d", tm.ID), map[string]any{"name": "Mine"}, http.StatusForbidden, "team manager")
	a.refused("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusForbidden, "team manager")
	a.refused("GET", "/api/users", nil, http.StatusForbidden, "team manager")

	// A team owning a flow stays until the flow goes elsewhere.
	usersSignIn(a, p, bob)
	f := a.newFlow("Payouts")
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "team", "id": tm.ID}, http.StatusOK, nil)
	a.refused("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusConflict, "owns 1")
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "user", "id": bobRow.ID}, http.StatusOK, nil)
	a.call("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusNoContent, nil)
}

// A flow is its maker's, whom only they see; given to a team, its members
// see it; made global, everyone does. Whatever is in a flow nobody else sees
// is as if it didn't exist.
func TestTeamsFlowOwners(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	bobRow, carlRow := usersAdd(a, bob.email), usersAdd(a, carl.email)
	var tm, other teamsTeam
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Billing", bobRow.ID), "members": []int64{bobRow.ID}}, http.StatusCreated, &tm)
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Other", bobRow.ID)}, http.StatusCreated, &other)

	usersSignIn(a, p, bob)
	var f flow
	a.call("POST", "/api/flows", map[string]any{"name": "Invoices", "owner": map[string]any{"kind": "user", "id": bobRow.ID}}, http.StatusCreated, &f)
	if f.Owner.Kind != "user" || f.Owner.ID != bobRow.ID || f.Owner.Name != "Bob" {
		t.Errorf("a new flow is owned by %+v", f.Owner)
	}
	proc := a.agent(f.ID, "Draft", "Draft it.")
	obj := a.newObject(f.ID, "Invoice")
	a.output(proc.ID, obj.ID)
	var mine flow
	a.call("POST", "/api/flows", map[string]any{"name": "Elsewhere", "owner": map[string]any{"kind": "user", "id": bobRow.ID}}, http.StatusCreated, &mine)
	rn := a.startRun(f.ID)

	usersSignIn(a, p, carl)
	if slices.Contains(teamsFlows(a), f.ID) {
		t.Errorf("carl sees bob's flow")
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", fmt.Sprintf("/api/flows/%d", f.ID), nil},
		{"PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"name": "Mine"}},
		{"GET", fmt.Sprintf("/api/processes?flow=%d", f.ID), nil},
		{"PATCH", fmt.Sprintf("/api/processes/%d", proc.ID), map[string]any{"name": "Mine"}},
		{"DELETE", fmt.Sprintf("/api/objects/%d", obj.ID), nil},
		{"POST", "/api/processes", map[string]any{"flow": f.ID, "name": "Sneak", "kind": "agent"}},
		{"POST", fmt.Sprintf("/api/objects/%d/states", obj.ID), map[string]any{"name": "Paid"}},
		{"GET", fmt.Sprintf("/api/runs/%d", rn.ID), nil},
		{"POST", "/api/runs", map[string]any{"flow": f.ID}},
		{"POST", "/api/paste", map[string]any{"from": f.ID, "processes": []int64{proc.ID}, "flow": f.ID}},
	} {
		a.refused(c.method, c.path, c.body, http.StatusNotFound, "not found")
	}
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "global"}, http.StatusNotFound, "not found")

	// Given to Bob's team, its members see it; Carl joins, and does too.
	usersSignIn(a, p, bob)
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "team", "id": other.ID}, http.StatusForbidden, "a team you are in")
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "user", "id": carlRow.ID}, http.StatusForbidden, "only an admin")
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "team", "id": tm.ID}, http.StatusOK, &f)
	if f.Owner.Kind != "team" || f.Owner.ID != tm.ID || f.Owner.Name != tm.Name {
		t.Errorf("given to %+v", f.Owner)
	}
	usersSignIn(a, p, carl)
	if slices.Contains(teamsFlows(a), f.ID) {
		t.Errorf("carl sees a team's flow he isn't in")
	}
	usersSignIn(a, p, usersAda)
	a.call("PATCH", fmt.Sprintf("/api/teams/%d", tm.ID), map[string]any{"members": []int64{bobRow.ID, carlRow.ID}}, http.StatusOK, nil)
	usersSignIn(a, p, carl)
	if !slices.Contains(teamsFlows(a), f.ID) {
		t.Errorf("carl doesn't see his team's flow")
	}
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", proc.ID), map[string]any{"name": "Draft it"}, http.StatusOK, nil)

	// Made global, a member can't take it back; only an admin does.
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "global"}, http.StatusOK, &f)
	if f.Owner.Kind != "global" {
		t.Errorf("made global: %+v", f.Owner)
	}
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "user", "id": carlRow.ID}, http.StatusForbidden, "only an admin")
	usersSignIn(a, p, usersAda)
	if !slices.Contains(teamsFlows(a), mine.ID) {
		t.Errorf("the admin doesn't see bob's own flow")
	}
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "user", "id": carlRow.ID}, http.StatusOK, &f)
	if f.Owner.Kind != "user" || f.Owner.ID != carlRow.ID {
		t.Errorf("given to carl: %+v", f.Owner)
	}
}

// An admin removes someone: their flows go to the admin, they can't sign in
// until added back, and their runs still say they started them.
func TestTeamsRemoveSomeone(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	var me usersMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	a.refused("DELETE", fmt.Sprintf("/api/users/%d", me.User.ID), nil, http.StatusBadRequest, "yourself")
	bob := usersNewPerson("Bob")
	row := usersAdd(a, bob.email)

	usersSignIn(a, p, bob)
	a.refused("DELETE", fmt.Sprintf("/api/users/%d", me.User.ID), nil, http.StatusForbidden, "only an admin")
	f := a.newFlow("Bob's")
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	a.waitStep(rn.ID, "Review", stepWaiting)
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)

	usersSignIn(a, p, usersAda)
	a.call("DELETE", fmt.Sprintf("/api/users/%d", row.ID), nil, http.StatusNoContent, nil)
	a.refused("DELETE", fmt.Sprintf("/api/users/%d", row.ID), nil, http.StatusNotFound, "no one")
	var got flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusOK, &got)
	if got.Owner.Kind != "user" || got.Owner.ID != me.User.ID {
		t.Errorf("bob's flow went to %+v", got.Owner)
	}
	if d := a.getRun(rn.ID); d.StartedBy != "Bob" {
		t.Errorf("the run says it was started by %q", d.StartedBy)
	}
	var people []usersPersonRow
	a.call("GET", "/api/users", nil, http.StatusOK, &people)
	if slices.ContainsFunc(people, func(p usersPersonRow) bool { return p.ID == row.ID }) {
		t.Errorf("bob is still listed")
	}
	if _, status, _ := usersTrySignIn(a, p, bob); status != http.StatusForbidden {
		t.Errorf("bob signing in got %d", status)
	}

	// Added back, he is who he was.
	if again := usersAdd(a, bob.email); again.ID != row.ID {
		t.Errorf("added back as %d, was %d", again.ID, row.ID)
	}
	usersSignIn(a, p, bob)
}

// Someone holding the role a flow's human object names sees the flow and its
// runs, to decide its reviews, but changes nothing in it.
func TestTeamsReviewersSeeFlows(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email, "Editor")
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)

	usersSignIn(a, p, bob)
	var fs []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &fs)
	i := slices.IndexFunc(fs, func(f flow) bool { return f.ID == b.flow.ID })
	if i < 0 || fs[i].Access != "review" {
		t.Fatalf("bob sees %+v", fs)
	}
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", b.flow.ID), nil, http.StatusOK, nil)
	a.refused("PATCH", fmt.Sprintf("/api/flows/%d", b.flow.ID), map[string]any{"name": "Mine"}, http.StatusForbidden, "only decide")
	a.refused("POST", "/api/runs", map[string]any{"flow": b.flow.ID}, http.StatusForbidden, "only decide")
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	if r := onlyStep(t, a.finish(rn.ID), "Review"); r.DecidedBy != "Bob" {
		t.Errorf("decided by %q", r.DecidedBy)
	}
}

// A new flow is its maker's team's when they are in one only; otherwise
// theirs, unless they give it to a team of theirs or everyone as they make it.
func TestTeamsNewFlowOwner(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	bobRow := usersAdd(a, bob.email)
	var one, two, other teamsTeam
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("One", bobRow.ID), "members": []int64{bobRow.ID}}, http.StatusCreated, &one)
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Two", bobRow.ID)}, http.StatusCreated, &two)
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Other", bobRow.ID)}, http.StatusCreated, &other)

	usersSignIn(a, p, bob)
	if f := a.newFlow("Team's"); f.Owner.Kind != "team" || f.Owner.ID != one.ID {
		t.Errorf("in one team, a new flow is owned by %+v", f.Owner)
	}
	usersSignIn(a, p, usersAda)
	a.call("PATCH", fmt.Sprintf("/api/teams/%d", two.ID), map[string]any{"members": []int64{bobRow.ID}}, http.StatusOK, nil)
	usersSignIn(a, p, bob)
	if f := a.newFlow("Mine"); f.Owner.Kind != "user" || f.Owner.ID != bobRow.ID {
		t.Errorf("in two teams, a new flow is owned by %+v", f.Owner)
	}
	var f flow
	a.call("POST", "/api/flows", map[string]any{"name": "Two's", "owner": map[string]any{"kind": "team", "id": two.ID}}, http.StatusCreated, &f)
	if f.Owner.Kind != "team" || f.Owner.ID != two.ID {
		t.Errorf("given to two, owned by %+v", f.Owner)
	}
	a.call("POST", "/api/flows", map[string]any{"name": "Everyone's", "owner": map[string]any{"kind": "global"}}, http.StatusCreated, &f)
	if f.Owner.Kind != "global" {
		t.Errorf("made global, owned by %+v", f.Owner)
	}
	a.refused("POST", "/api/flows", map[string]any{"name": "Theirs", "owner": map[string]any{"kind": "team", "id": other.ID}}, http.StatusForbidden, "a team you are in")
}
