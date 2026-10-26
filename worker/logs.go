package main

// What the worker logs of a step it runs goes to its own log, as ever, and
// back to the webapp, which keeps it in the step's run's log: getting its
// workspace ready as it goes, with git's progress and what it waits for
// (see git_progress.go), where the step worked, how pi or bash was
// started, what they printed to stderr, often progress or warnings, and
// how it ended, with what went wrong. The lines are streamed as they come,
// so a step stopped half way keeps those it got to; a script's come with
// its result to a webapp not asking for a stream.
// Prompts, keys and tokens are never logged: the step's tokens are blanked
// out of what it prints, as set -x or curl -v would print them (hiding).

import (
	"encoding/base64"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
)

// A line of a step's log, as the webapp takes it.
type logLine struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// How much of a step's log goes back to the webapp; what it logs past that
// is only in the worker's log.
const maxStepLogBytes = 1 << 20

// Logs the lines of one step. Each goes to emit as it is logged if there is
// one, else is kept for the result. A nil logger logs only to the worker's
// log.
type stepLogger struct {
	label string
	emit  func(logLine)
	// Blanks the step's tokens out of each line; see hiding.
	blank *strings.Replacer

	mu      sync.Mutex
	lines   []logLine
	size    int
	dropped bool
}

func newStepLogger(label string, emit func(logLine)) *stepLogger {
	return &stepLogger{label: label, emit: emit}
}

func (l *stepLogger) infof(format string, args ...any)  { l.add("info", format, args...) }
func (l *stepLogger) errorf(format string, args ...any) { l.add("error", format, args...) }

// Has the logger blank the step's tokens out of every line it logs from
// now on, in the worker's log and the webapp's alike, in each form they
// could be printed in: as they are, in base64 within something longer (a
// curl -v printing Proxy-Authorization: Basic with a username of the
// step's making), and as basic auth's user:token for the usernames the
// step is given. The webapp blanks them out of what it keeps of the step
// the same way (tokenForms in webapp/credentials.go).
func (l *stepLogger) hiding(tokens, usernames []string) *stepLogger {
	var forms []string
	for _, t := range tokens {
		if t == "" {
			continue
		}
		forms = append(forms, t)
		forms = append(forms, base64Within(t)...)
		for _, u := range usernames {
			forms = append(forms, base64.StdEncoding.EncodeToString([]byte(u+":"+t)))
		}
	}
	// Longest first, so one holding another is blanked out whole.
	slices.SortFunc(forms, func(a, b string) int { return len(b) - len(a) })
	var pairs []string
	for i, f := range forms {
		if !slices.Contains(forms[:i], f) {
			pairs = append(pairs, f, "[redacted]")
		}
	}
	if len(pairs) > 0 {
		l.blank = strings.NewReplacer(pairs...)
	}
	return l
}

// What of s's base64 shows wherever s is encoded, whatever comes before or
// after it: for each of the three places s can start at in a group of
// three bytes, the characters its own whole groups make, in either
// alphabet; none shorter than eight, which could be anybody's.
func base64Within(s string) []string {
	var out []string
	for skip := 0; skip < 3 && skip < len(s); skip++ {
		rest := s[skip:]
		rest = rest[:len(rest)/3*3]
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			if e := enc.EncodeToString([]byte(rest)); len(e) >= 8 {
				out = append(out, e)
			}
		}
	}
	return out
}

// s without the step's tokens.
func (l *stepLogger) hide(s string) string {
	if l == nil || l.blank == nil {
		return s
	}
	return l.blank.Replace(s)
}

func (l *stepLogger) add(level, format string, args ...any) {
	msg := l.hide(fmt.Sprintf(format, args...))
	label := "run"
	if l != nil {
		label = l.label
	}
	for _, line := range strings.Split(msg, "\n") {
		log.Printf("worker: %s| %s", label, line)
	}
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dropped {
		return
	}
	line := logLine{Time: time.Now().UTC(), Level: level, Message: msg}
	if l.size += len(msg); l.size > maxStepLogBytes {
		l.dropped = true
		line = logLine{Time: line.Time, Level: "error", Message: fmt.Sprintf("the step logged more than %d MB; the rest is only in the worker's log", maxStepLogBytes>>20)}
	}
	if l.emit != nil {
		l.emit(line)
		return
	}
	l.lines = append(l.lines, line)
}

// The lines kept for the result.
func (l *stepLogger) kept() []logLine {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lines
}

// Logs what a program that succeeded printed to stderr, as one line, often
// its progress or warnings; nothing if it printed nothing.
func (l *stepLogger) stderr(program, text string) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return
	}
	l.infof("%s printed to stderr:\n%s", program, text)
}
