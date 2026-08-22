//go:build docker

// S21 reachability acceptance against a real daemon: a container prepared from the S19-built
// spec, under network policy `none` (the internal network with zero direct egress), reaches
// the Lexicode MCP endpoint and completes the initialize handshake — both ways a real client
// gets there: dialing the relay by name (origin-form), and through the container's proxy env
// (absolute-form). A revoked token answers 404 over the same path.
//
//	go test -tags docker -run TestS21 -timeout 30m ./internal/module/docker/
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

func TestS21MCPReachableUnderPolicyNone(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// The store and the MCP server, as cmd/lexicode wires them (no bus needed here — this
	// test is about the network path).
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
		ID: "run-s21-mcp", Seq: 1, ProjectID: project.ID, AgentID: agent.ID,
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
	in.Project = project
	in.Agent = agent
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
