// S28: the "inapp" Notifier over the real S24 delivery seam — the row is written, and a
// second delivery for the same (user, run) updates that row in place rather than stacking
// (interaction rule 3).
package notify_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
	notifymod "github.com/spruce/lexicode/internal/module/notify"
	notifysvc "github.com/spruce/lexicode/internal/service/notify"
)

func TestInAppDeliverWritesAndUpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(t.TempDir(), "notify.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := domain.Now()
	u := domain.User{ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#000", CreatedAt: now}
	if err := st.Users().Create(ctx, &u); err != nil {
		t.Fatal(err)
	}
	p := domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#fff",
		OwnerID: u.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &p); err != nil {
		t.Fatal(err)
	}
	agent := domain.Agent{
		ID: domain.NewID(), ProjectID: p.ID, Name: "Dev", Role: "developer", Color: "#888",
		RuntimeID: "scripted", Model: "fake", Effort: "medium", Autonomy: domain.AutonomyAuto,
		GitAuthorName: "Dev", GitAuthorEmail: "dev@agents.local", ConcurrencyCap: 1,
		MaxWallClockSeconds: 60, MaxSteps: 10, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.Agents().Create(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{
		ID: domain.NewID(), Seq: 1, ProjectID: p.ID, AgentID: agent.ID,
		State: domain.RunQueued, Autonomy: domain.AutonomyAuto, Model: "fake", Effort: "medium",
		RuntimeID: "scripted", SandboxID: "fake", SubjectKey: "repo", QueuedAt: now,
	}
	if err := st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}

	svc := notifysvc.New(notifysvc.Options{Store: st, Logger: logger})
	var inapp ports.Notifier = notifymod.NewInApp(svc.DeliverInApp)
	if inapp.ID() != "inapp" {
		t.Fatalf("notifier id = %q, want inapp", inapp.ID())
	}

	rid := run.ID
	deliver := func(title string) {
		t.Helper()
		if err := inapp.Deliver(ctx, domain.Notification{
			UserID: u.ID, ProjectID: p.ID, RunID: &rid,
			Flavor: domain.FlavorReview, Title: title, Body: "Sent by trigger `t`",
		}); err != nil {
			t.Fatal(err)
		}
	}
	deliver("first message")
	deliver("second message")

	ns, err := st.Notifications().ForUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 {
		t.Fatalf("rows = %d, want the (user, run) row updated in place, never stacked", len(ns))
	}
	if ns[0].Title != "second message" {
		t.Fatalf("title = %q, want the refreshed content", ns[0].Title)
	}
	if ns[0].State != domain.NotificationUnread {
		t.Fatalf("state = %s, want unread", ns[0].State)
	}

	// A userless notification is refused before the seam runs.
	if err := inapp.Deliver(ctx, domain.Notification{ProjectID: p.ID, Title: "x"}); err == nil {
		t.Fatal("a notification without a user must be refused")
	}
}
