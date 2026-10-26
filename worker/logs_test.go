package main

import (
	"bytes"
	"encoding/base64"
	"log"
	"os"
	"strings"
	"testing"
	"time"
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

// What the worker logs of a step, in its own log and the lines it sends the
// webapp, never holds the step's tokens, as set -x or curl -v print them.
func TestStepLogsHideTheStepsTokens(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	credentials, egressToken, gitToken := "Y3JlZGVudGlhbHMtdG9rZW4tMDEyMzQ1Njc4OWFi", "ZWdyZXNzLXRva2VuLTAxMjM0NTY3ODlhYmNkZWY", "ghs_0123456789abcdefABCDEF0123456789"
	run := scriptRequest{CredentialsToken: credentials, Egress: &egressAccess{URL: "http://step:" + egressToken + "@webapp:8080"},
		Git: &gitCredentials{Username: "x-access-token", Token: gitToken}}
	var streamed []logLine
	l := newStepLogger("process 1", func(line logLine) { streamed = append(streamed, line) }).hiding(run.tokens())
	proxyBasic := base64.StdEncoding.EncodeToString([]byte("step:" + egressToken))
	theirBasic := base64.StdEncoding.EncodeToString([]byte("whoever:" + credentials))
	l.stderr("the script", "+ curl -H 'Authorization: Bearer "+credentials+"' \"$CREDENTIAL_JIRA_URL\"\n"+
		"> Proxy-Authorization: Basic "+proxyBasic+"\n> Authorization: Basic "+theirBasic+"\n"+
		"git: https://x-access-token:"+gitToken+"@github.com/acme/app\n")
	logOutcome(l, "bash", runResult{OK: true, Output: "printed " + egressToken}, time.Millisecond)
	all := logged.String()
	for _, line := range streamed {
		all += line.Message
	}
	if !strings.Contains(all, "Authorization: Bearer [redacted]'") || !strings.Contains(all, "printed [redacted]") {
		t.Errorf("logged %s", all)
	}
	// Basic auth with a username of the step's making keeps only the
	// groups the username shares.
	for _, form := range []string{credentials, egressToken, gitToken, proxyBasic[:12], theirBasic[12 : len(theirBasic)-4]} {
		if strings.Contains(all, form) {
			t.Errorf("the log holds %s: %s", form, all)
		}
	}
}
