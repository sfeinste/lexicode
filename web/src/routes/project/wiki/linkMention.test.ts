import { describe, expect, it } from "vitest";

import { linkFirstPlainOccurrence } from "./linkMention";

describe("linkFirstPlainOccurrence", () => {
  it("replaces the first plain occurrence with the token", () => {
    expect(
      linkFirstPlainOccurrence("Check the API runbook before paging.", "API runbook", "p1"),
    ).toBe("Check the @[API runbook](wiki:p1) before paging.");
  });

  it("is case-insensitive but keeps the title's canonical casing in the token", () => {
    expect(linkFirstPlainOccurrence("see api runbook.", "API runbook", "p1")).toBe(
      "see @[API runbook](wiki:p1).",
    );
  });

  it("never matches inside an existing mention token's label", () => {
    const body = "See @[API runbook](wiki:p1) here.";
    expect(linkFirstPlainOccurrence(body, "API runbook", "p1")).toBeNull();
  });

  it("skips a token label but links a later plain occurrence", () => {
    const body = "See @[API runbook](wiki:p1) and also the API runbook appendix.";
    expect(linkFirstPlainOccurrence(body, "API runbook", "p1")).toBe(
      "See @[API runbook](wiki:p1) and also the @[API runbook](wiki:p1) appendix.",
    );
  });
});
