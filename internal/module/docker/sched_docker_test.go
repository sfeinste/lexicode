//go:build docker

// S22 docker smoke: a real run through the real scheduler against a real container — the
// S19 builder produces the spec, the docker sandbox provisions (clone from a bind-mounted
// local bare repository, no network), the claudecode adapter launches a scripted fake
// `claude` (the S20 smoke pattern), the agent commits and pushes its work, and the run
// completes with the provisioning checklist on record.
package docker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	kstore "github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/module/claudecode"
	runsvc "github.com/spruce/lexicode/internal/service/runs"
)

// schedFakeClaude is the in-container stand-in agent: consume the prompt from stdin the way
// the real CLI would, do real work in the real workspace — an empty commit on the run
// branch — then emit a well-formed stream-json session.
//
// It does NOT push. It cannot: the clone step points `origin` at a tokenless URL before the
// agent starts. Committing is the whole of the agent's contribution, and the branch reaching
// the remote — asserted at the bottom of this test — is the orchestrator's teardown push
// doing its job.
const schedFakeClaude = `#!/bin/sh
head -n 1 >/dev/null
git commit --allow-empty -m "smoke: agent work" >/dev/null 2>&1
cat <<'EOF'
{"type":"system","subtype":"init","cwd":"/workspace","session_id":"smoke","tools":["Bash"],"model":"fake"}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"committing the change"}],"usage":{"input_tokens":5,"output_tokens":9}}}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git commit --allow-empty -m 'smoke: agent work'"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"committed"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"smoke complete","total_cost_usd":0.005,"usage":{"input_tokens":5,"output_tokens":9}}
EOF
`

type schedStubForge struct{ ports.ForgeProvider }

func (schedStubForge) ID() string { return "github" }

func (schedStubForge) CloneURL(context.Context, ports.Creds, domain.RepoRef) (string, error) {
	return "file:///fixtures/fixture.git", nil
}

type schedStubSource struct{}

func (schedStubSource) ID() string { return "oauth-token" }
func (schedStubSource) AgentEnv(context.Context, string) (map[string]string, error) {
	return map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-docker-test"}, nil
}
func (schedStubSource) Health(context.Context) error { return nil }

// builderAdapter satisfies sched.SpecBuilder over the real S19 Builder — the same adapter
// cmd/lexicode wires.
type builderAdapter struct{ b *runsvc.Builder }

func (a builderAdapter) Build(ctx context.Context, in sched.SpecInput) (sched.SpecResult, error) {
	prep, err := a.b.Build(ctx, runsvc.PrepInput{
		Workspace: in.Workspace, Project: in.Project, Repo: in.Repo,
		Agent: in.Agent, Ticket: in.Ticket, Run: in.Run, RunToken: in.RunToken,
	})
	if err != nil {
		return sched.SpecResult{}, err
	}
	return sched.SpecResult{Spec: prep.Spec, Branch: prep.Branch, SecretValues: prep.SecretValues}, nil
}

