package main

// The database's schema is kept by migrations, run in order as the webapp
// starts, each once: the SQL files in migrations/, numbered, and the few
// written in Go here. Goose (github.com/pressly/goose) runs them and records
// which ran in goose_db_version. A change to the schema is a new file, such
// as migrations/00003_add_run_labels.sql, never an edit of one that ran.
//
// 00001 brings a database from before migrations up to date with the code
// that kept it then (legacy_schema.go); a new database has nothing to bring,
// and gets the whole schema from 00002. Neither can be undone.
//
// Each later migration says how to undo it under -- +goose Down, so that
// `webapp migrate down-to <version>` can take the database back to where an
// older webapp expects it. It is run with the webapp that has the
// migrations to undo, before the older one is deployed: the older one
// doesn't know them. `webapp migrate status` lists which have run.

import (
	"bufio"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func migrations(db *sql.DB) (*goose.Provider, fs.FS, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, nil, err
	}
	provider, err := goose.NewProvider(goose.DialectMySQL, db, files,
		goose.WithGoMigrations(goose.NewGoMigration(1, &goose.GoFunc{RunDB: upgradeLegacySchema}, nil)))
	return provider, files, err
}

func migrateDatabase(ctx context.Context, db *sql.DB) error {
	provider, _, err := migrations(db)
	if err != nil {
		return err
	}
	results, err := provider.Up(ctx)
	for _, r := range results {
		log.Printf("migrated the database to version %d in %s", r.Source.Version, r.Duration.Round(time.Millisecond))
	}
	if err != nil {
		return err
	}
	// An older webapp started on a database a newer one migrated, without
	// it being migrated down first, finds it ahead of what it knows.
	at, err := provider.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		log.Printf("the database is at version %d, with nothing to migrate", at)
	}
	sources := provider.ListSources()
	if known := sources[len(sources)-1].Version; at > known {
		log.Printf("WARNING: the database is at version %d, past this webapp's last migration, %d; migrate it down with the webapp that migrated it (`webapp migrate down-to %d`)", at, known, known)
	}
	return nil
}

// Runs what kept the schema before migrations, on a database from then.
func upgradeLegacySchema(ctx context.Context, db *sql.DB) error {
	old, err := hasTable(db, "processes")
	if err != nil || !old {
		return err
	}
	for _, migrate := range []func(*sql.DB) error{
		migrateProcesses, migrateMCP, migrateSchedules, migrateAssistant, migrateUsers, migrateUserRoles,
		migrateTeams, migrateOwners, migrateModelKeys, migrateModelProviders, migrateAPIKeys, migrateSkills,
	} {
		if err := migrate(db); err != nil {
			return err
		}
	}
	return nil
}

const migrateUsage = "usage: webapp migrate status | webapp migrate down-to <version>"

// `webapp migrate status` and `webapp migrate down-to <version>`, which
// write what they did to out and run nothing else of the webapp.
func migrateCommand(ctx context.Context, db *sql.DB, args []string, out io.Writer) error {
	provider, files, err := migrations(db)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "status":
		statuses, err := provider.Status(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tSTATE\tAPPLIED AT (UTC)\tMIGRATION")
		for _, s := range statuses {
			applied := ""
			if s.State == goose.StateApplied {
				applied = s.AppliedAt.UTC().Format(time.DateTime)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", s.Source.Version, s.State, applied, migrationName(s.Source))
		}
		return w.Flush()
	case len(args) == 2 && args[0] == "down-to":
		target, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || target < 0 {
			return errors.New(migrateUsage)
		}
		statuses, err := provider.Status(ctx)
		if err != nil {
			return err
		}
		if err := checkReversible(files, statuses, target); err != nil {
			return err
		}
		results, err := provider.DownTo(ctx, target)
		for _, r := range results {
			fmt.Fprintf(out, "undid %s in %s\n", migrationName(r.Source), r.Duration.Round(time.Millisecond))
		}
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Fprintf(out, "the database is at version %d already or below\n", target)
		}
		return nil
	}
	return errors.New(migrateUsage)
}

func migrationName(s *goose.Source) string {
	if s.Type == goose.TypeGo {
		return "upgrade from before migrations"
	}
	return path.Base(s.Path)
}

// Goose takes a migration with nothing to undo it as undone, only forgetting
// it ran; going down past one is refused instead, before anything is undone,
// naming the first met on the way down.
func checkReversible(files fs.FS, statuses []*goose.MigrationStatus, target int64) error {
	for _, s := range slices.Backward(statuses) {
		if s.State != goose.StateApplied || s.Source.Version <= target {
			continue
		}
		ok := false
		if s.Source.Type == goose.TypeSQL {
			var err error
			if ok, err = hasDown(files, path.Base(s.Source.Path)); err != nil {
				return err
			}
		}
		if !ok {
			return fmt.Errorf("can't migrate down to version %d: %s has no -- +goose Down to undo it", target, migrationName(s.Source))
		}
	}
	return nil
}

// Whether a SQL migration has statements under -- +goose Down.
func hasDown(files fs.FS, name string) (bool, error) {
	f, err := files.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	down := false
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		switch {
		case strings.HasPrefix(line, "-- +goose Down"):
			down = true
		case strings.HasPrefix(line, "-- +goose Up"):
			down = false
		case down && line != "" && !strings.HasPrefix(line, "--"):
			return true, nil
		}
	}
	return false, lines.Err()
}
