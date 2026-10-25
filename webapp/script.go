package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"strings"
)

// Script processes run in the worker with bash, which has curl and jq to
// fetch from public APIs. Each object the script takes is a file in
// the directory $INPUTS points to, named after the object, and what it prints
// is its output, which must follow the interface of what it generates as an
// agent's output does: {"data": ...} for an object without attributes, a
// JSON array for a list. Files it generates go in the directory $OUTPUTS
// points to, with their paths in its output. A non-zero exit fails the step.
// In a scheduled run, $SINCE and its alias $LAST_RUN say when the schedule
// last got through, so the script can fetch only what changed since. What
// it prints is kept as it prints it, for reading; see script_output.go.

// What the worker runs.
type scriptRun struct {
	// The run the step is of, whose home the worker gives every step of it.
	Run int64 `json:"run,omitempty"`
	// Identify the process in the worker's logs.
	ProcessID   int64         `json:"processId"`
	ProcessName string        `json:"processName"`
	Script      string        `json:"script"`
	Inputs      []scriptInput `json:"inputs"`
	// Where the script writes the files it generates, which $OUTPUTS points to.
	FilesDir string `json:"filesDir,omitempty"`
	// The workspace it works in, its working directory, if it works on
	// repositories; $WORKSPACE and $BRANCH say where and on which branch.
	Workspace *workspaceSpec `json:"workspace,omitempty"`
	// The most disk the run may take, in bytes; see agentRun.
	DiskLimit int64 `json:"diskLimit,omitempty"`
	// The token the step's git reaches its repositories with; see
	// git_tokens.go.
	Git *gitCredentials `json:"git,omitempty"`
	// Where the script's container reaches the internet hosts it may; see
	// egress.go.
	Egress *egressAccess `json:"egress,omitempty"`
	// How long the script may take, if the process has a timeout; the
	// worker's own limit otherwise.
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
	// When its schedule's last run that got through started, as a Unix
	// timestamp, which $SINCE and $LAST_RUN hold; see scheduleSince.
	Since *int64 `json:"since,omitempty"`
}

type scriptInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Executes a script process as a step of the run, which records its script
// as the step's prompt. A change request has nothing to go to. started gets
// the step's id once it is recorded.
func runScriptStep(ctx context.Context, db *sql.DB, workerURL string, ws *runWorkspace, runID, id int64, inputs []runInput, started func(int64)) (runResult, error) {
	var res runResult
	run := scriptRun{Run: runID, ProcessID: id, Inputs: []scriptInput{}}
	var settings processSettings
	err := db.QueryRowContext(ctx, "SELECT name, script, settings FROM processes WHERE id = ?", id).Scan(&run.ProcessName, &run.Script, &settings)
	if errors.Is(err, sql.ErrNoRows) {
		return res, errNotFound
	}
	if err != nil {
		return res, err
	}
	for _, in := range inputs {
		run.Inputs = append(run.Inputs, scriptInput{Name: in.Name, Content: in.Content})
	}
	contract, msg, err := contractFor(ctx, db, id, true)
	if err != nil {
		return res, err
	}
	switch {
	case strings.TrimSpace(run.Script) == "":
		res.Error = "the process has no script"
	case msg != "":
		res.Error = msg
	default:
		// One file in $INPUTS would hide the other.
		res.Error = inputClash(run.ProcessName, inputs)
	}

	run.Workspace = ws.forProcess(settings)
	run.DiskLimit = currentLimits().diskBytes()
	run.TimeoutMS = settings.timeout().Milliseconds()
	if run.Since, err = scheduleSince(ctx, db, runID); err != nil {
		return res, err
	}
	stepID, err := startStep(ctx, db, runID, id, stepActive, run.Script, inputs, nil)
	if err != nil {
		return res, err
	}
	started(stepID)
	if run.Workspace != nil {
		run.Workspace.Step = stepID
	}
	if err := recordBranch(ctx, db, stepID, run.Workspace); err != nil {
		return res, err
	}
	if contract != nil {
		contract.filesDir = stepFilesDir(runID, stepID)
		run.FilesDir = contract.filesDir
	}
	if res.Error == "" && run.Workspace != nil {
		if run.Git, err = gitTokens.forWorkspace(ctx, run.Workspace); err != nil {
			res.Error = err.Error()
		}
	}
	if res.Error == "" {
		who, err := runUser(ctx, db, runID)
		if err != nil {
			return res, err
		}
		run.Egress = egress.grant(who, runID, stepID, run.Workspace, settings.timeout())
		defer egress.revoke(run.Egress)
		res, err = streamScript(ctx, workerURL, run, func(stream, text string) {
			recordOutput(db, stepID, stream, text)
		}, func(l workerLogLine) {
			recordWorkerLog(db, stepID, []workerLogLine{l})
		})
		if err != nil {
			res = runResult{Error: "worker unavailable: " + err.Error()}
			if ctx.Err() != nil {
				res.Error = stopReason(ctx)
			}
		}
		if err := recordCommits(context.Background(), db, stepID, res.Commits); err != nil {
			log.Printf("record commits of step %d: %v", stepID, err)
		}
		if contract != nil && res.OK {
			// Unlike an agent, a script gives the same reply when asked again.
			out, objects, err := contract.parse(res.Output)
			if err != nil {
				res = runResult{Output: res.Output, Error: "The output didn't follow the interface of what the process generates: " + err.Error() + "."}
			} else {
				res = runResult{OK: true, Output: out, Objects: objects}
			}
		}
	}
	status := stepError
	if res.OK {
		status = stepDone
	}
	// The run may have been stopped, but the step still has to end.
	res.Duplicate, err = finishStep(context.Background(), db, stepID, status, res.Output, res.Error, res.Objects)
	return res, err
}
