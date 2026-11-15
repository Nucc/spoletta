package main

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pi and Claude Code get the same submit_output tool, pi from its extension
// (submit-output.ts), whose parameters typebox builds, and Claude Code from
// an MCP server (submit-output-mcp.ts), whose parameters are the same schema
// built as plain JSON Schema (output-spec.ts). scripts/output-schema.ts
// checks the two schemas are one for a set of specs, when pi, which alone
// has typebox, is installed, and drives the MCP server over a pipe as Claude
// Code does; this runs it when Node can.

// Node from 22.6 on loads TypeScript with --experimental-strip-types.
var typeScriptNode = regexp.MustCompile(`^v(\d+)\.(\d+)\.`)

func TestOutputSchema(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node isn't installed")
	}
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Skipf("node --version: %v", err)
	}
	version := strings.TrimSpace(string(out))
	m := typeScriptNode.FindStringSubmatch(version)
	if m == nil {
		t.Skipf("node's version %q isn't known", version)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 22 || major == 22 && minor < 6 {
		t.Skipf("node %s can't load TypeScript", version)
	}
	// The script kills the servers it starts and gives up on one not
	// answering within seconds; this only keeps a script stuck regardless
	// from holding the test, or its output, past a minute.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--experimental-strip-types", "--disable-warning=ExperimentalWarning", "scripts/output-schema.ts")
	cmd.WaitDelay = 5 * time.Second
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("submit_output's MCP server and pi's extension part ways: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
