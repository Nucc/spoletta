package main

// The audit log: what the webapp handed steps and what they did with it,
// and what owners changed, an event each in audit_events, for whom and
// which run and step. Steps' tokens granted for the model proxy (llm.go),
// MCP servers (mcp.go), the internet (egress.go), credentials
// (credentials.go) and git (git_tokens.go), and when the model proxy's,
// the internet's and credentials' end, as each lets a step do something
// until then; each internet connection, allowed or refused, and each call
// through the credential proxy; calls the proxies refused, and runs
// stopped for spending what they may (spend.go); and the limits, internet
// hosts, model keys, credentials and git token owners set.
// What each model call took and cost is in llm_usage, not here. Events
// never hold a token, a key or a secret, only what they reach.
//
// Each event is a project's: the one its step's grant names, its run's, or
// the one a request changing a setting is worked on in. A project's owners
// search its events alone. What is no project's, such as a call with a
// token no grant has, or a ceiling the instance's admin sets, goes in
// instance_events with what happens above the projects (projects.go),
// which super admins read.
//
// Events are written in the background, so writing one never holds up or
// fails what it records; should the database fall behind by more than
// auditBacklog events, the newest are dropped and the server's log says
// so. Owners search them on the Audit log page (Audit.tsx) as they search
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
	// The project whose log it goes in: aboveProjects for the instance's
	// events, and 0 for its run's, or Legacy's for none, as events not yet
	// naming theirs are while projects come in (see projects.go).
	project int64
}

// What an event's project is when it is the instance's, in instance_events.
const aboveProjects int64 = -1

const auditBacklog = 4096

var auditEvents = make(chan auditEvent, auditBacklog)

// Records an event, for whom, run and step (0 for none), in the
// background, in the log of the run's project, or Legacy's without one.
func audit(kind string, who *int64, run, step int64, format string, args ...any) {
	auditIn(0, kind, who, run, step, format, args...)
}

// Records an event in the project's log, for whom, run and step (0 for
// none), in the background.
func auditIn(project int64, kind string, who *int64, run, step int64, format string, args ...any) {
	recordAudit(auditEvent{at: time.Now(), user: who, run: run, step: step, kind: kind, detail: fmt.Sprintf(format, args...), project: project})
}

// Records an event in the project's log, for whom, in the background.
func auditProject(project int64, kind string, who *int64, format string, args ...any) {
	auditIn(project, kind, who, 0, 0, format, args...)
}

// Records an event that is no project's in instance_events, for whom, in
// the background.
func auditInstance(kind string, who *int64, format string, args ...any) {
	auditIn(aboveProjects, kind, who, 0, 0, format, args...)
}

// Hands the event to writeAudit, or drops it if the log is too far behind.
func recordAudit(e auditEvent) {
	select {
	case auditEvents <- e:
	default:
		log.Printf("WARNING: the audit log is behind, so this event went unrecorded: %s %s", e.kind, e.detail)
	}
}

// Records the MCP servers a step was given a token for, if any, in its
// run's project's log.
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

// Records the request's user's change, in the log of the project the
// request is worked on in.
func auditChange(r *http.Request, kind, format string, args ...any) {
	auditIn(requestProject(r), kind, requestUserID(r), 0, 0, format, args...)
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
		kind, detail := truncate(e.kind, 64), truncate(e.detail, 4000)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var err error
		switch e.project {
		case aboveProjects:
			_, err = db.ExecContext(ctx, "INSERT INTO instance_events (at, user_id, kind, detail) VALUES (?, ?, ?, ?)",
				e.at.UTC(), e.user, kind, detail)
		case 0:
			// The run's project, or Legacy's for an event of no run.
			_, err = db.ExecContext(ctx, `INSERT INTO audit_events (at, user_id, run_id, step_id, kind, detail, project_id)
				VALUES (?, ?, ?, ?, ?, ?, COALESCE((SELECT r.project_id FROM runs r WHERE r.id = ?), ?))`,
				e.at.UTC(), e.user, run, step, kind, detail, run, legacyProject)
		default:
			_, err = db.ExecContext(ctx, "INSERT INTO audit_events (at, user_id, run_id, step_id, kind, detail, project_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
				e.at.UTC(), e.user, run, step, kind, detail, e.project)
		}
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
	// The latest events of the project the request is worked on in that the
	// query q matches, sorted by sort; for its owners.
	mux.HandleFunc("GET /api/audit", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var events []auditRow
		err := scanAll(r.Context(), db, `SELECT e.id, e.at, COALESCE(NULLIF(u.name, ''), u.email, ''), e.run_id, rn.flow_id, e.step_id, e.kind, e.detail
			FROM audit_events e LEFT JOIN users u ON u.id = e.user_id LEFT JOIN runs rn ON rn.id = e.run_id
			WHERE e.project_id = ? ORDER BY e.id DESC LIMIT ?`, func(rows *sql.Rows) error {
			var e auditRow
			if err := rows.Scan(&e.ID, &e.At, &e.Who, &e.Run, &e.Flow, &e.Step, &e.Kind, &e.Detail); err != nil {
				return err
			}
			events = append(events, e)
			return nil
		}, requestProject(r), searchedRows+1)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, searchEvents(r, events, true))
	})

	// The latest of what happened above the projects (instance_events) that
	// the query q matches, sorted by sort, searched as the audit log is: a
	// project made, archived or deleted, a ceiling set, a call with a token
	// no grant has. For super admins.
	mux.HandleFunc("GET /api/instance/events", func(w http.ResponseWriter, r *http.Request) {
		if refuseNonSuper(w, r) {
			return
		}
		var events []auditRow
		err := scanAll(r.Context(), db, `SELECT e.id, e.at, COALESCE(NULLIF(u.name, ''), u.email, ''), e.kind, e.detail
			FROM instance_events e LEFT JOIN users u ON u.id = e.user_id ORDER BY e.id DESC LIMIT ?`, func(rows *sql.Rows) error {
			var e auditRow
			if err := rows.Scan(&e.ID, &e.At, &e.Who, &e.Kind, &e.Detail); err != nil {
				return err
			}
			events = append(events, e)
			return nil
		}, searchedRows+1)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, searchEvents(r, events, false))
	})
}

// The events of those read, the latest first, that the request's query q
// matches, sorted by its sort, with their facets: the runs and steps they
// were for too, for a project's.
func searchEvents(r *http.Request, events []auditRow, ofRuns bool) any {
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
	runFacet, stepFacet := -1, -1
	if ofRuns {
		runFacet = t.facet("run", facet{Key: "run", Label: "Run", Group: "Event"})
		stepFacet = t.facet("step", facet{Key: "step", Label: "Step", Group: "Event", Hidden: true})
	}
	detail := t.facet("detail", facet{Key: "detail", Label: "What", Group: "Event", Hidden: true})
	for _, e := range events {
		row := t.row()
		row[idFacet] = []string{strconv.FormatInt(e.ID, 10)}
		row[kind] = []string{e.Kind}
		row[who] = scalars(e.Who)
		row[at] = []string{e.At.UTC().Format(time.RFC3339)}
		if e.Run != nil && runFacet >= 0 {
			row[runFacet] = []string{strconv.FormatInt(*e.Run, 10)}
		}
		if e.Step != nil && stepFacet >= 0 {
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
	return out
}
