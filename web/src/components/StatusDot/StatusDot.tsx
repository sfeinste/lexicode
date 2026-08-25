/*
 * StatusDot — THE status renderer (architecture §13): the only place in the app where a
 * status becomes a color and a glyph. Every status anywhere renders through it, which is
 * what guarantees the §10 rule that color is never the sole carrier — each status has a
 * distinct glyph alongside its hue.
 *
 * The vocabulary is UI spec §4 in full: the run states (§4.1) and the trigger outcome
 * classes (§4.2). §4.3's "flavors of needs-you" are copy on NeedsYouCard, not statuses.
 *
 * D-1 (amended) — composition, not invention: this is `Box` + `Typography` from Material
 * UI, and the seven §3.2 hues are palette entries on the theme (`success`, `error`,
 * `warning`, `primary` and the three `lexicode.*` slots Material has no name for). The
 * vocabulary table below is unchanged from the CSS-module version; only the rendering moved
 * onto library primitives.
 */
import Box from "@mui/material/Box";
import Typography from "@mui/material/Typography";
import { visuallyHidden } from "@mui/utils";

// The §3.2 meaning → theme palette path map. Its own module so this stays a component file.
import { PALETTE_PATH } from "./statusPalette";

export interface StatusMeta {
  /** The semantic color token, without the leading `--`. One hue per meaning (§3.2). */
  color: "accent" | "running" | "needs-you" | "ok" | "fail" | "halt" | "muted";
  glyph: "●" | "▲" | "✓" | "✕" | "⊘" | "⊗" | "○" | "◐";
  label: string;
  /** The 2s pulse — only the in-flight run dot animates (§3.4). */
  pulse?: boolean;
}

/** §4.1 run states. */
export const RUN_STATUSES = {
  queued: { color: "muted", glyph: "○", label: "Queued" },
  provisioning: { color: "running", glyph: "◐", label: "Provisioning" },
  running: { color: "running", glyph: "●", label: "Running", pulse: true },
  needs_input: { color: "needs-you", glyph: "▲", label: "Needs input" },
  awaiting_approval: { color: "needs-you", glyph: "▲", label: "Awaiting approval" },
  completed: { color: "ok", glyph: "✓", label: "Completed" },
  failed: { color: "fail", glyph: "✕", label: "Failed" },
  timed_out: { color: "fail", glyph: "✕", label: "Timed out" },
  canceled: { color: "muted", glyph: "⊘", label: "Canceled" },
  loop_stopped: { color: "halt", glyph: "⊗", label: "Loop stopped" },
} as const satisfies Record<string, StatusMeta>;

/**
 * §4.2 trigger outcome classes ("no action" deliberately its own class).
 * `awaiting_approval` and `loop_stopped` are shared with the run vocabulary — one meaning,
 * one rendering, exactly as §4 demands.
 */
export const TRIGGER_OUTCOMES = {
  succeeded: { color: "ok", glyph: "✓", label: "Succeeded" },
  no_action: { color: "muted", glyph: "○", label: "No action" },
  awaiting_approval: RUN_STATUSES.awaiting_approval,
  errored: { color: "fail", glyph: "✕", label: "Errored" },
  debounced: { color: "halt", glyph: "⊘", label: "Debounced" },
  superseded: { color: "halt", glyph: "⊘", label: "Superseded" },
  loop_stopped: RUN_STATUSES.loop_stopped,
  budget_exceeded: { color: "halt", glyph: "⊗", label: "Budget exceeded" },
} as const satisfies Record<string, StatusMeta>;

export const STATUS_VOCABULARY = { ...RUN_STATUSES, ...TRIGGER_OUTCOMES } as const;

export type Status = keyof typeof STATUS_VOCABULARY;

export interface StatusDotProps {
  status: Status;
  /** Override the default label copy (e.g. "4/4 · queued: 2"). */
  label?: string;
  /** Glyph only; the label stays for screen readers. */
  hideLabel?: boolean;
}

export function StatusDot({ status, label, hideLabel }: StatusDotProps) {
  const meta: StatusMeta = STATUS_VOCABULARY[status];
  const text = label ?? meta.label;
  return (
    <Box
      component="span"
      data-status={status}
      sx={{
        display: "inline-flex",
        alignItems: "center",
        gap: "6px",
        whiteSpace: "nowrap",
      }}
    >
      <Box
        component="span"
        aria-hidden="true"
        sx={{
          color: PALETTE_PATH[meta.color],
          fontSize: "var(--fs-mono)",
          lineHeight: 1,
          // §3.4: the 2s pulse on the running dot — one of the only three animations in
          // the app. The keyframes are declared once, in tokens.css.
          ...(meta.pulse === true
            ? { animation: "lx-pulse 2s ease-in-out infinite" }
            : null),
        }}
      >
        {meta.glyph}
      </Box>
      <Typography
        component="span"
        variant="inherit"
        sx={hideLabel === true ? visuallyHidden : { color: "inherit" }}
      >
        {text}
      </Typography>
    </Box>
  );
}
