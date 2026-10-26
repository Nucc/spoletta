package main

// A board's problems: what is wrong with it, worked out before it runs from
// the rules the engine and the import keep to, so a flow that can start but
// is broken shows so on the board rather than when it is run. Each problem
// names the process or object it is about and says why in a sentence.
//
// An error is something a run of the board can't get past as it is drawn:
// nothing for the start to begin with, a subflow process running no flow
// or one that is gone, a connector process with no connector or operation,
// or an operation missing an argument it needs. Run refuses a board with
// errors, listing them. A warning is what a run may well get past, or what
// depends on what is set up outside the board and can change before the
// step runs, which the step itself checks: an object nothing generates, a
// process nothing leads to, an agent with no model to use, a review whose
// role nobody holds, a reminder that never comes, a repository the flow
// doesn't have, and skills, MCP servers, credentials and tokens the flow
// may not or can't use. Schedules, the API and runs on records run the
// version published without asking (see publish.go), so the board also says
// whether that version has errors.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	problemError   = "error"
	problemWarning = "warning"
)

// What is wrong with a board, and where.
type problem struct {
	// problemError or problemWarning.
	Level string `json:"level"`
	// Which rule it breaks, such as "unreached", for the board to tell
	// problems apart by.
	Rule    string `json:"rule"`
	Message string `json:"message"`
	// The process or object it is about, if any.
	Process *int64 `json:"process,omitempty"`
	Object  *int64 `json:"object,omitempty"`
	// The flow process whose board that is on; none for the top board.
	Board *int64 `json:"board,omitempty"`
}

// What the rules need to know of the app beyond the board.
type problemFacts struct {
	// Whether the flow is a procedure, whose top board's objects the flows
	// running it give.
	module bool
	// Whether people sign in, so roles say who decides reviews.
	signIn bool
	// The MCP servers, the installed connectors among them, by id.
	servers map[int64]serverFacts
	// The flows there are, by id: what subflow processes run.
	flows map[int64]bool
	// Skills and credentials processes picked, by id; those not there are
	// gone.
	skills      map[int64]pickFacts
	credentials map[int64]pickFacts
	// The roles someone holds, by roleKey.
	roles map[string]bool
	// The model a process without one of its own uses, and whether that is
	// known: not when the worker can't be asked.
	defaultModel string
	modelKnown   bool
	// The objects copies follow that are still there.
	sources map[int64]bool
}

type serverFacts struct {
	name, auth, connector string
	// For a server taking a token, a connector's included, whether an admin
	// set it; for one people sign in to, whether whoever asks connected it.
	connected bool
}

// A skill or credential picked: its name, whether the flow may use it, and
// for a credential whether it has its secret.
type pickFacts struct {
	name           string
	usable, secret bool
}

// The board's problems, errors first, each kind in the order of the board.
func boardProblems(s snapshot, f problemFacts) []problem {
	c := newProblemCheck(s, f)
	c.start()
	c.processes()
	c.objects()
	c.unreached()
	out := []problem{}
	for _, level := range []string{problemError, problemWarning} {
		for _, p := range c.found {
			if p.Level == level {
				out = append(out, p)
			}
		}
	}
	return out
}

// The board as the rules read it.
type problemCheck struct {
	s snapshot
	f problemFacts
	// Processes and objects by id.
	procs map[int64]process
	objs  map[int64]snapshotObject
	// Each process's links, and the states each link lists.
	links      map[int64][]snapshotLink
	linkStates map[int64][]int64
	// Whether something on the board generates the object, as the engine
	// counts it, and the processes taking it other than by a query.
	generated map[int64]bool
	takers    map[int64][]int64
	// The connections out of each process, and into it.
	out, in map[int64][]connection
	found   []problem
}

