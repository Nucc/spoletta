package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSentrySettingsProblem(t *testing.T) {
	ok := defaultSentrySettings()
	if msg := ok.problem(); msg != "" {
		t.Errorf("the default settings: %q", msg)
	}
	for _, c := range []struct {
		change  func(*sentrySettings)
		problem string
	}{
		{func(s *sentrySettings) { s.Organization, s.Project = "acme-co", "web_app" }, ""},
		{func(s *sentrySettings) { s.Project = "4505" }, ""},
		{func(s *sentrySettings) { s.Sort = "" }, ""},
		{func(s *sentrySettings) { s.Sort = "freq" }, ""},
		{func(s *sentrySettings) { s.Limit = 0 }, ""},
		{func(s *sentrySettings) { s.Limit = maxSentryIssues }, ""},
		{func(s *sentrySettings) { s.Organization = "acme/../other" }, "slugs"},
		{func(s *sentrySettings) { s.Organization = "acme co" }, "slugs"},
		{func(s *sentrySettings) { s.Project = "web?x=1" }, "slugs"},
		{func(s *sentrySettings) { s.Sort = "oldest" }, "sort must be one of"},
		{func(s *sentrySettings) { s.Limit = -1 }, "limit must be"},
		{func(s *sentrySettings) { s.Limit = maxSentryIssues + 1 }, "limit must be"},
		{func(s *sentrySettings) { s.Query = strings.Repeat("q", 2001) }, "too long"},
		{func(s *sentrySettings) { s.Environment = strings.Repeat("e", 256) }, "too long"},
	} {
		s := defaultSentrySettings()
		c.change(&s)
		msg := s.problem()
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("problem(%+v) = %q, want %q", s, msg, c.problem)
		}
	}
}

// Settings left empty are left out of the request, but the limit, which
// defaults to the default settings'; the instance is sentry.io unless set.
func TestSentryIssuesURL(t *testing.T) {
	for _, c := range []struct {
		base string
		s    sentrySettings
		path string
		q    url.Values
	}{
		{"", sentrySettings{Organization: "acme"}, "https://sentry.io/api/0/organizations/acme/issues/", url.Values{"limit": {"10"}}},
		{"https://sentry.example.com/", defaultSentrySettings(), "https://sentry.example.com/api/0/organizations//issues/",
			url.Values{"query": {"is:unresolved"}, "sort": {"date"}, "limit": {"10"}}},
		{"https://sentry.example.com", sentrySettings{Organization: "acme", Project: "web", Environment: "prod", Query: "level:error is:unresolved", Sort: "freq", Limit: 25},
			"https://sentry.example.com/api/0/organizations/acme/issues/",
			url.Values{"query": {"level:error is:unresolved"}, "project": {"web"}, "environment": {"prod"}, "sort": {"freq"}, "limit": {"25"}}},
		// A slug is escaped, should one get past the settings' check.
		{"", sentrySettings{Organization: "a/b"}, "https://sentry.io/api/0/organizations/a%2Fb/issues/", url.Values{"limit": {"10"}}},
	} {
		got := sentryConfig{url: c.base}.issuesURL(c.s)
		path, query, _ := strings.Cut(got, "?")
		q, err := url.ParseQuery(query)
		if path != c.path || err != nil || q.Encode() != c.q.Encode() {
			t.Errorf("issuesURL(%q, %+v) = %q, want %s?%s", c.base, c.s, got, c.path, c.q.Encode())
		}
	}
}

func sentryIssue(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var issue map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &issue); err != nil {
		t.Fatal(err)
	}
	return issue
}

