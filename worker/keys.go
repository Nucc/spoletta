package main

// Model keys: pi reaches each model's provider with a key it reads from an
// environment variable. A provider pi knows reads its own, such as
// ANTHROPIC_API_KEY; one models.json defines names its variable as its
// apiKey, "${NAME}" or "NAME", or holds the key itself, or a command
// giving it. Several providers may read the same variable, as the providers
// of one gateway do.
//
// The webapp keeps keys an admin set and sends them with each run, over the
// worker's own environment; it asks which variables the offered models read
// and which of them the worker has, so its settings show what is missing.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// The variables pi's own providers read their keys from. Others, such as
// amazon-bedrock's, sign in with their cloud's credentials instead, which
// the worker's environment holds.
var builtinKeys = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"google":     "GEMINI_API_KEY",
	"groq":       "GROQ_API_KEY",
	"cerebras":   "CEREBRAS_API_KEY",
	"xai":        "XAI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
	"zai":        "ZAI_API_KEY",
	"mistral":    "MISTRAL_API_KEY",
	"fireworks":  "FIREWORKS_API_KEY",
}

var (
	envReference = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	envName      = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// A variable the offered models read their key from, the providers that
// read it, and whether the worker's environment has it. With no variable,
// the providers' keys are set up otherwise: in models.json, or with cloud
// credentials.
type modelKey struct {
	Env       string   `json:"env"`
	Providers []string `json:"providers"`
	Set       bool     `json:"set"`
}

type modelKeys []modelKey

// The variables a run's keys may set; set once at startup.
var keyNames = map[string]bool{}

// The keys the offered models read, from the models.json in hostDir.
func modelKeysOf(hostDir string, models agentModels) (modelKeys, error) {
	raw, err := readHostJSON(hostDir, "models.json")
	if err != nil {
		return nil, err
	}
	var config struct {
		Providers map[string]struct {
			APIKey *string `json:"apiKey"`
		} `json:"providers"`
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &config); err != nil {
			return nil, fmt.Errorf("models.json: %w", err)
		}
	}
	var providers []string
	for _, m := range append([]string{models.Default}, models.Models...) {
		if p, _, ok := strings.Cut(m, "/"); ok && p != "" && !slices.Contains(providers, p) {
			providers = append(providers, p)
		}
	}
	slices.Sort(providers)

	var keys modelKeys
	add := func(env, provider string) {
		for i := range keys {
			if keys[i].Env == env {
				keys[i].Providers = append(keys[i].Providers, provider)
				return
			}
		}
		keys = append(keys, modelKey{Env: env, Providers: []string{provider}})
	}
	for _, p := range providers {
		env := builtinKeys[p]
		if c, ok := config.Providers[p]; ok && c.APIKey != nil {
			switch k := *c.APIKey; {
			case envReference.MatchString(k):
				env = envReference.FindStringSubmatch(k)[1]
			case envName.MatchString(k):
				env = k
			default:
				env = ""
			}
		}
		add(env, p)
	}
	for _, k := range keys {
		if k.Env != "" {
			keyNames[k.Env] = true
		}
	}
	slices.SortFunc(keys, func(a, b modelKey) int { return strings.Compare(a.Env, b.Env) })
	return keys, nil
}

// The keys, with whether the worker's environment has each.
func (keys modelKeys) report() modelKeys {
	out := slices.Clone(keys)
	for i := range out {
		out[i].Set = out[i].Env != "" && os.Getenv(out[i].Env) != ""
	}
	return out
}

// The environment with the run's model keys in place of the worker's own.
// Only the variables the offered models and the run's providers read are
// set; a key for another, such as one a model no longer offered read, is
// left out. A provider's
// OAuth token, which pi prefers to its key, goes, so the key given is the
// one used.
func (run runRequest) withKeys(env []string) ([]string, error) {
	keys := map[string]string{}
	drop := map[string]bool{}
	for name, key := range run.Keys {
		if !keyNames[name] && !run.providerReads(name) {
			continue
		}
		if strings.ContainsAny(key, "\x00\n") {
			return nil, fmt.Errorf("invalid model key %s", name)
		}
		keys[name] = key
		drop[name] = true
		drop[strings.TrimSuffix(name, "_API_KEY")+"_OAUTH_TOKEN"] = true
	}
	if len(keys) == 0 {
		return env, nil
	}
	out := make([]string, 0, len(env)+len(keys))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			out = append(out, kv)
		}
	}
	for name, key := range keys {
		out = append(out, name+"="+key)
	}
	return out, nil
}
