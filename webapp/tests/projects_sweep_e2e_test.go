package tests

// The walls between projects, swept. Two projects get one of everything
// (a flow with every kind of process and object, a procedure, structs with
// records, a task board, runs with steps done and waiting, versions, a
// schedule, a webhook, an API key, an MCP server, a connector, a skill, a
// command, a credential, a model provider, an allowed host, a git token,
// limits, a team, a role, a note, a sample, a favourite, a tag, a flow in
// the trash), and every route the webapp serves (routes_test.go) is called
// as an owner of the first, as a super admin and with nobody signing in,
// under the first project's header, naming the second's things, up to four
// times: with the second's ids in the path, the query and the JSON body,
// which the guard turns away (owners.go); with the second's in the path
// and the first's in what the guard reads of the query and body, so the
// route's own checks answer; with the first's leading a path of two ids or
// more and the second's after it; and with the first's in the path and the
// second's in every field the guard doesn't read, its bodies shaped for
// each route where the shape matters (sweepBody). A path naming the second
// project's thing is answered 404, 403 or 400 (a route not served for
// whoever asks, 405, and one saying a step of another run isn't waiting,
// 409, as these say nothing of it); nothing answers with an error of the
// server's; no request changes a row of the second project; no answer
// under the first project's header holds anything of the second (its
// things all carry a marker in their names); and nothing of the first
// project ends up naming the second's things where no key checks it. Then
// the proxies, with grants of the first project, on the second's servers,
// credentials and providers.
//
// A route the sweep doesn't call, or calls expecting it to reach the
// second project as it should, is in sweepSkipped or sweepInstance with
// why: that list is the record of what isn't covered. The routes whose
// bodies the sweep doesn't shape, which refuse it for its shape before
// looking at what it names, are logged as it ends.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Routes the sweep doesn't call, by pattern, and why. Nor does it call
// GET /, the static files, which are no project's; nor, with the first
// project's own ids, the routes deleting what they name, which then name
// nothing of the second project's.
var sweepSkipped = map[string]string{
	"GET /api/auth/login":      "signing in, which names no project; it would sign the sweep's own client in as someone else",
	"GET /api/auth/callback":   "the identity provider's return from signing in, which names no project",
	"GET /api/auth/signed-out": "a page saying one signed out, which names no project",
	"POST /api/auth/logout":    "signing out, which names no project; it would end the sweep's own session",
	"POST /hooks/{token}":      "a webhook's token is its own address, a secret whoever holds it sends to; it names no project in a header, and its signature is checked against its own secret (webhooks_e2e_test.go)",
	"PUT /api/instance/limits": "the ceilings over every project alike, a super admin's; setting them in the sweep would change every other test's runs (limits_e2e_test.go)",
}

// Routes that are the instance's rather than a project's, which a super
// admin, and anyone when nobody signs in, may use on the second project
// whatever project the header names: not swept for them, and swept for
// the first project's owner, who must be refused.
func sweepInstance(pattern string) bool {
	return strings.HasPrefix(pattern, "GET /api/projects") || strings.HasPrefix(pattern, "POST /api/projects") ||
		strings.HasPrefix(pattern, "PUT /api/projects") || strings.HasPrefix(pattern, "PATCH /api/projects") ||
		strings.HasPrefix(pattern, "DELETE /api/projects") ||
		pattern == "POST /api/users" || pattern == "DELETE /api/users/{id}"
}

// Routes whose answers may hold the second project's things for a super
// admin, who enters every project: the person's inbox across them, and
// who they are.
var sweepAcrossForSuper = []string{"GET /api/inbox", "GET /api/me"}

// One project of the sweep and what is in it.
type sweepSide struct {
	p      projectRow
	marker string
	// Its things by kind, as the sweep names them in paths and bodies.
	ids   map[string]int64
	names map[string]string
	key   string
}

func (s *sweepSide) id(t *testing.T, kind string) int64 {
	t.Helper()
	id, ok := s.ids[kind]
	if !ok || id == 0 {
		t.Fatalf("the sweep's project %s has no %s", s.p.Name, kind)
	}
	return id
}

