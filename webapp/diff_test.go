package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// Write drafts from the brief and Review decides the draft, approving it,
// which Publish takes; Review takes the reviewer, a person.
func diffBoard() snapshot {
	return snapshot{
		Processes: []process{
			{ID: 1, Name: "Write", Kind: kindAgent, Prompt: "Draft it.", X: 10, Y: 10},
			{ID: 2, Name: "Review", Kind: kindHuman, X: 200, Y: 10},
			{ID: 3, Name: "Publish", Kind: kindScript, Script: "publish"},
		},
		Objects: []snapshotObject{
			{ID: 10, Name: "Brief"},
			{ID: 11, Name: "Draft", Persistent: true},
			{ID: 12, Name: "Reviewer", Human: true},
		},
		States:     []objectState{{ID: 20, Object: 11, Name: "approved"}},
		Attributes: []attribute{{ID: 30, Object: 11, Name: "Title", Definition: "Its title"}},
		Links: []snapshotLink{
			{ID: 40, Process: 1, Object: 10, Direction: directionInput},
			{ID: 41, Process: 1, Object: 11, Direction: directionOutput},
			{ID: 42, Process: 2, Object: 11, Direction: directionInput},
			{ID: 43, Process: 2, Object: 11, Direction: directionOutput},
			{ID: 44, Process: 3, Object: 11, Direction: directionInput, State: 20},
			{ID: 45, Process: 2, Object: 12, Direction: directionInput},
		},
		LinkStates:  []snapshotLinkState{{Link: 43, State: 20}},
		Connections: []connection{{ID: 50, Source: 1, Target: 2}},
		Start:       startNode{Target: versionID(1)},
		Settings:    &flowSettings{Repositories: []repository{}},
	}
}

