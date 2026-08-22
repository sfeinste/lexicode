package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/spruce/lexicode/internal/domain"

	"github.com/spruce/lexicode/internal/kernel/store"
)

// ---- ask_human -------------------------------------------------------------------------

type askHumanArgs struct {
	Questions []askQuestion `json:"questions"`
}

type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header"`
	Options     []askOption `json:"options"`
	MultiSelect bool        `json:"multiSelect"`
}

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// toolAskHuman parks the run in needs_input and blocks until a human answers (contracts
// §3.3). A byte-identical re-ask while an elicitation is still pending reuses the open row —
// the restart/retry idempotency documented in the package comment.
func (s *Server) toolAskHuman(ctx context.Context, run domain.Run, raw json.RawMessage) (any, error) {
	var args askHumanArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid ask_human arguments: %w", err)
	}
	if len(args.Questions) == 0 {
		return nil, errors.New("ask_human needs at least one question")
	}
	for i, q := range args.Questions {
		if strings.TrimSpace(q.Question) == "" {
			return nil, fmt.Errorf("question %d has no text", i+1)
		}
		if utf8.RuneCountInString(q.Header) > 12 {
			return nil, fmt.Errorf("question %d: header %q exceeds 12 characters", i+1, q.Header)
		}
		if len(q.Options) < 2 || len(q.Options) > 4 {
			return nil, fmt.Errorf("question %d needs 2-4 options, got %d", i+1, len(q.Options))
		}
		for j, o := range q.Options {
			if strings.TrimSpace(o.Label) == "" {
				return nil, fmt.Errorf("question %d option %d has no label", i+1, j+1)
			}
		}
	}

	// Canonical request text: re-marshal the parsed args so a retry of the same call is
	// byte-identical regardless of the client's key order or whitespace.
	request := mustJSON(args)

	el, err := s.st.Elicitations().PendingByRequest(ctx, run.ID, domain.ElicitationQuestion, request)
	if errors.Is(err, store.ErrNotFound) {
		el, err = s.openElicitation(ctx, run, domain.ElicitationQuestion, request,
			"mcp__lexicode__ask_human", truncTitle("Question: "+args.Questions[0].Question))
	}
	if err != nil {
		return nil, err
	}

	s.transition(ctx, run.ID, domain.RunNeedsInput, "waiting for an answer")
	resp, err := s.await(ctx, run, el.ID)
	if err != nil {
		return nil, err
	}
	if len(resp.Answers) > 0 {
		return map[string]any{"answers": resp.Answers}, nil
	}
	return map[string]any{"response": resp.Text}, nil
}

// openElicitation writes the level-0 elicitation activity, the elicitations row keyed to it,
// and the run.elicitation frame — the shared open path for questions and parked approvals.
func (s *Server) openElicitation(ctx context.Context, run domain.Run, kind domain.ElicitationKind, request json.RawMessage, tool, title string) (domain.Elicitation, error) {
	seq := s.appendActivity(ctx, domain.Activity{
		RunID:    run.ID,
		Type:     domain.ActivityElicitation,
		Level:    0,
		ToolName: tool,
		GroupKey: tool,
		Title:    title,
		Payload:  request,
	})
	el := domain.Elicitation{
		ID:          domain.NewID(),
		RunID:       run.ID,
		ActivitySeq: seq,
		Kind:        kind,
		Request:     request,
		State:       domain.ElicitationPending,
		CreatedAt:   domain.FormatTime(s.now()),
	}
	if err := s.st.Elicitations().Create(context.WithoutCancel(ctx), &el); err != nil {
		return domain.Elicitation{}, fmt.Errorf("recording elicitation: %w", err)
	}
	s.emitElicitation(ctx, el)
	return el, nil
}

// ---- set_step --------------------------------------------------------------------------

type setStepArgs struct {
	Step  string `json:"step"`
	Index int64  `json:"index"`
	Total int64  `json:"total"`
}

// toolSetStep updates runs.current_step and publishes the run.step frame. Fire and forget:
// the result carries nothing the agent needs.
func (s *Server) toolSetStep(ctx context.Context, run domain.Run, raw json.RawMessage) (any, error) {
	var args setStepArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid set_step arguments: %w", err)
	}
	step := strings.TrimSpace(args.Step)
	if step == "" {
		return nil, errors.New("set_step needs a step")
	}
	if err := s.st.Runs().SetCurrentStep(ctx, run.ID, step); err != nil {
		return nil, fmt.Errorf("recording step: %w", err)
	}
	title := "Step: " + step
	if args.Index > 0 && args.Total > 0 {
		title = fmt.Sprintf("Step %d/%d: %s", args.Index, args.Total, step)
	}
	// Level 2: the step line is its own UI element (the run header); the transcript row is
	// the verbose-mode audit of when it changed.
	s.appendActivity(ctx, domain.Activity{
		RunID:    run.ID,
		Type:     domain.ActivityAction,
		Level:    2,
		ToolName: "mcp__lexicode__set_step",
		GroupKey: "mcp__lexicode__set_step",
		Title:    truncTitle(title),
		Payload:  mustJSON(args),
		OK:       boolPtr(true),
	})
	s.emitRunEvent(ctx, run, "step", map[string]any{
		"step": step, "index": args.Index, "total": args.Total,
	})
	return map[string]any{"ok": true}, nil
}

