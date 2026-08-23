// context_test.go is the S34 acceptance at the scheduler level (architecture §11): the
// recorded run_context_items carry the exact reason strings the Context panel renders, the
// dry preview and a real enqueue agree token-for-token, and a blown context budget warns
// without stopping the run.
package sched_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	contextmod "github.com/spruce/lexicode/internal/module/context"
)

// wikiPage seeds one live wiki page.
func (e *env) wikiPage(projectID, title string, scope domain.AgentScope, mutate func(*domain.WikiPage)) domain.WikiPage {
	e.t.Helper()
	now := domain.Now()
	pg := domain.WikiPage{
		ID: domain.NewID(), ProjectID: projectID, Slug: domain.NewID(),
		Title: title, Position: 1, AgentScope: scope,
		Body: "Body of " + title + ".", TokenEstimate: 25,
		State: domain.WikiLive, CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&pg)
	}
	if err := e.st.Wiki().CreatePage(context.Background(), &pg); err != nil {
		e.t.Fatal(err)
	}
	return pg
}

// stubDocs lists a fixed instruction-file set through the contextmod DocLister seam.
type stubDocs struct{ files map[string]string }

func (f stubDocs) ReadFileIfExists(_ context.Context, _ ports.Creds, _ domain.RepoRef, _ string, path string) ([]byte, bool, error) {
	content, ok := f.files[path]
	if !ok {
		return nil, false, nil
	}
	return []byte(content), true, nil
}

func (f stubDocs) ListDir(context.Context, ports.Creds, domain.RepoRef, string, string) ([]domain.DirEntry, error) {
	return nil, nil
}

