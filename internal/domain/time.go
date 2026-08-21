package domain

import (
	"fmt"
	"time"
)

// TimeFormat is how every timestamp is stored: RFC3339 in UTC, with millisecond precision so
// that rows created in the same second still sort in creation order. RFC3339 with a fractional
// second is still RFC3339; time.Parse(time.RFC3339, …) reads it back without loss of validity.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Now is the current time as a storable timestamp.
func Now() string {
	return FormatTime(time.Now())
}

// FormatTime renders t as an RFC3339 UTC string, the only timestamp shape the database holds.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeFormat)
}

// ParseTime reads a stored timestamp back. It accepts any RFC3339 string, not only the exact
// shape FormatTime writes, so that timestamps originating outside this process (forge payloads,
// hand-edited fixtures) still round-trip.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

// Day renders t as the 'YYYY-MM-DD' UTC day key the budget ledger uses.
func Day(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}
