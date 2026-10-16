package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestFlowSettingsScan(t *testing.T) {
	for _, v := range []any{nil, []byte(`{}`), `{"branchPrefix":"x"}`, []byte(`null`)} {
		var s flowSettings
		if err := s.Scan(v); err != nil || s.Repositories == nil {
			t.Errorf("Scan(%v) = %+v, %v, want no repositories rather than nil", v, s, err)
		}
	}
	s := flowSettings{BranchPrefix: "old", Repositories: []repository{{Name: "old"}}}
	if err := s.Scan(`{"repositories":[{"name":"app","url":"https://h/app"}]}`); err != nil {
		t.Fatal(err)
	}
	if s.BranchPrefix != "" || len(s.Repositories) != 1 || s.Repositories[0].Name != "app" {
		t.Errorf("Scan kept what was there before: %+v", s)
	}
	if err := s.Scan(42); err == nil {
		t.Error("Scan(42) took it")
	}
	if err := s.Scan([]byte(`{`)); err == nil {
		t.Error("Scan of broken JSON took it")
	}
}

func TestFlowSettingsValue(t *testing.T) {
	in := flowSettings{BranchPrefix: "team/x", Repositories: []repository{{Name: "app", URL: "https://h/app", Base: "dev"}}}
	v, err := in.Value()
	if err != nil {
		t.Fatal(err)
	}
	var out flowSettings
	if err := out.Scan(v); err != nil || !reflect.DeepEqual(in, out) {
		t.Errorf("Scan(Value()) = %+v, %v, want %+v", out, err, in)
	}
}

func TestValidBranch(t *testing.T) {
	for b, ok := range map[string]bool{
		"main":                   true,
		"flow/run-1":             true,
		"release/1.2_x-y":        true,
		"":                       false,
		"-main":                  false,
		"/main":                  false,
		".hidden":                false,
		"a..b":                   false,
		"a//b":                   false,
		"a/":                     false,
		"a.":                     false,
		"a.lock":                 false,
		"a b":                    false,
		"a~1":                    false,
		strings.Repeat("a", 200): true,
		strings.Repeat("a", 201): false,
	} {
		if validBranch(b) != ok {
			t.Errorf("validBranch(%q) = %v, want %v", b, !ok, ok)
		}
	}
}

func TestNameFromURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/org/app":       "app",
		"https://github.com/org/app.git":   "app",
		"https://github.com/org/app/":      "app",
		"https://github.com/org/app.git/":  "app",
		"https://github.com/org/app.git//": "app",
		"https://h/a/b/c.d.git":            "c.d",
	} {
		if got := nameFromURL(url); got != want {
			t.Errorf("nameFromURL(%q) = %q, want %q", url, got, want)
		}
	}
}

// Checking tidies the settings: spaces and slashes around the prefix go,
// repositories are named after their URLs unless named.
func TestFlowSettingsCheck(t *testing.T) {
	s := flowSettings{BranchPrefix: " /team/agents/ ", Repositories: []repository{
		{URL: " https://github.com/org/app.git ", Base: " dev "},
		{URL: "https://github.com/org/app", Name: " web "},
	}}
	if msg := s.check(); msg != "" {
		t.Fatalf("check() = %q", msg)
	}
	want := flowSettings{BranchPrefix: "team/agents", Repositories: []repository{
		{Name: "app", URL: "https://github.com/org/app.git", Base: "dev"},
		{Name: "web", URL: "https://github.com/org/app"},
	}}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("check() left %+v, want %+v", s, want)
	}
	var none flowSettings
	if msg := none.check(); msg != "" || none.Repositories == nil {
		t.Errorf("check() of nothing = %q, %+v", msg, none)
	}

	for _, c := range []struct {
		s       flowSettings
		problem string
	}{
		{flowSettings{BranchPrefix: "a..b"}, "branch prefix"},
		{flowSettings{Repositories: []repository{{URL: "http://h/app"}}}, "isn't an https URL"},
		{flowSettings{Repositories: []repository{{URL: "git@h:app.git"}}}, "isn't an https URL"},
		{flowSettings{Repositories: []repository{{URL: "https://h/a pp"}}}, "isn't an https URL"},
		{flowSettings{Repositories: []repository{{URL: "https://h/app", Name: "my app"}}}, "isn't a valid repository name"},
		{flowSettings{Repositories: []repository{{URL: "https://h/app", Name: ".app"}}}, "isn't a valid repository name"},
		{flowSettings{Repositories: []repository{{URL: "https://h/app"}, {URL: "https://other/app.git"}}}, `two repositories are named "app"`},
		{flowSettings{Repositories: []repository{{URL: "https://h/app", Base: "dev/"}}}, `"dev/" isn't a valid branch name`},
	} {
		if msg := c.s.check(); !strings.Contains(msg, c.problem) || msg == "" {
			t.Errorf("check(%+v) = %q, want %q", c.s, msg, c.problem)
		}
	}
}

