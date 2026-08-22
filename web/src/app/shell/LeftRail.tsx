/*
 * The 208px left rail (UI spec §2.1): the project list on top, the permanent NEEDS YOU
 * block at the bottom — always present, capped at 5 rows with "+N more" into /inbox.
 * Collapses to icons at ⌘\ and below 1100px; the preference persists (stores/ui).
 *
 * S07 renders both sections' empty states: the projects API lands in S08, the needs-you
 * feed in S28. The cap-at-5 logic is here already so the data only has to arrive.
 */
import { Link } from "@tanstack/react-router";

import { useInboxQuery } from "../../lib/api/attentionQueries";
import { useProjectsQuery } from "../../lib/api/projectQueries";
import styles from "./shell.module.css";

/** The §2.1 cap: at most 5 needs-you rows in the rail, the rest behind "+N more". */
export const NEEDS_YOU_RAIL_CAP = 5;

interface NeedsYouRow {
  id: string;
  ticketKey: string;
  projectKey: string;
  /** Wiki-proposal rows (S35) link to the page's review view instead of a run. */
  pageSlug?: string;
}

export function LeftRail({ collapsed }: { collapsed: boolean }) {
  const projectsQuery = useProjectsQuery();
  const projects = projectsQuery.data?.projects ?? [];
  // The rail block renders the same GET /inbox query as the home strip and /inbox
  // (architecture §12 — one query, three renderings).
  const inbox = useInboxQuery();
  const needsYou: NeedsYouRow[] = (inbox.data?.runs ?? []).map((r) => ({
    id: r.id,
    ticketKey:
      r.kind === "wiki_proposal" ? (r.page_title ?? "wiki proposal") : (r.ticket_key ?? r.agent),
    projectKey: r.project_key,
    pageSlug: r.kind === "wiki_proposal" ? r.page_slug : undefined,
  }));

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
              {row.pageSlug !== undefined ? (
                <Link
                  to="/p/$key/wiki/$slug"
                  params={{ key: row.projectKey, slug: row.pageSlug }}
                  className={styles.railLink}
                >
                  ▲ {row.ticketKey}
                </Link>
              ) : (
                <Link
                  to="/p/$key/runs/$id"
                  params={{ key: row.projectKey, id: row.id }}
                  className={styles.railLink}
                >
                  ▲ {row.ticketKey}
                </Link>
              )}
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
