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
// git token the step's project's owners set in its settings
// (project_git_tokens, sealed with SECRET_KEY and never sent back), or else
// the webapp's GIT_TOKEN as it is, with GIT_USERNAME, which reaches
// whatever it reaches for as long as it lasts: the settings say so.
//
// The GitHub App and GIT_TOKEN are what the instance is configured with
// outside the database, so only a project inheriting it (Legacy; see
// projects.go) has them; another project's steps get its own token, or
// none.
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
	"database/sql"
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
	// The webapp's own, handed to every step of a project inheriting it as
	// it is.
	static, username string
	// A GitHub App's.
	app *githubApp
	// Where projects' own tokens are kept; none in unit tests.
	db *sql.DB
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

// The credentials for a step working in the workspace, of its run's
// project: nil for one not in a workspace, or without a token to be had.
func (s gitTokenSource) forWorkspace(ctx context.Context, ws *workspaceSpec) (*gitCredentials, error) {
	if ws == nil {
		return nil, nil
	}
	project, inherits := legacyProject, true
	var own *gitCredentials
	if s.db != nil {
		var err error
		// A session from before workspaces named their run is Legacy's, as
		// everything from before projects is, unless it names its project;
		// every workspace made since names its run (follow-ups too, whose
		// sessions may not), whose project it is.
		switch {
		case ws.Run != 0:
			if project, err = runProject(ctx, s.db, ws.Run); err != nil {
				return nil, err
			}
		case ws.Project != 0:
			project = ws.Project
		}
		if inherits, err = inheritsInstance(ctx, s.db, project); err != nil {
			return nil, err
		}
		if own, err = projectGitToken(ctx, s.db, project); err != nil {
			return nil, err
		}
	}
	var urls []string
	for _, r := range ws.Repositories {
		urls = append(urls, r.URL)
	}
	creds, from, err := s.token(ctx, urls, inherits, own)
	joined := strings.Join(urls, ", ")
	switch {
	case err != nil:
		auditIn(project, "git.refused", nil, ws.Run, ws.Step, "no git token for %s: %v", joined, err)
	case creds == nil:
	case from == gitFromApp:
		auditIn(project, "git.grant", nil, ws.Run, ws.Step, "a GitHub App's token for %s, until %s", joined, creds.Expires.UTC().Format(time.RFC3339))
	case from == gitFromProject:
		auditIn(project, "git.grant", nil, ws.Run, ws.Step, "the project's git token, for %s", joined)
	default:
		auditIn(project, "git.grant", nil, ws.Run, ws.Step, "the instance's GIT_TOKEN, for %s", joined)
	}
	return creds, err
}

// Where a step's token came from.
const (
	gitFromApp      = "app"
	gitFromProject  = "project"
	gitFromInstance = "token"
	gitFromNone     = "none"
)

// A token for the repositories of a step of a project: the GitHub App's if
// the project inherits the instance's configuration and they are all on
// its GitHub, else the project's own, if it has one, else the static one,
// if it inherits that; and where it came from.
func (s gitTokenSource) token(ctx context.Context, repos []string, inherits bool, own *gitCredentials) (*gitCredentials, string, error) {
	if s.app != nil && inherits {
		if names, ok := s.app.repositories(repos); ok {
			creds, err := s.app.token(ctx, names)
			return creds, gitFromApp, err
		}
	}
	if own != nil {
		return own, gitFromProject, nil
	}
	if s.static == "" || !inherits {
		return nil, gitFromNone, nil
	}
	return &gitCredentials{Username: s.username, Token: s.static}, gitFromInstance, nil
}

