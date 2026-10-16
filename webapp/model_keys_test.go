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

func TestWorkerKeys(t *testing.T) {
	reply, status := "", http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/keys" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	defer srv.Close()

	reply = `[{"env":"ANTHROPIC_API_KEY","providers":["anthropic"],"set":true},{"env":"OPENROUTER_API_KEY","providers":["openrouter","gateway"]}]`
	keys, err := workerKeys(context.Background(), srv.URL)
	want := []workerKey{
		{Env: "ANTHROPIC_API_KEY", Providers: []string{"anthropic"}, Set: true},
		{Env: "OPENROUTER_API_KEY", Providers: []string{"openrouter", "gateway"}},
	}
	if err != nil || !reflect.DeepEqual(keys, want) {
		t.Errorf("workerKeys = %+v, %v, want %+v", keys, err, want)
	}

	reply, status = "down for maintenance", http.StatusServiceUnavailable
	if keys, err := workerKeys(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "status 503: down for maintenance") || keys != nil {
		t.Errorf("workerKeys of a worker that's down = %+v, %v", keys, err)
	}
}
