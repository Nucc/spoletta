package main

import (
	"strings"
	"testing"
)

func TestStepLoggerKeepsLinesUpToItsCap(t *testing.T) {
	l := newStepLogger("process 1", nil)
	l.infof("bash, given %s", "Ticket")
	l.stderr("the script", "\n")
	l.stderr("the script", "fetching 3 pages\n")
	l.errorf("bash failed in 3ms: no such file")
	got := l.kept()
	if len(got) != 3 || got[0].Message != "bash, given Ticket" || got[1].Level != "info" ||
		got[1].Message != "the script printed to stderr:\nfetching 3 pages" || got[2].Level != "error" {
		t.Fatalf("kept %+v", got)
	}

	big := strings.Repeat("x", maxStepLogBytes/2+1)
	l = newStepLogger("process 1", nil)
	l.infof("%s", big)
	l.infof("%s", big)
	l.infof("after the cap")
	got = l.kept()
	if len(got) != 2 || got[1].Level != "error" || !strings.Contains(got[1].Message, "only in the worker's log") {
		t.Fatalf("past the cap kept %d lines, the last %q", len(got), got[len(got)-1].Message[:40])
	}

	var streamed []logLine
	l = newStepLogger("process 1", func(line logLine) { streamed = append(streamed, line) })
	l.errorf("boom")
	if len(streamed) != 1 || streamed[0].Message != "boom" || len(l.kept()) != 0 {
		t.Fatalf("streamed %+v, kept %+v", streamed, l.kept())
	}
	var none *stepLogger
	none.infof("only in the worker's log")
}
