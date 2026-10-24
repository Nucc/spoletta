package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The engine's core: how a run walks a board of agents and scripts, joins
// and branches, conditions on states, direct connections, step failures and
// stops, and the runs API around it.

// An agent that replies with plain text, for processes generating nothing.
func corePlain(out string) func(context.Context, agentRun) runResult {
	return func(context.Context, agentRun) runResult { return runResult{OK: true, Output: out} }
}

// An agent that waits for release, or for its step to be stopped, before
// handing in v. started is told when it began, once per call.
type coreGate struct {
	started chan string
	release chan struct{}
}

func newCoreGate() *coreGate {
	return &coreGate{started: make(chan string, 16), release: make(chan struct{})}
}

func (g *coreGate) agent(name string, reply runResult) func(context.Context, agentRun) runResult {
	return func(ctx context.Context, _ agentRun) runResult {
		g.started <- name
		select {
		case <-g.release:
			return reply
		case <-ctx.Done():
			return runResult{Error: "cancelled"}
		case <-time.After(5 * time.Second):
			return runResult{Error: "the gate was never opened"}
		}
	}
}

// Waits for the named agent to start.
func (g *coreGate) wait(t *testing.T, name string) {
	t.Helper()
	select {
	case got := <-g.started:
		if got != name {
			t.Fatalf("%s started, want %s", got, name)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never started", name)
	}
}

// An app whose worker is at url, rather than a fake.
func coreAppWithWorker(t *testing.T, url string) *app {
	t.Helper()
	return startApp(t, url)
}

func coreInputNames(s step) []string {
	var names []string
	for _, in := range s.Inputs {
		names = append(names, in.Name)
	}
	slices.Sort(names)
	return names
}

func coreDone(t *testing.T, d runDetail) {
	t.Helper()
	if d.Status != runDone {
		t.Fatalf("run %s, want done; steps %s", d.Status, describeSteps(d))
	}
}

// A process taking several objects runs once all of them exist, once, with
// all of them; objects nothing generates count as existing from the start.
func TestEngineJoinWaitsForAllPlainInputs(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Join")
	seed := a.agent(f.ID, "Seed", "Seed.")
	s := a.newObject(f.ID, "S")
	left := a.agent(f.ID, "Left", "Left.")
	right := a.agent(f.ID, "Right", "Right.")
	x := a.newObject(f.ID, "X")
	y := a.newObject(f.ID, "Y")
	config := a.newObject(f.ID, "Config")
	join := a.agent(f.ID, "Join", "Join.")
	a.output(seed.ID, s.ID)
	a.input(left.ID, s.ID)
	a.input(right.ID, s.ID)
	a.output(left.ID, x.ID)
	a.output(right.ID, y.ID)
	a.input(join.ID, x.ID)
	a.input(join.ID, y.ID)
	a.input(join.ID, config.ID)
	a.startAt(f.ID, seed.ID)

	g := newCoreGate()
	a.worker.onAgent("Seed", handsIn(data("seed")))
	a.worker.onAgent("Left", handsIn(data("left")))
	a.worker.onAgent("Right", g.agent("Right", submitted(data("right"))))
	a.worker.onAgent("Join", corePlain("joined"))

	rn := a.startRun(f.ID)
	g.wait(t, "Right")
	a.waitStep(rn.ID, "Left", stepDone)
	if n := len(a.worker.runsOf("Join")); n != 0 {
		t.Fatalf("Join ran %d times before Y existed", n)
	}
	close(g.release)

	d := a.finish(rn.ID)
	coreDone(t, d)
	js := onlyStep(t, d, "Join")
	if got := coreInputNames(js); !slices.Equal(got, []string{"X", "Y"}) {
		t.Errorf("Join took %v, want X and Y", got)
	}
	for _, want := range []string{`<input name="X">`, `<input name="Y">`, "left", "right"} {
		if !strings.Contains(js.Prompt, want) {
			t.Errorf("Join's prompt lacks %q:\n%s", want, js.Prompt)
		}
	}
	if runs := a.worker.runsOf("Join"); len(runs) != 1 || runs[0].Prompt != js.Prompt {
		t.Errorf("the worker ran Join %d times; the step recorded a prompt other than the one sent", len(runs))
	}
}

// Only objects make a process run: one taking nothing, that the start doesn't
// point at, never runs, nor does one taking only objects nothing generates.
func TestEngineProcessesWithoutTriggersDontRun(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Untriggered")
	first := a.agent(f.ID, "First", "First.")
	a.output(first.ID, a.newObject(f.ID, "Out").ID)
	lonely := a.agent(f.ID, "Lonely", "Lonely.")
	a.output(lonely.ID, a.newObject(f.ID, "Never").ID)
	waiting := a.agent(f.ID, "Waiting", "Waiting.")
	a.input(waiting.ID, a.newObject(f.ID, "Given").ID)
	a.startAt(f.ID, first.ID)
	a.worker.onAgent("First", handsIn(data("first")))

	d := a.finish(a.startRun(f.ID).ID)
	coreDone(t, d)
	if len(d.Steps) != 1 || d.Steps[0].ProcessName != "First" {
		t.Errorf("steps %s, want only First", describeSteps(d))
	}
}

// A run of a board whose start points nowhere ends done at once.
func TestEngineNoStartTarget(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Startless")
	a.agent(f.ID, "Idle", "Idle.")
	d := a.finish(a.startRun(f.ID).ID)
	coreDone(t, d)
	if len(d.Steps) != 0 || len(d.Objects) != 0 {
		t.Errorf("a run without a start ran %s", describeSteps(d))
	}
}

// The start runs the process it points at even though what it takes doesn't
// exist yet, with whatever of it exists; that process doesn't run again in
// the pass once it does.
func TestEngineStartAtProcessWithInputs(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Start with inputs")
	first := a.agent(f.ID, "First", "First.")
	note := a.newObject(f.ID, "Note")
	other := a.agent(f.ID, "Other", "Other.")
	a.input(first.ID, note.ID)
	a.output(other.ID, note.ID)
	a.output(first.ID, a.newObject(f.ID, "Out").ID)
	a.startAt(f.ID, first.ID)
	a.worker.onAgent("First", handsIn(data("first")))

	d := a.finish(a.startRun(f.ID).ID)
	coreDone(t, d)
	fs := onlyStep(t, d, "First")
	if len(fs.Inputs) != 0 {
		t.Errorf("First took %+v, want nothing: Note doesn't exist yet", fs.Inputs)
	}
	if n := len(stepsOf(d, "Other")); n != 0 {
		t.Errorf("Other ran %d times", n)
	}
}

// One output feeding two processes runs both at once; one failing fails the
// run, while the other branch still goes on to its end.
func TestEngineParallelBranches(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Branches")
	seed := a.agent(f.ID, "Seed", "Seed.")
	s := a.newObject(f.ID, "S")
	good := a.agent(f.ID, "Good", "Good.")
	bad := a.agent(f.ID, "Bad", "Bad.")
	goodOut := a.newObject(f.ID, "GoodOut")
	badOut := a.newObject(f.ID, "BadOut")
	after := a.agent(f.ID, "AfterGood", "After good.")
	afterBad := a.agent(f.ID, "AfterBad", "After bad.")
	a.output(seed.ID, s.ID)
	a.input(good.ID, s.ID)
	a.input(bad.ID, s.ID)
	a.output(good.ID, goodOut.ID)
	a.output(bad.ID, badOut.ID)
	a.input(after.ID, goodOut.ID)
	a.input(afterBad.ID, badOut.ID)
	a.startAt(f.ID, seed.ID)

	// Both branches are going at once before either ends.
	g := newCoreGate()
	a.worker.onAgent("Seed", handsIn(data("seed")))
	a.worker.onAgent("Good", g.agent("Good", submitted(data("good"))))
	a.worker.onAgent("Bad", g.agent("Bad", runResult{Error: "bad luck"}))
	a.worker.onAgent("AfterGood", corePlain("after"))
	a.worker.onAgent("AfterBad", corePlain("after"))

	rn := a.startRun(f.ID)
	got := map[string]bool{}
	for range 2 {
		select {
		case n := <-g.started:
			got[n] = true
		case <-time.After(5 * time.Second):
			t.Fatalf("only %v started", got)
		}
	}
	close(g.release)

	d := a.finish(rn.ID)
	if d.Status != runFailed {
		t.Fatalf("run %s, want failed; steps %s", d.Status, describeSteps(d))
	}
	if s := onlyStep(t, d, "Bad"); s.Status != stepError || s.Error != "bad luck" {
		t.Errorf("Bad = %s %q", s.Status, s.Error)
	}
	if s := onlyStep(t, d, "Good"); s.Status != stepDone {
		t.Errorf("Good = %s", s.Status)
	}
	onlyStep(t, d, "AfterGood")
	if n := len(stepsOf(d, "AfterBad")); n != 0 {
		t.Errorf("AfterBad ran %d times after Bad failed", n)
	}
	if got := contentsOf(d, "BadOut"); len(got) != 0 {
		t.Errorf("BadOut held %q", got)
	}
}

// Generating an object in a state runs only the processes taking it in that
// state; a process taking it in several states runs on any of them.
func TestEngineStateConditionsPickTakers(t *testing.T) {
	for _, c := range []struct {
		state string
		ran   []string
	}{
		{"good", []string{"Either", "OnGood"}},
		{"bad", []string{"Either", "OnBad"}},
	} {
		t.Run(c.state, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("States")
			judge := a.agent(f.ID, "Judge", "Judge.")
			verdict := a.newObject(f.ID, "Verdict")
			good := a.newState(verdict.ID, "good")
			bad := a.newState(verdict.ID, "bad")
			states := map[string]int64{"good": good.ID, "bad": bad.ID}
			a.link(map[string]any{"process": judge.ID, "object": verdict.ID, "direction": directionOutput, "state": states[c.state]})
			onGood := a.agent(f.ID, "OnGood", "On good.")
			onBad := a.agent(f.ID, "OnBad", "On bad.")
			either := a.agent(f.ID, "Either", "Either.")
			a.link(map[string]any{"process": onGood.ID, "object": verdict.ID, "direction": directionInput, "state": good.ID})
			a.link(map[string]any{"process": onBad.ID, "object": verdict.ID, "direction": directionInput, "state": bad.ID})
			a.link(map[string]any{"process": either.ID, "object": verdict.ID, "direction": directionInput, "state": good.ID})
			a.link(map[string]any{"process": either.ID, "object": verdict.ID, "direction": directionInput, "state": bad.ID})
			a.startAt(f.ID, judge.ID)

			a.worker.onAgent("Judge", handsIn(data("verdict")))
			for _, n := range []string{"OnGood", "OnBad", "Either"} {
				a.worker.onAgent(n, corePlain(n))
			}

			d := a.finish(a.startRun(f.ID).ID)
			coreDone(t, d)
			var ran []string
			for _, s := range d.Steps {
				if s.ProcessName != "Judge" {
					ran = append(ran, s.ProcessName)
				}
			}
			slices.Sort(ran)
			if !slices.Equal(ran, c.ran) {
				t.Errorf("ran %v, want %v", ran, c.ran)
			}
			es := onlyStep(t, d, "Either")
			if len(es.Inputs) != 1 || es.Inputs[0].State != c.state {
				t.Errorf("Either took %+v, want Verdict %s", es.Inputs, c.state)
			}
			if got := d.Objects; len(got) != 1 || got[0].State != c.state {
				t.Errorf("run objects %+v, want Verdict %s", got, c.state)
			}
		})
	}
}

