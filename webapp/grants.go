package main

// Proxy grants: the tokens steps are handed for the webapp's proxies, to
// their models (llm.go), their MCP servers (mcp.go), the internet
// (egress.go) and the services of their credentials (credentials.go). They are kept in the database, so that whichever replica of
// the webapp a step's request reaches knows its token, wherever it was
// granted. Only a token's SHA-256 is kept, never the token, and never a
// key: what a grant reaches is worked out again by the replica it reaches,
// from what the grant names, such as its providers or its MCP servers' ids.
//
// A grant ends when it is revoked, as its step ends, or once it expires,
// whichever replica it is used at. Whoever deletes its row audits its end,
// so it is audited once: the replica that granted it, as its step ends or
// its timer goes, or any replica clearing out grants that expired, such as
// those of a replica that stopped. A broker without a database, as unit
// tests make, keeps its grants in its memory instead.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

// The proxies a grant is for, as proxy_grants.kind and the audit log name
// them.
const (
	grantModel      = "model"
	grantMCP        = "mcp"
	grantInternet   = "internet"
	grantCredential = "credential"
)

// Who and what a grant is for; what of its proxy it reaches is the proxy's
// own, kept as JSON beside it.
type proxyGrant struct {
	user      *int64
	run, step int64
	// The run whose spending a model grant's counts toward, 0 for none.
	root int64
}

// Where a broker keeps its grants.
type grantStore interface {
	// Keeps the grant of token for ttl, with what it reaches.
	put(ctx context.Context, kind, token string, g proxyGrant, reaches any, ttl time.Duration) error
	// The grant of token, with what it reaches read into reaches; false
	// if there is none, as it ended or never was.
	lookup(ctx context.Context, kind, token string, reaches any) (proxyGrant, bool, error)
	// Ends the grant of token, and returns who it was for; false if it had
	// ended already, so its end is audited once.
	remove(kind, token string) (proxyGrant, bool)
	// Ends the model grants whose spending counts toward root, and returns
	// them; none if another replica ended them first.
	removeRoot(root int64) ([]proxyGrant, error)
}

func grantsIn(db *sql.DB) grantStore {
	if db == nil {
		return &memoryGrants{grants: map[string]memoryGrant{}}
	}
	return dbGrants{db}
}

type dbGrants struct{ db *sql.DB }

