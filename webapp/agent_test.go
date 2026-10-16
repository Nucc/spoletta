package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWithFlow(t *testing.T) {
	if got := withFlow("Do it.", "Write", ""); got != "Do it." {
		t.Errorf("withFlow without OPL = %q, want the prompt as it is", got)
	}
	got := withFlow("Do it.", "Write", "Write yields Draft.")
	for _, want := range []string{"Do it.\n\n", `the process "Write"`, "<flow>\nWrite yields Draft.\n</flow>\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("withFlow = %q, want it to hold %q", got, want)
		}
	}
}

// Inputs are tagged with their name, state and role; consumed is the
// default role and isn't written, and only the roles present are explained.
func TestWithInputs(t *testing.T) {
	if got := withInputs("Do it.", nil); got != "Do it." {
		t.Errorf("withInputs without inputs = %q", got)
	}
	got := withInputs("Do it.", []runInput{
		{Name: `A "quoted" <name>`, Content: "a"},
		{Name: "Spec", State: "approved & signed", Content: "s", Role: inputInstrument},
		{Name: "Draft", Content: "d", Role: inputConsumed},
	})
	for _, want := range []string{
		"Do it.\n\nYour inputs follow, one <input> element per object.",
		"\n<input name=\"A &#34;quoted&#34; &lt;name&gt;\">\na\n</input>\n",
		"\n<input name=\"Spec\" state=\"approved &amp; signed\" role=\"instrument\">\ns\n</input>\n",
		"\n<input name=\"Draft\">\nd\n</input>\n",
		"role is instrument is a reference",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("withInputs = %q, want it to hold %q", got, want)
		}
	}
	if strings.Contains(got, "role is affected") {
		t.Errorf("withInputs explains the affected role with no affected input: %q", got)
	}
	got = withInputs("", []runInput{{Name: "Code", Content: "c", Role: inputAffected}})
	if !strings.Contains(got, "role is affected is yours to change") || strings.Contains(got, "role is instrument") {
		t.Errorf("withInputs with an affected input = %q", got)
	}
}

func TestRoleAndStateAttr(t *testing.T) {
	for role, want := range map[string]string{
		"":              "",
		inputConsumed:   "",
		inputInstrument: ` role="instrument"`,
		inputAffected:   ` role="affected"`,
	} {
		if got := roleAttr(role); got != want {
			t.Errorf("roleAttr(%q) = %q, want %q", role, got, want)
		}
	}
	for state, want := range map[string]string{
		"":         "",
		"approved": ` state="approved"`,
		`"x" & y`:  ` state="&#34;x&#34; &amp; y"`,
	} {
		if got := stateAttr(state); got != want {
			t.Errorf("stateAttr(%q) = %q, want %q", state, got, want)
		}
	}
}

// A change request from before states only asks for changes; one with a
// state says what the reviewer marked the work as.
func TestWithChangeRequest(t *testing.T) {
	if got := withChangeRequest("Do it.", nil); got != "Do it." {
		t.Errorf("withChangeRequest(nil) = %q", got)
	}
	reviewed := []runInput{{Name: "Draft", State: "rejected", Content: "old"}}
	got := withChangeRequest("Do it.", &changeRequest{Reviewer: "Review", Instructions: "Shorter.", Reviewed: reviewed})
	for _, want := range []string{
		`The reviewer "Review" requested changes.`,
		"\n<reviewed name=\"Draft\" state=\"rejected\">\nold\n</reviewed>\n",
		"\n<change_request>\nShorter.\n</change_request>\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("withChangeRequest = %q, want it to hold %q", got, want)
		}
	}
	got = withChangeRequest("Do it.", &changeRequest{Reviewer: "Review", State: "needs work", Instructions: "Fix."})
	if !strings.Contains(got, `The reviewer "Review" marked the work below as "needs work".`) || strings.Contains(got, "<reviewed") {
		t.Errorf("withChangeRequest with a state = %q", got)
	}
}

func TestNewSessionID(t *testing.T) {
	a, err1 := newSessionID()
	b, err2 := newSessionID()
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if len(a) != 32 || strings.Trim(a, "0123456789abcdef") != "" || a == b {
		t.Errorf("newSessionID = %q, %q, want two distinct 32-digit hex ids", a, b)
	}
}

func TestCallWorker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo":
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "not JSON", http.StatusBadRequest)
				return
			}
			b, _ := io.ReadAll(r.Body)
			w.Write(b)
		case "/empty":
			if r.ContentLength > 0 {
				http.Error(w, "a body", http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"default":"a/b","models":["a/b"]}`)
		case "/garbage":
			io.WriteString(w, "not json")
		default:
			http.Error(w, "  no such thing\n", http.StatusTeapot)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	var out map[string]int
	if err := callWorker(ctx, http.MethodPost, srv.URL+"/echo", map[string]int{"n": 7}, &out); err != nil || out["n"] != 7 {
		t.Errorf("callWorker echo = %v, %v", out, err)
	}
	var models agentModels
	if err := callWorker(ctx, http.MethodGet, srv.URL+"/empty", nil, &models); err != nil || models.Default != "a/b" {
		t.Errorf("callWorker without a body = %+v, %v", models, err)
	}
	if err := callWorker(ctx, http.MethodGet, srv.URL+"/garbage", nil, &out); err == nil || !strings.HasPrefix(err.Error(), "decode response") {
		t.Errorf("callWorker of garbage = %v", err)
	}
	if err := callWorker(ctx, http.MethodGet, srv.URL+"/missing", nil, &out); err == nil || err.Error() != "status 418: no such thing" {
		t.Errorf("callWorker of an error = %v", err)
	}
}

// The worker streams a record per line: each message of the agent's
// conversation as it ends, then the result.
func TestStreamAgent(t *testing.T) {
	var reply string
	var got agentRun
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/run" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, reply)
	}))
	defer srv.Close()

	for _, c := range []struct {
		name, reply string
		messages    int
		res         runResult
	}{
		{"result", `{"message":{"role":"user"}}` + "\n" + `{"message":{"role":"assistant"}}` + "\n" + `{"result":{"ok":true,"output":"done"}}` + "\n",
			2, runResult{OK: true, Output: "done"}},
		{"failure", `{"result":{"ok":false,"error":"boom"}}`, 0, runResult{Error: "boom"}},
		{"no result", `{"message":{"role":"user"}}` + "\n", 1, runResult{Error: "worker unavailable: it ended the run without a result"}},
	} {
		reply = c.reply
		var messages int
		res := streamAgent(context.Background(), srv.URL, agentRun{ProcessName: "Write", Prompt: "p"}, func(json.RawMessage) { messages++ })
		if res.OK != c.res.OK || res.Output != c.res.Output || res.Error != c.res.Error || messages != c.messages {
			t.Errorf("%s: streamAgent = %+v with %d messages, want %+v with %d", c.name, res, messages, c.res, c.messages)
		}
		if got.ProcessName != "Write" || got.Prompt != "p" {
			t.Errorf("%s: the worker got %+v", c.name, got)
		}
	}

	res := streamAgent(context.Background(), srv.URL+"/nowhere", agentRun{}, func(json.RawMessage) {})
	if !strings.HasPrefix(res.Error, "worker unavailable: status 404") {
		t.Errorf("streamAgent of a missing route = %+v", res)
	}
	// A stopped run says so rather than blaming the worker.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := streamAgent(ctx, srv.URL, agentRun{}, func(json.RawMessage) {}); res.Error != "it was stopped" {
		t.Errorf("streamAgent of a stopped run = %+v", res)
	}
}
