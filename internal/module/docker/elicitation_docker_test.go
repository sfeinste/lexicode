//go:build docker

// The end-to-end acceptance for the bug that made ask_human unusable, against a real
// container and the real MCP endpoint:
//
//	go test -tags docker -run TestElicitationOutlives -timeout 30m ./internal/module/docker/
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
)

// askHumanClient is a stand-in for Claude Code's MCP client, written to fail in the same two
// ways the real one does, so that a fix which only satisfies the server proves nothing here.
//
//   - It abandons the call after MCP_TOOL_TIMEOUT (curl --max-time), the variable that moves
//     the client's limit off its 60-second default for an HTTP MCP server. With the variable
//     absent it falls back to that same 60 seconds — which is exactly the failure this test
//     reproduces, and exactly what an unfixed workspace environment produces.
//   - It abandons the call after idleWindow seconds without a byte (--speed-time /
//     --speed-limit), standing in for CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT. Only the server's
//     progress notifications keep that from firing.
//
// It asks for progress the way the spec says a client does — a progressToken in
// params._meta, and an Accept header offering text/event-stream — and prints a summary line
// the test parses.
const askHumanClient = `#!/bin/sh
set -u
URL="$1"
IDLE="$2"
LIMIT_MS="${MCP_TOOL_TIMEOUT:-60000}"
LIMIT=$((LIMIT_MS / 1000))
echo "client: max-time ${LIMIT}s (MCP_TOOL_TIMEOUT=${MCP_TOOL_TIMEOUT:-unset}), idle window ${IDLE}s"
BODY='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"progressToken":"pt-1"},"name":"ask_human","arguments":{"questions":[{"question":"Which storage should the idempotency keys use?","header":"Storage","options":[{"label":"Postgres","description":"Same database as the charges table"},{"label":"Redis","description":"Another moving part"}],"multiSelect":false}]}}}'
curl -sS -N --max-time "$LIMIT" --speed-time "$IDLE" --speed-limit 1 \
  -X POST "$URL" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d "$BODY" >/tmp/stream.txt
echo "curl-exit: $?"
echo "progress-notifications: $(grep -c 'notifications/progress' /tmp/stream.txt)"
echo "stream:"
cat /tmp/stream.txt
`

