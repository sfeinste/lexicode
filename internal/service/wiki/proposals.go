package wiki

import (
	"context"
	"errors"
	"fmt"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/mentionparse"
)

// This file is the S35 review flow for agent proposals — pages the S21 propose_wiki_page
// tool wrote in state 'proposed' (interaction rule 10: never auto-written; a human accepts,
// edits or dismisses).
//
//   - Accepting a create-proposal flips the same row live (its version-1 snapshot already
//     exists) and derives its mention rows, which the proposal writer deliberately skipped.
//   - Accepting an edit-proposal runs the three-way check first: the target page's latest
//     version must still equal proposed_base_version. A target that moved on returns
//     ConflictError (HTTP 409 wiki_proposal_conflict) naming both versions — the reviewer
//     resolves by editing the proposal, never by silent clobbering. A clean accept applies
//     the proposal body to the target as a new version attributed to the proposing run,
//     then archives the proposal row.
//   - Dismissing archives the proposal row (archived_at — DELETE is archive everywhere in
//     the wiki, so the dismissal leaves the row *and* an audit entry behind).
//
// Proposal rows are archived on accept/dismiss, never deleted: the run's wiki_proposal
// output keeps pointing at a real row, and the audit trail keeps its subject.

// NotAProposalError is an accept/dismiss aimed at a page that is not a pending proposal —
// HTTP 409 `wiki_not_a_proposal`.
type NotAProposalError struct{ Title string }

// Error names the page.
func (e *NotAProposalError) Error() string { return "not a pending proposal: " + e.Title }

// ConflictError is the three-way check failing: the target page advanced past the version
// the proposal was written against — HTTP 409 `wiki_proposal_conflict`.
type ConflictError struct {
	TargetTitle    string
	BaseVersion    int64
	CurrentVersion int64
}

// Error names both versions.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("proposal conflict: %s is at version %d, the proposal was written against version %d",
		e.TargetTitle, e.CurrentVersion, e.BaseVersion)
}

// AcceptProposal accepts the proposal with this page id and returns the resulting live
// page: the proposal itself for a create-proposal, the updated target for an edit-proposal.
func (s *Service) AcceptProposal(ctx context.Context, id string) (domain.WikiPage, error) {
	page, err := s.st.Wiki().ByID(ctx, id)
	if err != nil {
		return domain.WikiPage{}, err
	}
	if page.State != domain.WikiProposed || page.ArchivedAt != nil {
		return domain.WikiPage{}, &NotAProposalError{Title: page.Title}
	}
	if page.ProposalTargetID != nil {
		return s.acceptEditProposal(ctx, page)
	}
	return s.acceptCreateProposal(ctx, page)
}

// acceptCreateProposal makes the proposal row itself live, at the end of its sibling list,
// with mention rows derived from its body.
func (s *Service) acceptCreateProposal(ctx context.Context, page domain.WikiPage) (domain.WikiPage, error) {
	before := page
	position, err := s.nextPosition(ctx, page.ProjectID, page.ParentID)
	if err != nil {
		return domain.WikiPage{}, err
	}
	mentionRows, err := s.resolveMentions(ctx, page.ProjectID, mentionparse.Parse(page.Body))
	if err != nil {
		return domain.WikiPage{}, err
	}
	page.State = domain.WikiLive
	page.Position = position
	page.UpdatedAt = s.now()
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Wiki().UpdatePage(ctx, &page); err != nil {
			return err
		}
		return writeMentions(ctx, tx, page.ID, mentionRows)
	})
	if err != nil {
		return domain.WikiPage{}, err
	}
	if err := s.audit.Write(ctx, "wiki.proposal_accept",
		audit.Target{Kind: "wiki_page", ID: page.ID, ProjectID: page.ProjectID},
		before, page); err != nil {
		return domain.WikiPage{}, err
	}
	s.emitWiki(ctx, "updated", page)
	return page, nil
}

