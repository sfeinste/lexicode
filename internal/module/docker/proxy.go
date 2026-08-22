package docker

// The egress proxy (story S18, decision D-10). Containers under the `none` and `allowlist`
// network policies live on lexicode-internal — an internal Docker network with no route out —
// and the only way to the outside is this proxy, run by the orchestrator on the host.
//
// Placement: architecture §3 assigns the egress proxy to module/docker, and that is where it
// lives — the kernel never imports it (the import-graph rule holds), and the scheduler (S22)
// reaches the concrete *Proxy through the module accessor at the cmd wiring site, the same
// precedent as Module.Sandbox().
//
// Reachability (measured on this machine, Docker Desktop for Mac): a container on an internal
// network cannot reach the host at all — host.docker.internal does not resolve there, and the
// bridge gateway IP carries no host listeners because the "host" is a VM. On native Linux the
// gateway IP would work, but only there. So the proxy is reached in two hops everywhere:
//
//	agent container ── lexicode-internal ──▶ lexicode-egress relay ── bridge ──▶ host proxy
//
// The relay (relay.go) is a dumb TCP forwarder pinned to host.docker.internal:<proxy port>;
// it enforces nothing and can reach nothing but this proxy. Enforcement lives here, behind
// per-run credentials.
//
// The proxy binds all interfaces (containers must reach it through Docker's NAT, and the relay
// makes the exact source interface platform-dependent), which is why it is not an open proxy
// by construction but by authentication: every request must carry Proxy-Authorization with a
// registered run token, or it is refused with 407 before anything is dialed.
//
// HTTPS is tunneled, never intercepted: a CONNECT is allowed or denied by its target hostname
// only, and the proxy itself dials that hostname — the client cannot tunnel to a host other
// than the one that was checked. Plain-HTTP proxying (absolute-form requests, what http_proxy
// clients send for port-80 URLs) is forwarded the same way, policy checked on the URL's host.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// anthropicHosts is what every proxied policy allows, even `none`: the hosts the agent itself
// needs to think. D-10 words the `none` policy as "nothing beyond what the agent itself
// needs" for exactly this reason — a container that cannot reach the model would make the
// setting a trap. Kept deliberately minimal: the Claude Code CLI's API traffic
// (api.anthropic.com) and its OAuth token endpoints (claude.ai). Telemetry hosts
// (statsig.anthropic.com, sentry) are intentionally absent — their denials are harmless to the
// run and visible in the verbose activity stream, not silent mysteries.
var anthropicHosts = []string{"api.anthropic.com", "claude.ai"}

// proxyUser is the username in the container's proxy URL; the password is the run token.
const proxyUser = "run"

// defaultDecisionWindow bounds decision-log volume: the same (run, host, outcome) within this
// window is logged once, with the number of suppressed repeats carried on the next logged row.
// An npm install resolving 500 packages against a blocked registry is one activity plus a
// count, not 500 rows.
const defaultDecisionWindow = 30 * time.Second

// ActivityAppender appends one activity to a run's transcript, allocating its seq. The proxy
// holds this one narrow function, never the store; the module wires it from the kernel store
// in Init, tests inject a recorder.
type ActivityAppender func(ctx context.Context, a *domain.Activity) error

// ProxyOptions configures NewProxy. The zero value logs decisions nowhere but slog and uses
// the default rate-limit window.
type ProxyOptions struct {
	Logger *slog.Logger
	Append ActivityAppender
	// Window overrides the decision-log rate-limit window. Zero means 30s.
	Window time.Duration
	// Now overrides the clock for tests.
	Now func() time.Time
}

// Proxy is the per-run-authenticated CONNECT and absolute-form HTTP proxy. Construct with
// NewProxy, Start it, Register runs, hand ProxyEnv to the env assembly (S19).
type Proxy struct {
	logger    *slog.Logger
	append    ActivityAppender
	window    time.Duration
	now       func() time.Time
	transport http.RoundTripper

	mu      sync.Mutex
	byToken map[string]*registration
	byRun   map[string]*registration
	recent  map[decisionKey]*decisionState

	srv *http.Server
	ln  net.Listener
}

