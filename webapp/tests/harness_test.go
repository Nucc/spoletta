package tests

// End-to-end tests run the webapp as built, a process of its own per test,
// and drive it through its HTTP API, as the frontend does, on a real MySQL.
// Only the worker is faked: agents and scripts reply with what each test
// tells them to, by process name, and every request the worker gets is kept
// for the test to check.
//
// They need the compose MySQL; its root account creates databases of their
// own, dropped once they are done. TEST_DB_ADDR, TEST_DB_USER and
// TEST_DB_PASSWORD say where and as whom, by default 127.0.0.1:3306 as root
// with compose's default password. Without a MySQL listening there they are
// skipped; with one refusing them they fail. Run with -race, the webapp is
// built with the race detector too, and a race it reports fails the test.
//
// They run side by side, as many at once as -parallel says (the number of
// CPUs unless told), each on a database no other test uses while it runs:
// a test starts with parallel(t), which takes one from a pool of as many,
// made as they are first needed, and gives it back once the test and its
// webapps are done. A database is used by one test after another, so each
// test still finds what earlier ones left there. One database is migrated
// as the tests start, and every other is a copy of it, which is quicker.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

const testDBPrefix = "spoletta_test_"

// Set by race_test.go when the tests run with -race.
var raceEnabled bool

// The SECRET_KEY webapps start with, unless a test gives another.
const testSecretKey = "e2e-secret-key-0123456789abcdef0123"

// The token the webapp asks the worker with, which the fake worker checks
// on every request, as the real one does.
const testWorkerToken = "e2e-worker-token"

var testDB struct {
	// Where the webapp finds its databases, and the webapp built.
	host, port, user, password string
	binary                     string
	// Why the tests can't run: a reason to skip them, or to fail.
	skip, fail string
}

// The databases tests run on, one per test going at once.
var testDBs struct {
	// Those no test is using, and how many may be made.
	free chan string
	size int
	// What names them, and its root connection, which makes them.
	prefix string
	root   *sql.DB
	// A database migrated as the tests start, which no test works on, and
	// its tables: every other the tests work on but those starting empty
	// (emptyDatabase) is a copy of it.
	template string
	tables   []templateTable

	mu sync.Mutex
	// Every database made, to drop once the tests are done.
	made []string
	// The database each test going works on, by the test's name.
	taken map[string]string
}

// Tells databases made apart, such as tests' own, made at once.
var testDBSeq atomic.Int64

// A model provider every provider of the tests' webapps is reached at,
// /<provider>/ for each, which answers with the key it was reached with, and
// says what the call took.
// A custom provider there lists two models, /gateway/v1/models, to whoever
// has its key, typed or in the vault.
var testProvider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("X-Api-Key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if r.URL.Path == "/gateway/v1/models" {
		if key != "typed-key" && key != "gw-from-vault" {
			http.Error(w, "who are you", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"id": "gw-2"}, {"id": "gw-1"}}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "path": r.URL.Path, "usage": map[string]int{
		"input_tokens": 30, "output_tokens": 7, "cache_read_input_tokens": 1000, "cache_creation_input_tokens": 200}})
}))

// Where the webapps reach providers pi knows: the test provider.
func testUpstreams() string {
	var pairs []string
	for _, id := range []string{"anthropic", "openai", "amazon-bedrock", "openrouter"} {
		pairs = append(pairs, id+"="+testProvider.URL+"/"+id)
	}
	return strings.Join(pairs, ",")
}

