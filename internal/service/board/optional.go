package board

import "encoding/json"

// OptInt is a tri-state JSON field for PATCH bodies: absent (Set=false, leave unchanged),
// null (Set && Null, clear — a WIP limit reverting to "no limit"), or a number. Plain pointers
// cannot tell absent from null. Same shape as the projects service's OptInt; duplicated rather
// than imported so services do not depend on each other for JSON plumbing.
type OptInt struct {
	Set   bool
	Null  bool
	Value int64
}

// UnmarshalJSON records that the field appeared, and whether it was null.
func (o *OptInt) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// OptStr is the string tri-state: absent, null, or a value. Reordering needs all three —
// absent means "no reorder", null means "move to the front", a value names the column to
// place this one after.
type OptStr struct {
	Set   bool
	Null  bool
	Value string
}

// UnmarshalJSON records that the field appeared, and whether it was null.
func (o *OptStr) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}
