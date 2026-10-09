package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// The engine executes runs in the server, so a run goes on without the page
// that started it, and a review can wait as long as it takes. Each run walks
// the board as its version has it; the steps it records are what the board
// shows of it.
//
// A process that succeeds generates its output objects, which hold what it
// printed, or for an object with attributes its part of the checked reply,
// each in the state its link names or in none. Then the processes taking
// those objects run, in parallel:
//   - A link without a state takes the object whatever state it is in. Its
//     process runs once every object it takes that way exists; objects no
//     process generates are given, so they exist from the start.
//   - A link naming a state is a condition: its process runs whenever the
//     object enters that state, and only then, with whichever objects it
//     takes without a state exist. Several such links are alternatives, not
//     all required.
//
// A failure stops its branch, and every process runs at most once per pass,
// so cycles end. An agent, script or Sentry step stopped while it goes
// fails, so stops its branch the same way; the rest of the run goes on.
// Processes still connected directly run right after their predecessor.
//
// A human process waits for a person to review its inputs and set an object
// to one of the states its output links name; the object keeps its content.
// Instructions they leave go as a change request to the processes taking the
// object in that state. Each decision starts a fresh pass, so review loops
// can repeat while cycles without a human still run once per pass. The
// objects its output links name without states are the person's to fill in,
// so a run can start from what they enter; see entries.go. Those come out
// like a process's, in the pass the human process ran in.
//
// A step generating a unique value another run stored terminates the run: no
// further process starts, and reviews still waiting end undecided.
//
// A flow process runs the board inside it, which starts from its start and
// from the objects the flow process takes, given as they are outside; the
// process its start points at takes them all, as do those linked to them. What
// the processes inside generate of the objects the flow process generates
// comes out of it. A recording flow process runs its board as a run of its
// own; the step in the run it is in refers to it, and how it ended doesn't
// end the outer run: a duplicate, or a stop, just ends that branch. Otherwise
// its processes' steps are steps of the run it is in.
//
// A subflow process runs another flow's top board the same way, always as a
// run of that flow; see calls.go. What comes out of it is stored as an agent
// step's output is, since it is made of objects of this flow.

// What a human decided on a review: the state they set, if they had any to
// choose from, and their instructions; and what they entered of the objects
// they fill in, by object id, with all of it as the step's output.
type decision struct {
	state        *int64
	instructions string
	output       string
	entries      map[int64]string
}

type engine struct {
	db        *sql.DB
	workerURL string
	sentry    sentryConfig
	mcp       *mcpBroker
	// A slot per agent, script or Sentry step that can run at once, across
	// all runs; reviews waiting take none.
	slots chan struct{}
	mu    sync.Mutex
	runs  map[int64]*liveRun
}

// A run in progress.
type liveRun struct {
	cancel context.CancelFunc
	// The reviews waiting for a decision, by step id.
	reviews map[int64]chan decision
	// What stops each agent, script or Sentry step going, by step id.
	steps map[int64]context.CancelFunc
}

func newEngine(db *sql.DB, workerURL string, sentry sentryConfig, mcp *mcpBroker, parallelSteps int) *engine {
	return &engine{db: db, workerURL: workerURL, sentry: sentry, mcp: mcp, slots: make(chan struct{}, parallelSteps), runs: map[int64]*liveRun{}}
}

// Executes a run of the whole flow, on the top board of version's board, in
// the background, and ends it once nothing in it is left to run.
// Its agents and scripts work in a workspace with the repositories, on a
// branch of the run's own, whose name starts with prefix.
func (e *engine) start(id, flow, version int64, board snapshot, repos []repository, prefix string) {
	ctx, cancel := context.WithCancel(context.Background())
	lr := e.register(id, cancel)
	go func() {
		fr := newFlowRun(flow, version, board, repos, prefix)
		w := newWalk(ctx, e, id, lr, fr, nil, nil, fr.workspace("run-"+strconv.FormatInt(id, 10)))
		w.enter()
		e.finish(ctx, id, w)
	}()
}

func (e *engine) register(id int64, cancel context.CancelFunc) *liveRun {
	lr := &liveRun{cancel: cancel, reviews: map[int64]chan decision{}, steps: map[int64]context.CancelFunc{}}
	e.mu.Lock()
	e.runs[id] = lr
	e.mu.Unlock()
	return lr
}