// The board of a sweep's project: a flow working on the ticket a change
// it picks names, with a script, an agent, a person's review and a
// procedure, a connector and a flow process nothing reaches, a note and a
// sample; shared tickets and changes; every name carrying the marker.
func sweepBoardFile(m, server, connector, skill string) string {
	return strings.NewReplacer("MK", m, "SERVER", server, "CONNECTOR", connector, "SKILL", skill).Replace(`spoletta: 1
structs:
  - name: MK Ticket
    shared: true
    states: [open, review, done]
    attributes:
      - {name: id, unique: true}
      - {name: title}
  - name: MK Change
    shared: true
    states: [open, merged]
    attributes:
      - {name: url, unique: true}
      - {name: ticket, ref: MK Ticket}
procedures:
  - name: MK Summarise
    start: {next: Sum}
    objects:
      - name: Draft
      - name: Summary
        end: true
    processes:
      - name: Sum
        kind: script
        script: sum
        takes: [Draft]
        yields: [Summary]
flows:
  - name: MK Work
    tags: [MK-tag]
    repositories:
      - name: app
        url: https://github.com/acme/app.git
    start: {next: Move}
    notes:
      - {text: MK note, process: Write}
    objects:
      - name: Editor
        human: true
      - name: Draft
        states: [approved, rejected]
      - name: Summary
      - name: Stamp
        persistent: true
        attributes:
          - {name: id, unique: true}
      - copyOf: {flow: MK Ticket}
        follows: {object: MK Change, attribute: ticket}
      - copyOf: {flow: MK Change}
        pick: {state: open}
    processes:
      - name: Move
        kind: script
        script: move
        takes: [{object: MK Change, role: instrument}, MK Ticket]
        yields: [{object: MK Ticket, state: review}]
      - name: Write
        prompt: Write a draft of the ticket.
        takes: [{object: MK Ticket, state: review}]
        yields: [Draft]
        samples:
          - name: MK sample
            inputs:
              - {object: MK Ticket, state: review, content: "{\"id\": \"S-1\"}"}
      - name: Mark
        kind: script
        script: mark
        takes: [{object: MK Ticket, state: review}]
        yields: [Stamp]
      - name: Approve
        kind: human
        takes: [Editor, Draft]
        yields:
          - object: Draft
            states: [approved, rejected]
      - name: Summarise
        kind: subflow
        runs: MK Summarise
        takes: [{object: Draft, state: approved}]
        yields: [Summary]
      - name: Post
        kind: connector
        connector: {server: CONNECTOR, operation: post_message, args: {channel: C1, text: MK}}
      - name: Ask
        prompt: Ask around.
        mcpServers: [SERVER]
        skills: [SKILL]
      - name: Inner
        kind: flow
        board:
          processes:
            - name: Deep
              kind: script
              script: deep
`)
}

