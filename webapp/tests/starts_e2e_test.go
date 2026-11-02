package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func startTargets(s startNode) []int64 {
	var ps []int64
	for _, t := range s.Targets {
		ps = append(ps, t.Process)
	}
	return ps
}

// A board's start points at several processes, which a run begins with all
// at once: three reviewers review together. Pointing it at another adds
// that one, away from one drops it, and Target alone points it at one
// again. The sentences, the setup file, versions and grouping processes into
// a flow process keep every one.
func TestStartsWithSeveral(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Review together")
	pr := a.newObject(f.ID, "Pull Request")
	var reviewers []process
	for _, name := range []string{"Claude", "GPT", "Gemini"} {
		p := a.newProcess(f.ID, map[string]any{"name": "Review with " + name, "kind": kindScript, "script": name})
		a.input(p.ID, pr.ID)
		a.output(p.ID, a.newObject(f.ID, "Review by "+name).ID)
		reviewers = append(reviewers, p)
	}
	connect := func(p int64, body any) startNode {
		t.Helper()
		var s startNode
		a.call("PUT", fmt.Sprintf("/api/processes/%d/start", p), body, http.StatusOK, &s)
		return s
	}
	for _, r := range reviewers {
		connect(r.ID, nil)
	}
	s := connect(reviewers[1].ID, map[string]any{"route": []float64{5, 6}})
	if got := startTargets(s); !slices.Equal(got, []int64{reviewers[0].ID, reviewers[1].ID, reviewers[2].ID}) || string(s.Targets[1].Route) != "[5,6]" {
		t.Fatalf("start = %+v, want every reviewer, GPT's link moved", s)
	}

	// Each reviewer runs at once, not one after another.
	var mu sync.Mutex
	going := map[string]bool{}
	together := make(chan struct{})
	for _, name := range []string{"Claude", "GPT", "Gemini"} {
		a.worker.onScript("Review with "+name, func(ctx context.Context, run scriptRun) runResult {
			mu.Lock()
			if !going[name] {
				going[name] = true
				if len(going) == 3 {
					close(together)
				}
			}
			mu.Unlock()
			select {
			case <-together:
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				return runResult{Error: "the other reviewers didn't run meanwhile"}
			}
			out, _ := json.Marshal(data("looks good to " + name))
			return runResult{OK: true, Output: string(out)}
		})
	}
	rn := a.startRun(f.ID)
	if d := a.finish(rn.ID); d.Status != runDone || len(d.Steps) != 3 {
		t.Fatalf("run ended %s with %s, want each reviewer done", d.Status, describeSteps(d))
	}

	var opl struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil, http.StatusOK, &opl)
	if !strings.Contains(opl.Text, `The flow starts with "Review with Claude", "Review with GPT" and "Review with Gemini" at once.`) {
		t.Errorf("OPL doesn't say the flow starts with each reviewer:\n%s", opl.Text)
	}
	var setup struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", f.ID), nil, http.StatusOK, &setup)
	for _, want := range []string{"- Review with Claude", "- Review with Gemini", "routes:", "Review with GPT:"} {
		if !strings.Contains(setup.Text, want) {
			t.Errorf("the setup file lacks %q:\n%s", want, setup.Text)
		}
	}

	// Saved as a version, then pointed away from one, the version brings it
	// back with its route.
	var v struct {
		ID int64 `json:"id"`
	}
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &v)
	a.call("DELETE", fmt.Sprintf("/api/processes/%d/start", reviewers[1].ID), nil, http.StatusOK, &s)
	if got := startTargets(s); !slices.Equal(got, []int64{reviewers[0].ID, reviewers[2].ID}) {
		t.Fatalf("start without GPT = %v", got)
	}
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", v.ID), nil, http.StatusNoContent, nil)
	a.call("GET", fmt.Sprintf("/api/start?flow=%d", f.ID), nil, http.StatusOK, &s)
	if got := startTargets(s); len(got) != 3 || string(s.Targets[1].Route) != "[5,6]" {
		t.Fatalf("start after restoring = %+v, want the three reviewers back", s)
	}

	// Grouping two of them into a flow process points the start at it, and
	// its own start at both.
	var box process
	a.call("POST", "/api/processes/wrap", map[string]any{"name": "Models", "processes": []int64{reviewers[1].ID, reviewers[2].ID}}, http.StatusCreated, &box)
	a.call("GET", fmt.Sprintf("/api/start?flow=%d", f.ID), nil, http.StatusOK, &s)
	if got := startTargets(s); !slices.Equal(got, []int64{reviewers[0].ID, box.ID}) {
		t.Errorf("top start after grouping = %v, want Claude and the flow process", got)
	}
	a.call("GET", fmt.Sprintf("/api/start?board=%d", box.ID), nil, http.StatusOK, &s)
	if got := startTargets(s); !slices.Equal(got, []int64{reviewers[1].ID, reviewers[2].ID}) || string(s.Targets[0].Route) != "null" {
		t.Errorf("inner start = %+v, want GPT and Gemini on new routes", s)
	}
	rn = a.startRun(f.ID)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Fatalf("run after grouping ended %s: %s", d.Status, describeSteps(d))
	}

	// Target alone points the start at one again.
	a.call("PATCH", fmt.Sprintf("/api/start?flow=%d", f.ID), map[string]any{"target": reviewers[0].ID}, http.StatusOK, &s)
	if got := startTargets(s); !slices.Equal(got, []int64{reviewers[0].ID}) || s.Target == nil || *s.Target != reviewers[0].ID {
		t.Errorf("start pointed at Claude alone = %+v", s)
	}
}

// A setup file names the processes a board starts with as a list, with a
// route to each under routes; one alone keeps its route as it was.
func TestStartsWithSeveralInSetupFiles(t *testing.T) {
	a := newApp(t)
	file := `{"spoletta": 1, "flows": [{"name": "Fan out", "start": {"next": ["A", "B"], "routes": {"B": [1, 2]}},
		"processes": [{"name": "A", "kind": "script", "script": "a", "yields": ["X"]}, {"name": "B", "kind": "script", "script": "b", "yields": ["Y"]}],
		"objects": [{"name": "X"}, {"name": "Y"}]}]}`
	var res struct {
		Flows []struct {
			ID int64 `json:"id"`
		} `json:"flows"`
	}
	status, reply := setupImport(a, file)
	if status != http.StatusOK || json.Unmarshal(reply, &res) != nil || len(res.Flows) != 1 {
		t.Fatalf("importing = %d %s", status, reply)
	}
	var s startNode
	a.call("GET", fmt.Sprintf("/api/start?flow=%d", res.Flows[0].ID), nil, http.StatusOK, &s)
	if len(s.Targets) != 2 || string(s.Targets[0].Route) != "null" || string(s.Targets[1].Route) != "[1,2]" {
		t.Fatalf("imported start = %+v, want A and B, B's link moved", s)
	}
	for _, c := range []struct{ start, msg string }{
		{`{"next": ["A", "A"]}`, "twice"},
		{`{"next": ["A", "B"], "route": [1, 2]}`, "give each its own under routes"},
		{`{"next": ["A"], "routes": {"B": [1, 2]}}`, "doesn't go to"},
	} {
		bad := strings.Replace(file, `{"next": ["A", "B"], "routes": {"B": [1, 2]}}`, c.start, 1)
		status, reply := setupImport(a, bad)
		if status != http.StatusBadRequest || !strings.Contains(string(reply), c.msg) {
			t.Errorf("start %s imported = %d %s, want it refused with %q", c.start, status, reply, c.msg)
		}
	}
}
