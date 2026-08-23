package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// ---- submit_review -----------------------------------------------------------------------
//
// The sixth tool (S39), and the caller contracts §2.2's SubmitReview was always waiting for.
// Step 4 of the brief's canonical chain is "Reviewer posts a review with severity-tagged
// findings"; this is how the findings get out of the container.
//
// Why an MCP tool and not a trigger action or a completion hook: a review's *content* is the
// agent's analysis, so it has to leave the container as data the agent produced (a trigger
// action could only post a static template). And it must not leave as a `gh` call, because
// then the submit_reviews grant, the D-9 marker and the run_outputs row would live in a
// prompt instead of in the adapter. So: the agent calls the tool, the ORCHESTRATOR performs
// the forge write through the port — the same division of labour as PR opening (propen.go).
//
// APPROVE is not an option the tool exposes and not a value the adapter accepts: agents never
// approve (brief D6). The `event` argument therefore has two legal values, and its default is
// derived from the findings' severities.

// ReviewSubmitFn is the forge seam: submit a review on prNumber, as run's agent, with the
// already-rendered body. Implemented by service/runs.ReviewSubmitter and wired in
// cmd/lexicode; nil means the tool reports that review submission is not wired.
type ReviewSubmitFn func(ctx context.Context, run domain.Run, prNumber int, event, body string) (domain.Review, error)

type submitReviewArgs struct {
	PRNumber int              `json:"pr_number"`
	Event    string           `json:"event"`
	Summary  string           `json:"summary"`
	Findings []reviewFindings `json:"findings"`
}

type reviewFindings struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	File     string `json:"file"`
	Line     int64  `json:"line"`
}

// severityOrder is both the vocabulary and the rendering order. blocker and major are the two
// that make a review "changes requested" when the agent does not say otherwise.
var severityOrder = []string{"blocker", "major", "minor", "nit"}

func knownSeverity(s string) bool {
	for _, k := range severityOrder {
		if k == s {
			return true
		}
	}
	return false
}

// severityNone is review.max_severity when a review carries no findings at all — a value, not
// an absence, so `review.max_severity enum.is_not none` is a condition a user can write.
const severityNone = "none"

// maxSeverity is the worst severity present, or severityNone.
func maxSeverity(counts map[string]int) string {
	for _, sev := range severityOrder {
		if counts[sev] > 0 {
			return sev
		}
	}
	return severityNone
}

// The internal event this tool emits when a review is submitted.
//
// Why it exists: the trigger that continues the loop ("changes requested → Dev addresses
// them") used to key on `review.state` read back from GitHub's API by the poller, and that
// round-trip loses everything. It loses the structure — the tool has severities, the API has
// one enum — and under D-9 it loses the verdict itself: every agent shares one project PAT,
// so a reviewer agent is the pull request's own author to GitHub, and GitHub answers 422 to
// REQUEST_CHANGES from the author. The review lands as COMMENTED and the chain stops.
//
// So the fact "a review with blockers was submitted" is published here, where it is known,
// as an event of its own kind. A distinct kind (not another activity type of
// `pull_request_review`) is what makes double-firing structurally impossible: the poller's
// event for the same review still arrives, and no single trigger can match both, because a
// trigger names exactly one event kind. Which of the two a rule should use is a choice the
// user makes in the editor; the bootstrap-suggested rule uses this one.
//
// The strings are duplicated in internal/module/github/catalog.go, which catalogs the
// descriptor for the trigger editor. Service → module is a forbidden import edge
// (architecture §2.1), so, as elsewhere in this codebase, the string IS the protocol.
const (
	agentReviewKind     = "agent_review"
	agentReviewActivity = "submitted"
)

// reviewState maps the forge event onto the contracts §4 `review.state` vocabulary — the same
// lowercase values the poller derives from GitHub's own review state, so a condition written
// against one event reads the same way against the other.
func reviewState(event string) string {
	if event == "REQUEST_CHANGES" {
		return "changes_requested"
	}
	return "commented"
}

