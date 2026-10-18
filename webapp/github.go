package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The GitHub connector works with a repository's issues, pull requests and
// files through GitHub's REST API, at api.github.com or a GitHub Enterprise
// server's /api/v3, with the token an admin sets: a fine-grained one limited
// to the repositories flows work on is best, since whoever builds or runs a
// flow acts on GitHub with it. What it answers is trimmed to what flows and
// agents use, an issue or pull request as the fields a struct's attributes
// can be named after, with key, owner/repo#number, telling it apart as a
// shared record.
//
// A pull request is opened from a branch already on GitHub: a run's
// branches stay in the worker's repositories unless a step pushes them.

var githubConnector = &connector{
	Name:     "github",
	Title:    "GitHub",
	Category: "Code",
	Description: "GitHub's issues, pull requests and files: find and read them, comment, label, open and close issues, " +
		"open, review and merge pull requests.",
	URL: "https://api.github.com",
	TokenHelp: "A fine-grained personal access token, or a GitHub App's, limited to the repositories flows work on, " +
		"with read and write access to issues and pull requests and read access to contents. On GitHub Enterprise " +
		"Server, set the URL to the server's /api/v3.",
	Headers: map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "Spoletta",
	},
	Operations: []operation{
		{Name: "list_issues", Title: "List issues", Description: "Lists a repository's issues, most recently updated first, leaving out pull requests.",
			Params: []param{ghOwner, ghRepo, ghState, {Name: "labels", Type: paramStrings, Description: "Only issues with all of these labels"},
				{Name: "assignee", Type: paramString, Description: "Only issues assigned to this login, none for unassigned ones, * for any"},
				{Name: "since", Type: paramString, Description: "Only issues updated at or after this time, as 2026-01-31T00:00:00Z"}, ghLimit},
			run: ghListIssues},
		{Name: "search_issues", Title: "Search issues", Description: "Finds issues and pull requests with GitHub's search syntax, such as repo:owner/name is:open label:bug.",
			Params: []param{{Name: "query", Type: paramString, Required: true, Description: "The search, in GitHub's syntax"}, ghLimit},
			run:    ghSearchIssues},
		{Name: "get_issue", Title: "Get an issue", Description: "Gets an issue, or a pull request as an issue, by its number.",
			Params: []param{ghOwner, ghRepo, ghNumber}, run: ghGetIssue},
		{Name: "list_comments", Title: "List comments", Description: "Lists the comments on an issue or pull request, oldest first.",
			Params: []param{ghOwner, ghRepo, ghNumber, ghLimit}, run: ghListComments},
		{Name: "list_pull_requests", Title: "List pull requests", Description: "Lists a repository's pull requests, most recently updated first.",
			Params: []param{ghOwner, ghRepo, ghState, {Name: "base", Type: paramString, Description: "Only those merging into this branch"},
				{Name: "head", Type: paramString, Description: "Only those from this branch, as owner:branch"}, ghLimit},
			run: ghListPulls},
		{Name: "get_pull_request", Title: "Get a pull request", Description: "Gets a pull request by its number, with whether it can be merged.",
			Params: []param{ghOwner, ghRepo, ghNumber}, run: ghGetPull},
		{Name: "list_pull_request_files", Title: "List a pull request's files", Description: "Lists the files a pull request changes, each with its patch.",
			Params: []param{ghOwner, ghRepo, ghNumber, ghLimit}, run: ghListPullFiles},
		{Name: "get_file", Title: "Get a file", Description: "Gets a file of a repository, as text, on its default branch or at a ref.",
			Params: []param{ghOwner, ghRepo, {Name: "path", Type: paramString, Required: true, Description: "The file's path in the repository"},
				{Name: "ref", Type: paramString, Description: "A branch, tag or commit; the default branch when left out"}},
			run: ghGetFile},

		{Name: "create_issue", Title: "Create an issue", Description: "Opens an issue in a repository.", Write: true,
			Params: []param{ghOwner, ghRepo, {Name: "title", Type: paramString, Required: true, Description: "The issue's title"},
				{Name: "body", Type: paramString, Description: "What it says, in Markdown"},
				{Name: "labels", Type: paramStrings, Description: "Labels to give it"},
				{Name: "assignees", Type: paramStrings, Description: "Logins to assign it to"}},
			run: ghCreateIssue},
		{Name: "update_issue", Title: "Update an issue", Description: "Changes an issue's or pull request's title, body, state, labels or assignees; what is left out stays.", Write: true,
			Params: []param{ghOwner, ghRepo, ghNumber, {Name: "title", Type: paramString, Description: "Its new title"},
				{Name: "body", Type: paramString, Description: "Its new body, in Markdown"},
				{Name: "state", Type: paramString, Enum: []string{"open", "closed"}, Description: "open or closed"},
				{Name: "state_reason", Type: paramString, Enum: []string{"completed", "not_planned", "reopened"}, Description: "Why its state changes"},
				{Name: "labels", Type: paramStrings, Description: "Its labels, in place of those it has"},
				{Name: "assignees", Type: paramStrings, Description: "Its assignees, in place of those it has"}},
			run: ghUpdateIssue},
		{Name: "add_comment", Title: "Comment", Description: "Comments on an issue or pull request.", Write: true,
			Params: []param{ghOwner, ghRepo, ghNumber, {Name: "body", Type: paramString, Required: true, Description: "The comment, in Markdown"}},
			run:    ghAddComment},
		{Name: "add_labels", Title: "Add labels", Description: "Adds labels to an issue or pull request, keeping those it has.", Write: true,
			Params: []param{ghOwner, ghRepo, ghNumber, {Name: "labels", Type: paramStrings, Required: true, Description: "The labels to add"}},
			run:    ghAddLabels},
		{Name: "create_pull_request", Title: "Open a pull request", Description: "Opens a pull request from a branch already on GitHub.", Write: true,
			Params: []param{ghOwner, ghRepo, {Name: "title", Type: paramString, Required: true, Description: "The pull request's title"},
				{Name: "head", Type: paramString, Required: true, Description: "The branch with the changes, as branch or owner:branch for a fork"},
				{Name: "base", Type: paramString, Required: true, Description: "The branch to merge them into"},
				{Name: "body", Type: paramString, Description: "What it says, in Markdown"},
				{Name: "draft", Type: paramBoolean, Description: "Whether it is a draft"}},
			run: ghCreatePull},
		{Name: "review_pull_request", Title: "Review a pull request", Description: "Approves a pull request, requests changes to it, or comments on it as a review.", Write: true,
			Params: []param{ghOwner, ghRepo, ghNumber,
				{Name: "event", Type: paramString, Required: true, Enum: []string{"APPROVE", "REQUEST_CHANGES", "COMMENT"}, Description: "APPROVE, REQUEST_CHANGES or COMMENT"},
				{Name: "body", Type: paramString, Description: "The review, in Markdown; needed to request changes or comment"}},
			run: ghReviewPull},
		{Name: "merge_pull_request", Title: "Merge a pull request", Description: "Merges a pull request.", Write: true,
			Params: []param{ghOwner, ghRepo, ghNumber,
				{Name: "method", Type: paramString, Enum: []string{"merge", "squash", "rebase"}, Description: "merge, squash or rebase; merge when left out"},
				{Name: "commit_title", Type: paramString, Description: "The merge commit's title"},
				{Name: "commit_message", Type: paramString, Description: "The merge commit's message"}},
			run: ghMergePull},
	},
}

