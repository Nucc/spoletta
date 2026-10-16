package main

import (
	"maps"
	"testing"
)

// The flow a subflow process calls: Brief and Report on its top board, a
// Brief inside a flow process too, Summary in no state, and Notes, which it
// is given rather than generates.
func calleeBoard() snapshot {
	return snapshot{
		Objects: []snapshotObject{
			{ID: 100, Name: "Brief", Parent: ptr(99)},
			{ID: 101, Name: "Brief"},
			{ID: 150, Name: "Report"},
			{ID: 155, Name: "Part", Owner: ptr(150)},
			{ID: 160, Name: "Summary"},
			{ID: 170, Name: "Notes"},
		},
		States: []objectState{
			{ID: 200, Object: 101, Name: "ready"},
			{ID: 151, Object: 150, Name: "ok"},
			{ID: 152, Object: 150, Name: "unknown here"},
		},
	}
}

// The caller: Call, a subflow process, takes Brief and Draft and generates
// Report, Summary in its final state, and Notes.
func callerBoard() snapshot {
	return snapshot{
		Processes: []process{{ID: 40, Name: "Call", Kind: kindSubflow}},
		Objects: []snapshotObject{
			{ID: 10, Name: "Brief"}, {ID: 11, Name: "Draft"},
			{ID: 50, Name: "Report"}, {ID: 51, Name: "Summary"}, {ID: 54, Name: "Notes"},
		},
		States: []objectState{
			{ID: 20, Object: 10, Name: "ready"},
			{ID: 21, Object: 10, Name: "late"},
			{ID: 52, Object: 51, Name: "final"},
			{ID: 53, Object: 50, Name: "ok"},
		},
		Links: []snapshotLink{
			{ID: 1, Process: 40, Object: 10, Direction: directionInput},
			{ID: 2, Process: 40, Object: 11, Direction: directionInput},
			{ID: 3, Process: 40, Object: 50, Direction: directionOutput},
			{ID: 4, Process: 40, Object: 51, Direction: directionOutput, State: 52},
			{ID: 5, Process: 40, Object: 54, Direction: directionOutput},
		},
	}
}

func TestCalleeTopObjectAndState(t *testing.T) {
	fr := newFlowRun(2, 1, calleeBoard(), nil, "")
	for name, want := range map[string]int64{"Brief": 101, "Report": 150, "Part": 0, "Draft": 0} {
		if got := fr.topObject(name); got != want {
			t.Errorf("topObject(%q) = %d, want %d", name, got, want)
		}
	}
	for _, c := range []struct {
		object int64
		name   string
		want   int64
	}{{101, "ready", 200}, {150, "ok", 151}, {150, "ready", 0}, {101, "", 0}} {
		if got := fr.stateOf(c.object, c.name); got != c.want {
			t.Errorf("stateOf(%d, %q) = %d, want %d", c.object, c.name, got, c.want)
		}
	}
}

// What a subflow process takes goes to its namesake on the called flow's top
// board, in its state's namesake there, or in none; what has no namesake
// isn't given.
func TestToCallee(t *testing.T) {
	w := walkOn(callerBoard(), nil, nil)
	callee := newFlowRun(2, 1, calleeBoard(), nil, "")
	for _, c := range []struct {
		given map[int64]value
		want  map[int64]value
	}{
		{map[int64]value{10: {content: "b", state: 20}}, map[int64]value{101: {content: "b", state: 200}}},
		{map[int64]value{10: {content: "b", state: 21}}, map[int64]value{101: {content: "b"}}},
		{map[int64]value{10: {content: "b"}}, map[int64]value{101: {content: "b"}}},
		{map[int64]value{11: {content: "d"}}, map[int64]value{}},
	} {
		if got := w.toCallee(callee, c.given); !maps.Equal(got, c.want) {
			t.Errorf("toCallee(%v) = %v, want %v", c.given, got, c.want)
		}
	}
}

// What comes out of the called flow is what its run generated of the
// namesakes of the objects the subflow process generates, in its state's
// namesake here, or else in the state the link names.
func TestFromCallee(t *testing.T) {
	w := walkOn(callerBoard(), nil, nil)
	callee := newFlowRun(2, 1, calleeBoard(), nil, "")
	inner := walkOn(calleeBoard(), nil, nil)
	inner.values[150] = value{content: "r", state: 151}
	inner.values[160] = value{content: "s"}
	inner.generated[150] = true
	inner.generated[160] = true
	want := map[int64]value{50: {content: "r", state: 53}, 51: {content: "s", state: 52}}
	if got := w.fromCallee(40, callee, inner); !maps.Equal(got, want) {
		t.Errorf("fromCallee = %v, want %v", got, want)
	}
	inner.values[150] = value{content: "r", state: 152}
	if got := w.fromCallee(40, callee, inner)[50]; got != (value{content: "r"}) {
		t.Errorf("Report in a state with no namesake = %v, want in none", got)
	}
}
