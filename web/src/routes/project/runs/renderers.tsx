/*
 * Tool-aware detail renderers (UI spec §5.7 centre pane; interaction rule 4: raw JSON is
 * never the default). One component per payload contract from the S20 adapter:
 *   Edit/Write → diff hunks · Bash → `$ cmd` + collapsible output + exit code · Read → one
 *   line · Grep/Glob → pattern + match count · TodoWrite → checklist · lexicode MCP tools →
 *   labelled cards · unknown → the honest compact fallback (title + result, raw one
 *   disclosure away, never the default).
 *
 * Log-line permalinks (rule 12): every output line renders through <OutputLines>, which
 * numbers lines and reports clicks so the page can write ?line=. The selected line
 * highlights and scrolls into view.
 */
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type ReactNode } from "react";

import type { Elicitation, RunActivity } from "../../../lib/api/client";
import { CostChip } from "../../../components/CostChip/CostChip";
import { useRespondElicitation } from "../../../lib/api/runQueries";
import { formatDuration } from "../../../lib/format/format";
import styles from "./runs.module.css";
import { timingSplit, toolDisplayName } from "./timeline";

// ---- payload shapes (the S20 adapter contracts) -----------------------------------------

interface DiffHunk {
  header: string;
  lines: string[];
}

interface KnownPayload {
  // Read / Edit / Write
  path?: string;
  lines?: number;
  hunks?: DiffHunk[];
  error?: string;
  // Bash
  argv?: string[];
  exit?: number;
  stdout?: string;
  stderr?: string;
  truncated?: boolean;
  // Grep / Glob
  pattern?: string;
  matches?: number;
  // TodoWrite
  items?: Array<{ content?: string; status?: string; activeForm?: string }>;
  // thought / response
  text?: string;
  // error
  subtype?: string;
  result?: string;
  // provision
  step?: string;
  state?: string;
  detail?: string;
  line?: string;
  // ask_human
  questions?: Array<{
    question?: string;
    header?: string;
    multiSelect?: boolean;
    options?: Array<{ label?: string; description?: string }>;
  }>;
  // set_step uses `step`; check_criterion:
  criterion_id?: string;
  met?: boolean;
  note?: string;
  // propose_wiki_page
  slug?: string;
  reason?: string;
  // fallback
  raw?: unknown;
}

function payloadOf(a: RunActivity): KnownPayload {
  const p = a.payload;
  return typeof p === "object" && p !== null ? (p as KnownPayload) : {};
}

// ---- line-addressable output ------------------------------------------------------------

export interface LineSelection {
  selected?: number;
  onSelect: (line: number) => void;
}

/**
 * Numbered, clickable output lines. `start` continues the step's numbering across blocks
 * (stdout then stderr, or successive hunks) so ?line= is unambiguous within a step.
 */
function OutputLines({
  text,
  start,
  sel,
  classify,
}: {
  text: string;
  start: number;
  sel: LineSelection;
  classify?: (line: string) => string | undefined;
}) {
  const selectedRef = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    selectedRef.current?.scrollIntoView({ block: "center" });
  }, [sel.selected]);
  if (text === "") return null;
  const lines = text.replace(/\n$/, "").split("\n");
  return (
    <ol className={styles.outputLines} start={start}>
      {lines.map((line, i) => {
        const n = start + i;
        const isSel = sel.selected === n;
        return (
          <li key={n}>
            <button
              type="button"
              ref={isSel ? selectedRef : undefined}
              className={styles.outputLine}
              data-selected={isSel || undefined}
              data-tone={classify?.(line)}
              onClick={() => sel.onSelect(n)}
            >
              <span className={styles.lineNo} aria-hidden="true">
                {n}
              </span>
              {line === "" ? " " : line}
            </button>
          </li>
        );
      })}
    </ol>
  );
}

function countLines(text: string | undefined): number {
  if (text === undefined || text === "") return 0;
  return text.replace(/\n$/, "").split("\n").length;
}

// ---- shared chrome ----------------------------------------------------------------------

function DetailCard({
  title,
  meta,
  children,
}: {
  title: ReactNode;
  meta?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <article className={styles.detailCard}>
      <header className={styles.detailCardHead}>
        <div className={styles.detailCardTitle}>{title}</div>
        {meta !== undefined && <div className={styles.detailCardMeta}>{meta}</div>}
      </header>
      {children}
    </article>
  );
}

