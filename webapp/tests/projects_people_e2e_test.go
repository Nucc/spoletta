package tests

// People in projects: the identity provider's groups, read as people sign
// in and again while their sessions live, letting them into the projects
// the groups are given to; teams and roles, each a project's; the inbox,
// across every project someone may enter; who is told of a step, only of
// its own project; and the Activity page and the flow list's health, each
// a project's. See users.go, teams.go, user_roles.go, inbox.go, notify.go
// and activity.go.

import (
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// A webapp people sign in to as usersApp's, whose provider names their
// groups in the claim "groups", in the ID token unless a test says
// otherwise, with the environment variables in extra.
func peopleApp(t *testing.T, extra ...string) (*app, *fakeProvider) {
	t.Helper()
	p := newFakeProvider(t)
	p.groupsClaim, p.groupsIn, p.userinfo = "groups", "id_token", true
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, append([]string{
		"OIDC_ISSUER=" + p.srv.URL,
		"OIDC_CLIENT_ID=" + usersClientID,
		"OIDC_CLIENT_SECRET=" + usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com",
		"OIDC_GROUPS_CLAIM=groups",
	}, extra...)...)
	a.worker = fw
	return a, p
}

// Says which groups the provider puts the person in from now: a string, a
// list of strings, or nil for none.
func peopleInGroups(p *fakeProvider, who usersPerson, groups any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if groups == nil {
		delete(p.groups, who.subject)
		return
	}
	p.groups[who.subject] = groups
}

// A group name no other test uses.
func peopleGroup(name string) string { return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()) }

// The groups the person is in, as the webapp last read them.
func peopleGroupsOf(t *testing.T, db *sql.DB, email string) []string {
	t.Helper()
	rows, err := db.Query("SELECT g.group_name FROM user_groups g JOIN users u ON u.id = g.user_id WHERE u.email = ? ORDER BY 1", email)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	groups := []string{}
	for rows.Next() {
		var g string
		rows.Scan(&g)
		groups = append(groups, g)
	}
	return groups
}

// The status of a request the app sends.
func peopleStatus(a *app, method, path string, body any) int {
	a.t.Helper()
	status, _ := a.do(method, path, body)
	return status
}

