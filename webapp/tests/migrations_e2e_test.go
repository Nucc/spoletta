package tests

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// A database from the days of a single board, long before migrations, is
// brought up to date by the first one: its board becomes the first flow,
// with its process on it, and it ends with the columns a new database gets
// from the schema migration.
func TestMigrateDatabaseFromBeforeMigrations(t *testing.T) {
	needDB(t)
	root := secretsDB(t)
	name := fmt.Sprintf("%s%d_old", testDBPrefix, time.Now().UnixNano())
	if _, err := root.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE IF EXISTS " + name) })
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

	upgraded, fresh := columns(t, old), columns(t, openDB(t, testDB.name))
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
	needDB(t)
	migrate := func(args ...string) (string, error) {
		cmd := exec.Command(testDB.binary, append([]string{"migrate"}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + os.Getenv("HOME"),
			"DB_HOST=" + testDB.host,
			"DB_PORT=" + testDB.port,
			"DB_NAME=" + testDB.name,
			"DB_USER=" + testDB.user,
			"DB_PASSWORD=" + testDB.password,
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

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

func openDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(testDB.host, testDB.port)
	cfg.DBName = name
	cfg.User, cfg.Passwd = testDB.user, testDB.password
	db, err := sql.Open("mysql", cfg.FormatDSN())
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
