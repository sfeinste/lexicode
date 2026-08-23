package store_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// prProject is the per-project half of the graph TicketForPR walks: a column to hang tickets
// on and an agent to hang runs on. Several can share one store, which is how the
// project-scoping test gets two projects with the same pull request number.
type prProject struct {
	st      *store.Store
	project domain.Project
	column  domain.Column
	agent   domain.Agent
	runSeq  int64
}

func seedPRProject(t *testing.T, s *store.Store, key string) *prProject {
	t.Helper()
	ctx := context.Background()

	owner := domain.User{
		ID: domain.NewID(), Email: key + "@example.com", DisplayName: "O", PasswordHash: "x",
		Role: domain.RoleOwner, AvatarColor: "#000", CreatedAt: domain.Now(),
	}
	if err := s.Users().Create(ctx, &owner); err != nil {
		t.Fatal(err)
	}
	proj := domain.Project{
		ID: domain.NewID(), Key: key, Name: key, Color: "#fff", OwnerID: owner.ID,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Projects().Create(ctx, &proj); err != nil {
		t.Fatal(err)
	}
	col := domain.Column{
		ID: domain.NewID(), ProjectID: proj.ID, Name: "Doing",
		Category: domain.CategoryRunning, Position: 1,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Columns().Create(ctx, &col); err != nil {
		t.Fatal(err)
	}
	ag := domain.Agent{
		ID: domain.NewID(), ProjectID: proj.ID, Name: "Dev", Color: "#0af",
		RuntimeID: "claude-code", Model: "claude-sonnet", Effort: "medium",
		Autonomy:      domain.AutonomyAutoGates,
		Permissions:   domain.AgentPermissions{OpenPRs: true},
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 200, Enabled: true,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Agents().Create(ctx, &ag); err != nil {
		t.Fatal(err)
	}
	return &prProject{st: s, project: proj, column: col, agent: ag}
}

// ticket inserts one ticket in this project.
func (f *prProject) ticket(t *testing.T, key, title string) domain.Ticket {
	t.Helper()
	ctx := context.Background()
	seq, err := f.st.Projects().AllocateTicketSeq(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: f.project.ID, Seq: seq, Key: key, Title: title,
		ColumnID: f.column.ID, Position: domain.PositionBetween(0, 0),
		Priority: domain.PriorityMedium, Origin: domain.OriginHuman,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := f.st.Tickets().Create(ctx, &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

// openedPR inserts a completed run — on ticketID, or free-floating when nil — that recorded a
// `pull_request` output for prNumber. createdAt orders the outputs.
func (f *prProject) openedPR(t *testing.T, ticketID *string, prNumber int, createdAt string) domain.Run {
	t.Helper()
	ctx := context.Background()
	f.runSeq++
	run := domain.Run{
		ID: domain.NewID(), Seq: f.runSeq, ProjectID: f.project.ID, AgentID: f.agent.ID,
		TicketID: ticketID, State: domain.RunCompleted, Autonomy: domain.AutonomyAutoGates,
		Model: "claude-sonnet", Effort: "medium", Prompt: "do the thing",
		RuntimeID: "claude-code", SandboxID: "docker", QueuedAt: domain.Now(),
	}
	if err := f.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	ref := strconv.Itoa(prNumber)
	out := domain.RunOutput{
		ID: domain.NewID(), RunID: run.ID, Kind: domain.OutputPullRequest, Ref: ref,
		URL: "https://github.com/acme/payments/pull/" + ref, Summary: "opened PR " + ref,
		CreatedAt: createdAt,
	}
	if err := f.st.RunOutputs().Append(ctx, &out); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestTicketForPRWalksTheArtifactTrail is the LEXI-11 lookup: a pull request number resolves
// to the ticket the run that opened it was working on, along with that run.
func TestTicketForPRWalksTheArtifactTrail(t *testing.T) {
	f := seedPRProject(t, migrated(t), "PAY")
	tk := f.ticket(t, "PAY-7", "Say so when a PR could not be opened")
	run := f.openedPR(t, &tk.ID, 4, "2026-08-20T10:00:00Z")

	got, err := f.st.RunOutputs().TicketForPR(context.Background(), f.project.ID, 4)
	if err != nil {
		t.Fatalf("TicketForPR: %v", err)
	}
	want := store.PRTicket{TicketID: tk.ID, RunID: run.ID, RunSeq: run.Seq}
	if got != want {
		t.Errorf("TicketForPR = %+v, want %+v", got, want)
	}
}

// TestTicketForPRPrefersTheOpeningRun: when several runs recorded a `pull_request` output for
// the same number, the oldest — the one that actually opened it — decides the ticket.
func TestTicketForPRPrefersTheOpeningRun(t *testing.T) {
	f := seedPRProject(t, migrated(t), "PAY")
	first := f.ticket(t, "PAY-7", "The work the PR came from")
	later := f.ticket(t, "PAY-9", "A different ticket entirely")
	f.openedPR(t, &first.ID, 4, "2026-08-20T10:00:00Z")
	f.openedPR(t, &later.ID, 4, "2026-08-21T10:00:00Z")

	got, err := f.st.RunOutputs().TicketForPR(context.Background(), f.project.ID, 4)
	if err != nil {
		t.Fatalf("TicketForPR: %v", err)
	}
	if got.TicketID != first.ID {
		t.Errorf("TicketID = %q, want the opening run's ticket %q", got.TicketID, first.ID)
	}
}

// TestTicketForPRSkipsFreeFloatingRuns: a ticketless run that recorded the same PR does not
// end the search — it would otherwise hide the ticket a later row still names.
func TestTicketForPRSkipsFreeFloatingRuns(t *testing.T) {
	f := seedPRProject(t, migrated(t), "PAY")
	tk := f.ticket(t, "PAY-7", "The work the PR came from")
	f.openedPR(t, nil, 4, "2026-08-20T10:00:00Z")
	f.openedPR(t, &tk.ID, 4, "2026-08-21T10:00:00Z")

	got, err := f.st.RunOutputs().TicketForPR(context.Background(), f.project.ID, 4)
	if err != nil {
		t.Fatalf("TicketForPR: %v", err)
	}
	if got.TicketID != tk.ID {
		t.Errorf("TicketID = %q, want %q", got.TicketID, tk.ID)
	}
}

// TestTicketForPRIsProjectScoped: PR numbers are per-repository, so two projects can both have
// a #4 and each must get its own ticket back.
func TestTicketForPRIsProjectScoped(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)
	pay := seedPRProject(t, s, "PAY")
	ship := seedPRProject(t, s, "SHIP")

	payTicket := pay.ticket(t, "PAY-7", "Payments work")
	pay.openedPR(t, &payTicket.ID, 4, "2026-08-20T10:00:00Z")
	shipTicket := ship.ticket(t, "SHIP-2", "Shipping work")
	ship.openedPR(t, &shipTicket.ID, 4, "2026-08-19T10:00:00Z")

	got, err := s.RunOutputs().TicketForPR(ctx, pay.project.ID, 4)
	if err != nil {
		t.Fatalf("TicketForPR(PAY): %v", err)
	}
	if got.TicketID != payTicket.ID {
		t.Errorf("PAY #4 = %q, want %q; the older SHIP row leaked across projects",
			got.TicketID, payTicket.ID)
	}
	got, err = s.RunOutputs().TicketForPR(ctx, ship.project.ID, 4)
	if err != nil {
		t.Fatalf("TicketForPR(SHIP): %v", err)
	}
	if got.TicketID != shipTicket.ID {
		t.Errorf("SHIP #4 = %q, want %q", got.TicketID, shipTicket.ID)
	}
}

// TestTicketForPRNotFound: a PR no run in this project opened, and a PR every opener of which
// was free-floating, both answer ErrNotFound rather than a zero value.
func TestTicketForPRNotFound(t *testing.T) {
	f := seedPRProject(t, migrated(t), "PAY")
	f.openedPR(t, nil, 4, "2026-08-20T10:00:00Z")

	ctx := context.Background()
	if _, err := f.st.RunOutputs().TicketForPR(ctx, f.project.ID, 99); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown PR = %v, want ErrNotFound", err)
	}
	if _, err := f.st.RunOutputs().TicketForPR(ctx, f.project.ID, 4); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("free-floating opener = %v, want ErrNotFound", err)
	}
}