func TestSchedulerDockerSmoke(t *testing.T) {
	bare := fixtureRepo(t)
	// Read-write: the agent pushes its branch back into the local bare repo.
	sb := newTestSandbox(t, bare+":/fixtures/fixture.git")

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := kstore.Open(kstore.Options{Path: filepath.Join(t.TempDir(), "smoke.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx0 := context.Background()
	if _, err := st.Migrate(ctx0); err != nil {
		t.Fatal(err)
	}
	b := bus.New(bus.Options{Store: st, Logger: logger})
	if err := b.Start(ctx0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	auditW := audit.New(audit.Options{Store: st, Logger: logger})

	// Seed: owner, project, connected repo (open network — the S18 egress tests own the
	// proxied paths), agent whose runtime is the real claudecode adapter pointed at the
	// scripted binary the setup script materialises.
	now := domain.Now()
	owner := domain.User{ID: domain.NewID(), Email: "smoke@example.com", DisplayName: "S",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#123456", CreatedAt: now}
	if err := st.Users().Create(ctx0, &owner); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments",
		OwnerID: owner.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx0, &project); err != nil {
		t.Fatal(err)
	}
	open := "open"
	setup := "cat > /workspace/.lexicode/fake-claude.sh <<'SCRIPT'\n" + schedFakeClaude +
		"SCRIPT\nchmod +x /workspace/.lexicode/fake-claude.sh"
	repo := domain.Repo{ProjectID: project.ID, Provider: "github", Owner: "acme",
		Name: "payments", NetworkPolicy: &open, SetupScript: setup,
		CreatedAt: now, UpdatedAt: now}
	if err := st.Repos().Create(ctx0, &repo); err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: project.ID, Name: "Dev", Color: "#888888",
		RuntimeID: "claude-code", Model: "fake-model", Effort: "medium",
		Autonomy: domain.AutonomyAuto,
		Permissions: domain.AgentPermissions{ReadFiles: true, EditFiles: true,
			RunCommands: true, PushBranches: true},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 600, MaxSteps: 100, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Agents().Create(ctx0, &agent); err != nil {
		t.Fatal(err)
	}
	seq, err := st.Projects().AllocateTicketSeq(ctx0, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	ticket := domain.Ticket{
		ID: domain.NewID(), ProjectID: project.ID, Seq: seq,
		Key: fmt.Sprintf("PAY-%d", seq), Title: "Add idempotency keys",
		ColumnID: seedColumn(t, st, project.ID), Position: domain.PositionGap,
		Priority: domain.PriorityNone, Origin: domain.OriginHuman,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Tickets().Create(ctx0, &ticket); err != nil {
		t.Fatal(err)
	}

	builder := &runsvc.Builder{
		Forge:      func(string) (ports.ForgeProvider, error) { return schedStubForge{}, nil },
		Credential: func(string) (ports.CredentialSource, error) { return schedStubSource{}, nil },
	}
	rt := claudecode.NewRuntime(claudecode.Options{Bin: "/workspace/.lexicode/fake-claude.sh"})

	scheduler := sched.New(sched.Options{
		Store: st, Bus: b, Audit: auditW, Logger: logger,
		Sandbox:       func(string) (ports.Sandbox, error) { return sb, nil },
		Runtime:       func(string) (ports.AgentRuntime, error) { return rt, nil },
		Specs:         builderAdapter{b: builder},
		SandboxID:     "docker",
		AdmitInterval: 100 * time.Millisecond,
	})
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scheduler.Stop(context.Background()) })

	run, err := scheduler.Enqueue(ctx0, sched.RunRequest{
		ProjectID: project.ID, AgentID: agent.ID, TicketID: ticket.ID,
		Reason: "docker smoke", PromptOverride: "Push an empty smoke commit.",
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(15 * time.Minute) // first run may build the base image
	for {
		r, err := st.Runs().ByID(ctx0, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.State.Terminal() {
			if r.State != domain.RunCompleted {
				t.Fatalf("run ended %s (%s): %s", r.State, r.StateReason, r.ErrorMessage)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run stuck in %s (%s)", r.State, r.HoldReason)
		}
		time.Sleep(250 * time.Millisecond)
	}
	final, err := st.Runs().ByID(ctx0, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run #%d completed: cost=%d¢ steps=%d branch=%v", final.Seq, final.CostCents,
		final.StepCount, deref(final.Branch))

	// Provisioning checklist activities were recorded, with the load-bearing steps ok.
	acts, err := st.Activities().ForRun(ctx0, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]bool{}
	var agentActivities int
	for _, a := range acts {
		if a.Type == domain.ActivityProvision && a.Level == 1 {
			if a.OK != nil && *a.OK {
				steps[a.Title] = true
			}
		}
		if a.Type == domain.ActivityAction || a.Type == domain.ActivityThought ||
			a.Type == domain.ActivityResponse {
			agentActivities++
		}
	}
	for _, want := range []string{"container", "clone", "setup script"} {
		if !steps[want] {
			t.Errorf("provisioning step %q not recorded ok; steps = %v", want, steps)
		}
	}
	t.Logf("provisioning checklist (ok): %v; agent activities: %d", keys(steps), agentActivities)
	if agentActivities < 3 {
		t.Fatalf("agent session too thin: %d activities", agentActivities)
	}

	// The branch the builder minted exists in the local bare repository. The agent never
	// pushed — it holds no credential and never runs `git push` — so this is the
	// orchestrator's teardown push, on a run that COMPLETED.
	if final.Branch == nil {
		t.Fatal("run has no branch")
	}
	out, err := exec.Command("git", "--git-dir", bare, "branch", "--list").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), *final.Branch) {
		t.Fatalf("bare repo lacks branch %q:\n%s", *final.Branch, out)
	}
	t.Logf("bare repo branches after the run:\n%s", out)

	// The commit on that branch carries the run trailer (D-9 machinery, through the whole
	// real path).
	msg, err := exec.Command("git", "--git-dir", bare, "log", "-1", "--format=%B",
		*final.Branch).CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, msg)
	}
	if !strings.Contains(string(msg), "Lexicode-Run: "+run.ID) {
		t.Errorf("pushed commit lacks the run trailer:\n%s", msg)
	}
	t.Logf("pushed commit message:\n%s", msg)
}

func seedColumn(t *testing.T, st *kstore.Store, projectID string) string {
	t.Helper()
	now := domain.Now()
	var first string
	for i, c := range []struct {
		name string
		cat  domain.ColumnCategory
	}{{"Backlog", domain.CategoryBacklog}, {"In Progress", domain.CategoryRunning},
		{"Done", domain.CategoryDone}} {
		col := domain.Column{ID: domain.NewID(), ProjectID: projectID, Name: c.name,
			Category: c.cat, Position: int64(i + 1), CreatedAt: now, UpdatedAt: now}
		if err := st.Columns().Create(context.Background(), &col); err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = col.ID
		}
	}
	return first
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