// Builds a project of the sweep with one of everything, as the app's
// signed-in super admin, with a member of its own holding a role and in a
// team; and the runs, one done with a follow-up, one waiting on its
// review with a comment.
func buildSweepSide(t *testing.T, a *app, db *sql.DB, label string) *sweepSide {
	t.Helper()
	n := time.Now().UnixNano()
	m := fmt.Sprintf("swp%s%d", strings.ToLower(label), n)
	s := &sweepSide{marker: m, ids: map[string]int64{}, names: map[string]string{}}
	s.p = newProject(a, "Sweep "+label)
	a.project = s.p.ID

	server, connector, skill, command := m+"_mcp", m+"_slack", m+"-skill", m+"-command"
	forgetMCPServers(t, server, connector)
	s.ids["server"] = psAddServer(a, map[string]any{"name": server, "url": newSettingsMCP(t).srv.URL + "/mcp"})
	s.ids["connector"] = psAddServer(a, map[string]any{"name": connector, "connector": "slack", "url": testProvider.URL + "/slack"})
	var named struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/skills", map[string]any{"name": skill, "description": m, "content": m}, http.StatusCreated, &named)
	s.ids["skill"] = named.ID
	a.call("POST", "/api/commands", map[string]any{"name": command, "content": m}, http.StatusCreated, &named)
	s.ids["command"] = named.ID
	cred := credentialsAdd(a, map[string]any{"name": credentialsName(t, m+" cred"), "baseUrl": "https://" + m + ".example.com", "auth": "header",
		"header": "X-Api-Key", "template": "{secret}", "secret": m + "-secret-0123456789", "everyone": true})
	s.ids["credential"] = cred.ID
	s.names["provider"] = psAddProvider(a, m+" provider", m+"-key-0123456789")
	s.names["host"] = m + ".example.com"
	a.call("POST", "/api/egress-hosts", map[string]any{"host": s.names["host"]}, http.StatusOK, nil)
	a.call("PUT", "/api/git-token", map[string]any{"token": m + "-git-0123456789"}, http.StatusOK, nil)
	a.call("PUT", "/api/limits", map[string]any{"runSpend": 7}, http.StatusOK, nil)
	s.names["env"] = "OPENAI_API_KEY"

	// A member of its own, in a team and holding a role.
	member := usersNewPerson(label + " member")
	row := usersAdd(a, member.email)
	s.ids["user"] = row.ID
	a.call("PATCH", fmt.Sprintf("/api/users/%d", row.ID), map[string]any{"roles": []string{m + " role"}}, http.StatusOK, nil)
	a.call("POST", "/api/teams", map[string]any{"name": m + " team", "members": []int64{row.ID}}, http.StatusCreated, &named)
	s.ids["team"] = named.ID

	// The boards.
	res := setupImportOK(a, sweepBoardFile(m, server, connector, skill))
	s.ids["struct"] = prImported(t, res, m+" Ticket")
	s.ids["changes"] = prImported(t, res, m+" Change")
	s.ids["procedure"] = prImported(t, res, m+" Summarise")
	s.ids["flow"] = prImported(t, res, m+" Work")
	work := s.ids["flow"]
	prSave(a, s.ids["procedure"])
	v := pubPublish(a, work, m+" version")
	s.ids["version"] = v.ID
	var procs []process
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", work), nil, http.StatusOK, &procs)
	byName := map[string]int64{}
	for _, p := range procs {
		byName[p.Name] = p.ID
	}
	s.ids["process"], s.ids["post"], s.ids["inner"] = byName["Write"], byName["Post"], byName["Inner"]
	var conn struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/connections", map[string]any{"source": byName["Post"], "target": byName["Inner"]}, http.StatusCreated, &conn)
	s.ids["connection"] = conn.ID
	objs := modObjects(a, work)
	draft := modNamed(t, objs, "Draft")
	s.ids["object"] = draft.ID
	s.ids["state"] = draft.States[0].ID
	stamp := modNamed(t, objs, "Stamp")
	s.ids["attribute"] = stamp.Attributes[0].ID
	ticketCopy := modNamed(t, objs, m+" Ticket")
	s.ids["copy"] = ticketCopy.ID
	s.ids["root"] = modObjects(a, s.ids["struct"])[0].ID
	for kind, q := range map[string]string{
		"link":   "SELECT l.id FROM object_links l JOIN processes p ON p.id = l.process_id WHERE p.flow_id = ? ORDER BY l.id LIMIT 1",
		"note":   "SELECT id FROM notes WHERE flow_id = ? ORDER BY id LIMIT 1",
		"sample": "SELECT s.id FROM process_samples s JOIN processes p ON p.id = s.process_id WHERE p.flow_id = ? ORDER BY s.id LIMIT 1",
	} {
		var id int64
		if err := db.QueryRow(q, work).Scan(&id); err != nil {
			t.Fatalf("the sweep's %s: %v", kind, err)
		}
		s.ids[kind] = id
	}
	s.names["sample"] = m + " sample"

	// Records: two open changes, each naming its ticket.
	for i := 1; i <= 2; i++ {
		taskRecord(a, s.ids["struct"], map[string]any{"id": fmt.Sprintf("T-%d", i), "title": m}, "open")
		rec := taskRecord(a, s.ids["changes"], map[string]any{"url": fmt.Sprintf("C-%d", i), "ticket": fmt.Sprintf("T-%d", i)}, "open")
		s.ids["record"] = rec.ID
	}

	// What else a flow has.
	var sched schedule
	a.call("POST", "/api/schedules", map[string]any{"flow": work, "cron": everySecond, "timezone": "UTC", "enabled": false}, http.StatusCreated, &sched)
	s.ids["schedule"] = sched.ID
	h := hooksAdd(a, work, map[string]any{"name": m + " hook", "kind": "github", "events": []string{"issues"}, "mapping": map[string]any{}})
	s.ids["webhook"] = h.ID
	key := apiNewKey(a, m+" key")
	s.ids["apikey"], s.key = key.ID, key.Key
	a.call("PUT", fmt.Sprintf("/api/flows/%d/favourite", work), nil, http.StatusNoContent, nil)
	b := taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": m + " board"}, http.StatusCreated)
	a.call("PUT", fmt.Sprintf("/api/taskboards/%d", b.ID), map[string]any{"name": m + " board", "columns": []any{
		map[string]any{"name": "Open", "struct": s.ids["changes"], "query": "state:open"},
	}}, http.StatusOK, nil)
	s.ids["taskboard"] = b.ID
	trashed := a.newFlow(m + " trashed")
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", trashed.ID), nil, http.StatusNoContent, nil)
	s.ids["trashed"] = trashed.ID

	// A run to its end, and its agent's follow-up.
	done := a.startRun(work)
	review := a.waitStep(done.ID, "Approve", stepWaiting)
	humanMustDecide(a, done.ID, review.ID, map[string]any{"state": pickState(t, draft, "approved")})
	d := a.finish(done.ID)
	if d.Status != runDone {
		t.Fatalf("the sweep's run ended %s: %s", d.Status, describeSteps(d))
	}
	write := onlyStep(t, d, "Write")
	var fu struct {
		ID int64 `json:"id"`
	}
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/followups", d.ID, write.ID), map[string]any{"message": m + " again"}, http.StatusCreated, &fu)
	a.waitRun(d.ID, "the follow-up to end", func(d runDetail) bool {
		s := onlyStep(t, d, "Write")
		return len(s.FollowUps) == 1 && s.FollowUps[0].Status == stepDone
	})
	s.ids["donerun"], s.ids["donestep"], s.ids["followup"] = d.ID, write.ID, fu.ID

	// A run waiting on its review, commented on.
	waiting := a.startRun(work)
	step := a.waitStep(waiting.ID, "Approve", stepWaiting)
	a.call("POST", fmt.Sprintf("/api/runs/%d/steps/%d/comments", waiting.ID, step.ID), map[string]any{"text": m + " comment"}, http.StatusCreated, nil)
	s.ids["run"], s.ids["step"] = waiting.ID, step.ID
	t.Cleanup(func() {
		db.Exec("UPDATE runs SET status = ? WHERE id = ? AND status = ?", runInterrupted, waiting.ID, runRunning)
	})
	a.project = 0
	return s
}

// The kind of thing a path's parameter names, by the segment before it.
var sweepParamKinds = map[string]string{
	"flows": "flow", "structs": "struct", "processes": "process", "objects": "object", "links": "link",
	"connections": "connection", "states": "state", "attributes": "attribute", "notes": "note",
	"runs": "run", "steps": "step", "followups": "followup", "schedules": "schedule", "webhooks": "webhook",
	"versions": "version", "taskboards": "taskboard", "teams": "team", "users": "user",
	"mcp-servers": "server", "credentials": "credential", "skills": "skill", "commands": "command",
	"api-keys": "apikey", "records": "record", "samples": "sample", "trash": "trashed", "projects": "project",
	"model-keys": "env", "model-providers": "provider", "egress-hosts": "host", "templates": "template",
	"mcp": "server", "hooks": "token",
}

// Kinds named by a name of the request's own project, or of the
// instance, rather than an id: the same name in the first project is the
// first project's own, so a path naming one may well be answered there.
var sweepOwnNames = map[string]bool{"env": true, "provider": true, "host": true, "template": true, "sample": true}

