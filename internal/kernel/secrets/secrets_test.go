package secrets_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// env is a migrated store plus a user and a project for foreign keys, and a data dir for the
// master key file.
type env struct {
	t       *testing.T
	st      *store.Store
	dataDir string
	userID  string
	projID  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "s13.db"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := domain.Now()
	u := domain.User{ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada",
		PasswordHash: "x", Role: domain.RoleOwner, AvatarColor: "#7c5cff", CreatedAt: now}
	if err := st.Users().Create(ctx, &u); err != nil {
		t.Fatal(err)
	}
	p := domain.Project{ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#7c5cff",
		OwnerID: u.ID, CreatedAt: now, UpdatedAt: now}
	if err := st.Projects().Create(ctx, &p); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, dataDir: dir, userID: u.ID, projID: p.ID}
}

func (e *env) keyPath() string { return filepath.Join(e.dataDir, "master.key") }

func (e *env) open() *secrets.Store {
	e.t.Helper()
	s, err := secrets.Open(secrets.Options{
		Store:   e.st,
		KeyPath: e.keyPath(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		e.t.Fatalf("secrets.Open: %v", err)
	}
	return s
}

func (e *env) projectSet(s *secrets.Store, name, value string) secrets.Info {
	e.t.Helper()
	inf, _, err := s.Set(context.Background(), secrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: e.projID,
		Name: name, Value: value, CreatedBy: e.userID,
	})
	if err != nil {
		e.t.Fatalf("Set(%s): %v", name, err)
	}
	return inf
}

// TestRoundTripReplaceDelete is the S13 definition-of-done test 3: set → Get round-trips,
// list never contains values, replace overwrites, delete removes.
func TestRoundTripReplaceDelete(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	ctx := context.Background()

	// set → Get round-trips.
	inf := e.projectSet(s, "GITHUB_TOKEN", "ghp_original_value")
	got, err := s.Get(ctx, inf.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "ghp_original_value" {
		t.Fatalf("Get = %q, want the value that was set", got)
	}

	// list carries names and dates, never values — asserted on the actual serialised bytes.
	list, err := s.List(ctx, domain.SecretScopeProject, e.projID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("List = %+v, want the one secret by name", list)
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ghp_original_value") || strings.Contains(string(raw), "value") {
		t.Fatalf("serialised list leaks a value or has a value field: %s", raw)
	}

	// replace overwrites: same name, same row, new value.
	inf2, created, err := s.Set(ctx, secrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: e.projID,
		Name: "GITHUB_TOKEN", Value: "ghp_replaced_value", CreatedBy: e.userID,
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if created {
		t.Fatal("replace reported created = true, want a replace of the existing row")
	}
	if inf2.ID != inf.ID {
		t.Fatalf("replace made a new row (%s -> %s), want the same row", inf.ID, inf2.ID)
	}
	if got, _ := s.Get(ctx, inf.ID); got != "ghp_replaced_value" {
		t.Fatalf("Get after replace = %q, want the new value", got)
	}

	// delete removes.
	if _, err := s.Delete(ctx, inf.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, inf.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	if list, _ := s.List(ctx, domain.SecretScopeProject, e.projID); len(list) != 0 {
		t.Fatalf("List after delete = %+v, want empty", list)
	}
}

// TestKeyGeneratedWithTightMode: first run creates <data_dir>/master.key with mode 0600.
func TestKeyGeneratedWithTightMode(t *testing.T) {
	e := newEnv(t)
	_ = e.open()
	fi, err := os.Stat(e.keyPath())
	if err != nil {
		t.Fatalf("master key was not created: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("master key mode = %04o, want 0600", mode)
	}
}

// TestWorldReadableKeyRefusesBoot is definition-of-done test 4: a key file wider than 0600
// prevents boot with an actionable message.
func TestWorldReadableKeyRefusesBoot(t *testing.T) {
	e := newEnv(t)
	_ = e.open() // generate the key
	if err := os.Chmod(e.keyPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := secrets.Open(secrets.Options{Store: e.st, KeyPath: e.keyPath(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil {
		t.Fatal("Open succeeded with a 0644 key file, want a boot refusal")
	}
	msg := err.Error()
	for _, want := range []string{"0644", "chmod 600", e.keyPath()} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal message %q does not mention %q — it must be actionable", msg, want)
		}
	}
}

// TestRotatedKeyFailsLoudly is definition-of-done test 5: overwrite master.key with a
// different key and Get fails naming the secret — never returns garbage.
func TestRotatedKeyFailsLoudly(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	inf := e.projectSet(s, "DEPLOY_KEY", "super secret value")

	// Rotate: overwrite the key file with a fresh, different 32-byte key.
	other := strings.Repeat("ab", 32)
	if raw, _ := os.ReadFile(e.keyPath()); strings.TrimSpace(string(raw)) == other {
		other = strings.Repeat("cd", 32)
	}
	if _, err := hex.DecodeString(other); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.keyPath(), []byte(other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s2 := e.open() // reopen over the rotated key, as a restarted process would
	_, err := s2.Get(context.Background(), inf.ID)
	if err == nil {
		t.Fatal("Get succeeded with a rotated master key, want a loud failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "DEPLOY_KEY") {
		t.Errorf("decrypt error %q does not name the secret", msg)
	}
	if !strings.Contains(msg, "re-set") {
		t.Errorf("decrypt error %q does not suggest re-setting the secret", msg)
	}
}

// TestGarbledKeyFileRefusesBoot: a key file that is not a 32-byte hex key refuses boot with
// a message that warns against deleting it casually.
func TestGarbledKeyFileRefusesBoot(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.keyPath(), []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := secrets.Open(secrets.Options{Store: e.st, KeyPath: e.keyPath(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil || !strings.Contains(err.Error(), "hex key") {
		t.Fatalf("Open = %v, want a refusal describing the malformed key file", err)
	}
}

// TestRenameKeepsValue: rename changes the name only; the value still decrypts (the GCM
// additional data is the ID, not the name).
func TestRenameKeepsValue(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	ctx := context.Background()
	inf := e.projectSet(s, "OLD_NAME", "v1")

	renamed, err := s.Rename(ctx, inf.ID, "NEW_NAME")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != "NEW_NAME" {
		t.Fatalf("renamed.Name = %q", renamed.Name)
	}
	if got, err := s.Get(ctx, inf.ID); err != nil || got != "v1" {
		t.Fatalf("Get after rename = %q, %v; want the original value", got, err)
	}
}

// TestNameCollisions: Set cannot silently create a duplicate via Rename, and workspace-scope
// names are unique even though SQLite's UNIQUE ignores NULL project_id rows.
func TestNameCollisions(t *testing.T) {
	e := newEnv(t)
	s := e.open()
	ctx := context.Background()

	a := e.projectSet(s, "A", "1")
	e.projectSet(s, "B", "2")

	if _, err := s.Rename(ctx, a.ID, "B"); !errors.Is(err, secrets.ErrNameTaken) {
		t.Fatalf("Rename onto an existing name = %v, want ErrNameTaken", err)
	}

	// Workspace scope: two Sets of the same name are one row, not NULL-project duplicates.
	w1, created1, err := s.Set(ctx, secrets.SetInput{Scope: domain.SecretScopeWorkspace,
		Name: "SHARED", Value: "x", CreatedBy: e.userID})
	if err != nil || !created1 {
		t.Fatalf("workspace set 1: created=%v err=%v", created1, err)
	}
	w2, created2, err := s.Set(ctx, secrets.SetInput{Scope: domain.SecretScopeWorkspace,
		Name: "SHARED", Value: "y", CreatedBy: e.userID})
	if err != nil || created2 {
		t.Fatalf("workspace set 2: created=%v err=%v, want a replace", created2, err)
	}
	if w1.ID != w2.ID {
		t.Fatalf("workspace re-set made a duplicate row (%s, %s)", w1.ID, w2.ID)
	}
	if got, _ := s.Get(ctx, w1.ID); got != "y" {
		t.Fatalf("workspace Get = %q, want the replaced value", got)
	}
}
