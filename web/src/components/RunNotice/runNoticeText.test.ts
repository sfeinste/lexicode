/*
 * The notice vocabulary. The rule under test: a queued run with a hold reason is NORMAL —
 * it must read as queued plus the reason in words, never as a failure — and a run that did
 * fail must say so with the reason the scheduler recorded.
 */
import { describe, expect, it } from "vitest";

import type { Run } from "../../lib/api/client";
import { runNoticeText } from "./runNoticeText";

function run(over: Partial<Run>): Run {
  return {
    id: "r1",
    seq: 4,
    project_id: "p1",
    agent_id: "a1",
    ticket_id: "t1",
    trigger_id: null,
    state: "queued",
    state_reason: "delegate button",
    hold_reason: "",
    autonomy: "supervised",
    directive_version_id: null,
    model: "claude-sonnet-5",
    effort: "medium",
    branch: null,
    subject_key: "ticket:TEST-1",
    current_step: "",
    cost_cents: 0,
    tokens_in: 0,
    tokens_out: 0,
    tokens_cache_read: 0,
    tokens_cache_write: 0,
    step_count: 0,
    error_message: "",
    takeover_note: "",
    queued_at: "2026-01-01T00:00:00Z",
    started_at: null,
    ended_at: null,
    acknowledged_at: null,
    ...over,
  } as Run;
}

describe("runNoticeText", () => {
  it("says queued before the run row has been read back", () => {
    expect(runNoticeText(undefined, "dev")).toBe("Queued a run for dev");
  });

  it("reads a held run as queued plus the limit, in words", () => {
    expect(
      runNoticeText(run({ hold_reason: "waiting: dev is at its 1-run limit" }), "dev"),
    ).toBe("Run #4 · dev · Queued — waiting: dev is at its 1-run limit");
    // Not a failure: nothing in the sentence claims one.
    expect(runNoticeText(run({ hold_reason: "waiting: Running is at 4/4" }), "dev")).not.toMatch(
      /fail/i,
    );
  });

  it("says only queued when nothing is holding it", () => {
    expect(runNoticeText(run({}), "dev")).toBe("Run #4 · dev · Queued");
  });

  it("reports a failure with the reason the scheduler recorded", () => {
    expect(
      runNoticeText(
        run({
          state: "failed",
          state_reason: "budget exceeded",
          error_message: "Budget exceeded: Test project has spent $5.00 of its $5.00 daily budget.",
        }),
        "dev",
      ),
    ).toBe(
      "Run #4 · dev · Failed — Budget exceeded: Test project has spent $5.00 of its $5.00 daily budget.",
    );
  });
});
