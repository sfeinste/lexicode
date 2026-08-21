package kernel_test

import (
	"testing"

	"github.com/spruce/lexicode/internal/kernel"
	"github.com/spruce/lexicode/internal/kernel/bus"
)

// TestBusAccessor pins the contracts §1 shape: the bus cmd/lexicode constructs is the bus every
// module sees, and a kernel built without one (kernel-only tests) reports that as nil rather
// than panicking.
func TestBusAccessor(t *testing.T) {
	b := bus.New(bus.Options{})
	if kernel.New(kernel.Options{Bus: b}).Bus() != b {
		t.Fatal("Kernel.Bus() is not the bus passed in Options")
	}
	if kernel.New(kernel.Options{}).Bus() != nil {
		t.Fatal("a kernel without a bus must report nil, not a stub")
	}
}
