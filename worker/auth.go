package main

// Only the webapp may ask the worker to run anything: with WORKER_TOKEN
// set, every request but GET /health must carry it as a bearer token, so
// whatever else reaches the worker's port can't run bash on it. The webapp
// sends the same WORKER_TOKEN. Either can come from a file named by
// WORKER_TOKEN_FILE instead, as a mounted secret. Without one the worker
// answers anyone, as it did before; compose sets one.

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// WORKER_TOKEN, or what the file WORKER_TOKEN_FILE names holds.
func workerToken() (string, error) {
	path := os.Getenv("WORKER_TOKEN_FILE")
	if path == "" {
		return os.Getenv("WORKER_TOKEN"), nil
	}
	if os.Getenv("WORKER_TOKEN") != "" {
		return "", fmt.Errorf("WORKER_TOKEN and WORKER_TOKEN_FILE are both set; set one")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("WORKER_TOKEN_FILE: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// Refuses requests without the token, unless there is none to ask for.
// Whether the worker is up says nothing, so the health check needs none.
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			reply(w, http.StatusUnauthorized, map[string]string{"error": "the worker takes requests only from the webapp, with WORKER_TOKEN"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
