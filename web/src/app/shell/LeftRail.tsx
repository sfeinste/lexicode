/*
 * The 208px left rail (UI spec §2.1): the project list on top, the permanent NEEDS YOU
 * block at the bottom — always present, capped at 5 rows with "+N more" into /inbox.
 * Collapses to icons at ⌘\ and below 1100px; the preference persists (stores/ui).
 *
 * S07 renders both sections' empty states: the projects API lands in S08, the needs-you
 * feed in S28. The cap-at-5 logic is here already so the data only has to arrive.
 */
import { Link } from "@tanstack/react-router";

import styles from "./shell.module.css";

/** The §2.1 cap: at most 5 needs-you rows in the rail, the rest behind "+N more". */
export const NEEDS_YOU_RAIL_CAP = 5;

interface NeedsYouRow {
  id: string;
  ticketKey: string;
  href: string;
}

export function LeftRail({ collapsed }: { collapsed: boolean }) {
  // Wired when the APIs exist; the layout logic below is real from day one.
  const projects: Array<{ key: string; name: string }> = [];
  const needsYou: NeedsYouRow[] = [];

  const shown = needsYou.slice(0, NEEDS_YOU_RAIL_CAP);
  const overflow = needsYou.length - shown.length;

  return (
    <nav className={styles.rail} aria-label="Projects" data-collapsed={collapsed || undefined}>
      <div className={styles.railSection}>
        <div className={styles.railHeading}>{collapsed ? "P" : "Projects"}</div>
        {projects.length === 0 && !collapsed && (
          <div className={styles.railEmpty}>No projects yet</div>
        )}
        <ul className={styles.railList}>
          {projects.map((p) => (
            <li key={p.key}>
              <Link
                to="/p/$key"
                params={{ key: p.key }}
                className={styles.railLink}
                activeProps={{ "data-active": "" }}
              >
                {collapsed ? p.key.slice(0, 2) : p.name}
              </Link>
            </li>
          ))}
        </ul>
      </div>

      <div className={`${styles.railSection} ${styles.railNeedsYou}`}>
        <div className={styles.railHeading}>{collapsed ? "!" : "Needs you"}</div>
        {shown.length === 0 && !collapsed && (
          <div className={styles.railEmpty}>Nothing waiting</div>
        )}
        <ul className={styles.railList}>
          {shown.map((row) => (
            <li key={row.id}>
              <a href={row.href} className={styles.railLink}>
                ▲ {row.ticketKey}
              </a>
            </li>
          ))}
        </ul>
        {overflow > 0 && (
          <Link to="/inbox" className={styles.railMore}>
            +{overflow} more
          </Link>
        )}
      </div>
    </nav>
  );
}
