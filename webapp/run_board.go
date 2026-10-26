package main

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A run runs on its version: the board as it was saved when the run started,
// or the version the flow published (see publish.go), while the board as it
// is may be a draft someone is halfway through. Its walk follows the
// version's links (engine.go), and its steps take their processes, the
// objects they generate, and what those are made of and stored as, from the
// version too, never from the board as it is, so editing the board, or
// publishing another version, doesn't reach a run going.
//
// What a run records refers to the board's rows by id, which versions keep
// (see versions.go). A row the board no longer has, such as a process deleted
// from the draft but still in the version published, is referred to by its
// ref column alone, as one deleted after the run is.

// A version's board, looked up by id.
type runBoard struct {
	// The flow it is a board of.
	flow int64
	snapshot
	processes map[int64]process
	objects   map[int64]snapshotObject
	states    map[int64]objectState
	// The links of each process, in id order, and the states each link lists.
	links      map[int64][]snapshotLink
	linkStates map[int64][]int64
	structure  structure
}

func newRunBoard(flow int64, s snapshot) *runBoard {
	b := &runBoard{flow: flow, snapshot: s, processes: map[int64]process{}, objects: map[int64]snapshotObject{},
		states: map[int64]objectState{}, links: map[int64][]snapshotLink{}, linkStates: map[int64][]int64{}}
	for i, p := range s.Processes {
		// Sentry processes, which versions from before they were retired
		// hold, run as agent processes, as restoring them makes them.
		if p.Kind == "sentry" {
			p.Kind = kindAgent
			s.Processes[i] = p
		}
		b.processes[p.ID] = p
	}
	for _, o := range s.Objects {
		b.objects[o.ID] = o
	}
	for _, st := range s.States {
		b.states[st.ID] = st
	}
	for _, l := range s.Links {
		b.links[l.Process] = append(b.links[l.Process], l)
	}
	for _, ls := range s.LinkStates {
		b.linkStates[ls.Link] = append(b.linkStates[ls.Link], ls.State)
	}
	b.structure = boardStructure(s)
	return b
}

// The board a run ran on: its version's, or for a run from before versions
// were recorded, the board as it is.
func runBoardOf(ctx context.Context, db *sql.DB, run int64) (*runBoard, error) {
	var flow int64
	var body []byte
	err := db.QueryRowContext(ctx, `SELECT r.flow_id, v.snapshot FROM runs r
		LEFT JOIN flow_versions v ON v.id = r.version_id WHERE r.id = ?`, run).Scan(&flow, &body)
	if err != nil {
		return nil, err
	}
	var s snapshot
	if body != nil {
		err = json.Unmarshal(body, &s)
	} else {
		err = inTx(ctx, db, func(tx *sql.Tx) error {
			var err error
			s, err = takeSnapshot(ctx, tx, flow)
			return err
		})
	}
	if err != nil {
		return nil, err
	}
	return newRunBoard(flow, s), nil
}

// The board's process, as startStepAs records a step of it; errNotFound if
// the board has none of that id.
func (b *runBoard) process(id int64) (process, error) {
	p, ok := b.processes[id]
	if !ok {
		return p, errNotFound
	}
	return p, nil
}

// Whether the process takes the object, by any input link.
func (b *runBoard) takes(process, object int64) bool {
	return slices.ContainsFunc(b.links[process], func(l snapshotLink) bool {
		return l.Object == object && l.Direction == directionInput
	})
}

// Whether a human process's output link is to an object it fills in: one
// listing no states to decide between, of an object it doesn't take; see
// entries.go.
func (b *runBoard) fillsIn(l snapshotLink) bool {
	return len(b.linkStates[l.ID]) == 0 && !b.takes(l.Process, l.Object)
}

// The process's output links, in object order, and in link order for one
// object; for a step of a human process, only those to what it fills in.
func (b *runBoard) outputLinks(process int64, kind string) []snapshotLink {
	var out []snapshotLink
	for _, l := range b.links[process] {
		if l.Direction == directionOutput && (kind != kindHuman || b.fillsIn(l)) {
			out = append(out, l)
		}
	}
	slices.SortStableFunc(out, func(x, y snapshotLink) int { return cmp.Compare(x.Object, y.Object) })
	return out
}

// The name of the state, empty for none or one the board doesn't have.
func (b *runBoard) stateName(id int64) string {
	return b.states[id].Name
}

// The name of a state a human step of the process can decide on: one its
// output links list. Empty if the state isn't one of them.
func (b *runBoard) decisionState(process, state int64) string {
	if b.processes[process].Kind != kindHuman {
		return ""
	}
	for _, l := range b.links[process] {
		if l.Direction == directionOutput && slices.Contains(b.linkStates[l.ID], state) {
			return b.stateName(state)
		}
	}
	return ""
}

