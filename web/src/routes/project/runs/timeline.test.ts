/*
 * The §5.7 timeline logic: ToolCallRow grouping over consecutive same-group_key actions,
 * the client-side verbosity filter, the timing-gutter split, failed-step auto-expansion,
 * and the `f` next-failure jump.
 */
import { describe, expect, it } from "vitest";

import type { RunActivity } from "../../../lib/api/client";
import {
  buildTimeline,
  defaultSelection,
  filterByVerbosity,
  groupLabel,
  nextFailure,
  timingSplit,
  toolDisplayName,
  type GroupRow,
} from "./timeline";

let seqCounter = 0;

function act(over: Partial<RunActivity>): RunActivity {
  seqCounter++;
  return {
    seq: over.seq ?? seqCounter,
    type: "action",
    level: 1,
    tool_name: "Read",
    group_key: "Read",
    title: "Read a file",
    payload: {},
    ok: true,
    attempt: 1,
    duration_ms: 100,
    queued_ms: null,
    model_ms: null,
    tool_ms: null,
    cost_cents: 0,
    tokens_in: 0,
    tokens_out: 0,
    created_at: "2026-08-22T10:00:00Z",
    ...over,
  };
}

const none = new Set<number>();

describe("ToolCallRow grouping", () => {
  it("collapses 5 consecutive Reads into one group row labelled 'Read 5 files'", () => {
    const activities = [1, 2, 3, 4, 5].map(() => act({ tool_name: "Read", group_key: "Read" }));
    const rows = buildTimeline(activities, { verbosity: "normal", expanded: none });
    expect(rows).toHaveLength(1);
    const g = rows[0] as GroupRow;
    expect(g.kind).toBe("group");
    expect(g.label).toBe("Read 5 files");
    expect(g.count).toBe(5);
    expect(g.expanded).toBe(false);
  });

  it("a thought between reads splits the group", () => {
    const activities = [
      act({ group_key: "Read" }),
      act({ group_key: "Read" }),
      act({ type: "thought", tool_name: "", group_key: "", title: "hmm" }),
      act({ group_key: "Read" }),
      act({ group_key: "Read" }),
    ];
    const rows = buildTimeline(activities, { verbosity: "normal", expanded: none });
    expect(rows.map((r) => r.kind)).toEqual(["group", "step", "group"]);
  });

  it("grouping runs AFTER the verbosity filter, so it survives filtering (§13)", () => {
    // Summary hides the level-1 thought sitting between two runs of reads → one group of 4.
    const activities = [
      act({ group_key: "Read", level: 0 }),
      act({ group_key: "Read", level: 0 }),
      act({ type: "thought", tool_name: "", group_key: "", level: 1 }),
      act({ group_key: "Read", level: 0 }),
      act({ group_key: "Read", level: 0 }),
    ];
    const rows = buildTimeline(activities, { verbosity: "summary", expanded: none });
    expect(rows).toHaveLength(1);
    expect((rows[0] as GroupRow).count).toBe(4);
  });

  it("a single call never becomes a group", () => {
    const rows = buildTimeline([act({})], { verbosity: "normal", expanded: none });
    expect(rows[0].kind).toBe("step");
  });

  it("expanding a group renders its members as child rows", () => {
    const activities = [act({ seq: 10 }), act({ seq: 11 }), act({ seq: 12 })];
    const rows = buildTimeline(activities, { verbosity: "normal", expanded: new Set([10]) });
    expect(rows).toHaveLength(4);
    expect(rows[0].kind).toBe("group");
    expect(rows.slice(1).every((r) => r.kind === "step" && r.child)).toBe(true);
  });

  it("rolls cost and duration up to the group row (§5.7 'rolled up to parents')", () => {
    const activities = [
      act({ cost_cents: 3, duration_ms: 100 }),
      act({ cost_cents: 4, duration_ms: 250 }),
    ];
    const g = buildTimeline(activities, { verbosity: "normal", expanded: none })[0] as GroupRow;
    expect(g.costCents).toBe(7);
    expect(g.durationMs).toBe(350);
  });

  it("labels non-Read groups with the ×N form, MCP tools by server: tool", () => {
    expect(groupLabel("Edit", 6)).toBe("Edit ×6");
    expect(groupLabel("Bash", 3)).toBe("Ran 3 commands");
    expect(groupLabel("mcp__lexicode__set_step", 2)).toBe("lexicode: set_step ×2");
    expect(toolDisplayName("mcp__lexicode__ask_human")).toBe("lexicode: ask_human");
  });
});

