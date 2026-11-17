package main

// The model proxy: agents reach their models' providers through the webapp,
// never with the providers' keys. Each agent step, follow-up and assistant
// reply gets a token of its own, which the worker hands its agent, pi or
// Claude Code, as every provider's key, with the provider's URL pointed
// here, at /llm/{provider}/: pi has it in its models.json, Claude Code as
// its API key with its base URL at /llm/{provider}. The proxy takes a
// request with a token granted at any replica of the webapp (grants.go),
// puts the provider's real key where the token was, and passes it on to
// the provider, streaming the answer back. The token ends with the step,
// or a while after its timeout should the webapp lose it, so an agent that
// printed it has nothing worth keeping. Keys are opened from their sealed
// rows by the replica a call reaches, and stay in its memory.
//
// The providers a token reaches are its project's: those its owners added
// in the settings, with their saved keys. A project inheriting what the
// instance is configured with outside the database (inherits_instance,
// Legacy alone; see projects.go) also has keys in the webapp's environment,
// the providers of the worker's own pi config (see the worker's
// host_providers.go) with the model keys saved for their variables
// (model_keys.go) or the environment's, and LLM_UPSTREAMS; another project
// has none of them, so its owners can't have the proxy send the webapp's
// environment anywhere. Where each is reached is its custom URL, the URL
// the worker's models.json gives it, as a gateway's, or the provider's
// own, below. OpenRouter and Fireworks serve Anthropic's API at another
// path than OpenAI's; the agent's requests in Anthropic's carry
// anthropic-version, which picks it.
//
// pi sends the key as x-api-key for Anthropic's API, x-goog-api-key for
// Google's, and as a bearer token for OpenAI's, Mistral's and the rest; the
// proxy finds the token in whichever and puts the key there in its place.
// Claude Code speaks Anthropic's API, sending the token as x-api-key to
// /llm/{provider}/v1/messages (with ?beta=true, and its anthropic-beta
// list, which pass on as they are), counting tokens at
// v1/messages/count_tokens, whose answer has no usage to record, and
// asking v1/models and api/hello as it starts, which record nothing
// either; or Bedrock's, sending it as a Bedrock API key, a bearer token, to
// /llm/amazon-bedrock/model/{model}/invoke-with-response-stream, listing
// inference-profiles and calling two models of its own once with a token
// each as it starts. Bedrock with a Bedrock API key is reached so too;
// without one, pi signs its requests with the worker's AWS credentials,
// which no proxy can do without them, so it isn't routed here.
//
// What each call took and gave, as the provider's answer says, goes in
// llm_usage, for whom, which step and the grant's project, with what it
// cost (spend.go);
// reading it is best effort and never fails the call. A run or a person
// that has spent what the limits let them (limits.go) has its calls
// refused, and a run is stopped as it gets there.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
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
// provider=URL pairs, anthropic=https://gateway.example.com/anthropic. Only
// for a project inheriting the instance's configuration.
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

// A model grant: who and which step it is for, and what it reaches.
type llmGrant struct {
	proxyGrant
	llmReaches
}

// What a model grant reaches, kept with it: the providers it was granted
// for, whose routes the replica a call reaches works out again, and the
// most its run and its person may spend (spend.go), 0 for no limit.
type llmReaches struct {
	Providers []string `json:"providers"`
	RunLimit  int64    `json:"runLimit,omitempty"`
	UserLimit int64    `json:"userLimit,omitempty"`
}

type llmBroker struct {
	db        *sql.DB
	workerURL string
	// Where the worker reaches the proxy.
	proxyURL string
	grants   grantStore

	mu sync.Mutex
	// Ends each token this replica granted as it expires.
	expiries map[string]*time.Timer
	// The routes to each project's providers, by project, worked out at
	// most every routesFor.
	routed map[int64]projectRoutes
	// The worker's providers, asked for at most once a minute.
	host   []hostProvider
	hostAt time.Time
	// Where providers pi knows are reached instead of their own URLs.
	upstreams map[string]string
	// What models cost, as pi's catalog says, asked for at most every
	// pricesFor.
	catalog   map[string]map[string]modelCost
	catalogAt time.Time
	// Stops a run that spent what it may, saying why; set to the engine's.
	overspent func(run int64, why string)
}

