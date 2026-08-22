package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
)

// reviewRecorder stands in for service/runs.ReviewSubmitter: it records what the tool asked
// the forge to write and can be made to refuse, the way the adapter refuses APPROVE and a
// missing submit_reviews grant.
type reviewRecorder struct {
	mu    sync.Mutex
	calls []reviewCall
	err   error
}

type reviewCall struct {
	runID    string
	prNumber int
	event    string
	body     string
}

func (r *reviewRecorder) submit(_ context.Context, run domain.Run, prNumber int, event, body string) (domain.Review, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return domain.Review{}, r.err
	}
	r.calls = append(r.calls, reviewCall{runID: run.ID, prNumber: prNumber, event: event, body: body})
	return domain.Review{ID: 7001, PRNumber: prNumber, State: event}, nil
}

func (r *reviewRecorder) last() reviewCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

func (r *reviewRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// A review with a blocker defaults to REQUEST_CHANGES, renders the severity tags, and
// records a level-1 activity.
func TestSubmitReviewSeverityTaggedBody(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})

	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 12,
		"summary":   "Two problems and a nit.",
		"findings": []map[string]any{
			{"severity": "blocker", "title": "Retry path double-charges",
				"detail": "charge() is called before the key is persisted.",
				"file":   "src/charge.ts", "line": 88},
			{"severity": "nit", "title": "Stray console.log", "file": "src/charge.ts"},
		},
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if result["event"] != "REQUEST_CHANGES" {
		t.Fatalf("event = %v, want REQUEST_CHANGES (a blocker is present)", result["event"])
	}
	call := e.reviews.last()
	if call.prNumber != 12 || call.event != "REQUEST_CHANGES" || call.runID != f.run.ID {
		t.Fatalf("forge call = %+v", call)
	}
	for _, want := range []string{
		"Two problems and a nit.",
		"1 blocker · 1 nit",
		"**[BLOCKER]** Retry path double-charges — `src/charge.ts:88`",
		"**[NIT]** Stray console.log — `src/charge.ts`",
	} {
		if !strings.Contains(call.body, want) {
			t.Fatalf("review body missing %q:\n%s", want, call.body)
		}
	}

	acts, err := e.st.Activities().ForRun(context.Background(), f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range acts {
		if a.ToolName == "mcp__lexicode__submit_review" {
			found = true
			if !strings.Contains(a.Title, "PR #12") {
				t.Fatalf("activity title = %q", a.Title)
			}
		}
	}
	if !found {
		t.Fatal("no submit_review activity recorded")
	}
}

// Only nits: the default event is a plain comment.
func TestSubmitReviewDefaultsToCommentWithoutBlockers(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 4,
		"findings":  []map[string]any{{"severity": "nit", "title": "Name the constant"}},
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if result["event"] != "COMMENT" {
		t.Fatalf("event = %v, want COMMENT", result["event"])
	}
}

// Brief D6: no argument, and no permission, makes an agent's approval possible.
func TestSubmitReviewRefusesApprove(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 3, "event": "approve",
		"findings": []map[string]any{{"severity": "nit", "title": "fine"}},
	})
	if !isErr {
		t.Fatalf("APPROVE was accepted: %v", result)
	}
	if !strings.Contains(result["text"].(string), "reserved for humans") {
		t.Fatalf("refusal = %v", result)
	}
	if e.reviews.count() != 0 {
		t.Fatal("an APPROVE reached the forge seam")
	}
}

// An unknown severity is refused before the forge is touched: the vocabulary is the contract.
func TestSubmitReviewRejectsUnknownSeverity(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 3,
		"findings":  []map[string]any{{"severity": "catastrophic", "title": "x"}},
	})
	if !isErr {
		t.Fatalf("unknown severity accepted: %v", result)
	}
	if e.reviews.count() != 0 {
		t.Fatal("an invalid review reached the forge seam")
	}
}

// The permission denial comes from the adapter, and its text reaches the agent verbatim.
func TestSubmitReviewSurfacesPermissionDenial(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{})
	e.reviews.err = &ports.PermissionDeniedError{AgentID: f.agent.ID, Grant: "submit_reviews"}
	isErr, result := e.callTool(f.token, "submit_review", map[string]any{
		"pr_number": 3,
		"findings":  []map[string]any{{"severity": "major", "title": "x"}},
	})
	if !isErr {
		t.Fatalf("a denied review reported success: %v", result)
	}
	if !strings.Contains(result["text"].(string), "submit_reviews") {
		t.Fatalf("denial did not name the grant: %v", result)
	}
}

// Without pr_number the tool reads the PR off the event that caused the run — the reviewer
// spawned by a "pull request opened" trigger never has to be told the number.
func TestSubmitReviewDefaultsPRNumberFromCausingEvent(t *testing.T) {
	e := newEnv(t)
	f := e.fixtures(domain.AutonomyAuto, domain.AgentPermissions{SubmitReviews: true})

	ctx := context.Background()
	number := int64(219)
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &f.project.ID, Source: "github.poll",
		Kind: "pull_request", ActivityType: "opened", ActorKind: domain.ActorAgent,
		SubjectKind: "pr", SubjectNumber: &number,
		Payload:       json.RawMessage(`{"pr":{"number":219}}`),
		DedupeKey:     "test:" + domain.NewID(),
		DispatchState: domain.DispatchDone,
		OccurredAt:    domain.Now(),
	}
	if err := e.st.Events().Insert(ctx, &ev); err != nil {
		t.Fatal(err)
	}
	// A trigger-spawned run: no ticket, one cause event.
	run := domain.Run{
		ID: domain.NewID(), Seq: 2, ProjectID: f.project.ID, AgentID: f.agent.ID,
		CauseEventID: &ev.ID, State: domain.RunRunning, Autonomy: domain.AutonomyAuto,
		Model: f.agent.Model, Effort: f.agent.Effort, Prompt: "review PR 219",
		RuntimeID: "scripted", SandboxID: "fake", SubjectKey: "pr:219",
		QueuedAt: domain.Now(),
	}
	if err := e.st.Runs().Create(ctx, &run); err != nil {
		t.Fatal(err)
	}
	token, err := e.mcp.MintToken(run.ID)
	if err != nil {
		t.Fatal(err)
	}

	isErr, result := e.callTool(token, "submit_review", map[string]any{
		"summary": "Looks reasonable.",
	})
	if isErr {
		t.Fatalf("submit_review failed: %v", result)
	}
	if got := e.reviews.last().prNumber; got != 219 {
		t.Fatalf("pr_number = %d, want 219 (from the causing event)", got)
	}
}
