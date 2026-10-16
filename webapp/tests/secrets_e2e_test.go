package tests

// Model keys and the tokens of MCP connections are sealed in the database
// with SECRET_KEY, those kept unsealed are sealed once it is set, and a
// webapp without the key they were sealed with doesn't start. The key is
// rotated through SECRET_KEY_SECONDARY and `webapp seal-secrets`.

import (
	"database/sql"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

// The test database, as the webapp sees it.
func secretsDB(t *testing.T) *sql.DB {
	t.Helper()
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(testDB.host, testDB.port)
	cfg.DBName = testDB.name
	cfg.User, cfg.Passwd = testDB.user, testDB.password
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func secretsStored(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var v string
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSecretsSealed(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	t.Cleanup(func() {
		db.Exec("DELETE FROM model_keys WHERE env IN ('OPENAI_API_KEY', 'OLD_UNSEALED_KEY')")
		db.Exec("DELETE FROM mcp_servers WHERE name = 'old-unsealed'")
	})

	// A key saved is kept sealed, and agents get it as it was saved.
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-sealed-openai"}, http.StatusOK, nil)
	if v := secretsStored(t, db, "SELECT api_key FROM model_keys WHERE env = 'OPENAI_API_KEY'"); !strings.HasPrefix(v, "sealed:v1:") || strings.Contains(v, "sk-sealed") {
		t.Errorf("the key was kept as %q", v)
	}
	var keys settingsKeys
	a.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)
	if keys.Keys[1].Env != "OPENAI_API_KEY" || keys.Keys[1].Hint != "enai" {
		t.Errorf("the settings show %+v", keys.Keys)
	}
	f := a.newFlow("Sealed keys")
	a.startAt(f.ID, a.agent(f.ID, "Write", "Write.").ID)
	a.worker.onAgent("Write", handsIn(data("done")))
	a.finish(a.startRun(f.ID).ID)
	if got := a.worker.runsOf("Write")[0].Keys; got["OPENAI_API_KEY"] != "sk-sealed-openai" {
		t.Errorf("the agent got keys %v", got)
	}

	// Secrets kept before SECRET_KEY was set are sealed when it is.
	if _, err := db.Exec("INSERT INTO model_keys (env, api_key) VALUES ('OLD_UNSEALED_KEY', 'sk-old')"); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec("INSERT INTO mcp_servers (name, url) VALUES ('old-unsealed', 'https://mcp.example.com')")
	if err != nil {
		t.Fatal(err)
	}
	server, _ := res.LastInsertId()
	if _, err := db.Exec("INSERT INTO mcp_tokens (server_id, user_id, access_token, refresh_token) VALUES (?, 0, 'old-access', NULL)", server); err != nil {
		t.Fatal(err)
	}
	b := startApp(t, a.worker.url)
	if v := secretsStored(t, db, "SELECT api_key FROM model_keys WHERE env = 'OLD_UNSEALED_KEY'"); !strings.HasPrefix(v, "sealed:v1:") {
		t.Errorf("the old key is kept as %q", v)
	}
	if v := secretsStored(t, db, "SELECT access_token FROM mcp_tokens WHERE server_id = ?", server); !strings.HasPrefix(v, "sealed:v1:") {
		t.Errorf("the old token is kept as %q", v)
	}
	var refresh sql.NullString
	db.QueryRow("SELECT refresh_token FROM mcp_tokens WHERE server_id = ?", server).Scan(&refresh)
	if refresh.Valid {
		t.Errorf("no refresh token became %q", refresh.String)
	}
	b.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)

	// Without the key they were sealed with, the webapp doesn't start.
	for _, key := range []string{"SECRET_KEY=", "SECRET_KEY=another-key-0123456789abcdef01234567"} {
		if w, err := startWebapp(a.worker.url, 30*time.Second, key); err == nil {
			w.stop()
			t.Errorf("started with %s", key)
		} else if !strings.Contains(err.Error(), "SECRET_KEY") {
			t.Errorf("with %s: %v", key, err)
		}
	}
	if _, err := startWebapp(a.worker.url, 30*time.Second, "SECRET_KEY=short"); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Errorf("with a short key: %v", err)
	}
}

