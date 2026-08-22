/*
 * Inbox — `/inbox` (UI spec §5.10): the cross-project needs-you list. One row per blocked
 * item, updated in place, never stacked; grouped by project; APPROVALS SORT TO THE TOP
 * ALWAYS (sortForInbox); every row carries the flavor in words and one inline action —
 * questions are answered and approvals decided from the row itself (the S24 respond
 * components, embedded via InlineElicitation — no navigation), PR review rows link to the
 * PR and the producing run, failures carry Dismiss (acknowledge).
 *
 * Keyboard (§6): J/K walk the rows, Enter opens the selected row, A fires its primary
 * action, X dismisses — through the S07 registry (keymap.ts), so the cheatsheet and ⌘K
 * palette show these chords like any other.
 */
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { EmptyState } from "../../components/EmptyState/EmptyState";
import { runsApi, type NeedsYouRun } from "../../lib/api/client";
import { attentionKeys, useInboxQuery } from "../../lib/api/attentionQueries";
import { useKeyBindings, useKeyScope } from "../../lib/keyboard/hooks";
import { formatRelativeTime } from "../../lib/format/format";
import { InlineElicitation } from "../project/runs/InlineElicitation";
import { buildInboxBindings } from "./keymap";
import { needsYouView, sortForInbox } from "./needsYouView";
import styles from "./inbox.module.css";

/** A list key that stays unique across the three row kinds. */
function rowKey(r: NeedsYouRun): string {
  return `${r.kind}:${r.id}`;
}

export function InboxPage() {
  const inbox = useInboxQuery();
  const navigate = useNavigate();
  const qc = useQueryClient();

  const sorted = sortForInbox(inbox.data?.runs ?? []);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [expandedKey, setExpandedKey] = useState<string | null>(null);

  const acknowledge = useMutation({
    mutationFn: (runId: string) => runsApi.acknowledge(runId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: attentionKeys.inbox });
    },
  });

  // Latest-state ref so the bindings register once and still see current data.
  const stateRef = useRef({ sorted, selectedKey, expandedKey });
  stateRef.current = { sorted, selectedKey, expandedKey };

  useKeyScope("route", true);
  useKeyBindings(
    () =>
      buildInboxBindings({
        hasSelection: () => stateRef.current.selectedKey !== null,
        moveSelection: (delta) => {
          const { sorted: rows, selectedKey: sel } = stateRef.current;
          if (rows.length === 0) return;
          const i = sel === null ? -1 : rows.findIndex((r) => rowKey(r) === sel);
          const next = rows[Math.min(rows.length - 1, Math.max(0, i + delta))];
          setSelectedKey(rowKey(next));
        },
        openSelected: () => {
          const row = selectedRow(stateRef.current);
          if (row === undefined) return;
          const view = needsYouView(row);
          void navigate({ to: view.link.to, params: view.link.params });
        },
        primaryAction: () => {
          const row = selectedRow(stateRef.current);
          if (row === undefined) return;
          const view = needsYouView(row);
          if (view.respondRunId !== undefined) {
            // Answer / approve inline, from the row (never a navigation).
            setExpandedKey((k) => (k === rowKey(row) ? null : rowKey(row)));
          } else if (view.href !== undefined) {
            window.open(view.href, "_blank", "noopener");
          } else {
            void navigate({ to: view.link.to, params: view.link.params });
          }
        },
        dismissSelected: () => {
          const row = selectedRow(stateRef.current);
          if (row !== undefined && row.kind === "run" && row.flavor === "failure") {
            acknowledge.mutate(row.id);
          }
        },
      }),
    [],
  );

  // Keep the keyboard cursor visible as J/K walk past the fold.
  useEffect(() => {
    if (selectedKey === null) return;
    document
      .querySelector(`[data-row-key="${CSS.escape(selectedKey)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [selectedKey]);

  if (inbox.isPending) {
    return <p className={styles.quiet}>Loading…</p>;
  }
  if (sorted.length === 0) {
    return (
      <EmptyState
        headline="Inbox zero"
        body="Everything awaiting a human, across all projects, lands here. Nothing is waiting right now."
      />
    );
  }

  // Group AFTER sorting: approvals surface at the top of each project group too.
  const byProject = new Map<string, NeedsYouRun[]>();
  for (const r of sorted) {
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
              <InboxRow
                key={rowKey(r)}
                row={r}
                selected={selectedKey === rowKey(r)}
                expanded={expandedKey === rowKey(r)}
                onSelect={() => setSelectedKey(rowKey(r))}
                onToggleExpand={() =>
                  setExpandedKey((k) => (k === rowKey(r) ? null : rowKey(r)))
                }
                onAcknowledge={() => acknowledge.mutate(r.id)}
                acknowledging={acknowledge.isPending}
              />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

function selectedRow(s: {
  sorted: NeedsYouRun[];
  selectedKey: string | null;
}): NeedsYouRun | undefined {
  if (s.selectedKey === null) return undefined;
  return s.sorted.find((r) => rowKey(r) === s.selectedKey);
}

function InboxRow({
  row,
  selected,
  expanded,
  onSelect,
  onToggleExpand,
  onAcknowledge,
  acknowledging,
}: {
  row: NeedsYouRun;
  selected: boolean;
  expanded: boolean;
  onSelect: () => void;
  onToggleExpand: () => void;
  onAcknowledge: () => void;
  acknowledging: boolean;
}) {
  const view = needsYouView(row);
  return (
    <li
      className={styles.row}
      data-row-key={rowKey(row)}
      data-selected={selected || undefined}
      onClick={onSelect}
    >
      <div className={styles.rowLine}>
        <span className={styles.flavor}>▲ {view.flavorLabel}</span>
        <span className={styles.ticket}>{view.subject}</span>
        <span className={styles.age}>{formatRelativeTime(row.started_at)}</span>
        {row.kind === "run" && row.flavor === "failure" && (
          <button
            type="button"
            className={styles.dismiss}
            disabled={acknowledging}
            onClick={(e) => {
              e.stopPropagation();
              onAcknowledge();
            }}
          >
            Dismiss
          </button>
        )}
        {view.respondRunId !== undefined ? (
          <button
            type="button"
            className={styles.action}
            data-active={expanded || undefined}
            onClick={(e) => {
              e.stopPropagation();
              onToggleExpand();
            }}
          >
            {view.action}
          </button>
        ) : view.href !== undefined ? (
          <>
            <Link to={view.link.to} params={view.link.params} className={styles.secondary}>
              View run
            </Link>
            <a
              href={view.href}
              target="_blank"
              rel="noreferrer noopener"
              className={styles.action}
            >
              {view.action}
            </a>
          </>
        ) : (
          <Link to={view.link.to} params={view.link.params} className={styles.action}>
            {view.action}
          </Link>
        )}
      </div>
      {expanded && view.respondRunId !== undefined && (
        <InlineElicitation runId={view.respondRunId} projectKey={row.project_key} />
      )}
    </li>
  );
}
