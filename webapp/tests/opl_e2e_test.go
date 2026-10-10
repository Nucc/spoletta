package tests

// OPL, the flow in sentences: for people beside the board, and for agents
// told the flow they work in.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestOPLOfAFlow(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Described")
	write := a.agent(f.ID, "Write", "Write it.")
	review := a.agent(f.ID, "Review", "Review it.")
	post := a.newObject(f.ID, "Post")
	good := a.newState(post.ID, "good")
	weak := a.newState(post.ID, "weak")
	a.output(write.ID, post.ID)
	a.input(review.ID, post.ID)
	decisionsDecides(a, review.ID, post.ID, good.ID, weak.ID)
	a.startAt(f.ID, write.ID)

	var got struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", f.ID), nil, http.StatusOK, &got)
	for _, want := range []string{`"Write" yields "Post".`, `"Review" decides whether "Post" is "good" or "weak".`, `The flow starts with "Write".`} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("OPL lacks %q:\n%s", want, got.Text)
		}
	}
	a.refused("GET", "/api/flows/999999999/opl", nil, http.StatusNotFound, "flow not found")

	// An agent describing the flow gets it in its prompt; one that doesn't, doesn't.
	failuresSettings(a, review.ID, processSettings{DescribeFlow: true})
	a.worker.onAgent("Write", handsIn(data("draft")))
	a.worker.onAgent("Review", handsIn(decisionsReply(nil, map[string]any{"Post": decisionsState("good")})))
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
	}
	prompt := a.worker.runsOf("Review")[0].Prompt
	if !strings.Contains(prompt, `You carry out the process "Review"`) || !strings.Contains(prompt, "<flow>\n") || !strings.Contains(prompt, `"Write" yields "Post".`) {
		t.Errorf("Review's prompt doesn't describe the flow:\n%s", prompt)
	}
	if strings.Contains(a.worker.runsOf("Write")[0].Prompt, "<flow>") {
		t.Error("Write's prompt describes the flow")
	}
}
