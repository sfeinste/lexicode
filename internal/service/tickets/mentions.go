package tickets

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Mention wire format (S12). The Editor inserts an explicit token for every accepted
// autocomplete pick:
//
//	@[Display Name](user:01H…)   @[dev](agent:01H…)   @[API runbook](wiki:01H…)   @[PAY-14](ticket:01H…)
//
// The token is unambiguous (bare `@name` text never creates a linked mention — the wiki
// story's backlink pass owns *unlinked* mention detection, mentions.linked = 0), renders
// legibly as plain markdown, and survives copy/paste. parseMentions finds the tokens;
// resolveMentions validates the targets and turns them into mentions rows with the containing
// paragraph as context (data model §5).
var mentionPattern = regexp.MustCompile(`@\[([^\]\n]+)\]\((user|agent|wiki|ticket):([A-Za-z0-9]+)\)`)

// parsedMention is one token found in a body, with the paragraph that contains it.
type parsedMention struct {
	Label   string
	Kind    string // user | agent | wiki | ticket
	ID      string
	Context string
}

// parseMentions extracts every mention token from a markdown body, in order. Context is the
// full containing paragraph (blank-line delimited) — the backlinks pane renders it (UI spec
// §5.6: a bare list of titles is useless).
func parseMentions(body string) []parsedMention {
	matches := mentionPattern.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]parsedMention, 0, len(matches))
	for _, m := range matches {
		out = append(out, parsedMention{
			Label:   body[m[2]:m[3]],
			Kind:    body[m[4]:m[5]],
			ID:      body[m[6]:m[7]],
			Context: paragraphAround(body, m[0], m[1]),
		})
	}
	return out
}

// paragraphAround returns the blank-line-delimited paragraph containing [start,end).
func paragraphAround(body string, start, end int) string {
	pStart := 0
	if i := strings.LastIndex(body[:start], "\n\n"); i != -1 {
		pStart = i + 2
	}
	pEnd := len(body)
	if i := strings.Index(body[end:], "\n\n"); i != -1 {
		pEnd = end + i
	}
	return strings.TrimSpace(body[pStart:pEnd])
}

// resolveMentions validates parsed mentions against the database and returns the mentions
// rows to write. Rules:
//
//   - user: must exist. Users are workspace-level (light auth, no per-project membership
//     table in V1).
//   - agent: must exist, belong to the project, and not be archived.
//   - ticket: must exist and belong to the project.
//   - wiki: written as-is — the wiki service (and its repository) is a later story; the
//     Editor cannot mint wiki tokens until that API exists, and the wiki story adds
//     validation when it lands. The mentions table carries no FK on to_id, so an
//     unvalidated row is inert.
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
			// Written unvalidated; see the function comment.
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