var (
	ghOwner = param{Name: "owner", Type: paramString, Required: true, Description: "The repository's owner, a user or organization login",
		Pattern: regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)}
	ghRepo = param{Name: "repo", Type: paramString, Required: true, Description: "The repository's name, without its owner",
		Pattern: regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)}
	ghNumber = param{Name: "number", Type: paramInteger, Required: true, Description: "The issue's or pull request's number"}
	ghState  = param{Name: "state", Type: paramString, Enum: []string{"open", "closed", "all"}, Description: "open, closed or all; open when left out"}
	ghLimit  = param{Name: "limit", Type: paramInteger, Description: fmt.Sprintf("How many to list, at most %d; %d when left out", maxItems, ghDefaultLimit)}
)

const ghDefaultLimit = 20

// The repository's path in the API, its parts checked so they can't reach
// anywhere else.
func ghRepoPath(a opArgs) (string, error) {
	repo := a.str("repo")
	if repo == "." || repo == ".." {
		return "", fmt.Errorf("%s isn't a repository's name", quote(repo))
	}
	return "/repos/" + url.PathEscape(a.str("owner")) + "/" + url.PathEscape(repo), nil
}

// The number's part of a path, which must be an issue's or pull request's.
func ghNumberPath(a opArgs) (string, error) {
	n := a.int("number")
	if n < 1 {
		return "", errors.New("number must be an issue's or pull request's, from 1")
	}
	return "/" + strconv.FormatInt(n, 10), nil
}

