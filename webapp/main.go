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

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.Dir(env("STATIC_DIR", "frontend/dist"))))
	workerURL := env("WORKER_URL", "http://127.0.0.1:8090")
	registerFlowListRoutes(mux, db, workerURL)
	registerProcessRoutes(mux, db)
	registerFlowRoutes(mux, db)
	registerBoardRoutes(mux, db)
	registerPasteRoutes(mux, db)
	registerStructRoutes(mux, db)
	registerObjectRoutes(mux, db)
	registerStateRoutes(mux, db)
	registerAttributeRoutes(mux, db)
	// How many agent, script and Sentry steps run at once.
	parallelSteps, err := strconv.Atoi(env("PARALLEL_STEPS", "3"))
	if err != nil || parallelSteps < 1 {
		log.Fatalf("PARALLEL_STEPS must be a positive number")
	}
	// Where the worker reaches this webapp's MCP proxy.
	mcp := newMCPBroker(db, env("MCP_PROXY_URL", "http://127.0.0.1:"+env("PORT", "8080")))
	registerMCPRoutes(mux, db, mcp)
	eng := newEngine(db, workerURL, sentryConfig{url: os.Getenv("SENTRY_URL"), token: os.Getenv("SENTRY_AUTH_TOKEN")}, mcp, parallelSteps)
	registerRunRoutes(mux, db, eng)
	registerScheduleRoutes(mux, db)
	go runSchedules(context.Background(), db, eng, time.Second)
	registerMessageRoutes(mux, db)
	registerFollowUpRoutes(mux, db, workerURL, mcp)
	registerVersionRoutes(mux, db)
	registerHistoryRoutes(mux, db)
	registerOPLRoutes(mux, db)
	registerSetupRoutes(mux, db)
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

	addr := ":" + env("PORT", "8080")
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
