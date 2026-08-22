/*
 * Same-Seq merge semantics (contracts §2.4, S23): a streamed activity re-emitted under an
 * existing seq REPLACES its row — that is how a tool_result completes its originating
 * tool_use — and a new seq appends in order even when frames arrive out of order.
 */
import { describe, expect, it } from "vitest";

import type { components } from "../api/types.gen";
import { mergeActivity } from "./applyEvent";

type RunActivity = components["schemas"]["RunActivity"];

function act(seq: number, over: Partial<RunActivity> = {}): RunActivity {
  return {
    seq,
    type: "action",
    level: 1,
    tool_name: "Bash",
    group_key: "Bash",
    title: "$ make check",
    payload: {},
    ok: null,
    attempt: 1,
    duration_ms: null,
    queued_ms: null,
    model_ms: null,
    tool_ms: null,
    cost_cents: 0,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    created_at: "2026-08-22T10:00:00Z",
    ...over,
  };
}

describe("mergeActivity", () => {
  it("appends a new seq at the tail", () => {
    const merged = mergeActivity({ activities: [act(1), act(2)] }, act(3));
    expect(merged.activities.map((a) => a.seq)).toEqual([1, 2, 3]);
  });

  it("a re-emitted seq replaces its row (tool_result completes the tool_use)", () => {
    const merged = mergeActivity(
      { activities: [act(1), act(2)] },
      act(2, { ok: true, duration_ms: 420, tool_ms: 420 }),
    );
    expect(merged.activities).toHaveLength(2);
    expect(merged.activities[1].ok).toBe(true);
    expect(merged.activities[1].duration_ms).toBe(420);
  });

  it("an out-of-order seq inserts in seq order", () => {
    const merged = mergeActivity({ activities: [act(1), act(4)] }, act(3));
    expect(merged.activities.map((a) => a.seq)).toEqual([1, 3, 4]);
  });
});
