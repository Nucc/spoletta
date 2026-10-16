package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Settings left out have their defaults: no timeout or retries, the default
// rounds, and Sentry's default query.
func TestProcessSettingsDefaults(t *testing.T) {
	var s processSettings
	if s.timeout() != 0 || s.retries() != 0 || s.rounds() != defaultRounds || s.sentry() != defaultSentrySettings() {
		t.Errorf("defaults = %v, %d, %d, %+v", s.timeout(), s.retries(), s.rounds(), s.sentry())
	}
	sentry := sentrySettings{Organization: "acme", Limit: 3}
	s = processSettings{Timeout: count(90), Retries: count(2), Rounds: count(5), Sentry: &sentry}
	if s.timeout() != 90*time.Second || s.retries() != 2 || s.rounds() != 5 || s.sentry() != sentry {
		t.Errorf("set = %v, %d, %d, %+v", s.timeout(), s.retries(), s.rounds(), s.sentry())
	}
}

func TestProcessSettingsProblem(t *testing.T) {
	for _, c := range []struct {
		s       processSettings
		problem string
	}{
		{processSettings{}, ""},
		{processSettings{Rounds: count(1), Timeout: count(1), Retries: count(0), Quorum: count(1)}, ""},
		{processSettings{Rounds: count(maxRounds), Timeout: count(maxTimeout), Retries: count(maxRetries)}, ""},
		{processSettings{Rounds: count(0)}, "rounds must be from 1 to 20"},
		{processSettings{Rounds: count(maxRounds + 1)}, "rounds"},
		{processSettings{Timeout: count(0)}, "timeout must be from 1 to 86400 seconds"},
		{processSettings{Timeout: count(maxTimeout + 1)}, "timeout"},
		{processSettings{Retries: count(-1)}, "retries must be from 0 to 5"},
		{processSettings{Retries: count(maxRetries + 1)}, "retries"},
		{processSettings{Quorum: count(0)}, "quorum must be at least 1"},
		{processSettings{Sentry: &sentrySettings{Organization: "acme"}}, ""},
		{processSettings{Sentry: &sentrySettings{Organization: "ac me"}}, "slugs"},
	} {
		msg := c.s.problem()
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("problem(%+v) = %q, want %q", c.s, msg, c.problem)
		}
	}
}

func TestProcessSettingsScan(t *testing.T) {
	for _, v := range []any{nil, []byte(`{}`), `{}`} {
		s := processSettings{Timeout: count(5)}
		if err := s.Scan(v); err != nil || !reflect.DeepEqual(s, processSettings{}) {
			t.Errorf("Scan(%v) = %+v, %v, want no settings", v, s, err)
		}
	}
	var s processSettings
	if err := s.Scan(42); err == nil {
		t.Error("Scan(42) took it")
	}
	if err := s.Scan(`{"timeout":`); err == nil {
		t.Error("Scan of broken JSON took it")
	}

	// What is stored comes back the same, and an empty list of repositories
	// (none) stays apart from no list (all of them).
	none := []string{}
	for _, in := range []processSettings{
		{},
		{Repositories: &none},
		{Sentry: &sentrySettings{Organization: "acme", Sort: "new", Limit: 4}, MCPServers: []int64{1, 2},
			Repositories: &[]string{"app"}, Flow: ptr(9), Rounds: count(2), Timeout: count(60), Retries: count(1), Quorum: count(3), DescribeFlow: true},
	} {
		v, err := in.Value()
		if err != nil {
			t.Fatal(err)
		}
		var out processSettings
		if err := out.Scan(v); err != nil || !reflect.DeepEqual(in, out) {
			t.Errorf("Scan(Value()) = %+v, %v, want %+v", out, err, in)
		}
	}
	if v, _ := (processSettings{}).Value(); v != "{}" {
		t.Errorf("Value() of no settings = %v, want {}", v)
	}
}

func TestValidKind(t *testing.T) {
	for _, k := range []string{kindAgent, kindScript, kindHuman, kindFlow, kindSubflow, kindSentry} {
		if !validKind(k) {
			t.Errorf("validKind(%q) = false", k)
		}
	}
	for _, k := range []string{"", "Agent", "module", "struct"} {
		if validKind(k) {
			t.Errorf("validKind(%q) = true", k)
		}
	}
}

// No thinking level uses pi's default.
func TestValidThinking(t *testing.T) {
	for _, l := range append([]string{""}, thinkingLevels...) {
		if !validThinking(l) {
			t.Errorf("validThinking(%q) = false", l)
		}
	}
	for _, l := range []string{"High", "ultra", " low"} {
		if validThinking(l) {
			t.Errorf("validThinking(%q) = true", l)
		}
	}
}

func TestValidWidth(t *testing.T) {
	width := func(w float64) *float64 { return &w }
	for _, c := range []struct {
		w  *float64
		ok bool
	}{
		{nil, true},
		{width(minWidth), true},
		{width(300.5), true},
		{width(maxWidth), true},
		{width(minWidth - 0.1), false},
		{width(maxWidth + 1), false},
		{width(0), false},
		{width(-200), false},
	} {
		if validWidth(c.w) != c.ok {
			t.Errorf("validWidth(%v) = %v", c.w, !c.ok)
		}
	}
}

// Model ids go to pi as arguments, so none may read as an option.
func TestValidModel(t *testing.T) {
	for m, ok := range map[string]bool{
		"":                          true,
		"anthropic/claude-sonnet-4": true,
		"a-b/c-d":                   true,
		"-x":                        false,
		"--model=evil":              false,
		strings.Repeat("m", 255):    true,
		strings.Repeat("m", 256):    false,
	} {
		if validModel(m) != ok {
			t.Errorf("validModel(%q) = %v, want %v", m, !ok, ok)
		}
	}
}

func TestSameFlow(t *testing.T) {
	for _, c := range []struct {
		a, b *int64
		same bool
	}{
		{nil, nil, true},
		{ptr(1), ptr(1), true},
		{ptr(1), ptr(2), false},
		{ptr(1), nil, false},
		{nil, ptr(1), false},
	} {
		if sameFlow(c.a, c.b) != c.same {
			t.Errorf("sameFlow(%v, %v) = %v", c.a, c.b, !c.same)
		}
	}
}