// toolSubmitReview validates the findings, renders the severity-tagged markdown body and
// hands it to the forge seam. Everything that is enforcement — the submit_reviews grant, the
// actor marker, the run_outputs row, the audit row, the APPROVE refusal — happens on the
// other side of that seam, in the adapter.
func (s *Server) toolSubmitReview(ctx context.Context, run domain.Run, raw json.RawMessage) (any, error) {
	var args submitReviewArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid submit_review arguments: %w", err)
	}
	if s.submitReview == nil {
		return nil, errors.New("review submission is not wired in this process")
	}
	if len(args.Findings) == 0 && strings.TrimSpace(args.Summary) == "" {
		return nil, errors.New("submit_review needs a summary, findings, or both")
	}
	counts := map[string]int{}
	for i, f := range args.Findings {
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		if !knownSeverity(sev) {
			return nil, fmt.Errorf("finding %d: severity %q is not one of %s",
				i+1, f.Severity, strings.Join(severityOrder, ", "))
		}
		if strings.TrimSpace(f.Title) == "" {
			return nil, fmt.Errorf("finding %d has no title", i+1)
		}
		args.Findings[i].Severity = sev
		counts[sev]++
	}

	number := args.PRNumber
	if number <= 0 {
		n, err := s.causePRNumber(ctx, run)
		if err != nil {
			return nil, err
		}
		number = n
	}

	event := strings.ToUpper(strings.TrimSpace(args.Event))
	switch event {
	case "":
		event = "COMMENT"
		if counts["blocker"]+counts["major"] > 0 {
			event = "REQUEST_CHANGES"
		}
	case "COMMENT", "REQUEST_CHANGES":
		// allowed; passed through to the adapter, which is the authority
	case "APPROVE":
		// Refused here too, so the message names the tool the agent actually called. The
		// adapter refuses it as well — this is a nicety, not the enforcement.
		return nil, errors.New(
			"agents cannot approve pull requests: approval is reserved for humans (brief D6). " +
				"Use COMMENT or REQUEST_CHANGES")
	default:
		return nil, fmt.Errorf("event %q is not supported; use COMMENT or REQUEST_CHANGES", args.Event)
	}

	body := renderReview(args.Summary, args.Findings)

	// The intent is recorded before the forge gets a say, because the forge may refuse it.
	//
	// REQUEST_CHANGES is still attempted whenever the findings call for it, even though under
	// D-9's single project PAT GitHub answers 422 today. It costs one API call that fails
	// closed and writes nothing; it starts working the moment an agent has an identity of its
	// own (a GitHub App installation, a per-agent PAT) with no code change; and — the reason
	// that decides it — a run whose output says "commented" when the reviewer demanded changes
	// is a record that misleads the human reading it. What was intended and what GitHub
	// accepted are two different facts, and both are kept: `intended_event` and `event` here,
	// `review.intended_state` and `review.state` on the emitted event.
	intended := event
	review, err := s.submitReview(context.WithoutCancel(ctx), run, number, event, body)
	if err != nil && event == "REQUEST_CHANGES" && errors.Is(err, ports.ErrReviewEventRejected) {
		s.logger.Info("mcp: the forge refused a REQUEST_CHANGES review; retrying as a comment",
			slog.String("run", run.ID), slog.Int("pr", number), slog.String("reason", err.Error()))
		refusal := err
		event = "COMMENT"
		review, err = s.submitReview(context.WithoutCancel(ctx), run, number, event, body)
		if err != nil {
			// Both attempts failed: report the refusal that started it, not just the retry.
			return nil, fmt.Errorf("%w (after %v)", err, refusal)
		}
	}
	if err != nil {
		return nil, err
	}
	downgraded := event != intended

	verdict := strings.ToLower(strings.ReplaceAll(intended, "_", " "))
	if downgraded {
		verdict += ", posted as a comment"
	}
	ok := true
	s.appendActivity(ctx, domain.Activity{
		RunID:    run.ID,
		Type:     domain.ActivityAction,
		Level:    1,
		ToolName: "mcp__lexicode__submit_review",
		GroupKey: "mcp__lexicode__submit_review",
		Title: truncTitle(fmt.Sprintf("Reviewed PR #%d: %s (%d findings)",
			number, verdict, len(args.Findings))),
		Payload: mustJSON(map[string]any{
			"pr_number": number, "event": event, "intended_event": intended,
			"severities": counts, "max_severity": maxSeverity(counts), "review_id": review.ID,
		}),
		OK: &ok,
	})

	s.emitAgentReview(ctx, run, agentReview{
		prNumber: number, event: event, intended: intended,
		review: review, body: body, counts: counts, findings: len(args.Findings),
	})

	return map[string]any{
		"ok": true, "pr_number": number, "event": event, "intended_event": intended,
		"review_id": fmt.Sprint(review.ID), "severities": counts,
		"max_severity": maxSeverity(counts),
	}, nil
}

