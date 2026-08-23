//go:build docker

// S21 reachability acceptance against a real daemon, over both network paths a container can
// take to the Lexicode MCP endpoint:
//
//   - policy `none` (the internal network with zero direct egress) reaches it through the
//     egress relay — both ways a real client gets there: dialing the relay by name
//     (origin-form) and through the container's proxy env (absolute-form). A revoked token
//     answers 404 over the same path.
//
//   - policy `open` — now the workspace default (migration 0005) — rides the default bridge
//     and dials host.docker.internal:<proxy port> directly, with no proxy env at all. This is
//     the path every run takes by default, so ask_human and approvals depend on it.
//
//     go test -tags docker -run TestS21 -timeout 30m ./internal/module/docker/
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
	mcpsvc "github.com/spruce/lexicode/internal/service/mcp"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
)

// mcpFixture is the store, the rows and the MCP server the two reachability tests share: one
// project, one agent, one running run, and a live token for it.
type mcpFixture struct {
	server *mcpsvc.Server
	// store is the same store the server reads: the elicitation acceptance polls it for the
	// pending row while the container's tool call is blocked.
	store   *store.Store
	project domain.Project
	agent   domain.Agent
	run     domain.Run
	token   string
}

func newMCPFixture(t *testing.T, runID string, logger *slog.Logger) mcpFixture {
	t.Helper()
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s21.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx0 := context.Background()
	if _, err := st.Migrate(ctx0); err != nil {
		t.Fatal(err)
	}
	now := domain.Now()
	owner := domain.User{
		ID: "user-s21", Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#888888", CreatedAt: now,
	}
	if err := st.Users().Create(ctx0, &owner); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{ID: "proj-s21", Key: "PAY", Name: "Payments", Color: "#3355ff",
		OwnerID: owner.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx0, &project); err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{
		ID: "agent-s21", ProjectID: project.ID, Name: "Dev", Role: "developer",
		Color: "#888888", RuntimeID: "claude-code", Model: "claude-sonnet-5", Effort: "medium",
		Autonomy: domain.AutonomyApproveEach,
		Permissions: domain.AgentPermissions{
			ReadFiles: true, EditFiles: true, RunCommands: true, PushBranches: true,
		},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Agents().Create(ctx0, &agent); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: runID, Seq: 1, ProjectID: project.ID, AgentID: agent.ID,
		State: domain.RunRunning, Autonomy: agent.Autonomy,
		Model: agent.Model, Effort: agent.Effort, Prompt: "prompt",
		RuntimeID: "claude-code", SandboxID: "docker", QueuedAt: now,
	}
	if err := st.Runs().Create(ctx0, &run); err != nil {
		t.Fatal(err)
	}

	mcpServer := mcpsvc.New(mcpsvc.Options{Store: st, Logger: logger})
	token, err := mcpServer.MintToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	return mcpFixture{server: mcpServer, store: st, project: project, agent: agent, run: run, token: token}
}

