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
 *
 * D-1 (amended) — what each thing is, in library terms:
 *   DetailCard      Paper(variant="outlined") + Typography
 *   Collapsible     Accordion / AccordionSummary / AccordionDetails
 *   OutputLines     List(component="ol") + ListItemButton, dense, monospace
 *   answer options  ToggleButtonGroup + ToggleButton (exclusive or multiple per question)
 *   approvals       Button · Checkbox+FormControlLabel · TextField · Alert · Chip
 *
 * Two of these are compositions rather than a single component, and both are named as such:
 * a numbered, line-addressable log is not a Material UI component, and neither is a diff
 * hunk. Both are built from List/ListItemButton and Paper/Typography respectively, with no
 * bespoke CSS beyond the §3 tokens the theme already exposes.
 */
import { useEffect, useRef, useState, type ReactNode } from "react";
import Accordion from "@mui/material/Accordion";
import AccordionDetails from "@mui/material/AccordionDetails";
import AccordionSummary from "@mui/material/AccordionSummary";
import Alert from "@mui/material/Alert";
import Box from "@mui/material/Box";
import Button from "@mui/material/Button";
import Checkbox from "@mui/material/Checkbox";
import Chip from "@mui/material/Chip";
import FormControlLabel from "@mui/material/FormControlLabel";
import List from "@mui/material/List";
import ListItem from "@mui/material/ListItem";
import ListItemButton from "@mui/material/ListItemButton";
import Paper from "@mui/material/Paper";
import Stack from "@mui/material/Stack";
import TextField from "@mui/material/TextField";
import ToggleButton from "@mui/material/ToggleButton";
import ToggleButtonGroup from "@mui/material/ToggleButtonGroup";
import Typography from "@mui/material/Typography";

import type { Elicitation, RunActivity } from "../../../lib/api/client";
import { CostChip } from "../../../components/CostChip/CostChip";
import { useRespondElicitation } from "../../../lib/api/runQueries";
import { formatDuration } from "../../../lib/format/format";
import { AppLink } from "../../../theme/routerLinks";
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

/** Machine output, everywhere it appears: §3.3's monospace stack at the mono size. */
const MONO = {
  fontFamily: "var(--font-mono)",
  fontSize: "var(--fs-mono)",
} as const;

/** The line tones an output line can carry, as §3.2 palette paths. */
const LINE_TONE: Record<string, string> = {
  add: "success.main",
  del: "error.main",
  err: "error.main",
};

// ---- line-addressable output ------------------------------------------------------------

export interface LineSelection {
  selected?: number;
  onSelect: (line: number) => void;
}

