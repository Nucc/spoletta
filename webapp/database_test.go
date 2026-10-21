package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// Writes a database configuration and points DB_CONFIG at it.
func writeDBConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DB_CONFIG", path)
	return path
}

// Without DB_CONFIG, config/database.yaml is read, each time, from the
// working directory or the one above; without that either, the built-in
// configuration.
func TestDBSettingsConfigDirectory(t *testing.T) {
	top := t.TempDir()
	os.Mkdir(filepath.Join(top, "config"), 0o700)
	os.Mkdir(filepath.Join(top, "webapp"), 0o700)
	t.Chdir(filepath.Join(top, "webapp"))
	t.Setenv("DB_CONFIG", "")
	t.Setenv("APP_ENV", "")
	t.Setenv("DB_HOST", "")
	if s, err := loadDBSettings(); err != nil || s.Host != "127.0.0.1" {
		t.Errorf("built in = %+v, %v", s, err)
	}
	os.WriteFile(filepath.Join(top, "config", "database.yaml"), []byte("development:\n  host: here\n  database: app\n  user: app\n"), 0o600)
	if s, err := loadDBSettings(); err != nil || s.Host != "here" {
		t.Errorf("from the directory above = %+v, %v", s, err)
	}
	os.WriteFile(filepath.Join(top, "config", "database.yaml"), []byte("development:\n  host: there\n  database: app\n  user: app\n"), 0o600)
	if s, err := loadDBSettings(); err != nil || s.Host != "there" {
		t.Errorf("after editing = %+v, %v", s, err)
	}
	t.Chdir(top)
	if s, err := loadDBSettings(); err != nil || s.Host != "there" {
		t.Errorf("from the working directory = %+v, %v", s, err)
	}
}

// The example configuration is valid in each of its environments.
func TestDBSettingsExample(t *testing.T) {
	t.Setenv("DB_CONFIG", "../config/database.yaml.example")
	t.Setenv("DB_HOST", "db.internal")
	secret := filepath.Join(t.TempDir(), "db_password")
	os.WriteFile(secret, []byte("from-file\n"), 0o600)
	t.Setenv("DB_PASSWORD_FILE", secret)
	for _, app := range []string{"development", "staging", "production"} {
		t.Setenv("APP_ENV", app)
		if _, err := loadDBSettings(); err != nil {
			t.Errorf("%s: %v", app, err)
		}
	}

	// Production takes its password from the file DB_PASSWORD_FILE names,
	// and needs DB_HOST.
	if s, err := loadDBSettings(); err != nil || s.Password != "from-file" || s.Host != "db.internal" || s.TLS != "true" {
		t.Errorf("production = %+v, %v", s, err)
	}
	t.Setenv("DB_HOST", "")
	if _, err := loadDBSettings(); err == nil || !strings.Contains(err.Error(), "production.host is empty") {
		t.Errorf("production without DB_HOST: %v", err)
	}
}

// Without a configuration file the built-in one reads the DB_* variables,
// with compose's defaults for those unset, in development alone.
func TestDBSettingsBuiltIn(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{"DB_CONFIG", "APP_ENV", "DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD"} {
		t.Setenv(name, "")
	}
	s, err := loadDBSettings()
	if err != nil {
		t.Fatal(err)
	}
	if want := (dbSettings{Host: "127.0.0.1", Port: "3306", Database: "app", User: "app", Password: "apppass"}); s != want {
		t.Errorf("defaults = %+v", s)
	}
	t.Setenv("DB_HOST", "db.internal")
	t.Setenv("DB_PASSWORD", "from-env")
	if s, _ := loadDBSettings(); s.Host != "db.internal" || s.Password != "from-env" {
		t.Errorf("from the environment = %+v", s)
	}

	t.Setenv("APP_ENV", "production")
	if _, err := loadDBSettings(); err == nil || !strings.Contains(err.Error(), `no environment "production"`) {
		t.Errorf("production built in: %v", err)
	}
}

