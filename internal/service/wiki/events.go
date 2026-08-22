package wiki

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
)

// emitWiki publishes a `wiki` bus event (SSE type "wiki.<activity>": created, updated,
// deleted; "wiki.proposed" belongs to the S21 proposal writer) on the project topic.
// Best-effort: the mutation is committed and audited by the time this runs, so a failure is
// logged, never unwound.
func (s *Service) emitWiki(ctx context.Context, activity string, p domain.WikiPage) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"page": map[string]any{
			"id": p.ID, "slug": p.Slug, "title": p.Title,
			"agent_scope": string(p.AgentScope), "state": string(p.State),
			"archived": p.ArchivedAt != nil,
		},
	})
	if err != nil {
		s.logger.Error("wiki: marshal event payload failed", slog.String("error", err.Error()))
		return
	}
	pid, id := p.ProjectID, p.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "wiki", ActivityType: activity,
		SubjectKind: "wiki_page", SubjectID: &id,
		Payload: payload, OccurredAt: s.now(),
	}
	if actor, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = actor.Kind
		if actor.ID != "" {
			aid := actor.ID
			e.ActorID = &aid
		}
	}
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("wiki: emit failed",
			slog.String("kind", "wiki."+activity), slog.String("error", err.Error()))
	}
}
