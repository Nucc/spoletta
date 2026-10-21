package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Keys are saved by the variable pi reads each from.
func TestEnvNamePattern(t *testing.T) {
	for env, ok := range map[string]bool{
		"ANTHROPIC_API_KEY":      true,
		"_KEY":                   true,
		"K2":                     true,
		"":                       false,
		"2K":                     false,
		"anthropic_api_key":      false,
		"API-KEY":                false,
		"API KEY":                false,
		strings.Repeat("K", 255): true,
		strings.Repeat("K", 256): false,
	} {
		if envNamePattern.MatchString(env) != ok {
			t.Errorf("envNamePattern(%q) = %v, want %v", env, !ok, ok)
		}
	}
}

// The worker's providers reading one variable share its key, which the
// webapp's environment has or not; a worker that's down says so, unless it
// said within the minute.
func TestWorkerKeys(t *testing.T) {
	reply, status := "", http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/host-providers" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	t.Cleanup(func() { llm = nil })

	reply, status = "down for maintenance", http.StatusServiceUnavailable
	llm = newLLMBroker(nil, srv.URL, "http://webapp")
	if keys, err := workerKeys(context.Background()); err == nil || !strings.Contains(err.Error(), "status 503: down for maintenance") || keys != nil {
		t.Errorf("workerKeys of a worker that's down = %+v, %v", keys, err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "in-the-webapp")
	reply, status = `[{"id":"anthropic","env":"ANTHROPIC_API_KEY"},{"id":"gateway","env":"OPENROUTER_API_KEY","baseUrl":"https://gw"},{"id":"local","env":""},{"id":"openrouter","env":"OPENROUTER_API_KEY"}]`, http.StatusOK
	keys, err := workerKeys(context.Background())
	want := []workerKey{
		{Env: "", Providers: []string{"local"}},
		{Env: "ANTHROPIC_API_KEY", Providers: []string{"anthropic"}, Set: true},
		{Env: "OPENROUTER_API_KEY", Providers: []string{"gateway", "openrouter"}},
	}
	if err != nil || !reflect.DeepEqual(keys, want) {
		t.Errorf("workerKeys = %+v, %v, want %+v", keys, err, want)
	}
	status = http.StatusServiceUnavailable
	if again, err := workerKeys(context.Background()); err != nil || !reflect.DeepEqual(again, want) {
		t.Errorf("workerKeys within the minute = %+v, %v", again, err)
	}
}
