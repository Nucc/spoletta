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

// Without sign-in the webapp listens only on this machine, unless HOST says
// where; with it, everywhere.
func TestListenAddr(t *testing.T) {
	t.Setenv("PORT", "9000")
	for _, c := range []struct {
		host   string
		signIn bool
		want   string
	}{
		{"", false, "127.0.0.1:9000"},
		{"", true, ":9000"},
		{"0.0.0.0", false, "0.0.0.0:9000"},
		{"10.0.0.5", true, "10.0.0.5:9000"},
	} {
		t.Setenv("HOST", c.host)
		if got := listenAddr(c.signIn); got != c.want {
			t.Errorf("listenAddr(%v) with HOST=%q = %q, want %q", c.signIn, c.host, got, c.want)
		}
	}
}