// ---- propose_wiki_page -----------------------------------------------------------------

type proposeWikiArgs struct {
	Title      string `json:"title"`
	Slug       string `json:"slug"`
	Parent     string `json:"parent"`
	Body       string `json:"body"`
	AgentScope string `json:"agent_scope"`
	Reason     string `json:"reason"`
	EditsSlug  string `json:"edits_slug"`
}

// toolProposeWikiPage writes a wiki page row in the `proposed` state — never live (brief
// §6.5; interaction rule 10) — plus a version snapshot attributed to the run, a
// wiki_proposal run output, and the wiki.proposed bus event. The create_wiki_pages
// permission is enforced here, in the service, per brief D7.
func (s *Server) toolProposeWikiPage(ctx context.Context, run domain.Run, raw json.RawMessage) (any, error) {
	var args proposeWikiArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid propose_wiki_page arguments: %w", err)
	}
	if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Body) == "" {
		return nil, errors.New("propose_wiki_page needs a title and a body")
	}
	agent, err := s.st.Agents().ByID(ctx, run.AgentID)
	if err != nil {
		return nil, err
	}
	if !agent.Permissions.CreateWikiPages {
		return nil, errors.New("this agent's permissions do not include create_wiki_pages")
	}
	scope := domain.AgentScope(args.AgentScope)
	if args.AgentScope == "" {
		scope = domain.ScopeAuto
	}
	if !scope.IsValid() {
		return nil, fmt.Errorf("unknown agent_scope %q", args.AgentScope)
	}

	nowStr := domain.FormatTime(s.now())
	page := domain.WikiPage{
		ID:              domain.NewID(),
		ProjectID:       run.ProjectID,
		Title:           args.Title,
		Body:            args.Body,
		AgentScope:      scope,
		TokenEstimate:   int64(len(args.Body) / 4),
		State:           domain.WikiProposed,
		ProposedByRunID: &run.ID,
		CreatedAt:       nowStr,
		UpdatedAt:       nowStr,
	}
	if reason := strings.TrimSpace(args.Reason); reason != "" {
		page.ProposedReason = &reason
	}

	baseSlug := slugify(args.Slug)
	if baseSlug == "" {
		baseSlug = slugify(args.Title)
	}
	if args.EditsSlug != "" {
		target, err := s.st.Wiki().BySlug(ctx, run.ProjectID, args.EditsSlug)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("edits_slug %q names no page in this project", args.EditsSlug)
			}
			return nil, err
		}
		version, err := s.st.Wiki().LatestVersion(ctx, target.ID)
		if err != nil {
			return nil, err
		}
		page.ProposalTargetID = &target.ID
		page.ProposedBaseVersion = &version
		baseSlug = slugify(args.EditsSlug)
	}
	if args.Parent != "" {
		if parent, err := s.st.Wiki().BySlug(ctx, run.ProjectID, args.Parent); err == nil {
			page.ParentID = &parent.ID
		}
		// A parent slug that names no page leaves the proposal at the root — the reviewer
		// places it; refusing the whole proposal over placement would lose the content.
	}

	// Slugs are unique per project, and a proposal must not claim the live page's slug
	// (edit proposals) or collide with an existing one — suffix until free.
	page.Slug = baseSlug
	if args.EditsSlug != "" {
		page.Slug = baseSlug + "-proposal"
	}
	for attempt := 0; ; attempt++ {
		err := s.st.Wiki().CreatePage(context.WithoutCancel(ctx), &page)
		if err == nil {
			break
		}
		if !errors.Is(err, store.ErrUnique) || attempt >= 3 {
			return nil, fmt.Errorf("recording proposal: %w", err)
		}
		page.Slug = baseSlug + "-proposal-" + strings.ToLower(domain.NewID()[20:])
	}

	if err := s.st.Wiki().CreateVersion(context.WithoutCancel(ctx), &domain.WikiVersion{
		ID: domain.NewID(), PageID: page.ID, Version: 1,
		Title: page.Title, Body: page.Body, FrontMatter: map[string]any{},
		AuthorRunID: &run.ID, CreatedAt: nowStr,
	}); err != nil {
		return nil, fmt.Errorf("recording proposal version: %w", err)
	}
	if err := s.st.RunOutputs().Append(context.WithoutCancel(ctx), &domain.RunOutput{
		ID: domain.NewID(), RunID: run.ID, Kind: domain.OutputWikiProposal,
		Ref: page.ID, Summary: page.Title, CreatedAt: nowStr,
	}); err != nil {
		s.logger.Warn("mcp: wiki proposal output not recorded", "error", err.Error())
	}

	s.appendActivity(ctx, domain.Activity{
		RunID:    run.ID,
		Type:     domain.ActivityAction,
		Level:    1,
		ToolName: "mcp__lexicode__propose_wiki_page",
		GroupKey: "mcp__lexicode__propose_wiki_page",
		Title:    truncTitle(`Proposed wiki page "` + page.Title + `"`),
		Payload: mustJSON(map[string]any{
			"page_id": page.ID, "slug": page.Slug, "reason": args.Reason,
			"edits_slug": args.EditsSlug,
		}),
		OK: boolPtr(true),
	})
	s.emitWikiProposed(ctx, run, page, args.Reason)

	return map[string]any{
		"ok": true, "page_id": page.ID, "slug": page.Slug, "state": "proposed",
	}, nil
}

