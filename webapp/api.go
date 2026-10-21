package main

// Other systems start flows over HTTP with an API key, as a person does with
// the Run button, and read back what the runs left. Each flow has an address
// of its own, /v1/flows/<id>/runs, and takes what it starts from: the objects
// on its top board that some process there takes but none generates, which
// a run from the Run button starts with empty. Those are given to the run as
// a subflow process gives its objects to the flow it runs (see calls.go), so
// the process the start points at takes them all, and the others taking them
// run once they exist. Each must be given, by name, holding what a person
// would enter of it (see entries.go): a JSON object with exactly its
// attributes, a JSON array of them for a list, or for an object without
// attributes its text, or any JSON, kept as its text. A flow whose processes start from nothing, or from
// what a person fills in, takes nothing. What a run comes to is what it left
// of the objects connected to the end of the top board, as for a flow a
// subflow process runs.
//
// What a run was given is recorded as a step of its own, before the others,
// so the run shows it, a re-run starts from it, and a persistent object it
// gives holding a unique value another run stored ends the run as a
// duplicate before it starts, as a flow fetching what it handled before
// does; see instances.go.
//
// A key is someone's: the runs it starts are theirs, using their model keys
// and MCP connections, and it starts only the flows they may run and reads
// only the runs they see; see owners.go. Without sign-in, keys are nobody's
// and start any flow, and once people sign in they stop working. A key is
// shown once, as it is made, and kept as its hash, as sessions are. The key
// routes are outside /api/, so a key reaches only them and a session none of
// them.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What a key starts with, so it can be told apart from other secrets.
const apiKeyPrefix = "spk_"

// What runs[].kind is for the step holding what a run was given.
const kindInput = "input"

// How large a request to start a run may be.
const maxAPIBody = 8 << 20

// A key as its owner sees it; the key itself only as it is made.
type apiKey struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// The key's first characters, to tell it apart.
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	Key        string     `json:"key,omitempty"`
}

// The keys of the user, or nobody's without sign-in.
const ownKeys = "user_id <=> ?"

// An object a flow starts from, as a request gives it.
type givenObject struct {
	id       int64
	name     string
	many     bool
	quantity quantity
	fields   []field
	states   []objectState
}

// What a flow takes as it starts, in the order its objects were added; or
// what keeps it from being started this way. Board is the flow's board, and
// db holds its objects as the board has them.
func flowInputs(ctx context.Context, db rowsQuerier, flow int64, board snapshot) ([]givenObject, string, error) {
	w := newWalk(ctx, nil, 0, nil, newFlowRun(flow, 0, board, nil, ""), nil, nil, nil)
	s, err := loadStructure(ctx, db, flow)
	if err != nil {
		return nil, "", err
	}
	var in []givenObject
	names := map[string]bool{}
	for _, o := range board.Objects {
		// What a person is, or reads of a shared object's records, isn't
		// given; nor is what nothing takes, or only queries.
		if o.Parent != nil || o.Owner != nil || o.Human || o.Store != nil || w.generated[o.ID] || len(w.takers[o.ID]) == 0 {
			continue
		}
		if names[o.Name] {
			return nil, fmt.Sprintf("two objects it starts from are named %s; rename one so a request can tell them apart", quote(o.Name)), nil
		}
		names[o.Name] = true
		fs, err := s.fields(o.ID)
		if err != nil {
			return nil, err.Error(), nil
		}
		if f := fileField(fs); f != "" {
			return nil, fmt.Sprintf("%s can't be given in a request: %s holds a file", quote(o.Name), quote(f)), nil
		}
		g := givenObject{id: o.ID, name: o.Name, many: o.Many, fields: fs}
		if o.Many && s[o.ID] != nil {
			g.quantity = s[o.ID].quantity
		}
		for _, st := range board.States {
			if st.Object == o.ID {
				g.states = append(g.states, st)
			}
		}
		in = append(in, g)
	}
	return in, "", nil
}

