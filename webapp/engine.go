package main

import (
	"context"
	"database/sql"
	"log"
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
//     object enters that state, with whichever objects it takes without a
//     state exist. Several such links are alternatives, not all required.
//
// A failure stops its branch, and every process runs at most once per pass,
// so cycles end. Processes still connected directly run right after their
// predecessor.
//
// A human process waits for a person to review its inputs and set an object
// to one of the states its output links name; the object keeps its content.
// Instructions they leave go as a change request to the processes taking the
// object in that state. Each decision starts a fresh pass, so review loops
// can repeat while cycles without a human still run once per pass.
//
// A step generating a unique value another run stored terminates the run: no
// further process starts, and reviews still waiting end undecided.

// Script processes don't execute yet; they just occupy their slot in the run.
const simulatedRunTime = 3 * time.Second

// What a human decided on a review: the state they set, if they had any to
// choose from, and their instructions.
type decision struct {
	state        *int64
	instructions string
}

type engine struct {
	db        *sql.DB
	workerURL string
	mu        sync.Mutex
	runs      map[int64]*liveRun
}

// A run in progress.
type liveRun struct {
	cancel context.CancelFunc
	// The reviews waiting for a decision, by step id.
	reviews map[int64]chan decision
}

func newEngine(db *sql.DB, workerURL string) *engine {
	return &engine{db: db, workerURL: workerURL, runs: map[int64]*liveRun{}}
}

// Executes a run on board in the background, and ends it once nothing in it
// is left to run.
func (e *engine) start(id int64, board snapshot) {
	ctx, cancel := context.WithCancel(context.Background())
	lr := &liveRun{cancel: cancel, reviews: map[int64]chan decision{}}
	e.mu.Lock()
	e.runs[id] = lr
	e.mu.Unlock()

	go func() {
		w := newWalk(ctx, e, id, lr, board)
		status := runDone
		if board.Start.Target != nil {
			w.runProcess(*board.Start.Target, newPass(), 0)
		}
		if w.failed {
			status = runFailed
		}
		e.mu.Lock()
		delete(e.runs, id)
		e.mu.Unlock()
		cancel()
		// A run stopped or terminated has ended already, and keeps how.
		if _, err := endRun(context.Background(), e.db, id, status); err != nil {
			log.Printf("end run %d: %v", id, err)
		}
	}()
}

// Cancels a run's steps still going, and its reviews still waiting.
func (e *engine) stop(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if lr, ok := e.runs[id]; ok {
		lr.cancel()
	}
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
func (e *engine) await(ctx context.Context, lr *liveRun, runID, processID int64, inputs []runInput) (decision, bool) {
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
		return decision{}, false
	}
	select {
	case d := <-ch:
		return d, true
	case <-ctx.Done():
		return decision{}, false
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

// A run walking its board.
type walk struct {
	e   *engine
	ctx context.Context
	run int64
	lr  *liveRun
	// Ends waiting reviews when the run is terminated.
	terminate context.CancelFunc
	reviews   context.Context

	kinds, names map[int64]string
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

func newWalk(ctx context.Context, e *engine, run int64, lr *liveRun, s snapshot) *walk {
	w := &walk{
		e: e, ctx: ctx, run: run, lr: lr,
		kinds: map[int64]string{}, names: map[int64]string{},
		outputs: map[int64][]linkEnd{}, inputs: map[int64][]linkEnd{},
		takers: map[int64][]taker{}, successors: map[int64][]int64{},
		objects: map[int64]string{}, stateNames: map[int64]string{}, stateObjects: map[int64]int64{},
		generated: map[int64]bool{}, values: map[int64]value{},
	}
	w.reviews, w.terminate = context.WithCancel(ctx)
	for _, p := range s.Processes {
		w.kinds[p.ID] = p.Kind
		w.names[p.ID] = p.Name
	}
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
		w.successors[c.Source] = append(w.successors[c.Source], c.Target)
	}
	for p, ends := range w.outputs {
		if w.kinds[p] == kindHuman {
			continue
		}
		for _, end := range ends {
			w.generated[end.object] = true
		}
	}
	for o := range w.objects {
		if !w.generated[o] {
			w.values[o] = value{}
		}
	}
	return w
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

// Called with w.mu held.
func (w *walk) ready(p int64) bool {
	plain := w.plainInputs(p)
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
	for _, i := range w.plainInputs(id) {
		if _, ok := w.values[i.object]; ok && w.generated[i.object] {
			given = append(given, w.asInput(i.object))
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
		w.runHuman(id, given)
		return
	}

	res, ok := w.execute(id, given, cr)
	if !ok {
		w.mu.Lock()
		w.failed = true
		w.mu.Unlock()
		return
	}
	produced := w.outputs[id]
	w.mu.Lock()
	for _, end := range produced {
		content, ok := res.Objects[end.object]
		if !ok {
			content = res.Output
		}
		w.values[end.object] = value{content: content, state: end.state}
	}
	if res.Duplicate != nil {
		w.terminated = true
		w.terminate()
	}
	if w.terminated {
		w.mu.Unlock()
		return
	}
	var next []func()
	for _, end := range produced {
		if end.state != 0 {
			for _, t := range w.takersIn(end.object, end.state) {
				next = append(next, func() { w.runProcess(t, p, end.object) })
			}
			continue
		}
		for _, t := range w.takers[end.object] {
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

// Waits for the human's decision, then starts a fresh pass from the object
// they set, for the processes taking it in that state.
func (w *walk) runHuman(id int64, given []runInput) {
	d, ok := w.e.await(w.reviews, w.lr, w.run, id, given)
	if !ok || d.state == nil || w.stopped() {
		return
	}
	object, ok := w.stateObjects[*d.state]
	if !ok {
		return
	}
	w.mu.Lock()
	v := value{content: w.values[object].content, state: *d.state}
	if d.instructions != "" {
		v.review = &review{reviewer: w.names[id], instructions: d.instructions}
	}
	w.values[object] = v
	w.mu.Unlock()

	var next []func()
	for _, t := range w.takersIn(object, *d.state) {
		next = append(next, func() { w.runProcess(t, newPass(), object) })
	}
	all(next)
}

// Executes a process as a step of the run. False if it failed; a duplicate
// is a result, and terminates the run.
func (w *walk) execute(id int64, inputs []runInput, cr *changeRequest) (runResult, bool) {
	if w.kinds[id] == kindAgent {
		res, err := runAgentStep(w.ctx, w.e.db, w.e.workerURL, w.run, id, inputs, cr)
		if err != nil {
			if w.ctx.Err() == nil {
				log.Printf("run process %d in run %d: %v", id, w.run, err)
			}
			return res, false
		}
		return res, res.OK || res.Duplicate != nil
	}

	stepID, err := startStep(w.ctx, w.e.db, w.run, id, stepActive, "", inputs, nil)
	if err != nil {
		if w.ctx.Err() == nil {
			log.Printf("record process %d in run %d: %v", id, w.run, err)
		}
		return runResult{}, false
	}
	select {
	case <-time.After(simulatedRunTime):
	case <-w.ctx.Done():
		// Stopping the run ended the step.
		return runResult{}, false
	}
	dup, err := finishStep(context.Background(), w.e.db, stepID, stepDone, "", "", nil)
	if err != nil {
		log.Printf("record step %d: %v", stepID, err)
		return runResult{}, false
	}
	return runResult{OK: true, Duplicate: dup}, true
}
