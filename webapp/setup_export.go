package main

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strings"
)

// A flow as exporting it needs it: its board, and the identifiers the file
// gives what is on it, by id.
type exportedFlow struct {
	id               int64
	name             string
	module, isStruct bool
	settings         flowSettings
	s                snapshot
	procIDs, objIDs  map[int64]string
	stateIDs         map[int64]string
	objects          map[int64]snapshotObject
	linkStates       map[int64][]int64
	schedules        []setupSchedule
}

// The setup as it is: every MCP server, struct, procedure and flow, by name,
// or with only set, that flow and what it refers to: the procedures it runs,
// the structs and procedures its copies follow, on down, and the MCP servers
// its processes use. Two flows can't share a name in the file, since it names
// them to refer to them; with two that do, it says so instead.
func exportSetup(ctx context.Context, tx *sql.Tx, only int64) (setupFile, error) {
	f := setupFile{Spoletta: setupVersion}
	var flows []*exportedFlow
	err := scanAll(ctx, tx, "SELECT id, name, module, is_struct, settings FROM flows ORDER BY name, id", func(rows *sql.Rows) error {
		e := &exportedFlow{}
		err := rows.Scan(&e.id, &e.name, &e.module, &e.isStruct, &e.settings)
		flows = append(flows, e)
		return err
	})
	if err != nil {
		return f, err
	}

	// Which flow each object is in, for copies to name their sources by.
	objectFlow := map[int64]*exportedFlow{}
	byID := map[int64]*exportedFlow{}
	for _, e := range flows {
		if e.s, err = takeSnapshot(ctx, tx, e.id); err != nil {
			return f, err
		}
		e.identify()
		byID[e.id] = e
		for _, o := range e.s.Objects {
			objectFlow[o.ID] = e
		}
	}
	if only != 0 {
		if byID[only] == nil {
			return f, errNotFound
		}
		flows = referredFrom(byID[only], byID, objectFlow)
	}
	var same []string
	for i := 1; i < len(flows); i++ {
		if flows[i].name == flows[i-1].name && (len(same) == 0 || same[len(same)-1] != quote(flows[i].name)) {
			same = append(same, quote(flows[i].name))
		}
	}
	if len(same) > 0 {
		return f, problemf("more than one flow is named %s; rename them apart to export, since the file refers to flows by name", list(same, "and"))
	}
	used := map[int64]bool{}
	for _, e := range flows {
		for _, p := range e.s.Processes {
			for _, m := range p.Settings.MCPServers {
				used[m] = true
			}
		}
		err := scanAll(ctx, tx, "SELECT cron, timezone, enabled FROM schedules WHERE flow_id = ? ORDER BY id", func(rows *sql.Rows) error {
			var s setupSchedule
			var enabled bool
			err := rows.Scan(&s.Cron, &s.Timezone, &enabled)
			if !enabled {
				s.Enabled = &enabled
			}
			e.schedules = append(e.schedules, s)
			return err
		}, e.id)
		if err != nil {
			return f, err
		}
	}

	mcpNames := map[int64]string{}
	err = scanAll(ctx, tx, "SELECT id, name, url, description FROM mcp_servers ORDER BY name", func(rows *sql.Rows) error {
		var id int64
		var s setupMCPServer
		err := rows.Scan(&id, &s.Name, &s.URL, &s.Description)
		mcpNames[id] = s.Name
		if only == 0 || used[id] {
			f.MCPServers = append(f.MCPServers, s)
		}
		return err
	})
	if err != nil {
		return f, err
	}
	flowNames := map[int64]string{}
	for _, e := range flows {
		flowNames[e.id] = e.name
	}

	for _, e := range flows {
		x := exporter{e: e, objectFlow: objectFlow, flowNames: flowNames, mcpNames: mcpNames}
		if e.isStruct {
			for _, o := range e.s.Objects {
				if o.Owner == nil && o.Parent == nil {
					f.Structs = append(f.Structs, x.object(o))
					break
				}
			}
			continue
		}
		sf := setupFlow{Name: e.name, BranchPrefix: e.settings.BranchPrefix, Schedules: e.schedules, setupBoard: x.board(nil)}
		for _, r := range e.settings.Repositories {
			sf.Repositories = append(sf.Repositories, setupRepository(r))
		}
		if e.module {
			f.Procedures = append(f.Procedures, sf)
		} else {
			f.Flows = append(f.Flows, sf)
		}
	}
	return f, nil
}

