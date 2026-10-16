package main

import (
	"maps"
	"slices"
	"testing"
)

func TestSameNames(t *testing.T) {
	rows := func(names ...string) []sourcedRow {
		var rs []sourcedRow
		for i, n := range names {
			rs = append(rs, sourcedRow{id: int64(i + 1), name: n})
		}
		return rs
	}
	for _, c := range []struct {
		a, b []sourcedRow
		want bool
	}{
		{nil, nil, true},
		{rows("open", "closed"), rows("closed", "open"), true},
		{rows("open"), rows("open", "closed"), false},
		{rows("open", "open"), rows("open"), false},
		{rows("open"), rows("Open"), false},
	} {
		if got := sameNames(c.a, c.b); got != c.want {
			t.Errorf("sameNames(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// A copy's rows pair with the module's rows they were copied from first,
// then by name among those copied from none of them; the rest are left over.
func TestMatchRows(t *testing.T) {
	want := []sourcedRow{{id: 1, name: "open"}, {id: 2, name: "closed"}, {id: 3, name: "draft"}}
	have := []sourcedRow{
		{id: 10, name: "renamed", source: ptr(1)},
		{id: 11, name: "open"},
		{id: 12, name: "closed", source: ptr(99)},
		{id: 13, name: "draft", source: ptr(2)},
		{id: 14, name: "closed", source: ptr(2)},
		{id: 15, name: "stale"},
	}
	pairs, left := matchRows(want, have)
	if !maps.Equal(pairs, map[int64]int64{1: 10, 2: 13}) {
		t.Errorf("pairs = %v, want draft unpaired", pairs)
	}
	if !slices.Equal(left, []int64{11, 12, 14, 15}) {
		t.Errorf("left = %v", left)
	}
	pairs, left = matchRows(want[:1], []sourcedRow{{id: 20, name: "open", source: ptr(7)}, {id: 21, name: "open"}})
	if !maps.Equal(pairs, map[int64]int64{1: 20}) || !slices.Equal(left, []int64{21}) {
		t.Errorf("by name: pairs = %v, left = %v", pairs, left)
	}
	if pairs, left := matchRows(nil, nil); len(pairs) != 0 || left != nil {
		t.Errorf("nothing: %v, %v", pairs, left)
	}
}

// A copy has no general, so it is made up of what its generals own, the
// most general's first, and then of what it owns.
func TestPartsOf(t *testing.T) {
	s := structure{
		1: {id: 1, name: "Vehicle"},
		2: {id: 2, name: "Car", general: ptr(1)},
		3: {id: 3, name: "Racing car", general: ptr(2)},
		4: {id: 4, name: "Wheel", owner: ptr(1)},
		5: {id: 5, name: "Engine", owner: ptr(2)},
		6: {id: 6, name: "Spoiler", owner: ptr(3)},
		7: {id: 7, name: "Loop", general: ptr(8)},
		8: {id: 8, name: "Back", general: ptr(7)},
		9: {id: 9, name: "Seat", owner: ptr(8)},
	}
	ids := func(ps []*structObject) []int64 {
		var out []int64
		for _, p := range ps {
			out = append(out, p.id)
		}
		return out
	}
	for object, want := range map[int64][]int64{1: {4}, 2: {4, 5}, 3: {4, 5, 6}, 7: {9}, 4: nil, 42: nil} {
		if got := ids(partsOf(s, object)); !slices.Equal(got, want) {
			t.Errorf("partsOf(%d) = %v, want %v", object, got, want)
		}
	}
}