func TestMain(m *testing.M) {
	// For -parallel, which says how many databases the tests may need.
	flag.Parse()
	cleanup := setUp()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Builds the webapp and creates the test database, migrated by starting
// the webapp once. Returns what undoes it.
func setUp() func() {
	addr, given := os.LookupEnv("TEST_DB_ADDR")
	if !given {
		addr = "127.0.0.1:3306"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		testDB.fail = fmt.Sprintf("TEST_DB_ADDR %q: %v", addr, err)
		return func() {}
	}
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = addr
	cfg.User = env("TEST_DB_USER", "root")
	cfg.Passwd = env("TEST_DB_PASSWORD", "rootpass")
	cfg.Timeout = 3 * time.Second

	root, err := sql.Open("mysql", cfg.FormatDSN())
	if err == nil {
		err = root.Ping()
	}
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && !given {
			testDB.skip = fmt.Sprintf("no MySQL at %s (start it with `podman compose up -d mysql`): %v", addr, err)
		} else {
			testDB.fail = fmt.Sprintf("MySQL at %s: %v; if another MySQL holds that address, set TEST_DB_ADDR, such as [::1]:3306", addr, err)
		}
		return func() {}
	}

	dir, err := os.MkdirTemp("", "webapp-tests-")
	if err != nil {
		testDB.fail = err.Error()
		return func() { root.Close() }
	}
	removeDir := func() { os.RemoveAll(dir) }
	testDB.binary = filepath.Join(dir, "webapp")
	args := []string{"build", "-o", testDB.binary}
	if raceEnabled {
		args = append(args, "-race")
	}
	build := exec.Command("go", append(args, ".")...)
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		testDB.fail = fmt.Sprintf("build the webapp: %v\n%s", err, out)
		return func() { root.Close(); removeDir() }
	}

	sweepTestDBs(root)
	testDB.host, testDB.port, testDB.user, testDB.password = host, port, cfg.User, cfg.Passwd
	testDBs.size = 1
	if f := flag.Lookup("test.parallel"); f != nil {
		if n, err := strconv.Atoi(f.Value.String()); err == nil && n > 1 {
			testDBs.size = n
		}
	}
	testDBs.free = make(chan string, testDBs.size)
	testDBs.taken = map[string]string{}
	testDBs.prefix = fmt.Sprintf("%s%d", testDBPrefix, time.Now().UnixNano())
	testDBs.root = root
	undo := func() {
		testDBs.mu.Lock()
		made := testDBs.made
		testDBs.mu.Unlock()
		for _, name := range made {
			if _, err := root.Exec("DROP DATABASE IF EXISTS " + name); err != nil {
				log.Printf("drop test database: %v", err)
			}
		}
		root.Close()
		removeDir()
	}
	// The template is migrated, by starting the webapp on it once, which
	// takes a while; a migration failing fails every test, saying why.
	testDBs.template = testDBs.prefix + "_template"
	testDBs.made = append(testDBs.made, testDBs.template)
	if _, err := root.Exec("CREATE DATABASE " + testDBs.template); err != nil {
		testDB.fail = "create the template database: " + err.Error()
		return undo
	}
	srv, err := startWebapp(testDBs.template, "http://127.0.0.1:1", time.Minute)
	if err != nil {
		testDB.fail = "migrate: " + err.Error()
		return undo
	}
	srv.stop()
	if testDBs.tables, err = readTemplate(testDBs.template); err != nil {
		testDB.fail = "read the template database: " + err.Error()
	}
	return undo
}

// A table of the template, as copyTemplate makes it again.
type templateTable struct {
	name string
	// What makes it, as SHOW CREATE TABLE says, but where its AUTO_INCREMENT
	// was, which the rows copied set, as on a database just migrated.
	create string
	// The columns its rows are copied by, without generated ones, which
	// MySQL works out again; none when it has no rows.
	columns string
}

var autoIncrement = regexp.MustCompile(` AUTO_INCREMENT=\d+`)

// Reads the tables of the migrated database name, refusing what the copy
// leaves out, as a copy would then differ from a database migrated.
func readTemplate(name string) ([]templateTable, error) {
	var others int
	if err := testDBs.root.QueryRow(`SELECT
		(SELECT COUNT(*) FROM information_schema.VIEWS WHERE TABLE_SCHEMA = ?) +
		(SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?) +
		(SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?) +
		(SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?)`, name, name, name, name).Scan(&others); err != nil {
		return nil, err
	}
	if others > 0 {
		return nil, errors.New("the migrations make views, triggers, routines or events, which copyTemplate in harness_test.go doesn't copy yet")
	}
	rows, err := testDBs.root.Query(`SELECT TABLE_NAME, COLUMN_NAME, GENERATION_EXPRESSION FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME IN (SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE')
		ORDER BY TABLE_NAME, ORDINAL_POSITION`, name, name)
	if err != nil {
		return nil, err
	}
	var tables []templateTable
	for rows.Next() {
		var table, column, generated string
		if err := rows.Scan(&table, &column, &generated); err != nil {
			rows.Close()
			return nil, err
		}
		if len(tables) == 0 || tables[len(tables)-1].name != table {
			tables = append(tables, templateTable{name: table})
		}
		if generated == "" {
			last := &tables[len(tables)-1]
			if last.columns != "" {
				last.columns += ", "
			}
			last.columns += "`" + column + "`"
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range tables {
		tt := &tables[i]
		var table string
		if err := testDBs.root.QueryRow(fmt.Sprintf("SHOW CREATE TABLE `%s`.`%s`", name, tt.name)).Scan(&table, &tt.create); err != nil {
			return nil, err
		}
		tt.create = autoIncrement.ReplaceAllString(tt.create, "")
		var some bool
		if err := testDBs.root.QueryRow(fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM `%s`.`%s`)", name, tt.name)).Scan(&some); err != nil {
			return nil, err
		}
		if !some {
			tt.columns = ""
		}
	}
	return tables, nil
}

// Makes a database by that name as the template is, its tables and their
// rows, which is quicker than migrating it.
func copyTemplate(name string) error {
	if _, err := testDBs.root.Exec("CREATE DATABASE " + name); err != nil {
		return fmt.Errorf("create test database: %v", err)
	}
	db, err := sql.Open("mysql", testDSN(name))
	if err != nil {
		return err
	}
	defer db.Close()
	// One connection throughout, so the rows go in with its foreign key
	// checks off, in whatever order.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		return err
	}
	for _, tt := range testDBs.tables {
		if _, err := conn.ExecContext(ctx, tt.create); err != nil {
			return fmt.Errorf("copy the template's %s: %v", tt.name, err)
		}
	}
	for _, tt := range testDBs.tables {
		if tt.columns == "" {
			continue
		}
		q := fmt.Sprintf("INSERT INTO `%s` (%s) SELECT %s FROM `%s`.`%s`", tt.name, tt.columns, tt.columns, testDBs.template, tt.name)
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("copy the template's %s: %v", tt.name, err)
		}
	}
	return nil
}

