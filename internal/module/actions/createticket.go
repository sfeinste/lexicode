package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// createTicket is the `create_ticket` action: file a ticket INTO TRIAGE — never directly onto
// the board (brief §6.4). The injected seam creates the ticket row and its pending triage
// item in one transaction; the board query excludes the ticket until a human accepts it in
// the S31 triage queue (data model §10.7). The triage row's provenance is the plan's exact
// sentence: "Created by trigger `CI failed → file a ticket` from run #482" — or, for an
// event no run caused, the event named instead.
type createTicket struct{ d Deps }

type createTicketParams struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

func (a *createTicket) ID() string    { return "create_ticket" }
func (a *createTicket) Label() string { return "File a ticket" }

func (a *createTicket) Schema() ports.ParamSchema {
	return ports.ParamSchema{Fields: []ports.ParamField{
		{Key: "title", Label: "Title", Type: "template", Required: true,
			Help: "{{...}} fields interpolate from the event, e.g. \"CI failed on PR {{pr.number}}\"."},
		{Key: "description", Label: "Description", Type: "template"},
		{Key: "labels", Label: "Labels", Type: "list",
			Help: "Existing label names to attach; unknown names are skipped."},
	}}
}

func (a *createTicket) Describe(params json.RawMessage) (string, error) {
	var p createTicketParams
	if err := decodeParams(params, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Title) == "" {
		return "", errors.New("a title is required")
	}
	return fmt.Sprintf("file a ticket into triage: %q", p.Title), nil
}

func (a *createTicket) Execute(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error) {
	var p createTicketParams
	if err := decodeParams(params, &p); err != nil {
		return ports.ActionResult{}, err
	}
	title, _ := ac.Interp(p.Title)
	if strings.TrimSpace(title) == "" {
		return ports.ActionResult{}, fmt.Errorf("the title template %q rendered empty for this event", p.Title)
	}
	description, _ := ac.Interp(p.Description)

	// Provenance (data model §4): from the causing run when the chain has one, from the
	// event otherwise.
	provenance := fmt.Sprintf("Created by trigger `%s` from a %s event", ac.Trigger.Name, ac.Event.Kind)
	runID := causeRunID(ac.Event)
	if runID != "" {
		if run, err := a.d.Store.Runs().ByID(ctx, runID); err == nil {
			provenance = fmt.Sprintf("Created by trigger `%s` from run #%d", ac.Trigger.Name, run.Seq)
		}
	}

	tk, err := a.d.Tickets.CreateInTriage(ctx, TriageCreate{
		ProjectID:   ac.Project.ID,
		Title:       title,
		Description: description,
		LabelNames:  p.Labels,
		Provenance:  provenance,
		TriggerID:   ac.Trigger.ID,
		RunID:       runID,
	})
	if err != nil {
		return ports.ActionResult{}, err
	}
	return ports.ActionResult{
		Outcome: domain.FiringSucceeded,
		Note:    fmt.Sprintf("filed %s into triage", tk.Key),
	}, nil
}
