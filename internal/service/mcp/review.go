package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
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
	review, err := s.submitReview(context.WithoutCancel(ctx), run, number, event, body)
	if err != nil {
		return nil, err
	}

	ok := true
	s.appendActivity(ctx, domain.Activity{
		RunID:    run.ID,
		Type:     domain.ActivityAction,
		Level:    1,
		ToolName: "mcp__lexicode__submit_review",
		GroupKey: "mcp__lexicode__submit_review",
		Title: truncTitle(fmt.Sprintf("Reviewed PR #%d: %s (%d findings)",
			number, strings.ToLower(strings.ReplaceAll(event, "_", " ")), len(args.Findings))),
		Payload: mustJSON(map[string]any{
			"pr_number": number, "event": event,
			"severities": counts, "review_id": review.ID,
		}),
		OK: &ok,
	})

	return map[string]any{
		"ok": true, "pr_number": number, "event": event,
		"review_id": fmt.Sprint(review.ID), "severities": counts,
	}, nil
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