// Where a connection to the named database goes, as the webapp's.
func testDSN(name string) string {
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(testDB.host, testDB.port)
	cfg.DBName = name
	cfg.User, cfg.Passwd = testDB.user, testDB.password
	return cfg.FormatDSN()
}

// Takes a database no test going uses: one free, else a new one while
// fewer were made than tests may run at once, else the first given back.
// Each test going holds one at most, so one is always given back in time.
func takeTestDB() (string, error) {
	select {
	case name := <-testDBs.free:
		return name, nil
	default:
	}
	testDBs.mu.Lock()
	// The template is one of those made, but no test takes it.
	if len(testDBs.made)-1 >= testDBs.size {
		testDBs.mu.Unlock()
		return <-testDBs.free, nil
	}
	name := fmt.Sprintf("%s_%d", testDBs.prefix, len(testDBs.made))
	testDBs.made = append(testDBs.made, name)
	testDBs.mu.Unlock()
	return name, copyTemplate(name)
}

// Runs the test beside others, on a database it takes for as long as it
// runs, which no other test uses meanwhile; see database. Every test that
// uses the database starts with it.
func parallel(t *testing.T) {
	t.Helper()
	if strings.Contains(t.Name(), "/") {
		t.Fatal("parallel(t) is for a test, not a subtest: subtests run one after another, on their test's database")
	}
	t.Parallel()
	if testDB.fail != "" || testDB.skip != "" {
		// database says why, to a test that needs one.
		return
	}
	name, err := takeTestDB()
	if err != nil {
		t.Fatal(err)
	}
	testDBs.mu.Lock()
	testDBs.taken[t.Name()] = name
	testDBs.mu.Unlock()
	// Before anything else, so it is given back last, once the test's
	// webapps stopped and its cleanups are done with it.
	t.Cleanup(func() {
		testDBs.mu.Lock()
		delete(testDBs.taken, t.Name())
		testDBs.mu.Unlock()
		// A task board left playing, such as the one the routes sweep plays,
		// would start runs in the next test's webapp, of flows its fake
		// worker knows nothing of.
		if _, err := testDBs.root.Exec("UPDATE " + name + ".task_boards SET playing = FALSE WHERE playing"); err != nil {
			t.Errorf("pausing the task boards left playing on %s: %v", name, err)
		}
		testDBs.free <- name
	})
}

// The name of the database the test, or the test a subtest is of, works
// on: the one it took starting (parallel), or the one it said since
// (useDatabase).
func database(t *testing.T) string {
	t.Helper()
	needDB(t)
	test, _, _ := strings.Cut(t.Name(), "/")
	testDBs.mu.Lock()
	name, ok := testDBs.taken[test]
	testDBs.mu.Unlock()
	if !ok {
		t.Fatal("the test uses the database without having taken one: start it with parallel(t)")
	}
	return name
}

// Has the test work on the named database from here on, such as one of its
// own (newDatabase), rather than the one it took.
func useDatabase(t *testing.T, name string) {
	t.Helper()
	database(t)
	test, _, _ := strings.Cut(t.Name(), "/")
	testDBs.mu.Lock()
	testDBs.taken[test] = name
	testDBs.mu.Unlock()
}

// Makes a database for the test alone, its name ending in suffix, as a
// database just migrated is, and drops it once the test is done.
func newDatabase(t *testing.T, suffix string) string {
	t.Helper()
	needDB(t)
	name := fmt.Sprintf("%s%d_%d%s", testDBPrefix, time.Now().UnixNano(), testDBSeq.Add(1), suffix)
	t.Cleanup(func() { testDBs.root.Exec("DROP DATABASE IF EXISTS " + name) })
	if err := copyTemplate(name); err != nil {
		t.Fatal(err)
	}
	return name
}

// Makes an empty database for the test alone, its name ending in suffix,
// and drops it once the test is done. A webapp started on it migrates it.
func emptyDatabase(t *testing.T, suffix string) string {
	t.Helper()
	needDB(t)
	name := fmt.Sprintf("%s%d_%d%s", testDBPrefix, time.Now().UnixNano(), testDBSeq.Add(1), suffix)
	if _, err := testDBs.root.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testDBs.root.Exec("DROP DATABASE IF EXISTS " + name) })
	return name
}