// repoFilesForTest builds a real repofiles provider over the env's store: a secrets store
// is opened, a repo token written, and the seeded repo row pointed at it, so the provider's
// whole path (repo row → token → DocLister) runs against the stub docs.
func repoFilesForTest(t *testing.T, e *env, f fixtures, docs contextmod.DocLister) ports.ContextProvider {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sec, err := secrets.Open(secrets.Options{
		Store: e.st, KeyPath: filepath.Join(t.TempDir(), "master.key"), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, _, err := sec.Set(ctx, secrets.SetInput{
		Scope: domain.SecretScopeProject, ProjectID: f.project.ID, Name: "GITHUB_TOKEN",
		Value: "ghp_test", CreatedBy: e.ownerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := e.st.Repos().ByProject(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo.TokenSecretID = &info.ID
	if err := e.st.Repos().Update(ctx, &repo); err != nil {
		t.Fatal(err)
	}
	return contextmod.NewRepoFilesProvider(e.st, sec, docs, logger)
}

// runRequest is the delegate-shaped request the S34 tests enqueue.
func runRequest(f fixtures, tk domain.Ticket, changed []string) sched.RunRequest {
	return sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID, TicketID: tk.ID,
		Reason: "delegate button", ChangedPaths: changed,
	}
}

// TestRunContextItemsExactReasons: a run's recorded context stack lists exactly the injected
// pages with the exact reason strings — `always`, `matched path infra/deploy.ts`,
// `retrieved for "deployment"` — and repofiles items as Injected=false `repo file` (D-11).
func TestRunContextItemsExactReasons(t *testing.T) {
	docs := stubDocs{files: map[string]string{"AGENTS.md": "# Agreements\nRun make check."}}
	e := newEnv(t, options{fixture: fixtureOK})
	f := e.seed(2, nil, nil)

	e.providers = []ports.ContextProvider{
		contextmod.NewTicketProvider(e.st),
		contextmod.NewWikiProvider(e.st),
		contextmod.NewProjectProvider(e.st),
		repoFilesForTest(t, e, f, docs),
	}

	e.wikiPage(f.project.ID, "Working agreements", domain.ScopeAlways, nil)
	e.wikiPage(f.project.ID, "Deploy pitfalls", domain.ScopePaths, func(pg *domain.WikiPage) {
		pg.ScopePaths = []string{"infra/**"}
	})
	e.wikiPage(f.project.ID, "Deployment runbook", domain.ScopeAuto, func(pg *domain.WikiPage) {
		pg.Tags = []string{"deployment"}
	})
	e.wikiPage(f.project.ID, "Manual only", domain.ScopeManual, nil)

	tk := e.ticket(f, f.backlog, "Fix the deployment pipeline")
	run, err := e.sch.Enqueue(context.Background(), runRequest(f, tk, []string{"infra/deploy.ts"}))
	if err != nil {
		t.Fatal(err)
	}

	items, err := e.st.RunContextItems().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		provider, reason string
		injected         bool
	}
	wants := []want{
		{"project", "project guidance", true},
		{"wiki", "always", true},
		{"wiki", "matched path infra/deploy.ts", true},
		{"wiki", `retrieved for "deployment"`, true},
		{"ticket", "ticket " + tk.Key, true},
		{"repofiles", "repo file", false},
	}
	if len(items) != len(wants) {
		t.Fatalf("items = %d, want %d: %+v", len(items), len(wants), items)
	}
	for i, w := range wants {
		if items[i].Provider != w.provider || items[i].Reason != w.reason || items[i].Injected != w.injected {
			t.Errorf("items[%d] = {%s %q injected=%v}, want {%s %q injected=%v}",
				i, items[i].Provider, items[i].Reason, items[i].Injected,
				w.provider, w.reason, w.injected)
		}
		if items[i].Position != int64(i+1) {
			t.Errorf("items[%d].Position = %d, want %d", i, items[i].Position, i+1)
		}
	}
	// The manual page must not appear at all.
	for _, it := range items {
		if strings.Contains(it.Title, "Manual") {
			t.Errorf("manual-scoped page was injected: %+v", it)
		}
	}
	// Injected wiki bodies are in the prompt; the repo file is not.
	if !strings.Contains(run.Prompt, "Body of Working agreements.") {
		t.Error("always page body missing from the prompt")
	}
	if strings.Contains(run.Prompt, "AGENTS.md") {
		t.Error("repo file leaked into the prompt; it is listed, not injected (D-11)")
	}
}

// TestPreviewRunParity: the agent-detail dry preview and a real ticketless run resolve
// token-for-token identical stacks when nothing changed between them — one resolver, one
// code path (architecture §11).
func TestPreviewRunParity(t *testing.T) {
	docs := stubDocs{files: map[string]string{"AGENTS.md": "# Agreements\nRun make check."}}
	e := newEnv(t, options{fixture: fixtureOK})
	f := e.seed(2, nil, nil)
	e.providers = []ports.ContextProvider{
		contextmod.NewTicketProvider(e.st),
		contextmod.NewWikiProvider(e.st),
		contextmod.NewProjectProvider(e.st),
		repoFilesForTest(t, e, f, docs),
	}
	e.wikiPage(f.project.ID, "Working agreements", domain.ScopeAlways, nil)
	e.wikiPage(f.project.ID, "Deployment runbook", domain.ScopeAuto, func(pg *domain.WikiPage) {
		pg.Tags = []string{"deployment"}
	})

	preview, err := e.sch.PreviewContext(context.Background(), f.project.ID, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A ticketless manual run with no reason: the same empty TaskSummary/ChangedPaths.
	run, err := e.sch.Enqueue(context.Background(), sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := e.st.RunContextItems().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(preview) != len(items) {
		t.Fatalf("preview has %d items, run has %d", len(preview), len(items))
	}
	var previewTotal, runTotal int64
	for i := range preview {
		p, r := preview[i], items[i]
		if p.Provider != r.Provider || p.SourceRef != r.SourceRef || p.Reason != r.Reason ||
			p.Tokens != r.Tokens || p.Injected != r.Injected || p.Position != r.Position {
			t.Errorf("item %d differs: preview {%s %s %q %d %v} vs run {%s %s %q %d %v}",
				i, p.Provider, p.SourceRef, p.Reason, p.Tokens, p.Injected,
				r.Provider, r.SourceRef, r.Reason, r.Tokens, r.Injected)
		}
		previewTotal += p.Tokens
		runTotal += r.Tokens
	}
	if previewTotal != runTotal {
		t.Fatalf("token totals differ: preview %d, run %d", previewTotal, runTotal)
	}
}

// TestContextBudgetExceededWarnsAndProceeds: always-on pages past the project threshold
// leave the run queued (it proceeds) with a level-1 warning activity on its transcript.
func TestContextBudgetExceededWarnsAndProceeds(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK})
	f := e.seed(2, nil, nil)
	e.providers = []ports.ContextProvider{
		contextmod.NewTicketProvider(e.st),
		contextmod.NewWikiProvider(e.st),
		contextmod.NewProjectProvider(e.st),
	}
	// Project threshold 100; one always page of 250 tokens blows it.
	threshold := int64(100)
	f.project.ContextThresholdTokens = &threshold
	if err := e.st.Projects().Update(context.Background(), &f.project); err != nil {
		t.Fatal(err)
	}
	e.wikiPage(f.project.ID, "Everything always", domain.ScopeAlways, func(pg *domain.WikiPage) {
		pg.TokenEstimate = 250
	})

	tk := e.ticket(f, f.backlog, "Small fix")
	run, err := e.sch.Enqueue(context.Background(), runRequest(f, tk, nil))
	if err != nil {
		t.Fatal(err)
	}
	if run.State != domain.RunQueued {
		t.Fatalf("run state = %s, want queued (the run proceeds)", run.State)
	}
	acts, err := e.st.Activities().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if a.Type == domain.ActivitySystem && strings.HasPrefix(a.Title, "Context budget exceeded") {
			found = true
			if a.Level != 1 {
				t.Errorf("warning level = %d, want 1", a.Level)
			}
			if !strings.Contains(a.Title, "250") || !strings.Contains(a.Title, "100") {
				t.Errorf("warning title lacks the numbers: %q", a.Title)
			}
		}
	}
	if !found {
		t.Fatalf("no context-budget warning activity; activities: %+v", acts)
	}
}

// TestContextBudgetUnderThresholdSilent: no warning when the always total fits.
func TestContextBudgetUnderThresholdSilent(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK})
	f := e.seed(2, nil, nil)
	e.providers = []ports.ContextProvider{
		contextmod.NewWikiProvider(e.st),
		contextmod.NewProjectProvider(e.st),
		contextmod.NewTicketProvider(e.st),
	}
	e.wikiPage(f.project.ID, "Small always", domain.ScopeAlways, func(pg *domain.WikiPage) {
		pg.TokenEstimate = 10 // workspace default threshold is 4000
	})
	tk := e.ticket(f, f.backlog, "Small fix")
	run, err := e.sch.Enqueue(context.Background(), runRequest(f, tk, nil))
	if err != nil {
		t.Fatal(err)
	}
	acts, err := e.st.Activities().ForRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if strings.HasPrefix(a.Title, "Context budget exceeded") {
			t.Fatalf("unexpected budget warning: %q", a.Title)
		}
	}
}