// Runs `webapp seal-secrets` with the keys given, and returns what it logged.
func secretsSeal(t *testing.T, primary, secondary string) (string, error) {
	t.Helper()
	cmd := exec.Command(testDB.binary, "seal-secrets")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"DB_HOST=" + testDB.host,
		"DB_PORT=" + testDB.port,
		"DB_NAME=" + testDB.name,
		"DB_USER=" + testDB.user,
		"DB_PASSWORD=" + testDB.password,
		"SECRET_KEY=" + primary,
		"SECRET_KEY_SECONDARY=" + secondary,
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Rotating: webapps get the secondary key, seal-secrets seals everything
// with it, and then it alone is SECRET_KEY, which webapps start with while
// the old key no longer does.
func TestSecretsRotate(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	t.Cleanup(func() { db.Exec("DELETE FROM model_keys WHERE env = 'OPENAI_API_KEY'") })
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-rotated"}, http.StatusOK, nil)
	before := secretsStored(t, db, "SELECT api_key FROM model_keys WHERE env = 'OPENAI_API_KEY'")

	// A webapp with the secondary opens what the primary sealed, and leaves
	// it so, for webapps without the secondary yet.
	next := "rotated-secret-key-0123456789abcdef0123"
	both := startApp(t, a.worker.url, "SECRET_KEY_SECONDARY="+next)
	var keys settingsKeys
	both.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)
	if keys.Keys[1].Hint != "ated" {
		t.Errorf("with both keys the settings show %+v", keys.Keys)
	}
	if v := secretsStored(t, db, "SELECT api_key FROM model_keys WHERE env = 'OPENAI_API_KEY'"); v != before {
		t.Errorf("starting with both keys sealed the key again")
	}
	a.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)

	out, err := secretsSeal(t, testSecretKey, next)
	if err != nil || !strings.Contains(out, "sealed with SECRET_KEY_SECONDARY") {
		t.Fatalf("seal-secrets: %v\n%s", err, out)
	}
	// Rotated back afterwards, as the other tests seal with testSecretKey.
	t.Cleanup(func() {
		if out, err := secretsSeal(t, next, testSecretKey); err != nil {
			t.Errorf("rotating back: %v\n%s", err, out)
		}
	})
	if after := secretsStored(t, db, "SELECT api_key FROM model_keys WHERE env = 'OPENAI_API_KEY'"); after == before || !strings.HasPrefix(after, "sealed:v1:") {
		t.Errorf("the key is kept as %q, %q before", after, before)
	}

	if w, err := startWebapp(a.worker.url, 30*time.Second, "SECRET_KEY="+testSecretKey); err == nil {
		w.stop()
		t.Errorf("a webapp with only the old key started")
	}
	b := startApp(t, a.worker.url, "SECRET_KEY="+next)
	b.worker = a.worker
	b.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)
	if keys.Keys[1].Env != "OPENAI_API_KEY" || keys.Keys[1].Hint != "ated" {
		t.Errorf("with the new key the settings show %+v", keys.Keys)
	}
	f := b.newFlow("Rotated keys")
	b.startAt(f.ID, b.agent(f.ID, "Write", "Write.").ID)
	b.worker.onAgent("Write", handsIn(data("done")))
	b.finish(b.startRun(f.ID).ID)
	runs := b.worker.runsOf("Write")
	if got := runs[len(runs)-1].Keys; got["OPENAI_API_KEY"] != "sk-rotated" {
		t.Errorf("with the new key the agent got %v", got)
	}
}

// The key can be in a file SECRET_KEY_FILE names, as secret stores mounting
// secrets as files hand it over, but not in both.
func TestSecretsKeyFile(t *testing.T) {
	a := newApp(t)
	path := t.TempDir() + "/secret-key"
	if err := os.WriteFile(path, []byte(testSecretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := startApp(t, a.worker.url, "SECRET_KEY=", "SECRET_KEY_FILE="+path)
	b.call("GET", "/api/model-keys", nil, http.StatusOK, nil)
	if _, err := startWebapp(a.worker.url, 30*time.Second, "SECRET_KEY_FILE="+path); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Errorf("with the key in both: %v", err)
	}
}