func newProblemCheck(s snapshot, f problemFacts) *problemCheck {
	c := &problemCheck{s: s, f: f, procs: map[int64]process{}, objs: map[int64]snapshotObject{}, links: map[int64][]snapshotLink{},
		linkStates: map[int64][]int64{}, generated: map[int64]bool{}, takers: map[int64][]int64{},
		out: map[int64][]connection{}, in: map[int64][]connection{}}
	for _, p := range s.Processes {
		c.procs[p.ID] = p
	}
	for _, o := range s.Objects {
		c.objs[o.ID] = o
	}
	for _, ls := range s.LinkStates {
		c.linkStates[ls.Link] = append(c.linkStates[ls.Link], ls.State)
	}
	for _, l := range s.Links {
		if _, ok := c.procs[l.Process]; !ok {
			continue
		}
		c.links[l.Process] = append(c.links[l.Process], l)
		if l.Direction == directionInput && l.Query == nil && !slices.Contains(c.takers[l.Object], l.Process) {
			c.takers[l.Object] = append(c.takers[l.Object], l.Process)
		}
	}
	for _, l := range s.Links {
		p, ok := c.procs[l.Process]
		if !ok || l.Direction != directionOutput {
			continue
		}
		states := c.linkStates[l.ID]
		if generatesOutput(p.Kind, l.State != 0 || len(states) > 0, len(states) > 0, c.takes(p.ID, l.Object)) {
			c.generated[l.Object] = true
		}
	}
	for _, cn := range s.Connections {
		c.out[cn.Source] = append(c.out[cn.Source], cn)
		c.in[cn.Target] = append(c.in[cn.Target], cn)
	}
	return c
}

// Whether a process of the kind generates the object an output link of its
// names: a human generates only what they fill in, not the objects they
// decide the state of nor those they review without states to decide
// between, and an agent doesn't generate what it reviews. inState says the
// link gives the object a state or states to pick from, decides that an
// agent picks among them, and takes that the process takes the object too.
func generatesOutput(kind string, inState, decides, takes bool) bool {
	switch {
	case kind == kindHuman && (inState || takes):
		return false
	case decides && takes:
		return false
	}
	return true
}

// Whether the process takes the object, by a link other than a query.
func (c *problemCheck) takes(process, object int64) bool {
	return slices.ContainsFunc(c.links[process], func(l snapshotLink) bool {
		return l.Object == object && l.Direction == directionInput && l.Query == nil
	})
}

func (c *problemCheck) add(level, rule string, process, object *int64, format string, args ...any) {
	p := problem{Level: level, Rule: rule, Message: fmt.Sprintf(format, args...), Process: process, Object: object}
	switch {
	case process != nil:
		p.Board = c.procs[*process].Parent
	case object != nil:
		p.Board = c.objs[*object].Parent
	}
	c.found = append(c.found, p)
}

func (c *problemCheck) onProcess(level, rule string, p process, format string, args ...any) {
	id := p.ID
	c.add(level, rule, &id, nil, format, args...)
}

func (c *problemCheck) onObject(level, rule string, o snapshotObject, format string, args ...any) {
	id := o.ID
	c.add(level, rule, nil, &id, format, args...)
}

// Whether the top board's start leads to a process on it.
func (c *problemCheck) started() bool {
	t := c.s.Start.Target
	if t == nil {
		return false
	}
	p, ok := c.procs[*t]
	return ok && p.Parent == nil
}

// A run begins at the top board's start; without it, Run runs nothing.
// A procedure is run with what the flow running it gives, which the
// processes taking it run on, so it only warns.
func (c *problemCheck) start() {
	if c.started() {
		return
	}
	top := slices.ContainsFunc(c.s.Processes, func(p process) bool { return p.Parent == nil })
	switch {
	case !top && !c.f.module:
		c.add(problemError, "start", nil, nil, "The board has no process yet: add one, such as an agent from the building blocks, and connect the start to it.")
	case !c.f.module:
		c.add(problemError, "start", nil, nil, "Nothing runs: connect the start to the process a run begins with.")
	case top:
		c.add(problemWarning, "start", nil, nil, "The procedure's start leads nowhere, so run on its own it does nothing, and run by another flow only its processes taking what that flow gives run.")
	}
}

func (c *problemCheck) processes() {
	for _, p := range c.s.Processes {
		switch p.Kind {
		case kindSubflow:
			c.subflow(p)
		case kindConnector:
			c.connector(p)
		case kindHuman:
			c.human(p)
		case kindAgent:
			c.agent(p)
		}
		if p.Kind == kindAgent || p.Kind == kindScript {
			c.picks(p)
		}
	}
}

func (c *problemCheck) subflow(p process) {
	switch flow := p.Settings.Flow; {
	case flow == nil:
		c.onProcess(problemError, "subflow", p, "%s runs no flow: pick the procedure it runs in its panel.", quote(p.Name))
	case !c.f.flows[*flow]:
		c.onProcess(problemError, "subflow", p, "The flow %s runs was deleted: pick another in its panel.", quote(p.Name))
	}
}

