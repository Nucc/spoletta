package main

// The model proxy: agents reach their models' providers through the webapp,
// never with the providers' keys. Each agent step, follow-up and assistant
// reply gets a token of its own, which the worker writes into pi's
// models.json as every provider's key, with the provider's URL pointed
// here, at /llm/{provider}/; the proxy takes a request with a token it
// granted, puts the provider's real key where the token was, and passes it
// on to the provider, streaming the answer back. The token ends with the
// step, or a while after its timeout should the webapp lose it, so an
// agent that printed it has nothing worth keeping. Keys are opened from
// their sealed rows as now and stay in this process.
//
// The providers a token reaches are those added in the settings, with
// their saved keys or ones in the webapp's environment, and those of the
// worker's own pi config (see the worker's host_providers.go) with the
// model keys saved for their variables (model_keys.go) or the webapp's
// environment's. Where each is reached is its custom URL, the URL the
// worker's models.json gives it, as a gateway's, or the provider's own,
// below. OpenRouter and Fireworks serve Anthropic's API at another path
// than OpenAI's; pi's requests in Anthropic's carry anthropic-version,
// which picks it.
//
// pi sends the key as x-api-key for Anthropic's API, x-goog-api-key for
// Google's, and as a bearer token for OpenAI's, Mistral's and the rest; the
// proxy finds the token in whichever and puts the key there in its place.
// Bedrock with a Bedrock API key is reached so too; without one, pi signs
// its requests with the worker's AWS credentials, which no proxy can do
// without them, so it isn't routed here.
//
// What each call took and gave, as the provider's answer says, goes in
// llm_usage, for whom and which step; reading it is best effort and never
// fails the call.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Where a provider pi knows is reached, as pi's catalog has it; for
// OpenRouter and Fireworks, with where their Anthropic API is.
var knownUpstreams = map[string]struct{ base, anthropic string }{
	"anthropic":  {base: "https://api.anthropic.com"},
	"openai":     {base: "https://api.openai.com/v1"},
	"openrouter": {base: "https://openrouter.ai/api/v1", anthropic: "https://openrouter.ai/api"},
	"google":     {base: "https://generativelanguage.googleapis.com/v1beta"},
	"mistral":    {base: "https://api.mistral.ai"},
	"groq":       {base: "https://api.groq.com/openai/v1"},
	"xai":        {base: "https://api.x.ai/v1"},
	"cerebras":   {base: "https://api.cerebras.ai/v1"},
	"fireworks":  {base: "https://api.fireworks.ai/inference/v1", anthropic: "https://api.fireworks.ai/inference"},
	"zai":        {base: "https://api.z.ai/api/coding/paas/v4"},
}

// LLM_UPSTREAMS reaches providers pi knows elsewhere than their own URLs,
// such as through a company's egress gateway: comma-separated
// provider=URL pairs, anthropic=https://gateway.example.com/anthropic.
func upstreamOverrides() map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(os.Getenv("LLM_UPSTREAMS"), ",") {
		id, u, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && id != "" && checkProviderURL(u) == "" {
			out[id] = strings.TrimSuffix(u, "/")
		} else if strings.TrimSpace(pair) != "" {
			log.Printf("WARNING: LLM_UPSTREAMS: %q isn't provider=URL; left out", pair)
		}
	}
	return out
}

// Where Bedrock is reached, in the webapp's AWS_REGION.
func bedrockUpstream() string {
	return "https://bedrock-runtime." + env("AWS_REGION", "us-east-1") + ".amazonaws.com"
}

// Where a step reaches its providers, as the worker gets it.
type llmAccess struct {
	URL       string   `json:"url"`
	Token     string   `json:"token"`
	Providers []string `json:"providers"`
}

// A provider a token reaches: where, and with which key.
type llmRoute struct {
	base, anthropic, key string
}

type llmGrant struct {
	user      *int64
	run, step int64
	routes    map[string]llmRoute
	issued    time.Time
	expire    *time.Timer
}