func ghPerPage(a opArgs) (string, error) {
	n := a.int("limit")
	if !a.has("limit") {
		n = ghDefaultLimit
	}
	if n < 1 || n > maxItems {
		return "", fmt.Errorf("limit must be from 1 to %d", maxItems)
	}
	return strconv.FormatInt(n, 10), nil
}

type ghUser struct {
	Login string `json:"login"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghIssueJSON struct {
	Number        int64            `json:"number"`
	Title         string           `json:"title"`
	State         string           `json:"state"`
	StateReason   *string          `json:"state_reason"`
	Body          *string          `json:"body"`
	User          ghUser           `json:"user"`
	Labels        []ghLabel        `json:"labels"`
	Assignees     []ghUser         `json:"assignees"`
	Comments      int64            `json:"comments"`
	HTMLURL       string           `json:"html_url"`
	RepositoryURL string           `json:"repository_url"`
	CreatedAt     string           `json:"created_at"`
	UpdatedAt     string           `json:"updated_at"`
	ClosedAt      *string          `json:"closed_at"`
	PullRequest   *json.RawMessage `json:"pull_request"`
}

type ghIssue struct {
	Key         string   `json:"key"`
	Repo        string   `json:"repo"`
	Number      int64    `json:"number"`
	Title       string   `json:"title"`
	State       string   `json:"state"`
	StateReason string   `json:"stateReason"`
	Body        string   `json:"body"`
	Author      string   `json:"author"`
	Labels      []string `json:"labels"`
	Assignees   []string `json:"assignees"`
	Comments    int64    `json:"comments"`
	URL         string   `json:"url"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	ClosedAt    string   `json:"closedAt"`
	PullRequest bool     `json:"pullRequest"`
}

