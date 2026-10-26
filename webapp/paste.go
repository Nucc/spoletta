package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
)

// Processes and objects copied from a board can be pasted onto another
// board, of the same flow or of another. Copying only remembers what was
// selected; pasting copies it as it is by then, with what it takes along: a
// flow process's board with all on it, an object's states, attributes and
// the objects making it up, the links, decisions and connections between
// what is copied, and the samples its processes are tried on. Links to
// anything left behind are left behind too, as is what samples give it.
//
// The copies are new rows of their own. A copy of a module's object stays a
// copy of it, following it, unless it is pasted into that module. An object
// keeps the object it specializes when that is copied along, or still in the
// same flow; pasted into another flow, it takes over what it inherited
// instead, as a module's copies do.

type pasteRequest struct {
	// The flow copied from, and what was selected on one of its boards.
	From      int64   `json:"from"`
	Processes []int64 `json:"processes"`
	Objects   []int64 `json:"objects"`
	// The board pasted onto: a flow process's, or with none the flow's top board.
	Flow  int64  `json:"flow"`
	Board *int64 `json:"board"`
	// How far what was selected moves on its way there.
	DX float64 `json:"dx"`
	DY float64 `json:"dy"`
}

// The copies of what was selected.
type pasteResult struct {
	Processes []int64 `json:"processes"`
	Objects   []int64 `json:"objects"`
}

// How deep objects can make each other up before pasting gives up on them.
const maxPasteDepth = 64

func registerPasteRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("POST /api/paste", func(w http.ResponseWriter, r *http.Request) {
		var req pasteRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		msg, err := checkParent(ctx, db, req.Board)
		if err == nil && msg == "" {
			req.Flow, msg, err = flowFor(ctx, db, req.Flow, req.Board)
		}
		if err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if refuseInStruct(w, r, db, req.Flow) {
			return
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		res, msg, err := paste(ctx, db, tx, req)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, res)
	})
}

// A paste under way: the board copied from, as a version would keep it, and
// the ids of the copies by the ids of what they copy.
type paster struct {
	ctx      context.Context
	db       *sql.DB
	tx       *sql.Tx
	req      pasteRequest
	procs    map[int64]process
	objs     map[int64]snapshotObject
	states   map[int64][]objectState
	attrs    map[int64][]attribute
	parts    map[int64][]snapshotObject
	selProcs map[int64]bool
	selObjs  map[int64]bool
	procMap  map[int64]int64
	objMap   map[int64]int64
	stateMap map[int64]int64
	// The copies that specialize an object, by the object they copy's general.
	generals [][2]int64
}

