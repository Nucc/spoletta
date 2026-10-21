package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A broker holding one grant, of the token, reaching the routes given.
func grantedBroker(t *testing.T, token string, routes map[string]llmRoute) *llmBroker {
	t.Helper()
	b := newLLMBroker(nil, "", "http://webapp")
	b.grants[token] = &llmGrant{routes: routes, issued: time.Now(), expire: time.AfterFunc(time.Hour, func() {})}
	return b
}

func serveLLM(b *llmBroker, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	registerLLMRoutes(mux, b)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// The token reaches the providers granted, with their keys in its place in
// whichever header carried it, and nothing else; a revoked one reaches none.
func TestLLMProxySwapsTheTokenForTheKey(t *testing.T) {
	var got *http.Request
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok": true}`)
	}))
	defer upstream.Close()
	b := grantedBroker(t, "step-token", map[string]llmRoute{
		"anthropic": {base: upstream.URL, key: "real-anthropic"},
		"google":    {base: upstream.URL + "/v1beta", key: "real-google"},
		"openai":    {base: upstream.URL + "/v1", key: "real-openai"},
	})

	for _, c := range []struct {
		path, header, value, wantPath, wantValue string
	}{
		{"/llm/anthropic/v1/messages?beta=true", "X-Api-Key", "step-token", "/v1/messages", "real-anthropic"},
		{"/llm/openai/responses", "Authorization", "Bearer step-token", "/v1/responses", "Bearer real-openai"},
		{"/llm/google/models/gemini:streamGenerateContent?alt=sse", "X-Goog-Api-Key", "step-token", "/v1beta/models/gemini:streamGenerateContent", "real-google"},
	} {
		got = nil
		r := httptest.NewRequest("POST", c.path, strings.NewReader(`{"model": "m"}`))
		r.Header.Set(c.header, c.value)
		r.Header.Set("Cookie", "session=someone")
		r.Header.Set("Anthropic-Beta", "tools")
		w := serveLLM(b, r)
		if w.Code != http.StatusOK || got == nil {
			t.Errorf("%s: %d %s", c.path, w.Code, w.Body)
			continue
		}
		if got.URL.Path != c.wantPath || got.Header.Get(c.header) != c.wantValue || gotBody != `{"model": "m"}` {
			t.Errorf("%s reached %s with %s %q and %q", c.path, got.URL.Path, c.header, got.Header.Get(c.header), gotBody)
		}
		for _, h := range llmKeyHeaders {
			if h != c.header && got.Header.Get(h) != "" {
				t.Errorf("%s: the provider got %s", c.path, h)
			}
		}
		if strings.Contains(strings.Join(flatten(got.Header), " "), "step-token") || got.Header.Get("Cookie") != "" || got.Header.Get("Anthropic-Beta") != "tools" {
			t.Errorf("%s: the provider got the headers %v", c.path, got.Header)
		}
	}

	for name, r := range map[string]*http.Request{
		"no token":         httptest.NewRequest("POST", "/llm/openai/responses", nil),
		"another provider": withHeader(httptest.NewRequest("POST", "/llm/mistral/v1/chat/completions", nil), "Authorization", "Bearer step-token"),
		"another token":    withHeader(httptest.NewRequest("POST", "/llm/openai/responses", nil), "Authorization", "Bearer guessed"),
	} {
		if w := serveLLM(b, r); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want refused", name, w.Code)
		}
	}
	b.revoke(&llmAccess{Token: "step-token"})
	if w := serveLLM(b, withHeader(httptest.NewRequest("POST", "/llm/openai/responses", nil), "Authorization", "Bearer step-token")); w.Code != http.StatusForbidden {
		t.Errorf("a revoked token: %d, want refused", w.Code)
	}
}

func withHeader(r *http.Request, k, v string) *http.Request {
	r.Header.Set(k, v)
	return r
}

func flatten(h http.Header) []string {
	var out []string
	for k, vs := range h {
		out = append(out, k+": "+strings.Join(vs, ","))
	}
	return out
}

// A provider serving Anthropic's API elsewhere is reached there for a
// request in Anthropic's API, and at its base for the others.
func TestLLMProxyPicksTheAnthropicBase(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
	}))
	defer upstream.Close()
	b := grantedBroker(t, "tok", map[string]llmRoute{"openrouter": {base: upstream.URL + "/api/v1", anthropic: upstream.URL + "/api", key: "k"}})
	r := withHeader(httptest.NewRequest("POST", "/llm/openrouter/v1/messages", nil), "X-Api-Key", "tok")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	serveLLM(b, r)
	serveLLM(b, withHeader(httptest.NewRequest("POST", "/llm/openrouter/chat/completions", nil), "Authorization", "Bearer tok"))
	if strings.Join(paths, " ") != "/api/v1/messages /api/v1/chat/completions" {
		t.Errorf("reached %v", paths)
	}
}

// A streamed answer comes through as it was sent, each event as it comes.
func TestLLMProxyStreams(t *testing.T) {
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":120,\"output_tokens\":1}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"Hi\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n\n",
	}
	next := make(chan bool)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			io.WriteString(w, e)
			w.(http.Flusher).Flush()
			<-next
		}
	}))
	defer upstream.Close()
	b := grantedBroker(t, "tok", map[string]llmRoute{"anthropic": {base: upstream.URL, key: "k"}})
	mux := http.NewServeMux()
	registerLLMRoutes(mux, b)
	proxy := httptest.NewServer(mux)
	defer proxy.Close()

	req, _ := http.NewRequest("POST", proxy.URL+"/llm/anthropic/v1/messages", strings.NewReader(`{"model":"claude"}`))
	req.Header.Set("X-Api-Key", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for i, e := range events {
		buf := make([]byte, len(e))
		if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != e {
			t.Fatalf("event %d = %q, %v", i, buf, err)
		}
		next <- true
	}
	if rest, _ := io.ReadAll(resp.Body); len(rest) != 0 {
		t.Errorf("then %q", rest)
	}
}

// The usage is read from a streamed answer's events, the most each says,
// and from a whole one in each API's words.
func TestUsageReader(t *testing.T) {
	r := &usageReader{}
	r.Write([]byte("data: {\"message\":{\"usage\":{\"input_tokens\":120,\"output_tokens\":1}}}\n\ndata: {\"usa"))
	r.Write([]byte("ge\":{\"output_tokens\":42}}\n\ndata: [DONE]\n"))
	if u := r.usage(); u != (llmUsage{120, 42}) {
		t.Errorf("Anthropic's stream = %+v", u)
	}
	for body, want := range map[string]llmUsage{
		`{"usage": {"prompt_tokens": 10, "completion_tokens": 3}}`:                                       llmUsage{10, 3},
		`{"type": "response.completed", "response": {"usage": {"input_tokens": 7, "output_tokens": 2}}}`: llmUsage{7, 2},
		`{"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 9}}`:                          llmUsage{5, 9},
		`{"error": "nope"}`: {},
	} {
		r := &usageReader{}
		r.Write([]byte(body))
		if u := r.usage(); u != want {
			t.Errorf("%s = %+v, want %+v", body, u, want)
		}
	}
	if m := modelOf(nil, "models/gemini-3:streamGenerateContent"); m != "gemini-3" {
		t.Errorf("Google's model = %q", m)
	}
}