// The flow e and the flows it refers to, on down, in the order of flows.
func referredFrom(e *exportedFlow, byID map[int64]*exportedFlow, objectFlow map[int64]*exportedFlow) []*exportedFlow {
	keep := map[int64]bool{}
	var visit func(e *exportedFlow)
	visit = func(e *exportedFlow) {
		if e == nil || keep[e.id] {
			return
		}
		keep[e.id] = true
		for _, p := range e.s.Processes {
			if p.Settings.Flow != nil {
				visit(byID[*p.Settings.Flow])
			}
		}
		for _, o := range e.s.Objects {
			if o.Source != nil {
				visit(objectFlow[*o.Source])
			}
		}
	}
	visit(e)
	var flows []*exportedFlow
	for _, f := range byID {
		if keep[f.id] {
			flows = append(flows, f)
		}
	}
	slices.SortFunc(flows, func(a, b *exportedFlow) int {
		return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(a.id, b.id))
	})
	return flows
}

// Gives what is on the flow's boards the identifiers the file refers to
// them by: processes and objects by flow, states by object.
func (e *exportedFlow) identify() {
	e.procIDs, e.objIDs, e.stateIDs = map[int64]string{}, map[int64]string{}, map[int64]string{}
	e.objects, e.linkStates = map[int64]snapshotObject{}, map[int64][]int64{}
	var names []string
	for _, p := range e.s.Processes {
		names = append(names, p.Name)
	}
	for i, k := range setupKeys(names) {
		e.procIDs[e.s.Processes[i].ID] = ident(names[i], k)
	}
	names = nil
	for _, o := range e.s.Objects {
		names = append(names, o.Name)
		e.objects[o.ID] = o
	}
	for i, k := range setupKeys(names) {
		e.objIDs[e.s.Objects[i].ID] = ident(names[i], k)
	}
	byObject := map[int64][]objectState{}
	for _, st := range e.s.States {
		byObject[st.Object] = append(byObject[st.Object], st)
	}
	for _, sts := range byObject {
		names = nil
		for _, st := range sts {
			names = append(names, st.Name)
		}
		for i, k := range setupKeys(names) {
			e.stateIDs[sts[i].ID] = ident(names[i], k)
		}
	}
	for _, ls := range e.s.LinkStates {
		e.linkStates[ls.Link] = append(e.linkStates[ls.Link], ls.State)
	}
}

// The key something got, "" when its name serves.
func setupKeyOf(name, id string) string {
	if id == name {
		return ""
	}
	return id
}

type exporter struct {
	e          *exportedFlow
	objectFlow map[int64]*exportedFlow
	flowNames  map[int64]string
	mcpNames   map[int64]string
}

// The board of a flow process, or with none the flow's top board.
func (x exporter) board(parent *int64) setupBoard {
	s := x.e.s
	var b setupBoard
	start, end := s.Start, s.End
	if parent != nil {
		for _, ib := range s.Boards {
			if ib.Process == *parent {
				start, end = ib.Start, &boardEnd{ib.End.X, ib.End.Y}
			}
		}
	}
	b.Start = &setupStart{X: addrOf(start.X), Y: addrOf(start.Y), Route: exportRoute(start.Route)}
	if start.Target != nil {
		b.Start.Next = x.e.procIDs[*start.Target]
	}
	if end != nil {
		b.End = &setupPoint{end.X, end.Y}
	}
	for _, p := range s.Processes {
		if sameBoard(p.Parent, parent) {
			b.Processes = append(b.Processes, x.process(p))
		}
	}
	for _, o := range s.Objects {
		if o.Owner == nil && sameBoard(o.Parent, parent) {
			b.Objects = append(b.Objects, x.object(o))
		}
	}
	return b
}

func addrOf[T any](v T) *T { return &v }

// A stored route as the file has it; one that isn't JSON is left out.
func exportRoute(r []byte) any {
	v, err := setupRoute(r)
	if err != nil {
		return nil
	}
	return v
}