// Waits until the request gets the status, failing the test if it doesn't
// soon; the webapp reads groups again every few seconds.
func peopleEventually(a *app, what string, method, path string, want int) {
	a.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		status := peopleStatus(a, method, path, nil)
		if status == want {
			return
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("%s: %s %s is still %d, want %d", what, method, path, status, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Makes the person's groups due to be read again, as an hour after they
// last were.
func peopleGroupsDue(t *testing.T, db *sql.DB, email string) {
	t.Helper()
	if _, err := db.Exec("UPDATE users SET groups_read_at = CURRENT_TIMESTAMP(3) - INTERVAL 2 HOUR WHERE email = ?", email); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions s JOIN users u ON u.id = s.user_id SET s.groups_check_at = CURRENT_TIMESTAMP(3)
		WHERE u.email = ? AND s.groups_check_at IS NOT NULL`, email); err != nil {
		t.Fatal(err)
	}
}

// A project with a flow everyone in it sees, made by the admin, who the
// app's requests are from then, in the project.
func peopleProjectWithFlow(a *app, p *fakeProvider, name string) (projectRow, flow) {
	a.t.Helper()
	usersSignIn(a, p, usersAda)
	a.project = 0
	pr := newProject(a, name)
	a.project = pr.ID
	f := a.newFlow(fmt.Sprintf("%s flow %d", name, time.Now().UnixNano()))
	usersShare(a, f.ID)
	return pr, f
}

// A group the identity provider puts someone in, given to a project, lets
// them in at once, at the group's level, as they sign in with it; taking
// the group away, or their next sign-in no longer carrying it, takes them
// out.
func TestProjectsPeopleGroupsFromSignIn(t *testing.T) {
	a, p := peopleApp(t)
	db := secretsDB(t)
	pr, shared := peopleProjectWithFlow(a, p, "Grouped")
	group := peopleGroup("engineering")
	gina := usersNewPerson("Gina")
	peopleInGroups(p, gina, []string{group, "", group, strings.Repeat("x", 300)})
	asGina := inboxAs(a, p, gina)
	asGina.project = pr.ID
	if got := peopleGroupsOf(t, db, gina.email); !slices.Equal(got, []string{group}) {
		t.Errorf("gina's groups = %q, want %q alone: no empty name, none repeated, none too long", got, group)
	}
	asGina.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")

	// Given as viewers, she sees it at once, and changes nothing.
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", pr.ID), []map[string]any{{"group": group, "level": "viewer"}}, http.StatusOK, nil)
	if names := projectFlowNames(asGina); !slices.Contains(names, shared.Name) {
		t.Errorf("gina sees %q", names)
	}
	asGina.refused("POST", "/api/flows", map[string]any{"name": "Nope"}, http.StatusForbidden, "you only see this project")
	// As editors, she changes it.
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", pr.ID), []map[string]any{{"group": group, "level": "editor"}}, http.StatusOK, nil)
	asGina.call("POST", "/api/flows", map[string]any{"name": fmt.Sprintf("Gina's %d", time.Now().UnixNano())}, http.StatusCreated, nil)

	// Taking the group's access away takes hers.
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", pr.ID), []any{}, http.StatusOK, nil)
	asGina.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")

	// Given again, she is in; out of the group at the provider, she is out
	// as she next signs in.
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", pr.ID), []map[string]any{{"group": group, "level": "editor"}}, http.StatusOK, nil)
	asGina.call("GET", "/api/flows", nil, http.StatusOK, nil)
	peopleInGroups(p, gina, nil)
	asGina = inboxAs(a, p, gina)
	asGina.project = pr.ID
	asGina.refused("GET", "/api/flows", nil, http.StatusForbidden, "no access to this project")
	if got := peopleGroupsOf(t, db, gina.email); len(got) != 0 {
		t.Errorf("gina's groups once out of them = %q", got)
	}
}

// The claim names one group as a string or several as a list, in the ID
// token or, where the ID token leaves it out, in userinfo; one that is
// neither stops the sign-in, saying so.
func TestProjectsPeopleGroupsClaimShapes(t *testing.T) {
	a, p := peopleApp(t)
	db := secretsDB(t)
	pr, shared := peopleProjectWithFlow(a, p, "Claims")
	group := peopleGroup("support")
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", pr.ID), []map[string]any{{"group": group, "level": "editor"}}, http.StatusOK, nil)
	for _, c := range []struct {
		name, in string
		claim    any
	}{
		{"a string in the ID token", "id_token", group},
		{"a list in the ID token", "id_token", []string{"elsewhere", group}},
		{"a string in userinfo", "userinfo", group},
		{"a list in userinfo", "userinfo", []string{group, "elsewhere"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a.t = t
			p.mu.Lock()
			p.groupsIn = c.in
			p.mu.Unlock()
			who := usersNewPerson("Claimed")
			peopleInGroups(p, who, c.claim)
			as := inboxAs(a, p, who)
			as.project = pr.ID
			if names := projectFlowNames(as); !slices.Contains(names, shared.Name) {
				t.Errorf("%s sees %q", who.email, names)
			}
			if got := peopleGroupsOf(t, db, who.email); !slices.Contains(got, group) {
				t.Errorf("groups read = %q", got)
			}
		})
	}
	a.t = t
	p.mu.Lock()
	p.groupsIn = "id_token"
	p.mu.Unlock()
	odd := usersNewPerson("Odd")
	peopleInGroups(p, odd, map[string]any{"not": "a group"})
	if _, status, _ := usersTrySignIn(a, p, odd); status != http.StatusBadGateway {
		t.Errorf("a claim that is neither a string nor a list signed in with %d, want 502", status)
	}
}

// Signing in with Google carries no groups, so it leaves the person in
// none; nor does the company's provider without OIDC_GROUPS_CLAIM.
func TestProjectsPeopleGroupsNoneWithoutTheClaim(t *testing.T) {
	company, google := newFakeProvider(t), newFakeProvider(t)
	for _, p := range []*fakeProvider{company, google} {
		p.groupsClaim, p.groupsIn = "groups", "id_token"
	}
	fw := newFakeWorker(t)
	a := startApp(t, fw.url,
		"OIDC_ISSUER="+company.srv.URL,
		"OIDC_CLIENT_ID="+usersClientID,
		"OIDC_CLIENT_SECRET="+usersClientSecret,
		"OIDC_GROUPS_CLAIM=groups",
		"GOOGLE_ISSUER="+google.srv.URL,
		"GOOGLE_CLIENT_ID="+usersClientID,
		"GOOGLE_CLIENT_SECRET="+usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com")
	a.worker = fw
	db := secretsDB(t)
	group := peopleGroup("finance")
	gus := usersNewPerson("Gus")
	gus.hd = "example.org"
	peopleInGroups(company, gus, []string{group})
	peopleInGroups(google, gus, []string{group})
	if _, status, _ := usersTrySignInWith(a, company, "oidc", gus); status != http.StatusNotFound {
		t.Fatalf("signing in with the company's provider ended with %d", status)
	}
	if got := peopleGroupsOf(t, db, gus.email); !slices.Equal(got, []string{group}) {
		t.Errorf("groups from the company's provider = %q", got)
	}
	if _, status, _ := usersTrySignInWith(a, google, "google", gus); status != http.StatusNotFound {
		t.Fatalf("signing in with Google ended with %d", status)
	}
	if got := peopleGroupsOf(t, db, gus.email); len(got) != 0 {
		t.Errorf("groups once signed in with Google = %q, want none", got)
	}

	// Without the variable, the company's provider carries none either.
	b, p := usersApp(t)
	p.groupsClaim, p.groupsIn = "groups", "id_token"
	peopleInGroups(p, gus, []string{group})
	usersSignIn(b, p, gus)
	if got := peopleGroupsOf(t, db, gus.email); len(got) != 0 {
		t.Errorf("groups without OIDC_GROUPS_CLAIM = %q, want none", got)
	}
}

// A live session's groups are read again from the provider with its refresh
// token, kept sealed, so someone taken out of a group at the provider loses
// what it gave them without signing in again; a session the provider
// refuses to refresh ends.
func TestProjectsPeopleGroupsReadAgainWithRefreshTokens(t *testing.T) {
	a, p := peopleApp(t)
	db := secretsDB(t)
	p.mu.Lock()
	p.refresh, p.refreshIDToken, p.scopes = true, true, []string{"openid", "email", "profile", "offline_access"}
	p.mu.Unlock()
	pr, _ := peopleProjectWithFlow(a, p, "Refreshed")
	group := peopleGroup("platform")
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", pr.ID), []map[string]any{{"group": group, "level": "editor"}}, http.StatusOK, nil)
	gina := usersNewPerson("Gina")
	peopleInGroups(p, gina, []string{group})
	asGina := inboxAs(a, p, gina)
	asGina.project = pr.ID
	asGina.call("GET", "/api/flows", nil, http.StatusOK, nil)
	p.mu.Lock()
	scope := p.scope
	p.mu.Unlock()
	if !strings.Contains(scope, "offline_access") {
		t.Errorf("the sign-in asked for %q, want offline_access, which the provider lists", scope)
	}
	if stored := secretsStored(t, db, `SELECT s.refresh_token FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE u.email = ? ORDER BY s.created_at DESC LIMIT 1`, gina.email); !strings.HasPrefix(stored, "sealed:v1:") {
		t.Errorf("the session keeps its refresh token as %q, want it sealed", stored)
	}

	// Out of the group at the provider, she is out once her groups are read
	// again, still signed in.
	peopleInGroups(p, gina, nil)
	peopleGroupsDue(t, db, gina.email)
	peopleEventually(asGina, "her groups being read again", "GET", "/api/flows", http.StatusForbidden)
	asGina.call("GET", "/api/me", nil, http.StatusOK, nil)
	p.mu.Lock()
	refreshes := p.refreshes
	p.mu.Unlock()
	if refreshes == 0 {
		t.Errorf("no refresh was asked for")
	}

	// In it again, as she signs in; a refresh the provider refuses ends the
	// session.
	peopleInGroups(p, gina, []string{group})
	asGina = inboxAs(a, p, gina)
	asGina.project = pr.ID
	asGina.call("GET", "/api/flows", nil, http.StatusOK, nil)
	p.mu.Lock()
	p.refuseRefresh = true
	p.mu.Unlock()
	peopleGroupsDue(t, db, gina.email)
	peopleEventually(asGina, "the session the provider no longer refreshes ending", "GET", "/api/me", http.StatusUnauthorized)
}

// Without a refresh token, a still good access token reads the groups again
// from userinfo; once none is left that works, the session of someone with
// groups ends when they are older than GROUPS_MAX_AGE, so they sign in
// again, while someone in no group stays signed in.
func TestProjectsPeopleGroupsMaxAge(t *testing.T) {
	a, p := peopleApp(t)
	db := secretsDB(t)
	p.mu.Lock()
	p.groupsIn, p.expiresIn = "userinfo", 3600
	p.mu.Unlock()
	pr, _ := peopleProjectWithFlow(a, p, "Aged")
	group := peopleGroup("ops")
	a.call("PUT", fmt.Sprintf("/api/projects/%d/groups", pr.ID), []map[string]any{{"group": group, "level": "editor"}}, http.StatusOK, nil)
	olly := usersNewPerson("Olly")
	peopleInGroups(p, olly, group)
	asOlly := inboxAs(a, p, olly)
	asOlly.project = pr.ID
	asOlly.call("GET", "/api/flows", nil, http.StatusOK, nil)

	// The access token still good, userinfo says he is out of the group.
	peopleInGroups(p, olly, nil)
	peopleGroupsDue(t, db, olly.email)
	peopleEventually(asOlly, "his groups being read again from userinfo", "GET", "/api/flows", http.StatusForbidden)

	// In it again; the access token gone, nothing reads them again, and
	// once they are older than GROUPS_MAX_AGE the session is over.
	peopleInGroups(p, olly, []string{group})
	asOlly = inboxAs(a, p, olly)
	asOlly.project = pr.ID
	asOlly.call("GET", "/api/flows", nil, http.StatusOK, nil)
	if _, err := db.Exec(`UPDATE sessions s JOIN users u ON u.id = s.user_id SET s.access_expires_at = CURRENT_TIMESTAMP(3) - INTERVAL 1 SECOND
		WHERE u.email = ?`, olly.email); err != nil {
		t.Fatal(err)
	}
	peopleGroupsDue(t, db, olly.email)
	deadline := time.Now().Add(30 * time.Second)
	for {
		var tokens int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sessions s JOIN users u ON u.id = s.user_id
			WHERE u.email = ? AND (s.access_token IS NOT NULL OR s.groups_check_at IS NOT NULL)`, olly.email).Scan(&tokens); err != nil {
			t.Fatal(err)
		}
		if tokens == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session whose access token expired still keeps it")
		}
		time.Sleep(200 * time.Millisecond)
	}
	asOlly.call("GET", "/api/flows", nil, http.StatusOK, nil)
	if _, err := db.Exec("UPDATE users SET groups_read_at = CURRENT_TIMESTAMP(3) - INTERVAL 25 HOUR WHERE email = ?", olly.email); err != nil {
		t.Fatal(err)
	}
	asOlly.refused("GET", "/api/me", nil, http.StatusUnauthorized, "sign in first")
	asOlly = inboxAs(a, p, olly)
	asOlly.project = pr.ID
	asOlly.call("GET", "/api/flows", nil, http.StatusOK, nil)

	// GROUPS_MAX_AGE says how long: a few seconds here, after which only
	// the session of someone with groups is over.
	b, q := peopleApp(t, "GROUPS_MAX_AGE=3s")
	nell := usersNewPerson("Nell")
	peopleInGroups(q, nell, []string{group})
	asNell := inboxAs(b, q, nell)
	asNobody := inboxAs(b, q, usersNewPerson("Nobody"))
	asNell.call("GET", "/api/me", nil, http.StatusOK, nil)
	time.Sleep(4 * time.Second)
	asNell.refused("GET", "/api/me", nil, http.StatusUnauthorized, "sign in first")
	asNobody.call("GET", "/api/me", nil, http.StatusOK, nil)
}

