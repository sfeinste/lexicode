package wiki

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
	"github.com/spruce/lexicode/internal/service/mentionparse"
)

// wikiReader is the subset of lookups mention resolution needs — satisfied by both *Store
// and *Tx repositories, so the rename pass can resolve inside its transaction.
type repoSource interface {
	Wiki() *store.WikiRepo
	Users() *store.UsersRepo
	Agents() *store.AgentsRepo
	Tickets() *store.TicketsRepo
}

// resolveMentions validates parsed mentions from a wiki body and returns the mentions rows
// to write. Same rules as the tickets service (S12): user must exist; agent must belong to
// the project and not be archived; ticket must belong to the project; wiki target must
// belong to the project and not be archived (proposed pages resolve — a proposal can be
// discussed before it is accepted). A token whose target does not resolve is dropped
// silently: a stale mention must not make the page unsaveable.
func (s *Service) resolveMentions(ctx context.Context, projectID string, parsed []mentionparse.Parsed) ([]domain.Mention, error) {
	return resolveAgainst(ctx, s.st, projectID, parsed)
}

// resolveMentionsTx is resolveMentions inside an open transaction.
func (s *Service) resolveMentionsTx(ctx context.Context, tx *store.Tx, projectID string, parsed []mentionparse.Parsed) ([]domain.Mention, error) {
	return resolveAgainst(ctx, tx, projectID, parsed)
}

func resolveAgainst(ctx context.Context, src repoSource, projectID string, parsed []mentionparse.Parsed) ([]domain.Mention, error) {
	var rows []domain.Mention
	for _, p := range parsed {
		switch p.Kind {
		case "user":
			if _, err := src.Users().ByID(ctx, p.ID); errors.Is(err, store.ErrNotFound) {
				continue
			} else if err != nil {
				return nil, err
			}
		case "agent":
			a, err := src.Agents().ByID(ctx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if a.ProjectID != projectID || a.ArchivedAt != nil {
				continue
			}
		case "ticket":
			tk, err := src.Tickets().ByID(ctx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if tk.ProjectID != projectID {
				continue
			}
		case "wiki":
			wp, err := src.Wiki().ByID(ctx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
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
	return rows, nil
}

// writeMentions stamps the wiki source onto the resolved rows and replaces the page's
// mention set inside the mutation's transaction. Always called on a body save — an edit
// that removes every token must clear the old rows, so a nil set still writes.
func writeMentions(ctx context.Context, tx *store.Tx, pageID string, rows []domain.Mention) error {
	for i := range rows {
		rows[i].FromKind = "wiki"
		rows[i].FromID = pageID
	}
	return tx.Mentions().ReplaceForSource(ctx, "wiki", pageID, rows)
}

// OptStr is a tri-state JSON string field for PATCH bodies: absent (Set=false, leave
// unchanged), null (Set && Null — clear), or a value. Plain pointers cannot tell absent
// from null. Same shape as the board and tickets services'; duplicated rather than imported
// so services do not depend on each other for JSON plumbing.
type OptStr struct {
	Set   bool
	Null  bool
	Value string
}

// UnmarshalJSON records that the field appeared, and whether it was null.
func (o *OptStr) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}
