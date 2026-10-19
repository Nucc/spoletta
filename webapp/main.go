package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Where the webapp listens: on PORT, and on HOST when that is set. Without
// sign-in anyone who reaches the webapp can do anything in it, running bash
// on the worker included, so it then listens only on this machine unless
// HOST says otherwise; a container sets HOST to 0.0.0.0 and publishes the
// port on the host's loopback instead, as compose.yaml does.
func listenAddr(signIn bool) string {
	host := os.Getenv("HOST")
	if host == "" && !signIn {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, env("PORT", "8080"))
}

func openDB() (*sql.DB, error) {
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(env("DB_HOST", "127.0.0.1"), env("DB_PORT", "3306"))
	cfg.DBName = env("DB_NAME", "app")
	cfg.User = env("DB_USER", "app")
	cfg.Passwd = env("DB_PASSWORD", "apppass")
	cfg.ParseTime = true

	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}

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

func main() {
	db, err := openDB()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := migrate(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateMCP(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateSchedules(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateAssistant(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateUsers(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateUserRoles(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateTeams(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateOwners(db); err != nil {
		log.Fatal(err)
	}
	if err := migrateModelKeys(db); err != nil {
		log.Fatal(err)
	}
	// seal-secrets seals the secrets with the key coming in, and ends,
	// failing if some changed meanwhile, to be run again; see secrets.go.
	sealSecrets := len(os.Args) > 1 && os.Args[1] == "seal-secrets"
	changed, err := setUpSecrets(db, sealSecrets)
	if err != nil {
		log.Fatal(err)
	}
	if sealSecrets {
		if changed > 0 {
			os.Exit(1)
		}
		return
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.Dir(env("STATIC_DIR", "frontend/dist"))))
	workerURL := env("WORKER_URL", "http://127.0.0.1:8090")
	auth := newAuth(db)
	registerAuthRoutes(mux, db, auth)
	registerUserRoleRoutes(mux, db, auth)
	registerTeamRoutes(mux, db, auth)
	registerOwnerRoutes(mux, db)
	registerModelKeyRoutes(mux, db, workerURL)
	registerFlowListRoutes(mux, db, workerURL)
	registerProcessRoutes(mux, db)
	registerFlowRoutes(mux, db)
	registerBoardRoutes(mux, db)
	registerPasteRoutes(mux, db)
	registerStructRoutes(mux, db)
	registerSharedRoutes(mux, db)
	registerObjectRoutes(mux, db)
	registerQueryRoutes(mux, db)
	registerStateRoutes(mux, db)
	registerAttributeRoutes(mux, db)
	// How many agent, script and Sentry steps run at once.
	parallelSteps, err := strconv.Atoi(env("PARALLEL_STEPS", "3"))
	if err != nil || parallelSteps < 1 {
		log.Fatalf("PARALLEL_STEPS must be a positive number")
	}
	// Where the worker reaches this webapp's MCP proxy.
	mcp := newMCPBroker(db, env("MCP_PROXY_URL", "http://127.0.0.1:"+env("PORT", "8080")), auth != nil)
	registerMCPRoutes(mux, db, mcp)
	registerConnectorRoutes(mux, db)
	sentry := sentryConfig{url: os.Getenv("SENTRY_URL"), token: os.Getenv("SENTRY_AUTH_TOKEN")}
	registerSentryRoutes(mux, sentry)
	eng := newEngine(db, workerURL, sentry, mcp, parallelSteps)
	registerRunRoutes(mux, db, eng)
	registerRerunRoutes(mux, db, eng)
	registerDataRoutes(mux, db)
	registerScheduleRoutes(mux, db)
	go runSchedules(context.Background(), db, eng, time.Second)
	registerMessageRoutes(mux, db)
	registerFollowUpRoutes(mux, db, workerURL, mcp)
	go releaseEnded(db, workerURL)
	registerVersionRoutes(mux, db)
	registerHistoryRoutes(mux, db)
	registerOPLRoutes(mux, db)
	registerSetupRoutes(mux, db)
	registerAssistantRoutes(mux, db, workerURL)
	registerAgentRoutes(mux, workerURL)
	registerFileRoutes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		var version string
		if err := db.QueryRowContext(r.Context(), "SELECT VERSION()").Scan(&version); err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "ok, mysql %s\n", version)
	})

	addr := listenAddr(auth != nil)
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, auth.protect(guardFlows(db, mux))))
}