type peopleTeam struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Members []struct {
		ID int64 `json:"id"`
	} `json:"members"`
}

// Teams and roles are each project's: the same names in two projects are
// two teams and two roles, each listed, changed and held in its own
// project alone, of people who may enter it. A project's owner gives its
// members roles and lists only those who may enter it, and adds a member
// by email.
func TestProjectsPeopleTeamsAndRolesPerProject(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	one, two := newProject(a, "Teams one"), newProject(a, "Teams two")
	bob, carl, dave, olga := usersNewPerson("Bob"), usersNewPerson("Carl"), usersNewPerson("Dave"), usersNewPerson("Olga")
	b, c, d, o := usersAdd(a, bob.email), usersAdd(a, carl.email), usersAdd(a, dave.email), usersAdd(a, olga.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", one.ID), []map[string]any{
		{"user": b.ID, "level": "editor"}, {"email": strings.ToUpper(carl.email), "level": "editor"}, {"user": o.ID, "level": "owner"},
	}, http.StatusOK, nil)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", two.ID), []map[string]any{
		{"user": b.ID, "level": "editor"}, {"user": c.ID, "level": "editor"},
	}, http.StatusOK, nil)
	a.refused("PUT", fmt.Sprintf("/api/projects/%d/members", two.ID), []map[string]any{{"email": "nobody-here@example.org", "level": "viewer"}},
		http.StatusBadRequest, "an admin of Spoletta adds people")

	// The same name in each project, of different people.
	var inOne, inTwo peopleTeam
	a.project = one.ID
	a.call("POST", "/api/teams", map[string]any{"name": "Payments", "members": []int64{b.ID}}, http.StatusCreated, &inOne)
	a.refused("POST", "/api/teams", map[string]any{"name": "Payments"}, http.StatusConflict, "another team")
	a.refused("POST", "/api/teams", map[string]any{"name": "Outsiders", "members": []int64{d.ID}}, http.StatusBadRequest, "may not enter this project")
	a.project = two.ID
	a.call("POST", "/api/teams", map[string]any{"name": "Payments", "members": []int64{c.ID}}, http.StatusCreated, &inTwo)
	var teams []peopleTeam
	a.call("GET", "/api/teams", nil, http.StatusOK, &teams)
	if len(teams) != 1 || teams[0].ID != inTwo.ID || len(teams[0].Members) != 1 || teams[0].Members[0].ID != c.ID {
		t.Errorf("the second project's teams = %+v, want its Payments of Carl alone", teams)
	}
	// Another project's team isn't there to change, or to give a flow to.
	a.refused("PATCH", fmt.Sprintf("/api/teams/%d", inOne.ID), map[string]any{"name": "Taken"}, http.StatusNotFound, "team not found")
	a.refused("DELETE", fmt.Sprintf("/api/teams/%d", inOne.ID), nil, http.StatusNotFound, "team not found")
	f := a.newFlow(fmt.Sprintf("Teamed %d", time.Now().UnixNano()))
	if status := peopleStatus(a, "PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "team", "id": inOne.ID}); status != http.StatusBadRequest {
		t.Errorf("giving a flow to another project's team = %d, want 400", status)
	}
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "team", "id": inTwo.ID}, http.StatusOK, nil)

	// Roles of the same name in each, held apart.
	var row usersPersonRow
	a.project = one.ID
	a.call("PATCH", fmt.Sprintf("/api/users/%d", b.ID), map[string]any{"roles": []string{"Finance"}}, http.StatusOK, &row)
	a.project = two.ID
	a.call("PATCH", fmt.Sprintf("/api/users/%d", b.ID), map[string]any{"roles": []string{"Legal"}}, http.StatusOK, &row)
	if !slices.Equal(row.Roles, []string{"Legal"}) {
		t.Errorf("bob's roles in the second project = %q", row.Roles)
	}
	a.refused("PATCH", fmt.Sprintf("/api/users/%d", d.ID), map[string]any{"roles": []string{"Legal"}}, http.StatusBadRequest, "may not enter this project")
	var roles []string
	a.call("GET", "/api/roles", nil, http.StatusOK, &roles)
	if !slices.Contains(roles, "Legal") || slices.Contains(roles, "Finance") {
		t.Errorf("the second project's roles = %q", roles)
	}
	a.project = one.ID
	var people []usersPersonRow
	a.call("GET", "/api/users", nil, http.StatusOK, &people)
	if i := slices.IndexFunc(people, func(p usersPersonRow) bool { return p.ID == b.ID }); i < 0 || !slices.Equal(people[i].Roles, []string{"Finance"}) {
		t.Errorf("bob in the first project = %+v", people)
	}

	// The first project's owner lists only those who may enter it, gives
	// its members roles there, and makes no one an admin.
	usersSignIn(a, prov, olga)
	a.project = one.ID
	a.call("GET", "/api/users", nil, http.StatusOK, &people)
	ids := []int64{}
	for _, p := range people {
		ids = append(ids, p.ID)
	}
	if !slices.Contains(ids, b.ID) || !slices.Contains(ids, c.ID) || !slices.Contains(ids, o.ID) || slices.Contains(ids, d.ID) {
		t.Errorf("the owner lists %v; want bob, carl and herself, not dave", ids)
	}
	a.call("PATCH", fmt.Sprintf("/api/users/%d", c.ID), map[string]any{"roles": []string{"Finance"}}, http.StatusOK, &row)
	if !slices.Equal(row.Roles, []string{"Finance"}) {
		t.Errorf("carl's roles given by the owner = %q", row.Roles)
	}
	a.refused("PATCH", fmt.Sprintf("/api/users/%d", c.ID), map[string]any{"admin": true}, http.StatusForbidden, "only an admin")
	a.refused("PATCH", fmt.Sprintf("/api/users/%d", d.ID), map[string]any{"roles": []string{"Finance"}}, http.StatusBadRequest, "may not enter this project")
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Owner's ", time.Now().UnixNano()), "members": []int64{c.ID}}, http.StatusCreated, nil)
	// She adds Dave, whom she doesn't list, by his email.
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", one.ID), []map[string]any{
		{"user": b.ID, "level": "editor"}, {"user": c.ID, "level": "editor"}, {"user": o.ID, "level": "owner"}, {"email": dave.email, "level": "viewer"},
	}, http.StatusOK, nil)
	var ms []projectMemberRow
	a.call("GET", fmt.Sprintf("/api/projects/%d/members", one.ID), nil, http.StatusOK, &ms)
	if m, ok := projectMemberOf(ms, dave.email); !ok || m.Level != "viewer" || m.Source != "manual" {
		t.Errorf("dave in the project = %+v (found %v)", m, ok)
	}

	// Bob, in each, has his roles and teams of each.
	usersSignIn(a, prov, bob)
	for project, want := range map[int64]string{one.ID: "Finance", two.ID: "Legal"} {
		a.project = project
		var me struct {
			User struct {
				Roles []string `json:"roles"`
				Teams []struct {
					ID int64 `json:"id"`
				} `json:"teams"`
			} `json:"user"`
		}
		a.call("GET", "/api/me", nil, http.StatusOK, &me)
		if !slices.Equal(me.User.Roles, []string{want}) {
			t.Errorf("bob's roles in project %d = %q, want %s", project, me.User.Roles, want)
		}
		if project == one.ID && (len(me.User.Teams) != 1 || me.User.Teams[0].ID != inOne.ID) {
			t.Errorf("bob's teams in the first project = %+v", me.User.Teams)
		}
		if project == two.ID && len(me.User.Teams) != 0 {
			t.Errorf("bob's teams in the second project = %+v", me.User.Teams)
		}
	}
}

