package main

// Templates: ready flows to start from, so a new person has something to
// copy rather than an empty list. Each is a setup file kept in the webapp
// (templates/*.yaml, embedded), with a title and a one-line description, and
// using one makes it a new flow of whoever picked it, imported through
// setup_import.go with a version saved as made from the template.
//
// A template holds one flow and nothing else: no MCP servers, skills,
// commands, structs or procedures, since importing those would make over
// what the app already has of the same name, which is everyone's and only
// admins change. For the same reason the flow is named apart from every
// flow there is, seen or not. What it needs of the app is worked out from
// it: the repositories its processes work on, which its flow then lacks
// until someone adds them in its settings; the connectors its connector
// processes use, named in the file by the connector, such as slack, and
// given the first installed one of that connector, or none while there is
// none, keeping the operation and its arguments; and the roles its people
// name. What a flow made from one still lacks shows as its problems (see
// problems.go). Its schedules come off, so nothing runs before someone
// says so.

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed templates/*.yaml
var templateFiles embed.FS

// A template as its file has it: the setup file under setup.
type templateFile struct {
	Title       string    `yaml:"title"`
	Description string    `yaml:"description"`
	Setup       yaml.Node `yaml:"setup"`
}

// A template as the gallery shows it.
type flowTemplate struct {
	// Its file's name, without the number ordering it and .yaml.
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// The name the flow takes, unless taken.
	Flow string `json:"flow"`
	// The processes of its top board, in the file's order.
	Steps []templateStep `json:"steps"`
	Needs []templateNeed `json:"needs"`
	setup []byte
}

type templateStep struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

const (
	needRepository = "repository"
	needConnector  = "connector"
	needRole       = "role"
)

// Something a template needs of the app: a repository, by the name its
// processes pick it by; a connector, by its name, with its title; or a
// role. Whether the app has it is Met: a connector installed, a role
// someone holds; a repository never is, being the flow's own.
type templateNeed struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	Met   bool   `json:"met"`
}

var loadedTemplates = sync.OnceValues(readTemplates)

// Reads every template file, in the order their names give.
func readTemplates() ([]flowTemplate, error) {
	entries, err := templateFiles.ReadDir("templates")
	if err != nil {
		return nil, err
	}
	var out []flowTemplate
	for _, e := range entries {
		body, err := templateFiles.ReadFile(path.Join("templates", e.Name()))
		if err != nil {
			return nil, err
		}
		t, err := parseTemplate(e.Name(), body)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", e.Name(), err)
		}
		out = append(out, t)
	}
	return out, nil
}

// Reads a template's file, saying what is wrong with it.
func parseTemplate(name string, body []byte) (flowTemplate, error) {
	var tf templateFile
	d := yaml.NewDecoder(strings.NewReader(string(body)))
	d.KnownFields(true)
	if err := d.Decode(&tf); err != nil {
		return flowTemplate{}, err
	}
	setup, err := yaml.Marshal(&tf.Setup)
	if err != nil {
		return flowTemplate{}, err
	}
	f, err := parseSetup(setup)
	if err != nil {
		return flowTemplate{}, err
	}
	id := strings.TrimSuffix(name, ".yaml")
	if i := strings.IndexByte(id, '-'); i > 0 && strings.Trim(id[:i], "0123456789") == "" {
		id = id[i+1:]
	}
	t := flowTemplate{ID: id, Title: strings.TrimSpace(tf.Title), Description: strings.TrimSpace(tf.Description), setup: setup}
	switch {
	case t.Title == "" || t.Description == "":
		return t, errors.New("a template has a title and a description")
	case len(f.Flows) != 1 || len(f.Procedures) > 0 || len(f.Structs) > 0 || len(f.MCPServers) > 0 || len(f.Skills) > 0 || len(f.Commands) > 0:
		return t, errors.New("a template holds one flow and nothing else")
	}
	fl := f.Flows[0]
	if len(fl.Repositories) > 0 {
		return t, errors.New("a template's flow has no repositories of its own; its processes name those they work on")
	}
	t.Flow = fl.Name
	t.Steps = []templateStep{}
	for _, p := range fl.Processes {
		kind := p.Kind
		if kind == "" {
			kind = kindAgent
		}
		t.Steps = append(t.Steps, templateStep{Name: p.Name, Kind: kind})
	}
	t.Needs = []templateNeed{}
	need := func(n templateNeed) {
		if !slices.ContainsFunc(t.Needs, func(m templateNeed) bool { return m.Kind == n.Kind && m.Name == n.Name }) {
			t.Needs = append(t.Needs, n)
		}
	}
	var walk func(b *setupBoard) error
	walk = func(b *setupBoard) error {
		for _, p := range b.Processes {
			if p.Repositories != nil {
				for _, r := range *p.Repositories {
					need(templateNeed{Kind: needRepository, Name: r})
				}
			}
			if c := p.Connector; c != nil {
				k := findConnector(c.Server)
				if k == nil {
					return fmt.Errorf("%s: a template names a connector process's connector by its name, such as slack", quote(p.Name))
				}
				if k.operation(c.Operation) == nil {
					return fmt.Errorf("%s: %s has no operation %s", quote(p.Name), k.Title, quote(c.Operation))
				}
				need(templateNeed{Kind: needConnector, Name: k.Name, Title: k.Title})
			}
			if p.Board != nil {
				if err := walk(p.Board); err != nil {
					return err
				}
			}
		}
		for _, o := range b.Objects {
			if o.Human {
				need(templateNeed{Kind: needRole, Name: o.Name})
			}
		}
		return nil
	}
	return t, walk(&fl.setupBoard)
}

// The template as the app stands: which of its needs it meets.
func (t flowTemplate) forApp(ctx context.Context, db *sql.DB, signIn bool) (flowTemplate, error) {
	needs := make([]templateNeed, len(t.Needs))
	for i, n := range t.Needs {
		var err error
		switch n.Kind {
		case needConnector:
			err = db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM mcp_servers WHERE connector = ?)", n.Name).Scan(&n.Met)
		case needRole:
			// Without sign-in, anyone decides.
			n.Met = !signIn
			if signIn {
				err = db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles ur JOIN users u ON u.id = ur.user_id
					WHERE u.removed_at IS NULL AND LOWER(TRIM(ur.name)) = ?)`, roleKey(n.Name)).Scan(&n.Met)
			}
		}
		if err != nil {
			return t, err
		}
		needs[i] = n
	}
	t.Needs = needs
	return t, nil
}

func findTemplate(id string) (flowTemplate, bool, error) {
	ts, err := loadedTemplates()
	if err != nil {
		return flowTemplate{}, false, err
	}
	i := slices.IndexFunc(ts, func(t flowTemplate) bool { return t.ID == id })
	if i < 0 {
		return flowTemplate{}, false, nil
	}
	return ts[i], true, nil
}

// The first name of the form name, name 2, name 3… no flow has.
func freeFlowName(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	taken := map[string]bool{}
	err := scanAll(ctx, tx, "SELECT name FROM flows WHERE name = ? OR name LIKE ?", func(rows *sql.Rows) error {
		var n string
		err := rows.Scan(&n)
		taken[n] = true
		return err
	}, name, escapeLike(name)+" %")
	if err != nil {
		return "", err
	}
	free := name
	for i := 2; taken[free]; i++ {
		free = fmt.Sprintf("%s %d", name, i)
	}
	return free, nil
}

// s with LIKE's wildcards taken as themselves.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// The template's setup file as it is imported into the app: its flow
// named name, each connector process given the first installed connector
// of the one it names, or none while none is, and its schedules off.
func (t flowTemplate) setupFor(ctx context.Context, tx *sql.Tx, name string) (setupFile, error) {
	f, err := parseSetup(t.setup)
	if err != nil {
		return f, err
	}
	fl := &f.Flows[0]
	fl.Name = name
	off := false
	for i := range fl.Schedules {
		fl.Schedules[i].Enabled = &off
	}
	installed := map[string]string{}
	var walk func(b *setupBoard) error
	walk = func(b *setupBoard) error {
		for i := range b.Processes {
			p := &b.Processes[i]
			if c := p.Connector; c != nil {
				server, ok := installed[c.Server]
				if !ok {
					err := tx.QueryRowContext(ctx, "SELECT name FROM mcp_servers WHERE connector = ? ORDER BY id LIMIT 1", c.Server).Scan(&server)
					if err != nil && !errors.Is(err, sql.ErrNoRows) {
						return err
					}
					installed[c.Server] = server
				}
				c.Server = server
			}
			if p.Board != nil {
				if err := walk(p.Board); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return f, walk(&fl.setupBoard)
}

// Why a version is saved as a flow is made from a template.
const savedForTemplate = "template"

func registerTemplateRoutes(mux *http.ServeMux, db *sql.DB) {
	// The templates, and which of their needs the app meets.
	mux.HandleFunc("GET /api/templates", func(w http.ResponseWriter, r *http.Request) {
		ts, err := loadedTemplates()
		if err != nil {
			serverError(w, err)
			return
		}
		out := []flowTemplate{}
		for _, t := range ts {
			t, err := t.forApp(r.Context(), db, requestUser(r) != nil)
			if err != nil {
				serverError(w, err)
				return
			}
			out = append(out, t)
		}
		writeJSON(w, http.StatusOK, out)
	})

	// Makes a new flow of the template, named as it is unless the body
	// names it, belonging to whoever the body says as a new flow does
	// (see newOwner), and answers it.
	mux.HandleFunc("POST /api/templates/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, ok, err := findTemplate(r.PathValue("id"))
		if err != nil {
			serverError(w, err)
			return
		}
		if !ok {
			http.Error(w, "template not found", http.StatusNotFound)
			return
		}
		var body struct {
			Name  string     `json:"name"`
			Owner *flowOwner `json:"owner"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			name = t.Flow
		}
		ctx := r.Context()
		ownerUser, ownerTeam, msg, err := newOwner(ctx, db, requestUser(r), body.Owner)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		if name, err = freeFlowName(ctx, tx, name); err != nil {
			serverError(w, err)
			return
		}
		f, err := t.setupFor(ctx, tx, name)
		if err != nil {
			serverError(w, err)
			return
		}
		res, err := importSetup(ctx, tx, f, saving{by: requestUserID(r), reason: savedForImport})
		var p setupProblem
		if errors.As(err, &p) {
			http.Error(w, p.msg, http.StatusConflict)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		// A flow of that name made meanwhile would have been made over.
		if len(res.Flows) != 1 || !res.Flows[0].Created {
			http.Error(w, "a flow named "+quote(name)+" was made meanwhile; try again", http.StatusConflict)
			return
		}
		id := res.Flows[0].ID
		if _, err := tx.ExecContext(ctx, "UPDATE flows SET owner_user_id = ?, owner_team_id = ? WHERE id = ?", ownerUser, ownerTeam, id); err != nil {
			serverError(w, err)
			return
		}
		note := "Made from the template " + quote(t.Title) + "."
		if _, err := saveVersion(ctx, tx, id, false, saving{by: requestUserID(r), note: note, reason: savedForTemplate}); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		fl, err := getFlow(ctx, db, id)
		if err == nil {
			fl, err = describeFlow(ctx, db, r, fl)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		fl.Access = accessEdit
		writeJSON(w, http.StatusCreated, fl)
	})
}
