package triggers

import (
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// Stage 1: MATCH (architecture §8). `event.Kind == trigger.Event` AND `event.ActivityType ∈
// trigger.ActivityTypes` AND every filter passes. A non-match writes nothing — it is not a
// firing, and most events end here for most triggers.

// triggerFilters is triggers.filters (data model §6): {branches:[], paths:[], labels:[]}. An
// absent or empty list is no constraint.
type triggerFilters struct {
	Branches []string `json:"branches,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Labels   []string `json:"labels,omitempty"`
}

// matchStage reports whether the event matches the trigger's WHEN. Malformed stored JSON —
// which save-time validation should have refused — matches nothing.
func matchStage(tr domain.Trigger, e domain.Event, payload map[string]any) bool {
	if e.Kind != tr.Event {
		return false
	}
	// An event addressed to one specific trigger — subject_kind "trigger" with a subject id,
	// the cron source's per-trigger firings (S32) — is matched only by the trigger it names.
	// Deliberately generic (any per-trigger source inherits it): without this, every schedule
	// trigger of a project would fire on every other schedule trigger's minutes, because the
	// expression lives on the trigger, not in the event's kind or activity type.
	if e.SubjectKind == "trigger" && (e.SubjectID == nil || *e.SubjectID != tr.ID) {
		return false
	}
	var kinds []string
	if len(tr.ActivityTypes) > 0 {
		if err := json.Unmarshal(tr.ActivityTypes, &kinds); err != nil {
			return false
		}
	}
	// An empty activity_types list (the schema default) means every activity type of the
	// kind: a trigger stored without narrowing is broad, not inert.
	if len(kinds) > 0 && !stringIn(kinds, e.ActivityType) {
		return false
	}

	var f triggerFilters
	if len(tr.Filters) > 0 {
		if err := json.Unmarshal(tr.Filters, &f); err != nil {
			return false
		}
	}
	if len(f.Branches) > 0 && !anyGlob(f.Branches, branchCandidates(e, payload)) {
		return false
	}
	if len(f.Paths) > 0 && !anyGlob(f.Paths, pathCandidates(payload)) {
		return false
	}
	if len(f.Labels) > 0 && !anyLabel(f.Labels, labelCandidates(payload)) {
		return false
	}
	return true
}

// branchCandidates is the branch a branch filter tests: the payload's pr.branch, falling back
// to the event's subject branch. No branch data → the filter cannot pass.
func branchCandidates(e domain.Event, payload map[string]any) []string {
	var out []string
	if s, ok := lookupPath(payload, "pr.branch").(string); ok && s != "" {
		out = append(out, s)
	}
	if e.SubjectBranch != nil && *e.SubjectBranch != "" {
		out = append(out, *e.SubjectBranch)
	}
	return out
}

// pathCandidates is what a path filter tests. The contracts §4 payload carries file paths in
// two places: comment.path (review comments) and, when a source supplies one, a pr.paths list.
// An event with neither cannot pass a path filter — a filter on data the event does not carry
// is a non-match, never a silent pass.
func pathCandidates(payload map[string]any) []string {
	var out []string
	if s, ok := lookupPath(payload, "comment.path").(string); ok && s != "" {
		out = append(out, s)
	}
	if els, ok := lookupPath(payload, "pr.paths").([]any); ok {
		for _, el := range els {
			if s, ok := el.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// labelCandidates is the union of pr.labels and ticket.labels.
func labelCandidates(payload map[string]any) []string {
	var out []string
	for _, p := range []string{"pr.labels", "ticket.labels"} {
		if els, ok := lookupPath(payload, p).([]any); ok {
			for _, el := range els {
				if s, ok := el.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// anyGlob is any-of semantics over path.Match globs (see globMatch for the dialect): some
// pattern matches some candidate.
func anyGlob(patterns, candidates []string) bool {
	for _, p := range patterns {
		for _, c := range candidates {
			if globMatch(p, c) {
				return true
			}
		}
	}
	return false
}

// anyLabel is exact-match any-of: some wanted label is present.
func anyLabel(wanted, present []string) bool {
	for _, w := range wanted {
		if stringIn(present, w) {
			return true
		}
	}
	return false
}

func stringIn(list []string, s string) bool {
	for _, el := range list {
		if el == s {
			return true
		}
	}
	return false
}
