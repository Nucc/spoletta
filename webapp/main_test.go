package main

import "testing"

// An unset or empty variable falls back.
func TestEnv(t *testing.T) {
	t.Setenv("SPOLETTA_TEST_SET", "value")
	t.Setenv("SPOLETTA_TEST_EMPTY", "")
	for key, want := range map[string]string{
		"SPOLETTA_TEST_SET":   "value",
		"SPOLETTA_TEST_EMPTY": "fallback",
		"SPOLETTA_TEST_UNSET": "fallback",
	} {
		if got := env(key, "fallback"); got != want {
			t.Errorf("env(%q) = %q, want %q", key, got, want)
		}
	}
}
