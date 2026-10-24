package main

// What the worker logs of a step it runs goes to its own log, as ever, and
// back to the webapp, which keeps it in the step's run's log: where the step
// worked, how pi or bash was started, what they printed to stderr, often
// progress or warnings, and how it ended, with what went wrong. The lines
// are streamed as they come, so a step stopped half way keeps those it got
// to; a script's come with its result to a webapp not asking for a stream.
// Prompts, keys and tokens are never logged.

import (
	"fmt"
	"log"
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

func (l *stepLogger) add(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
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
