/*
 * Tool-aware detail renderers (UI spec §5.7 centre pane; interaction rule 4: raw JSON is
 * never the default) — converted to MUI as part of the LEXI-13 proof of concept.
 *
 * One component per payload contract from the S20 adapter:
 *   Edit/Write → diff hunks · Bash → `$ cmd` + collapsible output + exit code · Read → one
 *   line · Grep/Glob → pattern + match count · TodoWrite → checklist · lexicode MCP tools →
 *   labelled cards · unknown → the honest compact fallback (title + result, raw one
 *   disclosure away, never the default).
 *
 * Composition notes (no invented components):
 * - The card shell is MUI `Card` + `CardHeader` + `CardContent`.
 * - Every disclosure is MUI `Accordion` — previously a hand-rolled ▸/▾ button.
 * - Answer options are a MUI `ToggleButtonGroup`; single-select questions use `exclusive`,
 *   multi-select ones do not, so the widget itself states which kind of question it is.
 * - A log line is a MUI `ButtonBase` (the documented primitive behind every clickable MUI
 *   surface). `ListItemButton` per line would cost far too much for a 5,000-line log.
 * - `CostChip` stays for now: it is shared with the run list and the project header, which
 *   this run does not convert (plan/06-ui-redesign-plan.md, stage 3).
 *
 * Log-line permalinks (rule 12): every output line renders through <OutputLines>, which
 * numbers lines and reports clicks so the page can write ?line=. The selected line
 * highlights and scrolls into view. The "copy this link" affordance itself is a visible
 * button in the page toolbar (RunDetailPage) — it used to be tacit knowledge.
 */
import Accordion from "@mui/material/Accordion";
import AccordionDetails from "@mui/material/AccordionDetails";
import AccordionSummary from "@mui/material/AccordionSummary";
import Alert from "@mui/material/Alert";
import AlertTitle from "@mui/material/AlertTitle";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import ButtonBase from "@mui/material/ButtonBase";
import Card from "@mui/material/Card";
import CardContent from "@mui/material/CardContent";
import CardHeader from "@mui/material/CardHeader";
import Checkbox from "@mui/material/Checkbox";
import Chip from "@mui/material/Chip";
import FormControlLabel from "@mui/material/FormControlLabel";
import MuiLink from "@mui/material/Link";
import List from "@mui/material/List";
import ListItem from "@mui/material/ListItem";
import ListItemText from "@mui/material/ListItemText";
import Stack from "@mui/material/Stack";
import TextField from "@mui/material/TextField";
import ToggleButton from "@mui/material/ToggleButton";
import ToggleButtonGroup from "@mui/material/ToggleButtonGroup";
import Typography from "@mui/material/Typography";
import { useEffect, useRef, useState, type ReactNode } from "react";

import type { Elicitation, RunActivity } from "../../../lib/api/client";
import { CostChip } from "../../../components/CostChip/CostChip";
import { RouterLink } from "../../../components/RouterLink/RouterLink";
import { useRespondElicitation } from "../../../lib/api/runQueries";
import { formatDuration } from "../../../lib/format/format";
import { MONO_FONT } from "../../../styles/muiTheme";
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

const monoSx = { fontFamily: MONO_FONT, fontSize: 12 } as const;

/**
 * A status glyph inside a control's label — a `Box` span, exactly the pattern the timeline
 * rows already use, named so the rule is greppable rather than remembered.
 *
 * §4 requires that colour is never the sole carrier; the WORD beside the glyph is what
 * satisfies that, so the glyph itself is decoration. Left visible it becomes part of the
 * control's accessible name and a screen reader announces "✓ Approve" as "check mark,
 * Approve". Every glyph that sits inside a Button or a Chip label goes through here.
 */
export function Glyph({ children }: { children: ReactNode }) {
  return (
    <Box component="span" aria-hidden="true" sx={{ mr: 0.5 }}>
      {children}
    </Box>
  );
}

// ---- line-addressable output ------------------------------------------------------------

export interface LineSelection {
  selected?: number;
  onSelect: (line: number) => void;
}

