/*
 * ScopeBadge — the five `agent_scope` values, everywhere a wiki page shows one (tree rows,
 * page headers, later the context panel; UI spec §7). ALWAYS renders amber (--needs-you)
 * because an always-scoped page costs context on every single run; NEVER is muted; the
 * middle three are neutral. Rendering is driven by data-scope so the palette lives in CSS.
 */
import type { AgentScope } from "../../lib/api/client";

import styles from "./ScopeBadge.module.css";

export function ScopeBadge({ scope }: { scope: AgentScope }) {
  return (
    <span className={styles.badge} data-scope={scope}>
      {scope.toUpperCase()}
    </span>
  );
}