// The path of the pattern with each parameter naming side's thing of its
// kind, and whether a parameter names an id of the side's.
func sweepPath(t *testing.T, pattern string, side *sweepSide) (string, bool) {
	t.Helper()
	segs := strings.Split(pattern, "/")
	ids := false
	for i, seg := range segs {
		if !strings.HasPrefix(seg, "{") {
			continue
		}
		if seg == "{path...}" {
			segs[i] = "issues"
			continue
		}
		kind := sweepParamKinds[segs[i-1]]
		switch {
		case kind == "":
			t.Fatalf("the sweep doesn't know what %s names in %s", seg, pattern)
		case kind == "project":
			segs[i] = fmt.Sprint(side.p.ID)
			ids = true
		case kind == "template":
			segs[i] = "triage-a-ticket"
		case kind == "sample":
			segs[i] = fmt.Sprint(side.id(t, "sample"))
			ids = true
		case sweepOwnNames[kind]:
			segs[i] = side.names[kind]
		case kind == "step" && strings.Contains(pattern, "/followups"):
			segs[i] = fmt.Sprint(side.id(t, "donestep"))
			ids = true
		case kind == "run" && strings.Contains(pattern, "/followups"):
			segs[i] = fmt.Sprint(side.id(t, "donerun"))
			ids = true
		default:
			segs[i] = fmt.Sprint(side.id(t, kind))
			ids = true
		}
	}
	return strings.Join(segs, "/"), ids
}

// The path of a pattern with two parameters or more naming ids, the first
// naming lead's thing and the rest rest's; false for a pattern with fewer.
func sweepMixedPath(t *testing.T, pattern string, lead, rest *sweepSide) (string, bool) {
	t.Helper()
	full, ids := sweepPath(t, pattern, rest)
	ledBy, _ := sweepPath(t, pattern, lead)
	if !ids || strings.Count(pattern, "{") < 2 || strings.Contains(pattern, "{path...}") {
		return "", false
	}
	a, b := strings.Split(ledBy, "/"), strings.Split(full, "/")
	first := true
	for i, seg := range strings.Split(pattern, "/") {
		if strings.HasPrefix(seg, "{") {
			if first {
				// A run that ended, so what the route does with the rest
				// of the path is what answers.
				b[i] = a[i]
				if seg == "{run}" {
					b[i] = fmt.Sprint(lead.id(t, "donerun"))
				}
				first = false
			}
		}
	}
	return strings.Join(b, "/"), true
}