// Pastes what req says, in tx. Says what is wrong, if anything.
func paste(ctx context.Context, db *sql.DB, tx *sql.Tx, req pasteRequest) (pasteResult, string, error) {
	res := pasteResult{Processes: []int64{}, Objects: []int64{}}
	s, err := takeSnapshot(ctx, tx, req.From)
	if errors.Is(err, sql.ErrNoRows) {
		return res, "the flow copied from is gone", nil
	}
	if err != nil {
		return res, "", err
	}
	p := &paster{
		ctx: ctx, db: db, tx: tx, req: req,
		procs: map[int64]process{}, objs: map[int64]snapshotObject{},
		states: map[int64][]objectState{}, attrs: map[int64][]attribute{}, parts: map[int64][]snapshotObject{},
		selProcs: map[int64]bool{}, selObjs: map[int64]bool{},
		procMap: map[int64]int64{}, objMap: map[int64]int64{}, stateMap: map[int64]int64{},
	}
	for _, pr := range s.Processes {
		p.procs[pr.ID] = pr
	}
	for _, o := range s.Objects {
		p.objs[o.ID] = o
		if o.Owner != nil {
			p.parts[*o.Owner] = append(p.parts[*o.Owner], o)
		}
	}
	for _, st := range s.States {
		p.states[st.Object] = append(p.states[st.Object], st)
	}
	for _, a := range s.Attributes {
		p.attrs[a.Object] = append(p.attrs[a.Object], a)
	}
	// What is gone since it was copied is left out.
	for _, id := range req.Processes {
		if _, ok := p.procs[id]; ok {
			p.selProcs[id] = true
		}
	}
	for _, id := range req.Objects {
		if _, ok := p.objs[id]; ok {
			p.selObjs[id] = true
		}
	}

	// The selection's own processes and objects, apart from what they take
	// along, must share a board, as their links do.
	// By flow process, 0 for the top board.
	boards := map[int64]bool{}
	onBoard := func(parent *int64) {
		var b int64
		if parent != nil {
			b = *parent
		}
		boards[b] = true
	}
	var topProcs []int64
	for _, pr := range s.Processes {
		if p.selProcs[pr.ID] && !p.takenAlong(pr.Parent) {
			topProcs = append(topProcs, pr.ID)
			onBoard(pr.Parent)
		}
	}
	var topObjs []int64
	for _, o := range s.Objects {
		if p.selObjs[o.ID] && p.objectRoot(o) {
			topObjs = append(topObjs, o.ID)
			if o.Owner == nil {
				onBoard(o.Parent)
			}
		}
	}
	if len(topProcs) == 0 && len(topObjs) == 0 {
		return res, "nothing to paste: what was copied is gone", nil
	}
	if len(boards) > 1 {
		return res, "what is copied must be on the same board", nil
	}

	var ps []process
	for _, pr := range s.Processes {
		if p.processCopied(pr.ID) {
			ps = append(ps, pr)
		}
	}
	for _, pr := range parentsFirst(ps) {
		if msg, err := p.copyProcess(pr); err != nil || msg != "" {
			return res, msg, err
		}
	}
	for _, b := range s.Boards {
		id, ok := p.procMap[b.Process]
		if !ok {
			continue
		}
		var target *int64
		if b.Start.Target != nil {
			if t, ok := p.procMap[*b.Start.Target]; ok {
				target = &t
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO inner_boards (process_id, x, y, target_id, route, end_x, end_y) VALUES (?, ?, ?, ?, ?, ?, ?)",
			id, b.Start.X, b.Start.Y, target, storedRoute(b.Start.Route), b.End.X, b.End.Y)
		if err != nil {
			return res, "", err
		}
	}
	// A flow process from before boards were kept still needs one.
	for _, pr := range ps {
		if pr.Kind == kindFlow {
			if err := ensureBoard(ctx, tx, p.procMap[pr.ID]); err != nil {
				return res, "", err
			}
		}
	}

	// Each object copied with those making it up: the ones selected, and the
	// ones on the boards of the flow processes copied.
	for _, o := range s.Objects {
		if !p.objectCopied(o) || (o.Owner != nil && p.objectCopied(p.objs[*o.Owner])) {
			continue
		}
		top := p.selObjs[o.ID] && p.objectRoot(o)
		parent := p.req.Board
		if !top {
			mapped := p.procMap[*o.Parent]
			parent = &mapped
		}
		if _, err := p.copyObject(o, parent, nil, top, true, 0); err != nil {
			return res, "", err
		}
	}
	for _, g := range p.generals {
		general := g[1]
		if c, ok := p.objMap[general]; ok {
			general = c
		} else if req.From != req.Flow {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE objects SET general_id = ? WHERE id = ?", general, g[0]); err != nil {
			return res, "", err
		}
	}

	links := map[int64]int64{}
	for _, l := range s.Links {
		process, ok := p.procMap[l.Process]
		object, ok2 := p.objMap[l.Object]
		if !ok || !ok2 {
			continue
		}
		state, ok := p.stateMap[l.State]
		if l.State == 0 {
			state, ok = 0, true
		}
		if !ok {
			continue
		}
		// A link drawn on the board pasted onto moves with what it links,
		// so its route runs the default way there.
		route := storedRoute(l.Route)
		if !p.takenAlong(p.procs[l.Process].Parent) {
			route = nil
		}
		var atX, atY sql.NullFloat64
		if l.At != nil {
			atX, atY = sql.NullFloat64{Float64: l.At.X, Valid: true}, sql.NullFloat64{Float64: l.At.Y, Valid: true}
		}
		// Only one process consumes an object; a copy consuming one that is
		// consumed already takes it instead.
		role := l.Role
		if role == roleConsumption {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_links WHERE object_id = ? AND role = ?", object, roleConsumption).Scan(&n); err != nil {
				return res, "", err
			}
			if n > 0 {
				role = roleNone
			}
		}
		r, err := tx.ExecContext(ctx, "INSERT INTO object_links (process_id, object_id, direction, state_id, route, at_x, at_y, role, is_condition, query) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			process, object, l.Direction, state, route, atX, atY, role, l.Condition && state != 0, storedQuery(l.Query))
		if err != nil {
			return res, "", err
		}
		if links[l.ID], err = r.LastInsertId(); err != nil {
			return res, "", err
		}
	}
	for _, ls := range s.LinkStates {
		link, ok := links[ls.Link]
		state, ok2 := p.stateMap[ls.State]
		if ok && ok2 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO link_states (link_id, state_id) VALUES (?, ?)", link, state); err != nil {
				return res, "", err
			}
		}
	}
	for _, c := range s.Connections {
		source, ok := p.procMap[c.Source]
		target, ok2 := p.procMap[c.Target]
		if ok && ok2 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO connections (source_id, target_id, kind) VALUES (?, ?, ?)", source, target, c.Kind); err != nil {
				return res, "", err
			}
		}
	}

	// The samples of the processes copied go along, with what they give the
	// objects copied along; one giving none of those any more stays behind.
	for _, sm := range s.Samples {
		process, ok := p.procMap[sm.Process]
		if !ok {
			continue
		}
		inputs := sampleInputs{}
		for _, in := range sm.Inputs {
			if object, ok := p.objMap[in.Object]; ok {
				inputs = append(inputs, sampleInput{Object: object, State: p.stateMap[in.State], Content: in.Content})
			}
		}
		if len(inputs) == 0 && len(sm.Inputs) > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO process_samples (process_id, name, inputs) VALUES (?, ?, ?)", process, sm.Name, inputs); err != nil {
			return res, "", err
		}
	}

	// A subflow process copied brings the outcomes of the flow it runs, those
	// it generates that weren't copied along.
	for _, pr := range ps {
		if pr.Kind == kindSubflow {
			if err := bringOutcomes(ctx, tx, p.procMap[pr.ID]); err != nil {
				return res, "", err
			}
		}
	}
	st, err := loadStructure(ctx, tx, req.Flow)
	if err != nil {
		return res, "", err
	}
	if msg := st.check(); msg != "" {
		return res, msg, nil
	}
	// Objects leading into the end are new outcomes of a module, and copies
	// of a module's objects follow it.
	if err := syncCopies(ctx, tx, req.Flow, true); err != nil {
		return res, "", err
	}

	for _, id := range topProcs {
		res.Processes = append(res.Processes, p.procMap[id])
	}
	for _, id := range topObjs {
		res.Objects = append(res.Objects, p.objMap[id])
	}
	return res, "", nil
}

