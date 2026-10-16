package main

import "testing"

// A link without a stored route has null for one, so the frontend draws it
// its own way.
func TestRouteJSON(t *testing.T) {
	for stored, want := range map[string]string{
		"":          "null",
		"[10,20.5]": "[10,20.5]",
		"[]":        "[]",
	} {
		if got := string(routeJSON([]byte(stored))); got != want {
			t.Errorf("routeJSON(%q) = %s, want %s", stored, got, want)
		}
	}
	if got := string(routeJSON(nil)); got != "null" {
		t.Errorf("routeJSON(nil) = %s", got)
	}
}
