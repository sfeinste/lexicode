package wiki

import (
	"context"
	"errors"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/mentionparse"
)

// CreateInput is the POST body: a title, an optional parent, an optional starting body.
// Front matter beyond the parent starts at its schema defaults (scope auto, no owner, no
// verified_until, no tags) and is edited on the page.
type CreateInput struct {
	Title    string
	ParentID *string
	Body     string
}

// Create makes a live page at the end of its parent's (or the root's) sibling list, appends
// version 1, and derives its mention rows.
func (s *Service) Create(ctx context.Context, projectKey string, in CreateInput) (domain.WikiPage, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.WikiPage{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.WikiPage{}, &ValidationError{Fields: []httpx.FieldError{
			{Field: "title", Message: "A page needs a title."}}}
	}
	if in.ParentID != nil {
		parent, err := s.st.Wiki().ByID(ctx, *in.ParentID)
		if errors.Is(err, store.ErrNotFound) {
			return domain.WikiPage{}, &ValidationError{Fields: []httpx.FieldError{
				{Field: "parent_id", Message: "The parent page does not exist."}}}
		}
		if err != nil {
			return domain.WikiPage{}, err
		}
		if parent.ProjectID != p.ID || parent.ArchivedAt != nil || parent.State != domain.WikiLive {
			return domain.WikiPage{}, &ValidationError{Fields: []httpx.FieldError{
				{Field: "parent_id", Message: "The parent page does not exist."}}}
		}
		if parent.ParentID != nil {
			return domain.WikiPage{}, &DepthError{
				Detail: "The wiki is two levels deep at most: " + parent.Title +
					" already has a parent, so it cannot have child pages."}
		}
	}
	slug, err := s.uniqueSlug(ctx, p.ID, slugify(title), "")
	if err != nil {
		return domain.WikiPage{}, err
	}
	position, err := s.nextPosition(ctx, p.ID, in.ParentID)
	if err != nil {
		return domain.WikiPage{}, err
	}

	now := s.now()
	page := domain.WikiPage{
		ID: domain.NewID(), ProjectID: p.ID, Slug: slug, Title: title,
		ParentID: in.ParentID, Position: position,
		AgentScope: domain.ScopeAuto, ScopePaths: []string{}, Tags: []string{},
		Body: in.Body, TokenEstimate: EstimateTokens(in.Body),
		State: domain.WikiLive, CreatedAt: now, UpdatedAt: now,
	}
	if uid := s.actorUserID(ctx); uid != "" {
		page.OwnerID = &uid
	}
	mentionRows, err := s.resolveMentions(ctx, p.ID, mentionparse.Parse(in.Body))
	if err != nil {
		return domain.WikiPage{}, err
	}
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Wiki().CreatePage(ctx, &page); err != nil {
			return err
		}
		if err := tx.Wiki().CreateVersion(ctx, s.versionRow(ctx, page, 1)); err != nil {
			return err
		}
		return writeMentions(ctx, tx, page.ID, mentionRows)
	})
	if err != nil {
		return domain.WikiPage{}, err
	}
	if err := s.audit.Write(ctx, "wiki.create",
		audit.Target{Kind: "wiki_page", ID: page.ID, ProjectID: p.ID}, nil, page); err != nil {
		return domain.WikiPage{}, err
	}
	s.emitWiki(ctx, "created", page)
	return page, nil
}

// List returns the project's pages for the tree — live pages plus agent proposals (state
// 'proposed'; the tree renders them with a PROPOSED chip, the accept/dismiss flow is S35),
// archived excluded, bodies included (a wiki is prompt-sized; the tree, mention autocomplete
// and tag index all read from this one payload).
func (s *Service) List(ctx context.Context, projectKey string) ([]domain.WikiPage, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return nil, err
	}
	pages, err := s.st.Wiki().ForProject(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.WikiPage, 0, len(pages))
	for _, pg := range pages {
		if pg.ArchivedAt == nil {
			out = append(out, pg)
		}
	}
	return out, nil
}