// Ends a run whose walk is over, and returns how it ended: a run stopped or
// terminated has ended already, and keeps how.
func (e *engine) finish(ctx context.Context, id int64, w *walk) string {
	status := runDone
	switch {
	case ctx.Err() != nil:
		status = runInterrupted
	case w.failed:
		status = runFailed
	}
	e.mu.Lock()
	lr := e.runs[id]
	delete(e.runs, id)
	e.mu.Unlock()
	if lr != nil {
		lr.cancel()
	}
	rn, err := endRun(context.Background(), e.db, id, status)
	if err != nil {
		log.Printf("end run %d: %v", id, err)
		return status
	}
	return rn.Status
}

// Cancels a run's steps still going, and its reviews still waiting.
func (e *engine) stop(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if lr, ok := e.runs[id]; ok {
		lr.cancel()
	}
}

// Cancels one step of a run, which fails it and ends its branch; the rest of
// the run goes on. False if the step isn't going.
func (e *engine) stopStep(runID, stepID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	lr, ok := e.runs[runID]
	if !ok {
		return false
	}
	cancel, ok := lr.steps[stepID]
	if ok {
		cancel()
	}
	return ok
}

// Lets a run waiting for a review go on with the human's decision.
func (e *engine) decide(runID, stepID int64, d decision) {
	e.mu.Lock()
	defer e.mu.Unlock()
	lr, ok := e.runs[runID]
	if !ok {
		return
	}
	if ch, ok := lr.reviews[stepID]; ok {
		delete(lr.reviews, stepID)
		ch <- d
	}
}

// Records a review as waiting and waits for its decision. The step and the
// wait start together, so a decision can't come in between. False if the
// run ended first, or the step couldn't be recorded.
func (e *engine) await(ctx context.Context, lr *liveRun, runID, processID int64, inputs []runInput) (int64, decision, bool) {
	ch := make(chan decision, 1)
	e.mu.Lock()
	stepID, err := startStep(ctx, e.db, runID, processID, stepWaiting, "", inputs, nil)
	if err == nil {
		lr.reviews[stepID] = ch
	}
	e.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("record review of process %d in run %d: %v", processID, runID, err)
		}
		return 0, decision{}, false
	}
	select {
	case d := <-ch:
		return stepID, d, true
	case <-ctx.Done():
		return stepID, decision{}, false
	}
}

// One end of a link: an object and the state it names, 0 for none.
type linkEnd struct {
	object, state int64
}

// An object that exists in a run: its content, state (0 for none) and the
// instructions of the human who set that state.
type value struct {
	content string
	state   int64
	review  *review
}

type review struct {
	reviewer, instructions string
}

// The processes run in one pass of a run.
type pass struct {
	mu      sync.Mutex
	visited map[int64]bool
}

func newPass() *pass { return &pass{visited: map[int64]bool{}} }

// Whether the process hadn't run in this pass yet; it has now.
func (p *pass) visit(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.visited[id] {
		return false
	}
	p.visited[id] = true
	return true
}

// What every board of a run of the whole flow walks on.
type flowRun struct {
	flow    int64
	version int64
	board   snapshot
	objects map[int64]snapshotObject
	// The flow's repositories as the run started, and its branches' prefix.
	repos  []repository
	prefix string
	// For a flow a subflow process runs: the flows it runs inside, outermost
	// first.
	callers []int64
}

func newFlowRun(flow, version int64, board snapshot, repos []repository, prefix string) *flowRun {
	fr := &flowRun{flow: flow, version: version, board: board, objects: map[int64]snapshotObject{}, repos: repos, prefix: prefix}
	for _, o := range board.Objects {
		fr.objects[o.ID] = o
	}
	return fr
}

// A workspace of its own, named id, on the branch <prefix>/id; nil when the
// flow has no repositories.
func (fr *flowRun) workspace(id string) *runWorkspace {
	if len(fr.repos) == 0 {
		return nil
	}
	return &runWorkspace{id: id, branch: fr.prefix + "/" + id, repos: fr.repos}
}

