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
// MCP servers, the structs, the procedures and the flows, with every board,
// process, object, link and schedule on them. What runs leave behind, runs,
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
	// Each struct is its object, which is named as the struct is.
	Structs    []setupObject `yaml:"structs,omitempty"`
	Procedures []setupFlow   `yaml:"procedures,omitempty"`
	Flows      []setupFlow   `yaml:"flows,omitempty"`
}

type setupMCPServer struct {
	Name        string `yaml:"name"`
	URL         string `yaml:"url"`
	Description string `yaml:"description,omitempty"`
}

type setupFlow struct {
	Name         string            `yaml:"name"`
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
	Runs         string       `yaml:"runs,omitempty"`
	Model        string       `yaml:"model,omitempty"`
	Thinking     string       `yaml:"thinking,omitempty"`
	MCPServers   []string     `yaml:"mcpServers,omitempty"`
	Repositories *[]string    `yaml:"repositories,omitempty"`
	Sentry       *setupSentry `yaml:"sentry,omitempty"`
	Rounds       *int         `yaml:"rounds,omitempty"`
	Timeout      *int         `yaml:"timeout,omitempty"`
	Retries      *int         `yaml:"retries,omitempty"`
	Quorum       *int         `yaml:"quorum,omitempty"`
	DescribeFlow bool         `yaml:"describeFlow,omitempty"`
	Recording    bool         `yaml:"recording,omitempty"`
	Prompt       string       `yaml:"prompt,omitempty"`
	Script       string       `yaml:"script,omitempty"`
	Takes        []setupLink  `yaml:"takes,omitempty"`
	Yields       []setupLink  `yaml:"yields,omitempty"`
	// The processes that run after it, and those that run instead once it fails.
	Next      []string    `yaml:"next,omitempty"`
	OnFailure []string    `yaml:"onFailure,omitempty"`
	X         *float64    `yaml:"x,omitempty"`
	Y         *float64    `yaml:"y,omitempty"`
	Width     *float64    `yaml:"width,omitempty"`
	Board     *setupBoard `yaml:"board,omitempty"`
}

type setupSentry struct {
	Organization string `yaml:"organization,omitempty"`
	Project      string `yaml:"project,omitempty"`
	Environment  string `yaml:"environment,omitempty"`
	Query        string `yaml:"query,omitempty"`
	Sort         string `yaml:"sort,omitempty"`
	Limit        int    `yaml:"limit,omitempty"`
}

// A link from a process to an object it takes or yields. Written as just
// the object when it says nothing else.
type setupLink struct {
	Object string `yaml:"object"`
	State  string `yaml:"state,omitempty"`
	// The states a human or an agent decides between.
	States    []string    `yaml:"states,omitempty"`
	Role      string      `yaml:"role,omitempty"`
	Condition bool        `yaml:"condition,omitempty"`
	At        *setupPoint `yaml:"at,omitempty"`
	Route     any         `yaml:"route,omitempty"`
}

type setupObject struct {
	Name       string   `yaml:"name"`
	Key        string   `yaml:"key,omitempty"`
	Persistent bool     `yaml:"persistent,omitempty"`
	Many       bool     `yaml:"many,omitempty"`
	Min        int      `yaml:"min,omitempty"`
	Max        *int     `yaml:"max,omitempty"`
	Human      bool     `yaml:"human,omitempty"`
	End        bool     `yaml:"end,omitempty"`
	X          *float64 `yaml:"x,omitempty"`
	Y          *float64 `yaml:"y,omitempty"`
	Width      *float64 `yaml:"width,omitempty"`
	// For a part: exhibition when absent, or aggregation.
	Relation    string       `yaml:"relation,omitempty"`
	Specializes string       `yaml:"specializes,omitempty"`
	CopyOf      *setupSource `yaml:"copyOf,omitempty"`
	// Written as just the name when it has no key or rule.
	States     []setupState     `yaml:"states,omitempty"`
	Attributes []setupAttribute `yaml:"attributes,omitempty"`
	Parts      []setupObject    `yaml:"parts,omitempty"`
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
	if l.State == "" && l.States == nil && l.Role == "" && !l.Condition && l.At == nil && l.Route == nil {
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
	mux.HandleFunc("GET /api/setup", func(w http.ResponseWriter, r *http.Request) {
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

	// Takes the file as the body, YAML or JSON, and says which flows it
	// created and which it made over.
	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) {
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
		res, err := importSetup(ctx, tx, f)
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