// "A link without a state takes the object whatever state it is in. Its
// process runs once every object it takes that way exists" (engine.go): so
// generating an object in a state runs the processes linked to it without one
// too.
func TestEnginePlainLinkTakesObjectInAnyState(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Plain link, stated object")
	judge := a.agent(f.ID, "Judge", "Judge.")
	verdict := a.newObject(f.ID, "Verdict")
	good := a.newState(verdict.ID, "good")
	a.link(map[string]any{"process": judge.ID, "object": verdict.ID, "direction": directionOutput, "state": good.ID})
	plain := a.agent(f.ID, "Plain", "Any state.")
	a.input(plain.ID, verdict.ID)
	a.startAt(f.ID, judge.ID)
	a.worker.onAgent("Judge", handsIn(data("verdict")))
	a.worker.onAgent("Plain", corePlain("plain"))

	d := a.finish(a.startRun(f.ID).ID)
	coreDone(t, d)
	ps := onlyStep(t, d, "Plain")
	if len(ps.Inputs) != 1 || ps.Inputs[0].State != "good" {
		t.Errorf("Plain took %+v, want Verdict good", ps.Inputs)
	}
}

// A process taking one object in a state and others without runs when the
// object enters the state, with those that exist, the conditioning one
// included, whichever way it was reached first.
func TestEngineStateLinkWithPlainInputs(t *testing.T) {
	for i := range 5 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("State plus plain")
			judge := a.agent(f.ID, "Judge", "Judge.")
			verdict := a.newObject(f.ID, "Verdict")
			good := a.newState(verdict.ID, "good")
			notes := a.newObject(f.ID, "Notes")
			combine := a.agent(f.ID, "Combine", "Combine.")
			a.link(map[string]any{"process": judge.ID, "object": verdict.ID, "direction": directionOutput, "state": good.ID})
			a.output(judge.ID, notes.ID)
			a.link(map[string]any{"process": combine.ID, "object": verdict.ID, "direction": directionInput, "state": good.ID})
			a.input(combine.ID, notes.ID)
			a.startAt(f.ID, judge.ID)

			a.worker.onAgent("Judge", handsIn(map[string]any{"Verdict": data("ok"), "Notes": data("some notes")}))
			a.worker.onAgent("Combine", corePlain("combined"))

			d := a.finish(a.startRun(f.ID).ID)
			coreDone(t, d)
			cs := onlyStep(t, d, "Combine")
			if got := coreInputNames(cs); !slices.Equal(got, []string{"Notes", "Verdict"}) {
				t.Errorf("Combine took %v, want Notes and Verdict", got)
			}
		})
	}
}

