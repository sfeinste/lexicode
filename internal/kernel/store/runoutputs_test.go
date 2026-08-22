package store_test

import (
	"context"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
)

// TestRunOutputsRoundTrip pushes one output through Append and ForRun, over the minimal
// foreign-key graph a run needs (user → project → agent → run).
func TestRunOutputsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	owner := domain.User{
		ID: domain.NewID(), Email: "o@example.com", DisplayName: "O", PasswordHash: "x",
		Role: domain.RoleOwner, AvatarColor: "#000", CreatedAt: domain.Now(),
	}
	if err := s.Users().Create(ctx, &owner); err != nil {
		t.Fatal(err)
	}
	proj := domain.Project{
		ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#fff", OwnerID: owner.ID,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Projects().Create(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: proj.ID, Name: "Dev", Color: "#0af",
		RuntimeID: "claude-code", Model: "claude-sonnet", Effort: "medium",
		Autonomy:      domain.AutonomyAutoGates,
		Permissions:   domain.AgentPermissions{OpenPRs: true},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 200, Enabled: true,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Agents().Create(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: 1, ProjectID: proj.ID, AgentID: agent.ID,
		State: domain.RunQueued, Autonomy: domain.AutonomyAutoGates,
		Model: "claude-sonnet", Effort: "medium", Prompt: "do the thing",
		RuntimeID: "claude-code", SandboxID: "docker", SubjectKey: "ticket:PAY-1",
		QueuedAt: domain.Now(),
	}
	if err := s.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}

	out := domain.RunOutput{
		ID: domain.NewID(), RunID: run.ID, Kind: domain.OutputPullRequest,
		Ref: "55", URL: "https://github.com/acme/payments/pull/55",
		Summary: "opened PR #55", CreatedAt: domain.Now(),
	}
	if err := s.RunOutputs().Append(ctx, &out); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.RunOutputs().ForRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(got) != 1 || got[0] != out {
		t.Errorf("ForRun = %+v; want [%+v]", got, out)
	}
}