// Each environment is filled in from the environment and files, picked by
// APP_ENV, and a value can hold anything YAML would otherwise take apart.
func TestDBSettingsTemplates(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "password")
	hostile := `p@ss: #word "quoted" {{x}}` + "\n"
	os.WriteFile(secret, []byte(hostile), 0o600)
	t.Setenv("STAGING_HOST", "staging.db")
	t.Setenv("UNSET_PORT", "")
	writeDBConfig(t, `
development:
  host: localhost
  database: app
  user: app
staging:
  host: '{{ env "STAGING_HOST" }}'
  port: '{{ env "UNSET_PORT" "3307" }}'
  database: spoletta
  user: '{{ env "UNSET_USER" "spoletta" }}'
  password: '{{ file "`+secret+`" }}'
  tls: skip-verify
`)
	t.Setenv("APP_ENV", "")
	if s, err := loadDBSettings(); err != nil || s.Host != "localhost" || s.Port != "3306" || s.Password != "" {
		t.Errorf("development = %+v, %v", s, err)
	}
	t.Setenv("APP_ENV", "staging")
	s, err := loadDBSettings()
	if err != nil {
		t.Fatal(err)
	}
	if want := (dbSettings{Host: "staging.db", Port: "3307", Database: "spoletta", User: "spoletta", Password: strings.TrimSuffix(hostile, "\n"), TLS: "skip-verify"}); s != want {
		t.Errorf("staging = %+v", s)
	}
	cfg := mysql.NewConfig()
	s.apply(cfg)
	if cfg.Addr != "staging.db:3307" || cfg.Passwd != s.Password || cfg.TLS == nil || !cfg.TLS.InsecureSkipVerify {
		t.Errorf("applied as %+v", cfg)
	}

	t.Setenv("APP_ENV", "qa")
	if _, err := loadDBSettings(); err == nil || !strings.Contains(err.Error(), `no environment "qa"`) {
		t.Errorf("an unknown environment: %v", err)
	}
}

// The configuration isn't kept: a password changed in its file is the one
// read next.
func TestDBSettingsReread(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "password")
	os.WriteFile(secret, []byte("first"), 0o600)
	path := writeDBConfig(t, `
development:
  host: localhost
  database: app
  user: app
  password: '{{ file "`+secret+`" }}'
`)
	t.Setenv("APP_ENV", "")
	if s, _ := loadDBSettings(); s.Password != "first" {
		t.Fatalf("password = %q", s.Password)
	}
	os.WriteFile(secret, []byte("second\n"), 0o600)
	if s, _ := loadDBSettings(); s.Password != "second" {
		t.Errorf("after rotating, password = %q", s.Password)
	}
	os.WriteFile(path, []byte("development:\n  host: elsewhere\n  database: app\n  user: app\n"), 0o600)
	if s, _ := loadDBSettings(); s.Host != "elsewhere" {
		t.Errorf("after changing the configuration, host = %q", s.Host)
	}
}

// What goes wrong names the setting, never the secret in it.
func TestDBSettingsErrors(t *testing.T) {
	t.Setenv("APP_ENV", "")
	missing := filepath.Join(t.TempDir(), "missing")
	for _, c := range []struct{ config, want string }{
		{"development:\n  host: h\n  database: d\n  user: u\n  password: '{{ file \"" + missing + "\" }}'\n", "development.password"},
		{"development:\n  host: '{{ env }}'\n  database: d\n  user: u\n", "development.host"},
		{"development:\n  host: h\n  database: d\n  user: u\n  password: '{{ nope }}'\n", "development.password"},
		{"development:\n  host: h\n  database: d\n  user: u\n  tls: maybe\n", "development.tls"},
		{"development: [\n", "database.yaml"},
	} {
		writeDBConfig(t, c.config)
		if _, err := loadDBSettings(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.config, err)
		}
	}
	t.Setenv("DB_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	if _, err := loadDBSettings(); err == nil || !strings.Contains(err.Error(), "DB_CONFIG") {
		t.Errorf("a missing DB_CONFIG: %v", err)
	}
}
