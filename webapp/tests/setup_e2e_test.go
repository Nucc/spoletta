package tests

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"
)

// A webapp on a database of its own, since exporting refuses flows sharing a
// name, as the other tests' flows do.
func setupApp(t *testing.T) *app {
	t.Helper()
	switch {
	case testDB.fail != "":
		t.Fatal(testDB.fail)
	case testDB.skip != "":
		t.Skip(testDB.skip)
	}
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr, cfg.User, cfg.Passwd = "tcp", testDB.host+":"+testDB.port, testDB.user, testDB.password
	if strings.Contains(testDB.host, ":") {
		cfg.Addr = "[" + testDB.host + "]:" + testDB.port
	}
	root, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	name := fmt.Sprintf("%s%d", testDBPrefix, time.Now().UnixNano())
	if _, err := root.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Exec("DROP DATABASE IF EXISTS " + name) })
	shared := testDB.name
	testDB.name = name
	defer func() { testDB.name = shared }()
	return newApp(t)
}

// The tests share a database, so each names what it imports with a prefix
// of its own and keeps only that of what an export holds.

func setupExport(a *app, prefix string) string {
	a.t.Helper()
	status, body := a.do("GET", "/api/setup", nil)
	if status != http.StatusOK {
		a.t.Fatalf("GET /api/setup = %d %s", status, body)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		a.t.Fatalf("the export isn't YAML: %v\n%s", err, body)
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		list := root.Content[i+1]
		if list.Kind != yaml.SequenceNode {
			continue
		}
		var kept []*yaml.Node
		for _, item := range list.Content {
			if strings.HasPrefix(setupName(item), prefix) {
				kept = append(kept, item)
			}
		}
		list.Content = kept
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		a.t.Fatal(err)
	}
	return string(out)
}

func setupName(item *yaml.Node) string {
	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value == "name" {
			return item.Content[i+1].Value
		}
	}
	return ""
}

type setupImported struct {
	Flows []struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Created bool   `json:"created"`
	} `json:"flows"`
}