type llmBroker struct {
	db        *sql.DB
	workerURL string
	// Where the worker reaches the proxy.
	proxyURL string

	mu     sync.Mutex
	grants map[string]*llmGrant
	// The worker's providers, asked for at most once a minute.
	host   []hostProvider
	hostAt time.Time
	// Where providers pi knows are reached instead of their own URLs.
	upstreams map[string]string
}

// The broker every agent run goes through; set as the webapp starts.
var llm *llmBroker

func newLLMBroker(db *sql.DB, workerURL, proxyURL string) *llmBroker {
	return &llmBroker{db: db, workerURL: workerURL, proxyURL: strings.TrimSuffix(proxyURL, "/"), grants: map[string]*llmGrant{}, upstreams: upstreamOverrides()}
}

// A provider of the worker's pi config, as the worker says; see the
// worker's host_providers.go.
type hostProvider struct {
	ID      string `json:"id"`
	Env     string `json:"env"`
	BaseURL string `json:"baseUrl,omitempty"`
	API     string `json:"api,omitempty"`
}

// The worker's providers, asked for again once a minute has passed; what
// it said last when it doesn't answer.
func (b *llmBroker) hostProviders(ctx context.Context) ([]hostProvider, error) {
	b.mu.Lock()
	host, at := b.host, b.hostAt
	b.mu.Unlock()
	if host != nil && time.Since(at) < time.Minute {
		return host, nil
	}
	var fresh []hostProvider
	if err := callWorker(ctx, http.MethodGet, b.workerURL+"/host-providers", nil, &fresh); err != nil {
		return host, err
	}
	if fresh == nil {
		fresh = []hostProvider{}
	}
	b.mu.Lock()
	b.host, b.hostAt = fresh, time.Now()
	b.mu.Unlock()
	return fresh, nil
}

// The providers a step may reach and how, by ID: those added in the
// settings over the worker's own of the same ID. A provider without a key
// to be had isn't one; its agents fail saying pi has no key for it.
func (b *llmBroker) routes(ctx context.Context) (map[string]llmRoute, error) {
	added, err := modelProviders(ctx, b.db)
	if err != nil {
		return nil, err
	}
	saved, err := modelKeys(ctx, b.db)
	if err != nil {
		return nil, err
	}
	host, err := b.hostProviders(ctx)
	if err != nil {
		log.Printf("the worker didn't say which providers its pi config has, so its own models' providers are reached as it last said: %v", err)
	}
	routes := map[string]llmRoute{}
	add := func(id, baseURL, key string) {
		if key == "" {
			return
		}
		r := llmRoute{base: baseURL, key: key}
		if r.base == "" {
			k := knownUpstreams[id]
			r.base, r.anthropic = k.base, k.anthropic
			if id == "amazon-bedrock" {
				r.base = bedrockUpstream()
			}
			if u, ok := b.upstreams[id]; ok {
				r.base, r.anthropic = u, ""
			}
		}
		if r.base != "" {
			routes[id] = r
		}
	}
	for _, p := range host {
		if p.Env != "" && !slices.ContainsFunc(added, func(a modelProvider) bool { return a.ID == p.ID }) {
			key := saved[p.Env]
			if key == "" {
				key = os.Getenv(p.Env)
			}
			add(p.ID, p.BaseURL, key)
		}
	}
	for _, p := range added {
		key := p.key
		if p.KeySource == keyEnv {
			key = os.Getenv(p.Env)
		}
		add(p.ID, p.BaseURL, key)
	}
	return routes, nil
}

// Grants a token reaching the providers for a step of the run, or for the
// run's follow-up or a flow's assistant (step 0, run 0), done for who, and
// ends it after ttl unless revoked first.
func (b *llmBroker) grant(ctx context.Context, who *int64, run, step int64, ttl time.Duration) (*llmAccess, error) {
	routes, err := b.routes(ctx)
	if err != nil {
		return nil, err
	}
	token := randomToken()
	g := &llmGrant{user: who, run: run, step: step, routes: routes, issued: time.Now()}
	g.expire = time.AfterFunc(ttl, func() { b.revokeToken(token) })
	b.mu.Lock()
	b.grants[token] = g
	b.mu.Unlock()
	access := &llmAccess{URL: b.proxyURL + "/llm", Token: token, Providers: []string{}}
	for id := range routes {
		access.Providers = append(access.Providers, id)
	}
	slices.Sort(access.Providers)
	return access, nil
}