// Drops the databases of test runs that went down before dropping theirs,
// leaving recent ones to test runs still going.
func sweepTestDBs(root *sql.DB) {
	rows, err := root.Query("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE ?", testDBPrefix+"%")
	if err != nil {
		return
	}
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	rows.Close()
	cutoff := time.Now().Add(-time.Hour).UnixNano()
	for _, n := range names {
		var started int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(n, testDBPrefix), "%d", &started); err == nil && started < cutoff {
			root.Exec("DROP DATABASE IF EXISTS " + n)
		}
	}
}

// A webapp process, and what it logged.
type webapp struct {
	url    string
	cmd    *exec.Cmd
	log    *syncBuffer
	exited chan struct{}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Starts the webapp on the named database with its worker at workerURL, and
// the environment variables in extra, which may name another database, and
// waits for it to answer.
func startWebapp(db, workerURL string, wait time.Duration, extra ...string) (*webapp, error) {
	for tries := 1; ; tries++ {
		w, err := startWebappOnce(db, workerURL, wait, extra...)
		// The port it was given, free a moment before, was taken by another
		// test's webapp or server starting meanwhile.
		if err != nil && tries < 3 && strings.Contains(err.Error(), "address already in use") {
			continue
		}
		return w, err
	}
}

func startWebappOnce(db, workerURL string, wait time.Duration, extra ...string) (*webapp, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	static, err := os.MkdirTemp("", "webapp-static-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(static)

	w := &webapp{url: "http://127.0.0.1:" + port, log: &syncBuffer{}}
	w.cmd = exec.Command(testDB.binary)
	w.cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"DB_HOST=" + testDB.host,
		"DB_PORT=" + testDB.port,
		"DB_NAME=" + db,
		"DB_USER=" + testDB.user,
		"DB_PASSWORD=" + testDB.password,
		"PORT=" + port,
		"WORKER_URL=" + workerURL,
		"MCP_PROXY_URL=" + w.url,
		"PARALLEL_STEPS=3",
		// Lets schedules come round every second; see schedules_e2e_test.go.
		"SCHEDULE_SECONDS=1",
		"STATIC_DIR=" + static,
		// Seals the secrets it keeps; see secrets_e2e_test.go.
		"SECRET_KEY=" + testSecretKey,
		// What the fake worker takes requests with.
		"WORKER_TOKEN=" + testWorkerToken,
		// Providers are reached at the test provider; the webapp's
		// environment has the key the worker's gateway providers read,
		// and the vault's, VAULT_*.
		"LLM_UPSTREAMS=" + testUpstreams(),
		"GATEWAY_KEY=gw-from-env",
		"VAULT_GATEWAY_KEY=gw-from-vault",
		// Told to stop, it exits at once, without the race detector's
		// second of waiting for reports; races are reported as they happen.
		"GORACE=atexit_sleep_ms=0",
		// Tests take turns on a database and leave runs going as they end,
		// which the next test's webapp would take over and carry on with
		// its own fake worker; so they end, unless a test takes runs over
		// on a database of its own (see adopt_e2e_test.go).
		"RESUME_RUNS=false",
	}
	w.cmd.Env = append(w.cmd.Env, extra...)
	w.cmd.Stdout, w.cmd.Stderr = w.log, w.log
	if err := w.cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		w.cmd.Wait()
		close(exited)
	}()
	deadline := time.Now().Add(wait)
	for {
		resp, err := http.Get(w.url + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				w.exited = exited
				return w, nil
			}
		}
		select {
		case <-exited:
			return nil, fmt.Errorf("the webapp exited:\n%s", w.log)
		default:
		}
		if time.Now().After(deadline) {
			w.cmd.Process.Kill()
			<-exited
			return nil, fmt.Errorf("the webapp didn't answer in %s:\n%s", wait, w.log)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Tells the webapp to stop, as Kubernetes does, so it leaves the replicas
// and the next webapp takes over, or ends, every run it was walking; killed
// if it doesn't stop. Returns what it logged.
func (w *webapp) stop() string {
	w.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-w.exited:
	case <-time.After(10 * time.Second):
		w.cmd.Process.Kill()
		<-w.exited
	}
	return w.log.String()
}

// Kills the webapp, as a crash would, so it doesn't leave the replicas.
func (w *webapp) kill() {
	w.cmd.Process.Kill()
	<-w.exited
}

// The webapp of a test, with a fake worker of its own. A test finds what
// earlier tests left on its database, so each works in flows of its own.
type app struct {
	t      *testing.T
	url    string
	worker *fakeWorker
	// Sends its requests; one with a session when people sign in, see
	// users_e2e_test.go.
	client *http.Client
	// Its process.
	proc *webapp
	// The project its requests name, as the browser's tab on a project
	// does, every one of them; 0 is Legacy. See projects_e2e_test.go.
	project int64
}

func newApp(t *testing.T) *app {
	t.Helper()
	fw := newFakeWorker(t)
	a := startApp(t, fw.url)
	a.worker = fw
	return a
}

// The webapp of a test, with its worker at workerURL and the environment
// variables in extra. It ends with the test, before the worker, so steps
// going end first.
func startApp(t *testing.T, workerURL string, extra ...string) *app {
	t.Helper()
	// A start on an empty database (emptyDatabase) migrates it first,
	// which, with other tests' webapps going, may take a while.
	w, err := startWebapp(database(t), workerURL, 2*time.Minute, extra...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		out := w.stop()
		switch {
		case strings.Contains(out, "WARNING: DATA RACE"):
			t.Errorf("the webapp reported a data race:\n%s", out)
		// Such as a handler asking for the project of a request that
		// names none (requestProject).
		case strings.Contains(out, "http: panic serving"):
			t.Errorf("the webapp panicked serving a request:\n%s", out)
		// A row inserted without naming its project, which has no default
		// (migration 00090), as what the webapp does in the background
		// only logs.
		case strings.Contains(out, "doesn't have a default value"):
			t.Errorf("the webapp inserted a row without a column it must name:\n%s", out)
		case t.Failed():
			t.Logf("the webapp logged:\n%s", out)
		}
	})
	return &app{t: t, url: w.url, client: http.DefaultClient, proc: w}
}

