package agents

import (
	"context"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// SaveDirective saves the agent's directive. Append-only versioning with a no-op guard: when
// the body equals the current version's body byte-for-byte, nothing is written and the current
// version comes back unchanged — an idle autosave or a reflexive ⌘S can never mint an empty
// diff. A changed body appends version N+1 and moves agents.directive_version_id.
func (s *Service) SaveDirective(ctx context.Context, agentID, body, note string) (domain.AgentDirective, bool, error) {
	a, err := s.st.Agents().ByID(ctx, agentID)
	if err != nil {
		return domain.AgentDirective{}, false, err
	}
	if a.ArchivedAt != nil {
		return domain.AgentDirective{}, false, &ArchivedError{Name: a.Name}
	}

	var current *domain.AgentDirective
	if a.DirectiveVersionID != nil {
		d, err := s.st.Directives().ByID(ctx, *a.DirectiveVersionID)
		if err != nil {
			return domain.AgentDirective{}, false, err
		}
		current = &d
	}
	if current != nil && current.Body == body {
		return *current, false, nil // unchanged — no new version (S16 acceptance)
	}

	now := s.now()
	next := domain.AgentDirective{
		ID: domain.NewID(), AgentID: a.ID, Version: 1,
		Body: body, TokenEstimate: EstimateTokens(body),
		Note: strings.TrimSpace(note), CreatedAt: now,
	}
	if current != nil {
		next.Version = current.Version + 1
	}
	if uid := s.actorUserID(ctx); uid != "" {
		next.AuthorID = &uid
	}
	err = s.st.Tx(ctx, func(tx *store.Tx) error {
		if err := tx.Directives().Create(ctx, &next); err != nil {
			return err
		}
		a.DirectiveVersionID = &next.ID
		a.UpdatedAt = now
		return tx.Agents().Update(ctx, &a)
	})
	if err != nil {
		return domain.AgentDirective{}, false, err
	}
	if err := s.audit.Write(ctx, "agent.directive.save",
		audit.Target{Kind: "agent", ID: a.ID, ProjectID: a.ProjectID},
		current, next); err != nil {
		return domain.AgentDirective{}, false, err
	}
	s.emitAgent(ctx, "updated", a)
	return next, true, nil
}

// Directives returns an agent's version list, newest first, bodies included (the diff view
// compares any two without another round trip; directives are prompt-sized).
func (s *Service) Directives(ctx context.Context, agentID string) ([]domain.AgentDirective, error) {
	if _, err := s.st.Agents().ByID(ctx, agentID); err != nil {
		return nil, err
	}
	return s.st.Directives().ForAgent(ctx, agentID)
}

// DirectiveVersion returns one version's full content.
func (s *Service) DirectiveVersion(ctx context.Context, agentID string, version int64) (domain.AgentDirective, error) {
	if _, err := s.st.Agents().ByID(ctx, agentID); err != nil {
		return domain.AgentDirective{}, err
	}
	return s.st.Directives().ByAgentVersion(ctx, agentID, version)
}