// Ends a token; nothing reaches a provider with it after.
func (b *llmBroker) revoke(access *llmAccess) {
	if access == nil {
		return
	}
	b.revokeToken(access.Token)
}

func (b *llmBroker) revokeToken(token string) {
	b.mu.Lock()
	if g, ok := b.grants[token]; ok {
		g.expire.Stop()
		delete(b.grants, token)
	}
	b.mu.Unlock()
}

// How long a token outlives its step's timeout, should the webapp lose the
// step: a restart ends every token anyway, as they are only kept here.
const llmGrantMargin = 5 * time.Minute

// The longest a model's request may be: prompts with images in them.
const maxLLMRequestBytes = 64 << 20

// The headers a request carries the token in, as pi's clients send it.
var llmKeyHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"}

// Headers that stay here: the token's, the connection's, and what says who
// or where the agent is.
var llmDroppedHeaders = []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Cookie", "Host", "Connection",
	"Proxy-Authorization", "Proxy-Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip",
	// The transport asks for and undoes compression itself, so usage can be read.
	"Accept-Encoding", "Content-Length"}

// The token a request carries, and the header carrying it.
func llmTokenOf(h http.Header) (token, header string) {
	for _, name := range llmKeyHeaders {
		v := h.Get(name)
		if name == "Authorization" {
			var ok bool
			if v, ok = strings.CutPrefix(v, "Bearer "); !ok {
				continue
			}
		}
		if v != "" {
			return v, name
		}
	}
	return "", ""
}

// Models are asked with POST, and listed with GET.
func registerLLMRoutes(mux *http.ServeMux, b *llmBroker) {
	for _, m := range []string{"GET", "POST"} {
		mux.HandleFunc(m+" /llm/{provider}/{path...}", b.proxy)
	}
}

