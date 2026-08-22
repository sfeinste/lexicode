// needsyou.go is the one needs-you query behind all of architecture §12's surfaces: the
// home strip, the board's pinned lane, /inbox, the left-rail block and the tab badges are
// renderings of ONE service method, NeedsYou, differing only in scope. The union is:
//
//   - non-terminal runs parked on a human (`needs_input` / `awaiting_approval`),
//   - terminal `failed` / `loop_stopped` runs not yet acknowledged,
//   - outputs awaiting review: pending wiki proposals (S35) and open agent PRs (S36) — a
//     completed run's `pull_request` output joined against poll_pr_state, kept while the
//     poller's snapshot says `open` (or while no snapshot exists — no poller, no closed
//     events yet; see store.RunOutputsRepo.OpenAgentPRs).
//
// The flavor — question / approval / review / failure — is computed here, in one place,
// and every renderer prints it in words (UI spec §4.3; interaction rule 1).
package runs

import (
	"context"
	"sort"
	"strconv"

	"github.com/spruce/lexicode/internal/domain"
)

// NeedsYouScope selects which slice of the needs-you union one surface renders.
type NeedsYouScope struct {
	// ProjectID limits the rows to one project (the board lane, view=needs_you). Empty
	// means every project (/inbox, home strip, left rail).
	ProjectID string
	// Visible gates rows by project — /inbox restricts to the caller's memberships. Nil
	// admits everything. Errors abort the query; a project that should simply be hidden
	// returns (false, nil).
	Visible func(projectID string) (bool, error)
}

// needsYouBody is one row of the needs-you surfaces (architecture §12): the flavor is the
// §4.3 vocabulary, and every renderer prints it in words (interaction rule 1).
//
// `kind` discriminates the row's subject:
//   - "run" — a blocked run; `id` is the run id and the row links to the run.
//   - "wiki_proposal" — a pending agent proposal awaiting review (S35); `id` is the
//     proposed page's id, `page_slug`/`page_title` name it.
//   - "pull_request" — an open agent PR awaiting review (S36); `id` is the run-output row's
//     id, `run_id` names the producing run, `pr_number`/`url` locate the PR itself. The row
//     links to both.
type needsYouBody struct {
	Kind        string  `json:"kind"`
	ID          string  `json:"id"`
	ProjectKey  string  `json:"project_key"`
	TicketID    *string `json:"ticket_id"`
	TicketKey   *string `json:"ticket_key"`
	TicketTitle *string `json:"ticket_title"`
	Agent       string  `json:"agent"`
	Flavor      string  `json:"flavor"`
	Status      string  `json:"status"`
	StartedAt   string  `json:"started_at"`
	PageSlug    string  `json:"page_slug,omitempty"`
	PageTitle   string  `json:"page_title,omitempty"`
	RunID       string  `json:"run_id,omitempty"`
	PRNumber    int64   `json:"pr_number,omitempty"`
	URL         string  `json:"url,omitempty"`
}

// needsYouFlavor is the §4.3 flavor computation, one place: parked runs are a question or
// an approval by state; unacknowledged terminal failures are a failure. The fourth flavor,
// review, belongs to the output rows (wiki proposals, open agent PRs).
func needsYouFlavor(state domain.RunState) string {
	switch state {
	case domain.RunNeedsInput:
		return "question"
	case domain.RunAwaitingApproval:
		return "approval"
	default:
		return "failure"
	}
}

// flavorRank is the home-strip sort (UI spec §5.1): answer a question first, then approve,
// then failed, then review. The inbox re-sorts client-side — approvals to the top always
// (UI spec §5.10) — which is a rendering decision of that surface, not a different query.
func flavorRank(flavor string) int {
	switch flavor {
	case "question":
		return 0
	case "approval":
		return 1
	case "failure":
		return 2
	default:
		return 3
	}
}

