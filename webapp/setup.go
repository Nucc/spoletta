package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"gopkg.in/yaml.v3"
)

// A setup is everything built in the app, written out as a YAML file to keep
// under version control, edit, or have an LLM write, and read back in: the
// MCP servers, the skills and commands, the structs, the procedures and the
// flows, with every board, process, object, link and schedule on them. What runs leave behind, runs,
// versions, history and what persistent objects stored, stays out of it, as
// do MCP servers' sign-ins. SETUP.md describes the format.
//
// Everything in the file refers to everything else by name rather than by
// id, so a file works in any app, not only the one it came from. Where two
// processes or objects of a flow, or two states of an object, share a name,
// each but the first gets a key of its own to be referred to by instead.
//
// Importing a file creates the flows it has that the app doesn't, and makes
// over the ones it has by name to match the file; flows it doesn't mention
// stay as they are. A flow made over keeps the processes, objects, states
// and attributes that match the file's by name, ids and all, so its runs and
// what its persistent objects stored still refer to them. Its board is saved
// as a version first, and the import is a step of its history, so it can be
// undone. A file is imported whole or not at all.

// The format's version, which a file states first.
const setupVersion = 1

type setupFile struct {
	Spoletta   int              `yaml:"spoletta"`
	MCPServers []setupMCPServer `yaml:"mcpServers,omitempty"`
	Skills     []setupSkill     `yaml:"skills,omitempty"`
	Commands   []setupSkill     `yaml:"commands,omitempty"`
	// Each struct is its object, which is named as the struct is.
	Structs    []setupObject `yaml:"structs,omitempty"`
	Procedures []setupFlow   `yaml:"procedures,omitempty"`
	Flows      []setupFlow   `yaml:"flows,omitempty"`
}

type setupMCPServer struct {
	Name        string `yaml:"name"`
	URL         string `yaml:"url,omitempty"`
	Description string `yaml:"description,omitempty"`
	// oauth or token; oauth when absent, but for a connector.
	Auth string `yaml:"auth,omitempty"`
	// The built-in connector it is, if any; its URL is then its service's
	// API, the connector's own when absent.
	Connector string `yaml:"connector,omitempty"`
}

// A skill or command, whole; see skills.go. Who it belongs to stays out,
// as a flow's owner does.
type setupSkill struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// A command's: the arguments it takes, as the menu shows them.
	Arguments string `yaml:"arguments,omitempty"`
	Content   string `yaml:"content"`
}

type setupFlow struct {
	Name string `yaml:"name"`
	// Its tags; see tags.go.
	Tags         []string          `yaml:"tags,omitempty"`
	Repositories []setupRepository `yaml:"repositories,omitempty"`
	BranchPrefix string            `yaml:"branchPrefix,omitempty"`
	Schedules    []setupSchedule   `yaml:"schedules,omitempty"`
	setupBoard   `yaml:",inline"`
}

type setupRepository struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Base string `yaml:"base,omitempty"`
}

type setupSchedule struct {
	Cron     string `yaml:"cron"`
	Timezone string `yaml:"timezone"`
	// Enabled when absent.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// A flow's top board, or a flow process's.
type setupBoard struct {
	Start     *setupStart    `yaml:"start,omitempty"`
	End       *setupPoint    `yaml:"end,omitempty"`
	Processes []setupProcess `yaml:"processes,omitempty"`
	Objects   []setupObject  `yaml:"objects,omitempty"`
}

type setupStart struct {
	// The process the board starts with.
	Next  string   `yaml:"next,omitempty"`
	X     *float64 `yaml:"x,omitempty"`
	Y     *float64 `yaml:"y,omitempty"`
	Route any      `yaml:"route,omitempty"`
}

type setupPoint struct {
	X float64 `yaml:"x"`
	Y float64 `yaml:"y"`
}

type setupProcess struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key,omitempty"`
	// agent when absent.
	Kind string `yaml:"kind,omitempty"`
	// The procedure a subflow process runs.
	Runs       string   `yaml:"runs,omitempty"`
	Model      string   `yaml:"model,omitempty"`
	Thinking   string   `yaml:"thinking,omitempty"`
	MCPServers []string `yaml:"mcpServers,omitempty"`
	// The tools it keeps to of its MCP servers, by server name.
	MCPTools     map[string][]string `yaml:"mcpTools,omitempty"`
	Skills       []string            `yaml:"skills,omitempty"`
	Repositories *[]string           `yaml:"repositories,omitempty"`
	Connector    *setupConnector     `yaml:"connector,omitempty"`
	Rounds       *int                `yaml:"rounds,omitempty"`
	Loops        *int                `yaml:"loops,omitempty"`
	Timeout      *int                `yaml:"timeout,omitempty"`
	Retries      *int                `yaml:"retries,omitempty"`
	Quorum       *int                `yaml:"quorum,omitempty"`
	DescribeFlow bool                `yaml:"describeFlow,omitempty"`
	Recording    bool                `yaml:"recording,omitempty"`
	Prompt       string              `yaml:"prompt,omitempty"`
	Script       string              `yaml:"script,omitempty"`
	Takes        []setupLink         `yaml:"takes,omitempty"`
	Yields       []setupLink         `yaml:"yields,omitempty"`
	// The processes that run after it, and those that run instead once it fails.
	Next      []string `yaml:"next,omitempty"`
	OnFailure []string `yaml:"onFailure,omitempty"`
	// What it is tried on; see trials.go. Absent, it keeps the samples it
	// has; empty, it has none.
	Samples *[]setupSample `yaml:"samples,omitempty"`
	X       *float64       `yaml:"x,omitempty"`
	Y       *float64       `yaml:"y,omitempty"`
	Width   *float64       `yaml:"width,omitempty"`
	Board   *setupBoard    `yaml:"board,omitempty"`
}