// What the object holds, from what a request gives of it, or what is wrong
// with that. An object without attributes holds text; anything else given
// for it is kept as its JSON.
func (g givenObject) content(raw json.RawMessage) (string, error) {
	if len(g.fields) == 0 {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text, nil
		}
		var b bytes.Buffer
		if err := json.Compact(&b, raw); err != nil {
			return "", err
		}
		return b.String(), nil
	}
	o := shapedObject{id: g.id, name: g.name, many: g.many, quantity: g.quantity, fields: g.fields}
	return (&outputContract{}).shapeObject(o, string(raw))
}

// What a request to start a run gives: the objects the flow starts from, by
// name, and the states any of them are in, by name.
type apiRunRequest struct {
	Inputs map[string]json.RawMessage `json:"inputs"`
	States map[string]string          `json:"states"`
}

// What the request gives of each object the flow starts from, by object, or
// what is wrong with it.
func givenValues(in []givenObject, req apiRunRequest) (map[int64]value, error) {
	keys := make([]string, len(in))
	for i, g := range in {
		keys[i] = g.name
	}
	if req.Inputs == nil {
		req.Inputs = map[string]json.RawMessage{}
	}
	if err := exactKeys(req.Inputs, keys); err != nil {
		if len(in) == 0 {
			return nil, errors.New("the flow takes no inputs")
		}
		return nil, fmt.Errorf("the inputs don't match what the flow takes: %w", err)
	}
	given := map[int64]value{}
	for _, g := range in {
		content, err := g.content(req.Inputs[g.name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", quote(g.name), err)
		}
		given[g.id] = value{content: content}
	}
	for name, state := range req.States {
		i := slices.IndexFunc(in, func(g givenObject) bool { return g.name == name })
		if i < 0 {
			return nil, fmt.Errorf("states: the flow takes no %s", quote(name))
		}
		j := slices.IndexFunc(in[i].states, func(st objectState) bool { return st.Name == state })
		if j < 0 {
			return nil, fmt.Errorf("states: %s has no state %s", quote(name), quote(state))
		}
		v := given[in[i].id]
		v.state = in[i].states[j].ID
		given[in[i].id] = v
	}
	return given, nil
}

// A JSON Schema of what a request gives of the object.
func (g givenObject) schema() map[string]any {
	if len(g.fields) == 0 {
		return map[string]any{"type": "string", "description": "Its text"}
	}
	item := fieldsSchema(g.fields)
	if g.many {
		return listSchema(item, g.quantity)
	}
	return item
}

func fieldsSchema(fs []field) map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, f := range fs {
		var p map[string]any
		if len(f.fields) > 0 {
			p = fieldsSchema(f.fields)
		} else {
			p = map[string]any{}
		}
		if f.many {
			p = listSchema(p, f.quantity)
		}
		if f.definition != "" {
			p["description"] = f.definition
		}
		props[f.name] = p
		required = append(required, f.name)
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func listSchema(item map[string]any, q quantity) map[string]any {
	s := map[string]any{"type": "array", "items": item}
	if q.min > 0 {
		s["minItems"] = q.min
	}
	if q.max != nil {
		s["maxItems"] = *q.max
	}
	return s
}

// How a flow is started over the API, as GET /v1/flows/<id> and the flow's
// settings show it.
type flowAPI struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Where a run of it is started, with POST.
	URL    string         `json:"url"`
	Inputs []apiInput     `json:"inputs"`
	Schema map[string]any `json:"schema"`
	// Why it can't be started this way, if it can't.
	Problem string `json:"problem,omitempty"`
}

// An object a flow starts from.
type apiInput struct {
	Name   string         `json:"name"`
	Many   bool           `json:"many"`
	States []string       `json:"states"`
	Schema map[string]any `json:"schema"`
}

// The scheme and host the webapp is reached by from elsewhere.
func publicOrigin(r *http.Request) string {
	if u := strings.TrimRight(env("PUBLIC_URL", ""), "/"); u != "" {
		return u
	}
	return requestOrigin(r)
}

// How the flow is started over the API; sql.ErrNoRows without it.
func describeFlowAPI(ctx context.Context, db *sql.DB, r *http.Request, flow int64) (flowAPI, error) {
	d := flowAPI{ID: flow, URL: publicOrigin(r) + "/v1/flows/" + strconv.FormatInt(flow, 10) + "/runs", Inputs: []apiInput{}}
	var structFlow bool
	if err := db.QueryRowContext(ctx, "SELECT name, is_struct FROM flows WHERE id = ?", flow).Scan(&d.Name, &structFlow); err != nil {
		return d, err
	}
	var in []givenObject
	err := inTx(ctx, db, func(tx *sql.Tx) error {
		board, err := takeSnapshot(ctx, tx, flow)
		if err != nil {
			return err
		}
		in, d.Problem, err = flowInputs(ctx, tx, flow, board)
		return err
	})
	if err != nil {
		return d, err
	}
	if structFlow {
		d.Problem = notInStruct
	}
	props := map[string]any{}
	required := []string{}
	states := map[string]any{}
	for _, g := range in {
		i := apiInput{Name: g.name, Many: g.many, States: []string{}, Schema: g.schema()}
		for _, st := range g.states {
			i.States = append(i.States, st.Name)
		}
		if len(i.States) > 0 {
			states[g.name] = map[string]any{"enum": i.States}
		}
		d.Inputs = append(d.Inputs, i)
		props[g.name] = i.Schema
		required = append(required, g.name)
	}
	body := map[string]any{
		"inputs": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false},
	}
	if len(states) > 0 {
		body["states"] = map[string]any{"type": "object", "properties": states, "additionalProperties": false}
	}
	d.Schema = map[string]any{"type": "object", "properties": body, "additionalProperties": false}
	if len(required) > 0 {
		d.Schema["required"] = []string{"inputs"}
	}
	return d, nil
}

