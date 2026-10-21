package main

// Egress: the internet hosts steps may reach. When steps run in containers
// of their own (the worker's SANDBOX=podman), their network reaches only
// the webapp; models and MCP servers are reached through its proxies, and
// anything else through this one, a forward proxy the step's HTTPS_PROXY
// and HTTP_PROXY name, with a token of the step's as the proxy's
// credentials. It takes CONNECT for TLS and plain requests for http, to a
// host an admin allowed in the settings (egress_hosts, Internet access) or
// one of the step's own repositories' hosts, which its git pushes to.
// Without any, a step reaches nothing. Every connection goes in the step's
// run's log.
//
// An allowed host is a name, which takes ports 80 and 443, or name:port;
// *.example.com takes example.com's subdomains. A host is dialled only at
// a public address, unless an admin allowed it by name: an allowed name
// that resolves to the webapp's own network, or a repository's URL a flow
// names, mustn't reach the database or the webapp's neighbours.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Where a step's container reaches the internet, as the worker gets it: the
// proxy, with the step's token as its credentials.
type egressAccess struct {
	URL string `json:"url"`
}

type egressGrant struct {
	user      *int64
	run, step int64
	// The hosts of the step's repositories, with their ports.
	repos  []string
	expire *time.Timer
	// The tunnels open with it, which end with it; the broker's lock
	// guards them.
	tunnels map[net.Conn]bool
}

type egressBroker struct {
	db       *sql.DB
	proxyURL string

	mu     sync.Mutex
	grants map[string]*egressGrant
	// Whether loopback addresses may be dialled, as only tests' hosts are.
	loopback bool
	// The hosts allowed, when not the database's, as for tests.
	hosts []egressHost
}

// The broker steps' egress goes through; set as the webapp starts.
var egress *egressBroker

func newEgressBroker(db *sql.DB, proxyURL string) *egressBroker {
	return &egressBroker{db: db, proxyURL: strings.TrimSuffix(proxyURL, "/"), grants: map[string]*egressGrant{}}
}

// Grants the step a token for the proxy, reaching the allowed hosts and
// its workspace's repositories' hosts, for ttl at most; nil for a broker
// there isn't.
func (b *egressBroker) grant(who *int64, run, step int64, ws *workspaceSpec, ttl time.Duration) *egressAccess {
	if b == nil {
		return nil
	}
	g := &egressGrant{user: who, run: run, step: step, tunnels: map[net.Conn]bool{}}
	if ws != nil {
		for _, r := range ws.Repositories {
			if u, err := url.Parse(r.URL); err == nil && u.Hostname() != "" {
				port := u.Port()
				if port == "" {
					port = "443"
				}
				g.repos = append(g.repos, net.JoinHostPort(strings.ToLower(u.Hostname()), port))
			}
		}
	}
	if ttl <= 0 {
		ttl = llmDefaultTTL
	}
	token := randomToken()
	g.expire = time.AfterFunc(ttl+llmGrantMargin, func() { b.revokeToken(token) })
	b.mu.Lock()
	b.grants[token] = g
	b.mu.Unlock()
	u, err := url.Parse(b.proxyURL)
	if err != nil {
		return nil
	}
	u.User = url.UserPassword("step", token)
	u.Path = ""
	return &egressAccess{URL: u.String()}
}

// Ends the step's token.
func (b *egressBroker) revoke(a *egressAccess) {
	if b == nil || a == nil {
		return
	}
	if u, err := url.Parse(a.URL); err == nil && u.User != nil {
		token, _ := u.User.Password()
		b.revokeToken(token)
	}
}

// Ends the token, and every tunnel open with it.
func (b *egressBroker) revokeToken(token string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if g, ok := b.grants[token]; ok {
		g.expire.Stop()
		delete(b.grants, token)
		for c := range g.tunnels {
			c.Close()
		}
	}
}

// Keeps the tunnel's connection with its grant, so ending the grant ends
// it; false if the grant is over already.
func (b *egressBroker) track(g *egressGrant, c net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !slices.ContainsFunc(slices.Collect(maps.Values(b.grants)), func(o *egressGrant) bool { return o == g }) {
		return false
	}
	g.tunnels[c] = true
	return true
}