// Detail is one page plus everything its screen renders in a single fetch: the latest
// version number, the backlinks pane (linked mentions grouped by source, full paragraphs),
// and the unlinked-mentions disclosure.
type Detail struct {
	Page      domain.WikiPage
	Version   int64
	Backlinks []BacklinkGroup
	Unlinked  []UnlinkedMention
}

// Get returns the page detail by id.
func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	page, err := s.st.Wiki().ByID(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	version, err := s.st.Wiki().LatestVersion(ctx, page.ID)
	if err != nil {
		return Detail{}, err
	}
	backlinks, err := s.backlinks(ctx, page)
	if err != nil {
		return Detail{}, err
	}
	unlinked, err := s.unlinkedMentions(ctx, page)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Page: page, Version: version, Backlinks: backlinks, Unlinked: unlinked}, nil
}

// UpdatePatch is the PATCH body: every field optional, tri-state where null means "clear".
type UpdatePatch struct {
	Title         *string
	Body          *string
	ParentID      OptStr // null → move to the root
	Position      *float64
	OwnerID       OptStr // null → no owner
	VerifiedUntil OptStr // null → not verified
	AgentScope    *string
	ScopePaths    *[]string
	Tags          *[]string
}

// Update applies a patch. Content changes (title or body) append a version — an unchanged
// content save is a no-op that mints nothing. A title change also regenerates the slug and
// rewrites inbound wiki-token labels in other live pages (see the package doc).
func (s *Service) Update(ctx context.Context, id string, patch UpdatePatch) (domain.WikiPage, error) {
	page, err := s.st.Wiki().ByID(ctx, id)
	if err != nil {
		return domain.WikiPage{}, err
	}
	if page.ArchivedAt != nil {
		return domain.WikiPage{}, &ArchivedError{Title: page.Title}
	}
	before := page

	var fields []httpx.FieldError
	renamed := false
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if title == "" {
			fields = append(fields, httpx.FieldError{Field: "title", Message: "A page needs a title."})
		} else if title != page.Title {
			page.Title = title
			renamed = true
		}
	}
	if patch.Body != nil {
		page.Body = *patch.Body
		page.TokenEstimate = EstimateTokens(page.Body)
	}
	if patch.ParentID.Set {
		if patch.ParentID.Null {
			page.ParentID = nil
		} else if err := s.checkParent(ctx, &page, patch.ParentID.Value, &fields); err != nil {
			return domain.WikiPage{}, err
		}
	}
	if patch.Position != nil {
		page.Position = *patch.Position
	}
	if patch.OwnerID.Set {
		if patch.OwnerID.Null {
			page.OwnerID = nil
		} else if _, err := s.st.Users().ByID(ctx, patch.OwnerID.Value); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fields = append(fields, httpx.FieldError{Field: "owner_id", Message: "No such user."})
			} else {
				return domain.WikiPage{}, err
			}
		} else {
			v := patch.OwnerID.Value
			page.OwnerID = &v
		}
	}
	if patch.VerifiedUntil.Set {
		if patch.VerifiedUntil.Null {
			page.VerifiedUntil = nil
		} else if !validDate(patch.VerifiedUntil.Value) {
			fields = append(fields, httpx.FieldError{Field: "verified_until",
				Message: "Use a YYYY-MM-DD date."})
		} else {
			v := patch.VerifiedUntil.Value
			page.VerifiedUntil = &v
		}
	}
	if patch.AgentScope != nil {
		sc := domain.AgentScope(*patch.AgentScope)
		if !sc.IsValid() {
			fields = append(fields, httpx.FieldError{Field: "agent_scope",
				Message: "Scope is one of always, auto, paths, manual, never."})
		} else {
			page.AgentScope = sc
		}
	}
	if patch.ScopePaths != nil {
		page.ScopePaths = *patch.ScopePaths
	}
	if patch.Tags != nil {
		page.Tags = normalizeTags(*patch.Tags)
	}
	if len(fields) > 0 {
		return domain.WikiPage{}, &ValidationError{Fields: fields}
	}

	if renamed {
		slug, err := s.uniqueSlug(ctx, page.ProjectID, slugify(page.Title), page.ID)
		if err != nil {
			return domain.WikiPage{}, err
		}
		page.Slug = slug
	}

	contentChanged := page.Title != before.Title || page.Body != before.Body
	now := s.now()
	page.UpdatedAt = now

	var mentionRows []domain.Mention
	if patch.Body != nil {
		mentionRows, err = s.resolveMentions(ctx, page.ProjectID, mentionparse.Parse(page.Body))
		if err != nil {
			return domain.WikiPage{}, err
		}
	}
	var nextVersion int64
	if contentChanged {
		latest, err := s.st.Wiki().LatestVersion(ctx, page.ID)
		if err != nil {
			return domain.WikiPage{}, err
		}
		nextVersion = latest + 1
	}

	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Wiki().UpdatePage(ctx, &page); err != nil {
			return err
		}
		if contentChanged {
			if err := tx.Wiki().CreateVersion(ctx, s.versionRow(ctx, page, nextVersion)); err != nil {
				return err
			}
		}
		if patch.Body != nil {
			if err := writeMentions(ctx, tx, page.ID, mentionRows); err != nil {
				return err
			}
		}
		if renamed {
			if err := s.rewriteInboundLabels(ctx, tx, page); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.WikiPage{}, err
	}
	if err := s.audit.Write(ctx, "wiki.update",
		audit.Target{Kind: "wiki_page", ID: page.ID, ProjectID: page.ProjectID},
		before, page); err != nil {
		return domain.WikiPage{}, err
	}
	s.emitWiki(ctx, "updated", page)
	return page, nil
}

