package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// A broker holding one grant, of the token, reaching the routes given.
func grantedBroker(t *testing.T, token string, routes map[string]llmRoute) *llmBroker {
	t.Helper()
	b := newLLMBroker(nil, "", "http://webapp")
	var providers []string
	for id := range routes {
		providers = append(providers, id)
	}
	if err := b.grants.put(context.Background(), grantModel, token, proxyGrant{project: legacyProject}, llmReaches{Providers: providers}, time.Hour); err != nil {
		t.Fatal(err)
	}
	// Worked out already, for as long as the test takes.
	b.routed[legacyProject] = projectRoutes{routes: routes, at: time.Now().Add(time.Hour)}
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
	// The path the mux decodes stays under the provider's URL: no step up
	// out of it to what else its host serves, and no query or fragment of
	// the agent's making, with the real key.
	for _, path := range []string{
		"/llm/openai/..%2F..%2Fx",
		"/llm/openai/%2e%2e/x",
		"/llm/openai/responses/%2E%2E%2F%2E%2E%2Fadmin",
		"/llm/openai/.%2Fresponses",
		"/llm/openai/..%5Cx",
		"/llm/openai/responses%3Fapi-key=x",
		"/llm/openai/responses%23x",
	} {
		got = nil
		w := serveLLM(b, withHeader(httptest.NewRequest("POST", path, strings.NewReader(`{}`)), "Authorization", "Bearer step-token"))
		if w.Code != http.StatusBadRequest || got != nil {
			reached := ""
			if got != nil {
				reached = got.URL.String()
			}
			t.Errorf("%s: %d, reaching %q, want refused", path, w.Code, reached)
		}
	}
	// What's left of the agent's escaping reaches the provider as written,
	// never read as a step up.
	got = nil
	w := serveLLM(b, withHeader(httptest.NewRequest("POST", "/llm/openai/%252e%252e/x", strings.NewReader(`{}`)), "Authorization", "Bearer step-token"))
	if w.Code != http.StatusOK || got == nil || got.URL.Path != "/v1/%2e%2e/x" {
		t.Errorf("an escaped %%2e: %d %+v", w.Code, got)
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

// Claude Code's requests pass as it sends them: its token in x-api-key
// swapped for the key, its query, anthropic-version and anthropic-beta list
// as they are, and the answer streamed back whole, whose usage is read from
// its events. Counting tokens, listing models and its start-up probe pass
// too, and their answers hold no usage to record.
func TestLLMProxyPassesClaudeCode(t *testing.T) {
	type reached struct {
		method, path, query string
		header              http.Header
	}
	var got []reached
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":3000,\"cache_creation_input_tokens\":400,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Done.\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":57}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	answers := map[string]string{
		"/v1/messages":              stream,
		"/v1/messages/count_tokens": `{"input_tokens": 2048}`,
		"/v1/models":                `{"data": [{"type": "model", "id": "claude-sonnet-4-6"}], "has_more": false}`,
		"/api/hello":                "",
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, reached{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone()})
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		io.WriteString(w, answers[r.URL.Path])
	}))
	defer upstream.Close()
	b := grantedBroker(t, "step-token", map[string]llmRoute{"anthropic": {base: upstream.URL, key: "sk-ant-real"}})
	beta := "claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"
	claude := func(method, path, body string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
		}
		r.Header.Set("X-Api-Key", "step-token")
		r.Header.Set("Anthropic-Version", "2023-06-01")
		r.Header.Set("Anthropic-Beta", beta)
		r.Header.Set("User-Agent", "claude-cli/2.1.325 (external, sdk-cli)")
		r.Header.Set("Content-Type", "application/json")
		return serveLLM(b, r)
	}

	w := claude("POST", "/llm/anthropic/v1/messages?beta=true", `{"model":"claude-sonnet-4-6","stream":true,"max_tokens":32000}`)
	if w.Code != http.StatusOK || w.Body.String() != stream || len(got) != 1 {
		t.Fatalf("a message: %d %q, reaching %+v", w.Code, w.Body, got)
	}
	m := got[0]
	if m.method != "POST" || m.path != "/v1/messages" || m.query != "beta=true" {
		t.Errorf("a message reached %s %s?%s", m.method, m.path, m.query)
	}
	if m.header.Get("X-Api-Key") != "sk-ant-real" || m.header.Get("Authorization") != "" ||
		m.header.Get("Anthropic-Version") != "2023-06-01" || strings.Join(m.header.Values("Anthropic-Beta"), ",") != beta ||
		m.header.Get("User-Agent") != "claude-cli/2.1.325 (external, sdk-cli)" || strings.Contains(strings.Join(flatten(m.header), " "), "step-token") {
		t.Errorf("a message reached the provider with %v", m.header)
	}
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("the answer came as %q", w.Header().Get("Content-Type"))
	}
	// What the proxy records of it: what each event says, the most of each.
	u := &usageReader{}
	u.Write([]byte(stream))
	if got := u.usage(); got != (llmUsage{input: 12, output: 57, cacheRead: 3000, cacheWrite: 400}) {
		t.Errorf("the stream's usage = %+v", got)
	}

	// Counting a message's tokens says how many it would take, which is no
	// usage: a call records nothing when its answer has none.
	w = claude("POST", "/llm/anthropic/v1/messages/count_tokens?beta=true", `{"model":"claude-sonnet-4-6","messages":[]}`)
	if w.Code != http.StatusOK || w.Body.String() != answers["/v1/messages/count_tokens"] || got[1].path != "/v1/messages/count_tokens" || got[1].query != "beta=true" {
		t.Errorf("counting tokens: %d %q, reaching %+v", w.Code, w.Body, got[1])
	}
	u = &usageReader{}
	u.Write(w.Body.Bytes())
	if got := u.usage(); got != (llmUsage{}) {
		t.Errorf("counting tokens reads as usage %+v", got)
	}

	// As it starts: models listed, and the API's HEAD probe, which the
	// mux serves on the GET pattern.
	w = claude("GET", "/llm/anthropic/v1/models?limit=1000", "")
	if w.Code != http.StatusOK || got[2].method != "GET" || got[2].path != "/v1/models" || got[2].header.Get("X-Api-Key") != "sk-ant-real" {
		t.Errorf("listing models: %d, reaching %+v", w.Code, got[2])
	}
	u = &usageReader{}
	u.Write(w.Body.Bytes())
	if got := u.usage(); got != (llmUsage{}) {
		t.Errorf("listing models reads as usage %+v", got)
	}
	w = claude("HEAD", "/llm/anthropic/api/hello", "")
	if w.Code != http.StatusOK || len(got) != 4 || got[3].method != "HEAD" || got[3].path != "/api/hello" {
		t.Errorf("the probe: %d, reaching %+v", w.Code, got[len(got)-1])
	}
	// Without its token, as a probe sent before it has one would be.
	r := httptest.NewRequest("HEAD", "/llm/anthropic/api/hello", nil)
	if w := serveLLM(b, r); w.Code != http.StatusForbidden || len(got) != 4 {
		t.Errorf("a probe without the token: %d", w.Code)
	}
}

