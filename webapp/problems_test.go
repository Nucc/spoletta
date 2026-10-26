package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A person fills in a ticket, an agent decides whether it is a bug, a
// fix is posted to Slack and a person approves it; nothing is wrong with it.
func soundBoard() snapshot {
	return snapshot{
		Processes: []process{
			{ID: 1, Name: "Enter ticket", Kind: kindHuman},
			{ID: 2, Name: "Classify", Kind: kindAgent, Model: "anthropic/claude"},
			{ID: 3, Name: "Fix", Kind: kindAgent, Settings: processSettings{Repositories: &[]string{"app"}}},
			{ID: 4, Name: "Post", Kind: kindConnector, Settings: processSettings{Connector: &connectorSettings{Server: 70, Operation: "post_message",
				Args: map[string]string{"channel": "C0123", "text": "{{Fix.summary}}"}}}},
			{ID: 5, Name: "Approve", Kind: kindHuman, Settings: processSettings{RemindAfter: intPtr(3600), WaitAtMost: intPtr(86400)}},
		},
		Objects: []snapshotObject{
			{ID: 10, Name: "Ticket"},
			{ID: 11, Name: "Fix"},
			{ID: 12, Name: "Approver", Human: true},
			{ID: 13, Name: "Posted"},
		},
		States: []objectState{{ID: 20, Object: 10, Name: "bug"}, {ID: 21, Object: 10, Name: "feature"}, {ID: 22, Object: 11, Name: "approved"}},
		Links: []snapshotLink{
			{ID: 40, Process: 1, Object: 10, Direction: directionOutput},
			{ID: 41, Process: 2, Object: 10, Direction: directionInput},
			{ID: 42, Process: 2, Object: 10, Direction: directionOutput},
			{ID: 43, Process: 3, Object: 10, Direction: directionInput, State: 20},
			{ID: 44, Process: 3, Object: 11, Direction: directionOutput},
			{ID: 45, Process: 5, Object: 11, Direction: directionInput},
			{ID: 46, Process: 5, Object: 12, Direction: directionInput},
			{ID: 47, Process: 5, Object: 11, Direction: directionOutput},
			{ID: 48, Process: 4, Object: 11, Direction: directionInput, State: 22},
			{ID: 49, Process: 4, Object: 13, Direction: directionOutput},
		},
		LinkStates: []snapshotLinkState{{Link: 42, State: 20}, {Link: 42, State: 21}, {Link: 47, State: 22}},
		Start:      startNode{Target: versionID(1)},
		Settings:   &flowSettings{Repositories: []repository{{Name: "app", URL: "https://example.com/app.git"}}},
	}
}

func soundFacts() problemFacts {
	return problemFacts{
		signIn:  true,
		servers: map[int64]serverFacts{70: {name: "Slack", auth: authToken, connector: "slack", connected: true}, 71: {name: "Docs", auth: authOAuth}},
		flows:   map[int64]bool{}, skills: map[int64]pickFacts{}, credentials: map[int64]pickFacts{},
		roles: map[string]bool{"approver": true}, sources: map[int64]bool{},
		defaultModel: "anthropic/claude", modelKnown: true,
	}
}

func intPtr(n int) *int { return &n }

func describeProblems(ps []problem) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "\n  %s %s p=%v o=%v: %s", p.Level, p.Rule, problemID(p.Process), problemID(p.Object), p.Message)
	}
	return b.String()
}

func problemID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func TestSoundBoardHasNoProblems(t *testing.T) {
	if ps := boardProblems(soundBoard(), soundFacts()); len(ps) != 0 {
		t.Errorf("problems:%s", describeProblems(ps))
	}
}