// Sends a request with body as JSON, if any, and returns the status and reply.
func (a *app) do(method, path string, body any) (int, []byte) {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.url+path, r)
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	a.inProject(req)
	resp, err := a.client.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(resp.Body)
	if err != nil {
		a.t.Fatal(err)
	}
	return resp.StatusCode, reply
}

// The project the app's requests name: the one the test says, else Legacy,
// project 1, where most tests work.
func (a *app) projectID() int64 {
	if a.project != 0 {
		return a.project
	}
	return 1
}

// Names the app's project in the request, as the browser's tab on it does
// with every request to /api/; for a request the test sends itself rather
// than through do.
func (a *app) inProject(req *http.Request) {
	req.Header.Set("X-Spoletta-Project", fmt.Sprint(a.projectID()))
}

// The address of a page of the app's at path that the browser opens, such
// as an MCP server's sign-in window: it can't send the header, so it names
// the app's project in its query instead.
func (a *app) pageURL(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%s%sproject=%d", a.url, path, sep, a.projectID())
}

// Sends a request that must get status want, and decodes the reply into out, if given.
func (a *app) call(method, path string, body any, want int, out any) {
	a.t.Helper()
	status, reply := a.do(method, path, body)
	if status != want {
		a.t.Fatalf("%s %s = %d %s, want %d", method, path, status, bytes.TrimSpace(reply), want)
	}
	if out != nil {
		if err := json.Unmarshal(reply, out); err != nil {
			a.t.Fatalf("%s %s: decode %s: %v", method, path, reply, err)
		}
	}
}

// Sends a request that must be refused with status want, with a message
// holding msg.
func (a *app) refused(method, path string, body any, want int, msg string) {
	a.t.Helper()
	status, reply := a.do(method, path, body)
	if status != want || !strings.Contains(string(reply), msg) {
		a.t.Errorf("%s %s = %d %q, want %d with %q", method, path, status, bytes.TrimSpace(reply), want, msg)
	}
}

func (a *app) newFlow(name string) flow {
	a.t.Helper()
	var f flow
	a.call("POST", "/api/flows", map[string]any{"name": name}, http.StatusCreated, &f)
	return f
}

// Adds a process to the flow's top board; p's flow is filled in.
func (a *app) newProcess(f int64, p map[string]any) process {
	a.t.Helper()
	p["flow"] = f
	var out process
	a.call("POST", "/api/processes", p, http.StatusCreated, &out)
	return out
}

func (a *app) agent(f int64, name, prompt string) process {
	a.t.Helper()
	return a.newProcess(f, map[string]any{"name": name, "kind": kindAgent, "prompt": prompt})
}

func (a *app) newObject(f int64, name string) object {
	a.t.Helper()
	var o object
	a.call("POST", "/api/objects", map[string]any{"flow": f, "name": name}, http.StatusCreated, &o)
	return o
}

func (a *app) newState(object int64, name string) objectState {
	a.t.Helper()
	var s objectState
	a.call("POST", fmt.Sprintf("/api/objects/%d/states", object), map[string]any{"name": name}, http.StatusCreated, &s)
	return s
}

func (a *app) output(p, o int64) link {
	a.t.Helper()
	return a.link(map[string]any{"process": p, "object": o, "direction": directionOutput})
}

func (a *app) input(p, o int64) link {
	a.t.Helper()
	return a.link(map[string]any{"process": p, "object": o, "direction": directionInput})
}

