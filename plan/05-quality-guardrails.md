# Quality guardrails for agent-written code

Research pass, August 2026. Four parallel literature reviews — change size and decomposition,
code reuse and duplication, in-flight verification, context curation — synthesised against what
Lexicode actually ships today.

Every claim below is tagged: **[measured]** peer-reviewed or preprint with stated method and
numbers · **[vendor]** published by a party with a commercial interest · **[folklore]** widely
repeated, no measurement found. Sources are listed at the end of each section's parent report;
the full source lists live in the research transcripts.

---

## 0. The finding that reframes the question

Across all four reviews, one result recurs with unusual consistency: **asking an agent to be
careful does not work; changing what it is graded on does.**

| Intervention | Effect |
|---|---|
| "anti-slop" / "plan first" prompts on long-horizon degradation | −34% initial verbosity, **zero change in degradation slope** [measured] |
| Blast-radius warnings ("don't touch adjacent code") | 65.5% → 64.0% action rate — **nothing** [measured] |
| "Write tests" instruction (0.6% → 64.4% test-writing rate) | 71.8% → 71.8% resolution — **nothing**, +19.8% output tokens [measured] |
| Rule present in context vs. no rule | 31.6% → 55.0% compliance [measured] |
| Same rule compiled into a runtime check | 55.0% → **70.1%** [measured] |
| Reframing the goal: "abstain or fix" | 60.5% → **88.5%** correct abstention [measured] |
| Naming the target files vs. leaving them ambiguous | 8.6% → **67.9%** safe success [measured] |

The pattern is not "prompts are useless." It is that **prompts asking for restraint or quality
fail, while prompts that change the success condition or remove ambiguity succeed.** A guardrail
built as guidance moves the intercept. A guardrail built as a gate moves the gradient.

This is plan rule 5 — *guidance and enforcement are different mechanisms* — arriving from the
evidence rather than from first principles. It was right, and it is more load-bearing than the
brief assumed.

---

## 1. Where Lexicode stands

**Already right.** Acceptance criteria as first-class ticket structure. Permissions compiled into
`.claude/settings.json` rather than asked for in prose. Container isolation per run. A wiki with
three scopes rather than one always-on blob. Budget and step ceilings. Sequential, test-gated
integration is *architecturally available* because every run is isolated.

**Wrong variable.** The context budget counts **tokens**. A 1,650-session factorial study found
context-file size has no detectable effect on adherence, with affirmative Bayes-factor support for
the null (BF10 0.05–0.10) [measured]. What degrades is the **count of instructions**: 1 → ~96%
compliance, 5 → 83%, 20 → **60%** on Sonnet-class models [measured].

**Wrong trigger.** `verified_until` demotes on calendar age. The measured harm is *referential
rot* — a page naming a symbol that no longer exists. Stale retrieved context produced stale
references in 15/17 samples where current context produced zero; the no-retrieval baseline also
produced zero [measured]. Stale guidance is worse than none. A page untouched for 90 days whose
references all resolve is fine; a page edited last week naming a deleted function is dangerous.

**Missing entirely.** Nothing in the run path executes tests, types, or lints. The setup script
runs *before* the agent; between the agent finishing and the PR opening there is no verification
step at all. This is the single largest gap.

**Advisory where it should bite.** The diff-size chip displays and gates nothing. `check_criterion`
is pure agent self-report with nothing verifying it.

---

## 2. What to build, ranked by evidence per unit of effort

### Tier 1 — strong evidence, low cost

**1. Ticket pre-flight, ordered by what actually predicts success.**
Converting issues from fully-specified to underspecified drops agent success from **43.8% to
23.7%** across 700 instances [measured]. This is the largest controllable effect in the entire
literature. Shapley attribution over information categories: error information **0.183**,
implementation details 0.098, environment/version 0.088, reproduction steps 0.076, expected
behaviour 0.057 [measured — authors caution the ordering is indicative, CIs overlap].

That ordering **inverts the usual template**, which leads with goals and treats error output as an
attachment. Note the finding is drawn from bug-fix tasks, so error-information dominance is partly
definitional; for feature work the ordering would differ.