// A JSON body naming side's things in every field a route takes them in;
// with guard's things given, those the guard looks for (owners.go) are
// guard's instead, so the request gets past the guard to the route.
func sweepBody(t *testing.T, pattern string, side, guard *sweepSide) map[string]any {
	t.Helper()
	g := side
	if guard != nil {
		g = guard
	}
	team := map[string]any{"kind": "team", "id": side.id(t, "team")}
	switch {
	case strings.HasSuffix(pattern, "/owner"):
		return team
	case pattern == "POST /api/credentials" || pattern == "PUT /api/credentials/{id}":
		return map[string]any{"name": fmt.Sprintf("Swept %d", time.Now().UnixNano()), "baseUrl": "https://swept.example.com", "auth": "header",
			"header": "X-Api-Key", "template": "{secret}", "secret": "swept-secret-0123456789", "teams": []int64{side.id(t, "team")}}
	case pattern == "POST /api/skills" || pattern == "POST /api/commands" || pattern == "PUT /api/skills/{id}" || pattern == "PUT /api/commands/{id}":
		return map[string]any{"name": fmt.Sprintf("swept-%d", time.Now().UnixNano()), "description": "Swept.", "content": "Swept.", "owner": team}
	case pattern == "POST /api/teams" || pattern == "PATCH /api/teams/{id}":
		return map[string]any{"name": fmt.Sprintf("Swept %d", time.Now().UnixNano()), "members": []int64{side.id(t, "user")}}
	case pattern == "POST /api/objects/{id}/attributes" || pattern == "PATCH /api/attributes/{id}":
		return map[string]any{"name": fmt.Sprintf("swept%d", time.Now().UnixNano()), "ref": side.id(t, "root")}
	case pattern == "PATCH /api/users/{id}":
		return map[string]any{"roles": []string{"Swept role"}}
	case pattern == "POST /api/schedules":
		return map[string]any{"flow": g.id(t, "flow"), "cron": everySecond, "timezone": "UTC", "enabled": false}
	case pattern == "POST /api/webhooks" || pattern == "PUT /api/webhooks/{id}":
		return map[string]any{"flow": g.id(t, "flow"), "name": "Swept", "kind": "github", "events": []string{"issues"}, "mapping": map[string]any{}}
	case pattern == "PUT /api/taskboards/{id}" || pattern == "POST /api/taskboards":
		return map[string]any{"name": "Swept", "owner": team, "columns": []any{
			map[string]any{"name": "Theirs", "struct": side.id(t, "changes"), "flow": side.id(t, "flow"), "query": ""}}}
	case pattern == "PATCH /api/runs/{run}/steps/{id}":
		return map[string]any{"state": side.id(t, "state")}
	case pattern == "POST /api/runs":
		return map[string]any{"flow": g.id(t, "flow")}
	case pattern == "PATCH /api/runs/{id}":
		return map[string]any{"status": runInterrupted}
	case pattern == "POST /api/runs/{run}/steps/{id}/loop":
		return map[string]any{"goOn": true}
	case pattern == "POST /api/structs/{id}/records/delete":
		return map[string]any{"ids": []int64{side.id(t, "record")}}
	case pattern == "POST /api/flows":
		return map[string]any{"name": fmt.Sprintf("Swept %d", time.Now().UnixNano()), "owner": team}
	case pattern == "POST /api/objects":
		return map[string]any{"flow": g.id(t, "flow"), "name": "Swept", "general": side.id(t, "root")}
	case pattern == "POST /api/links":
		return map[string]any{"process": g.id(t, "process"), "object": g.id(t, "copy"), "direction": "input", "state": side.id(t, "state")}
	case pattern == "PUT /api/processes/{id}/samples" || pattern == "POST /api/processes/{id}/trial":
		return map[string]any{"name": "Swept", "inputs": []any{map[string]any{"object": side.id(t, "copy"), "content": "{}"}}}
	case pattern == "PUT /api/mcp-servers/{id}/token":
		return map[string]any{"token": "swept-token"}
	case pattern == "PUT /api/objects/{id}/follows":
		return map[string]any{"follows": map[string]any{"object": side.id(t, "copy"), "attribute": "ticket"}}
	case pattern == "PUT /api/links/{id}/states":
		return map[string]any{"states": []int64{side.id(t, "state")}}
	}
	return map[string]any{
		"flow": g.id(t, "flow"), "from": g.id(t, "flow"), "process": g.id(t, "process"), "processes": []int64{g.id(t, "process")},
		"object": g.id(t, "copy"), "objects": []int64{g.id(t, "copy")}, "parent": g.id(t, "inner"), "board": g.id(t, "inner"),
		"source": g.id(t, "post"), "target": g.id(t, "inner"), "taskBoard": g.id(t, "taskboard"),
		"settings": map[string]any{
			"flow": g.id(t, "procedure"), "mcpServers": []int64{side.id(t, "server")}, "skills": []int64{side.id(t, "skill")},
			"credentials": []int64{side.id(t, "credential")},
			"connector":   map[string]any{"server": side.id(t, "connector"), "operation": "post_message"},
		},
		"struct": side.id(t, "struct"), "records": []int64{side.id(t, "record")}, "record": side.id(t, "record"),
		"run": side.id(t, "run"), "step": side.id(t, "step"), "version": side.id(t, "version"), "to": side.id(t, "version"),
		"state": side.id(t, "state"), "states": []int64{side.id(t, "state")}, "ref": side.id(t, "root"),
		"general": side.id(t, "root"), "copyOf": side.id(t, "struct"), "attribute": side.id(t, "attribute"),
		"follows": map[string]any{"object": side.id(t, "copy"), "attribute": side.id(t, "attribute")},
		"team":    side.id(t, "team"), "teams": []int64{side.id(t, "team")}, "members": []int64{side.id(t, "user")},
		"user": side.id(t, "user"), "users": []int64{side.id(t, "user")}, "owner": map[string]any{"kind": "team", "id": side.id(t, "team")},
		"server": side.id(t, "server"), "credential": side.id(t, "credential"), "skill": side.id(t, "skill"),
		"columns": []any{map[string]any{"name": "Theirs", "struct": side.id(t, "changes"), "flow": side.id(t, "flow"), "query": ""}},
		"cards":   map[string]any{fmt.Sprint(side.id(t, "changes")): map[string]any{"id": "url"}},
		"inputs":  map[string]any{"Draft": "x"},
		"name":    "Swept", "text": "Swept", "message": "Swept", "note": "Swept", "host": "swept.example.com", "provider": "swept",
		"project": side.p.ID, "level": "editor", "roles": []string{"Swept role"},
	}
}

// The query naming side's things, as a body does; with guard's given, the
// guarded fields are guard's.
func sweepQuery(t *testing.T, side, guard *sweepSide) string {
	t.Helper()
	g := side
	if guard != nil {
		g = guard
	}
	return fmt.Sprintf("flow=%d&process=%d&object=%d&taskBoard=%d&run=%d&to=%d&records=%d&struct=%d&version=%d&path=%s",
		g.id(t, "flow"), g.id(t, "process"), g.id(t, "copy"), g.id(t, "taskboard"), side.id(t, "run"), side.id(t, "version"),
		side.id(t, "record"), side.id(t, "struct"), side.id(t, "version"), fmt.Sprintf("/data/files/runs/%d/swept.txt", side.id(t, "run")))
}

