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
//
// A webapp built on Spoletta's code with tables or columns of its own keeps
// the migrations making them apart, as local migrations: the SQL files in
// local_migrations/, or in the directory LOCAL_MIGRATIONS names, numbered
// from 1 and recorded in goose_local_version, so that its numbers and
// Spoletta's never meet, however the two go on. Spoletta ships the directory
// with only its README. They run after Spoletta's, since they may change
// Spoletta's tables, and are undone apart, with `webapp migrate local
// down-to <version>`.

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
	"os"
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

//go:embed local_migrations
var localMigrationFiles embed.FS

// Where goose records which local migrations ran.
const localVersionTable = "goose_local_version"

// Migrations goose runs and records apart: Spoletta's, or the local ones,
// whose versions, names and commands say "local".
type migrationSet struct {
	provider *goose.Provider
	files    fs.FS
	local    string
}

// Spoletta's migrations.
func migrations(db *sql.DB) (*migrationSet, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectMySQL, db, files,
		goose.WithGoMigrations(goose.NewGoMigration(1, &goose.GoFunc{RunDB: upgradeLegacySchema}, nil)))
	if err != nil {
		return nil, err
	}
	return &migrationSet{provider: provider, files: files}, nil
}

// The local migrations, from the directory LOCAL_MIGRATIONS names, else
// those built in; nil when there are none, as goose takes no set without
// migrations.
func localMigrations(db *sql.DB) (*migrationSet, error) {
	files, err := localMigrationsDir()
	if err != nil {
		return nil, err
	}
	sources, err := fs.Glob(files, "*.sql")
	if err != nil || len(sources) == 0 {
		return nil, err
	}
	provider, err := goose.NewProvider(goose.DialectMySQL, db, files, goose.WithTableName(localVersionTable))
	if err != nil {
		return nil, fmt.Errorf("local migrations: %w", err)
	}
	return &migrationSet{provider: provider, files: files, local: "local "}, nil
}

func localMigrationsDir() (fs.FS, error) {
	dir := os.Getenv("LOCAL_MIGRATIONS")
	if dir == "" {
		return fs.Sub(localMigrationFiles, "local_migrations")
	}
	// A directory named that isn't there is a mistake, never no migrations,
	// which would leave the schema short of them.
	info, err := os.Stat(dir)
	if err == nil && !info.IsDir() {
		err = fmt.Errorf("%s isn't a directory", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("LOCAL_MIGRATIONS: %w", err)
	}
	return os.DirFS(dir), nil
}

// Spoletta's migrations, then the local ones, both found before either runs,
// so that local migrations gone wrong stop the webapp having migrated
// nothing.
func migrateDatabase(ctx context.Context, db *sql.DB) error {
	spoletta, err := migrations(db)
	if err != nil {
		return err
	}
	local, err := localMigrations(db)
	if err != nil {
		return err
	}
	if err := spoletta.up(ctx); err != nil {
		return err
	}
	if local != nil {
		return local.up(ctx)
	}
	// A webapp without them started on a database one with them migrated.
	at, err := localVersion(ctx, db)
	if err == nil && at > 0 {
		log.Printf("WARNING: the database is at local version %d, but this webapp has no local migrations; migrate it down with the webapp that migrated it (`webapp migrate local down-to 0`)", at)
	}
	return err
}

// Runs the set's migrations that haven't run, logging each.
func (s *migrationSet) up(ctx context.Context) error {
	results, err := s.provider.Up(ctx)
	for _, r := range results {
		log.Printf("migrated the database to %sversion %d in %s", s.local, r.Source.Version, r.Duration.Round(time.Millisecond))
	}
	if err != nil {
		return err
	}
	// An older webapp started on a database a newer one migrated, without
	// it being migrated down first, finds it ahead of what it knows.
	at, err := s.provider.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		log.Printf("the database is at %sversion %d, with nothing to migrate", s.local, at)
	}
	sources := s.provider.ListSources()
	if known := sources[len(sources)-1].Version; at > known {
		log.Printf("WARNING: the database is at %[1]sversion %[2]d, past this webapp's last %[1]smigration, %[3]d; migrate it down with the webapp that migrated it (`webapp migrate %[1]sdown-to %[3]d`)", s.local, at, known)
	}
	return nil
}

