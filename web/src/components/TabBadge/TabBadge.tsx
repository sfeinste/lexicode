/*
 * TabBadge — the count badge on tabs and the Inbox link (UI spec §2.1): counts render only
 * for ACTIONABLE states (Triage shows untriaged, Runs shows needs-attention, never totals),
 * and a zero renders nothing at all. The caller passes the actionable count; this component
 * enforces "no badge when there is nothing to act on".
 */
import styles from "./TabBadge.module.css";

export function TabBadge({ count }: { count: number | undefined }) {
  if (!count || count <= 0) return null;
  return <span className={styles.root}>{count > 99 ? "99+" : count}</span>;
}