// A copy of a board, to change.
func cloneBoard(t *testing.T, s snapshot) snapshot {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var c snapshot
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The change, id and name of each item, for comparing.
type diffSummary struct {
	change string
	id     int64
	name   string
}

func summarize(items []diffItem) []diffSummary {
	out := []diffSummary{}
	for _, it := range items {
		out = append(out, diffSummary{it.Change, it.ID, it.Name})
	}
	return out
}

func fieldNames(it diffItem) []string {
	var out []string
	for _, f := range it.Fields {
		out = append(out, f.Field)
	}
	return out
}

func TestDiffOfTheSameBoardIsEmpty(t *testing.T) {
	d := diffSnapshots(diffBoard(), diffBoard())
	if d.changed() || d.Layout {
		t.Errorf("diff of a board with itself = %+v", d)
	}
}

// Moving things, resizing and rerouting are layout, which the diff says
// changed without listing anything.
func TestDiffLayoutOnly(t *testing.T) {
	b := cloneBoard(t, diffBoard())
	b.Processes[0].X = 400
	w := 300.0
	b.Objects[1].Width = &w
	b.Links[1].Route = json.RawMessage(`[{"x":1,"y":2}]`)
	d := diffSnapshots(diffBoard(), b)
	if d.changed() {
		t.Errorf("a moved board changed: %+v", d)
	}
	if !d.Layout {
		t.Error("moving things isn't a change of layout")
	}
}

func TestDiffOfProcesses(t *testing.T) {
	b := cloneBoard(t, diffBoard())
	b.Processes[0].Prompt = "Draft it well."
	b.Processes[0].Model = "anthropic/claude"
	b.Processes[0].Settings.Timeout = count(60)
	b.Processes = slices.DeleteFunc(b.Processes, func(p process) bool { return p.ID == 3 })
	b.Links = slices.DeleteFunc(b.Links, func(l snapshotLink) bool { return l.Process == 3 })
	b.Processes = append(b.Processes, process{ID: 4, Name: "Announce", Kind: kindAgent})
	d := diffSnapshots(diffBoard(), b)

	want := []diffSummary{{diffChanged, 1, "Write"}, {diffAdded, 4, "Announce"}, {diffRemoved, 3, "Publish"}}
	if got := summarize(d.Processes); !reflect.DeepEqual(got, want) {
		t.Errorf("processes = %+v, want %+v", got, want)
	}
	if got := fieldNames(d.Processes[0]); !reflect.DeepEqual(got, []string{"prompt", "model", "timeout"}) {
		t.Errorf("Write's fields = %v", got)
	}
	if f := d.Processes[0].Fields[0]; f.Before != "Draft it." || f.After != "Draft it well." {
		t.Errorf("the prompt went from %q to %q", f.Before, f.After)
	}
	if f := d.Processes[0].Fields[2]; f.Before != "" || f.After != "60" {
		t.Errorf("the timeout went from %q to %q", f.Before, f.After)
	}
	// Publish's link went with it.
	if got := summarize(d.Links); !reflect.DeepEqual(got, []diffSummary{{diffRemoved, 44, "Draft (approved) → Publish"}}) {
		t.Errorf("links = %+v", got)
	}
}

func TestDiffOfObjectsStatesAndAttributes(t *testing.T) {
	b := cloneBoard(t, diffBoard())
	b.Objects[1].Name = "Article"
	b.Objects[1].Persistent = false
	b.States[0].Rule = `title != ""`
	b.States = append(b.States, objectState{ID: 21, Object: 11, Name: "rejected"})
	b.Attributes = nil
	d := diffSnapshots(diffBoard(), b)

	if got := summarize(d.Objects); !reflect.DeepEqual(got, []diffSummary{{diffChanged, 11, "Article"}}) {
		t.Fatalf("objects = %+v", got)
	}
	if got := fieldNames(d.Objects[0]); !reflect.DeepEqual(got, []string{"name", "stored"}) {
		t.Errorf("Article's fields = %v", got)
	}
	want := []diffSummary{{diffChanged, 20, "Article: approved"}, {diffAdded, 21, "Article: rejected"}}
	if got := summarize(d.States); !reflect.DeepEqual(got, want) {
		t.Errorf("states = %+v, want %+v", got, want)
	}
	if got := summarize(d.Attributes); !reflect.DeepEqual(got, []diffSummary{{diffRemoved, 30, "Draft: Title"}}) {
		t.Errorf("attributes = %+v", got)
	}
	if o := d.States[0].Object; o == nil || *o != 11 {
		t.Errorf("the state is of object %v", o)
	}
}

// A board made over by a setup file gives what it renamed, or made anew, new
// ids: what is left unmatched by id is matched by name in the same place,
// and links by their ends as matched.
func TestDiffMatchesByNameWhereIdsChanged(t *testing.T) {
	b := cloneBoard(t, diffBoard())
	renumber := map[int64]int64{3: 103}
	for i, p := range b.Processes {
		if n, ok := renumber[p.ID]; ok {
			b.Processes[i].ID = n
		}
	}
	b.Objects[0].ID = 110
	b.States[0].ID = 120
	b.LinkStates[0] = snapshotLinkState{Link: 143, State: 120}
	for i, l := range b.Links {
		b.Links[i].ID = l.ID + 100
		if n, ok := renumber[l.Process]; ok {
			b.Links[i].Process = n
		}
		if l.Object == 10 {
			b.Links[i].Object = 110
		}
		if l.State == 20 {
			b.Links[i].State = 120
		}
	}
	b.Processes[2].Script = "publish --now"
	d := diffSnapshots(diffBoard(), b)

	if got := summarize(d.Processes); !reflect.DeepEqual(got, []diffSummary{{diffChanged, 103, "Publish"}}) {
		t.Errorf("processes = %+v", got)
	}
	if len(d.Objects)+len(d.States)+len(d.Links)+len(d.Connections) != 0 {
		t.Errorf("renumbering alone changed %+v", d)
	}
}

func TestDiffOfLinksAndConnections(t *testing.T) {
	b := cloneBoard(t, diffBoard())
	b.Links[0].Role = roleInstrument
	b.Links = append(b.Links, snapshotLink{ID: 46, Process: 3, Object: 10, Direction: directionInput})
	b.Connections = []connection{{ID: 51, Source: 2, Target: 3}, {ID: 52, Source: 1, Target: 3, Kind: connectionFailure}}
	b.Start.Target = versionID(2)
	d := diffSnapshots(diffBoard(), b)

	want := []diffSummary{{diffChanged, 40, "Brief → Write"}, {diffAdded, 46, "Brief → Publish"}}
	if got := summarize(d.Links); !reflect.DeepEqual(got, want) {
		t.Errorf("links = %+v, want %+v", got, want)
	}
	want = []diffSummary{
		{diffAdded, 51, "Review → Publish"}, {diffAdded, 52, "Write → Publish, on failure"}, {diffAdded, 0, "Start → Review"},
		{diffRemoved, 50, "Write → Review"}, {diffRemoved, 0, "Start → Write"},
	}
	if got := summarize(d.Connections); !reflect.DeepEqual(got, want) {
		t.Errorf("connections = %+v, want %+v", got, want)
	}
}

// The decision states a link lists are part of it.
func TestDiffOfDecisionStates(t *testing.T) {
	b := cloneBoard(t, diffBoard())
	b.States = append(b.States, objectState{ID: 21, Object: 11, Name: "rejected"})
	b.LinkStates = append(b.LinkStates, snapshotLinkState{Link: 43, State: 21})
	d := diffSnapshots(diffBoard(), b)
	if len(d.Links) != 1 || d.Links[0].ID != 43 {
		t.Fatalf("links = %+v", d.Links)
	}
	if f := d.Links[0].Fields; len(f) != 1 || f[0].Before != "approved" || f[0].After != "approved, rejected" {
		t.Errorf("fields = %+v", f)
	}
}

// The flow's settings are compared too, unless a version is from before
// they were kept.
func TestDiffOfFlowSettings(t *testing.T) {
	b := cloneBoard(t, diffBoard())
	b.Settings = &flowSettings{Repositories: []repository{{Name: "app", URL: "https://example.com/app.git"}}, RunSpendLimit: 2.5}
	d := diffSnapshots(diffBoard(), b)
	var got []string
	for _, f := range d.Settings {
		got = append(got, f.Field+"="+f.After)
	}
	if want := []string{"repositories=app https://example.com/app.git", "run spend limit=$2.5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("settings = %v, want %v", got, want)
	}
	old := diffBoard()
	old.Settings = nil
	if d := diffSnapshots(old, b); len(d.Settings) != 0 {
		t.Errorf("a version from before settings were kept differs in %+v", d.Settings)
	}
}

// Flow processes' boards: a process moved onto another board is changed,
// and a board's start counts as a connection from its flow process.
func TestDiffOfInnerBoards(t *testing.T) {
	a := diffBoard()
	a.Processes = append(a.Processes, process{ID: 5, Name: "Inside", Kind: kindFlow}, process{ID: 6, Name: "Step", Kind: kindAgent, Parent: versionID(5)})
	a.Boards = []innerBoard{{Process: 5, Start: startNode{Target: versionID(6)}}}
	b := cloneBoard(t, a)
	b.Processes[1].Parent = versionID(5)
	b.Boards[0].Start.Target = versionID(2)
	b.Boards[0].End = boardEnd{X: 5}
	d := diffSnapshots(a, b)

	if len(d.Processes) != 1 || d.Processes[0].ID != 2 || !reflect.DeepEqual(fieldNames(d.Processes[0]), []string{"board"}) {
		t.Fatalf("processes = %+v", d.Processes)
	}
	if f := d.Processes[0].Fields[0]; f.Before != "the top board" || f.After != "Inside" {
		t.Errorf("Review moved from %q to %q", f.Before, f.After)
	}
	want := []diffSummary{{diffAdded, 0, "Start → Review"}, {diffRemoved, 0, "Start → Step"}}
	if got := summarize(d.Connections); !reflect.DeepEqual(got, want) {
		t.Errorf("connections = %+v, want %+v", got, want)
	}
	if !d.Layout {
		t.Error("moving the inner board's end isn't layout")
	}
}

// A process's samples are listed: matched by id, or by name on its process
// when made anew, as an import does, each change saying what it gives each
// object differently. They don't make the board differ from what it was.
func TestDiffOfSamples(t *testing.T) {
	a := diffBoard()
	a.Samples = []processSample{
		{ID: 60, Process: 1, Name: "Short", Inputs: sampleInputs{{Object: 10, Content: "one line"}}},
		{ID: 61, Process: 1, Name: "Long", Inputs: sampleInputs{{Object: 10, Content: "many lines"}}},
		{ID: 62, Process: 3, Name: "Approved", Inputs: sampleInputs{{Object: 11, State: 20, Content: "draft"}}},
		{ID: 63, Process: 1, Name: "Gone", Inputs: sampleInputs{}},
	}
	b := cloneBoard(t, a)
	b.Samples[0].Inputs[0].Content = "two lines"
	b.Samples[1].Name = "Longest"
	// Made anew under the same name, with the state left out.
	b.Samples[2] = processSample{ID: 70, Process: 3, Name: "Approved", Inputs: sampleInputs{{Object: 11, Content: "draft"}}}
	b.Samples[3] = processSample{ID: 71, Process: 2, Name: "Reviewed", Inputs: sampleInputs{}}

	d := diffSnapshots(a, b)
	want := []diffSummary{
		{diffChanged, 60, "Write: Short"},
		{diffChanged, 61, "Write: Longest"},
		{diffChanged, 70, "Publish: Approved"},
		{diffAdded, 71, "Review: Reviewed"},
		{diffRemoved, 63, "Write: Gone"},
	}
	if got := summarize(d.Samples); !reflect.DeepEqual(got, want) {
		t.Fatalf("samples = %v, want %v", got, want)
	}
	if got := d.Samples[0].Fields; len(got) != 1 || got[0] != (diffField{"Brief", "one line", "two lines"}) {
		t.Errorf("Short's fields = %+v, want what it gives Brief", got)
	}
	if got := fieldNames(d.Samples[1]); !slices.Equal(got, []string{"name"}) {
		t.Errorf("Long's fields = %v, want its name", got)
	}
	if got := d.Samples[2].Fields; len(got) != 1 || got[0] != (diffField{"Draft state", "approved", ""}) {
		t.Errorf("Approved's fields = %+v, want Draft's state gone", got)
	}
	if p := d.Samples[3].Process; p == nil || *p != 2 {
		t.Errorf("the sample added is of process %v, want Review", p)
	}
	if d.changed() || d.Layout {
		t.Errorf("samples alone changed the board: %+v", d)
	}
	if d := diffSnapshots(a, a); len(d.Samples) != 0 {
		t.Errorf("the same samples differ: %+v", d.Samples)
	}
}