func TestS21MCPReachableUnderPolicyNone(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx0 := context.Background()

	fx := newMCPFixture(t, "run-s21-mcp", logger)
	mcpServer, run, token := fx.server, fx.run, fx.token

	// The egress proxy with the MCP handler mounted, exactly as serve.go mounts it.
	proxy := NewProxy(ProxyOptions{Logger: logger})
	proxy.SetMCPHandler(mcpServer.Handler())
	if err := proxy.Start("0.0.0.0:0"); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop(context.Background()) })
	sb.proxyPort = proxy.Port()
	t.Logf("egress proxy (with MCP endpoint) on host port %d", proxy.Port())

	proxy.Register(run.ID, "proxy-cred-s21", ports.NetworkPolicy{Mode: ports.NetworkNone},
		"github.com")

	// The S19-built spec, network policy none, real run token.
	none := "none"
	b := &runsvc.Builder{
		Forge: func(string) (ports.ForgeProvider, error) { return prepStubForge{}, nil },
		Credential: func(string) (ports.CredentialSource, error) {
			return prepStubSource{}, nil
		},
		ProxyEnv: proxy.ProxyEnv,
	}
	in := builderInput("")
	in.Repo.NetworkPolicy = &none
	in.Run = run
	in.Project = fx.project
	in.Agent = fx.agent
	in.RunToken = token
	prep, err := b.Build(ctx0, in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	inst, err := sb.Prepare(ctx, prep.Spec, newTestSink(t))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { destroyQuietly(t, inst) })

	// The materialized mcp.json points at the relay.
	rawMCP, err := inst.ReadFile(ctx, ".lexicode/mcp.json")
	if err != nil {
		t.Fatalf("ReadFile mcp.json: %v", err)
	}
	var mcpCfg struct {
		Servers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(rawMCP, &mcpCfg); err != nil {
		t.Fatalf("mcp.json is not valid JSON: %v\n%s", err, rawMCP)
	}
	url := mcpCfg.Servers["lexicode"].URL
	wantURL := "http://lexicode-egress:3128/mcp/" + token
	if url != wantURL {
		t.Fatalf("mcp.json url = %q, want %q", url, wantURL)
	}
	t.Logf("mcp.json url: %s", url)

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2025-03-26","capabilities":{},` +
		`"clientInfo":{"name":"curl-s21","version":"1"}}}`
	post := func(env map[string]string, target string) (int, string) {
		return execEnv(t, inst, env, "curl", "-sS", "-m", "60",
			"-X", "POST", "-H", "Content-Type: application/json",
			"-d", initBody, target)
	}
	noProxy := map[string]string{
		"http_proxy": "", "HTTP_PROXY": "", "https_proxy": "", "HTTPS_PROXY": "",
	}

	// 1. Direct dial of the relay by name (origin-form) — proxy env cleared.
	code, out := post(noProxy, url)
	t.Logf("origin-form initialize: exit %d, %s", code, out)
	if code != 0 || !strings.Contains(out, `"protocolVersion":"2025-03-26"`) ||
		!strings.Contains(out, `"name":"lexicode"`) {
		t.Fatalf("origin-form initialize failed: exit %d, %s", code, out)
	}

	// 2. Through the container's own proxy env (absolute-form via the same relay).
	code, out = post(nil, url)
	t.Logf("absolute-form initialize (via HTTP_PROXY): exit %d, %s", code, out)
	if code != 0 || !strings.Contains(out, `"protocolVersion":"2025-03-26"`) {
		t.Fatalf("absolute-form initialize failed: exit %d, %s", code, out)
	}

	// 3. tools/list over the same path names the five tools.
	code, out = execEnv(t, inst, noProxy, "curl", "-sS", "-m", "60",
		"-X", "POST", "-H", "Content-Type: application/json",
		"-d", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, url)
	if code != 0 {
		t.Fatalf("tools/list failed: exit %d, %s", code, out)
	}
	for _, tool := range []string{"ask_human", "set_step", "propose_wiki_page",
		"check_criterion", "request_approval"} {
		if !strings.Contains(out, fmt.Sprintf("%q", tool)) {
			t.Fatalf("tools/list lacks %s: %s", tool, out)
		}
	}
	t.Logf("tools/list ok (all five tools present)")

	// 4. Revocation: the same URL answers 404 once the run's token is revoked.
	mcpServer.RevokeRun(run.ID)
	code, out = execEnv(t, inst, noProxy, "curl", "-sS", "-m", "60",
		"-o", "/dev/null", "-w", "%{http_code}",
		"-X", "POST", "-H", "Content-Type: application/json", "-d", initBody, url)
	t.Logf("revoked token: exit %d, HTTP %s", code, out)
	if code != 0 || strings.TrimSpace(out) != "404" {
		t.Fatalf("revoked token = exit %d HTTP %s, want 404", code, out)
	}
}