/** Tone → the §3.2 semantic token a log line is tinted with. */
const LINE_TONE: Record<string, string> = {
  err: "var(--fail)",
  add: "var(--ok)",
  del: "var(--fail)",
};

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
    <Box component="ol" start={start} sx={{ m: 0, p: 0, listStyle: "none" }}>
      {lines.map((line, i) => {
        const n = start + i;
        const isSel = sel.selected === n;
        const tone = classify?.(line);
        return (
          <li key={n}>
            <ButtonBase
              ref={isSel ? selectedRef : undefined}
              onClick={() => sel.onSelect(n)}
              sx={{
                ...monoSx,
                display: "flex",
                width: "100%",
                justifyContent: "flex-start",
                textAlign: "left",
                whiteSpace: "pre",
                px: 0.5,
                color: tone !== undefined ? LINE_TONE[tone] : "text.primary",
                bgcolor: isSel ? "action.selected" : "transparent",
                "&:hover": { bgcolor: "action.hover" },
              }}
            >
              <Box component="span" aria-hidden="true" sx={{ ...monoSx, width: 40, color: "text.disabled" }}>
                {n}
              </Box>
              {line === "" ? " " : line}
            </ButtonBase>
          </li>
        );
      })}
    </Box>
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
    <Card variant="outlined">
      <CardHeader
        title={title}
        action={
          meta !== undefined ? (
            <Stack direction="row" spacing={1} sx={{ alignItems: "center" }}>
              {meta}
            </Stack>
          ) : undefined
        }
        slotProps={{ title: { variant: "h3", component: "div" } }}
        sx={{ py: 1, px: 1.5 }}
      />
      {children !== undefined && <CardContent sx={{ pt: 0, px: 1.5 }}>{children}</CardContent>}
    </Card>
  );
}

/** The step's own timing + cost, echoed in the centre pane header. */
function StepMeta({ a }: { a: RunActivity }) {
  const split = timingSplit(a);
  return (
    <>
      {split !== null && (
        <Typography variant="caption" sx={{ fontFamily: MONO_FONT }}>
          {formatDuration(split.totalMs)}
        </Typography>
      )}
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
      {a.attempt > 1 && <Chip size="small" color="warning" label={`attempt ${a.attempt}`} />}
    </>
  );
}

/** Every disclosure on this screen is the same MUI Accordion. */
function Collapsible({
  label,
  open: openInitially,
  children,
}: {
  label: string;
  open?: boolean;
  children: ReactNode;
}) {
  return (
    <Accordion
      disableGutters
      defaultExpanded={openInitially ?? false}
      elevation={0}
      sx={{ "&::before": { display: "none" }, border: 1, borderColor: "divider", mt: 1 }}
    >
      <AccordionSummary
        expandIcon={<Box aria-hidden="true">▾</Box>}
        sx={{ minHeight: 32, "& .MuiAccordionSummary-content": { my: 0.5 } }}
      >
        <Typography variant="body2">{label}</Typography>
      </AccordionSummary>
      <AccordionDetails sx={{ pt: 0, overflowX: "auto" }}>{children}</AccordionDetails>
    </Accordion>
  );
}