// Passes an agent's request on to its provider with the provider's key.
func (b *llmBroker) proxy(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	token, header := llmTokenOf(r.Header)
	b.mu.Lock()
	g, granted := b.grants[token]
	var route llmRoute
	reaches := false
	if granted {
		route, reaches = g.routes[provider]
	}
	b.mu.Unlock()
	if token == "" || !granted || !reaches {
		http.Error(w, "the model provider "+provider+" isn't available to this step", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLLMRequestBytes))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	base := route.base
	if route.anthropic != "" && r.Header.Get("Anthropic-Version") != "" {
		base = route.anthropic
	}
	target := strings.TrimSuffix(base, "/") + "/" + r.PathValue("path")
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "the model provider's URL: "+err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range r.Header {
		if !slices.Contains(llmDroppedHeaders, http.CanonicalHeaderKey(k)) {
			req.Header[k] = v
		}
	}
	if header == "Authorization" {
		req.Header.Set(header, "Bearer "+route.key)
	} else {
		req.Header.Set(header, route.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Its URL holds no secret, but needn't be told the agent either.
		http.Error(w, "couldn't reach the model provider "+provider+": "+errorWithoutURL(err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Set-Cookie", "Connection", "Transfer-Encoding", "Content-Length":
			continue
		}
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	u := &usageReader{}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			u.Write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	if resp.StatusCode/100 == 2 {
		b.recordUsage(g, provider, modelOf(body, r.PathValue("path")), u.usage())
	}
}

// What went wrong reaching a URL, without the URL.
func errorWithoutURL(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// The model a request asks for: its body's model, or for Google's API,
// the path's models/<model>:<method>.
func modelOf(body []byte, path string) string {
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) == nil && req.Model != "" {
		return req.Model
	}
	if _, m, ok := strings.Cut(path, "models/"); ok {
		m, _, _ = strings.Cut(m, ":")
		return m
	}
	return ""
}

// How many tokens a call took in and gave out.
type llmUsage struct {
	input, output int64
}

// Reads the usage from an answer as it streams past: each event of a
// streamed one, which may say part of it (Anthropic's says what it took in
// as it starts and what it gave out as it ends), or the whole of one that
// isn't, up to a size.
type usageReader struct {
	line  []byte
	whole bytes.Buffer
	u     llmUsage
}

const maxUsageBody = 4 << 20

func (r *usageReader) Write(p []byte) {
	if r.whole.Len() < maxUsageBody {
		r.whole.Write(p)
	}
	r.line = append(r.line, p...)
	for {
		i := bytes.IndexByte(r.line, '\n')
		if i < 0 {
			break
		}
		r.event(bytes.TrimSpace(r.line[:i]))
		r.line = r.line[i+1:]
	}
	if len(r.line) > maxUsageBody {
		r.line = nil
	}
}

func (r *usageReader) event(line []byte) {
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	var v map[string]any
	if json.Unmarshal(bytes.TrimSpace(data), &v) == nil {
		r.u.add(usageIn(v))
	}
}

func (r *usageReader) usage() llmUsage {
	if r.u == (llmUsage{}) {
		var v map[string]any
		if json.Unmarshal(r.whole.Bytes(), &v) == nil {
			r.u.add(usageIn(v))
		}
	}
	return r.u
}

// Keeps the most of each a part says.
func (u *llmUsage) add(o llmUsage) {
	u.input, u.output = max(u.input, o.input), max(u.output, o.output)
}

// The usage an answer or an event of one holds, in whichever API's words:
// usage with input_tokens and output_tokens (Anthropic's, OpenAI's
// responses) or prompt_tokens and completion_tokens (OpenAI's chat), at the
// top or in its message or response, or Google's usageMetadata.
func usageIn(v map[string]any) llmUsage {
	var u llmUsage
	num := func(m map[string]any, keys ...string) int64 {
		for _, k := range keys {
			if f, ok := m[k].(float64); ok {
				return int64(f)
			}
		}
		return 0
	}
	for _, at := range []map[string]any{v, asMap(v["message"]), asMap(v["response"])} {
		if usage := asMap(at["usage"]); usage != nil {
			u.add(llmUsage{num(usage, "input_tokens", "prompt_tokens"), num(usage, "output_tokens", "completion_tokens")})
		}
	}
	if meta := asMap(v["usageMetadata"]); meta != nil {
		u.add(llmUsage{num(meta, "promptTokenCount"), num(meta, "candidatesTokenCount")})
	}
	return u
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// Records what a call took and gave, unless the answer didn't say.
func (b *llmBroker) recordUsage(g *llmGrant, provider, model string, u llmUsage) {
	if u == (llmUsage{}) || b.db == nil {
		return
	}
	var step, run any
	if g.step != 0 {
		step = g.step
	}
	if g.run != 0 {
		run = g.run
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := b.db.ExecContext(ctx,
		"INSERT INTO llm_usage (user_id, run_id, step_id, provider, model, input_tokens, output_tokens) VALUES (?, ?, ?, ?, ?, ?, ?)",
		g.user, run, step, provider, truncate(model, 255), u.input, u.output); err != nil {
		log.Printf("record the usage of %s: %v", provider, err)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// How long a token lasts without a timeout of the step's to go by.
const llmDefaultTTL = time.Hour

// Gives the run the providers added in the settings and a token reaching
// every provider through the proxy, for its timeout ttl (or an hour, for
// none) at most; the caller revokes it once the run is over.
func (run *agentRun) withModelAccess(ctx context.Context, db *sql.DB, who *int64, runID, stepID int64, ttl time.Duration) error {
	providers, err := modelProviders(ctx, db)
	if err != nil {
		return err
	}
	for _, p := range providers {
		run.Providers = append(run.Providers, runProvider{ID: p.ID, Name: p.Name, API: p.API, BaseURL: p.BaseURL, Env: p.Env, Models: p.Models})
	}
	if ttl <= 0 {
		ttl = llmDefaultTTL
	}
	run.LLM, err = llm.grant(ctx, who, runID, stepID, ttl+llmGrantMargin)
	return err
}
