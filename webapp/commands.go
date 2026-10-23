package main

// Commands: what only the replica owning a run, an assistant's reply or a
// follow-up can do to it (replicas.go), asked of it by another replica a
// request reached. Stopping a run, failing it, stopping one of its steps,
// letting it go on with a review's decision, stopping a reply or a
// follow-up: each cancels what the owner holds in its memory. A replica
// does what it owns itself; for the rest it leaves a command for the
// owner in replica_commands, which each replica reads every quarter of a
// second, taking each of its own once. What the request records, such as
// a run ended or a review decided, is recorded first, wherever it
// arrives; the command only lets the owner act on it.
//
// A command for a replica that stopped is dropped with it: what it owned
// is ended then anyway.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"
)

// What a command asks.
const (
	commandStopRun      = "stop-run"
	commandFailRun      = "fail-run"
	commandStopStep     = "stop-step"
	commandDecide       = "decide"
	commandStopReply    = "stop-reply"
	commandStopFollowUp = "stop-followup"
)

// A command as its owner gets it: what to act on, the run, step, flow or
// follow-up by id, and what else it needs, as JSON.
type command struct {
	kind    string
	target  int64
	step    int64
	payload []byte
}

// What a replica does with each kind of command, set as its parts start.
var commandHandlers = map[string]func(command){}

// Leaves the command for the replica owning what it acts on.
func sendCommand(ctx context.Context, db *sql.DB, owner, kind string, target, step int64, payload any) error {
	var data []byte
	if payload != nil {
		var err error
		if data, err = json.Marshal(payload); err != nil {
			return err
		}
	}
	_, err := db.ExecContext(ctx, "INSERT INTO replica_commands (replica_id, kind, target_id, step_id, payload) VALUES (?, ?, ?, ?, ?)",
		owner, kind, target, step, data)
	return err
}

// The replica alive owning the row of table with the id, "" for none; for
// a row this replica owns, its own id.
func ownerOf(ctx context.Context, db *sql.DB, table string, id int64) (string, error) {
	var owner string
	err := db.QueryRowContext(ctx, "SELECT t.replica_id FROM "+table+" t JOIN replicas r ON r.id = t.replica_id WHERE t.id = ?", id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner, err
}

// Takes this replica's commands and does them, every quarter of a second,
// until ctx ends.
func takeCommands(ctx context.Context, db *sql.DB) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		type taken struct {
			id int64
			command
		}
		var cmds []taken
		err := scanAll(ctx, db, "SELECT id, kind, target_id, step_id, payload FROM replica_commands WHERE replica_id = ? ORDER BY id", func(rows *sql.Rows) error {
			var c taken
			if err := rows.Scan(&c.id, &c.kind, &c.target, &c.step, &c.payload); err != nil {
				return err
			}
			cmds = append(cmds, c)
			return nil
		}, replicaID)
		if err != nil {
			log.Printf("WARNING: couldn't read this replica's commands: %v", err)
			continue
		}
		for _, c := range cmds {
			res, err := db.ExecContext(ctx, "DELETE FROM replica_commands WHERE id = ?", c.id)
			if err != nil {
				log.Printf("WARNING: couldn't take command %d: %v", c.id, err)
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			if handle := commandHandlers[c.kind]; handle != nil {
				handle(c.command)
			} else {
				log.Printf("WARNING: a command this replica doesn't know: %s", c.kind)
			}
		}
	}
}