// TestTriggerSpawnedRunPromptCarriesTheEvent is the regression for the reviewer that reviewed
// the wrong thing. A trigger-spawned run has no ticket, so before the `event` provider its
// prompt could contain nothing at all about the pull request it was started for — the run row
// knew `subject_key = pr:219` and carried a cause_event_id whose payload held the whole
// normalized event, and none of it reached the agent.
//
// The assertion is deliberately about the PROMPT, not about the provider: the provider can be
// correct and still be unwired (it was), and what the agent actually reads is the prompt.
func TestTriggerSpawnedRunPromptCarriesTheEvent(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK})
	f := e.seed(2, nil, nil)
	e.providers = []ports.ContextProvider{
		contextmod.NewProjectProvider(e.st),
		contextmod.NewEventProvider(e.st),
		contextmod.NewTicketProvider(e.st),
	}

	ctx := context.Background()
	number := int64(219)
	branch := "dev/PAY-14-idempotency"
	login := "spruce"
	ev := domain.Event{
		ID: domain.NewID(), ProjectID: &f.project.ID, Source: "github.poll",
		Kind: "pull_request", ActivityType: "opened", ActorKind: domain.ActorAgent,
		ActorLogin: &login, SubjectKind: "pr", SubjectNumber: &number, SubjectBranch: &branch,
		Payload: []byte(`{"pr":{"number":219,"title":"Idempotency keys for POST /charges",` +
			`"author":"spruce","author_kind":"agent","branch":"dev/PAY-14-idempotency",` +
			`"base":"main","draft":false,"state":"open","files_changed":7,` +
			`"body":"Adds a replay cache."},` +
			`"repo":{"owner":"acme","name":"payments","default_branch":"main"},` +
			`"actor":{"kind":"agent","login":"spruce","agent":"Dev"}}`),
		DedupeKey: "test:" + domain.NewID(), DispatchState: domain.DispatchDone,
		OccurredAt: domain.Now(), CreatedAt: domain.Now(),
	}
	if err := e.st.Events().Insert(ctx, &ev); err != nil {
		t.Fatal(err)
	}

	// Exactly what the run_agent action enqueues for a trigger firing: no ticket, no prompt
	// override, a cause event and a subject.
	run, err := e.sch.Enqueue(ctx, sched.RunRequest{
		ProjectID: f.project.ID, AgentID: f.agent.ID,
		Reason:       "trigger Agent PR opened → run Reviewer",
		CauseEventID: ev.ID, SubjectKey: "pr:219",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(run.Prompt, "# What happened") {
		t.Fatalf("the prompt has no event section:\n%s", run.Prompt)
	}
	// The number is the load-bearing fact: submit_review defaults to it, and an agent that
	// cannot name the pull request cannot review it.
	for _, want := range []string{
		"pull request #219",
		"dev/PAY-14-idempotency",
		"Idempotency keys for POST /charges",
		"spruce",
	} {
		if !strings.Contains(run.Prompt, want) {
			t.Errorf("the prompt does not name %q:\n%s", want, run.Prompt)
		}
	}
	// Prose, not a JSON blob: the payload must never be dumped verbatim.
	if strings.Contains(run.Prompt, `"author_kind":`) {
		t.Errorf("the raw payload leaked into the prompt:\n%s", run.Prompt)
	}

	// And the Context panel can explain it.
	items, err := e.st.RunContextItems().ForRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items {
		if it.Provider != "event" {
			continue
		}
		found = true
		if it.SourceKind != "event" || it.SourceRef != "pr:219" || !it.Injected {
			t.Errorf("event item = %+v", it)
		}
		if !strings.Contains(it.Reason, "pull_request opened") {
			t.Errorf("event item reason = %q", it.Reason)
		}
	}
	if !found {
		t.Fatalf("no `event` row in the run's context stack: %+v", items)
	}
}

// TestManualRunHasNoEventSection: the provider is silent for a run nobody triggered, so a
// delegated run's prompt is unchanged by any of this.
func TestManualRunHasNoEventSection(t *testing.T) {
	e := newEnv(t, options{fixture: fixtureOK})
	f := e.seed(2, nil, nil)
	e.providers = []ports.ContextProvider{
		contextmod.NewProjectProvider(e.st),
		contextmod.NewEventProvider(e.st),
		contextmod.NewTicketProvider(e.st),
	}
	tk := e.ticket(f, f.backlog, "Fix the deployment pipeline")
	run, err := e.sch.Enqueue(context.Background(), runRequest(f, tk, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(run.Prompt, "# What happened") {
		t.Fatalf("a manually delegated run got an event section:\n%s", run.Prompt)
	}
}
