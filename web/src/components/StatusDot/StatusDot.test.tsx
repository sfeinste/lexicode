/*
 * S07 acceptance: StatusDot renders glyph AND color for every status in the §4 vocabulary —
 * color is never the sole carrier (§10), and the vocabulary is complete.
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import {
  RUN_STATUSES,
  STATUS_VOCABULARY,
  StatusDot,
  TRIGGER_OUTCOMES,
  type Status,
  type StatusMeta,
} from "./StatusDot";

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
    const { container, getByText } = render(<StatusDot status={status} />);

    const glyph = container.querySelector('[aria-hidden="true"]');
    expect(glyph?.textContent).toBe(meta.glyph);
    expect((glyph as HTMLElement).style.color).toBe(`var(--${meta.color})`);
    expect(getByText(meta.label)).toBeTruthy();
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
    const { getByText } = render(<StatusDot status="failed" hideLabel />);
    expect(getByText("Failed")).toBeTruthy();
  });
});