func (b *egressBroker) untrack(g *egressGrant, c net.Conn) {
	b.mu.Lock()
	delete(g.tunnels, c)
	b.mu.Unlock()
}

// What the proxy is asked: a CONNECT, or a plain request naming its URL
// whole. Everything else goes on to the webapp.
func isProxyRequest(r *http.Request) bool {
	return r.Method == http.MethodConnect || strings.HasPrefix(r.RequestURI, "http://") || strings.HasPrefix(r.RequestURI, "https://")
}

// Serves proxy requests; next serves the rest.
func (b *egressBroker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b == nil || !isProxyRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		b.serve(w, r)
	})
}

// The grant of the token a proxy request carries as its password.
func (b *egressBroker) grantOf(r *http.Request) *egressGrant {
	user, pass, ok := strings.Cut(r.Header.Get("Proxy-Authorization"), " ")
	if !ok || !strings.EqualFold(user, "Basic") {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(pass)
	if err != nil {
		return nil
	}
	_, token, _ := strings.Cut(string(raw), ":")
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.grants[token]
}

func (b *egressBroker) serve(w http.ResponseWriter, r *http.Request) {
	g := b.grantOf(r)
	if g == nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="steps"`)
		http.Error(w, "only a step's own token reaches the internet through here", http.StatusProxyAuthRequired)
		return
	}
	target := r.Host
	if r.Method != http.MethodConnect {
		target = r.URL.Host
		if r.URL.Port() == "" {
			target = net.JoinHostPort(r.URL.Hostname(), map[string]string{"http": "80", "https": "443"}[r.URL.Scheme])
		}
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		http.Error(w, "the proxy takes host:port", http.StatusBadRequest)
		return
	}
	host = strings.ToLower(host)
	listed, err := b.allowed(r.Context(), host, port)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	viaRepo := slices.Contains(g.repos, net.JoinHostPort(host, port))
	if !listed && !viaRepo {
		b.logConnection(g, "refused %s:%s, which isn't allowed: an admin allows hosts in Settings, under Internet access", host, port)
		http.Error(w, fmt.Sprintf("%s isn't a host steps may reach; an admin allows hosts in Settings, under Internet access", host), http.StatusForbidden)
		return
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, Control: refusePrivate(listed, b.loopback)}
	if r.Method == http.MethodConnect {
		b.tunnel(w, r, g, dialer, net.JoinHostPort(host, port))
		return
	}
	b.forward(w, r, g, dialer)
}

// Passes the bytes of a CONNECT both ways, once the host is reached.
func (b *egressBroker) tunnel(w http.ResponseWriter, r *http.Request, g *egressGrant, dialer *net.Dialer, target string) {
	upstream, err := dialer.DialContext(r.Context(), "tcp", target)
	if err != nil {
		b.logConnection(g, "couldn't reach %s: %v", target, err)
		http.Error(w, "couldn't reach "+target+": "+errorWithoutURL(err), http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	client, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "the proxy can't tunnel here", http.StatusInternalServerError)
		return
	}
	defer client.Close()
	if !b.track(g, client) {
		return
	}
	defer b.untrack(g, client)
	b.logConnection(g, "connected to %s", target)
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		// What the client sent with its request already comes first.
		io.Copy(upstream, io.MultiReader(bufferedOf(buf.Reader), client))
		done <- struct{}{}
	}()
	go func() {
		io.Copy(client, upstream)
		done <- struct{}{}
	}()
	<-done
}

// What a hijacked connection's reader holds already.
func bufferedOf(r *bufio.Reader) io.Reader {
	n := r.Buffered()
	b, _ := r.Peek(n)
	return strings.NewReader(string(b))
}

// Sends a plain http request on.
func (b *egressBroker) forward(w http.ResponseWriter, r *http.Request, g *egressGrant, dialer *net.Dialer) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for k, v := range r.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Proxy-Authorization", "Proxy-Connection", "Connection", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		}
		req.Header[k] = v
	}
	req.ContentLength = r.ContentLength
	transport := &http.Transport{DialContext: dialer.DialContext, Proxy: nil}
	defer transport.CloseIdleConnections()
	resp, err := transport.RoundTrip(req)
	if err != nil {
		b.logConnection(g, "couldn't reach %s: %v", r.URL.Host, errorWithoutURL(err))
		http.Error(w, "couldn't reach "+r.URL.Host+": "+errorWithoutURL(err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	b.logConnection(g, "%s %s://%s, answered %d", r.Method, r.URL.Scheme, r.URL.Host, resp.StatusCode)
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// Notes a connection in the step's run's log.
func (b *egressBroker) logConnection(g *egressGrant, format string, args ...any) {
	if g.run == 0 || b.db == nil {
		log.Printf("egress of step %d: "+format, append([]any{g.step}, args...)...)
		return
	}
	runLog(b.db, g.run, g.step, logInfo, "internet access: "+format, args...)
}

// Refuses dialling an address that isn't public, unless an admin allowed
// the host by name.
func refusePrivate(listed, loopback bool) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if ip == nil || loopback && ip.IsLoopback() {
			return nil
		}
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
			return fmt.Errorf("%s isn't an address steps may reach", ip)
		}
		if ip.IsPrivate() && !listed {
			return fmt.Errorf("%s is a private address, which only a host an admin allowed may be at", ip)
		}
		return nil
	}
}

// Whether an admin allowed the host at the port.
func (b *egressBroker) allowed(ctx context.Context, host, port string) (bool, error) {
	hosts := b.hosts
	if hosts == nil {
		var err error
		if hosts, err = egressHosts(ctx, b.db); err != nil {
			return false, err
		}
	}
	for _, h := range hosts {
		if egressMatches(h.Host, host, port) {
			return true, nil
		}
	}
	return false, nil
}

// Whether an allowed host takes host:port.
func egressMatches(allowed, host, port string) bool {
	name, p, err := net.SplitHostPort(allowed)
	if err != nil {
		name, p = allowed, ""
	}
	if p == "" && port != "80" && port != "443" || p != "" && p != port {
		return false
	}
	if rest, ok := strings.CutPrefix(name, "*."); ok {
		return strings.HasSuffix(host, "."+rest)
	}
	return host == name
}

type egressHost struct {
	Host      string    `json:"host"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
}

func egressHosts(ctx context.Context, db rowsQuerier) ([]egressHost, error) {
	hosts := []egressHost{}
	err := scanAll(ctx, db, "SELECT host, note, created_at FROM egress_hosts ORDER BY host", func(rows *sql.Rows) error {
		var h egressHost
		if err := rows.Scan(&h.Host, &h.Note, &h.CreatedAt); err != nil {
			return err
		}
		hosts = append(hosts, h)
		return nil
	})
	return hosts, err
}

// What an allowed host may be: a name, *. and a name, either with :port.
var egressHostPattern = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

func registerEgressRoutes(mux *http.ServeMux, db *sql.DB) {
	list := func(w http.ResponseWriter, r *http.Request) {
		hosts, err := egressHosts(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, hosts)
	}
	mux.HandleFunc("GET /api/egress-hosts", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		list(w, r)
	})
	mux.HandleFunc("POST /api/egress-hosts", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		var body struct {
			Host string `json:"host"`
			Note string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		host := strings.ToLower(strings.TrimSpace(body.Host))
		if len(host) > 255 || !egressHostPattern.MatchString(host) {
			http.Error(w, "a host is a name such as registry.npmjs.org, *.example.com for its subdomains, with :port for a port other than 80 and 443", http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "INSERT INTO egress_hosts (host, note) VALUES (?, ?) ON DUPLICATE KEY UPDATE note = VALUES(note)", host, truncate(strings.TrimSpace(body.Note), 255)); err != nil {
			serverError(w, err)
			return
		}
		list(w, r)
	})
	mux.HandleFunc("DELETE /api/egress-hosts/{host}", func(w http.ResponseWriter, r *http.Request) {
		if refuseKeys(w, r) {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM egress_hosts WHERE host = ?", r.PathValue("host")); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