Keep it short. Specification value saturates and then *declines*: 0.280 at minimal prompt → 0.799
at 50 words → 0.921 at 200 words → **0.860 at maximal detail** [measured]. Do not build a
twenty-field mandatory template.

*Where it plugs in:* ticket creation, and a gate before delegation. Not a generic Definition of
Ready — INVEST and DoR have essentially no empirical validation, and only 23.5% of practitioners
use INVEST at all [measured].

**2. A verification field on every ticket.**
Prompts stating *how the result will be checked* predict roughly **eightfold higher odds** that
generated code is adopted (OR 7.78, CI [3.40, 17.82]) [measured, small n, conversational rather
than agentic]. One required field. Also the single thing Anthropic's own guidance puts first.

**3. A target-file list on the ticket, checked against the diff.**
Fully specified target: **67.9%** safe success. Maximum ambiguity: **8.6%**. Wrong-target rate
9.6% → **75.1%** [measured]. Flag for human review rather than blocking — the correct fix
sometimes genuinely lies outside the named files.

**4. Test execution in the loop.**
The cleanest industrial ablation, 123 real test failures: baseline 28.5% → +static analysis 34.1%
→ **+test execution 43.9%** [measured]. Test feedback is worth +15.4 points, roughly triple static
analysis. Layering static analysis *on top of* execution cost 1.6 points of solve rate while
cutting trajectory errors 1.0% → 0.2% — it buys reliability, not correctness.

*Prerequisite:* every container must be able to run the project's tests fast, and the agent must
be told the exact command. Today `setup_script` makes this possible and nothing guarantees it.

**5. "Abstain or fix" framing, with a no-op result treated as success.**
Agents make unnecessary edits to already-correct code **35–65%** of the time [measured]. Framing
lifted correct abstention 60.5% → **88.5%** on one model, 65% → 80.5% on Sonnet, with no loss on
genuine bugs. Critically, "reproduce the issue first" *alone* made it **worse** (47.5% vs 60.5%) —
reproduction pays only once abstaining is framed as a successful outcome.

**6. Cap instructions, not tokens, in `always` scope.**
Parse always-injected pages for imperatives and cap the total at **8–10**. Show the count as a live
meter with per-page contributions, so the cost of a new always page is visible when someone marks
it. Keep a soft token backstop, but know that the token number is [folklore] and the instruction
count is [measured].

Corollary: make `always` require justification rather than a checkbox, and default to scoped.

### Tier 2 — good evidence, moderate cost

**7. Fail-before / pass-after validation of agent-added tests, then freeze them.**
Tests written *after* seeing the implementation score **13.2% lower** on fault detection than
spec-only tests [measured]. Agents' self-written "tests" are debugging probes — print statements
outnumber assertions roughly **5-to-1** [measured]. Run each new test against the pre-change tree;
fail if it passes there. Then snapshot its hash and forbid edits. A test-driven workflow with a
frozen, non-editable test achieves ~0.9% test hacking [measured].

Reality check: agents can only produce a genuinely reproducing test on **19.2%** of issues by the
best measured harness (leaderboard best ~49%) [measured]. The gate will be *inapplicable* most of
the time. Handle that explicitly rather than letting the agent fake it.

**8. Hold part of the suite back from the agent.**
Agents saturate the visible suite (~90–97%) while held-out performance sits at **35–60%**, and the
gap grows ~27 points per 10× increase in LOC [measured]. Run the holdout at PR time and report the
gap. This is the best single detector of "passed for the wrong reason."

**9. Deterministic suppression and regression checks in the diff.**
Hard-fail on `# noqa`, `eslint-disable`, `@ts-ignore`, `type: ignore`; on any reduction in test
count; on `skip`/`xfail`/`.only`; on assertion weakening. Documented failure modes, deterministic
detection, nothing to argue with. (No measured *rate* of agents suppressing lint exists — that
part is [folklore] — but the prevention is mechanical and free.)

