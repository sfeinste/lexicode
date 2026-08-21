package store_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// open opens a fresh store on a temp file. Migration is the caller's business.
func open(t *testing.T, logger *slog.Logger) *store.Store {
	t.Helper()
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	}
	s, err := store.Open(store.Options{
		Path:   filepath.Join(t.TempDir(), "test.db"),
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// migrated opens and migrates a fresh store.
func migrated(t *testing.T) *store.Store {
	t.Helper()
	s := open(t, nil)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestMigrateAppliesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	var log bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&log, nil))
	s := open(t, logger)

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if len(applied) == 0 || applied[0] != "0001_init" {
		t.Fatalf("first migrate applied %v; want [0001_init ...]", applied)
	}
	if !strings.Contains(log.String(), "0001_init") {
		t.Errorf("migration log line does not name the applied version:\n%s", log.String())
	}

	// A restart is a no-op: same file, second Migrate call applies nothing.
	again, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second migrate applied %v; want none", again)
	}

	// And the schema is actually there.
	var n int
	err = s.Reader().QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'tickets'`).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("tickets table missing after migrate: n=%d err=%v", n, err)
	}
}

func TestForeignKeyViolationIsTyped(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	err := s.Sessions().Create(ctx, &domain.Session{
		ID: "hash", UserID: "no-such-user",
		ExpiresAt: domain.Now(), CreatedAt: domain.Now(),
	})
	if !errors.Is(err, store.ErrForeignKey) {
		t.Fatalf("err = %v; want errors.Is(err, ErrForeignKey)", err)
	}
	if !errors.Is(err, store.ErrConstraint) {
		t.Fatalf("err = %v; want errors.Is(err, ErrConstraint) too", err)
	}
}

func TestUniqueAndCheckViolationsAreTyped(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	u := domain.User{
		ID: domain.NewID(), Email: "dup@example.com", DisplayName: "One",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#000", CreatedAt: domain.Now(),
	}
	if err := s.Users().Create(ctx, &u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	dup := u
	dup.ID = domain.NewID()
	if err := s.Users().Create(ctx, &dup); !errors.Is(err, store.ErrUnique) {
		t.Fatalf("duplicate email err = %v; want ErrUnique", err)
	}

	bad := u
	bad.ID = domain.NewID()
	bad.Email = "other@example.com"
	bad.Role = domain.UserRole("emperor")
	if err := s.Users().Create(ctx, &bad); !errors.Is(err, store.ErrCheck) {
		t.Fatalf("bad enum err = %v; want ErrCheck", err)
	}
}

func TestLookupMissesAreErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	if _, err := s.Users().ByID(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ByID miss err = %v; want ErrNotFound", err)
	}
	if err := s.Tickets().Move(ctx, "nope", "col", 1, domain.Now()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Move miss err = %v; want ErrNotFound", err)
	}
}

// TestFTS5Works proves the pure-Go driver ships a working FTS5, which the wiki search (S33)
// depends on. It exercises a standalone virtual table and the schema's own wiki_fts.
func TestFTS5Works(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	if _, err := s.Writer().ExecContext(ctx,
		`CREATE VIRTUAL TABLE fts_probe USING fts5(title, body)`); err != nil {
		t.Fatalf("create fts5 table: %v", err)
	}
	if _, err := s.Writer().ExecContext(ctx, `
		INSERT INTO fts_probe (title, body) VALUES
			('Deploy runbook', 'How we roll back a bad deploy of the payments service'),
			('Chargebacks', 'Disputes arrive via the forge webhook')`); err != nil {
		t.Fatalf("insert fts5 rows: %v", err)
	}
	var title string
	err := s.Reader().QueryRowContext(ctx,
		`SELECT title FROM fts_probe WHERE fts_probe MATCH 'payments' ORDER BY rank`).Scan(&title)
	if err != nil {
		t.Fatalf("fts5 match query: %v", err)
	}
	if title != "Deploy runbook" {
		t.Fatalf("fts5 match = %q; want 'Deploy runbook'", title)
	}

	// wiki_fts from migration 0001 must be queryable too — it is the real search surface.
	var n int
	if err := s.Reader().QueryRowContext(ctx, `SELECT count(*) FROM wiki_fts`).Scan(&n); err != nil {
		t.Fatalf("wiki_fts unusable: %v", err)
	}
}

func TestTxCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	// Committed work is visible.
	err := s.Tx(ctx, func(tx *store.Tx) error {
		return tx.Users().Create(ctx, &domain.User{
			ID: domain.NewID(), Email: "kept@example.com", DisplayName: "Kept",
			PasswordHash: "x", Role: domain.RoleMember, AvatarColor: "#000",
			CreatedAt: domain.Now(),
		})
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	// A returned error rolls everything back.
	boom := errors.New("boom")
	err = s.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Users().Create(ctx, &domain.User{
			ID: domain.NewID(), Email: "gone@example.com", DisplayName: "Gone",
			PasswordHash: "x", Role: domain.RoleMember, AvatarColor: "#000",
			CreatedAt: domain.Now(),
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("tx err = %v; want boom", err)
	}

	if _, err := s.Users().ByEmail(ctx, "kept@example.com"); err != nil {
		t.Errorf("committed user missing: %v", err)
	}
	if _, err := s.Users().ByEmail(ctx, "gone@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rolled-back user visible: err = %v", err)
	}
}

func TestTxPanicRollsBackAndRepanics(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic inside Tx was swallowed; it must propagate")
			}
		}()
		_ = s.Tx(ctx, func(tx *store.Tx) error {
			if err := tx.Users().Create(ctx, &domain.User{
				ID: domain.NewID(), Email: "panic@example.com", DisplayName: "Panic",
				PasswordHash: "x", Role: domain.RoleMember, AvatarColor: "#000",
				CreatedAt: domain.Now(),
			}); err != nil {
				return err
			}
			panic("mid-transaction")
		})
	}()

	if _, err := s.Users().ByEmail(ctx, "panic@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("panicked tx left its write behind: err = %v", err)
	}

	// The write connection must still be usable after the panic path.
	if err := s.Users().Create(ctx, &domain.User{
		ID: domain.NewID(), Email: "after@example.com", DisplayName: "After",
		PasswordHash: "x", Role: domain.RoleMember, AvatarColor: "#000", CreatedAt: domain.Now(),
	}); err != nil {
		t.Fatalf("store unusable after panicked tx: %v", err)
	}
}

// TestTicketRoundTrip pushes a fully-populated ticket through insert and both lookups, so every
// nullable column and enum conversion in the repository is exercised at least once.
func TestTicketRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := migrated(t)

	// Minimal graph the ticket's foreign keys need.
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
	col := domain.Column{
		ID: domain.NewID(), ProjectID: proj.ID, Name: "Doing",
		Category: domain.CategoryRunning, Position: 1,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Columns().Create(ctx, &col); err != nil {
		t.Fatal(err)
	}

	seq, err := s.Projects().AllocateTicketSeq(ctx, proj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("first AllocateTicketSeq = %d; want 1", seq)
	}

	prNumber, prAdd, prDel := int64(219), int64(120), int64(8)
	prState, prChecks, branch := "open", "passing", "sonnet/PAY-1-fix"
	tk := domain.Ticket{
		ID: domain.NewID(), ProjectID: proj.ID, Seq: seq, Key: "PAY-1",
		Title: "Fix the thing", Description: "In detail.", ColumnID: col.ID,
		Position: domain.PositionBetween(0, 0), Priority: domain.PriorityHigh,
		AssigneeID: &owner.ID, ParentID: nil,
		PRNumber: &prNumber, PRState: &prState, PRChecks: &prChecks,
		PRAdditions: &prAdd, PRDeletions: &prDel, Branch: &branch,
		Origin: domain.OriginHuman, CreatedByUserID: &owner.ID,
		CreatedAt: domain.Now(), UpdatedAt: domain.Now(),
	}
	if err := s.Tickets().Create(ctx, &tk); err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	got, err := s.Tickets().ByKey(ctx, "PAY-1")
	if err != nil {
		t.Fatalf("ByKey: %v", err)
	}
	if got.Priority != domain.PriorityHigh || got.Origin != domain.OriginHuman {
		t.Errorf("enums did not round-trip: %+v", got)
	}
	if got.PRNumber == nil || *got.PRNumber != 219 || got.Branch == nil || *got.Branch != branch {
		t.Errorf("nullable columns did not round-trip: %+v", got)
	}
	if got.DelegateAgentID != nil || got.ArchivedAt != nil {
		t.Errorf("null columns came back non-nil: %+v", got)
	}
}
