package tickets

import "encoding/json"

// OptStr is a tri-state JSON string field for PATCH bodies: absent (Set=false, leave
// unchanged), null (Set && Null — unassign, remove the delegate, detach from parent, move to
// the front), or a value. Plain pointers cannot tell absent from null. Same shape as the
// board service's OptStr; duplicated rather than imported so services do not depend on each
// other for JSON plumbing.
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
