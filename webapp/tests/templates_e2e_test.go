package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A template as the gallery lists it; see templates.go.
type tplTemplate struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Flow        string `json:"flow"`
	Steps       []struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	} `json:"steps"`
	Needs []tplNeed `json:"needs"`
}

type tplNeed struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Title string `json:"title"`
	Met   bool   `json:"met"`
}

// What GET /api/flows/{id}/problems answers; see problems.go.
type prbProblems struct {
	Problems  []prbProblem `json:"problems"`
	Published *struct {
		Version int64        `json:"version"`
		Errors  []prbProblem `json:"errors"`
	} `json:"published"`
}

type prbProblem struct {
	Level   string `json:"level"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
	Process *int64 `json:"process"`
	Object  *int64 `json:"object"`
	Board   *int64 `json:"board"`
}

func prbGet(a *app, f int64) prbProblems {
	a.t.Helper()
	var ps prbProblems
	a.call("GET", fmt.Sprintf("/api/flows/%d/problems", f), nil, http.StatusOK, &ps)
	return ps
}

func prbErrors(ps []prbProblem) []prbProblem {
	var out []prbProblem
	for _, p := range ps {
		if p.Level == "error" {
			out = append(out, p)
		}
	}
	return out
}

func prbDescribe(ps []prbProblem) string {
	var lines []string
	for _, p := range ps {
		lines = append(lines, fmt.Sprintf("%s %s: %s", p.Level, p.Rule, p.Message))
	}
	return strings.Join(lines, "; ")
}

// Whether a problem of the rule and level is about the process or object
// id, and says says.
func prbHas(ps []prbProblem, level, rule string, id int64, says string) bool {
	return slices.ContainsFunc(ps, func(p prbProblem) bool {
		about := (p.Process != nil && *p.Process == id) || (p.Object != nil && *p.Object == id) || (id == 0 && p.Process == nil && p.Object == nil)
		return p.Level == level && p.Rule == rule && about && strings.Contains(p.Message, says)
	})
}

func tplList(a *app) []tplTemplate {
	a.t.Helper()
	var ts []tplTemplate
	a.call("GET", "/api/templates", nil, http.StatusOK, &ts)
	return ts
}

func tplFind(a *app, id string) tplTemplate {
	a.t.Helper()
	for _, t := range tplList(a) {
		if t.ID == id {
			return t
		}
	}
	a.t.Fatalf("no template %s", id)
	return tplTemplate{}
}

// Makes a flow of the template, which must succeed.
func tplUse(a *app, id string, body any) flow {
	a.t.Helper()
	var f flow
	a.call("POST", "/api/templates/"+id, body, http.StatusCreated, &f)
	return f
}

// A process with what a connector process carries out.
type tplProcess struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Settings struct {
		Connector *struct {
			Server    int64             `json:"server"`
			Operation string            `json:"operation"`
			Args      map[string]string `json:"args"`
		} `json:"connector"`
	} `json:"settings"`
}

// The flow's processes and objects, by name.
func tplBoard(a *app, f int64) (map[string]tplProcess, map[string]object) {
	a.t.Helper()
	var ps []tplProcess
	a.call("GET", fmt.Sprintf("/api/processes?flow=%d", f), nil, http.StatusOK, &ps)
	var os []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f), nil, http.StatusOK, &os)
	procs, objs := map[string]tplProcess{}, map[string]object{}
	for _, p := range ps {
		procs[p.Name] = p
	}
	for _, o := range os {
		objs[o.Name] = o
	}
	return procs, objs
}

func tplState(t *testing.T, o object, name string) int64 {
	t.Helper()
	for _, s := range o.States {
		if s.Name == name {
			return s.ID
		}
	}
	t.Fatalf("%s has no state %s", o.Name, name)
	return 0
}

// Fills in what the person entering it gives, by object.
func tplEnter(a *app, run, step int64, o object, v any) {
	a.t.Helper()
	humanMustDecide(a, run, step, map[string]any{"entries": map[string]any{fmt.Sprint(o.ID): v}})
}

// A Slack that lists a channel's messages and takes what is posted.
type tplSlack struct {
	srv    *httptest.Server
	mu     sync.Mutex
	posted []map[string]any
}

func newTplSlack(t *testing.T) *tplSlack {
	s := &tplSlack{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.history":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": []map[string]any{
				{"user": "U1", "text": "We ship on Friday.", "ts": "1730000000.000100", "reply_count": 2},
				{"user": "U2", "text": "Who reviews the release notes?", "ts": "1730000100.000100"},
			}})
		case "/chat.postMessage":
			var msg map[string]any
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &msg)
			s.mu.Lock()
			s.posted = append(s.posted, msg)
			s.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": msg["channel"], "ts": "1730000200.000100"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// Installs the Slack connector at the fake, with its token, and returns
// its id.
func (s *tplSlack) install(a *app, name string) int64 {
	a.t.Helper()
	forgetMCPServers(a.t, name)
	var server struct {
		ID int64 `json:"id"`
	}
	a.call("POST", "/api/mcp-servers", map[string]any{"name": name, "connector": "slack", "url": s.srv.URL}, http.StatusCreated, &server)
	a.call("PUT", fmt.Sprintf("/api/mcp-servers/%d/token", server.ID), map[string]any{"token": "xoxb-templates"}, http.StatusNoContent, nil)
	return server.ID
}

// Gives a connector process's operation an argument, keeping the rest.
func tplArg(a *app, p tplProcess, name, value string) {
	a.t.Helper()
	var cs map[string]any
	raw, _ := json.Marshal(p.Settings.Connector)
	json.Unmarshal(raw, &cs)
	args, _ := cs["args"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	args[name] = value
	cs["args"] = args
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", p.ID), map[string]any{"settings": map[string]any{"connector": cs}}, http.StatusOK, nil)
}

func noSlackInstalled(t *testing.T) {
	t.Helper()
	var n int
	if err := secretsDB(t).QueryRow("SELECT COUNT(*) FROM mcp_servers WHERE connector = 'slack'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d Slack connectors are installed by other tests", n)
	}
}

// The gallery lists between five and seven templates, each saying what it
// is and what it needs: the repositories its processes work on, its
// connectors, whether installed, and its roles, which without sign-in
// anyone holds.
func TestTemplatesListWhatTheyNeed(t *testing.T) {
	a := newApp(t)
	noSlackInstalled(t)
	ts := tplList(a)
	if len(ts) < 5 || len(ts) > 7 {
		t.Fatalf("%d templates", len(ts))
	}
	for _, tp := range ts {
		if tp.Title == "" || tp.Description == "" || tp.Flow == "" || len(tp.Steps) == 0 {
			t.Errorf("template %+v", tp)
		}
	}
	needs := map[string][]tplNeed{}
	for _, tp := range ts {
		needs[tp.ID] = tp.Needs
	}
	if want := []tplNeed{{Kind: "repository", Name: "app"}}; !slices.Equal(needs["triage-a-ticket"], want) {
		t.Errorf("triage needs %+v, want %+v", needs["triage-a-ticket"], want)
	}
	if want := []tplNeed{{Kind: "connector", Name: "slack", Title: "Slack"}}; !slices.Equal(needs["daily-digest-to-slack"], want) {
		t.Errorf("the digest needs %+v, want %+v", needs["daily-digest-to-slack"], want)
	}
	if want := []tplNeed{{Kind: "repository", Name: "app"}, {Kind: "role", Name: "Tech lead", Met: true}}; !slices.Equal(needs["fix-with-approval-by-risk"], want) {
		t.Errorf("the fix needs %+v, want %+v", needs["fix-with-approval-by-risk"], want)
	}
	slack := newTplSlack(t)
	slack.install(a, "tpl_slack_listed")
	if n := tplFind(a, "daily-digest-to-slack").Needs; len(n) != 1 || !n[0].Met {
		t.Errorf("with Slack installed the digest needs %+v", n)
	}
	a.refused("POST", "/api/templates/no-such-template", nil, http.StatusNotFound, "template not found")
}

// Each template makes a flow of its own that a run goes through end to
// end, with what it needs given: its errors, if any, are only what is the
// app's to set up, and once that is, Run starts it.
func TestEachTemplateRuns(t *testing.T) {
	t.Run("triage a ticket", func(t *testing.T) {
		a := newApp(t)
		f := tplUse(a, "triage-a-ticket", nil)
		procs, objs := tplBoard(a, f.ID)
		ps := prbGet(a, f.ID)
		if len(prbErrors(ps.Problems)) != 0 || !prbHas(ps.Problems, "warning", "repository", procs["Attempt fix"].ID, `repository "app"`) {
			t.Errorf("problems: %s", prbDescribe(ps.Problems))
		}
		a.worker.onAgent("Classify", handsIn(decisionsReply(data("It crashes, so it is a bug."), map[string]any{"Triage": decisionsState("bug")})))
		a.worker.onAgent("Attempt fix", handsIn(map[string]any{"cause": "A nil session.", "change": "Checked it, with a test."}))
		a.worker.onAgent("Summarize", handsIn(data("Fixed the crash on sign-in.")))
		rn := a.startRun(f.ID)
		enter := a.waitStep(rn.ID, "Enter ticket", stepWaiting)
		tplEnter(a, rn.ID, enter.ID, objs["Ticket"], map[string]any{"summary": "Signing in crashes", "description": "Every time."})
		d := a.finish(rn.ID)
		if d.Status != runDone || len(stepsOf(d, "Write spec")) > 0 && onlyStep(t, d, "Write spec").Status != "skipped" {
			t.Fatalf("run %s: %s", d.Status, describeSteps(d))
		}
		if got := contentsOf(d, "Summary"); len(got) != 1 || !strings.Contains(got[0], "crash") {
			t.Errorf("Summary held %q; steps %s", got, describeSteps(d))
		}
	})
	t.Run("write and critique", func(t *testing.T) {
		a := newApp(t)
		f := tplUse(a, "write-and-critique", nil)
		_, objs := tplBoard(a, f.ID)
		if ps := prbGet(a, f.ID); len(ps.Problems) != 0 {
			t.Errorf("problems: %s", prbDescribe(ps.Problems))
		}
		a.worker.onAgent("Write", handsIn(data("A draft.")))
		a.worker.onAgent("Critique", handsIn(decisionsReply(nil, map[string]any{"Draft": decisionsState("good")})))
		rn := a.startRun(f.ID)
		brief := a.waitStep(rn.ID, "Give the brief", stepWaiting)
		tplEnter(a, rn.ID, brief.ID, objs["Brief"], map[string]any{"topic": "Search", "audience": "Users", "length": "500"})
		d := a.finish(rn.ID)
		if d.Status != runDone || onlyStep(t, d, "Critique").Outcome != "good" || len(stepsOf(d, "Editor decides")) != 0 {
			t.Fatalf("run %s: %s", d.Status, describeSteps(d))
		}
		// The editor decides once the critic gives up.
		a.worker.onAgent("Critique", func(context.Context, agentRun) runResult { return runResult{Error: "can't tell"} })
		rn = a.startRun(f.ID)
		brief = a.waitStep(rn.ID, "Give the brief", stepWaiting)
		tplEnter(a, rn.ID, brief.ID, objs["Brief"], map[string]any{"topic": "Search", "audience": "Users", "length": "500"})
		editor := a.waitStep(rn.ID, "Editor decides", stepWaiting)
		if !slices.ContainsFunc(editor.Inputs, func(in runInput) bool { return in.Name == "Draft" && in.Content != "" }) {
			t.Errorf("the editor is shown %+v", editor.Inputs)
		}
		humanMustDecide(a, rn.ID, editor.ID, map[string]any{"state": tplState(t, objs["Draft"], "good")})
		d = a.finish(rn.ID)
		if got := contentsOf(d, "Draft"); d.Status != runDone || len(got) == 0 {
			t.Fatalf("run %s, Draft %q: %s", d.Status, got, describeSteps(d))
		}
	})
	t.Run("fix with an approval by risk", func(t *testing.T) {
		a := newApp(t)
		f := tplUse(a, "fix-with-approval-by-risk", nil)
		procs, objs := tplBoard(a, f.ID)
		ps := prbGet(a, f.ID)
		if len(prbErrors(ps.Problems)) != 0 || !prbHas(ps.Problems, "warning", "repository", procs["Fix"].ID, `repository "app"`) {
			t.Errorf("problems: %s", prbDescribe(ps.Problems))
		}
		a.worker.onAgent("Fix", handsIn(map[string]any{"cause": "Rounding.", "change": "Rounded once.", "risk": 4}))
		a.worker.onAgent("Open pull request", handsIn(map[string]any{"url": "https://example.com/pr/1"}))
		rn := a.startRun(f.ID)
		report := a.waitStep(rn.ID, "Report bug", stepWaiting)
		tplEnter(a, rn.ID, report.ID, objs["Bug"], map[string]any{"summary": "Totals are off", "steps": "Add two items."})
		approve := a.waitStep(rn.ID, "Approve risky fix", stepWaiting)
		humanMustDecide(a, rn.ID, approve.ID, map[string]any{"state": tplState(t, objs["Patch"], "approved")})
		d := a.finish(rn.ID)
		if got := contentsOf(d, "Pull request"); d.Status != runDone || len(got) != 1 || !strings.Contains(got[0], "pr/1") {
			t.Fatalf("run %s, pull request %q: %s", d.Status, got, describeSteps(d))
		}
		// A fix of low risk goes straight to its pull request.
		a.worker.onAgent("Fix", handsIn(map[string]any{"cause": "A typo.", "change": "Spelt it right.", "risk": 1}))
		rn = a.startRun(f.ID)
		report = a.waitStep(rn.ID, "Report bug", stepWaiting)
		tplEnter(a, rn.ID, report.ID, objs["Bug"], map[string]any{"summary": "A typo", "steps": "Open the page."})
		d = a.finish(rn.ID)
		if d.Status != runDone || len(stepsOf(d, "Approve risky fix")) != 0 || len(stepsOf(d, "Open pull request")) != 1 {
			t.Fatalf("run %s: %s", d.Status, describeSteps(d))
		}
	})
	t.Run("daily digest to Slack", func(t *testing.T) {
		a := newApp(t)
		slack := newTplSlack(t)
		slack.install(a, "tpl_slack_digest")
		f := tplUse(a, "daily-digest-to-slack", nil)
		procs, _ := tplBoard(a, f.ID)
		// Which channels is the person's to say; until then it can't run.
		ps := prbGet(a, f.ID)
		for _, name := range []string{"Read the channel", "Post the digest"} {
			if !prbHas(ps.Problems, "error", "connector", procs[name].ID, "needs its channel") {
				t.Errorf("%s isn't said to need its channel: %s", name, prbDescribe(ps.Problems))
			}
		}
		a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, "needs its channel")
		tplArg(a, procs["Read the channel"], "channel", "C0123456789")
		tplArg(a, procs["Post the digest"], "channel", "C0987654321")
		if ps := prbGet(a, f.ID); len(ps.Problems) != 0 {
			t.Errorf("problems: %s", prbDescribe(ps.Problems))
		}
		// Its schedule waits to be turned on.
		var scheds []struct {
			Enabled bool `json:"enabled"`
		}
		a.call("GET", fmt.Sprintf("/api/schedules?flow=%d", f.ID), nil, http.StatusOK, &scheds)
		if len(scheds) != 1 || scheds[0].Enabled {
			t.Errorf("schedules %+v, want one turned off", scheds)
		}
		a.worker.onAgent("Write the digest", func(_ context.Context, run agentRun) runResult {
			if !strings.Contains(run.Prompt, "We ship on Friday.") {
				t.Errorf("the digest was written from:\n%s", run.Prompt)
			}
			return submitted(data("*Decided:* we ship on Friday."))
		})
		d := a.finish(a.startRun(f.ID).ID)
		if d.Status != runDone {
			t.Fatalf("run %s: %s", d.Status, describeSteps(d))
		}
		slack.mu.Lock()
		defer slack.mu.Unlock()
		if len(slack.posted) != 1 || slack.posted[0]["channel"] != "C0987654321" || slack.posted[0]["text"] != "*Decided:* we ship on Friday." {
			t.Errorf("posted %+v", slack.posted)
		}
	})
	t.Run("answer each question", func(t *testing.T) {
		a := newApp(t)
		f := tplUse(a, "answer-each-question", nil)
		_, objs := tplBoard(a, f.ID)
		if ps := prbGet(a, f.ID); len(ps.Problems) != 0 {
			t.Errorf("problems: %s", prbDescribe(ps.Problems))
		}
		a.worker.onAgent("Draft the answer", func(_ context.Context, run agentRun) runResult {
			q := "?"
			if strings.Contains(run.Prompt, "Where is it?") {
				q = "Where is it?"
			} else if strings.Contains(run.Prompt, "Who runs it?") {
				q = "Who runs it?"
			}
			return submitted(map[string]any{"question": q, "answer": "An answer to " + q})
		})
		a.worker.onAgent("Gather the answers", handsIn(data("Two answers.")))
		rn := a.startRun(f.ID)
		list := a.waitStep(rn.ID, "List the questions", stepWaiting)
		tplEnter(a, rn.ID, list.ID, objs["Questions"], []any{
			map[string]any{"question": "Where is it?", "context": ""},
			map[string]any{"question": "Who runs it?", "context": "The team."},
		})
		d := a.finish(rn.ID)
		if d.Status != runDone || len(stepsOf(d, "Each question")) == 0 {
			t.Fatalf("run %s: %s", d.Status, describeSteps(d))
		}
		gather := onlyStep(t, d, "Gather the answers")
		if len(gather.Inputs) != 1 || !strings.Contains(gather.Inputs[0].Content, "An answer to Where is it?") || !strings.Contains(gather.Inputs[0].Content, "An answer to Who runs it?") {
			t.Errorf("Gather took %+v", gather.Inputs)
		}
	})
	t.Run("expense claim", func(t *testing.T) {
		a := newApp(t)
		f := tplUse(a, "expense-claim", nil)
		_, objs := tplBoard(a, f.ID)
		if ps := prbGet(a, f.ID); len(ps.Problems) != 0 {
			t.Errorf("problems: %s", prbDescribe(ps.Problems))
		}
		a.worker.onAgent("Tell the claimant", handsIn(data("It is approved.")))
		claim := func(amount int) runDetail {
			rn := a.startRun(f.ID)
			step := a.waitStep(rn.ID, "Claim an expense", stepWaiting)
			tplEnter(a, rn.ID, step.ID, objs["Expense"], map[string]any{"amount": amount, "purpose": "A taxi", "date": "2026-10-01", "receipt": "https://example.com/r"})
			return a.getRun(rn.ID)
		}
		if d := a.finish(claim(40).ID); d.Status != runDone || len(stepsOf(d, "Approve expense")) != 0 || len(stepsOf(d, "Tell the claimant")) != 1 {
			t.Errorf("a small claim's run %s: %s", d.Status, describeSteps(d))
		}
		rn := claim(400)
		approve := a.waitStep(rn.ID, "Approve expense", stepWaiting)
		humanMustDecide(a, rn.ID, approve.ID, map[string]any{"state": tplState(t, objs["Expense"], "rejected"), "instructions": "No receipt."})
		if d := a.finish(rn.ID); d.Status != runDone || len(stepsOf(d, "Tell the claimant")) != 1 {
			t.Errorf("a large claim's run %s: %s", d.Status, describeSteps(d))
		}
	})
}

// A template's flow is a new one: named apart from every flow there is,
// whoever's it is, which stays as it was; or as the person names it. A
// version is saved of it as made from the template.
func TestTemplateMakesANewFlow(t *testing.T) {
	a := newApp(t)
	tp := tplFind(a, "expense-claim")
	have := a.newFlow(tp.Flow)
	first := tplUse(a, "expense-claim", nil)
	second := tplUse(a, "expense-claim", nil)
	if first.ID == have.ID || !strings.HasPrefix(first.Name, tp.Flow+" ") || second.Name == first.Name || !strings.HasPrefix(second.Name, tp.Flow+" ") {
		t.Errorf("made %q and %q beside %q", first.Name, second.Name, have.Name)
	}
	if procs, _ := tplBoard(a, have.ID); len(procs) != 0 {
		t.Errorf("the flow named as the template got %d processes", len(procs))
	}
	named := tplUse(a, "expense-claim", map[string]any{"name": "  Taxi claims "})
	if named.Name != "Taxi claims" || named.Owner.Kind != "global" || named.Access != "edit" {
		t.Errorf("named flow %+v", named)
	}
	var vs struct {
		Current  *int64 `json:"current"`
		Versions []struct {
			ID     int64  `json:"id"`
			Reason string `json:"reason"`
			Note   string `json:"note"`
		} `json:"versions"`
	}
	a.call("GET", fmt.Sprintf("/api/versions?flow=%d", named.ID), nil, http.StatusOK, &vs)
	if len(vs.Versions) != 1 || vs.Versions[0].Reason != "template" || !strings.Contains(vs.Versions[0].Note, tp.Title) || vs.Current == nil || *vs.Current != vs.Versions[0].ID {
		t.Errorf("versions %+v", vs)
	}
}

// Without the connector it needs, a template still makes its flow: its
// connector processes keep their operations, with none picked, which the
// board says is an error until an admin installs the connector and it is
// picked; the flow's setup file carries them, and imports again.
func TestTemplateWithoutItsConnector(t *testing.T) {
	a := newApp(t)
	noSlackInstalled(t)
	if n := tplFind(a, "daily-digest-to-slack").Needs; len(n) != 1 || n[0].Met {
		t.Errorf("without Slack the digest needs %+v", n)
	}
	f := tplUse(a, "daily-digest-to-slack", nil)
	procs, _ := tplBoard(a, f.ID)
	read := procs["Read the channel"]
	if c := read.Settings.Connector; c == nil || c.Server != 0 || c.Operation != "list_messages" || c.Args["limit"] != "50" {
		t.Fatalf("Read the channel's connector %+v", c)
	}
	ps := prbGet(a, f.ID)
	if !prbHas(ps.Problems, "error", "connector", read.ID, "needs the Slack connector, which isn't installed") {
		t.Errorf("problems: %s", prbDescribe(ps.Problems))
	}
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", f.ID), nil, http.StatusConflict, "needs the Slack connector")

	var setup struct {
		Text string `json:"text"`
	}
	a.call("GET", fmt.Sprintf("/api/flows/%d/setup", f.ID), nil, http.StatusOK, &setup)
	if !strings.Contains(setup.Text, "operation: list_messages") || strings.Contains(setup.Text, "server:") {
		t.Errorf("the setup file has:\n%s", setup.Text)
	}
	if status, reply := humanRaw(a, "POST", "/api/setup", setup.Text); status != http.StatusOK {
		t.Errorf("importing the flow again = %d %s", status, reply)
	}
	a.refused("POST", "/api/setup", map[string]any{"spoletta": 1, "flows": []any{map[string]any{"name": "Nowhere", "processes": []any{
		map[string]any{"name": "Do", "kind": "connector", "connector": map[string]any{"operation": "fly"}}}}}}, http.StatusBadRequest, "no connector has an operation")

	slack := newTplSlack(t)
	id := slack.install(a, "tpl_slack_later")
	a.call("PATCH", fmt.Sprintf("/api/processes/%d", read.ID), map[string]any{"settings": map[string]any{"connector": map[string]any{
		"server": id, "operation": "list_messages", "args": map[string]any{"limit": "50"}}}}, http.StatusOK, nil)
	ps = prbGet(a, f.ID)
	if !prbHas(ps.Problems, "error", "connector", read.ID, "needs its channel") || prbHas(ps.Problems, "error", "connector", read.ID, "isn't installed") {
		t.Errorf("problems once installed: %s", prbDescribe(ps.Problems))
	}
}

// With sign-in, a template's flow is whoever made it's, or the team they
// say; its roles are met once someone holds them, and until then its board
// warns that only admins decide.
func TestTemplatesWithSignIn(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob := usersNewPerson("Bob")
	bobRow := usersAdd(a, bob.email)
	if n := tplFind(a, "fix-with-approval-by-risk").Needs; !slices.ContainsFunc(n, func(n tplNeed) bool { return n.Kind == "role" && !n.Met }) {
		t.Errorf("with nobody holding it, the fix needs %+v", n)
	}
	usersSignIn(a, p, bob)
	f := tplUse(a, "fix-with-approval-by-risk", nil)
	if f.Owner.Kind != "user" || f.Owner.ID != bobRow.ID {
		t.Errorf("the flow is %+v's", f.Owner)
	}
	procs, _ := tplBoard(a, f.ID)
	if ps := prbGet(a, f.ID); !prbHas(ps.Problems, "warning", "role", procs["Approve risky fix"].ID, `Nobody holds the role "Tech lead"`) {
		t.Errorf("problems: %s", prbDescribe(ps.Problems))
	}
	a.refused("POST", "/api/templates/expense-claim", map[string]any{"owner": map[string]any{"kind": "user", "id": bobRow.ID + 1000}}, http.StatusForbidden, "only an admin")

	usersSignIn(a, p, usersAda)
	usersAdd(a, usersNewPerson("Tess").email, "Tech lead")
	if n := tplFind(a, "fix-with-approval-by-risk").Needs; slices.ContainsFunc(n, func(n tplNeed) bool { return n.Kind == "role" && !n.Met }) {
		t.Errorf("with Tess holding it, the fix needs %+v", n)
	}
	usersSignIn(a, p, bob)
	if ps := prbGet(a, f.ID); prbHas(ps.Problems, "warning", "role", procs["Approve risky fix"].ID, "Nobody holds") {
		t.Errorf("problems: %s", prbDescribe(ps.Problems))
	}
}
