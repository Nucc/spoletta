package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// What changed between two boards of a flow: two of its versions, or a
// version and the board as it is. The Versions panel lists it, the board
// highlights what it names (Board.tsx), and after the assistant changed the
// flow the board shows what it changed against the version saved before;
// whether a flow's draft differs from what it published is whether this
// finds anything (publish.go). It is worked out here alone, from the two
// snapshots.
//
// A board keeps its rows' ids from version to version (versions.go), so
// processes, objects, states and attributes are matched by id first. A
// setup file, and so the assistant, makes a flow over by name: what it
// renamed, or made anew, has a new id. So what is left unmatched is
// matched by name in the same place: a process by its name on the board
// matched to its own, an object by its name there and the object it is
// part of, a state or attribute by its name on the object matched to its
// own; the first by id wins. Links and connections have no lasting id of
// their own, so a link is matched by its process, object, direction and
// state, as matched, and a connection by its ends and kind. A start is a
// connection from the board's flow process, or none for the top board.
//
// Where things sit, their widths and how links are routed are layout: the
// diff only says whether any changed, and never lists them.
//
// The samples processes are tried on (trials.go) are listed too, matched by
// id, then by name on the process matched, each change saying what it
// gives each object differently. Like layout, they don't make a draft
// differ from what it published: a trial tries the board as it is, and no
// run of a version reads them.

// What changed, by kind of thing, and of the flow's settings.
type boardDiff struct {
	Processes   []diffItem `json:"processes"`
	Objects     []diffItem `json:"objects"`
	States      []diffItem `json:"states"`
	Attributes  []diffItem `json:"attributes"`
	Links       []diffItem `json:"links"`
	Connections []diffItem `json:"connections"`
	// For a sample: its process, as Process, and what it gives each object.
	Samples  []diffItem  `json:"samples"`
	Settings []diffField `json:"settings"`
	// Whether anything moved, was resized or rerouted.
	Layout bool `json:"layout"`
}

const (
	diffAdded   = "added"
	diffRemoved = "removed"
	diffChanged = "changed"
)

// A thing added, removed or changed.
type diffItem struct {
	Change string `json:"change"`
	// Its id on the newer board, or on the older for one removed.
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// The flow process whose board it is on, null for the top board's.
	Board *int64 `json:"board"`
	// For a state or attribute, the object it is of; for an object, the one
	// it is part of, if any; for a link, its object. On the same board as
	// the id.
	Object *int64 `json:"object,omitempty"`
	// For a link, its process; for a connection, its ends.
	Process *int64 `json:"process,omitempty"`
	Target  *int64 `json:"target,omitempty"`
	// What changed of one changed.
	Fields []diffField `json:"fields,omitempty"`
}

// One thing changed of something, as it was and as it is.
type diffField struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type diffFields []diffField

// Whether the boards differ in more than their layout.
func (d boardDiff) changed() bool {
	return len(d.Processes)+len(d.Objects)+len(d.States)+len(d.Attributes)+len(d.Links)+len(d.Connections)+len(d.Settings) > 0
}

// The older board's ids matched to the newer's, by kind.
type diffMatch struct {
	procs, objs, states, attrs map[int64]int64
}

// What changed from board a to board b.
func diffSnapshots(a, b snapshot) boardDiff {
	d := boardDiff{Processes: []diffItem{}, Objects: []diffItem{}, States: []diffItem{}, Attributes: []diffItem{},
		Links: []diffItem{}, Connections: []diffItem{}, Samples: []diffItem{}, Settings: []diffField{}}
	m := matchSnapshots(a, b)
	d.diffProcesses(a, b, m)
	d.diffObjects(a, b, m)
	d.diffStates(a, b, m)
	d.diffAttributes(a, b, m)
	d.diffLinks(a, b, m)
	d.diffConnections(a, b, m)
	d.diffSamples(a, b, m)
	d.Settings = diffSettings(a.Settings, b.Settings)
	if a.Start.X != b.Start.X || a.Start.Y != b.Start.Y || !sameJSON(a.Start.Route, b.Start.Route) || !sameEnd(a.End, b.End) {
		d.Layout = true
	}
	inner := map[int64]innerBoard{}
	for _, ib := range a.Boards {
		inner[ib.Process] = ib
	}
	for _, ib := range b.Boards {
		for old, now := range m.procs {
			if now == ib.Process {
				if was, ok := inner[old]; ok && (was.Start.X != ib.Start.X || was.Start.Y != ib.Start.Y || was.End != ib.End || !sameJSON(was.Start.Route, ib.Start.Route)) {
					d.Layout = true
				}
			}
		}
	}
	return d
}

