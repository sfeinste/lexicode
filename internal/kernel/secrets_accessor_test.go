package kernel_test

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// TestSecretsAccessor pins the contracts §1 shape (S13): the secret store cmd/lexicode opens
// is the one every module sees through Kernel.Secrets(), and a kernel built without one
// (kernel-only tests) reports nil rather than panicking.
func TestSecretsAccessor(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "k.db"), Logger: logger})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	sec, err := secrets.Open(secrets.Options{
		Store: st, KeyPath: filepath.Join(dir, "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatalf("open secrets: %v", err)
	}

	k := kernel.New(kernel.Options{Store: st, Secrets: sec})
	if k.Secrets() != sec {
		t.Fatal("Kernel.Secrets() is not the store passed in Options")
	}

	if kernel.New(kernel.Options{}).Secrets() != nil {
		t.Fatal("a kernel without a secret store must report nil, not a stub")
	}
}