// The names of the human objects handling a process: the roles that may
// decide its reviews, in order, none when anyone may; see user_roles.go.
func (b *runBoard) handlers(process int64) []string {
	names := []string{}
	for _, l := range b.links[process] {
		if o := b.objects[l.Object]; l.Direction == directionInput && o.Human {
			if name := strings.TrimSpace(o.Name); !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return names
}

// The store a shared object reads and writes, 0 if it isn't shared.
func (b *runBoard) store(object int64) int64 {
	if s := b.objects[object].Store; s != nil {
		return *s
	}
	return 0
}

// The object's unique attributes, its generals' first, since it has those
// too; as uniqueAttributes, on the board.
func (b *runBoard) uniqueAttributes(object int64) []attribute {
	var attrs []attribute
	for seen := map[int64]bool{}; !seen[object]; {
		seen[object] = true
		var own []attribute
		if o := b.structure[object]; o != nil {
			for _, a := range o.attrs {
				if a.Unique {
					own = append(own, a)
				}
			}
		}
		attrs = append(own, attrs...)
		general := b.objects[object].General
		if general == nil {
			break
		}
		object = *general
	}
	return attrs
}

// The contract for what a process generates, or nil if it generates
// nothing. With all false, only objects with fields are in it, and nil means
// none has any. Says what is wrong if the objects can't be told apart.
func (b *runBoard) contractFor(process int64, all bool) (*outputContract, string) {
	decisions := b.decidedObjects(process)
	reviewed := map[int64]bool{}
	for _, d := range decisions {
		reviewed[d.id] = d.review
	}
	var outputs []int64
	for _, l := range b.links[process] {
		if l.Direction == directionOutput && !slices.Contains(outputs, l.Object) {
			outputs = append(outputs, l.Object)
		}
	}
	objects, msg := b.shapedObjects(process, outputs)
	if msg != "" {
		return nil, msg
	}

	c := &outputContract{decisions: decisions}
	names := map[string]bool{}
	for _, o := range objects {
		if reviewed[o.id] {
			continue
		}
		if o.data {
			if !all {
				continue
			}
			o.fields = []field{{name: dataAttribute, definition: fmt.Sprintf("What %s holds, as any JSON value", quote(o.name))}}
		}
		if names[o.name] {
			return nil, fmt.Sprintf("the process generates two objects named %q; rename one so its output can tell them apart", o.name)
		}
		names[o.name] = true
		c.shaped = append(c.shaped, o)
	}
	for _, d := range decisions {
		if d.review && names[d.name] {
			return nil, fmt.Sprintf("the process generates and decides on two objects named %q; rename one so its output can tell them apart", d.name)
		}
	}
	if len(c.shaped) == 0 && len(c.decisions) == 0 {
		return nil, ""
	}
	return c, ""
}

// The objects a process decides the state of, in id order: those its output
// links list states for, each with the states in id order.
func (b *runBoard) decidedObjects(process int64) []decidedObject {
	var ds []decidedObject
	for _, l := range b.links[process] {
		states := b.linkStates[l.ID]
		if l.Direction != directionOutput || len(states) == 0 {
			continue
		}
		i := slices.IndexFunc(ds, func(d decidedObject) bool { return d.id == l.Object })
		if i < 0 {
			ds = append(ds, decidedObject{id: l.Object, name: b.objects[l.Object].Name, review: b.takes(process, l.Object)})
			i = len(ds) - 1
		}
		for _, st := range states {
			if s, ok := b.states[st]; ok && !slices.ContainsFunc(ds[i].states, func(x objectState) bool { return x.ID == st }) {
				ds[i].states = append(ds[i].states, s)
			}
		}
	}
	for i := range ds {
		slices.SortFunc(ds[i].states, func(x, y objectState) int { return cmp.Compare(x.ID, y.ID) })
	}
	slices.SortFunc(ds, func(x, y decidedObject) int { return cmp.Compare(x.id, y.id) })
	return ds
}

// The objects of the board, in id order, with the fields of what each holds,
// for what process generates or fills in. Inside a flow process a list is
// one item of it, unless the list is on the process's own board. Says what
// is wrong if their fields never end.
func (b *runBoard) shapedObjects(process int64, ids []int64) ([]shapedObject, string) {
	parent := b.processes[process].Parent
	ids = slices.Clone(ids)
	slices.Sort(ids)
	var objects []shapedObject
	for _, id := range slices.Compact(ids) {
		o, ok := b.objects[id]
		if !ok {
			continue
		}
		so := shapedObject{id: id, name: o.Name, many: o.Many && samePlace(o.Parent, parent)}
		fs, err := b.structure.fields(id)
		if err != nil {
			return nil, err.Error()
		}
		so.fields = fs
		so.data = len(fs) == 0
		if so.many {
			so.quantity = b.structure[id].quantity
		}
		objects = append(objects, so)
	}
	return objects, ""
}

// The objects a human process fills in, in id order. Says what is wrong if
// what they hold never ends.
func (b *runBoard) entryObjects(process int64) ([]shapedObject, string) {
	if b.processes[process].Kind != kindHuman {
		return nil, ""
	}
	var ids []int64
	for _, l := range b.outputLinks(process, kindHuman) {
		ids = append(ids, l.Object)
	}
	return b.shapedObjects(process, ids)
}

// Whether two boards are the same one: both the top board, or the same flow
// process's.
func samePlace(a, b *int64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}
