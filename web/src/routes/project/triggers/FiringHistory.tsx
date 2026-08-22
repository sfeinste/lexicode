/*
 * The firing history list on the trigger editor page: one row per firing — outcome chip in
 * the §4.2 StatusDot vocabulary, the reason in words, the causing event's summary, links to
 * the run it started or was absorbed by, and the timestamp. A loop-stopped firing expands
 * inline into the loop chain view (the same component the run detail renders).
 */
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { LoopChain } from "../../../components/LoopChain/LoopChain";
import { StatusDot, type Status } from "../../../components/StatusDot/StatusDot";
import type { TriggerFiring } from "../../../lib/api/client";
import { useRunChainQuery } from "../../../lib/api/triggerQueries";
import { formatRelativeTime } from "../../../lib/format/format";
import styles from "./triggers.module.css";

export function FiringHistory({
  projectKey,
  firings,
}: {
  projectKey: string;
  firings: TriggerFiring[];
}) {
  const [openChain, setOpenChain] = useState<string | null>(null);

  if (firings.length === 0) {
    return <p className={styles.muted}>No firings yet — history appears as events arrive.</p>;
  }

  return (
    <ul className={styles.firingList}>
      {firings.map((f) => (
        <li key={f.id} className={styles.firingRow}>
          <div className={styles.firingLine}>
            <StatusDot status={f.outcome as Status} />
            {f.event !== undefined && (
              <span className={styles.firingEvent}>
                {f.event.kind.replace(/_/g, " ")} {f.event.activity_type} on {f.event.subject}
                {f.event.actor_login != null && f.event.actor_login !== "" && (
                  <span className={styles.muted}> by @{f.event.actor_login}</span>
                )}
              </span>
            )}
            {f.reason !== "" && <span className={styles.firingReason}>{f.reason}</span>}
            <span className={styles.firingLinks}>
              {f.run_id !== null && f.outcome !== "loop_stopped" && (
                <Link to="/p/$key/runs/$id" params={{ key: projectKey, id: f.run_id }}>
                  run →
                </Link>
              )}
              {f.absorbed_by_run_id !== null && (
                <Link
                  to="/p/$key/runs/$id"
                  params={{ key: projectKey, id: f.absorbed_by_run_id }}
                >
                  absorbed by →
                </Link>
              )}
              {f.outcome === "loop_stopped" && f.run_id !== null && (
                <button
                  type="button"
                  className={styles.chainToggle}
                  aria-expanded={openChain === f.id}
                  onClick={() => setOpenChain(openChain === f.id ? null : f.id)}
                >
                  {openChain === f.id ? "hide chain" : "view chain"}
                </button>
              )}
            </span>
            <span className={styles.firingTime}>{formatRelativeTime(f.created_at)}</span>
          </div>
          {Array.isArray(f.warnings) && f.warnings.length > 0 && (
            <ul className={styles.firingWarnings}>
              {(f.warnings as string[]).map((w, i) => (
                <li key={i}>⚠ {w}</li>
              ))}
            </ul>
          )}
          {openChain === f.id && f.run_id !== null && (
            <InlineChain projectKey={projectKey} runId={f.run_id} />
          )}
        </li>
      ))}
    </ul>
  );
}

function InlineChain({ projectKey, runId }: { projectKey: string; runId: string }) {
  const chain = useRunChainQuery(runId);
  if (chain.isPending) return <p className={styles.muted}>Loading chain…</p>;
  if (chain.isError || chain.data === undefined) {
    return <p className={styles.muted}>The chain failed to load.</p>;
  }
  return (
    <div className={styles.inlineChain}>
      <LoopChain chain={chain.data.chain} projectKey={projectKey} />
    </div>
  );
}
