package audit_test

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
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "audit.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func newWriter(t *testing.T, st *store.Store, now func() string) *audit.Writer {
	t.Helper()
	return audit.New(audit.Options{
		Store:  st,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    now,
	})
}

// TestWriteReadsTheActorFromContext is S06 acceptance: attribution comes from the context the
// auth middleware populated, never from a parameter a service could forget.
func TestWriteReadsTheActorFromContext(t *testing.T) {
	st := newStore(t)
	w := newWriter(t, st, nil)

	userID := domain.NewID()
	ctx := auth.WithActor(context.Background(), auth.Actor{Kind: domain.ActorHuman, ID: userID})

	type snap struct {
		Column string `json:"column"`
	}
	err := w.Write(ctx, "ticket.move", audit.Target{Kind: "ticket", ID: "T-1"},
		snap{Column: "Backlog"}, snap{Column: "Running"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	// A context with no actor is a system entry (boot tasks, seeds), and nil snapshots are NULL.
	err = w.Write(context.Background(), "workspace.migrate",
		audit.Target{Kind: "workspace", ID: "ws"}, nil, nil)
	if err != nil {
		t.Fatalf("system Write: %v", err)
	}

	rows, err := st.Audit().List(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	byAction := map[string]domain.AuditEntry{}
	for _, r := range rows {
		byAction[r.Action] = r
	}

	moved := byAction["ticket.move"]
	if moved.ActorKind != domain.ActorHuman || moved.ActorID == nil || *moved.ActorID != userID {
		t.Errorf("ticket.move actor = %s/%v, want human/%s from the context", moved.ActorKind, moved.ActorID, userID)
	}
	if string(moved.Before) != `{"column":"Backlog"}` || string(moved.After) != `{"column":"Running"}` {
		t.Errorf("snapshots = %s → %s", moved.Before, moved.After)
	}

	sys := byAction["workspace.migrate"]
	if sys.ActorKind != domain.ActorSystem || sys.ActorID != nil {
		t.Errorf("actorless entry = %s/%v, want system/nil", sys.ActorKind, sys.ActorID)
	}
	if sys.Before != nil || sys.After != nil {
		t.Errorf("nil snapshots stored as %q/%q, want NULL", sys.Before, sys.After)
	}
}

func TestWriteIsNilSafeForTypedNils(t *testing.T) {
	st := newStore(t)
	w := newWriter(t, st, nil)

	type snap struct{ X int }
	var p *snap // typed nil — the classic non-nil interface holding nil
	if err := w.Write(context.Background(), "thing.update",
		audit.Target{Kind: "thing", ID: "1"}, p, json.RawMessage(nil)); err != nil {
		t.Fatalf("Write with typed nils: %v", err)
	}
	rows, err := st.Audit().List(context.Background(), store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Before != nil || rows[0].After != nil {
		t.Errorf("typed-nil snapshots stored as %q/%q, want NULL", rows[0].Before, rows[0].After)
	}
}

func TestWriteRefusesAnEmptyAction(t *testing.T) {
	w := newWriter(t, newStore(t), nil)
	if err := w.Write(context.Background(), "", audit.Target{Kind: "t", ID: "1"}, nil, nil); err == nil {
		t.Error("Write with no action succeeded; want an error")
	}
}

// auditQuery hits the handler and decodes the page.
func auditQuery(t *testing.T, h http.Handler, query string) (int, struct {
	Entries []struct {
		ID        string `json:"id"`
		ActorKind string `json:"actor_kind"`
		Action    string `json:"action"`
		CreatedAt string `json:"created_at"`
	} `json:"entries"`
	NextCursor string `json:"next_cursor"`
},
) {
	t.Helper()
	var body struct {
		Entries []struct {
			ID        string `json:"id"`
			ActorKind string `json:"actor_kind"`
			Action    string `json:"action"`
			CreatedAt string `json:"created_at"`
		} `json:"entries"`
		NextCursor string `json:"next_cursor"`
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit?"+query, nil))
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, body
}

// TestAuditEndpointFilters is S06 acceptance: GET /api/v1/audit filters by actor, action,
// target kind, time range, and pages with a cursor. (The owner-only wiring is a kernel test.)
func TestAuditEndpointFilters(t *testing.T) {
	st := newStore(t)
	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	step := 0
	w := newWriter(t, st, func() string {
		step++
		return domain.FormatTime(base.Add(time.Duration(step) * time.Minute))
	})
	h := w.Handler()

	humanID := domain.NewID()
	human := auth.WithActor(context.Background(), auth.Actor{Kind: domain.ActorHuman, ID: humanID})
	agent := auth.WithActor(context.Background(), auth.Actor{Kind: domain.ActorAgent, ID: domain.NewID()})

	// t+1m human ticket.move · t+2m agent ticket.update · t+3m human agent.update
	for _, e := range []struct {
		ctx    context.Context
		action string
		kind   string
	}{
		{human, "ticket.move", "ticket"},
		{agent, "ticket.update", "ticket"},
		{human, "agent.update", "agent"},
	} {
		if err := w.Write(e.ctx, e.action, audit.Target{Kind: e.kind, ID: "x"}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	// No filter: everything, newest first.
	code, page := auditQuery(t, h, "")
	if code != http.StatusOK || len(page.Entries) != 3 {
		t.Fatalf("unfiltered = %d, %d entries, want 200 with 3", code, len(page.Entries))
	}
	if page.Entries[0].Action != "agent.update" {
		t.Errorf("first entry = %s, want the newest (agent.update)", page.Entries[0].Action)
	}

	// actor kind, actor kind:id, action, target kind.
	if _, p := auditQuery(t, h, "actor=agent"); len(p.Entries) != 1 || p.Entries[0].Action != "ticket.update" {
		t.Errorf("actor=agent → %+v, want just ticket.update", p.Entries)
	}
	if _, p := auditQuery(t, h, "actor=human:"+humanID); len(p.Entries) != 2 {
		t.Errorf("actor=human:<id> → %d entries, want 2", len(p.Entries))
	}
	if _, p := auditQuery(t, h, "action=ticket.move"); len(p.Entries) != 1 {
		t.Errorf("action=ticket.move → %d entries, want 1", len(p.Entries))
	}
	if _, p := auditQuery(t, h, "target=ticket"); len(p.Entries) != 2 {
		t.Errorf("target=ticket → %d entries, want 2", len(p.Entries))
	}

	// Time range: only the middle minute.
	since := base.Add(90 * time.Second).Format(time.RFC3339)
	until := base.Add(150 * time.Second).Format(time.RFC3339)
	if _, p := auditQuery(t, h, "since="+since+"&until="+until); len(p.Entries) != 1 || p.Entries[0].Action != "ticket.update" {
		t.Errorf("time range → %+v, want just the t+2m entry", p.Entries)
	}

	// Pagination: page of 2 hands back a cursor; the cursor page holds the rest, no overlap.
	code, first := auditQuery(t, h, "limit=2")
	if code != http.StatusOK || len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("limit=2 → %d entries, cursor %q", len(first.Entries), first.NextCursor)
	}
	_, second := auditQuery(t, h, "limit=2&cursor="+first.NextCursor)
	if len(second.Entries) != 1 {
		t.Fatalf("cursor page → %d entries, want the remaining 1", len(second.Entries))
	}
	seen := map[string]bool{}
	for _, e := range append(first.Entries, second.Entries...) {
		if seen[e.ID] {
			t.Errorf("entry %s appears on both pages", e.ID)
		}
		seen[e.ID] = true
	}
	if second.NextCursor != "" {
		t.Errorf("short page carries cursor %q, want none", second.NextCursor)
	}

	// Bad filters are named 400s.
	if code, _ := auditQuery(t, h, "actor=alien"); code != http.StatusBadRequest {
		t.Errorf("actor=alien → %d, want 400", code)
	}
	if code, _ := auditQuery(t, h, "since=yesterday"); code != http.StatusBadRequest {
		t.Errorf("since=yesterday → %d, want 400", code)
	}
	if code, _ := auditQuery(t, h, "limit=0"); code != http.StatusBadRequest {
		t.Errorf("limit=0 → %d, want 400", code)
	}
}
