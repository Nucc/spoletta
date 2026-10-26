package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// A flow an import created or made over, and for one made over, the version
// its board was saved as first.
type importedFlow struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Created bool   `json:"created"`
	Version int64  `json:"version,omitempty"`
}

type setupResult struct {
	Flows []importedFlow `json:"flows"`
}

const (
	entryStruct    = "struct"
	entryProcedure = "procedure"
	entryFlow      = "flow"
)

// A struct, procedure or flow of the file.
type setupEntry struct {
	name   string
	kind   string
	flow   *setupFlow
	object *setupObject
}

type flowInfo struct {
	id               int64
	module, isStruct bool
}

type importer struct {
	ctx context.Context
	tx  *sql.Tx
	// MCP servers by mcpNameKey.
	mcp map[string]int64
	// The connector each MCP server is, by id, if it is one.
	mcpConnectors map[int64]string
	// Skills by name.
	skills map[string]int64
	// The flows in the app before the import, by name.
	existing map[string][]flowInfo
	// The flows of the file imported so far, by name.
	imported map[string]flowInfo
	entries  map[string]*setupEntry
	// Each flow's objects by identifier, for copies to find their sources.
	objects map[int64]map[string]int64
	// How the boards it makes over are saved as versions first.
	saved saving
	res   setupResult
}

// Imports f in tx: what is wrong with it is a setupProblem, and changes
// nothing. A flow it makes over has its board saved as a version first, as
// saved says.
func importSetup(ctx context.Context, tx *sql.Tx, f setupFile, saved saving) (setupResult, error) {
	im := &importer{ctx: ctx, tx: tx, mcp: map[string]int64{}, mcpConnectors: map[int64]string{}, skills: map[string]int64{}, existing: map[string][]flowInfo{},
		imported: map[string]flowInfo{}, entries: map[string]*setupEntry{}, objects: map[int64]map[string]int64{},
		saved: saved, res: setupResult{Flows: []importedFlow{}}}
	var entries []*setupEntry
	add := func(e *setupEntry) error {
		e.name = strings.TrimSpace(e.name)
		if e.name == "" {
			return problemf("every %s needs a name", e.kind)
		}
		if im.entries[e.name] != nil {
			return problemf("the file has more than one struct, procedure or flow named %s", quote(e.name))
		}
		im.entries[e.name] = e
		entries = append(entries, e)
		return nil
	}
	for i := range f.Structs {
		if err := add(&setupEntry{name: f.Structs[i].Name, kind: entryStruct, object: &f.Structs[i]}); err != nil {
			return im.res, err
		}
	}
	for i := range f.Procedures {
		if err := add(&setupEntry{name: f.Procedures[i].Name, kind: entryProcedure, flow: &f.Procedures[i]}); err != nil {
			return im.res, err
		}
	}
	for i := range f.Flows {
		if err := add(&setupEntry{name: f.Flows[i].Name, kind: entryFlow, flow: &f.Flows[i]}); err != nil {
			return im.res, err
		}
	}

	err := scanAll(ctx, tx, "SELECT id, name, module, is_struct FROM flows ORDER BY id FOR UPDATE", func(rows *sql.Rows) error {
		var fi flowInfo
		var name string
		err := rows.Scan(&fi.id, &name, &fi.module, &fi.isStruct)
		im.existing[name] = append(im.existing[name], fi)
		return err
	})
	if err != nil {
		return im.res, err
	}
	if err := im.mcpServers(f.MCPServers); err != nil {
		return im.res, err
	}
	if err := im.skillsOf(kindSkill, f.Skills); err != nil {
		return im.res, err
	}
	if err := im.skillsOf(kindCommand, f.Commands); err != nil {
		return im.res, err
	}
	order, err := im.order(entries)
	if err != nil {
		return im.res, err
	}
	for _, e := range order {
		if err := im.importEntry(e); err != nil {
			return im.res, err
		}
	}
	for _, fl := range im.res.Flows {
		if err := im.checkCalls(fl.ID, fl.Name); err != nil {
			return im.res, err
		}
	}
	return im.res, nil
}

// Adds the file's MCP servers the app doesn't have, and gives the ones it
// has by name the file's URL, description, auth and connector. One whose
// URL or auth changes is signed out, since its sign-ins or token were for
// the old one. A file never holds tokens: an admin sets those.
func (im *importer) mcpServers(servers []setupMCPServer) error {
	type have struct {
		id        int64
		url, auth string
	}
	existing := map[string]have{}
	err := scanAll(im.ctx, im.tx, "SELECT id, name, url, auth, connector FROM mcp_servers", func(rows *sql.Rows) error {
		var h have
		var name, connector string
		err := rows.Scan(&h.id, &name, &h.url, &h.auth, &connector)
		existing[mcpNameKey(name)] = h
		im.mcp[mcpNameKey(name)] = h.id
		im.mcpConnectors[h.id] = connector
		return err
	})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, sm := range servers {
		s := mcpServer{Name: strings.TrimSpace(sm.Name), URL: strings.TrimSpace(sm.URL), Description: strings.TrimSpace(sm.Description),
			Auth: strings.TrimSpace(sm.Auth), Connector: strings.TrimSpace(sm.Connector)}
		s.defaults()
		if msg := s.problem(); msg != "" {
			return problemf("MCP server %s: %s", quote(s.Name), msg)
		}
		k := mcpNameKey(s.Name)
		if seen[k] {
			return problemf("the file has more than one MCP server named %s", quote(s.Name))
		}
		seen[k] = true
		h, ok := existing[k]
		if !ok {
			res, err := im.tx.ExecContext(im.ctx, "INSERT INTO mcp_servers (name, url, description, auth, connector) VALUES (?, ?, ?, ?, ?)",
				s.Name, s.URL, s.Description, s.Auth, s.Connector)
			if err != nil {
				return err
			}
			if im.mcp[k], err = res.LastInsertId(); err != nil {
				return err
			}
			im.mcpConnectors[im.mcp[k]] = s.Connector
			continue
		}
		q := "UPDATE mcp_servers SET name = ?, url = ?, description = ?, auth = ?, connector = ? WHERE id = ?"
		if s.URL != h.url || s.Auth != h.auth {
			q = "UPDATE mcp_servers SET name = ?, url = ?, description = ?, auth = ?, connector = ?, client_id = '', redirect_uri = '', token_endpoint = '', access_token = NULL, refresh_token = NULL, expires_at = NULL WHERE id = ?"
			if _, err := im.tx.ExecContext(im.ctx, "DELETE FROM mcp_tokens WHERE server_id = ?", h.id); err != nil {
				return err
			}
		}
		if _, err := im.tx.ExecContext(im.ctx, q, s.Name, s.URL, s.Description, s.Auth, s.Connector, h.id); err != nil {
			return err
		}
		im.mcpConnectors[h.id] = s.Connector
	}
	return nil
}

