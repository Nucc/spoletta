package main

// Each flow, procedure and struct has an owner: someone, a team, or nobody,
// when it is global. Most are a team's: a new one is its maker's team's,
// when they are in one only, unless they give it to another team of theirs,
// themselves or everyone as they make it. Only its owner sees it, or
// for a team's, the team's members, and anyone sees a global one; they may
// change it and run it. Admins see everything, so nothing can end up out of
// everyone's reach. Someone holding a role a human object on a flow names,
// who decides its reviews (see user_roles.go), sees the flow and its runs
// too, to decide them, but changes nothing in it. The object may be on the
// board as it is, on the version the flow published, or on the version of
// a run waiting for a person, so a draft dropping it doesn't shut its role
// holders out of the reviews of the runs that still have it.
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
var ownerColumns = ownerColumnsOf("f")

// The columns of the owner of a row of table t, which has owner_user_id
// and owner_team_id as flows do, for scanOwner.
func ownerColumnsOf(t string) string {
	return t + ".owner_user_id, " + t + ".owner_team_id, COALESCE((SELECT COALESCE(NULLIF(u.name, ''), u.email) FROM users u WHERE u.id = " +
		t + ".owner_user_id), (SELECT t.name FROM teams t WHERE t.id = " + t + ".owner_team_id), '')"
}

func scanOwner(user, team sql.NullInt64, name string) flowOwner {
	switch {
	case user.Valid:
		return flowOwner{Kind: ownerUser, ID: user.Int64, Name: name}
	case team.Valid:
		return flowOwner{Kind: ownerTeam, ID: team.Int64, Name: name}
	}
	return flowOwner{Kind: ownerGlobal}
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
	return ownedBy("f", u)
}

// The condition on rows of table t, owned as flows are, that only the ones
// the user may change meet: everyone's, theirs and their teams'.
func ownedBy(t string, u *user) (string, []any) {
	if u == nil || u.Admin {
		return "TRUE", nil
	}
	return "((" + t + ".owner_user_id IS NULL AND " + t + ".owner_team_id IS NULL) OR " + t + `.owner_user_id = ?
		OR ` + t + ".owner_team_id IN (SELECT team_id FROM team_members WHERE user_id = ?))", []any{u.ID, u.ID}
}

// The condition on flows f that only the ones out of the trash meet; see
// trash.go.
const liveFlows = "f.deleted_at IS NULL"

// The condition on flows f that only the ones the user sees meet: of those
// out of the trash, the ones they may change, and the ones with reviews
// their roles decide: a human object names one of their roles on the board
// as it is, on the version published, or on the version of a run of it
// waiting for a person. Versions keep the roles their human objects name in
// version_roles, so this reads no snapshot.
func visibleFlows(u *user) (string, []any) {
	cond, args := editableFlows(u)
	if u == nil || u.Admin {
		return liveFlows, args
	}
	return "(" + liveFlows + " AND (" + cond + ` OR EXISTS (SELECT 1 FROM user_roles ur WHERE ur.user_id = ? AND (
		EXISTS (SELECT 1 FROM objects o WHERE o.flow_id = f.id AND o.human AND LOWER(TRIM(o.name)) = LOWER(ur.name))
		OR EXISTS (SELECT 1 FROM version_roles vr WHERE vr.role = LOWER(ur.name) AND (vr.version_id = f.published_version_id
			OR vr.version_id IN (SELECT r.version_id FROM runs r JOIN run_steps s ON s.run_id = r.id
				WHERE r.flow_id = f.id AND r.status = 'running' AND s.status = 'waiting' AND s.kind IN ('human', 'loop'))))))))`, append(args, u.ID)
}

// How far the user may go with the flow: accessEdit, accessReview, or ""
// for not at all, as for a flow in the trash, whoever asks; only the trash
// reaches those (trash.go). A flow that doesn't exist is everyone's to be
// told so.
func flowAccess(ctx context.Context, db querier, u *user, flow int64) (string, error) {
	edit, editArgs := editableFlows(u)
	see, seeArgs := visibleFlows(u)
	var editable, visible bool
	err := db.QueryRowContext(ctx, "SELECT "+edit+", "+see+" FROM flows f WHERE f.id = ?",
		append(append(editArgs, seeArgs...), flow)...).Scan(&editable, &visible)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return accessEdit, nil
	case err != nil:
		return "", err
	case !visible:
		return "", nil
	case editable:
		return accessEdit, nil
	}
	return accessReview, nil
}

// Deciding a review, which its route lets only those holding its role do.
var decidePattern = regexp.MustCompile(`^/api/runs/\d+/steps/\d+$`)

// The access a request needs to the flows it is about. Starring a flow as
// a favourite changes nothing in it, so seeing it is enough.
func neededAccess(r *http.Request) string {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || (r.Method == http.MethodPatch && decidePattern.MatchString(r.URL.Path)) ||
		favouritePattern.MatchString(r.URL.Path) {
		return accessReview
	}
	return accessEdit
}

