package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// moveTicket is the `move_ticket` action: move a ticket to the project's first column of a
// CATEGORY — never a column name (brief D2), which is exactly why the rule keeps working when
// the column is renamed. The project having no column of the category is a named error
// (tickets.NoColumnOfCategoryError, surfaced in the firing's words); a ticket still pending
// triage is invisible to this action (§10 invariant 7).
//
// Interplay with auto-start (brief D3): "a move never starts a run" has exactly one
// exception — the destination column opted into auto-start-delegate and the ticket has a
// delegate — and per D3 that exception applies to ANY move, this one included. The injected
// seam (tickets.TriggerMoveToCategory) honours it the same way a human drag does, through
// the scheduler seam, audited. This action itself never touches the scheduler.
type moveTicket struct{ d Deps }

type moveTicketParams struct {
	Category string `json:"category"`
	// Ticket names the ticket to move — a template, usually left empty to mean the event's
	// own ticket ({{ticket.key}}).
	Ticket string `json:"ticket"`
}

func (a *moveTicket) ID() string    { return "move_ticket" }
func (a *moveTicket) Label() string { return "Move the ticket" }

func (a *moveTicket) Schema() ports.ParamSchema {
	return ports.ParamSchema{Fields: []ports.ParamField{
		{Key: "category", Label: "To category", Type: "category", Required: true,
			Enum: []string{
				string(domain.CategoryBacklog), string(domain.CategoryReady),
				string(domain.CategoryRunning), string(domain.CategoryReview),
				string(domain.CategoryDone), string(domain.CategoryCanceled),
			},
			Help: "Categories survive column renames; the ticket lands in the project's first column of the category."},
		{Key: "ticket", Label: "Ticket", Type: "template",
			Help: "Which ticket to move. Empty means the event's own ticket ({{ticket.key}})."},
	}}
}

func (a *moveTicket) Describe(params json.RawMessage) (string, error) {
	var p moveTicketParams
	if err := decodeParams(params, &p); err != nil {
		return "", err
	}
	cat := domain.ColumnCategory(p.Category)
	if p.Category == "" {
		return "", errors.New("a destination category is required")
	}
	if !cat.IsValid() {
		return "", fmt.Errorf("%q is not a column category (backlog, ready, running, review, done, canceled)", p.Category)
	}
	subject := "the ticket"
	if p.Ticket != "" {
		subject = p.Ticket
	}
	return fmt.Sprintf("move %s to a %s column", subject, cat), nil
}

func (a *moveTicket) Execute(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error) {
	var p moveTicketParams
	if err := decodeParams(params, &p); err != nil {
		return ports.ActionResult{}, err
	}
	cat := domain.ColumnCategory(p.Category)
	if !cat.IsValid() {
		return ports.ActionResult{}, fmt.Errorf("%q is not a column category", p.Category)
	}
	tmpl := p.Ticket
	if tmpl == "" {
		tmpl = "{{ticket.key}}"
	}
	key, _ := ac.Interp(tmpl)
	if key == "" {
		return ports.ActionResult{}, fmt.Errorf(
			"this %s event carries no ticket; set the action's ticket parameter", ac.Event.Kind)
	}
	tk, err := a.d.Store.Tickets().ByKey(ctx, key)
	if err != nil {
		return ports.ActionResult{}, fmt.Errorf("no ticket %s exists", key)
	}
	if tk.ProjectID != ac.Project.ID {
		return ports.ActionResult{}, fmt.Errorf("ticket %s belongs to another project", key)
	}
	moved, err := a.d.Tickets.MoveToCategory(ctx, tk.ID, cat, "trigger "+ac.Trigger.Name)
	if err != nil {
		return ports.ActionResult{}, err
	}
	if !moved {
		return ports.ActionResult{
			Outcome: domain.FiringNoAction,
			Note:    fmt.Sprintf("%s is already where a %s move would put it", key, cat),
		}, nil
	}
	return ports.ActionResult{
		Outcome: domain.FiringSucceeded,
		Note:    fmt.Sprintf("moved %s to a %s column", key, cat),
	}, nil
}