**10. One fresh-context reviewer pass before the PR opens, routed to the human.**
Fresh session 28.6% F1 vs. same-session self-review 24.6% (p=0.008); reviewing twice in the same
session was **worst** at 21.7%; a context-*aware* subagent scored 23.8% — **worse than a clean
session** [measured]. So: give it the diff and the ticket, never the production transcript.

Temper expectations hard. The best condition catches **under 30%** of deliberately injected
errors. In real deployment, **8.1% / 7.2%** of LLM review comments were accepted, and only
**4.8% / 5.2%** for functional issues [measured]. Refactoring comments were ~3.5× more actionable.
Route output to the human, not back into the loop.

*Lexicode already has the Reviewer agent and trigger.* The change is ensuring it runs with a clean
context and that its findings reach a person.

**11. Reference-resolution demotion, replacing calendar demotion.**
Extract code identifiers and paths from each wiki page; demote on the first unresolvable
reference. Keep `verified_until` only for prose with nothing checkable. Catches the failure mode
with the strongest evidence of harm; misses "the helper exists but is deprecated," which argues for
keeping human verification too, not replacing it.

**12. Executable conventions: dependency allowlist and module-boundary rules.**
Agents routinely add libraries duplicating existing capability [measured]. A CI check failing on
an unapproved manifest entry converts that into a zero-false-positive gate. Module-boundary rules
(one HTTP client, one logger, one config accessor) are what actually prevent "a third HTTP
client." One field report observed agents **self-correcting** on dependency-cruiser violations
[vendor/field report, no numbers].

**13. A files-and-lines tripwire that escalates rather than blocks.**
Count **files first** — usefulness of review comments falls with file count [measured], and files
touched discriminates agent PRs far better than line count (Cliff's δ 0.45 vs 0.28–0.32)
[measured]. Its job is to convert a silent overrun into a human decision, not to make the work
smaller.

Note: among studied agents, **Claude Code has the widest variability** in additions and files
touched [measured]. Clamp the tail, don't shift the mean — agentic PRs are already *smaller* than
human PRs on every dimension measured.

### Tier 3 — plausible, weak or no evidence

Diff-scoped duplication detection (catches Type-1/2 copy-paste only, not reinvention) · diff-scoped
mutation testing as a *report* not a gate (correlation with fault detection collapses on buggy
code, r≈0.48–0.51) · oscillation detection as an escalation trigger (entirely [folklore], but the
failure mode is real).

---

## 3. What not to build

**A semantic/embedding code index.** The only clean comparison against a real grep-based harness
is **+5.1pp resolve, p=0.087 — not significant** [measured]. The strongest vendor case reports
**+0.3% code retention**, reaching 2.6% only past 1,000 files [vendor]. Best-in-class dense
retrieval gets Recall@20 of **0.70** on agentic relevance — you would build and host an index to
still miss 30% of relevant files [measured]. And container-per-ticket is the worst case for
freshness: the agent writes code then immediately searches for it. Stale indexes fail
*malignantly* (false negatives it trusts); grep fails benignly (false positives it filters).

*Revisit if:* the repo exceeds ~1,000 files, or a local eval shows agentic search missing known
helpers above ~30% after the cheaper mechanisms are in, or identifiers are generically named
(`handler`, `Manager`) — lexical collision rate is the actual determinant [measured].

*Cheaper substitute for the real gap (vocabulary mismatch — ticket says "retry", helper is
`withBackoff`):* a deterministic per-container symbol/exports inventory from tree-sitter or ctags,
plus a domain glossary. Structure-based RepoMap-style context led on causal-indirect retrieval and
on context-budget yield [measured].

**"Show the agent similar existing code."** An ablation ranking context types by value per token
found caller and call-chain context **highest**, and *similar code fragments* **lowest** — 31% of
tokens for the least benefit [measured]. The intuitive reuse mechanism measures worst. Prefer call
chains and import structure.

**Coverage-delta gates.** Coverage correlates weakly with suite effectiveness once size is
controlled [measured, replicated for LLM suites in 2026]. Trivially satisfied by assertion-free
exercise code.

**Same-session self-review.** Measurably worse than reviewing once [measured].

**A generic "write tests" instruction.** Zero measured effect, ~20% output-token tax [measured].

**Ordering/position optimisation of injected context.** Null in the factorial study; zero aggregate
delta in the stale-retrieval study [measured].

**An automatic prompt compressor.** Capability-graded: +11pp for a small model, **−1.2pp for
Sonnet 4.6** [measured]. Helps weak models; you are not running weak models.

**Static decomposition without per-subtask retry.** Measured at **+80.5% retry cost** versus not
decomposing at all [measured, small]. If you split into five steps and any failure re-runs the
pipeline, you have made things strictly worse.

**A blocking plan gate, before measuring it in shadow mode.** No controlled evidence that human
plan-approval improves agent output. Run it shadow — let the agent proceed, record what a human
*would* have rejected — and measure correction rate first.

**Repository overviews in `AGENTS.md`.** Specifically found unhelpful; context files did not
improve success and raised inference cost **20–23%**, with Claude Code the worst case — even
developer-written files failed to beat no file at all [measured]. Agents *do* follow specific
instructions reliably. Keep short imperative non-discoverable rules; delete orientation.

---

## 4. The central tension

One study gave agents an in-loop behavioural oracle. The checked metric hit a perfect **222/222**
— and **11 of 12 runs** shipped code where the separately-required reusable library was dead or
absent. The agents' own spontaneous spec-writing went from **31 files to zero** [measured, n=18,
sound ablation].

