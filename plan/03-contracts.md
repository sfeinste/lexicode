# Contracts

These are frozen interfaces (see [README](README.md) rule 2). Package: `internal/kernel/ports`.

---

## 1. Module and kernel

```go
package kernel

type Module interface {
    Name() string
    Init(k *Kernel) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}

type Kernel struct { /* unexported */ }

func (k *Kernel) Store() *store.Store
func (k *Kernel) Bus() *bus.Bus
func (k *Kernel) Scheduler() *sched.Scheduler
func (k *Kernel) Secrets() *secrets.Store
func (k *Kernel) Audit() *audit.Writer
func (k *Kernel) Mux() *httpx.Mux
func (k *Kernel) SSE() *httpx.Hub
func (k *Kernel) Logger() *slog.Logger

// Port registration. Called from Module.Init. Duplicate IDs abort boot.
func (k *Kernel) RegisterEventSource(ports.EventSource) error
func (k *Kernel) RegisterForge(ports.ForgeProvider) error
func (k *Kernel) RegisterSandbox(ports.Sandbox) error
func (k *Kernel) RegisterRuntime(ports.AgentRuntime) error
func (k *Kernel) RegisterAction(ports.TriggerAction) error
func (k *Kernel) RegisterContextProvider(ports.ContextProvider) error
func (k *Kernel) RegisterNotifier(ports.Notifier) error

// Lookup. Returns a typed not-found error naming the missing ID.
func (k *Kernel) Forge(id string) (ports.ForgeProvider, error)
func (k *Kernel) Runtime(id string) (ports.AgentRuntime, error)
// ...one per port; ContextProviders() and Actions() return the full set.
```

---

## 2. Ports

### 2.1 EventSource

```go
type EventSource interface {
    ID() string                       // "github.poll"
    Catalog() EventCatalog            // what the trigger editor may offer for this source
    Start(ctx context.Context, emit Emit) error
    Stop(ctx context.Context) error
}

type Emit func(context.Context, domain.Event) error

type EventCatalog struct {
    Events []EventDescriptor
}

type EventDescriptor struct {
    Kind          string          // "pull_request"
    Label         string          // "Pull request"
    ActivityTypes []ActivityType  // {Value:"synchronize", Label:"pushed to", Help:"…where the loop lives"}
    Filters       []FilterField   // {Key:"branches", Kind:"glob-list", Label:"Branches"}
    Fields        []PayloadField  // {Path:"pr.author", Type:"text"} — drives the IF row dropdowns
    SubjectKey    string          // template: "pr:{{pr.number}}" — the guard key
}
```

`Catalog()` is what makes the trigger editor generic. A new event source contributes new WHEN
options and new IF fields with no editor changes.

### 2.2 ForgeProvider

```go
type ForgeProvider interface {
    ID() string
    Verify(ctx context.Context, c Creds, r RepoRef) (RepoInfo, error)

    // Read — used by the poller and by UI surfaces.
    ListPullRequests(ctx, c, r, since time.Time) ([]PullRequest, error)
    ListReviews(ctx, c, r, prNumber int) ([]Review, error)
    ListReviewComments(ctx, c, r, since time.Time) ([]Comment, error)
    ListIssueComments(ctx, c, r, since time.Time) ([]Comment, error)
    ListCheckSuites(ctx, c, r, sha string) ([]CheckSuite, error)
    ListOpenIssues(ctx, c, r) ([]Issue, error)          // bootstrap import
    ReadFile(ctx, c, r, ref, path string) ([]byte, error) // bootstrap doc detection

    // Write — every method takes the acting agent so the marker (D-9) is never forgotten.
    OpenPullRequest(ctx, c, r, a Actor, spec PRSpec) (PullRequest, error)
    CommentOnPullRequest(ctx, c, r, a Actor, n int, body string) (Comment, error)
    SubmitReview(ctx, c, r, a Actor, n int, rev ReviewSpec) (Review, error)

    CloneURL(ctx, c, r RepoRef) (string, error)          // token embedded for the container
}
```

There is **no `Merge`, no `ForcePush`, no `Approve`-as-final-say method.** Brief D6 is implemented
as an absent capability. `ReviewSpec.Event` accepts `COMMENT` and `REQUEST_CHANGES` only; `APPROVE`
is rejected at the adapter with a named error.