// A field is the issue's own, a dotted name reaches into one, and project
// is the project's slug.
func TestSentryField(t *testing.T) {
	issue := sentryIssue(t, `{"id": "101", "count": "7", "metadata": {"value": "boom", "deep": {"x": 1}},
		"project": {"id": "5", "slug": "web"}, "tags": null, "level": "error"}`)
	for _, c := range []struct {
		name, want string
		ok         bool
	}{
		{"id", `"101"`, true},
		{"level", `"error"`, true},
		{"metadata.value", `"boom"`, true},
		{"metadata.deep.x", `1`, true},
		{"metadata.deep", `{"x": 1}`, true},
		{"project", `"web"`, true},
		{"project.id", `"5"`, true},
		{"tags", `null`, true},
		{"missing", ``, false},
		{"metadata.missing", ``, false},
		{"id.x", ``, false},
		{"tags.x", ``, false},
	} {
		got, ok := sentryField(issue, c.name)
		if ok != c.ok || string(got) != c.want {
			t.Errorf("sentryField(%q) = %s, %v, want %s, %v", c.name, got, ok, c.want, c.ok)
		}
	}
	// A project without a slug is given as it is.
	got, ok := sentryField(sentryIssue(t, `{"project": {"id": "5"}}`), "project")
	if !ok || string(got) != `{"id": "5"}` {
		t.Errorf("sentryField(project without slug) = %s, %v", got, ok)
	}
}

func compactJSON(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	return b.String()
}

// The list holds the fields in the order asked; a field some issues lack is
// null in those, but one none has is most likely misnamed, unless lenient.
func TestSentryList(t *testing.T) {
	issues := []map[string]json.RawMessage{
		sentryIssue(t, `{"id": "1", "title": "A", "metadata": {"value": "x"}, "project": {"slug": "web"}}`),
		sentryIssue(t, `{"id": "2", "title": "B", "project": {"slug": "api"}}`),
	}
	got, err := sentryList(issues, []string{"title", "id", "metadata.value", "project"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"title":"A","id":"1","metadata.value":"x","project":"web"},{"title":"B","id":"2","metadata.value":null,"project":"api"}]`
	if compactJSON(t, got) != want {
		t.Errorf("sentryList = %s, want %s", got, want)
	}

	if _, err := sentryList(issues, []string{"id", "titel"}, false); err == nil || !strings.Contains(err.Error(), `no field "titel"`) {
		t.Errorf("a misnamed field: %v", err)
	}
	got, err = sentryList(issues, []string{"id", "titel"}, true)
	if err != nil || compactJSON(t, got) != `[{"id":"1","titel":null},{"id":"2","titel":null}]` {
		t.Errorf("lenient: %s, %v", got, err)
	}
	// No issues name no fields, so none is misnamed.
	got, err = sentryList(nil, []string{"titel"}, false)
	if err != nil || compactJSON(t, got) != `[]` {
		t.Errorf("no issues: %s, %v", got, err)
	}
}

// Fetching sends the token, and says what Sentry said when it turns the
// request down, shortened.
func TestSentryFetch(t *testing.T) {
	status, answer := http.StatusOK, ""
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		w.Write([]byte(answer))
	}))
	defer srv.Close()
	cfg := sentryConfig{url: srv.URL, token: "sntrys_abc"}
	u := cfg.issuesURL(sentrySettings{Organization: "acme"})

	status, answer = http.StatusOK, `[{"id": "1", "title": "A"}, {"id": "2"}]`
	issues, err := cfg.fetch(context.Background(), u)
	if err != nil || len(issues) != 2 || string(issues[0]["title"]) != `"A"` {
		t.Errorf("fetch = %v, %v", issues, err)
	}
	if auth != "Bearer sntrys_abc" {
		t.Errorf("Authorization = %q", auth)
	}

	for _, c := range []struct {
		status          int
		answer, problem string
	}{
		{http.StatusOK, `[]`, ""},
		{http.StatusForbidden, `{"detail": "You do not have permission"}`, "Sentry answered 403 Forbidden: You do not have permission"},
		{http.StatusNotFound, ` not here `, "Sentry answered 404 Not Found: not here"},
		{http.StatusInternalServerError, strings.Repeat("x", 600), "Sentry answered 500 Internal Server Error: " + strings.Repeat("x", 500) + "…"},
		{http.StatusOK, `{"detail": "not a list"}`, "not a list of issues"},
	} {
		status, answer = c.status, c.answer
		_, err := cfg.fetch(context.Background(), u)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("fetch(%d %.40s) = %q, want %q", c.status, c.answer, msg, c.problem)
		}
	}
	// Exactly 500 characters, and no more.
	status, answer = http.StatusInternalServerError, strings.Repeat("x", 600)
	if _, err := cfg.fetch(context.Background(), u); err == nil || strings.Count(err.Error(), "x") != 500 {
		t.Errorf("a long answer isn't shortened to 500 characters: %v", err)
	}
}
