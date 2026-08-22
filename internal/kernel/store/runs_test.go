package store_test

import (
	"context"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
)

// TestRunsBranchInUse covers the S19 collision source: a branch is "in use" once any run of
// the project claimed it — terminal runs included, their branches may still exist on the
// remote — and never across projects.
func TestRunsBranchInUse(t *testing.T) {
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
		Autonomy: domain.AutonomyAutoGates, GitAuthorName: "Dev",
		GitAuthorEmail: "dev@agents.lexicode.local", ConcurrencyCap: 1,
		MaxWallClockSeconds: 3600, MaxSteps: 200, Enabled: true,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Agents().Create(ctx, &agent); err != nil {
		t.Fatal(err)
	}

	branch := "dev/PAY-14-idempotency-keys"
	run := domain.Run{
		ID: domain.NewID(), Seq: 1, ProjectID: proj.ID, AgentID: agent.ID,
		State: domain.RunFailed, Autonomy: domain.AutonomyAutoGates,
		Model: "claude-sonnet", Effort: "medium", RuntimeID: "claude-code",
		SandboxID: "docker", SubjectKey: "ticket:PAY-14", Branch: &branch,
		QueuedAt: domain.Now(),
	}
	if err := s.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}

	if used, err := s.Runs().BranchInUse(ctx, proj.ID, branch); err != nil || !used {
		t.Errorf("BranchInUse(same project, claimed branch) = %v, %v; want true, nil", used, err)
	}
	if used, err := s.Runs().BranchInUse(ctx, proj.ID, branch+"-2"); err != nil || used {
		t.Errorf("BranchInUse(free name) = %v, %v; want false, nil", used, err)
	}
	if used, err := s.Runs().BranchInUse(ctx, "other-project", branch); err != nil || used {
		t.Errorf("BranchInUse(other project) = %v, %v; want false, nil", used, err)
	}
}
