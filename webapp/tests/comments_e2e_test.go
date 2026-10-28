package tests

// Comments on a review: those who may decide it, or edit its flow, say
// something of it while it waits, kept with its step once it is decided.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type reviewComment struct {
	ID     int64  `json:"id"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// Bob, who decides the review by his role, and Ada, whose flow it is,
// comment on it while it waits, in order; Carl, who doesn't see the flow,
// can't. Once it is decided it takes no more, but keeps them.
func TestCommentsOnAWaitingReview(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	b := humanNewReviewBoard(a)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	usersAdd(a, bob.email, "Editor")
	usersAdd(a, carl.email)

	rn := a.startRun(b.flow.ID)
	review := a.waitStep(rn.ID, "Review", stepWaiting)
	path := fmt.Sprintf("/api/runs/%d/steps/%d/comments", rn.ID, review.ID)
	var got []reviewComment
	a.call("POST", path, map[string]any{"text": "  Bob, check the version number?  "}, http.StatusCreated, &got)
	if len(got) != 1 || got[0].Text != "Bob, check the version number?" || got[0].Author != usersAda.name {
		t.Fatalf("after Ada's comment: %+v", got)
	}
	a.refused("POST", path, map[string]any{"text": " \n "}, http.StatusBadRequest, "says something")
	a.refused("POST", path, map[string]any{"text": strings.Repeat("x", 4001)}, http.StatusBadRequest, "at most 4000")
	draft := onlyStep(t, a.getRun(rn.ID), "Draft")
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/comments", rn.ID, draft.ID), map[string]any{"text": "hi"}, http.StatusNotFound, "no review")

	usersSignIn(a, p, bob)
	a.call("POST", path, map[string]any{"text": "It says 2.4, which is right."}, http.StatusCreated, &got)
	if len(got) != 2 || got[1].Author != bob.name || got[1].Text != "It says 2.4, which is right." {
		t.Errorf("after Bob's comment: %+v", got)
	}
	usersSignIn(a, p, carl)
	a.refused("POST", path, map[string]any{"text": "me too"}, http.StatusNotFound, "not found")

	usersSignIn(a, p, bob)
	humanMustDecide(a, rn.ID, review.ID, map[string]any{"state": b.approved.ID})
	d := a.finish(rn.ID)
	if s := onlyStep(t, d, "Review"); len(s.Comments) != 2 || s.Comments[0].Author != usersAda.name || s.Comments[1].Author != bob.name {
		t.Errorf("the decided review keeps %+v", s.Comments)
	}
	a.refused("POST", path, map[string]any{"text": "late"}, http.StatusConflict, "no longer waits")
}
