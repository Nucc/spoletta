package main

import (
	"slices"
	"testing"
)

// Build is a selected flow process, with Step on its board and Sub on
// Step's; Other isn't selected. Car is selected, made up of a Wheel, made up
// of a Bolt in turn; Note is on Step's board. Of Bike, only its Bell is
// selected. Orphan's owner isn't on the board copied from.
func pasteBoard() *paster {
	p := &paster{
		procs:    map[int64]process{},
		objs:     map[int64]snapshotObject{},
		selProcs: map[int64]bool{30: true},
		selObjs:  map[int64]bool{1: true, 6: true},
	}
	for _, pr := range []process{
		{ID: 30, Name: "Build", Kind: kindFlow},
		{ID: 31, Name: "Step", Kind: kindFlow, Parent: ptr(30)},
		{ID: 32, Name: "Sub", Kind: kindAgent, Parent: ptr(31)},
		{ID: 33, Name: "Other", Kind: kindFlow},
		{ID: 34, Name: "Inside other", Kind: kindAgent, Parent: ptr(33)},
	} {
		p.procs[pr.ID] = pr
	}
	for _, o := range []snapshotObject{
		{ID: 1, Name: "Car"},
		{ID: 2, Name: "Wheel", Owner: ptr(1)},
		{ID: 3, Name: "Bolt", Owner: ptr(2)},
		{ID: 4, Name: "Note", Parent: ptr(31)},
		{ID: 5, Name: "Bike"},
		{ID: 6, Name: "Bell", Owner: ptr(5)},
		{ID: 7, Name: "Orphan", Owner: ptr(99)},
		{ID: 10, Name: "Racing car", General: ptr(11)},
		{ID: 11, Name: "Car kind", General: ptr(12)},
		{ID: 12, Name: "Vehicle"},
		{ID: 13, Name: "Ping", General: ptr(14)},
		{ID: 14, Name: "Pong", General: ptr(13)},
		{ID: 15, Name: "Lost", General: ptr(999)},
	} {
		p.objs[o.ID] = o
	}
	return p
}

func TestPasteProcessCopied(t *testing.T) {
	p := pasteBoard()
	for id, want := range map[int64]bool{30: true, 31: true, 32: true, 33: false, 34: false, 999: false} {
		if got := p.processCopied(id); got != want {
			t.Errorf("processCopied(%d) = %v, want %v", id, got, want)
		}
	}
	if p.takenAlong(nil) || !p.takenAlong(ptr(31)) || p.takenAlong(ptr(33)) {
		t.Error("takenAlong")
	}
}

// An object is copied when selected, making up one that is, or on the board
// of a flow process that is; it is copied for itself only when not along
// with what it makes up or the board it is on.
func TestPasteObjectCopied(t *testing.T) {
	p := pasteBoard()
	for _, c := range []struct {
		id           int64
		copied, root bool
	}{
		{1, true, true},
		{2, true, false},
		{3, true, false},
		{4, true, false},
		{5, false, true},
		{6, true, true},
		{7, false, true},
	} {
		o := p.objs[c.id]
		if got := p.objectCopied(o); got != c.copied {
			t.Errorf("objectCopied(%s) = %v, want %v", o.Name, got, c.copied)
		}
		if got := p.objectRoot(o); got != c.root {
			t.Errorf("objectRoot(%s) = %v, want %v", o.Name, got, c.root)
		}
	}
}

func TestPasteGeneralChain(t *testing.T) {
	p := pasteBoard()
	for id, want := range map[int64][]int64{
		10:  {12, 11, 10},
		12:  {12},
		13:  {14, 13},
		15:  {15},
		999: nil,
	} {
		if got := p.generalChain(id); !slices.Equal(got, want) {
			t.Errorf("generalChain(%d) = %v, want %v", id, got, want)
		}
	}
}
