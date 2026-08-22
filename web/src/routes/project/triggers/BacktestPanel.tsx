/*
 * The backtest panel (S30, UI spec §5.9): replay the project's stored event history through
 * the rule AS THE EDITOR CURRENTLY HOLDS IT — the current form state posts as a draft body,
 * so an unsaved condition edit changes the count without saving. Stages 1–2 only; the panel
 * carries the honest caveat that loop protection and budget are evaluated live (architecture
 * §8.1 — an over-claimed dry run is worse than an honest partial one).
 */
import { useMutation } from "@tanstack/react-query";
import { useState } from "react";

import { ApiProblem, triggersApi, type BacktestResult } from "../../../lib/api/client";
import { formatRelativeTime } from "../../../lib/format/format";
import { draftToInput } from "./draft";
import type { TriggerDraft } from "./TriggerForm";
import styles from "./triggers.module.css";

const WINDOWS = [7, 14, 30];

export function BacktestPanel({ triggerId, draft }: { triggerId: string; draft: TriggerDraft }) {
  const [days, setDays] = useState(7);
  const backtest = useMutation({
    mutationFn: (window: number) => triggersApi.backtest(triggerId, window, draftToInput(draft)),
  });

  const run = (window: number) => {
    setDays(window);
    backtest.mutate(window);
  };

  return (
    <section className={styles.backtestSection} aria-label="Backtest">
      <div className={styles.backtestHead}>
        <h2 className={styles.sectionHead}>Backtest</h2>
        <label className={styles.backtestWindow}>
          Window
          <select
            aria-label="Backtest window"
            value={days}
            onChange={(ev) => run(Number(ev.target.value))}
          >
            {WINDOWS.map((w) => (
              <option key={w} value={w}>
                last {w} days
              </option>
            ))}
          </select>
        </label>
        <button
          type="button"
          className={styles.saveButton}
          onClick={() => run(days)}
          disabled={backtest.isPending}
        >
          {backtest.isPending ? "Replaying…" : "Run backtest"}
        </button>
      </div>

      {backtest.isError && (
        <p className={styles.errorText}>
          {backtest.error instanceof ApiProblem && backtest.error.errors !== undefined
            ? "The rule as drafted is invalid: " +
              backtest.error.errors.map((e) => `${e.field} — ${e.message}`).join("; ")
            : backtest.error instanceof Error
              ? backtest.error.message
              : "Backtest failed."}
        </p>
      )}
      {backtest.data !== undefined && !backtest.isPending && (
        <BacktestResults result={backtest.data} />
      )}
      <p className={styles.backtestHint}>
        Replays the project's real event history against the rule as written here — unsaved
        edits included. Nothing is executed.
      </p>
    </section>
  );
}

/** The result panel, split out so it renders (and tests) without the mutation plumbing. */
export function BacktestResults({ result }: { result: BacktestResult }) {
  if (result.no_history) {
    return (
      <div className={styles.backtestEmpty} data-testid="backtest-no-history">
        <p>
          <strong>No stored events yet, so there is nothing to replay.</strong>
        </p>
        <p>History builds up from the moment a repository is connected.</p>
      </div>
    );
  }

  const n = result.matched;
  const headline =
    n === 0
      ? `This rule would not have fired in the last ${result.days} days.`
      : `This rule would have fired ${n} ${n === 1 ? "time" : "times"} in the last ${result.days} days.`;
  const wouldLine =
    result.would_do.length > 0 ? `would ${result.would_do.join(", then ")}` : "no actions configured";

  return (
    <div className={styles.backtestResults}>
      <p className={styles.backtestHeadline}>{headline}</p>
      <p className={styles.backtestCaveat}>
        {n} {n === 1 ? "event" : "events"} matched. Loop protection and budget are evaluated
        live and may reduce this.
      </p>
      {result.events.length > 0 && (
        <ul className={styles.backtestList}>
          {result.events.map((ev) => (
            <li key={ev.event_id} className={styles.backtestRow}>
              <span className={styles.backtestSubject}>
                {ev.subject} · {ev.kind.replace(/_/g, " ")} {ev.activity_type}
                {ev.actor_login != null && ev.actor_login !== "" ? (
                  <span className={styles.muted}> by @{ev.actor_login}</span>
                ) : (
                  <span className={styles.muted}> by {ev.actor_kind}</span>
                )}
              </span>
              <span className={styles.backtestWould}>{wouldLine}</span>
              <span className={styles.backtestTime}>{formatRelativeTime(ev.occurred_at)}</span>
            </li>
          ))}
        </ul>
      )}
      {result.truncated && (
        <p className={styles.muted}>
          Showing the newest {result.events.length} of {result.matched} matching events.
        </p>
      )}
      <p className={styles.muted}>
        Scanned {result.scanned} stored {result.scanned === 1 ? "event" : "events"} from the
        last {result.days} days.
      </p>
    </div>
  );
}
