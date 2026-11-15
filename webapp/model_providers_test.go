package main

import (
	"slices"
	"testing"
)

// Claude Code takes the models of Anthropic, and of providers added or of
// the worker's own config whose API is Anthropic's, an added one going over
// the worker's of its ID; never by what a model is called.
func TestClaudeProviders(t *testing.T) {
	added := []modelProvider{
		{ID: "acme", API: anthropicMessages, BaseURL: "https://gateway.example.com"},
		{ID: "openai"},
		// The worker's own speaks Anthropic's API, but the one added doesn't.
		{ID: "house", API: "openai-completions", BaseURL: "https://house.example.com"},
	}
	host := []hostProvider{
		{ID: "gateway", API: anthropicMessages},
		{ID: "house", API: anthropicMessages},
		{ID: "openrouter"},
		{ID: "amazon-bedrock", API: "bedrock-converse-stream"},
	}
	c := claudeProvidersOf(added, host)
	for model, want := range map[string]bool{
		"anthropic/claude-sonnet-4-6":            true,
		"acme/claude-opus":                       true,
		"gateway/claude-opus":                    true,
		"gateway/gpt-x":                          true,
		"house/claude-opus":                      false,
		"openai/gpt-x":                           false,
		"openrouter/anthropic/claude-sonnet-4-6": false,
		"amazon-bedrock/global.anthropic.claude": false,
		"nowhere/claude-opus":                    false,
		"claude-sonnet-4-6":                      false,
		"anthropic/":                             false,
		"":                                       false,
	} {
		if got := c.takes(model); got != want {
			t.Errorf("takes(%q) = %v, want %v", model, got, want)
		}
	}
	// Anthropic itself, added or not.
	if !claudeProvidersOf(nil, nil).takes("anthropic/claude-sonnet-4-6") || !claudeProvidersOf([]modelProvider{{ID: "anthropic"}}, nil).takes("anthropic/claude-sonnet-4-6") {
		t.Error("Anthropic's models aren't taken")
	}

	// A step's own model it can't use is named; a default it can't use is
	// said in the words people see on the step.
	if msg := claudeModelProblem(c, "acme/claude-opus", true); msg != "" {
		t.Errorf("a gateway's model: %q", msg)
	}
	if msg := claudeModelProblem(c, "openai/gpt-x", true); msg != "Claude Code can't use the model openai/gpt-x: it takes only models of a provider speaking Anthropic's API; pick another for the process" {
		t.Errorf("a model of OpenAI's: %q", msg)
	}
	for _, def := range []string{"openai/gpt-x", ""} {
		if msg := claudeModelProblem(c, def, false); msg != "the default model isn't one Claude Code can use; pick one for the process" {
			t.Errorf("the default %q: %q", def, msg)
		}
	}
	if msg := claudeModelProblem(c, "anthropic/claude-sonnet-4-6", false); msg != "" {
		t.Errorf("a default of Anthropic's: %q", msg)
	}
}

// The models offered say which coding agents the worker offers, pi alone
// when it says none, and which of them Claude Code may use.
func TestOfferModels(t *testing.T) {
	worker := agentModels{Default: "openai/gpt-x", Models: []string{"openai/gpt-x", "gateway/claude-opus", "acme/old"}, Harnesses: []string{"pi", "claude"}}
	host := []hostProvider{{ID: "gateway", API: anthropicMessages}, {ID: "openai"}}
	added := []modelProvider{
		{ID: "anthropic", Name: "Anthropic", Models: []string{"claude-sonnet-4-6"}},
		{ID: "acme", Name: "Acme gateway", API: anthropicMessages, BaseURL: "https://gateway.example.com", Models: []string{"claude-opus"}},
	}
	m := offerModels(worker, added, host, "anthropic/claude-sonnet-4-6")
	if want := []string{"openai/gpt-x", "gateway/claude-opus", "anthropic/claude-sonnet-4-6", "acme/claude-opus"}; !slices.Equal(m.Models, want) {
		t.Errorf("models = %v, want %v", m.Models, want)
	}
	if want := []string{"gateway/claude-opus", "anthropic/claude-sonnet-4-6", "acme/claude-opus"}; !slices.Equal(m.Claude, want) {
		t.Errorf("Claude Code's = %v, want %v", m.Claude, want)
	}
	if !slices.Equal(m.Harnesses, []string{"pi", "claude"}) || m.Default != "anthropic/claude-sonnet-4-6" || m.Providers["acme"] != "Acme gateway" {
		t.Errorf("offered %+v", m)
	}

	// A worker from before there was a choice, or one that couldn't be
	// asked, offers pi alone; with nothing of Anthropic's API, Claude Code
	// has none.
	m = offerModels(agentModels{Default: "openai/gpt-x", Models: []string{"openai/gpt-x"}}, nil, nil, "")
	if !slices.Equal(m.Harnesses, []string{"pi"}) || m.Claude != nil || m.Default != "openai/gpt-x" {
		t.Errorf("an older worker's: %+v", m)
	}
	m = offerModels(agentModels{}, added[:1], nil, "anthropic/claude-sonnet-4-6")
	if !slices.Equal(m.Harnesses, []string{"pi"}) || !slices.Equal(m.Claude, []string{"anthropic/claude-sonnet-4-6"}) {
		t.Errorf("without the worker: %+v", m)
	}
	if !slices.Equal(offeredHarnesses([]string{}), []string{"pi"}) || !slices.Equal(offeredHarnesses([]string{"pi", "claude"}), []string{"pi", "claude"}) {
		t.Error("offeredHarnesses")
	}
}
