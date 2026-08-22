package docker

// Unit tests for the S18 egress proxy: policy evaluation, per-run auth (the open-proxy
// probe), absolute-form forwarding, CONNECT tunneling, decision activities, and the
// repeat-decision rate limit. No Docker daemon involved — the proxy is plain TCP/HTTP.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// activityRecorder captures what the proxy would append to the run transcript.
type activityRecorder struct {
	mu   sync.Mutex
	rows []domain.Activity
}

func (r *activityRecorder) append(_ context.Context, a *domain.Activity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, *a)
	return nil
}

func (r *activityRecorder) all() []domain.Activity {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.Activity(nil), r.rows...)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startTestProxy runs a proxy on a loopback ephemeral port and tears it down with the test.
func startTestProxy(t *testing.T, opts ProxyOptions) *Proxy {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = quietLogger()
	}
	p := NewProxy(opts)
	if err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	return p
}

func proxyURL(t *testing.T, p *Proxy, token string) *url.URL {
	t.Helper()
	u, err := url.Parse(fmt.Sprintf("http://run:%s@127.0.0.1:%d", token, p.Port()))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestProxyPolicyEvaluation(t *testing.T) {
	gitHosts := []string{"github.com", "codeload.github.com", "objects.githubusercontent.com"}

	cases := []struct {
		name    string
		policy  ports.NetworkPolicy
		host    string
		allowed bool
	}{
		// none: only what the agent itself needs — the Anthropic API and the git host.
		{"none allows the anthropic api", ports.NetworkPolicy{Mode: ports.NetworkNone}, "api.anthropic.com", true},
		{"none allows the git host", ports.NetworkPolicy{Mode: ports.NetworkNone}, "codeload.github.com", true},
		{"none denies a registry", ports.NetworkPolicy{Mode: ports.NetworkNone}, "registry.npmjs.org", false},
		{"none is case- and fqdn-insensitive", ports.NetworkPolicy{Mode: ports.NetworkNone}, "API.Anthropic.Com.", true},
		{"none ignores the allow list", ports.NetworkPolicy{Mode: ports.NetworkNone, Allow: []string{"registry.npmjs.org"}}, "registry.npmjs.org", false},

		// allowlist: none plus the repo's domains, wildcards included.
		{"allowlist allows a listed host", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"registry.npmjs.org"}}, "registry.npmjs.org", true},
		{"allowlist still allows anthropic", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"registry.npmjs.org"}}, "api.anthropic.com", true},
		{"allowlist denies the unlisted", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"registry.npmjs.org"}}, "evil.example.com", false},
		{"wildcard matches a subdomain", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"*.npmjs.org"}}, "registry.npmjs.org", true},
		{"wildcard matches a deep subdomain", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"*.npmjs.org"}}, "a.b.npmjs.org", true},
		{"wildcard matches the apex", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"*.npmjs.org"}}, "npmjs.org", true},
		{"wildcard is a label boundary, not a substring", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"*.npmjs.org"}}, "evilnpmjs.org", false},
		{"a plain entry does not match subdomains", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"npmjs.org"}}, "registry.npmjs.org", false},
		{"a bare star compiles to nothing", ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"*."}}, "anything.example", false},

		// open: everything (open runs are never registered in production; the proxy is
		// honest if one is).
		{"open allows anything", ports.NetworkPolicy{Mode: ports.NetworkOpen}, "anything.example", true},
	}

	p := NewProxy(ProxyOptions{Logger: quietLogger()})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p.Register("run-1", "tok-1", tc.policy, gitHosts...)
			reg := p.byToken["tok-1"]
			got := reg.mode == ports.NetworkOpen || reg.allow.match(tc.host)
			if got != tc.allowed {
				t.Errorf("mode %s, host %q: allowed = %v, want %v", tc.policy.Mode, tc.host, got, tc.allowed)
			}
		})
	}
}

// TestProxyRefusesWithoutCredential is the open-proxy probe: a client on the host (or
// anywhere) that connects without a registered run token gets 407 for both request forms, and
// nothing is dialed or logged on any run.
func TestProxyRefusesWithoutCredential(t *testing.T) {
	rec := &activityRecorder{}
	p := startTestProxy(t, ProxyOptions{Append: rec.append})
	p.Register("run-1", "good-token", ports.NetworkPolicy{Mode: ports.NetworkNone})

	// Absolute-form, no credential at all.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{
		Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", p.Port()),
	})}}
	resp, err := client.Get("http://api.anthropic.com/")
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Errorf("no-credential status = %d, want 407", resp.StatusCode)
	}

	// CONNECT with a wrong token.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cred := base64.StdEncoding.EncodeToString([]byte("run:wrong-token"))
	fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", cred)
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "407") {
		t.Errorf("bad-token CONNECT status line = %q, want 407", status)
	}

	if rows := rec.all(); len(rows) != 0 {
		t.Errorf("unauthenticated requests logged %d activities, want 0 (no run to attribute them to)", len(rows))
	}
}

func TestProxyForwardsAbsoluteFormHTTP(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Proxy-Authorization"); got != "" {
			t.Errorf("run credential leaked upstream: %q", got)
		}
		w.Header().Set("X-Backend", "yes")
		fmt.Fprint(w, "hello from origin")
	}))
	defer backend.Close()

	rec := &activityRecorder{}
	p := startTestProxy(t, ProxyOptions{Append: rec.append})
	p.Register("run-1", "tok-1", ports.NetworkPolicy{
		Mode: ports.NetworkAllowlist, Allow: []string{"127.0.0.1"},
	})

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL(t, p, "tok-1"))}}
	resp, err := client.Get(backend.URL + "/thing")
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello from origin" {
		t.Errorf("proxied GET = %d %q, want 200 %q", resp.StatusCode, body, "hello from origin")
	}
	if resp.Header.Get("X-Backend") != "yes" {
		t.Error("origin response headers did not come back through the proxy")
	}

	rows := rec.all()
	if len(rows) != 1 || !strings.Contains(rows[0].Title, "allowed 127.0.0.1") {
		t.Fatalf("activities = %+v, want one allow row for 127.0.0.1", rows)
	}
}