// NeedsYou is the one service method behind every needs-you surface (architecture §12):
// blocked runs, pending wiki proposals, and open agent PRs, joined with agent and ticket
// names, sorted by flavor rank then oldest-first (the longest-blocked row surfaces first).
func (s *Service) NeedsYou(ctx context.Context, scope NeedsYouScope) ([]needsYouBody, error) {
	visible := func(projectID string) (bool, error) {
		if scope.Visible == nil {
			return true, nil
		}
		return scope.Visible(projectID)
	}

	var (
		runs []domain.Run
		err  error
	)
	if scope.ProjectID != "" {
		runs, err = s.st.Runs().NeedsYou(ctx, scope.ProjectID)
	} else {
		runs, err = s.st.Runs().NeedsYouAll(ctx)
	}
	if err != nil {
		return nil, err
	}
	proposals, err := s.st.Wiki().Proposals(ctx, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	prOutputs, err := s.st.RunOutputs().OpenAgentPRs(ctx, scope.ProjectID)
	if err != nil {
		return nil, err
	}

	// Per-request lookup caches: the surfaces render a handful of rows, but several rows
	// often share one agent, ticket or project.
	agents := map[string]string{}
	projects := map[string]string{}
	agentName := func(agentID string) string {
		name, ok := agents[agentID]
		if !ok {
			if a, err := s.st.Agents().ByID(ctx, agentID); err == nil {
				name = a.Name
			} else {
				name = "agent"
			}
			agents[agentID] = name
		}
		return name
	}
	projectKey := func(projectID string) string {
		key, ok := projects[projectID]
		if !ok {
			if p, err := s.st.Projects().ByID(ctx, projectID); err == nil {
				key = p.Key
			}
			projects[projectID] = key
		}
		return key
	}

	out := make([]needsYouBody, 0, len(runs)+len(proposals)+len(prOutputs))

	for _, run := range runs {
		ok, err := visible(run.ProjectID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		row := needsYouBody{
			Kind:       "run",
			ID:         run.ID,
			ProjectKey: projectKey(run.ProjectID),
			TicketID:   run.TicketID,
			Agent:      agentName(run.AgentID),
			Flavor:     needsYouFlavor(run.State),
			Status:     string(run.State),
			StartedAt:  run.QueuedAt,
		}
		if run.StartedAt != nil {
			row.StartedAt = *run.StartedAt
		}
		if run.TicketID != nil {
			if tk, err := s.st.Tickets().ByID(ctx, *run.TicketID); err == nil {
				k, t := tk.Key, tk.Title
				row.TicketKey, row.TicketTitle = &k, &t
			}
		}
		out = append(out, row)
	}

	for _, page := range proposals {
		ok, err := visible(page.ProjectID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		// The proposing agent's name, through the proposing run — "agent" when either link
		// is gone (a proposal outlives nothing, but degrade rather than 500).
		name := "agent"
		if page.ProposedByRunID != nil {
			if run, err := s.st.Runs().ByID(ctx, *page.ProposedByRunID); err == nil {
				name = agentName(run.AgentID)
			}
		}
		out = append(out, needsYouBody{
			Kind:       "wiki_proposal",
			ID:         page.ID,
			ProjectKey: projectKey(page.ProjectID),
			Agent:      name,
			Flavor:     "review",
			Status:     string(page.State),
			StartedAt:  page.CreatedAt,
			PageSlug:   page.Slug,
			PageTitle:  page.Title,
		})
	}

	for _, o := range prOutputs {
		run, err := s.st.Runs().ByID(ctx, o.RunID)
		if err != nil {
			continue // the output row outlived its run mid-delete; hide, don't 500
		}
		ok, err := visible(run.ProjectID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		number, _ := strconv.ParseInt(o.Ref, 10, 64)
		row := needsYouBody{
			Kind:       "pull_request",
			ID:         o.ID,
			ProjectKey: projectKey(run.ProjectID),
			TicketID:   run.TicketID,
			Agent:      agentName(run.AgentID),
			Flavor:     "review",
			Status:     "open",
			StartedAt:  o.CreatedAt,
			RunID:      run.ID,
			PRNumber:   number,
			URL:        o.URL,
		}
		if run.TicketID != nil {
			if tk, err := s.st.Tickets().ByID(ctx, *run.TicketID); err == nil {
				k, t := tk.Key, tk.Title
				row.TicketKey, row.TicketTitle = &k, &t
			}
		}
		out = append(out, row)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := flavorRank(out[i].Flavor), flavorRank(out[j].Flavor)
		if ri != rj {
			return ri < rj
		}
		return out[i].StartedAt < out[j].StartedAt
	})
	return out, nil
}
