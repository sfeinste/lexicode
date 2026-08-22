//go:build docker

// Real-daemon acceptance for story S18: a container on the internal network (zero direct
// egress) reaches the outside only through the relay + proxy, and the per-run policy decides
// per host. Needs a Docker daemon and real network access (api.anthropic.com,
// registry.npmjs.org).
//
//	go test -tags docker -run TestS18 -timeout 30m ./internal/module/docker/
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// execEnv runs argv in the instance with extra env and returns (exitCode, combined output).
func execEnv(t *testing.T, inst ports.Instance, env map[string]string, argv ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := inst.Exec(ctx, argv, ports.ExecOpts{Env: env})
	if err != nil {
		t.Fatalf("Exec %v: %v", argv, err)
	}
	if err := st.Stdin.Close(); err != nil {
		t.Fatalf("closing stdin: %v", err)
	}
	var mu sync.Mutex
	var buf strings.Builder
	var wg sync.WaitGroup
	for _, r := range []io.Reader{st.Stdout, st.Stderr} {
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			b, _ := io.ReadAll(r)
			mu.Lock()
			buf.Write(b)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	code, err := st.Wait()
	if err != nil {
		t.Fatalf("Wait for %v: %v", argv, err)
	}
	return code, buf.String()
}

// TestS18NetworkPolicyEndToEnd is the story's acceptance: under `none`,
// curl https://registry.npmjs.org fails and the denial is a visible run activity; under
// `allowlist` with the host added, it succeeds; the Anthropic API is reachable under both.
func TestS18NetworkPolicyEndToEnd(t *testing.T) {
	sb := newTestSandbox(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// The proxy, exactly as the module runs it, on an ephemeral port so parallel checkouts
	// cannot collide. The relay is recreated per target port, so the ephemeral port is fine.
	rec := &activityRecorder{}
	proxy := NewProxy(ProxyOptions{Logger: logger, Append: rec.append})
	if err := proxy.Start("0.0.0.0:0"); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop(context.Background()) })
	sb.proxyPort = proxy.Port()
	t.Logf("egress proxy on host port %d", proxy.Port())

	// Two registered runs against one container: registration is by token, and the env
	// carries the token, so one container can probe both policies.
	gitHosts := []string{"github.com", "codeload.github.com", "objects.githubusercontent.com"}
	proxy.Register("run-s18-none", "tok-s18-none",
		ports.NetworkPolicy{Mode: ports.NetworkNone}, gitHosts...)
	proxy.Register("run-s18-allow", "tok-s18-allow",
		ports.NetworkPolicy{Mode: ports.NetworkAllowlist, Allow: []string{"registry.npmjs.org"}},
		gitHosts...)

	envNone, ok := proxy.ProxyEnv("run-s18-none")
	if !ok {
		t.Fatal("ProxyEnv(run-s18-none)")
	}
	envAllow, ok := proxy.ProxyEnv("run-s18-allow")
	if !ok {
		t.Fatal("ProxyEnv(run-s18-allow)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	sink := newTestSink(t)
	inst, err := sb.Prepare(ctx, ports.SandboxSpec{
		RunID:     "run-s18-none",
		ProjectID: "proj-s18",
		Network:   ports.NetworkPolicy{Mode: ports.NetworkNone},
	}, sink)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { destroyQuietly(t, inst) })

	// The relay exists, runs, and targets this proxy's port.
	args := filters.NewArgs(filters.Arg("name", relayContainerName))
	relays, err := sb.cli.ContainerList(ctx, container.ListOptions{Filters: args})
	if err != nil {
		t.Fatal(err)
	}
	if len(relays) != 1 || relays[0].Labels[labelEgressPort] != fmt.Sprint(proxy.Port()) {
		t.Fatalf("relay containers = %+v, want one running relay targeting port %d", relays, proxy.Port())
	}

	curl := func(env map[string]string, url string) (int, string) {
		return execEnv(t, inst, env, "curl", "-sS", "-o", "/dev/null", "-m", "60",
			"-w", "%{http_code}", url)
	}

	// Sanity: with no proxy env at all, the internal network has zero egress.
	if code, out := curl(nil, "https://registry.npmjs.org/"); code == 0 {
		t.Fatalf("direct egress from the internal network succeeded (%s); the network is not internal", out)
	}

	// 1. Under `none`, the npm registry is blocked (curl exit 56: CONNECT refused with 403).
	code, out := curl(envNone, "https://registry.npmjs.org/")
	t.Logf("none  → registry.npmjs.org: exit %d, %s", code, out)
	if code == 0 {
		t.Errorf("under policy none, curl https://registry.npmjs.org succeeded; it must be blocked")
	}

	// …and the denial is a visible level-2 system activity on the run.
	foundDenial := false
	for _, a := range rec.all() {
		if a.RunID != "run-s18-none" || a.Level != 2 || a.Type != domain.ActivitySystem {
			continue
		}
		var p struct {
			Host, Outcome string
		}
		if err := json.Unmarshal(a.Payload, &p); err == nil &&
			p.Host == "registry.npmjs.org" && p.Outcome == "denied" {
			foundDenial = true
			t.Logf("denial activity: %s payload=%s", a.Title, a.Payload)
		}
	}
	if !foundDenial {
		t.Errorf("no denial activity for registry.npmjs.org on run-s18-none; activities: %+v", rec.all())
	}

	// 2. Under `allowlist` with the host added, it succeeds (any HTTP status is success —
	// the policy question is reachability).
	code, out = curl(envAllow, "https://registry.npmjs.org/")
	t.Logf("allow → registry.npmjs.org: exit %d, http %s", code, out)
	if code != 0 {
		t.Errorf("under allowlist with registry.npmjs.org added, curl failed with exit %d (%s)", code, out)
	}

	// 3. The Anthropic API is reachable under both policies (4xx is fine; reachable is the claim).
	code, out = curl(envNone, "https://api.anthropic.com/")
	t.Logf("none  → api.anthropic.com: exit %d, http %s", code, out)
	if code != 0 {
		t.Errorf("under none, curl https://api.anthropic.com failed with exit %d (%s)", code, out)
	}
	code, out = curl(envAllow, "https://api.anthropic.com/")
	t.Logf("allow → api.anthropic.com: exit %d, http %s", code, out)
	if code != 0 {
		t.Errorf("under allowlist, curl https://api.anthropic.com failed with exit %d (%s)", code, out)
	}
}