// Notes say nothing a run needs: a sound board with notes, even one
// pointing at a process gone, has no problems, and a board of notes alone
// has an empty board's.
func TestNotesAreNoProblems(t *testing.T) {
	b := soundBoard()
	b.Notes = []note{
		{ID: 90, Text: "Why we classify first", Colour: "yellow", Width: 240, Height: 140, Process: versionID(2)},
		{ID: 91, Text: "Gone", Colour: "pink", Width: 240, Height: 140, Process: versionID(99), Object: versionID(98)},
		{ID: 92, Text: "Inside nothing", Colour: "grey", Width: 240, Height: 140, Parent: versionID(97)},
	}
	if ps := boardProblems(b, soundFacts()); len(ps) != 0 {
		t.Errorf("a sound board with notes has problems:%s", describeProblems(ps))
	}
	empty := snapshot{Settings: &flowSettings{}}
	only := snapshot{Notes: b.Notes, Settings: &flowSettings{}}
	if got, want := describeProblems(boardProblems(only, soundFacts())), describeProblems(boardProblems(empty, soundFacts())); got != want {
		t.Errorf("a board of notes alone has problems:%s\nwant an empty board's:%s", got, want)
	}
}

// Each rule, broken on its own, shows as the problem it is, on the process
// or object it is about, at the level it is.
func TestBoardProblems(t *testing.T) {
	cases := []struct {
		name    string
		change  func(s *snapshot, f *problemFacts)
		level   string
		rule    string
		process int64
		object  int64
		says    string
	}{
		{name: "no start", change: func(s *snapshot, f *problemFacts) { s.Start.Target = nil },
			level: problemError, rule: "start", says: "connect the start"},
		{name: "empty board", change: func(s *snapshot, f *problemFacts) { *s = snapshot{} },
			level: problemError, rule: "start", says: "no process yet"},
		{name: "start on an inner board", change: func(s *snapshot, f *problemFacts) {
			s.Processes[0].Parent = versionID(99)
		}, level: problemError, rule: "start", says: "connect the start"},
		{name: "procedure without a start", change: func(s *snapshot, f *problemFacts) { s.Start.Target = nil; f.module = true },
			level: problemWarning, rule: "start", says: "procedure's start leads nowhere"},
		{name: "subflow running nothing", change: func(s *snapshot, f *problemFacts) {
			s.Processes = append(s.Processes, process{ID: 6, Name: "Call", Kind: kindSubflow})
			s.Connections = append(s.Connections, connection{Source: 2, Target: 6})
		}, level: problemError, rule: "subflow", process: 6, says: "runs no flow"},
		{name: "subflow running a deleted flow", change: func(s *snapshot, f *problemFacts) {
			s.Processes = append(s.Processes, process{ID: 6, Name: "Call", Kind: kindSubflow, Settings: processSettings{Flow: versionID(5)}})
			s.Connections = append(s.Connections, connection{Source: 2, Target: 6})
		}, level: problemError, rule: "subflow", process: 6, says: "was deleted"},
		{name: "subflow running a procedure in the trash", change: func(s *snapshot, f *problemFacts) {
			s.Processes = append(s.Processes, process{ID: 6, Name: "Call", Kind: kindSubflow, Settings: processSettings{Flow: versionID(5)}})
			s.Connections = append(s.Connections, connection{Source: 2, Target: 6})
			f.flows = map[int64]bool{5: true}
			f.trashed = map[int64]bool{5: true}
		}, level: problemError, rule: "subflow", process: 6, says: "is in the trash"},
		{name: "connector with none picked", change: func(s *snapshot, f *problemFacts) {
			s.Processes[3].Settings.Connector.Server = 0
		}, level: problemError, rule: "connector", process: 4, says: "uses no connector"},
		{name: "connector not installed", change: func(s *snapshot, f *problemFacts) {
			s.Processes[3].Settings.Connector.Server = 0
			delete(f.servers, 70)
		}, level: problemError, rule: "connector", process: 4, says: "needs the Slack connector, which isn't installed"},
		{name: "connector removed", change: func(s *snapshot, f *problemFacts) { delete(f.servers, 70) },
			level: problemError, rule: "connector", process: 4, says: "was removed"},
		{name: "connector an MCP server", change: func(s *snapshot, f *problemFacts) { s.Processes[3].Settings.Connector.Server = 71 },
			level: problemError, rule: "connector", process: 4, says: "not a connector"},
		{name: "connector without an operation", change: func(s *snapshot, f *problemFacts) { s.Processes[3].Settings.Connector.Operation = "" },
			level: problemError, rule: "connector", process: 4, says: "Pick what"},
		{name: "connector missing an argument", change: func(s *snapshot, f *problemFacts) { delete(s.Processes[3].Settings.Connector.Args, "channel") },
			level: problemError, rule: "connector", process: 4, says: "needs its channel"},
		{name: "connector without a token", change: func(s *snapshot, f *problemFacts) {
			f.servers[70] = serverFacts{name: "Slack", auth: authToken, connector: "slack"}
		},
			level: problemWarning, rule: "token", process: 4, says: "has no token"},
		{name: "argument naming what isn't taken", change: func(s *snapshot, f *problemFacts) {
			s.Processes[3].Settings.Connector.Args["text"] = "{{Ticket.title}}"
		}, level: problemWarning, rule: "argument", process: 4, says: `names "Ticket.title"`},
		{name: "object nothing generates", change: func(s *snapshot, f *problemFacts) {
			s.Objects = append(s.Objects, snapshotObject{ID: 14, Name: "Guide"})
			s.Links = append(s.Links, snapshotLink{ID: 50, Process: 3, Object: 14, Direction: directionInput, Role: roleInstrument})
		}, level: problemWarning, rule: "generated", object: 14, says: `never gives it to "Fix"`},
		{name: "object only the API gives", change: func(s *snapshot, f *problemFacts) {
			s.Objects = append(s.Objects, snapshotObject{ID: 14, Name: "Request"})
			s.Processes = append(s.Processes, process{ID: 6, Name: "Answer", Kind: kindAgent})
			s.Links = append(s.Links, snapshotLink{ID: 50, Process: 6, Object: 14, Direction: directionInput})
		}, level: problemWarning, rule: "generated", object: 14, says: `never runs "Answer"; only a run over the API`},
		{name: "process nothing leads to", change: func(s *snapshot, f *problemFacts) {
			s.Processes = append(s.Processes, process{ID: 6, Name: "Archive", Kind: kindScript})
			s.Links = append(s.Links, snapshotLink{ID: 50, Process: 6, Object: 11, Direction: directionInput, State: 21})
			s.Links = append(s.Links, snapshotLink{ID: 51, Process: 6, Object: 13, Direction: directionOutput})
			s.Links = slices.DeleteFunc(s.Links, func(l snapshotLink) bool { return l.ID == 49 })
			// What it waits for comes out of Fix, which nothing leads to either.
			s.Links = slices.DeleteFunc(s.Links, func(l snapshotLink) bool { return l.ID == 43 })
		}, level: problemWarning, rule: "unreached", process: 6, says: "Nothing leads to"},
		{name: "a human's next isn't followed", change: func(s *snapshot, f *problemFacts) {
			s.Processes = append(s.Processes, process{ID: 6, Name: "Archive", Kind: kindScript})
			s.Connections = append(s.Connections, connection{Source: 5, Target: 6})
		}, level: problemWarning, rule: "unreached", process: 6, says: `Nothing leads to "Archive"`},
		{name: "agent without a model", change: func(s *snapshot, f *problemFacts) { f.defaultModel = "" },
			level: problemWarning, rule: "model", process: 3, says: "no default model"},
		{name: "role nobody holds", change: func(s *snapshot, f *problemFacts) { f.roles = map[string]bool{"finance": true} },
			level: problemWarning, rule: "role", process: 5, says: `role "Approver"`},
		{name: "reminder at the time limit", change: func(s *snapshot, f *problemFacts) { s.Processes[4].Settings.RemindAfter = intPtr(86400) },
			level: problemWarning, rule: "reminder", process: 5, says: "reminds every 1 day but fails after 1 day"},
		{name: "repository the flow lacks", change: func(s *snapshot, f *problemFacts) { s.Settings.Repositories = nil },
			level: problemWarning, rule: "repository", process: 3, says: `repository "app"`},
		{name: "skill deleted", change: func(s *snapshot, f *problemFacts) { s.Processes[2].Settings.Skills = []int64{80} },
			level: problemWarning, rule: "skill", process: 3, says: "was deleted"},
		{name: "skill the flow may not use", change: func(s *snapshot, f *problemFacts) {
			s.Processes[2].Settings.Skills = []int64{80}
			f.skills[80] = pickFacts{name: "Style"}
		}, level: problemWarning, rule: "skill", process: 3, says: "The skill Style isn't one this flow may use"},
		{name: "MCP server removed", change: func(s *snapshot, f *problemFacts) { s.Processes[2].Settings.MCPServers = []int64{72} },
			level: problemWarning, rule: "mcp", process: 3, says: "was removed"},
		{name: "MCP server not connected", change: func(s *snapshot, f *problemFacts) { s.Processes[2].Settings.MCPServers = []int64{71} },
			level: problemWarning, rule: "mcp", process: 3, says: "You haven't connected the MCP server Docs"},
		{name: "credential removed", change: func(s *snapshot, f *problemFacts) { s.Processes[2].Settings.Credentials = []int64{90} },
			level: problemWarning, rule: "credential", process: 3, says: "was removed"},
		{name: "credential the flow may not use", change: func(s *snapshot, f *problemFacts) {
			s.Processes[2].Settings.Credentials = []int64{90}
			f.credentials[90] = pickFacts{name: "Jira", secret: true}
		}, level: problemWarning, rule: "credential", process: 3, says: "isn't one this flow may use"},
		{name: "credential without a secret", change: func(s *snapshot, f *problemFacts) {
			s.Processes[2].Settings.Credentials = []int64{90}
			f.credentials[90] = pickFacts{name: "Jira", usable: true}
		}, level: problemWarning, rule: "credential", process: 3, says: "The credential Jira has no secret"},
		{name: "copy of what is gone", change: func(s *snapshot, f *problemFacts) { s.Objects[0].Source = versionID(500) },
			level: problemWarning, rule: "copy", object: 10, says: "is gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := soundBoard(), soundFacts()
			tc.change(&s, &f)
			ps := boardProblems(s, f)
			found := slices.ContainsFunc(ps, func(p problem) bool {
				return p.Level == tc.level && p.Rule == tc.rule && problemID(p.Process) == idOrNil(tc.process) &&
					problemID(p.Object) == idOrNil(tc.object) && strings.Contains(p.Message, tc.says)
			})
			if !found {
				t.Errorf("no %s %s about %d/%d saying %q among:%s", tc.level, tc.rule, tc.process, tc.object, tc.says, describeProblems(ps))
			}
			for i := 1; i < len(ps); i++ {
				if ps[i-1].Level == problemWarning && ps[i].Level == problemError {
					t.Errorf("an error after a warning:%s", describeProblems(ps))
				}
			}
		})
	}
}

