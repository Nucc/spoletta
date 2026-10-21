package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Only a request with the token gets through; the health check needs none.
func TestRequireToken(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := requireToken("s3cret", ok)
	for _, c := range []struct {
		method, path, auth string
		want               int
	}{
		{"POST", "/script", "", http.StatusUnauthorized},
		{"POST", "/script", "Bearer wrong", http.StatusUnauthorized},
		{"POST", "/script", "s3cret", http.StatusUnauthorized},
		{"POST", "/script", "Bearer s3cret", http.StatusOK},
		{"GET", "/health", "", http.StatusOK},
		{"POST", "/health", "", http.StatusUnauthorized},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if c.auth != "" {
			r.Header.Set("Authorization", c.auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s %s with %q = %d, want %d", c.method, c.path, c.auth, w.Code, c.want)
		}
	}
	if requireToken("", ok) == nil {
		t.Error("without a token, requests should pass")
	}
}