// agentReview is what emitAgentReview needs to know about a submission that succeeded.
type agentReview struct {
	prNumber int
	event    string // what the forge accepted
	intended string // what the tool asked for
	review   domain.Review
	body     string
	counts   map[string]int
	findings int
}

// emitAgentReview publishes the `agent_review.submitted` event: the reviewing agent is the
// ACTOR and the pull request is the SUBJECT, so every loop-protection layer treats it exactly
// like a poller event about the same pull request — actor suppression can recognise the
// reviewer (and therefore does not suppress a rule that runs somebody else), debounce and
// cancel-in-progress key on the same "pr:<n>" subject the engine derives for the poller's
// events, and the depth counter can walk cause_run_id up through the review to the run that
// produced it.
//
// Best-effort, like every other emit on this path: the review is on GitHub and the activity
// is written by the time this runs, so a failure is logged, never unwound.
func (s *Server) emitAgentReview(ctx context.Context, run domain.Run, in agentReview) {
	if s.bus == nil {
		return
	}
	severities := map[string]int{}
	for _, sev := range severityOrder {
		severities[sev] = in.counts[sev]
	}
	pr, repo, branch := s.prContext(ctx, run, in.prNumber)

	agentName := ""
	if a, err := s.st.Agents().ByID(ctx, run.AgentID); err == nil {
		agentName = a.Name
	}
	payload := map[string]any{
		// The review sub-object of contracts §4, extended: the same id/author/state/body an
		// event from the poller carries, plus the structure only this side of the round-trip
		// has. `state` is derived from the event the forge accepted rather than read off the
		// returned row so it is always one of the two §4 values.
		"review": map[string]any{
			"id":              fmt.Sprint(in.review.ID),
			"author":          in.review.AuthorLogin,
			"state":           reviewState(in.event),
			"intended_state":  reviewState(in.intended),
			"body":            in.body,
			"max_severity":    maxSeverity(in.counts),
			"severity_counts": severities,
			"findings_count":  in.findings,
		},
		"pr":    pr,
		"actor": map[string]any{"kind": string(domain.ActorAgent), "login": in.review.AuthorLogin, "agent": agentName},
	}
	if repo != nil {
		payload["repo"] = repo
	}

	pid, num, runID := run.ProjectID, int64(in.prNumber), run.ID
	e := domain.Event{
		ProjectID: &pid, Kind: agentReviewKind, ActivityType: agentReviewActivity,
		ActorKind: domain.ActorAgent, ActorID: &run.AgentID,
		SubjectKind: "pr", SubjectNumber: &num,
		CauseRunID: &runID,
		Payload:    mustJSON(payload),
		OccurredAt: domain.FormatTime(s.now()),
	}
	if in.review.AuthorLogin != "" {
		login := in.review.AuthorLogin
		e.ActorLogin = &login
	}
	if branch != "" {
		b := branch
		e.SubjectBranch = &b
	}
	if err := s.bus.Emit(context.WithoutCancel(ctx), e); err != nil {
		s.logger.Error("mcp: emit agent_review.submitted failed",
			slog.String("run", run.ID), slog.String("error", err.Error()))
	}
}

