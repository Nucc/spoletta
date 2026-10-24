package main

// Getting a workspace ready can take minutes: the first fetch of a big
// repository, or another step holding the workspace. So what the worker does
// meanwhile goes into the step's log as it does it, which the run's panel
// follows: each repository fetched and cloned, how far git has got, and
// what it waits for. Git reports its progress on stderr, rewriting a line
// with \r as a phase ("Receiving objects:  45% (450/1000), 12.00 MiB |
// 4.00 MiB/s") moves on; a phase's first line, one every few seconds and
// its last, ending in "done.", are logged, so the log follows without a
// line for every percent.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// How often a phase of git's progress is logged at most, apart from its
// first and last lines.
const progressEvery = 2 * time.Second

// A line of git's progress: its phase, such as "remote: Counting objects",
// then a count or percentage.
var progressLine = regexp.MustCompile(`^((?:remote: )?[A-Z][A-Za-z ]*?): +\d`)

// Logs git's progress through say as git writes it to stderr, and keeps the
// last lines that are no progress, which say what went wrong when it fails.
type gitProgress struct {
	say   func(string)
	now   func() time.Time
	every time.Duration

	buf     []byte
	phase   string
	pending string
	logged  time.Time
	said    []string
}

func newGitProgress(say func(string)) *gitProgress {
	return &gitProgress{say: say, now: time.Now, every: progressEvery}
}

func (p *gitProgress) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexAny(p.buf, "\r\n")
		if i < 0 {
			return len(b), nil
		}
		line := strings.TrimSpace(string(p.buf[:i]))
		p.buf = p.buf[i+1:]
		if line != "" {
			p.line(line)
		}
	}
}

func (p *gitProgress) line(line string) {
	m := progressLine.FindStringSubmatch(line)
	if m == nil {
		p.flush()
		// Where git clones into is the worker's own business.
		if strings.HasPrefix(line, "Cloning into") {
			return
		}
		if p.said = append(p.said, line); len(p.said) > 5 {
			p.said = p.said[1:]
		}
		// What went wrong is the error, said once the step fails.
		if !strings.HasPrefix(line, "fatal:") && !strings.HasPrefix(line, "error:") {
			p.say(line)
		}
		return
	}
	now := p.now()
	switch {
	case m[1] != p.phase:
		p.flush()
		p.phase = m[1]
	case strings.HasSuffix(line, "done.") || now.Sub(p.logged) >= p.every:
	default:
		p.pending = line
		return
	}
	p.say(line)
	p.logged, p.pending = now, ""
}

// Logs the latest line of the phase, if it wasn't.
func (p *gitProgress) flush() {
	if p.pending != "" {
		p.say(p.pending)
		p.pending = ""
	}
	p.phase = ""
}

// Runs git as gitOK does, logging its progress to l, each line after what.
// It fails with the last lines git said that are no progress.
func gitLogged(ctx context.Context, l *stepLogger, what, dir string, args ...string) error {
	p := newGitProgress(func(line string) { l.infof("%s: %s", what, line) })
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = childEnv(gitCredentialEnv(ctx)...)
	cmd.Stderr = p
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if len(p.buf) > 0 {
		p.Write([]byte("\n"))
	}
	p.flush()
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("git %s: %w", args[0], ctx.Err())
	case len(p.said) > 0:
		return fmt.Errorf("git %s: %s", args[0], strings.Join(p.said, "\n"))
	}
	return fmt.Errorf("git %s: %w", args[0], err)
}

// A repository's URL as logged: without whatever user and password it
// carries.
func shownURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return "the repository"
	}
	parsed.User = nil
	return parsed.String()
}

// Locks the path, saying first what the step waits for if someone holds it,
// and how long it waited, each line after what.
func lockSaying(p string, l *stepLogger, what, waits string) func() {
	if unlock, ok := tryLockPath(p); ok {
		return unlock
	}
	l.infof("%s: waiting for %s", what, waits)
	started := time.Now()
	unlock := lockPath(p)
	l.infof("%s: waited %s", what, time.Since(started).Round(time.Second))
	return unlock
}

// Says how long getting the workspace ready took if that is why it failed.
func prepareError(ctx, pctx context.Context, err error) error {
	if ctx.Err() == nil && errors.Is(pctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("getting it ready took longer than %s", prepareTimeout)
	}
	return err
}
