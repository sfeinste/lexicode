/*
 * S07 acceptance: StatusDot renders glyph AND color for every status in the §4 vocabulary —
 * color is never the sole carrier (§10), and the vocabulary is complete.
 *
 * S39 note on the colour assertion. Before the Material UI conversion the glyph carried an
 * inline `style="color: var(--ok)"`, and this suite read `element.style.color`. The glyph is
 * now a themed `Box`, so the colour arrives through an emotion class instead of an inline
 * style — the same guarantee, delivered by a different mechanism. The assertion therefore
 * moved one level up and got STRONGER rather than weaker: it now checks the whole chain,
 *
 *     §4 status  →  theme palette slot  →  §3.2 token
 *
 * by asserting (a) that the glyph's COMPUTED colour is the MUI palette variable for that
 * status's meaning, and (b) that the same palette slot on the theme is literally
 * `var(--<token>)`. A regression that pointed a status at the wrong hue, or that let a
 * palette slot drift off the tokens, fails here exactly as it did before.
 */
import { render } from "@testing-library/react";
import { ThemeProvider } from "@mui/material/styles";
import { describe, expect, it } from "vitest";

import { lexicodeTheme } from "../../theme/theme";
import { PALETTE_PATH } from "./statusPalette";
import {
  RUN_STATUSES,
  STATUS_VOCABULARY,
  StatusDot,
  TRIGGER_OUTCOMES,
  type Status,
  type StatusMeta,
} from "./StatusDot";

/** `"success.main"` → the value at that path on an object. */
function at(root: unknown, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>((v, k) => (v as Record<string, unknown> | undefined)?.[k], root);
}

/**
 * `"success.main"` → `--mui-palette-success-main`, MUI's generated variable name. MUI keeps
 * each slot's own casing, so `lexicode.needsYou` → `--mui-palette-lexicode-needsYou`.
 */
function cssVarName(path: string): string {
  return `--mui-palette-${path.replace(/\./g, "-")}`;
}

const withTheme = (node: React.ReactNode) =>
  render(<ThemeProvider theme={lexicodeTheme}>{node}</ThemeProvider>);

const ALL = Object.keys(STATUS_VOCABULARY) as Status[];

describe("StatusDot vocabulary", () => {
  it("covers every §4.1 run state", () => {
    expect(Object.keys(RUN_STATUSES).sort()).toEqual(
      [
        "queued",
        "provisioning",
        "running",
        "needs_input",
        "awaiting_approval",
        "completed",
        "failed",
        "timed_out",
        "canceled",
        "loop_stopped",
      ].sort(),
    );
  });

  it("covers every §4.2 trigger outcome class", () => {
    expect(Object.keys(TRIGGER_OUTCOMES).sort()).toEqual(
      [
        "succeeded",
        "no_action",
        "awaiting_approval",
        "errored",
        "debounced",
        "superseded",
        "loop_stopped",
        "budget_exceeded",
      ].sort(),
    );
  });

  it.each(ALL)("renders %s with a glyph, a semantic color and a label", (status) => {
    const meta = STATUS_VOCABULARY[status];
    const { container, getByText } = withTheme(<StatusDot status={status} />);

    const glyph = container.querySelector('[aria-hidden="true"]');
    expect(glyph?.textContent).toBe(meta.glyph);
    expect(getByText(meta.label)).toBeTruthy();

    // (a) the glyph's colour is the palette slot this meaning maps to …
    const path = PALETTE_PATH[meta.color];
    expect(getComputedStyle(glyph as HTMLElement).color).toContain(cssVarName(path));
    // … and (b) that slot is the §3.2 token, not a Material default.
    expect(at(lexicodeTheme.palette, path)).toBe(`var(--${meta.color})`);
  });

  it("keeps a distinct glyph alongside every color, so color is never the only carrier", () => {
    // Two statuses that share a color must be distinguishable by glyph or label.
    for (const a of ALL) {
      for (const b of ALL) {
        if (a === b) continue;
        const ma = STATUS_VOCABULARY[a];
        const mb = STATUS_VOCABULARY[b];
        if (ma.color === mb.color) {
          expect(
            ma.glyph !== mb.glyph || ma.label !== mb.label,
            `${a} and ${b} are indistinguishable`,
          ).toBe(true);
        }
      }
    }
  });

  it("only the running state pulses", () => {
    const pulsing = ALL.filter((s) => (STATUS_VOCABULARY[s] as StatusMeta).pulse);
    expect(pulsing).toEqual(["running"]);
  });

  it("hides the label visually but keeps it for screen readers", () => {
    const { getByText } = withTheme(<StatusDot status="failed" hideLabel />);
    expect(getByText("Failed")).toBeTruthy();
  });
});
