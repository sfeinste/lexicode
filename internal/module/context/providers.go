package contextmod

import (
	"context"
	"fmt"
	"strings"

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
// parent/sub-ticket structure (architecture §11: priority 30, reason "ticket PAY-14"). A
// ticketless run contributes nothing.
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
	if req.TicketID == "" {
		return nil, nil
	}
	tk, err := p.st.Tickets().ByID(ctx, req.TicketID)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	if desc := strings.TrimSpace(tk.Description); desc != "" {
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
		Reason:     "ticket " + tk.Key,
		Body:       body,
		Tokens:     estimateTokens(body),
		Injected:   true,
	}}, nil
}
