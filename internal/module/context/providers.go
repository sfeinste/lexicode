package contextmod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// estimateTokens is the crude chars/4 heuristic every context surface shares.
func estimateTokens(text string) int { return (len(text) + 3) / 4 }

// ProjectProvider yields the project-wide agent guidance from project settings
// (architecture §11: priority 10, reason "project guidance"). A project with no guidance
// contributes nothing.
type ProjectProvider struct {
	st *store.Store
}

// NewProjectProvider builds the provider over st.
func NewProjectProvider(st *store.Store) *ProjectProvider { return &ProjectProvider{st: st} }

// ID implements ports.ContextProvider.
func (p *ProjectProvider) ID() string { return "project" }

// Priority implements ports.ContextProvider.
func (p *ProjectProvider) Priority() int { return 10 }

// Resolve implements ports.ContextProvider.
func (p *ProjectProvider) Resolve(ctx context.Context, req ports.ContextRequest) ([]ports.ContextItem, error) {
	project, err := p.st.Projects().ByID(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	guidance := strings.TrimSpace(project.AgentGuidance)
	if guidance == "" {
		return nil, nil
	}
	return []ports.ContextItem{{
		SourceKind: "project",
		SourceRef:  project.Key,
		Title:      "Project guidance",
		Reason:     "project guidance",
		Body:       guidance,
		Tokens:     estimateTokens(guidance),
		Injected:   true,
	}}, nil
}

// TicketProvider yields the run's ticket — title, description, acceptance criteria and the
// parent/sub-ticket structure (architecture §11: priority 30, reason "ticket PAY-14").
//
// A run with no ticket_id of its own is not necessarily ticketless work. Trigger-spawned runs
// are free-floating by design (D-15), so a run started by a review on the pull request that
// LEXI-7 produced never learned that LEXI-7 is what it is being asked to fix — it got the
// review and nothing to measure the change against. When such a run's causing event is about a
// pull request, this provider resolves the ticket through `run_outputs`: the run that opened
// that pull request, and the ticket that run was working on. LEXI-12 will make
// `tickets.pr_number` a direct link and this walk redundant.
type TicketProvider struct {
	st *store.Store
}

// NewTicketProvider builds the provider over st.
func NewTicketProvider(st *store.Store) *TicketProvider { return &TicketProvider{st: st} }

// ID implements ports.ContextProvider.
func (p *TicketProvider) ID() string { return "ticket" }

// Priority implements ports.ContextProvider.
func (p *TicketProvider) Priority() int { return 30 }

// Resolve implements ports.ContextProvider.
func (p *TicketProvider) Resolve(ctx context.Context, req ports.ContextRequest) ([]ports.ContextItem, error) {
	tk, via, err := p.ticketFor(ctx, req)
	if err != nil {
		return nil, err
	}
	if tk == nil {
		return nil, nil
	}

	var b strings.Builder
	if lead := via.lead(*tk); lead != "" {
		b.WriteString(lead)
		b.WriteString("\n")
	}
	if desc := strings.TrimSpace(tk.Description); desc != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(desc)
		b.WriteString("\n")
	}

	criteria, err := p.st.Criteria().ForTicket(ctx, tk.ID)
	if err != nil {
		return nil, err
	}
	if len(criteria) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## Acceptance criteria\n\n")
		for _, c := range criteria {
			mark := " "
			if c.Checked {
				mark = "x"
			}
			// The criterion id rides along so `check_criterion` (contracts §3.3) can
			// address it.
			fmt.Fprintf(&b, "- [%s] %s (criterion_id: %s)\n", mark, c.Text, c.ID)
		}
	}

	if tk.ParentID != nil {
		if parent, err := p.st.Tickets().ByID(ctx, *tk.ParentID); err == nil {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "Sub-ticket of %s: %s\n", parent.Key, parent.Title)
		}
	}
	if children, err := p.st.Tickets().Children(ctx, tk.ID); err == nil && len(children) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## Sub-tickets\n\n")
		for _, c := range children {
			fmt.Fprintf(&b, "- %s: %s\n", c.Key, c.Title)
		}
	}

	body := strings.TrimSpace(b.String())
	return []ports.ContextItem{{
		SourceKind: "ticket",
		SourceRef:  tk.Key,
		Title:      fmt.Sprintf("Ticket %s: %s", tk.Key, tk.Title),
		Reason:     via.reason(*tk),
		Body:       body,
		Tokens:     estimateTokens(body),
		Injected:   true,
	}}, nil
}