// A process taking an object in a state that a direct connection reaches
// too takes the object when it is in that state, whether the connection or
// the object entering the state ran it first.
func TestEngineStateLinkWithConnection(t *testing.T) {
	for i := range 5 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow("State plus connection")
			judge := a.agent(f.ID, "Judge", "Judge.")
			verdict := a.newObject(f.ID, "Verdict")
			good := a.newState(verdict.ID, "good")
			act := a.agent(f.ID, "Act", "Act.")
			a.link(map[string]any{"process": judge.ID, "object": verdict.ID, "direction": directionOutput, "state": good.ID})
			a.link(map[string]any{"process": act.ID, "object": verdict.ID, "direction": directionInput, "state": good.ID})
			a.call("POST", "/api/connections", map[string]any{"source": judge.ID, "target": act.ID}, http.StatusCreated, nil)
			a.startAt(f.ID, judge.ID)

			a.worker.onAgent("Judge", handsIn(data("ok")))
			a.worker.onAgent("Act", corePlain("acted"))

			d := a.finish(a.startRun(f.ID).ID)
			coreDone(t, d)
			as := onlyStep(t, d, "Act")
			if len(as.Inputs) != 1 || as.Inputs[0].Name != "Verdict" || as.Inputs[0].State != "good" {
				t.Errorf("Act took %+v, want Verdict good", as.Inputs)
			}
		})
	}
}

