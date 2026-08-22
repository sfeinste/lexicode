package contextres

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
)

// Start launches the daily verified_until job (architecture §11): on boot and every 24h,
// demote `always` pages past their verification date to `auto`. The demotion is a real data
// change — the page row's agent_scope flips, so the next resolve simply no longer sees an
// always page — not a display rule. Stops when ctx is done; Wait blocks until drained.
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.DemoteExpired(ctx)
		t := time.NewTicker(s.tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.DemoteExpired(ctx)
			}
		}
	}()
}

// Wait blocks until the job goroutine has exited (call after the ctx passed to Start is done).
func (s *Service) Wait() { s.wg.Wait() }

// DemoteExpired runs one demotion pass: every live `always` page whose verified_until is
// before today becomes `auto`, with demoted_at/demoted_from set, an audit row written, and
// the page owner notified. "Verified until 2026-11-01" holds through that day — the page
// demotes the day after, matching the S33 red-date rule.
func (s *Service) DemoteExpired(ctx context.Context) {
	now := s.now().UTC()
	today := now.Format("2006-01-02")
	pages, err := s.st.Wiki().ExpiredAlways(ctx, today)
	if err != nil {
		s.logger.Error("contextres: expired-page query failed", slog.String("error", err.Error()))
		return
	}
	for _, pg := range pages {
		if err := s.demoteOne(ctx, pg, now); err != nil {
			s.logger.Error("contextres: demotion failed",
				slog.String("page", pg.ID), slog.String("error", err.Error()))
		}
	}
}

func (s *Service) demoteOne(ctx context.Context, pg domain.WikiPage, now time.Time) error {
	at := domain.FormatTime(now)
	before := map[string]any{"agent_scope": string(pg.AgentScope), "verified_until": pg.VerifiedUntil}

	from := string(pg.AgentScope)
	pg.AgentScope = domain.ScopeAuto
	pg.DemotedAt = &at
	pg.DemotedFrom = &from
	pg.UpdatedAt = at
	if err := s.st.Wiki().UpdatePage(ctx, &pg); err != nil {
		return err
	}

	if s.audit != nil {
		verified := ""
		if pg.VerifiedUntil != nil {
			verified = *pg.VerifiedUntil
		}
		if err := s.audit.Write(ctx, "wiki.page.demote",
			audit.Target{Kind: "wiki_page", ID: pg.ID, ProjectID: pg.ProjectID,
				Note: "verified_until " + verified + " expired"},
			before,
			map[string]any{"agent_scope": "auto", "demoted_at": at, "demoted_from": from},
		); err != nil {
			s.logger.Error("contextres: demotion audit failed",
				slog.String("page", pg.ID), slog.String("error", err.Error()))
		}
	}

	// Notify the page owner (architecture §11: "notifies the page owner"). No owner, no row.
	if s.notify != nil && pg.OwnerID != nil {
		verified := ""
		if pg.VerifiedUntil != nil {
			verified = *pg.VerifiedUntil
		}
		if err := s.notify(ctx, domain.Notification{
			UserID: *pg.OwnerID, ProjectID: pg.ProjectID, Flavor: domain.FlavorReview,
			Title: "Always-on wiki page demoted: " + pg.Title,
			Body: "Its verification date (" + verified + ") passed, so it no longer " +
				"steers every run. Re-verify the page to restore always-on scope.",
		}); err != nil {
			s.logger.Error("contextres: demotion notification failed",
				slog.String("page", pg.ID), slog.String("error", err.Error()))
		}
	}

	s.emitDemoted(ctx, pg)
	return nil
}

// emitDemoted publishes a wiki.updated event so open wiki trees refresh their ScopeBadges.
// Best-effort, like the wiki service's own emitter.
func (s *Service) emitDemoted(ctx context.Context, pg domain.WikiPage) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"page": map[string]any{
			"id": pg.ID, "slug": pg.Slug, "title": pg.Title,
			"agent_scope": string(pg.AgentScope), "state": string(pg.State),
			"archived": pg.ArchivedAt != nil, "demoted_from": pg.DemotedFrom,
		},
	})
	if err != nil {
		s.logger.Error("contextres: marshal event payload failed", slog.String("error", err.Error()))
		return
	}
	pid, id := pg.ProjectID, pg.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "wiki", ActivityType: "updated",
		SubjectKind: "wiki_page", SubjectID: &id,
		ActorKind: domain.ActorSystem,
		Payload:   payload, OccurredAt: domain.Now(),
	}
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("contextres: emit failed", slog.String("error", err.Error()))
	}
}
