package tests

// What someone may do in a project is what their level there lets them do
// now, whatever they could when they set something going: an API key, a
// webhook and a task board playing do no more than their maker may now; a
// viewer connects no MCP server, though the browser opens it as a page;
// anyone deletes their own key whatever their level; and someone asking
// about a project they may not enter is told whom to ask. See projects.go,
// api.go and run_projects.go.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// A key, a webhook and a board of someone who was an editor start nothing
// once they are a viewer, saying why, though the key still reads what a
// viewer reads; and they delete their key all the same.
func TestProjectsWhatTheyStartDoesNoMoreThanTheyMayNow(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	db := secretsDB(t)
	n := time.Now().UnixNano()
	a.worker.onScript("Handle", func(context.Context, scriptRun) runResult { return runResult{OK: true, Output: `{"data": "handled"}`} })
	p := newProject(a, "Demoted")
	a.project = p.ID
	bob := usersNewPerson("Bob")
	bobRow := usersAdd(a, bob.email)
	issues := prIssueFlow(a, fmt.Sprintf("Demoted issues %d", n))
	usersShare(a, issues.ID)
	pubPublish(a, issues.ID, "issues")

	// As an editor, Bob makes a key, adds a webhook and plays a board.
	usersSignIn(a, prov, bob)
	key := apiNewKey(a, "Bob's")
	body := map[string]any{"inputs": map[string]any{"Issue": "Printer jam"}}
	a.finish(apiStart(a, key.Key, issues.ID, body, http.StatusCreated).ID)
	h := hooksAdd(a, issues.ID, map[string]any{"name": "Issues", "kind": "github", "events": []string{"issues"},
		"mapping": map[string]any{"Issue": map[string]any{"path": "issue.title"}}})
	var board playBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Bob's board"}, http.StatusCreated, &board)
	t.Cleanup(func() { db.Exec("UPDATE task_boards SET playing = FALSE WHERE id = ?", board.ID) })
	a.call("POST", fmt.Sprintf("/api/taskboards/%d/play", board.ID), nil, http.StatusOK, &board)

	// Made a viewer, nothing of his starts anything.
	usersSignIn(a, prov, usersAda)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", p.ID), []map[string]any{{"user": bobRow.ID, "level": "viewer"}}, http.StatusOK, nil)
	apiRefused(a, key.Key, "POST", fmt.Sprintf("/v1/flows/%d/runs", issues.ID), body, http.StatusForbidden, "only sees its project now")
	if status, reply := apiCall(a, key.Key, "GET", fmt.Sprintf("/v1/flows/%d", issues.ID), nil); status != http.StatusOK {
		t.Errorf("the viewer's key describing the flow = %d %s, want 200", status, reply)
	}
	if status, reply := hookGitHub(a, h, h.Secret, "issues", "v-1", hookIssue(1, "Jam", "opened")); status != http.StatusForbidden ||
		!strings.Contains(string(reply), "only sees the project now") {
		t.Errorf("a delivery to the viewer's webhook = %d %s, want it refused as he only sees the project", status, reply)
	}
	b := waitBoard(a, board.ID, "the board to stop", func(b playBoard) bool { return !b.Playing })
	if !strings.Contains(b.Stopped, "only sees the project now") {
		t.Errorf("the board stopped saying %q", b.Stopped)
	}

	// A viewer still deletes his own key, which is then none.
	usersSignIn(a, prov, bob)
	a.call("DELETE", fmt.Sprintf("/api/api-keys/%d", key.ID), nil, http.StatusNoContent, nil)
	apiRefused(a, key.Key, "GET", fmt.Sprintf("/v1/flows/%d", issues.ID), nil, http.StatusUnauthorized, "isn't one")
	a.refused("POST", "/api/api-keys", map[string]any{"name": "Another"}, http.StatusForbidden, "you only see this project")
}

// A viewer may not connect the project's MCP server, which changes what
// its steps use on their behalf, though the browser opens it with GET; an
// editor may. Someone asking about a project they may not enter is told
// whom to ask, as someone in none is.
func TestProjectsViewersConnectNothingAndOutsidersAreToldWhomToAsk(t *testing.T) {
	a, prov := usersApp(t)
	usersSignIn(a, prov, usersAda)
	m := newSettingsMCP(t)
	p := newProject(a, "Connecting")
	a.project = p.ID
	name := fmt.Sprintf("viewers_mcp_%d", time.Now().UnixNano())
	forgetMCPServers(t, name)
	server := psAddServer(a, map[string]any{"name": name, "url": m.srv.URL + "/mcp"})
	vera, ed, nobu := usersNewPerson("Vera"), usersNewPerson("Ed"), usersNewPerson("Nobu")
	v, e := usersAdd(a, vera.email), usersAdd(a, ed.email)
	a.call("PUT", fmt.Sprintf("/api/projects/%d/members", p.ID), []map[string]any{{"user": v.ID, "level": "viewer"}, {"user": e.ID, "level": "editor"}},
		http.StatusOK, nil)

	usersSignIn(a, prov, vera)
	resp, err := a.client.Get(a.pageURL(fmt.Sprintf("/api/mcp-servers/%d/connect", server)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "you only see this project") {
		t.Errorf("the viewer connecting the project's MCP server got %d %s", resp.StatusCode, body)
	}
	usersSignIn(a, prov, ed)
	psConnect(a, m, p.ID, server, "ed-token")

	// Nobu, in Legacy alone, asks about the project.
	usersSignIn(a, prov, usersAda)
	a.project = 0
	usersAdd(a, nobu.email)
	usersSignIn(a, prov, nobu)
	var me struct {
		Projects []projectRow `json:"projects"`
		Admins   []string     `json:"admins"`
	}
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if me.Admins != nil {
		t.Errorf("someone asking about a project they may enter is told whom to ask: %q", me.Admins)
	}
	a.project = p.ID
	a.call("GET", "/api/me", nil, http.StatusOK, &me)
	if len(me.Projects) != 1 || !slices.Contains(me.Admins, "Ada Lovelace") {
		t.Errorf("someone asking about a project they may not enter is told %+v, want Ada to ask", me)
	}
}