// The owner a new flow gets from its maker: the one they ask for, else
// their team, when they are in one only, else themselves; nobody without
// sign-in. Says why they may not give it to the one they asked for, if they
// may not.
func newOwner(ctx context.Context, db *sql.DB, u *user, asked *flowOwner) (userID, teamID *int64, msg string, err error) {
	if u == nil {
		return nil, nil, "", nil
	}
	maker := flowOwner{Kind: ownerUser, ID: u.ID}
	to := maker
	if asked != nil {
		to = *asked
		if msg, err = refuseOwner(ctx, db, u, maker, to); msg != "" || err != nil {
			return nil, nil, msg, err
		}
	} else {
		teams, err := userTeams(ctx, db, u.ID)
		if err != nil {
			return nil, nil, "", err
		}
		if len(teams) == 1 {
			to = flowOwner{Kind: ownerTeam, ID: teams[0].ID}
		}
	}
	switch to.Kind {
	case ownerUser:
		return &to.ID, nil, "", nil
	case ownerTeam:
		return nil, &to.ID, "", nil
	}
	return nil, nil, "", nil
}

// Why the user may not give the flow, or anything owned as flows are, from
// its owner now to the one given, if they may not.
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
			return "only an admin takes back what is everyone's", nil
		}
	case ownerUser:
		if now.ID != u.ID {
			return "only its owner or an admin gives it to someone else", nil
		}
	}
	switch to.Kind {
	case ownerUser:
		if to.ID != u.ID {
			return "only an admin gives it to someone else", nil
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
		if err == nil {
			f, err = describeFlow(r.Context(), db, r, f)
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
	{regexp.MustCompile(`^/api/notes/(\d+)(/|$)`), "SELECT flow_id FROM notes WHERE id = ?"},
	{regexp.MustCompile(`^/api/runs/(\d+)(/|$)`), "SELECT flow_id FROM runs WHERE id = ?"},
	{regexp.MustCompile(`^/api/schedules/(\d+)(/|$)`), "SELECT flow_id FROM schedules WHERE id = ?"},
	{regexp.MustCompile(`^/api/webhooks/(\d+)(/|$)`), "SELECT flow_id FROM webhooks WHERE id = ?"},
	{regexp.MustCompile(`^/api/versions/(\d+)(/|$)`), "SELECT flow_id FROM flow_versions WHERE id = ?"},
}

// The query giving the flow of a row an id in a request names, by the
// table the id is of.
var flowOfTable = map[string]string{
	"flows":         "SELECT id FROM flows WHERE id = ?",
	"processes":     "SELECT flow_id FROM processes WHERE id = ?",
	"objects":       "SELECT flow_id FROM objects WHERE id = ?",
	"runs":          "SELECT flow_id FROM runs WHERE id = ?",
	"flow_versions": "SELECT flow_id FROM flow_versions WHERE id = ?",
	// The flow a subflow process runs, which may stay named while it is in
	// the trash, so the process's settings can still be saved; picking one
	// anew is refused there (checkCall).
	"callees": "SELECT id FROM flows WHERE id = ? AND deleted_at IS NULL",
}

// The tables of the ids a request's query or JSON body names by field.
// Bodies with a source and a target name processes only in connections and
// starts, and a flow in settings is the one a subflow process runs.
var flowFields = map[string]string{
	"flow": "flows", "from": "flows",
	"parent": "processes", "board": "processes", "process": "processes", "processes": "processes",
	"object": "objects", "objects": "objects",
}

// A diff of a version with another; see diff.go.
var versionDiffPattern = regexp.MustCompile(`^/api/versions/\d+/diff$`)

// The files of a run's steps; see files.go.
var runFilesPattern = regexp.MustCompile(`^runs/(\d+)(/|$)`)

// How large a body the guard reads to find the flows it names; the routes
// with larger bodies, setup files, name none.
const maxGuardedBody = 1 << 20

// Turns away a request about a flow, or anything in one, that its user
// doesn't see, as if it didn't exist; see visibleFlows. It finds the flows
// a request is about from its path, its query, and the ids its JSON body
// names, so each route needn't; the routes listing flows, or what is in
// several, filter them themselves. A flow in the trash is no one's to see,
// admins' included, with nobody signing in too; only the trash's own routes
// reach it (trash.go), and they name it by a path of their own.
func guardFlows(db *sql.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := requestUser(r)
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/setup" {
			next.ServeHTTP(w, r)
			return
		}
		// Someone who sees every flow may send a body larger than the guard
		// reads, which goes on unread, as their routes keep to their own
		// limits; it can only be about flows they see, or one in the trash,
		// which runs refuse anyway (newRun).
		everything := u == nil || u.Admin
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
		// A diff of two versions names the second in its query; see diff.go.
		if versionDiffPattern.MatchString(r.URL.Path) {
			for _, v := range q["to"] {
				if id, err := strconv.ParseInt(v, 10, 64); err == nil {
					refs = append(refs, ref{flowOfTable["flow_versions"], id})
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
			if err != nil {
				r.Body.Close()
				http.Error(w, "couldn't read the request", http.StatusBadRequest)
				return
			}
			switch {
			case len(body) <= maxGuardedBody:
				r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(body))
				for table, id := range bodyRefs(r.URL.Path, body) {
					for _, i := range id {
						refs = append(refs, ref{flowOfTable[table], i})
					}
				}
			case everything:
				r.Body = struct {
					io.Reader
					io.Closer
				}{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
			default:
				r.Body.Close()
				http.Error(w, "the request is too large", http.StatusRequestEntityTooLarge)
				return
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
				add("callees", f)
			}
		}
	}
	return refs
}
