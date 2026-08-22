package wiki

import (
	"context"
	"errors"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/mentionparse"
)

// BacklinkGroup is one source that mentions the page: where the mention lives plus every
// containing paragraph (UI spec §5.6 — a bare list of titles is useless). SourceSlug is set
// for wiki sources, SourceKey for tickets and comments (the ticket the comment sits on),
// so the pane can link back.
type BacklinkGroup struct {
	SourceKind string // wiki | ticket | comment
	SourceID   string
	Title      string
	SourceSlug string
	SourceKey  string
	Paragraphs []string
}

// UnlinkedMention is one page whose plain text contains this page's title outside any
// mention token — one click from becoming a real link. The click edits the SOURCE page:
// the client replaces the first plain occurrence in that page's body with the mention token
// and PATCHes it, which re-derives mentions and appends a version like any body edit.
type UnlinkedMention struct {
	PageID    string
	Slug      string
	Title     string
	Paragraph string
}

// backlinks groups the linked mentions targeting a page by source, resolving each source's
// display identity. Archived wiki sources are skipped (their mention rows are cleared on
// archive, but belt and braces); a source that no longer resolves is dropped.
func (s *Service) backlinks(ctx context.Context, page domain.WikiPage) ([]BacklinkGroup, error) {
	mentions, err := s.st.Mentions().ForTarget(ctx, "wiki", page.ID)
	if err != nil {
		return nil, err
	}
	var out []BacklinkGroup
	index := map[string]int{}
	for _, m := range mentions {
		if !m.Linked {
			continue
		}
		key := m.FromKind + ":" + m.FromID
		if i, ok := index[key]; ok {
			out[i].Paragraphs = append(out[i].Paragraphs, m.ContextText)
			continue
		}
		g, ok, err := s.resolveSource(ctx, m.FromKind, m.FromID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		g.Paragraphs = []string{m.ContextText}
		index[key] = len(out)
		out = append(out, g)
	}
	return out, nil
}

// resolveSource turns a mention source into its display identity.
func (s *Service) resolveSource(ctx context.Context, kind, id string) (BacklinkGroup, bool, error) {
	g := BacklinkGroup{SourceKind: kind, SourceID: id}
	switch kind {
	case "wiki":
		src, err := s.st.Wiki().ByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return g, false, nil
		}
		if err != nil {
			return g, false, err
		}
		if src.ArchivedAt != nil {
			return g, false, nil
		}
		g.Title = src.Title
		g.SourceSlug = src.Slug
	case "ticket":
		tk, err := s.st.Tickets().ByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return g, false, nil
		}
		if err != nil {
			return g, false, err
		}
		g.Title = tk.Title
		g.SourceKey = tk.Key
	case "comment":
		// A comment's from_id is its ticket_stream row; the pane links to the ticket.
		entry, err := s.st.TicketStream().ByID(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return g, false, nil
		}
		if err != nil {
			return g, false, err
		}
		tk, err := s.st.Tickets().ByID(ctx, entry.TicketID)
		if errors.Is(err, store.ErrNotFound) {
			return g, false, nil
		}
		if err != nil {
			return g, false, err
		}
		g.Title = "Comment on " + tk.Title
		g.SourceKey = tk.Key
	default:
		return g, false, nil
	}
	return g, true, nil
}

// unlinkedMentions finds live pages whose plain text contains this page's title without a
// mention token. Search-based, computed at read time (see the package doc): FTS narrows to
// pages matching the title as a phrase, then the plain-occurrence check masks mention
// tokens so a token's label ("@[API runbook](wiki:…)") never counts as unlinked.
func (s *Service) unlinkedMentions(ctx context.Context, page domain.WikiPage) ([]UnlinkedMention, error) {
	title := strings.TrimSpace(page.Title)
	if title == "" {
		return nil, nil
	}
	hits, err := s.st.Wiki().Search(ctx, page.ProjectID, ftsPhrase(title), 100)
	if err != nil {
		return nil, err
	}
	var out []UnlinkedMention
	for _, h := range hits {
		src := h.Page
		if src.ID == page.ID {
			continue
		}
		start, end := mentionparse.FindPlainOccurrence(src.Body, title)
		if start == -1 {
			continue
		}
		out = append(out, UnlinkedMention{
			PageID: src.ID, Slug: src.Slug, Title: src.Title,
			Paragraph: mentionparse.ParagraphAround(src.Body, start, end),
		})
	}
	return out, nil
}

// ftsPhrase quotes a string as one FTS5 phrase ("api runbook"), doubling embedded quotes.
func ftsPhrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
