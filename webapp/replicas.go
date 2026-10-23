package main

// Replicas: the webapps running on one database, such as a Kubernetes
// Deployment's pods behind one Service. Each says it is alive in replicas
// every few seconds, and owns what it runs: the runs it walks, the
// assistant's replies and the follow-ups it waits on, and the tokens it
// granted their steps, which each records with its id. A request about one
// of them can reach any replica; whatever only its owner can do, such as
// stopping a step, the others ask of it (commands.go).
//
// A replica that hasn't said it is alive for REPLICA_TIMEOUT (30s by
// default), or that left, as one does when it is told to stop (SIGTERM),
// has stopped, and with it whatever it ran: whichever replica notices
// first, or the next one to start, ends what it left, as a server's restart
// used to end everything left running. Staleness is judged by the
// database's clock, so replicas' clocks needn't agree. A replica that finds
// another took it for stopped, as after a long pause, exits at once rather
// than go on with what was ended, and is started again.
//
// Rows from before replicas had ids have none, and are taken as left by a
// replica that stopped.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// This replica's id, set as it starts.
var replicaID string

// Whether this replica is leaving, so its row going isn't taken for
// another replica having taken it for stopped.
var leaving atomic.Bool

// How long a replica may go without saying it is alive before it is taken
// for stopped.
func replicaTimeout() (time.Duration, error) {
	v := env("REPLICA_TIMEOUT", "30s")
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Second {
		return 0, fmt.Errorf("REPLICA_TIMEOUT must be a duration of a second or more, such as 30s; it is %q", v)
	}
	return d, nil
}

// Records this replica as alive, and ends what replicas that stopped left,
// before it starts anything.
func joinReplicas(db *sql.DB, timeout time.Duration) error {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	replicaID = hex.EncodeToString(b)
	host, _ := os.Hostname()
	if _, err := db.Exec("INSERT INTO replicas (id, host) VALUES (?, ?)", replicaID, truncate(host, 255)); err != nil {
		return err
	}
	var others int
	if err := db.QueryRow("SELECT COUNT(*) FROM replicas WHERE id <> ? AND seen_at > CURRENT_TIMESTAMP(3) - INTERVAL ? MICROSECOND",
		replicaID, timeout.Microseconds()).Scan(&others); err != nil {
		return err
	}
	log.Printf("joined as replica %s on %s, beside %d others alive", replicaID, host, others)
	return endStopped(context.Background(), db, timeout)
}

// Says this replica is alive, and ends what replicas that stopped left,
// every sixth of the timeout, until ctx ends.
func heartbeat(ctx context.Context, db *sql.DB, timeout time.Duration) {
	t := time.NewTicker(timeout / 6)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var alive int
		_, err := db.ExecContext(ctx, "UPDATE replicas SET seen_at = CURRENT_TIMESTAMP(3) WHERE id = ?", replicaID)
		if err == nil {
			err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM replicas WHERE id = ?", replicaID).Scan(&alive)
		}
		if err != nil {
			log.Printf("WARNING: couldn't say this replica is alive: %v", err)
			continue
		}
		if alive == 0 && !leaving.Load() {
			log.Fatalf("another replica took this one for stopped, as it didn't say it was alive for %s, and ended what it ran; exiting", timeout)
		}
		if err := endStopped(ctx, db, timeout); err != nil {
			log.Printf("WARNING: couldn't end what stopped replicas left: %v", err)
		}
	}
}

// Leaves the replicas as this one stops, so another, or the next to start,
// ends what it ran at once.
func leaveReplicas(db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "DELETE FROM replicas WHERE id = ?", replicaID); err != nil {
		log.Printf("WARNING: couldn't leave the replicas: %v", err)
	}
}

// Where a row is owned by no replica alive, the column holding its owner
// being col.
func orphaned(col string) string {
	return fmt.Sprintf("(%[1]s IS NULL OR %[1]s NOT IN (SELECT id FROM replicas))", col)
}

// Runs execute in a replica; whatever one ran when it stopped can't
// continue, so it ends. The replicas that stopped go first; then their open
// steps, while their runs still read as running; then the runs, whose
// stored values are released; then the assistant's replies and step
// follow-ups under way, and the tokens their steps were granted. Each run
// ended so says so in its log, and what was ended is logged.
func endStopped(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	res, err := db.ExecContext(ctx, "DELETE FROM replicas WHERE seen_at <= CURRENT_TIMESTAMP(3) - INTERVAL ? MICROSECOND", timeout.Microseconds())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("%d replicas didn't say they were alive for %s, so they stopped", n, timeout)
	}
	var runs []int64
	err = scanAll(ctx, db, "SELECT id FROM runs WHERE status = 'running' AND "+orphaned("replica_id"), func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		runs = append(runs, id)
		return nil
	})
	if err != nil {
		return err
	}
	if len(runs) > 0 {
		ids := strings.TrimSuffix(strings.Repeat("?,", len(runs)), ",")
		args := make([]any, len(runs))
		for i, id := range runs {
			args[i] = id
		}
		for _, s := range []struct{ query, what string }{
			{`UPDATE run_steps SET status = 'error', error = 'The run ended before this step finished.', finished_at = CURRENT_TIMESTAMP(3)
			WHERE run_id IN (` + ids + `) AND status IN ('active', 'waiting')`, "steps"},
			{"UPDATE runs SET status = 'interrupted', finished_at = CURRENT_TIMESTAMP(3) WHERE status = 'running' AND id IN (" + ids + ")", "runs"},
			{"DELETE FROM object_instances WHERE run_id IN (" + ids + ")", ""},
		} {
			res, err := db.ExecContext(ctx, s.query, args...)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err == nil && n > 0 && s.what != "" {
				log.Printf("ended %d %s a replica that stopped left going", n, s.what)
			}
		}
		for _, id := range runs {
			runLog(db, id, 0, logError, "interrupted: the server stopped while it ran")
		}
	}
	for _, s := range []struct{ query, what string }{
		{`UPDATE assistant_messages SET status = 'error', error = 'The server stopped before this finished.', finished_at = CURRENT_TIMESTAMP(3)
		WHERE status = 'active' AND ` + orphaned("replica_id"), "assistant's replies"},
		{`UPDATE run_step_followups SET status = 'error', error = 'The server stopped before this finished.', finished_at = CURRENT_TIMESTAMP(3)
		WHERE status = 'active' AND ` + orphaned("replica_id"), "follow-ups"},
	} {
		res, err := db.ExecContext(ctx, s.query)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n > 0 {
			log.Printf("ended %d %s a replica that stopped left going", n, s.what)
		}
	}
	return clearGrants(ctx, db, orphaned("replica_id"), "ended, as the server granting it stopped")
}

// Leaves the replicas and exits once told to stop, as Kubernetes does a
// pod it replaces; what this replica ran is ended by another, or the next
// to start, at once.
func leaveOnStop(db *sql.DB) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	s := <-stop
	log.Printf("stopping on %s", s)
	leaving.Store(true)
	leaveReplicas(db)
	os.Exit(0)
}