func (a *app) link(l map[string]any) link {
	a.t.Helper()
	var out link
	a.call("POST", "/api/links", l, http.StatusCreated, &out)
	return out
}

func (a *app) startAt(f, p int64) {
	a.t.Helper()
	a.call("PATCH", fmt.Sprintf("/api/start?flow=%d", f), map[string]any{"target": p}, http.StatusOK, nil)
}

func (a *app) startRun(f int64) run {
	a.t.Helper()
	var rn run
	a.call("POST", fmt.Sprintf("/api/runs?flow=%d", f), nil, http.StatusCreated, &rn)
	return rn
}

type runDetail struct {
	run
	Steps   []step      `json:"steps"`
	Objects []runObject `json:"objects"`
}

func (a *app) getRun(id int64) runDetail {
	a.t.Helper()
	var d runDetail
	a.call("GET", fmt.Sprintf("/api/runs/%d", id), nil, http.StatusOK, &d)
	return d
}

// Polls until cond holds of the run, and returns it as it is then.
func (a *app) waitRun(id int64, what string, cond func(runDetail) bool) runDetail {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d := a.getRun(id)
		if cond(d) {
			return d
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("run %d: timed out waiting for %s; it is %s with steps %s", id, what, d.Status, describeSteps(d))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Waits for the run to end.
func (a *app) finish(id int64) runDetail {
	a.t.Helper()
	return a.waitRun(id, "it to end", func(d runDetail) bool { return d.Status != runRunning })
}

// Waits for a step of the named process in the given status that isn't one
// of those already seen, and returns it.
func (a *app) waitStep(id int64, process, status string, seen ...int64) step {
	a.t.Helper()
	var found step
	a.waitRun(id, fmt.Sprintf("a step of %q %s", process, status), func(d runDetail) bool {
		for _, s := range d.Steps {
			if s.ProcessName == process && s.Status == status && !slices.Contains(seen, s.ID) {
				found = s
				return true
			}
		}
		return false
	})
	return found
}

func describeSteps(d runDetail) string {
	var parts []string
	for _, s := range d.Steps {
		p := s.ProcessName + ":" + s.Status
		if s.Error != "" {
			p += " (" + s.Error + ")"
		}
		parts = append(parts, p)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// The steps of the named process, in the order they started.
func stepsOf(d runDetail, process string) []step {
	var ss []step
	for _, s := range d.Steps {
		if s.ProcessName == process {
			ss = append(ss, s)
		}
	}
	return ss
}

// The one step of the named process; fails unless there is exactly one.
func onlyStep(t *testing.T, d runDetail, process string) step {
	t.Helper()
	ss := stepsOf(d, process)
	if len(ss) != 1 {
		t.Fatalf("%d steps of %q, want 1; steps %s", len(ss), process, describeSteps(d))
	}
	return ss[0]
}

// What the run's objects of that name held, in the order they were generated.
func contentsOf(d runDetail, object string) []string {
	var cs []string
	for _, o := range d.Objects {
		if o.Name == object {
			cs = append(cs, o.Content)
		}
	}
	return cs
}

// A worker whose agents and scripts reply as the test says, by process name.
type fakeWorker struct {
	t   *testing.T
	url string

	mu      sync.Mutex
	agents  map[string]func(context.Context, agentRun) runResult
	scripts map[string]func(context.Context, scriptRun) runResult
	// The messages an agent's conversation records, by process name,
	// instead of the one every agent sends.
	messages map[string][]map[string]any
	// What a script prints as it runs, by process name, sent before it
	// replies: each a stream and its text. Like the messages, it may print
	// the step's credentials token, as stepToken says.
	scriptOutput map[string][][2]string
	// Every run asked for, in the order asked.
	agentRuns  []agentRun
	scriptRuns []scriptRun
	// The runs whose workspaces the webapp asked to release, in the order asked.
	released []int64
	// The projects whose shared repositories it was asked to remove.
	deletedProjects []int64
	// The diffs the webapp asked for, in the order asked, and what the
	// worker replies to each; see worker/diff.go.
	diffs     []map[string]string
	diffReply func(map[string]string) map[string]any
	// How many requests came without the worker's token.
	refused int
	// Whether agents leave reaching their models to the test, rather than
	// reaching every provider granted as they start.
	quiet bool
	// The coding agents it says it offers; none said, as a worker from
	// before there was a choice, when nil.
	harnesses []string
}

func newFakeWorker(t *testing.T) *fakeWorker {
	fw := &fakeWorker{t: t, agents: map[string]func(context.Context, agentRun) runResult{}, scripts: map[string]func(context.Context, scriptRun) runResult{}, messages: map[string][]map[string]any{}, scriptOutput: map[string][][2]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		fw.mu.Lock()
		harnesses := fw.harnesses
		fw.mu.Unlock()
		writeJSON(w, http.StatusOK, agentModels{Default: "fake/model", Models: []string{"fake/model"}, Harnesses: harnesses})
	})
	// Its models read OpenAI's key and a gateway's, reached at the test
	// provider as its models.json says.
	mux.HandleFunc("GET /host-providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"id": "amazon-bedrock", "env": "GATEWAY_KEY", "baseUrl": testProvider.URL + "/gateway-bedrock"},
			{"id": "fireworks", "env": "GATEWAY_KEY", "baseUrl": testProvider.URL + "/gateway-fireworks"},
			{"id": "openai", "env": "OPENAI_API_KEY", "baseUrl": testProvider.URL + "/gateway-openai"},
		})
	})
	// pi knows Anthropic, Amazon Bedrock and OpenAI, with a model or two
	// each, and prices the model agents ask for at Anthropic's.
	mux.HandleFunc("GET /catalog", func(w http.ResponseWriter, r *http.Request) {
		models := func(ids ...string) []map[string]any {
			var ms []map[string]any
			for _, id := range ids {
				ms = append(ms, map[string]any{"id": id, "name": id})
			}
			return ms
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": []map[string]any{
			{"id": "anthropic", "name": "Anthropic", "env": "ANTHROPIC_API_KEY", "models": models("claude-a", "claude-b")},
			{"id": "amazon-bedrock", "name": "Amazon Bedrock", "env": "AWS_BEARER_TOKEN_BEDROCK", "models": models("global.anthropic.claude-b")},
			{"id": "openai", "name": "OpenAI", "env": "OPENAI_API_KEY", "models": models("gpt-x")},
		}, "prices": map[string]any{
			"anthropic": map[string]any{"fake-model": map[string]float64{"input": 3, "output": 15, "cacheRead": 0.3, "cacheWrite": 3.75}},
		}})
	})
	mux.HandleFunc("POST /workspaces/delete", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{})
	})
	mux.HandleFunc("POST /projects/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Project int64 `json:"project"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fw.mu.Lock()
		fw.deletedProjects = append(fw.deletedProjects, body.Project)
		fw.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /workspaces/release", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Runs []int64 `json:"runs"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fw.mu.Lock()
		fw.released = append(fw.released, body.Runs...)
		fw.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string][]string{"released": {}})
	})
	mux.HandleFunc("POST /workspaces/diff", func(w http.ResponseWriter, r *http.Request) {
		// As the real one, the project a number and the rest strings; kept
		// as text each.
		var raw map[string]any
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		if err := dec.Decode(&raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := raw["project"].(json.Number); raw["project"] != nil && !ok {
			http.Error(w, "the project is a number", http.StatusBadRequest)
			return
		}
		body := map[string]string{}
		for k, v := range raw {
			body[k] = fmt.Sprint(v)
		}
		fw.mu.Lock()
		fw.diffs = append(fw.diffs, body)
		reply := fw.diffReply
		fw.mu.Unlock()
		if reply == nil {
			writeJSON(w, http.StatusOK, map[string]string{"error": "the fake worker keeps no such repository of the workspace"})
			return
		}
		writeJSON(w, http.StatusOK, reply(body))
	})
	// As the real one: a record per line, each message of the conversation
	// and a line it logs, then the result.
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var run agentRun
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		run.Raw = string(body)
		fw.mu.Lock()
		quiet := fw.quiet
		fw.mu.Unlock()
		if !quiet {
			run.Reached = reachModels(run.LLM)
		}
		fw.mu.Lock()
		fw.agentRuns = append(fw.agentRuns, run)
		reply := fw.agents[run.ProcessName]
		messages := fw.messages[run.ProcessName]
		fw.mu.Unlock()
		res := runResult{Error: "the fake worker has no agent for " + run.ProcessName}
		if reply != nil {
			res = reply(r.Context(), run)
		} else {
			t.Errorf("fake worker: no agent for %q", run.ProcessName)
		}
		enc := json.NewEncoder(w)
		enc.Encode(map[string]any{"log": logLine{Time: time.Now(), Level: "info", Message: "fake pi runs " + run.ProcessName}})
		if messages == nil {
			messages = []map[string]any{{"role": "assistant", "content": []map[string]any{{"type": "text", "text": "working on " + run.ProcessName}}}}
		}
		for _, m := range messages {
			b, _ := json.Marshal(m)
			enc.Encode(map[string]any{"message": json.RawMessage(stepToken(string(b), run.CredentialsToken))})
		}
		enc.Encode(map[string]any{"result": res})
	})
	mux.HandleFunc("POST /script", func(w http.ResponseWriter, r *http.Request) {
		var run scriptRun
		if err := json.NewDecoder(r.Body).Decode(&run); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fw.mu.Lock()
		fw.scriptRuns = append(fw.scriptRuns, run)
		reply := fw.scripts[run.ProcessName]
		output := fw.scriptOutput[run.ProcessName]
		fw.mu.Unlock()
		// As the real one: streamed when asked to, what the script prints
		// first and its log as it goes, else the result alone.
		stream := strings.Contains(r.Header.Get("Accept"), "application/x-ndjson")
		enc := json.NewEncoder(w)
		started := logLine{Time: time.Now(), Level: "info", Message: "fake bash runs " + run.ProcessName}
		if stream {
			w.Header().Set("Content-Type", "application/x-ndjson")
			enc.Encode(map[string]any{"log": started})
			for _, o := range output {
				enc.Encode(map[string]any{"output": map[string]string{"stream": o[0], "text": stepToken(o[1], run.CredentialsToken)}})
			}
			w.(http.Flusher).Flush()
		}
		res := runResult{Error: "the fake worker has no script for " + run.ProcessName}
		if reply != nil {
			res = reply(r.Context(), run)
		} else {
			t.Errorf("fake worker: no script for %q", run.ProcessName)
		}
		if !stream {
			res.Log = append([]logLine{started}, res.Log...)
			writeJSON(w, http.StatusOK, res)
			return
		}
		for _, l := range res.Log {
			enc.Encode(map[string]any{"log": l})
		}
		res.Log = nil
		enc.Encode(map[string]any{"result": res})
	})
	// As the real one with WORKER_TOKEN set: nothing gets through without it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testWorkerToken {
			fw.mu.Lock()
			fw.refused++
			fw.mu.Unlock()
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the worker takes requests only from the webapp, with WORKER_TOKEN"})
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	fw.url = srv.URL
	return fw
}

