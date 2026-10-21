package main

// The database the webapp keeps everything in, as a configuration file says:
// one entry per environment (development, staging, production), APP_ENV
// picking which, development by default. DB_CONFIG names the file; without
// it config/database.yaml is used, looked for in the working directory and
// the one above it, so the webapp finds the repository's whether started
// from its top or from webapp/. It isn't committed: copy it from
// config/database.yaml.example. Without any, the webapp connects as the
// DB_* variables say, as it always has (builtInDatabaseConfig).
//
// Each value is a template (Go's text/template) that can take what it says
// from the environment or from a file:
//
//	host: '{{ env "DB_HOST" "127.0.0.1" }}'
//	password: '{{ file "/run/secrets/db_password" }}'
//
// env gives a variable, or the fallback after it when the variable is unset
// or empty; file gives a file's content without its trailing newline, as
// secret stores mount secrets. Values are filled in after the file is read
// as YAML, so what env and file give needs no quoting whatever it holds;
// a value written in the file holding {{ writes it as {{ "{{" }}.
//
// Nothing of it is kept: the file is read and its templates filled in anew
// each time a connection to the database is opened, so a password rotated in
// the file it comes from is the one the next connection signs in with.
// Connections are given up after a few minutes for that reason, the pool
// then opening new ones.

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"strings"
	"text/template"
	"time"

	"github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"
)

// Where config/database.yaml is looked for when DB_CONFIG is unset.
var databaseConfigPaths = []string{"config/database.yaml", "../config/database.yaml"}

// The configuration without a file: the DB_* variables, with compose's
// defaults, for development only.
const builtInDatabaseConfig = `
development:
  host: '{{ env "DB_HOST" "127.0.0.1" }}'
  port: '{{ env "DB_PORT" "3306" }}'
  database: '{{ env "DB_NAME" "app" }}'
  user: '{{ env "DB_USER" "app" }}'
  password: '{{ env "DB_PASSWORD" "apppass" }}'
`

// How long a connection is used before the pool opens another, reading the
// configuration again.
const dbConnLifetime = 5 * time.Minute

// Where one environment's database is, and who the webapp signs in as.
// TLS is false (or empty), true, or skip-verify to take any certificate.
type dbSettings struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	Database string `yaml:"database"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	TLS      string `yaml:"tls"`
}

var dbTemplateFuncs = template.FuncMap{
	"env": func(name string, fallback ...string) (string, error) {
		if len(fallback) > 1 {
			return "", fmt.Errorf("env %s takes one fallback, not %d", name, len(fallback))
		}
		if v := os.Getenv(name); v != "" || len(fallback) == 0 {
			return v, nil
		}
		return fallback[0], nil
	},
	"file": func(path string) (string, error) {
		if path == "" {
			return "", fmt.Errorf("file: no path given")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	},
}

// The configuration file's content, and what to call it in errors.
func databaseConfigFile() ([]byte, string, error) {
	path := os.Getenv("DB_CONFIG")
	if path == "" {
		for _, p := range databaseConfigPaths {
			b, err := os.ReadFile(p)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return b, p, err
		}
		return []byte(builtInDatabaseConfig), "the built-in database configuration", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("DB_CONFIG: %w", err)
	}
	return b, path, nil
}

func appEnv() string {
	return env("APP_ENV", "development")
}

// Reads the configuration and fills in the current environment's settings.
// Errors name the setting that failed, never what it holds.
func loadDBSettings() (dbSettings, error) {
	content, name, err := databaseConfigFile()
	if err != nil {
		return dbSettings{}, err
	}
	var envs map[string]dbSettings
	if err := yaml.Unmarshal(content, &envs); err != nil {
		return dbSettings{}, fmt.Errorf("%s: %w", name, err)
	}
	app := appEnv()
	s, ok := envs[app]
	if !ok {
		return dbSettings{}, fmt.Errorf("%s has no environment %q (APP_ENV)", name, app)
	}
	for _, f := range []struct {
		key   string
		value *string
	}{
		{"host", &s.Host}, {"port", &s.Port}, {"database", &s.Database},
		{"user", &s.User}, {"password", &s.Password}, {"tls", &s.TLS},
	} {
		v, err := renderDBSetting(f.key, *f.value)
		if err != nil {
			return dbSettings{}, fmt.Errorf("%s: %s.%s: %w", name, app, f.key, err)
		}
		*f.value = v
	}
	if s.Port == "" {
		s.Port = "3306"
	}
	for _, f := range []struct{ key, value string }{{"host", s.Host}, {"database", s.Database}, {"user", s.User}} {
		if f.value == "" {
			return dbSettings{}, fmt.Errorf("%s: %s.%s is empty", name, app, f.key)
		}
	}
	switch s.TLS {
	case "", "false", "true", "skip-verify":
	default:
		return dbSettings{}, fmt.Errorf("%s: %s.tls is %q, not true, false or skip-verify", name, app, s.TLS)
	}
	return s, nil
}

func renderDBSetting(key, text string) (string, error) {
	t, err := template.New(key).Funcs(dbTemplateFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, nil); err != nil {
		return "", err
	}
	return out.String(), nil
}

// Puts the settings into the driver's configuration for one connection.
func (s dbSettings) apply(cfg *mysql.Config) {
	cfg.Addr = net.JoinHostPort(s.Host, s.Port)
	cfg.DBName = s.Database
	cfg.User = s.User
	cfg.Passwd = s.Password
	cfg.TLS = nil
	switch s.TLS {
	case "true":
		cfg.TLS = &tls.Config{ServerName: s.Host}
	case "skip-verify":
		cfg.TLS = &tls.Config{InsecureSkipVerify: true}
	}
}

func openDB() (*sql.DB, error) {
	// Read once up front, so a broken configuration stops the webapp
	// starting rather than every connection failing.
	if _, err := loadDBSettings(); err != nil {
		return nil, err
	}
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.ParseTime = true
	if err := cfg.Apply(mysql.BeforeConnect(func(_ context.Context, c *mysql.Config) error {
		s, err := loadDBSettings()
		if err != nil {
			return err
		}
		s.apply(c)
		return nil
	})); err != nil {
		return nil, err
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetConnMaxLifetime(dbConnLifetime)

	// MySQL can take a while to accept connections on first start.
	for attempt := 1; attempt <= 30; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			return db, nil
		}
		log.Printf("waiting for database (attempt %d): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	db.Close()
	return nil, fmt.Errorf("database not reachable: %w", err)
}
