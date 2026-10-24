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
// names, mustn't reach the database or the webapp's neighbours. Not public
// are the private ranges, shared address space (100.64.0.0/10, where
// clusters often keep pods and services), the IETF's, benchmarking and
// reserved ranges, in IPv4 or written as IPv6; the machine's own,
// link-local (the cloud's metadata service) and multicast addresses are
// never dialled, allowed or not.

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

// What an internet grant reaches, kept with it: the hosts of the step's
// repositories, with their ports.
type egressReaches struct {
	Repos []string `json:"repos"`
}

type egressGrant struct {
	proxyGrant
	egressReaches
	token string
}

type egressBroker struct {
	db       *sql.DB
	proxyURL string
	grants   grantStore

	mu sync.Mutex
	// Ends each token this replica granted as it expires.
	expiries map[string]*time.Timer
	// The tunnels open here, by the token they were opened with, which end
	// with it, wherever it is revoked; see watchTunnels.
	tunnels map[string]map[net.Conn]bool
	// Whether loopback addresses may be dialled, as only tests' hosts are.
	loopback bool
	// The hosts allowed, when not the database's, as for tests.
	hosts []egressHost
}

// The broker steps' egress goes through; set as the webapp starts.
var egress *egressBroker

func newEgressBroker(db *sql.DB, proxyURL string) *egressBroker {
	return &egressBroker{db: db, proxyURL: strings.TrimSuffix(proxyURL, "/"), grants: grantsIn(db),
		expiries: map[string]*time.Timer{}, tunnels: map[string]map[net.Conn]bool{}}
}

// Grants the step a token for the proxy, reaching the allowed hosts and
// its workspace's repositories' hosts, for ttl at most; nil for a broker
// there isn't, or a token that couldn't be kept, which leaves the step
// without the internet.
func (b *egressBroker) grant(who *int64, run, step int64, ws *workspaceSpec, ttl time.Duration) *egressAccess {
	if b == nil {
		return nil
	}
	g := proxyGrant{user: who, run: run, step: step}
	reaches := egressReaches{Repos: []string{}}
	if ws != nil {
		for _, r := range ws.Repositories {
			if u, err := url.Parse(r.URL); err == nil && u.Hostname() != "" {
				port := u.Port()
				if port == "" {
					port = "443"
				}
				reaches.Repos = append(reaches.Repos, net.JoinHostPort(strings.ToLower(u.Hostname()), port))
			}
		}
	}
	if ttl <= 0 {
		ttl = llmDefaultTTL
	}
	u, err := url.Parse(b.proxyURL)
	if err != nil {
		return nil
	}
	token := randomToken()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.grants.put(ctx, grantInternet, token, g, reaches, ttl+llmGrantMargin); err != nil {
		log.Printf("keep the egress token of step %d: %v", step, err)
		return nil
	}
	b.mu.Lock()
	b.expiries[token] = time.AfterFunc(ttl+llmGrantMargin, func() { b.revokeToken(token, "expired") })
	b.mu.Unlock()
	u.User = url.UserPassword("step", token)
	u.Path = ""
	audit("internet.grant", who, run, step, "the egress proxy, reaching the hosts allowed and %s", hostList(reaches.Repos))
	return &egressAccess{URL: u.String()}
}

// Ends the step's token.
func (b *egressBroker) revoke(a *egressAccess) {
	if b == nil || a == nil {
		return
	}
	if u, err := url.Parse(a.URL); err == nil && u.User != nil {
		token, _ := u.User.Password()
		b.revokeToken(token, "ended with its step")
	}
}

// Ends the token, and every tunnel open with it here.
func (b *egressBroker) revokeToken(token, why string) {
	b.mu.Lock()
	if t, ok := b.expiries[token]; ok {
		t.Stop()
		delete(b.expiries, token)
	}
	b.mu.Unlock()
	g, ok := b.grants.remove(grantInternet, token)
	open := b.closeTunnels(token)
	if ok {
		audit("internet.revoke", g.user, g.run, g.step, "the egress proxy's token %s, with %d connections open", why, open)
	}
}

// Closes the tunnels open here with the token, and says how many there were.
func (b *egressBroker) closeTunnels(token string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	open := len(b.tunnels[token])
	for c := range b.tunnels[token] {
		c.Close()
	}
	delete(b.tunnels, token)
	return open
}

