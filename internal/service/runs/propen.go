// propen.go is the S24 PR-opening path (§10.4 step 6): when a run completes with a pushed
// branch and its agent holds the open_prs grant, the ORCHESTRATOR opens the pull request
// through the forge port — the agent never needs `gh` or credentials for the GitHub API in
// its container, and the D-9 actor marker plus the grant check live in the forge adapter
// (enforcement, not prompt). The scheduler calls this through its narrow sched.PROpener seam.
package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	// Now overrides the clock in tests. Nil means time.Now.
	Now func() time.Time
}

func (o *PROpener) now() string {
	if o.Now != nil {
		return domain.FormatTime(o.Now())
	}
	return domain.Now()
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
		// Not every refusal is a failure. A run whose subject is an existing pull request
		// works on that pull request's own head branch — the "changes requested → address
		// them" and "CI failed → fix it" hops of the canonical chain — so the forge answers
		// 422: a pull request for this head already exists. A run that produced no commits
		// of its own gets the other 422: there is no head branch to open from. Neither is an
		// error on an otherwise successful run: there is simply nothing to open, and the
		// honest place to say so is the run's own transcript.
		if note, isNoOp := nothingToOpen(err, *run.Branch); isNoOp {
			o.noteNothingToOpen(ctx, run.ID, note)
			if o.Logger != nil {
				o.Logger.Info("runs: no pull request to open",
					slog.String("run", run.ID), slog.String("reason", note.title))
			}
			return false, nil
		}
		return false, err
	}
	if o.Logger != nil {
		o.Logger.Info("runs: pull request opened",
			slog.String("run", run.ID), slog.Int("pr", pr.Number))
	}
	return true, nil
}

// noOpNote is the sentence the run's transcript carries when there was nothing to open: a
// one-line title (what the activity list shows) and the paragraph behind it.
type noOpNote struct {
	title  string
	detail string
}

// nothingToOpen classifies a forge refusal as "there was nothing to open" rather than a
// failure.
//
// The classification is on the forge's 422 text because that is what the API gives us: the
// adapter wraps go-github's *ErrorResponse, whose Error() carries the status and the
// validation errors. GitHub spells the two cases several ways depending on the endpoint's
// mood — a `head`/`invalid` validation error, "head branch … does not exist", "No commits
// between …", "A pull request already exists for …" — so all the spellings are matched, and a
// 422 is required so an unrelated failure with a similar word in it stays an error.
func nothingToOpen(err error, branch string) (noOpNote, bool) {
	if err == nil {
		return noOpNote{}, false
	}
	low := strings.ToLower(err.Error())
	if !strings.Contains(low, "422") {
		return noOpNote{}, false
	}
	switch {
	case strings.Contains(low, "a pull request already exists"):
		return noOpNote{
			title: fmt.Sprintf("No pull request opened: one already exists for `%s`.", branch),
			detail: fmt.Sprintf(
				"The forge already has an open pull request for `%s`, so this run opened none. "+
					"The run itself succeeded.", branch),
		}, true
	case strings.Contains(low, "does not exist"),
		strings.Contains(low, "field:head code:invalid"),
		strings.Contains(low, "no commits between"):
		return noOpNote{
			title: fmt.Sprintf("No pull request opened: nothing was pushed to `%s`.", branch),
			detail: fmt.Sprintf(
				"Nothing was pushed to `%s`, so there is no head to open a pull request from — "+
					"the run either produced no commits of its own or worked on an existing "+
					"pull request's branch, where its commits already are. The run itself "+
					"succeeded.", branch),
		}, true
	}
	return noOpNote{}, false
}

// noteNothingToOpen records the level-2 system line. The scheduler writes the same shape for
// its own post-terminal notes (appendSystemActivity); this one is written here because only
// this file knows what the forge answered.
func (o *PROpener) noteNothingToOpen(ctx context.Context, runID string, note noOpNote) {
	a := domain.Activity{
		RunID: runID, Type: domain.ActivitySystem, Level: 2, GroupKey: "system",
		Title: note.title, Payload: json.RawMessage(mustJSONText(note.detail)), Attempt: 1,
		CreatedAt: o.now(),
	}
	// Detached from the caller's context: the run is already terminal, and a cancelled
	// context must not lose the record.
	if err := o.Store.Activities().AppendNext(context.WithoutCancel(ctx), &a); err != nil && o.Logger != nil {
		o.Logger.Error("runs: system activity append failed",
			slog.String("run", runID), slog.String("error", err.Error()))
	}
}

// mustJSONText renders {"text": …} for the activity payload.
func mustJSONText(text string) []byte {
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil { // impossible for a string map
		return []byte(`{"text":""}`)
	}
	return b
}