// prContext fills the `pr` and `repo` sub-objects of the emitted event from the event that
// caused this run — for the common case (a reviewer spawned by "pull request opened") that is
// the poller's own fully-populated pr object, so the follow-on rule can address pr.branch,
// pr.labels and the rest exactly as it would on a poller event.
//
// A review on some OTHER pull request than the one that caused the run yields just the number:
// honest about what is known, and every unknown path evaluates to nil, which every condition
// operator has defined behaviour for.
func (s *Server) prContext(ctx context.Context, run domain.Run, number int) (pr, repo map[string]any, branch string) {
	pr = map[string]any{"number": number}
	ev := s.causeEvent(ctx, run)
	if ev == nil {
		return pr, nil, ""
	}
	var payload struct {
		PR   map[string]any `json:"pr"`
		Repo map[string]any `json:"repo"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err == nil {
		if n, ok := payload.PR["number"].(float64); ok && int(n) == number {
			pr = payload.PR
			repo = payload.Repo
			if b, ok := pr["branch"].(string); ok {
				branch = strings.TrimSpace(b)
			}
		}
	}
	if branch == "" && ev.SubjectBranch != nil && ev.SubjectNumber != nil && int(*ev.SubjectNumber) == number {
		branch = strings.TrimSpace(*ev.SubjectBranch)
	}
	return pr, repo, branch
}

// causeEvent reads the event that caused this run, or nil when there is none or it cannot be
// read. Best-effort by design: it feeds enrichment, never a decision.
func (s *Server) causeEvent(ctx context.Context, run domain.Run) *domain.Event {
	if run.CauseEventID == nil {
		return nil
	}
	ev, err := s.st.Events().ByID(ctx, *run.CauseEventID)
	if err != nil {
		return nil
	}
	return &ev
}

// causePRNumber reads pr.number off the event that caused this run — the common case, where
// the reviewer was spawned by a "pull request opened" trigger and never had to be told the
// number. A run with no such event must pass pr_number explicitly.
func (s *Server) causePRNumber(ctx context.Context, run domain.Run) (int, error) {
	const ask = "pass pr_number explicitly"
	if run.CauseEventID == nil {
		return 0, errors.New("this run was not caused by a pull request event; " + ask)
	}
	ev, err := s.st.Events().ByID(ctx, *run.CauseEventID)
	if err != nil {
		return 0, fmt.Errorf("reading the causing event: %w", err)
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
	return 0, errors.New("the event that caused this run names no pull request; " + ask)
}

// renderReview turns the summary and findings into the review body GitHub will show:
// severities grouped, worst first, each tagged so a human scanning the review sees the shape
// of it before reading a word of prose.
func renderReview(summary string, findings []reviewFindings) string {
	var b strings.Builder
	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString(s)
		b.WriteString("\n")
	}
	bySeverity := map[string][]reviewFindings{}
	for _, f := range findings {
		bySeverity[f.Severity] = append(bySeverity[f.Severity], f)
	}
	if len(findings) > 0 {
		b.WriteString("\n")
		b.WriteString(severitySummary(bySeverity))
		b.WriteString("\n")
	}
	for _, sev := range severityOrder {
		group := bySeverity[sev]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", strings.ToUpper(sev))
		for _, f := range group {
			fmt.Fprintf(&b, "- **[%s]** %s", strings.ToUpper(sev), strings.TrimSpace(f.Title))
			if f.File != "" {
				fmt.Fprintf(&b, " — `%s", f.File)
				if f.Line > 0 {
					fmt.Fprintf(&b, ":%d", f.Line)
				}
				b.WriteString("`")
			}
			b.WriteString("\n")
			if detail := strings.TrimSpace(f.Detail); detail != "" {
				for _, line := range strings.Split(detail, "\n") {
					fmt.Fprintf(&b, "  %s\n", line)
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// severitySummary is the one-line count header, e.g. "1 blocker · 2 minor".
func severitySummary(bySeverity map[string][]reviewFindings) string {
	var parts []string
	for _, sev := range severityOrder {
		if n := len(bySeverity[sev]); n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, sev))
		}
	}
	return strings.Join(parts, " · ")
}