// TestElicitationOutlivesTheClientsSixtySecondTimeout is the acceptance for the whole fix, in
// the shape of the production failure it comes from. Run #10 on the user's own instance:
// ask_human created the elicitation, two tool_progress heartbeats went by, and at +63 seconds
// the agent's tool call came back an error and the agent gave up — "no one available to
// answer" — while the elicitation row sat pending and the server was prepared to wait an
// hour. The 60 seconds were the client's, and nothing the server believed about its own
// ceiling mattered.
//
// Everything here is real except the agent: a real container on the default bridge, the real
// SandboxSpec from the S19 builder (so MCP_TOOL_TIMEOUT is the value production computes, not
// one the test made up), the real MCP endpoint over the real network path, the real
// notification cadence, and a human who takes well over two minutes to answer.
func TestElicitationOutlivesTheClientsSixtySecondTimeout(t *testing.T) {
	// How long the "human" takes. Comfortably past the 60 seconds that used to be fatal,
	// and past enough 20-second progress intervals to prove the stream stayed alive.
	const answerAfter = 75 * time.Second
	// The stand-in client's idle window, compressed from the real five minutes so that a
	// server which sent no progress at all would fail this test rather than pass it by
	// simply being fast enough.
	const idleWindow = 25

	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git:ro")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx0 := context.Background()

	fx := newMCPFixture(t, "run-elicitation-slow", logger)

	proxy := NewProxy(ProxyOptions{Logger: logger})
	proxy.SetMCPHandler(fx.server.Handler())
	if err := proxy.Start("0.0.0.0:0"); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop(context.Background()) })
	sb.proxyPort = proxy.Port()

	b := &runsvc.Builder{
		Forge:      func(string) (ports.ForgeProvider, error) { return prepStubForge{}, nil },
		Credential: func(string) (ports.CredentialSource, error) { return prepStubSource{}, nil },
		ProxyEnv:   proxy.ProxyEnv,
		MCPBaseURL: fmt.Sprintf("http://host.docker.internal:%d", proxy.Port()),
	}
	in := builderInput("")
	in.Repo.NetworkPolicy = nil
	in.Workspace.DefaultNetworkPolicy = "open"
	in.Run = fx.run
	in.Project = fx.project
	in.Agent = fx.agent
	in.RunToken = fx.token
	prep, err := b.Build(ctx0, in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The client-side half of the fix, as production computes it. Everything below depends
	// on this: the stand-in reads the variable out of its own environment.
	raw := prep.Spec.Env["MCP_TOOL_TIMEOUT"]
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("MCP_TOOL_TIMEOUT = %q, want milliseconds: %v", raw, err)
	}
	if ms <= 60000 {
		t.Fatalf("MCP_TOOL_TIMEOUT = %dms; at or under 60000 Claude Code keeps the "+
			"60-second per-request timer for HTTP MCP servers and the question dies", ms)
	}
	t.Logf("MCP_TOOL_TIMEOUT from the S19 builder: %dms", ms)

	prep.Spec.Files[".lexicode/ask-human-client.sh"] = []byte(askHumanClient)
	prep.Spec.SetupScript = "chmod +x /workspace/.lexicode/ask-human-client.sh"

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
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(rawMCP, &mcpCfg); err != nil {
		t.Fatalf("mcp.json: %v\n%s", err, rawMCP)
	}
	url := mcpCfg.Servers["lexicode"].URL

	type callResult struct {
		code int
		out  string
	}
	done := make(chan callResult, 1)
	started := time.Now()
	go func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer ccancel()
		code, out := execOutputCtx(cctx, t, inst,
			"/workspace/.lexicode/ask-human-client.sh", url, strconv.Itoa(idleWindow))
		done <- callResult{code, out}
	}()

	// The question reaches the orchestrator.
	var elicitationID string
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) && elicitationID == "" {
		pending, err := fx.store.Elicitations().PendingForRun(ctx0, fx.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) == 1 {
			elicitationID = pending[0].ID
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if elicitationID == "" {
		t.Fatal("no elicitation appeared; the container never reached the MCP endpoint")
	}
	t.Logf("elicitation %s pending after %s", elicitationID, time.Since(started).Round(time.Second))

	// Nobody answers for well over a minute — the window in which every question used to
	// die, and in which S24's escalation notification is only just reaching a human.
	select {
	case r := <-done:
		t.Fatalf("the tool call ended after %s, before anyone answered:\nexit %d\n%s",
			time.Since(started).Round(time.Second), r.code, r.out)
	case <-time.After(answerAfter):
	}

	el, err := fx.store.Elicitations().ByID(ctx0, elicitationID)
	if err != nil {
		t.Fatal(err)
	}
	if el.State != domain.ElicitationPending {
		t.Fatalf("elicitation state after %s = %s, want pending",
			answerAfter, el.State)
	}

	if _, err := fx.server.Resolve(ctx0, elicitationID, ports.Response{
		Answers: map[string][]string{
			"Which storage should the idempotency keys use?": {"Postgres"},
		},
	}, nil); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var res callResult
	select {
	case res = <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("the answer never reached the blocked tool call")
	}
	t.Logf("tool call returned after %s:\n%s", time.Since(started).Round(time.Second), res.out)

	if res.code != 0 || !strings.Contains(res.out, "curl-exit: 0") {
		t.Fatalf("the client abandoned the call: exit %d\n%s", res.code, res.out)
	}
	// The answer came back as the tool's own result, which is what lets the agent resume
	// exactly where it asked.
	if !strings.Contains(res.out, "Postgres") {
		t.Fatalf("the answer did not come back as the tool result:\n%s", res.out)
	}
	// And it stayed alive on progress notifications, not on luck: at a 20-second cadence a
	// 75-second wait produces at least three.
	notes := progressCount(t, res.out)
	if notes < 3 {
		t.Fatalf("progress notifications = %d over %s, want at least 3; an idle client "+
			"would have abandoned the call\n%s", notes, answerAfter, res.out)
	}
	t.Logf("progress notifications observed inside the container: %d", notes)
}

// progressCount reads the stand-in's "progress-notifications: N" summary line.
func progressCount(t *testing.T, out string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "progress-notifications:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				t.Fatalf("unreadable progress count %q: %v", rest, err)
			}
			return n
		}
	}
	t.Fatalf("the client printed no progress count:\n%s", out)
	return 0
}
