/*
 * ContextMeter — the ONE context-budget meter (S34, architecture §11): always-on wiki
 * tokens against the project's effective threshold, rendered identically on the wiki tree,
 * the Agents tab and the run detail's Context panel. Amber past the threshold, with the
 * spec's advice inline — the run still proceeds; the meter is a pressure gauge, not a gate.
 */
import { formatTokenCount } from "../../lib/format/format";
import { CONTEXT_BUDGET_ADVICE, isOverBudget } from "./budget";
import styles from "./ContextMeter.module.css";

export function ContextMeter({
  alwaysTokens,
  thresholdTokens,
  pageCount,
}: {
  alwaysTokens: number;
  thresholdTokens: number;
  /** When known, the always-on page count renders in the label. */
  pageCount?: number;
}) {
  const over = isOverBudget(alwaysTokens, thresholdTokens);
  const fill =
    thresholdTokens > 0 ? Math.min(100, Math.round((alwaysTokens / thresholdTokens) * 100)) : 0;
  return (
    <div className={styles.meter} data-over={over || undefined} aria-label="Context budget">
      <div className={styles.label}>
        Always-on:{" "}
        {pageCount !== undefined && (
          <>
            {pageCount} {pageCount === 1 ? "page" : "pages"} ·{" "}
          </>
        )}
        ~{formatTokenCount(alwaysTokens)} of {formatTokenCount(thresholdTokens)} tokens
      </div>
      <div className={styles.track} role="presentation">
        <div className={styles.fill} style={{ width: `${fill}%` }} />
      </div>
      {over && (
        <p className={styles.advice}>
          Over the context budget — {CONTEXT_BUDGET_ADVICE}.
        </p>
      )}
    </div>
  );
}
