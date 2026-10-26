package tests

import (
	"fmt"
	"net/http"
	"testing"
)

// The board says what is wrong with it before it runs: each problem on the
// process or object it is about, with the flow process whose board that is,
// an error or a warning; Run refuses a board with errors, saying them, and
// runs it once they are gone.
func TestProblemsOfABoard(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Problems")
	ps := prbGet(a, f.ID)
	if len(ps.Problems) != 1 || !prbHas(ps.Problems, "error", "start", 0, "no process yet") || ps.Published != nil {
		t.Errorf("an empty board's problems: %s", prbDescribe(ps.Problems))
	}

	fetch := a.agent(f.ID, "Fetch", "Fetch the news.")
	news := a.newObject(f.ID, "News")
	a.output(fetch.ID, news.ID)
	guide := a.newObject(f.ID, "Guide")
	write := a.agent(f.ID, "Write", "Write it up.")
	a.input(write.ID, news.ID)
	a.input(write.ID, guide.ID)
	post := a.newProcess(f.ID, map[string]any{"name": "Post", "kind": kindConnector})
	a.input(post.ID, news.ID)
	call := a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindSubflow})
	a.call("POST", "/api/connections", map[string]any{"source": write.ID, "target": call.ID}, http.StatusCreated, nil)
	each := a.newProcess(f.ID, map[string]any{"name": "Each", "kind": kindFlow})
	a.input(each.ID, news.ID)
	stray := a.newProcess(f.ID, map[string]any{"name": "Stray", "kind": kindScript, "parent": each.ID})

	ps = prbGet(a, f.ID)
	if !prbHas(ps.Problems, "error", "start", 0, "connect the start") {
		t.Errorf("without a start: %s", prbDescribe(ps.Problems))
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, "The board can't run:\n- Nothing runs")

	a.startAt(f.ID, fetch.ID)
	ps = prbGet(a, f.ID)
	for _, c := range []struct {
		level, rule string
		id          int64
		says        string
	}{
		{"error", "connector", post.ID, `"Post" uses no connector`},
		{"error", "subflow", call.ID, `"Call" runs no flow`},
		{"warning", "generated", guide.ID, `Nothing generates "Guide"`},
		{"warning", "unreached", stray.ID, `Nothing leads to "Stray"`},
	} {
		if !prbHas(ps.Problems, c.level, c.rule, c.id, c.says) {
			t.Errorf("no %s %s on %d saying %q: %s", c.level, c.rule, c.id, c.says, prbDescribe(ps.Problems))
		}
	}
	for _, p := range ps.Problems {
		if p.Process != nil && *p.Process == stray.ID && (p.Board == nil || *p.Board != each.ID) {
			t.Errorf("Stray's problem is on board %v, not Each's", p.Board)
		}
	}
	if n := len(prbErrors(ps.Problems)); n != 2 || ps.Problems[0].Level != "error" || ps.Problems[1].Level != "error" {
		t.Errorf("errors come first: %s", prbDescribe(ps.Problems))
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, `- "Post" uses no connector: pick one in its panel.`)

	// Warnings don't stop it.
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", post.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", call.ID), nil, http.StatusNoContent, nil)
	a.worker.onAgent("Fetch", handsIn(data("news")))
	a.worker.onAgent("Write", handsIn(data("written")))
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Errorf("run %s: %s", d.Status, describeSteps(d))
	}
	a.refused("GET", "/api/flows/1099511627776/problems", nil, http.StatusNotFound, "flow not found")
}

// A flow publishing a version says that version's errors too, as schedules
// and the API run it; Run with {"published": true} refuses it, while the
// board, fixed since, runs.
func TestProblemsOfThePublishedVersion(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Published problems")
	fetch := a.agent(f.ID, "Fetch", "Fetch.")
	a.startAt(f.ID, fetch.ID)
	call := a.newProcess(f.ID, map[string]any{"name": "Call", "kind": kindSubflow})
	a.call("POST", "/api/connections", map[string]any{"source": fetch.ID, "target": call.ID}, http.StatusCreated, nil)
	var ver struct {
		ID int64 `json:"id"`
	}
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), map[string]any{"publish": true}, http.StatusCreated, &ver)
	a.call("DELETE", fmt.Sprintf("/api/processes/%d", call.ID), nil, http.StatusNoContent, nil)

	ps := prbGet(a, f.ID)
	if len(ps.Problems) != 0 || ps.Published == nil || ps.Published.Version != ver.ID ||
		!prbHas(ps.Published.Errors, "error", "subflow", call.ID, `"Call" runs no flow`) {
		t.Errorf("problems %s, published %+v", prbDescribe(ps.Problems), ps.Published)
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), map[string]any{"published": true}, http.StatusConflict,
		fmt.Sprintf("Version %d, the one published, can't run", ver.ID))
	a.worker.onAgent("Fetch", handsIn(data("fetched")))
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Errorf("run %s: %s", d.Status, describeSteps(d))
	}
}
