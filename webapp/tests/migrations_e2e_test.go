package tests

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// A database from the days of a single board, long before migrations, is
// brought up to date by the first one: its board becomes the first flow,
// with its process on it, and it ends with the columns a new database gets
// from the schema migration.
func TestMigrateDatabaseFromBeforeMigrations(t *testing.T) {
	parallel(t)
	needDB(t)
	name := emptyDatabase(t, "_old")
	old := openDB(t, name)
	for _, s := range []string{
		`CREATE TABLE processes (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			x DOUBLE NOT NULL DEFAULT 0,
			y DOUBLE NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE start_node (
			id INT PRIMARY KEY,
			x DOUBLE NOT NULL DEFAULT 0,
			y DOUBLE NOT NULL DEFAULT 0,
			target_id BIGINT NULL
		)`,
		"INSERT INTO processes (id, name, x, y) VALUES (7, 'Fix the bug', 100, 200)",
		"INSERT INTO start_node (id, x, y, target_id) VALUES (1, 40, 40, 7)",
	} {
		if _, err := old.Exec(s); err != nil {
			t.Fatal(err)
		}
	}

	a := startApp(t, newFakeWorker(t).url, "DB_NAME="+name)
	var flows []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
	if len(flows) != 1 || flows[0].Name != "Flow" {
		t.Fatalf("flows = %+v, want the one board as Flow", flows)
	}
	var processes []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", flows[0].ID), nil, http.StatusOK, &processes)
	if len(processes) != 1 || processes[0].ID != 7 || processes[0].Name != "Fix the bug" || processes[0].Kind != "agent" {
		t.Fatalf("processes = %+v, want Fix the bug, an agent", processes)
	}

	var versions []int64
	rows, err := old.Query("SELECT version_id FROM goose_db_version WHERE version_id > 0 ORDER BY version_id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int64
		rows.Scan(&v)
		versions = append(versions, v)
	}
	rows.Close()
	if len(versions) < 2 || versions[0] != 1 || versions[1] != 2 {
		t.Fatalf("migrations recorded = %v, want 1, 2 and on", versions)
	}

	upgraded, fresh := columns(t, old), columns(t, openDB(t, database(t)))
	for _, c := range fresh {
		if !slices.Contains(upgraded, c) {
			t.Errorf("the upgraded database lacks %s", c)
		}
	}
	for _, c := range upgraded {
		if !slices.Contains(fresh, c) {
			t.Errorf("the upgraded database has %s, which a new one lacks", c)
		}
	}
}

// `webapp migrate status` lists the migrations that ran; `webapp migrate
// down-to` refuses to go below the schema migration, which can't be undone,
// and undoes nothing.
func TestMigrateCommand(t *testing.T) {
	parallel(t)
	needDB(t)
	migrate := func(args ...string) (string, error) { return migrateDB(database(t), args...) }

	out, err := migrate("status")
	if err != nil {
		t.Fatalf("migrate status: %v\n%s", err, out)
	}
	for _, want := range []string{"upgrade from before migrations", "00002_schema.sql"} {
		if !regexp.MustCompile(`(?m)^\d+\s+applied\s+.*` + regexp.QuoteMeta(want) + `$`).MatchString(out) {
			t.Errorf("migrate status doesn't list %s as applied:\n%s", want, out)
		}
	}

	out, err = migrate("down-to", "1")
	if err == nil || !strings.Contains(out, "00002_schema.sql has no -- +goose Down") {
		t.Errorf("migrate down-to 1 = %v, want it refused:\n%s", err, out)
	}
	var schema int
	if err := secretsDB(t).QueryRow("SELECT COUNT(*) FROM goose_db_version WHERE version_id = 2").Scan(&schema); err != nil || schema != 1 {
		t.Errorf("the schema migration is recorded %d times (%v), want still once", schema, err)
	}

	if out, err := migrate("down-to"); err == nil || !strings.Contains(out, "usage:") {
		t.Errorf("migrate down-to without a version = %v, want the usage:\n%s", err, out)
	}
}

