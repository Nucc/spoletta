package tests

// Attributes typed Markdown: text an agent is told to write in Markdown,
// checked as text, described to other systems as text/markdown, and named
// to the pages showing runs and records, which render them as Markdown.

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// An agent is told to write a Markdown field in Markdown, and a run names
// the fields its version types Markdown, which a later edit of the board
// leaves as they were, while the Data page follows the board as it is.
func TestMarkdownAttributesReachAgentsAndRuns(t *testing.T) {
	parallel(t)
	a := newApp(t)
	f := a.newFlow("Report findings")
	review := a.agent(f.ID, "Review", "Review the change.")
	findings := a.newObject(f.ID, "Findings")
	attrs := fmt.Sprintf("/api/objects/%d/attributes", findings.ID)
	var report attribute
	a.call("POST", attrs, map[string]any{"name": "report", "type": "markdown", "definition": "What was found"}, http.StatusCreated, &report)
	if report.Type != "markdown" {
		t.Fatalf("a Markdown attribute was made %+v", report)
	}
	a.call("POST", attrs, map[string]any{"name": "severity", "type": "text"}, http.StatusCreated, nil)
	a.refused("POST", attrs, map[string]any{"name": "notes", "type": "markdown", "pattern": "#.*"}, http.StatusBadRequest, "only text has a pattern")
	a.output(review.ID, findings.ID)
	a.startAt(f.ID, review.ID)

	var mu sync.Mutex
	var told agentRun
	a.worker.onAgent("Review", func(_ context.Context, r agentRun) runResult {
		mu.Lock()
		told = r
		mu.Unlock()
		return submitted(map[string]any{"report": "# Findings\n\n- the cache is never cleared", "severity": "low"})
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("the run ended %s: %s", d.Status, describeSteps(d))
	}
	mu.Lock()
	prompt, spec := told.Prompt, told.Output
	mu.Unlock()
	if !strings.Contains(prompt, `- "report": text in Markdown, `) {
		t.Errorf("the agent's prompt doesn't say to write the report in Markdown:\n%s", prompt)
	}
	if spec == nil || len(spec.Objects) != 1 || len(spec.Objects[0].Attributes) != 2 {
		t.Fatalf("the agent's tool takes %+v", spec)
	}
	if got := spec.Objects[0].Attributes[0]; got.Type != "markdown" || !strings.HasPrefix(got.Description, "Text in Markdown, ") ||
		!strings.HasSuffix(got.Description, ". What was found") {
		t.Errorf("the agent's tool takes the report as %+v", got)
	}

	markdownOf := func(path string) []string {
		t.Helper()
		var got struct {
			Markdown []string `json:"markdown"`
		}
		a.call("GET", path, nil, http.StatusOK, &got)
		return got.Markdown
	}
	runPath, dataPath := fmt.Sprintf("/api/runs/%d", d.ID), fmt.Sprintf("/api/data?flow=%d", f.ID)
	if got := markdownOf(runPath); !slices.Equal(got, []string{"report"}) {
		t.Errorf("the run names %q as Markdown", got)
	}
	if got := markdownOf(dataPath); !slices.Equal(got, []string{"report"}) {
		t.Errorf("the Data page names %q as Markdown", got)
	}

	// Typed text no more, the run still shows it as its version had it.
	a.call("PATCH", fmt.Sprintf("/api/attributes/%d", report.ID), map[string]any{"type": "text"}, http.StatusOK, nil)
	if got := markdownOf(runPath); !slices.Equal(got, []string{"report"}) {
		t.Errorf("after the board changed, the run names %q as Markdown", got)
	}
	if got := markdownOf(dataPath); got == nil || len(got) != 0 {
		t.Errorf("after the board changed, the Data page names %#v as Markdown", got)
	}
}

// What a run starts from describes a Markdown field as text/markdown, as
// JSON Schema says what a string holds.
func TestMarkdownInputsAreDescribed(t *testing.T) {
	parallel(t)
	a := newApp(t)
	tf := newAPITicketFlow(a)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", tf.ticket.ID), map[string]any{"name": "details", "type": "markdown"}, http.StatusCreated, nil)

	in := handInputsOf(a, tf.flow.ID, false)
	props, _ := in.Inputs[0].Schema["properties"].(map[string]any)
	details, _ := props["details"].(map[string]any)
	if details["type"] != "string" || details["contentMediaType"] != "text/markdown" {
		t.Errorf("the ticket's details are described as %v", details)
	}
}

// A struct's Markdown fields, its own and those of what it is made of, are
// named to the Records page and each task board column holding its records.
func TestMarkdownFieldsOfRecords(t *testing.T) {
	parallel(t)
	a := newApp(t)
	n := time.Now().UnixNano()
	incident := taskStruct(a, fmt.Sprint("Incident", n), nil, "key", []string{"title"}, "open", "closed")
	root := modObjects(a, incident.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": "postmortem", "type": "markdown"}, http.StatusCreated, nil)
	var timeline object
	a.call("POST", "/api/objects", map[string]any{"name": "timeline", "owner": root.ID, "relation": "aggregation", "many": true}, http.StatusCreated, &timeline)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", timeline.ID), map[string]any{"name": "entry", "type": "markdown"}, http.StatusCreated, nil)
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", timeline.ID), map[string]any{"name": "at", "type": "datetime"}, http.StatusCreated, nil)
	taskRecord(a, incident.ID, map[string]any{"key": "I-1", "title": "Outage", "postmortem": "## What happened\n\nThe cache filled up."}, "open")

	var records struct {
		Markdown []string `json:"markdown"`
	}
	a.call("GET", fmt.Sprintf("/api/structs/%d/records", incident.ID), nil, http.StatusOK, &records)
	if !slices.Equal(records.Markdown, []string{"entry", "postmortem"}) {
		t.Errorf("the Records page names %q as Markdown", records.Markdown)
	}

	b := taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": fmt.Sprint("Incidents ", n)}, http.StatusCreated)
	path := fmt.Sprintf("/api/taskboards/%d", b.ID)
	columns := []map[string]any{{"name": "Open", "struct": incident.ID, "query": "state:open"}}
	taskBoardCall(a, "PUT", path, map[string]any{"name": b.Name, "columns": columns}, http.StatusOK)
	var board struct {
		Columns []struct {
			Cards *struct {
				Markdown []string `json:"markdown"`
			} `json:"cards"`
		} `json:"columns"`
	}
	a.call("GET", path, nil, http.StatusOK, &board)
	if len(board.Columns) != 1 || board.Columns[0].Cards == nil || !slices.Equal(board.Columns[0].Cards.Markdown, []string{"entry", "postmortem"}) {
		t.Errorf("the task board's column is %+v", board.Columns)
	}
}