/**
 * Numbered, clickable output lines. `start` continues the step's numbering across blocks
 * (stdout then stderr, or successive hunks) so ?line= is unambiguous within a step.
 *
 * A composition (Material UI has no log viewer): `List` as an `<ol>` with the HTML `start`
 * attribute doing the numbering the app used to do by hand, and one `ListItemButton` per
 * line so a line is selectable by click AND by keyboard — `ListItemButton` is focusable and
 * Enter-activated, which the old bare `<button>` grid also was, but now with the library's
 * `selected` state carrying the highlight instead of a data attribute.
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
  const selectedRef = useRef<HTMLLIElement | null>(null);
  useEffect(() => {
    selectedRef.current?.scrollIntoView({ block: "center" });
  }, [sel.selected]);
  if (text === "") return null;
  const lines = text.replace(/\n$/, "").split("\n");
  return (
    <List
      component="ol"
      dense
      disablePadding
      // The <ol>'s own `start` attribute does the numbering the app used to do by hand;
      // it is what makes the numbers line up with the step, not with the block.
      start={start}
      sx={{ ...MONO, overflowX: "auto" }}
    >
      {lines.map((line, i) => {
        const n = start + i;
        const isSel = sel.selected === n;
        const tone = classify?.(line);
        return (
          <ListItemButton
            key={n}
            component="li"
            ref={isSel ? selectedRef : undefined}
            selected={isSel}
            dense
            onClick={() => sel.onSelect(n)}
            sx={{
              ...MONO,
              py: 0,
              gap: 1,
              alignItems: "baseline",
              whiteSpace: "pre",
              color: tone !== undefined ? LINE_TONE[tone] : "text.primary",
            }}
          >
            <Box
              component="span"
              aria-hidden="true"
              sx={{ color: "text.disabled", minWidth: "3ch", textAlign: "right" }}
            >
              {n}
            </Box>
            {line === "" ? " " : line}
          </ListItemButton>
        );
      })}
    </List>
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
    <Paper component="article" variant="outlined" sx={{ p: 1, display: "grid", gap: 1 }}>
      <Box
        component="header"
        sx={{
          display: "flex",
          alignItems: "center",
          gap: 1,
          justifyContent: "space-between",
          flexWrap: "wrap",
        }}
      >
        <Typography component="div" variant="body1" sx={{ fontWeight: 600 }}>
          {title}
        </Typography>
        {meta !== undefined && (
          <Box sx={{ display: "flex", alignItems: "center", gap: 1, color: "text.secondary" }}>
            {meta}
          </Box>
        )}
      </Box>
      {children}
    </Paper>
  );
}

/** The step's own timing + cost, echoed in the centre pane header. */
function StepMeta({ a }: { a: RunActivity }) {
  const split = timingSplit(a);
  return (
    <>
      {split !== null && (
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
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
      {a.attempt > 1 && <Chip size="small" variant="outlined" label={`attempt ${a.attempt}`} />}
    </>
  );
}

/** A disclosure. MUI's Accordion, with the spec's own ▸/▾ glyph as the expand indicator. */
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
    <Accordion
      expanded={open}
      onChange={() => setOpen(!open)}
      disableGutters
      square
      elevation={0}
      sx={{ backgroundColor: "transparent", "&::before": { display: "none" } }}
    >
      <AccordionSummary
        expandIcon={
          <Box component="span" aria-hidden="true" sx={{ fontSize: "var(--fs-mono)" }}>
            ▸
          </Box>
        }
        sx={{ minHeight: 0, px: 0, "& .MuiAccordionSummary-content": { my: "4px" } }}
      >
        <Typography variant="body1" sx={{ color: "text.secondary" }}>
          {label}
        </Typography>
      </AccordionSummary>
      <AccordionDetails sx={{ p: 0 }}>{children}</AccordionDetails>
    </Accordion>
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
        <Box component="code" sx={MONO}>
          $ {cmd}
        </Box>
      }
      meta={<StepMeta a={a} />}
    >
      {done && (
        // §10: colour is never the only carrier — the exit chip leads with ✓ or ✕.
        <Chip
          size="small"
          variant="outlined"
          color={failed ? "error" : "success"}
          label={`${failed ? "✕" : "✓"} exit ${p.exit}${
            p.truncated === true ? " · output truncated" : ""
          }`}
          sx={{ ...MONO, alignSelf: "start" }}
        />
      )}
      {(stdout !== "" || stderr !== "") && (
        <Collapsible label="output" open={failed || sel.selected !== undefined}>
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
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
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
          <Box component="code" sx={MONO}>
            {p.path ?? ""}
          </Box>
        </>
      }
      meta={<StepMeta a={a} />}
    >
      {p.error !== undefined && <Alert severity="error">{p.error}</Alert>}
      {hunks.map((h, i) => {
        const start = offset + 1;
        offset += h.lines.length;
        return (
          <Paper key={i} variant="outlined" sx={{ overflow: "hidden" }}>
            <Typography
              variant="body2"
              sx={{
                ...MONO,
                px: 1,
                py: "2px",
                color: "text.secondary",
                backgroundColor: "lexicode.surface2",
                borderBottom: 1,
                borderColor: "divider",
              }}
            >
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
          </Paper>
        );
      })}
      {hunks.length === 0 && p.error === undefined && (
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
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
          <Box component="code" sx={MONO}>
            {p.path ?? ""}
          </Box>
          {p.lines !== undefined && (
            <Box component="span" sx={{ color: "text.secondary", fontWeight: 400 }}>
              {" "}
              · {p.lines} lines
            </Box>
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
          <Box component="code" sx={MONO}>
            {p.pattern ?? ""}
          </Box>
        </>
      }
      meta={<StepMeta a={a} />}
    >
      {p.matches !== undefined && (
        <Typography variant="body1">
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
          <ListItem key={i} data-status={it.status} disableGutters sx={{ gap: 1, py: 0 }}>
            <Box
              component="span"
              aria-hidden="true"
              sx={{
                color:
                  it.status === "completed"
                    ? "success.main"
                    : it.status === "in_progress"
                      ? "lexicode.running"
                      : "text.disabled",
              }}
            >
              {it.status === "completed" ? "✓" : it.status === "in_progress" ? "●" : "○"}
            </Box>
            <Typography variant="body1">{it.content ?? ""}</Typography>
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
            <RawBlock value={p.response} />
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

/** Raw JSON, always behind a disclosure and never a default rendering (rule 4). */
function RawBlock({ value }: { value: unknown }) {
  return (
    <Box
      component="pre"
      sx={{
        ...MONO,
        m: 0,
        p: 1,
        overflowX: "auto",
        backgroundColor: "lexicode.surface2",
        border: 1,
        borderColor: "divider",
        borderRadius: 1,
      }}
    >
      {typeof value === "string" ? value : JSON.stringify(value, null, 2)}
    </Box>
  );
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
          <Paper key={i} variant="outlined" sx={{ p: 1, display: "grid", gap: 1 }}>
            {q.header !== undefined && q.header !== "" && (
              <Chip size="small" variant="outlined" label={q.header} sx={{ justifySelf: "start" }} />
            )}
            <Typography variant="body1" sx={{ fontWeight: 600 }}>
              {q.question}
            </Typography>
            {/* ToggleButtonGroup is the library's own multi/single choice control, and it
                gives each option a real pressed state instead of a data attribute. */}
            <ToggleButtonGroup
              orientation="vertical"
              exclusive={!multi}
              value={multi ? c.labels : (c.labels[0] ?? null)}
              disabled={!pending}
              aria-label={q.question ?? `Question ${i + 1}`}
              onChange={(_e, next: string | string[] | null) => {
                const labels =
                  next === null ? [] : Array.isArray(next) ? next : [next];
                setChoice(i, { labels, other: c.other, useOther: false });
              }}
              sx={{ alignItems: "stretch" }}
            >
              {(q.options ?? []).map((o, j) => (
                <ToggleButton
                  key={j}
                  value={o.label ?? ""}
                  sx={{ display: "grid", gap: "2px", justifyItems: "start", textAlign: "left" }}
                >
                  <Typography variant="body1">{o.label ?? ""}</Typography>
                  {o.description !== undefined && o.description !== "" && (
                    <Typography variant="body2" sx={{ color: "text.secondary" }}>
                      {o.description}
                    </Typography>
                  )}
                </ToggleButton>
              ))}
            </ToggleButtonGroup>
            <TextField
              size="small"
              label="Other"
              placeholder="Type your own answer…"
              disabled={!pending}
              value={c.other}
              onFocus={() => setChoice(i, { ...c, useOther: true })}
              onChange={(e) => setChoice(i, { labels: [], other: e.target.value, useOther: true })}
            />
          </Paper>
        );
      })}
      {questions.length === 0 && <RawBlock value={a.payload} />}
      {pending && intervene !== undefined ? (
        <>
          <Button
            variant="contained"
            disabled={!complete || respond.isPending}
            onClick={submit}
            sx={{ justifySelf: "start" }}
          >
            Answer
          </Button>
          {respond.isError && <Alert severity="error">{respond.error.message}</Alert>}
        </>
      ) : (
        <Chip
          size="small"
          variant="outlined"
          data-state={el?.state ?? "pending"}
          label={resolvedLabel(el)}
          sx={{ justifySelf: "start" }}
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
        <Box component="span" sx={{ color: "lexicode.needsYou" }}>
          ▲ {req.action ?? a.title}
        </Box>
      }
      meta={<StepMeta a={a} />}
    >
      <Box component="dl" sx={{ display: "grid", gap: "2px", m: 0 }}>
        {fields.map(([label, value]) =>
          value !== undefined && value !== "" ? (
            <Box key={label} sx={{ display: "flex", gap: 1 }}>
              <Typography component="dt" variant="body1" sx={{ color: "text.secondary", minWidth: 96 }}>
                {label}
              </Typography>
              <Typography component="dd" variant="body1" sx={{ m: 0 }}>
                {value}
              </Typography>
            </Box>
          ) : null,
        )}
      </Box>
      {req.input !== undefined && (
        <Collapsible label="tool input">
          <RawBlock value={req.input} />
        </Collapsible>
      )}

      {pending ? (
        <Stack spacing={1}>
          {mode === "edits" && (
            <>
              <TextField
                multiline
                minRows={8}
                fullWidth
                value={edited}
                slotProps={{ htmlInput: { style: { fontFamily: "var(--font-mono)" } } }}
                onChange={(e) => {
                  setEdited(e.target.value);
                  setEditError(null);
                }}
                label="Edited tool input (JSON)"
              />
              {editError !== null && <Alert severity="error">{editError}</Alert>}
            </>
          )}
          {mode === "respond" && (
            <TextField
              multiline
              minRows={3}
              fullWidth
              value={message}
              placeholder="Tell the agent what to do instead…"
              onChange={(e) => setMessage(e.target.value)}
              label="Response to the agent"
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
                ✓ Approve with these edits
              </Button>
            ) : mode === "respond" ? (
              <Button
                variant="contained"
                disabled={respond.isPending || message.trim() === ""}
                onClick={() => act({ action: "deny", message: message.trim() })}
              >
                ↩ Send response
              </Button>
            ) : (
              <Button
                variant="contained"
                disabled={respond.isPending}
                onClick={() => act(alwaysAllow ? { action: "remember" } : { action: "approve" })}
              >
                ✓ Approve
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
              ✕ Deny
            </Button>
          </Stack>
          <FormControlLabel
            control={
              <Checkbox
                checked={alwaysAllow}
                onChange={(e) => setAlwaysAllow(e.target.checked)}
              />
            }
            label={
              <Typography variant="body1">
                Always allow {req.tool_name ?? "this tool"} like this — writes one scoped rule
                in agent settings
              </Typography>
            }
          />
          {respond.isError && <Alert severity="error">{respond.error.message}</Alert>}
        </Stack>
      ) : (
        <Chip
          size="small"
          variant="outlined"
          data-state={el.state}
          label={resolvedLabel(el)}
          sx={{ justifySelf: "start" }}
        />
      )}
      {ruleID !== null && (
        <Alert severity="success">
          Rule added —{" "}
          <AppLink
            to="/p/$key/agents/$id"
            params={{ key: intervene.projectKey, id: intervene.agentID }}
          >
            view in agent settings
          </AppLink>
        </Alert>
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
      body = <Typography variant="body1">current step → “{p.step ?? ""}”</Typography>;
      break;
    case "mcp__lexicode__check_criterion":
      body = (
        <Typography variant="body1">
          <Box
            component="span"
            aria-hidden="true"
            sx={{ color: p.met === true ? "success.main" : "text.disabled" }}
          >
            {p.met === true ? "✓" : "○"}
          </Box>{" "}
          {p.met === true ? "met" : "unmet"}
          {p.note !== undefined && p.note !== "" && <> — {p.note}</>}
        </Typography>
      );
      break;
    case "mcp__lexicode__propose_wiki_page":
      body = (
        <Typography variant="body1">
          proposed{" "}
          <Box component="code" sx={MONO}>
            {p.slug ?? ""}
          </Box>
          {p.reason !== undefined && p.reason !== "" && <> — {p.reason}</>}
        </Typography>
      );
      break;
    default:
      body = (
        <Collapsible label="parameters">
          <RawBlock value={p.raw ?? a.payload} />
        </Collapsible>
      );
  }
  return (
    <DetailCard
      title={
        <Box component="span" sx={MONO}>
          {label}
        </Box>
      }
      meta={<StepMeta a={a} />}
    >
      {body}
      {typeof p.result === "string" && p.result !== "" && (
        <Collapsible label="result">
          <RawBlock value={p.result} />
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
        <Typography variant="body2" sx={{ color: "text.secondary" }}>
          output truncated
        </Typography>
      )}
      {p.raw !== undefined && (
        <Collapsible label="raw input">
          <RawBlock value={p.raw} />
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
          <Box component="code" sx={MONO}>
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
          <Box
            component="span"
            aria-hidden="true"
            sx={{
              color:
                p.state === "ok"
                  ? "success.main"
                  : p.state === "failed"
                    ? "error.main"
                    : p.state === "running"
                      ? "lexicode.running"
                      : "text.disabled",
            }}
          >
            {p.state === "ok" ? "✓" : p.state === "failed" ? "✕" : p.state === "running" ? "●" : "○"}
          </Box>{" "}
          {p.step ?? a.title}
        </>
      }
    >
      {p.detail !== undefined && p.detail !== "" && (
        <Typography variant="body1">{p.detail}</Typography>
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
          <Typography variant="body1" sx={{ whiteSpace: "pre-wrap" }}>
            {payloadOf(a).text ?? a.title}
          </Typography>
        </DetailCard>
      );
    case "response":
      return (
        <Alert
          severity="success"
          icon={
            <Box component="span" aria-hidden="true">
              ✓
            </Box>
          }
          action={<StepMeta a={a} />}
        >
          <Typography variant="body1" sx={{ whiteSpace: "pre-wrap" }}>
            {payloadOf(a).text ?? a.title}
          </Typography>
        </Alert>
      );
    case "error": {
      const p = payloadOf(a);
      return (
        <Alert
          severity="error"
          icon={
            <Box component="span" aria-hidden="true">
              ✕
            </Box>
          }
        >
          <Typography variant="body1" sx={{ fontWeight: 600 }}>
            {p.subtype !== undefined && p.subtype !== "" ? p.subtype : "Error"}
          </Typography>
          <Typography variant="body1" sx={{ whiteSpace: "pre-wrap" }}>
            {p.result !== undefined && p.result !== "" ? p.result : a.title}
          </Typography>
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