// A run walking a board: the top board, or a flow process's.
type walk struct {
	e   *engine
	ctx context.Context
	run int64
	lr  *liveRun
	fr  *flowRun
	// The flow process whose board it is, nil for the top board.
	board *int64
	// Where its agents and scripts work; nil without repositories.
	ws *runWorkspace
	// Where it starts: the process its start points at, and the objects given
	// from outside.
	target *int64
	given  []int64
	// Ends waiting reviews when the run is terminated.
	terminate context.CancelFunc
	reviews   context.Context

	kinds, names map[int64]string
	recording    map[int64]bool
	settings     map[int64]processSettings
	// Process to the objects it generates, or the states a human decides on.
	outputs map[int64][]linkEnd
	// Process to the objects it takes, and object to the processes taking it.
	inputs     map[int64][]linkEnd
	takers     map[int64][]taker
	successors map[int64][]int64
	objects    map[int64]string
	stateNames map[int64]string
	// State to the object it belongs to.
	stateObjects map[int64]int64
	generated    map[int64]bool

	mu         sync.Mutex
	values     map[int64]value
	failed     bool
	terminated bool
}

type taker struct {
	process, state int64
}

// A walk of board, nil for the top one, with the objects given from outside,
// working in ws.
func newWalk(ctx context.Context, e *engine, run int64, lr *liveRun, fr *flowRun, board *int64, given map[int64]value, ws *runWorkspace) *walk {
	w := &walk{
		e: e, ctx: ctx, run: run, lr: lr, fr: fr, board: board, ws: ws,
		kinds: map[int64]string{}, names: map[int64]string{}, recording: map[int64]bool{}, settings: map[int64]processSettings{},
		outputs: map[int64][]linkEnd{}, inputs: map[int64][]linkEnd{},
		takers: map[int64][]taker{}, successors: map[int64][]int64{},
		objects: map[int64]string{}, stateNames: map[int64]string{}, stateObjects: map[int64]int64{},
		generated: map[int64]bool{}, values: map[int64]value{},
	}
	w.reviews, w.terminate = context.WithCancel(ctx)
	s := fr.board
	on := func(parent *int64) bool {
		return (parent == nil) == (board == nil) && (parent == nil || *parent == *board)
	}
	for _, p := range s.Processes {
		if on(p.Parent) {
			w.kinds[p.ID] = p.Kind
			w.names[p.ID] = p.Name
			w.recording[p.ID] = p.Recording
			w.settings[p.ID] = p.Settings
		}
	}
	// Objects show on more boards than their own, so all are known by name.
	for _, o := range s.Objects {
		w.objects[o.ID] = o.Name
	}
	for _, st := range s.States {
		w.stateNames[st.ID] = st.Name
		w.stateObjects[st.ID] = st.Object
	}
	linkStates := map[int64][]int64{}
	for _, ls := range s.LinkStates {
		linkStates[ls.Link] = append(linkStates[ls.Link], ls.State)
	}
	for _, l := range s.Links {
		if _, ok := w.kinds[l.Process]; !ok {
			continue
		}
		if l.Direction == directionOutput {
			// A human's link stands for each state it can set.
			if states := linkStates[l.ID]; len(states) > 0 {
				for _, st := range states {
					w.outputs[l.Process] = append(w.outputs[l.Process], linkEnd{l.Object, st})
				}
			} else {
				w.outputs[l.Process] = append(w.outputs[l.Process], linkEnd{l.Object, l.State})
			}
		} else {
			w.inputs[l.Process] = append(w.inputs[l.Process], linkEnd{l.Object, l.State})
			w.takers[l.Object] = append(w.takers[l.Object], taker{l.Process, l.State})
		}
	}
	for _, c := range s.Connections {
		if _, ok := w.kinds[c.Source]; ok {
			w.successors[c.Source] = append(w.successors[c.Source], c.Target)
		}
	}
	// A human generates only what they fill in, not the objects they decide
	// on, nor those they review without states to decide between.
	for p, ends := range w.outputs {
		for _, end := range ends {
			if w.kinds[p] != kindHuman || (end.state == 0 && !slices.ContainsFunc(w.inputs[p], func(i linkEnd) bool { return i.object == end.object })) {
				w.generated[end.object] = true
			}
		}
	}
	for _, o := range s.Objects {
		if on(o.Parent) && o.Owner == nil && !w.generated[o.ID] {
			w.values[o.ID] = value{}
		}
	}
	// Objects from outside are passed on like generated ones.
	for o, v := range given {
		w.values[o] = v
		w.generated[o] = true
		w.given = append(w.given, o)
	}
	slices.Sort(w.given)

	if board == nil {
		w.target = s.Start.Target
	} else {
		for _, b := range s.Boards {
			if b.Process == *board {
				w.target = b.Start.Target
			}
		}
	}
	return w
}