func sameEnd(a, b *boardEnd) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Matches a's processes, objects, states and attributes to b's: by id,
// then by name in the same place.
func matchSnapshots(a, b snapshot) diffMatch {
	m := diffMatch{procs: map[int64]int64{}, objs: map[int64]int64{}, states: map[int64]int64{}, attrs: map[int64]int64{}}
	same := func(old *int64, now *int64, by map[int64]int64) bool {
		if old == nil || now == nil {
			return old == nil && now == nil
		}
		n, ok := by[*old]
		return ok && n == *now
	}

	taken := map[int64]bool{}
	ids := map[int64]bool{}
	for _, p := range b.Processes {
		ids[p.ID] = true
	}
	for _, p := range a.Processes {
		if ids[p.ID] {
			m.procs[p.ID], taken[p.ID] = p.ID, true
		}
	}
	for _, p := range parentsFirst(a.Processes) {
		if _, ok := m.procs[p.ID]; ok {
			continue
		}
		for _, q := range b.Processes {
			if !taken[q.ID] && q.Name == p.Name && same(p.Parent, q.Parent, m.procs) {
				m.procs[p.ID], taken[q.ID] = q.ID, true
				break
			}
		}
	}

	taken, ids = map[int64]bool{}, map[int64]bool{}
	for _, o := range b.Objects {
		ids[o.ID] = true
	}
	for _, o := range a.Objects {
		if ids[o.ID] {
			m.objs[o.ID], taken[o.ID] = o.ID, true
		}
	}
	for _, o := range ownersFirst(a.Objects) {
		if _, ok := m.objs[o.ID]; ok {
			continue
		}
		for _, q := range b.Objects {
			if !taken[q.ID] && q.Name == o.Name && same(o.Parent, q.Parent, m.procs) && same(o.Owner, q.Owner, m.objs) {
				m.objs[o.ID], taken[q.ID] = q.ID, true
				break
			}
		}
	}

	matchNamed := func(old, now []namedOf, into map[int64]int64) {
		taken, ids := map[int64]bool{}, map[int64]bool{}
		for _, x := range now {
			ids[x.id] = true
		}
		for _, x := range old {
			if ids[x.id] {
				into[x.id], taken[x.id] = x.id, true
			}
		}
		for _, x := range old {
			if _, ok := into[x.id]; ok {
				continue
			}
			for _, y := range now {
				if !taken[y.id] && y.name == x.name && m.objs[x.object] == y.object {
					into[x.id], taken[y.id] = y.id, true
					break
				}
			}
		}
	}
	matchNamed(statesNamed(a.States), statesNamed(b.States), m.states)
	matchNamed(attributesNamed(a.Attributes), attributesNamed(b.Attributes), m.attrs)
	return m
}

// A state or attribute as matching by name needs it.
type namedOf struct {
	id, object int64
	name       string
}

func statesNamed(sts []objectState) []namedOf {
	out := make([]namedOf, len(sts))
	for i, st := range sts {
		out[i] = namedOf{st.ID, st.Object, st.Name}
	}
	return out
}

func attributesNamed(as []attribute) []namedOf {
	out := make([]namedOf, len(as))
	for i, a := range as {
		out[i] = namedOf{a.ID, a.Object, a.Name}
	}
	return out
}

// The old ids matched to each new one, the other way round.
func reverse(by map[int64]int64) map[int64]int64 {
	out := make(map[int64]int64, len(by))
	for old, now := range by {
		out[now] = old
	}
	return out
}