Every write method:
1. Checks the agent's `permissions` and returns `ErrPermissionDenied` with the missing grant named.
2. Appends the actor marker `<!-- lexicode:actor=agent:<id> run=<id> -->` to the body.
3. Writes an audit row and a `run_outputs` row.

### 2.3 Sandbox

```go
type Sandbox interface {
    ID() string                          // "docker"
    Available(ctx context.Context) error // preflight; surfaced as module degraded state
    Prepare(ctx context.Context, spec SandboxSpec, sink ProvisionSink) (Instance, error)
    Reattach(ctx context.Context, ref InstanceRef) (Instance, error)   // crash recovery (§10.6)
}

type SandboxSpec struct {
    RunID, ProjectID string
    Image            string          // "" = built-in, built on demand
    Clone            CloneSpec       // url, ref, branch to create, git identity
    SetupScript      string
    Env              map[string]string   // secrets, OAuth token, git identity
    Files            map[string][]byte   // .claude/settings.json, .mcp.json, prompt file
    Network          NetworkPolicy       // {Mode: none|allowlist|open, Allow: []string}
    Labels           map[string]string
    Limits           ResourceLimits      // cpu, memory, pids, wall clock
}

type ProvisionSink interface {
    Step(name string, state StepState, detail string)  // pending|running|ok|failed
    Log(line string)
}

type Instance interface {
    Ref() InstanceRef
    Exec(ctx context.Context, argv []string, opts ExecOpts) (Streams, error)
    ReadFile(ctx context.Context, path string) ([]byte, error)
    Destroy(ctx context.Context) error
}

type Streams struct {
    Stdin  io.WriteCloser
    Stdout io.Reader
    Stderr io.Reader
    Wait   func() (exitCode int, err error)
}
```

### 2.4 AgentRuntime

```go
type AgentRuntime interface {
    ID() string                    // "claude-code"
    Caps() Caps                    // {Steering, Elicitation, Approvals, CostReporting bool}
    Launch(ctx context.Context, spec RunSpec, inst Sandbox.Instance, sink RunSink) (Handle, error)
}

type RunSpec struct {
    RunID        string
    Prompt       string
    Model        string
    Effort       string
    Autonomy     domain.Autonomy
    Permissions  domain.Permissions
    MCPEndpoint  string          // host MCP URL with the run token (D-12)
    MaxSteps     int
    ResumeFrom   int64           // byte offset for reattach; 0 for a fresh launch
}

type Handle interface {
    Steer(ctx context.Context, msg string) error
    Respond(ctx context.Context, elicitationID string, r Response) error
    Stop(ctx context.Context, reason string) error
    Wait(ctx context.Context) (Result, error)
}

// RunSink is the runtime→kernel direction. All methods are non-blocking and ordered.
type RunSink interface {
    Activity(domain.Activity)         // appended, streamed, cost rolled up
    CurrentStep(string)               // the mutable one-liner
    Usage(domain.UsageDelta)
    Elicit(domain.Elicitation) error  // parks the run; returns when persisted
    Output(domain.RunOutput)
    Offset(int64)                     // persisted for reattach
}
```

### 2.5 TriggerAction

```go
type TriggerAction interface {
    ID() string                       // "run_agent"
    Label() string                    // "Run an agent"
    Schema() ParamSchema              // drives the THEN form; fields are typed and validated
    Describe(params json.RawMessage) (string, error)  // "run agent Reviewer" — for cards + backtest
    Execute(ctx context.Context, ac ActionContext, params json.RawMessage) (ActionResult, error)
}

type ActionContext struct {
    Event     domain.Event
    Trigger   domain.Trigger
    Project   domain.Project
    Interp    func(string) (string, []string)   // {{...}} interpolation → value + warnings
    Kernel    *kernel.Kernel
}

type ActionResult struct {
    Outcome domain.FiringOutcome   // succeeded | awaiting_approval | errored | no_action
    RunID   string
    Note    string
}
```

`run_agent` **must** call `kernel.Scheduler().Enqueue(...)` and must never touch a Sandbox or
Runtime directly (D-14).