// A direct connection runs its target right after its source succeeds, and
// not after it fails; a cycle of connections runs each process once.
func TestEngineConnections(t *testing.T) {
	t.Run("after success", func(t *testing.T) {
		a := newApp(t)
		f := a.newFlow("Connected")
		first := a.agent(f.ID, "First", "First.")
		second := a.agent(f.ID, "Second", "Second.")
		a.call("POST", "/api/connections", map[string]any{"source": first.ID, "target": second.ID}, http.StatusCreated, nil)
		a.startAt(f.ID, first.ID)
		a.worker.onAgent("First", corePlain("1"))
		a.worker.onAgent("Second", corePlain("2"))

		d := a.finish(a.startRun(f.ID).ID)
		coreDone(t, d)
		fs, ss := onlyStep(t, d, "First"), onlyStep(t, d, "Second")
		if fs.FinishedAt == nil || ss.StartedAt.Before(*fs.FinishedAt) {
			t.Errorf("Second started %v, before First finished %v", ss.StartedAt, fs.FinishedAt)
		}
	})
	t.Run("not after failure", func(t *testing.T) {
		a := newApp(t)
		f := a.newFlow("Connected failing")
		first := a.agent(f.ID, "First", "First.")
		second := a.agent(f.ID, "Second", "Second.")
		a.call("POST", "/api/connections", map[string]any{"source": first.ID, "target": second.ID}, http.StatusCreated, nil)
		a.startAt(f.ID, first.ID)
		a.worker.onAgent("First", func(context.Context, agentRun) runResult { return runResult{Error: "no"} })
		a.worker.onAgent("Second", corePlain("2"))

		d := a.finish(a.startRun(f.ID).ID)
		if d.Status != runFailed || len(stepsOf(d, "Second")) != 0 {
			t.Errorf("run %s with steps %s, want failed without Second", d.Status, describeSteps(d))
		}
	})
	t.Run("cycle", func(t *testing.T) {
		a := newApp(t)
		f := a.newFlow("Cycle")
		ps := []process{a.agent(f.ID, "A", "A."), a.agent(f.ID, "B", "B."), a.agent(f.ID, "C", "C.")}
		for i, p := range ps {
			next := ps[(i+1)%len(ps)]
			a.call("POST", "/api/connections", map[string]any{"source": p.ID, "target": next.ID}, http.StatusCreated, nil)
			a.worker.onAgent(p.Name, corePlain(p.Name))
		}
		a.startAt(f.ID, ps[0].ID)

		d := a.finish(a.startRun(f.ID).ID)
		coreDone(t, d)
		for _, p := range ps {
			onlyStep(t, d, p.Name)
		}
	})
	t.Run("cycle through objects", func(t *testing.T) {
		a := newApp(t)
		f := a.newFlow("Object cycle")
		ping := a.agent(f.ID, "Ping", "Ping.")
		pong := a.agent(f.ID, "Pong", "Pong.")
		ball := a.newObject(f.ID, "Ball")
		back := a.newObject(f.ID, "Back")
		a.output(ping.ID, ball.ID)
		a.input(pong.ID, ball.ID)
		a.output(pong.ID, back.ID)
		a.input(ping.ID, back.ID)
		a.startAt(f.ID, ping.ID)
		a.worker.onAgent("Ping", handsIn(data("ping")))
		a.worker.onAgent("Pong", handsIn(data("pong")))

		d := a.finish(a.startRun(f.ID).ID)
		coreDone(t, d)
		onlyStep(t, d, "Ping")
		onlyStep(t, d, "Pong")
	})
}

