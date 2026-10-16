package main

import (
	"maps"
	"slices"
	"testing"
)

// MakeA makes A, which Grow takes to make B; Polish runs right after Grow,
// making C, and Rescue handles Polish failing, making D. Aside makes E on a
// branch of its own.
func rerunBoard() snapshot {
	return snapshot{
		Processes: []process{
			{ID: 1, Name: "MakeA", Kind: kindAgent},
			{ID: 2, Name: "Grow", Kind: kindAgent},
			{ID: 3, Name: "Polish", Kind: kindAgent},
			{ID: 4, Name: "Rescue", Kind: kindAgent},
			{ID: 5, Name: "Aside", Kind: kindAgent},
		},
		Objects: []snapshotObject{{ID: 12, Name: "A"}, {ID: 13, Name: "B"}, {ID: 14, Name: "C"}, {ID: 15, Name: "D"}, {ID: 16, Name: "E"}, {ID: 17, Name: "Brief"}},
		States:  []objectState{{ID: 20, Object: 17, Name: "ready"}},
		Links: []snapshotLink{
			{ID: 101, Process: 1, Object: 12, Direction: directionOutput},
			{ID: 102, Process: 2, Object: 12, Direction: directionInput},
			{ID: 103, Process: 2, Object: 17, Direction: directionInput},
			{ID: 104, Process: 2, Object: 13, Direction: directionOutput},
			{ID: 105, Process: 3, Object: 14, Direction: directionOutput},
			{ID: 106, Process: 4, Object: 15, Direction: directionOutput},
			{ID: 107, Process: 5, Object: 16, Direction: directionOutput},
		},
		Connections: []connection{
			{ID: 201, Source: 2, Target: 3, Kind: connectionNext},
			{ID: 202, Source: 3, Target: 4, Kind: connectionFailure},
		},
	}
}

// What a re-run generates anew: what the process generates and what the
// processes following it do, by object, connection or failure; not what
// processes on branches of their own do.
func TestRerunAfter(t *testing.T) {
	w := walkOn(rerunBoard(), nil, nil)
	for p, want := range map[int64][]int64{
		1: {12, 13, 14, 15},
		2: {13, 14, 15},
		3: {14, 15},
		4: {15},
		5: {16},
	} {
		if got := slices.Sorted(maps.Keys(w.after(p))); !slices.Equal(got, want) {
			t.Errorf("after(%s) = %v, want %v", w.names[p], got, want)
		}
	}
}

func TestRerunPlanTakesAndCopies(t *testing.T) {
	w := walkOn(rerunBoard(), nil, nil)
	p := rerunPlan{
		process: 2,
		w:       w,
		inputs:  []int64{17, 12},
		values:  map[int64]value{17: {content: "brief", state: 20}, 16: {content: "e"}},
		after:   w.after(2),
	}
	want := []rerunInput{
		{Object: 17, Name: "Brief", State: "ready", Content: "brief", Present: true},
		{Object: 12, Name: "A"},
	}
	if got := p.takes(); !slices.Equal(got, want) {
		t.Errorf("takes = %+v, want %+v", got, want)
	}
	copied, anew := p.copied()
	if !slices.Equal(copied, []string{"E"}) || !slices.Equal(anew, []string{"B", "C", "D"}) {
		t.Errorf("copied = %v, anew = %v", copied, anew)
	}
	none := rerunPlan{w: w}
	if got := none.takes(); got == nil || len(got) != 0 {
		t.Errorf("takes of nothing = %#v, want empty", got)
	}
	if c, a := none.copied(); c == nil || a == nil {
		t.Error("copied of nothing should be empty, not nil")
	}
}

func TestSortedKeys(t *testing.T) {
	if got := sortedKeys(map[int64]bool{3: true, 1: false, 2: true}); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Errorf("sortedKeys = %v", got)
	}
	if got := sortedKeys(map[int64]value{}); got == nil || len(got) != 0 {
		t.Errorf("sortedKeys of none = %#v", got)
	}
}

// An object is found by name on the board asked for first, among those not
// making up another; else anywhere on the flow.
func TestObjectNamed(t *testing.T) {
	b := snapshot{Objects: []snapshotObject{
		{ID: 1, Name: "Doc", Owner: ptr(2)},
		{ID: 2, Name: "Doc"},
		{ID: 3, Name: "Doc", Parent: ptr(30)},
		{ID: 4, Name: "Inner", Parent: ptr(30)},
		{ID: 5, Name: "Wheel", Owner: ptr(2)},
	}}
	for _, c := range []struct {
		name string
		on   *int64
		want int64
	}{
		{"Doc", nil, 2},
		{"Doc", ptr(30), 3},
		{"Doc", ptr(31), 1},
		{"Inner", nil, 4},
		{"Wheel", nil, 5},
		{"None", nil, 0},
	} {
		if got := objectNamed(b, c.name, c.on); got != c.want {
			t.Errorf("objectNamed(%q, %v) = %d, want %d", c.name, c.on, got, c.want)
		}
	}
}
