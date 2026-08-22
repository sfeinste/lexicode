package triggers

import "testing"

// TestOperatorCatalogComplete pins the S29 operator catalog to the evaluation map: every
// §4.1 operator the engine evaluates is offered to the editor, and the editor offers
// nothing the engine cannot evaluate.
func TestOperatorCatalogComplete(t *testing.T) {
	offered := map[string]bool{}
	for _, o := range operatorCatalog {
		if offered[o.Op] {
			t.Errorf("operator %q listed twice", o.Op)
		}
		offered[o.Op] = true
		if _, known := operators[o.Op]; !known {
			t.Errorf("catalog offers %q but the engine cannot evaluate it", o.Op)
		}
	}
	for op := range operators {
		if !offered[op] {
			t.Errorf("engine evaluates %q but the catalog does not offer it", op)
		}
	}
}
