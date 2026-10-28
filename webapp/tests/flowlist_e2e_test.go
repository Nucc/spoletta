package tests

// What the flow list shows of each flow: its tags, whether it is a
// favourite, and how its runs go.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Moves the run and its steps back in time by ago, as if it ran then.
func backdate(t *testing.T, db *sql.DB, run int64, ago time.Duration) {
	t.Helper()
	s := int64(ago.Seconds())
	for _, q := range []string{
		"UPDATE runs SET started_at = started_at - INTERVAL ? SECOND, finished_at = finished_at - INTERVAL ? SECOND WHERE id = ?",
		"UPDATE run_steps SET started_at = started_at - INTERVAL ? SECOND, finished_at = finished_at - INTERVAL ? SECOND WHERE run_id = ?",
	} {
		if _, err := db.Exec(q, s, s, run); err != nil {
			t.Fatal(err)
		}
	}
}

// A flow building something, whose Build fails each time fails says.
func buildsFlow(a *app, name string, fails func(n int) bool) (flow, process) {
	a.t.Helper()
	f := a.newFlow(name)
	build := a.agent(f.ID, "Build", "Build it.")
	a.output(build.ID, a.newObject(f.ID, "Binary").ID)
	a.startAt(f.ID, build.ID)
	var mu sync.Mutex
	n := 0
	a.worker.onAgent("Build", func(context.Context, agentRun) runResult {
		mu.Lock()
		defer mu.Unlock()
		n++
		if fails(n) {
			return runResult{Error: "the compiler gave up"}
		}
		return submitted(data("binary"))
	})
	return f, build
}

// The flow as the flow list shows it.
func listed(a *app, id int64) flow {
	a.t.Helper()
	var fs []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &fs)
	i := slices.IndexFunc(fs, func(f flow) bool { return f.ID == id })
	if i < 0 {
		a.t.Fatalf("flow %d isn't listed", id)
	}
	return fs[i]
}

// Those who may change a flow tag it; anyone starts and stops it being a
// favourite of theirs, without sign-in everyone's; the list says how the
// flow's runs of its own go of late. Tags and favourites go with the flow.
func TestFlowListTagsFavouritesAndHealth(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	f, _ := buildsFlow(a, "Tagged", func(n int) bool { return n == 1 })
	tags := fmt.Sprintf("/api/flows/%d/tags", f.ID)
	var got struct {
		Tags []string `json:"tags"`
	}
	a.call("PUT", tags, map[string]any{"tags": []string{" nightly ", "Billing", "billing", ""}}, http.StatusOK, &got)
	if !slices.Equal(got.Tags, []string{"Billing", "nightly"}) {
		t.Errorf("the tags saved = %q", got.Tags)
	}
	a.refused("PUT", tags, map[string]any{"tags": []string{strings.Repeat("x", 65)}}, http.StatusBadRequest, "longer than")
	a.refused("PUT", "/api/flows/999999999/tags", map[string]any{"tags": []string{"x"}}, http.StatusNotFound, "not found")
	if l := listed(a, f.ID); !slices.Equal(l.Tags, []string{"Billing", "nightly"}) || l.Favourite || l.Health == nil || *l.Health != (flowHealth{}) {
		t.Errorf("listed with %q, favourite %v, health %+v", l.Tags, l.Favourite, l.Health)
	}

	star := fmt.Sprintf("/api/flows/%d/favourite", f.ID)
	a.call("PUT", star, nil, http.StatusNoContent, nil)
	a.call("PUT", star, nil, http.StatusNoContent, nil)
	var one flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusOK, &one)
	if !listed(a, f.ID).Favourite || !one.Favourite || !slices.Equal(one.Tags, got.Tags) {
		t.Errorf("starred, the flow is %+v", one)
	}
	a.call("DELETE", star, nil, http.StatusNoContent, nil)
	if listed(a, f.ID).Favourite {
		t.Errorf("unstarred, the flow is still a favourite")
	}
	a.refused("PUT", "/api/flows/999999999/favourite", nil, http.StatusNotFound, "not found")

	// One run failed and one is done this week; one from before doesn't count.
	for range 3 {
		a.finish(a.startRun(f.ID).ID)
	}
	old := a.startRun(f.ID)
	a.finish(old.ID)
	backdate(t, db, old.ID, 8*24*time.Hour)
	if h := listed(a, f.ID).Health; h == nil || *h != (flowHealth{Runs: 3, Failed: 1}) {
		t.Errorf("the flow's health = %+v, want 3 runs this week, 1 failed", h)
	}
	// Without sign-in everyone decides every review.
	b := humanNewReviewBoard(a)
	rn := a.startRun(b.flow.ID)
	a.waitStep(rn.ID, "Review", stepWaiting)
	if h := listed(a, b.flow.ID).Health; h == nil || *h != (flowHealth{Runs: 1, Running: 1, Waiting: 1}) {
		t.Errorf("the review's health = %+v, want one run going on, waiting on whoever asked", h)
	}
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", rn.ID), map[string]any{"status": runInterrupted}, http.StatusOK, nil)

	// A struct is tagged as a flow is, and never runs.
	var s flow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprint("Ticket", time.Now().UnixNano()), "struct": true}, http.StatusCreated, &s)
	a.call("PUT", fmt.Sprintf("/api/flows/%d/tags", s.ID), map[string]any{"tags": []string{"support"}}, http.StatusOK, nil)
	a.call("PUT", fmt.Sprintf("/api/flows/%d/favourite", s.ID), nil, http.StatusNoContent, nil)
	if l := listed(a, s.ID); !slices.Equal(l.Tags, []string{"support"}) || !l.Favourite || l.Health != nil {
		t.Errorf("the struct is listed as %+v", l)
	}
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", s.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/trash/%d", s.ID), nil, http.StatusNoContent, nil)
	var left int
	if err := db.QueryRow("SELECT (SELECT COUNT(*) FROM flow_tags WHERE flow_id = ?) + (SELECT COUNT(*) FROM favourite_flows WHERE flow_id = ?)", s.ID, s.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("the deleted struct left %d tags and favourites (%v)", left, err)
	}
}

