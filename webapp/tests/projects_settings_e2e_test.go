package tests

// A project's settings are its own: model providers and keys, the default
// models, MCP servers with their connections, connectors, skills and
// commands, credentials, internet hosts, limits, API keys, its git token
// and its audit log. A new project has none of them, though Legacy has and
// the worker has providers of its own, which only Legacy inherits; the
// same names may be used in each; a step's grant reaches only its run's
// project's, through every proxy; and an owner of one project can't reach
// another's. Above them, the instance's admin sets ceilings over every
// project's limits. See docs/PROJECTS.md.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

type psProvider struct {
	ID   string `json:"id"`
	Hint string `json:"hint"`
}

type psProviders struct {
	Providers     []psProvider `json:"providers"`
	Default       string       `json:"default"`
	ClaudeDefault string       `json:"claudeDefault"`
}

type psModels struct {
	Default   string   `json:"default"`
	Models    []string `json:"models"`
	Harnesses []string `json:"harnesses"`
}

type psNamed struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Hint string `json:"hint"`
}

type psLimits struct {
	RunSpend       float64 `json:"runSpend"`
	UserMonthSpend float64 `json:"userMonthSpend"`
	UserSteps      int64   `json:"userParallelSteps"`
	RunDiskMB      int64   `json:"runDiskMb"`
}

type psProjectLimits struct {
	psLimits
	Ceilings psLimits `json:"ceilings"`
	Holding  psLimits `json:"holding"`
}

type psEvent struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
	Run    *int64 `json:"run"`
}

type psEvents struct {
	Events []psEvent `json:"events"`
}

// Adds a custom provider speaking Anthropic's API at the test provider's
// /gateway, its key saved, to the project the app's requests name, and
// returns its id, its name in lowercase with hyphens for spaces.
func psAddProvider(a *app, name, key string) string {
	a.t.Helper()
	var ps psProviders
	a.call("POST", "/api/model-providers", map[string]any{"name": name, "api": "anthropic-messages", "baseUrl": testProvider.URL + "/gateway",
		"keySource": "saved", "key": key, "models": []string{"gw-1"}}, http.StatusOK, &ps)
	id := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if !slices.ContainsFunc(ps.Providers, func(p psProvider) bool { return p.ID == id && strings.HasSuffix(key, p.Hint) }) {
		a.t.Fatalf("the provider %s isn't listed: %+v", id, ps)
	}
	return id
}

func psAddServer(a *app, body map[string]any) int64 {
	a.t.Helper()
	var s psNamed
	a.call("POST", "/api/mcp-servers", body, http.StatusCreated, &s)
	return s.ID
}