// A flow, a skill, a task board and a credential are given only to a team
// of their own project: another project's team, which the keys refuse, is
// none, and asking for it is answered plainly, whether making it or giving
// it on.
func TestProjectsPeopleGivenOnlyToTheirProjectsTeams(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	one, two := newProject(a, "Given one"), newProject(a, "Given two")
	var theirs, ours peopleTeam
	a.project = one.ID
	a.call("POST", "/api/teams", map[string]any{"name": "Theirs"}, http.StatusCreated, &theirs)
	a.project = two.ID
	a.call("POST", "/api/teams", map[string]any{"name": "Ours"}, http.StatusCreated, &ours)
	to := map[string]any{"kind": "team", "id": theirs.ID}

	f := a.newFlow(fmt.Sprintf("Given %d", time.Now().UnixNano()))
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), to, http.StatusBadRequest, "no such team")
	a.refused("POST", "/api/skills", map[string]any{"name": skillsName("given"), "description": "Given.", "content": "Given.", "owner": to},
		http.StatusBadRequest, "no such team")
	s := skillsNew(a, "skills", map[string]any{"name": skillsName("given"), "description": "Given.", "content": "Given."})
	a.refused("PUT", fmt.Sprintf("/api/skills/%d/owner", s.ID), to, http.StatusBadRequest, "no such team")
	a.refused("POST", "/api/taskboards", map[string]any{"name": "Given", "owner": to}, http.StatusBadRequest, "no such team")
	b := taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": "Given"}, http.StatusCreated)
	a.refused("PUT", fmt.Sprintf("/api/taskboards/%d/owner", b.ID), to, http.StatusBadRequest, "no such team")
	cred := map[string]any{"name": credentialsName(t, "Given"), "baseUrl": "https://given.example.com", "auth": "header",
		"header": "X-Api-Key", "template": "{secret}", "secret": "given-secret-0123456789", "teams": []int64{theirs.ID}}
	a.refused("POST", "/api/credentials", cred, http.StatusBadRequest, "no such team")
	cred["teams"] = []int64{ours.ID}
	c := credentialsAdd(a, cred)
	cred["teams"] = []int64{ours.ID, theirs.ID}
	a.refused("PUT", fmt.Sprintf("/api/credentials/%d", c.ID), cred, http.StatusBadRequest, "no such team")

	// Their own team they are given to.
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "team", "id": ours.ID}, http.StatusOK, nil)
	a.call("PUT", fmt.Sprintf("/api/skills/%d/owner", s.ID), map[string]any{"kind": "team", "id": ours.ID}, http.StatusOK, nil)
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d/owner", b.ID), map[string]any{"kind": "team", "id": ours.ID}, http.StatusOK, nil)
}

