// Package secrets is the AES-256-GCM secret store (story S13, decision D-16).
//
// The master key lives at <data_dir>/master.key — the configured data directory, not a
// hardcoded home path — hex-encoded, mode 0600, generated on first boot. A key file readable
// by anyone else refuses boot with the chmod command that fixes it: a lax mode silently
// undoes the whole design, so it is a hard error, never a warning.
//
// Values are write-only above this package. Set, Rename, Delete and List work on metadata
// (Info) and never return a value; the one plaintext reader is Get, called in-process by the
// forge adapter (S14) and container env building (S19). No HTTP handler may call Get or touch
// ciphertext — apilint_test.go in this package enforces that (data model invariant 9).
//
// Failure mode by construction: each value is sealed with a fresh random nonce and the
// secret's ID as GCM additional data, so a swapped or rotated master key — or ciphertext
// copied between rows — fails authentication loudly at Get, naming the secret and telling the
// caller to re-set it. Garbage plaintext is unrepresentable.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// keySize is AES-256.
const keySize = 32

// ErrNameTaken reports a Set or Rename that would collide with a different secret's name in
// the same scope. (The schema's UNIQUE does not cover workspace rows — NULL project_id is
// distinct in SQLite — so the store enforces this itself, uniformly for both scopes.)
var ErrNameTaken = errors.New("a secret with this name already exists in this scope")

// Store encrypts, decrypts and persists secrets. One per process, shared through
// Kernel.Secrets().
type Store struct {
	st      *store.Store
	aead    cipher.AEAD
	keyPath string
	logger  *slog.Logger
	now     func() string
}

// Options configures Open.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// KeyPath is the master key file, <data_dir>/master.key. Required. The parent directory
	// must exist (cmd/lexicode creates the data dir before opening anything).
	KeyPath string
	// Logger receives the "generated master key" line. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means domain.Now.
	Now func() string
}