// owner/repo of an API URL ending in them.
func ghRepoOf(apiURL string) string {
	parts := strings.Split(strings.TrimSuffix(apiURL, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

func logins(users []ghUser) []string {
	out := []string{}
	for _, u := range users {
		out = append(out, u.Login)
	}
	return out
}

func labelNames(labels []ghLabel) []string {
	out := []string{}
	for _, l := range labels {
		out = append(out, l.Name)
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (i ghIssueJSON) trim() ghIssue {
	repo := ghRepoOf(i.RepositoryURL)
	return ghIssue{
		Key: fmt.Sprintf("%s#%d", repo, i.Number), Repo: repo, Number: i.Number, Title: i.Title, State: i.State,
		StateReason: deref(i.StateReason), Body: deref(i.Body), Author: i.User.Login, Labels: labelNames(i.Labels),
		Assignees: logins(i.Assignees), Comments: i.Comments, URL: i.HTMLURL, CreatedAt: i.CreatedAt,
		UpdatedAt: i.UpdatedAt, ClosedAt: deref(i.ClosedAt), PullRequest: i.PullRequest != nil,
	}
}

type ghPullJSON struct {
	Number    int64     `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	Body      *string   `json:"body"`
	User      ghUser    `json:"user"`
	Labels    []ghLabel `json:"labels"`
	HTMLURL   string    `json:"html_url"`
	Merged    bool      `json:"merged"`
	MergedAt  *string   `json:"merged_at"`
	Mergeable *bool     `json:"mergeable"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

type ghPull struct {
	Key       string   `json:"key"`
	Repo      string   `json:"repo"`
	Number    int64    `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Draft     bool     `json:"draft"`
	Body      string   `json:"body"`
	Author    string   `json:"author"`
	Labels    []string `json:"labels"`
	Head      string   `json:"head"`
	HeadSHA   string   `json:"headSha"`
	Base      string   `json:"base"`
	URL       string   `json:"url"`
	Merged    bool     `json:"merged"`
	Mergeable *bool    `json:"mergeable"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
	MergedAt  string   `json:"mergedAt"`
}

// A pull request's state is merged once it is, rather than closed.
func (p ghPullJSON) trim() ghPull {
	state := p.State
	if p.Merged || p.MergedAt != nil {
		state = "merged"
	}
	repo := p.Base.Repo.FullName
	return ghPull{
		Key: fmt.Sprintf("%s#%d", repo, p.Number), Repo: repo, Number: p.Number, Title: p.Title, State: state,
		Draft: p.Draft, Body: deref(p.Body), Author: p.User.Login, Labels: labelNames(p.Labels), Head: p.Head.Ref,
		HeadSHA: p.Head.SHA, Base: p.Base.Ref, URL: p.HTMLURL, Merged: state == "merged", Mergeable: p.Mergeable,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, MergedAt: deref(p.MergedAt),
	}
}

func ghListIssues(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	repo, err := ghRepoPath(a)
	if err != nil {
		return nil, err
	}
	perPage, err := ghPerPage(a)
	if err != nil {
		return nil, err
	}
	q := url.Values{"sort": {"updated"}, "direction": {"desc"}, "per_page": {perPage}}
	if s := a.str("state"); s != "" {
		q.Set("state", s)
	}
	if l := a.strings("labels"); len(l) > 0 {
		q.Set("labels", strings.Join(l, ","))
	}
	if s := a.str("assignee"); s != "" {
		q.Set("assignee", s)
	}
	if s := a.str("since"); s != "" {
		q.Set("since", s)
	}
	var raw []ghIssueJSON
	if err := c.do(ctx, http.MethodGet, repo+"/issues", q, nil, &raw); err != nil {
		return nil, err
	}
	issues := []ghIssue{}
	for _, i := range raw {
		if i.PullRequest == nil {
			issues = append(issues, i.trim())
		}
	}
	return issues, nil
}

func ghSearchIssues(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	perPage, err := ghPerPage(a)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Items []ghIssueJSON `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/search/issues", url.Values{"q": {a.str("query")}, "per_page": {perPage}}, nil, &raw); err != nil {
		return nil, err
	}
	issues := []ghIssue{}
	for _, i := range raw.Items {
		issues = append(issues, i.trim())
	}
	return issues, nil
}

// The path of an issue, or what is wrong with the arguments naming it.
func ghIssuePath(a opArgs) (string, error) {
	repo, err := ghRepoPath(a)
	if err != nil {
		return "", err
	}
	n, err := ghNumberPath(a)
	if err != nil {
		return "", err
	}
	return repo + "/issues" + n, nil
}

func ghGetIssue(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghIssuePath(a)
	if err != nil {
		return nil, err
	}
	var raw ghIssueJSON
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &raw); err != nil {
		return nil, err
	}
	return raw.trim(), nil
}

type ghComment struct {
	ID        int64  `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
}

type ghCommentJSON struct {
	ID        int64  `json:"id"`
	User      ghUser `json:"user"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	CreatedAt string `json:"created_at"`
}

func (c ghCommentJSON) trim() ghComment {
	return ghComment{ID: c.ID, Author: c.User.Login, Body: c.Body, URL: c.HTMLURL, CreatedAt: c.CreatedAt}
}

func ghListComments(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghIssuePath(a)
	if err != nil {
		return nil, err
	}
	perPage, err := ghPerPage(a)
	if err != nil {
		return nil, err
	}
	var raw []ghCommentJSON
	if err := c.do(ctx, http.MethodGet, path+"/comments", url.Values{"per_page": {perPage}}, nil, &raw); err != nil {
		return nil, err
	}
	comments := []ghComment{}
	for _, cm := range raw {
		comments = append(comments, cm.trim())
	}
	return comments, nil
}

// The path of a pull request, or what is wrong with the arguments naming it.
func ghPullPath(a opArgs) (string, error) {
	repo, err := ghRepoPath(a)
	if err != nil {
		return "", err
	}
	n, err := ghNumberPath(a)
	if err != nil {
		return "", err
	}
	return repo + "/pulls" + n, nil
}

func ghListPulls(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	repo, err := ghRepoPath(a)
	if err != nil {
		return nil, err
	}
	perPage, err := ghPerPage(a)
	if err != nil {
		return nil, err
	}
	q := url.Values{"sort": {"updated"}, "direction": {"desc"}, "per_page": {perPage}}
	for _, k := range []string{"state", "base", "head"} {
		if s := a.str(k); s != "" {
			q.Set(k, s)
		}
	}
	var raw []ghPullJSON
	if err := c.do(ctx, http.MethodGet, repo+"/pulls", q, nil, &raw); err != nil {
		return nil, err
	}
	pulls := []ghPull{}
	for _, p := range raw {
		pulls = append(pulls, p.trim())
	}
	return pulls, nil
}

func ghGetPull(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghPullPath(a)
	if err != nil {
		return nil, err
	}
	var raw ghPullJSON
	if err := c.do(ctx, http.MethodGet, path, nil, nil, &raw); err != nil {
		return nil, err
	}
	return raw.trim(), nil
}

type ghFileChange struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
	Patch     string `json:"patch"`
}

func ghListPullFiles(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghPullPath(a)
	if err != nil {
		return nil, err
	}
	perPage, err := ghPerPage(a)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int64  `json:"additions"`
		Deletions int64  `json:"deletions"`
		Patch     string `json:"patch"`
	}
	if err := c.do(ctx, http.MethodGet, path+"/files", url.Values{"per_page": {perPage}}, nil, &raw); err != nil {
		return nil, err
	}
	files := []ghFileChange{}
	for _, f := range raw {
		files = append(files, ghFileChange{Path: f.Filename, Status: f.Status, Additions: f.Additions, Deletions: f.Deletions, Patch: f.Patch})
	}
	return files, nil
}

// The largest file get_file hands back.
const ghMaxFile = 1 << 20

func ghGetFile(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	repo, err := ghRepoPath(a)
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, p := range strings.Split(strings.Trim(a.str("path"), "/"), "/") {
		if p == "" || p == "." || p == ".." {
			return nil, fmt.Errorf("%s isn't a file's path in the repository", quote(a.str("path")))
		}
		parts = append(parts, url.PathEscape(p))
	}
	q := url.Values{}
	if ref := a.str("ref"); ref != "" {
		q.Set("ref", ref)
	}
	var raw struct {
		Type     string `json:"type"`
		Path     string `json:"path"`
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := c.do(ctx, http.MethodGet, repo+"/contents/"+strings.Join(parts, "/"), q, nil, &raw); err != nil {
		return nil, err
	}
	if raw.Type != "file" {
		return nil, fmt.Errorf("%s isn't a file", quote(a.str("path")))
	}
	if raw.Size > ghMaxFile || raw.Encoding != "base64" {
		return nil, fmt.Errorf("%s is too large to get, at %d bytes", quote(raw.Path), raw.Size)
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(raw.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("GitHub's content of %s isn't base64", quote(raw.Path))
	}
	return map[string]any{"path": raw.Path, "sha": raw.SHA, "size": raw.Size, "content": string(content)}, nil
}

func ghCreateIssue(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	repo, err := ghRepoPath(a)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"title": a.str("title")}
	if a.has("body") {
		body["body"] = a.str("body")
	}
	if a.has("labels") {
		body["labels"] = a.strings("labels")
	}
	if a.has("assignees") {
		body["assignees"] = a.strings("assignees")
	}
	var raw ghIssueJSON
	if err := c.do(ctx, http.MethodPost, repo+"/issues", nil, body, &raw); err != nil {
		return nil, err
	}
	return raw.trim(), nil
}

func ghUpdateIssue(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghIssuePath(a)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	for _, k := range []string{"title", "body", "state", "state_reason"} {
		if a.has(k) {
			body[k] = a.str(k)
		}
	}
	for _, k := range []string{"labels", "assignees"} {
		if a.has(k) {
			body[k] = a.strings(k)
		}
	}
	if len(body) == 0 {
		return nil, errors.New("update_issue needs something to change: title, body, state, state_reason, labels or assignees")
	}
	var raw ghIssueJSON
	if err := c.do(ctx, http.MethodPatch, path, nil, body, &raw); err != nil {
		return nil, err
	}
	return raw.trim(), nil
}

func ghAddComment(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghIssuePath(a)
	if err != nil {
		return nil, err
	}
	var raw ghCommentJSON
	if err := c.do(ctx, http.MethodPost, path+"/comments", nil, map[string]any{"body": a.str("body")}, &raw); err != nil {
		return nil, err
	}
	return raw.trim(), nil
}

func ghAddLabels(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghIssuePath(a)
	if err != nil {
		return nil, err
	}
	var raw []ghLabel
	if err := c.do(ctx, http.MethodPost, path+"/labels", nil, map[string]any{"labels": a.strings("labels")}, &raw); err != nil {
		return nil, err
	}
	return map[string]any{"labels": labelNames(raw)}, nil
}

func ghCreatePull(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	repo, err := ghRepoPath(a)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"title": a.str("title"), "head": a.str("head"), "base": a.str("base"), "draft": a.bool("draft")}
	if a.has("body") {
		body["body"] = a.str("body")
	}
	var raw ghPullJSON
	if err := c.do(ctx, http.MethodPost, repo+"/pulls", nil, body, &raw); err != nil {
		return nil, err
	}
	return raw.trim(), nil
}

func ghReviewPull(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghPullPath(a)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"event": a.str("event")}
	if a.has("body") {
		body["body"] = a.str("body")
	}
	var raw struct {
		ID      int64  `json:"id"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodPost, path+"/reviews", nil, body, &raw); err != nil {
		return nil, err
	}
	return map[string]any{"id": raw.ID, "state": raw.State, "url": raw.HTMLURL}, nil
}

func ghMergePull(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	path, err := ghPullPath(a)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if a.has("method") {
		body["merge_method"] = a.str("method")
	}
	if a.has("commit_title") {
		body["commit_title"] = a.str("commit_title")
	}
	if a.has("commit_message") {
		body["commit_message"] = a.str("commit_message")
	}
	var raw struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	if err := c.do(ctx, http.MethodPut, path+"/merge", nil, body, &raw); err != nil {
		return nil, err
	}
	return map[string]any{"merged": raw.Merged, "sha": raw.SHA, "message": raw.Message}, nil
}
