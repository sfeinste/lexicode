/*
 * LoopChain — the §5.9 loop chain view: the causal chain (event → run → event → …) from
 * GET /runs/{id}/chain, rendered vertically with the repeating element highlighted. Used on
 * loop-stopped run detail (S23 page) and inline in a trigger's firing history — the same
 * component, so "here is the cycle you built" reads identically everywhere.
 *
 * The "repeating element" is any event signature (kind · activity · subject) that occurs
 * more than once in the chain — the pushed-to-PR hop the loop rides on.
 */
import { Link } from "@tanstack/react-router";

import type { RunChainEntry } from "../../lib/api/client";
import { formatRelativeTime } from "../../lib/format/format";
import { StatusDot, type Status } from "../StatusDot/StatusDot";
import styles from "./LoopChain.module.css";

export interface LoopChainProps {
  chain: RunChainEntry[];
  /** Project key, so run hops can link to their detail pages. */
  projectKey: string;
}

function eventSignature(e: { kind: string; activity_type: string; subject: string }): string {
  return `${e.kind}·${e.activity_type}·${e.subject}`;
}

export function LoopChain({ chain, projectKey }: LoopChainProps) {
  const signatureCounts = new Map<string, number>();
  for (const entry of chain) {
    if (entry.type === "event" && entry.event) {
      const sig = eventSignature(entry.event);
      signatureCounts.set(sig, (signatureCounts.get(sig) ?? 0) + 1);
    }
  }

  return (
    <ol className={styles.chain} aria-label="Causal chain">
      {chain.map((entry, i) => {
        const connector = i > 0 && (
          <span className={styles.connector} aria-hidden="true">
            ▼
          </span>
        );
        if (entry.type === "event" && entry.event) {
          const e = entry.event;
          const repeating = (signatureCounts.get(eventSignature(e)) ?? 0) > 1;
          return (
            <li key={`e${e.id}`} className={styles.hop}>
              {connector}
              <span className={styles.eventHop} data-repeating={repeating || undefined}>
                <span className={styles.hopGlyph} aria-hidden="true">
                  ◆
                </span>
                <span className={styles.hopText}>
                  {e.kind.replace(/_/g, " ")} {e.activity_type} on {e.subject}
                  {e.actor_login != null && e.actor_login !== "" && (
                    <span className={styles.actor}> by @{e.actor_login}</span>
                  )}
                </span>
                {repeating && <span className={styles.repeatBadge}>repeats</span>}
                <span className={styles.hopTime}>{formatRelativeTime(e.occurred_at)}</span>
              </span>
            </li>
          );
        }
        if (entry.type === "run" && entry.run) {
          const r = entry.run;
          return (
            <li key={`r${r.id}`} className={styles.hop}>
              {connector}
              <span className={styles.runHop} data-focus={r.focus || undefined}>
                <Link
                  to="/p/$key/runs/$id"
                  params={{ key: projectKey, id: r.id }}
                  className={styles.runLink}
                >
                  run #{r.seq}
                </Link>
                <span className={styles.hopText}>{r.agent_name || r.agent_id}</span>
                <StatusDot status={r.state as Status} />
                {r.state === "loop_stopped" && r.state_reason !== "" && (
                  <span className={styles.stopReason}>{r.state_reason}</span>
                )}
              </span>
            </li>
          );
        }
        return null;
      })}
    </ol>
  );
}