// emitWikiProposed publishes the wiki.proposed frame: Kind "wiki" + subject wiki maps onto
// the project topic in the hub.
func (s *Server) emitWikiProposed(ctx context.Context, run domain.Run, page domain.WikiPage, reason string) {
	if s.bus == nil {
		return
	}
	pid, pageID := run.ProjectID, page.ID
	e := domain.Event{
		ProjectID: &pid, Kind: "wiki", ActivityType: "proposed",
		SubjectKind: "wiki", SubjectID: &pageID,
		ActorKind: domain.ActorAgent, ActorID: &run.AgentID,
		Payload: mustJSON(map[string]any{
			"page": map[string]any{
				"id": page.ID, "slug": page.Slug, "title": page.Title,
				"state": string(page.State), "reason": reason,
			},
			"run_id": run.ID,
		}),
		OccurredAt: domain.FormatTime(s.now()),
	}
	if err := s.bus.Emit(context.WithoutCancel(ctx), e); err != nil {
		s.logger.Error("mcp: emit wiki.proposed failed", "error", err.Error())
	}
}

// ---- check_criterion -------------------------------------------------------------------

type checkCriterionArgs struct {
	CriterionID string `json:"criterion_id"`
	Met         bool   `json:"met"`
	Note        string `json:"note"`
}

// toolCheckCriterion marks one acceptance criterion met/unmet with checked_by_run_id
// attribution. Criteria of other tickets are refused — a run may only check off the ticket
// it is coupled to.
func (s *Server) toolCheckCriterion(ctx context.Context, run domain.Run, raw json.RawMessage) (any, error) {
	var args checkCriterionArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid check_criterion arguments: %w", err)
	}
	if args.CriterionID == "" {
		return nil, errors.New("check_criterion needs a criterion_id")
	}
	c, err := s.st.Criteria().ByID(ctx, args.CriterionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("criterion %s does not exist", args.CriterionID)
		}
		return nil, err
	}
	if run.TicketID == nil || *run.TicketID != c.TicketID {
		return nil, errors.New("this criterion belongs to a different ticket; a run may only check criteria of its own ticket")
	}

	c.Checked = args.Met
	c.Note = args.Note
	c.CheckedByRunID = &run.ID
	c.CheckedByUserID = nil
	c.UpdatedAt = domain.FormatTime(s.now())
	if err := s.st.Criteria().Update(context.WithoutCancel(ctx), &c); err != nil {
		return nil, fmt.Errorf("recording criterion: %w", err)
	}

	verb := "met"
	ok := true
	if !args.Met {
		verb = "unmet"
		ok = false
	}
	s.appendActivity(ctx, domain.Activity{
		RunID:    run.ID,
		Type:     domain.ActivityAction,
		Level:    1,
		ToolName: "mcp__lexicode__check_criterion",
		GroupKey: "mcp__lexicode__check_criterion",
		Title:    truncTitle("Criterion " + verb + ": " + c.Text),
		Payload: mustJSON(map[string]any{
			"criterion_id": c.ID, "met": args.Met, "note": args.Note,
		}),
		OK: &ok,
	})
	return map[string]any{"ok": true, "criterion_id": c.ID, "met": args.Met}, nil
}

// ---- helpers ---------------------------------------------------------------------------

var slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

// slugify renders a title (or a user-suggested slug) as a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugCleaner.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
