/*
 * Inbox — `/inbox` (UI spec §5.10): the cross-project needs-you list, one row per blocked
 * run, updated in place. S24 renders the run rows from GET /inbox (the same query as the
 * home strip and the board lane), grouped by project, each with the flavor in words and one
 * inline action; failures carry Dismiss (acknowledge). The full S36 inbox adds outputs
 * awaiting review and the J/K/Enter/A/X keyboard.
 */
import { Link } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { EmptyState } from "../../components/EmptyState/EmptyState";
import { runsApi, type NeedsYouRun } from "../../lib/api/client";
import { attentionKeys, useInboxQuery } from "../../lib/api/attentionQueries";
import { NEEDS_YOU_FLAVORS } from "../project/board/TicketCard";
import { formatRelativeTime } from "../../lib/format/format";
import styles from "./inbox.module.css";

const FLAVOR_ACTIONS: Record<string, string> = {
  question: "Answer",
  approval: "Approve",
  review: "View diff",
  failure: "View run",
};

export function InboxPage() {
  const inbox = useInboxQuery();
  const rows = inbox.data?.runs ?? [];

  if (inbox.isPending) {
    return <p className={styles.quiet}>Loading…</p>;
  }
  if (rows.length === 0) {
    return (
      <EmptyState
        headline="Inbox zero"
        body="Everything awaiting a human, across all projects, lands here. Nothing is waiting right now."
      />
    );
  }

  const byProject = new Map<string, NeedsYouRun[]>();
  for (const r of rows) {
    const list = byProject.get(r.project_key) ?? [];
    list.push(r);
    byProject.set(r.project_key, list);
  }

  return (
    <div className={styles.root}>
      <h1 className={styles.title}>Inbox</h1>
      {[...byProject.entries()].map(([project, group]) => (
        <section key={project} aria-label={project} className={styles.group}>
          <h2 className={styles.groupTitle}>{project}</h2>
          <ul className={styles.list}>
            {group.map((r) => (
              <InboxRow key={r.id} row={r} />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function InboxRow({ row }: { row: NeedsYouRun }) {
  const qc = useQueryClient();
  const acknowledge = useMutation({
    mutationFn: () => runsApi.acknowledge(row.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: attentionKeys.inbox });
    },
  });
  const flavor = NEEDS_YOU_FLAVORS[row.flavor as keyof typeof NEEDS_YOU_FLAVORS] ?? row.flavor;
  return (
    <li className={styles.row}>
      <span className={styles.flavor}>▲ {flavor}</span>
      <span className={styles.ticket}>
        {row.ticket_key !== null ? `${row.ticket_key} · ${row.ticket_title ?? ""}` : row.agent}
      </span>
      <span className={styles.age}>{formatRelativeTime(row.started_at)}</span>
      {row.flavor === "failure" && (
        <button
          type="button"
          className={styles.dismiss}
          disabled={acknowledge.isPending}
          onClick={() => acknowledge.mutate()}
        >
          Dismiss
        </button>
      )}
      <Link
        to="/p/$key/runs/$id"
        params={{ key: row.project_key, id: row.id }}
        className={styles.action}
      >
        {FLAVOR_ACTIONS[row.flavor] ?? "Open"}
      </Link>
    </li>
  );
}