// A connector process carries out its one operation, so without the
// connector, the operation or an argument the operation needs, it fails
// whenever it runs.
func (c *problemCheck) connector(p process) {
	cs := p.Settings.Connector
	if cs == nil || cs.Server == 0 {
		msg := fmt.Sprintf("%s uses no connector: pick one in its panel.", quote(p.Name))
		if k := operationConnector(cs); k != nil && !c.installed(k.Name) {
			msg = fmt.Sprintf("%s needs the %s connector, which isn't installed: an admin installs it in Settings, under Connectors, and then you pick it in its panel.", quote(p.Name), k.Title)
		}
		c.onProcess(problemError, "connector", p, "%s", msg)
		return
	}
	server, ok := c.f.servers[cs.Server]
	if !ok {
		c.onProcess(problemError, "connector", p, "The connector %s uses was removed: pick one again in its panel.", quote(p.Name))
		return
	}
	k := findConnector(server.connector)
	if k == nil {
		c.onProcess(problemError, "connector", p, "%s is an MCP server, which an agent process uses, not a connector: pick a connector for %s in its panel.", server.name, quote(p.Name))
		return
	}
	op := k.operation(cs.Operation)
	if op == nil {
		c.onProcess(problemError, "connector", p, "Pick what %s does with %s in its panel.", quote(p.Name), server.name)
		return
	}
	for _, param := range op.Params {
		if param.Required && strings.TrimSpace(cs.Args[param.Name]) == "" {
			c.onProcess(problemError, "connector", p, "%s's %s needs its %s: fill it in in its panel.", quote(p.Name), op.Title, param.Name)
		}
	}
	if !server.connected {
		c.onProcess(problemWarning, "token", p, "The connector %s has no token yet, so %s fails until an admin sets it in Settings, under MCP servers.", server.name, quote(p.Name))
	}
	names := c.inputNames(p)
	for _, name := range slices.Sorted(maps.Keys(cs.Args)) {
		for _, m := range placeholder.FindAllStringSubmatch(cs.Args[name], -1) {
			if ref := m[1]; !slices.ContainsFunc(names, func(n string) bool { return ref == n || strings.HasPrefix(ref, n+".") }) {
				c.onProcess(problemWarning, "argument", p, "%s's %s names %s, which %s doesn't take, so it fails once it runs.", quote(p.Name), name, quote(ref), quote(p.Name))
			}
		}
	}
}

// The connector an operation of a connector process with none picked
// belongs to, when only one has an operation of that name.
func operationConnector(cs *connectorSettings) *connector {
	if cs == nil || cs.Operation == "" {
		return nil
	}
	var found *connector
	for _, k := range connectors {
		if k.operation(cs.Operation) != nil {
			if found != nil {
				return nil
			}
			found = k
		}
	}
	return found
}

// Whether a server of the connector is installed.
func (c *problemCheck) installed(connector string) bool {
	for _, s := range c.f.servers {
		if s.connector == connector {
			return true
		}
	}
	return false
}

// The names of what a step of the process takes: what it links to, what
// a failure it handles says, and for the process a board's start leads
// to, what is given the board from outside.
func (c *problemCheck) inputNames(p process) []string {
	var names []string
	for _, l := range c.links[p.ID] {
		if l.Direction == directionInput {
			names = append(names, c.objs[l.Object].Name)
		}
	}
	for _, cn := range c.in[p.ID] {
		if cn.Kind == connectionFailure {
			names = append(names, "Failure of "+c.procs[cn.Source].Name)
		}
	}
	if p.Parent != nil && c.innerStart(*p.Parent) == p.ID {
		for _, l := range c.links[*p.Parent] {
			if l.Direction == directionInput {
				names = append(names, c.objs[l.Object].Name)
			}
		}
	}
	if p.Parent == nil && c.f.module && c.started() && *c.s.Start.Target == p.ID {
		for _, o := range c.s.Objects {
			if o.Parent == nil && o.Owner == nil {
				names = append(names, o.Name)
			}
		}
	}
	return names
}

// The process a flow process's board starts with, 0 for none.
func (c *problemCheck) innerStart(flowProcess int64) int64 {
	for _, b := range c.s.Boards {
		if b.Process == flowProcess && b.Start.Target != nil {
			return *b.Start.Target
		}
	}
	return 0
}

