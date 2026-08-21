/*
 * EmptyState — every list's empty rendering (UI spec §7/§8): headline, a two-sentence body,
 * ONE primary CTA, optional secondary. Everything else on the screen dims; never a blank
 * canvas with a toolbar.
 */
import type { ReactNode } from "react";

import styles from "./EmptyState.module.css";

export interface EmptyStateProps {
  headline: string;
  body: string;
  /** The single primary CTA (a button or link). */
  primary?: ReactNode;
  secondary?: ReactNode;
}

export function EmptyState({ headline, body, primary, secondary }: EmptyStateProps) {
  return (
    <div className={styles.root}>
      <h2 className={styles.headline}>{headline}</h2>
      <p className={styles.body}>{body}</p>
      {(primary || secondary) && (
        <div className={styles.actions}>
          {primary}
          {secondary && <span className={styles.secondary}>{secondary}</span>}
        </div>
      )}
    </div>
  );
}