// Whether the board of parent is copied along with the flow process it is.
func (p *paster) takenAlong(parent *int64) bool {
	return parent != nil && p.processCopied(*parent)
}

// Whether a process is copied: selected, or on the board of a flow process
// that is.
func (p *paster) processCopied(id int64) bool {
	for range maxPasteDepth {
		if p.selProcs[id] {
			return true
		}
		pr, ok := p.procs[id]
		if !ok || pr.Parent == nil {
			return false
		}
		id = *pr.Parent
	}
	return false
}

// Whether an object is copied: selected, making up one that is, or on the
// board of a flow process that is.
func (p *paster) objectCopied(o snapshotObject) bool {
	for range maxPasteDepth {
		if p.selObjs[o.ID] {
			return true
		}
		if o.Owner == nil {
			return p.takenAlong(o.Parent)
		}
		owner, ok := p.objs[*o.Owner]
		if !ok {
			return false
		}
		o = owner
	}
	return false
}

// Whether a selected object is copied for itself, rather than along with
// what it makes up or the board it is on.
func (p *paster) objectRoot(o snapshotObject) bool {
	if o.Owner != nil {
		owner, ok := p.objs[*o.Owner]
		return !ok || !p.objectCopied(owner)
	}
	return !p.takenAlong(o.Parent)
}