// The usage is read from a streamed answer's events, the most each says,
// and from a whole one in each API's words, with what was read from the
// cache apart from what was taken in.
func TestUsageReader(t *testing.T) {
	r := &usageReader{}
	r.Write([]byte("data: {\"message\":{\"usage\":{\"input_tokens\":120,\"cache_read_input_tokens\":9000,\"cache_creation_input_tokens\":300,\"output_tokens\":1}}}\n\ndata: {\"usa"))
	r.Write([]byte("ge\":{\"output_tokens\":42}}\n\ndata: [DONE]\n"))
	if u := r.usage(); u != (llmUsage{120, 42, 9000, 300}) {
		t.Errorf("Anthropic's stream = %+v", u)
	}
	for body, want := range map[string]llmUsage{
		`{"usage": {"prompt_tokens": 10, "completion_tokens": 3, "prompt_tokens_details": {"cached_tokens": 4}}}`:                                      {input: 6, output: 3, cacheRead: 4},
		`{"type": "response.completed", "response": {"usage": {"input_tokens": 7, "output_tokens": 2, "input_tokens_details": {"cached_tokens": 0}}}}`: {input: 7, output: 2},
		`{"usageMetadata": {"promptTokenCount": 5, "cachedContentTokenCount": 2, "candidatesTokenCount": 9, "thoughtsTokenCount": 4}}`:                 {input: 3, output: 13, cacheRead: 2},
		// Bedrock's binary events, with the usage in the last.
		"\x00\x00\x00\x9b:event-type\x07\x00\x08metadata{\"metrics\":{\"latencyMs\":5},\"usage\":{\"inputTokens\":11,\"outputTokens\":5,\"cacheReadInputTokens\":100}}\x8f\x12": {input: 11, output: 5, cacheRead: 100},
		`{"error": "nope"}`: {},
	} {
		r := &usageReader{}
		r.Write([]byte(body))
		if u := r.usage(); u != want {
			t.Errorf("%q = %+v, want %+v", body, u, want)
		}
	}
	if m := modelOf(nil, "models/gemini-3:streamGenerateContent"); m != "gemini-3" {
		t.Errorf("Google's model = %q", m)
	}
	for path, want := range map[string]string{
		"model/global.anthropic.claude-sonnet-5-5/invoke-with-response-stream": "global.anthropic.claude-sonnet-5-5",
		"model/us.anthropic.claude-haiku-4-5-20251001-v1:0/invoke":             "us.anthropic.claude-haiku-4-5-20251001-v1:0",
		"model/amazon.nova-pro-v1:0/converse-stream":                           "amazon.nova-pro-v1:0",
	} {
		if m := modelOf([]byte(`{"anthropic_version":"bedrock-2023-05-31","max_tokens":1}`), path); m != want {
			t.Errorf("Bedrock's model of %s = %q", path, m)
		}
	}
}

