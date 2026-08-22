/*
 * S33 acceptance: a page past `verified_until` renders red before any demotion job runs —
 * the chip is a pure client-side date check (the S34 job does not exist yet, and must not
 * need to for the UI to warn).
 */
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { VerifiedChip } from "./VerifiedChip";
import { isPastDue, verifiedLabel } from "./verified";

const today = new Date(2026, 7, 21); // 2026-08-21, local time

describe("isPastDue", () => {
  it("is false until the named day is over", () => {
    expect(isPastDue("2026-11-01", today)).toBe(false); // future
    expect(isPastDue("2026-08-21", today)).toBe(false); // today still counts as verified
  });

  it("is true once the date is past", () => {
    expect(isPastDue("2026-08-20", today)).toBe(true);
    expect(isPastDue("2024-01-01", today)).toBe(true);
  });
});

describe("VerifiedChip", () => {
  it("renders the exact spec copy", () => {
    expect(verifiedLabel("2026-11-01")).toBe("verified until 2026-11-01");
    const { getByText } = render(<VerifiedChip verifiedUntil="2026-11-01" today={today} />);
    getByText("verified until 2026-11-01");
  });

  it("marks a past-due date red (data-past-due drives the --fail color)", () => {
    const { getByText } = render(<VerifiedChip verifiedUntil="2025-12-31" today={today} />);
    expect(getByText("verified until 2025-12-31").getAttribute("data-past-due")).toBe("true");
  });

  it("leaves a future date unmarked", () => {
    const { getByText } = render(<VerifiedChip verifiedUntil="2026-11-01" today={today} />);
    expect(getByText("verified until 2026-11-01").getAttribute("data-past-due")).toBeNull();
  });
});