// Copies a process onto the board pasted onto, or onto its flow process's
// copy, after that.
func (p *paster) copyProcess(pr process) (string, error) {
	parent := p.req.Board
	x, y := pr.X, pr.Y
	if p.takenAlong(pr.Parent) {
		mapped := p.procMap[*pr.Parent]
		parent = &mapped
	} else {
		x, y = x+p.req.DX, y+p.req.DY
	}
	if msg, err := checkCall(p.ctx, p.db, p.req.Flow, pr.Kind, pr.Settings); err != nil || msg != "" {
		return msg, err
	}
	r, err := p.tx.ExecContext(p.ctx, `INSERT INTO processes (flow_id, name, kind, prompt, script, model, thinking, x, y, width, parent_id, recording, settings)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.req.Flow, pr.Name, pr.Kind, pr.Prompt, pr.Script, pr.Model, pr.Thinking, x, y, pr.Width, parent, pr.Recording, pr.Settings)
	if err != nil {
		return "", err
	}
	id, err := r.LastInsertId()
	p.procMap[pr.ID] = id
	return "", err
}

// Copies an object on parent's board, or making up owner, with its states,
// attributes and the objects making it up. One copied for itself moves as
// the selection does; mapped says links to the object go to the copy.
// Returns the copy's id.
func (p *paster) copyObject(o snapshotObject, parent, owner *int64, top, mapped bool, depth int) (int64, error) {
	if depth > maxPasteDepth {
		return 0, errors.New("objects made up of each other too deeply to paste")
	}
	x, y := o.X, o.Y
	if top {
		x, y = x+p.req.DX, y+p.req.DY
	}
	relation, end, unique := o.Relation, o.End, true
	if owner == nil {
		relation = ""
		end = end && parent == nil
	} else {
		parent, end, unique = nil, false, false
		if relation == "" {
			relation = relationExhibition
		}
	}
	// A copy of the module's own object would follow itself.
	var source *int64
	if o.Source != nil {
		module, err := flowOfObject(p.ctx, p.tx, *o.Source)
		if err != nil {
			return 0, err
		}
		if module != 0 && module != p.req.Flow {
			source = o.Source
		}
	}
	var sourceOf = func(s *int64) *int64 {
		if source == nil {
			return nil
		}
		return s
	}
	// Another flow can't hold the general, unless it is copied along.
	var inherited []int64
	if o.General != nil {
		if g, ok := p.objs[*o.General]; ok && !p.objectCopied(g) && p.req.From != p.req.Flow {
			inherited = p.generalChain(*o.General)
		}
	}

	r, err := p.tx.ExecContext(p.ctx, `INSERT INTO objects (flow_id, name, x, y, width, persistent, many, min_items, max_items, human, at_end, parent_id, owner_id, relation, source_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.req.Flow, o.Name, x, y, o.Width, o.Persistent && owner == nil, o.Many, o.Min, o.Max, o.Human && owner == nil, end, parent, owner, relation, source)
	if err != nil {
		return 0, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return 0, err
	}
	if mapped {
		p.objMap[o.ID] = id
	}
	if o.General != nil && inherited == nil {
		p.generals = append(p.generals, [2]int64{id, *o.General})
	}

	for _, st := range p.states[o.ID] {
		r, err := p.tx.ExecContext(p.ctx, "INSERT INTO object_states (object_id, name, rule, source_id) VALUES (?, ?, ?, ?)", id, st.Name, st.Rule, sourceOf(st.Source))
		if err != nil {
			return 0, err
		}
		state, err := r.LastInsertId()
		if err != nil {
			return 0, err
		}
		if mapped {
			p.stateMap[st.ID] = state
		}
	}
	// What it inherits comes first, as it does in what it holds.
	var attrs []attribute
	for _, g := range inherited {
		attrs = append(attrs, p.attrs[g]...)
	}
	attrs = append(attrs, p.attrs[o.ID]...)
	for _, a := range attrs {
		_, err := p.tx.ExecContext(p.ctx, "INSERT INTO object_attributes (object_id, name, definition, is_unique, is_file, source_id) VALUES (?, ?, ?, ?, ?, ?)",
			id, a.Name, a.Definition, a.Unique && unique, a.File, sourceOf(a.Source))
		if err != nil {
			return 0, err
		}
	}
	for _, g := range inherited {
		for _, part := range p.parts[g] {
			if _, err := p.copyObject(part, nil, &id, false, false, depth+1); err != nil {
				return 0, err
			}
		}
	}
	for _, part := range p.parts[o.ID] {
		if _, err := p.copyObject(part, nil, &id, false, mapped, depth+1); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// An object and the ones it specializes in turn, the most general first.
func (p *paster) generalChain(id int64) []int64 {
	var chain []int64
	seen := map[int64]bool{}
	for o, ok := p.objs[id]; ok && !seen[o.ID]; o, ok = p.objs[*o.General] {
		seen[o.ID] = true
		chain = append([]int64{o.ID}, chain...)
		if o.General == nil {
			break
		}
	}
	return chain
}