// The git token the project's owners set, with its username; nil for none.
func projectGitToken(ctx context.Context, db querier, project int64) (*gitCredentials, error) {
	var username, stored string
	err := db.QueryRowContext(ctx, "SELECT username, token FROM project_git_tokens WHERE project_id = ?", project).Scan(&username, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token, err := secrets.open(gitTokenPlace(project), stored)
	if err != nil {
		return nil, err
	}
	return &gitCredentials{Username: username, Token: token}, nil
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

// The git token a project's owners set, as its settings show it: whether
// there is one, its username, the token's last four characters when it is
// long enough for them to say little, and when it was set; never the token.
type projectGitTokenJSON struct {
	Set       bool       `json:"set"`
	Username  string     `json:"username,omitempty"`
	Hint      string     `json:"hint,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// The longest git token and username taken.
const (
	maxGitToken    = 4096
	maxGitUsername = 255
)

// Where the steps of the project the request is worked on in get their git
// tokens, which a flow's settings say beside its repositories; and its own
// token, as its owners set it in its settings. Where projects' own tokens
// are kept is the database's, which the source is told here.
func registerGitTokenRoutes(mux *http.ServeMux, db *sql.DB) {
	gitTokens.db = db

	// "app" for a GitHub App's, "project" for the project's own, "token"
	// for GIT_TOKEN, "none" for none; otherHosts if repositories elsewhere
	// than the App's GitHub get the project's own or GIT_TOKEN.
	mux.HandleFunc("GET /api/git-access", func(w http.ResponseWriter, r *http.Request) {
		project := requestProject(r)
		inherits, err := inheritsInstance(r.Context(), db, project)
		if err != nil {
			serverError(w, err)
			return
		}
		own, err := projectGitToken(r.Context(), db, project)
		if err != nil {
			serverError(w, err)
			return
		}
		app, static := gitTokens.app != nil && inherits, gitTokens.static != "" && inherits
		source := gitFromNone
		switch {
		case app:
			source = gitFromApp
		case own != nil:
			source = gitFromProject
		case static:
			source = gitFromInstance
		}
		writeJSON(w, http.StatusOK, map[string]any{"source": source, "otherHosts": app && (own != nil || static)})
	})

	writeToken := func(w http.ResponseWriter, r *http.Request) {
		out := projectGitTokenJSON{}
		var stored string
		var at time.Time
		project := requestProject(r)
		err := db.QueryRowContext(r.Context(), "SELECT username, token, updated_at FROM project_git_tokens WHERE project_id = ?", project).
			Scan(&out.Username, &stored, &at)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			serverError(w, err)
			return
		default:
			token, err := secrets.open(gitTokenPlace(project), stored)
			if err != nil {
				serverError(w, err)
				return
			}
			out.Set, out.UpdatedAt = true, &at
			if len(token) > 12 {
				out.Hint = token[len(token)-4:]
			}
		}
		writeJSON(w, http.StatusOK, out)
	}

	// The project's own git token, as its settings show it; its owners'.
	mux.HandleFunc("GET /api/git-token", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		writeToken(w, r)
	})

	// Sets the project's own git token: {"token", "username"}, the username
	// x-access-token unless given; its owners'.
	mux.HandleFunc("PUT /api/git-token", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var body struct {
			Token    string `json:"token"`
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		token, username := strings.TrimSpace(body.Token), strings.TrimSpace(body.Username)
		if username == "" {
			username = "x-access-token"
		}
		switch {
		case token == "":
			http.Error(w, "the token is required", http.StatusBadRequest)
			return
		case len(token) > maxGitToken || strings.ContainsAny(token, "\x00\r\n"):
			http.Error(w, "that isn't a token: one line of at most 4 KB", http.StatusBadRequest)
			return
		case len(username) > maxGitUsername || strings.ContainsAny(username, "\x00\r\n:"):
			http.Error(w, "the username is one line of at most 255 characters, without a colon", http.StatusBadRequest)
			return
		}
		project := requestProject(r)
		stored, err := secrets.seal(gitTokenPlace(project), token)
		if err != nil {
			serverError(w, err)
			return
		}
		if _, err := db.ExecContext(r.Context(), `INSERT INTO project_git_tokens (project_id, username, token) VALUES (?, ?, ?) AS given
			ON DUPLICATE KEY UPDATE username = given.username, token = given.token`, project, username, stored); err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "git_token.set", "the project's git token, as %s", username)
		writeToken(w, r)
	})

	// Removes the project's own git token; its owners'.
	mux.HandleFunc("DELETE /api/git-token", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM project_git_tokens WHERE project_id = ?", requestProject(r)); err != nil {
			serverError(w, err)
			return
		}
		auditChange(r, "git_token.remove", "the project's git token")
		w.WriteHeader(http.StatusNoContent)
	})
}