// A project's routes to its providers, and when they were worked out.
type projectRoutes struct {
	routes map[string]llmRoute
	at     time.Time
}

// The broker every agent run goes through; set as the webapp starts.
var llm *llmBroker

func newLLMBroker(db *sql.DB, workerURL, proxyURL string) *llmBroker {
	return &llmBroker{db: db, workerURL: workerURL, proxyURL: strings.TrimSuffix(proxyURL, "/"), grants: grantsIn(db),
		expiries: map[string]*time.Timer{}, routed: map[int64]projectRoutes{}, upstreams: upstreamOverrides()}
}

// A provider of the worker's pi config, as the worker says; see the
// worker's host_providers.go.
type hostProvider struct {
	ID      string `json:"id"`
	Env     string `json:"env"`
	BaseURL string `json:"baseUrl,omitempty"`
	API     string `json:"api,omitempty"`
	// Its models models.json prices.
	Models []struct {
		ID   string    `json:"id"`
		Cost modelCost `json:"cost"`
	} `json:"models,omitempty"`
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

// The providers a step of the project may reach and how, by ID: those
// added in its settings, over the worker's own of the same ID for a
// project inheriting the instance's configuration. A provider without a
// key to be had isn't one; its agents fail saying they have no key for it.
func (b *llmBroker) routes(ctx context.Context, project int64) (map[string]llmRoute, error) {
	inherits, err := inheritsInstance(ctx, b.db, project)
	if err != nil {
		return nil, err
	}
	added, err := modelProviders(ctx, b.db, project)
	if err != nil {
		return nil, err
	}
	var saved map[string]string
	var host []hostProvider
	if inherits {
		if saved, err = modelKeys(ctx, b.db, project); err != nil {
			return nil, err
		}
		if host, err = b.hostProviders(ctx); err != nil {
			log.Printf("the worker didn't say which providers its pi config has, so its own models' providers are reached as it last said: %v", err)
		}
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
			if u, ok := b.upstreams[id]; ok && inherits {
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
		switch {
		case p.KeySource == keyEnv && inherits:
			key = os.Getenv(p.Env)
		case p.KeySource == keyEnv:
			// The webapp's environment is the instance's.
			key = ""
		}
		add(p.ID, p.BaseURL, key)
	}
	return routes, nil
}

// How long the routes to the providers are kept before they are worked
// out again, so a key changed in the settings reaches calls soon after.
const routesFor = 10 * time.Second

// The project's routes as worked out last, or again if that was a while
// ago.
func (b *llmBroker) currentRoutes(ctx context.Context, project int64) (map[string]llmRoute, error) {
	b.mu.Lock()
	routed, ok := b.routed[project]
	b.mu.Unlock()
	if ok && time.Since(routed.at) < routesFor {
		return routed.routes, nil
	}
	return b.freshRoutes(ctx, project)
}

func (b *llmBroker) freshRoutes(ctx context.Context, project int64) (map[string]llmRoute, error) {
	routes, err := b.routes(ctx, project)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.routed[project] = projectRoutes{routes: routes, at: time.Now()}
	b.mu.Unlock()
	return routes, nil
}

// Grants a token reaching the project's providers for a step of the run or
// a follow-up of it, or for one of its flows' assistant (step 0, run 0),
// done for who, and ends it after ttl unless revoked first.
func (b *llmBroker) grant(ctx context.Context, project int64, who *int64, run, step int64, ttl time.Duration) (*llmAccess, error) {
	routes, err := b.freshRoutes(ctx, project)
	if err != nil {
		return nil, err
	}
	g := &llmGrant{proxyGrant: proxyGrant{project: project, user: who, run: run, step: step}, llmReaches: llmReaches{Providers: []string{}}}
	if err := b.limitSpend(ctx, g); err != nil {
		return nil, err
	}
	for id := range routes {
		g.Providers = append(g.Providers, id)
	}
	slices.Sort(g.Providers)
	token := randomToken()
	if err := b.grants.put(ctx, grantModel, token, g.proxyGrant, g.llmReaches, ttl); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.expiries[token] = time.AfterFunc(ttl, func() { b.revokeToken(token, "expired") })
	b.mu.Unlock()
	g.audit("model.grant", "the model proxy, reaching %s, for %s at most", strings.Join(g.Providers, ", "), ttl.Round(time.Second))
	return &llmAccess{URL: b.proxyURL + "/llm", Token: token, Providers: g.Providers}, nil
}

// Ends a token; nothing reaches a provider with it after.
func (b *llmBroker) revoke(access *llmAccess) {
	if access == nil {
		return
	}
	b.revokeToken(access.Token, "ended with its step")
}

func (b *llmBroker) revokeToken(token, why string) {
	b.mu.Lock()
	if t, ok := b.expiries[token]; ok {
		t.Stop()
		delete(b.expiries, token)
	}
	b.mu.Unlock()
	if g, ok := b.grants.remove(grantModel, token); ok {
		auditGrantEnd(grantModel, g, why)
	}
}

// How long a token outlives its step's timeout, should the webapp lose the
// step.
const llmGrantMargin = 5 * time.Minute

// The longest a model's request may be: prompts with images in them.
const maxLLMRequestBytes = 64 << 20

// The headers a request carries the token in, as the agents' clients send it.
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

// Passes an agent's request on to its provider with the provider's key:
// one of the project the step's grant names.
func (b *llmBroker) proxy(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	token, header := llmTokenOf(r.Header)
	g := &llmGrant{}
	pg, granted, err := b.grants.lookup(r.Context(), grantModel, token, &g.llmReaches)
	if err != nil {
		serverError(w, err)
		return
	}
	g.proxyGrant = pg
	var route llmRoute
	reaches := false
	if granted && slices.Contains(g.Providers, provider) {
		routes, err := b.currentRoutes(r.Context(), g.project)
		if err != nil {
			serverError(w, err)
			return
		}
		route, reaches = routes[provider]
	}
	if token == "" || !granted || !reaches {
		if granted {
			g.audit("model.refused", "a call to the model provider %s, which the step may not reach", provider)
		} else if token != "" {
			auditInstance("model.refused", nil, "a call to the model provider %s with a token that isn't granted, or ended", provider)
		}
		http.Error(w, "the model provider "+provider+" isn't available to this step", http.StatusForbidden)
		return
	}
	refused, err := b.outOfBudget(r.Context(), g)
	if err != nil {
		serverError(w, err)
		return
	}
	if refused != "" {
		g.audit("model.refused", "a call to the model provider %s: %s", provider, refused)
		if g.run != 0 {
			runLog(b.db, g.run, g.step, logError, "a call to the model provider %s was refused: %s", provider, refused)
		}
		// In the words of Anthropic's errors, which OpenAI's clients read too.
		writeJSON(w, http.StatusForbidden, map[string]any{"type": "error", "error": map[string]string{"type": "out_of_budget", "message": refused}})
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
	target, ok := llmTarget(base, r.PathValue("path"), r.URL.RawQuery)
	if !ok {
		g.audit("model.refused", "a call to the model provider %s at a path outside its URL", provider)
		http.Error(w, "the path isn't one under the model provider's URL", http.StatusBadRequest)
		return
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
	u := &usageReader{bedrock: strings.HasPrefix(resp.Header.Get("Content-Type"), "application/vnd.amazon.eventstream")}
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

// The URL a request for a path under the proxy's /llm/{provider}/ goes to:
// the same path under the provider's URL, with the request's query; false
// when the path would leave it. The path comes decoded, so %2F..%2F or
// %3F in an agent's request would otherwise reach the provider as a step
// up out of a gateway's URL, which may hold more than the provider's API
// (https://gateway/tenant/v1), or as a query of the agent's making, with
// the real key either way.
func llmTarget(base, path, query string) (string, bool) {
	if strings.ContainsAny(path, `?#\`) {
		return "", false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." || segment == "." {
			return "", false
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	// Set as a path, never as text read as a URL again, so what's left of
	// the agent's escaping, such as %2e, reaches the provider as written.
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + path
	u.RawPath = ""
	if query != "" {
		if u.RawQuery != "" {
			u.RawQuery += "&"
		}
		u.RawQuery += query
	}
	return u.String(), true
}

// What went wrong reaching a URL, without the URL.
func errorWithoutURL(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// The model a request asks for: its body's model, or for Bedrock's API the
// path's model/<model>/<method>, or for Google's the path's
// models/<model>:<method>.
func modelOf(body []byte, path string) string {
	var req struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &req) == nil && req.Model != "" {
		return req.Model
	}
	if rest, ok := strings.CutPrefix(path, "model/"); ok {
		m, _, _ := strings.Cut(rest, "/")
		return m
	}
	if _, m, ok := strings.Cut(path, "models/"); ok {
		m, _, _ = strings.Cut(m, ":")
		return m
	}
	return ""
}

// How many tokens a call took in (besides what it read from the
// provider's cache), gave out (thinking too), and read from and wrote to
// the cache.
type llmUsage struct {
	input, output, cacheRead, cacheWrite int64
}

// Reads the usage from an answer as it streams past: each event of a
// streamed one, which may say part of it (Anthropic's says what it took in
// as it starts and what it gave out as it ends), or the whole of one that
// isn't, up to a size. Bedrock streams AWS's binary events, whose usage is
// found by its key among them; its InvokeModel's, as Claude Code asks for,
// each hold one of Anthropic's events, base64'd under "bytes", read as
// they stream past, as a long answer's last, saying what it gave out, comes
// past the size kept whole.
type usageReader struct {
	line  []byte
	whole bytes.Buffer
	u     llmUsage
	// Whether the answer is AWS's event stream, and what of it is yet to be
	// read for Anthropic's events in it.
	bedrock bool
	events  []byte
}

const maxUsageBody = 4 << 20

func (r *usageReader) Write(p []byte) {
	if r.whole.Len() < maxUsageBody {
		r.whole.Write(p)
	}
	if r.bedrock {
		r.bedrockEvents(p)
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

// The key of the base64'd event in each of Bedrock InvokeModel's.
var bedrockEventKey = []byte(`"bytes":"`)

// Reads each of Anthropic's events in Bedrock's, keeping what may be the
// start of one that hasn't fully come yet.
func (r *usageReader) bedrockEvents(p []byte) {
	r.events = append(r.events, p...)
	for {
		at := bytes.Index(r.events, bedrockEventKey)
		if at < 0 {
			if n := len(r.events) - len(bedrockEventKey); n > 0 {
				r.events = r.events[n:]
			}
			return
		}
		start := at + len(bedrockEventKey)
		end := bytes.IndexByte(r.events[start:], '"')
		if end < 0 {
			r.events = r.events[at:]
			if len(r.events) > maxUsageBody {
				r.events = nil
			}
			return
		}
		var v map[string]any
		if raw, err := base64.StdEncoding.DecodeString(string(r.events[start : start+end])); err == nil && json.Unmarshal(raw, &v) == nil {
			r.u.add(usageIn(v))
		}
		r.events = r.events[start+end:]
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
	if r.u != (llmUsage{}) {
		return r.u
	}
	whole := r.whole.Bytes()
	var v map[string]any
	if json.Unmarshal(whole, &v) == nil {
		r.u.add(usageIn(v))
		return r.u
	}
	for i := 0; ; {
		at := bytes.Index(whole[i:], []byte(`"usage":`))
		if at < 0 {
			return r.u
		}
		i += at + len(`"usage":`)
		var usage map[string]any
		if json.NewDecoder(bytes.NewReader(whole[i:])).Decode(&usage) == nil {
			r.u.add(usageOf(usage))
		}
	}
}

// Keeps the most of each a part says.
func (u *llmUsage) add(o llmUsage) {
	u.input, u.output = max(u.input, o.input), max(u.output, o.output)
	u.cacheRead, u.cacheWrite = max(u.cacheRead, o.cacheRead), max(u.cacheWrite, o.cacheWrite)
}

// The usage an answer or an event of one holds, in whichever API's words,
// at the top or in its message or response, or Google's usageMetadata.
func usageIn(v map[string]any) llmUsage {
	var u llmUsage
	for _, at := range []map[string]any{v, asMap(v["message"]), asMap(v["response"])} {
		if usage := asMap(at["usage"]); usage != nil {
			u.add(usageOf(usage))
		}
	}
	if meta := asMap(v["usageMetadata"]); meta != nil {
		cached := tokens(meta, "cachedContentTokenCount")
		u.add(llmUsage{
			input:     max(tokens(meta, "promptTokenCount")-cached, 0),
			output:    tokens(meta, "candidatesTokenCount") + tokens(meta, "thoughtsTokenCount"),
			cacheRead: cached,
		})
	}
	return u
}

// A usage block: Anthropic's, whose input_tokens leaves out what was read
// from and written to the cache, which it counts apart; OpenAI's, whose
// input (prompt_tokens in its chat API) holds what was read from the cache,
// said in its details; Bedrock's, as Anthropic's in other words.
func usageOf(m map[string]any) llmUsage {
	details := asMap(m["prompt_tokens_details"])
	if details == nil {
		details = asMap(m["input_tokens_details"])
	}
	cached := tokens(details, "cached_tokens")
	in := tokens(m, "input_tokens", "prompt_tokens", "inputTokens")
	if details != nil {
		in = max(in-cached, 0)
	}
	return llmUsage{
		input:      in,
		output:     tokens(m, "output_tokens", "completion_tokens", "outputTokens"),
		cacheRead:  cached + tokens(m, "cache_read_input_tokens", "cacheReadInputTokens"),
		cacheWrite: tokens(m, "cache_creation_input_tokens", "cacheWriteInputTokens"),
	}
}

// The first of the keys m has as a number.
func tokens(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return int64(f)
		}
	}
	return 0
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// Records what a call took and gave and what it cost, unless the answer
// didn't say.
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
	inherits, err := inheritsInstance(ctx, b.db, g.project)
	if err != nil {
		log.Printf("read whether project %d inherits the instance's prices: %v", g.project, err)
	}
	price, priced := b.prices(ctx, inherits).of(provider, model)
	if !priced {
		price = unknownModelCost
	}
	cost := price.micros(u)
	var root any
	if g.root != 0 {
		root = g.root
	}
	defer b.checkSpent(ctx, g)
	if _, err := b.db.ExecContext(ctx,
		`INSERT INTO llm_usage (project_id, user_id, run_id, root_run_id, step_id, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_micros, priced)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.project, g.user, run, root, step, provider, truncate(model, 255), u.input, u.output, u.cacheRead, u.cacheWrite, cost, priced); err != nil {
		log.Printf("record the usage of %s: %v", provider, err)
		return
	}
	// The run the call counts toward keeps what its calls cost, for the
	// Activity page (activity.go). Limits go by the calls recorded above, so
	// a call this misses is still held to them.
	if g.root != 0 {
		unpriced := 0
		if !priced {
			unpriced = 1
		}
		if _, err := b.db.ExecContext(ctx, "UPDATE runs SET spent_micros = spent_micros + ?, unpriced_calls = unpriced_calls + ? WHERE id = ?",
			cost, unpriced, g.root); err != nil {
			log.Printf("add what a call of %s cost to run %d: %v", provider, g.root, err)
		}
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

// Gives the run of the project the providers added in its settings, a
// token reaching every provider of its through the proxy, for its timeout
// ttl (or an hour, for none) at most, which the caller revokes once the run
// is over, and says to the worker its project and whether it inherits what
// the instance is configured with outside the database, which the worker
// withholds otherwise. Every agent the webapp asks the worker for gets it
// here: a step's, a follow-up's and an assistant's reply.
func (run *agentRun) withModelAccess(ctx context.Context, db *sql.DB, project int64, who *int64, runID, stepID int64, ttl time.Duration) error {
	inherits, err := inheritsInstance(ctx, db, project)
	if err != nil {
		return err
	}
	run.Project, run.InheritsInstance = project, inherits
	providers, err := modelProviders(ctx, db, project)
	if err != nil {
		return err
	}
	for _, p := range providers {
		run.Providers = append(run.Providers, runProvider{ID: p.ID, Name: p.Name, API: p.API, BaseURL: p.BaseURL, Env: p.Env, Models: p.Models})
	}
	if ttl <= 0 {
		ttl = llmDefaultTTL
	}
	run.LLM, err = llm.grant(ctx, project, who, runID, stepID, ttl+llmGrantMargin)
	return err
}