// Runs the board from its start, and the processes taking the objects given
// from outside: those taking them in the state they are in, and those whose
// objects all exist then. Returns once nothing on it is left to run.
func (w *walk) enter() {
	p := newPass()
	var next []func()
	if w.target != nil {
		next = append(next, func() { w.runProcess(*w.target, p, 0) })
	}
	w.mu.Lock()
	for _, o := range w.given {
		if st := w.values[o].state; st != 0 {
			for _, t := range w.takersIn(o, st) {
				next = append(next, func() { w.runProcess(t, p, o) })
			}
		}
		for _, t := range w.takers[o] {
			if t.state == 0 && w.ready(t.process) {
				next = append(next, func() { w.runProcess(t.process, p, 0) })
			}
		}
	}
	w.mu.Unlock()
	all(next)
}

// The run is over for whatever hasn't started yet.
func (w *walk) stopped() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.terminated || w.ctx.Err() != nil
}

// Called with w.mu held.
func (w *walk) asInput(o int64) runInput {
	v := w.values[o]
	return runInput{Name: w.objects[o], State: w.stateNames[v.state], Content: v.content}
}

func (w *walk) plainInputs(p int64) []linkEnd {
	var plain []linkEnd
	for _, i := range w.inputs[p] {
		if i.state == 0 {
			plain = append(plain, i)
		}
	}
	return plain
}

// The objects given from outside lead into a flow process's start, or the
// start of the flow a subflow process runs, so the process it points at takes
// them, linked to them or not.
func (w *walk) startGiven(p int64) []int64 {
	if w.target == nil || *w.target != p {
		return nil
	}
	return w.given
}

// Whether a process runs now that objects it takes without a state exist:
// it takes some that way and all of them exist. A process with a condition
// waits for its object to enter that state instead. Called with w.mu held.
func (w *walk) ready(p int64) bool {
	plain := w.plainInputs(p)
	if len(plain) < len(w.inputs[p]) {
		return false
	}
	for _, i := range plain {
		if _, ok := w.values[i.object]; !ok {
			return false
		}
	}
	return len(plain) > 0
}

func (w *walk) takersIn(object, state int64) []int64 {
	var ps []int64
	for _, t := range w.takers[object] {
		if t.state == state {
			ps = append(ps, t.process)
		}
	}
	return ps
}

// Runs each in parallel and waits for all of them.
func all(fns []func()) {
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	wg.Wait()
}

// trigger: the object whose entering a state the process takes it in made it
// run, 0 for none.
func (w *walk) runProcess(id int64, p *pass, trigger int64) {
	if w.stopped() || !p.visit(id) {
		return
	}
	human := w.kinds[id] == kindHuman

	w.mu.Lock()
	given := []runInput{}
	taken := map[int64]bool{}
	for _, i := range w.plainInputs(id) {
		if _, ok := w.values[i.object]; ok && w.generated[i.object] {
			given = append(given, w.asInput(i.object))
			taken[i.object] = true
		}
	}
	for _, o := range w.startGiven(id) {
		if !taken[o] && o != trigger {
			given = append(given, w.asInput(o))
		}
	}
	var cr *changeRequest
	if trigger != 0 {
		if v := w.values[trigger]; v.review != nil && !human {
			cr = &changeRequest{
				Reviewer:     v.review.reviewer,
				State:        w.stateNames[v.state],
				Instructions: v.review.instructions,
				Reviewed:     []runInput{w.asInput(trigger)},
			}
		} else {
			given = append(given, w.asInput(trigger))
		}
	}
	w.mu.Unlock()

	if human {
		w.runHuman(id, p, given)
		return
	}

	var out map[int64]value
	var ok bool
	if w.kinds[id] == kindFlow || w.kinds[id] == kindSubflow {
		out, ok = w.runFlow(id)
	} else {
		out, ok = w.runStep(id, given, cr)
	}
	if !ok {
		return
	}
	objects := slices.Sorted(maps.Keys(out))
	w.mu.Lock()
	for _, o := range objects {
		w.values[o] = out[o]
	}
	if w.terminated {
		w.mu.Unlock()
		return
	}
	var next []func()
	for _, o := range objects {
		if st := out[o].state; st != 0 {
			for _, t := range w.takersIn(o, st) {
				next = append(next, func() { w.runProcess(t, p, o) })
			}
		}
		for _, t := range w.takers[o] {
			if t.state == 0 && w.ready(t.process) {
				next = append(next, func() { w.runProcess(t.process, p, 0) })
			}
		}
	}
	w.mu.Unlock()
	for _, s := range w.successors[id] {
		next = append(next, func() { w.runProcess(s, p, 0) })
	}
	all(next)
}

