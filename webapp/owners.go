package main

// Each flow, procedure and struct has an owner: someone, a team, or nobody,
// when it is global. A new one is its maker's. Only its owner sees it, or
// for a team's, the team's members, and anyone sees a global one; they may
// change it and run it. Admins see everything, so nothing can end up out of
// everyone's reach. Someone holding a role a human object on a flow names,
// who decides its reviews (see user_roles.go), sees the flow and its runs
// too, to decide them, but changes nothing in it.
//
// Who owns it changes hands this way: someone gives what is theirs to one
// of their teams or makes it global; a team's member gives the team's to
// themselves, another team of theirs, or makes it global; only an admin
// takes a global one back, or gives anything to anyone. The flows that run a
// procedure, or use a struct, run it even when it is no longer theirs to
// see, so taking one out of view breaks nothing.
//
// Without sign-in there is nobody to own anything, and everything is global.
// Who owns what belongs to people, so it stays out of versions and the
// setup file, and a flow the setup file brings in is global.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Who owns a flow.
type flowOwner struct {
	// "global", "team" or "user".
	Kind string `json:"kind"`
	// The team's or the user's id; none for global.
	ID int64 `json:"id,omitempty"`
	// The team's name, or the user's name or email.
	Name string `json:"name,omitempty"`
}

const (
	ownerGlobal = "global"
	ownerTeam   = "team"
	ownerUser   = "user"
)

// The columns of a flow's owner in flows f, for scanOwner.
const ownerColumns = "f.owner_user_id, f.owner_team_id, COALESCE((SELECT COALESCE(NULLIF(u.name, ''), u.email) FROM users u WHERE u.id = f.owner_user_id), (SELECT t.name FROM teams t WHERE t.id = f.owner_team_id), '')"

func scanOwner(user, team sql.NullInt64, name string) flowOwner {
	switch {
	case user.Valid:
		return flowOwner{Kind: ownerUser, ID: user.Int64, Name: name}
	case team.Valid:
		return flowOwner{Kind: ownerTeam, ID: team.Int64, Name: name}
	}
	return flowOwner{Kind: ownerGlobal}
}

func migrateOwners(db *sql.DB) error {
	for _, c := range []struct{ table, name, def string }{
		{"flows", "owner_user_id", "BIGINT NULL"},
		{"flows", "owner_team_id", "BIGINT NULL"},
		// Whose schedule it is: its runs use their model keys and MCP
		// connections; see runUser.
		{"schedules", "created_by", "BIGINT NULL"},
	} {
		if err := addColumn(db, c.table, c.name, c.def); err != nil {
			return err
		}
	}
	// A team or someone that owns flows can't go without them going elsewhere
	// first; see teams.go and user_roles.go.
	for _, c := range []struct{ name, def string }{
		{"flows_owner_user", "FOREIGN KEY (owner_user_id) REFERENCES users(id)"},
		{"flows_owner_team", "FOREIGN KEY (owner_team_id) REFERENCES teams(id)"},
	} {
		ok, err := hasConstraint(db, "flows", c.name)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := db.Exec("ALTER TABLE flows ADD CONSTRAINT " + c.name + " " + c.def); err != nil {
				return err
			}
		}
	}
	return nil
}

// How far someone may go with a flow.
const (
	// Change it and run it.
	accessEdit = "edit"
	// See it and its runs, and decide the reviews their roles let them.
	accessReview = "review"
)

// The condition on flows f that only the ones the user may change meet, and
// its arguments; none for nobody signed in or an admin.
func editableFlows(u *user) (string, []any) {
	if u == nil || u.Admin {
		return "TRUE", nil
	}
	return `((f.owner_user_id IS NULL AND f.owner_team_id IS NULL) OR f.owner_user_id = ?
		OR f.owner_team_id IN (SELECT team_id FROM team_members WHERE user_id = ?))`, []any{u.ID, u.ID}
}

// The condition on flows f that only the ones the user sees meet: the ones
// they may change, and the ones with reviews their roles decide.
func visibleFlows(u *user) (string, []any) {
	cond, args := editableFlows(u)
	if u == nil || u.Admin {
		return cond, args
	}
	return "(" + cond + ` OR EXISTS (SELECT 1 FROM objects o JOIN user_roles ur ON LOWER(TRIM(o.name)) = LOWER(ur.name)
		WHERE o.flow_id = f.id AND o.human AND ur.user_id = ?))`, append(args, u.ID)
}