// ticketOrigin records how the ticket was found, which is the difference between "this run's
// ticket" and "the ticket the pull request this run is about belongs to". The prompt and the
// Context panel both say which, because they are not the same claim: an inferred ticket is the
// work the pull request came from, not necessarily the task of this run.
type ticketOrigin struct {
	// PRNumber is the pull request the ticket was resolved through; 0 when the run carried
	// its own ticket_id.
	PRNumber int
	// RunSeq is the run that opened that pull request, as "run #482".
	RunSeq int64
}

// reason is the run_context_items row's reason, rendered verbatim in the Context panel.
func (o ticketOrigin) reason(tk domain.Ticket) string {
	if o.PRNumber == 0 {
		return "ticket " + tk.Key
	}
	return fmt.Sprintf("ticket %s, the ticket run #%d opened pull request #%d for",
		tk.Key, o.RunSeq, o.PRNumber)
}

// lead is the sentence that opens the prompt section when the ticket was inferred. It states
// the inference rather than presenting the ticket as this run's assignment: the agent is being
// given the pull request's original goal to measure a change against, and the chain that
// produced it is short enough to state.
func (o ticketOrigin) lead(tk domain.Ticket) string {
	if o.PRNumber == 0 {
		return ""
	}
	return fmt.Sprintf(
		"This run has no ticket of its own. Pull request #%d was opened by run #%d, which was working on %s — so this is the ticket the pull request has to satisfy.",
		o.PRNumber, o.RunSeq, tk.Key)
}

// ticketFor finds the ticket this run's context should carry: the run's own ticket when it has
// one, otherwise the ticket behind the pull request its causing event is about. A nil ticket
// with a nil error is the normal answer for a run that has neither.
//
// The inferred path is non-fatal throughout. A run's own ticket_id is a foreign key and a
// failure to read it is a real fault, but an inference that comes up empty — no causing event,
// an event about something other than a pull request, no run known to have opened it, a ticket
// since deleted — is just an absent prompt section, never a failed enqueue.
func (p *TicketProvider) ticketFor(ctx context.Context, req ports.ContextRequest) (*domain.Ticket, ticketOrigin, error) {
	if req.TicketID != "" {
		tk, err := p.st.Tickets().ByID(ctx, req.TicketID)
		if err != nil {
			return nil, ticketOrigin{}, err
		}
		return &tk, ticketOrigin{}, nil
	}

	prNumber, err := p.causePRNumber(ctx, req)
	if err != nil || prNumber == 0 {
		return nil, ticketOrigin{}, err
	}
	found, err := p.st.RunOutputs().TicketForPR(ctx, req.ProjectID, prNumber)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ticketOrigin{}, nil
		}
		return nil, ticketOrigin{}, err
	}
	tk, err := p.st.Tickets().ByID(ctx, found.TicketID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ticketOrigin{}, nil
		}
		return nil, ticketOrigin{}, err
	}
	return &tk, ticketOrigin{PRNumber: prNumber, RunSeq: found.RunSeq}, nil
}

// causePRNumber is the pull request the run's causing event is about, or 0 when it is about
// something else or there is no causing event at all.
//
// It prefers the event's own subject columns — the normalized, indexed fact that both producers
// of a PR event set (the GitHub poller and the `submit_review` MCP tool) — and falls back to the
// payload's `pr.number`, exactly as mcp.Server.causePRNumber does. A module may not import a
// service, so the read is restated here rather than shared.
func (p *TicketProvider) causePRNumber(ctx context.Context, req ports.ContextRequest) (int, error) {
	if req.CauseEventID == "" {
		return 0, nil
	}
	ev, err := p.st.Events().ByID(ctx, req.CauseEventID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if ev.SubjectKind == "pr" && ev.SubjectNumber != nil && *ev.SubjectNumber > 0 {
		return int(*ev.SubjectNumber), nil
	}
	var payload struct {
		PR struct {
			Number int `json:"number"`
		} `json:"pr"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err == nil && payload.PR.Number > 0 {
		return payload.PR.Number, nil
	}
	return 0, nil
}
