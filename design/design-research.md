# Design Research — Agent Orchestration for SDLC

Compiled August 2026. Four parallel research passes over the current landscape, kept whole
because the product brief and UI/UX spec cite them. Every claim carries a source URL.

---


# Part 1 — AI coding-agent platforms: product & UI/UX landscape

# AI Coding-Agent Platforms: Product & UI/UX Landscape (August 2026)

## 0. How the market is shaped right now

Four surface archetypes have converged, and almost every product is some mix of them:

| Archetype | Canonical example | Core screen |
|---|---|---|
| **Agent-native cloud IDE** — chat + live workspace (shell/editor/browser) you can watch and seize | Devin, OpenHands, Cursor cloud agents | Split chat / workspace with tool tabs |
| **Task queue / inbox** — a list of async runs, each ending in a diff or PR | Codex cloud, Jules, Claude Code on the web, Copilot agents page | Sidebar of task cards with status pills |
| **In-situ agent** — no new UI; the agent lives in GitHub/Slack/Linear as a participant | Copilot coding agent, Charlie, Tembo, CodeRabbit, Bugbot | The PR/issue/thread itself |
| **Mission control / orchestrator** — one pane over many agents, sometimes many *vendors* | GitHub Agent HQ, Factory Missions, OpenHands Agent Canvas, Cursor 2.0 | Multi-session board |

The single biggest product shift of the last 12 months: **the bottleneck moved from generation to review**, and the UI investment followed it. Devin Review, Copilot's internal review step, Cursor Bugbot, Greptile Swarm Agents, and Codex's P0/P1-only review policy are all responses to the same measured problem (see §7).