// A review is decided by those holding a role its people name, or admins
// when nobody does; and a reminder due at or after its time limit never
// goes.
func (c *problemCheck) human(p process) {
	s := p.Settings
	if s.RemindAfter != nil && s.WaitAtMost != nil && *s.RemindAfter >= *s.WaitAtMost {
		c.onProcess(problemWarning, "reminder", p, "%s reminds every %s but fails after %s, so nobody is ever reminded: make the reminder shorter.",
			quote(p.Name), durationText(*s.RemindAfter), durationText(*s.WaitAtMost))
	}
	if !c.f.signIn {
		return
	}
	var roles []string
	for _, l := range c.links[p.ID] {
		if o := c.objs[l.Object]; l.Direction == directionInput && o.Human && !slices.Contains(roles, strings.TrimSpace(o.Name)) {
			roles = append(roles, strings.TrimSpace(o.Name))
		}
	}
	if len(roles) == 0 || slices.ContainsFunc(roles, func(r string) bool { return c.f.roles[roleKey(r)] }) {
		return
	}
	who := quote(roles[0])
	if len(roles) > 1 {
		who = "any of " + quotedList(roles)
	}
	c.onProcess(problemWarning, "role", p, "Nobody holds the role %s, so only admins decide %s: an admin gives the role in Settings, under People.", who, quote(p.Name))
}

// An agent without a model of its own uses the default; without one, it
// has none to run on.
func (c *problemCheck) agent(p process) {
	if p.Model == "" && c.f.modelKnown && c.f.defaultModel == "" {
		c.onProcess(problemWarning, "model", p, "%s has no model, and there is no default model: pick one in its panel, or have an admin add a model provider in Settings.", quote(p.Name))
	}
	for _, id := range p.Settings.MCPServers {
		server, ok := c.f.servers[id]
		switch {
		case !ok:
			c.onProcess(problemWarning, "mcp", p, "An MCP server %s uses was removed, so it fails once it runs: pick its MCP servers again in its panel.", quote(p.Name))
		case server.connected:
		case server.auth == authToken:
			c.onProcess(problemWarning, "mcp", p, "The MCP server %s has no token yet, so %s fails until an admin sets it in Settings, under MCP servers.", server.name, quote(p.Name))
		default:
			c.onProcess(problemWarning, "mcp", p, "You haven't connected the MCP server %s, so %s fails in runs you start: connect it in Settings, under MCP servers.", server.name, quote(p.Name))
		}
	}
	for _, id := range p.Settings.Skills {
		skill, ok := c.f.skills[id]
		switch {
		case !ok:
			c.onProcess(problemWarning, "skill", p, "A skill %s uses was deleted, so it fails once it runs: pick its skills again in its panel.", quote(p.Name))
		case !skill.usable:
			c.onProcess(problemWarning, "skill", p, "The skill %s isn't one this flow may use, so %s fails once it runs: share it with the flow's owner, or pick another.", skill.name, quote(p.Name))
		}
	}
}

// What an agent or script process picked besides: the credentials it calls
// services with, and the repositories it works on.
func (c *problemCheck) picks(p process) {
	for _, id := range p.Settings.Credentials {
		cred, ok := c.f.credentials[id]
		switch {
		case !ok:
			c.onProcess(problemWarning, "credential", p, "A credential %s uses was removed, so it fails once it runs: pick its credentials again in its panel.", quote(p.Name))
		case !cred.usable:
			c.onProcess(problemWarning, "credential", p, "A credential %s uses isn't one this flow may use, so it fails once it runs: an admin gives it in Settings, under Credentials, or pick another.", quote(p.Name))
		case !cred.secret:
			c.onProcess(problemWarning, "credential", p, "The credential %s has no secret, so %s fails until an admin sets it in Settings, under Credentials.", cred.name, quote(p.Name))
		}
	}
	if p.Settings.Repositories == nil {
		return
	}
	var have []string
	if c.s.Settings != nil {
		for _, r := range c.s.Settings.Repositories {
			have = append(have, r.Name)
		}
	}
	for _, name := range *p.Settings.Repositories {
		if !slices.Contains(have, name) {
			c.onProcess(problemWarning, "repository", p, "%s works on the repository %s, which the flow doesn't have, so it gets no checkout of it: add it in the flow's settings, or pick its repositories again.", quote(p.Name), quote(name))
		}
	}
}