// Runs' branches go under the prefix set, or flow/ and the flow's name as a slug.
func TestFlowSettingsPrefix(t *testing.T) {
	for _, c := range []struct {
		prefix, flow, want string
	}{
		{"team/x", "Anything", "team/x"},
		{"", "Fix Sentry issues", "flow/fix-sentry-issues"},
		{"", "  --Ship it!! v2--  ", "flow/ship-it-v2"},
		{"", "Ünïcode", "flow/n-code"},
		{"", "!!!", "flow/flow"},
		{"", "", "flow/flow"},
	} {
		if got := (flowSettings{BranchPrefix: c.prefix}).prefix(c.flow); got != c.want {
			t.Errorf("prefix(%q, %q) = %q, want %q", c.prefix, c.flow, got, c.want)
		}
	}
}

// A process works on all the flow's repositories without a list of its own,
// on none with an empty one, and on those named otherwise; a re-run's
// repositories carry the commit they start from.
func TestRunWorkspaceForProcess(t *testing.T) {
	var none *runWorkspace
	if got := none.forProcess(processSettings{}); got != nil {
		t.Errorf("no workspace gives %+v", got)
	}
	app := repository{Name: "app", URL: "https://h/app"}
	docs := repository{Name: "docs", URL: "https://h/docs", Base: "main"}
	ws := &runWorkspace{id: "run-4", run: 4, branch: "flow/x/run-4", repos: []repository{app, docs}, starts: map[string]string{"docs": "abc"}}
	names := func(ns ...string) *[]string { return &ns }
	for _, c := range []struct {
		repos *[]string
		want  []workspaceRepo
	}{
		{nil, []workspaceRepo{{app, ""}, {docs, "abc"}}},
		{names("app"), []workspaceRepo{{app, ""}}},
		{names("docs", "gone"), []workspaceRepo{{docs, "abc"}}},
		{names(), nil},
		{names("gone"), nil},
	} {
		got := ws.forProcess(processSettings{Repositories: c.repos})
		if c.want == nil {
			if got != nil {
				t.Errorf("forProcess(%v) = %+v, want none", c.repos, got)
			}
			continue
		}
		want := &workspaceSpec{ID: "run-4", Branch: "flow/x/run-4", Run: 4, Repositories: c.want}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("forProcess(%v) = %+v, want %+v", c.repos, got, want)
		}
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"a", "b"}, "b") || contains([]string{"a"}, "c") || contains(nil, "") {
		t.Error("contains is wrong")
	}
}

func TestWithWorkspace(t *testing.T) {
	if got := withWorkspace("Do it.", nil); got != "Do it." {
		t.Errorf("withWorkspace(nil) = %q", got)
	}
	got := withWorkspace("Do it.", &workspaceSpec{Branch: "flow/x/run-4", Repositories: []workspaceRepo{
		{repository: repository{Name: "app", URL: "https://h/app"}},
		{repository: repository{Name: "docs", URL: "https://h/docs", Base: "main"}},
		{repository: repository{Name: "web", URL: "https://h/web", Base: "main"}, Start: "0123456789abcdef"},
		{repository: repository{Name: "cli", URL: "https://h/cli"}, Start: "abc"},
	}})
	for _, want := range []string{
		"Do it.\n\nYou work in a workspace",
		`on the branch "flow/x/run-4".`,
		"- app/: https://h/app, branched from its default branch\n",
		"- docs/: https://h/docs, branched from main\n",
		"- web/: https://h/web, branched from commit 0123456789ab, where the run this one re-runs had got to\n",
		"- cli/: https://h/cli, branched from commit abc, where",
		"Don't switch branches, and don't push.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("withWorkspace = %q, want it to hold %q", got, want)
		}
	}
}

// Two tries of a step together start where the first started and end where
// the second left off, with the commits of both, newest first.
func TestMergeCommits(t *testing.T) {
	first := []repoCommits{
		{Name: "app", Before: "a0", After: "a1", Commits: []string{"a1 first"}, Uncommitted: true},
		{Name: "docs", Before: "d0", After: "d0", Commits: []string{}},
	}
	second := []repoCommits{
		{Name: "app", Before: "a1", After: "a2", Commits: []string{"a2 second"}},
		{Name: "docs", Before: "d0", After: "d1", Commits: []string{"d1 docs"}},
		{Name: "web", Before: "w0", After: "w1", Commits: []string{"w1 web"}},
	}
	want := []repoCommits{
		{Name: "app", Before: "a0", After: "a2", Commits: []string{"a2 second", "a1 first"}},
		{Name: "docs", Before: "d0", After: "d1", Commits: []string{"d1 docs"}},
		{Name: "web", Before: "w0", After: "w1", Commits: []string{"w1 web"}},
	}
	if got := mergeCommits(first, second); !reflect.DeepEqual(got, want) {
		t.Errorf("mergeCommits = %+v, want %+v", got, want)
	}
	if got := mergeCommits(first, nil); got != nil {
		t.Errorf("mergeCommits without a second try = %+v", got)
	}
	if got := mergeCommits(nil, second); !reflect.DeepEqual(got, second) {
		t.Errorf("mergeCommits without a first try = %+v", got)
	}
}

func TestWorkspaceRun(t *testing.T) {
	for name, want := range map[string]string{
		"run-42":         "42",
		"run-42-step-7":  "42",
		"run-":           "",
		"run-42-step-":   "",
		"flow/x/run-42":  "",
		"run-42-other-7": "",
	} {
		got := ""
		if m := workspaceRun.FindStringSubmatch(name); m != nil {
			got = m[1]
		}
		if got != want {
			t.Errorf("workspaceRun(%q) = %q, want %q", name, got, want)
		}
	}
}