// Adds the file's skills or commands of a kind the app doesn't have, as
// everyone's, as a flow it adds is, and gives the ones it has by name the
// file's description, arguments and content, leaving whose they are. The
// skills the app then has are kept for processes to name.
func (im *importer) skillsOf(kind string, items []setupSkill) error {
	existing := map[string]int64{}
	err := scanAll(im.ctx, im.tx, "SELECT id, name FROM skills WHERE kind = ? FOR UPDATE", func(rows *sql.Rows) error {
		var id int64
		var name string
		err := rows.Scan(&id, &name)
		existing[name] = id
		return err
	}, kind)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, it := range items {
		s := skillFields{Name: it.Name, Description: it.Description, ArgumentHint: it.Arguments, Content: it.Content}
		if msg := s.check(kind); msg != "" {
			return problemf("%s %s: %s", kind, quote(it.Name), msg)
		}
		if kind == kindSkill && s.ArgumentHint != "" {
			return problemf("skill %s: only a command takes arguments", quote(s.Name))
		}
		if seen[s.Name] {
			return problemf("the file has more than one %s named %s", kind, quote(s.Name))
		}
		seen[s.Name] = true
		if id, ok := existing[s.Name]; ok {
			_, err := im.tx.ExecContext(im.ctx, "UPDATE skills SET description = ?, argument_hint = ?, content = ? WHERE id = ?",
				s.Description, s.ArgumentHint, s.Content, id)
			if err != nil {
				return err
			}
			continue
		}
		res, err := im.tx.ExecContext(im.ctx, "INSERT INTO skills (kind, name, description, argument_hint, content) VALUES (?, ?, ?, ?, ?)",
			kind, s.Name, s.Description, s.ArgumentHint, s.Content)
		if err != nil {
			return err
		}
		if existing[s.Name], err = res.LastInsertId(); err != nil {
			return err
		}
	}
	if kind == kindSkill {
		im.skills = existing
	}
	return nil
}

// The flows of the file a board's processes run and its copies follow.
func boardUses(b *setupBoard, uses func(name string, runs bool)) {
	if b == nil {
		return
	}
	for i := range b.Processes {
		p := &b.Processes[i]
		if p.Runs != "" {
			uses(p.Runs, true)
		}
		boardUses(p.Board, uses)
	}
	for i := range b.Objects {
		objectUses(&b.Objects[i], uses)
	}
}

func objectUses(o *setupObject, uses func(name string, runs bool)) {
	if o.CopyOf != nil {
		uses(o.CopyOf.Flow, false)
	}
	for i := range o.Parts {
		objectUses(&o.Parts[i], uses)
	}
}

// The entries in the order they can be imported in: each after the
// procedures it runs and the flows its copies follow, in the file's order
// otherwise. Says what is wrong with what they refer to, if anything.
func (im *importer) order(entries []*setupEntry) ([]*setupEntry, error) {
	deps := map[*setupEntry][]*setupEntry{}
	for _, e := range entries {
		var err error
		uses := func(name string, runs bool) {
			if err != nil {
				return
			}
			d := im.entries[name]
			switch {
			case d == nil:
				fi, ok, msg := im.existingFlow(name)
				switch {
				case msg != "":
					err = problemf("%s: %s", quote(e.name), msg)
				case !ok:
					err = problemf("%s refers to %s, which is neither in the file nor in the app", quote(e.name), quote(name))
				case runs && fi.isStruct:
					err = problemf("%s runs %s, which is a struct, an object rather than processes to run", quote(e.name), quote(name))
				case !runs && !fi.isStruct && !fi.module:
					err = problemf("%s copies an object of %s, but copies follow a struct's object or a procedure's", quote(e.name), quote(name))
				}
				return
			case runs && d.kind == entryStruct:
				err = problemf("%s runs %s, which is a struct, an object rather than processes to run", quote(e.name), quote(name))
			case d.kind == entryFlow:
				err = problemf("%s runs or copies from %s, so %s belongs under procedures", quote(e.name), quote(name), quote(name))
			case d == e:
				err = problemf("%s can't run or copy from itself", quote(e.name))
			}
			deps[e] = append(deps[e], d)
		}
		if e.flow != nil {
			boardUses(&e.flow.setupBoard, uses)
		} else {
			objectUses(e.object, uses)
		}
		if err != nil {
			return nil, err
		}
	}
	var order []*setupEntry
	placed := map[*setupEntry]bool{}
	for len(order) < len(entries) {
		before := len(order)
		for _, e := range entries {
			ready := !placed[e]
			for _, d := range deps[e] {
				ready = ready && placed[d]
			}
			if ready {
				placed[e] = true
				order = append(order, e)
			}
		}
		if len(order) == before {
			var left []string
			for _, e := range entries {
				if !placed[e] {
					left = append(left, quote(e.name))
				}
			}
			return nil, problemf("%s run or copy from each other in a circle", list(left, "and"))
		}
	}
	return order, nil
}

// The flow of the app named name, if there is exactly one; says so if there
// are more.
func (im *importer) existingFlow(name string) (flowInfo, bool, string) {
	fs := im.existing[name]
	switch len(fs) {
	case 0:
		return flowInfo{}, false, ""
	case 1:
		return fs[0], true, ""
	}
	return flowInfo{}, false, fmt.Sprintf("the app has more than one flow named %s; rename them apart first", quote(name))
}

// The flow a reference names: the file's, imported by now, or the app's.
func (im *importer) flow(name string) (flowInfo, error) {
	if fi, ok := im.imported[name]; ok {
		return fi, nil
	}
	fi, ok, msg := im.existingFlow(name)
	if msg != "" {
		return fi, setupProblem{msg}
	}
	if !ok {
		return fi, problemf("there is no flow named %s", quote(name))
	}
	return fi, nil
}