// What a step prints or says with the step's credentials token where it
// says {token}, its first ten characters and the rest where it says
// {token-head} and {token-tail}, as a script printing it a piece at a time,
// and basic auth's whoever:token in base64 where it says {token-basic}, as
// curl -v prints it.
func stepToken(s, token string) string {
	head, tail := token, ""
	if len(token) > 10 {
		head, tail = token[:10], token[10:]
	}
	basic := base64.StdEncoding.EncodeToString([]byte("whoever:" + token))
	return strings.NewReplacer("{token}", token, "{token-head}", head, "{token-tail}", tail, "{token-basic}", basic).Replace(s)
}

func (fw *fakeWorker) onAgent(process string, reply func(context.Context, agentRun) runResult) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	fw.agents[process] = reply
}

// Has the named agent's conversation record these messages.
func (fw *fakeWorker) onMessages(process string, messages ...map[string]any) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	fw.messages[process] = messages
}

func (fw *fakeWorker) onScript(process string, reply func(context.Context, scriptRun) runResult) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	fw.scripts[process] = reply
}

// The runs of the named agent asked for so far.
func (fw *fakeWorker) runsOf(process string) []agentRun {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	var rs []agentRun
	for _, r := range fw.agentRuns {
		if r.ProcessName == process {
			rs = append(rs, r)
		}
	}
	return rs
}