func setupImport(a *app, file string) (int, []byte) {
	a.t.Helper()
	req, err := http.NewRequest("POST", a.url+"/api/setup", strings.NewReader(file))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/yaml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func setupImportOK(a *app, file string) setupImported {
	a.t.Helper()
	status, body := setupImport(a, file)
	if status != http.StatusOK {
		a.t.Fatalf("POST /api/setup = %d %s", status, bytes.TrimSpace(body))
	}
	var res setupImported
	if err := yaml.Unmarshal(body, &res); err != nil {
		a.t.Fatal(err)
	}
	return res
}

// A setup as an LLM might write it: no places, every kind of thing.
const setupWritten = `spoletta: 1
mcpServers:
  - name: PFX_jira
    url: https://jira.example.com/mcp
structs:
  - name: PFX Ticket
    states: [open, closed]
    attributes:
      - name: title
        definition: What the ticket is about
        unique: true
    parts:
      - name: comments
        relation: aggregation
        many: true
procedures:
  - name: PFX Summarize
    start: {next: Summarize}
    processes:
      - name: Summarize
        prompt: |
          Summarize the ticket.
          Keep it short.
        takes: [PFX Ticket]
        yields: [Summary]
    objects:
      - copyOf: {flow: PFX Ticket}
      - name: Summary
        end: true
flows:
  - name: PFX Triage
    repositories:
      - name: app
        url: https://github.com/example/app
    schedules:
      - cron: "0 9 * * 1-5"
        timezone: Europe/London
        enabled: false
    start: {next: Fetch}
    processes:
      - name: Fetch
        model: anthropic/claude-sonnet
        thinking: low
        mcpServers: [PFX_jira]
        timeout: 600
        retries: 2
        prompt: Fetch the newest ticket.
        yields: [{object: PFX Ticket, state: open}]
        next: [Summarize it]
        onFailure: [Alert]
      - name: Summarize it
        kind: subflow
        runs: PFX Summarize
        takes: [PFX Ticket]
        next: [Review]
      - name: Review
        kind: human
        prompt: Is the summary right?
        takes: [PFX Ticket, Reviewer]
        yields: [{object: PFX Ticket, states: [open, closed]}]
        next: [Close]
      - name: Close
        kind: flow
        takes: [{object: PFX Ticket, state: closed, condition: true}]
        board:
          start: {next: Archive}
          processes:
            - name: Archive
              kind: script
              script: echo archived
              takes: [{object: PFX Ticket, role: instrument}]
              yields: [Archive record]
          objects:
            - name: Archive record
              persistent: true
      - name: Alert
        kind: script
        script: echo failed
    objects:
      - copyOf: {flow: PFX Ticket}
      - name: Reviewer
        human: true
      - name: Note
        specializes: Base
        states:
          - draft
          - name: final
            rule: When it has been reviewed
      - name: Base
        many: true
        min: 1
        max: 3
`

func TestSetupRoundTrips(t *testing.T) {
	a := setupApp(t)
	prefix := fmt.Sprintf("rt%d", time.Now().UnixNano())
	written := strings.ReplaceAll(setupWritten, "PFX", prefix)

	res := setupImportOK(a, written)
	if len(res.Flows) != 3 {
		t.Fatalf("imported %+v, want the struct, the procedure and the flow", res.Flows)
	}
	for _, f := range res.Flows {
		if !f.Created {
			t.Errorf("%s was made over, want created", f.Name)
		}
	}
	// The subflow process brought the procedure's outcome in by itself.
	first := setupExport(a, prefix)
	if strings.Count(first, "- Summary") != 2 {
		t.Errorf("Summarize it yields no Summary:\n%s", first)
	}
	for _, want := range []string{"copyOf:", "runs: " + prefix + " Summarize", "enabled: false", "relation: aggregation",
		"specializes: Base", "rule: When it has been reviewed", "role: instrument", "condition: true", "onFailure:", "mcpServers:", "prompt: |"} {
		if !strings.Contains(first, want) {
			t.Errorf("the export has no %q:\n%s", want, first)
		}
	}

	// Importing what was exported changes nothing, ids and all.
	before := map[int64]map[string]any{}
	for _, f := range res.Flows {
		before[f.ID] = verDump(a, f.ID)
	}
	again := setupImportOK(a, first)
	for _, f := range again.Flows {
		if f.Created {
			t.Errorf("%s was created again, want it made over", f.Name)
		}
		verSameBoard(t, before[f.ID], verDump(a, f.ID))
	}
	if second := setupExport(a, prefix); second != first {
		t.Errorf("exporting again differs:\nfirst\n%s\nsecond\n%s", first, second)
	}

	// Imported under other names, it makes flows of its own just the same.
	other := prefix + "b"
	fresh := setupImportOK(a, strings.ReplaceAll(first, prefix, other))
	for _, f := range fresh.Flows {
		if !f.Created {
			t.Errorf("%s was made over, want created", f.Name)
		}
	}
	if copied := strings.ReplaceAll(setupExport(a, other), other, prefix); copied != first {
		t.Errorf("the copy exports differently:\nfirst\n%s\ncopy\n%s", first, copied)
	}
}

func TestSetupImportMakesOverWhatChanged(t *testing.T) {
	a := setupApp(t)
	prefix := fmt.Sprintf("mo%d", time.Now().UnixNano())
	res := setupImportOK(a, strings.ReplaceAll(setupWritten, "PFX", prefix))
	var triage int64
	for _, f := range res.Flows {
		if f.Name == prefix+" Triage" {
			triage = f.ID
		}
	}
	procs := verProcesses(a, triage)
	ids := map[string]int64{}
	for _, p := range procs {
		ids[p.Name] = p.ID
	}

	// Alert goes; Fetch gets a new prompt and keeps its id.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(setupExport(a, prefix)), &doc); err != nil {
		t.Fatal(err)
	}
	flow := doc["flows"].([]any)[0].(map[string]any)
	var kept []any
	for _, p := range flow["processes"].([]any) {
		p := p.(map[string]any)
		switch p["name"] {
		case "Alert":
			continue
		case "Fetch":
			p["prompt"] = "Fetch the oldest ticket."
			delete(p, "onFailure")
		}
		kept = append(kept, p)
	}
	flow["processes"] = kept
	changed, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	setupImportOK(a, string(changed))

	after := map[string]process{}
	for _, p := range verProcesses(a, triage) {
		after[p.Name] = p
	}
	if _, ok := after["Alert"]; ok {
		t.Error("Alert is still there")
	}
	if p := after["Fetch"]; p.ID != ids["Fetch"] || p.Prompt != "Fetch the oldest ticket." {
		t.Errorf("Fetch is %+v, want id %d with the new prompt", p, ids["Fetch"])
	}
}

func TestSetupImportRefusesWhatIsWrong(t *testing.T) {
	a := setupApp(t)
	prefix := fmt.Sprintf("bad%d", time.Now().UnixNano())
	for _, c := range []struct{ file, msg string }{
		{"spoletta: 2\n", "spoletta: 1"},
		{"spoletta: 1\nflows:\n  - name: X\n    procesess: []\n", "procesess"},
		{"spoletta: 1\nflows:\n  - name: PFX A\n    processes:\n      - name: P\n        takes: [Nothing]\n", "there is no such object"},
		{"spoletta: 1\nflows:\n  - name: PFX A\n    processes:\n      - name: P\n      - name: P\n", "give one a key"},
		{"spoletta: 1\nflows:\n  - name: PFX A\n    processes:\n      - name: P\n        kind: subflow\n        runs: PFX B\n  - name: PFX B\n", "belongs under procedures"},
		{"spoletta: 1\nflows:\n  - name: PFX A\n    processes:\n      - name: P\n        kind: script\n        yields: [{object: O, states: [s]}]\n    objects:\n      - name: O\n        states: [s]\n", "only a human's or an agent's output link lists states"},
		{"spoletta: 1\nprocedures:\n  - name: PFX A\n    processes:\n      - name: P\n        kind: subflow\n        runs: PFX B\n  - name: PFX B\n    processes:\n      - name: Q\n        kind: subflow\n        runs: PFX A\n", "in a circle"},
	} {
		file := strings.ReplaceAll(c.file, "PFX", prefix)
		status, body := setupImport(a, file)
		if status != http.StatusBadRequest || !strings.Contains(string(body), c.msg) {
			t.Errorf("importing\n%s= %d %q, want 400 with %q", file, status, bytes.TrimSpace(body), c.msg)
		}
	}
	// Nothing of a file turned down was imported.
	if got := setupExport(a, prefix); strings.Contains(got, prefix) {
		t.Errorf("a file turned down left flows behind:\n%s", got)
	}
}
