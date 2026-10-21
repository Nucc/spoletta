package main

// Where a step's pi or bash runs: the worker is a trusted launcher, getting
// the step's workspace and private directory ready, and the step's program
// runs in a sandbox it starts for the step and throws away after. SANDBOX
// picks one:
//
//   - process (the default): a plain process of the worker's, in a process
//     group of its own that stopping the step kills. It sees what the
//     worker sees, so it isolates nothing; it is for development and
//     tests, and for a laptop without podman in podman.
//   - podman: a container per step, made through a rootless podman's API,
//     that sees only the step's own directories, on a network reaching
//     only the webapp; see sandbox_podman.go.
//
// Another, such as one starting a Kubernetes pod per step, goes behind the
// same interface.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type sandbox interface {
	// Runs spec.Cmd in the sandbox, writing what it prints to stdout and
	// stderr as it goes; returns once it exits or ctx ends, which stops it.
	// An exitError says it exited with another status than 0.
	run(ctx context.Context, spec sandboxSpec, stdout, stderr io.Writer) error
}

type sandboxSpec struct {
	Cmd []string
	// The file the program reads as its stdin, if any: a prompt can't be an
	// argument, which a leading @ or - would turn into a file or an option.
	Stdin string
	// Where it starts, and its whole environment.
	Dir string
	Env []string
	// The worker's directories it sees, each at the same path; nothing else
	// of the worker's.
	Mounts []mount
	// Whether stopping it may let it tidy up first, as pi stops the tools it
	// started, or kills it at once.
	Graceful bool
}

type mount struct {
	Path     string
	ReadOnly bool
}

// A program that exited with a status other than 0.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// The sandbox steps run in, chosen as the worker starts.
var steps sandbox = processSandbox{}

func newSandbox(kind string) (sandbox, error) {
	switch kind {
	case "", "process":
		return processSandbox{}, nil
	}
	return nil, fmt.Errorf("SANDBOX: no sandbox %q; process or podman", kind)
}

// Runs the program as a process of the worker's.
type processSandbox struct{}

func (processSandbox) run(ctx context.Context, spec sandboxSpec, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, spec.Cmd[0], spec.Cmd[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if spec.Stdin != "" {
		f, err := os.Open(spec.Stdin)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdin = f
	}
	// A process group of its own, so stopping it stops what it started too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	signal := syscall.SIGKILL
	if spec.Graceful {
		signal = syscall.SIGTERM
	}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, signal) }
	// Don't wait forever on one that ignores SIGTERM or leaves children holding its output.
	cmd.WaitDelay = 10 * time.Second
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exitError(exit.ExitCode())
	}
	return err
}

// Whether the program couldn't be started at all, rather than failing.
func notStarted(err error) bool {
	var exit exitError
	return err != nil && !errors.As(err, &exit) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// Reads a file a step may have written, refusing a symbolic link, which
// could lead to what isn't the step's.
func readNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxScriptBytes))
}
