package main

import (
	"reflect"
	"strings"
	"testing"
)

// What a board uses: the procedures its processes run, on boards inside
// it too, and the flows its objects' copies follow, parts' included.
func TestBoardUses(t *testing.T) {
	b := &setupBoard{
		Processes: []setupProcess{
			{Name: "Check", Runs: "Review"},
			{Name: "Plain"},
			{Name: "Group", Kind: kindFlow, Board: &setupBoard{
				Processes: []setupProcess{{Name: "Inner", Runs: "Deep"}},
				Objects:   []setupObject{{Name: "Note", CopyOf: &setupSource{Flow: "Notes"}}},
			}},
		},
		Objects: []setupObject{
			{Name: "Customer", CopyOf: &setupSource{Flow: "Customer"}},
			{Name: "Order", Parts: []setupObject{{Name: "address", CopyOf: &setupSource{Flow: "Address", Object: "street"}}}},
		},
	}
	var got []string
	boardUses(b, func(name string, runs bool) {
		if runs {
			name = "runs " + name
		} else {
			name = "copies " + name
		}
		got = append(got, name)
	})
	want := []string{"runs Review", "runs Deep", "copies Notes", "copies Customer", "copies Address"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("boardUses = %v, want %v", got, want)
	}
	boardUses(nil, func(string, bool) { t.Error("a missing board uses nothing") })
}

func importEntries(entries ...*setupEntry) *importer {
	im := &importer{entries: map[string]*setupEntry{}, existing: map[string][]flowInfo{}}
	for _, e := range entries {
		im.entries[e.name] = e
	}
	return im
}

func runsFlow(name string, runs ...string) *setupEntry {
	f := &setupFlow{Name: name}
	for _, r := range runs {
		f.Processes = append(f.Processes, setupProcess{Name: "Run " + r, Runs: r})
	}
	return &setupEntry{name: name, kind: entryFlow, flow: f}
}

func runsProcedure(name string, runs ...string) *setupEntry {
	e := runsFlow(name, runs...)
	e.kind = entryProcedure
	return e
}

func copiesStruct(name string, from ...string) *setupEntry {
	o := &setupObject{Name: name}
	for _, f := range from {
		o.Parts = append(o.Parts, setupObject{Name: "part", CopyOf: &setupSource{Flow: f}})
	}
	return &setupEntry{name: name, kind: entryStruct, object: o}
}

func entryNames(es []*setupEntry) []string {
	var names []string
	for _, e := range es {
		names = append(names, e.name)
	}
	return names
}