**Every gate is a reallocation, not an addition.** Adding a check moves effort onto the checked
dimension and off everything else, including good behaviour the agent was previously exhibiting
unprompted.

Practical consequence: add gates **one at a time**, and measure what *stops* happening, not only
what improves.

---

## 5. What to measure, because nobody has measured it for you

No standardised benchmark exists for whether any of these interventions reduce duplication or
raise quality in a real codebase. Two cheap local instruments:

1. **A 30–50 ticket eval set** where the existing helper that *should* have been reused is known.
   Measure reuse rate with and without each mechanism. This is also the precondition for revisiting
   the index decision honestly.
2. **Cross-file function call ratio** on agent PRs vs. human PRs, derived from tree-sitter. It is
   the most on-point published proxy for reinvention ("new code calls existing code less often" —
   down 35% since 2023 [vendor, methodologically weak]).

Two hygiene notes before trusting any local numbers:

- **Do not instrument on PR rejection rate.** Of rejected agentic PRs, only **35.7%** were clear
  agent failures; 31.2% were workflow constraints and 33.1% had no observable rationale. Rejection
  overstates agent error by roughly 3× [measured].
- **Containers now have open egress and full git history.** An audit of 731 trajectories found
  **63%** of one model's successful benchmark resolutions *retrieved* the fix rather than deriving
  it — 57% by finding the merged PR on the web, 9% by mining `.git` [vendor, method stated].
  Restricting both dropped scores 87% → 73%. Harmless for real tickets; fatal for any evaluation
  against past tickets.

---

## 6. Suggested sequence

Nothing here requires the whole list. Ordered by evidence per unit of effort, and by the
one-at-a-time rule:

1. Ticket fields: error info, target files, verification method — plus a pre-flight check. *(Tier 1
   items 1–3; largest measured effect, lowest cost.)*
2. Test execution guaranteed in-container, with the command discoverable. *(Item 4.)*
3. "Abstain or fix" framing and a no-op-is-success outcome. *(Item 5; a prompt change, but one of
   the ones that measurably works.)*
4. Instruction-count budget replacing the token meter. *(Item 6; corrects a shipped design.)*
5. Fail-before/pass-after test validation and freeze. *(Item 7.)*
6. Fresh-context reviewer routed to the human. *(Item 10; mostly a wiring change to what exists.)*
7. Reference-resolution demotion. *(Item 11; corrects a shipped design.)*

Everything below that is optional and should wait for the local eval set to exist.
