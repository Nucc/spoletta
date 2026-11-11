package tests

// A struct's copy on a board named for itself, such as Gemini Review for a
// copy of Review Comment: a process tells what it takes apart by name, so
// several copies of one struct reach it only so. The rest of the copy
// follows the struct still, and the name stays through the struct's
// changes, versions and setup files.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

const reviewersFile = `spoletta: 1
structs:
  - name: SN Review Comment
    attributes:
      - {name: comment}
    parts:
      - {name: meta}
flows:
  - name: SN Reviews
    start: {next: Seed}
    processes:
      - name: Seed
        prompt: Find the pull request.
        yields: [SN PR]
      - name: Review with Gemini
        prompt: Review it.
        takes: [SN PR]
        yields: [Gemini Review]
      - name: Review with GPT
        prompt: Review it.
        takes: [SN PR]
        yields: [GPT Review]
      - name: Review with Claude
        prompt: Review it.
        takes: [SN PR]
        yields: [Claude Review]
      - name: Synthesize
        prompt: Sum the reviews up.
        takes: [Gemini Review, GPT Review, Claude Review]
        yields: [SN Summary]
    objects:
      - name: SN PR
      - name: SN Summary
      - name: Gemini Review
        copyOf: {flow: SN Review Comment}
      - name: GPT Review
        copyOf: {flow: SN Review Comment}
      - name: Claude Review
        copyOf: {flow: SN Review Comment}
`

// Three reviewers each generate a copy of one struct named for
// themselves, and the process taking all three runs once every one came,
// with each by its name.
func TestStructCopiesNamedForThemselves(t *testing.T) {
	a := newApp(t)
	ids := map[string]int64{}
	for _, f := range setupImportOK(a, reviewersFile).Flows {
		ids[f.Name] = f.ID
	}
	s, f := ids["SN Review Comment"], ids["SN Reviews"]
	if s == 0 || f == 0 {
		t.Fatalf("imported %v, want the struct and the flow", ids)
	}
	root := modNamed(t, modObjects(a, s), "SN Review Comment")
	for _, name := range []string{"Gemini Review", "GPT Review", "Claude Review"} {
		cp := modNamed(t, modObjects(a, f), name)
		if cp.Source == nil || *cp.Source != root.ID || len(cp.Attributes) != 1 || cp.Attributes[0].Name != "comment" {
			t.Errorf("%s = %+v, want a copy of the struct's object %d", name, cp, root.ID)
		}
	}

	a.worker.onAgent("Seed", handsIn(data("diff")))
	for _, who := range []string{"Gemini", "GPT", "Claude"} {
		a.worker.onAgent("Review with "+who, handsIn(map[string]any{"comment": who + " says fine", "meta": "m"}))
	}
	a.worker.onAgent("Synthesize", handsIn(data("all fine")))
	d := a.finish(a.startRun(f).ID)
	syn := onlyStep(t, d, "Synthesize")
	if d.Status != runDone || syn.Status != stepDone {
		t.Fatalf("run %s with steps %s", d.Status, describeSteps(d))
	}
	took := map[string]string{}
	for _, in := range syn.Inputs {
		took[in.Name] = in.Content
	}
	for _, who := range []string{"Gemini", "GPT", "Claude"} {
		if !strings.Contains(took[who+" Review"], who+" says fine") {
			t.Errorf("Synthesize took %+v, want %s's review by its name", syn.Inputs, who)
		}
	}

	// The OPL says which struct each is; a setup file keeps their names.
	var opl struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/opl", f), nil, http.StatusOK, &opl)
	if !strings.Contains(opl.Text, `"Gemini Review" is an instance of "SN Review Comment".`) {
		t.Errorf("the OPL doesn't say what Gemini Review is:\n%s", opl.Text)
	}
	var setup struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", f), nil, http.StatusOK, &setup)
	if !strings.Contains(setup.Text, "name: Gemini Review") || !strings.Contains(setup.Text, "flow: SN Review Comment") {
		t.Errorf("the setup file loses the copy's own name:\n%s", setup.Text)
	}
	setupImportOK(a, reviewersFile)
	modNamed(t, modObjects(a, f), "Gemini Review")
}