// Entries are imported after the procedures they run and the structs and
// procedures their copies follow, in the file's order otherwise.
func TestImporterOrder(t *testing.T) {
	entries := []*setupEntry{
		runsFlow("Triage", "Review"),
		copiesStruct("Order", "Customer"),
		runsProcedure("Review", "Check"),
		copiesStruct("Customer"),
		runsProcedure("Check"),
		runsFlow("Nightly", "Existing"),
	}
	im := importEntries(entries...)
	im.existing["Existing"] = []flowInfo{{id: 9, module: true}}
	order, err := im.order(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Customer", "Check", "Nightly", "Order", "Review", "Triage"}
	if got := entryNames(order); !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestImporterOrderProblems(t *testing.T) {
	for _, c := range []struct {
		name     string
		entries  []*setupEntry
		existing map[string][]flowInfo
		problem  string
	}{
		{"missing", []*setupEntry{runsFlow("F", "Gone")}, nil,
			`"F" refers to "Gone", which is neither in the file nor in the app`},
		{"runs a struct of the file", []*setupEntry{runsFlow("F", "S"), copiesStruct("S")}, nil,
			`"F" runs "S", which is a struct`},
		{"runs a struct of the app", []*setupEntry{runsFlow("F", "S")}, map[string][]flowInfo{"S": {{id: 1, isStruct: true}}},
			`"F" runs "S", which is a struct`},
		{"copies a flow of the app", []*setupEntry{copiesStruct("S", "F")}, map[string][]flowInfo{"F": {{id: 1}}},
			`"S" copies an object of "F", but copies follow a struct's object or a procedure's`},
		{"runs a flow of the file", []*setupEntry{runsFlow("F", "G"), runsFlow("G")}, nil,
			`"F" runs or copies from "G", so "G" belongs under procedures`},
		{"runs itself", []*setupEntry{runsProcedure("P", "P")}, nil,
			`"P" can't run or copy from itself`},
		{"copies itself", []*setupEntry{copiesStruct("S", "S")}, nil,
			`"S" can't run or copy from itself`},
		{"two in the app", []*setupEntry{runsFlow("F", "P")}, map[string][]flowInfo{"P": {{id: 1, module: true}, {id: 2, module: true}}},
			`"F": the app has more than one flow named "P"; rename them apart first`},
		{"circle", []*setupEntry{runsProcedure("A", "B"), runsProcedure("B", "A")}, nil,
			`"A" and "B" run or copy from each other in a circle`},
	} {
		im := importEntries(c.entries...)
		if c.existing != nil {
			im.existing = c.existing
		}
		_, err := im.order(c.entries)
		if _, ok := err.(setupProblem); !ok || !strings.Contains(err.Error(), c.problem) {
			t.Errorf("%s: order error = %v, want %q", c.name, err, c.problem)
		}
	}
}

func layoutFloat(f float64) *float64 { return &f }

// A builder for laying out board, as walking the board gives it: each
// process and object by identifier, none of them on the flow's board yet.
func layoutBuilder(board *setupBoard) *builder {
	b := &builder{procs: map[string]*bproc{}, objs: map[string]*bobj{}}
	next := int64(1)
	var walkObject func(o *setupObject)
	walkObject = func(o *setupObject) {
		b.objs[ident(o.Name, o.Key)] = &bobj{id: next, o: o}
		next++
		for i := range o.Parts {
			walkObject(&o.Parts[i])
		}
	}
	var walk func(board *setupBoard, parent *int64)
	walk = func(board *setupBoard, parent *int64) {
		for i := range board.Processes {
			p := &board.Processes[i]
			bp := &bproc{id: next, p: p, parent: parent}
			next++
			b.procs[ident(p.Name, p.Key)] = bp
			if p.Board != nil {
				walk(p.Board, &bp.id)
			}
		}
		for i := range board.Objects {
			walkObject(&board.Objects[i])
		}
	}
	walk(board, nil)
	return b
}

type layoutAt struct{ x, y float64 }

// Processes go in rows down from the start as they follow one another, a
// row for each further one nothing leads to; objects beside the first
// process linking to them, stacked, or in a row below the processes when
// none does; parts in a row on their owner's board. What the file or the
// board places stays where it is.
func TestBuilderLayout(t *testing.T) {
	board := &setupBoard{
		Start: &setupStart{Next: "A"},
		Processes: []setupProcess{
			{Name: "A", Next: []string{"B"}, OnFailure: []string{"C"}},
			{Name: "B", Takes: []setupLink{{Object: "O"}}, Yields: []setupLink{{Object: "P"}}},
			{Name: "C", Takes: []setupLink{{Object: "O"}}},
			{Name: "D"},
			{Name: "E", X: layoutFloat(500), Y: layoutFloat(50)},
			{Name: "F"},
			{Name: "G", Kind: kindFlow, Board: &setupBoard{
				Start:     &setupStart{Next: "I"},
				Processes: []setupProcess{{Name: "I"}},
				Objects:   []setupObject{{Name: "J"}},
			}},
		},
		Objects: []setupObject{
			{Name: "O"},
			{Name: "P"},
			{Name: "Q", Parts: []setupObject{{Name: "q1"}, {Name: "q2", X: layoutFloat(7), Y: layoutFloat(8)}, {Name: "q3"}}},
			{Name: "R", X: layoutFloat(1), Y: layoutFloat(2)},
			{Name: "S"},
		},
	}
	b := layoutBuilder(board)
	// F is on the board already, where it stays.
	b.procs["F"].cur = &process{X: 900, Y: 900}
	b.objs["q3"].cur = &snapshotObject{X: 11, Y: 12}
	b.layout(board, nil)

	want := map[string]layoutAt{
		"A": {40, 140}, "B": {40, 300}, "C": {320, 300},
		"D": {40, 460}, "E": {500, 50}, "F": {900, 900}, "G": {40, 620},
		"I": {40, 140},
	}
	for name, at := range want {
		bp := b.procs[name]
		if !bp.placed || (layoutAt{bp.x, bp.y}) != at {
			t.Errorf("process %s at %v (placed %v), want %v", name, layoutAt{bp.x, bp.y}, bp.placed, at)
		}
	}
	wantObjs := map[string]layoutAt{
		"O": {300, 300}, "P": {300, 370},
		"Q": {40, 1060}, "R": {1, 2}, "S": {300, 1060},
		"q1": {40, 140}, "q2": {7, 8}, "q3": {11, 12},
		"J": {40, 300},
	}
	for name, at := range wantObjs {
		bo := b.objs[name]
		if !bo.placed || (layoutAt{bo.x, bo.y}) != at {
			t.Errorf("object %s at %v (placed %v), want %v", name, layoutAt{bo.x, bo.y}, bo.placed, at)
		}
	}
}

// A board with no processes has its objects in a row at the top.
func TestBuilderLayoutObjectsAlone(t *testing.T) {
	board := &setupBoard{Objects: []setupObject{{Name: "A"}, {Name: "B"}}}
	b := layoutBuilder(board)
	b.layout(board, nil)
	for name, at := range map[string]layoutAt{"A": {40, 40}, "B": {300, 40}} {
		if bo := b.objs[name]; (layoutAt{bo.x, bo.y}) != at {
			t.Errorf("object %s at %v, want %v", name, layoutAt{bo.x, bo.y}, at)
		}
	}
}
