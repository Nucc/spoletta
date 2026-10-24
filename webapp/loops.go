package main

// A loop's circuit breaker. A flow can lead back to a process that ran
// already: a person sending work back, an agent reviewing what another
// generated, a failure connection handling a failure by fixing what the
// failed process takes. A person's decision and a reviewer's rounds bound
// the first two; nothing bounds a failure connection leading back to what
// failed, so a misconfigured flow could go round for ever.
//
// So every pass keeps the pass it came from, back to the last a person
// started, and what each process in it took. A process about to run again
// in a pass that is neither a person's nor a reviewer's, having run in the
// passes before it that many times (its loops setting, defaultLoops when
// absent), or on just what it took in one of them, which gets nowhere,
// waits for someone who may edit the flow to say whether it goes on: a step
// of kind loop, asking, which the board shows as it does a review. Going on
// lets it run that many more times, and on what it took before, for the
// rest of the run; stopping ends its branch and fails the run, unhandled,
// since what handles its failure is likely the loop itself.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Steps asking whether a process goes on round a loop.
const kindLoop = "loop"

// How many times a process runs round a loop before someone is asked, by
// default, and at most whatever it says.
const (
	defaultLoops = 5
	maxLoops     = 100
)

func (s processSettings) loops() int {
	if s.Loops == nil {
		return defaultLoops
	}
	return *s.Loops
}

// A digest of what a process takes: its inputs and change request.
type inputsSum [sha256.Size]byte

func sumInputs(given []runInput, cr *changeRequest) inputsSum {
	b, _ := json.Marshal(struct {
		Inputs        []runInput     `json:"inputs"`
		ChangeRequest *changeRequest `json:"changeRequest"`
	}{given, cr})
	return sha256.Sum256(b)
}

// How many of the passes up to and including p, back to the last a person
// started, the process ran in counting toward its loops, and whether it took
// sum in one before p.
func (p *pass) lineage(id int64, sum inputsSum) (runs int, same bool) {
	for q := p; q != nil; q = q.parent {
		q.mu.Lock()
		// What it took is kept only when it ran, not when it was skipped.
		s, ran := q.inputs[id]
		if q.counts && ran {
			runs++
		}
		if ran && q != p && s == sum {
			same = true
		}
		q.mu.Unlock()
	}
	return runs, same
}

// Whether the process goes on, taking given and cr in pass p: at once, unless
// it goes round a loop as above, when it waits for someone to say. False if
// they stopped it, which fails the run, or the run ended meanwhile.
func (w *walk) goOn(id int64, p *pass, given []runInput, cr *changeRequest) bool {
	sum := sumInputs(given, cr)
	p.mu.Lock()
	p.inputs[id] = sum
	p.mu.Unlock()
	if !p.counts {
		return true
	}
	runs, same := p.lineage(id, sum)
	w.mu.Lock()
	limit := w.settings[id].loops() + w.allowed[id]
	trusted := w.trusted[id]
	w.mu.Unlock()
	name := strconv.Quote(w.names[id])
	var question string
	switch {
	case runs > limit:
		question = fmt.Sprintf("%s has run %d times round a loop with no person deciding in between, the most it may. Should it go on?", name, runs-1)
	case same && !trusted:
		question = fmt.Sprintf("%s would run again on just what it took before, so the loop it is in may be getting nowhere. Should it go on?", name)
	default:
		return true
	}
	runLog(w.e.db, w.run, 0, logInfo, "%s", question)
	_, d, ok := w.e.await(w.reviews, w.lr, w.run, id, given, question)
	if !ok {
		return false
	}
	if !d.goOn {
		w.fail()
		return false
	}
	w.mu.Lock()
	w.allowed[id] += w.settings[id].loops()
	w.trusted[id] = true
	w.mu.Unlock()
	return true
}

func registerLoopRoutes(mux *http.ServeMux, db *sql.DB, eng *engine) {
	// Answers a loop step: whether its process goes on round the loop, or
	// stops, which fails the run. Only those who may edit the flow answer;
	// see guardFlows.
	mux.HandleFunc("POST /api/runs/{run}/steps/{id}/loop", func(w http.ResponseWriter, r *http.Request) {
		runID, err1 := strconv.ParseInt(r.PathValue("run"), 10, 64)
		id, err2 := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err1 != nil || err2 != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			GoOn *bool `json:"goOn"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRunRequestBytes)).Decode(&body); err != nil || body.GoOn == nil {
			msg := "goOn is required"
			if err != nil {
				msg = "invalid JSON: " + err.Error()
			}
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		status, output, errMsg := stepDone, "It goes on.", ""
		if !*body.GoOn {
			status, output, errMsg = stepError, "", "Stopped going round the loop."
		}
		// Only a loop step of this run still waiting; answering one twice
		// would let the run go on twice.
		res, err := db.ExecContext(r.Context(),
			`UPDATE run_steps SET status = ?, output = ?, error = ?, decided_by = ?, finished_at = CURRENT_TIMESTAMP(3)
			WHERE id = ? AND run_id = ? AND kind = ? AND status = ?`,
			status, output, errMsg, requestUserID(r), id, runID, kindLoop, stepWaiting)
		if err != nil {
			serverError(w, err)
			return
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			if err != nil {
				serverError(w, err)
				return
			}
			http.Error(w, "no loop step waiting with that id in this run", http.StatusConflict)
			return
		}
		logStepFinished(db, id, true)
		eng.decide(runID, id, decision{goOn: *body.GoOn})
		w.WriteHeader(http.StatusNoContent)
	})
}

// Whether the user may answer the run's loop steps: whether they may edit
// its flow.
func mayAnswerLoops(ctx context.Context, db *sql.DB, u *user, flow int64) (bool, error) {
	access, err := flowAccess(ctx, db, u, flow)
	return access == accessEdit, err
}