// An object a process takes that nothing generates exists from the start,
// but holds nothing: the engine gives it to none of its processes. Only a
// run over the API, or for a procedure the flow running it, gives what
// the top board's processes take.
func (c *problemCheck) objects() {
	reached, _ := c.reach(false)
	for _, o := range c.s.Objects {
		if o.Source != nil && !c.f.sources[*o.Source] {
			c.onObject(problemWarning, "copy", o, "The struct or procedure %s was copied from is gone, so it no longer follows it and keeps what it had.", quote(o.Name))
		}
		takers := c.takers[o.ID]
		if o.Human || o.Owner != nil || c.generated[o.ID] || len(takers) == 0 {
			continue
		}
		if o.Parent == nil && c.f.module {
			continue
		}
		names := make([]string, len(takers))
		for i, t := range takers {
			names[i] = c.procs[t].Name
		}
		who := quotedList(names)
		switch {
		case o.Parent != nil:
			c.onObject(problemWarning, "generated", o, "Nothing generates %s, so %s never gets it: have a process generate it, or a person fill it in.", quote(o.Name), who)
		case !slices.ContainsFunc(takers, func(t int64) bool { return reached[t] }):
			c.onObject(problemWarning, "generated", o, "Nothing generates %s, so a run started with Run or a schedule never runs %s; only a run over the API, which is given %s, does. Have a person fill it in first, or a process fetch it.",
				quote(o.Name), who, quote(o.Name))
		default:
			c.onObject(problemWarning, "generated", o, "Nothing generates %s, so a run started with Run or a schedule never gives it to %s; only a run over the API is given it. Have a person fill it in first, or a process fetch it.",
				quote(o.Name), who)
		}
	}
}

// A process no run gets to: not the start, nor a process connected to it
// or generating what it takes, nor what an API run or a flow running the
// procedure gives. Without a start, the start's error says it all.
func (c *problemCheck) unreached() {
	if !c.started() && !c.f.module {
		return
	}
	reached, given := c.reach(true)
	for _, p := range c.s.Processes {
		if !reached[p.ID] && !given[p.ID] {
			c.onProcess(problemWarning, "unreached", p, "Nothing leads to %s: the start doesn't, nor does any connection, and nothing generates what it waits for, so no run gets to it.", quote(p.Name))
		}
	}
}

// The processes a run gets to from the top board's start, as the engine
// walks it: along connections, but a human's next ones, and to the
// processes taking what those reached generate or decide on, a flow
// process leading to its board's start and the processes there taking
// what it takes. With inputs, also those a run gets to from what it is
// given: the top board's objects its processes take and nothing
// generates, given apart.
func (c *problemCheck) reach(inputs bool) (reached, given map[int64]bool) {
	walk := func(seeds []int64) map[int64]bool {
		seen := map[int64]bool{}
		for queue := seeds; len(queue) > 0; {
			id := queue[0]
			queue = queue[1:]
			p, ok := c.procs[id]
			if !ok || seen[id] {
				continue
			}
			seen[id] = true
			for _, cn := range c.out[id] {
				if cn.Kind == connectionFailure || p.Kind != kindHuman {
					queue = append(queue, cn.Target)
				}
			}
			for _, l := range c.links[id] {
				if l.Direction == directionOutput {
					queue = append(queue, c.takers[l.Object]...)
				}
			}
			if p.Kind == kindFlow {
				if t := c.innerStart(id); t != 0 {
					queue = append(queue, t)
				}
				for _, l := range c.links[id] {
					if l.Direction != directionInput {
						continue
					}
					for _, t := range c.takers[l.Object] {
						if pr := c.procs[t]; pr.Parent != nil && *pr.Parent == id {
							queue = append(queue, t)
						}
					}
				}
			}
		}
		return seen
	}
	var seeds []int64
	if c.started() {
		seeds = append(seeds, *c.s.Start.Target)
	}
	reached = walk(seeds)
	given = map[int64]bool{}
	if inputs {
		var from []int64
		for _, o := range c.s.Objects {
			if o.Parent == nil && o.Owner == nil && !o.Human && !c.generated[o.ID] {
				for _, t := range c.takers[o.ID] {
					if c.procs[t].Parent == nil {
						from = append(from, t)
					}
				}
			}
		}
		given = walk(from)
	}
	return reached, given
}

