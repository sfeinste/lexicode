/*
 * S30 acceptance for the result panel: the headline count, the honest caveat (verbatim —
 * loop protection and budget are evaluated live), the per-event would-do lines, and the
 * distinct no-history empty state.
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { BacktestResult } from "../../../lib/api/client";
import { BacktestResults } from "./BacktestPanel";

function result(overrides: Partial<BacktestResult>): BacktestResult {
  return {
    days: 7,
    scanned: 42,
    matched: 0,
    truncated: false,
    events: [],
    would_do: [],
    no_history: false,
    ...overrides,
  };
}

describe("backtest results panel", () => {
  it("renders the would-have-fired headline with the count and window", () => {
    render(
      <BacktestResults
        result={result({
          matched: 7,
          would_do: ["run agent Reviewer"],
          events: [
            {
              event_id: "e1",
              kind: "pull_request",
              activity_type: "opened",
              actor_kind: "human",
              actor_login: "ada",
              subject: "pr:219",
              occurred_at: new Date().toISOString(),
            },
          ],
        })}
      />,
    );
    expect(
      screen.getByText("This rule would have fired 7 times in the last 7 days."),
    ).toBeTruthy();
  });

  it("carries the honest caveat verbatim: guard and budget are evaluated live", () => {
    render(<BacktestResults result={result({ matched: 7 })} />);
    expect(
      screen.getByText(
        "7 events matched. Loop protection and budget are evaluated live and may reduce this.",
      ),
    ).toBeTruthy();
  });

  it("lists each matching event with its would-do line", () => {
    render(
      <BacktestResults
        result={result({
          matched: 1,
          would_do: ["run agent Reviewer", "notify the delegating human"],
          events: [
            {
              event_id: "e1",
              kind: "pull_request",
              activity_type: "opened",
              actor_kind: "human",
              actor_login: "ada",
              subject: "pr:219",
              occurred_at: new Date().toISOString(),
            },
          ],
        })}
      />,
    );
    expect(screen.getByText(/pr:219/)).toBeTruthy();
    expect(screen.getByText(/by @ada/)).toBeTruthy();
    expect(
      screen.getByText("would run agent Reviewer, then notify the delegating human"),
    ).toBeTruthy();
    // Singular forms for a single match.
    expect(
      screen.getByText("This rule would have fired 1 time in the last 7 days."),
    ).toBeTruthy();
    expect(
      screen.getByText(
        "1 event matched. Loop protection and budget are evaluated live and may reduce this.",
      ),
    ).toBeTruthy();
  });

  it("shows a plain zero when history exists but nothing matched", () => {
    render(<BacktestResults result={result({ matched: 0, scanned: 42 })} />);
    expect(
      screen.getByText("This rule would not have fired in the last 7 days."),
    ).toBeTruthy();
    expect(screen.getByText(/Scanned 42 stored events from the last 7 days/)).toBeTruthy();
    expect(screen.queryByTestId("backtest-no-history")).toBeNull();
  });

  it("shows the distinct no-history empty state when the project has no stored events", () => {
    render(<BacktestResults result={result({ no_history: true, scanned: 0 })} />);
    expect(screen.getByTestId("backtest-no-history")).toBeTruthy();
    expect(
      screen.getByText("History builds up from the moment a repository is connected."),
    ).toBeTruthy();
    // The would-have-fired framing does not render at all here.
    expect(screen.queryByText(/would not have fired/)).toBeNull();
  });

  it("names the truncation when the match count exceeds the event cap", () => {
    const events = Array.from({ length: 3 }, (_, i) => ({
      event_id: `e${i}`,
      kind: "pull_request",
      activity_type: "opened",
      actor_kind: "human",
      actor_login: null,
      subject: `pr:${i}`,
      occurred_at: new Date().toISOString(),
    }));
    render(
      <BacktestResults
        result={result({
          matched: 150,
          truncated: true,
          events,
          would_do: ["run agent Reviewer"],
        })}
      />,
    );
    expect(screen.getByText("Showing the newest 3 of 150 matching events.")).toBeTruthy();
  });
});
