/*
 * One rule as prose (UI spec §5.9): name + enable toggle, the WHEN/IF/THEN lines, then the
 * health strip — fired count, the outcome sparkline colored by class (last ~20 firings,
 * oldest→newest), the per-outcome breakdown in §4.2 words, the actor-suppression line and
 * last-fired. A rule with zero firings renders an empty sparkline and "never fired".
 */
import type { ReactNode } from "react";

import {
  TRIGGER_OUTCOMES,
  type StatusMeta,
} from "../../../components/StatusDot/StatusDot";
import type { FiringOutcome, Trigger, TriggerCatalog } from "../../../lib/api/client";
import { formatRelativeTime } from "../../../lib/format/format";
import {
  actionAgentIds,
  composeBreakdown,
  composeIf,
  composeThen,
  composeWhen,
  firedCount,
  suppressionLine,
} from "./prose";
import styles from "./triggers.module.css";

/** The sparkline: one cell per firing, colored by outcome class, never by binary success. */
export function OutcomeSparkline({ outcomes }: { outcomes: FiringOutcome[] }) {
  if (outcomes.length === 0) {
    return (
      <span className={styles.sparkline} aria-label="No firings yet" data-empty="true" />
    );
  }
  return (
    <span className={styles.sparkline} role="img" aria-label={`Last ${outcomes.length} outcomes`}>
      {outcomes.map((o, i) => {
        const meta: StatusMeta | undefined = TRIGGER_OUTCOMES[o];
        return (
          <span
            key={i}
            className={styles.sparkCell}
            title={meta?.label ?? o}
            style={{ background: `var(--${meta?.color ?? "muted"})` }}
          />
        );
      })}
    </span>
  );
}

export interface TriggerCardProps {
  trigger: Trigger;
  catalog: TriggerCatalog;
  /** id → display name, for the actor-suppression line. */
  agentNames: Map<string, string>;
  onToggle: (enabled: boolean) => void;
  /** The card body wraps in this (the list page passes a Link to the editor). */
  wrap?: (children: ReactNode) => ReactNode;
}

export function TriggerCard({ trigger, catalog, agentNames, onToggle, wrap }: TriggerCardProps) {
  const health = trigger.health;
  const counts = (health?.counts ?? {}) as Record<string, number>;
  const fired = firedCount(counts);
  const recent = (health?.recent ?? []) as FiringOutcome[];
  const whenLine = composeWhen(trigger, catalog);
  const ifLine = composeIf(trigger.conditions, catalog);
  const names = actionAgentIds(trigger).map((id) => agentNames.get(id) ?? id);

  const body = (
    <>
      <div className={styles.proseLines}>
        <ProseLine label="WHEN">{whenLine}</ProseLine>
        {ifLine !== "" && <ProseLine label="IF">{ifLine}</ProseLine>}
        <ProseLine label="THEN">{composeThen(trigger)}</ProseLine>
      </div>
      <div className={styles.cardRule} role="presentation" />
      <div className={styles.healthRow}>
        {fired > 0 ? (
          <>
            <span className={styles.firedCount}>Fired {fired}×</span>
            <OutcomeSparkline outcomes={recent} />
            <span className={styles.breakdown}>{composeBreakdown(counts)}</span>
          </>
        ) : (
          <>
            <span className={styles.firedCount}>Never fired</span>
            <OutcomeSparkline outcomes={[]} />
          </>
        )}
      </div>
      <div className={styles.suppressionRow}>
        <span className={styles.suppression}>{suppressionLine(trigger, names)}</span>
        <span className={styles.lastFired}>
          {health?.last_fired_at != null
            ? `last: ${formatRelativeTime(health.last_fired_at)}`
            : "never fired"}
        </span>
      </div>
    </>
  );

  return (
    <article
      className={styles.card}
      data-disabled={!trigger.enabled || undefined}
      aria-label={trigger.name}
    >
      <header className={styles.cardHead}>
        <span className={styles.cardGlyph} aria-hidden="true">
          ⚡
        </span>
        <h2 className={styles.cardName}>{trigger.name}</h2>
        <label className={styles.toggle}>
          <input
            type="checkbox"
            checked={trigger.enabled}
            onChange={(e) => onToggle(e.target.checked)}
            aria-label={`${trigger.name} enabled`}
          />
          {trigger.enabled ? "enabled" : "disabled"}
        </label>
      </header>
      {wrap ? wrap(body) : body}
    </article>
  );
}

function ProseLine({ label, children }: { label: string; children: ReactNode }) {
  return (
    <p className={styles.proseLine}>
      <span className={styles.proseKeyword}>{label}</span>
      <span>{children}</span>
    </p>
  );
}
