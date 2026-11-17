package main

// Other systems start flows over HTTP with an API key, as a person does with
// the Run button, and read back what the runs left. A run started this way
// runs the version the flow published, or its board as it is while it
// publishes none (see publish.go), and what it takes is that version's.
// Each flow has an address of its own, /v1/flows/<id>/runs, and takes what
// it starts from: the objects on its top board that some process there
// takes but none generates, which
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
// does; see instances.go. A webhook's delivery starts a run the same way
// (startGivenRun), with what its mapping takes from the payload; see
// webhooks.go. So does a person giving what the flow starts from by hand,
// from Run's menu on the board, which may run the board as it is.
//
// A key is someone's, in a project: the runs it starts are theirs, using
// their model keys and MCP connections, as they are in the project, and it
// starts only the flows of its project they may run there and reads only
// the runs there they see; see owners.go and projects.go. A flow or a run of
// another project, or of one archived, isn't there for it, and once its
// maker may no longer enter its project it stops working. Without sign-in,
// keys are nobody's and start any flow of their project, and once people
// sign in they stop working. A key is shown once, as it is made, and kept
// as its hash, as sessions are. The key routes are outside /api/, so a key
// reaches only them and a session none of them.

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
// what keeps it from being started this way. Board is the board its runs
// start on: the version published, if any, or else the board as it is.
func flowInputs(ctx context.Context, flow int64, board snapshot) ([]givenObject, string) {
	// Only looked at, so in no project.
	w := newWalk(ctx, nil, 0, nil, newFlowRun(0, flow, 0, board, nil, ""), nil, nil, nil)
	s := w.fr.rb.structure
	var in []givenObject
	names := map[string]bool{}
	for _, o := range board.Objects {
		// What a person is, or reads of a shared object's records, isn't
		// given; nor is what nothing takes, or only queries.
		if o.Parent != nil || o.Owner != nil || o.Human || o.Store != nil || w.generated[o.ID] || len(w.takers[o.ID]) == 0 {
			continue
		}
		if names[o.Name] {
			return nil, fmt.Sprintf("two objects it starts from are named %s; rename one so a request can tell them apart", quote(o.Name))
		}
		names[o.Name] = true
		fs, err := s.fields(o.ID)
		if err != nil {
			return nil, err.Error()
		}
		if f := fileField(fs); f != "" {
			return nil, fmt.Sprintf("%s can't be given in a request: %s holds a file", quote(o.Name), quote(f))
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
	return in, ""
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
			p = typeSchema(f.kind, f.pattern)
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

// What the flow starts from, on the board a run of it would take: with
// published the version it published, if any, else the board as it is;
// and why it can't be given anything, if it can't.
func flowInputsOf(ctx context.Context, db *sql.DB, flow int64, published bool) (in []givenObject, problem string, err error) {
	err = inTx(ctx, db, func(tx *sql.Tx) error {
		var board snapshot
		var err error
		if published {
			board, err = boardToRun(ctx, tx, flow)
		} else {
			board, err = takeSnapshot(ctx, tx, flow)
		}
		if err != nil {
			return err
		}
		in, problem = flowInputs(ctx, flow, board)
		return nil
	})
	return in, problem, err
}

// The objects a flow starts from as they are described, each with a JSON
// Schema of what to give of it.
func apiInputs(in []givenObject) []apiInput {
	out := []apiInput{}
	for _, g := range in {
		i := apiInput{Name: g.name, Many: g.many, States: []string{}, Schema: g.schema()}
		for _, st := range g.states {
			i.States = append(i.States, st.Name)
		}
		out = append(out, i)
	}
	return out
}

// How the flow is started over the API; sql.ErrNoRows without it.
func describeFlowAPI(ctx context.Context, db *sql.DB, r *http.Request, flow int64) (flowAPI, error) {
	d := flowAPI{ID: flow, URL: publicOrigin(r) + "/v1/flows/" + strconv.FormatInt(flow, 10) + "/runs", Inputs: []apiInput{}}
	var structFlow bool
	if err := db.QueryRowContext(ctx, "SELECT name, is_struct FROM flows WHERE id = ?", flow).Scan(&d.Name, &structFlow); err != nil {
		return d, err
	}
	// What a run over the API takes is what the version it runs takes.
	in, problem, err := flowInputsOf(ctx, db, flow, true)
	if err != nil {
		return d, err
	}
	d.Problem = problem
	if structFlow {
		d.Problem = notInStruct
	}
	d.Inputs = apiInputs(in)
	props := map[string]any{}
	required := []string{}
	states := map[string]any{}
	for _, i := range d.Inputs {
		if len(i.States) > 0 {
			states[i.Name] = map[string]any{"enum": i.States}
		}
		props[i.Name] = i.Schema
		required = append(required, i.Name)
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
	project, err := runProject(ctx, db, id)
	if err != nil {
		return apiRun{}, err
	}
	out := apiRun{ID: rn.ID, Flow: rn.Flow, Status: rn.Status, StartedAt: rn.StartedAt, FinishedAt: rn.FinishedAt,
		URL:      runAddress(publicOrigin(r), project, rn.Flow, rn.ID),
		Outcomes: map[string]apiOutcome{}, Errors: []apiStepError{}}
	// The outcomes are the objects the run's board connects to its end.
	b, err := runBoardOf(ctx, db, id)
	if err != nil {
		return out, err
	}
	err = scanAll(ctx, db,
		`SELECT object_ref, name, state, content FROM run_objects WHERE run_id = ? AND object_ref IS NOT NULL ORDER BY id`,
		func(rows *sql.Rows) error {
			var object int64
			var name, state, content string
			if err := rows.Scan(&object, &name, &state, &content); err != nil {
				return err
			}
			if o, ok := b.objects[object]; !ok || !o.End || o.Parent != nil || o.Owner != nil {
				return nil
			}
			fs, _ := b.structure.fields(object)
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

// The address of a run's page, in its project, as the frontend has it.
func runAddress(origin string, project, flow, run int64) string {
	return fmt.Sprintf("%s/#/p/%d/flows/%d/runs/%d", origin, project, flow, run)
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

// What started a run given what it starts from: an API key, a webhook (see
// webhooks.go), or a person by hand. A key or a webhook keeps the runs it
// started apart by its idempotency keys, so a request or a delivery sent
// again starts no second run.
type runSource struct {
	// The column of runs naming it, api_key_id or webhook_id; none for a
	// person, whose run is started_by them as any run from the board.
	column string
	id     int64
	// What the step holding what the run was given is called.
	given string
}

func apiKeySource(key int64) runSource {
	return runSource{column: "api_key_id", id: key, given: "Given through the API"}
}

// A person giving what the flow starts from by hand, from Run's menu.
var handSource = runSource{given: "Given by hand"}

// Starts a run of the flow for what started it, given what ask makes of the
// objects the flow starts from, for whom that is; see startRun. A request
// sent again with the same idempotency key returns the run it started, with
// created false. With published the run takes the version the flow
// published, if any, as one started from elsewhere does, else the board as
// it is, and what it is given is checked against what that board starts
// from.
func startGivenRun(ctx context.Context, db *sql.DB, eng *engine, flow int64, src runSource, startedBy *int64, idempotency string, published bool, ask func([]givenObject) (apiRunRequest, error)) (id int64, created bool, refused *apiRefusal, err error) {
	var idem *string
	if idempotency != "" {
		idem = &idempotency
		if id, refused, err := idempotentRun(ctx, db, flow, src, idempotency); id != 0 || refused != nil || err != nil {
			return id, false, refused, err
		}
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
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
	nr, err := newRun(ctx, tx, flow, nil, startedBy, published)
	// A key or a webhook finds no flow in an archived project; a person
	// starting it by hand is told why.
	if errors.Is(err, errProjectArchived) && src.column == "" {
		return 0, false, &apiRefusal{http.StatusConflict, archivedRuns}, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, &apiRefusal{http.StatusNotFound, "flow not found"}, nil
	}
	if err != nil {
		return 0, false, nil, err
	}
	b := newRunBoard(flow, nr.board)
	b.project = nr.project
	in, msg := flowInputs(ctx, flow, nr.board)
	if msg != "" {
		return 0, false, &apiRefusal{http.StatusUnprocessableEntity, msg}, nil
	}
	req, err := ask(in)
	if err != nil {
		return 0, false, &apiRefusal{http.StatusBadRequest, err.Error()}, nil
	}
	given, err := givenValues(in, req)
	if err != nil {
		return 0, false, &apiRefusal{http.StatusBadRequest, err.Error()}, nil
	}
	if src.column != "" {
		_, err = tx.ExecContext(ctx, "UPDATE runs SET "+src.column+" = ?, idempotency_key = ? WHERE id = ?", src.id, idem, nr.id)
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			// Another request with the same idempotency key started it meanwhile.
			tx.Rollback()
			id, refused, err := idempotentRun(ctx, db, flow, src, idempotency)
			return id, false, refused, err
		}
		if err != nil {
			return 0, false, nil, err
		}
	}
	if msg, err := refusedGiven(ctx, tx, nr.project, nr.board, in, given); err != nil {
		return 0, false, nil, err
	} else if msg != "" {
		return 0, false, &apiRefusal{http.StatusUnprocessableEntity, msg}, nil
	}
	nr.given = given
	var none *nothingToTake
	if err := takeRecords(ctx, tx, flow, &nr); errors.As(err, &none) {
		return 0, false, &apiRefusal{http.StatusConflict, "nothing to run on: " + none.Error()}, nil
	} else if err != nil {
		return 0, false, nil, err
	}
	dup, err := recordGiven(ctx, tx, b, nr.id, src.given, in, given)
	if err != nil {
		return 0, false, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, nil, err
	}
	if dup == nil {
		eng.start(nr, flow, given)
	}
	return nr.id, true, nil, nil
}

// The run the source started with the idempotency key, if any; refused if
// it is of another flow.
func idempotentRun(ctx context.Context, db *sql.DB, flow int64, src runSource, idempotency string) (int64, *apiRefusal, error) {
	var id, of int64
	err := db.QueryRowContext(ctx, "SELECT id, flow_id FROM runs WHERE "+src.column+" = ? AND idempotency_key = ?", src.id, idempotency).Scan(&id, &of)
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

// Records what a run was given as a step before the others, called name,
// and stores the persistent objects among it. A unique value another run
// stored ends the run as terminated, and is returned.
func recordGiven(ctx context.Context, tx *sql.Tx, b *runBoard, run int64, name string, in []givenObject, given map[int64]value) (*duplicate, error) {
	if len(given) == 0 {
		return nil, nil
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO run_steps (run_id, process_name, kind, prompt, inputs, status, output, error, instructions, finished_at, project_id)
		VALUES (?, ?, ?, '', '[]', ?, '', '', '', CURRENT_TIMESTAMP(3), ?)`,
		run, truncate(name, 255), kindInput, stepDone, b.project)
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
		if err := insertRunObject(ctx, tx, run, step, g.id, g.name, state, v.content); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT store"); err != nil {
		return nil, err
	}
	dup, err := storeInstances(ctx, tx, b, run, step)
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

// An API key as a request presents it: the key, the project it is of, and
// its owner as they are there, nil when nobody signs in.
type apiCaller struct {
	key, project int64
	user         *user
}

// Who a request's API key is. Writes why not and returns false if the key
// doesn't let it in: it isn't one, its owner was removed, or may no longer
// enter its project.
func apiKeyUser(w http.ResponseWriter, r *http.Request, db *sql.DB, signIn bool) (apiCaller, bool) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "an API key is needed, as Authorization: Bearer <key>; make one in the settings", http.StatusUnauthorized)
		return apiCaller{}, false
	}
	var c apiCaller
	var owner sql.NullInt64
	var u user
	var removed bool
	// The owner's admin flag is the instance's; what they are in the key's
	// project is worked out below.
	err := db.QueryRowContext(r.Context(),
		`SELECT k.id, k.project_id, k.user_id, COALESCE(u.email, ''), COALESCE(u.name, ''), COALESCE(u.admin, FALSE), COALESCE(u.team_manager, FALSE), u.removed_at IS NOT NULL
		FROM api_keys k LEFT JOIN users u ON u.id = k.user_id WHERE k.token_hash = ?`, hashToken(strings.TrimSpace(token))).
		Scan(&c.key, &c.project, &owner, &u.Email, &u.Name, &u.Super, &u.TeamManager, &removed)
	if errors.Is(err, sql.ErrNoRows) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "the API key isn't one; it may have been deleted", http.StatusUnauthorized)
		return apiCaller{}, false
	}
	if err != nil {
		serverError(w, err)
		return apiCaller{}, false
	}
	switch {
	case signIn && !owner.Valid:
		http.Error(w, "the API key was made before people signed in, so it is nobody's; make a new one", http.StatusUnauthorized)
		return apiCaller{}, false
	case removed:
		http.Error(w, "the API key's owner was removed", http.StatusUnauthorized)
		return apiCaller{}, false
	}
	if signIn {
		u.ID = owner.Int64
		in, ok, err := userInProject(r.Context(), db, &u, c.project)
		if err != nil {
			serverError(w, err)
			return apiCaller{}, false
		}
		if !ok {
			http.Error(w, "the API key's owner may no longer enter its project", http.StatusUnauthorized)
			return apiCaller{}, false
		}
		c.user = in
	}
	if _, err := db.ExecContext(r.Context(), "UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP(3) WHERE id = ?", c.key); err != nil {
		serverError(w, err)
		return apiCaller{}, false
	}
	return c, true
}

// Whether the key's owner may go as far as need with the flow, writing why
// not if not; a flow they don't see doesn't exist to them, nor does one of
// another project than the key's, or of one archived, whoever asks.
func apiFlowAccess(w http.ResponseWriter, r *http.Request, db *sql.DB, c apiCaller, flow int64, need string) bool {
	var project int64
	var archived bool
	err := db.QueryRowContext(r.Context(), "SELECT f.project_id, p.archived_at IS NOT NULL FROM flows f JOIN projects p ON p.id = f.project_id WHERE f.id = ?", flow).
		Scan(&project, &archived)
	switch {
	case errors.Is(err, sql.ErrNoRows) || err == nil && (project != c.project || archived):
		http.Error(w, "flow not found", http.StatusNotFound)
		return false
	case err != nil:
		serverError(w, err)
		return false
	}
	access, err := flowAccess(r.Context(), db, c.user, flow)
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
	// The keys of whoever asks, of the project the request is worked on in,
	// whose flows alone each starts.
	mux.HandleFunc("GET /api/api-keys", func(w http.ResponseWriter, r *http.Request) {
		keys := []apiKey{}
		err := scanAll(r.Context(), db, "SELECT id, name, prefix, created_at, last_used_at FROM api_keys WHERE project_id = ? AND "+ownKeys+" ORDER BY id",
			func(rows *sql.Rows) error {
				var k apiKey
				err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsedAt)
				keys = append(keys, k)
				return err
			}, requestProject(r), requestUserID(r))
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})

	// Makes a key for whoever asks, in the project the request is worked on
	// in: {"name": "..."}. The reply holds the key, which is never shown
	// again.
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
		res, err := db.ExecContext(r.Context(), "INSERT INTO api_keys (project_id, user_id, name, prefix, token_hash) VALUES (?, ?, ?, ?, ?)",
			requestProject(r), requestUserID(r), k.Name, k.Prefix, hashToken(token))
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
		res, err := db.ExecContext(r.Context(), "DELETE FROM api_keys WHERE id = ? AND project_id = ? AND "+ownKeys, id, requestProject(r), requestUserID(r))
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

	// What a run started by hand from the board is given, as Run's menu
	// asks for it: what the board as it is starts from, or with
	// ?published=true what the version published does, if any.
	mux.HandleFunc("GET /api/flows/{id}/inputs", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var structFlow bool
		err = db.QueryRowContext(r.Context(), "SELECT is_struct FROM flows WHERE id = ?", id).Scan(&structFlow)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		out := struct {
			Inputs []apiInput `json:"inputs"`
			// Why it can't be given anything, if it can't.
			Problem string `json:"problem,omitempty"`
		}{Inputs: []apiInput{}, Problem: notInStruct}
		if !structFlow {
			in, problem, err := flowInputsOf(r.Context(), db, id, r.URL.Query().Get("published") == "true")
			if err != nil {
				serverError(w, err)
				return
			}
			out.Inputs, out.Problem = apiInputs(in), problem
		}
		writeJSON(w, http.StatusOK, out)
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
		c, ok := apiKeyUser(w, r, db, signIn)
		if !ok {
			return
		}
		id, ok := flowID(w, r)
		if !ok || !apiFlowAccess(w, r, db, c, id, accessReview) {
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

	// Starts a run of the flow on the version it published, or its board as
	// it is, given its inputs:
	// {"inputs": {"<object>": ...}, "states": {"<object>": "<state>"}}. An
	// Idempotency-Key header makes sending it again return the same run.
	mux.HandleFunc("POST /v1/flows/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		c, ok := apiKeyUser(w, r, db, signIn)
		if !ok {
			return
		}
		id, ok := flowID(w, r)
		if !ok || !apiFlowAccess(w, r, db, c, id, accessEdit) {
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
		if c.user != nil {
			startedBy = &c.user.ID
		}
		run, created, refused, err := startGivenRun(r.Context(), db, eng, id, apiKeySource(c.key), startedBy, idempotency, true,
			func([]givenObject) (apiRunRequest, error) { return req, nil })
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
		c, ok := apiKeyUser(w, r, db, signIn)
		if !ok {
			return
		}
		id, ok := flowID(w, r)
		if !ok {
			return
		}
		// A run of another project than the key's isn't there for it.
		var flow, project int64
		err := db.QueryRowContext(r.Context(), "SELECT flow_id, project_id FROM runs WHERE id = ?", id).Scan(&flow, &project)
		if errors.Is(err, sql.ErrNoRows) || err == nil && project != c.project {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if !apiFlowAccess(w, r, db, c, flow, accessReview) {
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