// How far the user may go with the flow: accessEdit, accessReview, or ""
// for not at all. A flow that doesn't exist is everyone's to be told so.
func flowAccess(ctx context.Context, db querier, u *user, flow int64) (string, error) {
	if u == nil || u.Admin {
		return accessEdit, nil
	}
	edit, editArgs := editableFlows(u)
	see, seeArgs := visibleFlows(u)
	var exists, editable, visible bool
	err := db.QueryRowContext(ctx, "SELECT TRUE, "+edit+", "+see+" FROM flows f WHERE f.id = ?",
		append(append(editArgs, seeArgs...), flow)...).Scan(&exists, &editable, &visible)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accessEdit, nil
	case err != nil:
		return "", err
	case editable:
		return accessEdit, nil
	case visible:
		return accessReview, nil
	}
	return "", nil
}

// Deciding a review, which its route lets only those holding its role do.
var decidePattern = regexp.MustCompile(`^/api/runs/\d+/steps/\d+$`)

// The access a request needs to the flows it is about.
func neededAccess(r *http.Request) string {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || (r.Method == http.MethodPatch && decidePattern.MatchString(r.URL.Path)) {
		return accessReview
	}
	return accessEdit
}

// The owner a new flow gets: its maker, when there is one.
func newOwner(r *http.Request) *int64 {
	return requestUserID(r)
}

// Why the user may not give the flow from its owner now to the one given, if
// they may not.
func refuseOwner(ctx context.Context, db querier, u *user, now, to flowOwner) (string, error) {
	switch to.Kind {
	case ownerGlobal, ownerUser, ownerTeam:
	default:
		return "an owner is global, a team or a user", nil
	}
	if u == nil {
		return "nobody signs in, so everything is global", nil
	}
	if u.Admin {
		return "", nil
	}
	// Giving it away from someone or a team they aren't is only an admin's.
	switch now.Kind {
	case ownerGlobal:
		if to.Kind != ownerGlobal {
			return "only an admin takes a global flow back from everyone", nil
		}
	case ownerUser:
		if now.ID != u.ID {
			return "only its owner or an admin gives it to someone else", nil
		}
	}
	switch to.Kind {
	case ownerUser:
		if to.ID != u.ID {
			return "only an admin gives a flow to someone else", nil
		}
	case ownerTeam:
		in, err := inTeam(ctx, db, u.ID, to.ID)
		if err != nil || !in {
			return "you can only give it to a team you are in", err
		}
	}
	return "", nil
}

func registerOwnerRoutes(mux *http.ServeMux, db *sql.DB) {
	// Gives the flow to someone, a team, or everyone.
	mux.HandleFunc("PUT /api/flows/{id}/owner", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var to flowOwner
		if err := json.NewDecoder(r.Body).Decode(&to); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		f, err := getFlow(r.Context(), db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		msg, err := refuseOwner(r.Context(), db, requestUser(r), f.Owner, to)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		var userID, teamID *int64
		switch to.Kind {
		case ownerUser:
			userID = &to.ID
		case ownerTeam:
			teamID = &to.ID
		}
		_, err = db.ExecContext(r.Context(), "UPDATE flows SET owner_user_id = ?, owner_team_id = ? WHERE id = ?", userID, teamID, id)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "no such "+to.Kind, http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		f, err = getFlow(r.Context(), db, id)
		if err == nil {
			f.Access, err = flowAccess(r.Context(), db, requestUser(r), id)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, f)
	})
}

// Paths whose id is of a row in a flow, and the query giving that flow.
var flowPaths = []struct {
	pattern *regexp.Regexp
	query   string
}{
	{regexp.MustCompile(`^/api/flows/(\d+)(/|$)`), "SELECT id FROM flows WHERE id = ?"},
	{regexp.MustCompile(`^/api/structs/(\d+)(/|$)`), "SELECT id FROM flows WHERE id = ?"},
	{regexp.MustCompile(`^/api/processes/(\d+)(/|$)`), "SELECT flow_id FROM processes WHERE id = ?"},
	{regexp.MustCompile(`^/api/objects/(\d+)(/|$)`), "SELECT flow_id FROM objects WHERE id = ?"},
	{regexp.MustCompile(`^/api/links/(\d+)(/|$)`), "SELECT p.flow_id FROM object_links l JOIN processes p ON p.id = l.process_id WHERE l.id = ?"},
	{regexp.MustCompile(`^/api/connections/(\d+)(/|$)`), "SELECT p.flow_id FROM connections c JOIN processes p ON p.id = c.source_id WHERE c.id = ?"},
	{regexp.MustCompile(`^/api/states/(\d+)(/|$)`), "SELECT o.flow_id FROM object_states s JOIN objects o ON o.id = s.object_id WHERE s.id = ?"},
	{regexp.MustCompile(`^/api/attributes/(\d+)(/|$)`), "SELECT o.flow_id FROM object_attributes a JOIN objects o ON o.id = a.object_id WHERE a.id = ?"},
	{regexp.MustCompile(`^/api/runs/(\d+)(/|$)`), "SELECT flow_id FROM runs WHERE id = ?"},
	{regexp.MustCompile(`^/api/schedules/(\d+)(/|$)`), "SELECT flow_id FROM schedules WHERE id = ?"},
	{regexp.MustCompile(`^/api/versions/(\d+)(/|$)`), "SELECT flow_id FROM flow_versions WHERE id = ?"},
}

