package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A run is its flow's project's (see projects.go): it is recorded there, as
// is everything it writes, its steps, objects, logs, records and what it
// stores, and it reaches nothing of another project. The project goes with
// the run from where it starts (newRun, startChildRun) into what walks it
// (flowRun, runBoard) and what the worker is asked (workspaceSpec), so
// nothing about it is kept only in a replica's memory: a replica taking a
// run over reads it again from the run (adopt.go).
//
// A board somehow naming another project's procedure, struct, store or
// record, which editing it never lets it, doesn't make a run reach it: the
// step, or the run, fails saying so. An archived project starts no runs, by
// hand, on a schedule, over the API, from a webhook or a task board.
//
// What is started outside a request, by an API key, a webhook or a task
// board playing, is for whoever made it, pressed Play or added it, as they
// are in the project (memberAs): an owner there, an editor or a viewer,
// whatever they are in the instance.

// What starting a run of an archived project's flow fails with. It is
// sql.ErrNoRows too, so whatever answers a flow that isn't there answers it
// the same, as /v1 and /hooks must.
var errProjectArchived = fmt.Errorf("the project is archived, so it starts no runs: %w", sql.ErrNoRows)

// What starting the run of a flow another project's fails with.
var errOtherProject = errors.New("the flow it runs is another project's")

// Why a project starts no runs, as a run's log and a request say.
const archivedRuns = "the project is archived, so it starts no runs"

// The project the object is in; 0 when there is no such object.
func objectProject(ctx context.Context, db querier, object int64) (int64, error) {
	var project int64
	err := db.QueryRowContext(ctx, "SELECT project_id FROM objects WHERE id = ?", object).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return project, err
}

// Whether the project is archived; false for one that isn't there.
func projectArchived(ctx context.Context, db querier, project int64) (bool, error) {
	var archived bool
	err := db.QueryRowContext(ctx, "SELECT archived_at IS NOT NULL FROM projects WHERE id = ?", project).Scan(&archived)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return archived, err
}

// Someone by id as they are in the project, for what is done on their
// behalf outside a request of theirs: the runs of an API key they made, of
// a webhook they added, of a task board they pressed Play on. Why not, when
// they were removed, are no one, or may no longer enter the project; then
// the user is nil.
func memberAs(ctx context.Context, db *sql.DB, id, project int64) (*user, string, error) {
	u := &user{ID: id}
	var removed bool
	err := db.QueryRowContext(ctx, "SELECT email, name, admin, team_manager, removed_at IS NOT NULL FROM users WHERE id = ?", id).
		Scan(&u.Email, &u.Name, &u.Super, &u.TeamManager, &removed)
	switch {
	case errors.Is(err, sql.ErrNoRows) || err == nil && removed:
		return nil, "was removed", nil
	case err != nil:
		return nil, "", err
	}
	in, ok, err := userInProject(ctx, db, u, project)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "may no longer enter the project", nil
	}
	return in, "", nil
}
