// Package wiki is the wiki domain service (story S33): pages with the full front-matter set
// (title, parent, owner, verified_until, agent_scope, paths, tags), a two-level tree with
// fractional drag positions, append-only versions on every content save, FTS5 search as the
// primary navigation, `@`-mentions between pages/tickets/agents with the full containing
// paragraph, and the backlinks read (linked + unlinked).
//
// The rules this package exists to protect:
//
//   - Two levels, never three: a page whose parent already has a parent is refused with the
//     409 `wiki_depth_exceeded` problem, as is re-parenting a page that has children of its
//     own. The schema comments this invariant; this service enforces it.
//   - Versions are append-only and content-addressed by change: saving an unchanged
//     title+body is a no-op that mints no version (same guard as agent directives, S16).
//     Front-matter-only edits (scope, tags, owner, verified_until, position, parent) do not
//     version either — versions exist to diff content.
//   - Renaming a page rewrites the label of every inbound `@[label](wiki:id)` token in other
//     live wiki pages' bodies (the id keeps the link itself stable) and re-derives those
//     pages' mention rows so backlink paragraphs stay correct. The rewrite is mechanical, so
//     it mints no versions on the source pages. Tokens in ticket descriptions and comments
//     keep their stored label until that body's next edit — the link still resolves by id.
//   - DELETE is archive (archived_at), never row deletion — disappearance without data loss.
//     An archived page drops out of the tree, search and mention resolution, and its own
//     outbound mention rows are cleared.
//   - Unlinked mentions are a read-time, search-based query (FTS candidates → plain-text
//     confirmation outside mention tokens), never stored rows: they can appear and disappear
//     as other pages' bodies change, and storage would need invalidation on every save.
//   - Every mutation writes the audit log and emits a bus event (`wiki.created`,
//     `wiki.updated`, `wiki.deleted`).
package wiki

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/bus"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Service is the wiki service. Construct with New.
type Service struct {
	st     *store.Store
	audit  *audit.Writer
	bus    *bus.Bus
	logger *slog.Logger
	now    func() string
}

// Options configures New.
type Options struct {
	// Store is the open, migrated database. Required.
	Store *store.Store
	// Audit is the audit-log writer. Required — every mutation writes an entry.
	Audit *audit.Writer
	// Bus emits internal events for mutations. Nil (tests) skips emission.
	Bus *bus.Bus
	// Logger receives failure lines. Nil means slog.Default().
	Logger *slog.Logger
	// Now overrides the clock in tests. Nil means domain.Now.
	Now func() string
}

// New builds the service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = domain.Now
	}
	return &Service{st: opts.Store, audit: opts.Audit, bus: opts.Bus, logger: logger, now: now}
}

// ---------------------------------------------------------------- errors -----

// ValidationError carries field-level problems up to the HTTP layer as a 400 validation_failed.
type ValidationError struct{ Fields []httpx.FieldError }

// Error names the invalid fields.
func (e *ValidationError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return "invalid fields: " + strings.Join(names, ", ")
}

// DepthError is the two-level rule refusing a third level — the 409 `wiki_depth_exceeded`
// typed problem. Detail distinguishes the two ways to hit it.
type DepthError struct{ Detail string }

// Error returns the detail.
func (e *DepthError) Error() string { return e.Detail }

// ArchivedError is a mutation aimed at an archived page — 409 `wiki_page_archived`.
type ArchivedError struct{ Title string }

// Error names the page.
func (e *ArchivedError) Error() string { return "page is archived: " + e.Title }

// ---------------------------------------------------------------- helpers -----

// EstimateTokens is the documented chars/4 heuristic, shared with directives (S16).
func EstimateTokens(body string) int64 { return int64(len(body) / 4) }

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "page"
	}
	if len(s) > 64 {
		s = strings.Trim(s[:64], "-")
	}
	return s
}

// uniqueSlug returns base, or base-2, base-3, … — the first slug free in the project.
// takenBy may hold the page's own id: keeping your slug on a save is never a collision.
func (s *Service) uniqueSlug(ctx context.Context, projectID, base, selfID string) (string, error) {
	slug := base
	for attempt := 2; attempt <= 50; attempt++ {
		existing, err := s.st.Wiki().BySlug(ctx, projectID, slug)
		if errors.Is(err, store.ErrNotFound) || (err == nil && existing.ID == selfID) {
			return slug, nil
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		slug = fmt.Sprintf("%s-%d", base, attempt)
	}
	return "", fmt.Errorf("wiki: no free slug for %q", base)
}

// actorUserID returns the acting human's id, "" for agent/system actors.
func (s *Service) actorUserID(ctx context.Context) string {
	if u, ok := auth.UserFrom(ctx); ok {
		return u.ID
	}
	return ""
}

// validDate accepts the front-matter date format for verified_until (a plain day).
func validDate(v string) bool {
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}
