package triggers

import (
	"encoding/json"
	"path"
	"strings"
)

// The condition tree (architecture §8, contracts §4.1): `{all:[…]}` | `{any:[…]}` |
// `{field, op, value}`. Evaluation is total — it cannot error, cannot loop, and has no
// expression language underneath. An unknown payload path yields nil, and every operator has
// defined nil behaviour: false for everything except text.is_empty, which is true on nil
// (contracts §8's parenthetical, taken literally — including set.is_empty, which is false on
// nil: a set that is absent from this event kind is unknown, not known-empty; only the present
// empty set satisfies it).

// condNode is one tree node. Exactly one of All, Any or (Field, Op) is meaningful; validation
// enforces that at save time, and evaluation treats a malformed node as false.
type condNode struct {
	All   []condNode      `json:"all,omitempty"`
	Any   []condNode      `json:"any,omitempty"`
	Field string          `json:"field,omitempty"`
	Op    string          `json:"op,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// evalConditions evaluates a stored condition tree against a normalized payload. Malformed
// JSON — which save-time validation should have refused — evaluates false, never panics: an
// event must not be able to wedge the engine.
func evalConditions(raw json.RawMessage, payload map[string]any) bool {
	if len(raw) == 0 {
		return true
	}
	var node condNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return false
	}
	return evalNode(node, payload)
}

func evalNode(n condNode, payload map[string]any) bool {
	switch {
	case n.All != nil:
		for _, c := range n.All {
			if !evalNode(c, payload) {
				return false
			}
		}
		return true // {"all":[]} is vacuously true — the schema's default conditions
	case n.Any != nil:
		for _, c := range n.Any {
			if evalNode(c, payload) {
				return true
			}
		}
		return false // {"any":[]} is vacuously false
	default:
		return evalLeaf(n, payload)
	}
}

// lookupPath walks a dotted path through the payload. Anything missing — or a segment that is
// not an object — yields nil, the defined "unknown" (contracts §4).
func lookupPath(payload map[string]any, p string) any {
	if p == "" {
		return nil
	}
	var cur any = payload
	for _, seg := range strings.Split(p, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[seg]
		if !ok {
			return nil
		}
	}
	return cur
}

// operators is every contracts §4.1 operator. An operator absent from this map is unknown:
// save-time validation refuses it, and a stored rule that somehow carries one evaluates false.
var operators = map[string]func(got any, want json.RawMessage) bool{
	// ---- text ----
	"text.is":     func(got any, want json.RawMessage) bool { s, w, ok := textPair(got, want); return ok && s == w },
	"text.is_not": func(got any, want json.RawMessage) bool { s, w, ok := textPair(got, want); return ok && s != w },
	"text.contains": func(got any, want json.RawMessage) bool {
		s, w, ok := textPair(got, want)
		return ok && strings.Contains(s, w)
	},
	"text.not_contains": func(got any, want json.RawMessage) bool {
		s, w, ok := textPair(got, want)
		return ok && !strings.Contains(s, w)
	},
	"text.starts_with": func(got any, want json.RawMessage) bool {
		s, w, ok := textPair(got, want)
		return ok && strings.HasPrefix(s, w)
	},
	"text.matches_glob": func(got any, want json.RawMessage) bool {
		s, w, ok := textPair(got, want)
		if !ok {
			return false
		}
		return globMatch(w, s)
	},
	// text.is_empty is the one operator that is true on nil: "no body" and "empty body" read
	// the same to a rule author. A present non-text value is not empty, so it is false.
	"text.is_empty": func(got any, _ json.RawMessage) bool {
		if got == nil {
			return true
		}
		s, ok := got.(string)
		return ok && s == ""
	},

	// ---- number ----
	"number.eq":  numberOp(func(a, b float64) bool { return a == b }),
	"number.gt":  numberOp(func(a, b float64) bool { return a > b }),
	"number.gte": numberOp(func(a, b float64) bool { return a >= b }),
	"number.lt":  numberOp(func(a, b float64) bool { return a < b }),
	"number.lte": numberOp(func(a, b float64) bool { return a <= b }),

	// ---- enum ---- (string identity; is_not is false on nil — an unknown value is not
	// known to differ)
	"enum.is":     func(got any, want json.RawMessage) bool { s, w, ok := textPair(got, want); return ok && s == w },
	"enum.is_not": func(got any, want json.RawMessage) bool { s, w, ok := textPair(got, want); return ok && s != w },
	"enum.in": func(got any, want json.RawMessage) bool {
		s, ok := got.(string)
		if !ok {
			return false
		}
		var opts []string
		if err := json.Unmarshal(want, &opts); err != nil {
			return false
		}
		for _, o := range opts {
			if s == o {
				return true
			}
		}
		return false
	},

	// ---- bool ----
	"bool.is": func(got any, want json.RawMessage) bool {
		b, ok := got.(bool)
		if !ok {
			return false
		}
		var w bool
		if err := json.Unmarshal(want, &w); err != nil {
			return false
		}
		return b == w
	},

	// ---- set ----
	"set.includes": func(got any, want json.RawMessage) bool {
		els, w, ok := setPair(got, want)
		return ok && contains(els, w)
	},
	// set.excludes is false on nil: the set is unknown, so nothing is known to be excluded.
	"set.excludes": func(got any, want json.RawMessage) bool {
		els, w, ok := setPair(got, want)
		return ok && !contains(els, w)
	},
	// set.is_empty is false on nil (see the package note above): only a present, empty set
	// satisfies it.
	"set.is_empty": func(got any, _ json.RawMessage) bool {
		els, ok := got.([]any)
		return ok && len(els) == 0
	},

	// ---- actor ---- (evaluated against the payload's actor sub-object; see evalLeaf)
	"actor.is_agent": func(got any, _ json.RawMessage) bool { return actorKind(got) == "agent" },
	"actor.is_human": func(got any, _ json.RawMessage) bool { return actorKind(got) == "human" },
	// actor.is matches the actor's agent name or login — "the Reviewer agent" and "spruce"
	// both address the acting identity.
	"actor.is": func(got any, want json.RawMessage) bool {
		var w string
		if err := json.Unmarshal(want, &w); err != nil || w == "" {
			return false
		}
		m, ok := got.(map[string]any)
		if !ok {
			if s, isStr := got.(string); isStr {
				return s == w
			}
			return false
		}
		if s, _ := m["agent"].(string); s != "" && s == w {
			return true
		}
		s, _ := m["login"].(string)
		return s != "" && s == w
	},
}

func evalLeaf(n condNode, payload map[string]any) bool {
	fn, ok := operators[n.Op]
	if !ok {
		return false
	}
	field := n.Field
	// actor.* operators default their field to the payload's actor sub-object, so the rule
	// editor can offer "author is an agent" without a field picker.
	if field == "" && strings.HasPrefix(n.Op, "actor.") {
		field = "actor"
	}
	return fn(lookupPath(payload, field), n.Value)
}

// textPair unpacks a string payload value and a string condition value; ok is false when
// either side is not text (nil included).
func textPair(got any, want json.RawMessage) (string, string, bool) {
	s, ok := got.(string)
	if !ok {
		return "", "", false
	}
	var w string
	if err := json.Unmarshal(want, &w); err != nil {
		return "", "", false
	}
	return s, w, true
}

func numberOp(cmp func(a, b float64) bool) func(any, json.RawMessage) bool {
	return func(got any, want json.RawMessage) bool {
		a, ok := toNumber(got)
		if !ok {
			return false
		}
		var b float64
		if err := json.Unmarshal(want, &b); err != nil {
			return false
		}
		return cmp(a, b)
	}
}

func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

func setPair(got any, want json.RawMessage) ([]any, string, bool) {
	els, ok := got.([]any)
	if !ok {
		return nil, "", false
	}
	var w string
	if err := json.Unmarshal(want, &w); err != nil {
		return nil, "", false
	}
	return els, w, true
}

func contains(els []any, w string) bool {
	for _, el := range els {
		if s, ok := el.(string); ok && s == w {
			return true
		}
	}
	return false
}

func actorKind(got any) string {
	switch v := got.(type) {
	case map[string]any:
		s, _ := v["kind"].(string)
		return s
	case string:
		// The rule addressed actor.kind directly rather than the actor object.
		return v
	default:
		return ""
	}
}

// globMatch is the one glob dialect everywhere in the trigger engine: stdlib path.Match —
// `*` matches any run of non-`/` characters, `?` one character, `[...]` a class. There is no
// `**`; a pattern that should cross directories says so with `/` segments. A malformed
// pattern matches nothing.
func globMatch(pattern, s string) bool {
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}
