package store_test

import (
	"context"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
)

// TestActivitiesAppendNextAllocatesSeq covers the S18 out-of-band appender: AppendNext takes
// the run's next free seq (starting at 0), interleaves safely with explicitly numbered
// Appends, and hands the allocated seq back on the struct.
func TestActivitiesAppendNextAllocatesSeq(t *testing.T) {
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

	first := domain.Activity{
		RunID: run.ID, Type: domain.ActivitySystem, Level: 2,
		Title: "Network: blocked registry.npmjs.org (policy: none)", CreatedAt: domain.Now(),
	}
	if err := s.Activities().AppendNext(ctx, &first); err != nil {
		t.Fatalf("AppendNext: %v", err)
	}
	if first.Seq != 0 {
		t.Errorf("first seq = %d, want 0", first.Seq)
	}

	// An explicitly numbered append in between, as the S20 ingest will do.
	explicit := domain.Activity{
		RunID: run.ID, Seq: 5, Type: domain.ActivityAction, Level: 1,
		Title: "Bash npm install", CreatedAt: domain.Now(),
	}
	if err := s.Activities().Append(ctx, &explicit); err != nil {
		t.Fatalf("Append: %v", err)
	}

	second := domain.Activity{
		RunID: run.ID, Type: domain.ActivitySystem, Level: 2,
		Title: "Network: allowed api.anthropic.com", CreatedAt: domain.Now(),
	}
	if err := s.Activities().AppendNext(ctx, &second); err != nil {
		t.Fatalf("AppendNext after explicit: %v", err)
	}
	if second.Seq != 6 {
		t.Errorf("second seq = %d, want 6 (MAX+1 past the explicit row)", second.Seq)
	}

	got, err := s.Activities().ForRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("ForRun: %v", err)
	}
	if len(got) != 3 || got[0].Seq != 0 || got[1].Seq != 5 || got[2].Seq != 6 {
		t.Errorf("transcript seqs = %+v, want 0,5,6", got)
	}
}