// The query giving the flow of a row an id in a request names, by the
// table the id is of.
var flowOfTable = map[string]string{
	"flows":     "SELECT id FROM flows WHERE id = ?",
	"processes": "SELECT flow_id FROM processes WHERE id = ?",
	"objects":   "SELECT flow_id FROM objects WHERE id = ?",
	"runs":      "SELECT flow_id FROM runs WHERE id = ?",
}

// The tables of the ids a request's query or JSON body names by field.
// Bodies with a source and a target name processes only in connections and
// starts, and a flow in settings is the one a subflow process runs.
var flowFields = map[string]string{
	"flow": "flows", "from": "flows",
	"parent": "processes", "board": "processes", "process": "processes", "processes": "processes",
	"object": "objects", "objects": "objects",
}

// The files of a run's steps; see files.go.
var runFilesPattern = regexp.MustCompile(`^runs/(\d+)(/|$)`)

// How large a body the guard reads to find the flows it names; the routes
// with larger bodies, setup files, name none.
const maxGuardedBody = 1 << 20

// Turns away a request about a flow, or anything in one, that its user
// doesn't see, as if it didn't exist; see visibleFlows. It finds the flows
// a request is about from its path, its query, and the ids its JSON body
// names, so each route needn't; the routes listing flows, or what is in
// several, filter them themselves.
func guardFlows(db *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := requestUser(r)
		if u == nil || u.Admin || !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/setup" {
			next.ServeHTTP(w, r)
			return
		}
		type ref struct {
			query string
			id    int64
		}
		var refs []ref
		for _, p := range flowPaths {
			if m := p.pattern.FindStringSubmatch(r.URL.Path); m != nil {
				id, _ := strconv.ParseInt(m[1], 10, 64)
				refs = append(refs, ref{p.query, id})
			}
		}
		q := r.URL.Query()
		for field, table := range flowFields {
			for _, v := range q[field] {
				if id, err := strconv.ParseInt(v, 10, 64); err == nil {
					refs = append(refs, ref{flowOfTable[table], id})
				}
			}
		}
		if r.URL.Path == "/api/files" {
			if rel, err := filepath.Rel(filesRoot, filepath.Clean(q.Get("path"))); err == nil {
				if m := runFilesPattern.FindStringSubmatch(filepath.ToSlash(rel)); m != nil {
					id, _ := strconv.ParseInt(m[1], 10, 64)
					refs = append(refs, ref{flowOfTable["runs"], id})
				}
			}
		}
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxGuardedBody+1))
			r.Body.Close()
			if err != nil {
				http.Error(w, "couldn't read the request", http.StatusBadRequest)
				return
			}
			if len(body) > maxGuardedBody {
				http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			for table, id := range bodyRefs(r.URL.Path, body) {
				for _, i := range id {
					refs = append(refs, ref{flowOfTable[table], i})
				}
			}
		}
		need := neededAccess(r)
		for _, rf := range refs {
			var flow int64
			err := db.QueryRowContext(r.Context(), rf.query, rf.id).Scan(&flow)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				serverError(w, err)
				return
			}
			access, err := flowAccess(r.Context(), db, u, flow)
			if err != nil {
				serverError(w, err)
				return
			}
			switch {
			case access == "":
				http.Error(w, "flow not found", http.StatusNotFound)
				return
			case access == accessReview && need == accessEdit:
				http.Error(w, "you only decide this flow's reviews; ask its owner to change it", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// The ids a JSON body names, by table; nothing for a body that isn't a
// JSON object, which its route turns down.
func bodyRefs(path string, body []byte) map[string][]int64 {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	refs := map[string][]int64{}
	add := func(table string, raw json.RawMessage) {
		var one int64
		if json.Unmarshal(raw, &one) == nil {
			refs[table] = append(refs[table], one)
			return
		}
		var many []int64
		if json.Unmarshal(raw, &many) == nil {
			refs[table] = append(refs[table], many...)
		}
	}
	for field, table := range flowFields {
		if raw, ok := fields[field]; ok {
			add(table, raw)
		}
	}
	switch path {
	case "/api/connections", "/api/start", "/api/end":
		for _, field := range []string{"source", "target"} {
			if raw, ok := fields[field]; ok {
				add("processes", raw)
			}
		}
	}
	if raw, ok := fields["settings"]; ok {
		var settings map[string]json.RawMessage
		if json.Unmarshal(raw, &settings) == nil {
			if f, ok := settings["flow"]; ok {
				add("flows", f)
			}
		}
	}
	return refs
}