// A checksum of each table's rows of the project, by table.
func sweepChecksums(t *testing.T, db *sql.DB, project int64) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT c.TABLE_NAME, c.COLUMN_NAME FROM information_schema.COLUMNS c
		JOIN information_schema.COLUMNS p ON p.TABLE_SCHEMA = c.TABLE_SCHEMA AND p.TABLE_NAME = c.TABLE_NAME AND p.COLUMN_NAME = 'project_id'
		JOIN information_schema.TABLES tb ON tb.TABLE_SCHEMA = c.TABLE_SCHEMA AND tb.TABLE_NAME = c.TABLE_NAME AND tb.TABLE_TYPE = 'BASE TABLE'
		WHERE c.TABLE_SCHEMA = DATABASE() ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`)
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string][]string{}
	var tables []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		if _, ok := columns[table]; !ok {
			tables = append(tables, table)
		}
		// Each column as text, a NULL apart from an empty one.
		columns[table] = append(columns[table], fmt.Sprintf("COALESCE(CAST(`%s` AS CHAR), '\\N')", column))
	}
	rows.Close()
	var parts []string
	var args []any
	for _, table := range tables {
		// Grants end, and are swept, on a timer, whatever requests come.
		if table == "proxy_grants" {
			continue
		}
		parts = append(parts, fmt.Sprintf("SELECT '%s', COUNT(*), COALESCE(BIT_XOR(CRC32(CONCAT_WS('#', %s))), 0) FROM `%s` WHERE project_id = ?",
			table, strings.Join(columns[table], ", "), table))
		args = append(args, project)
	}
	sums := map[string]string{}
	rows, err = db.Query(strings.Join(parts, " UNION ALL "), args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var count, sum int64
		if err := rows.Scan(&table, &count, &sum); err != nil {
			t.Fatal(err)
		}
		sums[table] = fmt.Sprintf("%d/%d", count, sum)
	}
	return sums
}

// The tables whose rows differ between two checksums.
func sweepChanged(before, after map[string]string) []string {
	var changed []string
	for table, sum := range after {
		if before[table] != sum {
			changed = append(changed, table)
		}
	}
	slices.Sort(changed)
	return changed
}

// Waits until the project's rows stay as they are for a second, what the
// background writes of its runs and audit log settle to, and returns them.
func sweepSettled(t *testing.T, db *sql.DB, project int64) map[string]string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	last := sweepChecksums(t, db, project)
	for {
		time.Sleep(time.Second)
		now := sweepChecksums(t, db, project)
		if len(sweepChanged(last, now)) == 0 {
			return now
		}
		if time.Now().After(deadline) {
			t.Fatalf("the second project's rows keep changing: %v", sweepChanged(last, now))
		}
		last = now
	}
}

// Rows of the project naming the other's things where no key checks them:
// a process's servers, skills, credentials, connector and procedure, a
// copy's store, a reference's struct, records a run took or was started
// on, runs inside or of another, a record's struct.
func sweepCrossReferences(t *testing.T, db *sql.DB, project, other int64) []string {
	t.Helper()
	var found []string
	for what, q := range map[string]string{
		"a process naming another project's MCP server": `SELECT COUNT(*) FROM processes p JOIN mcp_servers s ON s.project_id = ?
			AND (JSON_CONTAINS(COALESCE(JSON_EXTRACT(p.settings, '$.mcpServers'), JSON_ARRAY()), CAST(s.id AS JSON))
				OR JSON_EXTRACT(p.settings, '$.connector.server') = s.id) WHERE p.project_id = ?`,
		"a process naming another project's skill": `SELECT COUNT(*) FROM processes p JOIN skills s ON s.project_id = ?
			AND JSON_CONTAINS(COALESCE(JSON_EXTRACT(p.settings, '$.skills'), JSON_ARRAY()), CAST(s.id AS JSON)) WHERE p.project_id = ?`,
		"a process naming another project's credential": `SELECT COUNT(*) FROM processes p JOIN credentials c ON c.project_id = ?
			AND JSON_CONTAINS(COALESCE(JSON_EXTRACT(p.settings, '$.credentials'), JSON_ARRAY()), CAST(c.id AS JSON)) WHERE p.project_id = ?`,
		"a subflow process running another project's procedure": `SELECT COUNT(*) FROM processes p JOIN flows f ON f.project_id = ?
			AND JSON_EXTRACT(p.settings, '$.flow') = f.id WHERE p.project_id = ?`,
		"an object keeping another project's store":            `SELECT COUNT(*) FROM objects o JOIN objects s ON s.id = o.store_id AND s.project_id = ? WHERE o.project_id = ?`,
		"an attribute referring to another project's struct":   `SELECT COUNT(*) FROM object_attributes a JOIN objects s ON s.id = a.ref_id AND s.project_id = ? WHERE a.project_id = ?`,
		"a record of another project's struct":                 `SELECT COUNT(*) FROM shared_records r JOIN objects s ON s.id = r.object_id AND s.project_id = ? WHERE r.project_id = ?`,
		"a record taken by a run of another project":           `SELECT COUNT(*) FROM taken_records tr JOIN shared_records r ON r.id = tr.record_id AND r.project_id = ? WHERE tr.project_id = ?`,
		"a board's run on another project's record":            `SELECT COUNT(*) FROM task_board_runs tr JOIN shared_records r ON r.id = tr.record_id AND r.project_id = ? WHERE tr.project_id = ?`,
		"a run on another project's record":                    `SELECT COUNT(*) FROM runs x JOIN shared_records r ON r.id = x.record_id AND r.project_id = ? WHERE x.project_id = ?`,
		"a run inside, re-running or trying another project's": `SELECT COUNT(*) FROM runs x JOIN runs o ON o.id IN (x.parent_run_id, x.rerun_of, x.trial_of) AND o.project_id = ? WHERE x.project_id = ?`,
		"a step's run in another project":                      `SELECT COUNT(*) FROM run_steps s JOIN runs o ON o.id = s.child_run_id AND o.project_id = ? WHERE s.project_id = ?`,
	} {
		var n int
		if err := db.QueryRow(q, other, project).Scan(&n); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if n > 0 {
			found = append(found, fmt.Sprintf("%d of %s", n, what))
		}
	}
	slices.Sort(found)
	return found
}

// What a route answers, with 409, of a step, a review or a follow-up that
// isn't the run's, as of one that isn't waiting or going: it says nothing
// of the other project's.
var sweepNotWaiting = regexp.MustCompile(`^no (review waiting|loop step waiting|step going|follow-up going|step) with that id`)

// Who sweeps: how their requests go out, and whether they are the
// instance's (a super admin, or anyone when nobody signs in).
type sweeper struct {
	name     string
	a        *app
	instance bool
	// A super admin, who enters every project, the second too.
	super bool
}

// Sends a request with the first project's header, as the browser's tab on
// it does, giving up after a while rather than hanging the sweep.
func sweepSend(t *testing.T, s sweeper, method, path string, body any) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.a.url+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	s.a.inProject(req)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := s.a.client.Do(req.WithContext(ctx))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(reply)
}

func TestProjectsSweepEveryRoute(t *testing.T) {
	routes := webappRoutes(t)
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	db := secretsDB(t)
	a.worker.onScript("Move", func(_ context.Context, run scriptRun) runResult {
		for _, in := range run.Inputs {
			if strings.HasSuffix(in.Name, " Ticket") {
				var fields map[string]string
				if err := json.Unmarshal([]byte(in.Content), &fields); err != nil {
					return runResult{Error: err.Error()}
				}
				out, _ := json.Marshal(fields)
				return runResult{OK: true, Output: string(out)}
			}
		}
		return runResult{Error: fmt.Sprintf("given no ticket but %+v", run.Inputs)}
	})
	a.worker.onScript("Mark", func(context.Context, scriptRun) runResult {
		return runResult{OK: true, Output: fmt.Sprintf(`{"id": "S-%d"}`, time.Now().UnixNano())}
	})
	a.worker.onScript("Sum", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"data": "in short"}`} })
	a.worker.onAgent("Write", func(_ context.Context, run agentRun) runResult {
		if run.Resume {
			return runResult{OK: true, Output: "again"}
		}
		return submitted(data("the draft"))
	})
	// The assistant's route is swept too; its reply fails rather than
	// bring a setup file that would change the boards being swept.
	a.worker.onAgent("Assistant", func(context.Context, agentRun) runResult {
		return runResult{Error: "the sweep asks the assistant nothing"}
	})
	one := buildSweepSide(t, a, db, "One")
	two := buildSweepSide(t, a, db, "Two")
	olga := usersNewPerson("Olga")
	o := usersAdd(a, olga.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", one.p.ID), []map[string]any{{"user": o.ID, "level": "owner"}}, http.StatusOK, nil)

	// Nobody signing in, on another webapp of the same database.
	nobody := startApp(t, a.worker.url)
	nobody.worker = a.worker
	owner, super := *a, *a
	owner.client, owner.t, super.t, nobody.t = nil, t, t, t
	usersSignIn(&owner, prov, olga)
	super.client = a.client
	sweepers := []sweeper{{"the first project's owner", &owner, false, false}, {"a super admin", &super, true, true}, {"nobody signing in", nobody, true, false}}
	for _, s := range sweepers {
		s.a.project = one.p.ID
	}

	baseline := sweepSettled(t, db, two.p.ID)
	before := baseline
	holes := map[string]bool{}
	hole := func(format string, args ...any) {
		t.Helper()
		msg := fmt.Sprintf(format, args...)
		if !holes[msg] {
			holes[msg] = true
			t.Error(msg)
		}
	}
	check := func(s sweeper, pattern, method, path string, status int, reply string, mustRefuse bool) {
		t.Helper()
		if status == http.StatusMethodNotAllowed || status == http.StatusConflict && sweepNotWaiting.MatchString(reply) {
			status = http.StatusNotFound
		}
		if status >= 500 || status == 0 {
			hole("%s: %s %s = %d %.300s, an error rather than an answer", s.name, method, path, status, reply)
		}
		if mustRefuse && status != http.StatusBadRequest && status != http.StatusForbidden && status != http.StatusNotFound && status != http.StatusUnauthorized {
			hole("%s: %s %s naming the second project's thing = %d %.300s, want 404, 403 or 400", s.name, method, path, status, reply)
		}
		if strings.Contains(reply, two.marker) && !strings.Contains(path, two.marker) && !(s.instance && sweepInstance(pattern)) && !(s.super && slices.Contains(sweepAcrossForSuper, pattern)) {
			hole("%s: %s %s answered with the second project's things: %.300s", s.name, method, path, reply)
		}
		after := sweepChecksums(t, db, two.p.ID)
		if changed := sweepChanged(before, after); len(changed) > 0 {
			hole("%s: %s %s changed the second project's %v", s.name, method, path, changed)
			before = after
		}
	}

	swept := 0
	shapes := map[string]string{}
	send := func(s sweeper, pattern, method, path string, body any, mustRefuse bool) (int, string) {
		t.Helper()
		status, reply := sweepSend(t, s, method, path, body)
		check(s, pattern, method, path, status, reply, mustRefuse)
		swept++
		return status, reply
	}
	for _, s := range sweepers {
		for _, pattern := range routes {
			if _, skip := sweepSkipped[pattern]; skip {
				continue
			}
			method, path, _ := strings.Cut(pattern, " ")
			// The proxies are swept below, with grants rather than sessions.
			if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/v1/") && path != "/health" {
				continue
			}
			if s.instance && sweepInstance(pattern) {
				continue
			}
			if strings.HasPrefix(path, "/v1/") {
				// The first project's API key, with the second's ids.
				p, _ := sweepPath(t, path, two)
				status, reply := apiCall(s.a, one.key, method, p, sweepBody(t, pattern, two, nil))
				check(s, pattern, method, p, status, string(reply), true)
				swept++
				continue
			}
			// The second project's ids in the path, the query and the
			// body, which the guard turns away (owners.go).
			p, ids := sweepPath(t, path, two)
			send(s, pattern, method, p+"?"+sweepQuery(t, two, nil), sweepBody(t, pattern, two, nil), ids)
			// The second project's ids in the path, and the first's in what
			// the guard reads of the query and the body, so the route's own
			// checks answer.
			if ids {
				send(s, pattern, method, p+"?"+sweepQuery(t, two, one), sweepBody(t, pattern, two, one), true)
			}
			// The first project's id where a path leads with what it is
			// about, the second's in the rest of it.
			if mixed, ok := sweepMixedPath(t, path, one, two); ok {
				send(s, pattern, method, mixed+"?"+sweepQuery(t, two, one), sweepBody(t, pattern, two, one), true)
			}
			// The first project's own ids where the route names what it is
			// about, the second's in every other field. What deletes the
			// first project's own things, naming no other, is left be.
			if method == http.MethodDelete {
				continue
			}
			p, _ = sweepPath(t, path, one)
			status, reply := send(s, pattern, method, p+"?"+sweepQuery(t, two, one), sweepBody(t, pattern, two, one), false)
			if status == http.StatusBadRequest {
				shapes[pattern] = reply
			}
		}
	}
	// What the routes said of the bodies they refused, for whoever extends
	// the sweep's bodies: a refusal of the body's shape is no proof.
	for pattern, reply := range shapes {
		t.Logf("%s refused the first project's ids with the second's in its fields: %.200s", pattern, reply)
	}
	if found := sweepCrossReferences(t, db, one.p.ID, two.p.ID); len(found) > 0 {
		hole("the first project names the second's things where no key checks it: %v", found)
	}
	t.Logf("swept %d requests over %d routes", swept, len(routes))

	// The proxies, with grants of the first project reaching the second's
	// providers, servers and credentials as if given them.
	var replica string
	if err := db.QueryRow("SELECT id FROM replicas ORDER BY seen_at DESC LIMIT 1").Scan(&replica); err != nil {
		t.Fatal(err)
	}
	plant := func(kind string, reaches any) string {
		t.Helper()
		token := fmt.Sprintf("swept-%s-%d", kind, time.Now().UnixNano())
		sum := sha256.Sum256([]byte(token))
		hash := hex.EncodeToString(sum[:])
		data, _ := json.Marshal(reaches)
		if _, err := db.Exec(`INSERT INTO proxy_grants (token_hash, kind, project_id, reaches, replica_id, expires_at)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP(3) + INTERVAL 1 HOUR)`, hash, kind, one.p.ID, data, replica); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec("DELETE FROM proxy_grants WHERE token_hash = ?", hash) })
		return token
	}
	tokens := map[string]string{
		"llm":         plant("model", map[string]any{"providers": []string{two.names["provider"]}}),
		"mcp":         plant("mcp", map[string]any{"servers": map[string]any{fmt.Sprint(two.ids["server"]): nil, fmt.Sprint(two.ids["connector"]): nil}}),
		"credentials": plant("credential", map[string]any{"credentials": []int64{two.ids["credential"]}, "flow": one.ids["flow"]}),
	}
	proxied := regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|OPTIONS) /(llm|mcp|credentials)/`)
	for _, pattern := range routes {
		m := proxied.FindStringSubmatch(pattern)
		if m == nil {
			continue
		}
		method, path, _ := strings.Cut(pattern, " ")
		for _, id := range []int64{two.ids["server"], two.ids["connector"]} {
			p := path
			switch m[2] {
			case "llm":
				p = strings.Replace(p, "{provider}", two.names["provider"], 1)
			case "mcp":
				p = strings.Replace(p, "{id}", fmt.Sprint(id), 1)
			case "credentials":
				p = strings.Replace(p, "{id}", fmt.Sprint(two.ids["credential"]), 1)
			}
			p = strings.Replace(p, "{path...}", "v1/messages", 1)
			req, _ := http.NewRequest(method, a.url+p, strings.NewReader(`{"model": "gw-1", "jsonrpc": "2.0", "id": 1, "method": "tools/list"}`))
			req.Header.Set("Authorization", "Bearer "+tokens[m[2]])
			req.Header.Set("x-api-key", tokens[m[2]])
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			reply, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			s := sweeper{name: "a grant of the first project", instance: false}
			check(s, pattern, method, p, resp.StatusCode, string(reply), true)
			// The provider's key and the credential's secret, which only
			// reaching them would echo.
			if strings.Contains(string(reply), two.marker+"-key-") || strings.Contains(string(reply), two.marker+"-secret-") {
				hole("the first project's grant reached the second's %s at %s: %.300s", m[2], p, reply)
			}
			if m[2] != "mcp" {
				break
			}
		}
	}
	if changed := sweepChanged(baseline, sweepChecksums(t, db, two.p.ID)); len(changed) > 0 {
		t.Errorf("the second project's %v changed over the sweep", changed)
	}
}
