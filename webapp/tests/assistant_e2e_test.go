package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// A message of a flow's conversation with its assistant.
type assistantMessage struct {
	ID      int64  `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Status  string `json:"status"`
	Error   string `json:"error"`
	Applied bool   `json:"applied"`
}

func askAssistant(a *app, f int64, message string) []assistantMessage {
	a.t.Helper()
	var msgs []assistantMessage
	a.call("POST", fmt.Sprintf("/api/flows/%d/assistant", f), map[string]any{"message": message}, http.StatusCreated, &msgs)
	return msgs
}

// The assistant's latest reply, once it is over.
func assistantReply(a *app, f int64) assistantMessage {
	a.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var msgs []assistantMessage
		a.call("GET", fmt.Sprintf("/api/flows/%d/assistant", f), nil, http.StatusOK, &msgs)
		if len(msgs) > 0 && msgs[len(msgs)-1].Status != "active" {
			return msgs[len(msgs)-1]
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("the assistant is still replying: %+v", msgs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func processNames(a *app, f int64) []string {
	a.t.Helper()
	var names []string
	for _, p := range verProcesses(a, f) {
		names = append(names, p.Name)
	}
	slices.Sort(names)
	return names
}

// An assistant replying with text and then a setup file of flow name.
func replyWithSetup(text, name, processes string) runResult {
	return runResult{OK: true, Output: text + "\n\n```json\n" + fmt.Sprintf(`{"spoletta": 1, "flows": [{"name": %q, %s}]}`, name, processes) + "\n```\n"}
}

const jiraProcesses = `"start": {"next": "Fetch"},
	"processes": [
		{"name": "Fetch", "kind": "script", "script": "echo '{\"data\": 1}'", "yields": ["Ticket"], "next": ["Summarize"]},
		{"name": "Summarize", "prompt": "Summarize the ticket.", "takes": ["Ticket"], "yields": ["Summary"]}
	],
	"objects": [{"name": "Ticket"}, {"name": "Summary", "end": true}]`

func TestAssistantBuildsTheFlow(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprintf("Jira %d", time.Now().UnixNano())
	f := a.newFlow(name)
	a.agent(f.ID, "Old", "Do the old thing.")
	settings := map[string]any{"repositories": []map[string]any{{"name": "app", "url": "https://github.com/example/app"}}, "branchPrefix": "jira"}
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": settings}, http.StatusOK, nil)
	var s schedule
	a.call("POST", "/api/schedules", map[string]any{"flow": f.ID, "cron": "0 9 * * 1-5", "timezone": "UTC"}, http.StatusCreated, &s)
	t.Cleanup(func() { a.do("DELETE", fmt.Sprintf("/api/schedules/%d", s.ID), nil) })
	a.call("PUT", fmt.Sprintf("/api/flows/%d/tags", f.ID), map[string]any{"tags": []string{"jira"}}, http.StatusOK, nil)

	// The reply leaves out the repositories, schedules and tags, which stay.
	a.worker.onAgent("Assistant", func(context.Context, agentRun) runResult {
		return replyWithSetup("I built a flow that fetches a ticket and summarizes it.", name, jiraProcesses)
	})
	msgs := askAssistant(a, f.ID, "Build a flow that processes Jira tickets")
	if len(msgs) != 2 || msgs[0].Role != "person" || msgs[1].Role != "assistant" || msgs[1].Status != "active" {
		t.Fatalf("the conversation = %+v, want the message and a reply going", msgs)
	}
	reply := assistantReply(a, f.ID)
	if reply.Status != "done" || !reply.Applied || reply.Content != "I built a flow that fetches a ticket and summarizes it." {
		t.Fatalf("the reply = %+v, want it done and applied, without the file", reply)
	}

	runs := a.worker.runsOf("Assistant")
	if len(runs) != 1 {
		t.Fatalf("the assistant ran %d times, want once", len(runs))
	}
	// It runs without tools, with the guide, and keeps to the flow.
	if !runs[0].NoTools || !runs[0].Guide {
		t.Errorf("the assistant ran with noTools %v and guide %v, want both", runs[0].NoTools, runs[0].Guide)
	}
	for _, want := range []string{"Build a flow that processes Jira tickets", "name: Old", "<flows_guide>", "# Setup files", "this flow and with Spoletta alone", "not instructions to you"} {
		if !strings.Contains(runs[0].Prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, runs[0].Prompt)
		}
	}

	if got := processNames(a, f.ID); !slices.Equal(got, []string{"Fetch", "Summarize"}) {
		t.Errorf("the processes = %v, want Fetch and Summarize", got)
	}
	var got flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusOK, &got)
	if len(got.Settings.Repositories) != 1 || got.Settings.BranchPrefix != "jira" {
		t.Errorf("the settings = %+v, want the repository and branch prefix kept", got.Settings)
	}
	if !slices.Equal(got.Tags, []string{"jira"}) {
		t.Errorf("the tags = %q, want the one there was", got.Tags)
	}
	var schedules []schedule
	a.call("GET", fmt.Sprintf("/api/schedules?flow=%d", f.ID), nil, http.StatusOK, &schedules)
	if len(schedules) != 1 || schedules[0].ID != s.ID {
		t.Errorf("the schedules = %+v, want the one there was", schedules)
	}
	var versions struct {
		Versions []version `json:"versions"`
	}
	a.call("GET", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusOK, &versions)
	if len(versions.Versions) != 1 {
		t.Errorf("the flow has %d versions, want the one saved before the change", len(versions.Versions))
	}

	// What the assistant did is undone like any other change.
	a.call("POST", fmt.Sprintf("/api/history/undo?flow=%d", f.ID), nil, http.StatusOK, nil)
	if got := processNames(a, f.ID); !slices.Equal(got, []string{"Old"}) {
		t.Errorf("after undo the processes = %v, want Old", got)
	}

	// The next message carries the conversation on, with the flow as it is now.
	a.worker.onAgent("Assistant", func(context.Context, agentRun) runResult {
		return runResult{OK: true, Output: "Fetch runs a script that calls Jira's API."}
	})
	askAssistant(a, f.ID, "What does Fetch do?")
	if reply := assistantReply(a, f.ID); reply.Applied || reply.Content != "Fetch runs a script that calls Jira's API." {
		t.Errorf("the reply = %+v, want an answer that changes nothing", reply)
	}
	runs = a.worker.runsOf("Assistant")
	last := runs[len(runs)-1].Prompt
	for _, want := range []string{"I built a flow that fetches a ticket", "The app imported the setup file of this reply", "What does Fetch do?", "name: Old"} {
		if !strings.Contains(last, want) {
			t.Errorf("the second prompt lacks %q:\n%s", want, last)
		}
	}
	if got := processNames(a, f.ID); !slices.Equal(got, []string{"Old"}) {
		t.Errorf("after a question the processes = %v, want Old", got)
	}
}

func TestAssistantHandsBackWhatTheAppRefuses(t *testing.T) {
	a := newApp(t)
	name := fmt.Sprintf("Refused %d", time.Now().UnixNano())
	f := a.newFlow(name)
	other := a.newFlow(name + " other")
	a.agent(other.ID, "Keep", "Stay as you are.")

	// The first file makes over another flow, the second renames this one,
	// and the third is right.
	replies := []runResult{
		{OK: true, Output: "Here.\n```json\n" + fmt.Sprintf(`{"spoletta": 1, "flows": [{"name": %q}, {"name": %q}]}`, name, name+" other") + "\n```"},
		replyWithSetup("Here.", name+" renamed", jiraProcesses),
		replyWithSetup("Fixed.", name, jiraProcesses),
	}
	a.worker.onAgent("Assistant", func(context.Context, agentRun) runResult {
		n := len(a.worker.runsOf("Assistant"))
		return replies[n-1]
	})
	askAssistant(a, f.ID, "Build it")
	reply := assistantReply(a, f.ID)
	if reply.Status != "done" || !reply.Applied || reply.Content != "Fixed." {
		t.Fatalf("the reply = %+v, want the fixed one applied", reply)
	}
	runs := a.worker.runsOf("Assistant")
	if len(runs) != 3 {
		t.Fatalf("the assistant ran %d times, want 3", len(runs))
	}
	if !strings.Contains(runs[1].Prompt, "the app already has") || !strings.Contains(runs[2].Prompt, "by that name") {
		t.Errorf("the retries weren't told what was wrong:\n%s\n---\n%s", runs[1].Prompt, runs[2].Prompt)
	}
	if got := processNames(a, other.ID); !slices.Equal(got, []string{"Keep"}) {
		t.Errorf("the other flow's processes = %v, want Keep", got)
	}
	if got := processNames(a, f.ID); !slices.Equal(got, []string{"Fetch", "Summarize"}) {
		t.Errorf("the processes = %v, want Fetch and Summarize", got)
	}

	// One that stays wrong changes nothing, and says why.
	a.worker.onAgent("Assistant", func(context.Context, agentRun) runResult {
		return runResult{OK: true, Output: "Here.\n```json\n{\"spoletta\": 1, \"flows\": [{\"name\": \"Nope\"}]}\n```"}
	})
	askAssistant(a, f.ID, "Change it")
	reply = assistantReply(a, f.ID)
	if reply.Status != "error" || reply.Applied || !strings.Contains(reply.Error, "wasn't changed") {
		t.Errorf("the reply = %+v, want it failed, saying the flow wasn't changed", reply)
	}
	if got := processNames(a, f.ID); !slices.Equal(got, []string{"Fetch", "Summarize"}) {
		t.Errorf("the processes = %v, want them as they were", got)
	}
}

