package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The process sandbox runs the program with its stdin from the file, its
// environment and directory, says how it exited, and stopping it stops
// what it started too.
func TestProcessSandbox(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "stdin")
	os.WriteFile(in, []byte("hello"), 0o600)
	var out, errOut bytes.Buffer
	err := processSandbox{}.run(context.Background(), sandboxSpec{
		Cmd: []string{"sh", "-c", `cat; echo " $GREETING $(pwd)"; echo oops >&2; exit 3`}, Stdin: in, Dir: dir, Env: []string{"GREETING=from-env"},
	}, &out, &errOut)
	real, _ := filepath.EvalSymlinks(dir)
	if want := "hello from-env " + real + "\n"; out.String() != want || errOut.String() != "oops\n" {
		t.Errorf("printed %q and %q, want %q", out.String(), errOut.String(), want)
	}
	var exit exitError
	if !errors.As(err, &exit) || exit != 3 || notStarted(err) {
		t.Errorf("exited with %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = processSandbox{}.run(ctx, sandboxSpec{Cmd: []string{"sh", "-c", "sleep 30 & wait"}, Graceful: true}, &out, &errOut)
	if err == nil || time.Since(started) > 5*time.Second {
		t.Errorf("stopping took %v: %v", time.Since(started), err)
	}
	if err := (processSandbox{}).run(context.Background(), sandboxSpec{Cmd: []string{"no-such-program-here"}}, &out, &errOut); !notStarted(err) {
		t.Errorf("a missing program: %v", err)
	}
}