func idOrNil(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// What the board doesn't count as wrong: what a flow process gives its
// board, a procedure's inputs, people, queries and roles without sign-in.
func TestBoardProblemsLeaveAlone(t *testing.T) {
	t.Run("a flow process's board", func(t *testing.T) {
		s, f := soundBoard(), soundFacts()
		// Each fix is checked inside a flow process: Check takes Fix, which
		// the flow process takes outside, and Note, which Check generates.
		s.Processes = append(s.Processes,
			process{ID: 6, Name: "Each fix", Kind: kindFlow},
			process{ID: 7, Name: "Check", Kind: kindScript, Parent: versionID(6)},
			process{ID: 8, Name: "Log", Kind: kindScript, Parent: versionID(6)})
		s.Objects = append(s.Objects, snapshotObject{ID: 14, Name: "Note", Parent: versionID(6)})
		s.Links = append(s.Links,
			snapshotLink{ID: 50, Process: 6, Object: 11, Direction: directionInput},
			snapshotLink{ID: 51, Process: 7, Object: 11, Direction: directionInput},
			snapshotLink{ID: 52, Process: 7, Object: 14, Direction: directionOutput},
			snapshotLink{ID: 53, Process: 8, Object: 14, Direction: directionInput})
		if ps := boardProblems(s, f); len(ps) != 0 {
			t.Errorf("problems:%s", describeProblems(ps))
		}
		// With its board started at Log, Check still takes what is given.
		s.Boards = []innerBoard{{Process: 6, Start: startNode{Target: versionID(8)}}}
		if ps := boardProblems(s, f); len(ps) != 0 {
			t.Errorf("problems with a start:%s", describeProblems(ps))
		}
	})
	t.Run("a procedure's inputs", func(t *testing.T) {
		s, f := soundBoard(), soundFacts()
		f.module = true
		s.Objects = append(s.Objects, snapshotObject{ID: 14, Name: "Request"})
		s.Processes = append(s.Processes, process{ID: 6, Name: "Answer", Kind: kindAgent})
		s.Links = append(s.Links, snapshotLink{ID: 50, Process: 6, Object: 14, Direction: directionInput})
		if ps := boardProblems(s, f); len(ps) != 0 {
			t.Errorf("problems:%s", describeProblems(ps))
		}
	})
	t.Run("queries and roles without sign-in", func(t *testing.T) {
		s, f := soundBoard(), soundFacts()
		f.signIn, f.roles = false, map[string]bool{}
		s.Objects = append(s.Objects, snapshotObject{ID: 14, Name: "Past fixes", Persistent: true})
		s.Links = append(s.Links, snapshotLink{ID: 50, Process: 3, Object: 14, Direction: directionInput, Query: &linkQuery{}})
		if ps := boardProblems(s, f); len(ps) != 0 {
			t.Errorf("problems:%s", describeProblems(ps))
		}
	})
	t.Run("a model the worker can't say", func(t *testing.T) {
		s, f := soundBoard(), soundFacts()
		f.defaultModel, f.modelKnown = "", false
		if ps := boardProblems(s, f); len(ps) != 0 {
			t.Errorf("problems:%s", describeProblems(ps))
		}
	})
	t.Run("a failure handler's arguments", func(t *testing.T) {
		s, f := soundBoard(), soundFacts()
		s.Processes[3].Settings.Connector.Args["text"] = "{{Failure of Fix}}: {{Ticket.summary}}"
		s.Connections = append(s.Connections, connection{Source: 3, Target: 4, Kind: connectionFailure})
		if ps := boardProblems(s, f); len(ps) != 0 {
			t.Errorf("problems:%s", describeProblems(ps))
		}
	})
}

// The engine and the board's problems count what generates an object the
// same way.
func TestGeneratesOutput(t *testing.T) {
	cases := []struct {
		kind                   string
		inState, decides, take bool
		want                   bool
	}{
		{kindHuman, false, false, false, true},
		{kindHuman, true, false, false, false},
		{kindHuman, false, false, true, false},
		{kindAgent, true, true, true, false},
		{kindAgent, true, true, false, true},
		{kindAgent, true, false, true, true},
		{kindScript, false, false, true, true},
	}
	for _, c := range cases {
		if got := generatesOutput(c.kind, c.inState, c.decides, c.take); got != c.want {
			t.Errorf("generatesOutput(%s, %v, %v, %v) = %v", c.kind, c.inState, c.decides, c.take, got)
		}
	}
}

func TestDurationText(t *testing.T) {
	for n, want := range map[int]string{1: "1 second", 59: "59 seconds", 60: "1 minute", 5400: "90 minutes", 7200: "2 hours", 86400: "1 day", 90000: "25 hours"} {
		if got := durationText(n); got != want {
			t.Errorf("durationText(%d) = %q, want %q", n, got, want)
		}
	}
}
