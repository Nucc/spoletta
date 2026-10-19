package tests

// Model providers admins add in the settings: one pi knows, reached with its
// key, or a custom one, such as a company's AI gateway, at its URL; the key
// saved here or kept in a vault, in the worker's environment.

import (
	"net/http"
	"slices"
	"testing"
)

type settingsProvider struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	API       string   `json:"api"`
	BaseURL   string   `json:"baseUrl"`
	KeySource string   `json:"keySource"`
	Env       string   `json:"env"`
	Models    []string `json:"models"`
	Hint      string   `json:"hint"`
	EnvSet    *bool    `json:"envSet"`
}

type settingsProviders struct {
	Providers   []settingsProvider `json:"providers"`
	Default     string             `json:"default"`
	WorkerError string             `json:"workerError"`
}

// Adding a provider offers the models picked of it, the first of the first
// becoming the default; every agent gets the providers, and the keys saved
// here, never the ones a vault keeps. Only an admin sees and changes them.
func TestModelProviders(t *testing.T) {
	db := secretsDB(t)
	t.Cleanup(func() {
		db.Exec("DELETE FROM model_providers")
		db.Exec("DELETE FROM model_default")
	})
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	// Each answer decoded afresh, as the last one's fields would stay.
	providers := func(method, path string, body any) settingsProviders {
		t.Helper()
		var ps settingsProviders
		a.call(method, path, body, http.StatusOK, &ps)
		return ps
	}
	ps := providers("GET", "/api/model-providers", nil)
	if len(ps.Providers) != 0 || ps.Default != "" {
		t.Errorf("before any is added: %+v", ps)
	}

	ps = providers("POST", "/api/model-providers", map[string]any{
		"id": "anthropic", "keySource": "saved", "key": "sk-ant-1234", "models": []string{"claude-a", "claude-b"},
	})
	if len(ps.Providers) != 1 || ps.Providers[0].Name != "Anthropic" || ps.Providers[0].Hint != "1234" || ps.Providers[0].Env != "MODEL_PROVIDER_ANTHROPIC_KEY" || ps.Default != "anthropic/claude-a" {
		t.Errorf("with Anthropic: %+v", ps)
	}
	if v := secretsStored(t, db, "SELECT api_key FROM model_providers WHERE id = 'anthropic'"); v == "sk-ant-1234" {
		t.Errorf("the key was kept as it came")
	}

	// A gateway of the company's own, its key in a vault.
	ps = providers("POST", "/api/model-providers", map[string]any{
		"name": "Company gateway", "api": "bedrock-converse-stream", "baseUrl": "https://gateway.example.com/bedrock",
		"keySource": "env", "env": "VAULT_GATEWAY_KEY", "models": []string{"global.anthropic.claude-b", " "},
	})
	gw := ps.Providers[1]
	if gw.ID != "company-gateway" || gw.KeySource != "env" || gw.Env != "VAULT_GATEWAY_KEY" || gw.EnvSet == nil || !*gw.EnvSet || gw.Hint != "" || !slices.Equal(gw.Models, []string{"global.anthropic.claude-b"}) {
		t.Errorf("the gateway: %+v", gw)
	}
	if ps.Default != "anthropic/claude-a" {
		t.Errorf("a second provider changed the default to %q", ps.Default)
	}

	for _, c := range []struct {
		body map[string]any
		msg  string
	}{
		{map[string]any{"id": "made-up", "keySource": "saved", "key": "k", "models": []string{"m"}}, "doesn't know"},
		{map[string]any{"id": "anthropic", "keySource": "saved", "key": "k", "models": []string{"m"}}, "already"},
		{map[string]any{"id": "openai", "keySource": "saved", "models": []string{"gpt-x"}}, "key is required"},
		{map[string]any{"id": "openai", "keySource": "saved", "key": "k"}, "at least one model"},
		{map[string]any{"id": "openai", "keySource": "saved", "key": "k", "models": []string{"gpt-x:high"}}, "thinking level"},
		{map[string]any{"id": "openai", "keySource": "env", "env": "lower", "models": []string{"gpt-x"}}, "capital letters"},
		{map[string]any{"id": "openai", "keySource": "env", "env": "MODEL_PROVIDER_ANTHROPIC_KEY", "models": []string{"gpt-x"}}, "Anthropic reads its key"},
		{map[string]any{"name": "OpenAI", "api": "openai-completions", "baseUrl": "https://x.example.com", "keySource": "saved", "key": "k", "models": []string{"m"}}, "provider pi knows"},
		{map[string]any{"name": "Other", "api": "openai-completions", "baseUrl": "x.example.com", "keySource": "saved", "key": "k", "models": []string{"m"}}, "http or https"},
		{map[string]any{"name": "Other", "api": "smoke", "baseUrl": "https://x.example.com", "keySource": "saved", "key": "k", "models": []string{"m"}}, "API"},
	} {
		a.refused("POST", "/api/model-providers", c.body, http.StatusBadRequest, c.msg)
	}

	var models agentModels
	a.call("GET", "/api/models", nil, http.StatusOK, &models)
	if !slices.Equal(models.Models, []string{"fake/model", "anthropic/claude-a", "anthropic/claude-b", "company-gateway/global.anthropic.claude-b"}) ||
		models.Default != "anthropic/claude-a" || models.Providers["company-gateway"] != "Company gateway" {
		t.Errorf("models offered: %+v", models)
	}

	// An agent of no model of its own takes the default, and gets the
	// providers, with the saved key and not the vault's.
	f := a.newFlow("Providers")
	usersShare(a, f.ID)
	a.startAt(f.ID, a.agent(f.ID, "Write", "Write.").ID)
	a.worker.onAgent("Write", handsIn(data("done")))
	bob := usersNewPerson("Bob")
	usersAdd(a, bob.email)
	usersSignIn(a, p, bob)
	a.refused("GET", "/api/model-providers", nil, http.StatusForbidden, "only an admin")
	a.refused("POST", "/api/model-providers", map[string]any{"id": "openai"}, http.StatusForbidden, "only an admin")
	a.refused("DELETE", "/api/model-providers/anthropic", nil, http.StatusForbidden, "only an admin")
	a.refused("PUT", "/api/model-default", map[string]any{"model": "fake/model"}, http.StatusForbidden, "only an admin")
	a.finish(a.startRun(f.ID).ID)
	last := func() agentRun {
		runs := a.worker.runsOf("Write")
		return runs[len(runs)-1]
	}
	run := last()
	if run.Model != "anthropic/claude-a" || len(run.Providers) != 2 || run.Providers[1].BaseURL != "https://gateway.example.com/bedrock" ||
		run.Keys["MODEL_PROVIDER_ANTHROPIC_KEY"] != "sk-ant-1234" || run.Keys["ANTHROPIC_API_KEY"] != "" || run.Keys["VAULT_GATEWAY_KEY"] != "" {
		t.Errorf("bob's agent got %+v", run)
	}

	usersSignIn(a, p, usersAda)
	a.refused("PUT", "/api/model-default", map[string]any{"model": "anthropic/claude-z"}, http.StatusBadRequest, "no model offered")
	ps = providers("PUT", "/api/model-default", map[string]any{"model": "company-gateway/global.anthropic.claude-b"})
	if ps.Default != "company-gateway/global.anthropic.claude-b" {
		t.Errorf("default = %q", ps.Default)
	}

	// Moving Anthropic's key to the vault, its saved one goes.
	ps = providers("PUT", "/api/model-providers/anthropic", map[string]any{"keySource": "env", "env": "", "models": []string{"claude-b"}})
	if an := ps.Providers[0]; an.KeySource != "env" || an.Env != "ANTHROPIC_API_KEY" || an.Hint != "" || an.EnvSet == nil || *an.EnvSet {
		t.Errorf("anthropic from the vault: %+v", an)
	}
	a.finish(a.startRun(f.ID).ID)
	if run := last(); run.Model != "company-gateway/global.anthropic.claude-b" || run.Keys["MODEL_PROVIDER_ANTHROPIC_KEY"] != "" || run.Keys["ANTHROPIC_API_KEY"] != "" {
		t.Errorf("then the agent got %+v", run)
	}
	// Changing its URL alone, a saved key stays.
	providers("PUT", "/api/model-providers/company-gateway", map[string]any{
		"name": "Company gateway", "api": "bedrock-converse-stream", "baseUrl": "https://gateway.example.com/v2/bedrock",
		"keySource": "saved", "key": "gw-key-5678", "models": []string{"global.anthropic.claude-b"},
	})
	ps = providers("PUT", "/api/model-providers/company-gateway", map[string]any{
		"name": "Company gateway", "api": "bedrock-converse-stream", "baseUrl": "https://gateway.example.com/v3/bedrock",
		"keySource": "saved", "models": []string{"global.anthropic.claude-b"},
	})
	if gw := ps.Providers[1]; gw.Hint != "5678" || gw.BaseURL != "https://gateway.example.com/v3/bedrock" {
		t.Errorf("the gateway with a new URL: %+v", gw)
	}

	// A key saved for a provider goes in a variable of its own, never one
	// the worker's pi config reads, such as a gateway's OPENAI_API_KEY.
	providers("POST", "/api/model-providers", map[string]any{"id": "openai", "keySource": "saved", "key": "sk-openai", "models": []string{"gpt-x"}})
	a.finish(a.startRun(f.ID).ID)
	if run := last(); run.Keys["MODEL_PROVIDER_OPENAI_KEY"] != "sk-openai" || run.Keys["OPENAI_API_KEY"] != "" {
		t.Errorf("with OpenAI's key saved, the agent got keys %v", run.Keys)
	}
	providers("DELETE", "/api/model-providers/openai", nil)

	// A provider added takes the worker's own models of it out of what is
	// offered, and of the keys they read.
	ps = providers("POST", "/api/model-providers", map[string]any{"id": "amazon-bedrock", "keySource": "env", "models": []string{"global.anthropic.claude-b"}})
	var keys settingsKeys
	a.call("GET", "/api/model-keys", nil, http.StatusOK, &keys)
	if k := keys.Keys[0]; k.Env != "GATEWAY_KEY" || !slices.Equal(k.Providers, []string{"fireworks"}) {
		t.Errorf("the worker's keys: %+v", keys.Keys)
	}

	// A custom provider's models, as it says.
	var listed struct {
		Models []string `json:"models"`
	}
	a.call("POST", "/api/model-providers/models", map[string]any{"baseUrl": "https://gateway.example.com/v1", "api": "openai-completions", "keySource": "saved", "key": "typed-key"}, http.StatusOK, &listed)
	if !slices.Equal(listed.Models, []string{"gw-1", "gw-2"}) {
		t.Errorf("listed %v", listed.Models)
	}
	a.refused("POST", "/api/model-providers/models", map[string]any{"baseUrl": "https://gateway.example.com/v1", "api": "openai-completions", "keySource": "saved", "key": "wrong"}, http.StatusBadGateway, "401 Unauthorized")

	// Removing the default's provider makes the first one's first model
	// the default; removing all, the worker's own is again.
	ps = providers("DELETE", "/api/model-providers/company-gateway", nil)
	if ps.Default != "anthropic/claude-b" {
		t.Errorf("after removing the gateway, default = %q", ps.Default)
	}
	providers("DELETE", "/api/model-providers/anthropic", nil)
	ps = providers("DELETE", "/api/model-providers/amazon-bedrock", nil)
	a.call("GET", "/api/models", nil, http.StatusOK, &models)
	if ps.Default != "" || models.Default != "fake/model" || !slices.Equal(models.Models, []string{"fake/model"}) {
		t.Errorf("after removing all: %+v, %+v", ps, models)
	}
	a.finish(a.startRun(f.ID).ID)
	if run := last(); run.Model != "" || len(run.Providers) != 0 {
		t.Errorf("without providers, the agent got %+v", run)
	}
}
