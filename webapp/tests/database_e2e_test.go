package tests

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A webapp started with a configuration file of its own, its environment
// picked by APP_ENV and its password read from a file, signs in with the
// password put in that file since: rotated in MySQL and in the file while
// it runs, the connections it opens next use the new one.
func TestDatabaseConfigRotate(t *testing.T) {
	needDB(t)
	fw := newFakeWorker(t)
	root := secretsDB(t)
	user := fmt.Sprintf("spoletta_rot_%d", time.Now().UnixNano()%1e6)
	if _, err := root.Exec(fmt.Sprintf("CREATE USER '%s'@'%%' IDENTIFIED BY 'first-password'", user)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec(fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'", user)) })
	if _, err := root.Exec(fmt.Sprintf("GRANT ALL ON `%s`.* TO '%s'@'%%'", testDB.name, user)); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	secret := filepath.Join(dir, "db_password")
	if err := os.WriteFile(secret, []byte("first-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "database.yaml")
	if err := os.WriteFile(config, []byte(`
development:
  host: nowhere.invalid
  database: nothing
  user: nobody
staging:
  host: '{{ env "DB_HOST" }}'
  port: '{{ env "DB_PORT" }}'
  database: '{{ env "DB_NAME" }}'
  user: `+user+`
  password: '{{ file (env "STAGING_PASSWORD_FILE") }}'
`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := startApp(t, fw.url, "DB_CONFIG="+config, "APP_ENV=staging", "STAGING_PASSWORD_FILE="+secret)
	a.worker = fw
	var flows []any
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)

	// Rotated, and the connections it has gone, as when they reach their
	// lifetime.
	if _, err := root.Exec(fmt.Sprintf("ALTER USER '%s'@'%%' IDENTIFIED BY 'second-password'", user)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("second-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	killConnections := func() int {
		rows, err := root.Query("SELECT id FROM information_schema.PROCESSLIST WHERE user = ?", user)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			root.Exec(fmt.Sprintf("KILL %d", id))
		}
		return len(ids)
	}
	if killConnections() == 0 {
		t.Fatalf("the webapp has no connections as %s", user)
	}
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
	if killConnections() == 0 {
		t.Errorf("the webapp didn't connect again as %s", user)
	}

	// With a password MySQL no longer takes in the file, new connections
	// fail.
	if err := os.WriteFile(secret, []byte("wrong-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	killConnections()
	if status, _ := a.do("GET", "/api/flows", nil); status == http.StatusOK {
		t.Errorf("answered with the wrong password in the file")
	}
	os.WriteFile(secret, []byte("second-password\n"), 0o600)
	a.call("GET", "/api/flows", nil, http.StatusOK, &flows)
}

// A configuration naming no environment APP_ENV picks stops the webapp
// starting, saying why.
func TestDatabaseConfigUnknownEnvironment(t *testing.T) {
	needDB(t)
	fw := newFakeWorker(t)
	_, err := startWebapp(fw.url, 30*time.Second, "APP_ENV=qa")
	if err == nil || !strings.Contains(err.Error(), `no environment "qa"`) {
		t.Errorf("started with APP_ENV=qa: %v", err)
	}
}

func needDB(t *testing.T) {
	t.Helper()
	switch {
	case testDB.fail != "":
		t.Fatal(testDB.fail)
	case testDB.skip != "":
		t.Skip(testDB.skip)
	}
}