// Names in quotes, joined with "and".
func quotedList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = quote(n)
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

// A number of seconds as the panel would say it, such as "2 days" or "90
// minutes".
func durationText(seconds int) string {
	for _, u := range []struct {
		name string
		size int
	}{{"day", 86400}, {"hour", 3600}, {"minute", 60}} {
		if seconds >= u.size && seconds%u.size == 0 {
			n := seconds / u.size
			if n == 1 {
				return "1 " + u.name
			}
			return strconv.Itoa(n) + " " + u.name + "s"
		}
	}
	if seconds == 1 {
		return "1 second"
	}
	return strconv.Itoa(seconds) + " seconds"
}

// The board's errors alone.
func problemErrors(ps []problem) []problem {
	var out []problem
	for _, p := range ps {
		if p.Level == problemError {
			out = append(out, p)
		}
	}
	return out
}

// Reads what the rules need to know of the app for the boards given, as
// whoever asks sees it: their MCP connections, and with withModel the
// default model, asking the worker when the settings keep none.
func loadProblemFacts(ctx context.Context, db *sql.DB, r *http.Request, workerURL string, flow int64, withModel bool, boards ...snapshot) (problemFacts, error) {
	f := problemFacts{signIn: requestUser(r) != nil, servers: map[int64]serverFacts{}, flows: map[int64]bool{}, skills: map[int64]pickFacts{},
		credentials: map[int64]pickFacts{}, roles: map[string]bool{}, sources: map[int64]bool{}}
	if err := db.QueryRowContext(ctx, "SELECT module FROM flows WHERE id = ?", flow).Scan(&f.module); err != nil {
		return f, err
	}
	err := scanAll(ctx, db, "SELECT "+mcpServerColumns+" FROM mcp_servers", func(rows *sql.Rows) error {
		var s mcpServer
		err := scanMCPServer(rows, &s)
		f.servers[s.ID] = serverFacts{name: s.Name, auth: s.Auth, connector: s.Connector, connected: s.Connected}
		return err
	}, tokenOwner(r))
	if err != nil {
		return f, err
	}
	var flows, skills, credentials, sources []any
	for _, b := range boards {
		for _, p := range b.Processes {
			if p.Settings.Flow != nil {
				flows = append(flows, *p.Settings.Flow)
			}
			for _, id := range p.Settings.Skills {
				skills = append(skills, id)
			}
			for _, id := range p.Settings.Credentials {
				credentials = append(credentials, id)
			}
		}
		for _, o := range b.Objects {
			if o.Source != nil {
				sources = append(sources, *o.Source)
			}
		}
	}
	if len(flows) > 0 {
		err := scanAll(ctx, db, "SELECT id FROM flows WHERE id IN "+placeholders(len(flows)), func(rows *sql.Rows) error {
			var id int64
			err := rows.Scan(&id)
			f.flows[id] = true
			return err
		}, flows...)
		if err != nil {
			return f, err
		}
	}
	if len(sources) > 0 {
		err := scanAll(ctx, db, "SELECT id FROM objects WHERE id IN "+placeholders(len(sources)), func(rows *sql.Rows) error {
			var id int64
			err := rows.Scan(&id)
			f.sources[id] = true
			return err
		}, sources...)
		if err != nil {
			return f, err
		}
	}
	if len(skills) > 0 {
		cond, args, err := flowSkills(ctx, db, flow)
		if err != nil {
			return f, err
		}
		err = scanAll(ctx, db, "SELECT s.id, s.name, "+cond+" FROM skills s WHERE s.kind = ? AND s.id IN "+placeholders(len(skills)), func(rows *sql.Rows) error {
			var id int64
			var p pickFacts
			err := rows.Scan(&id, &p.name, &p.usable)
			f.skills[id] = p
			return err
		}, append(append(args, kindSkill), skills...)...)
		if err != nil {
			return f, err
		}
	}
	if len(credentials) > 0 {
		cond, args, err := flowCredentials(ctx, db, flow)
		if err != nil {
			return f, err
		}
		err = scanAll(ctx, db, "SELECT c.id, c.name, "+cond+", c.secret <> '' FROM credentials c WHERE c.id IN "+placeholders(len(credentials)), func(rows *sql.Rows) error {
			var id int64
			var p pickFacts
			err := rows.Scan(&id, &p.name, &p.usable, &p.secret)
			f.credentials[id] = p
			return err
		}, append(args, credentials...)...)
		if err != nil {
			return f, err
		}
	}
	if f.signIn {
		err := scanAll(ctx, db, "SELECT DISTINCT ur.name FROM user_roles ur JOIN users u ON u.id = ur.user_id WHERE u.removed_at IS NULL", func(rows *sql.Rows) error {
			var name string
			err := rows.Scan(&name)
			f.roles[roleKey(name)] = true
			return err
		})
		if err != nil {
			return f, err
		}
	}
	if withModel && needsDefaultModel(boards) {
		if f.defaultModel, err = defaultModel(ctx, db); err != nil {
			return f, err
		}
		f.modelKnown = true
		if f.defaultModel == "" {
			// The worker's pi config may have one; not asked, nothing is said.
			var models agentModels
			wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if callWorker(wctx, http.MethodGet, workerURL+"/models", nil, &models) == nil {
				f.defaultModel = models.Default
			} else {
				f.modelKnown = false
			}
		}
	}
	return f, nil
}

