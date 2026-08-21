package seed_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/kernel/store/seed"
)

func seeded(t *testing.T) (*store.Store, *seed.Data) {
	t.Helper()
	s, err := store.Open(store.Options{
		Path:   filepath.Join(t.TempDir(), "seed.db"),
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	empty, err := seed.IsEmpty(ctx, s)
	if err != nil || !empty {
		t.Fatalf("fresh database IsEmpty = %v, %v; want true, nil", empty, err)
	}
	d, err := seed.Apply(ctx, s)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	empty, err = seed.IsEmpty(ctx, s)
	if err != nil || empty {
		t.Fatalf("seeded database IsEmpty = %v, %v; want false, nil", empty, err)
	}
	return s, d
}

// TestSeedInvariants loads the fixture set and asserts the cross-table rules the rest of the
// system will lean on (data model §10).
func TestSeedInvariants(t *testing.T) {
	ctx := context.Background()
	s, d := seeded(t)

	// Referential integrity across the whole seeded graph, straight from SQLite.
	rows, err := s.Reader().QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("PRAGMA foreign_key_check reported violations in the seeded database")
	}

	// Every ticket's column belongs to the ticket's project (invariant §10.2).
	var mismatched int
	err = s.Reader().QueryRowContext(ctx, `
		SELECT count(*) FROM tickets t JOIN columns c ON c.id = t.column_id
		WHERE c.project_id != t.project_id`).Scan(&mismatched)
	if err != nil || mismatched != 0 {
		t.Fatalf("%d tickets sit in a column of another project (err=%v)", mismatched, err)
	}

	// The column fixtures cover at least one backlog, one running and one done column
	// (invariant §10.3), and the running-category column really carries category 'running'.
	for _, cat := range []domain.ColumnCategory{
		domain.CategoryBacklog, domain.CategoryRunning, domain.CategoryDone,
	} {
		cols, err := s.Columns().ByCategory(ctx, d.Project.ID, cat)
		if err != nil {
			t.Fatal(err)
		}
		if len(cols) == 0 {
			t.Fatalf("seed has no %q column", cat)
		}
	}
	running := d.ColumnByCategory[domain.CategoryRunning]
	if running.Category != domain.CategoryRunning {
		t.Fatalf("running column has category %q", running.Category)
	}

	// Ticket keys agree with their (project key, seq).
	for _, tk := range d.Tickets {
		if want := d.Project.Key + "-" + strconv.FormatInt(tk.Seq, 10); tk.Key != want {
			t.Errorf("ticket key %q; want %q", tk.Key, want)
		}
		if !tk.Priority.IsValid() || !tk.Origin.IsValid() {
			t.Errorf("ticket %s has invalid enum values", tk.Key)
		}
	}
	if len(d.Tickets) < 10 {
		t.Errorf("seed has %d tickets; want ~10", len(d.Tickets))
	}

	// Every run's agent belongs to the run's project, and every run state is one the schema and
	// the domain agree on.
	for _, r := range d.Runs {
		agent, err := s.Agents().ByID(ctx, r.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		if agent.ProjectID != r.ProjectID {
			t.Errorf("run %s uses agent from another project", r.ID)
		}
		if !r.State.IsValid() {
			t.Errorf("run %s has invalid state %q", r.ID, r.State)
		}
	}

	// The running ticket's stream carries its run card.
	stream, err := s.TicketStream().ForTicket(ctx, d.Tickets[5].ID)
	if err != nil {
		t.Fatal(err)
	}
	var hasRunCard bool
	for _, e := range stream {
		if e.Kind == domain.StreamRun && e.RunID != nil {
			hasRunCard = true
		}
	}
	if !hasRunCard {
		t.Error("webhook ticket's stream has no run entry")
	}

	// Agent permissions round-trip as the typed struct, and no fixture agent can merge —
	// there is no such field to even set (brief D6).
	sonnet, err := s.Agents().ByID(ctx, d.Agents[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sonnet.Permissions.EditFiles || sonnet.Permissions.SubmitReviews {
		t.Errorf("agent permissions did not round-trip: %+v", sonnet.Permissions)
	}
}

// TestSeedRefusesNonEmpty documents that Apply on an already-seeded database fails loudly
// instead of half-merging (the unique project key collides first).
func TestSeedRefusesNonEmpty(t *testing.T) {
	ctx := context.Background()
	s, _ := seeded(t)
	if _, err := s.Projects().ByKey(ctx, "PAY"); err != nil {
		t.Fatalf("expected seeded project: %v", err)
	}
	if _, err := seed.Apply(ctx, s); err == nil {
		t.Fatal("second Apply succeeded; it must fail on the unique collision")
	}
	// And it must not have half-merged: still exactly one project.
	projects, err := s.Projects().List(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("after failed re-seed: %d projects, err=%v; want 1, nil", len(projects), err)
	}
}
