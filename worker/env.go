package main

// The environment the worker starts pi, bash and git in. It is built from
// a list of the variables they need, never the worker's own environment
// whole: that holds what compose passes through, such as a token for a
// git host, and whatever an agent or a script can see it can print. What
// a step needs beyond the list is added by name where
// the step starts: the cloud credentials Bedrock signs in with, for pi
// only (see host_providers.go), and the git token only for the git
// commands the worker runs itself (see workspace.go). Model keys never
// reach the worker; agents reach their models through the webapp.
//
// CHILD_ENV names further variables, comma-separated, that every agent and
// script gets from the worker's environment, for an instance that wants
// its scripts to call an API with a token of its own, knowing its agents
// can read it too.

import (
	"os"
	"slices"
	"strings"
)

// What every step gets from the worker's environment when it has them:
// where programs and the home directory are, the locale, pi's own setup,
// proxies and certificates for reaching out, and who commits.
var childVars = []string{
	"PATH", "HOME", "LANG", "LC_ALL", "TERM", "TZ",
	"PI_SKIP_VERSION_CHECK", "PI_TELEMETRY",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM",
}

// The environment of a step's process: the variables above and those
// CHILD_ENV names, as the worker has them, then extra, NAME=value each,
// which win over them. Git never asks for credentials on a terminal.
func childEnv(extra ...string) []string {
	names := slices.Clone(childVars)
	for _, n := range strings.Split(os.Getenv("CHILD_ENV"), ",") {
		if n = strings.TrimSpace(n); envName.MatchString(n) {
			names = append(names, n)
		}
	}
	set := map[string]bool{}
	for _, kv := range extra {
		name, _, _ := strings.Cut(kv, "=")
		set[name] = true
	}
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok && !set[n] && n != "GIT_TERMINAL_PROMPT" {
			env = append(env, n+"="+v)
			set[n] = true
		}
	}
	return append(env, extra...)
}