func (s dbGrants) put(ctx context.Context, kind, token string, g proxyGrant, reaches any, ttl time.Duration) error {
	data, err := json.Marshal(reaches)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO proxy_grants (token_hash, kind, user_id, run_id, step_id, root_run_id, reaches, replica_id, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP(3) + INTERVAL ? MICROSECOND)`,
		hashToken(token), kind, g.user, nullID(g.run), nullID(g.step), nullID(g.root), data, replicaID, ttl.Microseconds())
	return err
}

func (s dbGrants) lookup(ctx context.Context, kind, token string, reaches any) (proxyGrant, bool, error) {
	if token == "" {
		return proxyGrant{}, false, nil
	}
	g, data, ok, err := s.row(ctx, kind, token, "AND expires_at > CURRENT_TIMESTAMP(3)")
	if err != nil || !ok {
		return proxyGrant{}, false, err
	}
	if reaches != nil {
		if err := json.Unmarshal(data, reaches); err != nil {
			return proxyGrant{}, false, err
		}
	}
	return g, true, nil
}

// The grant of token, expired or not, where and says.
func (s dbGrants) row(ctx context.Context, kind, token, and string) (proxyGrant, []byte, bool, error) {
	var g proxyGrant
	var run, step, root sql.NullInt64
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT user_id, run_id, step_id, root_run_id, reaches FROM proxy_grants WHERE token_hash = ? AND kind = ? "+and,
		hashToken(token), kind).Scan(&g.user, &run, &step, &root, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return proxyGrant{}, nil, false, nil
	}
	if err != nil {
		return proxyGrant{}, nil, false, err
	}
	g.run, g.step, g.root = run.Int64, step.Int64, root.Int64
	return g, data, true, nil
}

func (s dbGrants) remove(kind, token string) (proxyGrant, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g, _, ok, err := s.row(ctx, kind, token, "")
	if err == nil && ok {
		var res sql.Result
		if res, err = s.db.ExecContext(ctx, "DELETE FROM proxy_grants WHERE token_hash = ? AND kind = ?", hashToken(token), kind); err == nil {
			n, _ := res.RowsAffected()
			ok = n > 0
		}
	}
	if err != nil {
		log.Printf("end a token of the %s proxy: %v", kind, err)
		return proxyGrant{}, false
	}
	return g, ok
}

func (s dbGrants) removeRoot(root int64) ([]proxyGrant, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var ended []proxyGrant
	err = scanAll(ctx, tx, "SELECT user_id, run_id, step_id FROM proxy_grants WHERE kind = ? AND root_run_id = ? FOR UPDATE", func(rows *sql.Rows) error {
		g := proxyGrant{root: root}
		var run, step sql.NullInt64
		if err := rows.Scan(&g.user, &run, &step); err != nil {
			return err
		}
		g.run, g.step = run.Int64, step.Int64
		ended = append(ended, g)
		return nil
	}, grantModel, root)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_grants WHERE kind = ? AND root_run_id = ?", grantModel, root); err != nil {
		return nil, err
	}
	return ended, tx.Commit()
}

// A grant kept in memory, with what it reaches as JSON, as a row keeps it.
type memoryGrant struct {
	kind    string
	g       proxyGrant
	reaches []byte
	expires time.Time
}

type memoryGrants struct {
	mu     sync.Mutex
	grants map[string]memoryGrant
}

func (s *memoryGrants) put(_ context.Context, kind, token string, g proxyGrant, reaches any, ttl time.Duration) error {
	data, err := json.Marshal(reaches)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[hashToken(token)] = memoryGrant{kind: kind, g: g, reaches: data, expires: time.Now().Add(ttl)}
	return nil
}

func (s *memoryGrants) lookup(_ context.Context, kind, token string, reaches any) (proxyGrant, bool, error) {
	s.mu.Lock()
	m, ok := s.grants[hashToken(token)]
	s.mu.Unlock()
	if !ok || m.kind != kind || !time.Now().Before(m.expires) {
		return proxyGrant{}, false, nil
	}
	if reaches != nil {
		if err := json.Unmarshal(m.reaches, reaches); err != nil {
			return proxyGrant{}, false, err
		}
	}
	return m.g, true, nil
}

func (s *memoryGrants) remove(kind, token string) (proxyGrant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.grants[hashToken(token)]
	if !ok || m.kind != kind {
		return proxyGrant{}, false
	}
	delete(s.grants, hashToken(token))
	return m.g, true
}

func (s *memoryGrants) removeRoot(root int64) ([]proxyGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ended []proxyGrant
	for hash, m := range s.grants {
		if m.kind == grantModel && m.g.root == root {
			ended = append(ended, m.g)
			delete(s.grants, hash)
		}
	}
	return ended, nil
}

// Clears out the grants that expired, auditing their end, every minute.
func clearExpiredGrants(ctx context.Context, db *sql.DB) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := clearGrants(ctx, db, "expires_at <= CURRENT_TIMESTAMP(3)", "expired"); err != nil {
			log.Printf("clear out the proxies' tokens that expired: %v", err)
		}
	}
}

// Ends the grants where says, auditing each as ended why.
func clearGrants(ctx context.Context, db *sql.DB, where, why string, args ...any) error {
	type ended struct {
		hash, kind string
		g          proxyGrant
	}
	var found []ended
	err := scanAll(ctx, db, "SELECT token_hash, kind, user_id, run_id, step_id FROM proxy_grants WHERE "+where, func(rows *sql.Rows) error {
		var e ended
		var run, step sql.NullInt64
		if err := rows.Scan(&e.hash, &e.kind, &e.g.user, &run, &step); err != nil {
			return err
		}
		e.g.run, e.g.step = run.Int64, step.Int64
		found = append(found, e)
		return nil
	}, args...)
	if err != nil {
		return err
	}
	for _, e := range found {
		res, err := db.ExecContext(ctx, "DELETE FROM proxy_grants WHERE token_hash = ? AND kind = ?", e.hash, e.kind)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			auditGrantEnd(e.kind, e.g, why)
		}
	}
	return nil
}

// Records the end of a grant in the audit log, as its proxy words it.
func auditGrantEnd(kind string, g proxyGrant, why string) {
	switch kind {
	case grantModel:
		audit("model.revoke", g.user, g.run, g.step, "the model proxy's token %s", why)
	case grantMCP:
		audit("mcp.revoke", g.user, g.run, g.step, "the MCP proxy's token %s", why)
	case grantInternet:
		audit("internet.revoke", g.user, g.run, g.step, "the egress proxy's token %s", why)
	case grantCredential:
		audit("credential.revoke", g.user, g.run, g.step, "the credential proxy's token %s", why)
	}
}