// TestS21MCPReachableUnderPolicyOpen is the same claim for the path that is now the default
// (migration 0005): an `open` run sits on Docker's default bridge with no proxy env, and
// reaches the MCP endpoint at host.docker.internal:<proxy port> — the same listener the relay
// forwards to under `none`, approached from the other side. If this breaks, every ask_human
// and every approval breaks with it.
func TestS21MCPReachableUnderPolicyOpen(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx0 := context.Background()

	fx := newMCPFixture(t, "run-s21-mcp-open", logger)
	mcpServer, run, token := fx.server, fx.run, fx.token

	proxy := NewProxy(ProxyOptions{Logger: logger})
	proxy.SetMCPHandler(mcpServer.Handler())
	if err := proxy.Start("0.0.0.0:0"); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop(context.Background()) })
	sb.proxyPort = proxy.Port()
	t.Logf("egress proxy (with MCP endpoint) on host port %d", proxy.Port())

	// No proxy.Register: an `open` run is never registered with the egress proxy, and the
	// MCP endpoint is not gated on that — only on the run token.
	//
	// MCPBaseURL is what cmd/lexicode passes for the same reason: the port is config.
	b := &runsvc.Builder{
		Forge: func(string) (ports.ForgeProvider, error) { return prepStubForge{}, nil },
		Credential: func(string) (ports.CredentialSource, error) {
			return prepStubSource{}, nil
		},
		ProxyEnv:   proxy.ProxyEnv,
		MCPBaseURL: fmt.Sprintf("http://host.docker.internal:%d", proxy.Port()),
	}
	in := builderInput("")
	// The workspace default is what carries the policy: no per-repo override at all, exactly
	// as a fresh project looks after migration 0005.
	in.Repo.NetworkPolicy = nil
	in.Workspace.DefaultNetworkPolicy = "open"
	in.Run = run
	in.Project = fx.project
	in.Agent = fx.agent
	in.RunToken = token
	prep, err := b.Build(ctx0, in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if prep.Spec.Network.Mode != ports.NetworkOpen {
		t.Fatalf("resolved network mode = %q, want open", prep.Spec.Network.Mode)
	}
	// An open run gets no proxy env — the MCP call must succeed without one.
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		if v, ok := prep.Spec.Env[k]; ok {
			t.Errorf("open run carries %s=%q; open means no proxy", k, v)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	inst, err := sb.Prepare(ctx, prep.Spec, newTestSink(t))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { destroyQuietly(t, inst) })

	rawMCP, err := inst.ReadFile(ctx, ".lexicode/mcp.json")
	if err != nil {
		t.Fatalf("ReadFile mcp.json: %v", err)
	}
	var mcpCfg struct {
		Servers map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(rawMCP, &mcpCfg); err != nil {
		t.Fatalf("mcp.json is not valid JSON: %v\n%s", err, rawMCP)
	}
	url := mcpCfg.Servers["lexicode"].URL
	wantURL := fmt.Sprintf("http://host.docker.internal:%d/mcp/%s", proxy.Port(), token)
	if url != wantURL {
		t.Fatalf("mcp.json url = %q, want %q", url, wantURL)
	}
	t.Logf("mcp.json url: %s", url)

	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2025-03-26","capabilities":{},` +
		`"clientInfo":{"name":"curl-s21-open","version":"1"}}}`

	// 1. The handshake, straight off the bridge with the container's own env untouched.
	code, out := execOutput(t, inst, "curl", "-sS", "-m", "60",
		"-X", "POST", "-H", "Content-Type: application/json", "-d", initBody, url)
	t.Logf("open initialize: exit %d, %s", code, out)
	if code != 0 || !strings.Contains(out, `"protocolVersion":"2025-03-26"`) ||
		!strings.Contains(out, `"name":"lexicode"`) {
		t.Fatalf("initialize over the open path failed: exit %d, %s", code, out)
	}

	// 2. tools/list names the five tools — the surface ask_human and approvals ride on.
	code, out = execOutput(t, inst, "curl", "-sS", "-m", "60",
		"-X", "POST", "-H", "Content-Type: application/json",
		"-d", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, url)
	if code != 0 {
		t.Fatalf("tools/list failed: exit %d, %s", code, out)
	}
	for _, tool := range []string{"ask_human", "set_step", "propose_wiki_page",
		"check_criterion", "request_approval"} {
		if !strings.Contains(out, fmt.Sprintf("%q", tool)) {
			t.Fatalf("tools/list lacks %s: %s", tool, out)
		}
	}
	t.Logf("tools/list ok (all five tools present)")

	// 3. Revocation closes the open path too.
	mcpServer.RevokeRun(run.ID)
	code, out = execOutput(t, inst, "curl", "-sS", "-m", "60",
		"-o", "/dev/null", "-w", "%{http_code}",
		"-X", "POST", "-H", "Content-Type: application/json", "-d", initBody, url)
	t.Logf("revoked token: exit %d, HTTP %s", code, out)
	if code != 0 || strings.TrimSpace(out) != "404" {
		t.Fatalf("revoked token = exit %d HTTP %s, want 404", code, out)
	}
}
