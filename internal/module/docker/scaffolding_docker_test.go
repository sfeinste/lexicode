//go:build docker

package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// TestWorkspaceExcludesOrchestratorScaffolding is the security regression for the run token:
// `.lexicode/` (which holds the live per-run MCP token) and `.claude/` are materialized into
// the workspace, and the §10.5 failure-artifact path runs `git add -A && git commit && git
// push`. Prepare must leave the checkout unable to stage either one, so nothing the run does
// — the artifact push included — can carry the token into the user's repository.
func TestWorkspaceExcludesOrchestratorScaffolding(t *testing.T) {
	bare := fixtureRepo(t)
	sb := newTestSandbox(t, bare+":/fixture:ro")
	sink := newTestSink(t)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	spec := ports.SandboxSpec{
		RunID:     "run-" + domain.NewID(),
		ProjectID: "proj-scaffolding",
		Clone: ports.CloneSpec{
			URL:       "file:///fixture",
			Ref:       "main",
			Branch:    "agent/scaffolding",
			UserName:  "Agent Smith",
			UserEmail: "agent@lexicode.test",
		},
		Files: map[string][]byte{
			".claude/settings.json": []byte(`{"permissions":{}}`),
			".lexicode/prompt.md":   []byte("the prompt"),
			// The one that matters: a live run token.
			".lexicode/mcp.json": []byte(`{"mcpServers":{"lexicode":{"type":"http",` +
				`"url":"http://host.docker.internal:7798/mcp/super-secret-run-token"}}}`),
		},
		Network: ports.NetworkPolicy{Mode: ports.NetworkOpen},
	}

	inst, err := sb.Prepare(ctx, spec, sink)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer destroyQuietly(t, inst)

	// The files really are in the workspace — otherwise this test proves nothing.
	if code, out := execOutput(t, inst, "/bin/sh", "-c",
		"cat .lexicode/mcp.json"); code != 0 || !strings.Contains(out, "super-secret-run-token") {
		t.Fatalf("the run token was not materialized: exit %d, out %q", code, out)
	}

	// 1. git status never lists them.
	code, status := execOutput(t, inst, "git", "status", "--porcelain")
	if code != 0 {
		t.Fatalf("git status: exit %d, out %q", code, status)
	}
	for _, path := range []string{".lexicode", ".claude"} {
		if strings.Contains(status, path) {
			t.Errorf("git status --porcelain lists %s:\n%s", path, status)
		}
	}
	t.Logf("git status --porcelain (want empty):\n%s", status)

	// 2. The §10.5 artifact path itself: `git add -A && git commit` produces a tree with
	// neither path in it.
	code, out := execOutput(t, inst, "/bin/sh", "-c",
		"git add -A && git commit -q -m 'artifact: partial work' --allow-empty && "+
			"git ls-tree -r --name-only HEAD")
	if code != 0 {
		t.Fatalf("artifact commit: exit %d, out %q", code, out)
	}
	for _, path := range []string{".lexicode", ".claude"} {
		if strings.Contains(out, path) {
			t.Errorf("the artifact commit's tree contains %s:\n%s", path, out)
		}
	}
	t.Logf("git ls-tree -r --name-only HEAD:\n%s", out)

	// 3. And the token itself is nowhere in the commit's content.
	if code, out := execOutput(t, inst, "/bin/sh", "-c",
		"git grep -q super-secret-run-token HEAD; echo grep-exit=$?"); code != 0 ||
		!strings.Contains(out, "grep-exit=1") {
		t.Errorf("the run token is reachable from the commit: exit %d, out %q", code, out)
	}
}

// TestContainerCarriesResourceLimits proves the docker adapter applies ports.ResourceLimits to
// the container's HostConfig — the other half of the S19 builder filling them in.
func TestContainerCarriesResourceLimits(t *testing.T) {
	sb := newTestSandbox(t)
	sink := newTestSink(t)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	spec := ports.SandboxSpec{
		RunID:     "run-" + domain.NewID(),
		ProjectID: "proj-limits",
		Network:   ports.NetworkPolicy{Mode: ports.NetworkOpen},
		Limits: ports.ResourceLimits{
			CPUs:        2,
			MemoryBytes: 4 << 30,
			Pids:        512,
			WallClock:   30 * time.Minute,
		},
	}

	inst, err := sb.Prepare(ctx, spec, sink)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer destroyQuietly(t, inst)

	di, ok := inst.(*Instance)
	if !ok {
		t.Fatalf("instance is %T, want *Instance", inst)
	}
	info, err := sb.cli.ContainerInspect(ctx, di.containerID)
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	res := info.HostConfig.Resources
	t.Logf("HostConfig.Resources: NanoCPUs=%d Memory=%d PidsLimit=%v",
		res.NanoCPUs, res.Memory, res.PidsLimit)
	if res.NanoCPUs != 2e9 {
		t.Errorf("NanoCPUs = %d, want %d", res.NanoCPUs, int64(2e9))
	}
	if res.Memory != 4<<30 {
		t.Errorf("Memory = %d, want %d", res.Memory, int64(4<<30))
	}
	if res.PidsLimit == nil || *res.PidsLimit != 512 {
		t.Errorf("PidsLimit = %v, want 512", res.PidsLimit)
	}
}
