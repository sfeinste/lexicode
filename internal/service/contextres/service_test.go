package contextres

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
	contextmod "github.com/spruce/lexicode/internal/module/context"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "s34.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func seedProject(t *testing.T, st *store.Store) (domain.Project, domain.User) {
	t.Helper()
	ctx := context.Background()
	now := domain.Now()
	u := domain.User{
		ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#123456", CreatedAt: now,
	}
	if err := st.Users().Create(ctx, &u); err != nil {
		t.Fatal(err)
	}
	p := domain.Project{
		ID: domain.NewID(), Key: "PAY", Name: "Payments", OwnerID: u.ID,
		Color: "#336699", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Projects().Create(ctx, &p); err != nil {
		t.Fatal(err)
	}
	return p, u
}

func seedPage(t *testing.T, st *store.Store, projectID, title string, scope domain.AgentScope, mutate func(*domain.WikiPage)) domain.WikiPage {
	t.Helper()
	now := domain.Now()
	pg := domain.WikiPage{
		ID: domain.NewID(), ProjectID: projectID, Slug: domain.NewID(),
		Title: title, Position: 1, AgentScope: scope,
		Body: "Body of " + title + ".", TokenEstimate: 40,
		State: domain.WikiLive, CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&pg)
	}
	if err := st.Wiki().CreatePage(context.Background(), &pg); err != nil {
		t.Fatal(err)
	}
	return pg
}