describe("verbosity filter", () => {
  const activities = [
    act({ level: 0, title: "summary row" }),
    act({ level: 1, title: "normal row" }),
    act({ level: 2, title: "verbose row" }),
  ];

  it("summary shows only level 0", () => {
    expect(filterByVerbosity(activities, "summary").map((a) => a.level)).toEqual([0]);
  });

  it("normal shows levels 0-1", () => {
    expect(filterByVerbosity(activities, "normal").map((a) => a.level)).toEqual([0, 1]);
  });

  it("verbose shows everything", () => {
    expect(filterByVerbosity(activities, "verbose")).toHaveLength(3);
  });
});

describe("timing gutter split", () => {
  it("splits queued / model / tool with duration_ms as the total", () => {
    const split = timingSplit(
      act({ queued_ms: 50, model_ms: 200, tool_ms: 750, duration_ms: 1000 }),
    );
    expect(split).toEqual({ queuedMs: 50, modelMs: 200, toolMs: 750, totalMs: 1000 });
  });

  it("falls back to the segment sum when duration_ms is absent", () => {
    const split = timingSplit(act({ queued_ms: null, model_ms: 300, tool_ms: 200, duration_ms: null }));
    expect(split).toEqual({ queuedMs: 0, modelMs: 300, toolMs: 200, totalMs: 500 });
  });

  it("is null when the step carries no timing at all", () => {
    expect(
      timingSplit(act({ queued_ms: null, model_ms: null, tool_ms: null, duration_ms: null })),
    ).toBeNull();
  });
});

describe("failed steps", () => {
  it("a group containing a failure auto-expands without a click, and wears the failure", () => {
    const activities = [act({ seq: 1 }), act({ seq: 2, ok: false }), act({ seq: 3 })];
    const rows = buildTimeline(activities, { verbosity: "normal", expanded: none });
    const g = rows[0] as GroupRow;
    expect(g.expanded).toBe(true);
    expect(g.ok).toBe(false);
    expect(rows).toHaveLength(4); // group + 3 children visible on load
  });

  it("the default selection is the first failed step (auto-expanded on load)", () => {
    const activities = [
      act({ seq: 1 }),
      act({ seq: 2, ok: false, type: "error", tool_name: "", group_key: "" }),
      act({ seq: 3 }),
    ];
    expect(defaultSelection(activities)).toBe(2);
  });

  it("without failures the default selection is the stream tail", () => {
    expect(defaultSelection([act({ seq: 1 }), act({ seq: 2 })])).toBe(2);
  });

  it("`f` finds the next failure and wraps", () => {
    const activities = [
      act({ seq: 1, ok: false }),
      act({ seq: 2 }),
      act({ seq: 3, ok: false }),
    ];
    expect(nextFailure(activities, undefined)).toBe(1);
    expect(nextFailure(activities, 1)).toBe(3);
    expect(nextFailure(activities, 3)).toBe(1); // wraps
    expect(nextFailure([act({ seq: 1 })], 1)).toBeNull();
  });

  it("the selected step's group expands so the selection is visible", () => {
    const activities = [act({ seq: 1 }), act({ seq: 2 }), act({ seq: 3 })];
    const rows = buildTimeline(activities, {
      verbosity: "normal",
      expanded: none,
      selectedSeq: 2,
    });
    expect(rows).toHaveLength(4);
    expect((rows[0] as GroupRow).expanded).toBe(true);
  });
});
