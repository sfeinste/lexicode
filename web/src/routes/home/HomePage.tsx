/*
 * Home — `/` (UI spec §5.1): the cross-project "Needs you" strip on top, the projects table
 * below. S08 wires the projects table to GET /projects (real queries; counts largely 0 until
 * later stories run agents) and the create-project flow. The needs-you cards land in S28 —
 * the strip renders its empty state.
 */
import { Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { EmptyState } from "../../components/EmptyState/EmptyState";
import type { NeedsYouRun, ProjectListItem } from "../../lib/api/client";
import { useInboxQuery } from "../../lib/api/attentionQueries";
import { useProjectsQuery } from "../../lib/api/projectQueries";
import { formatRelativeTime, formatUSD } from "../../lib/format/format";
import { NEEDS_YOU_FLAVORS } from "../project/board/TicketCard";
import { CreateProjectDialog } from "./CreateProjectDialog";
import styles from "./home.module.css";

/** The single inline action per card (UI spec §5.1). All four land on the run detail,
 * where answering/approving happens inline in the timeline. */
const FLAVOR_ACTIONS: Record<string, string> = {
  question: "Answer",
  approval: "Approve",
  review: "View diff",
  failure: "View run",
};

export function HomePage() {
  const [creating, setCreating] = useState(false);
  const projects = useProjectsQuery();
  const inbox = useInboxQuery();
  const needsYou = inbox.data?.runs ?? [];

  return (
    <div className={styles.root}>
      <section aria-label="Needs you">
        <h2 className={styles.sectionTitle}>Needs you</h2>
        {needsYou.length === 0 ? (
          <p className={styles.quiet}>Nothing is waiting on you.</p>
        ) : (
          <div className={styles.needsYouStrip}>
            {needsYou.map((r) => (
              <NeedsYouCard key={r.id} row={r} />
            ))}
          </div>
        )}
      </section>
      <section aria-label="Projects">
        <div className={styles.sectionHeader}>
          <h2 className={styles.sectionTitle}>Projects</h2>
          {(projects.data?.projects.length ?? 0) > 0 && (
            <button className={styles.smallCta} onClick={() => setCreating(true)}>
              + New project
            </button>
          )}
        </div>
        {projects.isPending && <p className={styles.quiet}>Loading…</p>}
        {projects.isError && (
          <p role="alert" className={styles.quiet}>
            Projects could not load: {projects.error.message}
          </p>
        )}
        {projects.data && projects.data.projects.length === 0 && (
          <EmptyState
            headline="No projects yet"
            body="A project connects a repo, a board, and a roster of agents."
            primary={
              <button className={styles.cta} onClick={() => setCreating(true)}>
                Create your first project
              </button>
            }
          />
        )}
        {projects.data && projects.data.projects.length > 0 && (
          <ProjectsTable projects={projects.data.projects} />
        )}
      </section>
      {creating && <CreateProjectDialog onClose={() => setCreating(false)} />}
    </div>
  );
}

/** One blocked item, the flavor in words, one inline action (interaction rule 1). */
function NeedsYouCard({ row }: { row: NeedsYouRun }) {
  const flavor = NEEDS_YOU_FLAVORS[row.flavor as keyof typeof NEEDS_YOU_FLAVORS] ?? row.flavor;
  return (
    <div className={styles.needsYouCard}>
      <div className={styles.needsYouTop}>
        <span className={styles.needsYouProject}>{row.project_key}</span>
        <span className={styles.needsYouAge}>{formatRelativeTime(row.started_at)}</span>
      </div>
      <div className={styles.needsYouTicket}>
        {row.ticket_key !== null ? `${row.ticket_key} · ${row.ticket_title ?? ""}` : row.agent}
      </div>
      <div className={styles.needsYouBottom}>
        <span className={styles.needsYouFlavor}>▲ {flavor}</span>
        <Link
          to="/p/$key/runs/$id"
          params={{ key: row.project_key, id: row.id }}
          className={styles.needsYouAction}
        >
          {FLAVOR_ACTIONS[row.flavor] ?? "Open"}
        </Link>
      </div>
    </div>
  );
}

function ProjectsTable({ projects }: { projects: ProjectListItem[] }) {
  const navigate = useNavigate();
  return (
    <div className={styles.tableWrap}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th>Name</th>
            <th>Repo</th>
            <th className={styles.num}>Open tickets</th>
            <th className={styles.num}>Running</th>
            <th className={styles.num}>Needs you</th>
            <th>Spend today</th>
            <th>Last activity</th>
          </tr>
        </thead>
        <tbody>
          {projects.map((p) => (
            <tr
              key={p.id}
              className={styles.row}
              tabIndex={0}
              onClick={() => void navigate({ to: "/p/$key", params: { key: p.key } })}
              onKeyDown={(e) => {
                if (e.key === "Enter")
                  void navigate({ to: "/p/$key", params: { key: p.key } });
              }}
            >
              <td>
                <span className={styles.colorDot} style={{ background: p.color }} />
                <span className={styles.projectName}>{p.name}</span>
                <span className={styles.projectKey}>{p.key}</span>
              </td>
              {/* Repo connects in S14; render the honest empty value until then. */}
              <td className={styles.quietCell}>—</td>
              <td className={styles.num}>{p.stats.open_tickets}</td>
              <td className={styles.num}>
                {p.stats.running_agents > 0 ? (
                  <span className={styles.running}>
                    <span className={styles.pulse} aria-hidden="true" />
                    {p.stats.running_agents}
                  </span>
                ) : (
                  0
                )}
              </td>
              <td className={styles.num}>
                {p.stats.needs_you > 0 ? (
                  <span className={styles.needsYou}>{p.stats.needs_you}</span>
                ) : (
                  0
                )}
              </td>
              <td className={styles.mono}>
                {formatUSD(p.stats.spend_today_cents / 100)}
                <span className={styles.ceiling}>
                  {" / "}
                  {formatUSD(p.settings.daily_budget_cents.value / 100)}
                </span>
              </td>
              <td className={styles.quietCell}>
                {p.stats.last_activity ? formatRelativeTime(p.stats.last_activity) : "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
