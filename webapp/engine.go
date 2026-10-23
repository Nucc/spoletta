package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
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
// An object an agent, script, Sentry or connector step, or a person, generates in no
// state comes out in the first of its states whose rule holds of it, if
// any; see rules.go. That state goes on as any other, in the same pass.
//
// A condition link takes an object, as one without a state does, but lets
// its process run only if the object is in the state it names, or one of
// those its process's condition links to it name; otherwise the process is
// skipped once it is ready to run. A skipped process generates nothing:
// what it would have is settled as absent, so the processes taking it go on
// without it, and are skipped in turn if they would get nothing at all. A
// process with a quorum runs once that many of the objects it takes without
// waiting for a state exist, and hold their conditions, rather than all of
// them; it is skipped once too few can.
//
// An input link's role says how the process takes the object: an
// instrument goes into an agent's prompt as a reference not to change, and
// an object the process consumes is gone once it takes it, so no process
// waiting for it later runs on it, until it is generated again.
//
// An agent deciding the state of an object starts a fresh pass from it, as
// a human's decision does, so an agent reviewing what another generated can
// send it back to be revised, round after round. Its rounds setting bounds
// that: once it has run that many times, a decision sending the object back
// to a process that ran already fails it instead, which ends its branch.
//
// A failure stops its branch, and every process runs at most once per pass,
// so cycles end. An agent, script, Sentry or connector step stopped while it goes
// fails, so stops its branch the same way; the rest of the run goes on. Such
// a step can have a timeout, after which it fails, and retries, each a step
// of its own, before it fails for good; a stopped one isn't retried. A
// failure connection, OPM's exception link, handles a failure instead: the
// failed process's branch ends, but the run doesn't fail, and the processes
// it leads to run in a fresh pass, taking what the failed one took and what
// went wrong. A step stopped by a person or with its run isn't handled.
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
// A persistent object that only the processes taking it generate, such as a
// cursor its fetching process moves on, is carried over: a run of the flow
// starts with what its last run to end done stored of it, as an object no
// process generates exists from the start; see carry in instances.go.
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
	// A slot per agent, script, Sentry or connector step that can run at once, across
	// all runs; reviews waiting take none.
	slots chan struct{}
	mu    sync.Mutex
	runs  map[int64]*liveRun
	// How many steps each person's runs have going, and what is closed, and
	// made again, as one ends; see personSlot.
	personSteps map[int64]int64
	stepEnded   chan struct{}
}

// A run in progress.
type liveRun struct {
	cancel context.CancelFunc
	// Why it was stopped as failed, rather than by a person: it spent what
	// it may; see fail. The run's context ends with it as its cause, which
	// its steps going then fail with; a subflow's or item's run has none of
	// its own, as only the run at their top is failed.
	failure  string
	failWith context.CancelCauseFunc
	// The reviews waiting for a decision, by step id.
	reviews map[int64]chan decision
	// What stops each agent, script, Sentry or connector step going, by step id.
	steps map[int64]context.CancelFunc
}

func newEngine(db *sql.DB, workerURL string, sentry sentryConfig, mcp *mcpBroker, parallelSteps int) *engine {
	return &engine{db: db, workerURL: workerURL, sentry: sentry, mcp: mcp, slots: make(chan struct{}, parallelSteps), runs: map[int64]*liveRun{},
		personSteps: map[int64]int64{}, stepEnded: make(chan struct{})}
}

// Executes a run of the whole flow, on the top board of version's board,
// given what came from outside, if anything (see api.go), in the background,
// and ends it once nothing in it is left to run.
// Its agents and scripts work in a workspace with the repositories, on a
// branch of the run's own, whose name starts with prefix.
func (e *engine) start(id, flow, version int64, board snapshot, repos []repository, prefix string, given map[int64]value) {
	ctx, cancel := context.WithCancelCause(context.Background())
	lr := e.register(id, func() { cancel(nil) })
	lr.failWith = cancel
	go func() {
		logRunStarted(e.db, id)
		fr := newFlowRun(flow, version, board, repos, prefix)
		w := newWalk(ctx, e, id, lr, fr, nil, given, fr.workspace(id, "run-"+strconv.FormatInt(id, 10)))
		if err := w.carry(); err != nil {
			runLog(e.db, id, 0, logError, "the objects it carries couldn't be carried in: %v", err)
			w.fail()
		} else {
			w.enter()
		}
		e.finish(ctx, id, w)
	}()
}

