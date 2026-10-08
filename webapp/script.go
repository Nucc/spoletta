package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// Script processes run in the worker with bash, which has curl and jq to
// fetch from APIs such as Sentry's. Each object the script takes is a file in
// the directory $INPUTS points to, named after the object, and what it prints
// is its output, which must follow the interface of what it generates as an
// agent's output does: {"data": ...} for an object without attributes, a
// JSON array for a list. Files it generates go in the directory $OUTPUTS
// points to, with their paths in its output. A non-zero exit fails the step.

// What the worker runs.
type scriptRun struct {
	// Identify the process in the worker's logs.
	ProcessID   int64         `json:"processId"`
	ProcessName string        `json:"processName"`
	Script      string        `json:"script"`
	Inputs      []scriptInput `json:"inputs"`
	// Where the script writes the files it generates, which $OUTPUTS points to.
	FilesDir string `json:"filesDir,omitempty"`
}

type scriptInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Executes a script process as a step of the run, which records its script
// as the step's prompt. A change request has nothing to go to.
func runScriptStep(ctx context.Context, db *sql.DB, workerURL string, runID, id int64, inputs []runInput) (runResult, error) {
	var res runResult
	run := scriptRun{ProcessID: id, Inputs: []scriptInput{}}
	err := db.QueryRowContext(ctx, "SELECT name, script FROM processes WHERE id = ?", id).Scan(&run.ProcessName, &run.Script)
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
	}

	stepID, err := startStep(ctx, db, runID, id, stepActive, run.Script, inputs, nil)
	if err != nil {
		return res, err
	}
	if contract != nil {
		contract.filesDir = stepFilesDir(runID, stepID)
		run.FilesDir = contract.filesDir
	}
	if res.Error == "" {
		if err := callWorker(ctx, http.MethodPost, workerURL+"/script", run, &res); err != nil {
			res = runResult{Error: "worker unavailable: " + err.Error()}
			if ctx.Err() != nil {
				res.Error = "the run was cancelled"
			}
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