// What's wrong with a process itself fails its step, saying why, without
// asking the worker.
func TestEngineStepErrors(t *testing.T) {
	for _, c := range []struct {
		name    string
		build   func(a *app, f int64) process
		worker  func(a *app)
		message string
		asked   bool
	}{
		{
			name: "agent without a prompt",
			build: func(a *app, f int64) process {
				return a.agent(f, "P", "  ")
			},
			message: "the process has no prompt",
		},
		{
			name: "script without a script",
			build: func(a *app, f int64) process {
				return a.newProcess(f, map[string]any{"name": "P", "kind": kindScript, "script": "\n"})
			},
			message: "the process has no script",
		},
		{
			name: "script output off the interface",
			build: func(a *app, f int64) process {
				p := a.newProcess(f, map[string]any{"name": "P", "kind": kindScript, "script": "echo hi"})
				a.output(p.ID, a.newObject(f, "Out").ID)
				return p
			},
			worker: func(a *app) {
				a.worker.onScript("P", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: "hi"} })
			},
			message: "The output didn't follow the interface of what the process generates",
			asked:   true,
		},
		{
			name: "script exiting non-zero",
			build: func(a *app, f int64) process {
				return a.newProcess(f, map[string]any{"name": "P", "kind": kindScript, "script": "exit 3"})
			},
			worker: func(a *app) {
				a.worker.onScript("P", func(context.Context, scriptRun) runResult {
					return runResult{Output: "partial", Error: "exit status 3"}
				})
			},
			message: "exit status 3",
			asked:   true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newApp(t)
			f := a.newFlow(c.name)
			p := c.build(a, f.ID)
			a.startAt(f.ID, p.ID)
			if c.worker != nil {
				c.worker(a)
			}
			d := a.finish(a.startRun(f.ID).ID)
			if d.Status != runFailed {
				t.Fatalf("run %s, want failed; steps %s", d.Status, describeSteps(d))
			}
			s := onlyStep(t, d, "P")
			if s.Status != stepError || !strings.Contains(s.Error, c.message) {
				t.Errorf("step %s %q, want error with %q", s.Status, s.Error, c.message)
			}
			a.worker.mu.Lock()
			asked := len(a.worker.agentRuns)+len(a.worker.scriptRuns) > 0
			a.worker.mu.Unlock()
			if asked != c.asked {
				t.Errorf("worker asked: %v, want %v", asked, c.asked)
			}
			if len(d.Objects) != 0 {
				t.Errorf("the failed step generated %+v", d.Objects)
			}
		})
	}
}

