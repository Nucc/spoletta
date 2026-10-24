package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type sentOutput struct {
	mu     sync.Mutex
	chunks []outputChunk
}

func (s *sentOutput) send(stream, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = append(s.chunks, outputChunk{stream, []byte(text)})
}

func (s *sentOutput) text(stream string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, c := range s.chunks {
		if c.stream == stream {
			b.Write(c.text)
		}
	}
	return b.String()
}

// Writes are gathered and sent in the order they came, a character cut in
// two by a write is sent whole, and what is written also reaches the
// step's own buffer.
func TestLiveOutputGathersWrites(t *testing.T) {
	var sent sentOutput
	o := newLiveOutput(sent.send)
	var own bytes.Buffer
	stdout, stderr := o.writer("stdout", &own), o.writer("stderr", &bytes.Buffer{})
	for _, c := range "one " {
		stdout.Write([]byte(string(c)))
	}
	é := []byte("é")
	stdout.Write(é[:1])
	o.flush(false)
	stdout.Write(é[1:])
	stderr.Write([]byte("warning\n"))
	stdout.Write([]byte("two\n"))
	o.close()
	stdout.Write([]byte("after"))
	o.flush(true)

	if got := sent.text("stdout"); got != "one étwo\n" {
		t.Errorf("stdout sent %q", got)
	}
	if got := sent.text("stderr"); got != "warning\n" {
		t.Errorf("stderr sent %q", got)
	}
	var order []string
	for _, c := range sent.chunks {
		order = append(order, c.stream)
	}
	if got := strings.Join(order, " "); got != "stdout stdout stderr stdout" {
		t.Errorf("sent %s, want the first four characters together, then each stream in turn", got)
	}
	if own.String() != "one étwo\nafter" {
		t.Errorf("the step's buffer got %q", own.String())
	}
}

// Only so much of a step's output is sent.
func TestLiveOutputStopsPastItsLimit(t *testing.T) {
	var sent sentOutput
	o := newLiveOutput(sent.send)
	w := o.writer("stdout", &bytes.Buffer{})
	line := []byte(strings.Repeat("x", 1<<20))
	for range 6 {
		w.Write(line)
	}
	o.close()
	if n := len(sent.text("stdout")); n > maxLiveOutputBytes {
		t.Errorf("sent %d bytes, more than %d", n, maxLiveOutputBytes)
	}
	if !strings.Contains(sent.text("stderr"), "printed more than 4 MB") {
		t.Errorf("no word of the rest: %q", sent.text("stderr"))
	}
}

// A script's output comes as it prints it, not only once it ends.
func TestScriptOutputIsSentAsItPrints(t *testing.T) {
	var sent sentOutput
	o := newLiveOutput(sent.send)
	done := make(chan runResult)
	go func() {
		done <- runScript(context.Background(), scriptRequest{Script: "echo first; echo oops >&2; sleep 3; echo '{\"data\": 1}'"}, time.Minute, newStepLogger("test", nil), o)
	}()
	deadline := time.Now().Add(2500 * time.Millisecond)
	for sent.text("stdout") != "first\n" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := sent.text("stdout"); got != "first\n" {
		t.Errorf("while the script slept, stdout sent %q", got)
	}
	res := <-done
	if !res.OK || res.Output != `first
{"data": 1}` {
		t.Errorf("result %+v", res)
	}
	if sent.text("stdout") != "first\n{\"data\": 1}\n" || sent.text("stderr") != "oops\n" {
		t.Errorf("sent stdout %q, stderr %q", sent.text("stdout"), sent.text("stderr"))
	}
}