func (im *importer) importEntry(e *setupEntry) error {
	ctx, tx := im.ctx, im.tx
	module := e.kind == entryProcedure
	isStruct := e.kind == entryStruct
	var settings flowSettings
	if e.flow != nil {
		for _, r := range e.flow.Repositories {
			settings.Repositories = append(settings.Repositories, repository(r))
		}
		settings.BranchPrefix = e.flow.BranchPrefix
		if msg := settings.check(); msg != "" {
			return problemf("%s: %s", quote(e.name), msg)
		}
	}

	fi, exists, msg := im.existingFlow(e.name)
	if msg != "" {
		return setupProblem{msg}
	}
	var before int64
	if exists {
		if fi.isStruct != isStruct {
			what := map[bool]string{true: "a struct", false: "a flow or procedure"}
			return problemf("%s is %s in the app but %s in the file", quote(e.name), what[fi.isStruct], what[isStruct])
		}
		running, err := lockBoard(ctx, tx, fi.id)
		if err != nil {
			return err
		}
		if running {
			return problemf("%s is running; import once it is done", quote(e.name))
		}
		if fi.module && !module && !isStruct {
			n, err := usersOf(ctx, tx, fi.id)
			if err != nil {
				return err
			}
			if n > 0 {
				return problemf("%d other flows run %s, so it belongs under procedures", n, quote(e.name))
			}
		}
		// So that what the import makes over can be gone back to.
		if before, err = saveVersion(ctx, tx, fi.id, true, im.saved); err != nil {
			return err
		}
		if _, err := recordStep(ctx, tx, fi.id); err != nil {
			return err
		}
		if !isStruct {
			if _, err := tx.ExecContext(ctx, "UPDATE flows SET module = ?, settings = ? WHERE id = ?", module, settings, fi.id); err != nil {
				return err
			}
		}
		fi.module = module || (fi.module && isStruct)
	} else {
		res, err := tx.ExecContext(ctx, "INSERT INTO flows (name, module, is_struct, settings) VALUES (?, ?, ?, ?)", e.name, module, isStruct, settings)
		if err != nil {
			return err
		}
		if fi.id, err = res.LastInsertId(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO flow_starts (flow_id, x, y) VALUES (?, 40, 40)", fi.id); err != nil {
			return err
		}
		fi.module, fi.isStruct = module, isStruct
	}
	im.imported[e.name] = fi

	b, err := newBuilder(im, fi.id, e.name)
	if err != nil {
		return err
	}
	if isStruct {
		err = b.buildStruct(e.object)
	} else {
		err = b.build(&e.flow.setupBoard)
	}
	if err != nil {
		return err
	}
	if !isStruct {
		if err := im.schedules(fi.id, e.name, e.flow.Schedules); err != nil {
			return err
		}
	}
	given := e.object.tags()
	if e.flow != nil {
		given = e.flow.Tags
	}
	tags, msg := cleanTags(given)
	if msg != "" {
		return problemf("%s: %s", quote(e.name), msg)
	}
	if err := setTags(ctx, tx, fi.id, tags); err != nil {
		return err
	}
	if _, err := recordStep(ctx, tx, fi.id); err != nil {
		return err
	}
	im.res.Flows = append(im.res.Flows, importedFlow{ID: fi.id, Name: e.name, Created: !exists, Version: before})
	return nil
}