// Executes an agent, script or Sentry process; returns what it generated, each
// object in the state its link names. False if it failed or found a
// duplicate, which terminates the run and so still returns what it generated.
func (w *walk) runStep(id int64, given []runInput, cr *changeRequest) (map[int64]value, bool) {
	res, ok := w.execute(id, given, cr)
	if !ok {
		w.mu.Lock()
		w.failed = true
		w.mu.Unlock()
		return nil, false
	}
	out := map[int64]value{}
	for _, end := range w.outputs[id] {
		content, ok := res.Objects[end.object]
		if !ok {
			content = res.Output
		}
		out[end.object] = value{content: content, state: end.state}
	}
	if res.Duplicate != nil {
		w.mu.Lock()
		w.terminated = true
		w.mu.Unlock()
		w.terminate()
	}
	return out, true
}

// Runs a flow process's board with the objects it takes as they are here,
// and returns what the processes inside left of the objects it generates.
// An object the flow process generates in a state comes out in that state,
// unless it got one inside. False if the board didn't finish; only a
// failure inside fails this run. A flow process taking a list runs once per
// item; see runItems. A subflow process runs the flow it names that way.
func (w *walk) runFlow(id int64) (map[int64]value, bool) {
	var callee *flowRun
	if w.kinds[id] == kindSubflow {
		var msg string
		var err error
		if callee, msg, err = w.fr.call(w.ctx, w.e.db, w.settings[id].Flow); err != nil || msg != "" {
			if err != nil {
				log.Printf("run the flow of process %d in run %d: %v", id, w.run, err)
				msg = "The flow it runs couldn't start."
			}
			w.note(id, stepError, msg)
			w.fail()
			return nil, false
		}
	}
	w.mu.Lock()
	given := map[int64]value{}
	for _, i := range w.inputs[id] {
		if v, ok := w.values[i.object]; ok {
			given[i.object] = v
		}
	}
	for _, o := range w.startGiven(id) {
		given[o] = w.values[o]
	}
	w.mu.Unlock()
	var lists []int64
	for _, o := range slices.Sorted(maps.Keys(given)) {
		if isList(w.fr.objects[o], w.board) {
			lists = append(lists, o)
		}
	}
	if len(lists) > 0 {
		return w.runItems(id, given, lists, callee)
	}
	out, status, _ := w.runBoard(id, given, nil, callee)
	return out, status == runDone
}

func (w *walk) fail() {
	w.mu.Lock()
	w.failed = true
	w.mu.Unlock()
}

// One item of a list a flow process runs for.
type item struct {
	list int64
	key  string
	// Whether its run claims it, so no other run handles it; see claimItem.
	claim bool
}

// Item runs that found their item claimed end as this, leaving no trace.
const itemSkipped = "skipped"

