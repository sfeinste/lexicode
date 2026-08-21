package kernel_test

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// TestStoreAccessor pins the contracts §1 shape: the store cmd/lexicode opens is the store every
// module sees, and a kernel built without one (kernel-only tests) reports that as nil rather
// than panicking.
func TestStoreAccessor(t *testing.T) {
	st, err := store.Open(store.Options{
		Path:   filepath.Join(t.TempDir(), "k.db"),
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	k := kernel.New(kernel.Options{Store: st})
	if k.Store() != st {
		t.Fatal("Kernel.Store() is not the store passed in Options")
	}

	if kernel.New(kernel.Options{}).Store() != nil {
		t.Fatal("a kernel without a store must report nil, not a stub")
	}
}