// TestDemoteExpired is architecture §11's verified_until enforcement, on a fake clock: an
// `always` page past its verification date is demoted to `auto` as a real data change —
// demoted_at/demoted_from set, an audit row written, the owner notified — and a page still
// inside its window, or already `auto`, is untouched.
func TestDemoteExpired(t *testing.T) {
	st := openStore(t)
	p, owner := seedProject(t, st)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditW := audit.New(audit.Options{Store: st, Logger: logger})

	// The fake clock: 2026-09-01T09:00Z.
	clock := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

	expired := seedPage(t, st, p.ID, "Stale agreements", domain.ScopeAlways, func(pg *domain.WikiPage) {
		v := "2026-08-31"
		pg.VerifiedUntil = &v
		pg.OwnerID = &owner.ID
		pg.Tags = []string{"agreements"}
	})
	fresh := seedPage(t, st, p.ID, "Fresh agreements", domain.ScopeAlways, func(pg *domain.WikiPage) {
		v := "2026-09-01" // holds through today — not expired yet
		pg.VerifiedUntil = &v
	})
	unverified := seedPage(t, st, p.ID, "No date", domain.ScopeAlways, nil)

	var delivered []domain.Notification
	svc := New(Options{
		Store: st, Audit: auditW, Logger: logger,
		Now: func() time.Time { return clock },
		Notify: func(_ context.Context, n domain.Notification) error {
			delivered = append(delivered, n)
			return nil
		},
	})
	svc.DemoteExpired(context.Background())

	after, err := st.Wiki().ByID(context.Background(), expired.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AgentScope != domain.ScopeAuto {
		t.Fatalf("expired page scope = %s, want auto", after.AgentScope)
	}
	if after.DemotedAt == nil || after.DemotedFrom == nil || *after.DemotedFrom != "always" {
		t.Fatalf("demotion trace missing: at=%v from=%v", after.DemotedAt, after.DemotedFrom)
	}

	for _, id := range []string{fresh.ID, unverified.ID} {
		pg, err := st.Wiki().ByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if pg.AgentScope != domain.ScopeAlways || pg.DemotedAt != nil {
			t.Fatalf("page %q was demoted; verified_until holds through the named day", pg.Title)
		}
	}

	// Audit row.
	entries, err := st.Audit().List(context.Background(), store.AuditFilter{
		ProjectID: p.ID, Action: "wiki.page.demote",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(entries))
	}

	// Owner notification (a real notifications row when wired through DeliverInApp; here
	// the seam captures it).
	if len(delivered) != 1 || delivered[0].UserID != owner.ID {
		t.Fatalf("notifications = %+v, want one to the owner", delivered)
	}

	// The next resolve no longer yields the page AS always (it is `auto` now — it may
	// still be retrieved by keyword, which is the designed demotion, not a leak).
	items, err := contextmod.NewWikiProvider(st).Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID, Dry: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Reason == "always" && it.Title == "Stale agreements" {
			t.Fatalf("demoted page still resolves as always: %+v", it)
		}
	}
	// And a keyword task can still retrieve it as auto — the demotion demotes, not deletes.
	items, err = contextmod.NewWikiProvider(st).Resolve(context.Background(),
		ports.ContextRequest{ProjectID: p.ID, TaskSummary: "revisit the agreements"})
	if err != nil {
		t.Fatal(err)
	}
	foundAuto := false
	for _, it := range items {
		if it.Title == "Stale agreements" {
			foundAuto = true
			if it.Reason != `retrieved for "agreements"` {
				t.Fatalf("demoted page reason = %q, want auto retrieval", it.Reason)
			}
		}
	}
	if !foundAuto {
		t.Fatal("demoted page no longer retrievable as auto")
	}

	// Idempotent: a second pass finds nothing expired-and-always.
	svc.DemoteExpired(context.Background())
	entries, err = st.Audit().List(context.Background(), store.AuditFilter{
		ProjectID: p.ID, Action: "wiki.page.demote",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("second pass wrote more audit rows: %d", len(entries))
	}
}

// stubResolver returns a fixed stack.
type stubResolver struct{ items []domain.RunContextItem }

func (s stubResolver) PreviewContext(context.Context, string, string) ([]domain.RunContextItem, error) {
	return s.items, nil
}

// TestBudgetAndPreviewHandlers exercises the two read endpoints directly (auth middleware is
// covered by the shared kernel tests; the handlers take the path values).
func TestBudgetAndPreviewHandlers(t *testing.T) {
	st := openStore(t)
	p, _ := seedProject(t, st)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	seedPage(t, st, p.ID, "Always A", domain.ScopeAlways, func(pg *domain.WikiPage) { pg.TokenEstimate = 3000 })
	seedPage(t, st, p.ID, "Always B", domain.ScopeAlways, func(pg *domain.WikiPage) { pg.TokenEstimate = 2000 })
	seedPage(t, st, p.ID, "Auto page", domain.ScopeAuto, func(pg *domain.WikiPage) { pg.TokenEstimate = 999 })

	svc := New(Options{Store: st, Logger: logger, Resolver: stubResolver{items: []domain.RunContextItem{
		{Provider: "project", SourceKind: "project", SourceRef: "PAY", Title: "Project guidance",
			Reason: "project guidance", Tokens: 10, Position: 1, Injected: true},
		{Provider: "repofiles", SourceKind: "repo_file", SourceRef: "AGENTS.md", Title: "AGENTS.md",
			Reason: "repo file", Tokens: 55, Position: 2, Injected: false},
	}}})

	// Budget: 5000 always tokens against the workspace default threshold 4000 → over.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/PAY/wiki/context-budget", nil)
	req.SetPathValue("key", "PAY")
	rec := httptest.NewRecorder()
	svc.handleBudget(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("budget status = %d: %s", rec.Code, rec.Body.String())
	}
	var budget struct {
		ThresholdTokens int64 `json:"threshold_tokens"`
		AlwaysTokens    int64 `json:"always_tokens"`
		Over            bool  `json:"over"`
		Pages           []struct {
			Title string `json:"title"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &budget); err != nil {
		t.Fatal(err)
	}
	if budget.ThresholdTokens != 4000 || budget.AlwaysTokens != 5000 || !budget.Over || len(budget.Pages) != 2 {
		t.Fatalf("budget = %+v, want threshold 4000, always 5000, over, 2 pages", budget)
	}

	// Preview: total counts injected items only.
	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: p.ID, Name: "Dev", Role: "developer", Color: "#888888",
		RuntimeID: "scripted", Model: "fake", Effort: "medium", Autonomy: domain.AutonomyAuto,
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.lexicode.local",
		ConcurrencyCap: 1, MaxWallClockSeconds: 60, MaxSteps: 10, Enabled: true,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := st.Agents().Create(context.Background(), &agent); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID+"/context-preview", nil)
	req.SetPathValue("id", agent.ID)
	rec = httptest.NewRecorder()
	svc.handlePreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", rec.Code, rec.Body.String())
	}
	var preview struct {
		Items       []map[string]any `json:"items"`
		TotalTokens int64            `json:"total_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 2 || preview.TotalTokens != 10 {
		t.Fatalf("preview = %+v, want 2 items and total 10 (injected only)", preview)
	}
}
