package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Conversion helpers between domain values and SQL values. Nullable columns are pointers on the
// domain side; JSON columns cross as TEXT and are (un)marshalled here so that no repository ever
// hands raw JSON to a caller where the data model defines a shape.

// nullStr renders *string as a bindable value: nil → NULL.
func nullStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullInt renders *int64 as a bindable value: nil → NULL.
func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullBool renders *bool as a bindable INTEGER: nil → NULL.
func nullBool(p *bool) any {
	if p == nil {
		return nil
	}
	return boolInt(*p)
}

// boolInt renders bool as the INTEGER SQLite stores.
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// strPtr converts a scanned NullString back to *string.
func strPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

// intPtr converts a scanned NullInt64 back to *int64.
func intPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

// boolPtr converts a scanned NullInt64 back to *bool.
func boolPtr(n sql.NullInt64) *bool {
	if !n.Valid {
		return nil
	}
	v := n.Int64 != 0
	return &v
}

// jsonText marshals a typed JSON-column value for binding. A nil map/slice still renders as a
// valid JSON document, which the schema's json_valid CHECKs require.
func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal json column: %w", err)
	}
	return string(b), nil
}

// jsonScan unmarshals a JSON column into a typed value.
func jsonScan(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fmt.Errorf("unmarshal json column: %w", err)
	}
	return nil
}

// rawText renders a json.RawMessage column for binding, defaulting empty to the given document
// (usually "{}") so the json_valid CHECK holds.
func rawText(raw []byte, empty string) string {
	if len(raw) == 0 {
		return empty
	}
	return string(raw)
}

// nullRawText renders an optional raw JSON column: empty → NULL.
func nullRawText(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
