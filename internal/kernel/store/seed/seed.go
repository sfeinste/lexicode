// Package seed produces a realistic fixture set: a workspace with two users, one project with a
// column per category, a board of tickets, two agents, a little history of events, runs and
// activities, acceptance criteria, run outputs, wiki pages (one live, one agent proposal), a
// trigger and an inbox notification. Tests assert cross-table invariants on it, and
// `lexicode serve --demo` loads it into an empty database so a new user can explore a
// populated workspace before connecting anything (stories S03 and S39).
//
// The demo users have a REAL password (DemoPassword) — S39: a demo nobody can sign in to is
// not a demo. It is printed by `serve --demo` and is obviously a fixture credential; it is
// only ever written into a database the flag has just proven empty.
//
// Everything is inserted in one transaction: a half-seeded database never exists.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
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
	WikiPages        []domain.WikiPage
	Triggers         []domain.Trigger
}

// DemoPassword is the password both fixture users are seeded with, so `serve --demo` produces
// a workspace someone can actually sign in to and click around (S39). It is printed on the
// console at seed time; it is not a secret and is not meant to be one.
const DemoPassword = "demo-password"

// DemoOwnerEmail and DemoMemberEmail are the two fixture logins, exported for the same reason.
const (
	DemoOwnerEmail  = "ada@example.com"
	DemoMemberEmail = "theo@example.com"
)

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

	demoPasswordHash, err := auth.HashPassword(DemoPassword)
	if err != nil {
		return nil, err
	}

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
		ID: domain.NewID(), Email: DemoOwnerEmail, DisplayName: "Ada Muir",
		PasswordHash: demoPasswordHash, Role: domain.RoleOwner, AvatarColor: "#7c5cff",
		CreatedAt: at(),
	}
	d.Member = domain.User{
		ID: domain.NewID(), Email: DemoMemberEmail, DisplayName: "Theo Brandt",
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
			// Parked on a question, not running: boot recovery leaves a parked run alone
			// (§10.6 case 4) but fails a `running` one whose container is gone — and a demo
			// whose flagship run says "orchestrator restarted" is a bad demo. Parked is also
			// the more interesting state: it is what the needs-you lane and the inbox are for.
			AgentID: implementer.ID, TicketID: &webhookTicket.ID,
			State: domain.RunNeedsInput, Autonomy: implementer.Autonomy,
			Model: implementer.Model, Effort: implementer.Effort,
			Prompt:     "Fix the double-charge on retried webhooks; add a regression test.",
			SubjectKey: "ticket:" + webhookTicket.Key, CurrentStep: "Waiting on the storage decision",
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

	// The other two runs get a transcript too — an empty run detail is the emptiest screen
	// in the product, and a demo should never show one.
	otherActivities := []struct {
		run  int
		rows []domain.Activity
	}{
		{0, []domain.Activity{
			{Seq: 1, Type: domain.ActivitySystem, Level: 0, Title: "Run queued"},
			{Seq: 2, Type: domain.ActivityProvision, Title: "Cloned acme/payments at main"},
			{Seq: 3, Type: domain.ActivityThought, Title: "Reading migrations/ for the tax table shape"},
			{
				Seq: 4, Type: domain.ActivityAction, ToolName: "Bash", GroupKey: "bash:migrate",
				Title:   "go run ./cmd/migrate up",
				Payload: json.RawMessage(`{"argv":["go","run","./cmd/migrate","up"],"exit":0}`),
				OK:      boolTrue(),
			},
			{Seq: 5, Type: domain.ActivityResponse, Level: 0,
				Title: "Migrated invoices to invoice_tax_rates; opened PR #212"},
		}},
		{2, []domain.Activity{
			{Seq: 1, Type: domain.ActivitySystem, Level: 0, Title: "Run queued by trigger"},
			{Seq: 2, Type: domain.ActivityProvision, Title: "Cloned acme/payments at PR #212 head"},
			{Seq: 3, Type: domain.ActivityThought, Title: "Diffing the rounding helpers"},
			{Seq: 4, Type: domain.ActivitySystem, Level: 0, OK: boolFalse(),
				Title: "Container exited 137 (out of memory)"},
		}},
	}
	for _, group := range otherActivities {
		for i := range group.rows {
			a := &group.rows[i]
			a.RunID = runs[group.run].ID
			if a.Attempt == 0 {
				a.Attempt = 1
			}
			a.CreatedAt = at()
			if err := tx.Activities().Append(ctx, a); err != nil {
				return nil, err
			}
		}
	}

	// The question the parked run is waiting on: a level-0 elicitation activity and the
	// durable elicitations row keyed to it, exactly as the MCP server writes them.
	question := json.RawMessage(`{"questions":[{"question":"Where should the idempotency keys live?",` +
		`"header":"Storage","options":[` +
		`{"label":"Postgres","description":"Same database as the charges table; one fewer moving part"},` +
		`{"label":"Redis","description":"Faster, but another service to operate"}],` +
		`"multiSelect":false}]}`)
	askActivity := domain.Activity{
		RunID: runs[1].ID, Seq: 6, Type: domain.ActivityElicitation, Level: 0,
		ToolName: "mcp__lexicode__ask_human", GroupKey: "mcp__lexicode__ask_human",
		Title:   "Question: Where should the idempotency keys live?",
		Payload: question, Attempt: 1, CreatedAt: at(),
	}
	if err := tx.Activities().Append(ctx, &askActivity); err != nil {
		return nil, err
	}
	if err := tx.Elicitations().Create(ctx, &domain.Elicitation{
		ID: domain.NewID(), RunID: runs[1].ID, ActivitySeq: askActivity.Seq,
		Kind: domain.ElicitationQuestion, Request: question,
		State: domain.ElicitationPending, CreatedAt: askActivity.CreatedAt,
	}); err != nil {
		return nil, err
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

	// ---- acceptance criteria ------------------------------------------------------------
	// The urgent ticket carries criteria, one of them already checked off by its run — the
	// run-summary surface and the ticket's criteria list both have something to show.
	readyTicket := d.Tickets[3] // "Add idempotency keys to POST /charges", ready category
	criteria := []struct {
		ticketID string
		text     string
		checked  bool
		byRun    *string
		note     string
	}{
		{webhookTicket.ID, "A replayed webhook does not create a second charge", true, &runs[1].ID,
			"covered by webhooks/retry_test.go:88"},
		{webhookTicket.ID, "The regression test fails without the fix", false, nil, ""},
		{readyTicket.ID, "POST /charges accepts an Idempotency-Key header", false, nil, ""},
		{readyTicket.ID, "Keys expire after 24 hours", false, nil, ""},
	}
	for i, c := range criteria {
		row := domain.Criterion{
			ID: domain.NewID(), TicketID: c.ticketID, Position: int64(i + 1),
			Text: c.text, Checked: c.checked, CheckedByRunID: c.byRun, Note: c.note,
			UpdatedAt: at(),
		}
		if err := tx.Criteria().Create(ctx, &row); err != nil {
			return nil, err
		}
	}

	// ---- run outputs --------------------------------------------------------------------
	// The completed run left a branch and a pull request; the running one has pushed its
	// branch so far. Outputs are what the run detail's "what came out of this" list reads.
	outputs := []domain.RunOutput{
		{RunID: runs[0].ID, Kind: domain.OutputBranch, Ref: "sonnet/PAY-9-migrate-invoices",
			Summary: "pushed sonnet/PAY-9-migrate-invoices"},
		{RunID: runs[0].ID, Kind: domain.OutputPullRequest, Ref: "212",
			URL:     "https://github.com/acme/payments/pull/212",
			Summary: "opened PR #212: Migrate invoices to the new tax table"},
		{RunID: runs[1].ID, Kind: domain.OutputBranch, Ref: "sonnet/PAY-6-fix-double-charge",
			Summary: "pushed sonnet/PAY-6-fix-double-charge"},
	}
	for i := range outputs {
		o := &outputs[i]
		o.ID = domain.NewID()
		o.CreatedAt = at()
		if err := tx.RunOutputs().Append(ctx, o); err != nil {
			return nil, err
		}
	}

	// ---- wiki ---------------------------------------------------------------------------
	// Two live pages (one of them an `always` page with an owner and an expiry, so the
	// context budget meter and the verified_until machinery have real data), plus one agent
	// proposal waiting in the review queue.
	verified := domain.Day(base.AddDate(0, 3, 0))
	pages := []domain.WikiPage{
		{
			Slug: "engineering", Title: "Engineering", AgentScope: domain.ScopeManual,
			Body:  "Handbook root. Child pages carry the rules agents actually read.",
			State: domain.WikiLive, OwnerID: &d.Owner.ID,
		},
		{
			Slug: "database-migrations", Title: "Database migrations",
			AgentScope: domain.ScopeAlways, OwnerID: &d.Owner.ID, VerifiedUntil: &verified,
			Tags: []string{"database", "conventions"},
			Body: "# Database migrations\n\n" +
				"Every schema change ships as a numbered migration in `migrations/`.\n\n" +
				"- Never edit an applied migration; add a new one.\n" +
				"- The ledger tables are append-only. A migration that rewrites them needs " +
				"a human sign-off in the ticket before the run starts.\n" +
				"- Roll-forward only: there are no down migrations.\n",
			State: domain.WikiLive,
		},
		{
			Slug: "webhook-retries-proposal", Title: "Webhook retry semantics",
			AgentScope: domain.ScopeAuto,
			Body: "# Webhook retry semantics\n\n" +
				"Stripe retries a failed webhook up to 8 times with exponential backoff. " +
				"Handlers must therefore be idempotent on `event.id`, not on the payload " +
				"hash — the payload is re-serialized on each retry.\n",
			State: domain.WikiProposed, ProposedByRunID: &runs[1].ID,
		},
	}
	proposedReason := "You corrected me twice about retry semantics while I was fixing the double-charge."
	pages[2].ProposedReason = &proposedReason
	for i := range pages {
		pg := &pages[i]
		pg.ID = domain.NewID()
		pg.ProjectID = d.Project.ID
		pg.Position = float64(i + 1)
		pg.TokenEstimate = int64(len(pg.Body) / 4)
		pg.CreatedAt = at()
		pg.UpdatedAt = pg.CreatedAt
		if i == 1 {
			pg.ParentID = &pages[0].ID // the migrations page hangs off the handbook root
		}
		if err := tx.Wiki().CreatePage(ctx, pg); err != nil {
			return nil, err
		}
		version := domain.WikiVersion{
			ID: domain.NewID(), PageID: pg.ID, Version: 1,
			Title: pg.Title, Body: pg.Body, FrontMatter: map[string]any{},
			CreatedAt: pg.CreatedAt,
		}
		if pg.State == domain.WikiProposed {
			version.AuthorRunID = pg.ProposedByRunID
		} else {
			version.AuthorUserID = &d.Owner.ID
		}
		if err := tx.Wiki().CreateVersion(ctx, &version); err != nil {
			return nil, err
		}
	}
	d.WikiPages = pages

	// ---- a trigger ------------------------------------------------------------------------
	// Step 3 of the brief's canonical chain, seeded enabled so the trigger surfaces show a
	// real rule: a pull request opened BY AN AGENT spawns the reviewer.
	trigger := domain.Trigger{
		ID: domain.NewID(), ProjectID: d.Project.ID,
		Name: "PR opened by an agent → review it", Enabled: true,
		SourceID: "github.poll", Event: "pull_request",
		ActivityTypes: json.RawMessage(`["opened"]`),
		Filters:       json.RawMessage(`{}`),
		Conditions:    json.RawMessage(`{"all":[{"op":"actor.is_agent"}]}`),
		Actions: json.RawMessage(`[{"action_id":"run_agent","params":{"agent_id":"` + reviewer.ID + `",` +
			`"prompt_override":"Review pull request #{{pr.number}} on branch {{pr.branch}}. ` +
			`Post a review with severity-tagged findings."}}]`),
		LoopConfig: domain.DefaultLoopConfig(),
		CreatedBy:  &d.Owner.ID,
		CreatedAt:  at(),
	}
	trigger.UpdatedAt = trigger.CreatedAt
	if err := tx.Triggers().Create(ctx, &trigger); err != nil {
		return nil, err
	}
	d.Triggers = []domain.Trigger{trigger}

	// The rule fired once, on the review event, and started the run that then failed — so
	// the rule-health sparkline and the run's causal chain are both non-empty.
	if _, err := tx.Firings().Create(ctx, &domain.TriggerFiring{
		ID: domain.NewID(), TriggerID: trigger.ID, EventID: d.Events[0].ID,
		Outcome: domain.FiringSucceeded, Reason: "ran Sentry",
		RunID: &runs[2].ID, CreatedAt: at(),
	}); err != nil {
		return nil, err
	}

	// ---- the inbox ------------------------------------------------------------------------
	// The failed reviewer run is somebody's problem: one unread notification, flavor in
	// words, so the inbox and the needs-you lane are not empty on a fresh demo.
	notifications := []domain.Notification{
		{
			UserID: d.Owner.ID, RunID: &runs[1].ID, Flavor: domain.FlavorQuestion,
			Title: "Sonnet asked a question",
			Body:  "Where should the idempotency keys live?",
		},
		{
			UserID: d.Owner.ID, RunID: &runs[2].ID, Flavor: domain.FlavorFailure,
			Title: "Sentry failed reviewing PR #212",
			Body:  "The runtime container was OOM-killed after 6 steps.",
		},
	}
	for i := range notifications {
		n := &notifications[i]
		n.ID = domain.NewID()
		n.ProjectID = d.Project.ID
		n.State = domain.NotificationUnread
		n.CreatedAt = at()
		n.UpdatedAt = n.CreatedAt
		if err := tx.Notifications().Upsert(ctx, n); err != nil {
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

func boolTrue() *bool {
	b := true
	return &b
}