// Archive is DELETE: sets archived_at, clears the page's outbound mention rows, and — if
// the page had children — lifts them to the root so they never dangle under an archived
// parent. Idempotent.
func (s *Service) Archive(ctx context.Context, id string) (domain.WikiPage, error) {
	page, err := s.st.Wiki().ByID(ctx, id)
	if err != nil {
		return domain.WikiPage{}, err
	}
	if page.ArchivedAt != nil {
		return page, nil
	}
	before := page
	now := s.now()
	page.ArchivedAt = &now
	page.UpdatedAt = now
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Wiki().UpdatePage(ctx, &page); err != nil {
			return err
		}
		if err := writeMentions(ctx, tx, page.ID, nil); err != nil {
			return err
		}
		// Lift children to the root, keeping their relative order.
		children, err := tx.Wiki().ForProject(ctx, page.ProjectID)
		if err != nil {
			return err
		}
		for _, c := range children {
			if c.ParentID == nil || *c.ParentID != page.ID || c.ArchivedAt != nil {
				continue
			}
			c.ParentID = nil
			c.UpdatedAt = now
			if err := tx.Wiki().UpdatePage(ctx, &c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.WikiPage{}, err
	}
	if err := s.audit.Write(ctx, "wiki.archive",
		audit.Target{Kind: "wiki_page", ID: page.ID, ProjectID: page.ProjectID},
		before, page); err != nil {
		return domain.WikiPage{}, err
	}
	s.emitWiki(ctx, "deleted", page)
	return page, nil
}

// checkParent validates a re-parent: the new parent must be a live root page of the same
// project, must not be the page itself, and the page must have no children (two levels).
func (s *Service) checkParent(ctx context.Context, page *domain.WikiPage, parentID string, fields *[]httpx.FieldError) error {
	if parentID == page.ID {
		*fields = append(*fields, httpx.FieldError{Field: "parent_id",
			Message: "A page cannot be its own parent."})
		return nil
	}
	parent, err := s.st.Wiki().ByID(ctx, parentID)
	if errors.Is(err, store.ErrNotFound) {
		*fields = append(*fields, httpx.FieldError{Field: "parent_id",
			Message: "The parent page does not exist."})
		return nil
	}
	if err != nil {
		return err
	}
	if parent.ProjectID != page.ProjectID || parent.ArchivedAt != nil || parent.State != domain.WikiLive {
		*fields = append(*fields, httpx.FieldError{Field: "parent_id",
			Message: "The parent page does not exist."})
		return nil
	}
	if parent.ParentID != nil {
		return &DepthError{Detail: "The wiki is two levels deep at most: " + parent.Title +
			" already has a parent, so it cannot have child pages."}
	}
	hasKids, err := s.st.Wiki().HasChildren(ctx, page.ID)
	if err != nil {
		return err
	}
	if hasKids {
		return &DepthError{Detail: "The wiki is two levels deep at most: " + page.Title +
			" has child pages, so it cannot be nested under another page."}
	}
	v := parentID
	page.ParentID = &v
	return nil
}

// rewriteInboundLabels updates the label of every `@[…](wiki:page.ID)` token stored in other
// live wiki pages' bodies to the page's new title, then re-derives those pages' mention rows
// so the stored backlink paragraphs read the new label too. Mechanical — no versions minted.
func (s *Service) rewriteInboundLabels(ctx context.Context, tx *store.Tx, page domain.WikiPage) error {
	inbound, err := tx.Mentions().ForTarget(ctx, "wiki", page.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range inbound {
		if m.FromKind != "wiki" || m.FromID == page.ID || seen[m.FromID] {
			continue
		}
		seen[m.FromID] = true
		src, err := tx.Wiki().ByID(ctx, m.FromID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		body, changed := mentionparse.RewriteWikiLabels(src.Body, page.ID, page.Title)
		if !changed {
			continue
		}
		src.Body = body
		src.TokenEstimate = EstimateTokens(body)
		src.UpdatedAt = s.now()
		if err := tx.Wiki().UpdatePage(ctx, &src); err != nil {
			return err
		}
		rows, err := s.resolveMentionsTx(ctx, tx, src.ProjectID, mentionparse.Parse(body))
		if err != nil {
			return err
		}
		if err := writeMentions(ctx, tx, src.ID, rows); err != nil {
			return err
		}
	}
	return nil
}

// nextPosition returns max(sibling position)+1 — new pages append to their level.
func (s *Service) nextPosition(ctx context.Context, projectID string, parentID *string) (float64, error) {
	pages, err := s.st.Wiki().ForProject(ctx, projectID)
	if err != nil {
		return 0, err
	}
	max := 0.0
	for _, p := range pages {
		if p.ArchivedAt != nil {
			continue
		}
		sameParent := (p.ParentID == nil && parentID == nil) ||
			(p.ParentID != nil && parentID != nil && *p.ParentID == *parentID)
		if sameParent && p.Position > max {
			max = p.Position
		}
	}
	return max + 1, nil
}

// versionRow snapshots the page as one immutable wiki_versions row, authored by the acting
// human when there is one.
func (s *Service) versionRow(ctx context.Context, page domain.WikiPage, version int64) *domain.WikiVersion {
	v := &domain.WikiVersion{
		ID: domain.NewID(), PageID: page.ID, Version: version,
		Title: page.Title, Body: page.Body,
		FrontMatter: map[string]any{
			"parent_id": page.ParentID, "owner_id": page.OwnerID,
			"verified_until": page.VerifiedUntil, "agent_scope": string(page.AgentScope),
			"paths": page.ScopePaths, "tags": page.Tags,
		},
		CreatedAt: page.UpdatedAt,
	}
	if uid := s.actorUserID(ctx); uid != "" {
		v.AuthorUserID = &uid
	}
	return v
}

// normalizeTags trims, lowercases nothing (tags keep their case for display), drops empties
// and dedupes preserving order.
func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		key := strings.ToLower(t)
		if t == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}
