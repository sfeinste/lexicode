package ports

import (
	"context"
	"encoding/json"

	"github.com/spruce/lexicode/internal/domain"
)

// EventSource turns something that happens in the world into a normalized domain event: the
// GitHub poller, the cron source, and later webhooks or a filesystem watcher (architecture §7).
//
// Method set: contracts §2.1, transcribed verbatim in story S25.
type EventSource interface {
	// ID is the stable identifier a trigger stores, e.g. "github.poll". It must be unique across
	// registered event sources.
	ID() string
	// Catalog describes what the trigger editor may offer for this source: event kinds,
	// activity types, filter fields and payload fields. The editor is generated from it — a
	// new event source contributes new WHEN options and new IF fields with no editor changes.
	Catalog() EventCatalog
	// Start begins producing events through emit. It must return promptly: long-running work
	// happens on the source's own goroutines under ctx.
	Start(ctx context.Context, emit Emit) error
	// Stop drains the source's background work.
	Stop(ctx context.Context) error
}

// Emit delivers one normalized event onto the kernel bus (contracts §2.1). The implementation
// behind it is idempotent on Event.DedupeKey, which is what makes polling safe to replay.
type Emit func(context.Context, domain.Event) error

// EventCatalog is everything the trigger editor may offer for one source (contracts §2.1).
type EventCatalog struct {
	Events []EventDescriptor `json:"events"`
}

// EventDescriptor describes one event kind: its activity types, the filters the matcher
// supports for it, the payload fields conditions and {{...}} interpolation may address, and
// the SubjectKey template the loop-protection guard keys on.
type EventDescriptor struct {
	Kind          string         `json:"kind"`  // "pull_request"
	Label         string         `json:"label"` // "Pull request"
	ActivityTypes []ActivityType `json:"activity_types"`
	Filters       []FilterField  `json:"filters"`
	Fields        []PayloadField `json:"fields"`
	SubjectKey    string         `json:"subject_key"` // template: "pr:{{pr.number}}"
}

// ActivityType is one WHEN option: {Value:"synchronize", Label:"pushed to", Help:"…"}.
type ActivityType struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
}

// FilterField is one matcher filter: {Key:"branches", Kind:"glob-list", Label:"Branches"}.
type FilterField struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// PayloadField is one addressable payload path with the operator family it accepts:
// {Path:"pr.author", Type:"text"} — drives the IF row dropdowns.
type PayloadField struct {
	Path string `json:"path"`
	Type string `json:"type"` // "text" | "number" | "bool" | "enum" | "set"
}

// ForgeProvider lives in forge.go (contracts §2.2, transcribed in story S14).

// Sandbox lives in sandbox.go (contracts §2.3, transcribed in story S17).

// AgentRuntime lives in runtime.go (contracts §2.4, transcribed in story S20).

// TriggerAction is one THEN verb of the trigger engine: run_agent, create_ticket, move_ticket,
// post_comment, notify (architecture §8).
//
// Method set: contracts §2.5, transcribed in story S26; the five V1 implementations arrive in
// story S28. `run_agent` must call kernel.Scheduler().Enqueue(...) and must never touch a
// Sandbox or Runtime directly (D-14).
type TriggerAction interface {
	// ID is the stable identifier a trigger stores, e.g. "run_agent". It must be unique across
	// registered actions.
	ID() string
	// Label is the human name the THEN dropdown shows, e.g. "Run an agent".
	Label() string
	// Schema drives the THEN form; fields are typed and validated.
	Schema() ParamSchema
	// Describe renders the configured action in words — "run agent Reviewer" — for rule cards
	// and backtest. An error means the params are invalid for this action; the trigger CRUD
	// surfaces it as a save-time validation failure.
	Describe(params json.RawMessage) (string, error)
	// Execute performs the action. The returned ActionResult's Outcome becomes (part of) the
	// firing's outcome; an error is the `errored` outcome with the error's words as reason.
	Execute(ctx context.Context, ac ActionContext, params json.RawMessage) (ActionResult, error)
}

// ActionContext is what an executing action gets to work with (contracts §2.5).
type ActionContext struct {
	Event   domain.Event
	Trigger domain.Trigger
	Project domain.Project

	// Interp resolves a `{{...}}` template against the event's normalized payload —
	// interpolation only, never control flow (brief §6.6). It returns the rendered string plus
	// any warnings ("unknown path …"); the engine collects those onto the firing row.
	Interp func(string) (string, []string)

	// Kernel is the *kernel.Kernel. Contracts §2.5 types this field `*kernel.Kernel`, but the
	// kernel package imports ports (architecture §2.1's dependency rule), so the concrete type
	// cannot be named here without an import cycle. Actions assert it back:
	// `k := ac.Kernel.(*kernel.Kernel)` — asserting to the kernel is not a violation of the
	// "no type assertions to concrete module types" rule, which is about modules.
	Kernel any

	// Guard is the loop guard's stage-3 → stage-4 pass-through (S27, architecture §9). A
	// run_agent action copies it verbatim into the scheduler's RunRequest — that is the whole
	// contract: the guard decided, the action carries the decision, the scheduler applies it.
	Guard GuardPass
}

