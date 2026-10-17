package main

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// A process tells the objects it is linked to apart by their names: a
// script finds each object it takes as a file in $INPUTS named after it, an
// agent finds it by name in its prompt, and what either generates is keyed
// by name. So no two objects a process takes may share a name, nor two it
// generates; one it takes and another it generates may, such as a ticket
// coming in and the updated ticket going out. Names are compared as the
// worker makes them file names, so Jira Ticket and Jira_Ticket are the same.
//
// Linking, renaming and importing a setup file refuse what would break
// this. A board that breaks it already, such as an older version restored,
// still loads; a step taking two objects of one name fails instead of one
// file hiding the other.

// Characters the worker leaves in an input's file name; others become
// underscores. Keep in step with runScript in worker/main.go.
var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// The name of the file a script finds an object it takes in, in $INPUTS.
func inputFileName(name string) string {
	n := strings.Trim(unsafeNameChars.ReplaceAllString(name, "_"), "._")
	if n == "" {
		return "input"
	}
	return n
}

func sameName(a, b string) bool {
	return inputFileName(a) == inputFileName(b)
}

func nameClashMessage(process, direction, name string) string {
	verb := "takes"
	if direction == directionOutput {
		verb = "generates"
	}
	return fmt.Sprintf("%s already %s an object named %s; rename one so it can tell them apart", quote(process), verb, quote(name))
}

// What's wrong with linking the object to the process in direction, if the
// process is linked that way to another object of the same name.
func linkClash(ctx context.Context, db *sql.DB, process, object int64, direction string) (string, error) {
	var pname, oname string
	err := db.QueryRowContext(ctx, "SELECT p.name, o.name FROM processes p JOIN objects o ON o.id = ? WHERE p.id = ?", object, process).Scan(&pname, &oname)
	if err != nil {
		return "", err
	}
	var clash string
	err = scanAll(ctx, db, `SELECT DISTINCT o.name FROM object_links l JOIN objects o ON o.id = l.object_id
		WHERE l.process_id = ? AND l.direction = ? AND l.object_id <> ?`,
		func(rows *sql.Rows) error {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			if clash == "" && sameName(n, oname) {
				clash = nameClashMessage(pname, direction, n)
			}
			return nil
		}, process, direction, object)
	return clash, err
}

// What's wrong with naming the object name, if a process linked to it, or
// to a copy of it that follows its name, is linked the same way to another
// object of that name.
func renameClash(ctx context.Context, db *sql.DB, object int64, name string) (string, error) {
	renamed := map[int64]bool{object: true}
	ids := []int64{object}
	for next := ids; len(next) > 0; {
		var copies []int64
		err := scanAll(ctx, db, "SELECT id FROM objects WHERE source_id IN "+placeholders(len(next)),
			func(rows *sql.Rows) error {
				var id int64
				if err := rows.Scan(&id); err != nil {
					return err
				}
				if !renamed[id] {
					renamed[id] = true
					copies = append(copies, id)
				}
				return nil
			}, int64Args(next)...)
		if err != nil {
			return "", err
		}
		ids = append(ids, copies...)
		next = copies
	}
	var clash string
	err := scanAll(ctx, db, `SELECT p.name, l.direction, o.id, o.name FROM object_links mine
		JOIN processes p ON p.id = mine.process_id
		JOIN object_links l ON l.process_id = mine.process_id AND l.direction = mine.direction
		JOIN objects o ON o.id = l.object_id
		WHERE mine.object_id IN `+placeholders(len(ids))+` ORDER BY p.id, o.id`,
		func(rows *sql.Rows) error {
			var pname, direction, oname string
			var id int64
			if err := rows.Scan(&pname, &direction, &id, &oname); err != nil {
				return err
			}
			if clash == "" && !renamed[id] && sameName(oname, name) {
				clash = nameClashMessage(pname, direction, oname)
			}
			return nil
		}, int64Args(ids)...)
	return clash, err
}

// What's wrong with a step taking these inputs, if two share a name.
func inputClash(process string, inputs []runInput) string {
	seen := map[string]bool{}
	for _, in := range inputs {
		n := inputFileName(in.Name)
		if seen[n] {
			return fmt.Sprintf("%s takes two objects named %s; rename one so it can tell them apart", quote(process), quote(in.Name))
		}
		seen[n] = true
	}
	return ""
}