// A worker that can't be reached, or refuses, fails the step saying so.
func TestEngineWorkerUnavailable(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "worker overloaded", http.StatusServiceUnavailable)
	}))
	t.Cleanup(refusing.Close)
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	for _, c := range []struct {
		name, url, message string
	}{
		{"refusing", refusing.URL, "worker unavailable: status 503: worker overloaded"},
		{"unreachable", gone.URL, "worker unavailable: "},
	} {
		for _, kind := range []string{kindAgent, kindScript} {
			t.Run(c.name+" "+kind, func(t *testing.T) {
				a := coreAppWithWorker(t, c.url)
				f := a.newFlow("Unavailable")
				p := a.newProcess(f.ID, map[string]any{"name": "P", "kind": kind, "prompt": "Go.", "script": "echo go"})
				a.startAt(f.ID, p.ID)
				d := a.finish(a.startRun(f.ID).ID)
				if d.Status != runFailed {
					t.Fatalf("run %s, want failed", d.Status)
				}
				if s := onlyStep(t, d, "P"); s.Status != stepError || !strings.HasPrefix(s.Error, c.message) {
					t.Errorf("step %s %q, want error starting %q", s.Status, s.Error, c.message)
				}
			})
		}
	}
}

// Stopping one step fails it and ends its branch; the rest of the run goes
// on. Only a step going can be stopped.
func TestEngineStopOneStep(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Stop a step")
	seed := a.agent(f.ID, "Seed", "Seed.")
	s := a.newObject(f.ID, "S")
	slow := a.agent(f.ID, "Slow", "Slow.")
	fast := a.agent(f.ID, "Fast", "Fast.")
	slowOut := a.newObject(f.ID, "SlowOut")
	after := a.agent(f.ID, "AfterSlow", "After slow.")
	a.output(seed.ID, s.ID)
	a.input(slow.ID, s.ID)
	a.input(fast.ID, s.ID)
	a.output(slow.ID, slowOut.ID)
	a.input(after.ID, slowOut.ID)
	a.startAt(f.ID, seed.ID)

	g := newCoreGate()
	a.worker.onAgent("Seed", handsIn(data("seed")))
	a.worker.onAgent("Slow", g.agent("Slow", submitted(data("late"))))
	a.worker.onAgent("Fast", corePlain("fast"))
	a.worker.onAgent("AfterSlow", corePlain("after"))

	rn := a.startRun(f.ID)
	g.wait(t, "Slow")
	step := a.waitStep(rn.ID, "Slow", stepActive)
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/stop", rn.ID, step.ID+1000), nil, http.StatusConflict, "no step going")
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/stop", rn.ID+1000, step.ID), nil, http.StatusConflict, "no step going")
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/x/stop", rn.ID), nil, http.StatusBadRequest, "invalid id")
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/stop", rn.ID, step.ID), nil, http.StatusNoContent, nil)

	d := a.finish(rn.ID)
	if d.Status != runFailed {
		t.Fatalf("run %s, want failed; steps %s", d.Status, describeSteps(d))
	}
	if s := onlyStep(t, d, "Slow"); s.Status != stepError || s.Error != "it was stopped" {
		t.Errorf("Slow = %s %q, want stopped", s.Status, s.Error)
	}
	if s := onlyStep(t, d, "Fast"); s.Status != stepDone {
		t.Errorf("Fast = %s", s.Status)
	}
	if n := len(stepsOf(d, "AfterSlow")); n != 0 {
		t.Errorf("AfterSlow ran %d times after Slow was stopped", n)
	}
	a.refused("POST", fmt.Sprintf("/api/runs/%d/steps/%d/stop", rn.ID, step.ID), nil, http.StatusConflict, "no step going")
}