// Ends the tunnels open here whose tokens ended at another replica, or
// expired, every few seconds.
func (b *egressBroker) watchTunnels(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b.mu.Lock()
		tokens := slices.Collect(maps.Keys(b.tunnels))
		b.mu.Unlock()
		for _, token := range tokens {
			if _, ok, err := b.grants.lookup(ctx, grantInternet, token, nil); err == nil && !ok {
				b.closeTunnels(token)
			}
		}
	}
}

// The repositories' hosts as the audit log says them.
func hostList(hosts []string) string {
	if len(hosts) == 0 {
		return "no repository's host"
	}
	return "the repositories' " + strings.Join(hosts, ", ")
}

// Keeps the tunnel's connection with its grant, so ending the grant ends
// it; false if the grant is over already.
func (b *egressBroker) track(g *egressGrant, c net.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, ok, err := b.grants.lookup(ctx, grantInternet, g.token, nil); err != nil || !ok {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tunnels[g.token] == nil {
		b.tunnels[g.token] = map[net.Conn]bool{}
	}
	b.tunnels[g.token][c] = true
	return true
}

func (b *egressBroker) untrack(g *egressGrant, c net.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.tunnels[g.token], c)
	if len(b.tunnels[g.token]) == 0 {
		delete(b.tunnels, g.token)
	}
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

// The grant of the token a proxy request carries as its password; nil
// without one.
func (b *egressBroker) grantOf(r *http.Request) (*egressGrant, error) {
	user, pass, ok := strings.Cut(r.Header.Get("Proxy-Authorization"), " ")
	if !ok || !strings.EqualFold(user, "Basic") {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(pass)
	if err != nil {
		return nil, nil
	}
	_, token, _ := strings.Cut(string(raw), ":")
	g := &egressGrant{token: token}
	pg, ok, err := b.grants.lookup(r.Context(), grantInternet, token, &g.egressReaches)
	if err != nil || !ok {
		return nil, err
	}
	g.proxyGrant = pg
	return g, nil
}

func (b *egressBroker) serve(w http.ResponseWriter, r *http.Request) {
	g, err := b.grantOf(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	viaRepo := slices.Contains(g.Repos, net.JoinHostPort(host, port))
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

// Notes a connection in the step's run's log, and the audit log.
func (b *egressBroker) logConnection(g *egressGrant, format string, args ...any) {
	kind := "internet.connect"
	if strings.HasPrefix(format, "refused") {
		kind = "internet.refused"
	}
	audit(kind, g.user, g.run, g.step, format, args...)
	if g.run == 0 || b.db == nil {
		log.Printf("egress of step %d: "+format, append([]any{g.step}, args...)...)
		return
	}
	runLog(b.db, g.run, g.step, logInfo, "internet access: "+format, args...)
}

// The IPv4 ranges that aren't the internet's though IsPrivate doesn't say
// so: "this network", which some systems take for the machine itself, is
// never reached, like loopback; the rest are reached only by a host an
// admin allowed by name, as private addresses are. Shared address space
// (100.64.0.0/10) is where many Kubernetes clusters keep their pods and
// services, and Tailscale its machines; 192.0.0.0/24 is the IETF's own,
// 198.18.0.0/15 is for benchmarking networks, and 240.0.0.0/4, broadcast
// with it, is reserved. IPNet.Contains takes an IPv4 address written as
// IPv6 (::ffff:100.64.0.1) as the IPv4 one, so neither form gets past.
var (
	unreachableNets = parseNets("0.0.0.0/8")
	internalNets    = parseNets("100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4")
)

func parseNets(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, len(cidrs))
	for i, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		nets[i] = n
	}
	return nets
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	return slices.ContainsFunc(nets, func(n *net.IPNet) bool { return n.Contains(ip) })
}

// Refuses dialling an address that isn't public, unless an admin allowed
// the host by name: a private or otherwise internal address is reached only
// by a host allowed, and the machine's own, link-local and multicast
// addresses never.
func refusePrivate(listed, loopback bool) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if ip == nil || loopback && ip.IsLoopback() {
			return nil
		}
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || inNets(ip, unreachableNets) {
			return fmt.Errorf("%s isn't an address steps may reach", ip)
		}
		if (ip.IsPrivate() || inNets(ip, internalNets)) && !listed {
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
		auditChange(r, "internet.allow", "steps may reach %s", host)
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
		auditChange(r, "internet.disallow", "steps no longer reach %s", r.PathValue("host"))
		w.WriteHeader(http.StatusNoContent)
	})
}
