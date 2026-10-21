package main

// The providers of the worker's own pi config: those of the models it
// offers, with where each is reached and the variable it reads its key
// from. A provider pi knows reads its own variable, such as
// ANTHROPIC_API_KEY, and is reached where pi knows it, unless models.json
// gives it a baseUrl, as a gateway's providers do; one models.json defines
// names its variable as its apiKey, "${NAME}" or "NAME", or holds the key
// itself, or a command giving it, which the worker ignores.
//
// The worker holds no model keys. The webapp asks for these providers (GET
// /host-providers) and reaches them for agents through its proxy with the
// keys it has, saved in its settings or in its own environment; each agent
// step's models.json points them at the proxy (see providers.go). Bedrock
// without a key signs in with the worker's AWS credentials instead, which
// pi gets, and only pi.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// The variables pi's own providers read their keys from. amazon-bedrock
// reads a Bedrock API key; without one it signs in with the cloud's
// credentials, which the worker's environment holds.
var builtinKeys = map[string]string{
	"anthropic":      "ANTHROPIC_API_KEY",
	"openai":         "OPENAI_API_KEY",
	"google":         "GEMINI_API_KEY",
	"groq":           "GROQ_API_KEY",
	"cerebras":       "CEREBRAS_API_KEY",
	"xai":            "XAI_API_KEY",
	"openrouter":     "OPENROUTER_API_KEY",
	"zai":            "ZAI_API_KEY",
	"mistral":        "MISTRAL_API_KEY",
	"fireworks":      "FIREWORKS_API_KEY",
	"amazon-bedrock": "AWS_BEARER_TOKEN_BEDROCK",
}

var (
	envReference = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)
	envName      = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// A provider of the worker's pi config, as the webapp gets it. BaseURL and
// API are models.json's, empty for pi's own; Env is empty for a provider
// whose key the worker's config doesn't read from a variable.
type hostProvider struct {
	ID      string `json:"id"`
	Env     string `json:"env"`
	BaseURL string `json:"baseUrl,omitempty"`
	API     string `json:"api,omitempty"`
}

// The providers of the offered models, from the models.json in hostDir.
func hostProvidersOf(hostDir string, models agentModels) ([]hostProvider, error) {
	raw, err := readHostJSON(hostDir, "models.json")
	if err != nil {
		return nil, err
	}
	var config struct {
		Providers map[string]struct {
			APIKey  *string `json:"apiKey"`
			BaseURL string  `json:"baseUrl"`
			API     string  `json:"api"`
		} `json:"providers"`
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &config); err != nil {
			return nil, fmt.Errorf("models.json: %w", err)
		}
	}
	var ids []string
	for _, m := range append([]string{models.Default}, models.Models...) {
		if p, _, ok := strings.Cut(m, "/"); ok && p != "" && !slices.Contains(ids, p) {
			ids = append(ids, p)
		}
	}
	slices.Sort(ids)
	providers := []hostProvider{}
	for _, id := range ids {
		p := hostProvider{ID: id, Env: builtinKeys[id]}
		if c, ok := config.Providers[id]; ok {
			p.BaseURL, p.API = c.BaseURL, c.API
			if c.APIKey != nil {
				switch k := *c.APIKey; {
				case envReference.MatchString(k):
					p.Env = envReference.FindStringSubmatch(k)[1]
				case envName.MatchString(k):
					p.Env = k
				default:
					p.Env = ""
				}
			}
		}
		providers = append(providers, p)
	}
	return providers, nil
}

// The environment with the worker's AWS credentials added, which Bedrock
// signs in with when it has no key; pi gets them, never a script.
func withCloudCredentials(env []string) []string {
	out := slices.Clone(env)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "AWS_") {
			out = append(out, kv)
		}
	}
	return out
}
