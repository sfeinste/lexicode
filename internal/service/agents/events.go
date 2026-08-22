package agents

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
)

// emitAgent publishes an `agent` bus event (SSE type "agent.<activity>") on the project
// topic. Best-effort: the mutation is committed and audited by the time this runs, so a
// failure is logged, never unwound.
func (s *Service) emitAgent(ctx context.Context, activity string, a domain.Agent) {
	if s.bus == nil {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"agent": map[string]any{
			"id": a.ID, "name": a.Name, "role": a.Role, "model": a.Model,
			"autonomy": string(a.Autonomy), "enabled": a.Enabled,
			"archived": a.ArchivedAt != nil,
		},
	})
	if err != nil {
		s.logger.Error("agents: marshal event payload failed", slog.String("error", err.Error()))
		return
	}
	pid, aid := a.ProjectID, a.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "agent", ActivityType: activity,
		SubjectKind: "agent", SubjectID: &aid,
		Payload: payload, OccurredAt: s.now(),
	}
	if actor, ok := auth.ActorFrom(ctx); ok {
		e.ActorKind = actor.Kind
		if actor.ID != "" {
			id := actor.ID
			e.ActorID = &id
		}
	}
	if err := s.bus.Emit(ctx, e); err != nil {
		s.logger.Error("agents: emit failed",
			slog.String("kind", "agent."+activity), slog.String("error", err.Error()))
	}
}
