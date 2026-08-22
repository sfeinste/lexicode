package triggers

import (
	"encoding/json"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
)

func matchTrigger(event, activityTypes, filters string) domain.Trigger {
	return domain.Trigger{
		Event:         event,
		ActivityTypes: json.RawMessage(activityTypes),
		Filters:       json.RawMessage(filters),
	}
}

func prEvent(kind, activity string) domain.Event {
	return domain.Event{Kind: kind, ActivityType: activity}
}

func TestMatchStage(t *testing.T) {
	payload := samplePayload()
	cases := []struct {
		name    string
		trigger domain.Trigger
		event   domain.Event
		want    bool
	}{
		{"kind and activity match",
			matchTrigger("pull_request", `["opened","synchronize"]`, `{}`),
			prEvent("pull_request", "synchronize"), true},
		{"kind mismatch",
			matchTrigger("pull_request", `["synchronize"]`, `{}`),
			prEvent("issue_comment", "created"), false},
		{"activity not in set",
			matchTrigger("pull_request", `["opened"]`, `{}`),
			prEvent("pull_request", "synchronize"), false},
		{"empty activity set matches every activity of the kind",
			matchTrigger("pull_request", `[]`, `{}`),
			prEvent("pull_request", "closed"), true},
		{"branch glob passes",
			matchTrigger("pull_request", `[]`, `{"branches":["dev/*"]}`),
			prEvent("pull_request", "synchronize"), true},
		{"branch glob fails",
			matchTrigger("pull_request", `[]`, `{"branches":["release/*"]}`),
			prEvent("pull_request", "synchronize"), false},
		{"label filter passes",
			matchTrigger("pull_request", `[]`, `{"labels":["payments"]}`),
			prEvent("pull_request", "synchronize"), true},
		{"label filter fails",
			matchTrigger("pull_request", `[]`, `{"labels":["docs"]}`),
			prEvent("pull_request", "synchronize"), false},
		{"path filter without path data is a non-match, never a silent pass",
			matchTrigger("pull_request", `[]`, `{"paths":["src/*"]}`),
			prEvent("pull_request", "synchronize"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchStage(tc.trigger, tc.event, payload); got != tc.want {
				t.Fatalf("matchStage = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMatchBranchFallsBackToSubject: an event whose payload lacks pr.branch still matches a
// branch filter through the event's subject branch.
func TestMatchBranchFallsBackToSubject(t *testing.T) {
	branch := "dev/PAY-9"
	e := domain.Event{Kind: "pull_request", ActivityType: "opened", SubjectBranch: &branch}
	tr := matchTrigger("pull_request", `[]`, `{"branches":["dev/*"]}`)
	if !matchStage(tr, e, map[string]any{}) {
		t.Fatal("subject branch did not satisfy the branch filter")
	}
}

// TestMatchPathFilterAgainstCommentPath: a review comment's path satisfies a path filter.
func TestMatchPathFilterAgainstCommentPath(t *testing.T) {
	payload := parsePayload(json.RawMessage(`{"comment":{"path":"src/pay/refund.go"}}`))
	tr := matchTrigger("pull_request_review_comment", `["created"]`, `{"paths":["src/pay/*"]}`)
	if !matchStage(tr, prEvent("pull_request_review_comment", "created"), payload) {
		t.Fatal("comment.path did not satisfy the path filter")
	}
}
