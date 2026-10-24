package main

import (
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"
)

// What a script prints goes back to the webapp as it prints it, stdout and
// stderr in the order they came, so a person can follow the script as in a
// terminal. Writes are gathered for a moment and sent together, so a script
// printing a character at a time doesn't become as many records, and only
// so much is sent of a step: past that the rest is only in its result and
// the worker's log. A write ending part way through a character keeps that
// part for the next, so what is sent is always whole text.
type liveOutput struct {
	send func(stream, text string)
	// Held through a flush, so two never send out of order.
	flushing sync.Mutex

	mu      sync.Mutex
	pending []outputChunk
	size    int
	sent    int
	full    bool
	closed  bool
	stop    chan struct{}
	stopped sync.WaitGroup
	closing sync.Once
}

type outputChunk struct {
	stream string
	text   []byte
}

const (
	// How long writes are gathered before they are sent, and how much.
	outputFlushEvery = 250 * time.Millisecond
	outputFlushBytes = 32 << 10
	// The most of a step's output sent.
	maxLiveOutputBytes = 4 << 20
)

func newLiveOutput(send func(stream, text string)) *liveOutput {
	o := &liveOutput{send: send, stop: make(chan struct{})}
	o.stopped.Add(1)
	go func() {
		defer o.stopped.Done()
		t := time.NewTicker(outputFlushEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				o.flush(false)
			case <-o.stop:
				return
			}
		}
	}()
	return o
}

// A writer for one of the streams, stdout or stderr, also writing to w.
func (o *liveOutput) writer(stream string, w io.Writer) io.Writer {
	if o == nil {
		return w
	}
	return io.MultiWriter(w, streamWriter{o, stream})
}

type streamWriter struct {
	o      *liveOutput
	stream string
}

func (s streamWriter) Write(p []byte) (int, error) {
	o := s.o
	o.mu.Lock()
	if n := len(o.pending); n > 0 && o.pending[n-1].stream == s.stream {
		o.pending[n-1].text = append(o.pending[n-1].text, p...)
	} else {
		o.pending = append(o.pending, outputChunk{s.stream, append([]byte(nil), p...)})
	}
	o.size += len(p)
	big := o.size >= outputFlushBytes
	o.mu.Unlock()
	if big {
		o.flush(false)
	}
	return len(p), nil
}

// Sends what was written, but for a character cut short at the end of a
// stream's last write, unless this is the last flush.
func (o *liveOutput) flush(last bool) {
	o.flushing.Lock()
	defer o.flushing.Unlock()
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = last
	var out []outputChunk
	var keep []outputChunk
	for i, c := range o.pending {
		text := c.text
		if !last && i == len(o.pending)-1 {
			cut := incompleteTail(text)
			if cut > 0 {
				keep = []outputChunk{{c.stream, append([]byte(nil), text[len(text)-cut:]...)}}
				text = text[:len(text)-cut]
			}
		}
		if len(text) > 0 {
			out = append(out, outputChunk{c.stream, text})
		}
	}
	o.pending, o.size = keep, 0
	for _, c := range keep {
		o.size += len(c.text)
	}
	if o.full {
		o.mu.Unlock()
		return
	}
	var send []outputChunk
	for _, c := range out {
		if o.sent+len(c.text) > maxLiveOutputBytes {
			o.full = true
			send = append(send, outputChunk{"stderr", []byte(fmt.Sprintf("\n… it printed more than %d MB; the rest isn't shown here\n", maxLiveOutputBytes>>20))})
			break
		}
		o.sent += len(c.text)
		send = append(send, c)
	}
	o.mu.Unlock()
	for _, c := range send {
		o.send(c.stream, string(c.text))
	}
}

// Sends the rest and stops; nothing is sent after. Closing again does nothing.
func (o *liveOutput) close() {
	if o == nil {
		return
	}
	o.closing.Do(func() {
		close(o.stop)
		o.stopped.Wait()
		o.flush(true)
	})
}

// How many bytes at the end of p are the start of a character not yet
// written whole.
func incompleteTail(p []byte) int {
	for n := 1; n <= utf8.UTFMax-1 && n <= len(p); n++ {
		b := p[len(p)-n]
		if !utf8.RuneStart(b) {
			continue
		}
		if !utf8.FullRune(p[len(p)-n:]) {
			return n
		}
		return 0
	}
	return 0
}