### 2.6 ContextProvider

```go
type ContextProvider interface {
    ID() string
    Priority() int                  // ascending; determines prompt order
    Resolve(ctx context.Context, req ContextRequest) ([]ContextItem, error)
}

type ContextRequest struct {
    ProjectID, AgentID string
    TicketID           string   // may be empty
    CauseEventID       string   // the event that spawned the run; empty for a manual run
    TaskSummary        string   // ticket title + trigger description, for `auto` retrieval
    ChangedPaths       []string // known for PR-triggered runs; empty otherwise
    Dry                bool     // true for the agent's "what every run sees" preview
}

type ContextItem struct {
    SourceKind string   // "wiki" | "repo_file" | "project" | "ticket" | "event"
    SourceRef  string
    Title      string
    Reason     string   // rendered verbatim in the Context panel
    Body       string   // empty when Injected == false
    Tokens     int
    Injected   bool     // repo files are listed, not injected (D-11)
}
```

### 2.7 Notifier / CredentialSource

```go
type Notifier interface {
    ID() string
    Deliver(ctx context.Context, n domain.Notification) error
}

type CredentialSource interface {
    ID() string                                            // "oauth-token"
    AgentEnv(ctx context.Context, projectID string) (map[string]string, error)
    Health(ctx context.Context) error                      // surfaced in settings
}
```

---

## 3. The container protocol

### 3.1 Command line

```
claude -p                                         \
  --output-format stream-json --input-format stream-json --verbose \
  --model <model> --permission-prompt-tool mcp__lexicode__request_approval \
  --mcp-config /workspace/.lexicode/mcp.json      \
  --settings /workspace/.claude/settings.json
```

The prompt is written to `/workspace/.lexicode/prompt.md` and delivered as the first stdin message
rather than as an argv string — prompts contain wiki pages and exceed argv limits.

### 3.2 Parsing stdout → activities

The adapter is the only code that knows this format. Mapping:

| stream-json message | Activity |
|---|---|
| `system` / `init` | `system`, level 2, title `session started`, payload: session id, tools, cwd |
| `assistant` with text | `thought`, level 1, title = first line, payload: full text |
| `assistant` with `tool_use` | `action`, level 1, `tool_name` set, `group_key = tool_name`, title from a per-tool formatter |
| `user` with `tool_result` | merged into the matching `action` (sets `ok`, `duration_ms`, payload result) |
| `result` | `response`, level 0, title = final message; usage and `total_cost_usd` → `Usage` |
| any `is_error` | `error`, level 0, `ok=0` (auto-expands in the UI) |
| stderr line | `system`, level 2 |

**Per-tool title formatters** (this is what "tool-aware rendering" and grouping are built on):

| Tool | Title | Payload |
|---|---|---|
| `Read` | `Read src/api/charge.ts` | `{path, lines}` |
| `Edit`/`Write` | `Edit src/api/charge.ts` | `{path, hunks:[…]}` — renders as a diff |
| `Bash` | `$ npm test` | `{argv, exit, stdout, stderr, truncated}` |
| `Grep`/`Glob` | `Search "idempotency" in src/**` | `{pattern, matches}` |
| `TodoWrite` | `Plan updated (4 items)` | `{items}` |
| MCP `lexicode__*` | see §3.3 | |
| unknown | `<Tool>` + compact params | `{raw}` — never raw JSON as the *default* rendering, but the fallback is honest |

**Levels** decide the verbosity switch: `0` = summary (state changes, plan, result, errors),
`1` = normal (every tool call, one line), `2` = verbose (raw output, stderr, proxy decisions,
session metadata). Level is assigned at ingest; the client filters. Live switching costs nothing.

### 3.3 The Lexicode MCP server (D-12)

Hosted by the orchestrator at `/mcp/{run_token}`. Tools:

