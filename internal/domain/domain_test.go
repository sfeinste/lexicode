package domain_test

import (
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
)

func TestNewIDIsSortableAndUnique(t *testing.T) {
	prev := ""
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := domain.NewID()
		if len(id) != 26 {
			t.Fatalf("ULID length %d: %q", len(id), id)
		}
		if seen[id] {
			t.Fatalf("duplicate ULID %q", id)
		}
		seen[id] = true
		if id < prev {
			t.Fatalf("ULIDs out of order: %q after %q", id, prev)
		}
		prev = id
	}
}

func TestTimeRoundTrip(t *testing.T) {
	in := time.Date(2026, 8, 21, 17, 4, 5, 987_000_000, time.FixedZone("X", 3600))
	s := domain.FormatTime(in)
	if s != "2026-08-21T16:04:05.987Z" {
		t.Fatalf("FormatTime = %q", s)
	}
	back, err := domain.ParseTime(s)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(in) {
		t.Fatalf("round trip lost time: %v != %v", back, in)
	}
	// Plain RFC3339 without fraction parses too.
	if _, err := domain.ParseTime("2026-08-21T16:04:05Z"); err != nil {
		t.Fatal(err)
	}
	if d := domain.Day(in); d != "2026-08-21" {
		t.Fatalf("Day = %q", d)
	}
}

func TestPositionBetween(t *testing.T) {
	first := domain.PositionBetween(0, 0)
	if first <= 0 {
		t.Fatalf("first position %v; want > 0", first)
	}
	appended := domain.PositionBetween(first, 0)
	if appended <= first {
		t.Fatalf("append %v not after %v", appended, first)
	}
	prepended := domain.PositionBetween(0, first)
	if prepended <= 0 || prepended >= first {
		t.Fatalf("prepend %v not before %v", prepended, first)
	}
	mid := domain.PositionBetween(first, appended)
	if mid <= first || mid >= appended {
		t.Fatalf("midpoint %v not between %v and %v", mid, first, appended)
	}
}

func TestEnumsMatchSchema(t *testing.T) {
	// Spot-check each enum: a schema value validates, a stranger does not.
	if !domain.RunRunning.IsValid() || domain.RunState("sprinting").IsValid() {
		t.Error("RunState.IsValid wrong")
	}
	if !domain.CategoryRunning.IsValid() || domain.ColumnCategory("doing").IsValid() {
		t.Error("ColumnCategory.IsValid wrong")
	}
	if !domain.PriorityUrgent.IsValid() || domain.Priority("critical").IsValid() {
		t.Error("Priority.IsValid wrong")
	}
	if !domain.FiringBudgetExceeded.IsValid() || domain.FiringOutcome("failed").IsValid() {
		t.Error("FiringOutcome.IsValid wrong")
	}
	if !domain.ActivityProvision.IsValid() || domain.ActivityType("step").IsValid() {
		t.Error("ActivityType.IsValid wrong")
	}
	if !domain.RunCompleted.Terminal() || domain.RunNeedsInput.Terminal() {
		t.Error("RunState.Terminal wrong")
	}
}