// The script runs asked for so far, of every process.
func (fw *fakeWorker) scriptRunsSoFar() []scriptRun {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return slices.Clone(fw.scriptRuns)
}

// Removes the named MCP servers once the test is over, so a later test, or
// this one run again on the same database, may add them by the same names.
func forgetMCPServers(t *testing.T, names ...string) {
	t.Helper()
	db := secretsDB(t)
	t.Cleanup(func() {
		for _, name := range names {
			db.Exec("DELETE FROM mcp_servers WHERE name = ?", name)
		}
	})
}

// Receives what a step or server sends on ch, failing the test if nothing
// comes in a while rather than hanging the whole package.
func receive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// Reaches each provider the step may through the proxy, as an agent does
// with its token, and returns the key each was reached with, by provider.
func reachModels(llm *llmAccess) map[string]string {
	reached := map[string]string{}
	if llm == nil {
		return reached
	}
	for _, id := range llm.Providers {
		status, key := reachModel(llm, id)
		if status == http.StatusOK {
			reached[id] = key
		}
	}
	return reached
}

// Reaches the provider through the proxy with the step's token, returning
// the status and the key the provider was reached with.
func reachModel(llm *llmAccess, provider string) (int, string) {
	req, _ := http.NewRequest("POST", llm.URL+"/"+provider+"/v1/messages", strings.NewReader(`{"model": "fake-model"}`))
	req.Header.Set("X-Api-Key", llm.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	var got struct {
		Key string `json:"key"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got.Key
}

// An agent that hands in v through submit_output, every time.
func handsIn(v any) func(context.Context, agentRun) runResult {
	return func(context.Context, agentRun) runResult { return submitted(v) }
}

func submitted(v any) runResult {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return runResult{OK: true, Output: string(b), Submitted: true}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// What a process generating one object without attributes hands in.
func data(v any) map[string]any { return map[string]any{dataAttribute: v} }