// The list counts the steps in each flow's runs waiting on whoever asks, as
// their inbox lists them: the reviews their roles decide, not those of
// another role, and a loop question only for those who may edit the flow.
func TestFlowListCountsWhatWaitsOnEachPerson(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	b := humanNewReviewBoard(a)
	usersShare(a, b.flow.ID)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email, "Editor")
	usersAdd(a, carl.email, "Finance")
	loops, _ := loopsFailing(a, 1, handsIn(data("reworked")))

	for range 2 {
		a.waitStep(a.startRun(b.flow.ID).ID, "Review", stepWaiting)
	}
	loopRun := a.startRun(loops.ID)
	a.waitRun(loopRun.ID, "it to ask", func(d runDetail) bool { return len(loopsAsked(d, "Fix")) > 0 })
	if h := listed(a, b.flow.ID).Health; h == nil || h.Waiting != 2 || h.Running != 2 {
		t.Errorf("Ada's count of the reviews = %+v, want 2 waiting on her as an admin, 2 running", h)
	}
	if h := listed(a, loops.ID).Health; h == nil || h.Waiting != 1 {
		t.Errorf("Ada's count of her loop question = %+v, want 1", h)
	}

	usersSignIn(a, p, bob)
	if h := listed(a, b.flow.ID).Health; h == nil || h.Waiting != 2 {
		t.Errorf("Bob, an editor, counts %+v, want 2 waiting on him", h)
	}
	usersSignIn(a, p, carl)
	if h := listed(a, b.flow.ID).Health; h == nil || h.Waiting != 0 || h.Running != 2 {
		t.Errorf("Carl, in finance, counts %+v, want none waiting on him of the 2 running", h)
	}
}

// Someone who only decides a flow's reviews stars it for themselves, but
// doesn't tag it; someone who doesn't see it does neither.
func TestFlowListReviewersStarButDontTag(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email, "Editor")
	usersAdd(a, carl.email)
	b := humanNewReviewBoard(a)
	tags, star := fmt.Sprintf("/api/flows/%d/tags", b.flow.ID), fmt.Sprintf("/api/flows/%d/favourite", b.flow.ID)
	a.call("PUT", tags, map[string]any{"tags": []string{"releases"}}, http.StatusOK, nil)
	a.call("PUT", star, nil, http.StatusNoContent, nil)

	usersSignIn(a, p, bob)
	a.refused("PUT", tags, map[string]any{"tags": []string{"mine"}}, http.StatusForbidden, "only decide")
	if l := listed(a, b.flow.ID); l.Access != "review" || l.Favourite || !slices.Equal(l.Tags, []string{"releases"}) {
		t.Errorf("bob sees %+v", l)
	}
	a.call("PUT", star, nil, http.StatusNoContent, nil)
	if !listed(a, b.flow.ID).Favourite {
		t.Errorf("bob's star didn't stay")
	}

	usersSignIn(a, p, carl)
	a.refused("PUT", star, nil, http.StatusNotFound, "not found")
	a.refused("PUT", tags, map[string]any{"tags": []string{"mine"}}, http.StatusNotFound, "not found")

	usersSignIn(a, p, usersAda)
	a.call("DELETE", star, nil, http.StatusNoContent, nil)
	if listed(a, b.flow.ID).Favourite {
		t.Errorf("ada's star didn't go")
	}
	usersSignIn(a, p, bob)
	if !listed(a, b.flow.ID).Favourite {
		t.Errorf("ada taking her star away took bob's")
	}
}