// Inputs go into the prompt each tagged with its name and state, escaped, and
// the step records them as given.
func TestEngineInputsInPrompt(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Escaping")
	mk := a.agent(f.ID, "Make", "Make.")
	weird := a.newObject(f.ID, `Bug "A" <b>&`)
	st := a.newState(weird.ID, `"done" & <ok>`)
	use := a.agent(f.ID, "Use", "Use it.")
	a.link(map[string]any{"process": mk.ID, "object": weird.ID, "direction": directionOutput, "state": st.ID})
	a.link(map[string]any{"process": use.ID, "object": weird.ID, "direction": directionInput, "state": st.ID})
	a.startAt(f.ID, mk.ID)
	a.worker.onAgent("Make", handsIn(data("<content> stays & raw")))
	a.worker.onAgent("Use", corePlain("used"))

	d := a.finish(a.startRun(f.ID).ID)
	coreDone(t, d)
	us := onlyStep(t, d, "Use")
	if len(us.Inputs) != 1 || us.Inputs[0].Name != weird.Name || us.Inputs[0].State != st.Name || !strings.Contains(us.Inputs[0].Content, "stays") {
		t.Errorf("Use recorded inputs %+v", us.Inputs)
	}
	want := `<input name="Bug &#34;A&#34; &lt;b&gt;&amp;" state="&#34;done&#34; &amp; &lt;ok&gt;">`
	// The content goes in as the object holds it, unescaped.
	if !strings.HasPrefix(us.Prompt, "Use it.\n\n") || !strings.Contains(us.Prompt, want+"\n"+us.Inputs[0].Content+"\n</input>") {
		t.Errorf("Use's prompt lacks %s:\n%s", want, us.Prompt)
	}
	if runs := a.worker.runsOf("Use"); len(runs) != 1 || runs[0].Prompt != us.Prompt || runs[0].ProcessID != use.ID {
		t.Errorf("the worker got %+v, want the recorded prompt", runs)
	}
}

// A script gets its inputs as files rather than in its script, and the step
// records the script as its prompt.
func TestEngineScriptInputs(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Script inputs")
	mk := a.agent(f.ID, "Make", "Make.")
	one := a.newObject(f.ID, "One")
	two := a.newObject(f.ID, "Two")
	script := "cat \"$INPUTS/One\" \"$INPUTS/Two\""
	use := a.newProcess(f.ID, map[string]any{"name": "Use", "kind": kindScript, "script": script})
	a.output(mk.ID, one.ID)
	a.output(mk.ID, two.ID)
	a.input(use.ID, one.ID)
	a.input(use.ID, two.ID)
	a.startAt(f.ID, mk.ID)
	a.worker.onAgent("Make", handsIn(map[string]any{"One": data(1), "Two": data(2)}))
	a.worker.onScript("Use", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: "12"} })

	d := a.finish(a.startRun(f.ID).ID)
	coreDone(t, d)
	us := onlyStep(t, d, "Use")
	if us.Prompt != script || us.Output != "12" {
		t.Errorf("Use step prompt %q output %q", us.Prompt, us.Output)
	}
	a.worker.mu.Lock()
	runs := a.worker.scriptRuns
	a.worker.mu.Unlock()
	if len(runs) != 1 || runs[0].Script != script || len(runs[0].Inputs) != 2 {
		t.Fatalf("the worker ran %+v", runs)
	}
	var names []string
	for _, in := range runs[0].Inputs {
		names = append(names, in.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"One", "Two"}) {
		t.Errorf("script inputs %v", names)
	}
}

// No more steps than the engine has slots for go at once; the rest wait.
func TestEngineParallelStepsLimit(t *testing.T) {
	a := newApp(t) // 3 slots
	f := a.newFlow("Slots")
	seed := a.agent(f.ID, "Seed", "Seed.")
	s := a.newObject(f.ID, "S")
	a.output(seed.ID, s.ID)
	a.startAt(f.ID, seed.ID)
	a.worker.onAgent("Seed", handsIn(data("seed")))

	var mu sync.Mutex
	going, most := 0, 0
	arrived := make(chan struct{}, 8)
	release := make(chan struct{})
	for i := range 4 {
		p := a.agent(f.ID, fmt.Sprintf("W%d", i), "Work.")
		a.input(p.ID, s.ID)
		a.worker.onAgent(p.Name, func(ctx context.Context, _ agentRun) runResult {
			mu.Lock()
			going++
			most = max(most, going)
			mu.Unlock()
			arrived <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			mu.Lock()
			going--
			mu.Unlock()
			return runResult{OK: true, Output: "done"}
		})
	}

	rn := a.startRun(f.ID)
	for range 3 {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("three steps never went at once")
		}
	}
	// The fourth waits for a slot: it isn't asked for, nor recorded.
	select {
	case <-arrived:
		t.Fatal("a fourth step went while three were going")
	case <-time.After(300 * time.Millisecond):
	}
	d := a.getRun(rn.ID)
	if n := len(d.Steps); n != 4 { // Seed and three workers
		t.Errorf("%d steps recorded while three went, want 4: %s", n, describeSteps(d))
	}
	close(release)
	d = a.finish(rn.ID)
	coreDone(t, d)
	if len(d.Steps) != 5 || most != 3 {
		t.Errorf("%d steps, at most %d at once; want 5 and 3", len(d.Steps), most)
	}
}

