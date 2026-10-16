package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A Sentry process fetches issues from Sentry's organization issues
// endpoint, filtered as its settings say, and generates them as a list:
//   - Each attribute of the list takes the issue's field it is named after,
//     as Sentry gives it, so an id attribute holds Sentry's issue id and a
//     shortId one its short id. A dotted name reaches into a field, such as
//     metadata.value for an error's message; project is the project's slug.
//   - A list without attributes holds the fields in sentryFields.
//
// It authenticates with SENTRY_AUTH_TOKEN, a token with event:read, against
// SENTRY_URL, sentry.io unless set.

type sentrySettings struct {
	Organization string `json:"organization"`
	// Slug or id; empty fetches from all projects the token can see.
	Project     string `json:"project"`
	Environment string `json:"environment"`
	// Sentry's search syntax, e.g. is:unresolved level:error; empty is
	// is:unresolved.
	Query string `json:"query"`
	// How Sentry orders the issues before the limit applies.
	Sort  string `json:"sort"`
	Limit int    `json:"limit"`
}

type sentryConfig struct {
	url   string
	token string
}

// The orders Sentry offers, "date" being the last seen first.
var sentrySorts = []string{"date", "new", "freq", "user", "trends", "inbox", "recommended"}

// Organization and project slugs, or a project's id.
var sentrySlug = regexp.MustCompile(`^[A-Za-z0-9_-]*$`)

// At most this many issues come back, since a flow process runs at most
// maxItems items of a list at once.
const maxSentryIssues = maxItems

// The fields of an issue a list without attributes holds.
var sentryFields = []string{"id", "shortId", "title", "culprit", "level", "status", "count", "userCount", "firstSeen", "lastSeen", "permalink", "project"}

func defaultSentrySettings() sentrySettings {
	return sentrySettings{Query: "is:unresolved", Sort: "date", Limit: 10}
}

// Why the settings can't be used, or "".
func (s sentrySettings) problem() string {
	switch {
	case !sentrySlug.MatchString(s.Organization) || !sentrySlug.MatchString(s.Project):
		return "organization and project are slugs: letters, digits, - and _"
	case s.Sort != "" && !slices.Contains(sentrySorts, s.Sort):
		return "sort must be one of " + strings.Join(sentrySorts, ", ")
	case s.Limit < 0 || s.Limit > maxSentryIssues:
		return fmt.Sprintf("limit must be between 1 and %d, or 0 for the default of %d", maxSentryIssues, defaultSentrySettings().Limit)
	case len(s.Query) > 2000 || len(s.Environment) > 255:
		return "query or environment is too long"
	}
	return ""
}

// The request for the settings' issues.
func (c sentryConfig) issuesURL(s sentrySettings) string {
	q := url.Values{}
	// Left out, Sentry fetches the unresolved ones.
	if s.Query != "" {
		q.Set("query", s.Query)
	}
	if s.Project != "" {
		q.Set("project", s.Project)
	}
	if s.Environment != "" {
		q.Set("environment", s.Environment)
	}
	if s.Sort != "" {
		q.Set("sort", s.Sort)
	}
	limit := s.Limit
	if limit == 0 {
		limit = defaultSentrySettings().Limit
	}
	q.Set("limit", strconv.Itoa(limit))
	base := strings.TrimSuffix(c.url, "/")
	if base == "" {
		base = "https://sentry.io"
	}
	return base + "/api/0/organizations/" + url.PathEscape(s.Organization) + "/issues/?" + q.Encode()
}

// Fetches the issues, as Sentry gives them.
func (c sentryConfig) fetch(ctx context.Context, u string) ([]map[string]json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRunRequestBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Detail string `json:"detail"`
		}
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &e) == nil && e.Detail != "" {
			msg = e.Detail
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return nil, fmt.Errorf("Sentry answered %s: %s", resp.Status, msg)
	}
	var issues []map[string]json.RawMessage
	if err := json.Unmarshal(body, &issues); err != nil {
		return nil, fmt.Errorf("Sentry's answer is not a list of issues: %w", err)
	}
	return issues, nil
}