```jsonc
// ask_human — parks the run in `needs input`
{ "questions": [ { "question": "…", "header": "Format",      // ≤12 chars
                   "options": [ {"label":"…","description":"…"} ],   // 2–4
                   "multiSelect": false } ] }
// → {"answers": {"<question>": ["<label>"]}} or {"response": "<freeform>"}

// set_step — the mutable current-step line. Fire and forget.
{ "step": "editing src/api/charge.ts", "index": 4, "total": 9 }

// propose_wiki_page — never auto-written (brief §6.5)
{ "title": "…", "slug": "…", "parent": "engineering", "body": "…",
  "agent_scope": "auto", "reason": "You corrected me twice about migrations",
  "edits_slug": "database-migrations" }   // optional: edit rather than create

// check_criterion — checks off acceptance criteria in the run summary
{ "criterion_id": "…", "met": true, "note": "covered by charge.test.ts:88" }

// request_approval — backing tool for --permission-prompt-tool
// Input is Claude Code's permission payload; the server enriches it with the six
// fields an approval card must show (action, scope, impact, reason, alternatives, recovery)
// → {"behavior":"allow","updatedInput":{…}} | {"behavior":"deny","message":"…"}
```

**Autonomy short-circuits `request_approval` before any human sees it:**

| Autonomy | `request_approval` behaviour |
|---|---|
| `suggest` | Every mutating tool denied with *"this agent is in Suggest mode; it plans, it does not act."* |
| `approve_each` | Always parks for a human. |
| `auto_gates` | Auto-allows unless the tool is destructive (writes outside the workspace, `rm -rf`, force push, network egress beyond policy) or it is the plan gate. |
| `auto` | Auto-allows anything the agent's `permissions` grant; denies the rest without asking. |

An `agent_permission_rules` row matching tool+pattern short-circuits before autonomy. "Always allow"
in the UI writes exactly one such row and shows it in agent settings — never a global mute
(interaction rule 8).

### 3.4 Steering and answers (kernel → container)

Both are stdin writes in Claude Code's `--input-format stream-json` user-message shape. Steering
messages are drained from `run_messages` **between tool calls** — the adapter writes only when it
has just consumed a `tool_result` and not while a tool is in flight. The composer's promise,
*"Applied after the current step,"* is therefore literally true.

Elicitation answers are returned as the MCP tool's *result*, not as stdin, which is what makes the
agent resume exactly where it asked.

---

## 4. The normalized event payload

The user-visible field vocabulary. Condition fields and `{{...}}` interpolation both address it.
Adding a field is additive; renaming one breaks users' rules and requires a migration.

```jsonc
{
  "pr":     { "number": 219, "title": "…", "author": "spruce", "author_kind": "human|agent",
              "branch": "dev/PAY-14", "base": "main", "draft": false, "merged": false,
              "state": "open|closed", "additions": 142, "deletions": 18,
              "files_changed": 7, "labels": ["…"], "body": "…", "url": "…" },
  "review": { "id": "…", "author": "…", "state": "approved|changes_requested|commented", "body": "…" },
  "comment":{ "id": "…", "author": "…", "body": "…", "path": "src/…", "line": 88 },
  "check":  { "suite_id": "…", "name": "CI", "conclusion": "success|failure|…", "url": "…" },
  "ticket": { "key": "PAY-14", "title": "…", "column": "In Review", "category": "review",
              "priority": "high", "assignee": "spruce", "delegate": "dev", "labels": ["…"] },
  "run":    { "id": "…", "seq": 482, "agent": "dev", "status": "completed|failed|needs_input",
              "cost_cents": 142, "duration_seconds": 318, "ticket_key": "PAY-14" },
  "repo":   { "owner": "acme", "name": "payments", "default_branch": "main" },
  "actor":  { "kind": "human|agent|external", "login": "…", "agent": "reviewer" },
  "schedule": { "cron": "0 9 * * 1-5", "fired_at": "…" }
}
```

Only the sub-objects relevant to the event kind are populated. Unknown paths evaluate to `nil` and
interpolate to `""` with a warning on the firing row.

### 4.1 Condition operators

`text.is`, `text.is_not`, `text.contains`, `text.not_contains`, `text.starts_with`, `text.matches_glob`,
`text.is_empty` · `number.eq`, `number.gt`, `number.gte`, `number.lt`, `number.lte` ·
`enum.is`, `enum.is_not`, `enum.in` · `bool.is` · `set.includes`, `set.excludes`, `set.is_empty` ·
`actor.is_agent`, `actor.is_human`, `actor.is`.