// GuardPass is what the loop guard (stage 3) hands the actions (stage 4) when a firing
// proceeds. It lives in ports rather than kernel/guard so module actions can carry it without
// importing kernel internals.
type GuardPass struct {
	// SubjectKey is the derived loop-protection subject ("pr:219" / "ticket:PAY-14" /
	// "repo"), stored on the run at enqueue so debounce and cancel-in-progress can key on
	// (trigger, subject) with one indexed query.
	SubjectKey string
	// Depth is the run's position in the causal chain (0 for a chain root), stored on the
	// run at enqueue; the guard's depth counter recomputes it per event.
	Depth int64
	// SupersededRunID names the still-active run for the same (trigger, subject) that
	// cancel-in-progress elects to stop. The scheduler cancels it after the new run exists —
	// after, because the reason names the new run's seq ("superseded by run #N").
	SupersededRunID string
}

// ActionResult is one action's outcome (contracts §2.5).
type ActionResult struct {
	// Outcome is succeeded, awaiting_approval, errored or no_action — the stage-4 subset of
	// the eight firing outcome classes.
	Outcome domain.FiringOutcome
	// RunID is the run the action enqueued, when it enqueued one.
	RunID string
	// Note is a human sentence for the firing's reason column ("enqueued run for Reviewer").
	Note string
}

// ParamSchema describes a TriggerAction's parameters. Contracts §2.5 names this type without
// pinning its shape; this is the minimal typed form the THEN editor renders from. S28's
// actions fill it in.
type ParamSchema struct {
	Fields []ParamField `json:"fields"`
}

// ParamField is one typed action parameter.
type ParamField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // "text" | "number" | "bool" | "enum" | "agent" | "template"
	Required bool     `json:"required"`
	Enum     []string `json:"enum,omitempty"`
	Help     string   `json:"help,omitempty"`
}

// ContextProvider contributes context to an agent run — project facts, wiki pages, repository
// files, the ticket — and is the reason the Context panel can explain every run.
//
// Method set: contracts §2.6, transcribed verbatim. S22 ships the `project` and `ticket`
// providers (prompt assembly needs them); `wiki` and `repofiles` arrive with S34.
type ContextProvider interface {
	// ID is the stable identifier, e.g. "wiki". It must be unique across registered providers.
	ID() string
	// Priority orders providers ascending; it determines prompt order (architecture §11).
	Priority() int
	// Resolve returns this provider's items for one run (or, when req.Dry, for the agent
	// detail preview). An empty slice is a normal answer.
	Resolve(ctx context.Context, req ContextRequest) ([]ContextItem, error)
}

// ContextRequest is what a provider gets to decide relevance (contracts §2.6).
type ContextRequest struct {
	ProjectID, AgentID string
	TicketID           string   // may be empty
	TaskSummary        string   // ticket title + trigger description, for `auto` retrieval
	ChangedPaths       []string // known for PR-triggered runs; empty otherwise
	Dry                bool     // true for the agent's "what every run sees" preview
}

// ContextItem is one contribution: a labelled, token-counted piece of agent context. Reason is
// rendered verbatim in the run detail's Context panel.
type ContextItem struct {
	SourceKind string // "wiki" | "repo_file" | "project" | "ticket"
	SourceRef  string
	Title      string
	Reason     string
	Body       string // empty when Injected == false
	Tokens     int
	Injected   bool // repo files are listed, not injected (D-11)
}

// Notifier delivers a notification through one channel. V1 ships in-app only; Slack, email and
// push are the extension this port buys.
//
// Method set: contracts §2.7, transcribed in story S36.
type Notifier interface {
	// ID is the stable identifier, e.g. "inapp". It must be unique across registered notifiers.
	ID() string
}

// CredentialSource supplies the environment an agent run needs to authenticate — an OAuth token,
// an API key from the secret store, or the operator's own environment.
//
// Method set: contracts §2.7, transcribed in story S19. V1 ships two implementations in
// internal/module/credentials: "oauth-token" (D-5: the pasted `claude setup-token` output,
// stored in the encrypted secret store) and "env" (the orchestrator's own environment, as a
// fallback for development setups).
type CredentialSource interface {
	// ID is the stable identifier, e.g. "oauth-token". It must be unique across registered
	// credential sources.
	ID() string
	// AgentEnv returns the environment variables a run's container needs to authenticate
	// against the model provider, e.g. {"CLAUDE_CODE_OAUTH_TOKEN": "..."}. The values are
	// secrets: callers merge them into SandboxSpec.Env and register them with their
	// redactor; they must never be logged or serialized anywhere else.
	AgentEnv(ctx context.Context, projectID string) (map[string]string, error)
	// Health reports whether this source can currently supply credentials. The error is
	// surfaced verbatim in the settings UI, so it must name the fix (e.g. "run `claude
	// setup-token` and paste the result into Settings"), never contain a credential value.
	Health(ctx context.Context) error
}
