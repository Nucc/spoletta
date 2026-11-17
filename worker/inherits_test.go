package main

// What the worker is configured with is the instance's, so a step gets it
// only when its project inherits it; see env.go.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A request saying whether it inherits is taken at its word; one saying
// nothing is a webapp's from before projects, whose steps all inherit,
// unless it names a project, which only a webapp with projects does, so it
// forgot to say and gets nothing.
func TestInheritingTheInstance(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		said    *bool
		project int64
		ws      *workspaceSpec
		want    bool
	}{
		{nil, 0, nil, true},
		{nil, 0, &workspaceSpec{}, true},
		{&yes, 1, nil, true},
		{&no, 1, nil, false},
		{&no, 0, nil, false},
		{nil, 2, nil, false},
		{nil, 1, nil, false},
		{nil, 0, &workspaceSpec{Project: 2}, false},
	} {
		if got := inheritsInstance(c.said, c.project, c.ws); got != c.want {
			t.Errorf("said %v, project %d, workspace %+v: inherits %v, want %v", c.said, c.project, c.ws, got, c.want)
		}
	}
	if !(runRequest{}).inherits() || (scriptRequest{Project: 2}).inherits() {
		t.Error("requests don't say as inheritsInstance does")
	}
}

// A script of a project not inheriting the instance's configuration gets
// neither what CHILD_ENV names nor the worker's git config, and git reads
// no system config; one inheriting it gets them as before.
func TestScriptsOfOtherProjectsGetNoneOfTheInstances(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	t.Setenv("SHARED_TOKEN", "meant-for-legacy")
	t.Setenv("CHILD_ENV", "SHARED_TOKEN")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "instance.gitconfig"))
	t.Setenv("AWS_SECRET_ACCESS_KEY", "instance-aws")
	no := false
	for _, run := range []scriptRequest{
		{Script: "env", Project: 2, InheritsInstance: &no},
		// A webapp naming the project but forgetting to say.
		{Script: "env", Project: 2},
	} {
		res := runScript(context.Background(), run, time.Minute, newStepLogger("test", nil), nil)
		if !res.OK {
			t.Fatalf("script failed: %s", res.Error)
		}
		env := strings.Split(res.Output, "\n")
		names := envNames(env)
		for _, withheld := range []string{"SHARED_TOKEN", "GIT_CONFIG_GLOBAL", "AWS_SECRET_ACCESS_KEY"} {
			if slices.Contains(names, withheld) {
				t.Errorf("a script not inheriting got %s", withheld)
			}
		}
		if !slices.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
			t.Errorf("a script not inheriting may read git's system config: %v", names)
		}
	}
	yes := true
	res := runScript(context.Background(), scriptRequest{Script: "env", Project: 1, InheritsInstance: &yes}, time.Minute, newStepLogger("test", nil), nil)
	if names := envNames(strings.Split(res.Output, "\n")); !res.OK || !slices.Contains(names, "SHARED_TOKEN") || !slices.Contains(names, "GIT_CONFIG_GLOBAL") {
		t.Errorf("a script inheriting got %v, %s", names, res.Error)
	}
}

// The worker's own git for a step not inheriting reads none of its config,
// global or system, nor gets what CHILD_ENV names; for one inheriting, and
// for the worker's own work for no step, it does.
func TestTheWorkersGitForOtherProjects(t *testing.T) {
	t.Setenv("SHARED_TOKEN", "meant-for-legacy")
	t.Setenv("CHILD_ENV", "SHARED_TOKEN")
	t.Setenv("GIT_CONFIG_GLOBAL", "/instance.gitconfig")
	withheld := workerEnv(withInheritance(context.Background(), false))
	if !slices.Contains(withheld, "GIT_CONFIG_GLOBAL="+os.DevNull) || !slices.Contains(withheld, "GIT_CONFIG_NOSYSTEM=1") ||
		slices.Contains(withheld, "GIT_CONFIG_GLOBAL=/instance.gitconfig") || slices.Contains(envNames(withheld), "SHARED_TOKEN") {
		t.Errorf("not inheriting, the worker's git gets %v", envNames(withheld))
	}
	for _, ctx := range []context.Context{context.Background(), withInheritance(context.Background(), true)} {
		env := workerEnv(ctx)
		if !slices.Contains(env, "GIT_CONFIG_GLOBAL=/instance.gitconfig") || !slices.Contains(envNames(env), "SHARED_TOKEN") {
			t.Errorf("inheriting, the worker's git gets %v", envNames(env))
		}
	}
}

// pi of a step not inheriting gets none of the worker's AWS credentials,
// which Bedrock without a key would sign in with, nor its pi settings.
func TestPiOfOtherProjectsGetsNoCloudCredentials(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "instance-aws")
	t.Setenv("PI_MODEL_CATALOG", t.TempDir())
	agentConfig.settings = []byte(`{"defaultModel": "instance"}`)
	t.Cleanup(func() { agentConfig.settings = nil })
	no := false
	for _, c := range []struct {
		run  runRequest
		want bool
	}{{runRequest{}, true}, {runRequest{Project: 2, InheritsInstance: &no}, false}} {
		st := &agentStep{run: c.run, private: t.TempDir()}
		cmd, err := piHarness{}.prepare(st)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(cmd.env, "AWS_SECRET_ACCESS_KEY=instance-aws"); got != c.want {
			t.Errorf("inheriting %v, pi got the AWS credentials: %v", c.want, got)
		}
		settings, _ := os.ReadFile(filepath.Join(st.private, "agent", "settings.json"))
		if got := strings.Contains(string(settings), "instance"); got != c.want {
			t.Errorf("inheriting %v, pi's settings are %s", c.want, settings)
		}
	}
}

// The worker's own providers say which API Claude Code speaks to one of
// theirs only for a step inheriting them.
func TestClaudeCodeKeepsToItsProjectsProviders(t *testing.T) {
	ownProviders = []hostProvider{{ID: "gateway", API: "bedrock-converse-stream"}}
	t.Cleanup(func() { ownProviders = nil })
	no := false
	if !claudeBedrock(runRequest{}, "gateway") {
		t.Error("inheriting, the worker's own provider didn't steer Claude Code to Bedrock")
	}
	if claudeBedrock(runRequest{Project: 2, InheritsInstance: &no}, "gateway") {
		t.Error("not inheriting, the worker's own provider steered Claude Code to Bedrock")
	}
	t.Setenv("AWS_REGION", "eu-north-1")
	if bedrockRegion(true) != "eu-north-1" || bedrockRegion(false) != "us-east-1" {
		t.Error("the worker's AWS_REGION went to a step not inheriting it, or not to one inheriting it")
	}
}
