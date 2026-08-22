package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// postComment is the `post_comment` action: comment on the event's pull request through the
// forge port, AS a named agent. The acting agent is a required parameter because the D-9
// marker demands one — the forge adapter appends `<!-- lexicode:actor=agent:… -->` to every
// body, which is how the comment, re-polled as an event, attributes to the agent and gets
// dropped by actor suppression (loop layer 1) instead of re-firing its own rule. The agent
// also needs the comment_prs grant; the adapter enforces it before any network call.
//
// The PR number always comes from the event's normalized payload ({{pr.number}}): the action
// is only meaningful on pr.* events, and the save-time catalog check already scopes the WHEN
// side.
type postComment struct{ d Deps }

type postCommentParams struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Body      string `json:"body"`
}

func (a *postComment) ID() string    { return "post_comment" }
func (a *postComment) Label() string { return "Post a comment" }

func (a *postComment) Schema() ports.ParamSchema {
	return ports.ParamSchema{Fields: []ports.ParamField{
		{Key: "agent_id", Label: "As agent", Type: "agent", Required: true,
			Help: "The comment carries this agent's marker, so it can never re-trigger the rule. Needs the comment_prs permission."},
		{Key: "body", Label: "Comment", Type: "template", Required: true,
			Help: "{{...}} fields interpolate from the event."},
	}}
}

func (a *postComment) Describe(params json.RawMessage) (string, error) {
	var p postCommentParams
	if err := decodeParams(params, &p); err != nil {
		return "", err
	}
	if p.AgentID == "" && p.AgentName == "" {
		return "", errors.New("an acting agent is required — the comment's marker needs one")
	}
	if strings.TrimSpace(p.Body) == "" {
		return "", errors.New("a comment body is required")
	}
	return "comment on the pull request as " + agentLabel(a.d.Store, p.AgentID, p.AgentName), nil
}

func (a *postComment) Execute(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error) {
	var p postCommentParams
	if err := decodeParams(params, &p); err != nil {
		return ports.ActionResult{}, err
	}
	if p.AgentID == "" && p.AgentName == "" {
		return ports.ActionResult{}, errors.New("an acting agent is required")
	}
	body, _ := ac.Interp(p.Body)
	if strings.TrimSpace(body) == "" {
		return ports.ActionResult{}, fmt.Errorf("the comment template %q rendered empty for this event", p.Body)
	}
	numText, _ := ac.Interp("{{pr.number}}")
	n, err := strconv.Atoi(numText)
	if err != nil || n <= 0 {
		return ports.ActionResult{}, fmt.Errorf(
			"this %s event carries no pull request number; post_comment applies to pr.* events", ac.Event.Kind)
	}
	agent, err := resolveAgent(ctx, a.d.Store, ac.Project.ID, p.AgentID, p.AgentName)
	if err != nil {
		return ports.ActionResult{}, err
	}
	repo, err := a.d.Store.Repos().ByProject(ctx, ac.Project.ID)
	if err != nil {
		return ports.ActionResult{}, errors.New("no repository is connected to this project")
	}
	if repo.TokenSecretID == nil {
		return ports.ActionResult{}, errors.New("the connected repository has no stored token; reconnect it in project settings")
	}
	token, err := a.d.Secrets.Get(ctx, *repo.TokenSecretID)
	if err != nil {
		return ports.ActionResult{}, fmt.Errorf("reading the repository token: %w", err)
	}
	forge, err := a.d.Forge(repo.Provider)
	if err != nil {
		return ports.ActionResult{}, err
	}

	// The marker's run half is the causing run when the chain has one, empty otherwise —
	// either way the agent half attributes, which is what suppression keys on.
	actor := domain.Actor{AgentID: agent.ID, RunID: causeRunID(ac.Event)}
	if _, err := forge.CommentOnPullRequest(ctx, ports.Creds{Token: token}, repo.Ref(), actor, n, body); err != nil {
		return ports.ActionResult{}, err
	}
	return ports.ActionResult{
		Outcome: domain.FiringSucceeded,
		Note:    fmt.Sprintf("commented on PR #%d as %s", n, agent.Name),
	}, nil
}
