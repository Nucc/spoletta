package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A provider pi knows reads its own variable unless models.json names
// another, as a gateway's providers sharing one do, and is reached where
// models.json says; one whose key is a command has no variable.
func TestHostProvidersOf(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`{"providers": {
		"openai": {"baseUrl": "https://gw/openai"},
		"fireworks": {"apiKey": "${GATEWAY_KEY}", "baseUrl": "https://gw/fireworks/v1"},
		"amazon-bedrock": {"apiKey": "GATEWAY_KEY"},
		"local": {"apiKey": "!pass show local", "baseUrl": "http://localhost:11434/v1", "api": "openai-completions"}
	}}`), 0o644)
	got, err := hostProvidersOf(dir, agentModels{
		Default: "fireworks/accounts/fireworks/models/kimi",
		Models:  []string{"openai/gpt-5", "amazon-bedrock/claude", "local/llama", "anthropic/claude"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []hostProvider{
		{ID: "amazon-bedrock", Env: "GATEWAY_KEY"},
		{ID: "anthropic", Env: "ANTHROPIC_API_KEY"},
		{ID: "fireworks", Env: "GATEWAY_KEY", BaseURL: "https://gw/fireworks/v1"},
		{ID: "local", BaseURL: "http://localhost:11434/v1", API: "openai-completions"},
		{ID: "openai", Env: "OPENAI_API_KEY", BaseURL: "https://gw/openai"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("host providers = %+v\nwant %+v", got, want)
	}
}

// pi gets the worker's AWS credentials, and still no other variable; the
// git token goes only to the worker's own git.
func TestAgentsGetCloudCredentialsButNotTheGitToken(t *testing.T) {
	t.Setenv("GIT_TOKEN", "secret-git")
	t.Setenv("ANTHROPIC_API_KEY", "secret-model")
	t.Setenv("AWS_REGION", "eu-central-1")
	env := withCloudCredentials(childEnv())
	if !slices.Contains(env, "AWS_REGION=eu-central-1") || slices.Contains(env, "GIT_TOKEN=secret-git") || slices.Contains(env, "ANTHROPIC_API_KEY=secret-model") {
		t.Errorf("pi's environment = %v", envNames(env))
	}
	if git := childEnv(gitCredentials()...); !slices.Contains(git, "GIT_TOKEN=secret-git") {
		t.Errorf("git's environment = %v", envNames(git))
	}
}
