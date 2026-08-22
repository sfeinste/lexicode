package github

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// TestForgeCommentAuditAttributesToAgent is the S37 acceptance "the audit log shows an
// agent's PR comment attributed to the agent, not to the token owner" — asserted through the
// UI-backing API: the REAL audit writer records the forge write into a real store, and
// GET /api/v1/audit?actor=agent:<id> (the audit page's endpoint) returns the entry with
// actor_kind=agent. No human actor appears anywhere, even though a human's PAT was the
// credential on the wire.
func TestForgeCommentAuditAttributesToAgent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{
		Path: filepath.Join(t.TempDir(), "attr.db"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	auditW := audit.New(audit.Options{Store: st, Logger: logger})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/acme/payments/issues/55/comments", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":9001,"user":{"login":"ada-the-token-owner"},
			"body":"CI is green now.",
			"html_url":"https://github.com/acme/payments/pull/55#issuecomment-9001",
			"created_at":"2026-08-20T10:00:00Z","updated_at":"2026-08-20T10:00:00Z"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	m := New(Options{
		BaseURL: srv.URL + "/",
		Logger:  logger,
		Permissions: func(context.Context, string) (domain.AgentPermissions, error) {
			return domain.AgentPermissions{CommentPRs: true}, nil
		},
		RecordOutput: func(context.Context, domain.RunOutput) error { return nil },
		RecordAudit:  auditW.Write, // exactly how kernel Init wires it
	})

	actor := domain.Actor{AgentID: "agent-7", RunID: "run-99"}
	// A background context with NO human actor on it — attribution must come from the forge,
	// not from whoever's session happened to be around.
	if _, err := m.forge.CommentOnPullRequest(context.Background(), testCreds, testRepo, actor,
		55, "CI is green now."); err != nil {
		t.Fatalf("CommentOnPullRequest: %v", err)
	}

	// Through the UI-backing API: the audit page queries GET /api/v1/audit with the actor
	// filter it renders.
	req := httptest.NewRequest("GET", "/api/v1/audit?actor=agent:agent-7", nil)
	rec := httptest.NewRecorder()
	auditW.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit list = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Entries []struct {
			ActorKind string          `json:"actor_kind"`
			ActorID   *string         `json:"actor_id"`
			Action    string          `json:"action"`
			After     json.RawMessage `json:"after"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 1 {
		t.Fatalf("entries for agent:agent-7 = %d, want 1\n%s", len(body.Entries), rec.Body.String())
	}
	e := body.Entries[0]
	if e.ActorKind != "agent" || e.ActorID == nil || *e.ActorID != "agent-7" {
		t.Fatalf("actor = %s:%v, want agent:agent-7 (never the token owner)", e.ActorKind, e.ActorID)
	}
	if e.Action != "forge.pr.comment" {
		t.Fatalf("action = %s", e.Action)
	}

	// And nothing attributed the write to a human.
	req = httptest.NewRequest("GET", "/api/v1/audit?actor=human", nil)
	rec = httptest.NewRecorder()
	auditW.Handler().ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 0 {
		t.Fatalf("human-attributed entries = %d, want 0", len(body.Entries))
	}
}
