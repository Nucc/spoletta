package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The catalog holds the providers that can be added, each with its chat
// models, and knows every provider's models by ID.
func TestReadCatalog(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "anthropic.json"), []byte(`{"anthropic-messages": {
		"chat:claude-b": {"id": "claude-b", "name": "Claude B", "reasoning": true, "input": ["text", "image"], "contextWindow": 1000000, "maxTokens": 128000},
		"chat:claude-a": {"id": "claude-a", "name": "Claude A", "cost": {"input": 3, "output": 15, "cacheRead": 0.3, "cacheWrite": 3.75}},
		"image:painter": {"id": "painter"}
	}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "amazon-bedrock.json"), []byte(`{"bedrock-converse-stream": {
		"chat:global.anthropic.claude-b": {"id": "global.anthropic.claude-b", "name": "Claude B (global)", "reasoning": true, "thinkingLevelMap": {"off": "none", "minimal": null}}
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
	if m := c.byID["global.anthropic.claude-b"].ThinkingLevelMap; len(m) != 2 || *m["off"] != "none" || m["minimal"] != nil {
		t.Errorf("thinking levels = %v", m)
	}
	if p := c.Prices["anthropic"]["claude-a"]; p != (modelCost{3, 15, 0.3, 3.75}) || len(c.Prices["anthropic"]) != 1 {
		t.Errorf("prices = %+v", c.Prices)
	}
}

// A provider goes over the host's entry for it; a custom one's models get
// what the catalog knows of their IDs. Every provider the step reaches
// points at the proxy with the step's token, and no key the host's
// models.json holds is left.
func TestProvidersModels(t *testing.T) {
	host := []byte(`{"providers": {
		"openai": {"baseUrl": "https://gw/openai", "models": [{"id": "gpt-x"}]},
		"fireworks": {"apiKey": "${OPENAI_API_KEY}", "baseUrl": "https://gw/fireworks/v1"},
		"local": {"apiKey": "sk-literal", "baseUrl": "http://localhost:11434/v1"}
	}}`)
	none := "none"
	known := map[string]catalogModel{"global.anthropic.claude-b": {ID: "global.anthropic.claude-b", Name: "Claude B", Reasoning: true, Input: []string{"text", "image"}, ContextWindow: 1000000, MaxTokens: 128000,
		ThinkingLevelMap: map[string]*string{"off": &none, "minimal": nil}}}
	llm := &llmAccess{URL: "http://webapp:8080/llm/", Token: "step-token-0123456789", Providers: []string{"openai", "fireworks", "company-gateway", "anthropic"}}
	out, err := providersModels(host, []modelProvider{
		{ID: "openai", Env: "OPENAI_API_KEY", Models: []string{"gpt-5"}},
		{ID: "company-gateway", Name: "Company gateway", API: "bedrock-converse-stream", BaseURL: "https://gateway.example.com/bedrock", Env: "COMPANY_GATEWAY_API_KEY",
			Models: []string{"global.anthropic.claude-b", "unknown-model"}},
	}, known, llm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "sk-literal") || strings.Contains(string(out), "${") {
		t.Errorf("a key is left: %s", out)
	}
	var got struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	json.Unmarshal(out, &got)
	proxied := func(id string) bool {
		e := got.Providers[id]
		return e["baseUrl"] == "http://webapp:8080/llm/"+id && e["apiKey"] == "step-token-0123456789"
	}
	if openai := got.Providers["openai"]; len(openai) != 2 || !proxied("openai") {
		t.Errorf("openai = %v", openai)
	}
	if !proxied("fireworks") || !proxied("anthropic") {
		t.Errorf("fireworks = %v, anthropic = %v", got.Providers["fireworks"], got.Providers["anthropic"])
	}
	if local := got.Providers["local"]; local["apiKey"] != nil || local["baseUrl"] != "http://localhost:11434/v1" {
		t.Errorf("a provider the step doesn't reach = %v", local)
	}
	gw := got.Providers["company-gateway"]
	if !proxied("company-gateway") || gw["api"] != "bedrock-converse-stream" || gw["name"] != "Company gateway" {
		t.Errorf("company-gateway = %v", gw)
	}
	models := gw["models"].([]any)
	if m := models[0].(map[string]any); m["reasoning"] != true || m["contextWindow"] != float64(1000000) || m["maxTokens"] != float64(128000) || m["api"] != nil ||
		m["thinkingLevelMap"] == nil || m["thinkingLevelMap"].(map[string]any)["off"] != "none" {
		t.Errorf("a known model = %v", m)
	}
	if m := models[1].(map[string]any); len(m) != 1 || m["id"] != "unknown-model" {
		t.Errorf("an unknown model = %v", m)
	}

	if out, err := providersModels(nil, nil, known, nil); err != nil || out != nil {
		t.Errorf("without anything: %s, %v", out, err)
	}
	if err := (&llmAccess{URL: "http://webapp/llm", Token: "$ANTHROPIC_API_KEY"}).check(); err == nil {
		t.Error("a token pi would read as a variable passed the check")
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
	if err := writeAgentDir(dir, providers, nil); err != nil {
		t.Fatal(err)
	}
	want, _ := providersModels(host, providers, nil, nil)
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
	if err := writeAgentDir(other, nil, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(other, "models.json")); !json.Valid(b) || strings.Contains(string(b), "anthropic") {
		t.Errorf("without, models.json = %s", b)
	}
}
