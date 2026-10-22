package main

// The audit log: what the webapp handed steps and what they did with it,
// and what admins changed, an event each in audit_events, for whom and
// which run and step. Steps' tokens granted for the model proxy (llm.go),
// MCP servers (mcp.go), the internet (egress.go) and git (git_tokens.go),
// and when the model proxy's and the internet's end, as each lets a step
// do something until then; each internet connection, allowed or refused;
// calls the model proxy refused, and runs stopped for spending what they
// may (spend.go); and the limits, internet hosts and model keys admins set.
// What each model call took and cost is in llm_usage, not here. Events
// never hold a token or a key, only what they reach.
//
// Events are written in the background, so writing one never holds up or
// fails what it records; should the database fall behind by more than
// auditBacklog events, the newest are dropped and the server's log says
// so. Admins search them on the Audit log page (Audit.tsx) as they search
// a flow's runs (search.go), by kind, who, run, step, when and what.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type auditEvent struct {
	at        time.Time
	user      *int64
	run, step int64
	kind      string
	detail    string
}

const auditBacklog = 4096

var auditEvents = make(chan auditEvent, auditBacklog)

// Records an event, for whom, run and step (0 for none), in the background.
func audit(kind string, who *int64, run, step int64, format string, args ...any) {
	e := auditEvent{at: time.Now(), user: who, run: run, step: step, kind: kind, detail: fmt.Sprintf(format, args...)}
	select {
	case auditEvents <- e:
	default:
		log.Printf("WARNING: the audit log is behind, so this event went unrecorded: %s %s", kind, e.detail)
	}
}

// Records the MCP servers a step was given a token for, if any.
func auditMCP(who *int64, run, step int64, servers []mcpRunServer) {
	if len(servers) == 0 {
		return
	}
	names := make([]string, len(servers))
	for i, s := range servers {
		names[i] = s.Name
	}
	audit("mcp.grant", who, run, step, "the MCP proxy, reaching %s", strings.Join(names, ", "))
}

// Records the request's user's change.
func auditChange(r *http.Request, kind, format string, args ...any) {
	audit(kind, requestUserID(r), 0, 0, format, args...)
}

// Writes events as they come, for as long as the webapp runs.
func writeAudit(db *sql.DB) {
	for e := range auditEvents {
		var run, step any
		if e.run != 0 {
			run = e.run
		}
		if e.step != 0 {
			step = e.step
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := db.ExecContext(ctx, "INSERT INTO audit_events (at, user_id, run_id, step_id, kind, detail) VALUES (?, ?, ?, ?, ?, ?)",
			e.at.UTC(), e.user, run, step, truncate(e.kind, 64), truncate(e.detail, 4000))
		cancel()
		if err != nil {
			log.Printf("record the audit event %s %s: %v", e.kind, e.detail, err)
		}
	}
}

// An event as the Audit log page shows it.
type auditRow struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Who    string    `json:"who"`
	Run    *int64    `json:"run"`
	Flow   *int64    `json:"flow"`
	Step   *int64    `json:"step"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
}

// As many events as the page shows.
const auditRows = 500

func registerAuditRoutes(mux *http.ServeMux, db *sql.DB) {
	// The latest events the query q matches, sorted by sort; for admins.
	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var events []auditRow
		err := scanAll(r.Context(), db, `SELECT e.id, e.at, COALESCE(NULLIF(u.name, ''), u.email, ''), e.run_id, rn.flow_id, e.step_id, e.kind, e.detail
			FROM audit_events e LEFT JOIN users u ON u.id = e.user_id LEFT JOIN runs rn ON rn.id = e.run_id
			ORDER BY e.id DESC LIMIT ?`, func(rows *sql.Rows) error {
			var e auditRow
			if err := rows.Scan(&e.ID, &e.At, &e.Who, &e.Run, &e.Flow, &e.Step, &e.Kind, &e.Detail); err != nil {
				return err
			}
			events = append(events, e)
			return nil
		}, searchedRows+1)
		if err != nil {
			serverError(w, err)
			return
		}
		out := struct {
			Events    []auditRow `json:"events"`
			Facets    []facet    `json:"facets"`
			Matched   int        `json:"matched"`
			Searched  int        `json:"searched"`
			Truncated bool       `json:"truncated"`
		}{Events: []auditRow{}}
		if len(events) > searchedRows {
			events, out.Truncated = events[:searchedRows], true
		}
		t := newSearchTable()
		idFacet := t.facet("id", facet{Key: "id", Label: "Event", Group: "Event", Hidden: true})
		kind := t.facet("kind", facet{Key: "kind", Label: "Kind", Group: "Event"})
		who := t.facet("who", facet{Key: "who", Label: "Who", Group: "Event"})
		at := t.facet("at", facet{Key: "at", Label: "When", Group: "Event", Date: true, Hidden: true})
		runFacet := t.facet("run", facet{Key: "run", Label: "Run", Group: "Event"})
		stepFacet := t.facet("step", facet{Key: "step", Label: "Step", Group: "Event", Hidden: true})
		detail := t.facet("detail", facet{Key: "detail", Label: "What", Group: "Event", Hidden: true})
		for _, e := range events {
			row := t.row()
			row[idFacet] = []string{strconv.FormatInt(e.ID, 10)}
			row[kind] = []string{e.Kind}
			row[who] = scalars(e.Who)
			row[at] = []string{e.At.UTC().Format(time.RFC3339)}
			if e.Run != nil {
				row[runFacet] = []string{strconv.FormatInt(*e.Run, 10)}
			}
			if e.Step != nil {
				row[stepFacet] = []string{strconv.FormatInt(*e.Step, 10)}
			}
			row[detail] = scalars(e.Detail)
		}
		s := t.search()
		q := parseSearch(r.URL.Query().Get("q"))
		key, desc := sortParam(r.URL.Query().Get("sort"))
		matched := s.run(q, key, desc)
		s.count(q, matched)
		out.Facets, out.Matched, out.Searched = t.list(), len(matched), len(events)
		for _, i := range matched[:min(len(matched), auditRows)] {
			out.Events = append(out.Events, events[i])
		}
		writeJSON(w, http.StatusOK, out)
	})
}
