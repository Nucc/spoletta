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
	"strings"
	"time"
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

// As it starts, the webapp logs what it did, step by step, and stops with
// what went wrong if one fails, naming the step; see logs.go for runs' logs.
func main() {
	migrating := len(os.Args) > 1 && os.Args[1] == "migrate"
	if !migrating {
		log.Printf("starting in the %s environment", appEnv())
	}
	db, err := openDB()
	if err != nil {
		log.Fatalf("couldn't open the database: %v", err)
	}
	defer db.Close()
	if !migrating {
		logDatabase(db)
	}

	// migrate status and migrate down-to look at or undo migrations, and
	// end; see migrations.go.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := migrateCommand(context.Background(), db, os.Args[2:], os.Stdout); err != nil {
			db.Close()
			log.Fatal(err)
		}
		return
	}
	if err := migrateDatabase(context.Background(), db); err != nil {
		log.Fatalf("couldn't migrate the database: %v", err)
	}
	// seal-secrets seals the secrets with the key coming in, and ends,
	// failing if some changed meanwhile, to be run again; see secrets.go.
	sealSecrets := len(os.Args) > 1 && os.Args[1] == "seal-secrets"
	changed, err := setUpSecrets(db, sealSecrets)
	if err != nil {
		log.Fatalf("couldn't set up the secrets: %v", err)
	}
	if sealSecrets {
		if changed > 0 {
			os.Exit(1)
		}
		return
	}
	timeout, err := replicaTimeout()
	if err != nil {
		log.Fatal(err)
	}
	if err := joinReplicas(db, timeout); err != nil {
		log.Fatalf("couldn't join the replicas, or end what those that stopped left running: %v", err)
	}
	go heartbeat(context.Background(), db, timeout)
	go leaveOnStop(db)

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.Dir(env("STATIC_DIR", "frontend/dist"))))
	workerURL := env("WORKER_URL", "http://127.0.0.1:8090")
	auth := newAuth(db)
	if auth != nil {
		var with []string
		for _, p := range auth.providers {
			with = append(with, p.label)
		}
		log.Printf("people sign in with %s", strings.Join(with, " or "))
	}
	if workerToken, err = secretKeyFromEnv("WORKER_TOKEN"); err != nil {
		log.Fatalf("%v", err)
	}
	if workerToken == "" {
		log.Printf("the worker is at %s; WARNING: WORKER_TOKEN isn't set, so the worker answers anyone reaching it", workerURL)
	} else {
		log.Printf("the worker is at %s, asked with WORKER_TOKEN", workerURL)
	}
	go checkWorker(workerURL)
	if gitTokens, err = newGitTokenSource(); err != nil {
		log.Fatalf("%v", err)
	}
	logGitTokens()
	registerGitTokenRoutes(mux)
	registerAuthRoutes(mux, db, auth)
	registerUserRoleRoutes(mux, db, auth)
	registerTeamRoutes(mux, db, auth)
	registerOwnerRoutes(mux, db)
	registerModelKeyRoutes(mux, db)
	registerModelProviderRoutes(mux, db, workerURL)
	registerFlowListRoutes(mux, db, workerURL)
	registerTagRoutes(mux, db)
	registerFavouriteRoutes(mux, db)
	registerProcessRoutes(mux, db)
	registerFlowRoutes(mux, db)
	registerBoardRoutes(mux, db)
	registerPasteRoutes(mux, db)
	registerStructRoutes(mux, db)
	registerSharedRoutes(mux, db)
	registerRecordEditRoutes(mux, db)
	registerObjectRoutes(mux, db)
	registerQueryRoutes(mux, db)
	registerStateRoutes(mux, db)
	registerAttributeRoutes(mux, db)
	// How many agent, script and connector steps run at once.
	parallelSteps, err := strconv.Atoi(env("PARALLEL_STEPS", "3"))
	if err != nil || parallelSteps < 1 {
		log.Fatalf("PARALLEL_STEPS must be a positive number")
	}
	log.Printf("runs up to %d agent, script and connector steps at once at this replica (PARALLEL_STEPS)", parallelSteps)
	// Where the worker reaches this webapp's proxies, for MCP servers and for
	// models; MCP_PROXY_URL is its old name.
	proxyURL := env("PROXY_URL", env("MCP_PROXY_URL", "http://127.0.0.1:"+env("PORT", "8080")))
	mcp := newMCPBroker(db, proxyURL, auth != nil)
	registerMCPRoutes(mux, db, mcp)
	llm = newLLMBroker(db, workerURL, proxyURL)
	registerLLMRoutes(mux, llm)
	if err := loadLimits(context.Background(), db); err != nil {
		log.Fatalf("read the limits: %v", err)
	}
	go reloadLimits(context.Background(), db)
	registerLimitRoutes(mux, db)
	go writeAudit(db)
	registerAuditRoutes(mux, db)
	registerSpendingRoutes(mux, db)
	registerActivityRoutes(mux, db)
	egress = newEgressBroker(db, proxyURL)
	registerEgressRoutes(mux, db)
	go egress.watchTunnels(context.Background())
	go clearExpiredGrants(context.Background(), db)
	registerSkillRoutes(mux, db)
	registerConnectorRoutes(mux, db)
	eng := newEngine(db, workerURL, mcp, parallelSteps)
	mail, err := newMailer()
	if err != nil {
		log.Fatalf("%v", err)
	}
	eng.notify = newNotifier(db, auth != nil, mail, eng)
	go eng.notify.watch(context.Background(), noticeInterval)
	registerInboxRoutes(mux, db, eng.notify, auth)
	llm.overspent = eng.fail
	registerRunRoutes(mux, db, eng)
	registerRecordRunRoutes(mux, db, eng)
	registerLoopRoutes(mux, db, eng)
	registerLogRoutes(mux, db)
	registerRerunRoutes(mux, db, eng)
	registerTrialRoutes(mux, db, eng)
	registerAPIRoutes(mux, db, eng, auth != nil)
	registerDataRoutes(mux, db)
	registerScheduleRoutes(mux, db)
	go runSchedules(context.Background(), db, eng, time.Second)
	registerMessageRoutes(mux, db)
	registerChangeRoutes(mux, db, workerURL)
	registerScriptOutputRoutes(mux, db)
	registerFollowUpRoutes(mux, db, workerURL, mcp)
	go releaseEnded(db, workerURL)
	registerVersionRoutes(mux, db)
	registerPublishRoutes(mux, db)
	registerDiffRoutes(mux, db)
	registerHistoryRoutes(mux, db)
	registerOPLRoutes(mux, db)
	registerSetupRoutes(mux, db)
	registerAssistantRoutes(mux, db, workerURL)
	registerAgentRoutes(mux, db, workerURL)
	registerFileRoutes(mux)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		var version string
		if err := db.QueryRowContext(r.Context(), "SELECT VERSION()").Scan(&version); err != nil {
			http.Error(w, "db error: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, "ok, mysql %s\n", version)
	})

	// Once every part of the webapp says what it does with them.
	go takeCommands(context.Background(), db)

	addr := listenAddr(auth != nil)
	log.Printf("started; listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, egress.wrap(auth.protect(guardFlows(db, mux)))))
}

// Logs which database the webapp works on, never with its password.
func logDatabase(db *sql.DB) {
	var version string
	if err := db.QueryRow("SELECT VERSION()").Scan(&version); err != nil {
		log.Printf("WARNING: couldn't read the database's version: %v", err)
	}
	s, err := loadDBSettings()
	if err != nil {
		log.Printf("connected to MySQL %s", version)
		return
	}
	log.Printf("connected to the database %s at %s as %s, MySQL %s", s.Database, net.JoinHostPort(s.Host, s.Port), s.User, version)
}

// Logs whether the worker answers, and the models it offers, or a warning if
// it doesn't: agents and scripts fail until it does. The webapp starts all
// the same, as the worker may start after it.
func checkWorker(workerURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var models struct {
		Default string   `json:"default"`
		Models  []string `json:"models"`
	}
	if err := callWorker(ctx, http.MethodGet, workerURL+"/models", nil, &models); err != nil {
		log.Printf("WARNING: the worker at %s doesn't answer, so agent and script steps fail until it does: %v", workerURL, err)
		return
	}
	def := models.Default
	if def == "" {
		def = "none"
	}
	log.Printf("the worker answers, offering %d models, by default %s", len(models.Models), def)
}
