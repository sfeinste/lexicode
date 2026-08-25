/*
 * ContextMeter — the ONE context-budget meter (S34, architecture §11): always-on wiki
 * tokens against the project's effective threshold, rendered identically on the wiki tree,
 * the Agents tab and the run detail's Context panel. Amber past the threshold, with the
 * spec's advice inline — the run still proceeds; the meter is a pressure gauge, not a gate.
 *
 * D-1 (amended) — composition, not invention: Material UI's `LinearProgress` is the track
 * and the fill, `Typography` the label and the advice. The hand-rolled track/fill divs are
 * gone. `LinearProgress` also supplies the `progressbar` role with its value bounds, which
 * the two divs never did.
 */
import Box from "@mui/material/Box";
import LinearProgress from "@mui/material/LinearProgress";
import Typography from "@mui/material/Typography";

import { formatTokenCount } from "../../lib/format/format";
import { CONTEXT_BUDGET_ADVICE, isOverBudget } from "./budget";

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
    <Box
      data-over={over || undefined}
      aria-label="Context budget"
      sx={{ display: "grid", gap: "4px", py: "6px" }}
    >
      <Typography variant="body2" sx={{ color: over ? "warning.main" : "text.secondary" }}>
        Always-on:{" "}
        {pageCount !== undefined && (
          <>
            {pageCount} {pageCount === 1 ? "page" : "pages"} ·{" "}
          </>
        )}
        ~{formatTokenCount(alwaysTokens)} of {formatTokenCount(thresholdTokens)} tokens
      </Typography>
      <LinearProgress
        variant="determinate"
        value={fill}
        color={over ? "warning" : "primary"}
        aria-label="Always-on context against the project threshold"
        sx={{ height: 4, borderRadius: 2, backgroundColor: "lexicode.surface3" }}
      />
      {over && (
        <Typography variant="body2" sx={{ color: "warning.main" }}>
          Over the context budget — {CONTEXT_BUDGET_ADVICE}.
        </Typography>
      )}
    </Box>
  );
}