// acceptEditProposal applies the proposal body to its target page as a new version — after
// the three-way check — and archives the proposal row.
func (s *Service) acceptEditProposal(ctx context.Context, page domain.WikiPage) (domain.WikiPage, error) {
	target, err := s.st.Wiki().ByID(ctx, *page.ProposalTargetID)
	if err != nil {
		return domain.WikiPage{}, err
	}
	if target.ArchivedAt != nil {
		return domain.WikiPage{}, &ArchivedError{Title: target.Title}
	}
	current, err := s.st.Wiki().LatestVersion(ctx, target.ID)
	if err != nil {
		return domain.WikiPage{}, err
	}
	base := int64(0)
	if page.ProposedBaseVersion != nil {
		base = *page.ProposedBaseVersion
	}
	if current != base {
		return domain.WikiPage{}, &ConflictError{
			TargetTitle: target.Title, BaseVersion: base, CurrentVersion: current,
		}
	}

	beforeTarget := target
	now := s.now()
	target.Body = page.Body
	target.TokenEstimate = EstimateTokens(page.Body)
	target.UpdatedAt = now

	mentionRows, err := s.resolveMentions(ctx, target.ProjectID, mentionparse.Parse(target.Body))
	if err != nil {
		return domain.WikiPage{}, err
	}
	beforeProposal := page
	page.ArchivedAt = &now
	page.UpdatedAt = now

	version := s.versionRow(ctx, target, current+1)
	// The applied content is the proposing run's work — attribute the version to it (the
	// reviewer's identity is on the audit entry).
	version.AuthorUserID = nil
	version.AuthorRunID = page.ProposedByRunID

	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Wiki().UpdatePage(ctx, &target); err != nil {
			return err
		}
		if err := tx.Wiki().CreateVersion(ctx, version); err != nil {
			return err
		}
		if err := writeMentions(ctx, tx, target.ID, mentionRows); err != nil {
			return err
		}
		// The proposal row retires: archived, its own (empty) mention rows cleared.
		if err := tx.Wiki().UpdatePage(ctx, &page); err != nil {
			return err
		}
		return writeMentions(ctx, tx, page.ID, nil)
	})
	if err != nil {
		return domain.WikiPage{}, err
	}
	if err := s.audit.Write(ctx, "wiki.proposal_accept",
		audit.Target{Kind: "wiki_page", ID: target.ID, ProjectID: target.ProjectID},
		beforeTarget, target); err != nil {
		return domain.WikiPage{}, err
	}
	if err := s.audit.Write(ctx, "wiki.proposal_retire",
		audit.Target{Kind: "wiki_page", ID: page.ID, ProjectID: page.ProjectID},
		beforeProposal, page); err != nil {
		return domain.WikiPage{}, err
	}
	s.emitWiki(ctx, "updated", target)
	s.emitWiki(ctx, "deleted", page)
	return target, nil
}

// DismissProposal archives the proposal row and leaves an audit entry — the S35 acceptance:
// dismissing leaves a trace, never a silent disappearance.
func (s *Service) DismissProposal(ctx context.Context, id string) error {
	page, err := s.st.Wiki().ByID(ctx, id)
	if err != nil {
		return err
	}
	if page.State != domain.WikiProposed || page.ArchivedAt != nil {
		return &NotAProposalError{Title: page.Title}
	}
	before := page
	now := s.now()
	page.ArchivedAt = &now
	page.UpdatedAt = now
	if err := s.st.Wiki().UpdatePage(ctx, &page); err != nil {
		return err
	}
	if err := s.audit.Write(ctx, "wiki.proposal_dismiss",
		audit.Target{Kind: "wiki_page", ID: page.ID, ProjectID: page.ProjectID},
		before, page); err != nil {
		return err
	}
	s.emitWiki(ctx, "deleted", page)
	return nil
}

// ProposalInfo is what the proposal review view needs beyond the page row: why and by which
// run, and — for edit-proposals — the target with the bodies the diff and the three-way
// warning render from.
type ProposalInfo struct {
	Reason string
	RunID  *string
	// Edit-proposals only (nil target fields for create-proposals):
	TargetID       *string
	TargetSlug     string
	TargetTitle    string
	TargetBody     string // the target's CURRENT body — what the diff renders against
	BaseVersion    int64  // what the proposal was written against
	CurrentVersion int64  // the target's latest version now
	BaseBody       string // the base version's body — the three-way conflict view's anchor
}

// proposalInfo assembles ProposalInfo for a proposed page. A dangling or archived target
// still returns the reason/run — the review view degrades to the full-body rendering.
func (s *Service) proposalInfo(ctx context.Context, page domain.WikiPage) (*ProposalInfo, error) {
	info := &ProposalInfo{RunID: page.ProposedByRunID}
	if page.ProposedReason != nil {
		info.Reason = *page.ProposedReason
	}
	if page.ProposalTargetID == nil {
		return info, nil
	}
	target, err := s.st.Wiki().ByID(ctx, *page.ProposalTargetID)
	if errors.Is(err, store.ErrNotFound) {
		return info, nil
	}
	if err != nil {
		return nil, err
	}
	info.TargetID = &target.ID
	info.TargetSlug = target.Slug
	info.TargetTitle = target.Title
	info.TargetBody = target.Body
	if page.ProposedBaseVersion != nil {
		info.BaseVersion = *page.ProposedBaseVersion
	}
	current, err := s.st.Wiki().LatestVersion(ctx, target.ID)
	if err != nil {
		return nil, err
	}
	info.CurrentVersion = current
	if info.BaseVersion > 0 {
		if v, err := s.st.Wiki().Version(ctx, target.ID, info.BaseVersion); err == nil {
			info.BaseBody = v.Body
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	return info, nil
}