/** Raw payloads, when a disclosure is opened deliberately. Never a default rendering. */
function Raw({ value }: { value: unknown }) {
  return (
    <Box component="pre" sx={{ ...monoSx, m: 0, whiteSpace: "pre-wrap" }}>
      {typeof value === "string" ? value : JSON.stringify(value, null, 2)}
    </Box>
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
      title={
        <Box component="code" sx={monoSx}>
          $ {cmd}
        </Box>
      }
      meta={<StepMeta a={a} />}
    >
      {done && (
        // `role="presentation"`, not MUI's default `role="alert"`: an exit code is content
        // the centre pane displays, and the pane remounts on every selection — see the
        // header note in RunDetailPage.tsx.
        <Alert
          severity={failed ? "error" : "success"}
          role="presentation"
          icon={<span aria-hidden="true">{failed ? "✕" : "✓"}</span>}
          sx={{ py: 0 }}
        >
          exit {p.exit}
          {p.truncated === true && " · output truncated"}
        </Alert>
      )}
      {(stdout !== "" || stderr !== "") && (
        <Collapsible label="Output" open={failed || sel.selected !== undefined}>
          <OutputLines text={stdout} start={1} sel={sel} />
          <OutputLines
            text={stderr}
            start={countLines(stdout) + 1}
            sel={sel}
            classify={() => "err"}
          />
        </Collapsible>
      )}
      {!done && (
        <Typography variant="body2" color="text.secondary">
          still running…
        </Typography>
      )}
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
          {a.tool_name}{" "}
          <Box component="code" sx={monoSx}>
            {p.path ?? ""}
          </Box>
        </>
      }
      meta={<StepMeta a={a} />}
    >
      {p.error !== undefined && (
        <Alert severity="error" role="presentation">
          <Raw value={p.error} />
        </Alert>
      )}
      {hunks.map((h, i) => {
        const start = offset + 1;
        offset += h.lines.length;
        return (
          <Box key={i} sx={{ mt: 1, border: 1, borderColor: "divider", overflowX: "auto" }}>
            <Typography variant="caption" sx={{ ...monoSx, display: "block", px: 0.5, bgcolor: "action.hover" }}>
              {h.header}
            </Typography>
            <OutputLines
              text={h.lines.join("\n")}
              start={start}
              sel={sel}
              classify={(line) =>
                line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : undefined
              }
            />
          </Box>
        );
      })}
      {hunks.length === 0 && p.error === undefined && (
        <Typography variant="body2" color="text.secondary">
          no textual change
        </Typography>
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
          Read{" "}
          <Box component="code" sx={monoSx}>
            {p.path ?? ""}
          </Box>
          {p.lines !== undefined && (
            <Typography component="span" variant="caption" color="text.secondary">
              {" "}
              · {p.lines} lines
            </Typography>
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
          Search{" "}
          <Box component="code" sx={monoSx}>
            {p.pattern ?? ""}
          </Box>
        </>
      }
      meta={<StepMeta a={a} />}
    >
      {p.matches !== undefined && (
        <Typography variant="body2">
          {p.matches} {p.matches === 1 ? "match" : "matches"}
        </Typography>
      )}
    </DetailCard>
  );
}

function TodoDetail({ a }: { a: RunActivity }) {
  const items = payloadOf(a).items ?? [];
  return (
    <DetailCard title="Plan" meta={<StepMeta a={a} />}>
      <List dense disablePadding>
        {items.map((it, i) => (
          <ListItem key={i} disableGutters sx={{ py: 0 }}>
            <Box
              component="span"
              aria-hidden="true"
              sx={{
                width: 16,
                color:
                  it.status === "completed"
                    ? "var(--ok)"
                    : it.status === "in_progress"
                      ? "var(--running)"
                      : "var(--muted)",
              }}
            >
              {it.status === "completed" ? "✓" : it.status === "in_progress" ? "●" : "○"}
            </Box>
            <ListItemText
              primary={it.content ?? ""}
              secondary={it.status === "completed" ? "done" : (it.status ?? "")}
              slotProps={{
                primary: { variant: "body2" },
                secondary: { variant: "caption" },
              }}
            />
          </ListItem>
        ))}
      </List>
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
export function ElicitationDetail({
  a,
  intervene,
}: {
  a: RunActivity;
  intervene?: InterveneContext;
}) {
  const p = payloadOf(a) as KnownPayload & {
    elicitation_id?: string;
    response?: unknown;
    tool_name?: string;
  };
  if (p.elicitation_id !== undefined && p.questions === undefined && p.tool_name === undefined) {
    return (
      <DetailCard title={a.title} meta={<StepMeta a={a} />}>
        {p.response !== undefined && (
          <Collapsible label="Response">
            <Raw value={p.response} />
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
        const labelId = `question-${a.seq}-${i}`;
        return (
          <Box key={i} sx={{ mb: 2 }}>
            {q.header !== undefined && q.header !== "" && (
              <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                {q.header}
              </Typography>
            )}
            <Typography variant="body1" id={labelId} gutterBottom>
              {q.question}
            </Typography>
            <Typography variant="caption" color="text.secondary" sx={{ display: "block" }} gutterBottom>
              {multi ? "Choose any that apply." : "Choose one."}
            </Typography>
            <ToggleButtonGroup
              orientation="vertical"
              fullWidth
              exclusive={!multi}
              disabled={!pending}
              aria-labelledby={labelId}
              value={c.useOther ? (multi ? [] : null) : multi ? c.labels : (c.labels[0] ?? null)}
              onChange={(_e, next: string | string[] | null) => {
                const labels =
                  next === null ? [] : Array.isArray(next) ? next : [next];
                setChoice(i, { labels, other: c.other, useOther: false });
              }}
            >
              {(q.options ?? []).map((o, j) => (
                <ToggleButton
                  key={j}
                  value={o.label ?? ""}
                  sx={{ justifyContent: "flex-start", textAlign: "left", display: "block" }}
                >
                  <Typography variant="body2" component="span" sx={{ display: "block" }}>
                    {o.label ?? ""}
                  </Typography>
                  {o.description !== undefined && o.description !== "" && (
                    <Typography variant="caption" color="text.secondary" sx={{ display: "block" }}>
                      {o.description}
                    </Typography>
                  )}
                </ToggleButton>
              ))}
            </ToggleButtonGroup>
            <TextField
              fullWidth
              size="small"
              sx={{ mt: 1 }}
              label="Other — type your own answer"
              disabled={!pending}
              value={c.other}
              onFocus={() => setChoice(i, { ...c, useOther: true })}
              onChange={(e) =>
                setChoice(i, { labels: [], other: e.target.value, useOther: true })
              }
            />
          </Box>
        );
      })}
      {questions.length === 0 && <Raw value={a.payload} />}
      {pending && intervene !== undefined ? (
        <>
          <Button
            variant="contained"
            className={styles.answerButton}
            disabled={!complete || respond.isPending}
            onClick={submit}
          >
            Answer
          </Button>
          {respond.isError && (
            <Alert severity="error" role="alert" sx={{ mt: 1 }}>
              {respond.error.message}
            </Alert>
          )}
        </>
      ) : (
        <Chip
          size="small"
          variant="outlined"
          label={<ResolvedLabel el={el} />}
          color={el?.state === "denied" ? "error" : "default"}
        />
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
      title={
        <>
          <Box component="span" aria-hidden="true" sx={{ color: "var(--needs-you)" }}>
            ▲{" "}
          </Box>
          {req.action ?? a.title}
        </>
      }
      meta={<StepMeta a={a} />}
    >
      <Box component="dl" sx={{ m: 0, mb: 1 }}>
        {fields.map(([label, value]) =>
          value !== undefined && value !== "" ? (
            <Box key={label} sx={{ display: "flex", gap: 1, py: 0.25 }}>
              <Typography component="dt" variant="caption" color="text.secondary" sx={{ width: 92, flexShrink: 0 }}>
                {label}
              </Typography>
              <Typography component="dd" variant="body2" sx={{ m: 0 }}>
                {value}
              </Typography>
            </Box>
          ) : null,
        )}
      </Box>
      {req.input !== undefined && (
        <Collapsible label="Tool input">
          <Raw value={req.input} />
        </Collapsible>
      )}

      {pending ? (
        <Stack spacing={1} sx={{ mt: 1 }}>
          {mode === "edits" && (
            <>
              <TextField
                multiline
                minRows={8}
                fullWidth
                value={edited}
                label="Edited tool input (JSON)"
                slotProps={{ htmlInput: { style: { fontFamily: MONO_FONT, fontSize: 12 } } }}
                onChange={(e) => {
                  setEdited(e.target.value);
                  setEditError(null);
                }}
              />
              {editError !== null && (
                <Alert severity="error" role="alert">
                  {editError}
                </Alert>
              )}
            </>
          )}
          {mode === "respond" && (
            <TextField
              multiline
              minRows={3}
              fullWidth
              value={message}
              label="Response to the agent"
              placeholder="Tell the agent what to do instead…"
              onChange={(e) => setMessage(e.target.value)}
            />
          )}
          <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: "wrap" }}>
            {mode === "edits" ? (
              <Button
                variant="contained"
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
                <Glyph>✓</Glyph>Approve with these edits
              </Button>
            ) : mode === "respond" ? (
              <Button
                variant="contained"
                disabled={respond.isPending || message.trim() === ""}
                onClick={() => act({ action: "deny", message: message.trim() })}
              >
                <Glyph>↩</Glyph>Send response
              </Button>
            ) : (
              <Button
                variant="contained"
                disabled={respond.isPending}
                onClick={() => act(alwaysAllow ? { action: "remember" } : { action: "approve" })}
              >
                <Glyph>✓</Glyph>Approve
              </Button>
            )}
            <Button
              variant={mode === "edits" ? "contained" : "outlined"}
              color="inherit"
              onClick={() => setMode(mode === "edits" ? "none" : "edits")}
            >
              Approve with edits
            </Button>
            <Button
              variant={mode === "respond" ? "contained" : "outlined"}
              color="inherit"
              onClick={() => setMode(mode === "respond" ? "none" : "respond")}
            >
              Respond
            </Button>
            <Button
              variant="outlined"
              color="error"
              disabled={respond.isPending}
              onClick={() => act({ action: "deny" })}
            >
              <Glyph>✕</Glyph>Deny
            </Button>
          </Stack>
          <FormControlLabel
            control={
              <Checkbox
                size="small"
                checked={alwaysAllow}
                onChange={(e) => setAlwaysAllow(e.target.checked)}
              />
            }
            label={
              <Typography variant="body2">
                Always allow {req.tool_name ?? "this tool"} like this — writes one scoped
                rule in agent settings
              </Typography>
            }
          />
          {respond.isError && (
            <Alert severity="error" role="alert">
              {respond.error.message}
            </Alert>
          )}
        </Stack>
      ) : (
        <Chip
          size="small"
          variant="outlined"
          label={<ResolvedLabel el={el} />}
          color={el.state === "denied" ? "error" : "default"}
        />
      )}
      {ruleID !== null && (
        // The one alert here that appears in response to a click rather than a selection, so
        // it does announce — but politely (`role="status"`), not with MUI's assertive default.
        <Alert severity="info" role="status" sx={{ mt: 1 }}>
          Rule added —{" "}
          <MuiLink
            component={RouterLink}
            to="/p/$key/agents/$id"
            params={{ key: intervene.projectKey, id: intervene.agentID }}
          >
            view in agent settings
          </MuiLink>
        </Alert>
      )}
    </DetailCard>
  );
}

/** How an elicitation ended: a decorative glyph, and the word that actually says it. */
function resolution(el?: Elicitation): { glyph?: string; text: string } {
  switch (el?.state) {
    case "answered":
      return el.kind === "approval"
        ? { glyph: "✓", text: "Approved" }
        : { glyph: "↩", text: "Answered" };
    case "denied":
      return { glyph: "✕", text: "Denied" };
    case "expired":
      return { text: "Expired without an answer" };
    case "canceled":
      return { text: "Canceled with the run" };
    default:
      return { text: "Waiting…" };
  }
}

function ResolvedLabel({ el }: { el?: Elicitation }) {
  const { glyph, text } = resolution(el);
  return (
    <>
      {glyph !== undefined && <Glyph>{glyph}</Glyph>}
      {text}
    </>
  );
}

/** The lexicode MCP tools get labelled cards; other MCP tools a labelled honest card. */
function MCPDetail({ a }: { a: RunActivity }) {
  const p = payloadOf(a);
  const label = toolDisplayName(a.tool_name);
  let body: ReactNode = null;
  switch (a.tool_name) {
    case "mcp__lexicode__set_step":
      body = <Typography variant="body2">current step → “{p.step ?? ""}”</Typography>;
      break;
    case "mcp__lexicode__check_criterion":
      body = (
        <Typography variant="body2">
          {p.met === true ? "✓ met" : "○ unmet"}
          {p.note !== undefined && p.note !== "" && <> — {p.note}</>}
        </Typography>
      );
      break;
    case "mcp__lexicode__propose_wiki_page":
      body = (
        <Typography variant="body2">
          proposed{" "}
          <Box component="code" sx={monoSx}>
            {p.slug ?? ""}
          </Box>
          {p.reason !== undefined && p.reason !== "" && <> — {p.reason}</>}
        </Typography>
      );
      break;
    default:
      body = (
        <Collapsible label="Parameters">
          <Raw value={p.raw ?? a.payload} />
        </Collapsible>
      );
  }
  return (
    <DetailCard title={label} meta={<StepMeta a={a} />}>
      {body}
      {typeof p.result === "string" && p.result !== "" && (
        <Collapsible label="Result">
          <Raw value={p.result} />
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
      {p.truncated === true && (
        <Typography variant="caption" color="text.secondary">
          output truncated
        </Typography>
      )}
      {p.raw !== undefined && (
        <Collapsible label="Raw input">
          <Raw value={p.raw} />
        </Collapsible>
      )}
    </DetailCard>
  );
}

function ProvisionDetail({ a }: { a: RunActivity }) {
  const p = payloadOf(a);
  if (p.line !== undefined) {
    return (
      <DetailCard
        title={
          <Box component="code" sx={monoSx}>
            {p.line}
          </Box>
        }
      />
    );
  }
  return (
    <DetailCard
      title={
        <>
          <Box component="span" aria-hidden="true">
            {p.state === "ok" ? "✓" : p.state === "failed" ? "✕" : p.state === "running" ? "●" : "○"}{" "}
          </Box>
          {p.step ?? a.title}
        </>
      }
    >
      {p.detail !== undefined && p.detail !== "" && (
        <Typography variant="body2">{p.detail}</Typography>
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
          <Typography variant="body1">{payloadOf(a).text ?? a.title}</Typography>
        </DetailCard>
      );
    case "response":
      return (
        <Alert severity="success" role="presentation" icon={<span aria-hidden="true">✓</span>}>
          <AlertTitle>
            <Stack direction="row" spacing={1} sx={{ alignItems: "center" }}>
              <span>Outcome</span>
              <StepMeta a={a} />
            </Stack>
          </AlertTitle>
          {payloadOf(a).text ?? a.title}
        </Alert>
      );
    case "error": {
      const p = payloadOf(a);
      return (
        <Alert severity="error" role="presentation" icon={<span aria-hidden="true">✕</span>}>
          <AlertTitle>
            {p.subtype !== undefined && p.subtype !== "" ? p.subtype : "Error"}
          </AlertTitle>
          {p.result !== undefined && p.result !== "" ? p.result : a.title}
        </Alert>
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