// The latest local migration the database ran, 0 if none, read without
// goose for a webapp that has none of its own.
func localVersion(ctx context.Context, db *sql.DB) (int64, error) {
	if ok, err := hasTable(db, localVersionTable); err != nil || !ok {
		return 0, err
	}
	var at sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT MAX(version_id) FROM "+localVersionTable).Scan(&at)
	return at.Int64, err
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

const migrateUsage = "usage: webapp migrate status | webapp migrate down-to <version> | webapp migrate local down-to <version>"

// `webapp migrate status`, `webapp migrate down-to <version>` and `webapp
// migrate local down-to <version>`, which write what they did to out and
// run nothing else of the webapp.
func migrateCommand(ctx context.Context, db *sql.DB, args []string, out io.Writer) error {
	spoletta, err := migrations(db)
	if err != nil {
		return err
	}
	local, err := localMigrations(db)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "status":
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tSTATE\tAPPLIED AT (UTC)\tMIGRATION")
		for _, s := range []*migrationSet{spoletta, local} {
			if s == nil {
				continue
			}
			if err := s.status(ctx, w); err != nil {
				return err
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if local == nil {
			at, err := localVersion(ctx, db)
			if err == nil && at > 0 {
				fmt.Fprintf(out, "the database is at local version %d, but this webapp has no local migrations\n", at)
			}
			return err
		}
		return nil
	case len(args) == 2 && args[0] == "down-to":
		return spoletta.downTo(ctx, args[1], out)
	case len(args) == 3 && args[0] == "local" && args[1] == "down-to":
		if local == nil {
			return errors.New("this webapp has no local migrations to undo")
		}
		return local.downTo(ctx, args[2], out)
	}
	return errors.New(migrateUsage)
}

// Writes a row for each of the set's migrations, applied or pending.
func (s *migrationSet) status(ctx context.Context, w io.Writer) error {
	statuses, err := s.provider.Status(ctx)
	if err != nil {
		return err
	}
	for _, st := range statuses {
		applied := ""
		if st.State == goose.StateApplied {
			applied = st.AppliedAt.UTC().Format(time.DateTime)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", st.Source.Version, st.State, applied, s.name(st.Source))
	}
	return nil
}

// Undoes the set's applied migrations above the version arg names, newest
// first, refusing before undoing anything if one can't be undone.
func (s *migrationSet) downTo(ctx context.Context, arg string, out io.Writer) error {
	target, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || target < 0 {
		return errors.New(migrateUsage)
	}
	statuses, err := s.provider.Status(ctx)
	if err != nil {
		return err
	}
	if err := s.checkReversible(statuses, target); err != nil {
		return err
	}
	results, err := s.provider.DownTo(ctx, target)
	for _, r := range results {
		fmt.Fprintf(out, "undid %s in %s\n", s.name(r.Source), r.Duration.Round(time.Millisecond))
	}
	if err != nil {
		return err
	}
	if len(results) == 0 {
		fmt.Fprintf(out, "the database is at %sversion %d already or below\n", s.local, target)
	}
	return nil
}

func (s *migrationSet) name(src *goose.Source) string {
	if src.Type == goose.TypeGo {
		return "upgrade from before migrations"
	}
	return s.local + path.Base(src.Path)
}

// Goose takes a migration with nothing to undo it as undone, only forgetting
// it ran; going down past one is refused instead, before anything is undone,
// naming the first met on the way down.
func (s *migrationSet) checkReversible(statuses []*goose.MigrationStatus, target int64) error {
	for _, st := range slices.Backward(statuses) {
		if st.State != goose.StateApplied || st.Source.Version <= target {
			continue
		}
		ok := false
		if st.Source.Type == goose.TypeSQL {
			var err error
			if ok, err = hasDown(s.files, path.Base(st.Source.Path)); err != nil {
				return err
			}
		}
		if !ok {
			return fmt.Errorf("can't migrate down to %sversion %d: %s has no -- +goose Down to undo it", s.local, target, s.name(st.Source))
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
