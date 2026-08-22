package triggers

import (
	"strings"
	"testing"
)

func TestInterpolateLooksUpPaths(t *testing.T) {
	payload := samplePayload()
	got, warnings := interpolate(
		"Review PR #{{pr.number}} on {{pr.branch}} by {{actor.agent}} ({{pr.files_changed}} files, draft={{pr.draft}})",
		payload)
	want := "Review PR #219 on dev/PAY-14 by dev (7 files, draft=false)"
	if got != want {
		t.Fatalf("interpolate = %q, want %q", got, want)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
}

// TestInterpolateUnknownPath: unknown → "" plus a warning that names the path — the visible
// warning the acceptance criteria require.
func TestInterpolateUnknownPath(t *testing.T) {
	got, warnings := interpolate("fix {{pr.nonexistent}} now", samplePayload())
	if got != "fix  now" {
		t.Fatalf("interpolate = %q, want %q", got, "fix  now")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown path {{pr.nonexistent}}") {
		t.Fatalf("warnings = %v, want one naming pr.nonexistent", warnings)
	}
}

// TestInterpolateRejectsControlFlow is the story's explicit test: no construct resembling
// control flow is accepted — each renders as empty with a warning, and is never evaluated.
func TestInterpolateRejectsControlFlow(t *testing.T) {
	payload := samplePayload()
	cases := []string{
		"{{#if pr.draft}}skip{{/if}}",
		"{{pr.title|uppercase}}",
		"{{ pr.draft ? 'a' : 'b' }}",
		"{{#each pr.labels}}x{{/each}}",
		"{{pr.number + 1}}",
		"{{lookup pr 'title'}}",
		"{{{{pr.title}}}}",
		"{{eval('1+1')}}",
	}
	for _, tmpl := range cases {
		t.Run(tmpl, func(t *testing.T) {
			got, warnings := interpolate(tmpl, payload)
			if len(warnings) == 0 {
				t.Fatalf("interpolate(%q) accepted a control-flow construct (no warning)", tmpl)
			}
			// Nothing may have been evaluated: no payload value and no computed result
			// appears in the output. (Literal text between the rejected constructs may
			// remain — the construct itself renders empty, like an unknown path.)
			for _, leaked := range []string{"219", "220", "Add idempotency keys", "ADD IDEMPOTENCY", "payments"} {
				if strings.Contains(got, leaked) {
					t.Fatalf("interpolate(%q) = %q — evaluated something (%q leaked)", tmpl, got, leaked)
				}
			}
		})
	}
}

func TestInterpolateLiteralText(t *testing.T) {
	// Unclosed braces are literal text, not an error; single braces are untouched.
	got, warnings := interpolate("a {{pr.number", samplePayload())
	if got != "a {{pr.number" || len(warnings) != 0 {
		t.Fatalf("interpolate = %q (warnings %v), want the literal back", got, warnings)
	}
	got, _ = interpolate("no templates {here}", samplePayload())
	if got != "no templates {here}" {
		t.Fatalf("interpolate = %q, want the literal back", got)
	}
}

func TestInterpolateCompositeValues(t *testing.T) {
	got, warnings := interpolate("labels: {{pr.labels}}", samplePayload())
	if got != `labels: ["payments","agent"]` {
		t.Fatalf("interpolate = %q", got)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
}
