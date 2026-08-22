import { describe, expect, it } from "vitest";

import { formatDiffStat, isLargeDiff } from "./prSize";

describe("isLargeDiff", () => {
  it("warns strictly above the threshold", () => {
    expect(isLargeDiff(500, 300, 800)).toBe(false); // exactly at: no warning
    expect(isLargeDiff(501, 300, 800)).toBe(true);
    expect(isLargeDiff(1240, 310, 800)).toBe(true);
  });

  it("sums additions and deletions", () => {
    expect(isLargeDiff(799, 0, 800)).toBe(false);
    expect(isLargeDiff(0, 801, 800)).toBe(true);
  });

  it("is disabled at threshold 0 or below", () => {
    expect(isLargeDiff(1_000_000, 1_000_000, 0)).toBe(false);
    expect(isLargeDiff(10, 10, -1)).toBe(false);
  });

  it("never warns while sizes are unknown", () => {
    expect(isLargeDiff(null, null, 800)).toBe(false);
    expect(isLargeDiff(undefined, 5, 800)).toBe(false);
    expect(isLargeDiff(5, undefined, 800)).toBe(false);
  });
});

describe("formatDiffStat", () => {
  it("renders the +adds −dels stat with thousands separators", () => {
    expect(formatDiffStat(1240, 310)).toBe("+1,240 −310");
    expect(formatDiffStat(0, 0)).toBe("+0 −0");
  });
});