// The audit events of the project the app's requests name, waiting for
// those written in the background until cond holds of them.
func psAudit(a *app, path string, cond func([]psEvent) bool) []psEvent {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var out psEvents
		a.call("GET", path, nil, http.StatusOK, &out)
		if cond(out.Events) || time.Now().After(deadline) {
			return out.Events
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func psHasEvent(events []psEvent, kind, detail string) bool {
	return slices.ContainsFunc(events, func(e psEvent) bool { return e.Kind == kind && strings.Contains(e.Detail, detail) })
}

// The projects the rows a query finds are in, waiting for those written
// in the background until there are some.
func psProjectsOf(t *testing.T, db *sql.DB, q string, args ...any) []int64 {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var projects []int64
		rows, err := db.Query(q, args...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var p int64
			rows.Scan(&p)
			projects = append(projects, p)
		}
		rows.Close()
		if len(projects) > 0 || time.Now().After(deadline) {
			return projects
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A new project starts with none of Legacy's settings, nor the worker's own
// models and keys; each is made there under the same names as Legacy's,
// and listed in each project apart. Its owners can't read the webapp's
// environment as a provider's key. What is changed in it is in its own
// audit log; that it was made, in the instance's events.
func TestProjectSettingsStartEmptyAndAreTheirOwn(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	server, skillName, commandName := fmt.Sprintf("tracker_%d", n), fmt.Sprintf("triage-%d", n), fmt.Sprintf("greet-%d", n)
	host, keyName := fmt.Sprintf("hosts-%d.example.com", n), fmt.Sprintf("ci %d", n)
	gateway := fmt.Sprintf("Acme gateway %d", n)
	forgetMCPServers(t, server)
	credName := credentialsName(t, "Tracker")
	t.Cleanup(func() {
		db.Exec("DELETE FROM model_providers WHERE id = ?", fmt.Sprintf("acme-gateway-%d", n))
		db.Exec("DELETE FROM model_default WHERE project_id = 1")
		db.Exec("DELETE FROM skills WHERE name IN (?, ?)", skillName, commandName)
		db.Exec("DELETE FROM egress_hosts WHERE host = ?", host)
		db.Exec("DELETE FROM limits WHERE project_id = 1")
		db.Exec("DELETE FROM model_keys WHERE env = 'OPENAI_API_KEY'")
	})

	// Legacy has one of each.
	legacyGateway := psAddProvider(a, gateway, "legacy-key-LLLL")
	legacyServer := psAddServer(a, map[string]any{"name": server, "url": testProvider.URL + "/mcp"})
	a.call("POST", "/api/skills", map[string]any{"name": skillName, "description": "Triage.", "content": "Legacy's way."}, http.StatusCreated, nil)
	a.call("POST", "/api/commands", map[string]any{"name": commandName, "content": "Legacy's greeting."}, http.StatusCreated, nil)
	legacyCred := credentialsAdd(a, map[string]any{"name": credName, "baseUrl": "https://tracker.example.com", "auth": "header",
		"secret": "legacy-secret-0123456789LLLL", "everyone": true})
	a.call("POST", "/api/egress-hosts", map[string]any{"host": host}, http.StatusOK, nil)
	a.call("POST", "/api/api-keys", map[string]any{"name": keyName}, http.StatusCreated, nil)
	a.call("PUT", "/api/limits", psLimits{RunSpend: 5}, http.StatusOK, nil)

	p := newProject(a, "Settings")
	a.project = p.ID
	var ps psProviders
	a.call("GET", "/api/model-providers", nil, http.StatusOK, &ps)
	if len(ps.Providers) != 0 || ps.Default != "" || ps.ClaudeDefault != "" {
		t.Errorf("a new project's providers: %+v", ps)
	}
	var keys settingsKeys
	a.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)
	if len(keys.Keys) != 0 {
		t.Errorf("a new project has the keys the worker's models read: %+v", keys)
	}
	var models psModels
	a.call("GET", "/api/models", nil, http.StatusOK, &models)
	if len(models.Models) != 0 || models.Default != "" || !slices.Equal(models.Harnesses, []string{"pi"}) {
		t.Errorf("a new project is offered %+v", models)
	}
	for _, path := range []string{"/api/mcp-servers", "/api/skills", "/api/commands", "/api/credentials", "/api/egress-hosts", "/api/api-keys"} {
		var list []map[string]any
		a.call("GET", path, nil, http.StatusOK, &list)
		if len(list) != 0 {
			t.Errorf("a new project's %s: %v", path, list)
		}
	}
	var directory []map[string]any
	a.call("GET", "/api/connectors", nil, http.StatusOK, &directory)
	for _, e := range directory {
		if e["installed"] != nil {
			t.Errorf("a new project has installed %v", e["name"])
		}
	}
	var limits psProjectLimits
	a.call("GET", "/api/limits", nil, http.StatusOK, &limits)
	if limits.psLimits != (psLimits{}) || limits.Holding != (psLimits{}) {
		t.Errorf("a new project's limits: %+v", limits)
	}
	var access, token map[string]any
	a.call("GET", "/api/git-access", nil, http.StatusOK, &access)
	a.call("GET", "/api/git-token", nil, http.StatusOK, &token)
	if access["source"] != "none" || token["set"] != false {
		t.Errorf("a new project's git access: %v, token %v", access, token)
	}
	var spending struct {
		People []map[string]any `json:"people"`
		Runs   []map[string]any `json:"runs"`
	}
	a.call("GET", "/api/spending", nil, http.StatusOK, &spending)
	if len(spending.People) != 0 || len(spending.Runs) != 0 {
		t.Errorf("a new project spent %+v", spending)
	}
	var audit psEvents
	a.call("GET", "/api/audit", nil, http.StatusOK, &audit)
	if len(audit.Events) != 0 {
		t.Errorf("a new project's audit log: %+v", audit.Events)
	}

	// The same names, in it.
	gw := psAddProvider(a, gateway, "p-key-PPPP")
	if gw != legacyGateway {
		t.Errorf("the provider's id is %s here and %s in Legacy", gw, legacyGateway)
	}
	pServer := psAddServer(a, map[string]any{"name": server, "url": testProvider.URL + "/mcp"})
	a.call("POST", "/api/skills", map[string]any{"name": skillName, "description": "Triage.", "content": "Its way."}, http.StatusCreated, nil)
	a.call("POST", "/api/commands", map[string]any{"name": commandName, "content": "Its greeting."}, http.StatusCreated, nil)
	pCred := credentialsAdd(a, map[string]any{"name": credName, "baseUrl": "https://tracker.example.com", "auth": "header",
		"secret": "p-secret-0123456789PPPP", "everyone": true})
	a.call("POST", "/api/egress-hosts", map[string]any{"host": host}, http.StatusOK, nil)
	a.call("POST", "/api/api-keys", map[string]any{"name": keyName}, http.StatusCreated, nil)
	if pServer == legacyServer || pCred.ID == legacyCred.ID || legacyCred.Variable != pCred.Variable {
		t.Errorf("the server is %d here and %d in Legacy, the credential %+v and %+v", pServer, legacyServer, pCred, legacyCred)
	}
	// A name is still unique within the project.
	a.refused("POST", "/api/mcp-servers", map[string]any{"name": server, "url": testProvider.URL + "/mcp"}, http.StatusConflict, "another server has that name")
	a.refused("POST", "/api/skills", map[string]any{"name": skillName, "description": "Again.", "content": "Again."}, http.StatusConflict, "already")
	a.refused("POST", "/api/credentials", map[string]any{"name": credName, "baseUrl": "https://x.example.com", "auth": "header", "secret": "s", "everyone": true},
		http.StatusConflict, "already")

	// The webapp's environment is the instance's.
	a.refused("POST", "/api/model-providers", map[string]any{"name": fmt.Sprintf("Vault %d", n), "api": "anthropic-messages",
		"baseUrl": testProvider.URL + "/gateway", "keySource": "env", "env": "SECRET_KEY", "models": []string{"gw-1"}}, http.StatusBadRequest, "the webapp's environment is the instance's")
	a.refused("POST", "/api/model-providers/models", map[string]any{"baseUrl": testProvider.URL + "/gateway", "api": "anthropic-messages",
		"keySource": "env", "env": "VAULT_GATEWAY_KEY"}, http.StatusBadRequest, "the webapp's environment is the instance's")
	a.refused("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-mine"}, http.StatusBadRequest, "none of the worker's own models")
	a.call("PUT", "/api/limits", psLimits{RunSpend: 7}, http.StatusOK, nil)

	// Each project lists its own.
	lists := func() (psProviders, []psNamed, []psNamed, []credentialsItem, psProjectLimits) {
		var ps psProviders
		var servers, skills []psNamed
		var creds []credentialsItem
		var l psProjectLimits
		a.call("GET", "/api/model-providers", nil, http.StatusOK, &ps)
		a.call("GET", "/api/mcp-servers", nil, http.StatusOK, &servers)
		a.call("GET", "/api/skills", nil, http.StatusOK, &skills)
		a.call("GET", "/api/credentials", nil, http.StatusOK, &creds)
		a.call("GET", "/api/limits", nil, http.StatusOK, &l)
		return ps, servers, skills, creds, l
	}
	ps, servers, skills, creds, limits := lists()
	if len(ps.Providers) != 1 || ps.Providers[0].Hint != "PPPP" || ps.Default != gw+"/gw-1" || len(servers) != 1 || servers[0].ID != pServer ||
		len(skills) != 1 || len(creds) != 1 || creds[0].ID != pCred.ID || creds[0].Hint != "PPPP" || limits.RunSpend != 7 {
		t.Errorf("the project lists %+v, %+v, %+v, %+v, %+v", ps, servers, skills, creds, limits)
	}
	a.project = 0
	ps, servers, _, creds, limits = lists()
	legacyProvider := slices.IndexFunc(ps.Providers, func(p psProvider) bool { return p.ID == legacyGateway })
	if legacyProvider < 0 || ps.Providers[legacyProvider].Hint != "LLLL" || slices.ContainsFunc(servers, func(s psNamed) bool { return s.ID == pServer }) ||
		slices.ContainsFunc(creds, func(c credentialsItem) bool { return c.ID == pCred.ID }) || limits.RunSpend != 5 {
		t.Errorf("Legacy lists %+v, %+v, %+v, %+v", ps, servers, creds, limits)
	}
	var keysListed []psNamed
	a.call("GET", "/api/api-keys", nil, http.StatusOK, &keysListed)
	if c := slices.IndexFunc(keysListed, func(k psNamed) bool { return k.Name == keyName }); c < 0 || slices.ContainsFunc(keysListed[c+1:], func(k psNamed) bool { return k.Name == keyName }) {
		t.Errorf("Legacy's API keys: %+v", keysListed)
	}
	for _, table := range []string{"mcp_servers", "credentials"} {
		if got := psProjectsOf(t, db, "SELECT project_id FROM "+table+" WHERE name = ? ORDER BY project_id", map[string]string{"mcp_servers": server, "credentials": credName}[table]); !slices.Equal(got, []int64{1, p.ID}) {
			t.Errorf("%s of the name are in the projects %v", table, got)
		}
	}
	if got := psProjectsOf(t, db, "SELECT project_id FROM skills WHERE name IN (?, ?) ORDER BY project_id", skillName, commandName); !slices.Equal(got, []int64{1, 1, p.ID, p.ID}) {
		t.Errorf("the skills and commands are in the projects %v", got)
	}

	// The worker's own models' keys are for projects inheriting them; two
	// such keep a key each for the same variable.
	inheriting := newProject(a, "Inheriting")
	if _, err := db.Exec("UPDATE projects SET inherits_instance = TRUE WHERE id = ?", inheriting.ID); err != nil {
		t.Fatal(err)
	}
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-legacy-1111"}, http.StatusOK, nil)
	a.project = inheriting.ID
	a.call("PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "sk-other-2222"}, http.StatusOK, &keys)
	hint := func(keys settingsKeys) string {
		i := slices.IndexFunc(keys.Keys, func(k settingsKey) bool { return k.Env == "OPENAI_API_KEY" })
		if i < 0 {
			return ""
		}
		return keys.Keys[i].Hint
	}
	if hint(keys) != "2222" {
		t.Errorf("the inheriting project's keys: %+v", keys)
	}
	a.project = 0
	a.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)
	if hint(keys) != "1111" {
		t.Errorf("Legacy's keys: %+v", keys)
	}

	// What was changed in the project is in its log, not Legacy's; that it
	// was made, in the instance's events.
	a.project = p.ID
	events := psAudit(a, "/api/audit", func(es []psEvent) bool { return psHasEvent(es, "limits.set", "$7.00") })
	if !psHasEvent(events, "internet.allow", host) || !psHasEvent(events, "credential.add", credName) || psHasEvent(events, "limits.set", "$5.00") {
		t.Errorf("the project's audit log: %+v", events)
	}
	a.project = 0
	events = psAudit(a, "/api/audit", func(es []psEvent) bool { return psHasEvent(es, "limits.set", "$5.00") })
	if psHasEvent(events, "limits.set", "$7.00") {
		t.Errorf("Legacy's audit log has the project's change")
	}
	events = psAudit(a, "/api/instance/events", func(es []psEvent) bool { return psHasEvent(es, "project.made", p.Name) })
	if !psHasEvent(events, "project.made", p.Name) {
		t.Errorf("the instance's events: %+v", events)
	}
}

// The ceilings the instance's admin sets bound what a project's owners may
// set, and hold over what they set before; what holds is the least of
// the two, which the worker and the proxy are held to.
func TestProjectLimitsUnderTheCeilings(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	t.Cleanup(func() {
		db.Exec("DELETE FROM instance_limits")
		db.Exec("DELETE FROM limits")
	})
	var ceilings psLimits
	a.call("GET", "/api/instance/limits", nil, http.StatusOK, &ceilings)
	if ceilings != (psLimits{}) {
		t.Errorf("ceilings before any is set: %+v", ceilings)
	}
	a.call("PUT", "/api/instance/limits", psLimits{RunSpend: 1, UserMonthSpend: 10, UserSteps: 4, RunDiskMB: 100}, http.StatusOK, &ceilings)
	p := newProject(a, "Limited")
	a.project = p.ID
	a.refused("PUT", "/api/limits", psLimits{RunSpend: 2}, http.StatusBadRequest, "at most $1.00, the instance's ceiling")
	a.refused("PUT", "/api/limits", psLimits{UserSteps: 5}, http.StatusBadRequest, "at most 4 steps going at once")
	a.refused("PUT", "/api/limits", psLimits{RunDiskMB: 101}, http.StatusBadRequest, "at most 100 MB")
	a.refused("PUT", "/api/limits", psLimits{UserMonthSpend: 11}, http.StatusBadRequest, "at most $10.00 a month")
	var l psProjectLimits
	a.call("PUT", "/api/limits", psLimits{RunSpend: 0.5}, http.StatusOK, &l)
	want := psProjectLimits{psLimits: psLimits{RunSpend: 0.5}, Ceilings: psLimits{RunSpend: 1, UserMonthSpend: 10, UserSteps: 4, RunDiskMB: 100},
		Holding: psLimits{RunSpend: 0.5, UserMonthSpend: 10, UserSteps: 4, RunDiskMB: 100}}
	if l != want {
		t.Errorf("the project's limits: %+v, want %+v", l, want)
	}
	// A ceiling lowered holds over the project's own.
	a.call("PUT", "/api/instance/limits", psLimits{RunSpend: 0.25, RunDiskMB: 100}, http.StatusOK, nil)
	a.call("GET", "/api/limits", nil, http.StatusOK, &l)
	if l.RunSpend != 0.5 || l.Holding.RunSpend != 0.25 || l.Holding.UserMonthSpend != 0 {
		t.Errorf("under a lowered ceiling: %+v", l)
	}

	// Legacy's runs are held to the ceilings too.
	a.project = 0
	f := a.newFlow("Under the ceilings")
	a.startAt(f.ID, a.agent(f.ID, "Bounded", "Work.").ID)
	a.worker.onAgent("Bounded", handsIn(data("done")))
	d := a.finish(a.startRun(f.ID).ID)
	var detail struct {
		SpendLimit float64 `json:"spendLimit"`
	}
	a.call("GET", fmt.Sprintf("/api/runs/%d", d.ID), nil, http.StatusOK, &detail)
	if run := a.worker.runsOf("Bounded")[0]; run.DiskLimit != 100<<20 || detail.SpendLimit != 0.25 {
		t.Errorf("the run may take %d bytes and spend $%v", run.DiskLimit, detail.SpendLimit)
	}
	events := psAudit(a, "/api/instance/events", func(es []psEvent) bool { return psHasEvent(es, "limits.ceilings", "$0.25") })
	if !psHasEvent(events, "limits.ceilings", "$0.25") {
		t.Errorf("the instance's events: %+v", events)
	}
}

// What the steps of Legacy and of a second project may reach, each with
// one of the same name, as a world both tests below work in.
type psWorld struct {
	n                     int64
	p                     projectRow
	mcp                   *settingsMCP
	svc                   *credentialsService
	shared, only          string
	legacyServer, pServer int64
	legacyCred, pCred     credentialsItem
	legacyHost, pHost     string
	pSkill, pSlack        int64
	command               string
}

// Makes the world: in each project a provider of the same id, an MCP
// server of the same name connected with a token of its own, a credential
// of the same name at a service of its own, a command of the same name,
// and an internet host of its own; in the second project a provider, a
// skill and a Slack connector of its own, and a git token.
func newPSWorld(t *testing.T, a *app) *psWorld {
	t.Helper()
	w := &psWorld{n: time.Now().UnixNano(), mcp: newSettingsMCP(t), svc: newCredentialsService(t)}
	db := secretsDB(t)
	server, credName := fmt.Sprintf("tracker_%d", w.n), credentialsName(t, "Tracker")
	slack := fmt.Sprintf("slack_%d", w.n)
	w.command, w.legacyHost, w.pHost = fmt.Sprintf("greet-%d", w.n), fmt.Sprintf("legacy-%d.example.com", w.n), fmt.Sprintf("p-only-%d.example.com", w.n)
	skill := fmt.Sprintf("p-skill-%d", w.n)
	forgetMCPServers(t, server, slack)
	t.Cleanup(func() {
		db.Exec("DELETE FROM model_providers WHERE id IN (?, ?)", fmt.Sprintf("acme-shared-%d", w.n), fmt.Sprintf("acme-only-%d", w.n))
		db.Exec("DELETE FROM model_default WHERE project_id = 1")
		db.Exec("DELETE FROM skills WHERE name IN (?, ?)", w.command, skill)
		db.Exec("DELETE FROM egress_hosts WHERE host IN (?, ?)", w.legacyHost, w.pHost)
	})

	w.p = newProject(a, "World")
	a.project = w.p.ID
	w.shared = psAddProvider(a, fmt.Sprintf("Acme shared %d", w.n), "p-shared-key")
	w.only = psAddProvider(a, fmt.Sprintf("Acme only %d", w.n), "p-only-key")
	w.pServer = psAddServer(a, map[string]any{"name": server, "url": w.mcp.srv.URL + "/mcp"})
	psConnect(a, w.mcp, w.p.ID, w.pServer, "p-mcp-token")
	w.pCred = credentialsAdd(a, map[string]any{"name": credName, "baseUrl": w.svc.srv.URL + "/p", "auth": "header",
		"secret": "p-secret-0123456789", "everyone": true})
	a.call("POST", "/api/egress-hosts", map[string]any{"host": w.pHost}, http.StatusOK, nil)
	a.call("POST", "/api/commands", map[string]any{"name": w.command, "content": "The project's greeting."}, http.StatusCreated, nil)
	var s psNamed
	a.call("POST", "/api/skills", map[string]any{"name": skill, "description": "Its own.", "content": "Its own."}, http.StatusCreated, &s)
	w.pSkill = s.ID
	w.pSlack = psAddServer(a, map[string]any{"name": slack, "connector": "slack"})
	a.call("PUT", "/api/git-token", map[string]any{"token": "p-git-token-0123456789"}, http.StatusOK, nil)

	a.project = 0
	if legacy := psAddProvider(a, fmt.Sprintf("Acme shared %d", w.n), "legacy-shared-key"); legacy != w.shared {
		t.Fatalf("the shared provider is %s in Legacy and %s in the project", legacy, w.shared)
	}
	w.legacyServer = psAddServer(a, map[string]any{"name": server, "url": w.mcp.srv.URL + "/mcp"})
	psConnect(a, w.mcp, 0, w.legacyServer, "legacy-mcp-token")
	w.legacyCred = credentialsAdd(a, map[string]any{"name": credName, "baseUrl": w.svc.srv.URL + "/legacy", "auth": "header",
		"secret": "legacy-secret-0123456789", "everyone": true})
	a.call("POST", "/api/egress-hosts", map[string]any{"host": w.legacyHost}, http.StatusOK, nil)
	a.call("POST", "/api/commands", map[string]any{"name": w.command, "content": "Legacy's greeting."}, http.StatusCreated, nil)
	return w
}

// Connects the server of the project (0 for Legacy) for whoever is signed
// in, as the settings' window does, naming the project in its address.
func psConnect(a *app, m *settingsMCP, project, server int64, token string) {
	a.t.Helper()
	m.mu.Lock()
	m.next = token
	m.mu.Unlock()
	in := *a
	in.project = project
	resp, err := a.client.Get(in.pageURL(fmt.Sprintf("/api/mcp-servers/%d/connect", server)))
	if err != nil {
		a.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "is connected") {
		a.t.Fatalf("connecting ended with %d: %s", resp.StatusCode, body)
	}
}

// Calls the MCP proxy for the server with the step's token, returning the
// status and what it answered.
func psCallMCP(proxy string, server int64, token string) (int, string) {
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/mcp/%d", proxy, server), strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Calls the credential proxy's URL with the step's token.
func psCallCredential(u, token string) int {
	req, _ := http.NewRequest("GET", u+"/issues", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Legacy's steps reach Legacy's providers, MCP servers, credentials,
// commands, hosts and git token, through each proxy, and none of the
// second project's of the same name or its own, whether a process names
// them by id or its step's grant is tried on them; what they spend and do
// is in Legacy's records alone. A call with a token no grant has is the
// instance's to know of.
func TestProjectStepsReachOnlyTheirProjectsThings(t *testing.T) {
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, "GIT_TOKEN=instance-git-e2e")
	a.worker = fw
	db := secretsDB(t)
	w := newPSWorld(t, a)

	f := a.newFlow("Reaches")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
	}}, http.StatusOK, nil)
	reach := a.newProcess(f.ID, map[string]any{"name": "Reach", "kind": kindAgent, "prompt": "/" + w.command,
		"settings": map[string]any{"mcpServers": []int64{w.legacyServer}, "credentials": []int64{w.legacyCred.ID}}})
	a.startAt(f.ID, reach.ID)
	type reached struct {
		otherModel, mcp, otherMCP, cred, otherCred, host, otherHost int
		mcpAnswer                                                   string
	}
	got := make(chan reached, 1)
	a.worker.onAgent("Reach", func(ctx context.Context, run agentRun) runResult {
		var r reached
		if len(run.MCPServers) != 1 || len(run.Credentials) != 1 || run.Egress == nil || run.LLM == nil {
			t.Errorf("the step got %+v", run)
			got <- r
			return submitted(data("reached"))
		}
		r.otherModel, _ = callModel(run.LLM, w.only)
		req, _ := http.NewRequestWithContext(ctx, "POST", run.MCPServers[0].URL, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+run.MCPToken)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			r.mcp, r.mcpAnswer = resp.StatusCode, string(body)
		}
		r.otherMCP, _ = psCallMCP(a.url, w.pServer, run.MCPToken)
		r.cred = psCallCredential(run.Credentials[0].URL, run.CredentialsToken)
		r.otherCred = psCallCredential(fmt.Sprintf("%s/credentials/%d", a.url, w.pCred.ID), run.CredentialsToken)
		r.host = throughEgress(t, a, run.Egress, "http://"+w.legacyHost+"/")
		r.otherHost = throughEgress(t, a, run.Egress, "http://"+w.pHost+"/")
		got <- r
		return submitted(data("reached"))
	})
	d := a.finish(a.startRun(f.ID).ID)
	if d.Status != runDone {
		t.Fatalf("the run %s: %s", d.Status, describeSteps(d))
	}
	r := receive(t, got, "the step reaching what it may")
	if r.otherModel != http.StatusForbidden || r.mcp != http.StatusOK || !strings.Contains(r.mcpAnswer, "Bearer legacy-mcp-token") ||
		r.otherMCP != http.StatusForbidden || r.cred != http.StatusOK || r.otherCred != http.StatusForbidden ||
		r.host == http.StatusForbidden || r.otherHost != http.StatusForbidden {
		t.Errorf("Legacy's step reached %+v", r)
	}
	run := a.worker.runsOf("Reach")[0]
	if run.Reached[w.shared] != "legacy-shared-key" || run.Reached[w.only] != "" || run.Reached["fireworks"] != "gw-from-env" {
		t.Errorf("Legacy's step reached the providers with %v", run.Reached)
	}
	if !strings.Contains(run.Raw, `"project":1,`) || !strings.Contains(run.Raw, `"inheritsInstance":true`) {
		t.Errorf("Legacy's step was asked for without saying it inherits the instance's: %s", run.Raw)
	}
	if !strings.Contains(run.Prompt, "Legacy's greeting.") || strings.Contains(run.Prompt, "The project's greeting.") {
		t.Errorf("Legacy's step's prompt is %q", run.Prompt)
	}
	if run.Git == nil || run.Git.Token != "instance-git-e2e" {
		t.Errorf("Legacy's step's git got %+v", run.Git)
	}
	if seen := w.svc.requests(); !slices.ContainsFunc(seen, func(s credentialsSeen) bool {
		return s.path == "/legacy/issues" && s.authorization == "Bearer legacy-secret-0123456789"
	}) || slices.ContainsFunc(seen, func(s credentialsSeen) bool { return strings.HasPrefix(s.path, "/p/") }) {
		t.Errorf("the services saw %+v", seen)
	}
	if got := psProjectsOf(t, db, "SELECT DISTINCT project_id FROM llm_usage WHERE run_id = ?", d.ID); !slices.Equal(got, []int64{1}) {
		t.Errorf("the run's usage is in the projects %v", got)
	}
	if got := psProjectsOf(t, db, "SELECT DISTINCT project_id FROM audit_events WHERE run_id = ? AND kind IN ('model.grant', 'credential.refused', 'internet.refused')", d.ID); !slices.Equal(got, []int64{1}) {
		t.Errorf("the run's audit events are in the projects %v", got)
	}
	var left int
	db.QueryRow("SELECT COUNT(*) FROM proxy_grants WHERE run_id = ?", d.ID).Scan(&left)
	if left != 0 {
		t.Errorf("%d of the run's grants outlived it", left)
	}
	var access map[string]any
	a.call("GET", "/api/git-access", nil, http.StatusOK, &access)
	if access["source"] != "token" {
		t.Errorf("Legacy's git access: %v", access)
	}
	a.project = w.p.ID
	a.call("GET", "/api/git-access", nil, http.StatusOK, &access)
	if access["source"] != "project" {
		t.Errorf("the project's git access: %v", access)
	}
	var token map[string]any
	a.call("GET", "/api/git-token", nil, http.StatusOK, &token)
	if token["set"] != true || token["hint"] != "6789" || strings.Contains(fmt.Sprint(token), "p-git-token") {
		t.Errorf("the project's git token shows as %v", token)
	}
	if v := secretsStored(t, db, "SELECT token FROM project_git_tokens WHERE project_id = ?", w.p.ID); !strings.HasPrefix(v, "sealed:v1:") {
		t.Errorf("the project's git token was kept as %q", v)
	}
	a.project = 0

	// A process can't be given another project's things by id; one naming
	// them all the same, as only writing its row can make one, fails as if
	// they were none, and a connector process so can't even start, the
	// board's problems saying its connector was removed.
	for _, c := range []struct {
		name     string
		kind     string
		settings map[string]any
		failsFor string
	}{
		{"Stray server", kindAgent, map[string]any{"mcpServers": []int64{w.pServer}}, "MCP server the process uses was removed"},
		{"Stray credential", kindAgent, map[string]any{"credentials": []int64{w.pCred.ID}}, "credential the process uses was removed"},
		{"Stray skill", kindAgent, map[string]any{"skills": []int64{w.pSkill}}, "skill the process uses was deleted"},
		{"Stray connector", kindConnector, map[string]any{"connector": map[string]any{"server": w.pSlack, "operation": "post_message"}}, "uses was removed"},
	} {
		stray := a.newFlow(c.name)
		p := a.newProcess(stray.ID, map[string]any{"name": c.name, "kind": c.kind, "prompt": "Work."})
		a.refused("PATCH", fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"settings": c.settings}, http.StatusBadRequest, "no such")
		settings, _ := json.Marshal(c.settings)
		if _, err := db.Exec("UPDATE processes SET settings = ? WHERE id = ?", settings, p.ID); err != nil {
			t.Fatal(err)
		}
		a.startAt(stray.ID, p.ID)
		a.worker.onAgent(c.name, handsIn(data("strayed")))
		status, body := a.do("POST", fmt.Sprintf("/api/runs?flow=%d", stray.ID), nil)
		if c.kind == kindConnector {
			if status != http.StatusConflict || !strings.Contains(string(body), c.failsFor) {
				t.Errorf("%s: starting it got %d %s", c.name, status, body)
			}
			continue
		}
		var rn struct {
			ID int64 `json:"id"`
		}
		if status != http.StatusCreated || json.Unmarshal(body, &rn) != nil {
			t.Fatalf("%s: starting it got %d %s", c.name, status, body)
		}
		d := a.finish(rn.ID)
		if s := onlyStep(t, d, c.name); s.Status != stepError || !strings.Contains(s.Error, c.failsFor) {
			t.Errorf("%s: %s %q", c.name, s.Status, s.Error)
		}
	}

	// A call with a token no grant has is no project's.
	if status, _ := callModel(&llmAccess{URL: a.url + "/llm", Token: fmt.Sprintf("not-granted-%d", w.n)}, w.shared); status != http.StatusForbidden {
		t.Errorf("a token no grant has got %d", status)
	}
	events := psAudit(a, "/api/instance/events", func(es []psEvent) bool { return psHasEvent(es, "model.refused", "a token that isn't granted") })
	if !psHasEvent(events, "model.refused", "a token that isn't granted") {
		t.Errorf("the instance's events: %+v", events)
	}
}

