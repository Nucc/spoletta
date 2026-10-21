package main

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

// Going down past a migration that can't be undone is refused before
// anything is undone: one in Go, or in SQL with nothing under its Down.
func TestCheckReversible(t *testing.T) {
	files := fstest.MapFS{
		"00002_schema.sql": {Data: []byte("-- +goose Up\nCREATE TABLE a (id INT);\n")},
		"00003_label.sql": {Data: []byte("-- +goose Up\nALTER TABLE a ADD COLUMN label TEXT;\n\n" +
			"-- +goose Down\n-- The labels go.\nALTER TABLE a DROP COLUMN label;\n")},
		"00004_empty_down.sql": {Data: []byte("-- +goose Up\nALTER TABLE a ADD COLUMN b INT;\n-- +goose Down\n-- Nothing.\n\n")},
	}
	status := func(version int64, typ goose.MigrationType, name string, state goose.State) *goose.MigrationStatus {
		return &goose.MigrationStatus{Source: &goose.Source{Type: typ, Path: name, Version: version}, State: state}
	}
	statuses := []*goose.MigrationStatus{
		status(1, goose.TypeGo, "", goose.StateApplied),
		status(2, goose.TypeSQL, "00002_schema.sql", goose.StateApplied),
		status(3, goose.TypeSQL, "00003_label.sql", goose.StateApplied),
		status(4, goose.TypeSQL, "00004_empty_down.sql", goose.StatePending),
	}
	for _, c := range []struct {
		target  int64
		refused string
	}{
		{3, ""},
		{2, ""},
		{1, "00002_schema.sql"},
		{0, "00002_schema.sql"},
	} {
		err := checkReversible(files, statuses, c.target)
		switch {
		case c.refused == "" && err != nil:
			t.Errorf("down to %d: %v", c.target, err)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)):
			t.Errorf("down to %d: %v, want %s refused", c.target, err, c.refused)
		}
	}

	statuses[3].State = goose.StateApplied
	if err := checkReversible(files, statuses, 3); err == nil || !strings.Contains(err.Error(), "00004_empty_down.sql") {
		t.Errorf("down past a Down of comments only: %v, want it refused", err)
	}
	if err := checkReversible(files, statuses[:1], 0); err == nil || !strings.Contains(err.Error(), "before migrations") {
		t.Errorf("down past the Go migration: %v, want it refused", err)
	}
}
