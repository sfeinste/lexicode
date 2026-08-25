/*
 * CostChip — the only renderer of dollar amounts (architecture §13). It owns the D-5
 * "estimate" affordance: subscription usage is not billed per token, so every dollar figure
 * renders as an estimate (~), with the honest explanation one hover away. Token counts are
 * exact and render without it.
 *
 * Hover shows the input / output / reasoning / cache-read split when the caller has it
 * (UI spec §7).
 *
 * D-1 (amended) — composition, not invention: Material UI's `Tooltip` wrapping a `Box`.
 * The split moved from a `title` attribute onto a real Tooltip, which is the visible
 * upgrade the library buys here: `title` is invisible to touch, appears after a browser
 * delay nobody can tune, and cannot be read by keyboard at all. Tooltip opens on focus.
 */
import Box from "@mui/material/Box";
import Tooltip from "@mui/material/Tooltip";
import { visuallyHidden } from "@mui/utils";

import { formatTokenCount, formatUSD } from "../../lib/format/format";

export interface CostSplit {
  inputTokens?: number;
  outputTokens?: number;
  reasoningTokens?: number;
  cacheReadTokens?: number;
}

export interface CostChipProps {
  /** Cost in USD as reported by the runtime's result message. */
  usd: number | null | undefined;
  split?: CostSplit;
}

const ESTIMATE_NOTE =
  "Estimated — subscription usage is not billed per token. Token counts are exact.";

export function CostChip({ usd, split }: CostChipProps) {
  if (usd === null || usd === undefined) {
    return (
      <Box component="span" sx={{ fontFamily: "var(--font-mono)", color: "text.disabled" }}>
        —
      </Box>
    );
  }
  const parts: string[] = [ESTIMATE_NOTE];
  if (split) {
    const rows: Array<[string, number | undefined]> = [
      ["in", split.inputTokens],
      ["out", split.outputTokens],
      ["reasoning", split.reasoningTokens],
      ["cache read", split.cacheReadTokens],
    ];
    const detail = rows
      .filter((r): r is [string, number] => r[1] !== undefined)
      .map(([name, n]) => `${name} ${formatTokenCount(n)}`)
      .join(" · ");
    if (detail) parts.push(detail);
  }
  return (
    <Tooltip title={parts.join(" · ")} describeChild>
      {/* tabIndex so the split is reachable without a pointer — the Tooltip opens on
          focus, which the old `title` attribute never did. */}
      <Box
        component="span"
        tabIndex={0}
        sx={{
          display: "inline-flex",
          alignItems: "baseline",
          gap: "1px",
          fontFamily: "var(--font-mono)",
          fontSize: "var(--fs-mono)",
          color: "text.secondary",
        }}
      >
        <Box component="span" aria-hidden="true" sx={{ color: "text.disabled" }}>
          ~
        </Box>
        {formatUSD(usd)}
        <Box component="span" sx={visuallyHidden}>
          (estimated)
        </Box>
      </Box>
    </Tooltip>
  );
}