// registration is one run's standing in the proxy: its credential and its compiled policy.
type registration struct {
	runID string
	token string
	mode  ports.NetworkMode
	allow hostMatcher
}

type decisionKey struct {
	runID   string
	host    string
	allowed bool
}

type decisionState struct {
	at         time.Time
	suppressed int
}

// NewProxy builds a proxy. It does not listen; call Start.
func NewProxy(opts ProxyOptions) *Proxy {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	window := opts.Window
	if window <= 0 {
		window = defaultDecisionWindow
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Proxy{
		logger: logger,
		append: opts.Append,
		window: window,
		now:    now,
		// Never http.DefaultTransport: it honors the *orchestrator's* HTTP_PROXY environment,
		// and a proxy that re-proxies through itself is a loop waiting to happen.
		transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		},
		byToken: map[string]*registration{},
		byRun:   map[string]*registration{},
		recent:  map[decisionKey]*decisionState{},
	}
}

// Start listens on addr ("0.0.0.0:7718"; ":0" in tests) and serves until Stop.
func (p *Proxy) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("egress proxy: listen on %s: %w", addr, err)
	}
	p.ln = ln
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(p.logger.Handler(), slog.LevelWarn),
	}
	go func() {
		if err := p.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			p.logger.Error("egress proxy stopped serving", slog.String("error", err.Error()))
		}
	}()
	p.logger.Info("egress proxy listening", slog.String("addr", ln.Addr().String()))
	return nil
}

// Stop closes the listener and every live connection, tunnels included — a run being torn
// down has no tunnels worth draining.
func (p *Proxy) Stop(ctx context.Context) error {
	if p.srv == nil {
		return nil
	}
	_ = ctx
	return p.srv.Close()
}