// Info is a secret as everything above this package sees it: metadata only. There is no value
// field, so a List response cannot leak one even by accident.
type Info struct {
	ID        string             `json:"id"`
	Scope     domain.SecretScope `json:"scope"`
	ProjectID *string            `json:"project_id"`
	Name      string             `json:"name"`
	CreatedBy string             `json:"created_by"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
}

func info(s domain.Secret) Info {
	return Info{ID: s.ID, Scope: s.Scope, ProjectID: s.ProjectID, Name: s.Name,
		CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

// Open loads (or on first run generates) the master key and returns the store. It is called
// during boot, before the listener exists: every error it returns refuses boot.
func Open(opts Options) (*Store, error) {
	if opts.Store == nil || opts.KeyPath == "" {
		return nil, errors.New("secrets: Store and KeyPath are both required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = domain.Now
	}

	key, err := loadOrCreateKey(opts.KeyPath, logger)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: build AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: build GCM: %w", err)
	}
	return &Store{st: opts.Store, aead: aead, keyPath: opts.KeyPath, logger: logger, now: now}, nil
}

// loadOrCreateKey reads the hex-encoded 32-byte key, generating it with mode 0600 on first
// run. An existing file whose mode admits group or world access refuses boot with the exact
// command that fixes it.
func loadOrCreateKey(path string, logger *slog.Logger) ([]byte, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		key := make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("secrets: generate master key: %w", err)
		}
		// O_EXCL: if two processes race here, one of them loses instead of both half-writing.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("secrets: create master key file %s: %w", path, err)
		}
		if _, err := f.Write([]byte(hex.EncodeToString(key) + "\n")); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("secrets: write master key file %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("secrets: close master key file %s: %w", path, err)
		}
		logger.Info("secrets: generated master key", slog.String("path", path))
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: stat master key file %s: %w", path, err)
	}

	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf(
			"secrets: refusing to start: master key file %s has mode %04o, which lets other "+
				"users on this machine read every stored secret; restrict it to the owner and "+
				"start again:\n\n\tchmod 600 %s",
			path, mode, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: read master key file %s: %w", path, err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != keySize {
		return nil, fmt.Errorf(
			"secrets: master key file %s is not a %d-byte hex key; if it was hand-edited or "+
				"truncated, restore it from backup — deleting it generates a NEW key and every "+
				"existing secret becomes undecryptable and must be re-set", path, keySize)
	}
	return key, nil
}

// KeyPath is where the master key lives, for log lines and error messages.
func (s *Store) KeyPath() string { return s.keyPath }

// ---------------------------------------------------------------- mutations -----

// SetInput is one Set call. ProjectID is empty for workspace scope.
type SetInput struct {
	Scope     domain.SecretScope
	ProjectID string
	Name      string
	Value     string
	// CreatedBy stamps created_by on a create; a replace keeps the original creator.
	CreatedBy string
}

// Set creates the named secret or replaces its value — the API's only write shape (D-16:
// values can be set and cleared, never read back). It returns the metadata and whether a new
// row was created (false = replaced).
func (s *Store) Set(ctx context.Context, in SetInput) (Info, bool, error) {
	if err := validScope(in.Scope, in.ProjectID); err != nil {
		return Info{}, false, err
	}
	existing, err := s.st.Secrets().ByName(ctx, in.Scope, in.ProjectID, in.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Info{}, false, err
	}

	if err == nil { // replace: fresh nonce, fresh ciphertext, same row
		existing.Nonce, existing.Ciphertext, err = s.seal(existing.ID, in.Value)
		if err != nil {
			return Info{}, false, err
		}
		existing.UpdatedAt = s.now()
		if err := s.st.Secrets().Update(ctx, &existing); err != nil {
			return Info{}, false, err
		}
		return info(existing), false, nil
	}

	sec := domain.Secret{
		ID: domain.NewID(), Scope: in.Scope, Name: in.Name,
		CreatedBy: in.CreatedBy, CreatedAt: s.now(),
	}
	sec.UpdatedAt = sec.CreatedAt
	if in.Scope == domain.SecretScopeProject {
		pid := in.ProjectID
		sec.ProjectID = &pid
	}
	if sec.Nonce, sec.Ciphertext, err = s.seal(sec.ID, in.Value); err != nil {
		return Info{}, false, err
	}
	if err := s.st.Secrets().Create(ctx, &sec); err != nil {
		if errors.Is(err, store.ErrUnique) {
			// The UNIQUE index caught a project-scope race; report it as the name conflict
			// it is. (Workspace rows rely on the ByName check above.)
			return Info{}, false, ErrNameTaken
		}
		return Info{}, false, err
	}
	return info(sec), true, nil
}

// Rename changes a secret's name. The ciphertext is untouched — the GCM additional data is
// the ID, not the name, precisely so a rename cannot invalidate the value.
func (s *Store) Rename(ctx context.Context, id, name string) (Info, error) {
	sec, err := s.st.Secrets().ByID(ctx, id)
	if err != nil {
		return Info{}, err
	}
	if sec.Name == name {
		return info(sec), nil
	}
	var projectID string
	if sec.ProjectID != nil {
		projectID = *sec.ProjectID
	}
	if other, err := s.st.Secrets().ByName(ctx, sec.Scope, projectID, name); err == nil && other.ID != id {
		return Info{}, ErrNameTaken
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Info{}, err
	}
	sec.Name = name
	sec.UpdatedAt = s.now()
	if err := s.st.Secrets().Update(ctx, &sec); err != nil {
		if errors.Is(err, store.ErrUnique) {
			return Info{}, ErrNameTaken
		}
		return Info{}, err
	}
	return info(sec), nil
}

// Delete removes a secret. It returns the deleted row's metadata for the caller's audit
// entry. A secret something still references (a repo token, S14+) fails with the store's
// ErrForeignKey rather than orphaning the reference.
func (s *Store) Delete(ctx context.Context, id string) (Info, error) {
	sec, err := s.st.Secrets().ByID(ctx, id)
	if err != nil {
		return Info{}, err
	}
	if err := s.st.Secrets().Delete(ctx, id); err != nil {
		return Info{}, err
	}
	return info(sec), nil
}

// ---------------------------------------------------------------- reads -----

// List returns one scope's secrets as metadata, name order. projectID is empty for workspace
// scope. Values are absent by type: Info has no value field.
func (s *Store) List(ctx context.Context, scope domain.SecretScope, projectID string) ([]Info, error) {
	if err := validScope(scope, projectID); err != nil {
		return nil, err
	}
	rows, err := s.st.Secrets().List(ctx, scope, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(rows))
	for _, r := range rows {
		out = append(out, info(r))
	}
	return out, nil
}

// ByID returns one secret's metadata — for ownership checks before a mutation by ID.
func (s *Store) ByID(ctx context.Context, id string) (Info, error) {
	sec, err := s.st.Secrets().ByID(ctx, id)
	if err != nil {
		return Info{}, err
	}
	return info(sec), nil
}

// Get returns the plaintext. It is the only reader of secret values in the entire codebase,
// and it is in-process only: the forge adapter (S14) and container env building (S19) call
// it; no HTTP handler may (apilint_test.go). A value the master key cannot authenticate —
// the key was rotated, replaced or restored from elsewhere — fails loudly, naming the secret,
// rather than ever returning garbage.
func (s *Store) Get(ctx context.Context, id string) (string, error) {
	sec, err := s.st.Secrets().ByID(ctx, id)
	if err != nil {
		return "", err
	}
	plain, err := s.aead.Open(nil, sec.Nonce, sec.Ciphertext, []byte(sec.ID))
	if err != nil {
		return "", fmt.Errorf(
			"secrets: cannot decrypt secret %q (%s): the master key at %s is not the key "+
				"this secret was encrypted with — it was likely rotated or replaced; re-set "+
				"the secret with a fresh value to repair it: %w",
			sec.Name, sec.ID, s.keyPath, err)
	}
	return string(plain), nil
}

// seal encrypts value with a fresh random nonce, binding the ciphertext to the secret's ID
// via GCM additional data.
func (s *Store) seal(id, value string) (nonce, ciphertext []byte, err error) {
	nonce = make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	return nonce, s.aead.Seal(nil, nonce, []byte(value), []byte(id)), nil
}

func validScope(scope domain.SecretScope, projectID string) error {
	switch scope {
	case domain.SecretScopeProject:
		if projectID == "" {
			return errors.New("secrets: project scope requires a project id")
		}
	case domain.SecretScopeWorkspace:
		if projectID != "" {
			return errors.New("secrets: workspace scope takes no project id")
		}
	default:
		return fmt.Errorf("secrets: unknown scope %q", scope)
	}
	return nil
}
