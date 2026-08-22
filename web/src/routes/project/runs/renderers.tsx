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
import { useEffect, useRef, useState, type ReactNode } from "react";

import type { RunActivity } from "../../../lib/api/client";
import { CostChip } from "../../../components/CostChip/CostChip";
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
      {a.cost_cents > 0 && <CostChip usd={a.cost_cents / 100} />}
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

/** ask_human — read-only in S23: header chips, option cards, the disabled Answer control.
 * Answering (option pick + the "Other" free-text) is S24's composer. A resolution row
 * (the MCP server's "Answered: …" activity, payload {elicitation_id, response}) renders
 * the response, not a question card. */
function ElicitationDetail({ a }: { a: RunActivity }) {
  const p = payloadOf(a) as KnownPayload & { elicitation_id?: string; response?: unknown };
  if (p.elicitation_id !== undefined && p.questions === undefined) {
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
  const questions = p.questions ?? [];
  return (
    <DetailCard title={a.title} meta={<StepMeta a={a} />}>
      {questions.map((q, i) => (
        <div key={i} className={styles.questionCard}>
          {q.header !== undefined && q.header !== "" && (
            <span className={styles.questionHeader}>{q.header}</span>
          )}
          <div className={styles.questionText}>{q.question}</div>
          <ul className={styles.optionList}>
            {(q.options ?? []).map((o, j) => (
              <li key={j} className={styles.optionCard}>
                <span className={styles.optionLabel}>{o.label}</span>
                {o.description !== undefined && o.description !== "" && (
                  <span className={styles.optionDesc}>{o.description}</span>
                )}
              </li>
            ))}
          </ul>
        </div>
      ))}
      {questions.length === 0 && (
        <pre className={styles.rawFallback}>{JSON.stringify(a.payload, null, 2)}</pre>
      )}
      <button
        type="button"
        className={styles.answerButton}
        disabled
        title="Answering arrives with story S24"
      >
        Answer
      </button>
    </DetailCard>
  );
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

export function ActivityDetail({ a, sel }: { a: RunActivity; sel: LineSelection }) {
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
      return <ElicitationDetail a={a} />;
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
