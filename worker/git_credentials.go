package main

// A step's git token: the webapp makes one for each step working in a
// workspace, reaching the run's repositories (see the webapp's
// git_tokens.go), and the worker writes it to a file of the step's private
// directory, which git-credential.sh hands git. Every git of the step uses
// it: the worker's own, fetching and checking out, and the agent's or the
// script's, which can push. The step can read the token while it runs;
// that is the point of one that reaches only its repositories and, from a
// GitHub App, ends within the hour. The worker holds no token of its own,
// and nothing configures git to find one but the step's environment.
//
// A step that would outlive its token is stopped as it ends, saying so,
// rather than having git refused halfway.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type gitCredentials struct {
	Username string     `json:"username"`
	Token    string     `json:"token"`
	Expires  *time.Time `json:"expires,omitempty"`
}

// The credential helper the image puts here.
var gitCredentialHelper = envOr("GIT_CREDENTIAL_HELPER", "/opt/flow/git-credential.sh")

type gitCredentialsKey struct{}

// Writes the credentials, if any, into dir for git-credential.sh, and
// returns the context the step's own git runs in, which carries them, and
// the variables that hand them to the agent's or script's git.
func writeGitCredentials(ctx context.Context, dir string, c *gitCredentials) (context.Context, []string, error) {
	if c == nil || c.Token == "" {
		return ctx, nil, nil
	}
	if strings.ContainsAny(c.Token+c.Username, "\x00\r\n") {
		return ctx, nil, errors.New("invalid git credentials")
	}
	username := c.Username
	if username == "" {
		username = "x-access-token"
	}
	path := filepath.Join(dir, "git-credentials")
	if err := os.WriteFile(path, []byte("username="+username+"\npassword="+c.Token+"\n"), 0o600); err != nil {
		return ctx, nil, err
	}
	env := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=" + gitCredentialHelper,
		"FLOW_GIT_CREDENTIALS=" + path,
	}
	return context.WithValue(ctx, gitCredentialsKey{}, env), env, nil
}

// The variables handing git the step's credentials, if ctx carries any.
func gitCredentialEnv(ctx context.Context) []string {
	env, _ := ctx.Value(gitCredentialsKey{}).([]string)
	return env
}

// The step's timeout, cut short to end before its token does, and whether
// it was.
func untilTokenEnds(timeout time.Duration, c *gitCredentials) (time.Duration, bool) {
	if c == nil || c.Expires == nil {
		return timeout, false
	}
	left := time.Until(*c.Expires) - time.Minute
	if left < timeout {
		return max(left, 0), true
	}
	return timeout, false
}