func (x exporter) process(p process) setupProcess {
	id := x.e.procIDs[p.ID]
	sp := setupProcess{
		Name: p.Name, Key: setupKeyOf(p.Name, id), Kind: p.Kind, Model: p.Model, Thinking: p.Thinking,
		Prompt: p.Prompt, Script: p.Script, Recording: p.Recording,
		X: addrOf(p.X), Y: addrOf(p.Y), Width: p.Width,
	}
	st := p.Settings
	if st.Flow != nil {
		sp.Runs = x.flowNames[*st.Flow]
	}
	for _, m := range st.MCPServers {
		if n, ok := x.mcpNames[m]; ok {
			sp.MCPServers = append(sp.MCPServers, n)
		}
	}
	sp.Repositories = st.Repositories
	if st.Sentry != nil {
		sp.Sentry = (*setupSentry)(st.Sentry)
	}
	sp.Rounds, sp.Timeout, sp.Retries, sp.Quorum, sp.DescribeFlow = st.Rounds, st.Timeout, st.Retries, st.Quorum, st.DescribeFlow

	for _, l := range x.e.s.Links {
		if l.Process != p.ID {
			continue
		}
		sl := setupLink{Object: x.e.objIDs[l.Object], Role: l.Role, Condition: l.Condition, Route: exportRoute(l.Route)}
		if l.State != 0 {
			sl.State = x.e.stateIDs[l.State]
		}
		for _, st := range x.e.linkStates[l.ID] {
			sl.States = append(sl.States, x.e.stateIDs[st])
		}
		if l.At != nil {
			sl.At = &setupPoint{l.At.X, l.At.Y}
		}
		if l.Direction == directionInput {
			sp.Takes = append(sp.Takes, sl)
		} else {
			sp.Yields = append(sp.Yields, sl)
		}
	}
	for _, c := range x.e.s.Connections {
		if c.Source != p.ID {
			continue
		}
		if c.Kind == connectionFailure {
			sp.OnFailure = append(sp.OnFailure, x.e.procIDs[c.Target])
		} else {
			sp.Next = append(sp.Next, x.e.procIDs[c.Target])
		}
	}
	if p.Kind == kindFlow {
		b := x.board(&p.ID)
		sp.Board = &b
	}
	return sp
}

// An object with what makes it up, or for a copy, the object it follows,
// which has that instead.
func (x exporter) object(o snapshotObject) setupObject {
	id := x.e.objIDs[o.ID]
	so := setupObject{
		Name: o.Name, Key: setupKeyOf(o.Name, id), Persistent: o.Persistent, Many: o.Many, Min: o.Min, Max: o.Max,
		Human: o.Human, End: o.End, X: addrOf(o.X), Y: addrOf(o.Y), Width: o.Width,
	}
	if o.Owner != nil {
		so.Relation = o.Relation
		if so.Relation == relationExhibition {
			so.Relation = ""
		}
	}
	if o.General != nil {
		so.Specializes = x.e.objIDs[*o.General]
	}
	if o.Source != nil {
		if src := x.objectFlow[*o.Source]; src != nil {
			so.CopyOf = &setupSource{Flow: src.name}
			// Only a struct's own object goes without saying.
			if s := src.objects[*o.Source]; !src.isStruct || s.Owner != nil || s.Parent != nil {
				so.CopyOf.Object = src.objIDs[*o.Source]
			}
			return so
		}
	}
	for _, st := range x.e.s.States {
		if st.Object == o.ID {
			sid := x.e.stateIDs[st.ID]
			so.States = append(so.States, setupState{Name: st.Name, Key: setupKeyOf(st.Name, sid), Rule: st.Rule})
		}
	}
	for _, a := range x.e.s.Attributes {
		if a.Object == o.ID {
			so.Attributes = append(so.Attributes, setupAttribute{Name: a.Name, Definition: a.Definition, Unique: a.Unique, File: a.File})
		}
	}
	for _, part := range x.e.s.Objects {
		if part.Owner != nil && *part.Owner == o.ID {
			so.Parts = append(so.Parts, x.object(part))
		}
	}
	return so
}
