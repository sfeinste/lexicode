/*
 * The tabbed project frame (UI spec §1 rule 3, §2.1): one URL prefix, a persistent header,
 * tabs beneath. Tab badges render only actionable counts (Triage: untriaged, Runs:
 * needs-attention) — the TabBadge component suppresses zeros, and the counts wire up when
 * their APIs land (S16, S21).
 */
import { Link, Outlet, useParams } from "@tanstack/react-router";

import { TabBadge } from "../../components/TabBadge/TabBadge";
import { useProjectQuery } from "../../lib/api/projectQueries";
import styles from "./shell.module.css";

const TABS: Array<{ label: string; to: string; exact?: boolean; count?: number }> = [
  { label: "Overview", to: "/p/$key", exact: true },
  { label: "Board", to: "/p/$key/board" },
  { label: "Triage", to: "/p/$key/triage", count: undefined /* untriaged (S16) */ },
  { label: "Wiki", to: "/p/$key/wiki" },
  { label: "Runs", to: "/p/$key/runs", count: undefined /* needs-attention (S21) */ },
  { label: "Agents", to: "/p/$key/agents" },
  { label: "Triggers", to: "/p/$key/triggers" },
];

export function ProjectLayout() {
  const { key } = useParams({ from: "/shell/p/$key" });
  const project = useProjectQuery(key);

  return (
    <div className={styles.project}>
      <header className={styles.projectHeader}>
        <span className={styles.projectKey}>{key}</span>
        <span className={styles.projectName}>{project.data?.name ?? ""}</span>
        <span className={styles.projectMeta}>
          <Link
            to="/p/$key/settings"
            params={{ key }}
            aria-label="Project settings"
            className={styles.iconButton}
          >
            ⚙
          </Link>
        </span>
      </header>
      <nav className={styles.tabs} aria-label="Project">
        {TABS.map((tab) => (
          <Link
            key={tab.to}
            to={tab.to}
            params={{ key }}
            className={styles.tab}
            activeOptions={{ exact: tab.exact ?? false }}
            activeProps={{ "data-active": "" }}
          >
            {tab.label}
            <TabBadge count={tab.count} />
          </Link>
        ))}
        <Link
          to="/p/$key/settings"
          params={{ key }}
          className={`${styles.tab} ${styles.tabSettings}`}
          activeProps={{ "data-active": "" }}
          aria-label="Settings"
        >
          ⚙
        </Link>
      </nav>
      <div className={styles.tabContent}>
        <Outlet />
      </div>
    </div>
  );
}
