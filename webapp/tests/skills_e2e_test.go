package tests

// Skills and commands: people write them in the settings, each owned as a
// flow is, agents are given the skills their process picks, and a command
// an agent's prompt names is spelled out as the step runs.

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type skillsItem struct {
	ID           int64     `json:"id"`
	Kind         string    `json:"kind"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	ArgumentHint string    `json:"argumentHint"`
	Content      string    `json:"content"`
	Owner        flowOwner `json:"owner"`
}

// A name of its own, as the tests share a database.
func skillsName(name string) string {
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
}

func skillsNew(a *app, route string, fields map[string]any) skillsItem {
	a.t.Helper()
	var s skillsItem
	a.call("POST", "/api/"+route, fields, http.StatusCreated, &s)
	return s
}

func skillsNames(a *app, path string) []string {
	a.t.Helper()
	var list []skillsItem
	a.call("GET", path, nil, http.StatusOK, &list)
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	return names
}

// A flow whose one agent, Write, generates a Note.
func skillsFlow(a *app, prompt string, settings map[string]any) (flow, process) {
	a.t.Helper()
	f := a.newFlow("Notes")
	p := a.newProcess(f.ID, map[string]any{"name": "Write", "kind": "agent", "prompt": prompt, "settings": settings})
	a.output(p.ID, a.newObject(f.ID, "Note").ID)
	a.startAt(f.ID, p.ID)
	a.worker.onAgent("Write", handsIn(data("a note")))
	return f, p
}

// The agent is given the skills its process picked, and the commands its
// prompt names are spelled out before what it takes is added; a /word
// naming no command stays. A deleted skill fails the step.
func TestSkillsAndCommandsInRuns(t *testing.T) {
	a := newApp(t)
	notes := skillsNew(a, "skills", map[string]any{"name": skillsName("notes"), "description": "Writes notes: use for any note.", "content": "Keep notes short."})
	review := skillsNew(a, "commands", map[string]any{"name": skillsName("review"), "argumentHint": "[focus]", "content": "Review it. Focus on ${1:-correctness}.\n"})
	tone := skillsNew(a, "commands", map[string]any{"name": skillsName("tone"), "content": "Write plainly."})
	if notes.Owner.Kind != "global" || notes.Kind != "skill" || review.Kind != "command" {
		t.Errorf("made %+v and %+v; without sign-in everything is everyone's", notes, review)
	}
	prompt := fmt.Sprintf("/%s security\nThen /%s and read /etc/hosts.", review.Name, tone.Name)
	f, _ := skillsFlow(a, prompt, map[string]any{"skills": []int64{notes.ID}})

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != "done" {
		t.Fatalf("the run is %s: %s", d.Status, describeSteps(d))
	}
	runs := a.worker.runsOf("Write")
	want := []runSkill{{Name: notes.Name, Description: notes.Description, Content: notes.Content}}
	if len(runs) != 1 || !slices.Equal(runs[0].Skills, want) {
		t.Fatalf("the agent ran with %+v, want %+v", runs, want)
	}
	expanded := "Review it. Focus on security.\nThen Write plainly. and read /etc/hosts."
	if !strings.HasPrefix(runs[0].Prompt, expanded) {
		t.Errorf("the prompt is\n%s\nwant it to start\n%s", runs[0].Prompt, expanded)
	}
	if s := onlyStep(t, d, "Write"); !strings.HasPrefix(s.Prompt, expanded) {
		t.Errorf("the step recorded the prompt\n%s", s.Prompt)
	}

	// What it takes naming a command stays as it is.
	g := a.newFlow("Relay")
	first := a.newProcess(g.ID, map[string]any{"name": "Draft", "kind": "agent", "prompt": "Draft it."})
	draft := a.newObject(g.ID, "Draft")
	a.output(first.ID, draft.ID)
	second := a.newProcess(g.ID, map[string]any{"name": "Polish", "kind": "agent", "prompt": "Polish it."})
	a.input(second.ID, draft.ID)
	a.output(second.ID, a.newObject(g.ID, "Final").ID)
	a.startAt(g.ID, first.ID)
	a.worker.onAgent("Draft", handsIn(data("/"+tone.Name)))
	a.worker.onAgent("Polish", handsIn(data("done")))
	if d := a.finish(a.startRun(g.ID).ID); d.Status != "done" || strings.Contains(onlyStep(t, d, "Polish").Prompt, "Write plainly.") {
		t.Errorf("an input naming a command was spelled out: %s", describeSteps(d))
	}

	a.call("DELETE", fmt.Sprintf("/api/skills/%d", notes.ID), nil, http.StatusNoContent, nil)
	d = a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Write"); s.Status != "error" || !strings.Contains(s.Error, "was deleted") {
		t.Errorf("with its skill deleted the step is %s: %s", s.Status, s.Error)
	}
	if len(a.worker.runsOf("Write")) != 1 {
		t.Errorf("the agent ran without its skill")
	}
}

// Skills and commands are their maker's until they share them with a team
// or everyone; only those they belong to see and change them. A flow uses
// only what its owner may, which its board is offered.
func TestSkillsAreTheirOwners(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carl := usersNewPerson("Bob"), usersNewPerson("Carl")
	bobRow, carlRow := usersAdd(a, bob.email), usersAdd(a, carl.email)
	var tm teamsTeam
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Writers", bobRow.ID), "members": []int64{bobRow.ID}}, http.StatusCreated, &tm)

	usersSignIn(a, p, bob)
	name := skillsName("style")
	s := skillsNew(a, "skills", map[string]any{"name": " " + name + " ", "description": "House style.", "content": "Use the house style."})
	if s.Name != name || s.Owner.Kind != "user" || s.Owner.ID != bobRow.ID {
		t.Errorf("made %+v, want it bob's", s)
	}
	a.refused("POST", "/api/skills", map[string]any{"name": name, "description": "d", "content": "c"}, http.StatusConflict, "named "+name+" already")
	a.refused("POST", "/api/skills", map[string]any{"name": "House Style", "description": "d", "content": "c"}, http.StatusBadRequest, "lowercase letters")
	a.refused("POST", "/api/skills", map[string]any{"name": skillsName("bare"), "content": "c"}, http.StatusBadRequest, "needs a description")
	a.refused("POST", "/api/commands", map[string]any{"name": skillsName("empty"), "content": " "}, http.StatusBadRequest, "needs the prompt")
	// A command may share a skill's name; a command needs no description.
	cmd := skillsNew(a, "commands", map[string]any{"name": name, "content": "Mind the house style."})
	a.refused("POST", "/api/skills", map[string]any{"name": skillsName("theirs"), "description": "d", "content": "c", "owner": map[string]any{"kind": "user", "id": carlRow.ID}},
		http.StatusForbidden, "only an admin")

	// Bob's flow uses what is his; once everyone's, the flow may no longer.
	prompt := "/" + cmd.Name
	f, proc := skillsFlow(a, prompt, map[string]any{"skills": []int64{s.ID}})
	a.call("PUT", fmt.Sprintf("/api/flows/%d/owner", f.ID), map[string]any{"kind": "user", "id": bobRow.ID}, http.StatusOK, nil)
	if names := skillsNames(a, fmt.Sprintf("/api/skills?flow=%d", f.ID)); !slices.Contains(names, name) {
		t.Errorf("bob's flow is offered %v", names)
	}
	if d := a.finish(a.startRun(f.ID).ID); d.Status != "done" || onlyStep(t, d, "Write").Prompt[:len("Mind the house style.")] != "Mind the house style." {
		t.Errorf("bob's flow ran as %s: %s", d.Status, describeSteps(d))
	}
	usersShare(a, f.ID)
	if names := skillsNames(a, fmt.Sprintf("/api/skills?flow=%d", f.ID)); slices.Contains(names, name) {
		t.Errorf("everyone's flow is offered bob's own skill: %v", names)
	}
	d := a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Write"); s.Status != "error" || !strings.Contains(s.Error, "skill "+name+" isn't one this flow may use") {
		t.Errorf("everyone's flow picking bob's own skill: %s %s", s.Status, s.Error)
	}
	// Named in everyone's flow, bob's own command passes on as written, as
	// a path would.
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", proc.ID), map[string]any{"settings": map[string]any{}}, http.StatusOK, nil)
	d = a.finish(a.startRun(f.ID).ID)
	if s := onlyStep(t, d, "Write"); s.Status != "done" || !strings.HasPrefix(s.Prompt, prompt) {
		t.Errorf("everyone's flow naming bob's own command: %s %s\n%s", s.Status, s.Error, s.Prompt)
	}
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", proc.ID), map[string]any{"settings": map[string]any{"skills": []int64{s.ID}}}, http.StatusOK, nil)
	a.call("PUT", fmt.Sprintf("/api/commands/%d/owner", cmd.ID), map[string]any{"kind": "global"}, http.StatusOK, nil)

	// Carl sees neither Bob's own skill nor his team's.
	usersSignIn(a, p, carl)
	if slices.Contains(skillsNames(a, "/api/skills"), name) {
		t.Errorf("carl sees bob's skill")
	}
	a.refused("PUT", fmt.Sprintf("/api/skills/%d", s.ID), map[string]any{"name": name, "description": "d", "content": "Mine."}, http.StatusNotFound, "skill not found")
	a.refused("DELETE", fmt.Sprintf("/api/skills/%d", s.ID), nil, http.StatusNotFound, "skill not found")
	a.refused("DELETE", fmt.Sprintf("/api/commands/%d", s.ID), nil, http.StatusNotFound, "command not found")
	usersSignIn(a, p, bob)
	a.call("PUT", fmt.Sprintf("/api/skills/%d/owner", s.ID), map[string]any{"kind": "team", "id": tm.ID}, http.StatusOK, &s)
	if s.Owner.Kind != "team" || s.Owner.Name != tm.Name {
		t.Errorf("given to %+v", s.Owner)
	}
	usersSignIn(a, p, carl)
	if slices.Contains(skillsNames(a, "/api/skills"), name) {
		t.Errorf("carl sees the skill of a team he isn't in")
	}

	// Shared with everyone, Carl sees it and changes it, but only an admin
	// takes it back.
	usersSignIn(a, p, bob)
	a.call("PUT", fmt.Sprintf("/api/skills/%d/owner", s.ID), map[string]any{"kind": "global"}, http.StatusOK, nil)
	a.refused("PUT", fmt.Sprintf("/api/skills/%d/owner", s.ID), map[string]any{"kind": "user", "id": bobRow.ID}, http.StatusForbidden, "only an admin")
	usersSignIn(a, p, carl)
	a.call("PUT", fmt.Sprintf("/api/skills/%d", s.ID), map[string]any{"name": name, "description": "House style.", "content": "Use the house style, always."}, http.StatusOK, &s)
	if s.Content != "Use the house style, always." {
		t.Errorf("changed to %+v", s)
	}
	if d := a.finish(a.startRun(f.ID).ID); d.Status != "done" {
		t.Errorf("everyone's flow with everyone's skill and command: %s", describeSteps(d))
	}
	if runs := a.worker.runsOf("Write"); runs[len(runs)-1].Skills[0].Content != "Use the house style, always." {
		t.Errorf("the agent got %+v", runs[len(runs)-1].Skills)
	}

	// A team owning a skill stays until it goes elsewhere.
	usersSignIn(a, p, bob)
	team := skillsNew(a, "commands", map[string]any{"name": skillsName("team"), "content": "c", "owner": map[string]any{"kind": "team", "id": tm.ID}})
	usersSignIn(a, p, usersAda)
	a.refused("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusConflict, "owns 1 skills or commands")
	a.call("DELETE", fmt.Sprintf("/api/commands/%d", team.ID), nil, http.StatusNoContent, nil)
	a.call("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusNoContent, nil)
}

// The setup file names a process's skills, and importing one naming a
// skill the app hasn't is refused; the assistant is told the skills and
// commands its flow may use.
func TestSkillsInTheSetupFile(t *testing.T) {
	a := setupApp(t)
	prefix := fmt.Sprintf("skills%d", time.Now().UnixNano())
	s := skillsNew(a, "skills", map[string]any{"name": skillsName("notes"), "description": "Writes notes.", "content": "Keep notes short."})
	c := skillsNew(a, "commands", map[string]any{"name": skillsName("tone"), "argumentHint": "[how]", "content": "Write ${1:-plainly}."})
	file := fmt.Sprintf(`spoletta: 1
flows:
  - name: %s Notes
    processes:
      - name: Write
        prompt: /%s
        skills: [%s]
`, prefix, c.Name, s.Name)
	setupImportOK(a, file)
	if out := setupExport(a, prefix); !strings.Contains(out, "skills:\n") || !strings.Contains(out, "- "+s.Name) {
		t.Errorf("the export leaves out the skill:\n%s", out)
	}
	status, body := setupImport(a, strings.Replace(file, s.Name, "no-such-skill", 1))
	if status != http.StatusBadRequest || !strings.Contains(string(body), `no skill named "no-such-skill"`) {
		t.Errorf("importing an unknown skill = %d %s", status, body)
	}

	var flows []flow
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
	var f int64
	for _, fl := range flows {
		if fl.Name == prefix+" Notes" {
			f = fl.ID
		}
	}
	a.worker.onAgent("Assistant", func(_ context.Context, run agentRun) runResult {
		for _, want := range []string{"<skills>\n- " + s.Name + ": Writes notes.\n", "<commands>\n- /" + c.Name + " [how]: \n"} {
			if !strings.Contains(run.Prompt, want) {
				t.Errorf("the assistant's prompt lacks %q", want)
			}
		}
		return runResult{OK: true, Output: "Nothing to change."}
	})
	askAssistant(a, f, "What skills can it use?")
	if reply := assistantReply(a, f); reply.Status != "done" {
		t.Errorf("the reply = %+v", reply)
	}
}

// The setup file holds the skills and commands whole: export writes them
// out, and import adds the ones the app lacks, as everyone's, and makes over
// the ones it has by name.
func TestSkillsAndCommandsInTheSetupFile(t *testing.T) {
	a := setupApp(t)
	prefix := fmt.Sprintf("setup%d", time.Now().UnixNano())
	skillsNew(a, "skills", map[string]any{"name": prefix + "-notes", "description": "Writes notes.", "content": "Keep notes short.\nSay why."})
	skillsNew(a, "commands", map[string]any{"name": prefix + "-tone", "argumentHint": "[how]", "content": "Write ${1:-plainly}."})

	var out struct {
		Skills   []map[string]string `yaml:"skills"`
		Commands []map[string]string `yaml:"commands"`
	}
	if err := yaml.Unmarshal([]byte(setupExport(a, prefix)), &out); err != nil {
		t.Fatal(err)
	}
	wantSkill := map[string]string{"name": prefix + "-notes", "description": "Writes notes.", "content": "Keep notes short.\nSay why."}
	wantCommand := map[string]string{"name": prefix + "-tone", "arguments": "[how]", "content": "Write ${1:-plainly}."}
	if len(out.Skills) != 1 || !maps.Equal(out.Skills[0], wantSkill) || len(out.Commands) != 1 || !maps.Equal(out.Commands[0], wantCommand) {
		t.Errorf("the export has skills %v and commands %v", out.Skills, out.Commands)
	}

	file := fmt.Sprintf(`spoletta: 1
skills:
  - name: %[1]s-notes
    description: Writes notes, briefly.
    content: Keep notes shorter.
  - name: %[1]s-style
    description: How we write.
    content: Plain words.
commands:
  - name: /%[1]s-tone
    arguments: "[how] [who]"
    content: Write ${1:-plainly} for $2.
flows:
  - name: %[1]s Notes
    processes:
      - name: Write
        prompt: /%[1]s-tone
        skills: [%[1]s-notes, %[1]s-style]
`, prefix)
	setupImportOK(a, file)
	byName := map[string]skillsItem{}
	for _, route := range []string{"skills", "commands"} {
		var list []skillsItem
		a.call("GET", "/api/"+route, nil, http.StatusOK, &list)
		for _, s := range list {
			byName[s.Name] = s
		}
	}
	if s := byName[prefix+"-notes"]; s.Description != "Writes notes, briefly." || s.Content != "Keep notes shorter." {
		t.Errorf("the skill made over = %+v", s)
	}
	if s := byName[prefix+"-style"]; s.Kind != "skill" || s.Content != "Plain words." || s.Owner.Kind != "global" {
		t.Errorf("the skill added = %+v", s)
	}
	if s := byName[prefix+"-tone"]; s.ArgumentHint != "[how] [who]" || s.Content != "Write ${1:-plainly} for $2." {
		t.Errorf("the command made over = %+v", s)
	}

	style := "  - name: " + prefix + "-style\n"
	for bad, want := range map[string]string{
		strings.Replace(file, "    content: Plain words.\n", "", 1):                                              "a skill needs its instructions",
		strings.Replace(file, "    content: Plain words.\n", "    content: Plain words.\n    arguments: x\n", 1): "only a command takes arguments",
		strings.Replace(file, style, "  - name: "+prefix+"-notes\n", 1):                                          "the file has more than one skill named",
	} {
		if status, body := setupImport(a, bad); status != http.StatusBadRequest || !strings.Contains(string(body), want) {
			t.Errorf("importing with %q = %d %s", want, status, body)
		}
	}
}
