package main

// Git tokens: each step working in a workspace gets a token for its
// repositories, which the worker hands to its own git and to the agent's or
// script's, and nothing else; no token lives on the worker. Where GitHub
// is, the token is a GitHub App's installation token, made for the step,
// reaching only the run's repositories and lasting an hour: the app's ID,
// its installation's and its private key are GITHUB_APP_ID,
// GITHUB_APP_INSTALLATION_ID and GITHUB_APP_PRIVATE_KEY (a PEM, or a file
// GITHUB_APP_PRIVATE_KEY_FILE names), and GITHUB_API_URL a GitHub
// Enterprise's API. Otherwise, or for repositories elsewhere, it is the
// webapp's GIT_TOKEN as it is, with GIT_USERNAME, which reaches whatever it
// reaches for as long as it lasts: the settings say so.
//
// A token is made as the step starts; a step outliving it fails, saying so
// (see the worker's workspace.go), rather than having its pushes refused
// halfway.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// What a step's git is given: the token, the username it goes with, and
// when it ends, zero for a token that doesn't.
type gitCredentials struct {
	Username string     `json:"username"`
	Token    string     `json:"token"`
	Expires  *time.Time `json:"expires,omitempty"`
}

// Where a step's git token comes from.
type gitTokenSource struct {
	// The webapp's own, handed to every step as it is.
	static, username string
	// A GitHub App's.
	app *githubApp
}

type githubApp struct {
	api, id, installation string
	key                   *rsa.PrivateKey
}

// The webapp's source of git tokens; set as it starts.
var gitTokens gitTokenSource

// Reads where git tokens come from from the environment.
func newGitTokenSource() (gitTokenSource, error) {
	s := gitTokenSource{static: os.Getenv("GIT_TOKEN"), username: env("GIT_USERNAME", "x-access-token")}
	id, installation := os.Getenv("GITHUB_APP_ID"), os.Getenv("GITHUB_APP_INSTALLATION_ID")
	pemKey, err := secretKeyFromEnv("GITHUB_APP_PRIVATE_KEY")
	if err != nil {
		return s, err
	}
	if id == "" && installation == "" && pemKey == "" {
		return s, nil
	}
	if id == "" || installation == "" || pemKey == "" {
		return s, errors.New("a GitHub App needs GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID and GITHUB_APP_PRIVATE_KEY")
	}
	key, err := parseRSAKey(pemKey)
	if err != nil {
		return s, fmt.Errorf("GITHUB_APP_PRIVATE_KEY: %w", err)
	}
	s.app = &githubApp{api: strings.TrimSuffix(env("GITHUB_API_URL", "https://api.github.com"), "/"), id: id, installation: installation, key: key}
	return s, nil
}

// A PEM private key, PKCS#1 as GitHub hands it out, or PKCS#8.
func parseRSAKey(s string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("not a PEM private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return rk, nil
}

// Says where tokens come from, for the log as the webapp starts.
func (s gitTokenSource) describe() string {
	switch {
	case s.app != nil && s.static != "":
		return fmt.Sprintf("each step working on repositories gets a token of the GitHub App %s for them, lasting an hour; repositories elsewhere get GIT_TOKEN, which isn't short-lived", s.app.id)
	case s.app != nil:
		return fmt.Sprintf("each step working on repositories gets a token of the GitHub App %s for them, lasting an hour", s.app.id)
	case s.static != "":
		return "each step working on repositories gets GIT_TOKEN, which isn't short-lived and reaches whatever it reaches; a GitHub App's tokens would be (GITHUB_APP_ID)"
	}
	return "steps working on repositories get no git token, so only public ones clone"
}

// The credentials for a step working in the workspace: nil for one not in
// a workspace, or without a token to be had.
func (s gitTokenSource) forWorkspace(ctx context.Context, ws *workspaceSpec) (*gitCredentials, error) {
	if ws == nil {
		return nil, nil
	}
	var urls []string
	for _, r := range ws.Repositories {
		urls = append(urls, r.URL)
	}
	creds, err := s.token(ctx, urls)
	switch {
	case err != nil:
		audit("git.refused", nil, ws.Run, ws.Step, "no git token for %s: %v", strings.Join(urls, ", "), err)
	case creds == nil:
	case creds.Expires != nil:
		audit("git.grant", nil, ws.Run, ws.Step, "a GitHub App's token for %s, until %s", strings.Join(urls, ", "), creds.Expires.UTC().Format(time.RFC3339))
	default:
		audit("git.grant", nil, ws.Run, ws.Step, "the instance's GIT_TOKEN, for %s", strings.Join(urls, ", "))
	}
	return creds, err
}

// A token for the repositories: the GitHub App's if they are all on its
// GitHub, else the static one.
func (s gitTokenSource) token(ctx context.Context, repos []string) (*gitCredentials, error) {
	if s.app != nil {
		if names, ok := s.app.repositories(repos); ok {
			return s.app.token(ctx, names)
		}
	}
	if s.static == "" {
		return nil, nil
	}
	return &gitCredentials{Username: s.username, Token: s.static}, nil
}

// The repositories' names, if each is on the app's GitHub: github.com, or
// the host of a GitHub Enterprise's API.
func (a *githubApp) repositories(urls []string) ([]string, bool) {
	host := "github.com"
	if u, err := url.Parse(a.api); err == nil && u.Host != "api.github.com" {
		host = u.Host
	}
	var names []string
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Host != host {
			return nil, false
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 2 {
			return nil, false
		}
		name := strings.TrimSuffix(parts[1], ".git")
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names, len(names) > 0
}

// An installation token reaching only the repositories, which can read and
// push to them.
func (a *githubApp) token(ctx context.Context, repos []string) (*gitCredentials, error) {
	jwt, err := a.jwt(time.Now())
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"repositories": repos, "permissions": map[string]string{"contents": "write"}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.api+"/app/installations/"+url.PathEscape(a.installation)+"/access_tokens", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask GitHub for a token: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(b, &e)
		return nil, fmt.Errorf("GitHub didn't make a token for %s: %s %s", strings.Join(repos, ", "), resp.Status, e.Message)
	}
	var t struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(b, &t); err != nil || t.Token == "" {
		return nil, errors.New("GitHub's answer holds no token")
	}
	return &gitCredentials{Username: "x-access-token", Token: t.Token, Expires: &t.ExpiresAt}, nil
}

// The app's JSON Web Token, signed with its key, as GitHub asks: issued a
// minute ago for clocks that differ, lasting nine minutes of the ten
// allowed.
func (a *githubApp) jwt(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": a.id})
	signed := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

// Logs where tokens come from.
func logGitTokens() {
	log.Printf("git: %s", gitTokens.describe())
}

// Where steps' git tokens come from, which a flow's settings say beside its
// repositories: "app" for a GitHub App's, "token" for GIT_TOKEN alone,
// "none" for none.
func registerGitTokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/git-access", func(w http.ResponseWriter, r *http.Request) {
		source := "none"
		switch {
		case gitTokens.app != nil:
			source = "app"
		case gitTokens.static != "":
			source = "token"
		}
		writeJSON(w, http.StatusOK, map[string]any{"source": source, "otherHosts": gitTokens.app != nil && gitTokens.static != ""})
	})
}
