package main

// Where a step's pi or bash runs: the worker is a trusted launcher, getting
// the step's workspace and private directory ready, and the step's program
// runs in a sandbox it starts for the step and throws away after. SANDBOX
// picks one:
//
//   - process: a plain process of the worker's, in a process group of its
//     own that stopping the step kills. It sees what the worker sees, so it
//     isolates nothing: running as the worker's user, a step can read the
//     worker's environment through /proc, the private directories and
//     tokens of the other steps going at the same time, and reach whatever
//     the worker reaches. It is for development and tests, and for a laptop
//     without podman in podman; the worker warns as it starts with it.
//   - podman and docker: a container per step, made through the engine's
//     API, that sees only the step's own directories, on a network reaching
//     only the webapp; see sandbox_engine.go.
//   - kubernetes: a pod per step, beside the worker's, seeing only the
//     step's own parts of the worker's volumes; see sandbox_kubernetes.go.
//
// There is no default: a worker without SANDBOX set doesn't start, so
// running without isolation is something someone chose, never something a
// deploy forgot. Another sandbox goes behind the same interface.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	// Whether it reaches nothing over the network, not even the webapp.
	Offline bool
	// The home directory the program gets, the run's; on the tmpfs without one.
	Home string
	// The proxy a sandbox whose network reaches only the webapp reaches
	// the internet through, with the step's credentials; see the webapp's
	// egress.go. A process reaches it itself.
	Egress string
}

// What the webapp says a step reaches the internet through.
type egressAccess struct {
	URL string `json:"url"`
}

func (a *egressAccess) proxy() string {
	if a == nil {
		return ""
	}
	if u, err := url.Parse(a.URL); err != nil || u.Scheme != "http" || u.Host == "" {
		return ""
	}
	return a.URL
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
	case "":
		return nil, errors.New("SANDBOX isn't set: set it to podman, docker or kubernetes, which run each step in a container of its own, or to process to run steps as the worker's own processes, which isolates nothing and is for development and tests only")
	case "process":
		return processSandbox{}, nil
	case "podman":
		return newEngineSandbox(context.Background(), libpodAPI)
	case "docker":
		return newEngineSandbox(context.Background(), dockerAPI)
	case "kubernetes":
		return newKubeSandbox(context.Background())
	}
	return nil, fmt.Errorf("SANDBOX: no sandbox %q; process, podman, docker or kubernetes", kind)
}

// Runs the program as a process of the worker's.
type processSandbox struct{}

func (processSandbox) run(ctx context.Context, spec sandboxSpec, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, spec.Cmd[0], spec.Cmd[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	if spec.Home != "" {
		cmd.Env = append(slices.DeleteFunc(slices.Clone(spec.Env), func(kv string) bool { return strings.HasPrefix(kv, "HOME=") }), "HOME="+spec.Home)
	}
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

// The user a sandbox's program runs as, uid and gid, when it isn't the
// worker's own: what the worker makes for a step is that user's, so the
// step can write it. -1 when steps run as the worker.
var sandboxOwner = -1

// Hands the directory, and with tree everything in it, to the sandbox's
// user, if it isn't the worker's.
func ownForSandbox(path string, tree bool) error {
	if sandboxOwner < 0 {
		return nil
	}
	if !tree {
		return os.Lchown(path, sandboxOwner, sandboxOwner)
	}
	return filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, sandboxOwner, sandboxOwner)
	})
}

// Every step of a run gets the run's home: what it installs or caches
// outside its checkouts, ~/.npm, ~/.cache, tools it put in ~/.local, and
// its shell's setup, is there for the next step of the run and its
// follow-ups, while each step still runs in a sandbox of its own. It lives
// beside the workspaces, outside every checkout, and goes with the run's
// workspaces when they are deleted; releasing them keeps it.
func runHome(run int64) string {
	return filepath.Join(workspacesRoot, ".homes", fmt.Sprintf("run-%d", run))
}

// Gives the program the run's home, made the first time, if it is of a run.
func (spec *sandboxSpec) withHome(run int64) error {
	if run <= 0 {
		return nil
	}
	home := runHome(run)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := ownForSandbox(home, false); err != nil {
		return err
	}
	spec.Home = home
	spec.Mounts = append(spec.Mounts, mount{Path: home})
	return nil
}
