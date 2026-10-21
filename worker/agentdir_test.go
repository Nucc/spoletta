package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each agent step's pi gets an agent directory of its own, so what one
// step's pi left there, such as credentials, isn't there for the next.
func TestEachStepGetsItsOwnAgentDirectory(t *testing.T) {
	bin := t.TempDir()
	seen := filepath.Join(t.TempDir(), "seen")
	// A pi that notes what its agent directory holds and leaves a file there.
	fake := "#!/bin/sh\necho \"$PI_CODING_AGENT_DIR:$(ls -A \"$PI_CODING_AGENT_DIR\" | tr '\\n' ' ')\" >> \"$SEEN\"\ntouch \"$PI_CODING_AGENT_DIR/auth.json\"\ncat > /dev/null\n"
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("SEEN", seen)
	t.Setenv("CHILD_ENV", "SEEN")
	agentConfig.settings = []byte(`{}`)
	t.Cleanup(func() { agentConfig.settings = nil })

	for range 2 {
		res := runPi(context.Background(), runRequest{Prompt: "hi"}, time.Minute, newStepLogger("test", nil), nil)
		if !res.OK {
			t.Fatalf("step failed: %s", res.Error)
		}
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("pi ran with %q", lines)
	}
	dirs := map[string]bool{}
	for _, line := range lines {
		dir, files, _ := strings.Cut(line, ":")
		dirs[dir] = true
		if strings.TrimSpace(files) != "settings.json" {
			t.Errorf("a step's agent directory held %q, want only settings.json", files)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("the agent directory %s outlived its step", dir)
		}
	}
	if len(dirs) != 2 {
		t.Errorf("both steps had the agent directory %v", dirs)
	}
}