// Inputs a process is tried on, under a name of their own.
type setupSample struct {
	Name   string             `yaml:"name"`
	Inputs []setupSampleInput `yaml:"inputs,omitempty"`
}

// What a sample gives an object the process takes: what it holds, and the
// state it is in, if any.
type setupSampleInput struct {
	Object  string `yaml:"object"`
	State   string `yaml:"state,omitempty"`
	Content string `yaml:"content"`
}

// What a connector process carries out: an operation of a connector's
// server, by name, with its arguments.
type setupConnector struct {
	Server    string            `yaml:"server"`
	Operation string            `yaml:"operation"`
	Args      map[string]string `yaml:"args,omitempty"`
}

// A link from a process to an object it takes or yields. Written as just
// the object when it says nothing else.
type setupLink struct {
	Object string `yaml:"object"`
	State  string `yaml:"state,omitempty"`
	// The states a human or an agent decides between.
	States    []string `yaml:"states,omitempty"`
	Role      string   `yaml:"role,omitempty"`
	Condition bool     `yaml:"condition,omitempty"`
	// Which stored records it takes; see queries.go.
	Query *linkQuery  `yaml:"query,omitempty"`
	At    *setupPoint `yaml:"at,omitempty"`
	Route any         `yaml:"route,omitempty"`
}

type setupObject struct {
	Name       string `yaml:"name"`
	Key        string `yaml:"key,omitempty"`
	Persistent bool   `yaml:"persistent,omitempty"`
	// Only a struct's object; see shared.go. Its tags are the struct's.
	Shared bool     `yaml:"shared,omitempty"`
	Tags   []string `yaml:"tags,omitempty"`
	Many   bool     `yaml:"many,omitempty"`
	Min    int      `yaml:"min,omitempty"`
	Max    *int     `yaml:"max,omitempty"`
	Human  bool     `yaml:"human,omitempty"`
	End    bool     `yaml:"end,omitempty"`
	X      *float64 `yaml:"x,omitempty"`
	Y      *float64 `yaml:"y,omitempty"`
	Width  *float64 `yaml:"width,omitempty"`
	// For a part: exhibition when absent, or aggregation.
	Relation    string       `yaml:"relation,omitempty"`
	Specializes string       `yaml:"specializes,omitempty"`
	CopyOf      *setupSource `yaml:"copyOf,omitempty"`
	// Written as just the name when it has no key or rule.
	States     []setupState     `yaml:"states,omitempty"`
	Attributes []setupAttribute `yaml:"attributes,omitempty"`
	Parts      []setupObject    `yaml:"parts,omitempty"`
}

// A struct's tags, given on its object; none for no object.
func (o *setupObject) tags() []string {
	if o == nil {
		return nil
	}
	return o.Tags
}

// The object a copy follows: a struct's, or one of a procedure's.
type setupSource struct {
	Flow   string `yaml:"flow"`
	Object string `yaml:"object,omitempty"`
}

type setupState struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key,omitempty"`
	Rule string `yaml:"rule,omitempty"`
}

type setupAttribute struct {
	Name       string `yaml:"name"`
	Definition string `yaml:"definition,omitempty"`
	Unique     bool   `yaml:"unique,omitempty"`
	File       bool   `yaml:"file,omitempty"`
}

func (l setupLink) MarshalYAML() (any, error) {
	if l.State == "" && l.States == nil && l.Role == "" && !l.Condition && l.Query == nil && l.At == nil && l.Route == nil {
		return l.Object, nil
	}
	type plain setupLink
	return plain(l), nil
}

func (l *setupLink) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*l = setupLink{Object: n.Value}
		return nil
	}
	type plain setupLink
	return decodeKnown(n, (*plain)(l))
}

func (s setupState) MarshalYAML() (any, error) {
	if s.Key == "" && s.Rule == "" {
		return s.Name, nil
	}
	type plain setupState
	return plain(s), nil
}