// Executes a run re-running another from process from, as start does, but
// on board, nil for the top board, given what came from outside, starting at
// the process, with the objects holding values, and its branches starting
// from the commits in starts; see reruns.go.
func (e *engine) rerun(nr pendingRun, flow int64, board *int64, given map[int64]value, from int64, values map[int64]value, starts map[string]string) {
	ctx, cancel := context.WithCancelCause(context.Background())
	lr := e.register(nr.id, func() { cancel(nil) })
	lr.failWith = cancel
	go func() {
		logRunStarted(e.db, nr.id)
		fr := newFlowRun(flow, nr.version, nr.board, nr.repos, nr.prefix)
		fr.starts = starts
		w := newWalk(ctx, e, nr.id, lr, fr, board, given, fr.workspace(nr.id, "run-"+strconv.FormatInt(nr.id, 10)))
		w.resume(from, values)
		e.finish(ctx, nr.id, w)
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
	e.mu.Lock()
	lr := e.runs[id]
	delete(e.runs, id)
	failure := ""
	if lr != nil {
		failure = lr.failure
	}
	e.mu.Unlock()
	status := runDone
	switch {
	case failure != "":
		status = runFailed
	case ctx.Err() != nil:
		status = runInterrupted
	case w.failed:
		status = runFailed
	}
	if lr != nil {
		lr.cancel()
	}
	rn, err := endRun(context.Background(), e.db, id, status)
	// Its workspaces aren't needed until a follow-up or a re-run; see
	// releaseWorkspaces.
	if w.ws != nil {
		releaseWorkspaces(e.workerURL, []int64{id})
	}
	if err != nil {
		runLog(e.db, id, 0, logError, "couldn't be recorded as %s: %v", status, err)
		return status
	}
	logRunFinished(e.db, rn)
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

// Stops a run as stop does, but as failed, logging why, such as "it spent
// what it may"; a run that is over only has it logged.
func (e *engine) fail(id int64, why string) {
	runLog(e.db, id, 0, logError, "stopped: %s", why)
	e.mu.Lock()
	defer e.mu.Unlock()
	if lr, ok := e.runs[id]; ok {
		lr.failure = why
		if lr.failWith != nil {
			lr.failWith(errors.New(why))
		} else {
			lr.cancel()
		}
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
			runLog(e.db, runID, 0, logError, "the review of process %d couldn't be recorded: %v", processID, err)
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
	// For a re-run: the commit each repository's branches start from, by
	// name; see reruns.go.
	starts map[string]string
	// For a flow a subflow process runs: the flows it runs inside, outermost
	// first.
	callers []int64
	// The board's OPL, made once an agent asks for it.
	describe sync.Once
	about    string
}

// The OPL of the board the run walks, as an agent describing the flow gets it.
func (fr *flowRun) opl(ctx context.Context, db *sql.DB) string {
	fr.describe.Do(func() {
		fr.about = opl(fr.board, func(id int64) string { return flowNameOf(ctx, db, id) })
	})
	return fr.about
}

func newFlowRun(flow, version int64, board snapshot, repos []repository, prefix string) *flowRun {
	fr := &flowRun{flow: flow, version: version, board: board, objects: map[int64]snapshotObject{}, repos: repos, prefix: prefix}
	for _, o := range board.Objects {
		fr.objects[o.ID] = o
	}
	return fr
}

// A workspace of its own for run, named id, on the branch <prefix>/id; nil
// when the flow has no repositories.
func (fr *flowRun) workspace(run int64, id string) *runWorkspace {
	if len(fr.repos) == 0 {
		return nil
	}
	return &runWorkspace{id: id, run: run, branch: fr.prefix + "/" + id, repos: fr.repos, starts: fr.starts}
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
	// Agent to the objects it decides the state of; those it takes too it
	// reviews, rather than generates.
	decides map[int64]map[int64]bool
	// Process to the objects it takes, and object to the processes taking it.
	inputs map[int64][]linkEnd
	// Process to the role of each object it takes in a role.
	roles map[int64]map[int64]string
	// Process to the stored records it queries; see queries.go.
	queries map[int64][]queryEnd
	// Process to the states each object it takes on a condition must be in.
	conditions map[int64]map[int64][]int64
	takers     map[int64][]taker
	successors map[int64][]int64
	// Process to those handling its failure.
	handlers   map[int64][]int64
	objects    map[int64]string
	stateNames map[int64]string
	// State to the object it belongs to.
	stateObjects map[int64]int64
	// Object to the states with rules, in order, and their rules.
	rules     map[int64][]ruledState
	generated map[int64]bool

	mu     sync.Mutex
	values map[int64]value
	// Objects a skipped process would have generated, settled as absent.
	skipped map[int64]bool
	// Objects carried over that no run stored yet: they exist, so the
	// processes taking them run, but hold nothing to hand in.
	unstored map[int64]bool
	// How many times each process has run, over all passes.
	ran        map[int64]int
	failed     bool
	terminated bool
}

type taker struct {
	process, state int64
}

type ruledState struct {
	state int64
	rule  rule
}

// The state an object holding content is in by its states' rules, 0 for
// none.
func (w *walk) ruled(o int64, content string) int64 {
	for _, r := range w.rules[o] {
		if r.rule.holds(content) {
			return r.state
		}
	}
	return 0
}

// Puts objects a step generated in no state in the state their rules say,
// and records it as the step's.
func (w *walk) applyRules(stepID int64, out map[int64]value) {
	for _, o := range slices.Sorted(maps.Keys(out)) {
		v := out[o]
		if v.state != 0 {
			continue
		}
		if v.state = w.ruled(o, v.content); v.state == 0 {
			continue
		}
		out[o] = v
		if err := recordState(context.Background(), w.e.db, stepID, o, w.stateNames[v.state]); err != nil {
			log.Printf("record state of object %d in step %d: %v", o, stepID, err)
		}
	}
}

// A walk of board, nil for the top one, with the objects given from outside,
// working in ws.
func newWalk(ctx context.Context, e *engine, run int64, lr *liveRun, fr *flowRun, board *int64, given map[int64]value, ws *runWorkspace) *walk {
	w := &walk{
		e: e, ctx: ctx, run: run, lr: lr, fr: fr, board: board, ws: ws,
		kinds: map[int64]string{}, names: map[int64]string{}, recording: map[int64]bool{}, settings: map[int64]processSettings{},
		outputs: map[int64][]linkEnd{}, decides: map[int64]map[int64]bool{}, inputs: map[int64][]linkEnd{}, roles: map[int64]map[int64]string{}, queries: map[int64][]queryEnd{}, conditions: map[int64]map[int64][]int64{},
		takers: map[int64][]taker{}, successors: map[int64][]int64{}, handlers: map[int64][]int64{},
		objects: map[int64]string{}, stateNames: map[int64]string{}, stateObjects: map[int64]int64{}, rules: map[int64][]ruledState{},
		generated: map[int64]bool{}, values: map[int64]value{}, skipped: map[int64]bool{}, unstored: map[int64]bool{}, ran: map[int64]int{},
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
		// A rule that no longer parses holds of nothing.
		if r, err := parseRule(st.Rule); err == nil && len(r) > 0 {
			w.rules[st.Object] = append(w.rules[st.Object], ruledState{st.ID, r})
		}
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
			// A human's link stands for each state it can set; an agent's
			// generates its object in the state it decides.
			if states := linkStates[l.ID]; len(states) > 0 && w.kinds[l.Process] != kindHuman {
				if w.decides[l.Process] == nil {
					w.decides[l.Process] = map[int64]bool{}
				}
				w.decides[l.Process][l.Object] = true
				w.outputs[l.Process] = append(w.outputs[l.Process], linkEnd{l.Object, 0})
			} else if len(states) > 0 {
				for _, st := range states {
					w.outputs[l.Process] = append(w.outputs[l.Process], linkEnd{l.Object, st})
				}
			} else {
				w.outputs[l.Process] = append(w.outputs[l.Process], linkEnd{l.Object, l.State})
			}
		} else if l.Query != nil {
			// A query doesn't wait, nor take the object in this run.
			w.queries[l.Process] = append(w.queries[l.Process], queryEnd{l.Object, l.Query})
		} else {
			// A condition takes its object as a link without a state does.
			end := linkEnd{l.Object, l.State}
			if l.Condition && l.State != 0 {
				if w.conditions[l.Process] == nil {
					w.conditions[l.Process] = map[int64][]int64{}
				}
				w.conditions[l.Process][l.Object] = append(w.conditions[l.Process][l.Object], l.State)
				end.state = 0
			}
			if !slices.Contains(w.inputs[l.Process], end) {
				w.inputs[l.Process] = append(w.inputs[l.Process], end)
				w.takers[l.Object] = append(w.takers[l.Object], taker{l.Process, end.state})
			}
			if l.Role != roleNone {
				if w.roles[l.Process] == nil {
					w.roles[l.Process] = map[int64]string{}
				}
				w.roles[l.Process][l.Object] = l.Role
			}
		}
	}
	for _, c := range s.Connections {
		if _, ok := w.kinds[c.Source]; !ok {
			continue
		}
		if c.Kind == connectionFailure {
			w.handlers[c.Source] = append(w.handlers[c.Source], c.Target)
		} else {
			w.successors[c.Source] = append(w.successors[c.Source], c.Target)
		}
	}
	// A human generates only what they fill in, not the objects they decide
	// on, nor those they review without states to decide between. Nor does
	// an agent generate what it reviews.
	for p, ends := range w.outputs {
		for _, end := range ends {
			takes := slices.ContainsFunc(w.inputs[p], func(i linkEnd) bool { return i.object == end.object })
			switch {
			case w.kinds[p] == kindHuman && (end.state != 0 || takes):
			case w.decides[p][end.object] && takes:
			default:
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
		next = append(next, w.follow(o, p)...)
	}
	w.mu.Unlock()
	all(next)
}

// Runs the board from process from, with the objects holding values, rather
// than from its start. A process waiting for an object to enter a state takes
// it as entering the state it is in. Returns once nothing on it is left to run.
func (w *walk) resume(from int64, values map[int64]value) {
	w.mu.Lock()
	maps.Copy(w.values, values)
	var trigger int64
	for _, i := range w.inputs[from] {
		if v, ok := w.values[i.object]; ok && i.state != 0 && v.state == i.state {
			trigger = i.object
			break
		}
	}
	w.mu.Unlock()
	w.runProcess(from, newPass(), trigger)
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

// The object as process p takes it, in its role. Called with w.mu held.
func (w *walk) inputOf(p, o int64) runInput {
	in := w.asInput(o)
	switch w.roles[p][o] {
	case roleInstrument:
		in.Role = inputInstrument
	case roleConsumption:
		in.Role = inputConsumed
	default:
		// What an agent or a person reviews, deciding its state, keeps what
		// it holds; only one generated anew is affected.
		reviews := w.decides[p][o] || w.kinds[p] == kindHuman
		if !reviews && slices.ContainsFunc(w.outputs[p], func(e linkEnd) bool { return e.object == o }) {
			in.Role = inputAffected
		}
	}
	return in
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

// What becomes of a process now that objects it takes without waiting for a
// state exist or were skipped: it runs once all of them are settled and
// some exist, holding their conditions, or as many as its quorum says; it
// is skipped once that can no longer be; otherwise it waits. A process
// waiting for an object to enter a state waits for that instead. Called
// with w.mu held.
func (w *walk) readiness(p int64) (run, skip bool) {
	plain := w.plainInputs(p)
	if len(plain) == 0 || len(plain) < len(w.inputs[p]) {
		return false, false
	}
	var exist, settled, holding int
	for _, i := range plain {
		v, ok := w.values[i.object]
		switch {
		case ok:
			exist++
			settled++
			if w.holds(p, i.object, v.state) {
				holding++
			}
		case w.skipped[i.object]:
			settled++
		}
	}
	if q := w.settings[p].Quorum; q != nil {
		if holding >= min(*q, len(plain)) {
			return true, false
		}
		return false, settled == len(plain)
	}
	if settled < len(plain) {
		return false, false
	}
	if exist == 0 || holding < exist {
		return false, true
	}
	return true, false
}

// Whether an object in state holds the conditions p takes it on, if any.
func (w *walk) holds(p, o, state int64) bool {
	states, ok := w.conditions[p][o]
	return !ok || slices.Contains(states, state)
}

// The processes that run, or are skipped, in pass p now that o exists or
// was skipped. Called with w.mu held.
func (w *walk) follow(o int64, p *pass) []func() {
	var next []func()
	for _, t := range w.takers[o] {
		if t.state != 0 {
			continue
		}
		switch run, skip := w.readiness(t.process); {
		case run:
			next = append(next, func() { w.runProcess(t.process, p, 0) })
		case skip:
			next = append(next, func() { w.skip(t.process, p) })
		}
	}
	return next
}

// Why a process's conditions don't hold of what it takes, or "" if they
// do. Called with w.mu held.
func (w *walk) unmet(p int64) string {
	var why []string
	for _, o := range slices.Sorted(maps.Keys(w.conditions[p])) {
		v, ok := w.values[o]
		if ok && !w.holds(p, o, v.state) {
			names := make([]string, len(w.conditions[p][o]))
			for i, st := range w.conditions[p][o] {
				names[i] = strconv.Quote(w.stateNames[st])
			}
			state := "in no state"
			if v.state != 0 {
				state = strconv.Quote(w.stateNames[v.state])
			}
			why = append(why, fmt.Sprintf("%s is %s, not %s", strconv.Quote(w.objects[o]), state, strings.Join(names, " or ")))
		}
	}
	return strings.Join(why, "; ")
}

// Skips a process in pass p, unless it ran in it already.
func (w *walk) skip(id int64, p *pass) {
	if w.stopped() || !p.visit(id) {
		return
	}
	w.skipVisited(id, p)
}

// Records why the process didn't run, settles what it generates as absent
// and goes on to what follows it, as after it ran.
func (w *walk) skipVisited(id int64, p *pass) {
	w.mu.Lock()
	why := w.unmet(id)
	switch q := w.settings[id].Quorum; {
	case q != nil:
		why = fmt.Sprintf("Fewer than %d of what it takes came, holding its conditions.", *q)
	case why != "":
		why = "Its condition doesn't hold: " + why + "."
	default:
		why = "Nothing it takes came; what would have was skipped."
	}
	w.mu.Unlock()
	w.note(id, stepSkipped, why)

	w.mu.Lock()
	var next []func()
	for _, end := range w.outputs[id] {
		if _, ok := w.values[end.object]; !ok {
			w.skipped[end.object] = true
		}
	}
	for _, end := range w.outputs[id] {
		next = append(next, w.follow(end.object, p)...)
	}
	w.mu.Unlock()
	for _, s := range w.successors[id] {
		next = append(next, func() { w.runProcess(s, p, 0) })
	}
	all(next)
}

// Sets what an object holds, which settles it as existing. Called with w.mu
// held.
func (w *walk) set(o int64, v value) {
	w.values[o] = v
	delete(w.skipped, o)
	delete(w.unstored, o)
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
	w.runProcessWith(id, p, trigger, nil)
}

// Runs a process taking extra besides what it takes, such as a failure it
// handles.
func (w *walk) runProcessWith(id int64, p *pass, trigger int64, extra []runInput) {
	if w.stopped() || !p.visit(id) {
		return
	}
	human := w.kinds[id] == kindHuman
	// A process run from the start or by an event still keeps to its
	// conditions, unless its quorum is met.
	w.mu.Lock()
	if w.settings[id].Quorum == nil && w.unmet(id) != "" {
		w.mu.Unlock()
		w.skipVisited(id, p)
		return
	}
	w.ran[id]++
	w.mu.Unlock()

	w.mu.Lock()
	given := []runInput{}
	taken := map[int64]bool{}
	// What doesn't hold its condition doesn't go in, as with a quorum.
	for _, i := range w.plainInputs(id) {
		if v, ok := w.values[i.object]; ok && w.generated[i.object] && !w.unstored[i.object] && w.holds(id, i.object, v.state) {
			given = append(given, w.inputOf(id, i.object))
			taken[i.object] = true
		}
	}
	for _, o := range w.startGiven(id) {
		if !taken[o] && o != trigger {
			given = append(given, w.inputOf(id, o))
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
		} else if !taken[trigger] {
			// Taken plainly too, it goes in once.
			given = append(given, w.inputOf(id, trigger))
		}
	}
	for _, in := range extra {
		if !slices.ContainsFunc(given, func(g runInput) bool { return g.Name == in.Name }) {
			given = append(given, in)
		}
	}
	// What it consumes is gone once it takes it.
	for o, role := range w.roles[id] {
		if role == roleConsumption {
			delete(w.values, o)
		}
	}
	w.mu.Unlock()

	if !human {
		records, err := w.queried(id)
		if err != nil {
			runLog(w.e.db, w.run, 0, logError, "the records for process %d couldn't be queried: %v", id, err)
			w.note(id, stepError, "Its query couldn't read the records.")
			w.fail()
			return
		}
		given = append(given, records...)
	}

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
		w.set(o, out[o])
	}
	if w.terminated {
		w.mu.Unlock()
		return
	}
	var next []func()
	var fresh *pass
	for _, o := range objects {
		if st := out[o].state; st != 0 {
			takers := w.takersIn(o, st)
			// A decision starts a fresh pass from the object, while rounds last.
			from := p
			if w.decides[id][o] {
				if w.ran[id] >= w.settings[id].rounds() && slices.ContainsFunc(takers, func(t int64) bool { return w.ran[t] > 0 }) {
					w.mu.Unlock()
					msg := fmt.Sprintf("It decided %s is %s again after %d rounds, the most it runs; it can no longer send it back.",
						strconv.Quote(w.objects[o]), strconv.Quote(w.stateNames[st]), w.ran[id])
					w.note(id, stepError, msg)
					w.handleFailure(id, given, msg)
					return
				}
				if fresh == nil {
					fresh = newPass()
				}
				from = fresh
			}
			for _, t := range takers {
				next = append(next, func() { w.runProcess(t, from, o) })
			}
		}
		next = append(next, w.follow(o, p)...)
	}
	w.mu.Unlock()
	for _, s := range w.successors[id] {
		next = append(next, func() { w.runProcess(s, p, 0) })
	}
	all(next)
}

// Executes an agent, script, Sentry or connector process; returns what it generated, each
// object in the state its link names. False if it failed or found a
// duplicate, which terminates the run and so still returns what it generated.
func (w *walk) runStep(id int64, given []runInput, cr *changeRequest) (map[int64]value, bool) {
	var res runResult
	var ok, stopped bool
	var stepID int64
	for try := 0; ; try++ {
		res, ok, stopped, stepID = w.execute(id, given, cr)
		if ok || stopped || w.stopped() || try >= w.settings[id].retries() {
			break
		}
	}
	if !ok {
		if stopped {
			w.fail()
		} else {
			w.handleFailure(id, given, res.Error)
		}
		return nil, false
	}
	out := map[int64]value{}
	for _, end := range w.outputs[id] {
		content, ok := res.Objects[end.object]
		if !ok {
			content = res.Output
		}
		v := value{content: content, state: end.state}
		if d, ok := res.Decisions[end.object]; ok {
			v.state = d.state
			if d.instructions != "" {
				v.review = &review{reviewer: w.names[id], instructions: d.instructions}
			}
		}
		out[end.object] = v
	}
	w.applyRules(stepID, out)
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
				runLog(w.e.db, w.run, 0, logError, "the flow of process %d couldn't run: %v", id, err)
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
		if v, ok := w.values[i.object]; ok && !w.unstored[i.object] {
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

// The process failed, saying why: what handles its failure runs, each in a
// fresh pass, taking what it took and what went wrong; without any, the
// run fails.
func (w *walk) handleFailure(id int64, given []runInput, msg string) {
	handlers := w.handlers[id]
	if len(handlers) == 0 || w.stopped() {
		w.fail()
		return
	}
	if msg == "" {
		msg = "It failed."
	}
	extra := append([]runInput{{Name: "Failure of " + w.names[id], Content: msg}}, given...)
	next := make([]func(), len(handlers))
	for i, h := range handlers {
		next[i] = func() { w.runProcessWith(h, newPass(), 0, extra) }
	}
	all(next)
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
			runLog(w.e.db, w.run, 0, logError, "process %d couldn't be recorded: %v", id, err)
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
			runLog(db, w.run, stepID, logError, "the run of process %d couldn't start: %v", id, err)
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
					runLog(db, w.run, 0, logError, "item %s couldn't be claimed: %v", it.key, err)
					w.fail()
					return nil, runFailed, nil
				}
				return nil, itemSkipped, dup
			}
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(w.reviews)
		lr = w.e.register(run, cancel)
		logRunStarted(db, run)
	}
	// An item works on a branch of its own, so items running at once don't
	// meet; otherwise the board goes on with this run's.
	ws := w.ws
	if it != nil {
		name := fmt.Sprintf("run-%d-step-%d", w.run, stepID)
		if recording {
			name = "run-" + strconv.FormatInt(run, 10)
		}
		ws = w.fr.workspace(run, name)
	}
	// Another flow works on its own repositories, in a workspace of its run's.
	var inner *walk
	if callee != nil {
		inner = newWalk(ctx, w.e, run, lr, callee, nil, w.toCallee(callee, given), callee.workspace(run, "run-"+strconv.FormatInt(run, 10)))
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
		if err := recordState(context.Background(), db, stepID, o, w.stateNames[out[o].state]); err != nil {
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
		runLog(w.e.db, w.run, 0, logError, "process %d couldn't be recorded: %v", id, err)
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
		`INSERT INTO runs (flow_id, status, version_id, parent_run_id, process_ref, process_name, item_key, replica_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		flow, runRunning, version, parent, board, name, key, replicaID)
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
	entered := map[int64]value{}
	for _, o := range objects {
		entered[o] = value{content: d.entries[o]}
	}
	w.applyRules(stepID, entered)
	w.mu.Lock()
	defer w.mu.Unlock()
	var next []func()
	for _, o := range objects {
		w.set(o, entered[o])
		if st := entered[o].state; st != 0 {
			for _, t := range w.takersIn(o, st) {
				next = append(next, func() { w.runProcess(t, p, o) })
			}
		}
	}
	for _, o := range objects {
		next = append(next, w.follow(o, p)...)
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
	w.set(object, v)
	w.mu.Unlock()

	var next []func()
	for _, t := range w.takersIn(object, *d.state) {
		next = append(next, func() { w.runProcess(t, newPass(), object) })
	}
	return next
}

// Executes an agent, script, Sentry or connector process as a step of the run, once a
// slot is free; the step can be stopped on its own while it goes. False if
// it failed or was stopped, and whether it was stopped, by a person or with
// its run, rather than failed; a duplicate is a result, and terminates the
// run. A step with a timeout fails once it runs out: the worker ends agents
// and scripts then, and the engine a little later if the worker doesn't.
func (w *walk) execute(id int64, inputs []runInput, cr *changeRequest) (runResult, bool, bool, int64) {
	who, err := runUser(w.ctx, w.e.db, w.run)
	if err != nil {
		runLog(w.e.db, w.run, 0, logError, "process %d couldn't run: whose run it is: %v", id, err)
		return runResult{}, false, false, 0
	}
	if !w.e.personSlot(w.ctx, who, w.run, w.names[id]) {
		return runResult{}, false, true, 0
	}
	defer w.e.endPersonSlot(who)
	select {
	case w.e.slots <- struct{}{}:
	case <-w.ctx.Done():
		return runResult{}, false, true, 0
	}
	stop, cancel := context.WithCancel(w.ctx)
	defer cancel()
	ctx := stop
	if t := w.settings[id].timeout(); t > 0 {
		if w.kinds[id] != kindSentry && w.kinds[id] != kindConnector {
			t += timeoutGrace
		}
		var end context.CancelFunc
		ctx, end = context.WithTimeout(stop, t)
		defer end()
	}
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
	switch w.kinds[id] {
	case kindAgent:
		var about string
		if w.settings[id].DescribeFlow {
			about = w.fr.opl(ctx, w.e.db)
		}
		res, err = runAgentStep(ctx, w.e.db, w.e.workerURL, w.e.mcp, w.ws, w.run, id, inputs, cr, about, started)
	case kindSentry:
		res, err = runSentryStep(ctx, w.e.db, w.e.sentry, w.run, id, inputs, started)
	case kindConnector:
		res, err = runConnectorStep(ctx, w.e.db, w.run, id, inputs, started)
	default:
		res, err = runScriptStep(ctx, w.e.db, w.e.workerURL, w.ws, w.run, id, inputs, started)
	}
	<-w.e.slots
	if err != nil {
		if ctx.Err() == nil {
			runLog(w.e.db, w.run, stepID, logError, "process %d couldn't run: %v", id, err)
		}
		return res, false, stop.Err() != nil, stepID
	}
	return res, res.OK || res.Duplicate != nil, stop.Err() != nil, stepID
}

// Waits until the person's runs have fewer steps going than they may
// (limits.go), before the step takes one of the engine's slots, so one
// waiting holds none of everyone's; false if the run ended meanwhile. A run
// of nobody's isn't held to it. Its run's log says when a step waits.
func (e *engine) personSlot(ctx context.Context, who *int64, run int64, process string) bool {
	logged := false
	for {
		limit := currentLimits().UserSteps
		e.mu.Lock()
		if who == nil || limit <= 0 || e.personSteps[*who] < limit {
			if who != nil {
				e.personSteps[*who]++
			}
			e.mu.Unlock()
			return true
		}
		ended := e.stepEnded
		e.mu.Unlock()
		if !logged {
			runLog(e.db, run, 0, logInfo, "%s waits: its person's runs have as many steps going as they may at once, %d", process, limit)
			logged = true
		}
		select {
		case <-ended:
		// The limit may have been raised.
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return false
		}
	}
}

func (e *engine) endPersonSlot(who *int64) {
	if who == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.personSteps[*who]--; e.personSteps[*who] <= 0 {
		delete(e.personSteps, *who)
	}
	close(e.stepEnded)
	e.stepEnded = make(chan struct{})
}

// How long after its timeout the engine ends a step the worker didn't.
const timeoutGrace = 30 * time.Second

// Why a step's context ended, as the step's error: with the run failed on
// purpose, why.
func stopReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "it ran longer than its timeout"
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return "it was stopped, as its run " + strings.TrimPrefix(cause.Error(), "it ")
	}
	return "it was stopped"
}