// What a run came to, as the API shows it.
type apiRun struct {
	ID         int64      `json:"id"`
	Flow       int64      `json:"flow"`
	Status     string     `json:"status"`
	URL        string     `json:"url"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	// What it left of the objects connected to the top board's end, by
	// name, once it generated them.
	Outcomes map[string]apiOutcome `json:"outcomes"`
	// What went wrong in the steps that failed.
	Errors []apiStepError `json:"errors"`
}

type apiOutcome struct {
	State string `json:"state,omitempty"`
	// A JSON object or array as such; anything else as text.
	Value json.RawMessage `json:"value"`
}

type apiStepError struct {
	Process string `json:"process"`
	Error   string `json:"error"`
}

func getAPIRun(ctx context.Context, db *sql.DB, r *http.Request, id int64) (apiRun, error) {
	rn, err := getRun(ctx, db, id)
	if err != nil {
		return apiRun{}, err
	}
	out := apiRun{ID: rn.ID, Flow: rn.Flow, Status: rn.Status, StartedAt: rn.StartedAt, FinishedAt: rn.FinishedAt,
		URL:      fmt.Sprintf("%s/#/flows/%d/runs/%d", publicOrigin(r), rn.Flow, rn.ID),
		Outcomes: map[string]apiOutcome{}, Errors: []apiStepError{}}
	s, err := loadStructure(ctx, db, rn.Flow)
	if err != nil {
		return out, err
	}
	err = scanAll(ctx, db,
		`SELECT ro.object_ref, ro.name, ro.state, ro.content FROM run_objects ro JOIN objects o ON o.id = ro.object_ref
		WHERE ro.run_id = ? AND o.at_end AND o.parent_id IS NULL AND o.`+onProcessBoard+` ORDER BY ro.id`,
		func(rows *sql.Rows) error {
			var object int64
			var name, state, content string
			if err := rows.Scan(&object, &name, &state, &content); err != nil {
				return err
			}
			fs, _ := s.fields(object)
			// The latest of each wins.
			out.Outcomes[name] = apiOutcome{State: state, Value: outcomeValue(content, len(fs) == 0)}
			return nil
		}, id)
	if err != nil {
		return out, err
	}
	err = scanAll(ctx, db,
		"SELECT process_name, error FROM run_steps WHERE run_id = ? AND status IN (?, ?) AND error <> '' ORDER BY id",
		func(rows *sql.Rows) error {
			var e apiStepError
			err := rows.Scan(&e.Process, &e.Error)
			out.Errors = append(out.Errors, e)
			return err
		}, id, stepError, stepDuplicate)
	return out, err
}

// What an object holds as an outcome. An agent generating one without
// attributes hands it in under data, which it is unwrapped from.
func outcomeValue(content string, data bool) json.RawMessage {
	t := strings.TrimSpace(content)
	if !(strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) || !json.Valid([]byte(t)) {
		return json.RawMessage(quote(content))
	}
	if fields, err := jsonObject(t); data && err == nil && len(fields) == 1 && fields[dataAttribute] != nil {
		return fields[dataAttribute]
	}
	return json.RawMessage(t)
}

// Why a request to start a run was refused.
type apiRefusal struct {
	status int
	msg    string
}

// Starts a run of the flow for the key, given what the request gives, for
// whom the key is; see startRun. A request sent again with the same
// idempotency key returns the run it started, with created false.
func startAPIRun(ctx context.Context, db *sql.DB, eng *engine, flow, key int64, startedBy *int64, idempotency string, req apiRunRequest) (id int64, created bool, refused *apiRefusal, err error) {
	var idem *string
	if idempotency != "" {
		idem = &idempotency
		if id, refused, err := idempotentRun(ctx, db, flow, key, idempotency); id != 0 || refused != nil || err != nil {
			return id, false, refused, err
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, nil, err
	}
	defer tx.Rollback()
	if s, err := isStruct(ctx, tx, flow); err != nil || s {
		if s {
			return 0, false, &apiRefusal{http.StatusBadRequest, notInStruct}, nil
		}
		return 0, false, nil, err
	}
	nr, err := newRun(ctx, tx, flow, nil, startedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, &apiRefusal{http.StatusNotFound, "flow not found"}, nil
	}
	if err != nil {
		return 0, false, nil, err
	}
	in, msg, err := flowInputs(ctx, tx, flow, nr.board)
	if err != nil {
		return 0, false, nil, err
	}
	if msg != "" {
		return 0, false, &apiRefusal{http.StatusUnprocessableEntity, msg}, nil
	}
	given, err := givenValues(in, req)
	if err != nil {
		return 0, false, &apiRefusal{http.StatusBadRequest, err.Error()}, nil
	}
	_, err = tx.ExecContext(ctx, "UPDATE runs SET api_key_id = ?, idempotency_key = ? WHERE id = ?", key, idem, nr.id)
	if mysqlErrorIs(err, mysqlErrDuplicate) {
		// Another request with the same idempotency key started it meanwhile.
		tx.Rollback()
		id, refused, err := idempotentRun(ctx, db, flow, key, idempotency)
		return id, false, refused, err
	}
	if err != nil {
		return 0, false, nil, err
	}
	dup, err := recordGiven(ctx, tx, nr.id, in, given)
	if err != nil {
		return 0, false, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, nil, err
	}
	if dup == nil {
		eng.start(nr.id, flow, nr.version, nr.board, nr.repos, nr.prefix, given)
	}
	return nr.id, true, nil, nil
}

// The run the key started with the idempotency key, if any; refused if it
// is of another flow.
func idempotentRun(ctx context.Context, db *sql.DB, flow, key int64, idempotency string) (int64, *apiRefusal, error) {
	var id, of int64
	err := db.QueryRowContext(ctx, "SELECT id, flow_id FROM runs WHERE api_key_id = ? AND idempotency_key = ?", key, idempotency).Scan(&id, &of)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil, nil
	case err != nil:
		return 0, nil, err
	case of != flow:
		return 0, &apiRefusal{http.StatusConflict, fmt.Sprintf("the idempotency key started run #%d, of another flow", id)}, nil
	}
	return id, nil, nil
}

// Records what a run was given as a step before the others, and stores the
// persistent objects among it. A unique value another run stored ends the
// run as terminated, and is returned.
func recordGiven(ctx context.Context, tx *sql.Tx, run int64, in []givenObject, given map[int64]value) (*duplicate, error) {
	if len(given) == 0 {
		return nil, nil
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO run_steps (run_id, process_name, kind, prompt, inputs, status, output, error, instructions, finished_at)
		VALUES (?, 'Given through the API', ?, '', '[]', ?, '', '', '', CURRENT_TIMESTAMP(3))`,
		run, kindInput, stepDone)
	if err != nil {
		return nil, err
	}
	step, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, g := range in {
		v := given[g.id]
		state := ""
		if i := slices.IndexFunc(g.states, func(st objectState) bool { return st.ID == v.state }); i >= 0 {
			state = g.states[i].Name
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO run_objects (run_id, step_id, object_id, object_ref, name, state, content) VALUES (?, ?, ?, ?, ?, ?, ?)",
			run, step, g.id, g.id, g.name, state, v.content); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT store"); err != nil {
		return nil, err
	}
	dup, err := storeInstances(ctx, tx, run, step)
	if err != nil || dup == nil {
		return nil, err
	}
	for _, s := range []struct {
		query string
		args  []any
	}{
		{"ROLLBACK TO SAVEPOINT store", nil},
		{"UPDATE run_steps SET status = ?, error = ? WHERE id = ?", []any{stepDuplicate, dup.message(), step}},
		{"UPDATE runs SET status = ?, finished_at = CURRENT_TIMESTAMP(3) WHERE id = ?", []any{runTerminated, run}},
	} {
		if _, err := tx.ExecContext(ctx, s.query, s.args...); err != nil {
			return nil, err
		}
	}
	return dup, releaseRun(ctx, tx, run)
}

