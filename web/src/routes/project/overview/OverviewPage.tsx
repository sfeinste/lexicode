/*
 * Project Overview — `/p/:key` (UI spec §5.2). S08 renders the About card with the fields
 * that exist: description, owner, agent count, open tickets, runs today, spend today /
 * ceiling. Repo + branch + last commit arrive with S14; the three columns below (recent
 * runs, pinned wiki pages, activity feed) with their stories.
 */
import { useParams } from "@tanstack/react-router";

import { useProjectOverviewQuery } from "../../../lib/api/projectQueries";
import { formatUSD } from "../../../lib/format/format";
import styles from "./overview.module.css";

export function OverviewPage() {
  const { key } = useParams({ from: "/shell/p/$key/" });
  const overview = useProjectOverviewQuery(key);

  if (overview.isPending) {
    return <div className={styles.root} aria-busy="true" />;
  }
  if (overview.isError) {
    return (
      <div className={styles.root}>
        <p role="alert" className={styles.quiet}>
          Overview could not load: {overview.error.message}
        </p>
      </div>
    );
  }

  const { project, owner, agent_count, open_tickets, runs_today, spend_today_cents } =
    overview.data;
  const ceiling = project.settings.daily_budget_cents.value;

  return (
    <div className={styles.root}>
      <section className={styles.about} aria-label="About">
        <div className={styles.aboutMain}>
          <h1 className={styles.title}>
            <span className={styles.colorDot} style={{ background: project.color }} />
            {project.name}
            {project.archived_at && <span className={styles.archived}>Archived</span>}
          </h1>
          <p className={styles.description}>
            {project.description || "No description yet — add one in Settings."}
          </p>
        </div>
        <dl className={styles.facts}>
          <div className={styles.fact}>
            <dt>Owner</dt>
            <dd>
              <span
                className={styles.avatar}
                style={{ background: owner.avatar_color }}
                aria-hidden="true"
              >
                {owner.display_name.slice(0, 1).toUpperCase()}
              </span>
              {owner.display_name}
            </dd>
          </div>
          <div className={styles.fact}>
            <dt>Repo</dt>
            <dd className={styles.quiet}>Not connected</dd>
          </div>
          <div className={styles.fact}>
            <dt>Agents</dt>
            <dd>{agent_count}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Open tickets</dt>
            <dd>{open_tickets}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Runs today</dt>
            <dd>{runs_today}</dd>
          </div>
          <div className={styles.fact}>
            <dt>Spend today</dt>
            <dd className={styles.mono}>
              {formatUSD(spend_today_cents / 100)}
              <span className={styles.quiet}> / {formatUSD(ceiling / 100)}</span>
            </dd>
          </div>
        </dl>
      </section>
      <div className={styles.columns}>
        <section aria-label="Recent runs">
          <h2 className={styles.sectionTitle}>Recent runs</h2>
          <p className={styles.quiet}>Runs appear here once agents start working.</p>
        </section>
        <section aria-label="Pinned pages">
          <h2 className={styles.sectionTitle}>Pinned pages</h2>
          <p className={styles.quiet}>Pin wiki pages to keep them at hand.</p>
        </section>
        <section aria-label="Activity">
          <h2 className={styles.sectionTitle}>Activity</h2>
          <p className={styles.quiet}>Nothing has happened yet.</p>
        </section>
      </div>
    </div>
  );
}
