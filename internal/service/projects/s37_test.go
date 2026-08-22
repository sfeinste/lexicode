package projects_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// seedProjectContent inserts one ticket, one terminal run and one wiki page directly through
// the store, so the S37 deletion has real dependent rows (with their own children) to cascade
// through.
func seedProjectContent(t *testing.T, st *store.Store, projectID string) {
	t.Helper()
	ctx := context.Background()
	now := domain.Now()

	cols, err := st.Columns().ForProject(ctx, projectID)
	if err != nil || len(cols) == 0 {
		t.Fatalf("columns: %v (%d)", err, len(cols))
	}

	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: projectID, Name: "Implementer", Color: "#00a884",
		RuntimeID: "claude-code", Model: "claude-fable-5", Effort: "medium",
		Autonomy: domain.AutonomyAutoGates, Permissions: domain.AgentPermissions{},
		GitAuthorName: "Implementer", GitAuthorEmail: "impl@example.com",
		ConcurrencyCap: 1, MaxWallClockSeconds: 3600, MaxSteps: 200, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Agents().Create(ctx, &agent); err != nil {
		t.Fatal(err)
	}

	ticket := domain.Ticket{
		ID: domain.NewID(), ProjectID: projectID, Seq: 1, Key: "PAY-1", Title: "A ticket",
		ColumnID: cols[0].ID, Position: 1, Priority: domain.PriorityNone,
		Origin: domain.OriginHuman, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Tickets().Create(ctx, &ticket); err != nil {
		t.Fatal(err)
	}

	tid := ticket.ID
	run := domain.Run{
		ID: domain.NewID(), Seq: 1, ProjectID: projectID, AgentID: agent.ID, TicketID: &tid,
		State: domain.RunFailed, StateReason: "budget exceeded",
		Autonomy: domain.AutonomyAutoGates, Model: agent.Model, Effort: agent.Effort,
		Prompt: "p", RuntimeID: "claude-code", SandboxID: "docker",
		QueuedAt: now,
	}
	if err := st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	if err := st.Activities().Append(ctx, &domain.Activity{
		RunID: run.ID, Seq: 0, Type: domain.ActivitySystem, Title: "step", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	page := domain.WikiPage{
		ID: domain.NewID(), ProjectID: projectID, Slug: "conventions", Title: "Conventions",
		Position: 1, AgentScope: domain.ScopeAuto, State: domain.WikiLive,
		Body: "Prefer small PRs.", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Wiki().CreatePage(ctx, &page); err != nil {
		t.Fatal(err)
	}
}

// TestBudgetEndpointReflectsExhaustion is DoD item 2's banner half: with the ledger at the
// ceiling, GET /projects/{key}/budget names the ceiling, flags exhaustion, and points at the
// next midnight UTC — the same day scoping the scheduler admission check uses.
func TestBudgetEndpointReflectsExhaustion(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	ctx := context.Background()

	if status, _ := e.doJSON(c, "POST", "/api/v1/projects", `{"key":"PAY","name":"Payments"}`); status != 201 {
		t.Fatalf("create = %d", status)
	}

	// Fresh project, workspace default ceiling (2000), nothing spent.
	status, body := e.doJSON(c, "GET", "/api/v1/projects/PAY/budget", "")
	if status != http.StatusOK {
		t.Fatalf("budget = %d: %v", status, body)
	}
	if body["exhausted"] != false || body["inherited"] != true ||
		body["ceiling_cents"].(float64) != 2000 || body["spend_today_cents"].(float64) != 0 {
		t.Fatalf("fresh budget = %v", body)
	}

	// Override the ceiling to $5 and roll $6 into today's ledger — the admission table.
	if status, _ := e.doJSON(c, "PATCH", "/api/v1/projects/PAY", `{"daily_budget_cents":500}`); status != 200 {
		t.Fatalf("patch = %d", status)
	}
	p, err := e.st.Projects().ByKey(ctx, "PAY")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	if err := e.st.Budget().Add(ctx, day, p.ID, "", "", 600); err != nil {
		t.Fatal(err)
	}

	status, body = e.doJSON(c, "GET", "/api/v1/projects/PAY/budget", "")
	if status != http.StatusOK {
		t.Fatalf("budget = %d", status)
	}
	if body["exhausted"] != true || body["inherited"] != false ||
		body["ceiling_cents"].(float64) != 500 || body["spend_today_cents"].(float64) != 600 {
		t.Fatalf("exhausted budget = %v", body)
	}
	if body["day"] != day {
		t.Fatalf("day = %v, want %s", body["day"], day)
	}
	wantReset := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	if got := body["resets_at"].(string); got != domain.FormatTime(wantReset) {
		t.Fatalf("resets_at = %s, want %s (next midnight UTC)", got, domain.FormatTime(wantReset))
	}
}

// TestDeleteProjectTypedConfirmation is DoD item 4: the server enforces the typed key, the
// deletion removes the rows the response counts, and the audit trail survives at workspace
// level.
func TestDeleteProjectTypedConfirmation(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	ctx := context.Background()

	if status, _ := e.doJSON(c, "POST", "/api/v1/projects", `{"key":"PAY","name":"Payments"}`); status != 201 {
		t.Fatalf("create = %d", status)
	}
	p, err := e.st.Projects().ByKey(ctx, "PAY")
	if err != nil {
		t.Fatal(err)
	}
	seedProjectContent(t, e.st, p.ID)

	// Counts endpoint names what will go.
	status, body := e.doJSON(c, "GET", "/api/v1/projects/PAY/counts", "")
	if status != 200 || body["tickets"].(float64) != 1 || body["runs"].(float64) != 1 ||
		body["wiki_pages"].(float64) != 1 {
		t.Fatalf("counts = %d %v", status, body)
	}

	// Wrong confirm string: refused, nothing deleted.
	status, body = e.doJSON(c, "DELETE", "/api/v1/projects/PAY", `{"confirm":"pay"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("wrong confirm = %d, want 400: %v", status, body)
	}
	if _, err := e.st.Projects().ByKey(ctx, "PAY"); err != nil {
		t.Fatalf("project gone after refused delete: %v", err)
	}

	// Right confirm: deleted, counts echoed.
	status, body = e.doJSON(c, "DELETE", "/api/v1/projects/PAY", `{"confirm":"PAY"}`)
	if status != http.StatusOK {
		t.Fatalf("delete = %d: %v", status, body)
	}
	counts := body["counts"].(map[string]any)
	if counts["tickets"].(float64) != 1 || counts["runs"].(float64) != 1 ||
		counts["wiki_pages"].(float64) != 1 {
		t.Fatalf("delete counts = %v", counts)
	}

	// The rows are gone.
	if _, err := e.st.Projects().ByKey(ctx, "PAY"); err == nil {
		t.Fatal("project still readable after delete")
	}
	for name, q := range map[string]func() (int, error){
		"tickets": func() (int, error) { l, err := e.st.Tickets().ForProject(ctx, p.ID); return len(l), err },
		"runs":    func() (int, error) { l, err := e.st.Runs().ForProject(ctx, p.ID); return len(l), err },
	} {
		n, err := q()
		if err != nil {
			t.Fatalf("%s read: %v", name, err)
		}
		if n != 0 {
			t.Fatalf("%d %s rows survive the delete", n, name)
		}
	}

	// The deletion's own audit entry lives at workspace level (project_id NULL) and names
	// the counts; the project's earlier entries survive, detached from the dead project.
	entries, err := e.st.Audit().List(ctx, store.AuditFilter{Action: "project.delete"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("project.delete entries = %d, want 1", len(entries))
	}
	if entries[0].ProjectID != nil {
		t.Fatalf("delete audit entry scoped to project %v, want workspace level (NULL)", *entries[0].ProjectID)
	}
	if entries[0].TargetID != p.ID {
		t.Fatalf("delete audit target = %s, want %s", entries[0].TargetID, p.ID)
	}
	creates, err := e.st.Audit().List(ctx, store.AuditFilter{Action: "project.create"})
	if err != nil {
		t.Fatal(err)
	}
	if len(creates) != 1 || creates[0].ProjectID != nil {
		t.Fatalf("project.create entry should survive detached (NULL project); got %v", creates)
	}
}

// TestDeleteProjectRefusedWhileRunsActive: a queued run blocks deletion with 409.
func TestDeleteProjectRefusedWhileRunsActive(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	ctx := context.Background()

	if status, _ := e.doJSON(c, "POST", "/api/v1/projects", `{"key":"PAY","name":"Payments"}`); status != 201 {
		t.Fatalf("create = %d", status)
	}
	p, err := e.st.Projects().ByKey(ctx, "PAY")
	if err != nil {
		t.Fatal(err)
	}
	seedProjectContent(t, e.st, p.ID)
	agents, err := e.st.Agents().ForProject(ctx, p.ID)
	if err != nil || len(agents) == 0 {
		t.Fatalf("agents: %v", err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: 2, ProjectID: p.ID, AgentID: agents[0].ID,
		State: domain.RunQueued, Autonomy: domain.AutonomyAuto, Model: "m", Effort: "medium",
		Prompt: "p", RuntimeID: "claude-code", SandboxID: "docker", QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}

	status, body := e.doJSON(c, "DELETE", "/api/v1/projects/PAY", `{"confirm":"PAY"}`)
	if status != http.StatusConflict {
		t.Fatalf("delete with queued run = %d, want 409: %v", status, body)
	}
}