func TestProxyTunnelsConnectToAllowedHost(t *testing.T) {
	// A raw TCP echo listener stands in for any TLS origin: CONNECT is protocol-blind.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()

	rec := &activityRecorder{}
	p := startTestProxy(t, ProxyOptions{Append: rec.append})
	p.Register("run-1", "tok-1", ports.NetworkPolicy{
		Mode: ports.NetworkAllowlist, Allow: []string{"127.0.0.1"},
	})

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cred := base64.StdEncoding.EncodeToString([]byte("run:tok-1"))
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n",
		echo.Addr(), echo.Addr(), cred)

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT status line = %q, want 200 Connection Established", status)
	}
	for { // headers end at the blank line
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}

	if _, err := conn.Write([]byte("ping through the tunnel")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("ping through the tunnel"))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping through the tunnel" {
		t.Errorf("tunnel echoed %q", buf)
	}

	rows := rec.all()
	if len(rows) != 1 || !strings.Contains(rows[0].Title, "allowed 127.0.0.1") {
		t.Fatalf("activities = %+v, want one allow row for 127.0.0.1", rows)
	}
}

func TestProxyDenialWritesActivity(t *testing.T) {
	rec := &activityRecorder{}
	p := startTestProxy(t, ProxyOptions{Append: rec.append})
	p.Register("run-7", "tok-7", ports.NetworkPolicy{Mode: ports.NetworkNone})

	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL(t, p, "tok-7"))}}
	resp, err := client.Get("http://registry.npmjs.org/left-pad")
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(string(body), "network policy") {
		t.Errorf("denial body %q does not name the network policy", body)
	}

	rows := rec.all()
	if len(rows) != 1 {
		t.Fatalf("activities = %d, want exactly 1", len(rows))
	}
	a := rows[0]
	if a.RunID != "run-7" || a.Type != domain.ActivitySystem || a.Level != 2 {
		t.Errorf("activity = run %q type %q level %d, want run-7/system/2", a.RunID, a.Type, a.Level)
	}
	var payload struct {
		Host    string `json:"host"`
		Outcome string `json:"outcome"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(a.Payload, &payload); err != nil {
		t.Fatalf("payload %s: %v", a.Payload, err)
	}
	if payload.Host != "registry.npmjs.org" || payload.Outcome != "denied" || payload.Mode != "none" {
		t.Errorf("payload = %+v, want registry.npmjs.org/denied/none", payload)
	}
	if a.OK == nil || *a.OK {
		t.Error("denial activity OK should be false")
	}
}

func TestProxyRateLimitsRepeatedDecisions(t *testing.T) {
	rec := &activityRecorder{}
	clock := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	p := startTestProxy(t, ProxyOptions{Append: rec.append, Window: 30 * time.Second, Now: now})
	p.Register("run-1", "tok-1", ports.NetworkPolicy{Mode: ports.NetworkNone})

	client := &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyURL(proxyURL(t, p, "tok-1")),
		DisableKeepAlives: true,
	}}
	deny := func() {
		resp, err := client.Get("http://registry.npmjs.org/pkg")
		if err != nil {
			t.Fatalf("proxied GET: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	}

	// An npm-style burst: 5 identical denials inside the window log exactly once.
	for range 5 {
		deny()
	}
	if got := len(rec.all()); got != 1 {
		t.Fatalf("after burst: %d activities, want 1", got)
	}

	// A different host is a different decision — logged immediately, not suppressed.
	respOther, err := client.Get("http://other.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	_ = respOther.Body.Close()
	if got := len(rec.all()); got != 2 {
		t.Fatalf("different host: %d activities, want 2", got)
	}

	// Past the window the same decision logs again, carrying the suppressed count.
	clock = clock.Add(31 * time.Second)
	deny()
	rows := rec.all()
	if len(rows) != 3 {
		t.Fatalf("after window: %d activities, want 3", len(rows))
	}
	var payload struct {
		SuppressedRepeats int `json:"suppressed_repeats"`
	}
	if err := json.Unmarshal(rows[2].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SuppressedRepeats != 4 {
		t.Errorf("suppressed_repeats = %d, want 4 (five denials, one logged)", payload.SuppressedRepeats)
	}
}

// TestProxyEnv checks the env helper yields the relay-pointing credentials S19 will merge
// into the container environment, and nothing for unregistered (open) runs.
func TestProxyEnv(t *testing.T) {
	p := NewProxy(ProxyOptions{Logger: quietLogger()})
	p.Register("run-1", "tok-1", ports.NetworkPolicy{Mode: ports.NetworkNone})

	env, ok := p.ProxyEnv("run-1")
	if !ok {
		t.Fatal("ProxyEnv: run-1 should be registered")
	}
	want := fmt.Sprintf("http://run:tok-1@%s:%d", relayContainerName, relayPort)
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if env["NO_PROXY"] != "localhost,127.0.0.1" {
		t.Errorf("NO_PROXY = %q", env["NO_PROXY"])
	}

	if _, ok := p.ProxyEnv("run-open"); ok {
		t.Error("ProxyEnv for an unregistered run must report false")
	}

	p.Unregister("run-1")
	if _, ok := p.ProxyEnv("run-1"); ok {
		t.Error("ProxyEnv after Unregister must report false")
	}
	if p.byToken["tok-1"] != nil {
		t.Error("Unregister must revoke the token")
	}
}