func (s *setupState) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = setupState{Name: n.Value}
		return nil
	}
	type plain setupState
	return decodeKnown(n, (*plain)(s))
}

// Leaves out what the processes on every board are tried on; imported
// so, each keeps the samples it has.
func (f *setupFile) dropSamples() {
	var drop func(b *setupBoard)
	drop = func(b *setupBoard) {
		for i := range b.Processes {
			b.Processes[i].Samples = nil
			if b.Processes[i].Board != nil {
				drop(b.Processes[i].Board)
			}
		}
	}
	for _, flows := range [][]setupFlow{f.Procedures, f.Flows} {
		for i := range flows {
			drop(&flows[i].setupBoard)
		}
	}
}

// Decodes n into v, turning down fields v doesn't have, as the decoder
// does everywhere else; a node's own Decode doesn't.
func decodeKnown(n *yaml.Node, v any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	return d.Decode(v)
}

// What is wrong with a setup file; the import says so and changes nothing.
type setupProblem struct{ msg string }

func (p setupProblem) Error() string { return p.msg }

func problemf(format string, args ...any) error {
	return setupProblem{fmt.Sprintf(format, args...)}
}

// Gives each of names an identifier no other has: the name itself the first
// time, and then a key, the name with a number, for the ones after it. The
// keys are returned by index, "" where the name serves.
func setupKeys(names []string) []string {
	taken := map[string]bool{}
	for _, n := range names {
		taken[n] = true
	}
	seen := map[string]bool{}
	keys := make([]string, len(names))
	for i, n := range names {
		if !seen[n] {
			seen[n] = true
			continue
		}
		for j := 2; ; j++ {
			k := n + " (" + strconv.Itoa(j) + ")"
			if !taken[k] {
				taken[k] = true
				keys[i] = k
				break
			}
		}
	}
	return keys
}

// The identifier of something named name with key, as references give it.
func ident(name, key string) string {
	if key != "" {
		return key
	}
	return name
}

// A route as the file has it: decoded JSON, nil for the default route.
func setupRoute(r json.RawMessage) (any, error) {
	if len(r) == 0 || string(r) == "null" {
		return nil, nil
	}
	var v any
	err := json.Unmarshal(r, &v)
	return v, err
}

// A route from the file as stored, or why it can't be.
func storedSetupRoute(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// How large a setup file can be.
const maxSetupBytes = 16 << 20

func registerSetupRoutes(mux *http.ServeMux, db *sql.DB) {
	// The setup file is the whole setup, everyone's flows, so with sign-in
	// only admins take it out and bring it in.
	mux.HandleFunc("GET /api/setup", func(w http.ResponseWriter, r *http.Request) {
		if requestUser(r) != nil && refuseNonAdmin(w, r) {
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		f, err := exportSetup(ctx, tx, 0)
		var p setupProblem
		if errors.As(err, &p) {
			http.Error(w, p.msg, http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		body, err := yaml.Marshal(f)
		if err != nil {
			serverError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="spoletta-setup.yaml"`)
		w.Write(body)
	})

	// One flow alone as a setup file, as its assistant is told it, for
	// people to read beside the board. Whoever sees the flow may: the
	// structs and procedures it uses are only named, not written out.
	mux.HandleFunc("GET /api/flows/{id}/setup", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		af, err := loadAssistantFlow(r.Context(), db, id)
		var p setupProblem
		switch {
		case errors.Is(err, sql.ErrNoRows) || errors.Is(err, errNotFound):
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		case errors.As(err, &p):
			http.Error(w, p.msg, http.StatusConflict)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"name": af.name, "text": af.setup})
	})

	// Takes the file as the body, YAML or JSON, and says which flows it
	// created and which it made over.
	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) {
		if requestUser(r) != nil && refuseNonAdmin(w, r) {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSetupBytes))
		if err != nil {
			http.Error(w, "the file is too large", http.StatusRequestEntityTooLarge)
			return
		}
		f, err := parseSetup(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		res, err := importSetup(ctx, tx, f, saving{by: requestUserID(r), reason: savedForImport})
		var p setupProblem
		if errors.As(err, &p) {
			http.Error(w, p.msg, http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// Reads a setup file, turning down fields the format doesn't have, so a
// misspelt one doesn't quietly go missing.
func parseSetup(body []byte) (setupFile, error) {
	var f setupFile
	d := yaml.NewDecoder(bytes.NewReader(body))
	d.KnownFields(true)
	if err := d.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return f, errors.New("the file is empty")
		}
		return f, fmt.Errorf("invalid setup file: %v", err)
	}
	if f.Spoletta != setupVersion {
		return f, fmt.Errorf("a setup file starts with spoletta: %d, the version of its format", setupVersion)
	}
	return f, nil
}
