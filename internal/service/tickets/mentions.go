package tickets

import (
	"context"
	"errors"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/mentionparse"
)

// Mention wire format (S12): see the mentionparse package, the one parser shared with the
// wiki service (S33) so ticket and wiki bodies can never drift. parseMentions finds the
// tokens; resolveMentions validates the targets and turns them into mentions rows with the
// containing paragraph as context (data model §5).

// parsedMention is one token found in a body, with the paragraph that contains it.
type parsedMention = mentionparse.Parsed

// parseMentions extracts every mention token from a markdown body, in order.
func parseMentions(body string) []parsedMention {
	return mentionparse.Parse(body)
}

// resolveMentions validates parsed mentions against the database and returns the mentions
// rows to write. Rules:
//
//   - user: must exist. Users are workspace-level (light auth, no per-project membership
//     table in V1).
//   - agent: must exist, belong to the project, and not be archived.
//   - ticket: must exist and belong to the project.
//   - wiki: must exist, belong to the project, and not be archived (S33 — the validation
//     the S12 comment promised once the wiki service landed).
//
// A token whose target does not resolve is dropped silently: a stale mention must not make
// the comment or description unsaveable. Agents that resolve are also returned separately —
// the comment path stages a run per distinct mentioned agent.
func (s *Service) resolveMentions(ctx context.Context, projectID string, parsed []parsedMention) ([]domain.Mention, []domain.Agent, error) {
	var rows []domain.Mention
	var agents []domain.Agent
	seenAgent := map[string]bool{}
	for _, p := range parsed {
		switch p.Kind {
		case "user":
			if _, err := s.st.Users().ByID(ctx, p.ID); errors.Is(err, store.ErrNotFound) {
				continue
			} else if err != nil {
				return nil, nil, err
			}
		case "agent":
			a, err := s.st.Agents().ByID(ctx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			if a.ProjectID != projectID || a.ArchivedAt != nil {
				continue
			}
			if !seenAgent[a.ID] {
				seenAgent[a.ID] = true
				agents = append(agents, a)
			}
		case "ticket":
			tk, err := s.st.Tickets().ByID(ctx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			if tk.ProjectID != projectID {
				continue
			}
		case "wiki":
			wp, err := s.st.Wiki().ByID(ctx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			if wp.ProjectID != projectID || wp.ArchivedAt != nil {
				continue
			}
		}
		rows = append(rows, domain.Mention{
			ID:          domain.NewID(),
			ProjectID:   projectID,
			ToKind:      p.Kind,
			ToID:        p.ID,
			Linked:      true,
			ContextText: p.Context,
		})
	}
	return rows, agents, nil
}

// writeMentions stamps the source onto the resolved rows and replaces the source's mention
// set inside the mutation's transaction. Always called on a body save — an edit that removes
// every token must clear the old rows, so a nil set still writes.
func writeMentions(ctx context.Context, tx *store.Tx, fromKind, fromID string, rows []domain.Mention) error {
	for i := range rows {
		rows[i].FromKind = fromKind
		rows[i].FromID = fromID
	}
	return tx.Mentions().ReplaceForSource(ctx, fromKind, fromID, rows)
}
