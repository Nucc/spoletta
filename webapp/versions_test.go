package main

import (
	"reflect"
	"testing"
)

func versionID(id int64) *int64 { return &id }

// Processes are saved after the flow process whose board they are on, and
// in their order otherwise; those whose flow process isn't among them go
// last, for the foreign key to turn down.
func TestParentsFirst(t *testing.T) {
	for _, c := range []struct {
		name string
		ps   []process
		want []int64
	}{
		{"none", nil, nil},
		{"top board", []process{{ID: 1}, {ID: 2}}, []int64{1, 2}},
		{"nested", []process{
			{ID: 3, Parent: versionID(2)},
			{ID: 2, Parent: versionID(1)},
			{ID: 4},
			{ID: 1},
		}, []int64{4, 1, 2, 3}},
		{"missing parent", []process{
			{ID: 5, Parent: versionID(99)},
			{ID: 1},
			{ID: 6, Parent: versionID(5)},
		}, []int64{1, 5, 6}},
	} {
		var got []int64
		for _, p := range parentsFirst(c.ps) {
			got = append(got, p.ID)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: parentsFirst = %v, want %v", c.name, got, c.want)
		}
	}
}

// Objects are saved after the objects that own them, the same way.
func TestOwnersFirst(t *testing.T) {
	for _, c := range []struct {
		name string
		os   []snapshotObject
		want []int64
	}{
		{"none", nil, nil},
		{"unowned", []snapshotObject{{ID: 2}, {ID: 1}}, []int64{2, 1}},
		{"parts", []snapshotObject{
			{ID: 3, Owner: versionID(2)},
			{ID: 2, Owner: versionID(1)},
			{ID: 1},
		}, []int64{1, 2, 3}},
		// The board an object is on doesn't matter, only its owner.
		{"parent ignored", []snapshotObject{{ID: 2, Parent: versionID(9)}, {ID: 1}}, []int64{2, 1}},
		{"missing owner", []snapshotObject{{ID: 5, Owner: versionID(99)}, {ID: 1}}, []int64{1, 5}},
	} {
		var got []int64
		for _, o := range ownersFirst(c.os) {
			got = append(got, o.ID)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ownersFirst = %v, want %v", c.name, got, c.want)
		}
	}
}

// The default route is stored as NULL.
func TestStoredRoute(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want any
	}{
		{"", nil},
		{"null", nil},
		{"[1,2]", []byte("[1,2]")},
	} {
		if got := storedRoute([]byte(c.raw)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("storedRoute(%q) = %#v, want %#v", c.raw, got, c.want)
		}
	}
	if got := storedRoute(nil); got != nil {
		t.Errorf("storedRoute(nil) = %#v, want nil", got)
	}
}

// Snapshots are the same whatever the database did to their spacing, key
// order and numbers.
func TestSameSnapshot(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
		ok   bool
	}{
		{`{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1.0 }`, true, true},
		{`{"a":1}`, `{"a":2}`, false, true},
		{`{"b":[1,2]}`, `{"b":[2,1]}`, false, true},
		{`{"a":null}`, `{}`, false, true},
		{`{"a":1}`, `{"a":`, false, false},
		{`nope`, `{}`, false, false},
	} {
		same, err := sameSnapshot([]byte(c.a), []byte(c.b))
		if (err == nil) != c.ok || same != c.same {
			t.Errorf("sameSnapshot(%s, %s) = %v, %v, want %v, ok %v", c.a, c.b, same, err, c.same, c.ok)
		}
	}
}
