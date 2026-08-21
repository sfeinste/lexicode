package domain

import "github.com/oklog/ulid/v2"

// NewID returns a fresh ULID (D-2: IDs are ULIDs stored as TEXT). ULIDs sort by creation time,
// which is why they were chosen over UUIDs: a primary-key scan is a rough chronology. The
// underlying entropy source is monotonic within a millisecond, so IDs generated back-to-back in
// one process still sort in generation order.
func NewID() string {
	return ulid.Make().String()
}
