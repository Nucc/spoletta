package tests

// Whoever has a board open says so every few seconds, and is told who else
// has it open, who of them is editing, and whether another tab changed the
// board since it loaded; all of it kept in the database, so any replica
// answers alike.

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

type presenceOf struct {
	Rev       int64  `json:"rev"`
	Changed   bool   `json:"changed"`
	ChangedBy string `json:"changedBy"`
	People    []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Editing bool   `json:"editing"`
	} `json:"people"`
}

// Says the board is open in the tab client, which loaded the change since,
// if any.
func presenceBeat(a *app, f int64, client string, since *int64) presenceOf {
	a.t.Helper()
	body := map[string]any{"client": client}
	if since != nil {
		body["since"] = *since
	}
	var p presenceOf
	a.call("PUT", fmt.Sprintf("/api/flows/%d/presence", f), body, http.StatusOK, &p)
	return p
}

func presenceNames(p presenceOf) []string {
	out := []string{}
	for _, v := range p.People {
		name := v.Name
		if v.Editing {
			name += " (editing)"
		}
		out = append(out, name)
	}
	return out
}

// A change from a tab, kept as a step of the board's history, as the
// board asks for once its changes are answered.
func presenceRecord(a *app, f int64, client string) {
	a.t.Helper()
	a.call("POST", fmt.Sprintf("/api/history?flow=%d&client=%s", f, client), nil, http.StatusOK, nil)
}

func TestPresenceOnABoard(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	ada := a.client
	f := a.newFlow("Presence")
	review := humanHuman(a, f.ID, "Review")
	humanPerson(a, f.ID, review.ID, "Editor")
	usersShare(a, f.ID)
	presenceRecord(a, f.ID, "ada-1")
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email)
	usersAdd(a, carl.email, "Editor")
	usersSignIn(a, p, bob)
	bobs := a.client
	// Carl only decides the flow's reviews, and still has its board open.
	usersSignIn(a, p, carl)
	carls := a.client
	as := func(c *http.Client) *app { a.client = c; return a }

	first := presenceBeat(as(ada), f.ID, "ada-1", nil)
	if len(first.People) != 0 || first.Changed || first.Rev == 0 {
		t.Errorf("Ada alone on the board is told %+v", first)
	}
	// Ada just built the board, so she is editing it.
	seen := presenceBeat(as(bobs), f.ID, "bob-1", nil)
	if got := presenceNames(seen); !slices.Equal(got, []string{"Ada Lovelace (editing)"}) {
		t.Errorf("Bob sees %v on the board, want Ada editing", got)
	}
	presenceBeat(as(carls), f.ID, "carl-1", nil)
	// One person with the board open twice is there once.
	presenceBeat(as(ada), f.ID, "ada-2", nil)

	// Ada changes the board: Bob is told who did, Ada isn't told of her
	// own change, but her other tab is, without being told her name.
	a.client = ada
	a.agent(f.ID, "Draft", "Draft it.")
	presenceRecord(a, f.ID, "ada-1")
	for _, c := range []struct {
		who       *http.Client
		client    string
		changed   bool
		changedBy string
	}{
		{bobs, "bob-1", true, "Ada Lovelace"},
		{ada, "ada-1", false, ""},
		{ada, "ada-2", true, ""},
	} {
		got := presenceBeat(as(c.who), f.ID, c.client, &seen.Rev)
		if got.Changed != c.changed || got.ChangedBy != c.changedBy || got.Rev <= seen.Rev {
			t.Errorf("%s after Ada's change is told %+v, want changed %v by %q", c.client, got, c.changed, c.changedBy)
		}
	}
	now := presenceBeat(as(bobs), f.ID, "bob-1", nil)
	if got := presenceNames(now); !slices.Equal(got, []string{"Ada Lovelace (editing)", "Carl"}) {
		t.Errorf("Bob sees %v on the board, want Ada editing and Carl", got)
	}
	if again := presenceBeat(as(bobs), f.ID, "bob-1", &now.Rev); again.Changed {
		t.Errorf("Bob, having loaded Ada's change, is told %+v", again)
	}

	// Undoing is a change too.
	a.client = ada
	a.call("POST", fmt.Sprintf("/api/history/undo?flow=%d&client=ada-1", f.ID), nil, http.StatusOK, nil)
	if got := presenceBeat(as(bobs), f.ID, "bob-1", &now.Rev); !got.Changed || got.ChangedBy != "Ada Lovelace" {
		t.Errorf("after Ada undid her change, Bob is told %+v", got)
	}

	// Leaving goes at once; a tab not heard from goes after a while.
	a.client = carls
	a.call("DELETE", fmt.Sprintf("/api/flows/%d/presence?client=carl-1", f.ID), nil, http.StatusNoContent, nil)
	db := secretsDB(t)
	if _, err := db.Exec("UPDATE board_viewers SET seen_at = NOW(3) - INTERVAL 30 SECOND WHERE flow_id = ? AND client LIKE 'ada-%'", f.ID); err != nil {
		t.Fatal(err)
	}
	if got := presenceNames(presenceBeat(as(bobs), f.ID, "bob-1", nil)); len(got) != 0 {
		t.Errorf("after Carl left and Ada went quiet, Bob sees %v", got)
	}

	// Nobody but those who see the flow says they have it open.
	dora := usersNewPerson("Dora")
	usersAdd(as(ada), dora.email)
	usersSignIn(a, p, dora)
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/presence", review.ID+1000000), map[string]any{"client": "dora-1"}, http.StatusNotFound, "flow not found")
	a.client = ada
	var mine flow
	a.call("POST", "/api/flows", map[string]any{"name": "Ada's own"}, http.StatusCreated, &mine)
	a.client = bobs
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/presence", mine.ID), map[string]any{"client": "bob-1"}, http.StatusNotFound, "flow not found")
	a.refused("PUT", fmt.Sprintf("/api/flows/%d/presence", f.ID), map[string]any{}, http.StatusBadRequest, "client is required")
}

