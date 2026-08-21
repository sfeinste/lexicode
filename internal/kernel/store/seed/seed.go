// Package seed produces a realistic fixture set: a workspace with two users, one project with a
// column per category, a board of tickets, two agents, and a little history of events, runs and
// activities. Tests assert cross-table invariants on it, and `lexicode serve --demo` loads it
// into an empty database so the dashboard has something to show before a single story of UI
// exists (story S03).
//
// Everything is inserted in one transaction: a half-seeded database never exists.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Data is what Apply created, so tests can reference fixtures by role instead of re-querying.
type Data struct {
	Owner   domain.User
	Member  domain.User
	Project domain.Project
	// Columns is in board order; ColumnByCategory indexes the same rows.
	Columns          []domain.Column
	ColumnByCategory map[domain.ColumnCategory]domain.Column
	Tickets          []domain.Ticket
	Agents           []domain.Agent
	Events           []domain.Event
	Runs             []domain.Run
}

// demoPasswordHash is a placeholder, deliberately not a valid argon2id hash: nobody can log in
// as a fixture user. Real hashing arrives with auth (S05); its tests create their own users.
const demoPasswordHash = "!seed-fixture-no-login"

// IsEmpty reports whether the database has no users — the guard `serve --demo` uses so that it
// never seeds over real data.
func IsEmpty(ctx context.Context, s *store.Store) (bool, error) {
	n, err := s.Users().Count(ctx)
	return n == 0, err
}

