package tests

// Input roles, as in OPM: an instrument a process uses without changing it,
// an object it consumes, so it is gone once taken, and an object it takes
// and generates, which it affects.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func rolesTakes(a *app, process, object int64, role string) link {
	a.t.Helper()
	return a.link(map[string]any{"process": process, "object": object, "direction": directionInput, "role": role})
}

func rolesInput(t *testing.T, s step, name string) runInput {
	t.Helper()
	for _, in := range s.Inputs {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("%s took %+v, without %q", s.ProcessName, s.Inputs, name)
	return runInput{}
}

// An instrument and an affected object go to the agent marked so, and it is
// told what each means.
func TestInstrumentAndAffectedInputs(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Roles")
	seed := a.agent(f.ID, "Seed", "Seed.")
	guide := a.newObject(f.ID, "Style guide")
	doc := a.newObject(f.ID, "Doc")
	edit := a.agent(f.ID, "Edit", "Edit the doc.")
	a.output(seed.ID, doc.ID)
	l := rolesTakes(a, edit.ID, guide.ID, roleInstrument)
	if l.Role != roleInstrument {
		t.Errorf("link = %+v", l)
	}
	a.input(edit.ID, doc.ID)
	a.output(edit.ID, doc.ID)
	a.startAt(f.ID, seed.ID)
	a.worker.onAgent("Seed", handsIn(data("draft")))
	a.worker.onAgent("Edit", handsIn(data("edited")))

	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
	}
	s := onlyStep(t, d, "Edit")
	if in := rolesInput(t, s, "Doc"); in.Role != inputAffected {
		t.Errorf("Doc taken as %q, want affected", in.Role)
	}
	prompt := a.worker.runsOf("Edit")[0].Prompt
	for _, want := range []string{
		`<input name="Doc" role="affected">`,
		"is yours to change: what you hand in for it is its new version.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	// The guide nobody generates exists from the start, so it doesn't go in
	// as an input; one that is generated does.
	if strings.Contains(prompt, `name="Style guide"`) {
		t.Errorf("an object nobody generated went in:\n%s", prompt)
	}

	writer := a.agent(f.ID, "Guide writer", "Write the guide.")
	a.output(writer.ID, guide.ID)
	a.startAt(f.ID, writer.ID)
	a.call("POST", "/api/connections", map[string]any{"source": writer.ID, "target": seed.ID}, http.StatusCreated, nil)
	a.worker.onAgent("Guide writer", handsIn(data("be brief")))
	d = a.finish(a.startRun(f.ID).ID)
	if in := rolesInput(t, onlyStep(t, d, "Edit"), "Style guide"); in.Role != inputInstrument || !strings.Contains(in.Content, "be brief") {
		t.Errorf("the guide went in as %+v", in)
	}
	if prompt := a.worker.runsOf("Edit")[1].Prompt; !strings.Contains(prompt, `<input name="Style guide" role="instrument">`) ||
		!strings.Contains(prompt, "is a reference to work with: use it, but don't change it.") {
		t.Errorf("prompt doesn't mark the instrument:\n%s", prompt)
	}
}

// An object consumed is gone once its process takes it: a process waiting
// for it with something that comes later never runs on it.
func TestConsumedObjectIsGone(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("consumed %v", consumed), func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("Queue")
			seed := a.agent(f.ID, "Seed", "Seed.")
			item := a.newObject(f.ID, "Item")
			handle := a.agent(f.ID, "Handle", "Handle it.")
			late := a.newObject(f.ID, "Late")
			audit := a.agent(f.ID, "Audit", "Audit it.")
			a.output(seed.ID, item.ID)
			role := roleNone
			if consumed {
				role = roleConsumption
			}
			rolesTakes(a, handle.ID, item.ID, role)
			a.output(handle.ID, late.ID)
			a.input(audit.ID, item.ID)
			a.input(audit.ID, late.ID)
			a.startAt(f.ID, seed.ID)
			a.worker.onAgent("Seed", handsIn(data("job")))
			a.worker.onAgent("Handle", handsIn(data("handled")))
			a.worker.onAgent("Audit", handsIn(data("audited")))

			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != runDone {
				t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
			}
			if n := len(stepsOf(d, "Audit")); n != map[bool]int{false: 1, true: 0}[consumed] {
				t.Errorf("Audit ran %d times; steps %s", n, describeSteps(d))
			}
			if in := rolesInput(t, onlyStep(t, d, "Handle"), "Item"); consumed != (in.Role == inputConsumed) {
				t.Errorf("Item taken as %q", in.Role)
			}
		})
	}
}

// Roles are only an input's, a person has none, and one process consumes an
// object at most; a version keeps roles.
func TestRoleRefusals(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Role refusals")
	use := a.agent(f.ID, "Use", "Use it.")
	other := a.agent(f.ID, "Other", "Use it too.")
	review := humanHuman(a, f.ID, "Review")
	item := a.newObject(f.ID, "Item")
	person := humanPerson(a, f.ID, review.ID, "Editor")

	a.refused("POST", "/api/links", map[string]any{"process": use.ID, "object": item.ID, "direction": directionOutput, "role": roleInstrument}, http.StatusBadRequest, "only an input link has a role")
	a.refused("POST", "/api/links", map[string]any{"process": use.ID, "object": item.ID, "direction": directionInput, "role": "borrowed"}, http.StatusBadRequest, "role must be")
	a.refused("POST", "/api/links", map[string]any{"process": review.ID, "object": person.ID, "direction": directionInput, "role": roleConsumption}, http.StatusBadRequest, "neither its instrument nor consumed")
	l := rolesTakes(a, use.ID, item.ID, roleConsumption)
	a.refused("POST", "/api/links", map[string]any{"process": other.ID, "object": item.ID, "direction": directionInput, "role": roleConsumption}, http.StatusBadRequest, "consumes the object already")
	o := rolesTakes(a, other.ID, item.ID, roleNone)
	a.refused("PUT", fmt.Sprintf("/api/links/%d/role", o.ID), map[string]any{"role": roleConsumption}, http.StatusBadRequest, "consumes the object already")
	// A link can keep its own role, or change it.
	a.call("PUT", fmt.Sprintf("/api/links/%d/role", l.ID), map[string]any{"role": roleConsumption}, http.StatusNoContent, nil)
	a.call("PUT", fmt.Sprintf("/api/links/%d/role", o.ID), map[string]any{"role": roleInstrument}, http.StatusNoContent, nil)
	a.refused("PUT", "/api/links/999999999/role", map[string]any{"role": roleInstrument}, http.StatusNotFound, "link not found")

	var saved version
	a.call("POST", fmt.Sprintf("/api/versions?flow=%d", f.ID), nil, http.StatusCreated, &saved)
	a.call("PUT", fmt.Sprintf("/api/links/%d/role", l.ID), map[string]any{"role": roleNone}, http.StatusNoContent, nil)
	a.call("POST", fmt.Sprintf("/api/versions/%d/restore", saved.ID), nil, http.StatusNoContent, nil)
	roles := map[int64]string{}
	var ls []link
	a.call("GET", fmt.Sprintf("/api/links?flow=%d", f.ID), nil, http.StatusOK, &ls)
	for _, x := range ls {
		roles[x.Process] += x.Role
	}
	if roles[use.ID] != roleConsumption || roles[other.ID] != roleInstrument {
		t.Errorf("after restoring, roles %v", roles)
	}
}