// Without sign-in there are no people to show, but a tab is still told of
// another tab's change.
func TestPresenceWithoutSignIn(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprint("Presence alone ", time.Now().UnixNano())
	f := a.newFlow(name)
	presenceRecord(a, f.ID, "one")
	first := presenceBeat(a, f.ID, "one", nil)
	presenceBeat(a, f.ID, "two", nil)
	if got := presenceBeat(a, f.ID, "one", nil); len(got.People) != 0 {
		t.Errorf("without sign-in, the board shows %+v", got.People)
	}
	a.agent(f.ID, "Draft", "Draft it.")
	presenceRecord(a, f.ID, "two")
	if got := presenceBeat(a, f.ID, "one", &first.Rev); !got.Changed || got.ChangedBy != "" {
		t.Errorf("after another tab changed the board, one is told %+v", got)
	}
	if got := presenceBeat(a, f.ID, "two", &first.Rev); got.Changed {
		t.Errorf("the tab that changed the board is told %+v", got)
	}

	// A setup import, as the assistant's, is a change from no tab.
	now := presenceBeat(a, f.ID, "two", nil)
	setupImportOK(a, fmt.Sprintf("spoletta: 1\nflows:\n  - name: %s\n    processes: [{name: Draft}]\n    notes: [{text: Imported}]\n", name))
	if got := presenceBeat(a, f.ID, "two", &now.Rev); !got.Changed {
		t.Errorf("after an import, the board is told %+v", got)
	}
}

// One replica answers for the tabs another heard from.
func TestPresenceAcrossReplicas(t *testing.T) {
	a, p := usersApp(t)
	other := startApp(t, a.worker.url,
		"OIDC_ISSUER="+p.srv.URL,
		"OIDC_CLIENT_ID="+usersClientID,
		"OIDC_CLIENT_SECRET="+usersClientSecret,
		"OIDC_ALLOWED_DOMAINS=example.org",
		"ADMIN_EMAILS=Ada@Example.com")
	usersSignIn(a, p, usersAda)
	f := a.newFlow("Presence replicated")
	usersShare(a, f.ID)
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	presenceBeat(a, f.ID, "ada-1", nil)

	usersSignIn(a, p, bob)
	other.client = a.client
	if got := presenceNames(presenceBeat(other, f.ID, "bob-1", nil)); !slices.Equal(got, []string{"Ada Lovelace"}) {
		t.Errorf("Bob at the other replica sees %v", got)
	}
}