// Runs `webapp migrate` with args on the database name.
func migrateDB(name string, args ...string) (string, error) {
	return migrateDBWith(name, nil, args...)
}

// Runs `webapp migrate` with args on the database name, with env besides.
func migrateDBWith(name string, env []string, args ...string) (string, error) {
	cmd := exec.Command(testDB.binary, append([]string{"migrate"}, args...)...)
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"DB_HOST=" + testDB.host,
		"DB_PORT=" + testDB.port,
		"DB_NAME=" + name,
		"DB_USER=" + testDB.user,
		"DB_PASSWORD=" + testDB.password,
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// A webapp built on Spoletta's code keeps migrations of its own apart, as
// local migrations: they run after Spoletta's, so they may change its
// tables, recorded in goose_local_version; `webapp migrate status` lists
// them and `webapp migrate local down-to` undoes them alone, leaving
// Spoletta's; a webapp without them warns of a database that ran them; and
// a LOCAL_MIGRATIONS that isn't there stops the webapp before it migrates
// anything.
func TestLocalMigrations(t *testing.T) {
	parallel(t)
	needDB(t)
	name := emptyDatabase(t, "_local")
	db := openDB(t, name)

	dir := t.TempDir()
	for file, content := range map[string]string{
		"00001_add_badges.sql": "-- +goose Up\nCREATE TABLE badges (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) NOT NULL);\n\n" +
			"-- +goose Down\nDROP TABLE badges;\n",
		// On a table of Spoletta's, which a new database has only once
		// Spoletta's migrations ran.
		"00002_add_user_badges.sql": "-- +goose Up\nALTER TABLE users ADD COLUMN badge_id BIGINT NULL;\n\n" +
			"-- +goose Down\nALTER TABLE users DROP COLUMN badge_id;\n",
		"README.md": "Not a migration.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	local := "LOCAL_MIGRATIONS=" + dir

	if _, err := startWebapp(name, newFakeWorker(t).url, 30*time.Second, "LOCAL_MIGRATIONS="+filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "LOCAL_MIGRATIONS") {
		t.Fatalf("started with LOCAL_MIGRATIONS naming no directory: %v, want it to stop saying so", err)
	}
	var tables int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE()").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("stopped for LOCAL_MIGRATIONS, the database has %d tables (%v), want none", tables, err)
	}

	applied := func(table string) []int64 {
		t.Helper()
		rows, err := db.Query("SELECT version_id FROM " + table + " WHERE version_id > 0 ORDER BY version_id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var vs []int64
		for rows.Next() {
			var v int64
			rows.Scan(&v)
			vs = append(vs, v)
		}
		return vs
	}
	has := func(cs []string, column string) bool {
		return slices.ContainsFunc(cs, func(c string) bool { return strings.HasPrefix(c, column+" ") })
	}

	startApp(t, newFakeWorker(t).url, "DB_NAME="+name, local).proc.stop()
	if v := applied("goose_local_version"); !slices.Equal(v, []int64{1, 2}) {
		t.Fatalf("local migrations recorded = %v, want 1, 2", v)
	}
	spoletta := applied("goose_db_version")
	up := columns(t, db)
	for _, c := range []string{"badges.name", "users.badge_id"} {
		if !has(up, c) {
			t.Errorf("the local migrations didn't add %s", c)
		}
	}

	out, err := migrateDBWith(name, []string{local}, "status")
	if err != nil {
		t.Fatalf("migrate status: %v\n%s", err, out)
	}
	for _, want := range []string{"00002_schema.sql", "local 00001_add_badges.sql", "local 00002_add_user_badges.sql"} {
		if !regexp.MustCompile(`(?m)^\d+\s+applied\s+.*\s` + regexp.QuoteMeta(want) + `$`).MatchString(out) {
			t.Errorf("migrate status doesn't list %s as applied:\n%s", want, out)
		}
	}

	out, err = migrateDBWith(name, []string{local}, "local", "down-to", "1")
	if err != nil || !strings.Contains(out, "undid local 00002_add_user_badges.sql") {
		t.Fatalf("migrate local down-to 1: %v\n%s", err, out)
	}
	if v := applied("goose_local_version"); !slices.Equal(v, []int64{1}) {
		t.Errorf("down to local version 1, recorded %v", v)
	}
	if cs := columns(t, db); has(cs, "users.badge_id") || !has(cs, "badges.name") {
		t.Errorf("down to local version 1 left users.badge_id or took badges.name")
	}
	if v := applied("goose_db_version"); !slices.Equal(v, spoletta) {
		t.Errorf("going down the local migrations changed Spoletta's: %v, were %v", v, spoletta)
	}

	logged := startApp(t, newFakeWorker(t).url, "DB_NAME="+name).proc.stop()
	if !strings.Contains(logged, "WARNING: the database is at local version 1, but this webapp has no local migrations") {
		t.Errorf("a webapp without local migrations didn't warn of the database's:\n%s", logged)
	}
	if out, err := migrateDB(name, "local", "down-to", "0"); err == nil || !strings.Contains(out, "no local migrations") {
		t.Errorf("migrate local down-to without local migrations = %v, want it refused:\n%s", err, out)
	}

	startApp(t, newFakeWorker(t).url, "DB_NAME="+name, local).proc.stop()
	if again := columns(t, db); !slices.Equal(up, again) {
		t.Errorf("going down and up again changed the columns:\nbefore %v\nafter  %v", up, again)
	}
	if out, err := migrateDBWith(name, []string{local}, "local", "down-to", "0"); err != nil {
		t.Fatalf("migrate local down-to 0: %v\n%s", err, out)
	}
	if v, cs := applied("goose_local_version"), columns(t, db); len(v) != 0 || has(cs, "badges.name") || has(cs, "users.badge_id") {
		t.Errorf("down to local version 0, recorded %v, with badges or users.badge_id left", v)
	}
}

// Every migration after the schema one is undone by its Down, leaving the
// database as the schema migration made it, so an older webapp runs on it;
// and the webapp starting again brings it back up to the columns a new
// database has.
func TestMigrationsGoDownAndUpAgain(t *testing.T) {
	parallel(t)
	needDB(t)
	name := emptyDatabase(t, "_down")
	db := openDB(t, name)
	startApp(t, newFakeWorker(t).url, "DB_NAME="+name).proc.stop()
	up := columns(t, db)

	if out, err := migrateDB(name, "down-to", "2"); err != nil {
		t.Fatalf("migrate down-to 2: %v\n%s", err, out)
	}
	var latest int
	if err := db.QueryRow("SELECT MAX(version_id) FROM goose_db_version WHERE is_applied").Scan(&latest); err != nil || latest != 2 {
		t.Fatalf("down to version %d (%v), want 2", latest, err)
	}
	// Such as the record a run was started on (00015).
	if down := columns(t, db); slices.ContainsFunc(down, func(c string) bool { return strings.HasPrefix(c, "runs.record_id ") }) {
		t.Errorf("runs.record_id is still there after going down")
	}

	startApp(t, newFakeWorker(t).url, "DB_NAME="+name)
	again := columns(t, db)
	if !slices.Equal(up, again) {
		t.Errorf("going down and up again changed the columns:\nbefore %v\nafter  %v", up, again)
	}
}

func openDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", testDSN(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// Every column of a database's tables, with its type, whether it takes null
// and its default, but not where it stands in its table.
func columns(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COALESCE(COLUMN_DEFAULT, 'NULL')
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME <> 'goose_db_version'
		ORDER BY TABLE_NAME, COLUMN_NAME`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cs []string
	for rows.Next() {
		var table, column, typ, null, def string
		if err := rows.Scan(&table, &column, &typ, &null, &def); err != nil {
			t.Fatal(err)
		}
		cs = append(cs, fmt.Sprintf("%s.%s %s null=%s default=%s", table, column, typ, null, def))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cs
}
