/*
 * S29: the prose card composition — a trigger fixture renders as the §5.9 card lines, and
 * the conditions tree round-trips through the editor's row model.
 */
import { describe, expect, it } from "vitest";

import { parseConditions, serializeConditions } from "./conditions";
import {
  composeBreakdown,
  composeIf,
  composeThen,
  composeWhen,
  firedCount,
  suppressionLine,
} from "./prose";
import { GITHUB_CATALOG, makeTrigger } from "./testFixtures";

describe("prose card composition", () => {
  const trigger = makeTrigger();

  it("composes the WHEN line from catalog labels and filters", () => {
    expect(composeWhen(trigger, GITHUB_CATALOG)).toBe(
      "pull request opened, marked ready for review · branch main",
    );
  });

  it("composes the IF line in words with number symbols", () => {
    expect(composeIf(trigger.conditions, GITHUB_CATALOG)).toBe(
      "author is an agent · files changed < 400",
    );
  });

  it("renders OR groups parenthesized and joined with or", () => {
    const conditions = {
      any: [
        { all: [{ field: "pr.title", op: "text.contains", value: "WIP" }] },
        { all: [{ op: "actor.is_human" }] },
      ],
    };
    expect(composeIf(conditions, GITHUB_CATALOG)).toBe(
      "(title contains WIP) or (author is a human)",
    );
  });

  it("uses the server Describe sentences for THEN", () => {
    expect(composeThen(trigger)).toBe("run agent Reviewer");
    expect(
      composeThen(makeTrigger({ action_summaries: ["run agent Reviewer", "notify the human"] })),
    ).toBe("run agent Reviewer, then notify the human");
  });

  it("renders the breakdown in §4.2 words, never collapsed to success/failure", () => {
    expect(
      composeBreakdown({ succeeded: 14, no_action: 3, loop_stopped: 1 }),
    ).toBe("14 ok · 3 no action · 1 loop");
    expect(firedCount({ succeeded: 14, no_action: 3, loop_stopped: 1 })).toBe(18);
  });

  it("renders the actor-suppression line with the acting agents' names", () => {
    expect(suppressionLine(trigger, ["reviewer-bot"])).toBe(
      "Ignores events caused by @reviewer-bot",
    );
    const off = makeTrigger({
      loop_config: { ...trigger.loop_config, actor_suppression: false },
    });
    expect(suppressionLine(off, ["reviewer-bot"])).toMatch(/actor suppression is off/);
  });
});

describe("conditions row model", () => {
  it("round-trips a single AND group", () => {
    const tree = {
      all: [
        { op: "actor.is_agent" },
        { field: "pr.files_changed", op: "number.lt", value: 400 },
      ],
    };
    const groups = parseConditions(tree);
    expect(groups).toEqual([
      [
        { field: "", op: "actor.is_agent", value: undefined },
        { field: "pr.files_changed", op: "number.lt", value: 400 },
      ],
    ]);
    expect(serializeConditions(groups as NonNullable<typeof groups>)).toEqual(tree);
  });

  it("round-trips OR groups", () => {
    const tree = {
      any: [
        { all: [{ field: "pr.title", op: "text.contains", value: "WIP" }] },
        { all: [{ op: "actor.is_human" }] },
      ],
    };
    const groups = parseConditions(tree);
    expect(groups).toHaveLength(2);
    expect(serializeConditions(groups as NonNullable<typeof groups>)).toEqual(tree);
  });

  it("parses the empty rule and refuses trees deeper than rows", () => {
    expect(parseConditions({ all: [] })).toEqual([[]]);
    expect(parseConditions(undefined)).toEqual([[]]);
    const deep = { all: [{ any: [{ all: [{ op: "actor.is_agent" }] }] }] };
    expect(parseConditions(deep)).toBeNull();
  });
});
