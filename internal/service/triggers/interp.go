package triggers

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// {{...}} interpolation (architecture §8, brief §6.6): path lookup only, never control flow in
// a string. The only construct this file evaluates is a dotted payload path between double
// braces. Anything else between braces — `{{#if …}}`, `{{x|upper}}`, `{{ x ? y : z }}`, nested
// braces — is not a template feature waiting to be implemented; it is rejected: rendered as
// the empty string with a warning naming it, exactly like an unknown path, and never
// evaluated.

// pathPattern is the accepted form: dotted identifiers, matching contracts §4's field
// vocabulary (pr.files_changed, actor.login, ...).
var pathPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// interpolate renders tmpl against the normalized payload. Every `{{path}}` is replaced by the
// path's value; an unknown path renders as "" and adds a warning; a non-path construct renders
// as "" and adds a warning. The warnings end up on the firing row, which is what makes a
// silently-empty prompt debuggable (UI spec §4.2).
func interpolate(tmpl string, payload map[string]any) (string, []string) {
	var out strings.Builder
	var warnings []string
	rest := tmpl
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:start])
		rest = rest[start+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			// An unclosed {{ is literal text, not a template error.
			out.WriteString("{{")
			out.WriteString(rest)
			break
		}
		inner := rest[:end]
		rest = rest[end+2:]
		p := strings.TrimSpace(inner)
		if !pathPattern.MatchString(p) {
			warnings = append(warnings,
				"{{"+inner+"}} is not a payload path — templates are interpolation-only, "+
					"control flow is never evaluated; rendered as empty")
			continue
		}
		v := lookupPath(payload, p)
		if v == nil {
			warnings = append(warnings, "unknown path {{"+p+"}} rendered as empty")
			continue
		}
		out.WriteString(renderValue(v))
	}
	return out.String(), warnings
}

// renderValue formats one payload value for a string context: text verbatim, numbers without a
// trailing .0, booleans as true/false, and composite values as compact JSON.
func renderValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}
