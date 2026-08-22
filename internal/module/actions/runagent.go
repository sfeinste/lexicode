package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
)

// runAgent is the `run_agent` action: ask the kernel scheduler for a run of one agent,
// optionally with an interpolated prompt override. Per D-14 it calls Scheduler().Enqueue and
// NOTHING else — admission, prompt assembly, container launch are the scheduler's business.
// (Resolving an agent_name to its roster ID is a read, not a run start; it exists for the
// S15 bootstrap rules, which name agents that had no IDs yet.)
//
// The S28 plan lists an "output destination" parameter; the frozen RunRequest (contracts §1)
// carries no such field, so schema-ing one would be a lie the scheduler ignores. It is
// deliberately omitted rather than invented — adding it to RunRequest is a design escalation
// for the story that builds run-output routing.
type runAgent struct{ d Deps }

type runAgentParams struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"` // S15 bootstrap rules; agent_id is primary
	// PromptOverride is appended as the prompt's final Task section, after {{...}}
	// interpolation. "prompt" is the S15 bootstrap's spelling of the same field.
	PromptOverride string `json:"prompt_override"`
	Prompt         string `json:"prompt"`
}

func (p runAgentParams) prompt() string {
	if p.PromptOverride != "" {
		return p.PromptOverride
	}
	return p.Prompt
}

func (a *runAgent) ID() string    { return "run_agent" }
func (a *runAgent) Label() string { return "Run an agent" }

func (a *runAgent) Schema() ports.ParamSchema {
	return ports.ParamSchema{Fields: []ports.ParamField{
		{Key: "agent_id", Label: "Agent", Type: "agent", Required: true,
			Help: "The agent to run. Loop protection counts and suppresses on this agent."},
		{Key: "prompt_override", Label: "Prompt override", Type: "template",
			Help: "Optional extra task instruction; {{...}} fields interpolate from the event."},
	}}
}

func (a *runAgent) Describe(params json.RawMessage) (string, error) {
	var p runAgentParams
	if err := decodeParams(params, &p); err != nil {
		return "", err
	}
	if p.AgentID == "" && p.AgentName == "" {
		return "", errors.New("an agent is required")
	}
	desc := "run agent " + agentLabel(a.d.Store, p.AgentID, p.AgentName)
	if p.prompt() != "" {
		desc += " with a prompt override"
	}
	return desc, nil
}

func (a *runAgent) Execute(ctx context.Context, ac ports.ActionContext, params json.RawMessage) (ports.ActionResult, error) {
	var p runAgentParams
	if err := decodeParams(params, &p); err != nil {
		return ports.ActionResult{}, err
	}
	if p.AgentID == "" && p.AgentName == "" {
		return ports.ActionResult{}, errors.New("an agent is required")
	}
	agent, err := resolveAgent(ctx, a.d.Store, ac.Project.ID, p.AgentID, p.AgentName)
	if err != nil {
		return ports.ActionResult{}, err
	}
	sch := a.d.Scheduler()
	if sch == nil {
		return ports.ActionResult{}, errors.New("the run scheduler is not attached; the process is still booting")
	}

	var override string
	if tmpl := p.prompt(); tmpl != "" {
		override, _ = ac.Interp(tmpl) // warnings ride the shared Interp onto the firing row
	}

	// The whole action (D-14): one Enqueue, with the guard's stage-3 decision copied
	// verbatim — subject key and depth land on the run row, and the superseded run is
	// cancelled by the scheduler after this run's seq exists.
	run, err := sch.Enqueue(ctx, sched.RunRequest{
		ProjectID:       ac.Project.ID,
		AgentID:         agent.ID,
		Reason:          "trigger " + ac.Trigger.Name,
		PromptOverride:  override,
		TriggerID:       ac.Trigger.ID,
		CauseEventID:    ac.Event.ID,
		SubjectKey:      ac.Guard.SubjectKey,
		Depth:           ac.Guard.Depth,
		SupersededRunID: ac.Guard.SupersededRunID,
	})
	if err != nil {
		return ports.ActionResult{}, err
	}
	return ports.ActionResult{
		Outcome: domain.FiringSucceeded,
		RunID:   run.ID,
		Note:    fmt.Sprintf("enqueued run #%d for %s", run.Seq, agent.Name),
	}, nil
}
