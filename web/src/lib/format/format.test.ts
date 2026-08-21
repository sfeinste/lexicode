import { describe, expect, it } from "vitest";

import { formatDuration, formatRelativeTime, formatTokenCount, formatUSD } from "./format";

describe("formatRelativeTime", () => {
  const now = new Date("2026-08-21T12:00:00Z");
  it.each([
    ["2026-08-21T11:59:55Z", "just now"],
    ["2026-08-21T11:59:15Z", "45s ago"],
    ["2026-08-21T11:58:00Z", "2m ago"],
    ["2026-08-21T09:00:00Z", "3h ago"],
    ["2026-08-16T12:00:00Z", "5d ago"],
    ["2026-06-01T12:00:00Z", "2026-06-01"],
  ])("%s → %s", (iso, want) => {
    expect(formatRelativeTime(iso, now)).toBe(want);
  });

  it("passes garbage through rather than rendering NaN", () => {
    expect(formatRelativeTime("not-a-date", now)).toBe("not-a-date");
  });
});

describe("formatDuration", () => {
  it.each([
    [400, "0.4s"],
    [4_000, "4s"],
    [31_000, "31s"],
    [72_000, "1m 12s"],
    [120_000, "2m"],
    [7_380_000, "2h 3m"],
    [7_200_000, "2h"],
  ])("%d ms → %s", (ms, want) => {
    expect(formatDuration(ms)).toBe(want);
  });

  it("renders a dash for negative input", () => {
    expect(formatDuration(-1)).toBe("—");
  });
});

describe("formatTokenCount", () => {
  it.each([
    [0, "0"],
    [999, "999"],
    [13_000, "13k"],
    [84_200, "84.2k"],
    [71_000, "71k"],
    [1_200_000, "1.2M"],
  ])("%d → %s", (n, want) => {
    expect(formatTokenCount(n)).toBe(want);
  });
});

describe("formatUSD", () => {
  it.each([
    [1.42, "$1.42"],
    [0.034, "$0.03"],
    [12.5, "$12.50"],
  ])("%d → %s", (n, want) => {
    expect(formatUSD(n)).toBe(want);
  });
});