// Someone who may enter no project is told whom to ask: the instance's
// admins, by name.
func TestProjectsPeopleWhomToAsk(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	lone := usersNewPerson("Lone")
	a.call("POST", "/api/users", map[string]any{"email": lone.email}, http.StatusCreated, nil)
	usersSignIn(a, prov, lone)
	var me struct {
		Projects []projectRow `json:"projects"`
		Admins   []string     `json:"admins"`
	}
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if len(me.Projects) != 0 || !slices.Contains(me.Admins, "Ada Lovelace") {
		t.Errorf("someone in no project is told %+v, want no projects and Ada to ask", me)
	}
	usersSignIn(a, prov, usersAda)
	var ada struct {
		Admins []string `json:"admins"`
	}
	a.call("GET", "/api/me", nil, http.StatusOK, &ada)
	if ada.Admins != nil {
		t.Errorf("someone in projects is told whom to ask: %q", ada.Admins)
	}
}

// A flow everyone sees, whose run waits on a review its role decides, or
// anyone without one; returns the flow, the run and its review, waiting.
func peopleReview(a *app, name, role string, settings *processSettings) (flow, run, step) {
	a.t.Helper()
	f := a.newFlow(fmt.Sprintf("%s %d", name, time.Now().UnixNano()))
	// Without sign-in everything is everyone's already.
	var me usersMe
	if a.call("GET", "/api/me", nil, http.StatusOK, &me); me.SignIn {
		usersShare(a, f.ID)
	}
	draft := a.agent(f.ID, "Draft", "Draft it.")
	doc := a.newObject(f.ID, "Doc")
	ok := a.newState(doc.ID, "ok")
	review := humanHuman(a, f.ID, "Approve")
	if role != "" {
		humanPerson(a, f.ID, review.ID, role)
	}
	a.output(draft.ID, doc.ID)
	a.input(review.ID, doc.ID)
	humanDecides(a, review.ID, doc.ID, ok.ID)
	a.startAt(f.ID, draft.ID)
	if settings != nil {
		failuresSettings(a, review.ID, *settings)
	}
	a.worker.onAgent("Draft", handsIn(data("a doc")))
	rn := a.startRun(f.ID)
	return f, rn, a.waitStep(rn.ID, "Approve", stepWaiting)
}