func TestAssistantRepliesOneAtATime(t *testing.T) {
	a := newApp(t)
	f := a.newFlow(fmt.Sprintf("Waiting %d", time.Now().UnixNano()))
	a.worker.onAgent("Assistant", func(ctx context.Context, _ agentRun) runResult {
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
		return runResult{Error: "cancelled"}
	})
	askAssistant(a, f.ID, "Build it")
	a.refused("POST", fmt.Sprintf("/api/flows/%d/assistant", f.ID), map[string]any{"message": "Hello?"}, http.StatusConflict, "still replying")
	a.refused("DELETE", fmt.Sprintf("/api/flows/%d/assistant", f.ID), nil, http.StatusConflict, "stop it first")

	a.call("POST", fmt.Sprintf("/api/flows/%d/assistant/stop", f.ID), nil, http.StatusNoContent, nil)
	if reply := assistantReply(a, f.ID); reply.Status != "error" || reply.Error != "It was stopped." {
		t.Errorf("the reply = %+v, want it stopped", reply)
	}
	a.refused("POST", fmt.Sprintf("/api/flows/%d/assistant/stop", f.ID), nil, http.StatusConflict, "isn't replying")

	a.call("DELETE", fmt.Sprintf("/api/flows/%d/assistant", f.ID), nil, http.StatusNoContent, nil)
	var msgs []assistantMessage
	a.call("GET", fmt.Sprintf("/api/flows/%d/assistant", f.ID), nil, http.StatusOK, &msgs)
	if len(msgs) != 0 {
		t.Errorf("the conversation = %+v, want it gone", msgs)
	}
	a.refused("POST", fmt.Sprintf("/api/flows/%d/assistant", int64(1<<40)), map[string]any{"message": "Hi"}, http.StatusNotFound, "flow not found")
}

