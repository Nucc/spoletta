package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// The HTTP API connector calls any service's JSON API that has neither an
// MCP server nor a connector of its own: an admin installs it once for
// each service, at the API's URL, with its token, and flows call it by
// path within that URL, never outside it. A GET only reads, so agents
// have it unless their process picks otherwise; anything else changes
// something.
//
// The token goes as a bearer token, unless it is a whole header, such as
// X-API-Key: abc123 or Authorization: Basic …, which services wanting
// another goes as.

var httpConnector = &connector{
	Name:     "http",
	Title:    "HTTP API",
	Category: "Other",
	Description: "Any service's JSON API, at the URL it is installed at: get what a path holds, and post, put, " +
		"patch or delete at one.",
	URLExample: "https://api.example.com/v1",
	TokenHelp: "The API's token, sent as Authorization: Bearer <token>, or a whole header to send instead, " +
		"such as X-API-Key: abc123 or Authorization: Basic dXNlcjpwYXNz.",
	Multiple:  true,
	authorize: httpAuthorize,
	Operations: []operation{
		{Name: "get", Title: "Get", Description: "Gets what a path of the API holds.",
			Params: []param{httpPath, httpQuery}, run: httpCall(http.MethodGet)},
		{Name: "post", Title: "Post", Description: "Posts JSON to a path of the API, such as to create something.", Write: true,
			Params: []param{httpPath, httpQuery, httpBody}, run: httpCall(http.MethodPost)},
		{Name: "put", Title: "Put", Description: "Puts JSON at a path of the API, in place of what is there.", Write: true,
			Params: []param{httpPath, httpQuery, httpBody}, run: httpCall(http.MethodPut)},
		{Name: "patch", Title: "Patch", Description: "Changes part of what a path of the API holds, with JSON.", Write: true,
			Params: []param{httpPath, httpQuery, httpBody}, run: httpCall(http.MethodPatch)},
		{Name: "delete", Title: "Delete", Description: "Deletes what a path of the API holds.", Write: true,
			Params: []param{httpPath, httpQuery}, run: httpCall(http.MethodDelete)},
	},
}

var (
	httpPath = param{Name: "path", Type: paramString, Required: true, Description: "The path within the API's URL, such as /tickets/42",
		Pattern: regexp.MustCompile(`^/[A-Za-z0-9\-._~!$&'()*+,;=:@/%]*$`)}
	httpQuery = param{Name: "query", Type: paramString, Description: "The query, such as status=open&limit=10"}
	httpBody  = param{Name: "body", Type: paramString, Description: "What to send, as JSON"}
	// A whole header the token may be.
	httpHeader = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]{0,63}):\s*(\S.*)$`)
)

func httpAuthorize(_ context.Context, req *http.Request, token string) error {
	if m := httpHeader.FindStringSubmatch(token); m != nil {
		req.Header.Set(m[1], m[2])
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return nil
}

// Why a path can't be called within the API's URL: it would leave it, by
// going up or to another host.
func httpPathProblem(base, path string) error {
	lower := strings.ToLower(path)
	if strings.Contains(path, "//") || strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return fmt.Errorf("%s isn't a path within the API", quote(path))
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%s isn't a path within the API", quote(path))
		}
	}
	b, err := url.Parse(base)
	if err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimSuffix(base, "/") + path)
	if err != nil || u.Host != b.Host || u.Scheme != b.Scheme {
		return fmt.Errorf("%s isn't a path within the API", quote(path))
	}
	return nil
}

func httpCall(method string) func(ctx context.Context, c connectorClient, a opArgs) (any, error) {
	return func(ctx context.Context, c connectorClient, a opArgs) (any, error) {
		path := a.str("path")
		if err := httpPathProblem(c.url, path); err != nil {
			return nil, err
		}
		query, err := url.ParseQuery(a.str("query"))
		if err != nil {
			return nil, errors.New("query must be as it goes after the ?, such as status=open&limit=10")
		}
		var body any
		if raw := strings.TrimSpace(a.str("body")); raw != "" {
			if !json.Valid([]byte(raw)) {
				return nil, errors.New("body must be JSON")
			}
			body = json.RawMessage(raw)
		}
		answer, contentType, err := c.send(ctx, method, path, query, body)
		if err != nil {
			return nil, err
		}
		if len(answer) == 0 {
			return map[string]any{"ok": true}, nil
		}
		if json.Valid(answer) {
			return json.RawMessage(answer), nil
		}
		return map[string]any{"contentType": contentType, "text": string(answer)}, nil
	}
}