// The inbox items of the steps given, by step.
func peopleInbox(a *app, steps ...int64) map[int64]inboxItemOf {
	a.t.Helper()
	var all []inboxItemOf
	a.call("GET", "/api/inbox", nil, http.StatusOK, &all)
	got := map[int64]inboxItemOf{}
	for _, it := range all {
		if slices.Contains(steps, it.Step) {
			got[it.Step] = it
		}
	}
	return got
}

type inboxItemOf struct {
	Step        int64  `json:"step"`
	Project     int64  `json:"project"`
	ProjectName string `json:"projectName"`
}

// The inbox is the person's across every project they may enter, each step
// naming its project, and the same whichever project they ask from;
// someone in one project has only its steps. A role held in one project
// decides no review of another, nor sees its flow.
func TestProjectsPeopleInboxAcrossProjects(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	two := newProject(a, "Inbox two")
	bob, carl, dina := usersNewPerson("Bob"), usersNewPerson("Carl"), usersNewPerson("Dina")
	b, d := usersAdd(a, bob.email), usersAdd(a, dina.email)
	usersAdd(a, carl.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", two.ID), []map[string]any{
		{"email": bob.email, "level": "editor"}, {"email": dina.email, "level": "editor"},
	}, http.StatusOK, nil)
	// Bob decides Legacy's Approver reviews, Dina the second project's.
	a.call("PATCH", fmt.Sprintf("/api/users/%d", b.ID), map[string]any{"roles": []string{"Approver"}}, http.StatusOK, nil)
	a.project = two.ID
	a.call("PATCH", fmt.Sprintf("/api/users/%d", d.ID), map[string]any{"roles": []string{"Approver"}}, http.StatusOK, nil)
	a.project = 0

	// Anyone's reviews in each project, and the Approvers' in each.
	_, legacyRun, legacyFree := peopleReview(a, "Legacy free", "", nil)
	_, _, legacyRole := peopleReview(a, "Legacy approved", "Approver", nil)
	a.project = two.ID
	_, movedRun, twoFree := peopleReview(a, "Second free", "", nil)
	movedRole, movedRoleRun, twoRole := peopleReview(a, "Second approved", "Approver", nil)
	// The approved one Ada's own, so only its role holders see it.
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", movedRole.ID), map[string]any{"kind": "user", "id": peopleMe(a)}, http.StatusOK, nil)
	a.project = 0

	steps := []int64{legacyFree.ID, legacyRole.ID, twoFree.ID, twoRole.ID}
	asBob, asCarl, asDina := inboxAs(a, prov, bob), inboxAs(a, prov, carl), inboxAs(a, prov, dina)
	for _, from := range []int64{0, two.ID} {
		asBob.project = from
		got := peopleInbox(asBob, steps...)
		if len(got) != 3 || got[legacyFree.ID].Project != 1 || got[legacyFree.ID].ProjectName != "Legacy" ||
			got[legacyRole.ID].Project != 1 || got[twoFree.ID].Project != two.ID || got[twoFree.ID].ProjectName != two.Name {
			t.Errorf("bob's inbox asked from project %d = %+v; want both free reviews and Legacy's Approver review, each with its project", from, got)
		}
	}
	if got := peopleInbox(asCarl, steps...); len(got) != 1 || got[legacyFree.ID].Step == 0 {
		t.Errorf("carl's inbox, in Legacy alone, without a role = %+v; want Legacy's free review alone", got)
	}
	if got := peopleInbox(asDina, steps...); got[twoRole.ID].Project != two.ID || got[legacyRole.ID].Step != 0 {
		t.Errorf("dina's inbox = %+v; want the second project's Approver review, not Legacy's", got)
	}

	// Bob's Approver role is Legacy's: the second project's review of it
	// is in a flow he doesn't see there, nor decides.
	asBob.project = two.ID
	asBob.refused("GET", fmt.Sprintf("/api/runs/%d", movedRoleRun.ID), nil, http.StatusNotFound, "not found")
	if status, _ := humanDecide(asBob, movedRoleRun.ID, twoRole.ID, map[string]any{"instructions": "Fine."}); status != http.StatusNotFound {
		t.Errorf("bob deciding the second project's review = %d, want 404", status)
	}
	// From Legacy, the second project's runs aren't there at all.
	asBob.project = 0
	asBob.refused("GET", fmt.Sprintf("/api/runs/%d", movedRun.ID), nil, http.StatusNotFound, "not found")
	// Dina decides it there, as her role there does.
	asDina.project = two.ID
	if s := onlyStep(t, asDina.getRun(movedRoleRun.ID), "Approve"); !s.CanDecide {
		t.Errorf("dina, holding the role in the second project, can't decide its review")
	}
	asDina.project = 0
	if s := onlyStep(t, asDina.getRun(legacyRun.ID), "Approve"); !s.CanDecide {
		t.Errorf("dina can't decide Legacy's free review")
	}
	for _, r := range []int64{legacyRun.ID, movedRun.ID, movedRoleRun.ID} {
		a.project = 0
		if r != legacyRun.ID {
			a.project = two.ID
		}
		a.do("PATCH", fmt.Sprintf("/api/runs/%d", r), map[string]any{"status": runInterrupted})
	}
}

