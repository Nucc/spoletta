package main

// Every row of a board is its flow's project's: its processes, objects,
// states, attributes, links and the states they decide between,
// connections, starts and what they point at, flow processes' boards,
// notes, samples, schedules, versions and their roles, history, and who has
// it open and changed it (see projects.go). Each is written naming the
// project, taken from what it belongs to, its flow, process or object, and
// never from what a version's or a step's JSON, a clipboard or a setup file
// says, so a board brought back from one is its flow's project's whatever
// it holds; the keys naming the project refuse a row in another project
// than its parent (migrations 00053 and 00065).
//
// Nor does anything on a board refer to another project: a subflow
// process's procedure, a copy's source and store, the object an object
// specializes, a reference attribute's struct, what a note points at, and
// the MCP servers, skills, credentials and connector a process picks. A
// route writing one refuses it, as if it weren't there; bringing a board
// back (versions, history, setup files, the assistant) and pasting leave it
// out instead, as they do one that is gone. A struct's store and the struct
// a reference refers to have no key, as they outlive the struct deleted for
// good (shared.go), so the code alone keeps them to the project.
//
// The keys of what a board's rows refer to among themselves, a copy's
// source, an object's general and what a note points at, take no action as
// what they refer to goes, since MySQL can't null a key naming project_id,
// which is never null. Whatever deletes processes or objects lets go of
// them first (releaseGoing), as their keys did before projects.

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// The ids a query gives.
func idsOf(ctx context.Context, db rowsQuerier, query string, args ...any) ([]int64, error) {
	var ids []int64
	err := scanAll(ctx, db, query, func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		ids = append(ids, id)
		return err
	}, args...)
	return ids, err
}

// How many ids a statement names at most, well under MySQL's limit of
// placeholders.
const idsAtOnce = 1000

// Runs fn on ids a share of idsAtOnce at a time.
func inShares(ids []int64, fn func([]int64) error) error {
	for len(ids) > 0 {
		n := min(len(ids), idsAtOnce)
		if err := fn(ids[:n]); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

// Of the ids of rows of table, those of rows in another project than
// project; an id of no row is none of them, as one of what is gone.
func elsewhere(ctx context.Context, db rowsQuerier, table string, project int64, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	err := inShares(ids, func(share []int64) error {
		found, err := idsOf(ctx, db, "SELECT id FROM "+table+" WHERE project_id <> ? AND id IN "+placeholders(len(share)),
			append([]any{project}, int64Args(share)...)...)
		for _, id := range found {
			out[id] = true
		}
		return err
	})
	return out, err
}

// Whether an object is another project's than project; one that is gone
// isn't.
func objectElsewhere(ctx context.Context, db rowsQuerier, project int64, id *int64) (bool, error) {
	if id == nil {
		return false, nil
	}
	out, err := elsewhere(ctx, db, "objects", project, []int64{*id})
	return out[*id], err
}

// What processes' settings name of another project than project: the
// procedure a subflow process runs, the MCP servers and tools it uses, its
// skills, credentials and connector. Says what, for a route to refuse; with
// drop, it is left out of them instead, a connector keeping its operation
// and arguments for one picked anew, as restoring and pasting do. A query a
// kind, however many processes.
func settingsElsewhere(ctx context.Context, db rowsQuerier, project int64, drop bool, ss ...*processSettings) (string, error) {
	var flows, servers, skills, credentials []int64
	for _, s := range ss {
		if s.Flow != nil {
			flows = append(flows, *s.Flow)
		}
		servers = append(servers, s.MCPServers...)
		for id := range s.MCPTools {
			servers = append(servers, id)
		}
		if s.Connector != nil && s.Connector.Server != 0 {
			servers = append(servers, s.Connector.Server)
		}
		skills = append(skills, s.Skills...)
		credentials = append(credentials, s.Credentials...)
	}
	out := map[string]map[int64]bool{}
	for _, ref := range []struct {
		table, what string
		ids         []int64
	}{
		{"flows", "procedure", flows},
		{"mcp_servers", "MCP server", servers},
		{"skills", "skill", skills},
		{"credentials", "credential", credentials},
	} {
		found, err := elsewhere(ctx, db, ref.table, project, ref.ids)
		if err != nil {
			return "", err
		}
		if len(found) > 0 && !drop {
			return fmt.Sprintf("no such %s", ref.what), nil
		}
		out[ref.table] = found
	}
	in := func(table string) func(int64) bool {
		return func(id int64) bool { return out[table][id] }
	}
	for _, s := range ss {
		if s.Flow != nil && out["flows"][*s.Flow] {
			s.Flow = nil
		}
		s.MCPServers = slices.DeleteFunc(s.MCPServers, in("mcp_servers"))
		for id := range s.MCPTools {
			if out["mcp_servers"][id] {
				delete(s.MCPTools, id)
			}
		}
		if s.Connector != nil && out["mcp_servers"][s.Connector.Server] {
			s.Connector.Server = 0
		}
		s.Skills = slices.DeleteFunc(s.Skills, in("skills"))
		s.Credentials = slices.DeleteFunc(s.Credentials, in("credentials"))
	}
	return "", nil
}

// Lets go of what refers to the processes and objects a delete is to take,
// as their keys did before they named the project (see the top of this
// file): copies of the objects become objects of their own, objects
// specializing them specialize nothing, and notes pointing at either point
// at nothing. A delete takes the processes and objects named, everything on
// the boards of those processes, in turn, and every object making up one of
// those objects. What refers to them from rows going too is let go of as
// well, since MySQL checks each row as it deletes it, not once the
// statement is done.
func releaseGoing(ctx context.Context, tx *sql.Tx, processes, objects []int64) error {
	if len(processes) > 0 {
		var err error
		processes, err = idsOf(ctx, tx, `WITH RECURSIVE going (id) AS (
				SELECT id FROM processes WHERE id IN `+placeholders(len(processes))+`
				UNION SELECT p.id FROM processes p JOIN going g ON p.parent_id = g.id)
			SELECT id FROM going`, int64Args(processes)...)
		if err != nil {
			return err
		}
	}
	if len(processes)+len(objects) > 0 {
		var err error
		objects, err = idsOf(ctx, tx, `WITH RECURSIVE going (id) AS (
				SELECT id FROM objects WHERE id IN `+placeholders(len(objects))+` OR parent_id IN `+placeholders(len(processes))+`
				UNION SELECT o.id FROM objects o JOIN going g ON o.owner_id = g.id)
			SELECT id FROM going`, append(int64Args(objects), int64Args(processes)...)...)
		if err != nil {
			return err
		}
	}
	for _, release := range []struct {
		stmt string
		ids  []int64
	}{
		{"UPDATE objects SET source_id = NULL WHERE source_id IN ", objects},
		{"UPDATE objects SET general_id = NULL WHERE general_id IN ", objects},
		{"UPDATE notes SET object_id = NULL WHERE object_id IN ", objects},
		{"UPDATE notes SET process_id = NULL WHERE process_id IN ", processes},
	} {
		err := inShares(release.ids, func(share []int64) error {
			_, err := tx.ExecContext(ctx, release.stmt+placeholders(len(share)), int64Args(share)...)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