// A grant of the second project reaches its providers, MCP servers,
// credentials and hosts through each proxy, and is refused Legacy's of
// each though it names them, as the proxies keep to the grant's project;
// nor does it reach the worker's own providers, or a private address it
// allows by name, which are the instance's. What it spends and is refused
// is in the project's records. The grants are planted as a step of the
// project would be given them.
func TestProjectGrantsReachOnlyTheirProjects(t *testing.T) {
	a := newApp(t)
	db := secretsDB(t)
	w := newPSWorld(t, a)
	var replica string
	if err := db.QueryRow("SELECT id FROM replicas ORDER BY seen_at DESC LIMIT 1").Scan(&replica); err != nil {
		t.Fatal(err)
	}
	plant := func(kind string, reaches any) string {
		t.Helper()
		token := fmt.Sprintf("planted-%s-%d", kind, time.Now().UnixNano())
		sum := sha256.Sum256([]byte(token))
		hash := hex.EncodeToString(sum[:])
		data, _ := json.Marshal(reaches)
		if _, err := db.Exec(`INSERT INTO proxy_grants (token_hash, kind, project_id, reaches, replica_id, expires_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP(3) + INTERVAL 1 HOUR)`, hash, kind, w.p.ID, data, replica); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM proxy_grants WHERE token_hash = ?", hash) })
		return token
	}
	a.project = w.p.ID
	private := "10.255.255.1"
	a.call("POST", "/api/egress-hosts", map[string]any{"host": private}, http.StatusOK, nil)
	t.Cleanup(func() { db.Exec("DELETE FROM egress_hosts WHERE host = ?", private) })
	f := a.newFlow("Granted")

	llm := &llmAccess{URL: a.url + "/llm", Token: plant("model", map[string]any{"providers": []string{w.shared, w.only, "fireworks"}})}
	status, body := callModel(llm, w.shared)
	if status != http.StatusOK || !strings.Contains(body, "p-shared-key") {
		t.Errorf("the project's grant reached its provider with %d %s", status, body)
	}
	if status, body := callModel(llm, "fireworks"); status != http.StatusForbidden {
		t.Errorf("the project's grant reached the worker's own provider: %d %s", status, body)
	}

	mcp := plant("mcp", map[string]any{"servers": map[string]any{fmt.Sprint(w.pServer): nil, fmt.Sprint(w.legacyServer): nil}})
	if status, body := psCallMCP(a.url, w.pServer, mcp); status != http.StatusOK || !strings.Contains(body, "Bearer p-mcp-token") {
		t.Errorf("the project's grant reached its server with %d %s", status, body)
	}
	if status, body := psCallMCP(a.url, w.legacyServer, mcp); status == http.StatusOK || strings.Contains(body, "legacy-mcp-token") {
		t.Errorf("the project's grant reached Legacy's server with %d %s", status, body)
	}

	cred := plant("credential", map[string]any{"credentials": []int64{w.pCred.ID, w.legacyCred.ID}, "flow": f.ID})
	if status := psCallCredential(fmt.Sprintf("%s/credentials/%d", a.url, w.pCred.ID), cred); status != http.StatusOK {
		t.Errorf("the project's grant reached its credential's service with %d", status)
	}
	if status := psCallCredential(fmt.Sprintf("%s/credentials/%d", a.url, w.legacyCred.ID), cred); status != http.StatusForbidden {
		t.Errorf("the project's grant reached Legacy's credential's service with %d", status)
	}
	if seen := w.svc.requests(); !slices.ContainsFunc(seen, func(s credentialsSeen) bool {
		return s.path == "/p/issues" && s.authorization == "Bearer p-secret-0123456789"
	}) || slices.ContainsFunc(seen, func(s credentialsSeen) bool { return strings.HasPrefix(s.path, "/legacy/") }) {
		t.Errorf("the services saw %+v", seen)
	}

	egress := &egressAccess{URL: "http://step:" + plant("internet", map[string]any{"repos": []string{}}) + "@127.0.0.1"}
	if status := throughEgress(t, a, egress, "http://"+w.pHost+"/"); status == http.StatusForbidden {
		t.Errorf("the project's grant was refused its own host")
	}
	if status := throughEgress(t, a, egress, "http://"+w.legacyHost+"/"); status != http.StatusForbidden {
		t.Errorf("the project's grant reached Legacy's host with %d", status)
	}
	if status := throughEgress(t, a, egress, "http://"+private+"/"); status == http.StatusOK {
		t.Errorf("the project's grant reached a private address")
	}

	if got := psProjectsOf(t, db, "SELECT project_id FROM llm_usage WHERE provider = ?", w.shared); !slices.Equal(got, []int64{w.p.ID}) {
		t.Errorf("what the grant spent is in the projects %v", got)
	}
	events := psAudit(a, "/api/audit", func(es []psEvent) bool {
		return psHasEvent(es, "model.refused", "fireworks") && psHasEvent(es, "credential.refused", fmt.Sprint(w.legacyCred.ID)) &&
			psHasEvent(es, "internet.refused", w.legacyHost) && psHasEvent(es, "internet.connect", "private address")
	})
	for _, e := range []struct{ kind, detail string }{{"model.refused", "fireworks"}, {"credential.refused", fmt.Sprint(w.legacyCred.ID)},
		{"internet.refused", w.legacyHost}, {"internet.connect", "private address"}} {
		if !psHasEvent(events, e.kind, e.detail) {
			t.Errorf("the project's audit log has no %s of %s: %+v", e.kind, e.detail, events)
		}
	}
	a.project = 0
	if events := psAudit(a, "/api/audit", func([]psEvent) bool { return true }); psHasEvent(events, "internet.refused", w.legacyHost) {
		t.Errorf("Legacy's audit log has the project's refusals")
	}
}