// Who a request's API key is: the key, and its owner, nil when nobody
// signs in. Writes why not and returns false if the key doesn't let it in.
func apiKeyUser(w http.ResponseWriter, r *http.Request, db *sql.DB, signIn bool) (int64, *user, bool) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "an API key is needed, as Authorization: Bearer <key>; make one in the settings", http.StatusUnauthorized)
		return 0, nil, false
	}
	var key int64
	var owner sql.NullInt64
	var u user
	var removed bool
	err := db.QueryRowContext(r.Context(),
		`SELECT k.id, k.user_id, COALESCE(u.email, ''), COALESCE(u.name, ''), COALESCE(u.admin, FALSE), COALESCE(u.team_manager, FALSE), u.removed_at IS NOT NULL
		FROM api_keys k LEFT JOIN users u ON u.id = k.user_id WHERE k.token_hash = ?`, hashToken(strings.TrimSpace(token))).
		Scan(&key, &owner, &u.Email, &u.Name, &u.Admin, &u.TeamManager, &removed)
	if errors.Is(err, sql.ErrNoRows) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "the API key isn't one; it may have been deleted", http.StatusUnauthorized)
		return 0, nil, false
	}
	if err != nil {
		serverError(w, err)
		return 0, nil, false
	}
	switch {
	case signIn && !owner.Valid:
		http.Error(w, "the API key was made before people signed in, so it is nobody's; make a new one", http.StatusUnauthorized)
		return 0, nil, false
	case removed:
		http.Error(w, "the API key's owner was removed", http.StatusUnauthorized)
		return 0, nil, false
	}
	if _, err := db.ExecContext(r.Context(), "UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP(3) WHERE id = ?", key); err != nil {
		serverError(w, err)
		return 0, nil, false
	}
	if !signIn || !owner.Valid {
		return key, nil, true
	}
	u.ID = owner.Int64
	return key, &u, true
}