The type prefix is shown in the dropdown (UI spec §5.9) and is how users learn compatibility
without a type-system UI.

---

## 5. HTTP API

`/api/v1`, JSON, cookie session auth, `application/problem+json` errors. Full OpenAPI lives at
`internal/api/v1/openapi.yaml` and generates the frontend's types — it is the source of truth for
request/response shapes; this is the route inventory.

```
POST   /auth/setup                first-run owner creation (409 once a user exists)
POST   /auth/login  /auth/logout  GET /auth/me
POST   /invites  ·  POST /invites/{token}/redeem

GET    /projects · POST /projects · GET|PATCH /projects/{key}
GET    /projects/{key}/overview            About card + recent runs + pinned pages + activity
GET|PUT /projects/{key}/repo · POST /projects/{key}/repo/connect
POST   /projects/{key}/bootstrap/preview   detected issues, docs, CI, suggested agents+triggers
POST   /projects/{key}/bootstrap/apply     the checked subset only — never silent
GET|POST /projects/{key}/secrets · DELETE /projects/{key}/secrets/{id}

GET|POST /projects/{key}/agents · GET|PATCH|DELETE /agents/{id}
GET    /agents/{id}/directives · POST /agents/{id}/directives
GET    /agents/{id}/context-preview        dry context resolve (contracts §2.6)
GET|DELETE /agents/{id}/permission-rules

GET|POST /projects/{key}/columns · PATCH|DELETE /columns/{id}
GET    /projects/{key}/board?group_by=&filter=
GET|POST /projects/{key}/tickets · GET|PATCH|DELETE /tickets/{id}
POST   /tickets/{id}/move                  {column_id, position}
POST   /tickets/{id}/criteria · PATCH|DELETE /criteria/{id}
POST   /tickets/{id}/subtickets            {texts: []}  ← selection→sub-tickets
GET|POST /tickets/{id}/stream              unified stream; POST = comment
POST   /tickets/{id}/delegate              {agent_id, prompt?} → enqueues a run

GET    /projects/{key}/triage · POST /triage/{id}/{accept|duplicate|decline|snooze}

GET|POST /projects/{key}/wiki · GET|PATCH|DELETE /wiki/{id}
GET    /projects/{key}/wiki/search?q=
POST   /wiki/{id}/{accept|dismiss}         agent proposals
GET    /projects/{key}/wiki/context-budget
POST   /projects/{key}/wiki/import         from detected repo files (D-11)

GET|POST /projects/{key}/triggers · GET|PATCH|DELETE /triggers/{id}
POST   /triggers/{id}/backtest?days=7
GET    /triggers/{id}/firings
GET    /projects/{key}/trigger-catalog     merged EventCatalog + action schemas → drives the editor

GET    /projects/{key}/runs?status=&agent=&ticket=&trigger=&view=
GET    /runs/{id} · GET /runs/{id}/activities?since=&level=
GET    /runs/{id}/context · GET /runs/{id}/outputs · GET /runs/{id}/chain
POST   /runs/{id}/messages                 steering (queued)
POST   /runs/{id}/stop  ·  POST /runs/{id}/takeover
POST   /elicitations/{id}/respond          answer / approve / deny / approve-with-edits / remember
POST   /runs/{id}/acknowledge

GET    /inbox · GET /notifications · POST /notifications/{id}/{read|dismiss}
GET    /audit?project=&actor=&action=
GET|PUT /workspace/settings
GET    /stream?topics=…                    SSE
```

### 5.1 SSE

One connection per tab. `?topics=project:PAY,run:01H…,inbox`. Frames:

```
id: <ULID>
event: run.activity
data: {"topic":"run:01H…","payload":{…}}
```

Event types: `run.state`, `run.activity`, `run.step`, `run.usage`, `run.elicitation`,
`ticket.updated`, `board.updated`, `triage.created`, `trigger.fired`, `notification.updated`,
`wiki.proposed`, `provision.step`, `module.degraded`.

`Last-Event-ID` on reconnect replays from the activity/event log so a dropped connection loses
nothing. The hub fans out from the bus; handlers never write to SSE directly.