// Runs a flow process's board once, as a step of this run, and as a run of
// its own if it records; for an item of a list, that run is the item's. For a
// subflow process, runs the top board of callee, the flow it runs, always as
// a run of that flow. Returns what came out, how the board's run ended, and
// for an item another run claimed, that run's claim.
func (w *walk) runBoard(id int64, given map[int64]value, it *item, callee *flowRun) (map[int64]value, string, *duplicate) {
	inputs := []runInput{}
	for _, o := range slices.Sorted(maps.Keys(given)) {
		v := given[o]
		inputs = append(inputs, runInput{Name: w.objects[o], State: w.stateNames[v.state], Content: v.content})
	}
	db := w.e.db
	stepID, err := startStep(w.ctx, db, w.run, id, stepActive, "", inputs, nil)
	if err != nil {
		if w.ctx.Err() == nil {
			log.Printf("record process %d in run %d: %v", id, w.run, err)
		}
		w.fail()
		return nil, runFailed, nil
	}

	run, lr, ctx := w.run, w.lr, w.reviews
	recording := w.recording[id] || callee != nil
	if recording {
		key := ""
		if it != nil {
			key = it.key
		}
		// A flow process's run is on its board; another flow's on its top board.
		fr, board := w.fr, &id
		if callee != nil {
			fr, board = callee, nil
		}
		if run, err = startChildRun(w.ctx, db, fr.flow, fr.version, w.run, board, w.names[id], key, stepID); err != nil {
			log.Printf("start run of process %d in run %d: %v", id, w.run, err)
			w.endStep(stepID, stepError, "Its run couldn't start.")
			w.fail()
			return nil, runFailed, nil
		}
		if it != nil && it.claim {
			dup, err := claimItem(w.ctx, db, run, stepID, it.list, w.objects[it.list], given[it.list].content)
			if err != nil || dup != nil {
				// Another run handles the item; this one never started.
				if _, err := db.ExecContext(context.Background(), "DELETE FROM runs WHERE id = ?", run); err != nil {
					log.Printf("drop run %d: %v", run, err)
				}
				if _, err := db.ExecContext(context.Background(), "DELETE FROM run_steps WHERE id = ?", stepID); err != nil {
					log.Printf("drop step %d: %v", stepID, err)
				}
				if err != nil {
					log.Printf("claim item %s in run %d: %v", it.key, run, err)
					w.fail()
					return nil, runFailed, nil
				}
				return nil, itemSkipped, dup
			}
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(w.reviews)
		lr = w.e.register(run, cancel)
	}
	// An item works on a branch of its own, so items running at once don't
	// meet; otherwise the board goes on with this run's.
	ws := w.ws
	if it != nil {
		name := fmt.Sprintf("run-%d-step-%d", w.run, stepID)
		if recording {
			name = "run-" + strconv.FormatInt(run, 10)
		}
		ws = w.fr.workspace(name)
	}
	// Another flow works on its own repositories, in a workspace of its run's.
	var inner *walk
	if callee != nil {
		inner = newWalk(ctx, w.e, run, lr, callee, nil, w.toCallee(callee, given), callee.workspace("run-"+strconv.FormatInt(run, 10)))
	} else {
		inner = newWalk(ctx, w.e, run, lr, w.fr, &id, given, ws)
	}
	inner.enter()

	var status string
	if recording {
		status = w.e.finish(ctx, run, inner)
	} else {
		switch {
		case inner.terminated:
			status = runTerminated
		case ctx.Err() != nil:
			status = runInterrupted
		case inner.failed:
			status = runFailed
		default:
			status = runDone
		}
	}
	which := "A process inside"
	if recording {
		which = "Run #" + strconv.FormatInt(run, 10)
	}

	switch status {
	case runDone:
	case runTerminated:
		w.endStep(stepID, stepDuplicate, which+" found what it generated already processed.")
		// Inside this run, the duplicate terminated it.
		if !recording {
			w.mu.Lock()
			w.terminated = true
			w.mu.Unlock()
			w.terminate()
		}
		return nil, status, nil
	case runInterrupted:
		w.endStep(stepID, stepError, which+" was stopped.")
		return nil, status, nil
	default:
		w.endStep(stepID, stepError, which+" failed.")
		w.fail()
		return nil, status, nil
	}

	var out map[int64]value
	if callee != nil {
		out = w.fromCallee(id, callee, inner)
	} else {
		out = map[int64]value{}
		inner.mu.Lock()
		for _, end := range w.outputs[id] {
			v, ok := inner.values[end.object]
			if !ok || !inner.generated[end.object] {
				continue
			}
			if v.state == 0 {
				v.state = end.state
			}
			out[end.object] = v
		}
		inner.mu.Unlock()
	}
	contents := map[int64]string{}
	for o, v := range out {
		contents[o] = v.content
	}
	// Only another flow's objects are stored here; a flow process's processes
	// stored theirs. One another run stored terminates this run.
	dup, err := finishStep(context.Background(), db, stepID, stepDone, "", "", contents)
	if err != nil {
		log.Printf("record step %d: %v", stepID, err)
		w.fail()
		return nil, runFailed, nil
	}
	if dup != nil {
		w.mu.Lock()
		w.terminated = true
		w.mu.Unlock()
		w.terminate()
	}
	// The run keeps the state each object came out in, and only the objects
	// that came out.
	for o := range contents {
		if _, err := db.ExecContext(context.Background(), "UPDATE run_objects SET state = ? WHERE step_id = ? AND object_id = ?",
			w.stateNames[out[o].state], stepID, o); err != nil {
			log.Printf("record step %d: %v", stepID, err)
		}
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM run_objects WHERE step_id = ?"+notIn(len(contents)),
		append([]any{stepID}, int64Args(slices.Collect(maps.Keys(contents)))...)...); err != nil {
		log.Printf("record step %d: %v", stepID, err)
	}
	return out, runDone, nil
}

// Runs a flow process once per item of the list it takes, all at once; each
// item's run goes on whatever the others do. Items another run claimed are
// skipped, and one step says which. What comes out are lists: each list the
// flow process generates holds what each item's run left of it, in the
// order of the items. Objects that aren't lists don't come out.
func (w *walk) runItems(id int64, given map[int64]value, lists []int64, callee *flowRun) (map[int64]value, bool) {
	note := func(status, msg string) { w.note(id, status, msg) }
	if len(lists) > 1 {
		names := make([]string, len(lists))
		for i, o := range lists {
			names[i] = strconv.Quote(w.objects[o])
		}
		note(stepError, "It takes the lists "+strings.Join(names, " and ")+"; it can run once per item of only one.")
		w.fail()
		return nil, false
	}
	list := lists[0]
	items, err := listItems(given[list].content)
	if err != nil {
		note(stepError, strconv.Quote(w.objects[list])+" holds no list: "+err.Error()+".")
		w.fail()
		return nil, false
	}
	dropped := 0
	if len(items) > maxItems {
		dropped = len(items) - maxItems
		items = items[:maxItems]
	}
	claim := (w.recording[id] || callee != nil) && w.fr.objects[list].Persistent

	type result struct {
		out    map[int64]value
		status string
		dup    *duplicate
		key    string
	}
	results := make([]result, len(items))
	runs := make([]func(), len(items))
	for i, content := range items {
		key, err := itemKey(w.ctx, w.e.db, list, content, i)
		if err != nil {
			log.Printf("key of item %d of object %d: %v", i+1, list, err)
		}
		results[i].key = key
		g := maps.Clone(given)
		g[list] = value{content: content}
		runs[i] = func() {
			results[i].out, results[i].status, results[i].dup = w.runBoard(id, g, &item{list: list, key: key, claim: claim}, callee)
		}
	}
	all(runs)

	var skipped []string
	for _, r := range results {
		if r.status == itemSkipped {
			skipped = append(skipped, fmt.Sprintf("%s (run #%d)", r.key, r.dup.Run))
		}
	}
	var msgs []string
	if len(skipped) > 0 {
		msgs = append(msgs, fmt.Sprintf("%d of %d items of %q were already taken by other runs: %s.",
			len(skipped), len(items), w.objects[list], strings.Join(skipped, ", ")))
	}
	if dropped > 0 {
		msgs = append(msgs, fmt.Sprintf("Only the first %d of %d items ran.", maxItems, maxItems+dropped))
	}
	if len(msgs) > 0 {
		note(stepDuplicate, strings.Join(msgs, " "))
	}

	out := map[int64]value{}
	for _, end := range w.outputs[id] {
		if !isList(w.fr.objects[end.object], w.board) {
			continue
		}
		var parts []string
		for _, r := range results {
			if v, ok := r.out[end.object]; ok {
				parts = append(parts, asItem(v.content))
			}
		}
		out[end.object] = value{content: indentJSON("[" + strings.Join(parts, ",") + "]"), state: end.state}
	}
	return out, true
}

// " AND object_id NOT IN (...)" for n ids, nothing for none.
func notIn(n int) string {
	if n == 0 {
		return ""
	}
	return " AND object_id NOT IN " + placeholders(n)
}

// Records a step of a process that ran nothing, saying why.
func (w *walk) note(id int64, status, msg string) {
	stepID, err := startStep(w.ctx, w.e.db, w.run, id, stepActive, "", []runInput{}, nil)
	if err != nil {
		log.Printf("record process %d in run %d: %v", id, w.run, err)
		return
	}
	w.endStep(stepID, status, msg)
}

// Ends a step that generated nothing.
func (w *walk) endStep(stepID int64, status, msg string) {
	if _, err := finishStep(context.Background(), w.e.db, stepID, status, "", msg, nil); err != nil {
		log.Printf("record step %d: %v", stepID, err)
	}
}

// Starts the run of a recording flow process, or of the flow a subflow
// process runs, on version of flow, and refers its step in the outer run to
// it. board is the flow process whose board it runs, nil for the top board.
func startChildRun(ctx context.Context, db *sql.DB, flow, version, parent int64, board *int64, name, key string, stepID int64) (int64, error) {
	res, err := db.ExecContext(ctx,
		`INSERT INTO runs (flow_id, status, version_id, parent_run_id, process_ref, process_name, item_key)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		flow, runRunning, version, parent, board, name, key)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = db.ExecContext(ctx, "UPDATE run_steps SET child_run_id = ? WHERE id = ?", id, stepID)
	return id, err
}

// Waits for the human's decision. What they entered goes on in pass p, to the
// processes taking it; then a fresh pass starts from the object they set, for
// the processes taking it in that state.
func (w *walk) runHuman(id int64, p *pass, given []runInput) {
	stepID, d, ok := w.e.await(w.reviews, w.lr, w.run, id, given)
	if !ok {
		return
	}
	var next []func()
	if d.entries != nil {
		next = w.enterValues(stepID, p, d)
	}
	if d.state != nil {
		next = append(next, w.setState(id, stepID, d)...)
	}
	all(next)
}

// Records what the human entered, and returns the processes that can run
// with it. A unique value another run stored terminates the run, as a step
// generating it would.
func (w *walk) enterValues(stepID int64, p *pass, d decision) []func() {
	dup, err := finishStep(context.Background(), w.e.db, stepID, stepDone, d.output, "", d.entries)
	if err != nil {
		log.Printf("record entries of step %d: %v", stepID, err)
		w.mu.Lock()
		w.failed = true
		w.mu.Unlock()
		return nil
	}
	if dup != nil {
		w.mu.Lock()
		w.terminated = true
		w.mu.Unlock()
		w.terminate()
		return nil
	}
	if w.stopped() {
		return nil
	}
	objects := slices.Sorted(maps.Keys(d.entries))
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, o := range objects {
		w.values[o] = value{content: d.entries[o]}
	}
	var next []func()
	for _, o := range objects {
		for _, t := range w.takers[o] {
			if t.state == 0 && w.ready(t.process) {
				next = append(next, func() { w.runProcess(t.process, p, 0) })
			}
		}
	}
	return next
}

// Sets the object the human decided on to the state they set, and returns the
// processes taking it in that state, each in a fresh pass.
func (w *walk) setState(id, stepID int64, d decision) []func() {
	object, ok := w.stateObjects[*d.state]
	if !ok {
		return nil
	}
	w.mu.Lock()
	content := w.values[object].content
	w.mu.Unlock()
	if err := recordDecision(context.Background(), w.e.db, stepID, *d.state, content); err != nil {
		log.Printf("record decision of step %d: %v", stepID, err)
	}
	if w.stopped() {
		return nil
	}
	w.mu.Lock()
	v := value{content: content, state: *d.state}
	if d.instructions != "" {
		v.review = &review{reviewer: w.names[id], instructions: d.instructions}
	}
	w.values[object] = v
	w.mu.Unlock()

	var next []func()
	for _, t := range w.takersIn(object, *d.state) {
		next = append(next, func() { w.runProcess(t, newPass(), object) })
	}
	return next
}

// Executes an agent, script or Sentry process as a step of the run, once a
// slot is free; the step can be stopped on its own while it goes. False if
// it failed or was stopped; a duplicate is a result, and terminates the run.
func (w *walk) execute(id int64, inputs []runInput, cr *changeRequest) (runResult, bool) {
	select {
	case w.e.slots <- struct{}{}:
	case <-w.ctx.Done():
		return runResult{}, false
	}
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	var stepID int64
	started := func(id int64) {
		w.e.mu.Lock()
		stepID = id
		w.lr.steps[id] = cancel
		w.e.mu.Unlock()
	}
	defer func() {
		w.e.mu.Lock()
		delete(w.lr.steps, stepID)
		w.e.mu.Unlock()
	}()
	var res runResult
	var err error
	switch w.kinds[id] {
	case kindAgent:
		res, err = runAgentStep(ctx, w.e.db, w.e.workerURL, w.e.mcp, w.ws, w.run, id, inputs, cr, started)
	case kindSentry:
		res, err = runSentryStep(ctx, w.e.db, w.e.sentry, w.run, id, inputs, started)
	default:
		res, err = runScriptStep(ctx, w.e.db, w.e.workerURL, w.ws, w.run, id, inputs, started)
	}
	<-w.e.slots
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("run process %d in run %d: %v", id, w.run, err)
		}
		return res, false
	}
	return res, res.OK || res.Duplicate != nil
}