// Port is the port the proxy actually bound, which is what the relay must target. Zero before
// Start.
func (p *Proxy) Port() int {
	if p.ln == nil {
		return 0
	}
	if a, ok := p.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// Register makes a run's token valid and compiles its policy. gitHosts is the repo's git host
// set (for GitHub over https: github.com, codeload.github.com, objects.githubusercontent.com),
// allowed under every proxied policy so the workspace clone and push work under `none`.
// Registering a runID again replaces its previous registration (and revokes the old token).
// `open` runs are never registered — they ride the default bridge with no proxy env at all
// (S17's network selection); a registration with mode open would allow everything, honestly.
func (p *Proxy) Register(runID, token string, policy ports.NetworkPolicy, gitHosts ...string) {
	patterns := make([]string, 0, len(anthropicHosts)+len(gitHosts)+len(policy.Allow))
	patterns = append(patterns, anthropicHosts...)
	patterns = append(patterns, gitHosts...)
	if policy.Mode == ports.NetworkAllowlist {
		patterns = append(patterns, policy.Allow...)
	}
	reg := &registration{
		runID: runID,
		token: token,
		mode:  policy.Mode,
		allow: newHostMatcher(patterns),
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if old := p.byRun[runID]; old != nil {
		delete(p.byToken, old.token)
	}
	p.byRun[runID] = reg
	p.byToken[token] = reg
}

// Unregister revokes a run's token and drops its rate-limit state. Unknown runs are a no-op.
func (p *Proxy) Unregister(runID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if reg := p.byRun[runID]; reg != nil {
		delete(p.byToken, reg.token)
		delete(p.byRun, runID)
	}
	for k := range p.recent {
		if k.runID == runID {
			delete(p.recent, k)
		}
	}
}

// ProxyEnv is the environment a registered run's container needs to egress through the proxy:
// upper- and lowercase HTTP(S)_PROXY carrying the run credential, pointed at the relay
// (reachable by container name on lexicode-internal), plus a NO_PROXY that keeps loopback
// traffic — the container's own servers — direct. The env assembly (S19) merges this into
// SandboxSpec.Env for none/allowlist runs and omits it entirely for open runs. False when the
// run is not registered.
func (p *Proxy) ProxyEnv(runID string) (map[string]string, bool) {
	p.mu.Lock()
	reg := p.byRun[runID]
	p.mu.Unlock()
	if reg == nil {
		return nil, false
	}
	u := fmt.Sprintf("http://%s:%s@%s:%d", proxyUser, reg.token, relayContainerName, relayPort)
	return map[string]string{
		"HTTP_PROXY":  u,
		"HTTPS_PROXY": u,
		"http_proxy":  u,
		"https_proxy": u,
		"NO_PROXY":    "localhost,127.0.0.1",
		"no_proxy":    "localhost,127.0.0.1",
	}, true
}

// ServeHTTP authenticates, evaluates policy, logs the decision, then tunnels (CONNECT) or
// forwards (absolute-form). Order matters: an unauthenticated caller learns nothing but 407 —
// no policy evaluation, no dialing, no decision row.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reg := p.authenticate(r)
	if reg == nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="lexicode"`)
		http.Error(w, "lexicode egress proxy: missing or unknown run credential", http.StatusProxyAuthRequired)
		return
	}

	var host, port string
	switch {
	case r.Method == http.MethodConnect:
		host, port = splitAuthority(r.Host, "443")
	case r.URL.IsAbs():
		host, port = splitAuthority(r.URL.Host, "80")
	default:
		// Origin-form requests are what browsers send to servers, not to proxies; there is
		// nothing here to serve.
		http.Error(w, "lexicode egress proxy: expected CONNECT or an absolute-form request", http.StatusBadRequest)
		return
	}
	if host == "" {
		http.Error(w, "lexicode egress proxy: request has no target host", http.StatusBadRequest)
		return
	}

	allowed := reg.mode == ports.NetworkOpen || reg.allow.match(host)
	p.logDecision(r.Context(), reg, host, allowed)
	if !allowed {
		http.Error(w,
			fmt.Sprintf("lexicode network policy (%s): %s is not an allowed host", reg.mode, host),
			http.StatusForbidden)
		return
	}

	if r.Method == http.MethodConnect {
		p.tunnel(w, net.JoinHostPort(host, port))
		return
	}
	p.forward(w, r)
}

// authenticate resolves the request's Proxy-Authorization to a registration, or nil.
func (p *Proxy) authenticate(r *http.Request) *registration {
	auth := r.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return nil
	}
	user, token, ok := strings.Cut(string(raw), ":")
	if !ok || user != proxyUser || token == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byToken[token]
}

// tunnel answers a CONNECT: dial the (already-authorized) target ourselves, tell the client
// the tunnel is up, then copy bytes both ways until either side closes.
func (p *Proxy) tunnel(w http.ResponseWriter, target string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "lexicode egress proxy: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	upstream, err := net.DialTimeout("tcp", target, 15*time.Second)
	if err != nil {
		http.Error(w, "lexicode egress proxy: dialing "+target+": "+err.Error(), http.StatusBadGateway)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = upstream.Close()
		_ = client.Close()
		return
	}
	// buf.Reader may already hold client bytes read past the CONNECT header; drain through it,
	// not through the raw conn.
	go func() {
		_, _ = io.Copy(upstream, buf.Reader)
		_ = upstream.Close()
		_ = client.Close()
	}()
	_, _ = io.Copy(client, upstream)
	_ = upstream.Close()
	_ = client.Close()
}

// hopHeaders are stripped in both directions of a plain-HTTP forward (RFC 9110 §7.6.1), plus
// the proxy credential, which must never travel upstream.
var hopHeaders = []string{
	"Proxy-Authorization", "Proxy-Connection", "Proxy-Authenticate",
	"Connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
}

// forward serves an absolute-form plain-HTTP request (what http_proxy clients send for
// port-80 URLs, e.g. npm against an http registry): re-issue it upstream and copy the
// response back.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.RequestURI = "" // client-request field; must be empty on an outgoing request
	out.Header = r.Header.Clone()
	for _, name := range r.Header.Values("Connection") {
		out.Header.Del(strings.TrimSpace(name))
	}
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}

	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "lexicode egress proxy: forwarding to "+r.URL.Host+": "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	header := w.Header()
	for k, vv := range resp.Header {
		for _, v := range vv {
			header.Add(k, v)
		}
	}
	for _, h := range hopHeaders {
		header.Del(h)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// logDecision emits the D-10 activity: every allow/deny is a level-2 `system` row on the run,
// so "the install failed because the network policy blocked it" is a visible fact. Repeats of
// the same (host, outcome) within the window are counted, not logged; the count rides on the
// next logged row as suppressed_repeats.
func (p *Proxy) logDecision(ctx context.Context, reg *registration, host string, allowed bool) {
	key := decisionKey{runID: reg.runID, host: host, allowed: allowed}
	now := p.now()

	p.mu.Lock()
	suppressed := 0
	if st := p.recent[key]; st != nil && now.Sub(st.at) < p.window {
		st.suppressed++
		p.mu.Unlock()
		return
	} else if st != nil {
		suppressed = st.suppressed
	}
	p.recent[key] = &decisionState{at: now}
	p.mu.Unlock()

	outcome := "allowed"
	title := "Network: allowed " + host
	if !allowed {
		outcome = "denied"
		title = fmt.Sprintf("Network: blocked %s (policy: %s)", host, reg.mode)
	}
	p.logger.Debug("egress decision",
		slog.String("run", reg.runID), slog.String("host", host), slog.String("outcome", outcome))
	if p.append == nil {
		return
	}

	payload, _ := json.Marshal(map[string]any{
		"host":               host,
		"outcome":            outcome,
		"mode":               string(reg.mode),
		"suppressed_repeats": suppressed,
	})
	ok := allowed
	a := &domain.Activity{
		RunID:     reg.runID,
		Type:      domain.ActivitySystem,
		Level:     2, // verbose — contracts §3.2 places proxy decisions at level 2
		GroupKey:  "network",
		Title:     title,
		Payload:   payload,
		OK:        &ok,
		CreatedAt: domain.FormatTime(now),
	}
	// The request context ends when the client hangs up; the decision row must land anyway.
	if err := p.append(context.WithoutCancel(ctx), a); err != nil {
		p.logger.Warn("egress decision could not be recorded",
			slog.String("run", reg.runID), slog.String("host", host), slog.String("error", err.Error()))
	}
}

// hostMatcher is a compiled allow set: exact hosts plus `*.` wildcard suffixes. A wildcard
// entry `*.example.com` matches example.com itself and every subdomain — the friendlier
// reading, documented in the settings UI.
type hostMatcher struct {
	exact    map[string]struct{}
	suffixes []string // ".example.com" — also matched exactly without the leading dot
}

func newHostMatcher(patterns []string) hostMatcher {
	m := hostMatcher{exact: map[string]struct{}{}}
	for _, raw := range patterns {
		pat := normalizeHost(raw)
		if pat == "" {
			continue
		}
		if base, ok := strings.CutPrefix(pat, "*."); ok {
			if base == "" {
				continue // "*." alone would allow everything; refuse to compile it
			}
			m.exact[base] = struct{}{}
			m.suffixes = append(m.suffixes, "."+base)
			continue
		}
		m.exact[pat] = struct{}{}
	}
	return m
}

func (m hostMatcher) match(host string) bool {
	h := normalizeHost(host)
	if h == "" {
		return false
	}
	if _, ok := m.exact[h]; ok {
		return true
	}
	for _, suffix := range m.suffixes {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// normalizeHost lowercases and strips the FQDN trailing dot, so `API.Anthropic.Com.` cannot
// slip past an allowlist that spells the host plainly.
func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// splitAuthority splits "host:port" with a default port, tolerating a bare host.
func splitAuthority(authority, defaultPort string) (host, port string) {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return authority, defaultPort
	}
	if port == "" {
		port = defaultPort
	}
	return host, port
}