// Apply inserts the fixture set and returns it. It assumes a migrated, empty database; on a
// non-empty one it will fail on the first unique collision rather than half-merge.
func Apply(ctx context.Context, s *store.Store) (*Data, error) {
	var d *Data
	err := s.Tx(ctx, func(tx *store.Tx) error {
		var err error
		d, err = apply(ctx, tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return d, nil
}

func apply(ctx context.Context, tx *store.Tx) (*Data, error) {
	d := &Data{ColumnByCategory: map[domain.ColumnCategory]domain.Column{}}

	// A fixed base time keeps the fixture history stable and readably ordered; each step is a
	// minute apart so created_at sorts the way the story reads.
	base := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	step := 0
	at := func() string {
		step++
		return domain.FormatTime(base.Add(time.Duration(step) * time.Minute))
	}

	// ---- users ------------------------------------------------------------------------
	d.Owner = domain.User{
		ID: domain.NewID(), Email: "ada@example.com", DisplayName: "Ada Muir",
		PasswordHash: demoPasswordHash, Role: domain.RoleOwner, AvatarColor: "#7c5cff",
		CreatedAt: at(),
	}
	d.Member = domain.User{
		ID: domain.NewID(), Email: "theo@example.com", DisplayName: "Theo Brandt",
		PasswordHash: demoPasswordHash, Role: domain.RoleMember, AvatarColor: "#00a884",
		CreatedAt: at(),
	}
	for _, u := range []domain.User{d.Owner, d.Member} {
		u := u
		if err := tx.Users().Create(ctx, &u); err != nil {
			return nil, err
		}
	}

	// ---- project and board ------------------------------------------------------------
	d.Project = domain.Project{
		ID: domain.NewID(), Key: "PAY", Name: "Payments", Color: "#ff8a3d",
		Description:   "The payments service: checkout, refunds and the reconciliation worker.",
		OwnerID:       d.Owner.ID,
		AgentGuidance: "Prefer small PRs. Never touch the ledger tables without a migration.",
		CreatedAt:     at(), UpdatedAt: at(),
	}
	d.Project.UpdatedAt = d.Project.CreatedAt
	if err := tx.Projects().Create(ctx, &d.Project); err != nil {
		return nil, err
	}
	for _, uid := range []string{d.Owner.ID, d.Member.ID} {
		if err := tx.Projects().AddMember(ctx, d.Project.ID, uid); err != nil {
			return nil, err
		}
	}

	// One column per category — names are display strings and deliberately not the category
	// words, because nothing may ever key off them (plan rule 3).
	wip := int64(3)
	cols := []domain.Column{
		{Name: "Icebox", Category: domain.CategoryBacklog},
		{Name: "Up next", Category: domain.CategoryReady},
		{Name: "In progress", Category: domain.CategoryRunning, WIPLimit: &wip},
		{Name: "Needs review", Category: domain.CategoryReview},
		{Name: "Shipped", Category: domain.CategoryDone},
		{Name: "Abandoned", Category: domain.CategoryCanceled},
	}
	for i := range cols {
		c := &cols[i]
		c.ID = domain.NewID()
		c.ProjectID = d.Project.ID
		c.Position = int64(i + 1)
		c.CreatedAt = at()
		c.UpdatedAt = c.CreatedAt
		if err := tx.Columns().Create(ctx, c); err != nil {
			return nil, err
		}
		d.ColumnByCategory[c.Category] = *c
	}
	d.Columns = cols

	// ---- agents -----------------------------------------------------------------------
	agents := []domain.Agent{
		{
			Name: "Sonnet", Role: "Implementation", Color: "#5b8def",
			Model: "claude-sonnet-4-5", Effort: "medium", Autonomy: domain.AutonomyAutoGates,
			GitAuthorName: "Sonnet (Lexicode)", GitAuthorEmail: "sonnet@agents.example.com",
			Permissions: domain.AgentPermissions{
				ReadFiles: true, EditFiles: true, RunCommands: true,
				PushBranches: true, OpenPRs: true, CommentPRs: true,
				SubmitReviews: false, CreateWikiPages: true,
			},
		},
		{
			Name: "Sentry", Role: "Review", Color: "#d16ba5",
			Model: "claude-opus-4-1", Effort: "high", Autonomy: domain.AutonomyApproveEach,
			GitAuthorName: "Sentry (Lexicode)", GitAuthorEmail: "sentry@agents.example.com",
			Permissions: domain.AgentPermissions{
				ReadFiles: true, EditFiles: false, RunCommands: true,
				PushBranches: false, OpenPRs: false, CommentPRs: true,
				SubmitReviews: true, CreateWikiPages: false,
			},
		},
	}
	for i := range agents {
		a := &agents[i]
		a.ID = domain.NewID()
		a.ProjectID = d.Project.ID
		a.RuntimeID = "claude-code"
		a.ConcurrencyCap = 1
		a.MaxWallClockSeconds = 3600
		a.MaxSteps = 200
		a.Enabled = true
		a.CreatedAt = at()
		a.UpdatedAt = a.CreatedAt
		if err := tx.Agents().Create(ctx, a); err != nil {
			return nil, err
		}
	}
	d.Agents = agents
	implementer, reviewer := agents[0], agents[1]

	// ---- tickets ----------------------------------------------------------------------
	type spec struct {
		title    string
		column   domain.ColumnCategory
		priority domain.Priority
		assignee *string
		delegate *string
	}
	specs := []spec{
		{"Support partial refunds", domain.CategoryBacklog, domain.PriorityLow, nil, nil},
		{"Reconcile ledger drift nightly", domain.CategoryBacklog, domain.PriorityMedium, &d.Owner.ID, nil},
		{"Rate-limit the checkout endpoint", domain.CategoryBacklog, domain.PriorityNone, nil, nil},
		{"Add idempotency keys to POST /charges", domain.CategoryReady, domain.PriorityHigh, &d.Owner.ID, &implementer.ID},
		{"Upgrade Stripe SDK to v12", domain.CategoryReady, domain.PriorityMedium, &d.Member.ID, nil},
		{"Fix double-charge on retried webhooks", domain.CategoryRunning, domain.PriorityUrgent, &d.Owner.ID, &implementer.ID},
		{"Instrument refund latency", domain.CategoryRunning, domain.PriorityMedium, &d.Member.ID, &implementer.ID},
		{"Review currency rounding rules", domain.CategoryReview, domain.PriorityHigh, &d.Member.ID, &reviewer.ID},
		{"Migrate invoices to the new tax table", domain.CategoryDone, domain.PriorityMedium, &d.Owner.ID, &implementer.ID},
		{"Document the chargeback flow", domain.CategoryDone, domain.PriorityLow, &d.Member.ID, nil},
		{"Prototype crypto payouts", domain.CategoryCanceled, domain.PriorityNone, nil, nil},
	}
	positions := map[string]float64{}
	for _, sp := range specs {
		col := d.ColumnByCategory[sp.column]
		seq, err := tx.Projects().AllocateTicketSeq(ctx, d.Project.ID)
		if err != nil {
			return nil, err
		}
		pos := domain.PositionBetween(positions[col.ID], 0)
		positions[col.ID] = pos
		tk := domain.Ticket{
			ID: domain.NewID(), ProjectID: d.Project.ID, Seq: seq,
			Key:   fmt.Sprintf("%s-%d", d.Project.Key, seq),
			Title: sp.title, ColumnID: col.ID, Position: pos, Priority: sp.priority,
			AssigneeID: sp.assignee, DelegateAgentID: sp.delegate,
			Origin: domain.OriginHuman, CreatedByUserID: &d.Owner.ID,
			CreatedAt: at(),
		}
		tk.UpdatedAt = tk.CreatedAt
		if err := tx.Tickets().Create(ctx, &tk); err != nil {
			return nil, err
		}
		d.Tickets = append(d.Tickets, tk)
	}
	webhookTicket := d.Tickets[5] // the urgent one, in the running-category column
	doneTicket := d.Tickets[8]    // completed work with a run behind it

	// ---- events, runs, activities -----------------------------------------------------
	mkEvent := func(kind, activity, dedupe string, subjectNumber int64) (domain.Event, error) {
		num := subjectNumber
		login := "theo-brandt"
		e := domain.Event{
			ID: domain.NewID(), ProjectID: &d.Project.ID, Source: "github.poll",
			Kind: kind, ActivityType: activity,
			ActorKind: domain.ActorHuman, ActorLogin: &login,
			SubjectKind: "pr", SubjectNumber: &num,
			Payload:       json.RawMessage(`{"pr":{"number":` + fmt.Sprint(num) + `}}`),
			DedupeKey:     dedupe,
			DispatchState: domain.DispatchDone,
			OccurredAt:    at(),
		}
		e.CreatedAt = e.OccurredAt
		return e, tx.Events().Insert(ctx, &e)
	}
	for _, ev := range []struct {
		kind, activity, dedupe string
		num                    int64
	}{
		{"pull_request", "opened", "github.poll:pr:212:opened", 212},
		{"pull_request", "synchronize", "github.poll:pr:212:synchronize:9f31c2", 212},
		{"pull_request_review", "submitted", "github.poll:pr:212:review:88213", 212},
	} {
		e, err := mkEvent(ev.kind, ev.activity, ev.dedupe, ev.num)
		if err != nil {
			return nil, err
		}
		d.Events = append(d.Events, e)
	}

	runs := []domain.Run{
		{
			AgentID: implementer.ID, TicketID: &doneTicket.ID,
			State: domain.RunCompleted, Autonomy: implementer.Autonomy,
			Model: implementer.Model, Effort: implementer.Effort,
			Prompt:     "Migrate invoices to the new tax table. Keep the ledger untouched.",
			SubjectKey: "ticket:" + doneTicket.Key, StepCount: 42, CostCents: 118,
			TokensIn: 48211, TokensOut: 9160,
		},
		{
			AgentID: implementer.ID, TicketID: &webhookTicket.ID,
			State: domain.RunRunning, Autonomy: implementer.Autonomy,
			Model: implementer.Model, Effort: implementer.Effort,
			Prompt:     "Fix the double-charge on retried webhooks; add a regression test.",
			SubjectKey: "ticket:" + webhookTicket.Key, CurrentStep: "Running the webhook test suite",
			StepCount: 17, CostCents: 41, TokensIn: 20734, TokensOut: 3512,
		},
		{
			AgentID: reviewer.ID, CauseEventID: &d.Events[2].ID,
			State: domain.RunFailed, StateReason: "container exited 137",
			Autonomy: reviewer.Autonomy, Model: reviewer.Model, Effort: reviewer.Effort,
			Prompt:     "Review PR #212 for currency rounding regressions.",
			SubjectKey: "pr:212", StepCount: 6, CostCents: 12, TokensIn: 8090, TokensOut: 640,
			ErrorMessage: "runtime container was OOM-killed",
		},
	}
	for i := range runs {
		r := &runs[i]
		r.ID = domain.NewID()
		r.ProjectID = d.Project.ID
		r.RequestedByUserID = &d.Owner.ID
		r.RuntimeID = "claude-code"
		r.SandboxID = "docker"
		r.QueuedAt = at()
		started := at()
		r.StartedAt = &started
		if r.State.Terminal() {
			ended := at()
			r.EndedAt = &ended
		}
		seq, err := tx.Runs().NextSeq(ctx, d.Project.ID)
		if err != nil {
			return nil, err
		}
		r.Seq = seq
		if err := tx.Runs().Create(ctx, r); err != nil {
			return nil, err
		}
	}
	d.Runs = runs

	activities := []domain.Activity{
		{Seq: 1, Type: domain.ActivitySystem, Level: 0, Title: "Run queued"},
		{Seq: 2, Type: domain.ActivityProvision, Title: "Provisioned container lexicode-run"},
		{Seq: 3, Type: domain.ActivityThought, Title: "Reading the webhook retry handler"},
		{
			Seq: 4, Type: domain.ActivityAction, ToolName: "Bash", GroupKey: "bash:test",
			Title:   "go test ./internal/webhooks/...",
			Payload: json.RawMessage(`{"argv":["go","test","./internal/webhooks/..."],"exit":1}`),
			OK:      boolFalse(), Attempt: 1,
		},
		{Seq: 5, Type: domain.ActivityResponse, Level: 0, Title: "Reproduced: retries skip the idempotency check"},
	}
	for i := range activities {
		a := &activities[i]
		a.RunID = runs[1].ID
		if a.Attempt == 0 {
			a.Attempt = 1
		}
		a.CreatedAt = at()
		if err := tx.Activities().Append(ctx, a); err != nil {
			return nil, err
		}
	}

	// The unified stream: a comment and the run card on the webhook ticket.
	stream := []domain.StreamEntry{
		{
			Kind: domain.StreamComment, ActorKind: domain.ActorHuman, ActorID: &d.Owner.ID,
			Body: "Support saw three double-charges this morning — bumping to urgent.",
		},
		{
			Kind: domain.StreamRun, ActorKind: domain.ActorAgent, ActorID: &implementer.ID,
			RunID: &runs[1].ID,
		},
	}
	for i := range stream {
		e := &stream[i]
		e.ID = domain.NewID()
		e.TicketID = webhookTicket.ID
		e.CreatedAt = at()
		if err := tx.TicketStream().Append(ctx, e); err != nil {
			return nil, err
		}
	}

	// An audit trace of the seed itself, so the audit surfaces are never empty either.
	if err := tx.Audit().Append(ctx, &domain.AuditEntry{
		ID: domain.NewID(), ProjectID: &d.Project.ID,
		ActorKind: domain.ActorSystem, Action: "workspace.seed",
		TargetKind: "project", TargetID: d.Project.ID,
		Note:      "demo fixture set loaded",
		CreatedAt: at(),
	}); err != nil {
		return nil, err
	}

	return d, nil
}

func boolFalse() *bool {
	b := false
	return &b
}