Two notable exits: **Copilot Workspace was sunset May 30, 2025** and folded into Copilot coding agent ([source](https://www.javacodegeeks.com/2026/02/github-copilot-workspace-the-agentic-era.html)). **Codegen was acquired by ClickUp (Dec 22, 2025); the standalone service was deprecated Jan 16, 2026** ([source](https://clickup.com/blog/clickup-codegen-acquisition/)). **Sweep pivoted entirely** away from the GitHub issue→PR bot to a JetBrains IDE plugin ([source](https://github.com/sweepai/sweep)).

---

## 1. Devin (Cognition) — the most fully-realized "agent workspace"

### Main screen
Three-panel layout: **session list (left) → conversation (center) → workspace (right)**. The workspace is tabbed: **Shell, IDE (editor), Browser, Planner**. This is explicitly a simulation of a human dev's desk, and it remains the reference implementation everyone else copies. ([ppaolo analysis](https://ppaolo.substack.com/p/in-depth-product-analysis-devin-cognition-labs))

The left sidebar has matured into a real workspace manager during 2026:
- **Collapsible, drag-and-drop sidebar folders** for organizing sessions (May 17, 2026)
- **Persistent status labels on each session row** — "PR created", "Awaiting instructions" — plus timestamps, so you scan state without opening anything (May 8, 2026)
- **Pin/unpin sessions from the command palette**; **"start session in background"** so launching doesn't yank you out of what you're reading (Jun 24, 2026)
- **Cmd+K command palette** with search, keyboard nav, settings (May 29, 2026)
- **Archive All with undo** (May 17, 2026)
([release notes](https://docs.devin.ai/release-notes))

### Task creation
Prompt box in the web app; `@Devin` in Slack/Teams; Linear tickets; CLI (`curl -fsSL https://cli.devin.ai/install.sh`, with `/handoff` to escalate a local session to cloud Devin); REST API; MCP. **Devin Coach** (Aug 14, 2026) critiques your prompt *inside the input box while you type* — a pre-flight quality gate on the human's side, which is unusual and worth stealing.

### Monitoring
The **Progress tab is the control room**: one unified timeline of shell commands, code edits, and browser activity. Clicking any step scopes the shell/editor/browser panes to *that moment*. There's a **"Following" toggle** that pins your view to Devin's live cursor as it switches tools, and a **timeline scrubber** for replaying past work — the chat auto-scrolls to the matching context. ([Devin IDE guide](https://fast.io/resources/devin-ide-guide/))

Chat deliberately avoids character-by-character streaming: messages land complete after a typing indicator, and you can send follow-ups without waiting. This reads as talking to a colleague rather than to a completion.

### Interactive planning (the plan-approval flow)
1. **Initial Assessment** card: relevant files, key findings, implementation questions
2. **Detailed Plan** with **code citations and inline snippets**; citations deep-link into the Devin IDE
3. A **"Waiting for Approval" panel with a 30-second countdown**, plus an explicit **"Wait for my approval"** button that cancels auto-proceed. Default behavior is configurable at Settings → Customization.
([docs](https://docs.devin.ai/work-with-devin/interactive-planning))

The countdown-with-escape-hatch is the key design decision: it optimizes for the common case (plan is fine, don't make me click) without removing the gate.

### Mid-run intervention
Documented protocol: **pause Devin before taking over the IDE** to avoid simultaneous conflicting edits → toggle terminals from read-only to writable → edit with Cmd+K (terminal command) / Cmd+I (quick edit) → **post a change note before resuming** so Devin doesn't re-apply a discarded approach or clobber your fix. This "pause → take over → hand back with a note" ritual is the most concrete answer anyone has to the human-agent write conflict problem.

Historical weakness: the Editor was originally read-only, blocking pair-programming; Devin 2.0 fixed this.

### Knowledge, Playbooks, Search, Wiki
- **Knowledge** (Settings → Resources → Knowledge): each item has a **trigger description** (natural-language retrieval condition) + content, an optional **`!macro`** shorthand for direct invocation, nested folders with bulk enable/disable, per-user toggles, and **scoping to no repo / specific repos / all repos** at Org and Enterprise levels. Devin **suggests knowledge items from session feedback**, which you edit or regenerate before saving. ([docs](https://docs.devin.ai/product-guides/knowledge))
- **Playbooks**: reusable prompt documents with a fixed skeleton — *Overview, Procedure, Specifications (postconditions), Advice and Pointers, Forbidden Actions, Required from User*. Attach via web app, drag a `.devin.md` into a session, or `!macro`. When attached, a **blue pill** appears with an inline editor so you can tweak before launch. Version history with revert. ([docs](https://docs.devin.ai/product-guides/creating-playbooks))
- **Devin Search**: agentic codebase Q&A with cited code, optional **Deep Mode**.
- **Devin Wiki**: auto-indexes repos every couple of hours into wikis with architecture diagrams and source links.
- **DeepWiki**: the public version — swap `github.com` → `deepwiki.com` on any repo URL for instant generated docs plus an **"Ask Devin"** chat. 50,000+ repos pre-indexed, free for public repos. ([Cognition](https://cognition.com/blog/deepwiki))

### Slack
`@Devin` in any channel, attachments supported, bang-commands (`!ultra`, `!fast`) anywhere in the message. Devin replies **in-thread**. Inline control keywords: `mute`/`unmute`, `sleep` (any message wakes it), `archive`, `EXIT`. **Code channels** — a dedicated Slack channel per session, with **status indicators, PR chips, and live worklogs** rendered in-channel; created via `!new` or auto-migrated by Devin when a thread grows into real work. **Automations** watch reactions/mentions and trigger Devin autonomously. ([docs](https://docs.devin.ai/integrations/slack))

### Devin Review (the PR-review product)
This is the most interesting review UI in the market. Instead of GitHub's alphabetical file list, it does **intelligent diff organization**: groups logically connected changes, **orders the hunks into a reading sequence, and explains each hunk** — a guided walkthrough rather than a file tree. It detects moves/renames so they don't render as delete+rewrite. Findings are severity-coded — **red = probable bug, yellow = warning, gray = informational** — and interleave with normal human comment bubbles. Inline chat with full codebase context, without leaving the PR. **Clickable finding counts jump straight to the finding** (Aug 19, 2026). Access three ways: `app.devin.ai/review`, swap `github` → `devinreview` in any PR URL (no login for public repos), or an npm command locally. Free during early release. ([blog](https://cognition.com/blog/devin-review))

**Autofix**: Settings → Customization → Autofix settings, where you select *which bots* (linters, CI, security scanners, Devin Review itself) should trigger automatic fixes on a PR. Write → Catch → Fix → merge-ready. ([blog](https://cognition.com/blog/closing-the-agent-loop-devin-autofixes-review-comments))

### Pricing
Five tiers as of mid-2026: Free, Pro $20/mo, Teams $80/mo ($40/mo per full dev seat + unlimited free flex seats), Max $200/mo, Enterprise custom. ACU ≈ 15 minutes of active Devin work. The top tier dropped from $500 → $200 in July 2026. ([costbench](https://costbench.com/software/ai-coding-assistants/devin-ai/))

### Steal / avoid
**Steal:** the Progress-tab step→panel scoping; the Following toggle + timeline scrubber; sidebar status labels as first-class ("PR created" / "Awaiting instructions"); countdown-approval with an explicit "wait for me" escape; the blue-pill playbook attach with inline pre-launch editing; knowledge items with *trigger descriptions* rather than always-on context; `!macro` shorthand; Slack code-channels with PR chips and live worklogs; hunk-reordering review.
**Criticized:** no meta-awareness (Devin can't answer questions about its own product and gives generic advice instead); confidently claims capabilities it lacks, e.g. screenshot analysis; historically read-only editor. Answer.AI's month-long trial found the polished visibility was itself painful — you *watch* it go down wrong paths for hours, and the tasks it reliably does are "so small and well-defined that I may as well do them myself, faster, my way." Unpredictability, not incapability, was the trust-killer. ([Answer.AI](https://www.answer.ai/posts/2025-01-08-devin.html)) On HN, "Devin has negative brand value" from the original replace-your-engineers marketing still comes up. ([HN](https://news.ycombinator.com/item?id=46711589))

---

## 2. Factory.ai (Droids)

### Surfaces
Desktop app (Mac ARM/Intel, Windows x64/ARM64), **Droid CLI** (`droid` in any repo), web at `app.factory.ai`, mobile, IDE extensions. **Sessions, Droid Computers, MCP servers, and Skills sync across all of them** — start on CLI, continue on phone. ([docs](https://docs.factory.ai/))

### Droid roles
Code Droid (writes features/fixes, produces the diffs that become PRs), Review Droid (evaluates against standards; **explicit role boundary — cannot write features**), Docs Droid, Test Droid, Knowledge Droid (searchable index across repos/docs/tickets as "a shared memory layer across tickets"). Custom droids are Markdown-defined with focused system prompts, model preference, and **restricted tool access** — you can build a reviewer that literally cannot edit. ([digitalapplied](https://www.digitalapplied.com/blog/factory-ai-multi-agent-coding-platform-review), [Sid Bharath](https://sidbharath.com/blog/factory-ai-guide/))

### Task creation
Ticket-first, not chat-first: pulls **ticket title, description, acceptance criteria, comments, linked issues, and attached files** from Linear/Jira into the coordinator's initial context. Also Slack (with **per-channel auto-run model selection**, Apr 28, 2026), GitHub, Sentry, PagerDuty.

### Missions (the standout orchestration UI)
`/enter-mission` starts a **conversational scoping phase** — Droid asks clarifying questions and probes constraints before presenting a plan for approval ("most of the value comes from" this phase). Then it decomposes into **milestones → features**, spawning **a fresh worker session with clean context per feature**, coordinating handoffs through git, parallelizing *within* features and during validation. **Mission Control** shows the feature list, progress logs, and validation output. Validation workers do **user-simulation testing — clicking through the UI, verifying state transitions, catching layout bugs no test suite covers**. The human's stated role is project manager: monitor, unblock, redirect. ([Missions](https://factory.ai/news/missions))

**Specification mode** is the lighter-weight cousin: forced research phase producing acceptance criteria, technical design, file changes, testing strategy, implementation steps — an approval checkpoint before any code.

### Notable UI details from the 2026 changelog ([full changelog](https://docs.factory.ai/changelog/release-notes))
- **`/btw` side-questions panel** (May 28) — ask a tangential question without derailing the main thread. Devin shipped the same idea as "side chats" on Aug 12, 2026. This is becoming a standard pattern.
- **Sticky user messages** — your prompt stays anchored at the top as the agent's output scrolls under it (May 6). Solves "what did I even ask for" in long runs.
- **Mission side panel** (Jul 14); **fullscreen file preview** (Jul 21); **diff viewer for Droid Computers** (Apr 8); **multi-select questions** so you answer several agent questions at once (Jul 29); **hooks manager overhaul** (Jun 29); **draft messages persisted between sessions** (Apr 29); **keep-awake setting so your laptop doesn't sleep mid-run** (Jul 22); **Mermaid diagrams rendered with the official engine, themed to match** (Aug 18 / Apr 10); **live streaming of partial text and thinking** (May 4); **AutoWiki** with search and exports.
- Mobile: **mobile-first review flow** with diff viewer and terminal output optimized for small screens; approve changes and give feedback from a phone; **share a session link so teammates can observe, comment, or take over with zero install**. ([Factory Web](https://factory.ai/product/web))

### Pricing
Free (BYO keys), Pro $20/mo (20M tokens), Max $200/mo (200M), Ultra $2,000/mo (2B). Overages $2.70/M; cached tokens 90% cheaper. Token-based, not per-seat. ([Fritz](https://fritz.ai/factory-ai-review/))

### Steal / avoid
**Steal:** role-bounded droids with enforced tool restrictions; conversational scoping before plan approval; fresh-context worker per feature; validation workers that drive the UI; `/btw` side panel; sticky user messages; multi-select question batching; keep-awake; zero-install shareable session links for PMs/execs.
**Criticized:** overhead on trivial fixes (single-agent tools win on speed); poor fit for greenfield (nothing to index); **requires ticket hygiene** — teams that spec in Slack get much less value; inconsistent code quality requiring careful review; unpredictable token costs; steep learning curve.

---

## 3. OpenHands / All Hands AI (open source)

### Surfaces
**Agent Canvas** is now the default browser client and control center (it replaced the older local GUI), hosted as **OpenHands Cloud** at `app.all-hands.dev/canvas`; plus the CLI, the **Agent Server** (REST + WebSocket over agent execution, conversations, tools, workspaces), the **Automation Server** for scheduled/event-driven runs, and the **Software Agent SDK**. ([docs](https://docs.openhands.dev/overview/introduction.md))

The agent works in a sandboxed Docker environment with **terminal, code editor, browser, and file system**, surfaced as panels alongside the conversation.

### The 2026 differentiator: ACP / bring-your-own-agent
Agent Canvas added a setup wizard where you **choose OpenHands, Claude Code, Codex, Gemini CLI, or any ACP-compatible agent** (changeable later at Settings → Agent), then run them all in **one visual workspace**: "Keep every agent, task, and result visible in Agent Canvas, then turn the workflows that work into repeatable automations." Rich automations run agents on a schedule for code review, dependency upgrades, doc updates. ([blog, Jun 18 2026](https://www.openhands.dev/blog/use-any-coding-agent-in-openhands-with-acp))

This is the open-source answer to Agent HQ, and it landed *before* most of Agent HQ's multi-vendor rollout.

### GitHub resolver
Label-driven, zero-UI: add the **`fix-me`** label to an issue (or `fix-me-experimental`) and within minutes you get either a PR or a message that it failed **plus a branch containing its intermediate progress**. Runs as a GitHub Action from your own repo. The "here's the branch with partial work even though I failed" behavior is a good trust pattern — failure still produces an artifact. ([blog](https://www.openhands.dev/blog/open-source-coding-agents-in-your-github-fixing-your-issues))

### Microagents / Skills
Two kinds: **Knowledge agents** (keyword-triggered, reusable across projects — language/framework/tool expertise) and **Repository agents** (auto-loaded, project-specific conventions). Stored in `.openhands/skills/` (V1) or `.openhands/microagents/` (V0, still supported). Markdown with optional YAML frontmatter declaring triggers; no frontmatter = defaults. Load order: repo instructions first, then keyword-activated knowledge agents. Public shareable skills ship in the repo (`github.md`, `docker.md`, `code-review.md`). ([skills README](https://github.com/OpenHands/OpenHands/blob/main/skills/README.md))

### Pricing
Open Source local: free, unlimited conversations, MIT. Individual SaaS: free to start, **10 conversations/day**, BYO key or OpenHands models at cost. Enterprise: custom, SaaS or your VPC, SAML/SSO, named CE. ([pricing](https://www.openhands.dev/pricing))

### Steal / avoid
**Steal:** agent-picker at setup (vendor-agnostic canvas); "everything visible in one workspace, then promote what works into an automation"; label-as-trigger with no new UI; partial-progress branch on failure; two-tier microagent model (always-on repo context vs keyword-triggered knowledge).
**Criticized:** hosted version may use your content for training with no documented opt-out; **no SOC 2 / ISO 27001**; self-hosting means managing Docker infra and API spend; "not the right fit for unsupervised production deployment" — returns incomplete solutions needing human review. ([AI Agent Index](https://theaiagentindex.com/agents/openhands))

---

## 4. Google Jules

### Main screen
Dashboard → GitHub connect → **repo + branch selector** → prompt box → plan → execution status → **Code diff review tab** → **"Publish branch"** button that opens a PR against main. ([MachineLearningMastery walkthrough](https://machinelearningmastery.com/practical-agentic-coding-with-google-jules/))

### Plan approval flow — the cleanest in the category
Jules presents a plan with **a natural-language description of intent, a step-by-step breakdown, and any assumptions or setup steps**. Steps are **expandable** for detail. Controls: an **"approve plan"** button, and a **chat input alongside the plan** where you say "revise step 3", "you missed X", "here's what I actually meant" — Jules answers and rewrites the plan in place. **If you navigate away, Jules auto-approves on a timer.** ([docs](https://jules.google/docs/review-plan/))

Jules added a **Planning Critic** (Jan 26, 2026): a second review agent that critiques *auto-approved* plans before execution — +9.5% task reliability. That's a smart mitigation for the auto-approve escape hatch: if the human abstains, a machine reviewer stands in.

### 2026 changelog highlights ([changelog](https://jules.google/docs/changelog/))
- **CI Fixer** (Feb 19, 2026): auto-detects and fixes CI failures on PRs
- **Commit authoring modes**: Jules-only / co-authored / user-only — attribution as a first-class setting, which matters enormously for blame and audit
- **MCP servers** (Feb 2, 2026) — Linear, Stitch, Neon, Supabase — configured from the Settings page with API keys
- **Editable/pausable/resumable scheduled tasks** from the menu, no delete-and-recreate
- **Repoless sessions** via REST API; full file output as git patch; timestamp-filtered activity
- Browser notifications when a task completes or needs input

### Jules Tools (CLI)
**Side-by-side diff viewer in the TUI**, repo inference from cwd (no manual config), `jules remote new --parallel` for up to 5 concurrent attempts. ([changelog](https://jules.google/docs/changelog/2025-11-10/))

### Pricing
Free: 15 tasks/day, 3 concurrent, base model. Pro (bundled with Google AI Pro): 100/day, 15 concurrent. Ultra: 300/day, 60 concurrent, priority model access. **When you exhaust the daily limit the new-task button is disabled but history stays browsable.** Paid tiers currently require personal @gmail.com accounts — Workspace/enterprise not yet supported. ([usage limits](https://jules.google/docs/usage-limits/))

### Steal / avoid
**Steal:** expandable plan steps with a chat input *docked to the plan itself*; the plan-critic agent as a stand-in reviewer when the human auto-approves; explicit commit-authorship modes; disabled-but-browsable state at quota exhaustion; parallel attempts (`--parallel`) as a first-class CLI flag.
**Criticized:** auto-approve-on-navigate-away is the most-criticized part of the flow — it converts inattention into consent; struggles with architectural overhauls and hits context limits on large enterprise codebases; restrictive free tier slowed adoption; widely read as "catch-up rather than leading"; the Gmail-only paid tier blocks exactly the teams who'd pay. ([DEV](https://dev.to/pinishv/google-jules-always-on-my-radar-but-never-quite-the-star-3gba))

---

## 5. OpenAI Codex (cloud agent)

### Main screen
Lives in the **ChatGPT sidebar**. Prompt box with **two verbs: "Code" and "Ask"** — a small but important decision that separates read-only investigation from mutation at the point of intent, rather than hoping a system prompt holds. Each task runs in **its own isolated cloud sandbox preloaded with your codebase**, so parallelism is free. ([launch post](https://openai.com/index/introducing-codex/))

### Verification surface
Deliberately over-instrumented for trust: **terminal logs, test output, file citations linking to exact code, and a diff**, then a **pull request button**. The stated design goal is that you can verify without re-deriving.

Execution is **two-phase**: setup phase with internet *on* for dependency install, agent phase with internet *off* by default for edits and validation. This is legible in the UI as distinct stages. ([Vaughan](https://codex.danielvaughan.com/2026/04/08/codex-cloud-task-application/))

### Entry points
ChatGPT web, IDE ("kick off a cloud task from your editor, then monitor progress"), **`@codex` on GitHub issues and PRs**, **`@Codex` in Slack** (reads earlier thread messages for context so you rarely restate background; reacts 👀 while working; replies with a task link; optional `in openai/codex` repo targeting), and CLI.

### CLI ↔ cloud bridge
`codex cloud list` (plain text) / `--env ENV_ID --json --limit 10` (scriptable), `codex cloud status TASK_ID`, `codex cloud diff TASK_ID` (unified diff to pipe anywhere), `codex apply TASK_ID` (runs `git apply`, reports patched files or conflicts). Three review paths for one task: CLI, web dashboard, or the Slack thread link.

### Code review
`@codex review` on a PR → 👀 reaction → posts a review "just like a teammate would." Scoped comments: `@codex review for issues in the database migration`. Automatic reviews on new PRs can be enabled in settings with rules about which PRs and when. **It flags only P0 and P1 issues** so comments stay on high-priority risk — an explicit, product-level answer to reviewer fatigue. Repo-level tuning via a `## Code Review Rules` section in `AGENTS.md`, with guidance to write "two or three concise rules that encode checks reviewers often explain," not mechanical lint. Follow-up: `@codex fix the P1 issue` opens a cloud chat that pushes to the branch. ([docs](https://learn.chatgpt.com/docs/third-party/github))

### Pricing
Plus $20/mo is the practical baseline; Free/Go are evaluation-only; Pro 5x $100, Pro 20x $200; Business ~$480/yr/seat adds admin. ([Saffari review](https://omidsaffari.com/blog/codex-review))

### Steal / avoid
**Steal:** Ask-vs-Code as two buttons; file citations that deep-link into the exact code the agent read; visible setup-phase-online / agent-phase-offline staging; **P0/P1-only review policy as a product default**; `AGENTS.md` review rules written as "checks reviewers often explain"; a diff you can pipe (`codex cloud diff | ...`) so review isn't UI-locked; `codex apply` to pull the change local instead of forcing a PR.
**Criticized:** ~30–90s sandbox cold start per task frustrates quick iteration; the web diff UI is less polished than native surfaces and the split-pane doesn't adapt well to narrow screens; sandbox can't reach private package registries; inconsistent code style across multi-file changes creates visual friction while reviewing; official docs disagree with each other on included Plus usage ranges, so capacity is unplannable. Codex also has **the worst "ghosting" rate measured** — 10.0% of rejected PRs with feedback are abandoned by the agent, vs 3.8% overall ([arXiv 2601.00753](https://arxiv.org/html/2601.00753v1)).

---

## 6. Cursor — background/cloud agents, web, and Bugbot

### Cursor 2.0's reframe
The IDE was rebuilt "from the ground up to be centered around agents rather than files," with a one-click return to the classic file-centric layout. Many agents run in parallel **backed by git worktrees or remote machines** so they don't collide. Cursor explicitly says they run *multiple models on the same problem and pick the best result*. They name **code review and testing as the two bottlenecks** the release targets, and shipped a **native browser tool** so the agent verifies its own work before handing it to you. ([Cursor 2.0](https://cursor.com/blog/2-0))

### Cloud agents
Launch from: desktop (**"Cloud" in the dropdown under the agent input**), **cursor.com/agents** on any device, iOS app / Android PWA, **`@cursor` comment on a GitHub or Bitbucket PR or issue**, `@cursor` in Slack and Linear, or the API. ([docs](https://cursor.com/docs/cloud-agents))

Two standout patterns:
1. **Demo artifacts attached to the PR** — agents produce "merge-ready PRs with artifacts to demo their changes," including **screenshots, videos, and logs so you can see exactly what changed and how the agent verified its work.** This is the strongest available answer to "the agent's reasoning was discarded before review."
2. **Remote desktop takeover** — "take control of the agent's desktop to test the software yourself in a full development environment without checking out the branch locally." Reviewing by *using the software* rather than reading the diff.

The dashboard shows **which environment and Build each agent used**, with environment details and version history on repo-name hover. **Team follow-ups** (admin-gated) let a colleague continue someone else's run.

### Bugbot
Reviews every PR diff automatically, or on demand via a `cursor review` / `bugbot run` comment. Comments carry **severity indicators** and dual fix affordances: **"Fix in Cursor"** (deep-links into the editor) and **"Fix in Web"** (opens cursor.com/agents). Autofix spawns a Cloud Agent in an isolated VM that reproduces the bug, tests a fix, and opens a fix PR.

**Automations dashboard**: review activity and outcomes — issues found, PRs reviewed, **accepted findings, and acceptance rate per rule** — filterable by repo, PR number, date range, and posted-vs-dry-run. Per-rule acceptance rate is the single best noise-management instrument shipped by anyone; it turns "is this rule worth keeping" into a number.

Noise controls: per-repo enablement, reviewer allow/deny lists, per-member overrides ("run only when mentioned", "run only once per PR", "reviews on draft PRs"), admin-set effort levels (Default / High / Custom), **`bugbot run verbose=true`** to see every rule included in a run and which were truncated or omitted, and **`@cursor remember [fact]`** to teach it a persistent rule. Rules take scoped paths. ([docs](https://cursor.com/docs/bugbot))

### Pricing
Bugbot Pro: $60/user/mo ($20 Cursor + $40 Bugbot add-on), 200 PRs/mo; Business $80/user/mo unlimited. No free tier. As of May 2026 it moved to usage-based, ~$1.00–$1.50 per run depending on PR complexity. ([WeavAI](https://weavai.app/blog/en/2026/05/12/cursor-bugbot-2026-review-ai-bug-detection-autofix/), [Critique](https://www.critique.sh/blog/ai-code-review-pricing-2026))

### Steal / avoid
**Steal:** demo artifacts (screenshots/videos/logs) attached to every agent PR; remote desktop takeover for review-by-use; dry-run mode plus **per-rule acceptance-rate analytics**; `verbose=true` to expose which rules actually ran and which got truncated; `@cursor remember` as an in-PR teaching gesture; dual "Fix in Cursor" / "Fix in Web" affordances; environment + build version surfaced on the run card.
**Criticized:** branch management is clunky — errors creating branches that don't exist, and **it's unclear whether a follow-up will modify the existing branch or spawn a new one** ([Zack Proser](https://zackproser.com/blog/cursor-agents-review)); documentation is thin next to OpenAI's; GitHub-only for Bugbot (no GitLab/Bitbucket/ADO); $40/user/mo add-on is a hard sell for non-Cursor shops; Bugbot deliberately skips style/formatting, which surprises teams expecting full coverage.

---

## 7. GitHub — Copilot coding agent, Agent HQ, mission control

### Agents page / panel
Reachable from the **agents panel available on any GitHub page**, or directly at `github.com/copilot/agents`. Sessions you started *and sessions others requested* appear in your list. Selecting one opens the **session log and overview**, showing: live progress, **token usage**, session duration, and **Copilot's internal reasoning plus the tools it used** to understand the repo, make changes, and validate work. ([docs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/track-copilot-sessions))

Controls in the log viewer:
- **Steering**: a prompt box *below the live log* — type guidance, press Enter, and Copilot folds it in **after finishing its current action**. This "queue, don't interrupt" semantic avoids the mid-write corruption problem Devin solves with an explicit pause.
- **Stop session**: kills the Actions run; **commits already pushed are preserved**.
- **Archive** stopped sessions (cloud sessions can't be deleted; local ones can).
- **Commit → session log backlinks**: every cloud-agent commit links to the session log, so during code review you can ask "why was this line written" and get the actual reasoning. This is the single best-designed trust affordance on GitHub.

### Task creation
Assign Copilot as **the owner of an issue**; start from Copilot Chat on github.com (context carries forward); **`@copilot` in a PR comment**; automations on a schedule or event; Slack/Teams ("add context, steer Copilot sessions, monitor progress"). Copilot works on a feature branch and you can **iterate before it creates the PR**, or tell it to open one immediately.

### Agent HQ / mission control
Announced Oct 2025, multi-vendor rollout through 2026. **Mission control** is "a unified command interface across GitHub, VS Code, mobile, and CLI": assign tasks across repos, pick a custom agent, **watch real-time session logs, and steer mid-run (pause, refine, or restart)**; branch controls for CI oversight; identity-based access matching team permissions; one-click merge-conflict resolution; integrations with Slack, Linear, Teams, Azure Boards, Jira, and Raycast. ([Agent HQ](https://github.blog/news-insights/company-news/welcome-home-agents/), [mission control guide](https://github.blog/ai-and-ml/github-copilot/how-to-orchestrate-agents-using-mission-control/))

**Agent picker**: enter your request and pick an agent from the **Copilot icon dropdown**. In the repo, the **Agents tab**; in PR comments, `@Copilot` / `@Claude` / `@Codex`. In VS Code, `Cmd+Shift+P` → "agent sessions" opens the **Agent sessions view**, where you choose Local (Copilot), Claude, or Codex for interactive work, or Cloud for autonomous GitHub tasks. Critically: **"assign multiple agents to a task, and see how Copilot, Claude, and Codex reason about tradeoffs and arrive at different solutions."** ([pick your agent](https://github.blog/news-insights/company-news/pick-your-agent-use-claude-and-codex-on-agent-hq/))

**Custom agents**: `.github/agents/NAME.md` with YAML frontmatter (name, description, prompt, tools, MCP servers). Three scopes — repo (`.github/agents/`), org (`/agents/` in `.github` or `.github-private`), enterprise (`/agents/` in a designated `.github-private`). The **agent picker auto-populates from these locations** based on your access level, and they're available in cloud agent, IDEs (VS Code, JetBrains, Eclipse, Xcode), and Copilot CLI. ([docs](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-custom-agents))

**Plan mode** (VS Code): asks clarifying questions to build a step-by-step approach before implementation, locally or delegated to cloud.

**GitHub MCP Registry** in VS Code: discover and one-click install MCP servers (Stripe, Figma, Sentry).

### Reviewing Copilot's PRs
Three UI decisions worth noting ([docs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/review-copilot-prs)):
1. **Your approval of a Copilot PR doesn't count** toward required approvals — a hard human-in-the-loop gate the platform enforces, not a norm you have to police.
2. GitHub Actions **do not auto-run** on Copilot branches; an **"Approve and run workflows"** button appears in the merge box, so an agent can't self-trigger CI with privileged secrets.
3. **Thumbs up/down on PRs and individual comments**, with a reason picker on the downvote.
The coding agent workflow also includes **an internal Copilot code review step before it shows you the changes**.

### Copilot Workspace (historical)
Launched Apr 2024 with a three-stage pipeline — **spec generation → plan → implementation** from a plain-English issue. **Sunset May 30, 2025**; its sub-agent architecture, issue→PR workflow, and async execution model were rebuilt as the Copilot coding agent (GA Sept 2025).

Its recorded UI failures are instructive because everyone is rebuilding the same flow ([community discussion #145254](https://github.com/orgs/community/discussions/145254)):
- Users couldn't tell **when to hit "Generate plan" vs. update the brainstorm**, and the UI didn't clearly mark components as **"Outdated"** needing refresh
- **Manual file reordering in the plan was tedious** on large file lists — and order matters for output quality (tests should precede implementations)
- The brainstorm panel didn't distinguish **refinement questions from informational ones**, so users didn't know whether to accept all suggestions or pick
- Users routinely iterated past the original task but **had no affordance for "start a new task session"**, so quality silently degraded
- Rate limits interrupted moderately-scoped work; only VS Code + browser; "don't find all relevant files to update, seems to only scan small subset"; PRs couldn't be signed

### Steal / avoid
**Steal:** commit→session-log backlinks (reasoning survives into review); queued steering that applies after the current action; stop-session that preserves pushed commits; **platform-enforced "your approval doesn't count"**; **Actions gated behind "Approve and run workflows"**; agent picker populated from version-controlled `.github/agents/`; running the same task on multiple vendors' agents to compare reasoning; token usage + duration on the session card.
**Criticized:** the mission control launch materials are capability-heavy and screenshot-light, and the docs never describe the actual board; the Workspace complaint list above is a checklist of what to avoid in any brainstorm→plan→implement flow; Copilot has drawn maintainer complaints about PR volume and, in one incident, promotional text leaking into PR comments.

---

## 8. Claude Code on the web / Anthropic surfaces

### Main screen
`claude.ai/code`. Left sidebar with Chats / Projects / Artifacts / **Code** tabs and a Starred section; center a session list with live states ("In progress", "Idle"); right panel the active session showing the prompt and Claude's command output. Also a dedicated tab in the Claude mobile app. ([Simon Willison](https://simonwillison.net/2025/Oct/20/claude-code-for-web/), [docs](https://code.claude.com/docs/en/claude-code-on-the-web))

### Task creation & environments
Point at a GitHub repo, pick an **environment** (a saved config controlling network access, env vars, setup script), submit a prompt. **You can queue additional prompts while it runs.** Network modes: **no network / Trusted (proxy with a default allow-list of dozens of domains) / Custom allow-list including `*`**. The same environments apply from web, terminal, Claude Tag (Slack), routines, mobile, and Desktop.

### Web ↔ terminal handoff (the differentiator)
- `claude --cloud "task"` from a terminal spawns a cloud session; **while the container provisions the CLI shows a live checklist of setup steps** (cloning, running setup script) and **queues messages you type during provisioning**, sending them once ready.
- `claude -p "message" --cloud <session-id>` queues a steering message into a running session **from any machine**, without local session state.
- `claude --teleport` (or `/teleport`, `/tp`, or `t` from `/tasks`) pulls a cloud session **and its branch** into your terminal with full conversation history. Guardrails checked first: clean git state (prompts to stash), correct repository (errors naming both repos), branch pushed, same account.
- From the web: **Open in → Terminal** copies the exact command.
- Recommended flow: **plan locally in plan mode, commit the plan, then `claude --cloud "Execute the migration plan in docs/migration-plan.md"`.**

### Review
Each session shows a **diff indicator like `+42 -18`**; clicking opens the diff view where you **leave inline comments on specific lines and send them with your next message** — review comments as agent input rather than as a separate channel. Per-file diffs stream as Claude edits.

### Auto-fix pull requests
A **per-PR toggle in the CI status bar**. Claude subscribes to GitHub events on the PR and, per event: **clear fixes** → makes, pushes, and explains the change in the session; **ambiguous or architecturally significant requests** → asks you first; **duplicates/no-ops** → notes and moves on. It can reply to review threads on GitHub under your account, **each reply labeled as coming from Claude Code**. Enable from the CI bar, `/autofix-pr` in terminal, mobile ("watch this PR and fix any CI failures"), or by pasting a PR URL. Documented limitation: GitHub emits no webhook for base-branch conflicts, so you must ask for a rebase.

Docs carry an explicit warning that auto-fix + `issue_comment`-triggered automation (Atlantis, Terraform Cloud) can deploy infrastructure from a comment — a rare example of a vendor documenting the blast radius of its own convenience feature.

### Sessions
Sidebar with archive-on-hover and delete-from-archived-filter. **Sharing has explicit visibility states** — Private/Team for Enterprise & Team (with repository-access verification on by default), Private/Public for Max & Pro (verification **off** by default, with a docs warning to check for credentials before sharing). Expired sessions are **marked expired in the list** and reopening provisions a fresh VM with conversation history restored.

### Steal / avoid
**Steal:** the provisioning checklist with message queuing (dead time becomes usable); teleport with pre-flight guardrails that name what's wrong; `+42 -18` diff badge on every session row; **inline diff comments that become the next prompt**; three-tier auto-fix triage (act / ask / note); labeled agent replies posted under a human's account; expired-but-restorable sessions; documented warning about comment-triggered infra automation.
**Criticized:** Simon Willison flags the **Trusted network allow-list as broad enough to be an exfiltration vector**; teleport is one-way from the CLI (you can't push a terminal session to web); cloud sessions share your account rate limits, so parallelism costs you proportionally; GitHub-only for clone + PR (GitLab/Bitbucket only via local bundle, no push back).

---

## 9. The AI code-review tier

### CodeRabbit
PR comment structure: a **plain-English walkthrough/summary**, a **file-by-file table**, an **auto-generated sequence diagram** of the change's flow through the system, then line-by-line inline comments with **one-click "committable" suggestions**. **Agentic chat**: `@coderabbitai` in a thread to ask questions, have it explain logic, generate unit tests, add docstrings, or **open a new PR implementing its own suggestion**. Learns team preferences over time. Issue Planner ties to Linear/Jira/GitHub Issues. Analytics dashboard on Pro. GitHub, GitLab, Bitbucket, Azure DevOps.
Pricing: Free (summaries only), Pro $24/dev/mo annual ($30 monthly), Pro+ $48. **Billed only for developers who open PRs**, no repo caps.
**Criticized:** the defining complaint is volume — **8–20 comments per PR vs. 2–5 for competitors**, "overwhelming for small changes." It "can flag trivial issues as 'major,' comment on things that are out of scope, or simply be wrong with confidence." On diffs over ~50 files it can take many minutes, so **some teams have removed it from merge-blocking automation**. Blind to cross-service dependencies and business logic. ([CuratorBits](https://curatorbits.com/reviews/coderabbit/), [WeavAI](https://weavai.app/blog/en/2026/04/29/2026-coderabbit-ai-review-is-the-top-pr-tool-worth-it/))

### Greptile
GitHub App / GitLab, comments without touching CI. **Swarm Agents**: one PR analyzed simultaneously by multiple agents from different angles — security, performance, logic correctness, style — which moved addressed-comments-per-PR from 0.92 → 1.60 (+74%) and adoption 30% → 43%. Inline suggestion format for one-click apply. **Repository Q&A** ("which file handles this endpoint?") on a semantic graph. **`.greptile.yaml`** (v4) for custom rules, ignored directories, and strictness levels.
**Criticized:** measured at **~11 false positives vs. CodeRabbit's 2** in one head-to-head. Pricing moved from $30/user/mo flat to **$30 including 50 reviews, then $1 per review** — widely called out as unpredictable for high-velocity teams, against Greptile's own claim that <10% of users would exceed the limit. ([WeavAI](https://weavai.app/blog/en/2026/05/12/greptile-2026-review-ai-code-review-pricing-debate/), [Levelop](https://levelop.dev/blog/best-ai-code-review-tools-2026-coderabbit-greptile-qodo-compared))

### Qodo
Consolidated in 2026: former **Qodo Merge / Gen / Command / Aware** are now features of one platform — Git review agents, JetBrains + VS Code pair programmer, terminal agents, and a **multi-repo Context Engine** powering all three. Custom rule enforcement, custom labels for focused review, secret and vuln scanning, GitHub/GitLab/Bitbucket/ADO, and cloud / single-tenant / on-prem / **air-gapped** deployment. Multi-agent architecture shipped Feb 2026; **highest measured F1 at 60.1%** in one comparison. ([Qodo](https://www.qodo.ai/formerly-qodo-merge/))

### Bugbot, Devin Review
Covered in §6 and §1. Bugbot's per-rule acceptance-rate dashboard and Devin Review's hunk-reordering walkthrough are the two most differentiated review UIs.

### Amp (Sourcegraph)
**Threads** are the unit — "conversations with the agent, containing all your messages, context, and tool calls" — revisitable, editable, addressable by URL or thread ID. **Thread sharing with four visibility levels**: Unlisted (link-accessible to anyone), Workspace, Group (Enterprise), Private; default is workspace-shared, changed from the CLI command palette or the web sharing menu. Teammates **search and learn from each other's threads**, with costs shown per thread. **Feed at `ampcode.com/feed`** with search and filtering. **Oracle** is an explicit "second opinion" model (routes to a high-reasoning model) invoked automatically or on demand for review, debugging, architecture. **Subagents** get independent context and tools, run in parallel, and — stated plainly in the manual — **work in isolation without mid-task guidance**. **Handoff system** transfers context between sessions by generating a structured prompt rather than lossy compression. **Amp Tab** is compiler-error-aware autocomplete. Workspace leaderboards. ([manual](https://ampcode.com/manual))
**Amp Free**: ads at the bottom of the editor and CLI (Axiom, Chainguard, Vanta, WorkOS), **targeted on your codebase content**, "shown separately and never influence Amp's responses." Code snippets aren't shared with ad partners, but free mode requires opting into training-data sharing. Reception was more positive than expected; the main objection is the training-data opt-in, not the ads themselves. ([Amp Free](https://ampcode.com/news/amp-free), [tessl](https://tessl.io/blog/amp-s-new-business-model-ad-supported-ai-coding/))
**Steal:** shareable thread permalinks with a full reasoning trace as the review artifact; per-thread cost display; the team feed as an organizational learning surface; naming a distinct "second opinion" model; honest documentation of subagent limitations.

### Charlie Labs
**Daemons**: recurring engineering roles defined in **`.agents/daemons/<id>/DAEMON.md`** with policy, routines, and activation rules, firing on GitHub events, schedules, or Slack/Linear activity. "Agents create work. Daemons do the rest" — PR monitoring, issue hygiene, CI repair, doc maintenance, dependency updates, error triage. **PR reviews run multiple independent passes in parallel, then deduplicate findings before posting one coherent GitHub review** — a direct structural answer to comment spam. Aggressiveness is tunable per daemon; daemons are aware of each other's activity to avoid conflicts. Dashboard covers GitHub App setup, repos, integrations, daemon config, and **activity tracking with filtering and date ranges**. Multi-repo changes with shared context; Playwright browser testing with visual verification; repo-owned instructions (`AGENTS.md`, `CHARLIE.md`, `.agents/`); durable task trees with parallel child work; **"Charlie Credits"** as a unified usage unit with daily/weekly resets. Output lands as PRs, issues, and notifications **in the tools you already use — no separate platform**. Free tier supports multiple concurrent daemons. ([changelog](https://docs.charlielabs.ai/changelog), [Product Hunt](https://www.producthunt.com/products/daemons-by-charlie-labs))
**Steal:** dedupe-then-post-once review; daemons as version-controlled, tunably-aggressive roles; artifact-linked updates that give "one clear next action."

### Tembo
Workspace dashboard with **Active Sessions** (running tasks with participant avatars and status), **Recent Activity** timeline of completed work/PRs/automations, **Agent Templates** for common jobs (code review, incident triage), and **team visibility** — "See what teammates are working on across the team. No more hiding on a developer's laptop." Triggers: Slack mentions, Linear issues, GitHub events, schedules, webhooks, Sentry. Isolated cloud VMs up to **128GB RAM / 500GB disk**, with foreground interactive work (Cursor, Claude Code, Codex) that **pauses and resumes**, and teammates can **inspect, join, or hand off a session in progress**. Explicitly harness- and model-agnostic: "the infrastructure layer for your favorite agents." Free to start. ([Tembo](https://www.tembo.io/), [Cerebral Valley](https://cerebralvalley.beehiiv.com/p/tembo-the-background-coding-agents-company))
**Steal:** participant avatars on running sessions (agent work as visible team activity, not private laptop work); join/hand-off mid-session; agent templates as a gallery.

---

## 10. What users actually complain about

### The measured review crisis
From an analysis of **33,707 agent-authored PRs across 2,807 repositories** ([arXiv 2601.00753](https://arxiv.org/html/2601.00753v1)):
- A **bimodal outcome distribution**: **28.3% of agent PRs are merged within one minute of creation** (i.e., not reviewed at all), while the rest fall into iterative cycles that often fail.
- **"Ghosting" rates** — the agent abandoning a rejected PR after receiving feedback: **Codex 10.0%, Copilot 2.3%, Claude 3.1%, Devin 0.9%**, overall 3.8%. Maintainers describe an **"attention tax"** from abandoned PRs.
- Instant-merge PRs are smaller (median 68 changes vs 104) and touch critical config far less (7.1% vs 18.4%) — reviewers are, informally, triaging by size.
- A "Circuit Breaker" triage model hits **AUC 0.957** predicting high-effort PRs *at creation time* using only **patch size, files touched, file types** — while semantic analysis of PR text fails badly (TF-IDF AUC 0.57). **At a 20% review budget, you capture 69% of total review effort.**

Corroborating numbers: review time up **441%**, zero-review merges up **31%**, AI-generated PRs waiting **4.6× longer** for review, agents generating **4× more code for ~10% productivity gain**, reviewers capped at ~200–400 lines/hour of meaningful review. ([Addy Osmani](https://addyosmani.com/blog/agentic-code-review/), [Vaughan](https://codex.danielvaughan.com/2026/05/24/human-review-bottleneck-code-review-strategies-agent-output/))

### Visibility complaints
1. **Reasoning is discarded before review.** The reviewer becomes "the first human to ever lay eyes on this code." Agents reason internally but don't capture decision-making in the PR, so humans **reconstruct intent from scratch — much harder than checking work you already understand.** Review shifted from *verifying reasoning* to *recovering missing intent*. (Osmani)
2. **Watching doesn't help if you can't intervene cheaply.** Answer.AI's Devin trial: the dashboard let them watch it pursue unproductive paths for hours; the async/autonomous model meant "hours of wasted effort before humans detected failures," with no way to incrementally nudge like Cursor.
3. **Unclear branch/session semantics.** Cursor users can't tell whether a follow-up modifies the existing branch or creates a new one. Copilot Workspace users had no affordance for "start a new task session" and silently degraded.
4. **Cold starts and dead time.** 30–90s Codex sandbox provisioning per task, with no useful UI during it (Claude Code's provisioning checklist + message queuing is the counterexample).

### Trust complaints
1. **Unpredictability beats incapability.** Answer.AI couldn't tell which tasks would succeed *even when they resembled earlier wins*. That's a trust problem no amount of log detail fixes.
2. **Confident hallucination about self.** Devin claiming it can analyze screenshots when it can't; hallucinating platform features that don't exist and spending days pursuing them.
3. **False progress.** Work that looks complete and does nothing (the DaisyUI theme case: customizations "were doing nothing").
4. **"Borrowed confidence."** "The system's certainty becomes yours, and nobody actually understood anything." (Osmani)
5. **Correlated failure between generator and reviewer.** When both reason from the same training data, "agents check code against itself rather than against intent."
6. **Induced demand.** From HN on Devin Review: making review easier means teams "end up with way more PRs to review" while trusting flawed AI assessments. Cognition's own response: "Devin Review is not supposed to replace your judgment... It just organizes the PR in a way that makes it way easier for YOU to understand." Some HN commenters argued the right investment is **not** AI review at all but "better diff tools, semantically grouped files, and better UI for large diffs."
7. **Delegation ceiling.** Anthropic's 2026 Agentic Coding Trends Report: engineers use AI in ~60% of their work but can **fully delegate only 0–20% of tasks**, and they delegate what is "easily verifiable" or "low-stakes." Verifiability, not capability, is the gate.

### Review-tool-specific noise complaints
- CodeRabbit's 8–20 comments/PR, trivial issues labeled "major," confident wrongness, multi-minute latency on 50+ file diffs → removed from merge-blocking.
- Greptile ~11 false positives vs CodeRabbit's 2; per-review pricing punishing high-velocity teams.
- Bugbot's $40/user/mo add-on and GitHub-only lock-in.
- Pricing model confusion generally: "The cheapest plan on a pricing page is no longer the real answer" — seat pricing overstates cost for teams where few people open PRs; usage pricing needs guardrails on which PRs trigger deep analysis and who can rerun.

### Recommended counter-patterns (converging across sources)
- **Tier by blast radius**: config = minimal review, payments path = full verification stack. Gate the riskiest 20%.
- **Predict effort at creation** from patch size / files / file types; fast-fail expensive PRs before humans sink hours.
- **Raise intake standards**: require a statement of purpose, bounded diff size, test output — push intent-reconstruction back to the submitter.
- **Enforce small diffs** (one concrete threshold in use: reject >250 changed lines). Large PRs get rubber-stamped or rejected, never reviewed.
- **Read test changes with suspicion** — agents rewrite assertions to match new broken behavior.
- **Humans own merges. AI reviews are sensors, not verdicts.**
- Governance infrastructure: stale-PR expiration (14 days), CI-pass requirements, per-agent calibration, SLAs (4h first response / 24h resolution).

---

## 11. The steal list, ranked

**Tier 1 — highest leverage, few competitors have them**
1. **Commit → session-log backlinks** (GitHub). Reasoning survives into review; answers "why is this line here" at review time. Directly attacks the #1 complaint.
2. **Demo artifacts on every agent PR** — screenshots, videos, logs of the agent verifying its own work (Cursor). Review by evidence, not by re-derivation.
3. **Hunk reordering + per-hunk explanation** (Devin Review). Reading order as a designed artifact instead of an alphabetical accident.
4. **Per-rule acceptance-rate analytics with dry-run mode** (Cursor Bugbot). Makes noise measurable and therefore fixable.
5. **Dedupe-before-posting: run N independent review passes, merge findings, post one review** (Charlie).
6. **P0/P1-only as a product default** (Codex). Restraint shipped as policy, not as a config the user must find.
7. **Inline diff comments that become the agent's next prompt** (Claude Code web). Review and steering are the same gesture.

**Tier 2 — strong, increasingly table-stakes**
8. Unified **step timeline that scopes every tool panel to the selected step** (Devin Progress tab), plus a Following toggle and scrubber.
9. **Queued steering** — guidance applies after the current action completes (GitHub) — or the explicit **pause → take over → resume with a change note** ritual (Devin).
10. **Countdown approval with a "wait for me" escape** (Devin) *plus* a **critic agent that reviews auto-approved plans** (Jules). Use both: the countdown handles the common case, the critic covers inattention.
11. **Session-row status labels and diff badges** — "PR created", "Awaiting instructions", `+42 -18`.
12. **Provisioning checklist that accepts queued input** (Claude Code CLI). Never show a blank spinner.
13. **Trigger-scoped knowledge items** rather than always-on context, with `!macro` shorthand (Devin) or keyword-triggered microagents (OpenHands).
14. **Version-controlled agent definitions** that auto-populate the agent picker (`.github/agents/`, `.agents/daemons/`, custom droids), including **tool restrictions that make a review agent structurally unable to write code** (Factory).
15. **Side-question panel** (`/btw`, "side chats") and **sticky user messages** — both cheap, both fix real long-session pain.
16. **Shareable session/thread links with zero-install viewing** for PMs and reviewers (Factory, Amp, Claude Code).
17. **Failure still produces an artifact** — the partial-progress branch (OpenHands resolver).

**Tier 3 — differentiators worth considering**
18. Remote desktop takeover to test the software without checking out (Cursor).
19. Same task, multiple vendors' agents, compare reasoning (Agent HQ).
20. Explicit commit-authorship modes: agent-only / co-authored / user-only (Jules).
21. Prompt critique in the input box before you send (Devin Coach).
22. Slack code-channels with status indicators, PR chips, and live worklogs (Devin).
23. Participant avatars on running sessions so agent work reads as team activity (Tembo).
24. Piped diffs (`codex cloud diff | ...`) so review isn't UI-locked.

**Explicit anti-patterns to avoid**
- Auto-approve on navigate-away with **no** critic behind it (Jules's original design).
- Ambiguous branch/session continuation semantics (Cursor).
- No "start a new task" affordance when the user has drifted off the original task (Copilot Workspace).
- Read-only editor in a workspace that invites intervention (Devin 1.x).
- Un-tunable comment volume (CodeRabbit).
- A "Trusted" network default whose allow-list is broad enough to exfiltrate (Claude Code web, per Willison).
- Marketing the agent as an engineer replacement — it produces durable negative brand value that outlives the product improvements.

---

## Sources

Devin/Cognition: [Interactive Planning](https://docs.devin.ai/work-with-devin/interactive-planning) · [Devin intro](https://docs.devin.ai/get-started/devin-intro) · [Release notes](https://docs.devin.ai/release-notes) · [Knowledge](https://docs.devin.ai/product-guides/knowledge) · [Playbooks](https://docs.devin.ai/product-guides/creating-playbooks) · [Slack](https://docs.devin.ai/integrations/slack) · [Devin 2.0](https://cognition.com/blog/devin-2) · [Devin Review](https://cognition.com/blog/devin-review) · [Autofix review comments](https://cognition.com/blog/closing-the-agent-loop-devin-autofixes-review-comments) · [DeepWiki](https://cognition.com/blog/deepwiki) · [deepwiki.com](https://deepwiki.com/) · [Devin IDE guide](https://fast.io/resources/devin-ide-guide/) · [ppaolo product analysis](https://ppaolo.substack.com/p/in-depth-product-analysis-devin-cognition-labs) · [How Cognition uses Devin](https://nader.substack.com/p/how-cognition-uses-devin-to-build) · [Devin 2.0 explained](https://www.analyticsvidhya.com/blog/2025/04/devin-2-0/) · [Answer.AI: a month with Devin](https://www.answer.ai/posts/2025-01-08-devin.html) · [HN: Devin Review](https://news.ycombinator.com/item?id=46711589) · [Devin pricing](https://costbench.com/software/ai-coding-assistants/devin-ai/)

Factory: [docs](https://docs.factory.ai/) · [changelog](https://docs.factory.ai/changelog/release-notes) · [Missions](https://factory.ai/news/missions) · [Web & mobile](https://factory.ai/product/web) · [Sid Bharath guide](https://sidbharath.com/blog/factory-ai-guide/) · [digitalapplied review](https://www.digitalapplied.com/blog/factory-ai-multi-agent-coding-platform-review) · [Fritz review](https://fritz.ai/factory-ai-review/)

OpenHands: [docs](https://docs.openhands.dev/overview/introduction.md) · [Agent Canvas + ACP](https://www.openhands.dev/blog/use-any-coding-agent-in-openhands-with-acp) · [GitHub resolver](https://www.openhands.dev/blog/open-source-coding-agents-in-your-github-fixing-your-issues) · [skills/microagents](https://github.com/OpenHands/OpenHands/blob/main/skills/README.md) · [GUI mode](https://docs.openhands.dev/openhands/usage/how-to/gui-mode) · [pricing](https://www.openhands.dev/pricing) · [AI Agent Index review](https://theaiagentindex.com/agents/openhands)

Jules: [review plan](https://jules.google/docs/review-plan/) · [docs](https://jules.google/docs) · [changelog](https://jules.google/docs/changelog/) · [Jules Tools changelog](https://jules.google/docs/changelog/2025-11-10/) · [usage limits](https://jules.google/docs/usage-limits/) · [MachineLearningMastery walkthrough](https://machinelearningmastery.com/practical-agentic-coding-with-google-jules/) · [DEV critique](https://dev.to/pinishv/google-jules-always-on-my-radar-but-never-quite-the-star-3gba)

Codex: [Introducing Codex](https://openai.com/index/introducing-codex/) · [Codex cloud docs](https://developers.openai.com/codex/cloud/) · [GitHub/code review docs](https://learn.chatgpt.com/docs/third-party/github) · [Slack-to-merge workflow](https://codex.danielvaughan.com/2026/04/08/codex-cloud-task-application/) · [Saffari review](https://omidsaffari.com/blog/codex-review) · [openaitoolshub review](https://www.openaitoolshub.org/en/blog/openai-codex-review)

Cursor: [Cursor 2.0](https://cursor.com/blog/2-0) · [Cloud agents docs](https://cursor.com/docs/cloud-agents) · [Background agent docs](https://cursor.com/docs/background-agent) · [Bugbot docs](https://cursor.com/docs/bugbot) · [Proser review](https://zackproser.com/blog/cursor-agents-review) · [Bugbot 2026 review](https://weavai.app/blog/en/2026/05/12/cursor-bugbot-2026-review-ai-bug-detection-autofix/)

GitHub: [Agent HQ](https://github.blog/news-insights/company-news/welcome-home-agents/) · [Pick your agent](https://github.blog/news-insights/company-news/pick-your-agent-use-claude-and-codex-on-agent-hq/) · [Mission control guide](https://github.blog/ai-and-ml/github-copilot/how-to-orchestrate-agents-using-mission-control/) · [About coding agent](https://docs.github.com/en/copilot/concepts/agents/coding-agent/about-coding-agent) · [Track sessions](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/track-copilot-sessions) · [Review Copilot PRs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/review-copilot-prs) · [Custom agents](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-custom-agents) · [Copilot Workspace feedback](https://github.com/orgs/community/discussions/145254) · [Workspace sunset](https://www.javacodegeeks.com/2026/02/github-copilot-workspace-the-agentic-era.html) · [TestingCatalog](https://www.testingcatalog.com/github-unveils-agent-hq-for-ai-coding-agents/)

Anthropic: [Claude Code on the web docs](https://code.claude.com/docs/en/claude-code-on-the-web) · [announcement](https://www.anthropic.com/news/claude-code-on-the-web) · [Simon Willison](https://simonwillison.net/2025/Oct/20/claude-code-for-web/) · [2026 Agentic Coding Trends Report](https://resources.anthropic.com/hubfs/2026%20Agentic%20Coding%20Trends%20Report.pdf)

Amp / Charlie / Tembo / Codegen / Sweep: [Amp manual](https://ampcode.com/manual) · [Amp Free](https://ampcode.com/news/amp-free) · [tessl on Amp ads](https://tessl.io/blog/amp-s-new-business-model-ad-supported-ai-coding/) · [Amp review](https://baeseokjae.github.io/posts/amp-code-review-2026/) · [Charlie changelog](https://docs.charlielabs.ai/changelog) · [Charlie recap](https://charlielabs.ai/blog/charlie-2025-a-recap-and-whats-next/) · [Daemons on Product Hunt](https://www.producthunt.com/products/daemons-by-charlie-labs) · [Tembo](https://www.tembo.io/) · [Cerebral Valley on Tembo](https://cerebralvalley.beehiiv.com/p/tembo-the-background-coding-agents-company) · [ClickUp acquires Codegen](https://clickup.com/blog/clickup-codegen-acquisition/) · [Sweep repo](https://github.com/sweepai/sweep)

Code review tier: [CodeRabbit review](https://curatorbits.com/reviews/coderabbit/) · [WeavAI CodeRabbit](https://weavai.app/blog/en/2026/04/29/2026-coderabbit-ai-review-is-the-top-pr-tool-worth-it/) · [WeavAI Greptile](https://weavai.app/blog/en/2026/05/12/greptile-2026-review-ai-code-review-pricing-debate/) · [Levelop comparison](https://levelop.dev/blog/best-ai-code-review-tools-2026-coderabbit-greptile-qodo-compared) · [Qodo platform](https://www.qodo.ai/formerly-qodo-merge/) · [Critique on pricing](https://www.critique.sh/blog/ai-code-review-pricing-2026)

Complaints & data: [Addy Osmani, Agentic Code Review](https://addyosmani.com/blog/agentic-code-review/) · [arXiv: Early-Stage Prediction of Review Effort in AI-Generated PRs](https://arxiv.org/html/2601.00753v1) · [Human review bottleneck](https://codex.danielvaughan.com/2026/05/24/human-review-bottleneck-code-review-strategies-agent-output/) · [What HN gets right about AI coding agents 2026](https://www.developersdigest.tech/blog/what-hacker-news-gets-right-about-ai-coding-agents-2026) · [Reddit agent debates, May 2026](https://dev.to/liv_melendez_4be3c47ea998/what-the-ai-agent-crowd-on-reddit-is-arguing-about-in-early-may-2026-4j7e)

---


# Part 2 — Multi-agent orchestrators, agent GUIs, and human-in-the-loop patterns

# Orchestrating Multiple Coding Agents: UI Landscape, Screen-by-Screen (August 2026)

## 0. The headline finding: the category consolidated, and Anthropic absorbed it

Four of the tools named in the brief are dead or transformed as of August 2026. This matters more than any individual feature comparison, because the *shape* of what survived tells you which UI ideas actually worked.

| Tool | Status (Aug 2026) | What happened |
|---|---|---|
| **Vibe Kanban** (BloopAI/bloop) | **Sunset 10 Apr 2026**; cloud off 30 days later; repo now community-maintained Apache-2.0 | "Thousands of daily users… the vast majority are free users and we couldn't find a business model." Local workspaces still run via `npx vibe-kanban`. ([shutdown post](https://www.vibekanban.com/blog/shutdown), [repo](https://github.com/BloopAI/vibe-kanban)) |
| **Terragon / Terry** | **Shut down**; OSS snapshot released Jan 2026 | Repo is now `terragon-labs/terragon-oss`, "as-is," unmaintained. ([repo](https://github.com/terragon-labs/terragon-oss)) |
| **Crystal** (stravu) | **Deprecated Feb 2026 → Nimbalyst** | Repo is a redirect. ([repo](https://github.com/stravu/crystal), [successor](https://nimbalyst.com/crystal/)) |
| **Omnara** | **Repo archived 2 Feb 2026** | Now self-describes as "the open-source alternative to Claude Managed Agents." ([repo](https://github.com/omnara-ai/omnara)) |
| **Conductor** | Alive, shipping (v0.29.0+) | Still the reference implementation of the three-pane parallel-workspace model. |
| **Sculptor** (Imbue) | Alive | Container-per-agent instead of worktree-per-agent. |

Meanwhile Anthropic shipped two things that eat most of this category's surface area: the **Claude Code desktop redesign (April 2026)** built entirely around a parallel-session sidebar with automatic git worktrees, and **Claude Managed Agents** (beta, `managed-agents-2026-04-01`), a hosted agent harness with sessions, events, and SSE steering.

The durable UI vocabulary that emerged across every survivor is remarkably consistent — I'll name it up front so the rest reads as variations on a theme:

> **sidebar of named workspaces → one isolated worktree/container each → three-pane detail (chat | diff | terminal) → status badge that says "needs you" → inline diff comments that become the next prompt → archive on merge.**

---

## Part A — Board/queue orchestrators for coding agents

### A1. Vibe Kanban (BloopAI) — the canonical kanban-for-agents

**The board.** Classic left-to-right column board, drag-and-drop via `@hello-pangea/dnd` with optimistic UI (card moves instantly, backend confirms async). Five status columns, and critically **the columns are driven by agent execution state, not by human intent**:

| Status | Column | Trigger |
|---|---|---|
| Created | **To do** | Task created without starting an agent |
| Agent executing | **In Progress** | Attempt started |
| Agent finished (pass *or* fail) | **In Review** | Process exited |
| Changes integrated locally | **Done** | Merge |
| GitHub PR merged | **Done** | PR webhook/poll |

Manual dragging between columns is permitted but **does not trigger any automation** — a deliberate decision that keeps the board a *view* of agent state rather than a control surface. ([Creating Tasks docs](https://www.vibekanban.com/docs/core-features/creating-tasks))

**Cards** carry a human-readable ID (`TASK-123`), summary, priority icon, assignee avatar, tag badges, and — the agent-specific bit — **an indicator for linked active workspaces**. A dense tabular **List view** exists as an alternative for large backlogs, plus multi-select with a bulk action bar for status/priority/assignee. ([DeepWiki: Kanban Board and Issue Management](https://deepwiki.com/BloopAI/vibe-kanban/5.6-kanban-board-and-issue-management))

**Creation.** `+` on the column header or the `c` keyboard shortcut opens a dialog with two buttons: **Create Task** (board only) and **Create & Start** (immediately spawns an agent with default settings). `@` in the description inserts reusable snippets ("task tags"). Markdown editing preserves code-block and image labels.

**Task Attempts — the most-copied idea in the whole category.** A task has a 1:N relationship with attempts. The task detail panel has an **Attempts section** with an attempt history and a `+` button; the triple-dot menu top-right offers **Create New Attempt**, which opens a dialog with three selectors:

- **Agent profile** (Claude Code, Codex, Gemini CLI, Copilot, Amp, Cursor, OpenCode, Droid, CCR, Qwen — 10+)
- **Variant** (profile sub-configuration, e.g. model/effort)
- **Base branch** (so a retry can start from a branch that has since moved)

Subtasks created under an attempt stay bound to that attempt's branch; new subtasks from a new attempt rebase onto the new attempt's branch. The point is **run the same task N ways and compare diffs side by side**. ([New Task Attempts docs](https://www.vibekanban.com/docs/core-features/new-task-attempts))

**Worktree surfacing.** On start, the Rust backend runs `git worktree add` into `.vibe-workspaces/{task_id}` on a branch named `vk/{short-hash}-{slug}` (e.g. `vk/3f20-generate-integra`) or `vibe/{task_id}` depending on version. The user sees this as: a branch name on the card, an **Open in IDE** button (works over SSH for remote), a per-workspace terminal, and a per-workspace dev server. `DISABLE_WORKTREE_CLEANUP` controls teardown. ([Starlog teardown of the worktree strategy](https://starlog.is/articles/ai-dev-tools/bloopai-vibe-kanban/), [VirtusLab writeup](https://virtuslab.com/blog/ai/vibe-kanban))

**Monitoring pane.** Real-time WebSocket log stream showing, as distinct rendered element types: agent reasoning, shell commands, **file modifications with expandable details**, tool usage/API calls, and status messages. A **View Processes** menu item opens a list of *all* running and completed processes for the task — agent sessions, dev servers, build scripts — each with status and execution timeline. This "processes" abstraction is unusual and good: it makes the dev server a first-class citizen alongside the agent. ([Monitoring Task Execution docs](https://vibekanban.com/docs/core-features/monitoring-task-execution))

**Approval gates.** When the agent hits an action needing human judgement, **an approval row appears inline in the log stream with tick and cross buttons**. Not a modal, not a separate inbox — an inline row in the transcript.

**Talking to a running agent.** A follow-up message field under the log with `Cmd/Ctrl+Enter` to send, `Enter` for newline. Some agents support editing a previously sent message. A **Stop** button halts execution at any moment.

**Diff review.** File-watcher on each worktree streams changes over WebSocket to the React frontend. Line-by-line diff view; **inline comments on changed lines are sent directly back to the agent as feedback** without leaving the UI. That loop — assign on board → agent works in isolated worktree → review diff → comment → agent revises — is the product's core.

**Preview.** An eye icon opens an embedded browser pane with devtools, inspect mode, and device emulation. The dev-server script (`npm run dev`) is configured per project; VK auto-detects the localhost URL from stdout/stderr (ports 3000/3001/5173 etc.), rewrites `0.0.0.0`/`::` to `localhost` for embedding, and shows live dev-server logs in a strip at the bottom of the preview. With the Web Companion package installed, clicking a DOM element reveals its hierarchy and feeds a targeted selector to the agent. ([Testing docs](https://vibekanban.com/docs/core-features/testing-your-application))

**Integration.** AI-generated PR descriptions, one-click merge, background PR status polling. MCP server built in (`MCP_HOST`/`MCP_PORT`), tunnel mode (`VK_TUNNEL`), reverse-proxy origin validation (`VK_ALLOWED_ORIGINS`). Stack: Rust 50% / TypeScript 46%.

---

### A2. Conductor (conductor.build) — the three-pane parallel workspace, done best

Native macOS app. **The layout is three vertical panes:**

- **Left sidebar** — the workspace list. Workspaces get **auto-assigned city names** (Raleigh, Washington, Yokohama…), which reviewers universally cite as the thing that makes six concurrent agents navigable: *"you appreciate it when you have six open at 2am."* **Badges on sidebar rows indicate which workspaces need attention.** Below is a **History pane** for archived workspaces, restorable later.
- **Center** — a chat pane that is *identical to Claude Code*: `@file` tagging, slash commands, the same transcript. This is deliberate: no new mental model for the agent conversation itself.
- **Right** — split between a **live git diff viewer** that updates as the agent writes, and an **integrated terminal** scoped to that workspace.

([George Taskos' 5-parallel-sessions writeup](https://georgetaskos.medium.com/scaling-the-loop-run-5-claude-code-sessions-in-parallel-with-conductor-build-539b52888a81), [Julian Astrada](https://julianastrada.com/blog/conductor-parallel-agents/))

**The model.** *"The workspace is the unit of delegation. The branch and pull request are the unit of integration."* Each workspace = its own git worktree, branch, files, terminal, dev server, and diff. Supports Claude Code, Codex, Cursor, and OpenCode in parallel. ([Workflow concepts](https://www.conductor.build/docs/concepts/workflow))

**Five lifecycle phases**, each with its own keyboard shortcut:

1. **Creation** — `⌘⇧N` or the new-workspace button. One workspace per shippable unit.
2. **Development** — agent works; user has terminal, chat, attachments.
3. **Verification** — terminal, or a **Run button** that executes the project's run script. `CONDUCTOR_PORT` gives each workspace a distinct port range so five dev servers coexist.
4. **Review & refinement** — **`⌘⇧D` opens the Diff Viewer**.
5. **Integration** — **`⌘⇧P` creates a PR**; watch Checks; merge; archive.

**Diff Viewer specifics.** Inspect file changes; **leave comments on changed lines**; those inline comments become **attachments in the chat composer** — an elegant mechanic that makes "review feedback" and "next prompt" literally the same object. Also: send feedback to agents, resolve stale GitHub review threads, and **revert individual files**.

**Review action.** A **Review changes** button asks the agent to inspect its own diff. Since v0.29.0, Claude has a dedicated in-process MCP tool (built with `createSdkMcpServer` from the Claude Code SDK) to read the full workspace diff *including uncommitted changes* — solving the "there's no single git command for the full diff" problem — plus per-file diffs and `git diff --stat`-style summaries. Crucially it can **comment directly on specific lines so AI feedback renders inline with the code**, replacing the old pattern of Claude emitting a markdown table you had to cross-reference manually. Repository Settings has a **Code review preferences** field for repo-specific review guidance. ([Diff tools blog](https://www.conductor.build/blog/diff-tools))

**Checks tab.** A single merge-readiness surface consolidating: git status, PR metadata, CI and status checks, deployments, GitHub and review comments, and todos. House guidance: *"treat unresolved comments, failing checks, and open todos as blockers."*

**Other affordances.** `⌘O` opens a file in your real IDE for a hand edit. Claude is auto-notified on merge conflicts, with a `/resolve-merge-conflicts` command. Archiving removes the worktree and the city name from the sidebar. ([Review and merge guide](https://www.conductor.build/docs/guides/review-and-merge))

---

### A3. Terragon / Terry — cloud background agents (defunct, but the shape persists)

Web dashboard + `terry` CLI + GitHub comments + mobile. Each task got **an isolated sandbox container with its own copy of the repo**; agents read files, edit, and run tests without touching other concurrent tasks. Every task got a unique branch; work was checkpointed and pushed as a PR with AI-generated commits.

Notable UI/entry-point ideas worth stealing:
- **Task creation from four surfaces**: Slack, GitHub `@`-mention, CLI, web.
- **Recurring tasks and event-triggered workflows** (on issue, on PR) — i.e. cron and webhooks as first-class task sources.
- **Progress streams to the browser in real time**, with a completion notification.
- **Local handoff**: `terry` pulls a cloud task down to your machine to take over, plus MCP integration so Cursor can drive it.

([terragon-oss](https://github.com/terragon-labs/terragon-oss); [The Tool Nerd comparison of Terragon/Conductor/Cursor](https://www.thetoolnerd.com/p/era-of-virtual-employees-running))

The "delegate → sandbox → PR → notification → optionally teleport local" loop is exactly what Claude Code on the web ships today.

---

### A4. Sculptor (Imbue) — containers instead of worktrees, plus Pairing Mode

Sculptor's thesis is that **git worktrees are the wrong isolation primitive** because agents also need to *run* code — install dependencies, start servers, execute tests — and worktrees share your machine. So each agent gets **its own Docker container**, built from your repo's standard `.devcontainer/Dockerfile` spec.

**UI.** "Every active agent is visible in Sculptor." Each agent workspace has **its own worktree, branch, terminal, and diff view**, and you **review and merge all from the same surface**. Agents interact with a *local git remote*, so your normal `git fetch && git checkout` works against agent branches. ([product page](https://imbue.com/product/sculptor), [announcement](https://imbue.com/blog/sculptor-announce))

**Pairing Mode** — the differentiating interaction. One click **syncs a container's state directly into your local repo/IDE**, so you can inspect and edit an agent's work in your real editor without the agent ever touching your machine, then hand it back. Reviewers specifically praise the resulting clarity of *"agent output vs. my input."*

**Merge review.** Visual merge workflow: keep what you want, drop what you don't; **Sculptor flags potential merge conflicts automatically** and lets you hand a conflict back to an agent to resolve.

**Session persistence.** Every session is retained "with its plans, chats, tool calls, and code changes all intact" — reopen any past session without losing context.

**Suggestions (beta).** A second reviewing pass that reads the agent's code and **points out fixes to the agent** so you merge with more confidence — i.e. an automated reviewer sitting between "agent done" and "human reviews."

**Container startup UX.** Dependencies are pre-installed into the cached devcontainer image, cutting agent start time "from minutes to seconds" — a real UI concern, because the perceived cost of spinning up agent #6 determines whether people actually parallelize. ([containers post](https://imbue.com/blog/containers))

---

### A5. Crystal → Nimbalyst

**Crystal (deprecated Feb 2026)** was the Electron desktop app that established the pattern: sidebar of sessions, each in its own git worktree, resume any session with full conversation history, **rebase/squash/inspect git state without leaving the app**, review diffs and run build/test scripts per session, and — its signature — **run multiple sessions from one prompt to compare approaches**.

**Nimbalyst** (successor) adds a visual layer on top and is one of the few tools that went *toward* kanban rather than away:
- **Session kanban board with backlog → complete phases**
- Three workspace modes: **Files Mode** (three panels — file tree left, WYSIWYG editor center, agent panel right; supports markdown docs, Calc Sheets, mermaid, Excalidraw, HTML mockups, data models, CSV), **Agent Mode** (session management, file tracking, diff visualization, code editing), **Task Mode** (AI tracks and updates status/bugs/todos/decisions directly in markdown)
- **Inline AI diffs you can approve or reject in any file**
- **iOS companion app for remote session monitoring**
- Claude Code *and* Codex simultaneously

([Crystal repo](https://github.com/stravu/crystal), [Nimbalyst/Crystal page](https://nimbalyst.com/crystal/), [docs](https://docs.nimbalyst.com/))

---

### A6. Claudia (getAsterisk) — GUI *for* Claude Code, not multi-agent orchestration

Tauri desktop app. Its contribution is a different set of screens:

- **Visual Project Browser** over `~/.claude/projects/`, with search
- **Session History** showing first message, timestamp, and metadata at a glance — resume with full context
- **CC Agents**: create named agents with custom system prompts; an **Agent Library**; **Execution History** with logs and performance metrics; run agents as secure background processes
- **Visual Timeline / Checkpoints**: a *branching* timeline of the session. Jump back to any checkpoint in one click. A **Diff Viewer shows exactly what changed between checkpoints** — this is the best "time travel" UI in the Claude Code ecosystem
- **Usage Dashboard**: charts of usage trends, breakdown by model/project/time, CSV export for accounting
- **MCP Manager**: server registry, config forms, connection testing before deployment
- **Sandbox/permissions**: configure file read/write and network access per agent

([repo mirror](https://github.com/tdmatheus/claudia), [ClaudeLog entry](https://claudelog.com/claude-code-mcps/claudia/))

---

### A7. Claude Squad — the TUI version

Go TUI over **tmux** (one session per agent) + **git worktrees** (one branch per agent). The screen is a left instance list, a right pane with two tabs, and a bottom key menu.

- Navigate instances: `↑/j`, `↓/k`
- `n` new session · `N` new session with prompt · `D` kill · `?` help
- `↵`/`o` **attach** to the session to reprompt · `ctrl-q` **detach** back to the list
- `tab` switches the right pane between **Preview** (live terminal output) and **Diff**; `shift-↑/↓` scrolls the diff
- `s` commit and push branch · `c` checkout (commits changes and **pauses** the session) · `r` resume a paused session
- **yolo / auto-accept mode** for background running

The pause/resume/checkout triad is the most interesting bit: "checkout" simultaneously commits, pauses the agent, and hands you the branch — an explicit *takeover* affordance. ([README](https://github.com/smtg-ai/claude-squad/blob/main/README.md))

---

### A8. Omnara — the phone as the agent inbox (archived Feb 2026)

Omnara mirrored a normal terminal Claude Code session to a **web dashboard + iOS + Android app**, with an **Agent Activity Feed** as the main screen. Two modes: mirrored (work in terminal, watch on phone) and **Headless Mode — Dashboard-Only Interaction**, where the phone/web is the only surface. It shipped an **n8n "Human in the Loop" node** for real-time human-AI collaboration inside workflows. ([repo](https://github.com/omnara-ai/omnara))

Its spiritual successor in this space is **Moshi**, which has the most fully-specified agent-inbox UI I found:

- **Inbox**: **one active row per agent session**; new events *update the existing row* rather than stacking duplicates — the single most important design decision for agent notification UX. **Approval requests auto-sort to the top.** Rows grouped by project and host so you can tell your Mac from your homelab box from a cloud VM.
- **Live Activities**: active agent turns on the iOS lock screen and Dynamic Island. Approval and error events fire visible pushes; quieter events silently update only the inbox row and Live Activity.
- **Apple Watch**: inbox, usage rings, complications. Approve/deny/answer from the wrist. **Deliberately excludes** shell, tmux attach, dictation, image paste, and scrollback — "fast triage and small decisions" only.
- **Usage tab**: Claude Code's 5-hour and 7-day rate-limit windows as ring progress per account card (agent name, account label, host, refresh timestamp); context-remaining as a small ring in the inbox and on the Watch.

([Moshi agent hooks writeup](https://getmoshi.app/articles/agent-hooks-live-activities-usage))

Also in this niche: **Happy** (`slopus/happy`), a mobile+web client for Claude Code and Codex with realtime voice and E2E encryption ([repo](https://github.com/slopus/happy)), and **claude-push**, a minimal hook that pushes Claude Code permission requests to ntfy.sh ([repo](https://github.com/coa00/claude-push)).

---

### A9. async-code (ObservedObserver) — the Codex-style clone

Next.js/TS/Tailwind web UI, "Codex-style": a task list where you submit multiple coding jobs that run concurrently, **each in an isolated Docker sandbox**. Its distinguishing screen is **side-by-side output from different agents on the same task** for comparison/eval, with a visual diff review before you accept. Git integration handles clone, commit, and PR creation from successful outputs. ([repo](https://github.com/ObservedObserver/async-code))

---

### A10. Backlog.md — markdown-native board that agents can write to

Not an agent runner; a **task substrate** agents and humans share. Tasks are individual `.md` files in a project-local `backlog/` folder, version-controlled and human-readable. Fields: task ID (configurable prefix, `BACK-1`/`TASK-1`), description, **acceptance criteria**, **Definition of Done checklist**, milestones, dependencies, status, comments.

Three surfaces:
1. **`backlog board`** — a live kanban rendered in the terminal, columns by status. `backlog board export` produces a markdown board for non-technical stakeholders.
2. **`backlog browser`** — a browser dashboard with drag-and-drop kanban, task forms, real-time sync back to the markdown files, responsive on mobile, visual acceptance-criteria management.
3. **CLI**: `init`, `task create/edit/list`, `search` (fuzzy), `milestone`.

Its explicit HITL thesis is a **three-checkpoint review process**, which is the clearest statement of the "review upstream of code" idea I found: **Spec Review** (agent decomposes requirements into tasks with acceptance criteria *before* coding) → **Plan Review** (agent researches the codebase and writes an implementation strategy into the task for human approval) → **Code Review** (one task = one PR, keeping diffs readable). The framing: *"AI agents can now produce more plausible code in an hour than you can carefully read in a day."* Zero config, no server, no account, no telemetry. ([repo](https://github.com/MrLesk/Backlog.md))

---

### A11. Task Master AI (eyaltoledano/claude-task-master) — no GUI, MCP-native

Deliberately **has no kanban board**. It lives inside the editor chat (Cursor, Windsurf, VS Code) via an **MCP server exposing 36 tools** (reducible to 7–15 for token economy) plus a CLI. Tasks are JSON/markdown with ID, complexity rating, blocking/blocked-by dependencies, tags/workstreams, subtask hierarchies, and status (backlog / in-progress / in-review / done).

Workflow: write `.taskmaster/docs/prd.txt` → `task-master parse-prd` decomposes it into tasks with complexity analysis → `task-master next` picks the next unblocked task → agent implements. `task-master move` handles cross-tag moves with dependency fixups. A `research` command lets the agent pull fresh external information *with project context* before recommending an approach. Multi-provider (Anthropic, OpenAI, Gemini, Perplexity, xAI, OpenRouter, Mistral, Groq) with primary/research/fallback model roles. ([repo](https://github.com/eyaltoledano/claude-task-master))

**Contrast worth noting:** Backlog.md and Task Master are the two poles of "task queue for agents" — Backlog.md gives humans a visual board and lets agents write to files; Task Master gives agents an API and lets humans read the chat.

---

### A12. "Sirvine"

I could not find any product, repo, or company by this name in the coding-agent space. Searches returned only a GitHub user (`sirvine` / Sol Irvine), the unrelated UK firm Surevine, and Sircon/Sirion. **Likely a garbled name** — the nearest real candidates are **Sculptor**, **Sirvine→Sirvine?**, or possibly **`sortie`** (turns tracker tickets into agent sessions) or **`supacode`** (native macOS command center for worktree-per-agent development), both of which appear in the orchestrator census below. Flagging rather than inventing.

---

### A13. Mission-control / fleet dashboards

**Claude Code Agent Monitor** (hoangsonww) is the most concretely specified of these and worth reading as a spec:

- **Two kanban views**, toggled and persisted in localStorage. **Agents view**: Working / Waiting / Completed / Error. **Sessions view** adds Abandoned (5 columns).
- **The Waiting column is the product.** Hovering a Waiting badge tells you *why*: **Needs input / Turn done / At prompt / Interrupted**. That four-way disambiguation of "idle" is the single most useful micro-detail in this whole report — "waiting" is not one state.
- Cards show model, cost, and **current tool being used**. Column headers carry tooltips explaining lifecycle transitions. Client-side pagination at 10 cards/column with "Show more."
- **WebSocket push, no polling**, with subscriptions scoped to the active view so off-view updates don't refetch.
- **Session Detail**: top-tool usage as horizontal bars; **tool-aware renderers** — Bash renders as terminal output, file edits as side-by-side diffs, code as syntax-highlighted blocks with line numbers.
- **Subagent orchestration**: expandable tree of the subagent hierarchy; a Workflows section with **agent orchestration DAGs, tool-execution Sankey diagrams, and collaboration networks**, reconstructed from on-disk journals with agent count / tokens / tool calls per agent.
- **Analytics**: tokens by model, tool frequency distribution, activity heatmap (day-of-week, Sunday-aligned), live/offline indicator, skeleton placeholders that mirror chart layout to prevent flash.
- **A waiting-for-input banner** names the reason and elapsed time. Alerts engine with 14 webhook providers and inactivity detection.

([repo](https://github.com/hoangsonww/Claude-Code-Agent-Monitor))

Others in the "mission control" bucket ([survey](https://www.howtodeploy.app/blog/ai-agent-mission-control)):
- **OpenClaw Mission Control** (abhi1693) — board + org hierarchy tree; *"sensitive agent actions can be routed through explicit human review"*; multi-gateway.
- **Mission Control** (Builderz Labs) — 26-panel dashboard, GitHub sync, task tracking, token monitoring, cron, webhooks; WebSocket + SSE throughout. ([mc.builderz.dev](https://mc.builderz.dev/))
- **ClawDeck** — pure kanban; *"tasks move across columns as agents pick them up and complete them, with live updates."*
- **Ralph TUI** — agent-loop orchestrator; shows exactly which step the agent is on; **pause, resume, or kill a specific task without nuking the whole session**; writes state to `.ralph-tui/session.json` so it survives a crash. ([guide](https://www.verdent.ai/guides/ralph-tui-ai-agent-dashboard))

**cmux** — a native macOS terminal purpose-built for parallel agents, and the best example of *ambient* attention-routing:
- **Vertical tab sidebar**, each row showing git branch, linked PR status, working directory, **listening ports**, and the latest notification text for that workspace.
- Split panes: `⌘D` right, `⌘⇧D` down. Sidebar toggle `⌘B`, rename `⌘⇧R`.
- **When an agent needs input, its pane gets a blue visual ring and the sidebar tab lights up.** `⌘⇧U` jumps to the most recent unread agent.
- **`⌘I` opens a notification panel aggregating alerts across all agents.**
- A split browser pane with a scriptable API agents can drive to verify UI changes.

([DEV writeup](https://dev.to/arshtechpro/cmux-the-native-macos-terminal-built-for-running-ai-coding-agents-in-parallel-52il), [cmux.com](https://cmux.com/))

**The census.** [`andyrewlee/awesome-agent-orchestrators`](https://github.com/andyrewlee/awesome-agent-orchestrators) catalogues ~200 tools in six buckets. Notable GUI/board entries not covered above: **automaker** (kanban where agents implement features in isolated worktrees), **kandev** (kanban workbench assigning a different agent per workflow step), **dorothy** (orchestration + automations + kanban), **octomux** (local dashboard with **kanban fleet view and a unified permission inbox** — the exact pattern this brief asks about), **Fusion** (multi-node orchestrator with kanban, **plan-review gates**, hierarchical missions), **agent-kanban** (leader–worker task board with cryptographic agent identity), **Garcon** (self-hosted, diff review + **mobile approvals**), **collaborator** (terminals as tiles on an infinite pan-and-zoom canvas), **vibecraft** (RTS-style command interface for agents), **GraphCode** (agent sessions wired into graphs with live terminals), **clave** (macOS split/grid layouts with session groups), **codecast** (watches real sessions and surfaces them in a **live triage inbox**), **Ouijit** (kanban + terminals wired by lifecycle hooks).

---

## Part B — First-party GUIs for coding agents

### B1. Claude Code Desktop, April 2026 redesign — the sidebar became the product

This is the most important single artifact for the brief, because Anthropic rebuilt its own app around exactly the pattern the third-party tools invented. ([MacRumors](https://www.macrumors.com/2026/04/15/anthropic-rebuilds-claude-code-desktop-app/), [detailed guide](https://miraflow.ai/blog/claude-code-desktop-redesign-parallel-sessions-routines-workspace-guide))

**The sidebar.** All active and recent sessions in one place. **Filter by status, project, or environment; group by project.** `Ctrl+Tab` / `Ctrl+⇧+Tab` to move between sessions. `+ New session` or `⌘N`/`Ctrl+N`. No hard cap on concurrency — the limits are token budget and machine resources. **Sessions auto-archive when their PR merges or closes**, so the list stays clean without manual gardening. Hover actions on a sidebar row archive a session (and clean up its worktree). The sidebar also carries a **personal activity dashboard** modeled on GitHub contribution graphs: total sessions, message counts, token consumption, active streaks, usage patterns.

**Worktrees, automatic and invisible.** In a git repo, **every session automatically gets its own worktree** at `<project-root>/.claude/worktrees/`, with configurable location and branch prefix. *"Changes in one session do not affect other sessions until you commit them."* A `.worktreeinclude` file lets you pull gitignored files (e.g. `.env`) into new worktrees — the standard pain point, solved.

**Four drag-and-drop panes**, each independently movable:
- **Terminal** (`Ctrl+` `` ` ``) — shares the session's environment and working directory, so `npm test` runs against the same files Claude is editing.
- **File editor** — click a file path in chat or a diff to open it; renders HTML, PDF, and images; save with disk-change detection warnings.
- **Preview** — embedded browser for frontend apps plus server log viewing; Claude typically starts the dev server automatically after editing project files.
- **Diff viewer** — rebuilt for large changesets; unified view (split view not yet available).

**Three transcript verbosity modes** (`Ctrl+O` or the Transcript dropdown, switchable live without restarting) — this is the direct answer to "how do you show tool calls to a semi-technical user":
- **Verbose** — every tool call and every argument (classic CLI output)
- **Normal** — essentials, no extraneous detail
- **Summary** — final results only

The stated design rationale is exactly right: *"View mode selection dramatically affects cognitive overhead when orchestrating four or more parallel operations."* The verbosity control is a **fleet-management** control, not a debugging control.

**Side chat** (`⌘;` / `Ctrl+;`) — branch a clarifying question off the main thread; *"nothing you say in side chat gets added back to the main thread."* Ask "wait, what does this module do?" mid-run without polluting the agent's context. (Devin has the identical feature, `/btw`.)

**PR monitoring.** After PR creation, a **CI status bar** polls checks via `gh`, with two toggles: **Auto-fix** (Claude reads failure output and iterates) and **Auto-merge** (squash on green). Desktop notification on CI completion.

**Routines** — saved Claude Code configurations (prompt + repos + connectors) that run on Anthropic-managed cloud infra while your laptop is closed. Triggers: **scheduled** (hourly/nightly/weekly), **API** (HTTP POST to a per-routine endpoint with a bearer token), **GitHub** (PRs, releases). Daily limits by plan: Pro 5/day, Max 15/day, Team/Enterprise 25/day.

**Ultraplan** — planning offloaded to a cloud session with a review surface richer than terminal text: **comment on specific plan sections, emoji reactions to flag, a structured outline sidebar for navigation.** Two approval paths: implement in the browser (web diff review + PR), or **teleport the plan back to the local CLI**. Invoked by `/ultraplan`, by saying "ultraplan," or via a confirmation dialog.

**Known 1.0 gaps:** no multi-window (single window, panes only), no native pair-programming view, no mobile companion, layouts not always persistent across updates; some reported instability.

### B2. Claude Code on the web (claude.ai/code)

Research preview for Pro/Max/Team and premium Enterprise seats. ([docs](https://code.claude.com/docs/en/claude-code-on-the-web))

- **Sessions in a left sidebar.** Hover a row → archive icon. Filter for archived → hover → delete icon. Session menu → Delete, or **Open in > Terminal** (copies a `--teleport` command).
- **Diff indicator per session** rendered as `+42 -18`. Click to open the diff, **leave inline comments on specific lines, and send them to Claude with your next message.** Diffs are computed from raw git blob content (so repo `textconv`/diff drivers don't apply).
- **Cloud environments** are a saved config object (network access level, env vars, setup scripts), shared across web, terminal `--cloud`, Claude Tag, routines, mobile, and Desktop. Onboarding creates a **Default** environment with **Trusted** network access.
- **The provisioning UI is a live checklist**: while the container starts, the CLI shows setup steps (cloning the repo, running your setup script) ticking off, and **queues messages you type during provisioning**, sending them once ready. This is a small thing that matters enormously — it converts dead time into a legible progress surface.
- **Steering a running cloud agent from anywhere:** `claude -p "your message" --cloud <session-id>` queues a message and exits (works from any machine you're logged in on, any shell); `claude --cloud <session-id>` without `-p` **attaches your terminal to the running session** (gradual rollout). `/tasks` in the CLI lists background sessions; press `t` to teleport into one.
- **Teleport** (`--teleport`, `/teleport`, `/tp`) pulls a cloud session down: verifies you're in the right repo, prompts to stash uncommitted changes, fetches and checks out the branch, loads the full conversation history. **The terminal then gets its own copy** — new local work doesn't flow back to the web session. To keep phone steering after teleporting, start `/remote-control`.
- **If Claude asks a question and the session sits idle, you can answer whenever you come back** (up to environment expiry) and it resumes from your answer. Expired sessions are **marked expired in the session list** and reopening provisions a fresh VM with history restored.
- **Sharing**: Private/Team (Enterprise, Team) or Private/Public (Max, Pro), with optional repository-access verification of recipients. Recipients see latest state on open; no realtime co-viewing.
- **Auto-fix PRs**: toggle in the CI status bar. Claude subscribes to GitHub events; for **clear fixes** it pushes and explains in-session; for **ambiguous requests** (multiple valid readings, or architecturally significant) **it asks you before acting**; duplicates it notes and skips. Replies to review threads post under your GitHub account but are **labeled as coming from Claude Code**.
- **Isolation surfaced to the user**: per-session isolated VM; network limited by default; **git credentials and signing keys are never inside the sandbox** — auth goes through a proxy with scoped credentials.

### B3. Cursor 3 — Agents Window

`⌘⇧P → Open Agents Window` opens a dedicated window (return via *Open Editor Window*). Three regions: **left sidebar** listing agent workspaces and runs (each workspace independently indexed), **center prompt area**, **right inspection panel** containing file browser, sandbox terminal, Cursor browser, and review pane.

- **Agent Tabs** inside the window show multiple chats **side-by-side or in a grid**.
- Runs carry **running / waiting / completed** states. Open a run to read its diff; **search transcripts from the command palette**.
- **`/worktree`** creates a separate git worktree so an agent's changes are isolated.
- Current git branch shown top-right and in the review tab. Before committing, edits appear in a **dedicated review pane**; an **arrow button next to commit-and-push** lets you branch/redirect from the review interface.
- **Cloud runs execute in parallel and remain visible and controllable from the desktop window**, plus Slack/GitHub/Linear.
- `⌘P` and `⌘⇧F` still work without leaving agent mode.
- **Design Mode** (`⌘⇧D`) gives the agent visual browser context — element identification, layout, interaction state — without a screenshot-and-describe round trip.

([Learn Cursor](https://www.learncursor.dev/learn/cursor-agents/agents-window), [AgentPatterns](https://www.agentpatterns.ai/tools/cursor/agents-window/))

### B4. OpenAI Codex cloud

Delegate tasks that run in the background, **including in parallel**, in Codex's own cloud environment. Environment configuration = repo + setup steps + tools, plus an explicit **internet-access toggle** for cloud environments. Entry points: web, `@codex` on GitHub issues/PRs, and IDE ("kick off a cloud task from your editor, then monitor progress and **apply the resulting diffs locally**"). Output path is task → review → PR. ([docs](https://developers.openai.com/codex/cloud))

### B5. Devin (Cognition)

The richest "agent workspace" UI, and the origin of several patterns:

- **Progress tab** — a unified chronological view of *all* shell commands, code edits, and browser activity. **Clicking any step in the session, or the Progress tab, drills into that step's detail.** This is the step-timeline pattern in its purest form.
- **Desktop tab** (formerly "Browser") — Devin's interactive browser and desktop for frontend testing and auth flows.
- **IDE** — a full VS Code environment showing code changes in real time, which **you can take over** and edit yourself (with `⌘K`).
- **Shell** — command history with output previews.
- **Side Chats** — asynchronous questions that don't interrupt the main work. Started from the hover menu on **any message**, from the add-tab menu, or by typing **`/btw your question`**. Side chats are **read-only**: Devin can research but cannot modify files inside one.
- **Taking over** — *stop the session* to pause Devin, then use the IDE and terminal yourself.

([Devin session tools docs](https://docs.devin.ai/work-with-devin/devin-session-tools))

### B6. Google Jules

Deliberately **thin UI**. Submit via the Jules web UI, a GitHub issue mention, or the API; Jules works in a cloud VM and delivers **a pull request against a new branch with summary, diff, and test output**; you review it on GitHub like any other contribution, with notifications when PRs are ready. There is no meaningful dashboard, plan-approval gate, activity feed, or interrupt affordance — GitHub *is* the review surface. ([guide](https://www.digitalapplied.com/blog/google-jules-gemini-async-coding-agent-guide))

### B7. Amp (Sourcegraph)

Unit of work is the **thread**: "a thread records the prompt, model work, tool calls, outputs, and changes associated with a task." Threads are portable and durable — **share with a teammate, reopen later, move between terminal and the ampcode.com web interface**, control from mobile. **Orbs** are remote managed machines where threads keep running after you close your laptop; you can also register your own machines as runners via `amp --no-tui`. Subagents auto-spawn for isolated/parallel work; for reviews, *"Amp can launch a separate subagent for each check"* and combine results. `Ctrl+S` opens a **capability dial** from `low` to `ultra` — intensity, not model names. ([guide](https://sidbharath.com/blog/amp-code-guide/))

### B8. Claude Managed Agents (Anthropic, beta)

Not a GUI but the API substrate that GUIs are being built on. Four objects: **Agent** (model + system prompt + tools + MCP servers + skills), **Environment** (Anthropic cloud sandbox or self-hosted), **Session** (a running instance doing a task), **Events** (user turns, tool results, status updates). Sessions are **stateful, long-running, resume cleanly after pauses**, and store conversation history, sandbox state, and outputs server-side; full event history is fetchable. Responses stream via **SSE**. Step 5 of the documented workflow is literally *"Steer or interrupt — send additional user events to guide mid-execution or change direction."* Not currently eligible for ZDR or HIPAA BAA. ([overview](https://platform.claude.com/docs/en/managed-agents/overview))

---

## Part C — Multi-agent orchestration frameworks with UIs

### C1. CrewAI — Crew Studio (AMP)

**Three synchronized panels:**
- **Left — "AI Thoughts"**: the streaming reasoning of the assistant *as it designs the workflow*. (Note: the transparency surface here is about the *builder*, not the runtime.)
- **Center — Visual Canvas**: agents and tasks as connected nodes; AI-generated or hand-adjusted.
- **Right — Resources**: drag-and-drop palette of agents, tasks, tools.

Creation is conversational: you describe requirements, the assistant asks clarifying questions about agent roles and task definitions, then emits a structured `Crew` config. LLM Connections are set up separately in the AMP dashboard. **Execution view** = chronological event timeline + detailed logs with drill-in on agent messages and data payloads. Publish targets: AMP, downloadable Python source, **React component export**, or **MCP server export**. Published automations get a "Chat with this Crew" tester, integrated tracing, and REST API access. ([DeepWiki: Crew Studio](https://deepwiki.com/crewAIInc/crewAI/10.5-crew-studio), [Traces docs](https://docs.crewai.com/en/enterprise/features/traces))

### C2. LangGraph Studio + LangSmith + Agent Inbox

The most developed HITL UI stack in the ecosystem, and the one worth studying hardest.

**LangGraph Studio, Graph mode.** On an interrupt, the graph visualization **highlights the current execution point (which node triggered the interrupt)**, and visually distinguishes **nodes already executed** from the **remaining execution path**. You can **view the complete agent state at the interrupt point and edit state values directly in the Studio interface** — correct misclassified data, add missing context, override a decision — then resume with the modified state. Edits become part of the thread's execution history, so it's fully traceable. **Interrupted threads persist indefinitely** until resumed or cancelled, enabling genuinely asynchronous review. **Chat mode shows interrupted threads but does not offer the state-editing interface** — a significant asymmetry. ([DeepWiki: Interrupts and HITL](https://deepwiki.com/langchain-ai/langgraph-studio/6.2-interrupts-and-human-in-the-loop))

**Agent Inbox** (`langchain-ai/agent-inbox`) — an inbox UX for HITL agents. Setup via a settings popover in the bottom-left sidebar: LangSmith API key, then per-inbox Assistant/Graph ID, Deployment URL, optional Name (stored in browser local storage). Each interrupt renders as:
- an **action header** derived from the action name
- a **description field supporting markdown** for context and instructions
- an **arguments display** showing the action's parameters

**Four response actions, each individually gated by a config flag** (`allow_accept`, `allow_edit`, `allow_respond`, `allow_ignore`):
1. **Accept** — approve the proposed action as-is
2. **Edit** — modify argument values, then proceed
3. **Respond** — send a freeform text message back
4. **Ignore** — dismiss without acting

Plus an **"Open in Studio"** button to jump from the inbox to the graph. ([README](https://github.com/langchain-ai/agent-inbox/blob/main/README.md))

**Component structure** (worth copying wholesale): `ThreadView` is the orchestrator and switches responsively between an **Action View** (the primary interrupt-response interface, `ThreadActionsView`) and a **Side Panel View** (`StateView`, showing agent state or the interrupt description). `ThreadActionsView` has three sections — **Header** (interrupt action name, copyable thread ID, studio link, view toggles), **Action Controls** (an always-available **"Mark as Resolved"** plus conditional **Ignore**), and **User Input** (`InboxItemInput`). Container is `flex h-[80vh] w-full flex-col overflow-y-scroll rounded-2xl bg-gray-50/50 p-8 lg:flex-row` — desktop side-by-side, mobile stacked. ([DeepWiki: Agent Interaction Components](https://deepwiki.com/langchain-ai/agent-chat-ui/4.1-agent-interaction-components))

### C3. AutoGen Studio (Microsoft)

- **Team Builder**: a canvas with a main team node and connected agent nodes. Component Library sidebar; **agents and termination conditions drop onto the team node; models and tools drop onto agent nodes**, with distinct drop zones per component type. Edit icon on any node opens a properties panel with nested config (e.g. model client settings). A **JSON editor** mode is the escape hatch.
- **Playground**: test a team on a task; **displays "the team's inner monologue during task execution"**; review generated artifacts (images, code, text); tracks **turn count and token usage** and shows agent actions (tool usage, code execution results).
- **Gallery**: reusable components across projects; import configs; set a default gallery that populates Team Builder.

([usage docs](https://microsoft.github.io/autogen/stable//user-guide/autogenstudio-user-guide/usage.html))

### C4. Dify

Two levels of run inspection, which is the right decomposition:

**Application level** — every execution produces a log entry with three sections: **Result** (the final output the user saw, plus errors), **Detail** (original input, final output, system metadata), and **Tracing** (*"exactly how your workflow executed, including which nodes ran in what order, how long each took, and where data flowed between them"* — the key surface for branching/loop workflows and for spotting bottlenecks).

**Node level** — click **"Last run"** in any node's config panel to see that node's most recent input, output, and timing. ([Run History docs](https://docs.dify.ai/en/use-dify/debug/history-and-logs))

### C5. Flowise — AgentFlow V2

Canvas of 14 node types; *"visual connections between nodes on the canvas explicitly define the workflow's path and control sequence."* The relevant node is the **Human Input Node**: *"execution is paused while awaiting human input, without blocking the running thread. Each checkpoint is saved, allowing the workflow to resume from the same point even after an application restart."* It presents **distinct action choices — e.g. "Proceed," "Reject"** — and optionally collects free text. Agents can be configured to **request permission before executing tools, explicitly modeled on how Claude asks for approval before using MCP tools.** ([docs](https://docs.flowiseai.com/using-flowise/agentflowv2))

### C6. n8n

- **Wait node** is the HITL primitive: pauses execution at a decision point until approval arrives.
- **`sendAndWait` operations** route the approval request into Slack, Gmail, Discord, Telegram, or Microsoft Teams — *"your HITL checkpoints should incorporate your preferred tools where you already work."* The approval lands as a message with context (confidence scores, preview links) and clear **approve / reject / edit** choices, answerable from mobile or chat.
- **Selective gating**: IF-node branching lets high-confidence outputs bypass review entirely and routes only edge cases to a human.
- **Timeout paths** combined with IF branching let unanswered approvals escalate, shelve, or default to a safe outcome.
- The visual canvas makes decision trees and approval paths legible as topology.

([n8n HITL blog](https://blog.n8n.io/human-in-the-loop-automation/))

### C7. Temporal Web UI — the best long-log visualization in existence

Temporal isn't an agent tool, but its Event History UI solves exactly the "make a 4,000-event log readable" problem, and does it better than any agent product I found.

- **Workflow list**: table of all executions in the retention period; filter by status, Workflow ID, type, start/end time, and custom search attributes; namespace switcher top-right. A predefined **Task Failures** filter auto-flags executions after five consecutive failures.
- **Workflow detail tabs**: History, Workers, Relationships, **Pending Activities**, Nexus Operations, Queries, Metadata.
- **Four views of the same event history**: **Timeline** (chronological or reverse-chronological with a summary), **Compact** (*logical grouping of Activities, Signals, and Timers*), **JSON** (raw), **All** (complete listing). Offering the same data at four fidelities, switchable, is the key move.
- **Timeline View mechanics** (built on `vis-timeline`): events are grouped into **Event Groups** — the three raw events for one activity (scheduled, started, completed) collapse into **a single row spanning that activity's duration**. Single events (Markers, Signals) render as points. **Green = completed, red = failed.** Horizontal position reveals parallelism at a glance. **Retries show a retry icon with the current attempt number.** Scroll both axes; zoom by gesture or button with anti-over-zoom guards; a **Fit** button returns to initial zoom. Hovering a span gives exact start/end and millisecond duration. Filter by event type from the Event History table to reduce clutter.
- **Call Stack tab** runs the `__stack_trace` query to show where the workflow is currently blocked (requires a live worker).

([Timeline View blog](https://temporal.io/blog/lets-visualize-a-workflow), [Web UI docs](https://docs.temporal.io/web-ui), [Timeline changelog](https://temporal.io/changelog/updated-event-history-timeline-view-is-now-available))

**Translation to agents:** group `tool_use` + `tool_result` + any retries into one collapsible span; color by outcome; lay spans on a time axis so parallel subagents are visually obvious; offer Timeline / Compact / Raw as a switch rather than picking one.

---

## Part D — Human-in-the-loop interruption: the pattern catalogue

### D1. The six-state model

The cleanest formalization I found ([AI UX Playground](https://aiuxplayground.com/pattern/human-in-the-loop/)) — HITL is a state machine, not a dialog:

**Proposed** (AI stages an action) → **Under Review** (full payload visible and inspectable) → **Editing** (person modifies in place) → **Approved** (explicit confirmation) → **Rejected** (denied, no side effects) → **Executed** (separate from the decision).

Every approval card should expose six things: **Action** (active verb, exactly what will happen), **Scope** (target, quantity, boundaries), **Impact** (plain-language who/what changes), **Reason** (why *this* paused — which threshold or risk class), **Alternatives** (edit and deny as first-class, unpressured paths), **Recovery** (whether it can be cancelled or undone).

**When to use:** hard-to-reverse actions, spending money, contacting external parties, production changes, compliance implications. **When not to:** low-stakes reversible suggestions (approval becomes friction), fully autonomous batch jobs with rollback, granular creative edits (gate at publish instead).

**Four named anti-patterns:** silent auto-actions with buried undo; approval UI that hides the actual payload being authorized; global "always allow" toggles that bypass re-prompting for high-risk actions; requesting approval *after* side effects already ran.

### D2. Claude Code's permission modes — the most battle-tested implementation

Six modes, switched with `Shift+Tab` in the CLI, the mode indicator at the bottom of the prompt box in VS Code, or the mode selector next to the send button in Desktop. ([docs](https://code.claude.com/docs/en/permission-modes))

| Mode | Runs without asking | UI label / status bar |
|---|---|---|
| `default` | Reads only | **Manual** · gray `⏸ manual mode on` |
| `acceptEdits` | Reads, file edits, common fs commands (`mkdir`, `touch`, `mv`, `cp`) | **Edit automatically** · `⏵⏵ accept edits on` |
| `plan` | Reads, plus classifier-approved commands | **Plan** · `⏸ plan mode on` |
| `auto` | Everything, with background safety checks | **Auto** · `⏵⏵ auto mode on` |
| `dontAsk` | Only pre-approved tools | `⏵⏵ don't ask on` |
| `bypassPermissions` | Everything | `⏵⏵ bypass permissions on` |

Design lessons embedded here:

- **The mode is always visible in the status bar / mode indicator.** Autonomy level is ambient, never hidden in settings.
- **The cycle is ordered by increasing risk**, and dangerous modes are *not in the cycle by default*: `bypassPermissions` only appears if you launched with a flag that enables it; `dontAsk` never appears in the cycle at all.
- **Some things are never auto-approved in any mode, including bypass**: explicit `ask` rules, org-set connector tools, `AskUserQuestion` and MCP tools marked `requiresUserInteraction`, and `rm`/`rmdir` targeting critical paths (which **no allow rule or `PreToolUse` hook can approve**). A floor below which delegation cannot go.
- **Auto mode replaces the human reviewer with a classifier model** rather than removing review — it blocks actions that "escalate beyond your request, target unrecognized infrastructure, or appear driven by hostile content Claude read," and reviews every inter-agent `SendMessage`. This is a genuinely new pattern: *machine-in-the-loop as a substitute for human-in-the-loop*, with the human retaining `ask` rules as an override.
- **First-run disclosure**: the first time a session starts in auto mode by default, a one-time notice appears at the top of the terminal, or as a dismissible card on the VS Code new-conversation screen.
- **Mode choice is remembered per folder** in Desktop, and **Plan is the deliberate exception** — picking Plan applies to the current session only.

**Plan mode's approval dialog** is the canonical "Intent Preview" implementation. When the plan is ready, Claude presents it with three choices:
- **"Yes, and use auto mode"** — approve and drop into auto (reads *"Yes, auto-accept edits"* if auto is unavailable; reads *"Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session"* if bypass is enabled)
- **"Yes, manually approve edits"** — approve, but review each edit
- **"No, keep planning"** — stay in plan mode and say what to change

**`Ctrl+G` opens the proposed plan in your `$EDITOR` so you can edit it directly before Claude proceeds.** Accepting a plan **auto-names the session from the plan content**. With `showClearContextOnPlanAccept`, a fourth option appears that approves *and* clears planning context.

Note the compound move: **the approval control and the autonomy dial are the same control.** You don't just say yes; you say yes *at a level of supervision*.

### D3. The programmatic contract behind approval UIs

From the Agent SDK ([Handle approvals and user input](https://code.claude.com/docs/en/agent-sdk/user-input)) — this is what any custom agent GUI must implement:

`canUseTool(toolName, input, {suggestions, signal})` fires for both tool approvals and clarifying questions, and **pauses execution until it returns**. It can **stay pending indefinitely**; the SDK only cancels on query cancellation. For waits longer than your process can survive, return the **`defer` hook decision**, which lets the process exit and **resume later from the persisted session** — the mechanism that makes a genuine asynchronous approval inbox possible rather than a blocking modal.

Six response shapes, which map directly onto UI buttons:
1. **Approve** — `{behavior:"allow", updatedInput: input}`
2. **Approve with changes** — return a *modified* `updatedInput` (sanitize paths, add constraints, scope access). Claude sees the result but **isn't told you changed anything**.
3. **Approve and remember** — the callback's `suggestions` array carries ready-made `PermissionUpdate` entries; echo one back in `updatedPermissions`. A suggestion with `destination: "localSettings"` writes the rule to `.claude/settings.local.json` so future sessions skip the prompt. **This is what makes an "Always allow" button safe: the rule is a concrete, inspectable, scoped artifact, not a global mute.** The canonical UI is a three-way choice: `once` / `always` / `no`.
4. **Reject** — `{behavior:"deny", message}`; the message is shown to Claude, which may adapt.
5. **Suggest alternative** — deny *with guidance*: *"User doesn't want to delete files. They asked if you could compress them into an archive instead."*
6. **Redirect entirely** — use streaming input to send a wholly new instruction.

For notifications, a **`PermissionRequest` hook** fires so you can push to Slack/email/mobile when Claude is waiting. For rules that must apply to *every* call regardless of mode, use a **`PreToolUse` hook**, which runs before the permission flow.

### D4. Ask-for-clarification: `AskUserQuestion`

The structured-clarification pattern, specified precisely enough to build against:

```json
{"questions":[{
  "question": "How should I format the output?",
  "header": "Format",                      // ≤12 chars — the chip/tab label
  "options": [                             // 2–4 options
    {"label":"Summary","description":"Brief overview of key points"},
    {"label":"Detailed","description":"Full explanation with examples"}
  ],
  "multiSelect": false
}]}
```

1–4 questions per call, 2–4 options each. Response is `{questions, answers}` where `answers` maps question text → selected label(s); a separate optional `response` field carries a freeform reply *instead of* structured answers (Claude then receives "The user responded: …"). Recommended UI: render each question with its `header` as a compact label, options as cards showing `label` + `description`, and **append an "Other" choice that accepts text — using the typed text as the answer value, not the word "Other."**

**Option previews (TypeScript):** setting `toolConfig.askUserQuestion.previewFormat` to `"markdown"` or `"html"` adds a `preview` field per option so the UI can render **a visual mockup beside each choice** — an HTML `<div>` fragment (the SDK strips `<script>`, `<style>`, `<!DOCTYPE>` before your callback sees it), or ASCII art / fenced code for markdown. Claude includes previews only where visual comparison helps (layouts, color schemes) and omits them for yes/no. Check for `undefined`.

Limitations: not available in subagents spawned via the Agent tool.

Clarifying questions are *"especially common in plan mode,"* which makes plan mode the natural home for requirement-gathering interaction.

### D5. Pause / resume / takeover — how the survivors actually do it

| Mechanism | Implementations |
|---|---|
| **Stop / interrupt mid-run** | Vibe Kanban **Stop** button; Devin "stop the session to pause Devin"; Claude Squad `D` kill; Ralph TUI **pause/resume/kill a specific task without nuking the session** |
| **Explicit takeover** | Devin: stop → use the VS Code IDE with full terminal (`⌘K` available); Claude Squad `c` = **commit + pause + checkout**; Conductor `⌘O` opens the file in your real IDE; Sculptor **Pairing Mode** syncs container→local IDE in one click |
| **Steer without stopping** | Vibe Kanban follow-up field; Conductor chat composer with diff comments attached; `claude -p "…" --cloud <id>` queues a message into a running cloud session from any machine; Managed Agents "send additional user events to guide mid-execution"; streaming input in the SDK |
| **Ask without polluting context** | Claude Code Desktop **Side chat** `⌘;`; Devin **Side Chats** / `/btw` (read-only — the agent can research but not edit) |
| **Durable pause** | Flowise Human Input Node (checkpoint survives app restart); LangGraph interrupted threads persist indefinitely; Claude Code cloud sessions — answer an idle question whenever you return; SDK `defer` decision lets the process exit and resume from the persisted session |
| **Resume after crash** | Ralph TUI `.ralph-tui/session.json`; Temporal event history; Claudia checkpoint timeline |

### D6. Two consolidated pattern catalogues

**Smashing Magazine, "Designing For Agentic AI" (Feb 2026)** — six patterns, each with a proposed success metric ([link](https://www.smashingmagazine.com/2026/02/designing-agentic-ai-practical-ux-patterns/)):

1. **Intent Preview** — step-by-step plan before execution; options *Proceed / Edit Plan / Handle it Myself*. Metric: >85% acceptance without modification.
2. **Autonomy Dial** — progressive authorization across **Observe & Suggest → Plan & Propose → Act with Confirmation → Act Autonomously**, configurable per task type. Metric: setting churn as a trust-volatility signal.
3. **Explainable Rationale** — post-action "why," in user language grounded in *their* stated preferences: *"Because you said X, I did Y."* Not technical logs. Metric: support tickets tagged "unclear agent behavior."
4. **Confidence Signal** — certainty communicated via score, scope declaration, or icon (✓ high, ? uncertain). Metric: calibration score >0.8 correlation between model confidence and user acceptance.
5. **Action Audit & Undo** — persistent chronological log of agent-initiated actions with status and **prominent undo**; time-limited undos must transparently say when the action becomes permanent. Metric: reversion rate <5%.
6. **Escalation Pathway** — acknowledge ambiguity rather than guess: ask, present options, or route to a human expert. Metric: healthy escalation at **5–15% of tasks** (both too low and too high are failures).

**HatchWorks, "Agent UX Patterns"** — 12 patterns and a maturity ladder ([link](https://hatchworks.com/blog/ai-agents/agent-ux-patterns/)). Seven failure modes of chat-first UI: invisible actions, unclear state, no controls, no recovery path, no accountability trail, inability to pause, black-box reasoning. *"Your agent processes a refund… and the user sees a cheerful 'Done!' with no evidence of what actually happened, why, or how to undo it."*

| Pattern | Replaces |
|---|---|
| **Taskboard + Outcomes** — goals → tasks → sub-tasks with owner (agent or human), status, SLA | chat-only progress |
| **Activity Timeline** — chronological log of decisions, tool calls, state changes; filterable by severity and type; collapsible detail levels; "jump to artifact" links | mystery steps |
| **Start/Stop/Pause/Resume** — with **explicit semantics for what happens to in-flight and queued actions**, and safe-rollback messaging | autonomous black box |
| **Autonomy Levels** — Suggest → Draft → Execute slider, *per workflow* | all-or-nothing |
| **Two-Phase Actions** — show the plan, validate inputs/permissions/targets, then execute with a receipt. Mandatory for irreversible ops | invisible execution |
| **Action Receipts** — what changed, where, references, timestamps, responsible agent, rollback hooks | "Done!" |
| **Evidence Panel** — citations and sources, separated from assumptions, with a **"challenge" affordance** to flag a source or request a re-run | chain-of-thought dumps |
| **Human Checkpoint Gates** — typed gates: approve plan / approve execution / approve final output / approve exceptions, each with notification and logging rules | manual heroics |
| **Role Cards** — per-agent scope, tools, permissions, handoff rules | prompt bloat |
| **Safe Failure & Recovery** — fall back to deterministic steps when risk rises | stuck states |
| **Memory Controls** — "what I remember," retention windows, per-workflow scope | silent retention |
| **Budget + Time Boxes** — real-time ETA and spend with circuit breakers | runaway cost |

Maturity: **L1** chat-only (demo only) → **L2** guided agent (taskboard + timeline + controls + receipts + checkpoint gates) → **L3** trusted autonomy (autonomy sliders + evidence + memory controls + continuous eval). Recommendation: **ship L2 in v1**; move to L3 slowly in high-stakes domains.

**Zylos, "Agentic UX Frontend Design Patterns" (May 2026)** adds three things worth isolating ([link](https://zylos.ai/research/2026-05-28-agentic-ux-frontend-design-patterns-ai-agents/)):

- **Pause-and-inspect beats binary approve/abort.** Halt at *any* step, review reasoning, modify inputs, or **skip a step** without restarting the workflow. Direct user feedback: *"single-level control feels wrong."*
- **Progressive delegation / persistent approval profiles.** Novices approve all file modifications; users with consistent approval histories auto-approve routine operations while retaining interrupt capability **for novel contexts**. Risk is contextual, not per-tool.
- **Confidence signaling should be binary, not numeric.** Testing shows **high/low indicators produce faster and more accurate intervention decisions than probabilities.**

---

## Part E — Displaying a live tool-call stream for non-expert users

### E1. The three fidelity levels, and why you need all three

The single strongest empirical signal: **Claude Code Desktop ships Verbose / Normal / Summary as a live-switchable control** (`Ctrl+O`), and the documented reason is fleet management — Summary is *"ideal for monitoring multiple concurrent sessions,"* and mode choice *"dramatically affects cognitive overhead when orchestrating four or more parallel operations."* Temporal independently arrived at four views of the same history (Timeline / Compact / JSON / All). **Do not choose one rendering; make it a switch, and let the number of concurrent agents drive the default.**

### E2. Transparency patterns, from Smashing's Part 2 ([link](https://www.smashingmagazine.com/2026/05/practical-interface-patterns-ai-transparency/))

1. **The Living Breadcrumb** — a subtle, non-demanding background indicator for low-stakes work, updating smoothly through states: *"Reading email" → "Drafting reply" → "Checking tone."* Quiet assurance, no attention tax.
2. **Dynamic Checklist** — for high-stakes workflows: completed `[✓]`, current `[Processing]`, pending `[Pending]`. Crucially, it **anchors the user to a process stage rather than to elapsed time**, which is how you survive unpredictable durations without a fake progress bar.
3. **The Thinking Toggle** — a chevron that expands a friendly summary into a *sanitized* logic log: which API queries ran, what response codes came back, what filter criteria applied. Optional depth signals trustworthiness while protecting proprietary logic.
4. **Audit Trail / "Show Work"** — persistent post-task documentation you can replay: *"See how this price was calculated," "View search sources."*
5. **Partial Success Designation** — break a task into components, mark each `[Success]`/`[Failed]` individually. Prevents a misleading binary failure when the agent accomplished 80% of the goal. (Underused in coding-agent UIs, where "the run failed" often hides four completed files.)

**The status-line formula:** **Action Word + Specific Item + Limits/Rules.** *"Scanning the prices on Lufthansa and United to find anything under $600."* Every element of that sentence is falsifiable by the user, which is the point.

### E3. Separate the activity feed from the conversation

Zylos's strongest structural claim: **the conversation surface and the activity panel should be different surfaces.** The conversation handles clarification and feedback; the activity panel maintains an auditable log of tool calls, subtasks, progress signals, and approval requests. Conflating them "creates cognitive overload and makes multi-step workflow auditing impossible."

The activity panel uses **three-level progressive disclosure**:
- collapsed → summary progress (**"3 of 7 steps complete"**)
- expand → step-level detail
- expand again → tool invocation payloads and raw responses

Tool calls stream **progressively as they occur**, with inputs, outputs, and timing — including **live tool output streaming** for intermediate results rather than batching.

### E4. Tool-aware renderers (do not render everything as JSON)

The best concrete implementation is Claude Code Agent Monitor's rule: **format tool output by tool type** — Bash renders as terminal output, file edits as side-by-side diffs, code as syntax-highlighted blocks with line numbers. Vibe Kanban's log normalizer does the equivalent, emitting typed elements (reasoning / shell command / **file modification with expandable detail** / tool use / status) rather than a single text stream. Conductor renders the diff *as a diff pane that updates live*, not as text in the transcript.

**Corollary:** the raw JSON of a tool call is almost never the right default rendering. `Edit(file_path, old_string, new_string)` should render as a diff hunk. `Bash(command)` should render as a `$` prompt line with collapsible output. `Read(file_path)` should render as one line: *"Read src/auth/login.ts."*

### E5. Grouping and collapsing (Temporal's lesson)

Collapse the **N raw events of one logical operation into one row spanning its duration**. For Temporal that's scheduled+started+completed → one activity span. For an agent that's `tool_use` + `tool_result` + retries → one collapsible tool-call card. Color by outcome (green/red). Show retry count as a badge on the row rather than as N separate rows. Lay rows on a time axis so **parallelism is visible as horizontal overlap** — the only way a reader can see that four subagents ran concurrently.

### E6. Error surfaces

**Three-part structured error messaging** replaces "something went wrong": **what happened** (specific failure type), **why** (root cause), **what to try next** (concrete recovery step). Users need enough diagnostics to determine whether the fault is their prompt, an API failure, a tool permission, or agent reasoning.

### E7. Notification discipline (the most-violated rule)

From Moshi and cmux, the two tools that got this right:

- **One row per session, updated in place.** New events mutate the existing row; they do not stack. Anything else produces a notification avalanche at four concurrent agents.
- **Tier the events.** Approvals and errors get a visible push. Everything else silently updates the inbox row / Live Activity / sidebar badge.
- **Approval requests sort to the top**, always.
- **Ambient beats interruptive for "needs input."** cmux: a **blue ring around the pane** plus a lit sidebar tab, with `⌘⇧U` to jump to the most recent unread agent. Conductor: sidebar badges. Claude Code Desktop: filter the sidebar by status.
- **Disambiguate "waiting."** Needs input / Turn done / At prompt / Interrupted are four different states requiring four different human responses.
- **Constrain by device.** The Apple Watch tier deliberately drops shell, tmux attach, dictation, image paste, and scrollback, keeping only approve/deny/answer — "fast triage and small decisions."

---

## Part F — Synthesis: the reference design

If you were building a board/queue UI for multiple Claude Code agents today, the convergent evidence points at this:

**Left: workspace/session sidebar.** Human-memorable names (Conductor's cities; Claude Code auto-names from the accepted plan). Per-row: status badge, branch, diff stat (`+42 -18`), PR check state, listening port. **Filter by status/project/environment; group by project.** Auto-archive on PR merge. Badge only for *needs-you* states, disambiguated four ways.

**Board view as an alternate lens, not the primary.** Columns driven by *execution* state (To do / In Progress / In Review / Done), with manual drags explicitly non-triggering. Cards carry the linked-workspace indicator. Provide a dense list view for large backlogs.

**Center: the agent conversation, unchanged.** Same affordances as the CLI (`@file`, slash commands). Plus a **side chat** for questions that must not enter context.

**Right: diff + terminal + preview, drag-arrangeable.** Diff updates live as the agent writes. **Inline comments on diff lines become attachments on the next prompt** — one object, two roles. Per-file revert. A "Review changes" button that hands the diff to a second agent. Preview pane with auto-detected dev-server URL and its own log strip.

**Isolation, surfaced but not fetishized.** Worktree per session, auto-created, path and branch prefix configurable, plus a `.worktreeinclude` escape hatch for gitignored files. Container per session if agents must *run* code. Per-workspace port range. Show the branch name; hide the plumbing.

**Attempts, not retries.** N attempts per task, each with its own profile/variant/base-branch, compared side by side.

**Approval as an inline row, escalating to an inbox.** Tick/cross inline in the transcript when you're watching; a top-sorted inbox row + push when you're not; three choices (`once` / `always` / `no`) where "always" writes a concrete, scoped, inspectable rule. A visible autonomy indicator at all times, ordered by risk, with the dangerous rungs absent unless explicitly enabled — and a floor of actions no mode can auto-approve.

**Plan gate as the primary checkpoint.** Intent Preview with *Proceed / Edit Plan / Keep Planning*, where "Proceed" simultaneously selects the supervision level for what follows, and the plan itself is editable in a real editor before execution.

**Log stream: switchable fidelity, typed renderers, grouped spans.** Verbose / Normal / Summary, live-switchable, defaulting toward Summary as concurrency rises. Tool-aware rendering (diff hunks, terminal blocks, one-line reads). One collapsible card per logical operation with a retry badge. Three-level disclosure: "3 of 7 steps" → step detail → raw payload. Status lines as *Action + Specific Item + Limits*. Partial-success marking rather than binary failure.

---

## Sources

**Board/queue orchestrators**
- [BloopAI/vibe-kanban (GitHub)](https://github.com/BloopAI/vibe-kanban) · [Shutdown announcement](https://www.vibekanban.com/blog/shutdown) · [Creating Tasks](https://www.vibekanban.com/docs/core-features/creating-tasks) · [New Task Attempts](https://www.vibekanban.com/docs/core-features/new-task-attempts) · [Monitoring Task Execution](https://vibekanban.com/docs/core-features/monitoring-task-execution) · [Testing Your Application](https://vibekanban.com/docs/core-features/testing-your-application) · [DeepWiki: Kanban Board and Issue Management](https://deepwiki.com/BloopAI/vibe-kanban/5.6-kanban-board-and-issue-management) · [DeepWiki index](https://deepwiki.com/BloopAI/vibe-kanban) · [VirtusLab review](https://virtuslab.com/blog/ai/vibe-kanban) · [Starlog: git worktree strategy](https://starlog.is/articles/ai-dev-tools/bloopai-vibe-kanban/) · [vibecoding.app post-shutdown review](https://vibecoding.app/blog/vibe-kanban-review)
- [Conductor Docs — Introduction](https://www.conductor.build/docs) · [Workflow concepts](https://www.conductor.build/docs/concepts/workflow) · [Review and merge](https://www.conductor.build/docs/guides/review-and-merge) · [Run multiple Claude Code sessions](https://www.conductor.build/docs/guides/parallel-agents/run-multiple-claude-code-sessions) · [Claude can now comment on your code (diff tools)](https://www.conductor.build/blog/diff-tools) · [Taskos: 5 parallel sessions](https://georgetaskos.medium.com/scaling-the-loop-run-5-claude-code-sessions-in-parallel-with-conductor-build-539b52888a81) · [Julian Astrada](https://julianastrada.com/blog/conductor-parallel-agents/) · [CodePick intro](https://codepick.dev/en/guides/conductor-build-intro/)
- [terragon-labs/terragon-oss](https://github.com/terragon-labs/terragon-oss) · [The Tool Nerd: Terragon / Conductor / Cursor](https://www.thetoolnerd.com/p/era-of-virtual-employees-running)
- [Sculptor announcement](https://imbue.com/blog/sculptor-announce) · [Sculptor product page](https://imbue.com/product/sculptor) · [Sandboxed agents 10x faster to start](https://imbue.com/blog/containers)
- [stravu/crystal](https://github.com/stravu/crystal) · [Nimbalyst / Crystal successor](https://nimbalyst.com/crystal/) · [Nimbalyst docs](https://docs.nimbalyst.com/) · [Vibe Kanban after Bloop](https://nimbalyst.com/blog/vibe-kanban-after-bloop-whats-next/)
- [Claudia (repo mirror)](https://github.com/tdmatheus/claudia) · [ClaudeLog: Claudia](https://claudelog.com/claude-code-mcps/claudia/)
- [smtg-ai/claude-squad README](https://github.com/smtg-ai/claude-squad/blob/main/README.md)
- [omnara-ai/omnara](https://github.com/omnara-ai/omnara) · [Moshi: agent hooks, inbox, Live Activities, Watch](https://getmoshi.app/articles/agent-hooks-live-activities-usage) · [slopus/happy](https://github.com/slopus/happy) · [coa00/claude-push](https://github.com/coa00/claude-push)
- [ObservedObserver/async-code](https://github.com/ObservedObserver/async-code)
- [MrLesk/Backlog.md](https://github.com/MrLesk/Backlog.md)
- [eyaltoledano/claude-task-master](https://github.com/eyaltoledano/claude-task-master)
- [hoangsonww/Claude-Code-Agent-Monitor](https://github.com/hoangsonww/Claude-Code-Agent-Monitor) · [Best open-source mission control dashboards 2026](https://www.howtodeploy.app/blog/ai-agent-mission-control) · [Mission Control (Builderz)](https://mc.builderz.dev/) · [Ralph TUI guide](https://www.verdent.ai/guides/ralph-tui-ai-agent-dashboard)
- [cmux on DEV](https://dev.to/arshtechpro/cmux-the-native-macos-terminal-built-for-running-ai-coding-agents-in-parallel-52il) · [cmux.com](https://cmux.com/) · [awesome-agent-orchestrators (≈200-tool census)](https://github.com/andyrewlee/awesome-agent-orchestrators) · [awesome-cli-coding-agents](https://github.com/bradAGI/awesome-cli-coding-agents)

**First-party GUIs**
- [MacRumors: Anthropic rebuilds Claude Code desktop app](https://www.macrumors.com/2026/04/15/anthropic-rebuilds-claude-code-desktop-app/) · [Miraflow: complete guide to parallel sessions, routines, workspace](https://miraflow.ai/blog/claude-code-desktop-redesign-parallel-sessions-routines-workspace-guide)
- [Claude Code on the web (docs)](https://code.claude.com/docs/en/claude-code-on-the-web) · [Permission modes (docs)](https://code.claude.com/docs/en/permission-modes) · [Handle approvals and user input (Agent SDK)](https://code.claude.com/docs/en/agent-sdk/user-input) · [Claude Managed Agents overview](https://platform.claude.com/docs/en/managed-agents/overview)
- [Learn Cursor: Agents Window](https://www.learncursor.dev/learn/cursor-agents/agents-window) · [AgentPatterns: Cursor 3 Agents Window](https://www.agentpatterns.ai/tools/cursor/agents-window/)
- [Codex cloud (OpenAI developers)](https://developers.openai.com/codex/cloud)
- [Devin session tools](https://docs.devin.ai/work-with-devin/devin-session-tools)
- [Google Jules guide](https://www.digitalapplied.com/blog/google-jules-gemini-async-coding-agent-guide)
- [Amp Code guide](https://sidbharath.com/blog/amp-code-guide/)

**Orchestration frameworks with UIs**
- [DeepWiki: CrewAI Crew Studio](https://deepwiki.com/crewAIInc/crewAI/10.5-crew-studio) · [CrewAI Traces](https://docs.crewai.com/en/enterprise/features/traces)
- [DeepWiki: LangGraph Studio interrupts & HITL](https://deepwiki.com/langchain-ai/langgraph-studio/6.2-interrupts-and-human-in-the-loop) · [langchain-ai/agent-inbox](https://github.com/langchain-ai/agent-inbox/blob/main/README.md) · [DeepWiki: agent-chat-ui interaction components](https://deepwiki.com/langchain-ai/agent-chat-ui/4.1-agent-interaction-components) · [DeepWiki: handling interrupted actions](https://deepwiki.com/langchain-ai/agent-chat-ui/4.2-handling-interrupted-actions) · [LangChain HITL docs](https://docs.langchain.com/oss/python/langchain/human-in-the-loop)
- [AutoGen Studio usage](https://microsoft.github.io/autogen/stable//user-guide/autogenstudio-user-guide/usage.html)
- [Dify run history and logs](https://docs.dify.ai/en/use-dify/debug/history-and-logs) · [DeepWiki: Dify run history](https://deepwiki.com/langgenius/dify-docs/6.3-run-history-and-logging)
- [Flowise AgentFlow V2](https://docs.flowiseai.com/using-flowise/agentflowv2)
- [n8n: human in the loop automation](https://blog.n8n.io/human-in-the-loop-automation/)
- [Temporal: Let's visualize a workflow (Timeline View)](https://temporal.io/blog/lets-visualize-a-workflow) · [Temporal Web UI docs](https://docs.temporal.io/web-ui) · [Timeline view changelog](https://temporal.io/changelog/updated-event-history-timeline-view-is-now-available) · [Redesigning workflow experience](https://temporal.io/blog/the-dark-magic-of-workflow-exploration)

**UX pattern literature**
- [Smashing: Designing For Agentic AI — control, consent, accountability](https://www.smashingmagazine.com/2026/02/designing-agentic-ai-practical-ux-patterns/)
- [Smashing: Practical Interface Patterns For AI Transparency (Part 2)](https://www.smashingmagazine.com/2026/05/practical-interface-patterns-ai-transparency/)
- [HatchWorks: Agent UX Patterns — chat-first UX fails](https://hatchworks.com/blog/ai-agents/agent-ux-patterns/)
- [Zylos: Agentic UX frontend design patterns (May 2026)](https://zylos.ai/research/2026-05-28-agentic-ux-frontend-design-patterns-ai-agents/)
- [AI UX Playground: Human in the loop pattern](https://aiuxplayground.com/pattern/human-in-the-loop/)
- [Victor Dibia: 4 UX design principles for multi-agent systems](https://newsletter.victordibia.com/p/4-ux-design-principles-for-multi)
- [Addy Osmani: The Code Agent Orchestra](https://addyosmani.com/blog/code-agent-orchestra/)

**Caveats:** "Sirvine" could not be located as a real product — flagged rather than fabricated. Several vendor doc pages describe capability rather than layout (Conductor, Cursor, Codex, Sculptor), so pixel-level descriptions for those come from hands-on reviews rather than official docs, and are attributed accordingly. Vibe Kanban's docs remain live but describe a product whose cloud features were retired in May 2026; the local UI descriptions still apply to the OSS build.

---


# Part 3 — Automation rule builders and live run monitoring

# UI/UX Patterns Worth Borrowing: Automation Rule Builders & Live Run Monitoring

Research date: August 2026. Every claim below is anchored to a source URL; where I'm generalizing across tools rather than quoting docs, I mark it **[synthesis]**.

---

# AREA 1 — Event-trigger / automation rule builder UX

## 1.1 The core split: rule-row forms vs. node canvases

There are exactly two dominant archetypes, and the choice is not aesthetic — it's determined by **how much data-shape manipulation the user must do between trigger and action**. **[synthesis, but strongly supported by the tool sample below]**

| | **Rule-row form** (Linear, Jira, Sentry Alerts, Notion, Airtable, Slack WFB) | **Node canvas** (n8n, Make, Zapier-hybrid) |
|---|---|---|
| Mental model | A sentence: *When X, if Y, then Z* | A pipeline: data flows through boxes |
| Best when | Trigger and action operate on the **same object** (an issue, a PR, a record) and fields are named/typed by the host app | User must **reshape, map, split, merge, or fan out** data between heterogeneous systems |
| Branching | Awkward past 1 level; Jira solves with "branch rule" rows, Slack with a colored branch step | Native — routers/paths are first-class |
| Scanability at rest | Excellent — a list of rules reads like prose | Poor — you must trace edges |
| Diffability / review | Excellent | Bad (why n8n/Make users still export JSON) |
| Failure locality | You get a rule-level status | You get a node-level status, which is more precise |

**The most important observation:** Zapier — the market leader in this space — deliberately did *not* go to a free canvas. Its editor is a **linear step outline in a left sidebar, with a configuration panel on the right**, and branching happens through explicit `Paths` steps rather than arbitrary edges ([Zapier: key concepts](https://help.zapier.com/hc/en-us/articles/8496181725453-Learn-key-concepts-in-Zaps)). The linear-list-plus-detail-panel is the pattern that scales best for non-experts; canvases win only when the graph genuinely has fan-out.

**Recommendation for an agent-triggering product:** rule rows for the *binding* ("when PR opened on repo X → run agent Y"), and a trace/DAG view only for the *execution* (Area 2). Do not make users draw a graph to say "when a PR opens."

---

## 1.2 The events vocabulary — GitHub Actions is the reference taxonomy

GitHub Actions' event model is the single best-designed vocabulary to borrow, because it separates **event** from **activity type** from **filter**:

```yaml
on:
  pull_request:
    types: [opened, synchronize, ready_for_review]   # activity type
    branches: [main]                                  # filter
    paths: ['src/**']                                 # filter
```

([Trigger a workflow](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow))

The full vocabulary, from [Events that trigger workflows](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows):

- **`pull_request`** — `opened`, `synchronize`, `reopened`, `closed`, `assigned`, `unassigned`, `labeled`, `unlabeled`, `edited`, `converted_to_draft`, `ready_for_review`, `locked`, `unlocked`, `enqueued`, `dequeued`, `milestoned`, `demilestoned`, `review_requested`, `review_request_removed`, `auto_merge_enabled`, `auto_merge_disabled`
- **`pull_request_target`** — same activity types, but runs in default-branch context (the security-safe variant for fork PRs)
- **`pull_request_review`** — `submitted`, `edited`, `dismissed`
- **`pull_request_review_comment`** — `created`, `edited`, `deleted`
- **`issues`** — `opened`, `edited`, `deleted`, `transferred`, `pinned`, `unpinned`, `closed`, `reopened`, `assigned`, `unassigned`, `labeled`, `unlabeled`, `locked`, `unlocked`, `milestoned`, `demilestoned`, `typed`, `untyped`, `field_added`, `field_removed`
- **`issue_comment`** — `created`, `edited`, `deleted`
- **`discussion` / `discussion_comment`**, **`release`** (`published`, `prereleased`, …), **`check_run`** (`created`, `rerequested`, `completed`, `requested_action`), **`check_suite`** (`completed`), **`merge_group`** (`checks_requested`), **`label`**, **`milestone`**, **`branch_protection_rule`**, **`deployment`**, **`deployment_status`**, **`status`**, **`registry_package`**, **`create`**, **`delete`**, **`fork`**, **`gollum`**, **`watch`**, **`public`**, **`page_build`**
- **Non-repo-event triggers:** `workflow_dispatch` (manual, with typed inputs rendered as a form), `repository_dispatch` (external webhook), `schedule` (cron), `workflow_call` (reusable sub-workflow)

**Design lessons to steal:**

1. **Three-level narrowing** — `event → types[] → filters{}`. This is why GH Actions triggers stay readable while covering hundreds of cases. A rule-row UI can render this as three chips: `[Pull request ▾] [opened, synchronize ▾] [on branch main ▾]`.
2. **`synchronize` is the crucial one for agent loops.** "PR updated with new commits" is a distinct activity type from "opened." Any agent-trigger UI must let users distinguish *first open* from *every push*, because that's exactly where the runaway loop lives.
3. **Two variants of the same event for trust boundaries** (`pull_request` vs `pull_request_target`) — worth mirroring if your agent runs untrusted code.
4. **Many events only fire if the workflow file exists on the default branch** — a footgun GitHub documents but doesn't surface in UI. Do better: show a "this rule will not fire because…" inline validation.

---

## 1.3 Tool-by-tool teardown

### GitHub Actions
- **Authoring is text-first** (YAML in the repo), with a browser editor that has schema autocomplete. There is no visual rule builder — GitHub's bet is that the *config is code*, and the *visualization* is on the run side.
- **The visualization graph** appears on every run summary: jobs as nodes, status icon to the left of each job name, **lines between jobs indicating dependencies**, updating in real time; click a job to jump to its logs ([Use the visualization graph](https://docs.github.com/en/actions/how-tos/monitor-workflows/use-the-visualization-graph)).
- **Dry run:** none, really. `workflow_dispatch` is the closest — a manual trigger that renders declared inputs as a form. This is a genuine weakness worth beating.
- **Rule health:** the Actions tab lists runs per workflow with status; there's no "this rule fired 4 times, 1 failed" rollup — you infer it from the run list. Also a weakness worth beating.

### Linear
Linear is the best example of **rules-as-prose**, and it has two distinct surfaces:

- **Triage rules** ([linear.app/docs/triage](https://linear.app/docs/triage)): rows that read as `if <filter on issue properties> → set <team | status | assignee | label | project | priority>`. **Rules execute top-to-bottom in list order**, which is a deliberate simplification — order is the conflict-resolution mechanism instead of priorities or a rules engine. Multi-select within one filter (shift-click) gives OR semantics without ever exposing a boolean expression editor.
- **PR/commit automations** ([linear.app/docs/github](https://linear.app/docs/github)): configured at *Settings → Team → Workflows & automations → Pull request and commit automations*. Rows map GitHub PR lifecycle events to issue statuses: **PR drafted → status A; PR opened → In Progress; review requested → status B; ready for merge → status C; PR merged → Done.** Branch-targeted variants use regex (`^fea/.*`), e.g. merge to `staging` → "In QA", merge to `main` → "Deployed".
- **Magic words** as an inline, in-context trigger language: `fixes`/`resolves`/`closes` in a PR body creates a closing link; `ref`/`part of` creates a non-closing link; `skip`/`ignore` **opts out** of linking. This is a lovely pattern — an escape hatch expressed in the medium the user is already typing in, including an explicit *suppression* keyword.

**Steal:** the suppression keyword. For an agent product, a `skip-agent` / `no-review` token in a PR body or commit message is the cheapest possible loop-breaker and users understand it instantly.

### Jira Automation — the most complete rule-row builder in production
From [Create and edit rules](https://confluence.atlassian.com/spaces/AUTOMATION/pages/1141480599/Create+and+edit+Jira+automation+rules) and [Jira automation triggers](https://support.atlassian.com/cloud-automation/docs/jira-automation-triggers/):

- The rule is a vertical **"rule chain"**: trigger card at top, then a stack of component cards. You can **insert a component anywhere in the chain** and **drag-and-drop to reorder**. Three insertable component types: **New action**, **New condition**, **Branch rule** (act on *related* items — subtasks, linked issues, stories in an epic).
- Conditions can be **attached directly to the trigger** to filter which events proceed — a nice pattern that keeps cheap filtering visually adjacent to the event rather than as a separate downstream row.
- Trigger catalogue is grouped by domain: General (created/updated/transitioned/commented/field-value-changed/linked/moved/work-logged, incoming webhook, scheduled, manual), Software (sprint, version), **DevOps (branch created, commit created, build status changed, PR created/merged/declined, deployment)**, Service Management (SLA threshold, approval, alert), Security, Assets.
- **Smart values** (`{{issue.assignee.displayName}}`) are the templating layer — a mustache-lite that avoids a full DSL.

**Rule health — the best in class.** [Performance insights](https://confluence.atlassian.com/automation/view-performance-insights-for-automation-rules-1013851985.html) shows, per rule: **execution count, total duration, average duration**, plus a stacked chart of the top-20 rules **colored by outcome status**. The five outcome statuses are the key vocabulary:

| Status | Meaning |
|---|---|
| `SUCCESS` | all actions completed |
| `NO ACTIONS` | rule fired but conditions stopped it |
| `SOME ERRORS` | partial failure |
| `LOOP` | execution created a recursive loop |
| `THROTTLED` | service limits breached |

Clicking a rule drills into the **audit log** for individual executions. Distinguishing `NO ACTIONS` from `SUCCESS` is the single most underrated idea here: "the rule fired but nothing happened" is the most common user confusion, and giving it its own status and its own color solves most support tickets. **Steal this.**

### Sentry Alerts — the WHEN / IF / THEN sentence
[Issue alert config](https://docs.sentry.io/product/alerts/create-alerts/issue-alert-config/) structures a rule as three labeled sections: **triggers (WHEN)** — issue-state-based, evaluated with `ANY` semantics; **filters (IF)** — grouped under a user-selectable `ANY` or `ALL`; **actions (THEN)** — Slack/Teams/Discord/email/PagerDuty/Opsgenie/Jira/Linear/webhook. The literal words WHEN/IF/THEN as section headers is the cheapest legibility win available and costs nothing to implement.

### Zapier — the linear outline
- **Layout:** collapsible left sidebar = **Zap outline** (ordered steps); selecting a step opens the **right sidebar** with its configuration ([key concepts](https://help.zapier.com/hc/en-us/articles/8496181725453-Learn-key-concepts-in-Zaps)).
- **Conditions without a DSL:** the [Filter step](https://help.zapier.com/hc/en-us/articles/8496276332557-Add-conditions-to-Zaps-with-filters) is a three-dropdown row — **field / rule / value** — with `+ And` to add a row and `Add Or rule group` to add a disjunction group. Operators are **type-prefixed in the dropdown label** (`(Text) contains`, `(Number) greater than`), which is a clever way to teach type-compatibility without a type system UI.
- **Paths** use the identical three-dropdown rows plus an explicit all/any toggle ([Paths](https://help.zapier.com/hc/en-us/articles/8496288555917-Add-conditions-to-Zaps-with-filters)).
- **Testing:** every step has a **Data in / Data out** pair of tabs. Trigger tests pull a real **"test record"** from the source app with a **Find new records** button to refresh the sample pool. Action tests are *live* — they really create the record. Non-critical steps can be **Skip tests**'d, but trigger, Filter, and Path steps *must* be tested before publishing ([Test Zap steps](https://help.zapier.com/hc/en-us/articles/18811411817741-Test-Zap-steps)). You can also [use a previous Zap run as a test record](https://help.zapier.com/hc/en-us/articles/42896259263373-Use-a-previous-Zap-run-as-a-test-record) — replay-as-fixture, an excellent pattern.
- **Filter test feedback is visual, not textual:** pass = green left-edge highlight + checkmark; fail = amber left-edge + warning triangle, with copy "the Zap would have continued."

**Zapier's run-status vocabulary is the richest I found** ([Review Zap run statuses](https://help.zapier.com/hc/en-us/articles/20505304170637-Review-Zap-run-statuses)) — 11 statuses, each with distinct semantics:

`Successful` · `Errored` (repeated errors **auto-disable the Zap**) · `Handled error` (an error handler ran; notably does **not** auto-disable) · `Filtered` (conditions not met; downstream steps also show Filtered) · `Safely halted` (deliberate stop, e.g. a search found nothing — does not auto-disable) · `Skipped` (step-level: didn't run because upstream produced no data) · `Delayed` (a Delay step is pending) · `Scheduled` (auto-replay pending after error) · `On hold` (disconnected account / task limit) · `Needs review` (**human-in-the-loop approval gate**) · `Running`.

Three of these are worth copying verbatim: **`Filtered` vs `Safely halted` vs `Errored`** — three different kinds of "it didn't do the thing," only one of which is a bug. And **`Needs review`** as a first-class run status is exactly the primitive an agent product needs for approval gates.

### n8n — canvas + pinning
- Node canvas with typed connections; a right-hand NDV (node detail view) per node with **INPUT / OUTPUT** panes.
- **Data pinning is the killer dry-run feature** ([Pin and mock data](https://docs.n8n.io/build/work-with-data/pin-and-mock-data)): run a node once, click **Pin data** in the OUTPUT pane, and subsequent executions reuse that frozen payload instead of hitting the API. A persistent banner reads *"This data is pinned"* with an **Unpin** link. You can switch OUTPUT to JSON view, click **Edit**, hand-modify the payload, and saving auto-pins it — so you can synthesize edge cases without touching the upstream system.
- Mocking sources: **Edit Fields (Set)** node for simple literals, **Code** node for arbitrary structures, **Customer Datastore** node for canned fake data.
- **Debug in editor** ([debug executions](https://docs.n8n.io/build/understand-workflows/understand-executions/debug-executions/)): from a *failed* past execution, "Debug in editor" loads that execution's data into the current canvas and **auto-pins the payload into the first node** — so you re-run the real failing input against edited logic. Successful executions get "Copy to editor" instead. This closes the loop from observability back into authoring better than anything else I looked at. **Steal this.**

### Make.com
- Scenario editor is a **bubble-and-line canvas**; filters are attached to the *connection* between modules (a funnel icon on the wire, not a node) — a genuinely good idea, because it says "this is a gate on flow" rather than "this is a step."
- **Run once** executes the scenario in inspect mode; after the run, **each bubble is annotated with the operation/bundle count**, and clicking a bubble opens **Input / Output / metadata (time, operation counts, HTTP status)** ([scenario history guide](https://consultevo.com/make-com-scenario-history-guide/)).
- **History tab** columns: start time, status (Success / Error / Incomplete / Scheduled-Waiting), operations, data transfer, duration. Clicking a row **re-renders the scenario with markers showing how far execution reached**, with the failing module highlighted and the raw provider error message on click. Path-of-execution-overlaid-on-the-graph is the canvas equivalent of a step timeline.
- **Incomplete executions** ([help.make.com/incomplete-executions](https://help.make.com/incomplete-executions)) — an opt-in setting that parks failed runs in a dedicated tab instead of losing them, with **automatic retry for supported error types**, manual resolution, or delete. It's a user-visible dead-letter queue with a UI. Storage is quota-bounded.

### Inngest — the strongest primitives for *not* looping
- **Runs page** across all apps in an environment; filter by status, queue time, start time, app; plus **advanced search using CEL expressions** over `event.id`, `event.name`, `event.data`, `output` ([Inspecting function runs](https://www.inngest.com/docs/platform/monitor/inspecting-function-runs)). Being able to query `output` means you can search *for a specific error string across runs* — the difference between "find my failures" and "find *this* failure."
- **Run detail panel is three-part:** trigger details → event payload → run details with **step timeline**. Expanding a step shows error message + stack trace, **retry attempts with their individual failures**, timing, and step input/output.
- **Actions:** replay the whole run, **re-run from a specific step with modified inputs**, or send the trigger event to the local Dev Server to reproduce offline.
- **Function Replay** ([docs/platform/replay](https://www.inngest.com/docs/platform/replay)): "All actions → Replay" opens a modal asking for (a) a **human-readable name** ("Bug fix from PR #958"), (b) a **time range**, (c) a **status multi-select** (Failed, or Failed+Succeeded if a bug caused silent success). Inngest then **spreads the replayed runs out over time so you don't self-DDoS**. Naming the replay after the incident is a small touch that makes the runs list auditable months later.
- **Flow control primitives** ([docs/guides/flow-control](https://www.inngest.com/docs/guides/flow-control)), which are *the* answer to runaway agent loops: **concurrency** (cap simultaneous steps, scoped by an arbitrary key like user or repo), **throttling** (cap throughput over a window), **rate limiting** (*skip* events beyond a limit — drop, not queue), **debounce** (de-dupe events over a sliding window — the direct fix for "5 pushes in 30 seconds"), **priority**, plus **idempotency**, **singleton**, and **batching**.

### Trigger.dev
- Runs list filterable by **status, name, environment**; real-time trace view of each task with its logs streaming as it executes ([observability](https://trigger.dev/product/observability-and-monitoring)).
- The redesigned **run inspector** ([changelog](https://trigger.dev/changelog/run-page-inspector)) puts run status at top (matching the list), then a **timeline of events**, then tabs: **Overview** (payload, output, errors), **Detail** (tags + usage data), **Context** (the run context accessible inside the task fn). Supports **replay with a modified payload and different environment**, including SuperJSON types (`Date`, `Map`, `Set`, `BigInt`). Alerts via email/Slack/webhook on failure.

### Temporal
- **Workflows list** with filters on status, Workflow ID, Type, start/end time, and custom search attributes; **saved views** (up to 20, stored browser-locally) ([Temporal Web UI](https://docs.temporal.io/web-ui)).
- **History tab has four view modes** — the most instructive multi-fidelity pattern I found: **Timeline** (chronological with summaries, click for detail), **Compact** (logically groups related events into Activity/Signal/Timer groups), **All** (every raw event), **JSON** (downloadable). Same data, four zoom levels, user picks.
- **Call Stack** tab runs the `__stack_trace` query against a live worker to show where execution currently sits. **Pending Activities** section summarizes in-flight/retrying activities.
- Actions from UI: cancel, signal, update, **reset**, terminate, and "start new with pre-filled values."

### Slack Workflow Builder
- Steps are an ordered list; **conditional branching** is a step type described as "a visual switch statement" ([slack.dev](https://slack.dev/introducing-conditional-branching-in-workflow-builder/)): up to **10 custom rules per branch plus a fallback path**, each branch **named and color-coded**, rules **reorderable by drag-and-drop and duplicable**. Color-coding branches is a small, cheap, high-value affordance for keeping a linear list readable when it forks.
- **Activity logs** ([Slack help](https://slack.gcom/help/articles/360055655493-View-workflow-activity-logs) → [canonical](https://slack.com/help/articles/360055655493-View-workflow-activity-logs)): filter by status — **complete / in progress / workflow error / canceled / awaiting user action** — plus person and date; an **Errors** section where **Details** shows which steps completed, where it stopped, and the error. 90-day retention.

### Notion
- Entry point is the **⚡ lightning-bolt icon at the top of a database** → New automation ([Database automations](https://www.notion.com/help/database-automations)).
- Structure is literally **"When … / Do this …"**. Multiple triggers combine with a user-chosen **"any of these occur" / "all of these occur"**.
- Triggers: **Page added**, **Property edited** (with per-type predicates like *contains* / *is set to*), **Every {frequency}** (daily/weekly/monthly with time, start/end dates, timezone).
- Actions: Edit property, Add page to, Edit pages in, Send notification to, Send mail to, **Send webhook**, Send Slack notification, and **Define variables** (mentions + formulas, reusable across actions).
- **Loop prevention is absolute and stated plainly: "Database automations can't be triggered by other automations."** Button actions *can* trigger automations; automated actions cannot. This is the bluntest possible policy — it eliminates cascades entirely at the cost of expressiveness.
- Sharp edge worth avoiding: "**Edit pages in**" defaults to **all pages** in the target database unless filtered — a destructive default ([Thomas Frank's guide](https://thomasjfrank.com/notion-database-automations-the-complete-guide/)).
- Timing detail: multiple "is edited" triggers required to co-occur must happen within ~3 seconds.

### Airtable
- Left panel lists triggers/actions; **+ Add trigger**, then **+ Add advanced logic or action**; configuration in the center; dynamic values inserted via a **plus icon** that mixes static text with fields from the triggering record ([Getting started](https://support.airtable.com/docs/getting-started-with-airtable-automations)).
- **Testing is mandatory before enabling.** The system either auto-finds a matching record or you pick one; the test captures a **snapshot of base state at test time**. Failing tests block activation.
- **Run history** tab: filter All / succeeded / failed / canceled, with collapsible per-run sections revealing step-level detail and error logs. On failure, **email goes to the last user who enabled the automation** — a nice ownership heuristic (the person who turned it on owns its failures).

---

## 1.4 How conditions get expressed without a DSL — the catalogue

Ranked roughly by expressiveness-per-unit-of-user-pain:

1. **Filter chips on the trigger itself** (GH Actions `branches:`/`paths:`, Linear triage filters, Jira "conditions on the trigger"). Cheapest; keeps the gate next to the event.
2. **Three-dropdown rows: `field | operator | value`** (Zapier Filter/Paths, Jira conditions, Slack branch rules). Universal. Two refinements worth copying: **type-prefixed operator labels** (`(Text) contains`) and **`+ And` inline vs. `Add Or rule group`** as a separate button — making OR structurally heavier than AND is correct, because OR is rarer and more error-prone.
3. **A single all/any/none toggle at the group header** (Sentry `ALL`/`ANY`, Notion "any of these occur"). Covers ~95% of real rules without a boolean tree.
4. **Ordered rule lists where order *is* the logic** (Linear triage: top-to-bottom, first match wins). Eliminates precedence UI entirely.
5. **Mustache-style templating for values, not logic** (Jira smart values `{{issue.assignee.displayName}}`, Notion "Define variables", Zapier field mapping). Keep the DSL confined to *interpolation*; never let it become *control flow*.
6. **Escape hatch to a real expression language, clearly marked as advanced** (Inngest CEL in the runs search bar, Linear's regex branch matcher). The pattern: the expression box is never the *only* way to do something a dropdown can do.

**Anti-pattern to avoid:** a nested boolean tree builder with drag-handles. Nobody in this sample shipped one, which is itself the finding.

---

## 1.5 How users test / dry-run a rule — five distinct mechanisms

1. **Sample-record fetch** — Zapier's "test record" + **Find new records** button; Airtable auto-finding a matching record. The rule is tested against *real data from the real system*, but read-only.
2. **Pin/freeze the payload** — n8n's **Pin data** with a persistent banner and Unpin link; editable in JSON view to synthesize edge cases ([n8n](https://docs.n8n.io/build/work-with-data/pin-and-mock-data)).
3. **Replay a historical run as the fixture** — Zapier ["use a previous Zap run as a test record"](https://help.zapier.com/hc/en-us/articles/42896259263373-Use-a-previous-Zap-run-as-a-test-record); n8n **"Debug in editor"** (loads a failed execution's data and auto-pins it); Trigger.dev replay-with-modified-payload; Inngest re-run-from-step-with-modified-input.
4. **Run-once inspect mode with per-node annotation** — Make's **Run once**, which annotates each bubble with bundle counts and lets you open Input/Output per module.
5. **Gated publish** — Airtable and Zapier both **block activation until required steps are tested** (Zapier: trigger, Filter, and Path steps are non-skippable; other actions get a **Skip tests** affordance that clears the warning icon).

**Explicit "would-have" feedback is the piece most tools get wrong and Zapier gets right:** after testing a Filter, the UI says *the Zap would have continued* (green left-edge + check) or would not (amber left-edge + triangle). For an agent product, the equivalent is: *"This rule would have fired 7 times in the last 24h"* — a **backtest against recent event history**, which nobody in this sample offers and which would be a differentiator. **[synthesis]**

---

## 1.6 "This rule fired 4 times, 1 failed" — rule-health surfaces

Ranked by quality:

- **Jira Performance Insights** — per-rule execution count + total/avg duration, and a stacked chart of the top 20 rules **colored by outcome** across `SUCCESS / NO ACTIONS / SOME ERRORS / LOOP / THROTTLED`, drilling into the audit log ([source](https://confluence.atlassian.com/automation/view-performance-insights-for-automation-rules-1013851985.html)). Best in class: it's a *rollup*, it's *colored by outcome class*, and it *drills through*.
- **Zapier Zap history** — filterable run list with the 11-status vocabulary, per-run step-by-step **Data in / Data out**, **replay** of a step or a whole run, and **auto-disable on repeated errors** (with `Handled error` and `Safely halted` explicitly exempted from auto-disable) ([source](https://help.zapier.com/hc/en-us/articles/20505304170637-Review-Zap-run-statuses)).
- **Airtable** — Run History tab with All/succeeded/failed/canceled filters, collapsible per-run detail, and failure email to the enabling user.
- **Slack** — activity log filtered by complete / in progress / workflow error / canceled / awaiting user action, with per-run **Details** showing which step it stopped at.
- **Make** — History rows with status, **operations consumed**, data transfer, duration; plus the separate **Incomplete executions** tab as a user-facing DLQ.
- **Inngest** — runs list with CEL search over event/output, so you can count failures by error string.

**Design pattern to name and steal: the "outcome class" spectrum.** Do not model runs as success/failure. Model them as at least: **succeeded · did nothing (conditions not met) · deliberately halted · needs human approval · errored (retrying) · errored (given up) · throttled · looped**. Each gets a color and a filter chip. Almost every support burden in this category comes from collapsing "did nothing" into "failed" or into "success."

---

## 1.7 Loop and recursion protection — the full catalogue

This is the most important section for a "PR opened → agent reviews → agent pushes → PR updated → …" product. Seven distinct mechanisms exist in the wild:

**1. Actor-identity suppression (GitHub Actions).** *"Events triggered by the `GITHUB_TOKEN` will not create a new workflow run,"* with narrow exceptions (`workflow_dispatch`, `repository_dispatch`, and specific `pull_request` activity types). GitHub states the rationale directly: *"this behavior prevents you from accidentally creating recursive workflow runs."* The documented workaround — deliberately opting *in* to recursion — is to push using a **GitHub App installation token or a PAT** stored as a secret ([source](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow)). GitHub also blocks `check_run`/`check_suite` from re-triggering when Actions itself created the check suite ([events reference](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows)).
   → **This is the single best mechanism for an agent product.** Events caused by the agent's own identity should be invisible to the agent's own triggers by default, with an explicit, deliberately-inconvenient opt-in.

**2. Per-rule "can this rule trigger other rules?" toggle (Jira).** *"Allow rule trigger to specify whether or not actions in a rule can trigger other rules. By default, to avoid rule execution loops, automation actions in a rule will not trigger other rules"* ([source](https://confluence.atlassian.com/spaces/AUTOMATION/pages/1141480599/Create+and+edit+Jira+automation+rules)). Same policy as #1 but exposed as a **visible checkbox on the rule**, defaulted safe. Making the safety visible teaches the concept.

**3. Depth counter with a hard stop and a dedicated status (Jira).** A loop-detection limit of **10**: *"This controls how many times a flow can trigger itself (or other flows) in quick succession before the execution is stopped and marked as a `LOOP`"* ([service limits](https://support.atlassian.com/cloud-automation/docs/automation-service-limits/)). Critically, **`LOOP` is a first-class audit-log status with its own color in the insights chart** — the user *sees* that they built a loop rather than seeing mysterious throttling.

**4. Categorical prohibition (Notion).** *"Database automations can't be triggered by other automations."* Button-initiated actions may trigger automations; automation-initiated actions may not. Zero configuration, zero expressiveness.

**5. Flow-control primitives keyed on a business identifier (Inngest).** **Debounce** collapses a burst of events over a sliding window into one run; **rate limiting** *skips* (drops) events over a threshold rather than queuing them; **concurrency** caps simultaneous runs scoped by an arbitrary key; **singleton** ensures one run per key ([flow control](https://www.inngest.com/docs/guides/flow-control)). For a PR agent, `debounce({ key: "event.data.pr_id", period: "2m" })` is exactly the right shape for "the human pushed 5 commits, review once."

**6. Concurrency groups with cancel-in-progress (GitHub Actions).** `concurrency: { group: <expr>, cancel-in-progress: true }` — a new run in the same group **cancels the in-flight one** rather than queueing ([workflow syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#concurrency)). Semantically different from debounce: debounce delays, cancel-in-progress supersedes. For a PR agent, "latest push wins, kill the stale review" is usually the desired semantics.

**7. Marker/sentinel fields and content-based suppression (Zapier, Linear).** Zapier's official loop guidance ([Zap is stuck in a loop](https://help.zapier.com/hc/en-us/articles/8496232045453-Zap-is-stuck-in-a-loop)) is entirely manual and entirely userland: **turn the Zap off immediately**, then prevent recurrence with (a) a **marker string** appended by the action plus a `does not contain` Filter, (b) a **"Processed by Zap" field** that the filter requires to be empty, (c) **Find-or-Create instead of Create** in two-way syncs, (d) narrower trigger selection. Zapier has **no automatic loop detection and no loop notification** — a documented gap and an obvious place to beat the incumbent. Linear's `skip`/`ignore` magic words are the elegant version of the same idea.

**Additional hard-stop backstops observed:** Zapier **auto-disables Zaps that error repeatedly**; Jira **disables a flow that exceeds the 5,000-associated-items limit**; Jira caps **60 min of processing per 12 hrs** and surfaces a **"Service limit breached" trigger** so users can automate their own alerting; CircleCI enforces a **hard limit of 100 reruns per workflow**; Inngest **spreads replayed runs over time** to avoid self-inflicted load.

### Recommended loop-protection stack for a PR-review agent **[synthesis]**

Layer these, defaults-on, each with its own visible status:

| Layer | Mechanism | Surfaced as |
|---|---|---|
| 1 | **Actor suppression** — events authored by the agent's own identity don't re-trigger it. Opt-in override behind a warning. | A permanent line in the rule card: *"Ignores events caused by @agent-bot"* with an "allow" toggle |
| 2 | **Debounce keyed on PR id** (~60–120s) so a burst of pushes yields one review | Run status `Debounced`, with a link to the run that absorbed it |
| 3 | **Cancel-in-progress per PR** — a new push supersedes the running review | Superseded run shown as `Canceled — superseded by run #N` |
| 4 | **Depth counter** — max N agent-caused re-triggers per PR (Jira uses 10; for an agent, 3 is more apt), then hard stop | Run status **`Loop stopped`** in its own color, with the chain visualized: *run #1 → PR update → run #2 → PR update → run #3 → stopped* |
| 5 | **Budget ceiling** per PR/day (tokens or dollars) | Status `Budget exceeded`, plus the running total on the rule card |
| 6 | **User escape hatch** — `skip-agent` in PR body/commit, à la Linear's magic words | Documented inline in the rule builder's help text |

The step nobody does that you should: **visualize the causal chain**. When a loop is stopped, show the actual event→run→event→run chain as a small vertical trace with the repeating element highlighted. That's the difference between "your automation was throttled" and "here is the cycle you built."

---

# AREA 2 — Live run monitoring & log streaming UI

## 2.1 The canonical layout for a long-running process

Every mature tool converges on the same three-pane skeleton **[synthesis across GH Actions, Buildkite, CircleCI, Argo, Temporal, Inngest, Trigger.dev, Braintrust, LangSmith]**:

```
┌────────────────┬───────────────────────────────────────┬──────────────┐
│ STEP/JOB LIST  │  DETAIL: logs or trace for selection  │  METADATA    │
│ (nav + status) │  (streaming, searchable, collapsible) │  (in/out,    │
│  + timing      │                                       │   cost, env) │
└────────────────┴───────────────────────────────────────┴──────────────┘
        ▲ status icons          ▲ timing gutter               ▲ tabs
```

with a **graph/waterfall view as an alternate rendering of the left pane**, not a replacement for it.

## 2.2 Tool-by-tool teardown

### GitHub Actions run view — the baseline everyone knows
([Using workflow run logs](https://docs.github.com/actions/managing-workflow-runs/using-workflow-run-logs))
- Left sidebar: **Jobs** list with status icons; alternately the **visualization graph** with dependency edges.
- Each job's log is a **list of collapsible steps** with the **elapsed time shown per step** in a right-aligned gutter.
- **Failed steps auto-expand** — the highest-leverage single behavior in the whole product. You land on the failure without clicking.
- **Search box in the upper-right of the log output** — with the important caveat that **it only searches expanded steps**, a genuinely bad limitation worth beating.
- **Click a line number to get a permalink to that log line** — deep-linkable logs, essential for "here's the bug" in a PR comment.
- Download the full log archive; delete logs; `gh run view RUN_ID --log` for CLI/grep.
- **Re-runs** ([source](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)): **Re-run all jobs**, **Re-run failed jobs**, or a **per-job re-run icon** in the sidebar; an **"Enable debug logging" checkbox** in the re-run dialog turns on runner + step debug logging; re-runs create **attempts**, and a **"Latest ▾" dropdown next to the run name** switches between attempts. 30-day / 50-rerun window. Re-runs execute with the *original triggering actor's* privileges.

### Buildkite — the most thoughtfully designed build page in CI
([Build page](https://buildkite.com/docs/pipelines/build-page), [Waterfall](https://buildkite.com/docs/pipelines/insights/waterfall))
- Three components: **collapsible step sidebar**, main content area with switchable views, and a **resizable, dockable step panel** (right / bottom / center).
- Sidebar groups steps hierarchically with expand/collapse, and **toggles between pipeline order and grouping-by-state — remembering the choice across builds**.
- **Table view** as an alternative that flattens parallel jobs and sorts by duration or name.
- **Step search input** filtering by name/keyword.
- **Keyboard-first triage:** press **`f` to cycle through failures**, **`s`** for step search, **`j`** for **Follow mode** — which auto-focuses the currently-active step and advances as steps complete. This is the best-articulated version of "sticky current step" I found, and it's opt-in via keystroke rather than a fight with the scrollbar.
- Step panel tabs: **Logs / Artifacts / Environment**. Retried steps are marked in the sidebar with an attempt switcher.
- **Waterfall view:** each job is a bar with **three colored segments — gray = waiting for an agent, yellow = dispatch (assigned→started), green/red = actual run (green success, red failure)**. Hover gives the exact breakdown. **Parent rows for group/matrix/parallel steps show a combined bar with nested children beneath.** Supports up to 3,000 jobs / 1,800 steps; wait/block/input steps excluded.
  → Decomposing a bar into *queue vs. dispatch vs. execute* is the pattern to steal for agent runs: **queued / model latency / tool execution** as three segments of one bar instantly answers "why was this slow."
- **Annotations** ([docs](https://buildkite.com/docs/pipelines/configure/annotations)) — steps can emit Markdown that renders **at the top of the build page** in success/info/warning/error styles. This is the "run summary" pattern: the run itself writes its own headline, so triage doesn't require reading logs.

### CircleCI
([Workflows](https://circleci.com/docs/guides/orchestrate/workflows/))
- Pipelines page → workflow **graph/map** showing job dependencies and fan-out/fan-in; real-time status per job.
- A failing job puts the workflow into a **`FAILING`** state while unrelated jobs continue — a distinct state from `FAILED`, which is a useful nuance (partial failure in progress).
- **Rerun from failed** (dedicated icon on both pipelines and workflows pages), **rerun from start**, **rerun with SSH** — the last of which drops you into the actual container ([SSH debugging](https://circleci.com/docs/guides/execution-managed/ssh-access-jobs/)). Hard limit of 100 reruns per workflow.

### Vercel runtime logs — the best filter-facet design in the sample
([Runtime Logs](https://vercel.com/docs/logs/runtime))
- Log rows show execution, domain, HTTP status, function type, RequestId. Logs are **grouped per request**, which is the correct unit (a run, not a line).
- **Live mode** in the timeline filter follows logs in real time; a **"Show New Logs"** button at the bottom loads newly arrived rows on demand rather than yanking the viewport — the *pull-based* alternative to auto-scroll, and arguably better than pause-on-scroll-up because it never moves under you.
- **Severity coloring is derived, not just declared:** `stdout`→info, `stderr`→error, `console.warn`→warning (streaming) or error (non-streaming); additionally **4xx responses are marked Warning (amber) and 5xx Error (red)** at the *request* level. Deriving request-level severity from status codes so the list is scannable without opening rows is a strong idea.
- **Facet sidebar**: Timeline, Level, Route (pattern `/blog/[slug]`) vs. **Request Path** (concrete `/blog/my-post`) — the pattern-vs-instance distinction is explicitly designed and documented, Host, Deployment, Resource (function / middleware / CDN cache / rewrite / redirect), Request Type (api, ssr, isr, ppr, rsc, cron), Method, Cache status, Status Code, Environment, Branch — **plus "logs from your browser,"** which filters to requests matching your own IP + User-Agent. That last one is a delightful, cheap affordance for debugging your own activity in a noisy stream.
- Search bar supports typed key filters: `route`, `requestPath`, `level`, `resource`, `host`, `deployment`, `deploymentId`, `method`, `cache`, `status`, `requestId`, `environment`, `branch`, `sessionId`, `traceId`, `invocationId`.
- Right-side **log detail panel** includes a **timeline of events during the request with timings**, **outgoing sub-requests made during execution**, function metadata (name, region, runtime, duration, memory, cold/warm start), and the chronological log messages at the bottom.
- **Log sharing = copy the URL.** Selection state is in the URL. Non-negotiable for any triage UI.
- Hard limits worth noting for a log-viewer design: 256 lines per request, 256KB per line, 1MB total per request.

### Sentry — the reference for *failure triage*, not log streaming
- **Issue Details** ([docs](https://docs.sentry.io/product/issues/issue-details/)): header with error message + total event count + **users affected**, and the action row (assign / resolve / archive / share). Below: an **event distribution graph** with a search bar and expandable tags. The page defaults to a **"Recommended" event** — chosen by recency (within 7 days), search relevance, and *richness* (has replay, profile, trace) rather than just "latest." **Picking the most *informative* example rather than the most *recent* one is a genuinely good default.** Dropdown to jump to latest/oldest.
- **Stack trace** with the erroring line highlighted; explicitly the input to the grouping algorithm.
- **Breadcrumbs** — "a history and timeline leading up to the error event": HTTP requests, console/server log statements, DOM events.
- Sidebar: first/last seen, linked issues, activity chronology, **seen-by participants**. **Suspect commits** identifies the likely culprit commit and *suggests its author as assignee*.
- **Issue states** ([states & triage](https://docs.sentry.io/product/issues/states-triage/)): New (≤7d) · Ongoing · **Escalating** (exceeding predicted volume) · **Regressed** (previously resolved, came back) · Archived · Resolved. **Archive is conditional**: forever, for a duration, **after N occurrences**, or **after N users affected** — and archived issues **auto-resurface if they become Escalating**. `is:for_review` filters the review queue.
  → **"Snooze until it gets worse" is the pattern to steal.** For agent runs: "ignore this failure class unless it happens >5×/hour."

### Datadog
- **Log side panel** ([docs](https://docs.datadoghq.com/logs/explorer/side_panel/)): upper **context** section (infra/app tags) + lower **content** section (message + pipeline-extracted structured data). Tabs include **Metrics** (infra metrics in a ±30-min window around the log) and **Trace** (the full distributed trace containing this log, upstream and downstream). **"View in Context"** shows the logs immediately before and after this one, correlated by hostname/service/filename/container ID — the "what else was happening right then" affordance. JSON attributes are clickable to add/remove table columns, filter include/exclude, or promote to a facet. **Share as JSON / to email / to Slack.**
- **Live Tail** ([docs](https://docs.datadoghq.com/logs/explorer/live_tail/)): streams **both indexed and non-indexed** logs in near-real-time; when volume exceeds readability it applies **uniform random sampling** so the visible stream stays statistically representative. Sampling-with-a-disclosure beats either dropping silently or drowning the user.

### Grafana Explore — the most complete log-viewer option set
([Logs in Explore](https://grafana.com/docs/grafana/latest/explore/logs-integration/))
- **Log volume histogram** above the rows (native full-range for Loki/Elasticsearch; bucketed count otherwise). The histogram doubles as a time-range brush — scan for the spike, drag to it.
- **Six-level severity coloring with documented synonym mapping**: critical (purple) ← `emerg, emergency, fatal, alert, crit, critical, 0, 1, 2`; error (red); warning (yellow); info (blue); debug (gray); trace (light blue). Publishing the synonym table is why it works across heterogeneous sources.
- Options: sort oldest/newest first, **client-side search within displayed rows**, **deduplication with four modes** (none → exact → numbers → signature), level filtering, timestamp precision (ms/ns/off), **wrap lines** and **prettify JSON**, highlight toggle, font size, export TXT/JSON/CSV.
- **Live tail** streams with new rows appearing at the bottom in a distinct style; controls to **pause** (for manual exploration), **clear**, **resume**, and **exit** back to normal Explore. This is the canonical pause/resume model.
- **Log details expansion** on row click: fields with **positive and negative filter buttons** per field, plus a **stats icon giving ad-hoc distribution of that field across visible logs**.
- **"Escape newlines"** — detects incorrectly-escaped `\n`/`\r`/`\t` and offers one-click fixing, reversible via "Remove escaping." Tiny feature, enormous quality-of-life for anyone piping JSON-wrapped agent output into a log stream.

### Temporal Web UI — multi-fidelity event history
([Web UI](https://docs.temporal.io/web-ui), [Timeline view](https://temporal.io/blog/lets-visualize-a-workflow))
- **Four views of the same event history — Timeline / Compact / All / JSON.** Timeline: *"The Timeline represents the flow of Events in time as your Workflow is executing"*, using **Event Groups** to collapse schedule/start/complete triples into **one row spanning the activity's duration**. **Span color encodes outcome (green completed, red failed)**; **a retry icon with the current attempt number** renders on retrying activities; scroll vertically for more events, horizontally across time, with zoom and Event Type filters; hover tooltips give precise timings.
- **Call Stack** tab (live `__stack_trace` query) answers "where is it *right now*" for an in-flight run — the durable-execution equivalent of a sticky current-step indicator.
- **Pending Activities** section lists in-flight/retrying activities with click-through.
- **Labeling for readability** ([Label your agent steps](https://temporal.io/blog/label-your-agent-steps)) — directly relevant to agent runs. The problem stated: *"When Workflow history grows, each step becomes indistinguishable from a glance"* without opening payloads. The fix: a **`summary`** on each activity (`summary="Plan changes"`), **Static Summary** (≤400 bytes, immutable, "what is this workflow"), **Static Details** (≤20KB, immutable — model name, max iterations), and **Current Details** (≤20KB, **mutable, visible only while running** — e.g. `set_current_details(f"Step {i+1}/{max_iterations}: executing tools")`). Timers get summaries too (`workflow.sleep(..., summary="Wait for CI")`). These render in Timeline, Compact, and a user-metadata tab.
  → **"Current Details" is the sticky-current-step primitive done right**: a mutable, single-line, human-written status string that the run itself updates, shown prominently while running and hidden when done. Steal this exactly.

### Argo Workflows
- DAG rendering of the workflow with clickable nodes; **artifacts appear as elements in the DAG that you can click**, opening a side panel ([artifact visualization](https://argo-workflows.readthedocs.io/en/latest/artifact-visualization/)). Known file types (images, text, HTML) render **inline in an iframe**; a key ending in `/` is treated as a directory and renders `index.html` or a listing; JSON gets syntax highlighting; everything is **sandboxed under a Content-Security-Policy that blocks JavaScript execution**.
  → **Rendering run outputs inline, sandboxed**, rather than making users download them, is the pattern — and the CSP note is the security lesson if you ever render agent-produced HTML.
- Caveat worth designing around: with `TTLStrategy` set, the UI **stops showing workflow details after the TTL elapses** — evaluate logs before then.

### Inngest / Trigger.dev — agent-run-shaped monitoring
- Inngest: runs list + filters + **CEL search across `event.data` and `output`**; three-part detail (trigger → event payload → step timeline); expand a step for error + stack trace + **each retry attempt with its own failure**; actions are replay, **re-run from a step with modified input**, and **send the event to the local Dev Server** ([source](https://www.inngest.com/docs/platform/monitor/inspecting-function-runs)).
- Trigger.dev: **real-time trace view with logs streaming as the task executes**; filters by status/name/environment; run inspector with status → **event timeline** → tabs **Overview** (payload / output / errors), **Detail** (tags + **usage data**), **Context**; replay with modified payload/environment; failure alerts to email/Slack/webhook ([observability](https://trigger.dev/product/observability-and-monitoring), [run inspector](https://trigger.dev/changelog/run-page-inspector)).

### LangSmith & Braintrust — the LLM-agent trace views
**Braintrust** ([How to read a trace](https://www.braintrust.dev/foundations/how-to-read-a-trace)): **two-pane — span tree on the left, selected-span detail on the right**. Hierarchy:
```
eval (root span)          ← top-level input, output, final scores
└─ task
   ├─ Chat Completion     ← LLM span
   └─ Brand Alignment     ← scoring span
```
**LLM spans** show model + parameters, full input messages and output, **prompt tokens and completion tokens separately**, duration, **time-to-first-token**, **caching status**, and **estimated cost**. **Scoring spans** show the score value *and the judge's chain-of-thought reasoning* — so triage of a bad result starts at the scorer span, not the model span. Braintrust's stated triage method: go to the scorer span, read the judge's reasoning, and classify the cause as misunderstood rubric / tonal mismatch / dataset problem — *"a methodical process rather than trial-and-error re-running."*

**LangSmith** ([concepts](https://docs.langchain.com/langsmith/observability-concepts), [cost tracking](https://docs.langchain.com/langsmith/cost-tracking)):
- Vocabulary: **Run** (one unit of work — an LLM call, a prompt format, a retrieval), **Trace** (all runs for one request, one trace ID, **25,000-run cap**), **Thread** (multi-turn session linking traces via `thread_id`), **Trajectory** (*"a flattened view showing the path an agent took from start to finish"* as an ordered message list, **nesting removed**), **Project**, **Feedback** (categorical or continuous scores), tags/metadata.
  → **Trace tree and trajectory as two views of the same run** is the agent-specific analogue of Temporal's Timeline/Compact split: the tree shows *mechanism*, the trajectory shows *narrative*. Offer both.
- **Cost/token surfacing, three levels:** (1) **trace tree** — total for the whole trace, **aggregated at each parent run**, and **individual token/cost per child run**; (2) **project stats panel** — aggregate tokens and spend across all traces; (3) **prebuilt dashboards** with cost broken into input/output, plus custom charts. Token categories are **Input** (with subtypes: cache reads, text, image), **Output** (subtypes: **reasoning tokens**, text, image), and **Other** (tool calls, retrieval, custom). **Hovering a cost figure reveals the subtype breakdown** — e.g. cache-read cost vs. standard text cost.
  → **The rollup-at-every-parent-node pattern is the thing to copy**: every node in the tree shows its own cost *and* its subtree's cost, so you can collapse to the top and still see where the money went.

---

## 2.3 Named patterns — the transferable list

Consolidating across all of the above **[synthesis, each grounded in the cited tool]**:

**Making a long run scannable**
1. **Failed-step auto-expand** — land on the failure, not the top (GH Actions).
2. **Cycle-through-failures keystroke** — `f` (Buildkite).
3. **Follow mode as an opt-in keystroke**, auto-focusing the active step and advancing (Buildkite `j`) — superior to fighting the scroll position.
4. **Pull-based "Show New Logs" button** instead of auto-scroll (Vercel) — the viewport never moves without user action.
5. **Explicit pause / clear / resume / exit controls on the live stream** (Grafana live tail).
6. **Mutable "current details" line written by the run itself** (Temporal `set_current_details`) — e.g. `Step 3/10: running tool read_file`.
7. **Event grouping** — collapse schedule/start/complete into one spanning row (Temporal Compact/Timeline).
8. **Self-authored run summary rendered at the top** (Buildkite annotations, GH Actions job summaries).
9. **Sticky headers for step boundaries** in long logs — near-universal in these products though rarely documented.
10. **Multi-fidelity views over one dataset** — Timeline / Compact / All / JSON (Temporal); tree / trajectory (LangSmith); sidebar / table / canvas / waterfall (Buildkite).

**Search & filter within logs**
11. **In-log search box** (GH Actions — note its expanded-steps-only limitation as an anti-pattern).
12. **Client-side search over displayed rows, separate from server-side query** (Grafana).
13. **Facet sidebar with click-to-include / click-to-exclude per field value** (Datadog, Vercel, Grafana).
14. **Typed key:value search grammar** with the same keys as the facets (Vercel).
15. **Expression escape hatch over structured run data** — CEL over `event.data` / `output` (Inngest).
16. **Deduplication modes** (none/exact/numbers/signature) for repetitive output (Grafana).
17. **"View in context"** — the N logs before and after this one, same host/service/file (Datadog).
18. **Ad-hoc field statistics** from the expanded row (Grafana).

**Severity & timing**
19. **Documented synonym→level mapping** with a fixed 6-color palette (Grafana).
20. **Derived request-level severity** from status codes, so the *list* is colored without opening rows (Vercel: 4xx amber, 5xx red).
21. **Timing gutter**: per-step elapsed time right-aligned (GH Actions).
22. **Multi-segment duration bars**: queue → dispatch → execute, color-coded, hover for exact breakdown (Buildkite waterfall).
23. **Retry badge with attempt number rendered on the span itself** (Temporal).

**Deep-linking & sharing**
24. **Click line number → permalink to that log line** (GH Actions).
25. **Selection state in the URL; sharing = copy URL** (Vercel).
26. **Share as JSON / email / Slack** from the detail panel (Datadog).

## 2.4 How cost and token usage get surfaced

- **Per-span**: prompt vs. completion tokens separately, estimated cost, duration, **time-to-first-token**, cache-hit status (Braintrust).
- **Rolled up at every ancestor**: each parent run shows its subtree's total (LangSmith trace tree).
- **Categorized with hover-to-expand subtypes**: Input (cache reads / text / image) · Output (**reasoning tokens** / text / image) · Other (tools, retrieval) (LangSmith).
- **Project/environment aggregate panel** + prebuilt and custom dashboards for spend-over-time (LangSmith project stats).
- **In a run-detail tab labeled "usage"** alongside tags (Trigger.dev Detail tab).
- **Non-LLM analogue worth noting**: Make surfaces **operations consumed** as a first-class column in run history, right next to duration — because operations are the billing unit. The lesson: **put the billing unit in the run list, not buried in the detail panel.**

For an agent product, the minimum viable set: **cost + tokens on every span, rolled up to the parent, with input/output/reasoning/cache split available on hover, plus a per-run total in the runs *list* column.** **[synthesis]**

## 2.5 How a failed run gets triaged — the composite flow

1. **Land pre-scrolled to the failure** (GH Actions auto-expand; Buildkite `f`).
2. **Read the run's own summary/annotation first** (Buildkite annotations) before reading logs.
3. **See the error inline on the step, with the stack trace and every retry attempt's individual error** (Inngest).
4. **See where in the graph it broke** — Make re-renders the scenario with progress markers and highlights the failing module; Argo/CircleCI/GH color the failing DAG node; Temporal colors the failing span red.
5. **Correlate outward** — jump to the containing trace (Datadog Trace tab), the surrounding logs (Datadog View in Context), the suspect commit and its author (Sentry).
6. **Classify the failure with a status that isn't just "failed"** — Zapier's `Handled error` / `Safely halted` / `Filtered`; Jira's `NO ACTIONS` / `LOOP` / `THROTTLED`; CircleCI's `FAILING` (in-progress partial) vs `FAILED`.
7. **Reproduce with the real input** — n8n "Debug in editor" (auto-pins the failed payload), Inngest "send event to local Dev Server", Zapier "use a previous run as test record", CircleCI **rerun with SSH**.
8. **Re-run narrowly** — re-run failed jobs only (GH Actions, CircleCI "rerun from failed"), re-run from a specific step with modified input (Inngest), replay with a modified payload (Trigger.dev).
9. **Fix, then bulk-remediate** — Inngest Function Replay: name the replay after the incident, pick a time range and statuses, and let the platform spread the re-runs out over time.
10. **Suppress the noise without losing it** — Sentry conditional archive ("until it happens 100 more times / affects 50 more users"), auto-resurfacing on escalation; Make's Incomplete Executions tab as a visible DLQ.
11. **Route ownership automatically** — Sentry suggests the suspect commit's author as assignee; Airtable emails the last person who *enabled* the automation.

---

## 2.6 Concrete recommendations if you're building this

**Rule builder** **[synthesis]**
- Rule rows, not a canvas. Literal **WHEN / IF / THEN** section labels (Sentry).
- Trigger picker with the three-level `event → activity types → filters` narrowing (GH Actions). For PR agents, make `opened` vs `synchronize` vs `review submitted` visually distinct, and **default to `opened` only**.
- Conditions as `field | operator | value` rows with type-prefixed operators, `+ And` inline and `Add Or group` as a heavier separate action (Zapier).
- Interpolation-only templating (`{{pr.author}}`), never control flow in strings (Jira smart values).
- **Backtest button**: "this rule would have fired N times in the last 7 days," listing the actual events. Nobody offers this; it's the strongest dry-run available for an event-driven product.
- Rule card shows **fired count + outcome-class breakdown** inline, using the Jira color-by-outcome chart at rule scale.

**Loop protection** — layer actor-suppression → debounce-by-PR → cancel-in-progress → depth counter → budget ceiling → user opt-out keyword, and give **`Loop stopped`** its own status, its own color, and a rendered causal chain.

**Run view** — three panes; multi-fidelity (timeline / tree / trajectory / raw); failed step auto-expanded; per-step timing gutter with queue/model/tool segmentation; cost+tokens on each span rolled up to parents; `f`-to-next-failure; pull-based "load new logs"; a mutable current-step line the agent writes itself; permalinkable log lines; and a status vocabulary richer than pass/fail.

---

## Sources

**Area 1**
- [GitHub — Events that trigger workflows](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows)
- [GitHub — Trigger a workflow (incl. GITHUB_TOKEN recursion prevention)](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow)
- [GitHub — Workflow syntax: concurrency](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#concurrency)
- [GitHub — Use the visualization graph](https://docs.github.com/en/actions/how-tos/monitor-workflows/use-the-visualization-graph)
- [GitHub Changelog — Workflow visualization](https://github.blog/changelog/2020-12-08-github-actions-workflow-visualization/)
- [Linear — Triage](https://linear.app/docs/triage)
- [Linear — GitHub integration & PR automations](https://linear.app/docs/github)
- [Linear Docs index](https://linear.app/docs)
- [Atlassian — Create and edit Jira automation rules](https://confluence.atlassian.com/spaces/AUTOMATION/pages/1141480599/Create+and+edit+Jira+automation+rules)
- [Atlassian — Jira automation triggers](https://support.atlassian.com/cloud-automation/docs/jira-automation-triggers/)
- [Atlassian — Performance insights for automation rules](https://confluence.atlassian.com/automation/view-performance-insights-for-automation-rules-1013851985.html)
- [Atlassian — Automation service limits (LOOP / THROTTLED)](https://support.atlassian.com/cloud-automation/docs/automation-service-limits/)
- [Atlassian — Troubleshoot automation rules](https://confluence.atlassian.com/automation/troubleshoot-automation-rules-1141480666.html)
- [Atlassian — Using the audit log](https://support.atlassian.com/automation/kb/using-the-audit-log/)
- [Atlassian Community — A new look for the Rule Builder](https://community.atlassian.com/forums/Automation-articles/A-new-look-for-the-Rule-Builder-for-Automation-is-coming-soon-%EF%B8%8F/ba-p/2507226)
- [Zapier — Learn key concepts in Zaps](https://help.zapier.com/hc/en-us/articles/8496181725453-Learn-key-concepts-in-Zaps)
- [Zapier — Test Zap steps](https://help.zapier.com/hc/en-us/articles/18811411817741-Test-Zap-steps)
- [Zapier — Review Zap run statuses](https://help.zapier.com/hc/en-us/articles/20505304170637-Review-Zap-run-statuses)
- [Zapier — Add conditions with filters](https://help.zapier.com/hc/en-us/articles/8496276332557-Add-conditions-to-Zaps-with-filters)
- [Zapier — Paths](https://help.zapier.com/hc/en-us/articles/8496288555917-Add-conditions-to-Zaps-with-filters)
- [Zapier — Zap is stuck in a loop](https://help.zapier.com/hc/en-us/articles/8496232045453-Zap-is-stuck-in-a-loop)
- [Zapier — Replay Zap runs](https://help.zapier.com/hc/en-us/articles/8496241726989-Replay-Zap-runs)
- [Zapier — Use a previous Zap run as a test record](https://help.zapier.com/hc/en-us/articles/42896259263373-Use-a-previous-Zap-run-as-a-test-record)
- [n8n — Pin and mock data](https://docs.n8n.io/build/work-with-data/pin-and-mock-data)
- [n8n — Debug executions](https://docs.n8n.io/build/understand-workflows/understand-executions/debug-executions/)
- [Make — Incomplete executions](https://help.make.com/incomplete-executions)
- [Make — Manage incomplete executions](https://help.make.com/manage-incomplete-executions)
- [Make — Scenario settings](https://help.make.com/scenario-settings)
- [Make scenario history walkthrough (3rd party)](https://consultevo.com/make-com-scenario-history-guide/)
- [Inngest — Inspecting function runs](https://www.inngest.com/docs/platform/monitor/inspecting-function-runs)
- [Inngest — Function Replay](https://www.inngest.com/docs/platform/replay)
- [Inngest — Flow control](https://www.inngest.com/docs/guides/flow-control)
- [Trigger.dev — Observability & monitoring](https://trigger.dev/product/observability-and-monitoring)
- [Trigger.dev — Run inspector improvements](https://trigger.dev/changelog/run-page-inspector)
- [Slack — Introducing conditional branching in Workflow Builder](https://slack.dev/introducing-conditional-branching-in-workflow-builder/)
- [Slack — Branching workflows](https://slack.dev/branching-workflows/)
- [Slack — View workflow activity logs](https://slack.com/help/articles/360055655493-View-workflow-activity-logs)
- [Notion — Database automations](https://www.notion.com/help/database-automations)
- [Notion — Database buttons](https://www.notion.com/help/database-buttons)
- [Thomas Frank — Notion Database Automations complete guide](https://thomasjfrank.com/notion-database-automations-the-complete-guide/)
- [Airtable — Getting started with automations](https://support.airtable.com/docs/getting-started-with-airtable-automations)
- [Airtable — Troubleshooting automations](https://support.airtable.com/articles/6756755850-troubleshooting-airtable-automations)
- [Sentry — Issue alert configuration (WHEN/IF/THEN)](https://docs.sentry.io/product/alerts/create-alerts/issue-alert-config/)

**Area 2**
- [GitHub — Using workflow run logs](https://docs.github.com/actions/managing-workflow-runs/using-workflow-run-logs)
- [GitHub — Re-run workflows and jobs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)
- [GitHub — Viewing workflow run history](https://docs.github.com/actions/managing-workflow-runs/viewing-workflow-run-history)
- [GitHub Blog — Save time with partial re-runs](https://github.blog/news-insights/product-news/save-time-partial-re-runs-github-actions/)
- [Buildkite — Build page](https://buildkite.com/docs/pipelines/build-page)
- [Buildkite — Waterfall view](https://buildkite.com/docs/pipelines/insights/waterfall)
- [Buildkite — Annotations](https://buildkite.com/docs/pipelines/configure/annotations)
- [Buildkite — New build page changelog](https://buildkite.com/resources/changelog/266-introducing-the-new-build-page-engineered-for-scale-and-flexibility/)
- [CircleCI — Workflow orchestration](https://circleci.com/docs/guides/orchestrate/workflows/)
- [CircleCI — Debug with SSH](https://circleci.com/docs/guides/execution-managed/ssh-access-jobs/)
- [Vercel — Runtime logs](https://vercel.com/docs/logs/runtime)
- [Vercel — Observability](https://vercel.com/docs/observability)
- [Sentry — Issue details](https://docs.sentry.io/product/issues/issue-details/)
- [Sentry — Issue states & triage](https://docs.sentry.io/product/issues/states-triage/)
- [Sentry — Issue Views](https://docs.sentry.io/product/issues/issue-views/)
- [Datadog — Log side panel](https://docs.datadoghq.com/logs/explorer/side_panel/)
- [Datadog — Live Tail](https://docs.datadoghq.com/logs/explorer/live_tail/)
- [Datadog — Log facets](https://docs.datadoghq.com/logs/explorer/facets/)
- [Grafana — Logs in Explore](https://grafana.com/docs/grafana/latest/explore/logs-integration/)
- [Temporal — Web UI](https://docs.temporal.io/web-ui)
- [Temporal — Workflow visualization with Timeline View](https://temporal.io/blog/lets-visualize-a-workflow)
- [Temporal — Label your agent steps](https://temporal.io/blog/label-your-agent-steps)
- [Temporal — Redesigning the workflow experience](https://temporal.io/blog/the-dark-magic-of-workflow-exploration)
- [Argo Workflows — Artifact visualization](https://argo-workflows.readthedocs.io/en/latest/artifact-visualization/)
- [Argo Workflows — Quick start](https://argo-workflows.readthedocs.io/en/latest/quick-start/)
- [Argo Workflows 3.6 release notes](https://blog.argoproj.io/argo-workflows-3-6-aa037cd782be)
- [Braintrust — How to read a trace](https://www.braintrust.dev/foundations/how-to-read-a-trace)
- [Braintrust — Examine traces](https://www.braintrust.dev/docs/observe/examine-traces)
- [Braintrust — How to track LLM token usage (2026)](https://www.braintrust.dev/articles/how-to-track-llm-token-usage-2026)
- [LangSmith — Observability concepts](https://docs.langchain.com/langsmith/observability-concepts)
- [LangSmith — Cost tracking](https://docs.langchain.com/langsmith/cost-tracking)

---


# Part 4 — Project/wiki information architecture and agent-readable docs

# Information Architecture & UI Patterns for a Lightweight Agentic Project Workspace

Research brief for: `projects → (agents, github repo, kanban tickets, markdown wiki, event triggers, agent run monitoring)`

---

## 0. Executive synthesis — the five load-bearing decisions

Before the detail, the five findings that most constrain your design:

1. **Agents should be a *delegation* relationship, not an assignee value.** Linear's single most-copied decision: assigning an issue to an agent "delegates the issue to that agent **while the human teammate remains the primary assignee and owner**." Two fields, not one. This solves accountability, notification routing, and "who do I ask about this" in one move. ([Linear Docs — AI Agents](https://linear.app/docs/agents-in-linear))
2. **Agent runs need a first-class, spec'd activity model — not a log blob.** Linear defines exactly five activity types (`thought`, `action`, `elicitation`, `response`, `error`) and six session states (`pending`, `active`, `awaitingInput`, `error`, `complete`, `stale`), with the platform deriving state from the last emitted activity. Copy this schema more or less verbatim. ([Linear — Agent Interaction](https://linear.app/developers/agent-interaction))
3. **The wiki's minimum viable form is a shallow page tree + full-text search + `@`-mention backlinks.** Every tool that added more (Confluence's deep trees, Notion's database-backed wikis) generated a documented navigation-decay problem. Depth is the enemy; two levels is enough for v1.
4. **Agent-readable docs need a *scoping* mechanism, and the industry has converged on three:** always-on, glob/path-triggered, and semantically-retrieved-by-description. Cursor, Claude Code, OpenHands and Devin independently landed on the same trichotomy. Your wiki pages should carry that scope as front-matter.
5. **Empty state is the product for the first 10 minutes.** The consistent winner across research: *one* primary CTA, everything else dimmed, plus an optional labeled demo/template path. Never a blank canvas with a toolbar.

---

## 1. Linear — issue/project IA, boards, detail panel, agents

### 1.1 The conceptual model

Source: [linear.app/docs/conceptual-model](https://linear.app/docs/conceptual-model)

```
Workspace                 "the container for all issues, teams and other concepts
  └─ Team                  relating to an individual company"
      ├─ Issue            "the fundamental unit of work"
      │   └─ Sub-issue
      ├─ Cycle            repeating fixed planning period
      └─ Triage           per-team intake inbox
Initiative  (strategic, spans teams)
  └─ Project              "group issues together around a shared outcome"
      ├─ Milestone        "meaningful stages of completion"
      └─ Document
View        "different ways of looking at the same underlying work.
             They do not change the work itself"
```

Two things worth stealing:

- **Views are explicitly framed as non-destructive lenses.** That framing ("they do not change the work itself") is worth putting in your own UI copy — it lowers the stakes of the board-vs-list toggle and makes users willing to experiment with grouping.
- **Teams are the permission/workflow boundary; Projects are the outcome boundary.** For your v1 you almost certainly collapse Team into Project — one workspace, projects as the only container. That's fine, but it means your Project has to absorb *both* jobs: workflow config (statuses, triggers, agent access) and outcome framing (description, docs, board).

**Recommendation for your IA:**

```
Workspace
  └─ Project ◄── the only container. Owns everything below.
      ├─ Overview        (README-ish: description, status, recent runs, pinned docs)
      ├─ Board           tickets, kanban + list toggle
      │   └─ Ticket
      │       └─ Sub-ticket (one level only)
      ├─ Wiki            markdown pages, 2-level tree
      ├─ Repo            one linked GitHub repo (see §5)
      ├─ Agents          roster + per-agent config
      ├─ Runs            agent session monitoring
      └─ Settings        triggers, integrations, members, danger zone
```

### 1.2 Board vs list

Source: [linear.app/docs/display-options](https://linear.app/docs/display-options)

- Toggle is `Cmd/Ctrl+B`; the whole display menu is `Shift+V`.
- **Grouping** by status, assignee, project, priority, cycle, label, team. Board columns *are* the grouping dimension — this is the key architectural insight. A board isn't a separate data structure, it's `group_by` applied to a list with a horizontal layout. **Sub-grouping** renders as rows (i.e. swimlanes) in both list and board.
- Drag-and-drop between groups mutates the underlying property. So dragging a card from "Todo" to "In Progress" when grouped by status sets status; when grouped by assignee, it reassigns. Elegant, and cheap to implement once.
- **Ordering**: manual ordering is the board default, and Linear warns that "manual ordering... will update the manual order for everyone in the workspace" — a shared, not personal, ordering. Worth an explicit decision in your product.
- **Display properties** toggle which fields render on the card (ID, assignee, due date, estimate, labels, PRs). Do not skip this; a card with 8 badges is unreadable and users' needs differ.
- `Set as default` promotes your current display options to the team default. Nice pattern: personal customization by default, explicit promotion to shared.

**v1 verdict:** build list and board as the *same* view with a layout switch and a `group_by`. Sub-grouping/swimlanes: skip (see §2). Display properties: build it, it's cheap and high-value.

### 1.3 Issue detail panel & sub-issues

Sources: [parent-and-sub-issues](https://linear.app/docs/parent-and-sub-issues), [documents](https://linear.app/docs/documents)

- Sub-issues created via `+ Add sub-issues` **below the description**, or `Cmd/Ctrl+Shift+O`. Also: select text in a description, or a comment, or a list item → same shortcut converts it into a sub-issue. This is the best interaction in the whole product for an agentic workspace — an agent writes a plan as a markdown checklist in the description, the human selects it and one-shots it into sub-tickets.
- **Inheritance rules (specific, and worth copying exactly):** sub-issues inherit team, priority, and project. Cycle is inherited only if created in an active status. Assignee inherited only if you're assigned to the parent or all existing sub-issues share that assignee. **Labels are not inherited.**
- Two optional automations: parent auto-closes when all sub-issues are done; sub-issues auto-close when parent closes. Both off by default.
- Filters include "only parent issues", "issues with sub-issues", "sub-issues only", and "hide completed sub-issues" — the last is default-on and matters a lot for readability.
- Properties sidebar toggles with `Cmd/Ctrl+I`. Same editor is used for issue descriptions and documents — one editor component, reused. Slash commands, inline comments (`Cmd+Opt+M`), `@`-mentions that reference issues/docs/people, links to specific headers within a document, full version history.

**v1 verdict:** one level of sub-tickets, no deeper. Ship the "selected text → sub-tickets" conversion; it is the highest-leverage agent↔human handoff in the product. Ship the two auto-close automations as project settings.

### 1.4 Keyboard-first command menu

Sources: [fastshortcuts.com/shortcuts/linear](https://fastshortcuts.com/shortcuts/linear/), [linear.app/now/invisible-details](https://linear.app/now/invisible-details)

The shape of Linear's keyboard grammar:

| Class | Keys | Notes |
|---|---|---|
| Command menu | `Cmd+K` | "perform any action" — context-sensitive to selection |
| Help | `?` | shortcut cheatsheet overlay |
| Search | `/` | global |
| Navigate | `G` then `I/M/E/P/T/B/S/...` | Inbox, My issues, All, Projects, Teams, Backlog, Settings |
| Open entity | `O` then `I/P/C/T/U` | jump-to-entity picker by kind |
| Create | `C` new issue, `Alt+C` from template, `Cmd+Shift+O` sub-issue | |
| Move in list | `J`/`K`, `Space` = peek without opening, `Enter` = open | |
| Select | `X` toggle select, `Cmd+A` all, then right-click for bulk | |
| Mutate | `S` status, `P` priority, `A` assign, `I` assign self, `L` labels, `Shift+D` due date, `E` edit, `R` rename, `D` duplicate | |
| Priority | `Shift+1..4` | urgent/high/medium/low |

Design principles embedded in that table, in order of importance for you:

1. **Single-letter mutators are the entire point.** `S`, `P`, `A`, `L` acting on the hovered-or-selected row is what makes the app feel fast. Multi-key chords are for navigation, single keys for mutation.
2. **Prefix namespaces** (`G` = go, `O` = open) keep the single-letter space free for mutators.
3. **`Space` to peek** — a preview overlay that doesn't lose your list position — is underrated and cheap.
4. **Contextual menus display their own shortcuts** as an ambient teaching mechanism ("`C` to create issues", "`Cmd/Ctrl+Shift+M` to move issues between teams"). Users learn the keyboard grammar from the mouse UI.
5. Linear's craft post details a **triangular cursor safe-area** for submenus, implemented as a `clip-path` polygon (~40 lines of React), so diagonal mouse travel toward a submenu item doesn't dismiss it. Small, but it's the difference between "feels expensive" and "feels like a CRUD app."

**Your agent-specific additions to the command menu:** `Cmd+K → "Delegate to..."` and `Cmd+K → "Run agent on..."` need to be first-class verbs, not buried. Linear puts the whole agent chat behind `Cmd/Ctrl+J` — a dedicated second palette for conversation, separate from `Cmd+K` for commands. That split is worth copying: **`Cmd+K` = deterministic actions, `Cmd+J` = talk to an agent.**

### 1.5 Triage / intake inbox

Source: [linear.app/docs/triage](https://linear.app/docs/triage)

A dedicated inbox (`G` `T`) for work created outside the normal flow — from integrations (Slack, Sentry), from non-members, or from other teams. Four actions with number-key shortcuts:

| Action | Key | Effect |
|---|---|---|
| Accept | `1` | move to team's default status |
| Mark duplicate | `2` or `M M` | merge into existing issue, **transfers attachments** |
| Decline | `3` | status → Canceled, optional explanation |
| Snooze | `H` | hide until a time **or until new activity** |

Also: **triage responsibility** can be assigned to a person or rotated automatically off a PagerDuty/OpsGenie/Rootly/incident.io schedule.

**Why this matters enormously for an agentic workspace:** when agents and event triggers can create tickets, you get a firehose. Triage is the correct architectural answer — a quarantine queue between "something happened" and "the board." Your event triggers should default to *creating into triage*, not creating onto the board. And the `snooze until new activity` semantic is exactly right for agent-generated tickets.

**v1 verdict: build triage.** It is not a scale feature here; it is the pressure valve that makes automatic ticket creation safe. Four number-key actions, one screen.

### 1.6 Agents in Linear — the delegation model

Sources: [docs/agents-in-linear](https://linear.app/docs/agents-in-linear), [developers/agents](https://linear.app/developers/agents), [developers/agent-interaction](https://linear.app/developers/agent-interaction), [developers/aig](https://linear.app/developers/aig), [linear.app/agents](https://linear.app/agents), [docs/assigning-issues](https://linear.app/docs/assigning-issues)

**Identity model.** Agents are "app users" that "behave similar to other users in a workspace." They appear in mention menus and filter menus by app name+icon. They cannot sign in, access admin functionality, or manage users. Linear's chat agent additionally "operates within your existing permissions — it can only reference or change content that you already have access to," i.e. the agent borrows the invoking user's authority rather than holding its own.

**Delegation, not assignment.** The critical split, quoted above. Consequences visible in the UI:
- `My Issues` shows both assigned *and* delegated-on-your-behalf work.
- Custom views can filter by `Delegate` as a distinct field from `Assignee`.
- Insights can segment by `Delegate`.
- Assignment/delegation changes land in the issue's Activity feed.
- Agent completion notifications go to **the delegating user**, not a generic channel.

**Two invocation paths:** (a) assign/delegate an issue → fires an `AgentSessionEvent` webhook with `created` and an `agentSession` object carrying issue context and comments; (b) `@mention` in a comment or description.

**Agent Sessions.** "Sessions are created automatically when an agent is mentioned or delegated an issue." Session state is derived by the platform from the last emitted activity — the agent developer never sets state manually. Six states: `pending`, `active`, `awaitingInput`, `error`, `complete`, `stale`.

**Five activity types** (the schema, verbatim from the docs):

```jsonc
{ "type": "thought",     "body": "..." }                                  // internal reasoning
{ "type": "action",      "action": "...", "parameter": "...", "result": "..." }  // a tool invocation
{ "type": "elicitation", "body": "..." }                                  // asks user for input → state: awaitingInput
{ "type": "response",    "body": "..." }                                  // final result → state: complete
{ "type": "error",       "body": "..." }                                  // failure → state: error
```
Plus a `prompt` activity type that only *users* can create (follow-up messages). And **signals**: "optional metadata that modify how an Agent Activity should be interpreted or handled by the recipient."

**Latency contract:** the agent must emit a `thought` **within 10 seconds** to acknowledge the session began. That is a concrete, testable SLA and you should adopt a number like it.

**The Agent Interaction Guidelines** — six principles, all directly applicable:

1. *Identity*: "The agent must signal its identity clearly so that it can never be mistaken for a person."
2. *Native surfaces*: agents act through the same UI actions humans use; no bespoke agent-only interface.
3. *Immediate feedback*: "immediate, but unobtrusive, feedback to reassure the user it has received a request."
4. *Transparent internal state*: "Agents should clearly indicate whether they're thinking, waiting for input, executing, or finished working," and users can inspect reasoning and tool calls.
5. *Disengagement*: an agent must stop when told and only re-engage on an explicit signal.
6. *Human accountability*: "An agent can carry out tasks, but the final responsibility should always remain with a human."

**Guidance / instructions.** Linear provides "Additional Guidance" — "instructions that agents will automatically receive when they work on issues in your workspace," covering repository preferences, commit conventions, review processes. **Three scopes with defined precedence: workspace → team (team wins) → personal** (Settings → AI, and Settings → Agent personalization). This is the same pattern as CLAUDE.md/Cursor rules, but surfaced as a settings textarea rather than a file. See §6 — you want *both*.

**Coding sessions UI** ([changelog 2026-06-11](https://linear.app/changelog/2026-06-11-coding-sessions)): sessions render as **cards in the issue activity feed** showing status, a vertically-centered label, and preview text; expanding shows the full activity stream. Completion "returns a new diff for review," with preview links shareable and iteration happening "in the same thread." Admins get a **usage dashboard** for access, usage and credits. Linear reports resolving ~30% of incoming bug reports this way, mostly first-pass.

**v1 verdict — copy nearly all of it:**
- Ticket has `assignee` (human) + `delegate` (agent). Never one polymorphic field.
- Session model with the five activity types and six states, state derived server-side.
- Agent session renders as an expandable card inline in the ticket's activity feed, plus a row in the project-level Runs view. Same component, two placements.
- A `thought`-within-N-seconds acknowledgment contract.
- Elicitation → `awaitingInput` is the single most important state: it is how an agent asks a question without failing, and it's what makes the Runs view actionable rather than passive.

---

## 2. Jira / Trello / Height / Shortcut — what's real vs bloat

### 2.1 Jira

Source: [atlassian.com/agile/tutorials/how-to-do-kanban-with-jira](https://www.atlassian.com/agile/tutorials/how-to-do-kanban-with-jira)

- **Columns map to statuses**, configured in Board configuration → Columns. Default kanban workflow: Backlog / Selected for Development / In Progress / Done. Columns can hold multiple statuses.
- **WIP limits**: min *or* max per column, optionally excluding subtasks from the count. Violation surfacing: "the columns would be colored red at the top." That's the whole mechanism — a colored column header. Cheap.
- **Swimlanes**: default config ships an "Expedite" lane plus an "Everything Else" lane. Custom lanes are defined **by JQL queries**. That's powerful and also exactly where the complexity cliff is.
- **Kanban backlog** as a separate tab: "a bigger and dedicated space to freely build and prioritize the backlog, without distracting the team from their current work." This backlog/board split is genuinely load-bearing and cheap — it's just a status filter with its own route.

### 2.2 WIP limits — worth it?

Source: [teamhood.com/kanban-resources/kanban-wip-limits](https://teamhood.com/kanban-resources/kanban-wip-limits/)

- Standard starting heuristic: **team size + 1**. Test for 2–4 weeks, then adjust based on whether "tasks pile up," people are "overwhelmed," or people "run out of work."
- Represented as a number on the column header; can also be applied per-swimlane so urgent work bypasses the limit.

**Verdict for an agentic v1: WIP limits are worth building, but reinterpret them.** In a human kanban, WIP limits are a social contract. In an agent workspace they become a **concurrency governor** — a real, enforceable cap on how many agent runs can be in-flight on a project at once. That version has teeth: it's cost control, rate-limit protection, and merge-conflict avoidance. Render it exactly like Jira does (number in the column header, header turns red/amber over limit), but for the "In Progress"/"Agent Running" column make it *enforcing*, not advisory.

**Swimlanes: cut from v1.** JQL-defined lanes are the canonical example of a feature that turns a board into a configuration surface. If you want the 80% value at 5% cost, ship a single hard-coded lane split you control — e.g. **"Needs your input" pinned above everything else**, auto-populated from tickets whose agent session is in `awaitingInput`. That is the one swimlane that earns its rent in this product, and it maps to Jira's "Expedite" lane conceptually.

**Customizable columns: yes, but constrained.** Let projects add/rename/reorder statuses and assign each a *category* (`backlog` / `started` / `completed` / `canceled`). The category is what automations, triggers, and progress rollups key off, so the user can rename freely without breaking logic. This is the single most important schema decision in the board: **never let automations reference a status by name.**

### 2.3 Trello

Sources: [trello.com/guide/trello-101](https://trello.com/guide/trello-101), [List Limits Power-Up](https://trello.com/power-ups/5c2462c384ab8949b1724a20), [nira.com/trello-review](https://nira.com/trello-review/)

Trello's lesson is mostly negative-space: board/list/card with almost no schema, everything else via Power-Ups. It wins on time-to-first-value and loses on any query that spans boards. Note that even **WIP limits are a Power-Up** ("List Limits"), not core — Trello's judgment was that the median board doesn't need them. Reviews consistently flag the scaling ceiling: no cross-board rollup, no dependency modeling, card detail becomes a scroll of checklists and comments.

**Steal:** the card front is minimal and every badge on it is *earned* (a comment count appears only if there are comments). **Avoid:** letting the ticket detail become an unordered pile. Give it fixed regions.

### 2.4 Height

Source: [freshvanroot.com/blog/height-app-review](https://freshvanroot.com/blog/height-app-review/)

Height's distinctive moves, all relevant:

- **Four layouts over the same list — spreadsheet, kanban, calendar, gantt — and critically "all views remain editable."** You don't drop back to a table view to change a field.
- **Typed custom attributes** (date, user, single-select, multi-select) that "remain fully functional across all views, filters, and automations." One attribute system, no per-view special cases.
- **The task detail is a single chat stream that merges human discussion and the automatic changelog** — attribute changes are interleaved as messages in the same feed as comments. For an agent workspace this is precisely right: agent thoughts, tool calls, status changes, and human replies belong in one chronological thread, not in a "Comments" tab next to an "Activity" tab.
- `Cmd+P` search across everything; results savable as **Smart Lists** (dynamic saved filters that can span multiple lists). `Cmd+K` command bar.

**Steal hard:** the unified activity-and-chat stream on the ticket. It's the natural home for agent sessions and it eliminates the comments/activity tab split that every other tool regrets.

### 2.5 Shortcut

Sources: [shortcut.com/help](https://www.shortcut.com/help/), [Epic Workflow States](https://help.shortcut.com/hc/en-us/articles/360046059412-Epic-Workflow-States), [Transition from Linear](https://www.shortcut.com/help/getting-started/transition-from-linear/)

Model: Story → Epic → Milestone, with **separate workflow state machines for stories and for epics**. Stories carry native git integration (branch name generation, PR linking, auto state transition on merge). Multiple workflows per workspace, each with its own ordered states, each state typed as Unstarted / Started / Done.

**Steal:** the state-*type* concept (identical to what I recommended above), and the git-native story fields — a **"Create branch" button that copies a conventional branch name** derived from ticket id + slug is a two-hour feature with outsized payoff in a repo-linked workspace.

### 2.6 Kanban keep/cut table for your v1

| Feature | Verdict | Rationale |
|---|---|---|
| Custom columns (add/rename/reorder) | **Keep** | Table stakes; make each column carry a fixed *category* |
| Status categories (backlog/started/done/canceled) | **Keep** | Automations must key off category, never name |
| Board ⇄ list toggle over same query | **Keep** | One data path, two layouts |
| `group_by` as the board's column axis | **Keep** | Board is a lens, not a structure |
| Display properties (which badges on card) | **Keep** | Cheap, high value |
| Separate Backlog tab | **Keep** | Just a filtered route |
| WIP limits | **Keep, reinterpreted** | As an agent-concurrency governor with a red header |
| "Needs your input" pinned lane | **Keep** | The one swimlane that earns its cost |
| Generic/JQL swimlanes | **Cut** | Config-surface explosion |
| Sub-grouping | **Cut** | Ship after grouping proves out |
| Estimates / story points / velocity | **Cut** | Meaningless when agents do the work |
| Cycles/sprints | **Cut** | Agent throughput isn't sprint-shaped |
| Custom fields | **Defer** | Powerful (Height) but a schema/migration commitment |
| Timeline/Gantt | **Cut** | No dependency model in v1 |
| Multiple boards per project | **Cut** | One project = one board; saved filters cover the need |

---

## 3. The minimum viable markdown wiki

### 3.1 Page tree vs flat + tags — the actual evidence

The Obsidian community's synthesis ([forum thread](https://forum.obsidian.md/t/folders-vs-linking-vs-tags-the-definitive-guide-extremely-short-read-this/78468)) is the clearest statement of the tradeoff:

- Folders/trees suit **project-scoped** collections: "One folder = one project, containing all the notes used in that project."
- Links suit **knowledge** collections (Wikipedia-shaped).
- Tags are contested — criticized as "confusing" due to normalization drift ("should you tag *dog*, *dogs*, or *pets*?"), defended by heavy-reference users. The reconciling observation from a commenter: tags and folders are **orthogonal** — "a document can only be in one folder, but it can have multiple tags," so tags are for cross-cutting concerns only.

Confluence supplies the failure mode. The critiques ([Seibert](https://seibert.group/blog/en/best-way-to-organize-confluence-space/), [K15t](https://www.k15t.com/rock-the-docs/confluence-cloud-best-practices/how-to-structure-confluence-content-for-long-term-success)) describe content that "will quickly become an unsearchable maze of information, the antithesis of collaboration" — the filing-cabinet-with-no-alphabetization problem. The recommended mitigations are telling because they're all *compensating* for the tree: space homepages as curated landing pages, `Content by Label` macros to re-aggregate what the tree scattered, and designated human "Confluence Gardeners" to maintain structure. If your IA requires a staffed gardener, the IA is wrong.

Notion's version of the same failure is the **"Craigslist Effect"** ([Notion Mastery](https://notionmastery.com/designing-for-notion-the-craigslist-effect/)): a workspace degrades into an undifferentiated list of links where "the user either has to read the page to know what options are available... or click around to see if a page contains the information they are looking for," producing "search-based design" — you can only find things you already know exist. The prescriptions: "design for scanning, not reading," and use curated dashboards as entry points rather than one monolithic hub.

**Conclusion: for a *project-scoped* wiki — which yours is — a shallow tree is correct**, because the project boundary already does the hard partitioning work that makes trees fail at org scale. Cap it at two levels.

### 3.2 What each tool actually ships

**Outline** ([getoutline.com](https://www.getoutline.com/), [docs.getoutline.com/s/guide](https://docs.getoutline.com/s/guide)) — the closest analogue to what you want. Collections (top-level, permissioned) → nested documents, unlimited depth. Editor is "a blazing fast editor with markdown support, slash commands, interactive embeds." Search across the workspace with AI Q&A over documents. Backlinks, templates, public link sharing, an open API, 20+ integrations. Emphasis on "millisecond response times." **This is the best single reference implementation for a minimal team wiki.**

**Notion** ([sidebar help](https://www.notion.com/help/navigate-with-the-sidebar), [wikis & verified pages](https://www.notion.com/help/wikis-and-verified-pages)) — sidebar split into Favorites / Teamspaces / Shared / Private, sections individually collapsible, nesting with "no limit." The **wiki feature** is the interesting part: `•••` → "Turn into wiki" converts a page (not a database) into a wiki, which auto-generates three views — **Home** (freeform blocks + drag-ordered page list), **All pages** (database view of every page), **Pages I own** (filtered). Pages gain **Owner** and **Verification** properties; verified pages show "a blue check mark next to their name when they're @-mentioned, and when they're displayed in Notion search results." **Verification can be set to expire**, and on expiry "page owners will be notified in their Notion inbox as well as via email."

> **This is the single most transferable wiki idea for an agentic workspace.** If agents read your wiki, stale pages are actively dangerous — they don't get the eye-roll a human gives an obviously outdated doc. Per-page **owner + verified-until date + expiry nudge** is a small feature that directly controls the blast radius of documentation rot. Consider making verification status visible *to the agent* ("this page was last verified 2026-03-01") and/or excluding expired pages from always-on context.

**Confluence** — spaces → page tree, labels for cross-cutting aggregation, templates/blueprints that auto-apply labels, page properties. Recommendation from K15t: consistent top-level structure across spaces so "contributors intuitively understand new spaces once they learn one." Good advice for a project template.

**Obsidian Publish / Obsidian** ([backlinks docs](https://obsidian.md/help/plugins/backlinks)) — the backlinks pane distinguishes **linked mentions** ("backlinks to the notes that contain an internal link to the active note") from **unlinked mentions** ("any unlinked occurrence of the name of the active note"). Renders either in the right sidebar or, with "Backlink in document" enabled, **at the bottom of the note itself**. Options: collapse/expand per source note, "Show more context" to display the full containing paragraph rather than a truncated line, sort order, and a text filter over mentions.

> **Unlinked mentions are the cheap magic trick.** Full-text search for the page title across the wiki, list the hits, offer a one-click "link it." It creates the graph without asking anyone to maintain a graph. And *"show more context = full paragraph"* is the detail that makes a backlinks list actually readable — a bare list of titles is nearly useless.

**GitBook** ([content structure](https://docs.gitbook.com/creating-content/content-structure), [git-sync content configuration](https://github.com/GitbookIO/public-docs/blob/main/getting-started/git-sync/content-configuration.md)) — Organization → Spaces → page groups → pages (nestable). Bidirectional Git Sync where **`SUMMARY.md` is the source of truth for navigation order and hierarchy** and `.gitbook.yaml` maps repo paths to space content, with monorepo support. If your wiki pages live in (or sync to) the linked repo, `SUMMARY.md` is the proven serialization format for "the tree" and is human-editable in a PR.

**Docusaurus** ([sidebar docs](https://docusaurus.io/docs/sidebar)) — "Docusaurus can create a sidebar automatically from your filesystem structure: each folder creates a sidebar category, and each file creates a doc link." Ordering and titling overridden by `sidebar_position` front-matter and `_category_.json`; breadcrumbs at the top; collapsible/hideable sidebar.

> **The convention-over-configuration lesson:** derive the tree from the filesystem, allow front-matter to override position and label. Zero-config for the lazy path, full control when needed. That's the right default for a wiki whose pages are markdown files in a repo.

### 3.3 The minimum viable wiki — concrete spec

**Data model:** a page is a markdown file with YAML front-matter.

```yaml
---
title: Deployment runbook
slug: deployment-runbook
parent: engineering          # null = top level; max one level of nesting
tags: [ops, oncall]
owner: @dana
verified_until: 2026-11-01
agent_scope: auto            # always | auto | paths | manual   ← see §6
paths: ["infra/**"]          # only when agent_scope: paths
---
```

**Ship in v1:**
1. **Two-level tree in a left rail**, order from front-matter, drag to reorder (writes `sidebar_position`-equivalent). Docusaurus semantics.
2. **Full-text search, `/` to focus, results ranked with page-title matches boosted.** Search is what actually saves you from a bad tree. Prioritize it above the tree.
3. **`@`-mention linking between pages and tickets, with an automatic backlinks section at the bottom of the page**, showing full containing paragraph as context. Include unlinked mentions in a collapsed "Unlinked mentions (3)" disclosure.
4. **Tags, flat, with a tag index page.** One dimension only. This is your cross-cutting escape hatch so people stop trying to make the tree do two jobs.
5. **Markdown editor with slash commands** and live markdown shortcuts. One editor component, reused for ticket descriptions and comments (Linear's approach — "projects use the same editor as issues").
6. **Owner + verified-until**, with an expiry nudge to the owner. Notion's pattern, made load-bearing by agent consumption.
7. **Templates.** A new page picks from project templates; templates can pre-apply tags. Confluence's blueprint pattern, minus the ceremony.

**Explicitly cut from v1:** unlimited nesting depth; a graph view; page-level permissions (inherit from project); databases-as-pages; comments-on-pages (route discussion to tickets); version *branching* (keep linear history only); WYSIWYG block editor with drag handles.

---

## 4. Backstage — how a developer portal binds repos, docs and services

Sources: [software-catalog](https://backstage.io/docs/features/software-catalog/), [descriptor-format](https://backstage.io/docs/features/software-catalog/descriptor-format/), [TechDocs](https://backstage.io/docs/features/techdocs/), [Roadie catalog plugin](https://roadie.io/backstage/plugins/backstage-software-catalog/)

**The core idea:** the catalog is "a centralized system that keeps track of ownership and metadata for all the software in your ecosystem," built from "metadata YAML files stored together with the code, which are then harvested and visualized in Backstage." Ownership of the metadata stays with the team, edited "using their normal Git workflow."

**`catalog-info.yaml`** envelope:

```yaml
apiVersion: backstage.io/v1alpha1
kind: Component                     # Component | API | System | Domain | Resource | Group | User | Template | Location
metadata:
  name: my-service                  # required; 1–63 chars, [a-z0-9A-Z] separated by [-_.]
  namespace: default
  title: My Service                 # display name for UIs
  description: ...
  labels: {}                        # k8s-style identifying k/v
  annotations:                      # "arbitrary non-identifying metadata"
    backstage.io/techdocs-ref: dir:.     # ← this is the docs↔code binding
  tags: [typescript, payments]
  links: [{url: ..., title: ...}]
spec:
  type: service
  lifecycle: production
  owner: team-payments
  system: payments
  dependsOn: [...]
  providesApis: [...]
```

**Three registration paths:** manual (`/create` → "REGISTER EXISTING COMPONENT" → paste the full URL to the YAML in source control), automatic via Software Templates at scaffold time, and static config.

**TechDocs** is docs-like-code: "Engineers write their documentation in Markdown files which live together with their code," built by MkDocs from an `mkdocs.yml`. The binding is one annotation — `backstage.io/techdocs-ref` — and the payoff is a **Docs tab on every entity page**, so you "discover your Service's technical documentation from the Service's page in Backstage Catalog," plus a cross-cutting **Docs Explorer** and search integration. At Spotify: 5000+ doc sites, ~10k daily hits.

**Catalog UI:** list page with "a searchable view, filters for kind, tags, and owner"; entity pages with "about details, links, labels, relations, and optional docs and templates," organized as tabs (Overview / CI-CD / API / Dependencies / Docs), with an `EntityAboutCard` as the standard header component.

**Five transferable lessons:**

1. **A tabbed entity page is the right shape for your Project page.** Overview / Board / Wiki / Repo / Agents / Runs / Settings — one URL prefix, tabs beneath a persistent header. It's a proven IA for "one thing with many facets."
2. **An "About card" at the top of the Overview** with owner, lifecycle/status, tags, and links is the highest-density orientation device available. Your version: project owner, status, linked repo (with branch and last commit), agent count, open ticket count, last run.
3. **Bind docs to code with one annotation, and make the binding visible as a tab, not a separate app.** Your equivalent: the project's wiki *is* a tab on the project, and optionally syncs to a directory in the linked repo.
4. **Metadata lives with the code and is edited by PR.** Consider `.workspace/project.yaml` in the linked repo as an optional source of truth for project config — a mirror of `catalog-info.yaml`. It makes project setup reviewable and reproducible, and it means agents can propose config changes as PRs.
5. **`annotations` vs `labels` vs `tags` is a genuinely good three-way split** — identifying k/v, non-identifying k/v, and single-valued classification strings. Cheap to adopt, saves you from one overloaded `metadata` blob.

---

## 5. GitHub Projects, and GitHub's new Agents tab

### 5.1 Projects → issues → repo

Source: [docs.github.com — about projects](https://docs.github.com/en/issues/planning-and-tracking-with-projects/learning-about-projects/about-projects)

- "A project is an adaptable **table, board, and roadmap** that integrates with your issues and pull requests... at the user or organization level." Note: projects live *above* repos and can span them; the repo is not the container.
- **Bidirectional sync**: editing assignee/status/metadata in the project view writes through to the underlying issue or PR.
- Three layouts (table / board / roadmap), each **saved as an independent view** with its own filter/sort/group — tabs across the top of the project.
- **Custom fields**: text, number, date, single-select, and **iteration** (with break support). Cap of 50 fields including built-ins.
- Grouping is interactive: "group your project by assignee, and make changes to issue assignment by dragging issues into the different groups" — same drag-writes-property semantic as Linear.
- **Insights**: "view, create, and customize charts that use the items added to your project as their source data."

**Built-in workflows** ([docs](https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-built-in-automations)) — a fixed menu, not a general rules engine:
- item closed → status Done (on by default)
- PR merged → status Done (on by default)
- item added → status Todo
- item reopened → status
- code changes requested → status
- **auto-add items from a repository matching a filter**
- **auto-archive items meeting criteria**

Configured via project menu → Workflows → pick a default workflow → Edit → set fields → "Save and turn on workflow." Anything beyond this menu drops to GraphQL + GitHub Actions.

**The lesson for your event triggers:** GitHub's judgment is that ~6 canned workflows with a toggle and one or two fields covers the overwhelming majority of need, and that the escape hatch should be a real programming environment rather than a progressively-more-complex visual builder. **Do that.** A short list of named triggers with checkbox+field configuration, plus a webhook/script escape hatch. Do not build a node-graph automation editor in v1.

Contrast with Jira's WHEN/IF/THEN rule builder ([Atlassian](https://support.atlassian.com/jira-service-management-cloud/docs/how-do-when-if-and-then-statements-work-for-automation/)) — trigger, condition, action, plus branching, plus an audit log. Note the one piece of Jira's model you *must* take: **the audit/automation log.** A rules engine you can't debug is worse than no rules engine. Every trigger firing needs a row: when, what fired it, what it did, did it succeed.

### 5.2 The Agents tab — the closest existing analogue to your Runs view

Sources: [GitHub changelog 2026-01-26](https://github.blog/changelog/2026-01-26-introducing-the-agents-tab-in-your-repository/), [community discussion #185364](https://github.com/orgs/community/discussions/185364), [AgentPatterns — Mission Control](https://agentpatterns.ai/tools/copilot/agent-mission-control/)

GitHub added an **Agents tab to the repository**, sitting alongside Code, Issues, Pull requests — explicitly "a mission control style view." Concretely:

- **Session list for the repository in one place**, with one-click links to each session's associated pull request. You can "create new ones, and switch between tasks without leaving your codebase."
- **Archive sessions** to keep the list clean; paginated session history.
- **Redesigned logs**: "Similar tool calls are grouped together, reducing noise and making the flow more clear." Log readability treated as a first-class design problem.
- **"Continue in Copilot CLI"** button that copies a command to resume the session locally — a deliberate handoff between the hosted agent and the developer's machine.
- Session detail has three views: **session logs (reasoning), Overview, and Files changed (the diff)**.
- **Statuses** (from the enterprise filtering controls): `queued`, `in progress`, `completed`, `failed`, `idle (waiting for user)`, `timed out`, `canceled` — filterable also by repository and user.
- **Real-time steering** while a session runs, via chat redirection (Copilot adapts "as soon as its current tool call completes") and inline comments on specific lines.
- Multiple entry points to start a session: the dedicated dashboard at `github.com/copilot/agents`, `/task` in Copilot chat, direct assignment from the Issues page, and GitHub Mobile.
- The recommended review workflow is **log-first**: "Check session logs first to find reasoning errors before reviewing code."

**Direct implications for your Runs view:**

| Take | Detail |
|---|---|
| Runs is a **tab on the project**, peer to Board and Wiki | Not a separate global app |
| Status vocabulary | Merge GitHub's operational set with Linear's semantic set: `queued`, `running`, `awaiting input`, `completed`, `failed`, `timed out`, `canceled`, `stale` |
| Filter by status, agent, ticket, actor | Plus a saved default of "needs attention" (`awaiting input` + `failed`) |
| **Group similar tool calls in the log** | The single highest-value log affordance. A 400-line `read_file` sequence collapses to "Read 23 files ▸" |
| Three-pane session detail | Activity log / Summary / Diff. Log is the default tab, not the diff |
| Steering mid-run | Even a simple "send a message to this run" input beats cancel-and-restart |
| Archive, don't delete | Runs accumulate fast |
| Handoff affordance | The "Continue in CLI" pattern — copy a command that resumes this run locally. Very cheap, very reassuring |
| Cost/usage dashboard | Linear surfaces credits in admin settings; make per-project run cost visible early |

---

## 6. Documents an agent can read — the convergent pattern

This is the most important section for your product's differentiation, because your wiki is not just for humans.

### 6.1 The four implementations

**Claude Code — CLAUDE.md + `.claude/rules/`** ([code.claude.com/docs/en/memory](https://code.claude.com/docs/en/memory))

The most fully specified of the four. Scoped hierarchy, loaded broadest→most-specific so specific instructions land last in context:

| Scope | Location | Shared with |
|---|---|---|
| Managed policy | `/Library/Application Support/ClaudeCode/CLAUDE.md`, `/etc/claude-code/CLAUDE.md`, `C:\Program Files\ClaudeCode\CLAUDE.md` | whole org; **cannot be excluded** by users |
| User | `~/.claude/CLAUDE.md` | just you, all projects |
| Project | `./CLAUDE.md` or `./.claude/CLAUDE.md` | team, via source control |
| Local | `./CLAUDE.local.md` (gitignored) | just you, this project |

Mechanics worth copying wholesale:
- **Directory-tree walk**: files in ancestor directories load at launch and are *concatenated*, not overridden — "content is ordered from the filesystem root down to your working directory." Files in *sub*directories "load on demand when Claude reads files in those directories." Lazy loading by locality.
- **`@path/to/import` syntax**, max depth 4 hops, relative to the importing file. Import parsing skips code spans and fenced blocks, so `` `@README` `` stays literal.
- **Explicit size guidance**: "target under 200 lines per CLAUDE.md file. Longer files consume more context and reduce adherence." Files over 4 MiB are skipped entirely.
- **Path-scoped rules** in `.claude/rules/*.md` with `paths:` front-matter globs — "only apply when Claude is working with files matching the specified patterns," triggered when a matching file is read, not on every tool use. Rules *without* `paths` load unconditionally.
- **Block-level HTML comments are stripped before injection**, so maintainers can leave notes that cost zero tokens.
- **Verification affordances**: `/context` lists which memory files actually loaded; `/memory` browses/edits them; `/init` generates a starting file by analyzing the codebase (and reads existing `.cursor/rules/`, `.cursorrules`, `.github/copilot-instructions.md` to fold them in); `/doctor` proposes trims — cutting "content Claude can derive from the codebase, such as directory layouts, dependency lists, and architecture overviews" while keeping "pitfalls, rationale, and conventions that differ from tool defaults."
- **The honest disclaimer, which you should also give users:** "Claude treats them as context, not enforced configuration... there's no guarantee of strict compliance, especially for vague or conflicting instructions. To block an action regardless of what Claude decides, use a PreToolUse hook instead."
- **Auto memory** — a parallel system where the agent writes its own notes into `~/.claude/projects/<project>/memory/` as a `MEMORY.md` index plus topic files, typed `user` / `feedback` / `project` / `reference`. Only the index (first 200 lines / 25KB) loads each session; topic files are read on demand. Claude explicitly "skips anything it can derive from the codebase" and "anything your CLAUDE.md files already say."

**AGENTS.md** ([agents.md](https://agents.md/)) — "a simple, open format for guiding coding agents," 60k+ repos, plain markdown with no mandatory fields. Root placement; **nested files in package directories for monorepos** where "agents automatically read the nearest file in the directory tree, so the closest one takes precedence" (OpenAI's own repo has 88 of them). Precedence: nearest file wins, explicit user prompt overrides everything. Common sections: project overview, build/test commands, code style, testing instructions, security considerations, commit and deploy conventions. Read by Codex, Jules, Cursor, Aider, Copilot and others.

**Cursor rules** ([cursor.com/docs/context/rules](https://cursor.com/docs/context/rules)) — `.cursor/rules/*.mdc`, version-controlled. Critically: "Project rules must use the `.mdc` extension. A plain `.md` file in `.cursor/rules` is ignored by the rules system because it has no frontmatter." Four activation modes driven by three front-matter fields (`alwaysApply`, `description`, `globs`):

| Mode | Trigger |
|---|---|
| Always Apply | every chat session; globs and description ignored |
| Apply Intelligently | "when Agent decides it's relevant based on description" |
| Apply to Specific Files | when an opened file matches `globs` (`src/**/*.tsx`, comma-separated) |
| Apply Manually | when `@my-rule` is mentioned in chat |

Precedence: **Team Rules → Project Rules → User Rules**, earlier wins. User rules live in Customize → Rules. Nested `AGENTS.md` supported with "more specific instructions taking precedence."

**Devin — Knowledge + DeepWiki** ([knowledge](https://docs.devin.ai/product-guides/knowledge), [deepwiki](https://docs.devin.ai/work-with-devin/deepwiki))

Knowledge is the most *UI-native* of the four, and therefore the closest model for you. An entry is a structured record, not a file:

| Field | Purpose |
|---|---|
| **Trigger description** | phrases/sentences that "help Devin recall relevant Knowledge at the right times" |
| **Content** | "a handful of sentences with relevant information" |
| **Macro** (optional) | short `!`-prefixed identifier for invoking it explicitly in a prompt |
| **Repo scoping** | no repo / one specific repo / all repos; pinned-to-repo knowledge "is always used whenever Devin is working in that specific repo" |

Managed at Settings → Resources → Knowledge, organized in **nested folders**, with **per-item and per-folder on/off toggles**, and org→enterprise promotion. Retrieval is contextual, not all-upfront. And crucially: **"Devin will automatically suggest Knowledge to remember based on your feedback in chat"** — the user can edit, dismiss, or regenerate a suggestion before saving.

DeepWiki "automatically indexes your repos and produces wikis with architecture diagrams, links to sources, and summaries of your codebase," organized as hierarchical parent/child pages, generated at repo onboarding. It feeds retrieval: "Ask Devin will use information in the Wiki to better understand and find the relevant context in your codebase." Steered by a `.devin/wiki.json` config file specifying repo notes and explicit page priorities.

**OpenHands** ([docs.openhands.dev/overview/skills](https://docs.openhands.dev/overview/skills)) — has generalized microagents into skills at `.agents/skills/<name>/SKILL.md` (project), `~/.agents/skills/` (user), plus a public registry; legacy `.openhands/microagents/` still supported. Five distinct mechanisms with different loading semantics:

1. **Repository context** (`AGENTS.md`) — "Full content is included in the initial system prompt"
2. **Agent skills** (`SKILL.md`) — "Name and description are advertised first; the agent invokes the full skill when relevant"
3. **Keyword-triggered skills** — `triggers` front-matter; content injected on keyword match
4. **Path-triggered rules** — `paths` front-matter; "content is injected when the agent first touches a matching file"
5. **Model-specific context** — `CLAUDE.md`, `GEMINI.md`

Front-matter: `name` (must match directory, lowercase-hyphenated), `description` ("what the skill does and when to apply it"), optional `triggers`, optional `paths`. **Three-level progressive disclosure** — discovery (metadata in catalog) → invocation (full content) → resources (files in `scripts/`, `references/`, `assets/` loaded only when needed) — which "keeps the initial prompt smaller than loading every skill in full." UI: "Manage installed skills under `Customize > Skills`."

### 6.2 The convergence, and what it means for you

Strip away the file formats and all four systems are the same three-axis model:

**Axis 1 — Scope (who/where does it apply):** org → user → project → subdirectory/path. Universally loaded broad-to-specific with specific winning.

**Axis 2 — Activation (when does it enter context):**

| Mode | Cursor | Claude Code | OpenHands | Devin |
|---|---|---|---|---|
| Always | `alwaysApply: true` | CLAUDE.md, rules w/o `paths` | `AGENTS.md` | knowledge pinned to repo |
| Path/glob | `globs` | `paths:` front-matter | path-triggered rules | — |
| Semantic/description | "Apply Intelligently" via `description` | skills | skill `description` | trigger description |
| Keyword | — | — | `triggers` | trigger description |
| Manual | `@my-rule` | `/`-invoked skills | invoke by name | `!macro` |

**Axis 3 — Budget:** everyone has independently discovered that always-on context is scarce and must be rationed. Claude Code: <200 lines, 25KB memory index, 4 MiB hard skip. OpenHands: three-level progressive disclosure. Devin: retrieval rather than upfront loading.

### 6.3 Concrete design for your wiki-as-agent-context

**Give every wiki page an `agent_scope` front-matter field with exactly four values**, mirroring Cursor's four modes because they're proven and the vocabulary is already in users' heads:

```yaml
agent_scope: always   # injected into every agent run on this project
agent_scope: auto     # retrieved when the `description` matches the task (default)
agent_scope: paths    # injected when the agent touches a matching file
agent_scope: manual   # only when the user @-mentions the page, or the agent chooses to read it
agent_scope: never    # human-only doc; never enters agent context
```

**Surface it in the UI as five things, all cheap:**

1. **A badge on the page header and in the wiki tree.** A small "Always in context" / "Auto" / "Paths: `infra/**`" chip. Users must be able to see, at a glance in the tree, which pages are steering agents. This is the affordance every one of these systems lacks — file-based systems make agent context *invisible*, and you have a UI, so use it.

2. **A live context budget meter on the project's Agents tab.** "Always-on context: 3 pages, 1,840 words (~2.4k tokens)." Warn past a threshold, with the same advice `/doctor` gives: cut what the agent can derive from the code, keep pitfalls, rationale, and conventions that differ from defaults. **Nobody ships this today and it is the most obviously missing piece of UI in the entire category.**

3. **A per-run "Context" panel in the run detail** listing exactly which pages were loaded and why (`always` / matched path `infra/deploy.ts` / retrieved for "deployment"). This is Claude Code's `/context` given a GUI. It is the debugging tool for "why did the agent ignore my instructions," and it will single-handedly determine whether users trust the feature.

4. **Agent-proposed pages, human-approved.** Devin's best idea: after a run, the agent proposes a wiki page or an edit ("You corrected me twice about migrations — save this as `Database migrations`?"), rendered as a diff the user can edit, accept, or dismiss. Never auto-write. This is how the wiki gets populated without anyone sitting down to write documentation, which is the actual reason wikis die.

5. **Verified-until on always-on pages, enforced.** Combine with §3's Notion pattern: an `always`-scoped page whose verification has expired gets demoted to `auto` (or flagged loudly) rather than silently continuing to steer every run with stale instructions.

**Also do the boring compatibility work:** on repo link, detect and offer to import `AGENTS.md`, `CLAUDE.md`, `.cursor/rules/`, `.github/copilot-instructions.md` as wiki pages — and offer bidirectional sync of always-on pages back out to `AGENTS.md` in the repo. Both `/init` (Claude Code) and Cursor already read each other's formats; interop is table stakes now, and it is also your best import-time onboarding moment (see §7).

---

## 7. Empty states and onboarding

Sources: [Pixxen — 9 SaaS empty state patterns](https://pixxen.com/blog/saas-empty-state-design/), [Setproduct — empty state UI design](https://www.setproduct.com/blog/empty-state-ui-design), [Appcues — 10 onboarding patterns](https://www.appcues.com/blog/user-onboarding-ui-ux-patterns)

### 7.1 The anatomy everyone agrees on

Five components, in this priority order:
1. **Headline** explaining *why* it's empty — specific, not "No data found"
2. **Body**, two sentences maximum
3. **One primary CTA.** Singular. "Multiple CTAs dilute focus."
4. Optional **secondary link** (recovery path, docs, import)
5. Optional **illustration** — "only if it genuinely adds value," tied to the feature, not stock art, "centralized, not overwhelming"

Layout: centered, generous whitespace, headline dominant. Copy: contractions, human tone, explicit next step. The canonical contrast: *"No items found"* vs *"We couldn't find any results... Try fewer keywords or check spelling."*

### 7.2 The patterns that apply to you

**First-time dashboard — strip to one action.** Webflow's model: "massive central 'Create new site' button with everything else dimmed." Rule: eliminate competing CTAs.

**Pre-populated demo state.** Autopilot improved trial-to-paid with demo journey templates. Asana "uses pre-populated project templates that give new users a working example to explore rather than a blank screen." **Rule: always clearly label it as demo** to avoid deception.

**Filtered-to-nothing** is a *different* state from first-use and needs different copy: show active filters as individually removable chips and suggest which filter is most restrictive. You will hit this constantly in a Runs view with default filters.

**Permission-denied**: "name the next person to contact; link directly to that action. Never make users guess who to email."

**Skeleton screens, not spinners**, for anything over 500ms. Relevant for agent run logs, which stream.

**Checklists** (GrowthHackers' sliding right-side checklist that "keep[s] tasks visible without blocking the main interface") and **progress indicators** work because of the completion drive. **Persona-based onboarding** — Headspace asks "just two or three questions" to self-segment. **Progressive disclosure** — Grammarly stages feature introduction across the first week.

**Deferred account creation** — let people see value before signup. Worth considering for a "try it on a public repo" path.

### 7.3 The specific problem: a new project starts with nothing, and it has six sub-things

Your hard case isn't one empty state — it's an empty *workspace with six empty children*. Six simultaneous "create your first X" prompts is the worst possible first screen. Three strategies, in order of preference:

**A. Make repo-link the single gate, then derive everything from it.**
The new-project screen has exactly one action: **Connect a GitHub repo.** Everything else stays dimmed. On connect, you fill the workspace automatically:
- Import open issues → tickets on the board (offer a preview and a checkbox, don't just do it)
- Detect `AGENTS.md` / `CLAUDE.md` / `.cursor/rules/` / `README.md` / `docs/` → seed wiki pages, correctly scoped
- Detect CI config and default branch → propose two event triggers, pre-filled but off
- Detect language/framework → suggest matching agents
- Generate an Overview from the README

This turns the empty state into a *loading* state, which is a far better experience. It is exactly Backstage's "REGISTER EXISTING COMPONENT" flow and Devin's onboarding-generates-DeepWiki flow. **This is the recommendation.**

**B. Sequence the empty states rather than showing them in parallel.** Only the current step's tab is enabled; the rest show a lock with "Connect a repo first." Reveal as prerequisites are satisfied. This is progressive disclosure applied to IA rather than to features.

**C. Project templates.** New Project → a small gallery ("Bug triage", "Feature delivery", "Docs site") that pre-creates statuses, 2–3 wiki pages, one trigger, and one agent config. Asana's pattern. This is the fallback for users with no repo.

**Per-surface empty state copy, concretely:**

| Surface | Headline | Body | Primary CTA | Secondary |
|---|---|---|---|---|
| Project (fresh) | *Connect a repository to get started* | We'll import your issues, docs and agent instructions automatically. | **Connect GitHub repo** | Start from a template |
| Board | *No tickets yet* | Import from GitHub Issues, or write one — press `C`. | **Import 12 open issues** | New ticket |
| Wiki | *Your project has no docs yet* | Docs here steer your agents, not just your teammates. | **Import `AGENTS.md`** (detected) | New page |
| Agents | *No agents connected* | Agents pick up tickets you delegate to them. | **Add an agent** | What are agents? |
| Runs | *No runs yet* | Delegate a ticket to an agent and its run appears here. | **Go to board** | — |
| Runs (filtered empty) | *No runs match these filters* | with removable filter chips | **Clear filters** | — |
| Triggers | *No triggers yet* | Start an agent automatically when something happens. | **Add trigger** with 3 suggested pre-fills | — |

Two more, for the moments that matter:

- **First run completing** is your true activation event. Celebrate it explicitly (Setproduct's "match tone to the moment" applies) and immediately teach the next action: review the diff, or leave feedback that becomes a wiki page.
- **First `awaiting input` state.** This is where users learn agents are interactive rather than fire-and-forget. Give it an unmissable treatment — the pinned lane from §2, plus a notification to the delegating human specifically (Linear's routing).

---

## 8. Settings IA for per-project configuration

Sources: [memorable.design — SaaS settings page examples](https://memorable.design/saas-settings-page-examples/), [Linear preferences](https://linear.app/docs/account-preferences), [Linear agent guidance scoping](https://linear.app/docs/linear-agent), [Devin knowledge management](https://docs.devin.ai/product-guides/knowledge)

### 8.1 The scope ladder

Every mature tool in this research runs the same three-tier ladder, and every one of them makes precedence explicit:

- Linear: **workspace → team → personal**, with team guidance taking priority over workspace
- Cursor: **Team Rules → Project Rules → User Rules**, earlier wins
- Claude Code: **managed policy → user → project → local**, concatenated in that order, later read last; managed policy cannot be excluded
- Devin: item → folder → org → enterprise promotion

**Your ladder: Workspace → Project → Member.** Three levels, no more. And — the part most products get wrong — **show the inherited value in the UI, don't hide it.** Every project-level setting should render as a control plus a line reading *"Inherited from workspace: `main`. Override."* Once overridden, offer "Reset to workspace default." This one pattern eliminates the largest category of settings confusion.

### 8.2 Recommended project settings structure

Left rail of sections, single scrollable pane per section, autosave with an inline saved indicator (no global Save button — it makes every field feel transactional and blocks navigation):

```
Project settings
├─ General          name, key/prefix (drives ticket IDs), description, icon/color, archive
├─ Board            statuses (add/rename/reorder, each with a category), WIP limits,
│                   default view, auto-close parent/sub-ticket rules
├─ Wiki             default agent_scope for new pages, verification period default,
│                   repo sync path (e.g. docs/ ⇄ wiki), context budget warning threshold
├─ Repository       connected repo, default branch, branch naming template,
│                   PR conventions, path filters
├─ Agents           roster + per-agent: enabled, permissions, max concurrent runs,
│                   default scope; project-level agent guidance textarea;
│                   context preview ("what every run sees")
├─ Triggers         list of event triggers with on/off toggles + last-fired column;
│                   link to the trigger audit log
├─ Members & access roles, invites, whether agents inherit inviter permissions
├─ Notifications    per-event routing; who gets pinged on awaiting-input / failed
└─ Danger zone      transfer, archive, delete (typed confirmation)
```

### 8.3 Settings principles worth enforcing

1. **Settings live *inside* the object they configure**, as a tab on the project — not in a global settings app with a project dropdown. Backstage's entity-page model. It keeps the mental model "a project is a self-contained thing."
2. **Sensible-default-then-override.** Almost nothing should be required at creation. Every setting has a workspace default; the project settings page is a list of *deviations*.
3. **Search within settings.** Once you pass ~40 controls, a settings search box is the cheapest usability win available.
4. **Every destructive action gets typed confirmation and a named danger zone.** Deleting a project with a linked repo, a wiki, and run history is the highest-regret action in the product.
5. **Agent permissions belong in settings, not in a modal at first run.** Devin's per-item/per-folder toggles and Linear's per-team agent access are the right granularity: *which agents may act on this project, and what may they touch.*
6. **Separate "context" (steers) from "enforcement" (blocks).** Claude Code states this plainly: instructions "shape Claude's behavior but are not a hard enforcement layer"; use hooks/permissions for hard limits. Your settings UI should have a visibly different treatment for guidance (a textarea, best-effort) versus permissions (checkboxes, enforced). Users will otherwise assume guidance is enforcement and be badly surprised.

---

## 9. Consolidated v1 recommendation

### 9.1 Screens

| Route | Contents |
|---|---|
| `/` | Project list + "Needs your input" strip aggregating `awaiting input` and `failed` runs across all projects |
| `/p/:id` | **Overview** — About card (owner, status, repo + branch + last commit, counts), recent runs, pinned wiki pages, activity |
| `/p/:id/board` | List⇄board toggle (`Cmd+B`), `group_by` picker, filters, display properties, pinned "Needs your input" lane, Backlog sub-tab |
| `/p/:id/t/:tid` | Ticket detail: description (markdown, slash commands), sub-tickets, properties sidebar (`Cmd+I`) with **assignee + delegate as separate fields**, unified activity+chat stream with agent session cards inline |
| `/p/:id/triage` | Intake inbox, `1`/`2`/`3`/`H` actions. Default destination for trigger- and agent-created tickets |
| `/p/:id/wiki` | Two-level tree, search, editor, backlinks + unlinked mentions, agent_scope badges |
| `/p/:id/agents` | Agent roster, per-agent config, project guidance, **context budget meter and preview** |
| `/p/:id/runs` | Session list, status filters, archive; detail = Activity log / Summary / Diff, with grouped tool calls, mid-run steering, and a Context panel |
| `/p/:id/triggers` | ~6 canned triggers with toggles + fields, escape hatch, **audit log** |
| `/p/:id/settings` | §8.2 |

### 9.2 Keyboard map

```
Cmd+K  command menu (actions)        Cmd+J  agent chat
/      search                        ?      shortcuts
G then B/W/R/A/T/S    go to Board / Wiki / Runs / Agents / Triage / Settings
C  new ticket    Cmd+Shift+O  sub-ticket (or convert selection)
J/K  move   Space  peek   Enter  open   X  select   Esc  back
S status · P priority · A assign · D delegate to agent · L labels · E edit
Cmd+B  board/list    Shift+V  display options    Cmd+I  properties sidebar
1/2/3/H  triage: accept / duplicate / decline / snooze
```
Note `D` for delegate: giving delegation its own top-level single-key mutator alongside `A` for assign is the keyboard-level expression of the two-field model, and it signals the product's thesis every time someone presses it.

### 9.3 What to cut, ranked by confidence

Very confident cuts: JQL/query swimlanes; sub-grouping; story points/velocity; cycles/sprints; timeline/Gantt; graph view for the wiki; page-level permissions; unlimited wiki nesting; multiple boards per project; a visual node-graph automation builder; per-page comments.

Defer, don't kill: custom fields (Height's typed-attribute model is the right eventual answer); initiatives/portfolio layer above projects; insights/charts; multi-repo per project; cross-project saved views.

### 9.4 The three things that would make this product distinctive

Everything above is table stakes reconstruction. These three are not currently shipped well by anyone:

1. **The context budget meter and per-run context panel.** File-based agent instruction systems are invisible; you have a UI. Showing users exactly what steers each run — and what it costs — is the clearest unmet need in the entire category.
2. **Wiki pages as scoped, versioned, *verified* agent instructions.** Combining Notion's expiring verification with Cursor's four activation modes produces something neither has: documentation whose staleness is tracked *because* machines act on it.
3. **Agent-proposed docs with human approval.** Devin's suggestion loop, applied to a real wiki with diffs and ownership. It's the only credible answer to why wikis die, and in an agentic workspace the agent is the party with the most to gain from writing it.

---

## Sources

**Linear** — [Concepts](https://linear.app/docs/conceptual-model) · [Projects](https://linear.app/docs/projects) · [Parent and sub-issues](https://linear.app/docs/parent-and-sub-issues) · [Display options](https://linear.app/docs/display-options) · [Assign and delegate issues](https://linear.app/docs/assigning-issues) · [Triage](https://linear.app/docs/triage) · [Triage responsibility](https://linear.app/changelog/2023-10-12-triage-responsibility) · [Documents](https://linear.app/docs/documents) · [AI Agents](https://linear.app/docs/agents-in-linear) · [Linear Agent](https://linear.app/docs/linear-agent) · [Linear for Agents](https://linear.app/agents) · [Agents: Getting Started](https://linear.app/developers/agents) · [Agent Interaction](https://linear.app/developers/agent-interaction) · [Agent Interaction Guidelines](https://linear.app/developers/aig) · [Coding sessions changelog](https://linear.app/changelog/2026-06-11-coding-sessions) · [Introducing Linear Agent](https://linear.app/changelog/2026-03-24-introducing-linear-agent) · [Agent Interaction SDK approach](https://linear.app/now/our-approach-to-building-the-agent-interaction-sdk) · [Invisible details](https://linear.app/now/invisible-details) · [Preferences](https://linear.app/docs/account-preferences) · [Keyboard shortcuts cheatsheet](https://fastshortcuts.com/shortcuts/linear/)

**Kanban / PM tools** — [Atlassian: Kanban with Jira](https://www.atlassian.com/agile/tutorials/how-to-do-kanban-with-jira) · [Atlassian: Kanban boards](https://www.atlassian.com/software/jira/features/kanban-boards) · [Jira automation WHEN/IF/THEN](https://support.atlassian.com/jira-service-management-cloud/docs/how-do-when-if-and-then-statements-work-for-automation/) · [Teamhood: WIP limits](https://teamhood.com/kanban-resources/kanban-wip-limits/) · [Trello 101](https://trello.com/guide/trello-101) · [Trello List Limits Power-Up](https://trello.com/power-ups/5c2462c384ab8949b1724a20) · [Nira: Trello review](https://nira.com/trello-review/) · [Height review](https://freshvanroot.com/blog/height-app-review/) · [Shortcut help](https://www.shortcut.com/help/) · [Shortcut: Epic workflow states](https://help.shortcut.com/hc/en-us/articles/360046059412-Epic-Workflow-States) · [Shortcut: transition from Linear](https://www.shortcut.com/help/getting-started/transition-from-linear/)

**Wiki / docs** — [Outline](https://www.getoutline.com/) · [Outline guide](https://docs.getoutline.com/s/guide) · [Notion sidebar](https://www.notion.com/help/navigate-with-the-sidebar) · [Notion wikis & verified pages](https://www.notion.com/help/wikis-and-verified-pages) · [Notion: docs-first culture with a database wiki](https://www.notion.com/help/guides/build-a-docs-first-culture-with-a-beautiful-team-wiki-powered-by-a-database) · [Notion Mastery: Craigslist Effect](https://notionmastery.com/designing-for-notion-the-craigslist-effect/) · [Confluence navigation critique (Seibert)](https://seibert.group/blog/en/best-way-to-organize-confluence-space/) · [K15t: structuring Confluence for long-term success](https://www.k15t.com/rock-the-docs/confluence-cloud-best-practices/how-to-structure-confluence-content-for-long-term-success) · [Obsidian backlinks](https://obsidian.md/help/plugins/backlinks) · [Obsidian forum: folders vs links vs tags](https://forum.obsidian.md/t/folders-vs-linking-vs-tags-the-definitive-guide-extremely-short-read-this/78468) · [Obsidian Publish](https://publish.obsidian.md/) · [GitBook content structure](https://docs.gitbook.com/creating-content/content-structure) · [GitBook git-sync content configuration](https://github.com/GitbookIO/public-docs/blob/main/getting-started/git-sync/content-configuration.md) · [GitBook monorepos](https://gitbook.com/docs/getting-started/git-sync/monorepos) · [Docusaurus sidebar](https://docusaurus.io/docs/sidebar)

**Backstage / IDP** — [Software Catalog](https://backstage.io/docs/features/software-catalog/) · [Descriptor format](https://backstage.io/docs/features/software-catalog/descriptor-format/) · [TechDocs](https://backstage.io/docs/features/techdocs/) · [Roadie: catalog plugin](https://roadie.io/backstage/plugins/backstage-software-catalog/) · [internaldeveloperplatform.org: developer portals](https://internaldeveloperplatform.org/developer-portals/) · [Port: overview of IDPs](https://www.port.io/guide/overview-of-internal-developer-portals)

**GitHub** — [About Projects](https://docs.github.com/en/issues/planning-and-tracking-with-projects/learning-about-projects/about-projects) · [Built-in automations](https://docs.github.com/en/issues/planning-and-tracking-with-projects/automating-your-project/using-the-built-in-automations) · [Parent/sub-issue progress fields](https://docs.github.com/en/issues/planning-and-tracking-with-projects/understanding-fields/about-parent-issue-and-sub-issue-progress-fields) · [Agents tab changelog](https://github.blog/changelog/2026-01-26-introducing-the-agents-tab-in-your-repository/) · [Agents tab discussion](https://github.com/orgs/community/discussions/185364) · [Copilot app: agent-native desktop](https://github.blog/news-insights/product-news/github-copilot-app-the-agent-native-desktop-experience/) · [VS Code: unified agent experience](https://code.visualstudio.com/blogs/2025/11/03/unified-agent-experience) · [AgentPatterns: Agent Mission Control](https://agentpatterns.ai/tools/copilot/agent-mission-control/)

**Agent-readable docs** — [Claude Code memory](https://code.claude.com/docs/en/memory) · [AGENTS.md](https://agents.md/) · [Cursor rules](https://cursor.com/docs/context/rules) · [Devin Knowledge](https://docs.devin.ai/product-guides/knowledge) · [Devin DeepWiki](https://docs.devin.ai/work-with-devin/deepwiki) · [DeepWiki](https://deepwiki.com/) · [OpenHands skills/microagents](https://docs.openhands.dev/overview/skills.md)

**Empty states / onboarding / settings** — [Pixxen: 9 SaaS empty state patterns](https://pixxen.com/blog/saas-empty-state-design/) · [Setproduct: empty state UI design](https://www.setproduct.com/blog/empty-state-ui-design) · [Appcues: 10 onboarding UX patterns](https://www.appcues.com/blog/user-onboarding-ui-ux-patterns) · [The Hangline: empty states that guide](https://www.thehangline.com/how-to-design-an-empty-state-that-guides-users-ux-patterns-and-examples/) · [Memorable.design: SaaS settings page examples](https://memorable.design/saas-settings-page-examples/) · [Medium: IA for SaaS dashboards](https://medium.com/@brandon.mccrae/information-architecture-for-saas-dashboards-ship-clarity-not-chaos-da5295cb8e82)

---
