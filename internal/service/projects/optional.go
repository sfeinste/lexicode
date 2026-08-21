package projects

import "encoding/json"

// OptInt is a tri-state JSON field for PATCH bodies: absent (Set=false, leave unchanged),
// null (Set && Null, clear — for an inheritable setting that means "revert to inherit"), or a
// number (Set && !Null). Plain pointers cannot tell absent from null, and the inheritance
// pattern (data model §1) needs all three.
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

// apply resolves the tri-state against the current pointer: absent keeps cur, null clears,
// a value overrides.
func (o OptInt) apply(cur *int64) *int64 {
	if !o.Set {
		return cur
	}
	if o.Null {
		return nil
	}
	v := o.Value
	return &v
}