// Whether an agent process of the boards has no model of its own.
func needsDefaultModel(boards []snapshot) bool {
	for _, b := range boards {
		if slices.ContainsFunc(b.Processes, func(p process) bool { return p.Kind == kindAgent && p.Model == "" }) {
			return true
		}
	}
	return false
}

// The board as it is, and the version the flow published, if any.
func problemBoards(ctx context.Context, db *sql.DB, flow int64) (board snapshot, published *int64, publishedBoard *snapshot, err error) {
	err = inTx(ctx, db, func(tx *sql.Tx) error {
		var err error
		board, err = takeSnapshot(ctx, tx, flow)
		return err
	})
	if err != nil {
		return board, nil, nil, err
	}
	var body []byte
	err = db.QueryRowContext(ctx, `SELECT f.published_version_id, v.snapshot FROM flows f
		LEFT JOIN flow_versions v ON v.id = f.published_version_id WHERE f.id = ?`, flow).Scan(&published, &body)
	if err != nil || body == nil {
		return board, published, nil, err
	}
	var s snapshot
	if err := json.Unmarshal(body, &s); err != nil {
		return board, published, nil, err
	}
	return board, published, &s, nil
}

// What the problems route answers: the board's problems, and the version
// published's errors.
type flowProblems struct {
	Problems  []problem         `json:"problems"`
	Published *publishedProblem `json:"published"`
}

type publishedProblem struct {
	Version int64     `json:"version"`
	Errors  []problem `json:"errors"`
}

// Why a run of the board, or of the version published with published, is
// refused, listing its errors; "" when it isn't. A struct runs nothing,
// which its own refusal says.
func refuseRun(ctx context.Context, db *sql.DB, r *http.Request, flow int64, published bool) (string, error) {
	board, version, publishedBoard, err := problemBoards(ctx, db, flow)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	what := "The board"
	if published && publishedBoard != nil {
		board, what = *publishedBoard, fmt.Sprintf("Version %d, the one published,", *version)
	}
	facts, err := loadProblemFacts(ctx, db, r, "", flow, false, board)
	if err != nil {
		return "", err
	}
	errs := problemErrors(boardProblems(board, facts))
	if len(errs) == 0 {
		return "", nil
	}
	lines := []string{what + " can't run:"}
	for _, p := range errs {
		lines = append(lines, "- "+p.Message)
	}
	return strings.Join(lines, "\n"), nil
}

func registerProblemRoutes(mux *http.ServeMux, db *sql.DB, workerURL string) {
	// What is wrong with the flow's board as it is, and with the version
	// it published.
	mux.HandleFunc("GET /api/flows/{id}/problems", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		board, version, publishedBoard, err := problemBoards(ctx, db, id)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		boards := []snapshot{board}
		if publishedBoard != nil {
			boards = append(boards, *publishedBoard)
		}
		facts, err := loadProblemFacts(ctx, db, r, workerURL, id, true, boards...)
		if err != nil {
			serverError(w, err)
			return
		}
		out := flowProblems{Problems: boardProblems(board, facts)}
		if publishedBoard != nil {
			out.Published = &publishedProblem{Version: *version, Errors: problemErrors(boardProblems(*publishedBoard, facts))}
			if out.Published.Errors == nil {
				out.Published.Errors = []problem{}
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
}
