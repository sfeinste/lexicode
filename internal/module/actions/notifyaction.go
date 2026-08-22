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

// notifyAction is the `notify` action: deliver an interpolated message to the DELEGATING
// HUMAN — never "everyone" (brief D1) — through the Notifier port ("inapp" in V1).
//
// Routing: when the triggering event has a causing run, the injected S24 rule decides —
// requested_by → the run's ticket's assignee → project owner (notify.Service.RouteTo, the
// same ladder run escalation uses, injected so it cannot drift). Without a causing run the
// run rung does not exist: the event's subject ticket's assignee, then the project owner.
//
// The notification's flavor is `review` — "output waiting on you" is the closest of the
// schema's four flavors (question/approval/review/failure, data model §7) to "a rule you
// wrote wants your attention". Widening that CHECK vocabulary would be a design escalation
// this story deliberately does not take.
type notifyAction struct{ d Deps }

type notifyParams struct {
	Message string `json:"message"`
}

func (a *notifyAction) ID() string    { return "notify" }
func (a *notifyAction) Label() string { return "Notify me" }

func (a *notifyAction) Schema() ports.ParamSchema {
	return ports.ParamSchema{Fields: []ports.ParamField{
		{Key: "message", Label: "Message", Type: "template", Required: true,
			Help: "Delivered to the delegating human: the causing run's requester, else the ticket's assignee, else the project owner."},
	}}
}

func (a *notifyAction) Describe(params json.RawMessage) (string, error) {
	var p notifyParams
	if err := decodeParams(params, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Message) == "" {
		return "", errors.New("a message is required")
	}
	return fmt.Sprintf("notify the delegating human: %q", p.Message), nil
}

func (a *notifyAction) Execute(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error) {
	var p notifyParams
	if err := decodeParams(params, &p); err != nil {
		return ports.ActionResult{}, err
	}
	msg, _ := ac.Interp(p.Message)
	if strings.TrimSpace(msg) == "" {
		return ports.ActionResult{}, fmt.Errorf("the message template %q rendered empty for this event", p.Message)
	}

	userID := ""
	var runID *string
	if id := causeRunID(ac.Event); id != "" {
		run, err := a.d.Store.Runs().ByID(ctx, id)
		if err != nil {
			return ports.ActionResult{}, fmt.Errorf("loading the causing run: %w", err)
		}
		rid := run.ID
		runID = &rid
		if userID, err = a.d.Notify.RouteRun(ctx, run); err != nil {
			return ports.ActionResult{}, err
		}
	}
	if userID == "" && ac.Event.SubjectKind == "ticket" && ac.Event.SubjectID != nil {
		if tk, err := a.d.Store.Tickets().ByID(ctx, *ac.Event.SubjectID); err == nil &&
			tk.AssigneeID != nil && *tk.AssigneeID != "" {
			userID = *tk.AssigneeID
		}
	}
	if userID == "" {
		userID = ac.Project.OwnerID
	}
	if userID == "" {
		return ports.ActionResult{}, errors.New("nobody to notify: the project has no owner")
	}

	notifier, err := a.d.Notifier("inapp")
	if err != nil {
		return ports.ActionResult{}, err
	}
	err = notifier.Deliver(ctx, domain.Notification{
		UserID:    userID,
		ProjectID: ac.Project.ID,
		RunID:     runID, // the (user, run) row updates in place; runless deliveries insert
		Flavor:    domain.FlavorReview,
		Title:     msg,
		Body:      fmt.Sprintf("Sent by trigger `%s`", ac.Trigger.Name),
	})
	if err != nil {
		return ports.ActionResult{}, err
	}

	who := "the delegating human"
	if u, err := a.d.Store.Users().ByID(ctx, userID); err == nil {
		who = u.DisplayName
	}
	return ports.ActionResult{
		Outcome: domain.FiringSucceeded,
		Note:    "notified " + who,
	}, nil
}