// The signed-in user's id.
func peopleMe(a *app) int64 {
	a.t.Helper()
	var me usersMe
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	return me.User.ID
}

// With nobody signing in, the inbox, the Activity page and the flow list's
// health and counts are the request's project's.
func TestProjectsPeopleInboxActivityAndHealthWithoutSignIn(t *testing.T) {
	a := newApp(t)
	two := newProject(a, "Unsigned two")
	legacy, legacyRun, legacyStep := peopleReview(a, "Stays", "", nil)
	a.project = two.ID
	moved, movedRun, movedStep := peopleReview(a, "Second", "", nil)

	for project, want := range map[int64]struct {
		flow, run, step int64
	}{0: {legacy.ID, legacyRun.ID, legacyStep.ID}, two.ID: {moved.ID, movedRun.ID, movedStep.ID}} {
		a.project = project
		got := peopleInbox(a, legacyStep.ID, movedStep.ID)
		if len(got) != 1 || got[want.step].Step == 0 {
			t.Errorf("the inbox of project %d = %+v, want step %d alone", project, got, want.step)
		}
		act := activityOf(a, 7, "UTC")
		var running []int64
		for _, r := range act.Running.Runs {
			running = append(running, r.ID)
		}
		other := map[int64]int64{legacyRun.ID: movedRun.ID, movedRun.ID: legacyRun.ID}[want.run]
		if !slices.Contains(running, want.run) || slices.Contains(running, other) {
			t.Errorf("the Activity page of project %d lists runs %v going on, want %d and not %d", project, running, want.run, other)
		}
		if act.flow(want.flow) == nil || act.flow(map[int64]int64{legacy.ID: moved.ID, moved.ID: legacy.ID}[want.flow]) != nil {
			t.Errorf("the Activity page of project %d has flows %+v", project, act.Flows)
		}
		if h := listed(a, want.flow).Health; h == nil || *h != (flowHealth{Runs: 1, Running: 1, Waiting: 1}) {
			t.Errorf("flow %d's health in project %d = %+v, want one run going on, waiting", want.flow, project, h)
		}
	}
	a.project = 0
	a.do("PATCH", fmt.Sprintf("/api/runs/%d", legacyRun.ID), map[string]any{"status": runInterrupted})
	a.project = two.ID
	a.do("PATCH", fmt.Sprintf("/api/runs/%d", movedRun.ID), map[string]any{"status": runInterrupted})
}