// One of AWS's binary events: its lengths, the prelude's CRC, its headers,
// its payload and the whole's CRC.
func awsEvent(headers map[string]string, payload []byte) []byte {
	var hs []byte
	for _, k := range slices.Sorted(maps.Keys(headers)) {
		v := headers[k]
		hs = append(hs, byte(len(k)))
		hs = append(hs, k...)
		hs = append(hs, 7, byte(len(v)>>8), byte(len(v)))
		hs = append(hs, v...)
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(12+len(hs)+len(payload)+4))
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(hs)))
	frame = binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
	frame = append(append(frame, hs...), payload...)
	return binary.BigEndian.AppendUint32(frame, crc32.ChecksumIEEE(frame))
}

// Bedrock's InvokeModel, as Claude Code asks for it, streams each of
// Anthropic's events base64'd in one of AWS's; the usage is read from them
// however the stream is cut, past what is kept of it whole too, as a long
// answer's last event, saying what it gave out, comes after a lot.
func TestUsageReaderReadsBedrocksInvokeStream(t *testing.T) {
	var stream []byte
	chunk := func(event string) {
		payload, _ := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(event)), "p": "abcd"})
		stream = append(stream, awsEvent(map[string]string{":event-type": "chunk", ":content-type": "application/json", ":message-type": "event"}, payload)...)
	}
	chunk(`{"type":"message_start","message":{"usage":{"input_tokens":120,"cache_read_input_tokens":9000,"cache_creation_input_tokens":300,"output_tokens":1}}}`)
	text := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + strings.Repeat("word ", 40) + `"}}`
	for len(stream) < maxUsageBody+(1<<20) {
		chunk(text)
	}
	chunk(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4242}}`)
	chunk(`{"type":"message_stop","amazon-bedrock-invocationMetrics":{"inputTokenCount":120,"outputTokenCount":4242}}`)

	r := &usageReader{bedrock: true}
	for rest := stream; len(rest) > 0; {
		n := min(len(rest), 7777)
		r.Write(rest[:n])
		rest = rest[n:]
	}
	if u := r.usage(); u != (llmUsage{input: 120, output: 4242, cacheRead: 9000, cacheWrite: 300}) {
		t.Errorf("Bedrock's InvokeModel stream = %+v", u)
	}
	if len(r.events) > 64 {
		t.Errorf("%d bytes of the stream were kept", len(r.events))
	}
}

// A call costs what its tokens of each kind cost at its model's price: the
// worker's models.json's, the catalog's for its provider, or the catalog's
// for its ID with any provider, without what comes before a slash too.
func TestPrices(t *testing.T) {
	p := priceBook{
		host: map[string]map[string]modelCost{"gateway": {"house-model": {Input: 1}}},
		catalog: map[string]map[string]modelCost{
			"anthropic":  {"claude-x": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
			"openrouter": {"anthropic/claude-x": {Input: 4}},
		},
	}
	for _, c := range []struct {
		provider, model string
		want            float64
		priced          bool
	}{
		{"gateway", "house-model", 1, true},
		{"anthropic", "claude-x", 3, true},
		{"openrouter", "anthropic/claude-x", 4, true},
		{"gateway", "claude-x", 3, true},
		{"gateway", "vendor/claude-x", 3, true},
		{"gateway", "mystery", 0, false},
	} {
		got, ok := p.of(c.provider, c.model)
		if got.Input != c.want || ok != c.priced {
			t.Errorf("%s/%s = %+v, %v", c.provider, c.model, got, ok)
		}
	}
	c, _ := p.of("anthropic", "claude-x")
	// $3 a million taken in, $15 given out, $0.30 read and $3.75 written.
	if m := c.micros(llmUsage{input: 1000, output: 100, cacheRead: 10000, cacheWrite: 2000}); m != 3000+1500+3000+7500 {
		t.Errorf("cost = %d millionths", m)
	}
}

// Every request the worker gets says its project and whether it inherits
// the instance's configuration, false too, so a path that forgets to set
// it withholds the instance's from the step rather than saying nothing,
// which the worker takes for a webapp from before projects.
func TestWorkerRequestsSayWhetherTheyInherit(t *testing.T) {
	for name, v := range map[string]any{"agent": agentRun{}, "script": scriptRun{}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"inheritsInstance":false`) || !strings.Contains(string(b), `"project":0`) {
			t.Errorf("an %s request says %s", name, b)
		}
	}
}
