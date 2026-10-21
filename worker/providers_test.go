package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The catalog holds the providers that can be added, each with its chat
// models, and knows every provider's models by ID.
func TestReadCatalog(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "anthropic.json"), []byte(`{"anthropic-messages": {
		"chat:claude-b": {"id": "claude-b", "name": "Claude B", "reasoning": true, "input": ["text", "image"], "contextWindow": 1000000, "maxTokens": 128000},
		"chat:claude-a": {"id": "claude-a", "name": "Claude A"},
		"image:painter": {"id": "painter"}
	}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "amazon-bedrock.json"), []byte(`{"bedrock-converse-stream": {
		"chat:global.anthropic.claude-b": {"id": "global.anthropic.claude-b", "name": "Claude B (global)", "reasoning": true}
	}}`), 0o644)
	c, err := readCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != len(knownProviders) || c.Providers[0].ID != "anthropic" || c.Providers[0].Env != "ANTHROPIC_API_KEY" {
		t.Fatalf("providers = %+v", c.Providers)
	}
	var ids []string
	for _, m := range c.Providers[0].Models {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"claude-a", "claude-b"}) {
		t.Errorf("anthropic's models = %v", ids)
	}
	bedrock := c.Providers[slices.IndexFunc(c.Providers, func(p catalogProvider) bool { return p.ID == "amazon-bedrock" })]
	if bedrock.Env != "AWS_BEARER_TOKEN_BEDROCK" || len(bedrock.Models) != 1 {
		t.Errorf("amazon-bedrock = %+v", bedrock)
	}
	if m := c.byID["global.anthropic.claude-b"]; !m.Reasoning || c.byID["claude-b"].ContextWindow != 1000000 {
		t.Errorf("by ID = %+v", c.byID)
	}
}

// A provider goes over the host's entry for it, reading its key from its
// variable; a custom one's models get what the catalog knows of their IDs.
func TestProvidersModels(t *testing.T) {
	host := []byte(`{"providers": {
		"openai": {"baseUrl": "https://gw/openai", "models": [{"id": "gpt-x"}]},
		"fireworks": {"apiKey": "${OPENAI_API_KEY}"}
	}}`)
	known := map[string]catalogModel{"global.anthropic.claude-b": {ID: "global.anthropic.claude-b", Name: "Claude B", Reasoning: true, Input: []string{"text", "image"}, ContextWindow: 1000000, MaxTokens: 128000}}
	out, err := providersModels(host, []modelProvider{
		{ID: "openai", Env: "OPENAI_API_KEY", Models: []string{"gpt-5"}},
		{ID: "company-gateway", Name: "Company gateway", API: "bedrock-converse-stream", BaseURL: "https://gateway.example.com/bedrock", Env: "COMPANY_GATEWAY_API_KEY",
			Models: []string{"global.anthropic.claude-b", "unknown-model"}},
	}, known)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	json.Unmarshal(out, &got)
	if openai := got.Providers["openai"]; len(openai) != 1 || openai["apiKey"] != "${OPENAI_API_KEY}" {
		t.Errorf("openai = %v", openai)
	}
	if got.Providers["fireworks"]["apiKey"] != "${OPENAI_API_KEY}" {
		t.Errorf("fireworks = %v", got.Providers["fireworks"])
	}
	gw := got.Providers["company-gateway"]
	if gw["baseUrl"] != "https://gateway.example.com/bedrock" || gw["api"] != "bedrock-converse-stream" || gw["apiKey"] != "${COMPANY_GATEWAY_API_KEY}" || gw["name"] != "Company gateway" {
		t.Errorf("company-gateway = %v", gw)
	}
	models := gw["models"].([]any)
	if m := models[0].(map[string]any); m["reasoning"] != true || m["contextWindow"] != float64(1000000) || m["maxTokens"] != float64(128000) || m["api"] != nil {
		t.Errorf("a known model = %v", m)
	}
	if m := models[1].(map[string]any); len(m) != 1 || m["id"] != "unknown-model" {
		t.Errorf("an unknown model = %v", m)
	}

	if out, err := providersModels(host, nil, known); err != nil || string(out) != string(host) {
		t.Errorf("without providers: %s, %v", out, err)
	}
}

func TestProviderCheck(t *testing.T) {
	good := []modelProvider{
		{ID: "anthropic", Env: "ANTHROPIC_API_KEY", Models: []string{"claude-sonnet-4-6"}},
		{ID: "amazon-bedrock", Env: "VAULT_BEDROCK", Models: []string{"global.anthropic.claude-haiku-4-5-20251001-v1:0"}},
		{ID: "gw", API: "openai-completions", BaseURL: "https://gateway.example.com/v1", Env: "GW_API_KEY", Models: []string{"accounts/fireworks/models/kimi"}},
	}
	for _, p := range good {
		if err := p.check(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	bad := []modelProvider{
		{ID: "Anthropic", Env: "ANTHROPIC_API_KEY"},
		{ID: "made-up", Env: "MADE_UP_API_KEY"},
		{ID: "anthropic", Env: "lower"},
		{ID: "gw", API: "openai-completions", BaseURL: "file:///etc", Env: "GW"},
		{ID: "gw", API: "smoke-signals", BaseURL: "https://gw", Env: "GW"},
		{ID: "anthropic", Env: "ANTHROPIC_API_KEY", Models: []string{"--help"}},
		{ID: "anthropic", Env: "ANTHROPIC_API_KEY", Models: []string{"claude:high"}},
		{ID: "anthropic", Env: "ANTHROPIC_API_KEY", Models: []string{"two words"}},
	}
	for _, p := range bad {
		if p.check() == nil {
			t.Errorf("%+v passed the check", p)
		}
	}
}

// A step's agent directory holds the host's settings, and its models.json
// with the run's providers over the host's; the host's own without any.
func TestWriteAgentDir(t *testing.T) {
	host := []byte(`{"providers": {}}`)
	agentConfig.settings, agentConfig.models = []byte(`{"defaultModel": "a"}`), host
	t.Cleanup(func() { agentConfig.settings, agentConfig.models = nil, nil })
	t.Setenv("PI_MODEL_CATALOG", t.TempDir())

	providers := []modelProvider{{ID: "anthropic", Env: "ANTHROPIC_API_KEY", Models: []string{"claude-a"}}}
	dir := filepath.Join(t.TempDir(), "agent")
	if err := writeAgentDir(dir, providers); err != nil {
		t.Fatal(err)
	}
	want, _ := providersModels(host, providers, nil)
	if b, _ := os.ReadFile(filepath.Join(dir, "models.json")); string(b) != string(want) {
		t.Errorf("with a provider, models.json = %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(b) != `{"defaultModel": "a"}` {
		t.Errorf("settings.json = %s", b)
	}
	if fi, err := os.Stat(dir); err != nil {
		t.Error(err)
	} else if fi.Mode().Perm() != 0o700 {
		t.Errorf("the agent directory's mode = %v; want only the worker's", fi.Mode())
	}
	other := filepath.Join(t.TempDir(), "agent")
	if err := writeAgentDir(other, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(other, "models.json")); string(b) != string(host) {
		t.Errorf("without, models.json = %s", b)
	}
}

// A run's provider's key is set, as the offered models' are, and no other.
func TestWithKeysOfProviders(t *testing.T) {
	run := runRequest{
		Keys:      map[string]string{"GW_API_KEY": "saved", "ELSEWHERE_KEY": "x"},
		Providers: []modelProvider{{ID: "gw", Env: "GW_API_KEY"}},
	}
	t.Setenv("GW_API_KEY", "from-env")
	env, err := run.withKeys([]string{"PATH=/bin"})
	if err != nil || !slices.Contains(env, "GW_API_KEY=saved") || slices.Contains(env, "GW_API_KEY=from-env") || slices.Contains(env, "ELSEWHERE_KEY=x") {
		t.Errorf("env = %v, %v", envNames(env), err)
	}
	t.Setenv("VAULT_KEY", "from-vault")
	if set, err := envSet([]string{"VAULT_KEY", "MISSING_KEY"}); err != nil || !set["VAULT_KEY"] || set["MISSING_KEY"] {
		t.Errorf("envSet = %v, %v", set, err)
	}
	if _, err := envSet([]string{"bad name"}); err == nil {
		t.Error("envSet took a bad name")
	}
}

// A custom provider's models come from its models endpoint, with its key.
func TestProviderModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models" && r.Header.Get("Authorization") == "Bearer from-vault":
			w.Write([]byte(`{"data": [{"id": "b"}, {"id": "a"}, {"id": "a"}]}`))
		case r.URL.Path == "/v1/models" && r.Header.Get("x-api-key") == "typed":
			w.Write([]byte(`{"data": [{"id": "claude"}]}`))
		default:
			http.Error(w, "who are you", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	t.Setenv("GW_API_KEY", "from-vault")

	ids, err := providerModels(context.Background(), providerModelsRequest{BaseURL: srv.URL + "/v1/", API: "openai-completions", Env: "GW_API_KEY"})
	if err != nil || !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("OpenAI's = %v, %v", ids, err)
	}
	ids, err = providerModels(context.Background(), providerModelsRequest{BaseURL: srv.URL, API: "anthropic-messages", Key: "typed"})
	if err != nil || !slices.Equal(ids, []string{"claude"}) {
		t.Errorf("Anthropic's = %v, %v", ids, err)
	}
	if _, err := providerModels(context.Background(), providerModelsRequest{BaseURL: srv.URL + "/v1", API: "openai-completions"}); err == nil {
		t.Error("got models without the key")
	}
	if _, err := providerModels(context.Background(), providerModelsRequest{BaseURL: srv.URL, API: "bedrock-converse-stream"}); err == nil {
		t.Error("got Bedrock's models")
	}
}