// The Activity page of a project's owner, signed in, and the waiting count
// of its flow list, are its own.
func TestProjectsPeopleActivityOfAnOwner(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	two := newProject(a, "Owned two")
	olga := usersNewPerson("Olga")
	o := usersAdd(a, olga.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", two.ID), []map[string]any{{"user": o.ID, "level": "owner"}}, http.StatusOK, nil)
	legacy, legacyRun, _ := peopleReview(a, "Owner's legacy", "", nil)
	a.project = two.ID
	moved, movedRun, _ := peopleReview(a, "Owner's second", "", nil)

	usersSignIn(a, prov, olga)
	act := activityOf(a, 7, "UTC")
	if act.flow(moved.ID) == nil || act.flow(legacy.ID) != nil {
		t.Errorf("the owner's Activity page has flows %+v, want %d and not %d", act.Flows, moved.ID, legacy.ID)
	}
	if h := listed(a, moved.ID).Health; h == nil || h.Waiting != 1 {
		t.Errorf("the moved flow's health for its owner = %+v, want one step waiting on her", h)
	}
	usersSignIn(a, prov, usersAda)
	a.project = 0
	a.do("PATCH", fmt.Sprintf("/api/runs/%d", legacyRun.ID), map[string]any{"status": runInterrupted})
	a.project = two.ID
	a.do("PATCH", fmt.Sprintf("/api/runs/%d", movedRun.ID), map[string]any{"status": runInterrupted})
}

// Only people who may enter a step's project are told of it, holding the
// roles they hold there, through its own Slack connector, with links
// naming the project.
func TestProjectsPeopleNoticesKeepToTheProject(t *testing.T) {
	p := newFakeProvider(t)
	smtp, slack := newInboxSMTP(t), newInboxSlack(t)
	a := inboxApp(t, p, "", smtp.env()...)
	usersSignIn(a, p, usersAda)
	db := secretsDB(t)
	two := newProject(a, "Told two")
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	// Carl approves in Legacy alone; Bob in the second project alone.
	usersAdd(a, carl.email, "Approver")
	b := usersAdd(a, bob.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", two.ID), []map[string]any{{"user": b.ID, "level": "editor"}}, http.StatusOK, nil)
	a.project = two.ID
	a.call("PATCH", fmt.Sprintf("/api/users/%d", b.ID), map[string]any{"roles": []string{"Approver"}}, http.StatusOK, nil)
	name := fmt.Sprintf("people_slack_%d", time.Now().UnixNano())
	slack.install(a, name)
	if in := secretsStored(t, db, "SELECT project_id FROM mcp_servers WHERE name = ?", name); in != fmt.Sprint(two.ID) {
		t.Fatalf("the Slack connector was installed in project %s, want %d", in, two.ID)
	}

	f, rn, review := peopleReview(a, "Told", "Approver", &processSettings{RemindAfter: ptr(2)})
	link := inboxLink(a, fmt.Sprintf("/#/p/%d/flows/%d/runs/%d/steps/%d", two.ID, f.ID, rn.ID, review.ID))
	inboxEventually(t, "Bob being reminded in the second project", func() bool {
		return slices.ContainsFunc(smtp.to(bob.email), func(m inboxMail) bool {
			return strings.HasPrefix(m.subject, "Reminder: ") && strings.Contains(m.text, link)
		})
	})
	inboxEventually(t, "Bob's Slack message through the second project's connector", func() bool {
		return slices.ContainsFunc(slack.to(bob.email), func(dm string) bool { return strings.Contains(dm, link) })
	})
	if m := smtp.to(bob.email)[0]; !strings.Contains(m.text, inboxLink(a, fmt.Sprintf("/#/p/%d/settings/account", two.ID))) {
		t.Errorf("bob's message links to his settings elsewhere: %s", m.text)
	}
	time.Sleep(2500 * time.Millisecond)
	for _, who := range []string{carl.email, usersAda.email} {
		for _, m := range smtp.to(who) {
			if strings.Contains(m.text, fmt.Sprintf("/#/p/%d/", two.ID)) {
				t.Errorf("%s, who decides none of the second project's reviews, was told: %s", who, m.subject)
			}
		}
	}
	a.project = two.ID
	a.do("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted})
}
