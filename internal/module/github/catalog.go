package github

import "github.com/spruce/lexicode/internal/kernel/ports"

// pollSourceID is the EventSource port ID and events.source of everything the poller emits.
const pollSourceID = "github.poll"

// Event kinds the poller emits (architecture §7's table). They double as the resource part of
// the dedupe key.
const (
	kindPullRequest   = "pull_request"
	kindReview        = "pull_request_review"
	kindReviewComment = "pull_request_review_comment"
	kindIssueComment  = "issue_comment"
	kindCheckSuite    = "check_suite"
)

// Shared field groups. The pr group appears on every descriptor: every kind the poller emits
// is anchored to a pull request, and conditions on review/comment/check events routinely
// address the PR they sit on (pr.branch, pr.labels). A plain-issue comment has no pr
// sub-object at runtime; its pr.* paths then evaluate to nil, which every operator has defined
// behaviour for (contracts §8).
var (
	prFields = []ports.PayloadField{
		{Path: "pr.number", Type: "number"},
		{Path: "pr.title", Type: "text"},
		{Path: "pr.author", Type: "text"},
		{Path: "pr.author_kind", Type: "enum"},
		{Path: "pr.branch", Type: "text"},
		{Path: "pr.base", Type: "text"},
		{Path: "pr.draft", Type: "bool"},
		{Path: "pr.merged", Type: "bool"},
		{Path: "pr.state", Type: "enum"},
		{Path: "pr.additions", Type: "number"},
		{Path: "pr.deletions", Type: "number"},
		{Path: "pr.files_changed", Type: "number"},
		{Path: "pr.labels", Type: "set"},
		{Path: "pr.body", Type: "text"},
		{Path: "pr.url", Type: "text"},
	}
	repoFields = []ports.PayloadField{
		{Path: "repo.owner", Type: "text"},
		{Path: "repo.name", Type: "text"},
		{Path: "repo.default_branch", Type: "text"},
	}
	actorFields = []ports.PayloadField{
		{Path: "actor.kind", Type: "enum"},
		{Path: "actor.login", Type: "text"},
		{Path: "actor.agent", Type: "text"},
	}

	branchFilter = ports.FilterField{Key: "branches", Kind: "glob-list", Label: "Branches"}
	pathFilter   = ports.FilterField{Key: "paths", Kind: "glob-list", Label: "Paths"}
	labelFilter  = ports.FilterField{Key: "labels", Kind: "label-list", Label: "Labels"}
)

func fields(groups ...[]ports.PayloadField) []ports.PayloadField {
	var out []ports.PayloadField
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

// catalog is the github.poll EventCatalog (contracts §2.1): what the trigger editor may offer.
// Activity types are the derived ones from architecture §7 — one per emitted event. A review's
// approved/changes_requested/commented and a check suite's success/failure are payload state
// (review.state, check.conclusion), not extra activity types: an event carries exactly one
// activity type, and the enum-typed payload field is how a trigger narrows on the outcome.
func catalog() ports.EventCatalog {
	return ports.EventCatalog{Events: []ports.EventDescriptor{
		{
			Kind:  kindPullRequest,
			Label: "Pull request",
			ActivityTypes: []ports.ActivityType{
				{Value: "opened", Label: "opened"},
				{Value: "synchronize", Label: "pushed to",
					Help: "New commits on the head branch — distinguishing this from opened is where the runaway loop lives."},
				{Value: "ready_for_review", Label: "marked ready for review"},
				{Value: "closed", Label: "closed",
					Help: "pr.merged distinguishes a merge from a close."},
			},
			Filters:    []ports.FilterField{branchFilter, pathFilter, labelFilter},
			Fields:     fields(prFields, repoFields, actorFields),
			SubjectKey: "pr:{{pr.number}}",
		},
		{
			Kind:  kindReview,
			Label: "Pull request review",
			ActivityTypes: []ports.ActivityType{
				{Value: "submitted", Label: "submitted",
					Help: "review.state carries approved, changes_requested or commented."},
			},
			Filters: []ports.FilterField{branchFilter, labelFilter},
			Fields: fields([]ports.PayloadField{
				{Path: "review.id", Type: "text"},
				{Path: "review.author", Type: "text"},
				{Path: "review.state", Type: "enum"},
				{Path: "review.body", Type: "text"},
			}, prFields, repoFields, actorFields),
			SubjectKey: "pr:{{pr.number}}",
		},
		{
			Kind:  kindReviewComment,
			Label: "Review comment",
			ActivityTypes: []ports.ActivityType{
				{Value: "created", Label: "created"},
			},
			Filters: []ports.FilterField{branchFilter, labelFilter},
			Fields: fields([]ports.PayloadField{
				{Path: "comment.id", Type: "text"},
				{Path: "comment.author", Type: "text"},
				{Path: "comment.body", Type: "text"},
				{Path: "comment.path", Type: "text"},
				{Path: "comment.line", Type: "number"},
			}, prFields, repoFields, actorFields),
			SubjectKey: "pr:{{pr.number}}",
		},
		{
			Kind:  kindIssueComment,
			Label: "Issue or PR comment",
			ActivityTypes: []ports.ActivityType{
				{Value: "created", Label: "created"},
			},
			Filters: []ports.FilterField{branchFilter, labelFilter},
			Fields: fields([]ports.PayloadField{
				{Path: "comment.id", Type: "text"},
				{Path: "comment.author", Type: "text"},
				{Path: "comment.body", Type: "text"},
			}, prFields, repoFields, actorFields),
			SubjectKey: "pr:{{pr.number}}",
		},
		{
			Kind:  kindCheckSuite,
			Label: "Check suite",
			ActivityTypes: []ports.ActivityType{
				{Value: "completed", Label: "completed",
					Help: "check.conclusion carries success or failure."},
			},
			Filters: []ports.FilterField{branchFilter, labelFilter},
			Fields: fields([]ports.PayloadField{
				{Path: "check.suite_id", Type: "text"},
				{Path: "check.name", Type: "text"},
				{Path: "check.conclusion", Type: "enum"},
				{Path: "check.url", Type: "text"},
			}, prFields, repoFields, actorFields),
			SubjectKey: "pr:{{pr.number}}",
		},
	}}
}