// Adds a field that changed, if it did.
func (fs *diffFields) add(field, before, after string) {
	if before != after {
		*fs = append(*fs, diffField{Field: field, Before: before, After: after})
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// The name of the board a flow process's id is, on s; "the top board" for none.
func boardName(s snapshot, board *int64) string {
	if board == nil {
		return "the top board"
	}
	for _, p := range s.Processes {
		if p.ID == *board {
			return p.Name
		}
	}
	return "#" + strconv.FormatInt(*board, 10)
}

func (d *boardDiff) diffProcesses(a, b snapshot, m diffMatch) {
	was := map[int64]process{}
	for _, p := range a.Processes {
		was[p.ID] = p
	}
	back := reverse(m.procs)
	for _, p := range b.Processes {
		old, ok := back[p.ID]
		if !ok {
			d.Processes = append(d.Processes, diffItem{Change: diffAdded, ID: p.ID, Name: p.Name, Board: p.Parent})
			continue
		}
		o := was[old]
		var fs diffFields
		fs.add("name", o.Name, p.Name)
		fs.add("kind", o.Kind, p.Kind)
		fs.add("board", boardName(a, o.Parent), boardName(b, p.Parent))
		fs.add("prompt", o.Prompt, p.Prompt)
		fs.add("script", o.Script, p.Script)
		fs.add("model", o.Model, p.Model)
		fs.add("thinking", o.Thinking, p.Thinking)
		fs.add("runs of its own", yesNo(o.Recording), yesNo(p.Recording))
		fs = append(fs, diffJSONFields(o.Settings, p.Settings, processSettingNames)...)
		if o.X != p.X || o.Y != p.Y || !sameFloat(o.Width, p.Width) {
			d.Layout = true
		}
		if len(fs) > 0 {
			d.Processes = append(d.Processes, diffItem{Change: diffChanged, ID: p.ID, Name: p.Name, Board: p.Parent, Fields: fs})
		}
	}
	for _, p := range a.Processes {
		if _, ok := m.procs[p.ID]; !ok {
			d.Processes = append(d.Processes, diffItem{Change: diffRemoved, ID: p.ID, Name: p.Name, Board: p.Parent})
		}
	}
}

func sameFloat(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// What processes' settings are called where the diff names them.
var processSettingNames = map[string]string{
	"mcpServers": "MCP servers", "mcpTools": "MCP tools", "skills": "skills", "connector": "connector",
	"repositories": "repositories", "flow": "flow it runs", "rounds": "rounds", "loops": "loops",
	"timeout": "timeout", "retries": "retries", "quorum": "quorum", "describeFlow": "describes the flow",
}

// The fields of two values marshalled as JSON objects that differ, each
// as JSON, by the name names gives its key, or the key itself.
func diffJSONFields(a, b any, names map[string]string) []diffField {
	fields := func(v any) map[string]json.RawMessage {
		out := map[string]json.RawMessage{}
		if raw, err := json.Marshal(v); err == nil {
			json.Unmarshal(raw, &out)
		}
		return out
	}
	x, y := fields(a), fields(b)
	var keys []string
	for k := range x {
		keys = append(keys, k)
	}
	for k := range y {
		if _, ok := x[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var fs []diffField
	for _, k := range keys {
		if sameJSON(x[k], y[k]) {
			continue
		}
		name := names[k]
		if name == "" {
			name = k
		}
		fs = append(fs, diffField{Field: name, Before: jsonText(x[k]), After: jsonText(y[k])})
	}
	return fs
}

// A JSON value as the diff shows it: a string as itself, anything else as
// compact JSON, nothing for none.
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return compact(raw)
}

// Whether two JSON values are the same, whatever their spacing and key
// order; none is the same as null.
func sameJSON(a, b json.RawMessage) bool {
	norm := func(r json.RawMessage) any {
		var v any
		if len(r) == 0 || json.Unmarshal(r, &v) != nil {
			return nil
		}
		return v
	}
	x, y := norm(a), norm(b)
	xs, _ := json.Marshal(x)
	ys, _ := json.Marshal(y)
	return string(xs) == string(ys)
}

func quantityText(o snapshotObject) string {
	if !o.Many {
		return ""
	}
	return quantity{min: o.Min, max: o.Max}.text(true)
}

func (d *boardDiff) diffObjects(a, b snapshot, m diffMatch) {
	was := map[int64]snapshotObject{}
	for _, o := range a.Objects {
		was[o.ID] = o
	}
	named := func(s snapshot, id *int64) string {
		if id == nil {
			return ""
		}
		for _, o := range s.Objects {
			if o.ID == *id {
				return o.Name
			}
		}
		return "#" + strconv.FormatInt(*id, 10)
	}
	back := reverse(m.objs)
	for _, o := range b.Objects {
		old, ok := back[o.ID]
		if !ok {
			d.Objects = append(d.Objects, diffItem{Change: diffAdded, ID: o.ID, Name: o.Name, Board: o.Parent, Object: o.Owner})
			continue
		}
		w := was[old]
		var fs diffFields
		fs.add("name", w.Name, o.Name)
		fs.add("board", boardName(a, w.Parent), boardName(b, o.Parent))
		fs.add("part of", named(a, w.Owner), named(b, o.Owner))
		fs.add("specializes", named(a, w.General), named(b, o.General))
		fs.add("stored", yesNo(w.Persistent), yesNo(o.Persistent))
		fs.add("shared", yesNo(w.Store != nil), yesNo(o.Store != nil))
		fs.add("list", yesNo(w.Many), yesNo(o.Many))
		fs.add("items", quantityText(w), quantityText(o))
		fs.add("person", yesNo(w.Human), yesNo(o.Human))
		fs.add("outcome", yesNo(w.End), yesNo(o.End))
		if w.X != o.X || w.Y != o.Y || !sameFloat(w.Width, o.Width) {
			d.Layout = true
		}
		if len(fs) > 0 {
			d.Objects = append(d.Objects, diffItem{Change: diffChanged, ID: o.ID, Name: o.Name, Board: o.Parent, Object: o.Owner, Fields: fs})
		}
	}
	for _, o := range a.Objects {
		if _, ok := m.objs[o.ID]; !ok {
			d.Objects = append(d.Objects, diffItem{Change: diffRemoved, ID: o.ID, Name: o.Name, Board: o.Parent, Object: o.Owner})
		}
	}
}

// The name an object's id has on s, and the board it is on.
func objectOn(s snapshot, id int64) (string, *int64) {
	for _, o := range s.Objects {
		if o.ID == id {
			return o.Name, o.Parent
		}
	}
	return "#" + strconv.FormatInt(id, 10), nil
}

func (d *boardDiff) diffStates(a, b snapshot, m diffMatch) {
	was := map[int64]objectState{}
	for _, st := range a.States {
		was[st.ID] = st
	}
	item := func(s snapshot, change string, st objectState) diffItem {
		name, board := objectOn(s, st.Object)
		return diffItem{Change: change, ID: st.ID, Name: name + ": " + st.Name, Board: board, Object: &st.Object}
	}
	back := reverse(m.states)
	for _, st := range b.States {
		old, ok := back[st.ID]
		if !ok {
			d.States = append(d.States, item(b, diffAdded, st))
			continue
		}
		w := was[old]
		var fs diffFields
		fs.add("name", w.Name, st.Name)
		fs.add("rule", w.Rule, st.Rule)
		if len(fs) > 0 {
			it := item(b, diffChanged, st)
			it.Fields = fs
			d.States = append(d.States, it)
		}
	}
	for _, st := range a.States {
		if _, ok := m.states[st.ID]; !ok {
			d.States = append(d.States, item(a, diffRemoved, st))
		}
	}
}

func (d *boardDiff) diffSamples(a, b snapshot, m diffMatch) {
	procs := func(s snapshot) map[int64]process {
		out := map[int64]process{}
		for _, p := range s.Processes {
			out[p.ID] = p
		}
		return out
	}
	pa, pb := procs(a), procs(b)
	item := func(on map[int64]process, change string, sm processSample) diffItem {
		p := on[sm.Process]
		process := sm.Process
		return diffItem{Change: change, ID: sm.ID, Name: p.Name + ": " + sm.Name, Board: p.Parent, Process: &process}
	}
	// What a sample gives each object, by the object's name on its board,
	// matched to the newer's: what it holds, and its state's name.
	given := func(s snapshot, sm processSample, objs map[int64]int64) map[int64][2]string {
		out := map[int64][2]string{}
		for _, in := range sm.Inputs {
			state := ""
			for _, st := range s.States {
				if st.ID == in.State {
					state = st.Name
				}
			}
			o := in.Object
			if objs != nil {
				if now, ok := objs[o]; ok {
					o = now
				} else {
					o = -o
				}
			}
			out[o] = [2]string{in.Content, state}
		}
		return out
	}
	objectName := func(o int64) string {
		if o < 0 {
			name, _ := objectOn(a, -o)
			return name
		}
		name, _ := objectOn(b, o)
		return name
	}

	matched := map[int64]bool{}
	ids := map[int64]processSample{}
	for _, sm := range a.Samples {
		ids[sm.ID] = sm
	}
	for _, sm := range b.Samples {
		old, ok := ids[sm.ID]
		if !ok || matched[old.ID] || m.procs[old.Process] != sm.Process {
			old, ok = processSample{}, false
			for _, w := range a.Samples {
				if !matched[w.ID] && w.Name == sm.Name && m.procs[w.Process] == sm.Process {
					old, ok = w, true
					break
				}
			}
		}
		if !ok {
			d.Samples = append(d.Samples, item(pb, diffAdded, sm))
			continue
		}
		matched[old.ID] = true
		var fs diffFields
		fs.add("name", old.Name, sm.Name)
		was, now := given(a, old, m.objs), given(b, sm, nil)
		var objects []int64
		for o := range was {
			objects = append(objects, o)
		}
		for o := range now {
			if _, ok := was[o]; !ok {
				objects = append(objects, o)
			}
		}
		slices.Sort(objects)
		for _, o := range objects {
			name := objectName(o)
			fs.add(name, was[o][0], now[o][0])
			fs.add(name+" state", was[o][1], now[o][1])
		}
		if len(fs) > 0 {
			it := item(pb, diffChanged, sm)
			it.Fields = fs
			d.Samples = append(d.Samples, it)
		}
	}
	for _, sm := range a.Samples {
		if !matched[sm.ID] {
			d.Samples = append(d.Samples, item(pa, diffRemoved, sm))
		}
	}
}

func (d *boardDiff) diffAttributes(a, b snapshot, m diffMatch) {
	was := map[int64]attribute{}
	for _, at := range a.Attributes {
		was[at.ID] = at
	}
	item := func(s snapshot, change string, at attribute) diffItem {
		name, board := objectOn(s, at.Object)
		return diffItem{Change: change, ID: at.ID, Name: name + ": " + at.Name, Board: board, Object: &at.Object}
	}
	back := reverse(m.attrs)
	for _, at := range b.Attributes {
		old, ok := back[at.ID]
		if !ok {
			d.Attributes = append(d.Attributes, item(b, diffAdded, at))
			continue
		}
		w := was[old]
		var fs diffFields
		fs.add("name", w.Name, at.Name)
		fs.add("definition", w.Definition, at.Definition)
		fs.add("unique", yesNo(w.Unique), yesNo(at.Unique))
		fs.add("file", yesNo(w.File), yesNo(at.File))
		if len(fs) > 0 {
			it := item(b, diffChanged, at)
			it.Fields = fs
			d.Attributes = append(d.Attributes, it)
		}
	}
	for _, at := range a.Attributes {
		if _, ok := m.attrs[at.ID]; !ok {
			d.Attributes = append(d.Attributes, item(a, diffRemoved, at))
		}
	}
}

// What tells a link apart from the others of a board: its process, object,
// direction and state, by the newer board's ids; the older's are matched
// first, and ones left unmatched never match.
type linkKey struct {
	process, object int64
	direction       string
	state           int64
}

// A board's links and what each is called.
type linkSide struct {
	s     snapshot
	names map[int64]string
	objs  map[int64]string
	sts   map[int64]string
	board map[int64]*int64
	ls    map[int64][]int64
}

func newLinkSide(s snapshot) linkSide {
	side := linkSide{s: s, names: map[int64]string{}, objs: map[int64]string{}, sts: map[int64]string{}, board: map[int64]*int64{}, ls: map[int64][]int64{}}
	for _, p := range s.Processes {
		side.names[p.ID], side.board[p.ID] = p.Name, p.Parent
	}
	for _, o := range s.Objects {
		side.objs[o.ID] = o.Name
	}
	for _, st := range s.States {
		side.sts[st.ID] = st.Name
	}
	for _, x := range s.LinkStates {
		side.ls[x.Link] = append(side.ls[x.Link], x.State)
	}
	return side
}

// A link as the diff names it: the object, in its state if it names one,
// and its process, in the direction the link runs.
func (side linkSide) name(l snapshotLink) string {
	object := side.objs[l.Object]
	if st := side.sts[l.State]; st != "" {
		object += " (" + st + ")"
	}
	if l.Direction == directionOutput {
		return side.names[l.Process] + " → " + object
	}
	return object + " → " + side.names[l.Process]
}

// The names of the states a link lists, in order.
func (side linkSide) listed(l snapshotLink) string {
	var names []string
	for _, st := range side.ls[l.ID] {
		names = append(names, side.sts[st])
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

func (d *boardDiff) diffLinks(a, b snapshot, m diffMatch) {
	sa, sb := newLinkSide(a), newLinkSide(b)
	mapped := func(by map[int64]int64, id int64) int64 {
		if id == 0 {
			return 0
		}
		if n, ok := by[id]; ok {
			return n
		}
		return -id
	}
	waiting := map[linkKey][]snapshotLink{}
	for _, l := range a.Links {
		k := linkKey{mapped(m.procs, l.Process), mapped(m.objs, l.Object), l.Direction, mapped(m.states, l.State)}
		waiting[k] = append(waiting[k], l)
	}
	for _, l := range b.Links {
		process, object := l.Process, l.Object
		k := linkKey{l.Process, l.Object, l.Direction, l.State}
		olds := waiting[k]
		if len(olds) == 0 {
			d.Links = append(d.Links, diffItem{Change: diffAdded, ID: l.ID, Name: sb.name(l), Board: sb.board[l.Process], Process: &process, Object: &object})
			continue
		}
		o := olds[0]
		waiting[k] = olds[1:]
		var fs diffFields
		fs.add("role", o.Role, l.Role)
		fs.add("condition", yesNo(o.Condition), yesNo(l.Condition))
		before, _ := json.Marshal(o.Query)
		after, _ := json.Marshal(l.Query)
		fs.add("query", jsonText(before), jsonText(after))
		fs.add("states", sa.listed(o), sb.listed(l))
		if !sameJSON(o.Route, l.Route) || !samePoint(o.At, l.At) {
			d.Layout = true
		}
		if len(fs) > 0 {
			d.Links = append(d.Links, diffItem{Change: diffChanged, ID: l.ID, Name: sb.name(l), Board: sb.board[l.Process], Process: &process, Object: &object, Fields: fs})
		}
	}
	for _, l := range a.Links {
		k := linkKey{mapped(m.procs, l.Process), mapped(m.objs, l.Object), l.Direction, mapped(m.states, l.State)}
		for _, o := range waiting[k] {
			if o.ID == l.ID {
				process, object := l.Process, l.Object
				d.Links = append(d.Links, diffItem{Change: diffRemoved, ID: l.ID, Name: sa.name(l), Board: sa.board[l.Process], Process: &process, Object: &object})
			}
		}
	}
}

func samePoint(a, b *point) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// A connection, or a board's start, as the diff matches it: its ends by the
// newer board's ids, and its kind; a start's source is its board's flow
// process, 0 for the top board.
type connKey struct {
	source, target int64
	kind           string
}

const connectionStart = "start"

// A board's connections and starts.
func connectionsOf(s snapshot) []connection {
	out := slices.Clone(s.Connections)
	if s.Start.Target != nil {
		out = append(out, connection{Target: *s.Start.Target, Kind: connectionStart})
	}
	for _, ib := range s.Boards {
		if ib.Start.Target != nil {
			out = append(out, connection{Source: ib.Process, Target: *ib.Start.Target, Kind: connectionStart})
		}
	}
	return out
}

func (d *boardDiff) diffConnections(a, b snapshot, m diffMatch) {
	sa, sb := newLinkSide(a), newLinkSide(b)
	mapped := func(id int64) int64 {
		if id == 0 {
			return 0
		}
		if n, ok := m.procs[id]; ok {
			return n
		}
		return -id
	}
	name := func(side linkSide, c connection) string {
		from := side.names[c.Source]
		if c.Kind == connectionStart {
			from = "Start"
		}
		n := from + " → " + side.names[c.Target]
		if c.Kind == connectionFailure {
			n += ", on failure"
		}
		return n
	}
	item := func(side linkSide, change string, c connection) diffItem {
		source, target := c.Source, c.Target
		it := diffItem{Change: change, ID: c.ID, Name: name(side, c), Board: side.board[c.Target], Target: &target}
		if c.Kind != connectionStart {
			it.Process = &source
		}
		return it
	}
	waiting := map[connKey]int{}
	for _, c := range connectionsOf(a) {
		waiting[connKey{mapped(c.Source), mapped(c.Target), c.Kind}]++
	}
	for _, c := range connectionsOf(b) {
		k := connKey{c.Source, c.Target, c.Kind}
		if waiting[k] > 0 {
			waiting[k]--
			continue
		}
		d.Connections = append(d.Connections, item(sb, diffAdded, c))
	}
	for _, c := range connectionsOf(a) {
		k := connKey{mapped(c.Source), mapped(c.Target), c.Kind}
		if waiting[k] > 0 {
			waiting[k]--
			d.Connections = append(d.Connections, item(sa, diffRemoved, c))
		}
	}
}

// What changed of the flow's settings; nothing when either board is from
// before versions kept them.
func diffSettings(a, b *flowSettings) []diffField {
	fs := diffFields{}
	if a == nil || b == nil {
		return fs
	}
	repos := func(s *flowSettings) string {
		var lines []string
		for _, r := range s.Repositories {
			line := r.Name + " " + r.URL
			if r.Base != "" {
				line += " " + r.Base
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	}
	limit := func(s *flowSettings) string {
		if s.RunSpendLimit == 0 {
			return ""
		}
		return fmt.Sprintf("$%g", s.RunSpendLimit)
	}
	fs.add("repositories", repos(a), repos(b))
	fs.add("branch prefix", a.BranchPrefix, b.BranchPrefix)
	fs.add("run spend limit", limit(a), limit(b))
	return fs
}

// The board a version keeps, and the flow it is of.
func versionBoard(tx *sql.Tx, r *http.Request, id int64) (int64, snapshot, error) {
	var flow int64
	var body []byte
	var s snapshot
	if err := tx.QueryRowContext(r.Context(), "SELECT flow_id, snapshot FROM flow_versions WHERE id = ?", id).Scan(&flow, &body); err != nil {
		return 0, s, err
	}
	return flow, s, json.Unmarshal(body, &s)
}

func registerDiffRoutes(mux *http.ServeMux, db *sql.DB) {
	// What changed from the version to the board as it is, or with ?to= to
	// another version of the same flow.
	mux.HandleFunc("GET /api/versions/{id}/diff", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var to *int64
		if v := r.URL.Query().Get("to"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				http.Error(w, "invalid version to compare to", http.StatusBadRequest)
				return
			}
			to = &n
		}
		res := struct {
			From int64  `json:"from"`
			To   *int64 `json:"to"`
			boardDiff
		}{From: id, To: to}
		err = inTx(r.Context(), db, func(tx *sql.Tx) error {
			flow, a, err := versionBoard(tx, r, id)
			if err != nil {
				return err
			}
			var b snapshot
			if to == nil {
				b, err = takeSnapshot(r.Context(), tx, flow)
			} else {
				var of int64
				if of, b, err = versionBoard(tx, r, *to); err == nil && of != flow {
					return errOtherFlow
				}
			}
			if err != nil {
				return err
			}
			res.boardDiff = diffSnapshots(a, b)
			return nil
		})
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "version not found", http.StatusNotFound)
		case errors.Is(err, errOtherFlow):
			http.Error(w, "the versions are of different flows", http.StatusBadRequest)
		case err != nil:
			serverError(w, err)
		default:
			writeJSON(w, http.StatusOK, res)
		}
	})
}

var errOtherFlow = errors.New("of another flow")
