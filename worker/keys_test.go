package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Providers of a gateway reading one variable share a key; a provider pi
// knows reads its own; one whose models.json key is a command has none.
func TestModelKeysOf(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers": {
		"openai": {"baseUrl": "https://gw/openai"},
		"fireworks": {"apiKey": "${GATEWAY_KEY}"},
		"amazon-bedrock": {"apiKey": "GATEWAY_KEY"},
		"local": {"apiKey": "!pass show local"}
	}}`), 0o644)
	keys, err := modelKeysOf(dir, agentModels{
		Default: "fireworks/accounts/fireworks/models/kimi",
		Models:  []string{"openai/gpt-5", "amazon-bedrock/claude", "local/llama", "anthropic/claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := modelKeys{
		{Env: "", Providers: []string{"local"}},
		{Env: "ANTHROPIC_API_KEY", Providers: []string{"anthropic"}},
		{Env: "GATEWAY_KEY", Providers: []string{"amazon-bedrock", "fireworks"}},
		{Env: "OPENAI_API_KEY", Providers: []string{"openai"}},
	}
	if !slices.EqualFunc(keys, want, func(a, b modelKey) bool { return a.Env == b.Env && slices.Equal(a.Providers, b.Providers) }) {
		t.Errorf("keys = %+v", keys)
	}

	t.Setenv("GATEWAY_KEY", "from-env")
	if r := keys.report(); !r[2].Set || r[1].Set {
		t.Errorf("report = %+v", r)
	}
	run := runRequest{Keys: map[string]string{"GATEWAY_KEY": "saved", "UNOFFERED_API_KEY": "x"}}
	env, err := run.withKeys([]string{"PATH=/bin"})
	if err != nil || !slices.Contains(env, "GATEWAY_KEY=saved") || slices.Contains(env, "GATEWAY_KEY=from-env") || slices.ContainsFunc(env, func(kv string) bool { return kv[:9] == "UNOFFERED" }) {
		t.Errorf("env = %v, %v", envNames(env), err)
	}
	// Without a key of the run's, the worker's own is used.
	if env, err := (runRequest{}).withKeys(nil); err != nil || !slices.Contains(env, "GATEWAY_KEY=from-env") {
		t.Errorf("env without saved keys = %v, %v", envNames(env), err)
	}
}