// Whether the key's owner may go as far as need with the flow, writing why
// not if not; a flow they don't see doesn't exist to them.
func apiFlowAccess(w http.ResponseWriter, r *http.Request, db *sql.DB, u *user, flow int64, need string) bool {
	access, err := flowAccess(r.Context(), db, u, flow)
	switch {
	case err != nil:
		serverError(w, err)
		return false
	case access == "":
		http.Error(w, "flow not found", http.StatusNotFound)
		return false
	case access == accessReview && need == accessEdit:
		http.Error(w, "the API key's owner only decides this flow's reviews, and may not run it", http.StatusForbidden)
		return false
	}
	return true
}

func registerAPIRoutes(mux *http.ServeMux, db *sql.DB, eng *engine, signIn bool) {
	// The keys of whoever asks.
	mux.HandleFunc("GET /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		keys := []apiKey{}
		err := scanAll(r.Context(), db, "SELECT id, name, prefix, created_at, last_used_at FROM api_keys WHERE "+ownKeys+" ORDER BY id",
			func(rows *sql.Rows) error {
				var k apiKey
				err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsedAt)
				keys = append(keys, k)
				return err
			}, requestUserID(r))
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})

	// Makes a key for whoever asks: {"name": "..."}. The reply holds the key,
	// which is never shown again.
	mux.HandleFunc("POST /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" || len(name) > 255 {
			http.Error(w, "a key needs a name, of at most 255 characters, saying what uses it", http.StatusBadRequest)
			return
		}
		token := apiKeyPrefix + randomToken()
		k := apiKey{Name: name, Prefix: token[:len(apiKeyPrefix)+6], Key: token}
		res, err := db.ExecContext(r.Context(), "INSERT INTO api_keys (user_id, name, prefix, token_hash) VALUES (?, ?, ?, ?)",
			requestUserID(r), k.Name, k.Prefix, hashToken(token))
		if err == nil {
			k.ID, err = res.LastInsertId()
		}
		if err == nil {
			err = db.QueryRowContext(r.Context(), "SELECT created_at FROM api_keys WHERE id = ?", k.ID).Scan(&k.CreatedAt)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, k)
	})

	// Deletes one of whoever asks' keys; whatever uses it is turned away from then on.
	mux.HandleFunc("DELETE /api/api-keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		res, err := db.ExecContext(r.Context(), "DELETE FROM api_keys WHERE id = ? AND "+ownKeys, id, requestUserID(r))
		if err != nil {
			serverError(w, err)
			return
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			http.Error(w, "key not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// How the flow is started over the API, for its settings to show.
	mux.HandleFunc("GET /api/flows/{id}/api", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		d, err := describeFlowAPI(r.Context(), db, r, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})

	// The routes keys reach.
	flowID := func(w http.ResponseWriter, r *http.Request) (int64, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
		}
		return id, err == nil
	}

	// What the flow takes, as a JSON Schema of the request starting it too.
	mux.HandleFunc("GET /v1/flows/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, u, ok := apiKeyUser(w, r, db, signIn)
		if !ok {
			return
		}
		id, ok := flowID(w, r)
		if !ok || !apiFlowAccess(w, r, db, u, id, accessReview) {
			return
		}
		d, err := describeFlowAPI(r.Context(), db, r, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})

	// Starts a run of the flow on its board as it is, given its inputs:
	// {"inputs": {"<object>": ...}, "states": {"<object>": "<state>"}}. An
	// Idempotency-Key header makes sending it again return the same run.
	mux.HandleFunc("POST /v1/flows/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		key, u, ok := apiKeyUser(w, r, db, signIn)
		if !ok {
			return
		}
		id, ok := flowID(w, r)
		if !ok || !apiFlowAccess(w, r, db, u, id, accessEdit) {
			return
		}
		var req apiRunRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		idempotency := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if len(idempotency) > 255 {
			http.Error(w, "the Idempotency-Key is longer than 255 characters", http.StatusBadRequest)
			return
		}
		var startedBy *int64
		if u != nil {
			startedBy = &u.ID
		}
		run, created, refused, err := startAPIRun(r.Context(), db, eng, id, key, startedBy, idempotency, req)
		if err != nil {
			serverError(w, err)
			return
		}
		if refused != nil {
			http.Error(w, refused.msg, refused.status)
			return
		}
		out, err := getAPIRun(r.Context(), db, r, run)
		if err != nil {
			serverError(w, err)
			return
		}
		status := http.StatusOK
		switch {
		case created && out.Status == runTerminated:
			// What it was given another run handled already.
			status = http.StatusConflict
		case created:
			status = http.StatusCreated
		}
		w.Header().Set("Location", publicOrigin(r)+"/v1/runs/"+strconv.FormatInt(run, 10))
		writeJSON(w, status, out)
	})

	// What the run came to so far.
	mux.HandleFunc("GET /v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, u, ok := apiKeyUser(w, r, db, signIn)
		if !ok {
			return
		}
		id, ok := flowID(w, r)
		if !ok {
			return
		}
		var flow int64
		err := db.QueryRowContext(r.Context(), "SELECT flow_id FROM runs WHERE id = ?", id).Scan(&flow)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if !apiFlowAccess(w, r, db, u, flow, accessReview) {
			return
		}
		out, err := getAPIRun(r.Context(), db, r, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
}
