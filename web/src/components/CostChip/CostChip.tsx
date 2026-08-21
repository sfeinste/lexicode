/*
 * CostChip — the only renderer of dollar amounts (architecture §13). It owns the D-5
 * "estimate" affordance: subscription usage is not billed per token, so every dollar figure
 * renders as an estimate (~), with the honest explanation one hover away. Token counts are
 * exact and render without it.
 *
 * Hover shows the input / output / reasoning / cache-read split when the caller has it
 * (UI spec §7).
 */
import { formatTokenCount, formatUSD } from "../../lib/format/format";
import styles from "./CostChip.module.css";

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
    return <span className={styles.root}>—</span>;
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
    <span className={styles.root} title={parts.join("\n")}>
      <span aria-hidden="true" className={styles.estimateMark}>
        ~
      </span>
      {formatUSD(usd)}
      <span className={styles.srOnly}>(estimated)</span>
    </span>
  );
}