// An issue's field by a possibly dotted name; project gives its slug.
func sentryField(issue map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if name == "project" {
		var p struct {
			Slug string `json:"slug"`
		}
		if raw, ok := issue["project"]; ok && json.Unmarshal(raw, &p) == nil && p.Slug != "" {
			return json.RawMessage(quote(p.Slug)), true
		}
	}
	head, rest, nested := strings.Cut(name, ".")
	raw, ok := issue[head]
	if !ok || !nested {
		return raw, ok
	}
	var inner map[string]json.RawMessage
	if json.Unmarshal(raw, &inner) != nil || inner == nil {
		return nil, false
	}
	return sentryField(inner, rest)
}

// The issues as a list holding the given fields. A field no issue has is
// most likely misnamed, so it is an error rather than left empty.
func sentryList(issues []map[string]json.RawMessage, fields []string, lenient bool) (string, error) {
	items := make([]string, len(issues))
	seen := map[string]bool{}
	for i, issue := range issues {
		values := make([]json.RawMessage, len(fields))
		for j, f := range fields {
			v, ok := sentryField(issue, f)
			if !ok {
				v = json.RawMessage("null")
			} else {
				seen[f] = true
			}
			values[j] = v
		}
		items[i] = ordered(fields, values)
	}
	if !lenient && len(issues) > 0 {
		for _, f := range fields {
			if !seen[f] {
				return "", fmt.Errorf("Sentry's issues have no field %q; name attributes after fields such as %s", f, strings.Join(sentryFields, ", "))
			}
		}
	}
	return indentJSON("[" + strings.Join(items, ",") + "]"), nil
}

// Executes a Sentry process as a step of the run, which records the request
// it sent as the step's prompt. started gets the step's id once it is recorded.
func runSentryStep(ctx context.Context, db *sql.DB, cfg sentryConfig, runID, id int64, inputs []runInput, started func(int64)) (runResult, error) {
	var res runResult
	var settings processSettings
	err := db.QueryRowContext(ctx, "SELECT settings FROM processes WHERE id = ?", id).Scan(&settings)
	if errors.Is(err, sql.ErrNoRows) {
		return res, errNotFound
	}
	if err != nil {
		return res, err
	}
	s := settings.sentry()
	contract, msg, err := contractFor(ctx, db, id, false)
	if err != nil {
		return res, err
	}
	fields, lenient := sentryFields, true
	switch {
	case s.Organization == "":
		res.Error = "the process has no Sentry organization"
	case s.problem() != "":
		res.Error = s.problem()
	case cfg.token == "":
		res.Error = "SENTRY_AUTH_TOKEN isn't set for the webapp"
	case msg != "":
		res.Error = msg
	case contract != nil && len(contract.shaped) > 1:
		res.Error = "a Sentry process generates one list; only one of its objects can have attributes"
	case contract != nil && !contract.shaped[0].many:
		res.Error = fmt.Sprintf("%q isn't a list; mark it as a list of items to hold the issues", contract.shaped[0].name)
	case contract != nil:
		fields, lenient = nil, false
		for _, f := range contract.shaped[0].fields {
			fields = append(fields, f.name)
		}
	}

	u := cfg.issuesURL(s)
	stepID, err := startStep(ctx, db, runID, id, stepActive, "GET "+u, inputs, nil)
	if err != nil {
		return res, err
	}
	started(stepID)
	if res.Error == "" {
		issues, err := cfg.fetch(ctx, u)
		switch {
		case ctx.Err() != nil:
			res.Error = stopReason(ctx)
		case err != nil:
			res.Error = err.Error()
		default:
			res.Output, err = sentryList(issues, fields, lenient)
			if err != nil {
				res.Error = err.Error()
			} else {
				res.OK = true
			}
		}
		if res.OK && contract != nil {
			res.Objects = map[int64]string{contract.shaped[0].id: res.Output}
		}
	}
	status := stepError
	if res.OK {
		status = stepDone
	}
	// The run may have been stopped, but the step still has to end.
	res.Duplicate, err = finishStep(context.Background(), db, stepID, status, res.Output, res.Error, res.Objects)
	return res, err
}