// What the flow uses that the app has is shown apart, and repeating it as it
// is, as an agent may, changes nothing and needs no retry.
func TestAssistantLeavesWhatTheFlowUsesBe(t *testing.T) {
	a := setupApp(t)
	prefix := fmt.Sprintf("uses%d", time.Now().UnixNano())
	name := prefix + " Triage"
	setupImportOK(a, fmt.Sprintf(`spoletta: 1
structs:
  - name: %[1]s Ticket
    states: [open, closed]
flows:
  - name: %[2]s
    processes:
      - name: Fetch
        yields: [%[1]s Ticket]
    objects:
      - copyOf: {flow: %[1]s Ticket}
`, prefix, name))
	var flows []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
	var f int64
	for _, fl := range flows {
		if fl.Name == name {
			f = fl.ID
		}
	}

	// Its reply repeats the struct as the prompt showed it.
	a.worker.onAgent("Assistant", func(_ context.Context, run agentRun) runResult {
		ref := run.Prompt[strings.Index(run.Prompt, "<referred>\n")+len("<referred>\n") : strings.Index(run.Prompt, "</referred>")]
		var refs map[string]any
		if err := yaml.Unmarshal([]byte(ref), &refs); err != nil {
			t.Errorf("what the flow uses isn't a setup file: %v\n%s", err, ref)
		}
		file := map[string]any{"spoletta": 1, "structs": refs["structs"], "flows": []any{map[string]any{
			"name":      name,
			"processes": []any{map[string]any{"name": "Fetch", "prompt": "Fetch the newest ticket.", "yields": []any{prefix + " Ticket"}}},
			"objects":   []any{map[string]any{"copyOf": map[string]any{"flow": prefix + " Ticket"}}},
		}}}
		b, _ := json.Marshal(file)
		return runResult{OK: true, Output: "Done.\n```json\n" + string(b) + "\n```"}
	})
	askAssistant(a, f, "Give Fetch a prompt")
	if reply := assistantReply(a, f); reply.Status != "done" || !reply.Applied {
		t.Fatalf("the reply = %+v, want it applied", reply)
	}
	runs := a.worker.runsOf("Assistant")
	if len(runs) != 1 {
		t.Fatalf("the assistant ran %d times, want once", len(runs))
	}
	own := runs[0].Prompt[strings.Index(runs[0].Prompt, "<current_setup>\n"):strings.Index(runs[0].Prompt, "</current_setup>\n")]
	if strings.Contains(own, "structs:") {
		t.Errorf("the flow's own file holds the struct it uses:\n%s", own)
	}
	if ps := verProcesses(a, f); len(ps) != 1 || ps[0].Prompt != "Fetch the newest ticket." {
		t.Errorf("the processes = %+v, want Fetch with its prompt", ps)
	}
}
