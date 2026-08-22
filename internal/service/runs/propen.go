// propen.go is the S24 PR-opening path (§10.4 step 6): when a run completes with a pushed
// branch and its agent holds the open_prs grant, the ORCHESTRATOR opens the pull request
// through the forge port — the agent never needs `gh` or credentials for the GitHub API in
// its container, and the D-9 actor marker plus the grant check live in the forge adapter
// (enforcement, not prompt). The scheduler calls this through its narrow sched.PROpener seam.
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

// PROpener implements sched.PROpener. All fields are required.
type PROpener struct {
	Store   *store.Store
	Secrets *kernelsecrets.Store
	// Forge resolves the repo's forge provider by ID (kernel.Forge).
	Forge  func(id string) (ports.ForgeProvider, error)
	Logger *slog.Logger
}

// OpenForRun opens the run's pull request, once. (false, nil) means "nothing to open": no
// branch, no open_prs grant, no connected repo, or a PR already recorded for this run. A
// true error (missing token, forge refusal) is returned for the scheduler to surface.
func (o *PROpener) OpenForRun(ctx context.Context, run domain.Run) (bool, error) {
	if run.Branch == nil || *run.Branch == "" {
		return false, nil
	}
	agent, err := o.Store.Agents().ByID(ctx, run.AgentID)
	if err != nil {
		return false, err
	}
	if !agent.Permissions.OpenPRs {
		return false, nil
	}
	outputs, err := o.Store.RunOutputs().ForRun(ctx, run.ID)
	if err != nil {
		return false, err
	}
	for _, out := range outputs {
		if out.Kind == domain.OutputPullRequest {
			return false, nil // the agent (or a retry) already opened one
		}
	}
	repo, err := o.Store.Repos().ByProject(ctx, run.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if repo.TokenSecretID == nil {
		return false, errors.New("the connected repository has no stored token; reconnect it in project settings")
	}
	token, err := o.Secrets.Get(ctx, *repo.TokenSecretID)
	if err != nil {
		return false, fmt.Errorf("reading the repository token: %w", err)
	}
	forge, err := o.Forge(repo.Provider)
	if err != nil {
		return false, err
	}

	title := fmt.Sprintf("Run #%d by %s", run.Seq, agent.Name)
	body := fmt.Sprintf("Automated change by agent **%s** (run #%d).", agent.Name, run.Seq)
	if run.TicketID != nil {
		if tk, err := o.Store.Tickets().ByID(ctx, *run.TicketID); err == nil {
			title = tk.Title
			body = fmt.Sprintf("Automated change by agent **%s** for ticket %s (run #%d).",
				agent.Name, tk.Key, run.Seq)
		}
	}
	base := "main"
	if repo.DefaultBranch != nil && *repo.DefaultBranch != "" {
		base = *repo.DefaultBranch
	}

	pr, err := forge.OpenPullRequest(ctx, ports.Creds{Token: token}, repo.Ref(),
		domain.Actor{AgentID: run.AgentID, RunID: run.ID}, ports.PRSpec{
			Title: title,
			Body:  body,
			Head:  *run.Branch,
			Base:  base,
		})
	if err != nil {
		return false, err
	}
	if o.Logger != nil {
		o.Logger.Info("runs: pull request opened",
			slog.String("run", run.ID), slog.Int("pr", pr.Number))
	}
	return true, nil
}
