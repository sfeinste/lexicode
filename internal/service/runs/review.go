// review.go is the review-submission path (S39, completing the S14 forge port): when a
// reviewer agent finishes reading a pull request it submits its findings through the
// Lexicode MCP server's `submit_review` tool, and the ORCHESTRATOR performs the forge write
// — exactly like propen.go does for pull requests. The agent never holds GitHub credentials
// and never shells out to `gh`; the submit_reviews grant, the D-9 actor marker, the
// run_outputs row and the audit row all live in the forge adapter, where enforcement
// belongs (brief D7).
//
// APPROVE is not reachable from here: ports.ForgeProvider.SubmitReview rejects it with
// ErrSelfApprovalForbidden regardless of permissions (brief D6).
package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	kernelsecrets "github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// ReviewSubmitter implements the MCP server's review seam. All fields are required.
type ReviewSubmitter struct {
	Store   *store.Store
	Secrets *kernelsecrets.Store
	// Forge resolves the repo's forge provider by ID (kernel.Forge).
	Forge  func(id string) (ports.ForgeProvider, error)
	Logger *slog.Logger
}

// SubmitForRun submits one review on prNumber as the run's agent. event is "COMMENT" or
// "REQUEST_CHANGES"; anything else (APPROVE in particular) is refused by the adapter.
func (o *ReviewSubmitter) SubmitForRun(ctx context.Context, run domain.Run, prNumber int, event, body string) (domain.Review, error) {
	if prNumber <= 0 {
		return domain.Review{}, errors.New("a pull request number is required")
	}
	repo, err := o.Store.Repos().ByProject(ctx, run.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return domain.Review{}, errors.New("no repository is connected to this project")
	}
	if err != nil {
		return domain.Review{}, err
	}
	if repo.TokenSecretID == nil {
		return domain.Review{}, errors.New("the connected repository has no stored token; reconnect it in project settings")
	}
	token, err := o.Secrets.Get(ctx, *repo.TokenSecretID)
	if err != nil {
		return domain.Review{}, fmt.Errorf("reading the repository token: %w", err)
	}
	forge, err := o.Forge(repo.Provider)
	if err != nil {
		return domain.Review{}, err
	}
	review, err := forge.SubmitReview(ctx, ports.Creds{Token: token}, repo.Ref(),
		domain.Actor{AgentID: run.AgentID, RunID: run.ID}, prNumber,
		ports.ReviewSpec{Event: event, Body: body})
	if err != nil {
		return domain.Review{}, err
	}
	if o.Logger != nil {
		o.Logger.Info("runs: review submitted",
			slog.String("run", run.ID), slog.Int("pr", prNumber), slog.String("event", event))
	}
	return review, nil
}