// Makes the flow's schedules the file's: the ones it has already stay, with
// the runs they started, and the others go.
func (im *importer) schedules(flow int64, name string, want []setupSchedule) error {
	type have struct {
		id         int64
		cron, zone string
		enabled    bool
	}
	var haves []have
	err := scanAll(im.ctx, im.tx, "SELECT id, cron, timezone, enabled FROM schedules WHERE flow_id = ? ORDER BY id", func(rows *sql.Rows) error {
		var h have
		err := rows.Scan(&h.id, &h.cron, &h.zone, &h.enabled)
		haves = append(haves, h)
		return err
	}, flow)
	if err != nil {
		return err
	}
	kept := map[int64]bool{}
	now := time.Now()
	for _, s := range want {
		s.Cron = strings.TrimSpace(s.Cron)
		if _, _, msg := parseSchedule(s.Cron, s.Timezone); msg != "" {
			return problemf("%s: schedule %s: %s", quote(name), quote(s.Cron), msg)
		}
		enabled := s.Enabled == nil || *s.Enabled
		var match *have
		for i := range haves {
			if h := &haves[i]; !kept[h.id] && h.cron == s.Cron && h.zone == s.Timezone {
				match = h
				break
			}
		}
		if match == nil {
			_, err := im.tx.ExecContext(im.ctx, "INSERT INTO schedules (flow_id, cron, timezone, enabled, next_run_at) VALUES (?, ?, ?, ?, ?)",
				flow, s.Cron, s.Timezone, enabled, nextRun(s.Cron, s.Timezone, enabled, now))
			if err != nil {
				return err
			}
			continue
		}
		kept[match.id] = true
		if match.enabled != enabled {
			_, err := im.tx.ExecContext(im.ctx, "UPDATE schedules SET enabled = ?, next_run_at = ? WHERE id = ?",
				enabled, nextRun(s.Cron, s.Timezone, enabled, now), match.id)
			if err != nil {
				return err
			}
		}
	}
	for _, h := range haves {
		if !kept[h.id] {
			if _, err := im.tx.ExecContext(im.ctx, "DELETE FROM schedules WHERE id = ?", h.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Says so if the flow runs itself, however many procedures apart, as
// checkCall does for one process.
func (im *importer) checkCalls(flow int64, name string) error {
	seen := map[int64]bool{}
	next := []int64{flow}
	for len(next) > 0 {
		f := next[0]
		next = next[1:]
		if seen[f] {
			continue
		}
		seen[f] = true
		err := scanAll(im.ctx, im.tx, `SELECT DISTINCT CAST(JSON_EXTRACT(settings, '$.flow') AS SIGNED) FROM processes
			WHERE flow_id = ? AND kind = ? AND JSON_EXTRACT(settings, '$.flow') IS NOT NULL`, func(rows *sql.Rows) error {
			var c int64
			err := rows.Scan(&c)
			if c == flow {
				return problemf("%s runs itself, through the procedures it runs", quote(name))
			}
			next = append(next, c)
			return err
		}, f, kindSubflow)
		if err != nil {
			return err
		}
	}
	return nil
}

// The objects of a flow by identifier: the file's for a flow imported, or
// as an export would name them for another.
func (im *importer) objectsOf(flow int64) (map[string]int64, error) {
	if m, ok := im.objects[flow]; ok {
		return m, nil
	}
	e := &exportedFlow{}
	var err error
	if e.s, err = takeSnapshot(im.ctx, im.tx, flow); err != nil {
		return nil, err
	}
	e.identify()
	m := map[string]int64{}
	for id, k := range e.objIDs {
		m[k] = id
	}
	im.objects[flow] = m
	return m, nil
}

// A process of the file, and the id it gets.
type bproc struct {
	id     int64
	p      *setupProcess
	parent *int64
	cur    *process
	x, y   float64
	placed bool
	where  string
}

// An object of the file, and the id it gets.
type bobj struct {
	id            int64
	o             *setupObject
	parent, owner *int64
	cur           *snapshotObject
	// Its states' ids by identifier.
	states map[string]int64
	source *int64
	x, y   float64
	placed bool
	where  string
}

type blink struct {
	proc      *bproc
	obj       *bobj
	direction string
	state     int64
	states    []int64
	role      string
	condition bool
	query     *linkQuery
	at        *point
	route     []byte
	where     string
}

// Builds a flow's board from the file over the board it has: what matches
// by name keeps its id, and the rest gets a new one.
type builder struct {
	*importer
	flow  int64
	name  string
	cur   snapshot
	out   snapshot
	procs map[string]*bproc
	objs  map[string]*bobj
	plist []*bproc
	olist []*bobj
	links []blink
	conns []connection
	used  map[string]map[int64]bool
	// Placeholders get names of their own, as attributes' must differ.
	placeholders int
	// Building a struct, whose object alone can be shared.
	isStruct bool
}

func newBuilder(im *importer, flow int64, name string) (*builder, error) {
	cur, err := takeSnapshot(im.ctx, im.tx, flow)
	if err != nil {
		return nil, err
	}
	return &builder{importer: im, flow: flow, name: name, cur: cur,
		out: snapshot{Processes: []process{}, Objects: []snapshotObject{}, States: []objectState{}, Attributes: []attribute{},
			Links: []snapshotLink{}, LinkStates: []snapshotLinkState{}, Connections: []connection{}, Boards: []innerBoard{}},
		procs: map[string]*bproc{}, objs: map[string]*bobj{},
		used: map[string]map[int64]bool{"process": {}, "object": {}, "state": {}, "attribute": {}}}, nil
}

// A new row's id, kept by a placeholder row until the board is rewritten.
func (b *builder) reserve(query string, args ...any) (int64, error) {
	res, err := b.tx.ExecContext(b.ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (b *builder) placeholder() string {
	b.placeholders++
	return fmt.Sprintf("\x00import %d", b.placeholders)
}

func (b *builder) build(top *setupBoard) error {
	if err := b.walkBoard(top, nil, quote(b.name)); err != nil {
		return err
	}
	b.layout(top, nil)
	if err := b.resolveBoard(top, nil, quote(b.name)); err != nil {
		return err
	}
	return b.write()
}

// A struct's board holds its object alone, named as the struct is.
func (b *builder) buildStruct(o *setupObject) error {
	where := quote(b.name)
	if o.CopyOf != nil {
		return problemf("%s: a struct's object is its own, not a copy", where)
	}
	if o.Human || o.Relation != "" || o.Specializes != "" {
		return problemf("%s: a struct's object is neither a person, a part nor a specialization", where)
	}
	if o.Shared && !slices.ContainsFunc(o.Attributes, func(a setupAttribute) bool { return a.Unique }) {
		return problemf("%s: records are told apart by a unique attribute; mark one unique before sharing the object", where)
	}
	if o.Shared && o.Persistent {
		return problemf("%s: a shared object is stored by every flow using it; it isn't persistent too", where)
	}
	o.Name = b.name
	b.isStruct = true
	board := &setupBoard{Objects: []setupObject{*o}}
	return b.build(board)
}

func (b *builder) walkBoard(board *setupBoard, parent *int64, where string) error {
	for i := range board.Processes {
		p := &board.Processes[i]
		p.Name = strings.TrimSpace(p.Name)
		id := ident(p.Name, p.Key)
		at := fmt.Sprintf("%s: process %s", where, quote(id))
		if p.Name == "" {
			return problemf("%s: a process has no name", where)
		}
		if b.procs[id] != nil {
			return problemf("%s: two processes are %s; give one a key of its own", quote(b.name), quote(id))
		}
		bp := &bproc{p: p, parent: parent, where: at}
		for i, c := range b.cur.Processes {
			if !b.used["process"][c.ID] && sameBoard(c.Parent, parent) && c.Name == p.Name {
				bp.cur, bp.id = &b.cur.Processes[i], c.ID
				break
			}
		}
		if bp.cur == nil {
			var err error
			if bp.id, err = b.reserve("INSERT INTO processes (flow_id, name, kind, prompt, script) VALUES (?, '', 'agent', '', '')", b.flow); err != nil {
				return err
			}
		}
		b.used["process"][bp.id] = true
		b.procs[id] = bp
		b.plist = append(b.plist, bp)
		if p.Kind == "" {
			p.Kind = kindAgent
		}
		if p.Kind == kindFlow {
			if p.Board == nil {
				p.Board = &setupBoard{}
			}
			if err := b.walkBoard(p.Board, &bp.id, at); err != nil {
				return err
			}
		} else if p.Board != nil {
			return problemf("%s: only a flow process has a board", at)
		}
	}
	for i := range board.Objects {
		if err := b.walkObject(&board.Objects[i], parent, nil, where); err != nil {
			return err
		}
	}
	return nil
}

func (b *builder) walkObject(o *setupObject, parent, owner *int64, where string) error {
	o.Name = strings.TrimSpace(o.Name)
	var source int64
	if o.CopyOf != nil {
		// A copy is named as what it copies, which may go without saying.
		var name string
		var err error
		if source, name, err = b.copySource(o.CopyOf); err != nil {
			return problemf("%s: object %s: %v", where, quote(ident(o.Name, o.Key)), err)
		}
		if o.Name == "" {
			o.Name = name
		} else if o.Name != name {
			return problemf("%s: object %s: a copy is named as what it copies, %s", where, quote(ident(o.Name, o.Key)), quote(name))
		}
	}
	id := ident(o.Name, o.Key)
	at := fmt.Sprintf("%s: object %s", where, quote(id))
	if o.Name == "" {
		return problemf("%s: an object has no name", where)
	}
	if b.objs[id] != nil {
		return problemf("%s: two objects are %s; give one a key of its own", quote(b.name), quote(id))
	}
	bo := &bobj{o: o, parent: parent, owner: owner, states: map[string]int64{}, where: at}
	for i, c := range b.cur.Objects {
		if !b.used["object"][c.ID] && sameBoard(c.Parent, parent) && sameBoard(c.Owner, owner) && c.Name == o.Name {
			bo.cur, bo.id = &b.cur.Objects[i], c.ID
			break
		}
	}
	if bo.cur == nil {
		var err error
		if bo.id, err = b.reserve("INSERT INTO objects (flow_id, name) VALUES (?, '')", b.flow); err != nil {
			return err
		}
	}
	b.used["object"][bo.id] = true
	b.objs[id] = bo
	b.olist = append(b.olist, bo)

	if owner == nil && o.Relation != "" {
		return problemf("%s: only a part has a relation", at)
	}
	if owner != nil {
		if o.Relation == "" {
			o.Relation = relationExhibition
		}
		if o.Relation != relationExhibition && o.Relation != relationAggregation {
			return problemf("%s: relation must be exhibition or aggregation", at)
		}
		if o.Human {
			return problemf("%s: a person can't make up an object", at)
		}
	}
	if o.CopyOf != nil {
		if len(o.States) > 0 || len(o.Attributes) > 0 || len(o.Parts) > 0 {
			return problemf("%s: a copy has the states, attributes and parts of what it copies; leave them out", at)
		}
		return b.walkCopy(bo, source)
	}

	for _, st := range o.States {
		name := strings.TrimSpace(st.Name)
		sid := ident(name, st.Key)
		if name == "" {
			return problemf("%s: a state has no name", at)
		}
		if bo.states[sid] != 0 {
			return problemf("%s: two states are %s; give one a key of its own", at, quote(sid))
		}
		id, err := b.matchOrReserve("state", bo, name, "INSERT INTO object_states (object_id, name) VALUES (?, '')", bo.id)
		if err != nil {
			return err
		}
		bo.states[sid] = id
		b.out.States = append(b.out.States, objectState{ID: id, Object: bo.id, Name: name, Rule: st.Rule})
	}
	names := map[string]bool{}
	for _, a := range o.Attributes {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			return problemf("%s: an attribute has no name", at)
		}
		if names[name] {
			return problemf("%s: two attributes are named %s", at, quote(name))
		}
		names[name] = true
		id, err := b.matchOrReserve("attribute", bo, name, "INSERT INTO object_attributes (object_id, name, definition) VALUES (?, ?, '')", bo.id, b.placeholder())
		if err != nil {
			return err
		}
		b.out.Attributes = append(b.out.Attributes, attribute{ID: id, Object: bo.id, Name: name, Definition: a.Definition, Unique: a.Unique, File: a.File})
	}
	for i := range o.Parts {
		if err := b.walkObject(&o.Parts[i], nil, &bo.id, at); err != nil {
			return err
		}
	}
	return nil
}

// The id of the object's state or attribute of name it has already, or a new one's.
func (b *builder) matchOrReserve(what string, bo *bobj, name, reserve string, args ...any) (int64, error) {
	if bo.cur != nil {
		if what == "state" {
			for _, st := range b.cur.States {
				if st.Object == bo.id && st.Name == name && !b.used[what][st.ID] {
					b.used[what][st.ID] = true
					return st.ID, nil
				}
			}
		} else {
			for _, a := range b.cur.Attributes {
				if a.Object == bo.id && a.Name == name && !b.used[what][a.ID] {
					b.used[what][a.ID] = true
					return a.ID, nil
				}
			}
		}
	}
	return b.reserve(reserve, args...)
}

// A copy takes its states from its source, as they are now, keeping the
// ids of the ones it has. What else it had stays, for syncCopies to make
// over as the source has it; a new copy gets that from syncCopies too.
func (b *builder) walkCopy(bo *bobj, source int64) error {
	bo.source = &source
	var err error
	var sts []objectState
	err = scanAll(b.ctx, b.tx, "SELECT id, name, COALESCE(rule, '') FROM object_states WHERE object_id = ? ORDER BY id", func(rows *sql.Rows) error {
		var st objectState
		err := rows.Scan(&st.ID, &st.Name, &st.Rule)
		sts = append(sts, st)
		return err
	}, source)
	if err != nil {
		return err
	}
	names := make([]string, len(sts))
	for i, st := range sts {
		names[i] = st.Name
	}
	keys := setupKeys(names)
	for i, st := range sts {
		var id int64
		if bo.cur != nil {
			for _, have := range b.cur.States {
				if have.Object == bo.id && !b.used["state"][have.ID] && have.Source != nil && *have.Source == st.ID {
					id = have.ID
					break
				}
			}
			for _, have := range b.cur.States {
				if id == 0 && have.Object == bo.id && !b.used["state"][have.ID] && have.Name == st.Name {
					id = have.ID
				}
			}
		}
		if id == 0 {
			if id, err = b.reserve("INSERT INTO object_states (object_id, name) VALUES (?, '')", bo.id); err != nil {
				return err
			}
		}
		b.used["state"][id] = true
		bo.states[ident(st.Name, keys[i])] = id
		srcID := st.ID
		b.out.States = append(b.out.States, objectState{ID: id, Object: bo.id, Name: st.Name, Rule: st.Rule, Source: &srcID})
	}
	if bo.cur == nil {
		return nil
	}
	// What made it up stays, as it is.
	b.carry(bo.id)
	return nil
}

// Keeps the attributes of an object the board has, and the objects making
// it up with theirs, as they are.
func (b *builder) carry(object int64) {
	for _, a := range b.cur.Attributes {
		if a.Object == object && !b.used["attribute"][a.ID] {
			b.used["attribute"][a.ID] = true
			b.out.Attributes = append(b.out.Attributes, a)
		}
	}
	for _, o := range b.cur.Objects {
		if o.Owner == nil || *o.Owner != object || b.used["object"][o.ID] {
			continue
		}
		b.used["object"][o.ID] = true
		b.out.Objects = append(b.out.Objects, o)
		for _, st := range b.cur.States {
			if st.Object == o.ID && !b.used["state"][st.ID] {
				b.used["state"][st.ID] = true
				b.out.States = append(b.out.States, st)
			}
		}
		b.carry(o.ID)
	}
}

// The object a copy follows, and its name.
func (b *builder) copySource(c *setupSource) (int64, string, error) {
	src, err := b.importer.flow(c.Flow)
	if err != nil {
		return 0, "", err
	}
	if src.id == b.flow {
		return 0, "", errors.New("a copy follows an object of another flow")
	}
	var source int64
	if c.Object == "" {
		if !src.isStruct {
			return 0, "", fmt.Errorf("name the object of %s it copies", quote(c.Flow))
		}
		if source, err = structRoot(b.ctx, b.tx, src.id); err != nil {
			return 0, "", err
		}
	} else {
		objs, err := b.objectsOf(src.id)
		if err != nil {
			return 0, "", err
		}
		source = objs[c.Object]
	}
	if source == 0 {
		return 0, "", setupProblem{fmt.Sprintf("%s has no object %s to copy", quote(c.Flow), quote(c.Object))}
	}
	var name string
	err = b.tx.QueryRowContext(b.ctx, "SELECT name FROM objects WHERE id = ?", source).Scan(&name)
	return source, name, err
}

// Where the board's processes and objects go when neither the file nor the
// board says: the processes in rows down from the start, as they follow one
// another, and each object beside the first process linking to it.
func (b *builder) layout(board *setupBoard, parent *int64) {
	const (
		left, top = 40.0, 140.0
		colGap    = 280.0
		rowGap    = 160.0
		objGap    = 260.0
		stackGap  = 70.0
	)
	var procs []*bproc
	for i := range board.Processes {
		p := &board.Processes[i]
		bp := b.procs[ident(p.Name, p.Key)]
		procs = append(procs, bp)
		switch {
		case p.X != nil && p.Y != nil:
			bp.x, bp.y, bp.placed = *p.X, *p.Y, true
		case bp.cur != nil:
			bp.x, bp.y, bp.placed = bp.cur.X, bp.cur.Y, true
		}
	}
	rank := map[*bproc]int{}
	var queue []*bproc
	if board.Start != nil {
		if bp := b.procs[board.Start.Next]; bp != nil && sameBoard(bp.parent, parent) {
			rank[bp] = 0
			queue = append(queue, bp)
		}
	}
	for len(queue) > 0 {
		bp := queue[0]
		queue = queue[1:]
		for _, n := range append(append([]string{}, bp.p.Next...), bp.p.OnFailure...) {
			if t := b.procs[n]; t != nil && sameBoard(t.parent, parent) {
				if _, ok := rank[t]; !ok {
					rank[t] = rank[bp] + 1
					queue = append(queue, t)
				}
			}
		}
	}
	last := 0
	for _, r := range rank {
		last = max(last, r)
	}
	cols := map[int]int{}
	bottom := top
	for _, bp := range procs {
		if !bp.placed {
			r, ok := rank[bp]
			if !ok {
				last++
				r = last
			}
			bp.x, bp.y, bp.placed = left+float64(cols[r])*colGap, top+float64(r)*rowGap, true
			cols[r]++
		}
		bottom = max(bottom, bp.y)
	}

	stacked := map[*bproc]int{}
	loose := 0
	for i := range board.Objects {
		o := &board.Objects[i]
		bo := b.objs[ident(o.Name, o.Key)]
		switch {
		case o.X != nil && o.Y != nil:
			bo.x, bo.y, bo.placed = *o.X, *o.Y, true
		case bo.cur != nil:
			bo.x, bo.y, bo.placed = bo.cur.X, bo.cur.Y, true
		}
		if !bo.placed {
			var by *bproc
			for _, bp := range procs {
				for _, l := range append(append([]setupLink{}, bp.p.Takes...), bp.p.Yields...) {
					if l.Object == ident(o.Name, o.Key) && by == nil {
						by = bp
					}
				}
			}
			if by != nil {
				bo.x, bo.y = by.x+objGap, by.y+float64(stacked[by])*stackGap
				stacked[by]++
			} else {
				bo.x, bo.y = left+float64(loose)*objGap, bottom+rowGap
				if len(procs) == 0 {
					bo.y = 40
				}
				loose++
			}
			bo.placed = true
		}
		b.layoutParts(o)
	}
	for _, bp := range procs {
		if bp.p.Board != nil {
			b.layout(bp.p.Board, &bp.id)
		}
	}
}

// Where an object's parts go on its own board when neither the file nor
// the board says: in a row.
func (b *builder) layoutParts(o *setupObject) {
	if o == nil {
		return
	}
	for i := range o.Parts {
		part := &o.Parts[i]
		bo := b.objs[ident(part.Name, part.Key)]
		switch {
		case part.X != nil && part.Y != nil:
			bo.x, bo.y = *part.X, *part.Y
		case bo.cur != nil:
			bo.x, bo.y = bo.cur.X, bo.cur.Y
		default:
			bo.x, bo.y = 40+float64(i)*260, 140
		}
		bo.placed = true
		b.layoutParts(part)
	}
}

// Fills in the board's start and end, its processes and objects, with
// their links and connections, by the ids given them.
func (b *builder) resolveBoard(board *setupBoard, parent *int64, where string) error {
	var start startNode
	var end *boardEnd
	var curStart *startNode
	var curEnd *boardEnd
	if parent == nil {
		curStart, curEnd = &b.cur.Start, b.cur.End
	} else {
		for _, ib := range b.cur.Boards {
			if ib.Process == *parent {
				curStart, curEnd = &ib.Start, &boardEnd{ib.End.X, ib.End.Y}
			}
		}
	}
	start.X, start.Y = 40, 40
	if curStart != nil {
		start.X, start.Y = curStart.X, curStart.Y
	}
	if s := board.Start; s != nil {
		if s.X != nil && s.Y != nil {
			start.X, start.Y = *s.X, *s.Y
		}
		if s.Next != "" {
			bp := b.procs[s.Next]
			if bp == nil || !sameBoard(bp.parent, parent) {
				return problemf("%s: its start goes to %s, which isn't a process on its board", where, quote(s.Next))
			}
			start.Target = &bp.id
		}
		r, err := storedSetupRoute(s.Route)
		if err != nil {
			return problemf("%s: its start's route: %v", where, err)
		}
		start.Route = r
	}
	end = curEnd
	if board.End != nil {
		end = &boardEnd{board.End.X, board.End.Y}
	}

	bottom := 40.0
	for i := range board.Processes {
		p := &board.Processes[i]
		bp := b.procs[ident(p.Name, p.Key)]
		bottom = max(bottom, bp.y)
		if err := b.resolveProcess(bp); err != nil {
			return err
		}
		if p.Board != nil {
			if err := b.resolveBoard(p.Board, &bp.id, bp.where); err != nil {
				return err
			}
		}
	}
	for i := range board.Objects {
		o := &board.Objects[i]
		if err := b.resolveObject(b.objs[ident(o.Name, o.Key)]); err != nil {
			return err
		}
	}
	if parent == nil {
		b.out.Start, b.out.End = start, end
	} else {
		if end == nil {
			end = &boardEnd{40, bottom + 160}
		}
		b.out.Boards = append(b.out.Boards, innerBoard{Process: *parent, Start: start, End: *end})
	}
	return nil
}

func (b *builder) resolveProcess(bp *bproc) error {
	p, at := bp.p, bp.where
	// Its width, like its place, stays when the file doesn't say.
	if p.Width == nil && bp.cur != nil {
		p.Width = bp.cur.Width
	}
	switch {
	case !validKind(p.Kind):
		return problemf("%s: %s", at, invalidKindMessage)
	case !validThinking(p.Thinking):
		return problemf("%s: thinking must be one of %s", at, strings.Join(thinkingLevels, ", "))
	case !validModel(p.Model):
		return problemf("%s: invalid model", at)
	case !validWidth(p.Width):
		return problemf("%s: %s", at, invalidWidthMessage)
	}
	s := processSettings{Repositories: p.Repositories, Rounds: p.Rounds, Loops: p.Loops, Timeout: p.Timeout, Retries: p.Retries, Quorum: p.Quorum, DescribeFlow: p.DescribeFlow}
	for _, m := range p.MCPServers {
		id, ok := b.mcp[mcpNameKey(m)]
		if !ok {
			return problemf("%s: there is no MCP server named %s", at, quote(m))
		}
		s.MCPServers = append(s.MCPServers, id)
	}
	for _, name := range p.Skills {
		id, ok := b.skills[strings.TrimPrefix(strings.TrimSpace(name), "/")]
		if !ok {
			return problemf("%s: there is no skill named %s; add it in the settings' skills first", at, quote(name))
		}
		if !slices.Contains(s.Skills, id) {
			s.Skills = append(s.Skills, id)
		}
	}
	for m, tools := range p.MCPTools {
		id, ok := b.mcp[mcpNameKey(m)]
		if !ok || !slices.Contains(s.MCPServers, id) {
			return problemf("%s: mcpTools names %s, which isn't one of its mcpServers", at, quote(m))
		}
		if s.MCPTools == nil {
			s.MCPTools = map[int64][]string{}
		}
		s.MCPTools[id] = append([]string{}, tools...)
	}
	if pc := p.Connector; pc != nil {
		if p.Kind != kindConnector {
			return problemf("%s: only a connector process has connector settings", at)
		}
		id, ok := b.mcp[mcpNameKey(pc.Server)]
		if !ok {
			return problemf("%s: there is no connector named %s among the mcpServers; install it there, with connector set", at, quote(pc.Server))
		}
		c := findConnector(b.mcpConnectors[id])
		if c == nil {
			return problemf("%s: %s is an MCP server, not a connector", at, quote(pc.Server))
		}
		op := c.operation(pc.Operation)
		if op == nil {
			return problemf("%s: %s has no operation %s", at, c.Title, quote(pc.Operation))
		}
		for name := range pc.Args {
			if op.param(name) == nil {
				return problemf("%s: %s takes no argument %s", at, op.Name, quote(name))
			}
		}
		s.Connector = &connectorSettings{Server: id, Operation: pc.Operation, Args: pc.Args}
	}
	if p.Runs != "" {
		if p.Kind != kindSubflow {
			return problemf("%s: only a subflow process runs a procedure", at)
		}
		fi, err := b.importer.flow(p.Runs)
		if err != nil {
			return problemf("%s: %s", at, err)
		}
		s.Flow = &fi.id
	}
	if msg := s.problem(); msg != "" {
		return problemf("%s: %s", at, msg)
	}
	b.out.Processes = append(b.out.Processes, process{ID: bp.id, Name: p.Name, Kind: p.Kind, Prompt: p.Prompt, Script: p.Script,
		Model: p.Model, Thinking: p.Thinking, X: bp.x, Y: bp.y, Width: p.Width, Parent: bp.parent, Recording: p.Recording, Settings: s})
	if err := b.resolveSamples(bp); err != nil {
		return err
	}

	for _, l := range p.Takes {
		if err := b.resolveLink(bp, l, directionInput); err != nil {
			return err
		}
	}
	for _, l := range p.Yields {
		if err := b.resolveLink(bp, l, directionOutput); err != nil {
			return err
		}
	}
	for kind, targets := range map[string][]string{connectionNext: p.Next, connectionFailure: p.OnFailure} {
		for _, t := range targets {
			tp := b.procs[t]
			if tp == nil || !sameBoard(tp.parent, bp.parent) {
				return problemf("%s: %s isn't a process on its board", at, quote(t))
			}
			b.conns = append(b.conns, connection{Source: bp.id, Target: tp.id, Kind: kind})
		}
	}
	return nil
}

// The samples the process is tried on: the file's, or without any there,
// those it has, which keep their ids.
func (b *builder) resolveSamples(bp *bproc) error {
	p, at := bp.p, bp.where
	if p.Samples == nil {
		if bp.cur != nil {
			for _, sm := range b.cur.Samples {
				if sm.Process == bp.cur.ID {
					sm.Process = bp.id
					b.out.Samples = append(b.out.Samples, sm)
				}
			}
		}
		return nil
	}
	names := map[string]bool{}
	for _, ss := range *p.Samples {
		name := strings.TrimSpace(ss.Name)
		if msg := sampleNameProblem(name); msg != "" {
			return problemf("%s: %s", at, msg)
		}
		if names[name] {
			return problemf("%s: two samples are named %s", at, quote(name))
		}
		names[name] = true
		where := fmt.Sprintf("%s: sample %s", at, quote(name))
		sm := processSample{Process: bp.id, Name: name, Inputs: sampleInputs{}}
		for _, in := range ss.Inputs {
			bo := b.objs[in.Object]
			if bo == nil || !slices.ContainsFunc(p.Takes, func(l setupLink) bool { return b.objs[l.Object] == bo }) {
				return problemf("%s: the process takes no object %s", where, quote(in.Object))
			}
			si := sampleInput{Object: bo.id, Content: in.Content}
			if in.State != "" {
				if si.State = bo.states[in.State]; si.State == 0 {
					return problemf("%s: %s has no state %s", where, quote(in.Object), quote(in.State))
				}
			}
			sm.Inputs = append(sm.Inputs, si)
		}
		b.out.Samples = append(b.out.Samples, sm)
	}
	if len(names) > maxSamples {
		return problemf("%s: a process keeps at most %d samples", at, maxSamples)
	}
	return nil
}

func (b *builder) resolveLink(bp *bproc, l setupLink, direction string) error {
	verb := map[string]string{directionInput: "takes", directionOutput: "yields"}[direction]
	at := fmt.Sprintf("%s: %s %s", bp.where, verb, quote(l.Object))
	bo := b.objs[l.Object]
	if bo == nil {
		return problemf("%s: there is no such object", at)
	}
	if bo.owner != nil {
		return problemf("%s: that object makes up another, so it isn't on a board of processes", at)
	}
	bl := blink{proc: bp, obj: bo, direction: direction, role: l.Role, condition: l.Condition, query: l.Query, where: at}
	if l.State != "" {
		if bl.state = bo.states[l.State]; bl.state == 0 {
			return problemf("%s: the object has no state %s", at, quote(l.State))
		}
	}
	for _, st := range l.States {
		id := bo.states[st]
		if id == 0 {
			return problemf("%s: the object has no state %s", at, quote(st))
		}
		bl.states = append(bl.states, id)
	}
	if l.At != nil {
		bl.at = &point{l.At.X, l.At.Y}
	}
	r, err := storedSetupRoute(l.Route)
	if err != nil {
		return problemf("%s: its route: %v", at, err)
	}
	bl.route = r
	b.links = append(b.links, bl)
	return nil
}

func (b *builder) resolveObject(bo *bobj) error {
	o, at := bo.o, bo.where
	if o.Width == nil && bo.cur != nil {
		o.Width = bo.cur.Width
	}
	if !validWidth(o.Width) {
		return problemf("%s: %s", at, invalidWidthMessage)
	}
	if msg := checkQuantity(o.Many, quantity{min: o.Min, max: o.Max}); msg != "" {
		return problemf("%s: %s", at, msg)
	}
	if o.Shared && (!b.isStruct || bo.parent != nil || bo.owner != nil) {
		return problemf("%s: only a struct's object is shared, and its copies with it", at)
	}
	if o.Tags != nil && (!b.isStruct || bo.parent != nil || bo.owner != nil) {
		return problemf("%s: only flows, procedures and structs have tags, a struct's on its object", at)
	}
	if o.Persistent && b.shared(bo) {
		return problemf("%s: it is a copy of a shared object, which every flow using it stores already; it isn't persistent too", at)
	}
	so := snapshotObject{ID: bo.id, Name: o.Name, X: bo.x, Y: bo.y, Width: o.Width, Persistent: o.Persistent, Many: o.Many,
		Min: o.Min, Max: o.Max, Human: o.Human, End: o.End, Parent: bo.parent, Owner: bo.owner, Relation: o.Relation, Source: bo.source}
	if o.Shared {
		so.Store = &bo.id
	}
	if o.Specializes != "" {
		g := b.objs[o.Specializes]
		if g == nil || g == bo {
			return problemf("%s: it specializes %s, which isn't another object of its flow", at, quote(o.Specializes))
		}
		so.General = &g.id
	}
	b.out.Objects = append(b.out.Objects, so)
	for i := range o.Parts {
		part := &o.Parts[i]
		if err := b.resolveObject(b.objs[ident(part.Name, part.Key)]); err != nil {
			return err
		}
	}
	return nil
}

// Says what is wrong with the links, as checkLink and the rest do for one.
func (b *builder) checkLinks() error {
	type key struct {
		process, object int64
		direction       string
		state           int64
	}
	seen := map[key]bool{}
	// The object each process takes, and generates, by name; see names.go.
	type named struct {
		process   int64
		direction string
		name      string
	}
	names := map[named]*bobj{}
	outputs := map[[2]int64]int{}
	consumed := map[int64]*bproc{}
	linked := map[[2]int64]bool{}
	// How many input links each process has to each object, and whether
	// one is a query, which must be the only one.
	takes, queries := map[[2]int64]int{}, map[[2]int64]bool{}
	for _, l := range b.links {
		k := [2]int64{l.proc.id, l.obj.id}
		linked[k] = true
		if l.direction == directionInput {
			takes[k]++
			queries[k] = queries[k] || l.query != nil
		}
	}
	for _, l := range b.links {
		k := key{l.proc.id, l.obj.id, l.direction, l.state}
		if seen[k] {
			return problemf("%s: it does so twice", l.where)
		}
		seen[k] = true
		n := named{l.proc.id, l.direction, inputFileName(l.obj.o.Name)}
		if other := names[n]; other != nil && other != l.obj {
			return problemf("%s: %s", l.where, nameClashMessage(l.proc.p.Name, l.direction, other.o.Name))
		}
		names[n] = l.obj
		// On the process's board, or on the board around it and linked to the
		// flow process it is in, which shows it inside.
		if !sameBoard(l.obj.parent, l.proc.parent) && (l.proc.parent == nil || !linked[[2]int64{*l.proc.parent, l.obj.id}]) {
			return problemf("%s: the object isn't on the process's board", l.where)
		}
		kind := l.proc.p.Kind
		switch l.role {
		case roleNone:
		case roleInstrument, roleConsumption:
			if l.direction != directionInput {
				return problemf("%s: only an input link has a role", l.where)
			}
			if l.obj.o.Human {
				return problemf("%s: a person carries out a process; they are neither its instrument nor consumed", l.where)
			}
		default:
			return problemf("%s: role must be empty, instrument or consumption", l.where)
		}
		if l.role == roleConsumption {
			if other := consumed[l.obj.id]; other != nil && other != l.proc {
				return problemf("%s: another process consumes the object already; only one can", l.where)
			}
			consumed[l.obj.id] = l.proc
		}
		if l.condition && (l.direction != directionInput || l.state == 0) {
			return problemf("%s: only an input link naming a state is a condition", l.where)
		}
		if msg := b.checkQuery(l, takes, queries); msg != "" {
			return problemf("%s: %s", l.where, msg)
		}
		if l.direction == directionInput {
			if kind != kindHuman && l.obj.o.Human {
				return problemf("%s: %s", l.where, agentLinkMessage)
			}
			if len(l.states) > 0 {
				return problemf("%s: %s", l.where, decisionLinkMessage)
			}
			continue
		}
		switch {
		case kind == kindHuman && l.state != 0:
			return problemf("%s: a human process links to the object and lists the states it decides between", l.where)
		case kind == kindHuman:
		case len(l.states) > 0 && kind != kindAgent:
			return problemf("%s: %s", l.where, decisionLinkMessage)
		case len(l.states) > 0 && l.state != 0:
			return problemf("%s: an agent's link to an object either names the state it generates it in or lists the states it decides between", l.where)
		default:
			k := [2]int64{l.proc.id, l.obj.id}
			if outputs[k]++; outputs[k] > 1 {
				return problemf("%s: the process already generates that object", l.where)
			}
		}
	}
	return nil
}

// Whether an object is a copy of a shared one, whose records it reads.
func (b *builder) shared(bo *bobj) bool {
	if bo.source == nil {
		return false
	}
	store, err := storeOf(b.ctx, b.tx, *bo.source)
	return err == nil && store != 0
}

// Says what is wrong with the link as to queries, as checkQuery does for
// one; a query's state isn't checked, as a copy's states come from what it
// copies.
func (b *builder) checkQuery(l blink, takes map[[2]int64]int, queries map[[2]int64]bool) string {
	k := [2]int64{l.proc.id, l.obj.id}
	switch {
	case l.query == nil && l.direction == directionInput && queries[k]:
		return "the process queries that object already; it takes it by its query alone"
	case l.query == nil:
		return ""
	case l.direction != directionInput:
		return "only an input link has a query"
	case takes[k] > 1:
		return "the process takes that object already; it can't query it too"
	case l.query.problem() != "":
		return l.query.problem()
	case l.state != 0 || l.condition || len(l.states) > 0:
		return "a query link names no state; the query says which records"
	case l.role == roleConsumption:
		return "a query reads records; it can't consume them"
	case l.proc.p.Kind != kindAgent && l.proc.p.Kind != kindScript && l.proc.p.Kind != "":
		return "only an agent or a script queries records"
	case !l.obj.o.Persistent && !b.shared(l.obj):
		return "only a persistent or shared object has records to query"
	}
	return ""
}

// Rewrites the flow's board as built: the board, then its links and
// connections, then what follows from it, as rewriteBoard does.
func (b *builder) write() error {
	if err := b.checkLinks(); err != nil {
		return err
	}
	seen := map[[2]int64]bool{}
	for _, c := range b.conns {
		k := [2]int64{c.Source, c.Target}
		if seen[k] {
			return problemf("%s: a process goes on to another twice", quote(b.name))
		}
		seen[k] = true
	}
	ctx, tx := b.ctx, b.tx
	if err := restoreSnapshot(ctx, tx, b.flow, b.out); err != nil {
		return err
	}
	for _, l := range b.links {
		// The id it had, and where it showed inside, if the board had it.
		var id any
		for _, have := range b.cur.Links {
			if have.Process == l.proc.id && have.Object == l.obj.id && have.Direction == l.direction && have.State == l.state {
				id = have.ID
				if l.at == nil {
					l.at = have.At
				}
			}
		}
		var atX, atY sql.NullFloat64
		if l.at != nil {
			atX, atY = sql.NullFloat64{Float64: l.at.X, Valid: true}, sql.NullFloat64{Float64: l.at.Y, Valid: true}
		}
		res, err := tx.ExecContext(ctx, "INSERT INTO object_links (id, process_id, object_id, direction, state_id, route, at_x, at_y, role, is_condition, query) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			id, l.proc.id, l.obj.id, l.direction, l.state, storedRoute(l.route), atX, atY, l.role, l.condition, storedQuery(l.query))
		if err != nil {
			return err
		}
		linkID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, st := range l.states {
			if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO link_states (link_id, state_id) VALUES (?, ?)", linkID, st); err != nil {
				return err
			}
		}
	}
	// In the file's order, as the board had them.
	for _, bp := range b.plist {
		for _, c := range b.conns {
			if c.Source != bp.id {
				continue
			}
			var id any
			for _, have := range b.cur.Connections {
				if have.Source == c.Source && have.Target == c.Target {
					id = have.ID
				}
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO connections (id, source_id, target_id, kind) VALUES (?, ?, ?, ?)", id, c.Source, c.Target, c.Kind); err != nil {
				return err
			}
		}
	}
	if err := syncCopies(ctx, tx, b.flow, true); err != nil {
		return err
	}
	// A subflow process the file gives no outcomes brings them in itself.
	for _, bp := range b.plist {
		if bp.p.Kind == kindSubflow && len(bp.p.Yields) == 0 {
			if err := bringOutcomes(ctx, tx, bp.id); err != nil {
				return err
			}
		}
	}
	s, err := loadStructure(ctx, tx, b.flow)
	if err != nil {
		return err
	}
	if msg := s.check(); msg != "" {
		return problemf("%s: %s", quote(b.name), msg)
	}
	objs := map[string]int64{}
	for k, bo := range b.objs {
		objs[k] = bo.id
	}
	b.objects[b.flow] = objs
	return nil
}