// A run walks the board as it was when it started: what is added to the
// board meanwhile doesn't run.
func TestEngineRunWalksTheBoardItStartedOn(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Snapshot")
	first := a.agent(f.ID, "First", "First.")
	out := a.newObject(f.ID, "Out")
	a.output(first.ID, out.ID)
	a.startAt(f.ID, first.ID)

	g := newCoreGate()
	a.worker.onAgent("First", g.agent("First", submitted(data("first"))))
	a.worker.onAgent("Late", corePlain("late"))

	rn := a.startRun(f.ID)
	g.wait(t, "First")
	late := a.agent(f.ID, "Late", "Late.")
	a.input(late.ID, out.ID)
	a.call("POST", "/api/connections", map[string]any{"source": first.ID, "target": late.ID}, http.StatusCreated, nil)
	close(g.release)

	d := a.finish(rn.ID)
	coreDone(t, d)
	if n := len(stepsOf(d, "Late")); n != 0 {
		t.Errorf("Late, added mid-run, ran %d times", n)
	}
	// The next run is on the board as it is now, saved as a new version.
	rn2 := a.startRun(f.ID)
	if rn2.Version == nil || *rn2.Version == *rn.Version {
		t.Errorf("the second run is on version %v, the first's was %v", rn2.Version, rn.Version)
	}
	a.worker.onAgent("First", handsIn(data("first")))
	d2 := a.finish(rn2.ID)
	coreDone(t, d2)
	onlyStep(t, d2, "Late")
}

// The runs API: listing a flow's runs newest first, and what it refuses.
func TestEngineRunsAPI(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Runs API")
	p := a.agent(f.ID, "P", "Go.")
	a.startAt(f.ID, p.ID)
	a.worker.onAgent("P", corePlain("ok"))

	first := a.startRun(f.ID)
	a.finish(first.ID)
	second := a.startRun(f.ID)
	a.finish(second.ID)
	other := a.newFlow("Other runs")
	a.finish(a.startRun(other.ID).ID)

	var rs []run
	a.call("GET", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusOK, &rs)
	if len(rs) != 2 || rs[0].ID != second.ID || rs[1].ID != first.ID {
		t.Errorf("runs %+v, want %d then %d", rs, second.ID, first.ID)
	}
	for _, r := range rs {
		if r.Flow != f.ID || r.Status != runDone || r.FinishedAt == nil || r.Parent != nil {
			t.Errorf("listed run %+v", r)
		}
	}
	var fl flow
	a.call("GET", fmt.Sprintf("/api/flows/%d", f.ID), nil, http.StatusOK, &fl)
	if fl.LastRun == nil || fl.LastRun.ID != second.ID {
		t.Errorf("flow's last run %+v, want %d", fl.LastRun, second.ID)
	}

	a.refused("GET", "/api/runs", nil, http.StatusBadRequest, "flow is required")
	a.refused("GET", "/api/runs/1099511627776", nil, http.StatusNotFound, "run not found")
	a.refused("GET", "/api/runs/abc", nil, http.StatusBadRequest, "invalid id")
	a.refused("POST", "/api/runs?flow=1099511627776", nil, http.StatusNotFound, "flow not found")
	a.refused("POST", "/api/runs", nil, http.StatusBadRequest, "flow is required")
	a.refused("PATCH", "/api/runs/abc", map[string]any{"status": runInterrupted}, http.StatusBadRequest, "invalid id")
	a.refused("PATCH", "/api/runs/1099511627776", map[string]any{"status": runInterrupted}, http.StatusNotFound, "run not found")

	// Stopping a run that ended keeps how it ended.
	var ended run
	a.call("PATCH", fmt.Sprintf("/api/runs/%d", first.ID), map[string]any{"status": runInterrupted}, http.StatusOK, &ended)
	if ended.Status != runDone {
		t.Errorf("stopping a done run made it %s", ended.Status)
	}
}