// Steps of the second project reach its providers, MCP servers,
// credentials, hosts and git token, and none of Legacy's, nor what the
// instance is configured with outside the database, which the worker is
// told to withhold; what they spend and do is in its records. Needs runs in
// a second project, which the engine's part of phase 2 makes: until then
// it skips.
func TestProjectStepsOfASecondProjectReachTheirOwn(t *testing.T) {
	fw := newFakeWorker(t)
	a := startApp(t, fw.url, "GIT_TOKEN=instance-git-e2e")
	a.worker = fw
	db := secretsDB(t)
	w := newPSWorld(t, a)

	a.project = w.p.ID
	f := a.newFlow("Reaches its own")
	a.call("PATCH", fmt.Sprintf("/api/flows/%d", f.ID), map[string]any{"settings": map[string]any{
		"repositories": []map[string]any{{"url": "https://github.com/acme/api"}},
	}}, http.StatusOK, nil)
	status, body := a.do("POST", "/api/processes", map[string]any{"flow": f.ID, "name": "Own", "kind": kindAgent, "prompt": "/" + w.command,
		"settings": map[string]any{"mcpServers": []int64{w.pServer}, "credentials": []int64{w.pCred.ID}}})
	if status != http.StatusCreated {
		t.Skipf("making a process in a second project needs the boards' part of phase 2: %d %s", status, body)
	}
	var own process
	if err := json.Unmarshal(body, &own); err != nil {
		t.Fatal(err)
	}
	a.startAt(f.ID, own.ID)
	type reached struct {
		mcp, otherMCP, cred, otherCred, host, otherHost, legacyOnly int
		mcpAnswer                                                   string
	}
	got := make(chan reached, 4)
	a.worker.onAgent("Own", func(ctx context.Context, run agentRun) runResult {
		var r reached
		if len(run.MCPServers) == 1 && len(run.Credentials) == 1 && run.Egress != nil && run.LLM != nil {
			r.legacyOnly, _ = callModel(run.LLM, "openai")
			req, _ := http.NewRequestWithContext(ctx, "POST", run.MCPServers[0].URL, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer "+run.MCPToken)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				r.mcp, r.mcpAnswer = resp.StatusCode, string(body)
			}
			r.otherMCP, _ = psCallMCP(a.url, w.legacyServer, run.MCPToken)
			r.cred = psCallCredential(run.Credentials[0].URL, run.CredentialsToken)
			r.otherCred = psCallCredential(fmt.Sprintf("%s/credentials/%d", a.url, w.legacyCred.ID), run.CredentialsToken)
			r.host = throughEgress(t, a, run.Egress, "http://"+w.pHost+"/")
			r.otherHost = throughEgress(t, a, run.Egress, "http://"+w.legacyHost+"/")
		} else {
			t.Errorf("the project's step got %+v", run)
		}
		got <- r
		return submitted(data("reached"))
	})
	status, body = a.do("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil)
	if status != http.StatusCreated {
		t.Skipf("a run in a second project needs the engine's part of phase 2: %d %s", status, body)
	}
	var rn run
	if err := json.Unmarshal(body, &rn); err != nil {
		t.Fatal(err)
	}
	d := a.finish(rn.ID)
	if d.Status != runDone {
		t.Fatalf("the run %s: %s", d.Status, describeSteps(d))
	}
	r := receive(t, got, "the project's step reaching what it may")
	if r.legacyOnly != http.StatusForbidden || r.mcp != http.StatusOK || !strings.Contains(r.mcpAnswer, "Bearer p-mcp-token") ||
		r.otherMCP != http.StatusForbidden || r.cred != http.StatusOK || r.otherCred != http.StatusForbidden ||
		r.host == http.StatusForbidden || r.otherHost != http.StatusForbidden {
		t.Errorf("the project's step reached %+v", r)
	}
	run := a.worker.runsOf("Own")[0]
	want := map[string]string{w.shared: "p-shared-key", w.only: "p-only-key"}
	if fmt.Sprint(run.Reached) != fmt.Sprint(want) {
		t.Errorf("the project's step reached the providers with %v, want %v", run.Reached, want)
	}
	if !strings.Contains(run.Raw, fmt.Sprintf(`"project":%d,`, w.p.ID)) || !strings.Contains(run.Raw, `"inheritsInstance":false`) {
		t.Errorf("the project's step was asked for without saying it doesn't inherit the instance's: %s", run.Raw)
	}
	if !strings.Contains(run.Prompt, "The project's greeting.") || strings.Contains(run.Prompt, "Legacy's greeting.") {
		t.Errorf("the project's step's prompt is %q", run.Prompt)
	}
	if run.Git == nil || run.Git.Token != "p-git-token-0123456789" {
		t.Errorf("the project's step's git got %+v", run.Git)
	}
	if got := psProjectsOf(t, db, "SELECT DISTINCT project_id FROM llm_usage WHERE run_id = ?", d.ID); !slices.Equal(got, []int64{w.p.ID}) {
		t.Errorf("the run's usage is in the projects %v", got)
	}
	if got := psProjectsOf(t, db, "SELECT DISTINCT project_id FROM audit_events WHERE run_id = ? AND kind IN ('model.grant', 'git.grant', 'credential.refused')", d.ID); !slices.Equal(got, []int64{w.p.ID}) {
		t.Errorf("the run's audit events are in the projects %v", got)
	}

	// Without a git token of its own, Legacy's GIT_TOKEN doesn't reach it.
	a.call("DELETE", "/api/git-token", nil, http.StatusNoContent, nil)
	a.finish(a.startRun(f.ID).ID)
	if runs := a.worker.runsOf("Own"); len(runs) != 2 || runs[1].Git != nil {
		t.Errorf("without its own git token, the project's step's git got %+v", runs[len(runs)-1].Git)
	}
}

// What a person spends and has going in one project counts toward the
// limits of that project alone.
func TestProjectSpendingAndStepsCountPerProject(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	db := secretsDB(t)
	ada := projectUserID(t, db, usersAda.email)
	p := newProject(a, "Spends")
	planted := fmt.Sprintf("planted-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		db.Exec("DELETE FROM llm_usage WHERE model = ?", planted)
		db.Exec("DELETE FROM person_slots WHERE user_id = ? AND run_id = 0", ada)
		db.Exec("DELETE FROM limits")
	})
	// Ada's own calls in other tests spent cents at most.
	a.call("PUT", "/api/limits", map[string]any{"userMonthSpend": 0.5, "userParallelSteps": 1}, http.StatusOK, nil)
	f := a.newFlow("Spends")
	usersShare(a, f.ID)
	a.startAt(f.ID, a.agent(f.ID, "Spend", "Spend.").ID)
	a.worker.quiet = true
	calls := make(chan int, 4)
	a.worker.onAgent("Spend", func(_ context.Context, run agentRun) runResult {
		status, _ := callModel(run.LLM, "fireworks")
		calls <- status
		return submitted(data("spent"))
	})
	plant := func(project int64) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO llm_usage (project_id, user_id, provider, model, input_tokens, output_tokens, cost_micros, priced)
			VALUES (?, ?, 'openai', ?, 0, 0, 1000000, TRUE)`, project, ada, planted); err != nil {
			t.Fatal(err)
		}
	}
	// A dollar spent in the other project leaves her Legacy's cent.
	plant(p.ID)
	a.finish(a.startRun(f.ID).ID)
	if status := receive(t, calls, "the first call"); status != http.StatusOK {
		t.Errorf("with a dollar spent in another project, her call got %d", status)
	}
	plant(1)
	a.finish(a.startRun(f.ID).ID)
	if status := receive(t, calls, "the second call"); status != http.StatusForbidden {
		t.Errorf("with a dollar spent in Legacy, her call got %d", status)
	}
	db.Exec("DELETE FROM llm_usage WHERE model = ?", planted)

	// Her steps going in the other project leave her Legacy's one slot.
	// They are the replica's, which gives back only those of replicas that
	// stopped.
	var replica string
	if err := db.QueryRow("SELECT id FROM replicas ORDER BY seen_at DESC LIMIT 1").Scan(&replica); err != nil {
		t.Fatal(err)
	}
	slot := func(project int64) {
		t.Helper()
		if _, err := db.Exec("INSERT INTO person_slots (project_id, user_id, run_id, replica_id) VALUES (?, ?, 0, ?)", project, ada, replica); err != nil {
			t.Fatal(err)
		}
	}
	slot(p.ID)
	slot(p.ID)
	if d := a.finish(a.startRun(f.ID).ID); d.Status != runDone {
		t.Errorf("with steps going in another project, her run %s: %s", d.Status, describeSteps(d))
	}
	receive(t, calls, "the third call")
	slot(1)
	rn := a.startRun(f.ID)
	time.Sleep(1500 * time.Millisecond)
	if d := a.getRun(rn.ID); d.Status != runRunning || len(a.worker.runsOf("Spend")) != 3 {
		t.Errorf("with a step going in Legacy, her run went on: %s, %d steps asked for", d.Status, len(a.worker.runsOf("Spend")))
	}
	db.Exec("DELETE FROM person_slots WHERE user_id = ? AND run_id = 0 AND project_id = 1", ada)
	if d := a.finish(rn.ID); d.Status != runDone {
		t.Errorf("once Legacy's slot was given back, her run %s: %s", d.Status, describeSteps(d))
	}
}

// An owner of one project is refused every settings route of a project
// they have no access to, and in one they only edit, those for owners; in
// their own, another project's settings are nowhere to be found by id. The
// instance's routes are a super admin's alone.
func TestProjectSettingsAreItsOwnersAlone(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	server := fmt.Sprintf("legacy_tracker_%d", n)
	forgetMCPServers(t, server)
	legacyServer := psAddServer(a, map[string]any{"name": server, "url": "https://tracker.example.com/mcp", "auth": "token"})
	legacyCred := credentialsAdd(a, map[string]any{"name": credentialsName(t, "Legacys"), "baseUrl": "https://tracker.example.com", "auth": "header",
		"secret": "legacy-secret-0123456789", "everyone": true})
	p, q := newProject(a, "Olga's"), newProject(a, "Not Olga's")
	olga := usersNewPerson("Olga")
	o := usersAdd(a, olga.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", p.ID), []map[string]any{{"user": o.ID, "level": "owner"}}, http.StatusOK, nil)

	routes := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/model-providers", nil},
		{"POST", "/api/model-providers", map[string]any{"name": "Gateway", "api": "anthropic-messages", "baseUrl": "https://gw.example.com", "keySource": "saved", "key": "k", "models": []string{"m"}}},
		{"PUT", "/api/model-providers/gateway", map[string]any{"keySource": "saved", "key": "k", "models": []string{"m"}}},
		{"DELETE", "/api/model-providers/gateway", nil},
		{"PUT", "/api/model-default", map[string]any{"model": "gateway/m"}},
		{"PUT", "/api/model-default/claude", map[string]any{"model": ""}},
		{"POST", "/api/model-providers/models", map[string]any{"baseUrl": "https://gw.example.com", "api": "anthropic-messages"}},
		{"GET", "/api/model-keys", nil},
		{"PUT", "/api/model-keys/OPENAI_API_KEY", map[string]any{"key": "k"}},
		{"DELETE", "/api/model-keys/OPENAI_API_KEY", nil},
		{"POST", "/api/mcp-servers", map[string]any{"name": "mine", "url": "https://mine.example.com"}},
		{"PATCH", fmt.Sprintf("/api/mcp-servers/%d", legacyServer), map[string]any{"name": "taken"}},
		{"DELETE", fmt.Sprintf("/api/mcp-servers/%d", legacyServer), nil},
		{"PUT", fmt.Sprintf("/api/mcp-servers/%d/token", legacyServer), map[string]any{"token": "t"}},
		{"DELETE", fmt.Sprintf("/api/mcp-servers/%d/token", legacyServer), nil},
		{"GET", "/api/credentials", nil},
		{"POST", "/api/credentials", map[string]any{"name": "Mine", "baseUrl": "https://x.example.com", "auth": "header", "secret": "s", "everyone": true}},
		{"PUT", fmt.Sprintf("/api/credentials/%d", legacyCred.ID), map[string]any{"name": "Taken", "baseUrl": "https://x.example.com", "auth": "header", "everyone": true}},
		{"DELETE", fmt.Sprintf("/api/credentials/%d", legacyCred.ID), nil},
		{"GET", "/api/egress-hosts", nil},
		{"POST", "/api/egress-hosts", map[string]any{"host": "mine.example.com"}},
		{"DELETE", "/api/egress-hosts/mine.example.com", nil},
		{"PUT", "/api/limits", map[string]any{"runSpend": 1}},
		{"GET", "/api/audit", nil},
		{"GET", "/api/git-token", nil},
		{"PUT", "/api/git-token", map[string]any{"token": "t"}},
		{"DELETE", "/api/git-token", nil},
	}
	usersSignIn(a, prov, olga)
	a.project = q.ID
	for _, r := range routes {
		a.refused(r.method, r.path, r.body, http.StatusForbidden, "no access to this project")
	}
	// In Legacy she is an editor, as she joined it being added.
	a.project = 0
	for _, r := range routes {
		a.refused(r.method, r.path, r.body, http.StatusForbidden, "only an admin")
	}
	for _, r := range []struct{ method, path string }{{"GET", "/api/instance/limits"}, {"PUT", "/api/instance/limits"}, {"GET", "/api/instance/events"}} {
		a.refused(r.method, r.path, psLimits{}, http.StatusForbidden, "only an admin")
	}

	// In her own, Legacy's are nowhere to be found by id.
	a.project = p.ID
	a.refused("PATCH", fmt.Sprintf("/api/mcp-servers/%d", legacyServer), map[string]any{"name": "taken"}, http.StatusNotFound, "server not found")
	a.refused("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", legacyServer), map[string]any{"token": "t"}, http.StatusNotFound, "server not found")
	a.refused("DELETE", fmt.Sprintf("/api/mcp-servers/%d/token", legacyServer), nil, http.StatusNotFound, "server not found")
	a.refused("POST", fmt.Sprintf("/api/mcp-servers/%d/disconnect", legacyServer), nil, http.StatusNotFound, "server not found")
	a.refused("GET", fmt.Sprintf("/api/mcp-servers/%d/tools", legacyServer), nil, http.StatusNotFound, "server not found")
	a.refused("PUT", fmt.Sprintf("/api/credentials/%d", legacyCred.ID), map[string]any{"name": "Taken", "baseUrl": "https://tracker.example.com",
		"auth": "header", "everyone": true}, http.StatusNotFound, "credential not found")
	a.refused("DELETE", fmt.Sprintf("/api/credentials/%d", legacyCred.ID), nil, http.StatusNotFound, "credential not found")
	a.refused("DELETE", fmt.Sprintf("/api/mcp-servers/%d", legacyServer), nil, http.StatusNotFound, "server not found")
	var left int
	db.QueryRow("SELECT COUNT(*) FROM mcp_servers WHERE id = ?", legacyServer).Scan(&left)
	if left != 1 {
		t.Errorf("deleting Legacy's server from her project deleted it")
	}
	// What is her project's she may change.
	a.call("POST", "/api/egress-hosts", map[string]any{"host": fmt.Sprintf("olga-%d.example.com", n)}, http.StatusOK, nil)
	a.call("GET", "/api/audit", nil, http.StatusOK, nil)
	a.call("GET", "/api/git-token", nil, http.StatusOK, nil)

	usersSignIn(a, prov, usersAda)
	a.call("GET", "/api/instance/limits", nil, http.StatusOK, nil)
	a.call("GET", "/api/instance/events", nil, http.StatusOK, nil)
}

// An MCP server's sign-in is its project's: the window opened for it names
// the project, the sign-in keeps it, and the callback connects the server
// there for whoever started it alone, while they may still enter the
// project and the server is still in it.
func TestProjectMCPSignInIsBoundToItsProject(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	db := secretsDB(t)
	m := newSettingsMCP(t)
	n := time.Now().UnixNano()
	name := fmt.Sprintf("bound_%d", n)
	forgetMCPServers(t, name)
	p := newProject(a, "Signs in")
	olga, bob := usersNewPerson("Olga"), usersNewPerson("Bob")
	o, b := usersAdd(a, olga.email), usersAdd(a, bob.email)
	members := func(ms ...map[string]any) {
		t.Helper()
		usersSignIn(a, prov, usersAda)
		a.call("PUT", fmt.Sprintf("/api/projects/%d/members", p.ID), ms, http.StatusOK, nil)
	}
	members(map[string]any{"user": o.ID, "level": "editor"})
	a.project = p.ID
	server := psAddServer(a, map[string]any{"name": name, "url": m.srv.URL + "/mcp"})
	connected := func(user int64) (int64, bool) {
		var project int64
		err := db.QueryRow("SELECT project_id FROM mcp_tokens WHERE server_id = ? AND user_id = ?", server, user).Scan(&project)
		return project, err == nil
	}

	// Through the window naming the project, her connection is its.
	usersSignIn(a, prov, olga)
	psConnect(a, m, p.ID, server, "olga-token")
	if project, ok := connected(o.ID); !ok || project != p.ID {
		t.Errorf("olga's connection is in project %d (found %v)", project, ok)
	}
	// Without naming it, the window is refused; naming Legacy, where the
	// server isn't, it finds none.
	resp, err := a.client.Get(fmt.Sprintf("%s/api/mcp-servers/%d/connect", a.url, server))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "names no project") {
		t.Errorf("the window naming no project ended with %d: %s", resp.StatusCode, body)
	}
	resp, err = a.client.Get(fmt.Sprintf("%s/api/mcp-servers/%d/connect?project=1", a.url, server))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "doesn&#39;t exist") {
		t.Errorf("the window naming Legacy ended with %d: %s", resp.StatusCode, body)
	}
	// Someone who may not enter the project can't open it.
	usersSignIn(a, prov, bob)
	resp, err = a.client.Get(fmt.Sprintf("%s/api/mcp-servers/%d/connect?project=%d", a.url, server, p.ID))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("bob opening the window got %d", resp.StatusCode)
	}

	// Starts a sign-in as whoever is signed in, and returns the callback the
	// server's sign-in page sends the browser back to, not yet followed.
	start := func(token string) string {
		t.Helper()
		m.mu.Lock()
		m.next = token
		m.mu.Unlock()
		c := *a.client
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		u := fmt.Sprintf("%s/api/mcp-servers/%d/connect?project=%d", a.url, server, p.ID)
		for range 2 {
			resp, err := c.Get(u)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("signing in at %s got %d", u, resp.StatusCode)
			}
			u = resp.Header.Get("Location")
		}
		if !strings.HasPrefix(u, a.url+"/api/mcp-oauth/callback?") {
			t.Fatalf("the sign-in comes back to %s", u)
		}
		return u
	}
	finish := func(callback string) (int, string) {
		t.Helper()
		resp, err := a.client.Get(callback)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// A sign-in Olga started, handed to Bob, connects nobody.
	usersSignIn(a, prov, olga)
	callback := start("olga-again")
	usersSignIn(a, prov, bob)
	if status, body := finish(callback); status != http.StatusBadRequest || !strings.Contains(body, "someone else started") {
		t.Errorf("bob finishing olga's sign-in got %d: %s", status, body)
	}
	if _, ok := connected(b.ID); ok {
		t.Errorf("bob got connected with olga's sign-in")
	}
	// It is used up.
	usersSignIn(a, prov, olga)
	if status, body := finish(callback); status != http.StatusBadRequest || !strings.Contains(body, "expired or was already used") {
		t.Errorf("olga finishing her sign-in bob tried got %d: %s", status, body)
	}

	// Taken out of the project meanwhile, she can't finish it.
	callback = start("olga-gone")
	members()
	usersSignIn(a, prov, olga)
	if status, body := finish(callback); status != http.StatusBadRequest || !strings.Contains(body, "no longer have access") {
		t.Errorf("olga, taken out of the project, finishing got %d: %s", status, body)
	}

	// Nor once the server is gone.
	members(map[string]any{"user": o.ID, "level": "editor"})
	usersSignIn(a, prov, olga)
	callback = start("olga-late")
	usersSignIn(a, prov, usersAda)
	a.call("DELETE", fmt.Sprintf("/api/mcp-servers/%d", server), nil, http.StatusNoContent, nil)
	usersSignIn(a, prov, olga)
	if status, body := finish(callback); status != http.StatusBadRequest || !strings.Contains(body, "the server was removed") {
		t.Errorf("finishing for a server removed got %d: %s", status, body)
	}
}