/** The step's own timing + cost, echoed in the centre pane header. */
function StepMeta({ a }: { a: RunActivity }) {
  const split = timingSplit(a);
  return (
    <>
      {split !== null && <span>{formatDuration(split.totalMs)}</span>}
      {a.cost_cents > 0 && (
        <CostChip
          usd={a.cost_cents / 100}
          split={{
            inputTokens: a.tokens_in,
            outputTokens: a.tokens_out,
            cacheReadTokens: a.tokens_cache_read,
          }}
        />
      )}
      {a.attempt > 1 && <span className={styles.retryBadge}>attempt {a.attempt}</span>}
    </>
  );
}

function Collapsible({
  label,
  open: openInitially,
  children,
}: {
  label: string;
  open?: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(openInitially ?? false);
  return (
    <div>
      <button type="button" className={styles.collapseToggle} onClick={() => setOpen(!open)}>
        {open ? "▾" : "▸"} {label}
      </button>
      {open && children}
    </div>
  );
}

// ---- per-tool renderers -----------------------------------------------------------------

function BashDetail({ a, sel }: { a: RunActivity; sel: LineSelection }) {
  const p = payloadOf(a);
  const cmd = p.argv !== undefined && p.argv.length > 0 ? p.argv[p.argv.length - 1] : a.title;
  const stdout = p.stdout ?? "";
  const stderr = p.stderr ?? "";
  const done = p.exit !== undefined;
  const failed = a.ok === false;
  return (
    <DetailCard
      title={<code className={styles.cmdLine}>$ {cmd}</code>}
      meta={<StepMeta a={a} />}
    >
      {done && (
        <div className={styles.exitRow} data-failed={failed || undefined}>
          {failed ? "✕" : "✓"} exit {p.exit}
          {p.truncated === true && <span className={styles.truncNote}> · output truncated</span>}
        </div>
      )}
      {(stdout !== "" || stderr !== "") && (
        <Collapsible label={failed ? "output" : "output"} open={failed || (sel.selected !== undefined)}>
          <OutputLines text={stdout} start={1} sel={sel} />
          <OutputLines
            text={stderr}
            start={countLines(stdout) + 1}
            sel={sel}
            classify={() => "err"}
          />
        </Collapsible>
      )}
      {!done && <div className={styles.pendingNote}>still running…</div>}
    </DetailCard>
  );
}

function DiffDetail({ a, sel }: { a: RunActivity; sel: LineSelection }) {
  const p = payloadOf(a);
  const hunks = p.hunks ?? [];
  let offset = 0;
  return (
    <DetailCard
      title={
        <>
          {a.tool_name} <code>{p.path ?? ""}</code>
        </>
      }
      meta={<StepMeta a={a} />}
    >
      {p.error !== undefined && <pre className={styles.errorText}>{p.error}</pre>}
      {hunks.map((h, i) => {
        const start = offset + 1;
        offset += h.lines.length;
        return (
          <div key={i} className={styles.diffHunk}>
            <div className={styles.diffHeader}>{h.header}</div>
            <OutputLines
              text={h.lines.join("\n")}
              start={start}
              sel={sel}
              classify={(line) =>
                line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : undefined
              }
            />
          </div>
        );
      })}
      {hunks.length === 0 && p.error === undefined && (
        <div className={styles.pendingNote}>no textual change</div>
      )}
    </DetailCard>
  );
}

function ReadDetail({ a }: { a: RunActivity }) {
  const p = payloadOf(a);
  return (
    <DetailCard
      title={
        <>
          Read <code>{p.path ?? ""}</code>
          {p.lines !== undefined && (
            <span className={styles.detailInlineMeta}> · {p.lines} lines</span>
          )}
        </>
      }
      meta={<StepMeta a={a} />}
    />
  );
}

function SearchDetail({ a }: { a: RunActivity }) {
  const p = payloadOf(a);
  return (
    <DetailCard
      title={
        <>
          Search <code>{p.pattern ?? ""}</code>
        </>
      }
      meta={<StepMeta a={a} />}
    >
      {p.matches !== undefined && (
        <div className={styles.detailLine}>
          {p.matches} {p.matches === 1 ? "match" : "matches"}
        </div>
      )}
    </DetailCard>
  );
}

function TodoDetail({ a }: { a: RunActivity }) {
  const items = payloadOf(a).items ?? [];
  return (
    <DetailCard title="Plan" meta={<StepMeta a={a} />}>
      <ul className={styles.todoList}>
        {items.map((it, i) => (
          <li key={i} data-status={it.status}>
            <span aria-hidden="true" className={styles.todoMark}>
              {it.status === "completed" ? "✓" : it.status === "in_progress" ? "●" : "○"}
            </span>
            {it.content ?? ""}
          </li>
        ))}
      </ul>
    </DetailCard>
  );
}

/** The S24 respond context: what the elicitation renderers need to answer inline. */
export interface InterveneContext {
  projectKey: string;
  agentID: string;
  elicitations: Elicitation[];
}

/** ask_human / request_approval rows. A pending elicitation renders its interactive
 * respond surface inline — never a modal (interaction rule; UI spec §5.7). A resolution
 * row (payload {elicitation_id, response}) renders the response. Exported since S36:
 * InlineElicitation embeds this exact component in the home needs-you card and the inbox
 * rows, so answering from a card and answering on the run detail are one code path. */
export function ElicitationDetail({ a, intervene }: { a: RunActivity; intervene?: InterveneContext }) {
  const p = payloadOf(a) as KnownPayload & {
    elicitation_id?: string;
    response?: unknown;
    tool_name?: string;
  };
  if (p.elicitation_id !== undefined && p.questions === undefined && p.tool_name === undefined) {
    return (
      <DetailCard title={a.title} meta={<StepMeta a={a} />}>
        {p.response !== undefined && (
          <Collapsible label="response">
            <pre className={styles.rawFallback}>{JSON.stringify(p.response, null, 2)}</pre>
          </Collapsible>
        )}
      </DetailCard>
    );
  }
  const el = intervene?.elicitations.find((e) => e.activity_seq === a.seq);
  if (el !== undefined && el.kind === "approval") {
    return <ApprovalRow a={a} el={el} intervene={intervene!} />;
  }
  return <QuestionRow a={a} el={el} intervene={intervene} />;
}

// ---- ask_human ---------------------------------------------------------------------------

/** One question's chosen answer: option labels, or the "Other" free text. */
interface Chosen {
  labels: string[];
  other: string;
  useOther: boolean;
}

function QuestionRow({
  a,
  el,
  intervene,
}: {
  a: RunActivity;
  el?: Elicitation;
  intervene?: InterveneContext;
}) {
  const questions = payloadOf(a).questions ?? [];
  const pending = el?.state === "pending" && intervene !== undefined;
  const respond = useRespondElicitation(el?.run_id ?? "");
  const [chosen, setChosen] = useState<Record<number, Chosen>>({});

  const choiceOf = (i: number): Chosen => chosen[i] ?? { labels: [], other: "", useOther: false };
  const setChoice = (i: number, c: Chosen) => setChosen((prev) => ({ ...prev, [i]: c }));

  const complete =
    questions.length > 0 &&
    questions.every((_, i) => {
      const c = choiceOf(i);
      return c.useOther ? c.other.trim() !== "" : c.labels.length > 0;
    });

  const submit = () => {
    if (el === undefined || !complete) return;
    const answers: Record<string, string[]> = {};
    questions.forEach((q, i) => {
      const c = choiceOf(i);
      answers[q.question ?? String(i)] = c.useOther ? [c.other.trim()] : c.labels;
    });
    respond.mutate({ id: el.id, body: { action: "answer", answers } });
  };

  return (
    <DetailCard title={a.title} meta={<StepMeta a={a} />}>
      {questions.map((q, i) => {
        const c = choiceOf(i);
        const multi = q.multiSelect === true;
        return (
          <div key={i} className={styles.questionCard}>
            {q.header !== undefined && q.header !== "" && (
              <span className={styles.questionHeader}>{q.header}</span>
            )}
            <div className={styles.questionText}>{q.question}</div>
            <ul className={styles.optionList}>
              {(q.options ?? []).map((o, j) => {
                const label = o.label ?? "";
                const selected = !c.useOther && c.labels.includes(label);
                return (
                  <li key={j}>
                    <button
                      type="button"
                      className={styles.optionCard}
                      data-selected={selected || undefined}
                      disabled={!pending}
                      onClick={() => {
                        const labels = multi
                          ? selected
                            ? c.labels.filter((l) => l !== label)
                            : [...c.labels, label]
                          : [label];
                        setChoice(i, { labels, other: c.other, useOther: false });
                      }}
                    >
                      <span className={styles.optionLabel}>{label}</span>
                      {o.description !== undefined && o.description !== "" && (
                        <span className={styles.optionDesc}>{o.description}</span>
                      )}
                    </button>
                  </li>
                );
              })}
              <li>
                <div
                  className={styles.optionCard}
                  data-selected={c.useOther || undefined}
                  data-other="true"
                >
                  <span className={styles.optionLabel}>Other</span>
                  <input
                    className={styles.otherInput}
                    placeholder="Type your own answer…"
                    disabled={!pending}
                    value={c.other}
                    onFocus={() => setChoice(i, { ...c, useOther: true })}
                    onChange={(e) =>
                      setChoice(i, { labels: [], other: e.target.value, useOther: true })
                    }
                  />
                </div>
              </li>
            </ul>
          </div>
        );
      })}
      {questions.length === 0 && (
        <pre className={styles.rawFallback}>{JSON.stringify(a.payload, null, 2)}</pre>
      )}
      {pending && intervene !== undefined ? (
        <>
          <button
            type="button"
            className={styles.answerButton}
            disabled={!complete || respond.isPending}
            onClick={submit}
          >
            Answer
          </button>
          {respond.isError && (
            <div className={styles.respondError} role="alert">
              {respond.error.message}
            </div>
          )}
        </>
      ) : (
        <div className={styles.resolvedChip} data-state={el?.state ?? "pending"}>
          {resolvedLabel(el)}
        </div>
      )}
    </DetailCard>
  );
}

// ---- request_approval --------------------------------------------------------------------

interface ApprovalRequestPayload {
  tool_name?: string;
  input?: unknown;
  action?: string;
  scope?: string;
  impact?: string;
  reason?: string;
  alternatives?: string;
  recovery?: string;
}

/** The inline approval row (never a modal): the six card fields the server enriched, the
 * four responses — Approve · Approve with edits · Respond · Deny — and the "Always allow"
 * checkbox, which writes exactly ONE scoped permission rule and links to it in agent
 * settings (interaction rule 8). "Respond" sends the typed text as a deny message the
 * agent reads — a redirect in words rather than a bare no. */
function ApprovalRow({
  a,
  el,
  intervene,
}: {
  a: RunActivity;
  el: Elicitation;
  intervene: InterveneContext;
}) {
  const req = (el.request ?? {}) as ApprovalRequestPayload;
  const pending = el.state === "pending";
  const respond = useRespondElicitation(el.run_id);
  const [mode, setMode] = useState<"none" | "edits" | "respond">("none");
  const [alwaysAllow, setAlwaysAllow] = useState(false);
  const [edited, setEdited] = useState(() => JSON.stringify(req.input ?? {}, null, 2));
  const [message, setMessage] = useState("");
  const [editError, setEditError] = useState<string | null>(null);
  const [ruleID, setRuleID] = useState<string | null>(null);

  const act = (body: Parameters<typeof respond.mutate>[0]["body"]) => {
    respond.mutate(
      { id: el.id, body },
      {
        onSuccess: (res) => {
          if (res.rule_id !== undefined && res.rule_id !== "") setRuleID(res.rule_id);
        },
      },
    );
  };

  const fields: Array<[string, string | undefined]> = [
    ["Scope", req.scope],
    ["Impact", req.impact],
    ["Reason", req.reason],
    ["Alternatives", req.alternatives],
    ["Recovery", req.recovery],
  ];

  return (
    <DetailCard
      title={<span className={styles.approvalTitle}>▲ {req.action ?? a.title}</span>}
      meta={<StepMeta a={a} />}
    >
      <dl className={styles.approvalFields}>
        {fields.map(([label, value]) =>
          value !== undefined && value !== "" ? (
            <div key={label} className={styles.approvalField}>
              <dt>{label}</dt>
              <dd>{value}</dd>
            </div>
          ) : null,
        )}
      </dl>
      {req.input !== undefined && (
        <Collapsible label="tool input">
          <pre className={styles.rawFallback}>{JSON.stringify(req.input, null, 2)}</pre>
        </Collapsible>
      )}

      {pending ? (
        <div className={styles.approvalControls}>
          {mode === "edits" && (
            <div className={styles.approvalEditor}>
              <textarea
                className={styles.approvalTextarea}
                value={edited}
                rows={8}
                onChange={(e) => {
                  setEdited(e.target.value);
                  setEditError(null);
                }}
                aria-label="Edited tool input (JSON)"
              />
              {editError !== null && (
                <div className={styles.respondError} role="alert">
                  {editError}
                </div>
              )}
            </div>
          )}
          {mode === "respond" && (
            <textarea
              className={styles.approvalTextarea}
              value={message}
              rows={3}
              placeholder="Tell the agent what to do instead…"
              onChange={(e) => setMessage(e.target.value)}
              aria-label="Response to the agent"
            />
          )}
          <div className={styles.approvalButtons}>
            {mode === "edits" ? (
              <button
                type="button"
                className={styles.answerButton}
                disabled={respond.isPending}
                onClick={() => {
                  try {
                    const parsed: unknown = JSON.parse(edited);
                    act({
                      action: "approve_with_edits",
                      updated_input: parsed as Record<string, never>,
                    });
                  } catch {
                    setEditError("The edited input is not valid JSON.");
                  }
                }}
              >
                ✓ Approve with these edits
              </button>
            ) : mode === "respond" ? (
              <button
                type="button"
                className={styles.answerButton}
                disabled={respond.isPending || message.trim() === ""}
                onClick={() => act({ action: "deny", message: message.trim() })}
              >
                ↩ Send response
              </button>
            ) : (
              <button
                type="button"
                className={styles.answerButton}
                disabled={respond.isPending}
                onClick={() => act(alwaysAllow ? { action: "remember" } : { action: "approve" })}
              >
                ✓ Approve
              </button>
            )}
            <button
              type="button"
              className={styles.approvalSecondary}
              data-active={mode === "edits" || undefined}
              onClick={() => setMode(mode === "edits" ? "none" : "edits")}
            >
              Approve with edits
            </button>
            <button
              type="button"
              className={styles.approvalSecondary}
              data-active={mode === "respond" || undefined}
              onClick={() => setMode(mode === "respond" ? "none" : "respond")}
            >
              Respond
            </button>
            <button
              type="button"
              className={styles.approvalSecondary}
              data-danger="true"
              disabled={respond.isPending}
              onClick={() => act({ action: "deny" })}
            >
              ✕ Deny
            </button>
          </div>
          <label className={styles.alwaysAllow}>
            <input
              type="checkbox"
              checked={alwaysAllow}
              onChange={(e) => setAlwaysAllow(e.target.checked)}
            />
            Always allow {req.tool_name ?? "this tool"} like this — writes one scoped rule in
            agent settings
          </label>
          {respond.isError && (
            <div className={styles.respondError} role="alert">
              {respond.error.message}
            </div>
          )}
        </div>
      ) : (
        <div className={styles.resolvedChip} data-state={el.state}>
          {resolvedLabel(el)}
        </div>
      )}
      {ruleID !== null && (
        <div className={styles.ruleAdded}>
          Rule added —{" "}
          <Link
            to="/p/$key/agents/$id"
            params={{ key: intervene.projectKey, id: intervene.agentID }}
            className={styles.ruleLink}
          >
            view in agent settings
          </Link>
        </div>
      )}
    </DetailCard>
  );
}

function resolvedLabel(el?: Elicitation): string {
  switch (el?.state) {
    case "answered":
      return el.kind === "approval" ? "✓ Approved" : "↩ Answered";
    case "denied":
      return "✕ Denied";
    case "expired":
      return "Expired without an answer";
    case "canceled":
      return "Canceled with the run";
    default:
      return "Waiting…";
  }
}

/** The lexicode MCP tools get labelled cards; other MCP tools a labelled honest card. */
function MCPDetail({ a }: { a: RunActivity }) {
  const p = payloadOf(a);
  const label = toolDisplayName(a.tool_name);
  let body: ReactNode = null;
  switch (a.tool_name) {
    case "mcp__lexicode__set_step":
      body = <div className={styles.detailLine}>current step → “{p.step ?? ""}”</div>;
      break;
    case "mcp__lexicode__check_criterion":
      body = (
        <div className={styles.detailLine}>
          {p.met === true ? "✓ met" : "○ unmet"}
          {p.note !== undefined && p.note !== "" && <> — {p.note}</>}
        </div>
      );
      break;
    case "mcp__lexicode__propose_wiki_page":
      body = (
        <div className={styles.detailLine}>
          proposed <code>{p.slug ?? ""}</code>
          {p.reason !== undefined && p.reason !== "" && <> — {p.reason}</>}
        </div>
      );
      break;
    default:
      body = (
        <Collapsible label="parameters">
          <pre className={styles.rawFallback}>
            {JSON.stringify(p.raw ?? a.payload, null, 2)}
          </pre>
        </Collapsible>
      );
  }
  return (
    <DetailCard title={<span className={styles.mcpLabel}>{label}</span>} meta={<StepMeta a={a} />}>
      {body}
      {typeof p.result === "string" && p.result !== "" && (
        <Collapsible label="result">
          <pre className={styles.rawFallback}>{p.result}</pre>
        </Collapsible>
      )}
    </DetailCard>
  );
}

/** Unknown tool: the honest compact fallback — the title line, the result if any, and the
 * raw input one disclosure away. Never raw JSON as the default rendering. */
function UnknownToolDetail({ a, sel }: { a: RunActivity; sel: LineSelection }) {
  const p = payloadOf(a);
  return (
    <DetailCard title={a.title} meta={<StepMeta a={a} />}>
      {typeof p.result === "string" && p.result !== "" && (
        <OutputLines text={p.result} start={1} sel={sel} />
      )}
      {p.truncated === true && <div className={styles.truncNote}>output truncated</div>}
      {p.raw !== undefined && (
        <Collapsible label="raw input">
          <pre className={styles.rawFallback}>{JSON.stringify(p.raw, null, 2)}</pre>
        </Collapsible>
      )}
    </DetailCard>
  );
}

function ProvisionDetail({ a }: { a: RunActivity }) {
  const p = payloadOf(a);
  if (p.line !== undefined) {
    return <DetailCard title={<code>{p.line}</code>} />;
  }
  return (
    <DetailCard
      title={
        <>
          {p.state === "ok" ? "✓" : p.state === "failed" ? "✕" : p.state === "running" ? "●" : "○"}{" "}
          {p.step ?? a.title}
        </>
      }
    >
      {p.detail !== undefined && p.detail !== "" && (
        <div className={styles.detailLine}>{p.detail}</div>
      )}
    </DetailCard>
  );
}

// ---- the dispatcher ---------------------------------------------------------------------

export function ActivityDetail({
  a,
  sel,
  intervene,
}: {
  a: RunActivity;
  sel: LineSelection;
  intervene?: InterveneContext;
}) {
  switch (a.type) {
    case "thought":
      return (
        <DetailCard title="Thought" meta={<StepMeta a={a} />}>
          <p className={styles.prose}>{payloadOf(a).text ?? a.title}</p>
        </DetailCard>
      );
    case "response":
      return (
        <article className={styles.outcomeBlock}>
          <header className={styles.outcomeHead}>
            ✓ Outcome
            <StepMeta a={a} />
          </header>
          <p className={styles.prose}>{payloadOf(a).text ?? a.title}</p>
        </article>
      );
    case "error": {
      const p = payloadOf(a);
      return (
        <article className={styles.outcomeBlock} data-failed="true">
          <header className={styles.outcomeHead}>✕ {p.subtype !== undefined && p.subtype !== "" ? p.subtype : "Error"}</header>
          <p className={styles.prose}>{p.result !== undefined && p.result !== "" ? p.result : a.title}</p>
        </article>
      );
    }
    case "elicitation":
      return <ElicitationDetail a={a} intervene={intervene} />;
    case "provision":
      return <ProvisionDetail a={a} />;
    case "system":
      return <DetailCard title={a.title} meta={<StepMeta a={a} />} />;
    case "action":
      break;
  }
  switch (a.tool_name) {
    case "Bash":
      return <BashDetail a={a} sel={sel} />;
    case "Edit":
    case "Write":
      return <DiffDetail a={a} sel={sel} />;
    case "Read":
      return <ReadDetail a={a} />;
    case "Grep":
    case "Glob":
      return <SearchDetail a={a} />;
    case "TodoWrite":
      return <TodoDetail a={a} />;
    default:
      if (a.tool_name.startsWith("mcp__")) return <MCPDetail a={a} />;
      return <UnknownToolDetail a={a} sel={sel} />;
  }
}