// A struct's copy on a board is renamed by hand, keeps its name as the
// struct changes, and follows the struct's name again once named as it
// is. Its parts, and a copy of a procedure's object, keep theirs.
func TestRenamingStructCopies(t *testing.T) {
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Review Comment", "struct": true}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	var meta object
	a.call("POST", "/api/objects", map[string]any{"name": "meta", "owner": root.ID}, http.StatusCreated, &meta)

	f := a.newFlow("Reviews")
	synth := a.agent(f.ID, "Synthesize", "Sum up.")
	copyOf := func() modObject {
		t.Helper()
		var cp modObject
		a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": f.ID, "x": 300, "y": 200}, http.StatusCreated, &cp)
		return cp
	}
	gemini, gpt := copyOf(), copyOf()
	a.input(synth.ID, gemini.ID)
	// Two of one name can't both go in.
	a.refused("POST", "/api/links", map[string]any{"process": synth.ID, "object": gpt.ID, "direction": directionInput}, http.StatusBadRequest, "already takes an object named")
	verPatch(a, fmt.Sprintf("/api/objects/%d", gpt.ID), map[string]any{"name": "GPT Review"})
	a.input(synth.ID, gpt.ID)
	// Nor renamed to share one.
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", gpt.ID), map[string]any{"name": "Review Comment"}, http.StatusBadRequest, "already takes an object named")
	verPatch(a, fmt.Sprintf("/api/objects/%d", gemini.ID), map[string]any{"name": "Gemini Review"})

	// What makes a copy up keeps the struct's names.
	part := modPartsOf(modObjects(a, f.ID), gemini.ID)
	if len(part) != 1 {
		t.Fatalf("Gemini Review's parts = %+v", part)
	}
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", part[0].ID), map[string]any{"name": "mine"}, http.StatusBadRequest, "comes from the struct")

	// The struct's changes reach the copies; its name doesn't, but on a
	// copy named as it is.
	plain := copyOf()
	a.newState(root.ID, "posted")
	verPatch(a, fmt.Sprintf("/api/flows/%d", s.ID), map[string]any{"name": "Review Note"})
	objs := modObjects(a, f.ID)
	for _, name := range []string{"Gemini Review", "GPT Review"} {
		if cp := modNamed(t, objs, name); modStateNames(cp) != "posted" {
			t.Errorf("%s's states = %s, want the struct's", name, modStateNames(cp))
		}
	}
	if cp := modNamed(t, objs, "Review Note"); cp.ID != plain.ID {
		t.Errorf("the copy following the struct's name = %+v, want %d", cp, plain.ID)
	}

	// A version keeps the name, and named as the struct it follows it again.
	v := verSave(a, f.ID)
	verPatch(a, fmt.Sprintf("/api/objects/%d", gemini.ID), map[string]any{"name": "Review Note 2"})
	verRestore(a, v.ID)
	modNamed(t, modObjects(a, f.ID), "Gemini Review")
	verPatch(a, fmt.Sprintf("/api/objects/%d", gemini.ID), map[string]any{"name": "Review Note"})
	a.call("DELETE", fmt.Sprintf("/api/objects/%d", plain.ID), nil, http.StatusNoContent, nil)
	verPatch(a, fmt.Sprintf("/api/flows/%d", s.ID), map[string]any{"name": "Review"})
	objs = modObjects(a, f.ID)
	if cp := modNamed(t, objs, "Review"); cp.ID != gemini.ID {
		t.Errorf("the copy named as its struct again = %+v, want it to follow the struct's name", cp)
	}
	modNamed(t, objs, "GPT Review")

	// A setup file names only a struct's copy on a board for itself.
	status, body := setupImport(a, `spoletta: 1
procedures:
  - name: SN Fetch
    start: {next: Fetch}
    processes:
      - {name: Fetch, prompt: Fetch., yields: [Found]}
    objects:
      - name: Found
flows:
  - name: SN Uses fetch
    start: {next: Run fetch}
    processes:
      - {name: Run fetch, kind: subflow, runs: SN Fetch}
    objects:
      - name: Mine
        copyOf: {flow: SN Fetch, object: Found}
`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "only a struct's copy on a board may be named for itself") {
		t.Errorf("importing a procedure's object's copy named for itself = %d %s", status, body)
	}
}
