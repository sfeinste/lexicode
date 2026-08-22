/*
 * The audit log UI — `/settings/audit` (S37, UI spec §1: the audit log lives in workspace
 * settings). Owner-only: the API answers 403 for members and the screen says so instead of
 * rendering dead controls.
 *
 * Filters mirror GET /api/v1/audit exactly (actor, action, target kind, project, time
 * window); pagination is the endpoint's keyset cursor. A row expands to a before/after view:
 * both snapshots pretty-printed and run through the same LCS line diff the agent directive
 * history uses (diff.ts) — one diff mechanism in the app, not two.
 */
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import { auditApi, authApi, type AuditEntry } from "../../lib/api/client";
import { diffLines } from "../project/agents/diff";
import styles from "./workspace.module.css";

interface Filters {
  project: string;
  actorKind: string;
  actorId: string;
  action: string;
  target: string;
  since: string;
  until: string;
}

const EMPTY_FILTERS: Filters = {
  project: "",
  actorKind: "",
  actorId: "",
  action: "",
  target: "",
  since: "",
  until: "",
};

function toParams(f: Filters, cursor?: string): URLSearchParams {
  const p = new URLSearchParams();
  if (f.project !== "") p.set("project", f.project.trim());
  if (f.actorKind !== "") {
    p.set("actor", f.actorId.trim() !== "" ? `${f.actorKind}:${f.actorId.trim()}` : f.actorKind);
  }
  if (f.action !== "") p.set("action", f.action.trim());
  if (f.target !== "") p.set("target", f.target.trim());
  if (f.since !== "") p.set("since", new Date(f.since).toISOString());
  if (f.until !== "") p.set("until", new Date(f.until).toISOString());
  if (cursor !== undefined) p.set("cursor", cursor);
  return p;
}

export function AuditLogPage() {
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: ({ signal }) => authApi.me(signal),
    staleTime: 5 * 60_000,
  });

  // draft = what the controls show; applied = what the list queries. Apply is a discrete
  // action so half-typed filters do not thrash the endpoint.
  const [draft, setDraft] = useState<Filters>(EMPTY_FILTERS);
  const [applied, setApplied] = useState<Filters>(EMPTY_FILTERS);
  const [pages, setPages] = useState(1);

  const list = useQuery({
    queryKey: ["audit", applied, pages],
    queryFn: async ({ signal }) => {
      // Keyset pagination: refetch pages sequentially so "load more" extends one list.
      const entries: AuditEntry[] = [];
      let cursor: string | undefined;
      for (let i = 0; i < pages; i++) {
        const res = await auditApi.list(toParams(applied, cursor), signal);
        entries.push(...res.entries);
        if (res.next_cursor === undefined || res.next_cursor === "") {
          return { entries, done: true };
        }
        cursor = res.next_cursor;
      }
      return { entries, done: false };
    },
    enabled: me.data?.role === "owner",
  });

  if (me.isPending) {
    return <div className={styles.root} aria-busy="true" />;
  }
  if (me.data?.role !== "owner") {
    return (
      <div className={styles.root}>
        <h1 className={styles.title}>Audit log</h1>
        <p className={styles.quiet}>Only the workspace owner can read the audit log.</p>
      </div>
    );
  }

  const set = (patch: Partial<Filters>) => setDraft((d) => ({ ...d, ...patch }));

  return (
    <div className={styles.root}>
      <header className={styles.header}>
        <h1 className={styles.title}>Audit log</h1>
      </header>
      <p className={styles.lede}>
        Every mutation, with who did it and the before/after snapshots. Agent actions
        attribute to the agent, never to the token owner.
      </p>

      <form
        className={styles.auditFilters}
        onSubmit={(e) => {
          e.preventDefault();
          setPages(1);
          setApplied(draft);
        }}
        aria-label="Audit filters"
      >
        <label className={styles.auditFilter}>
          Actor
          <select value={draft.actorKind} onChange={(e) => set({ actorKind: e.target.value })}>
            <option value="">any</option>
            <option value="human">human</option>
            <option value="agent">agent</option>
            <option value="trigger">trigger</option>
            <option value="system">system</option>
          </select>
        </label>
        <label className={styles.auditFilter}>
          Actor id
          <input
            value={draft.actorId}
            placeholder="(optional)"
            onChange={(e) => set({ actorId: e.target.value })}
            disabled={draft.actorKind === ""}
          />
        </label>
        <label className={styles.auditFilter}>
          Action
          <input
            value={draft.action}
            placeholder="ticket.move"
            onChange={(e) => set({ action: e.target.value })}
          />
        </label>
        <label className={styles.auditFilter}>
          Target kind
          <input
            value={draft.target}
            placeholder="ticket"
            onChange={(e) => set({ target: e.target.value })}
          />
        </label>
        <label className={styles.auditFilter}>
          Project
          <input
            value={draft.project}
            placeholder="PAY"
            onChange={(e) => set({ project: e.target.value })}
          />
        </label>
        <label className={styles.auditFilter}>
          Since
          <input
            type="datetime-local"
            value={draft.since}
            onChange={(e) => set({ since: e.target.value })}
          />
        </label>
        <label className={styles.auditFilter}>
          Until
          <input
            type="datetime-local"
            value={draft.until}
            onChange={(e) => set({ until: e.target.value })}
          />
        </label>
        <button type="submit" className={styles.auditApply}>
          Apply
        </button>
      </form>

      {list.isError && (
        <p role="alert" className={styles.quiet}>
          The audit log could not load: {list.error.message}
        </p>
      )}
      {list.data !== undefined && list.data.entries.length === 0 && (
        <p className={styles.quiet}>Nothing matches these filters.</p>
      )}
      {list.data !== undefined && list.data.entries.length > 0 && (
        <ul className={styles.auditList} aria-busy={list.isFetching}>
          {list.data.entries.map((e) => (
            <AuditRow key={e.id} entry={e} />
          ))}
        </ul>
      )}
      {list.data !== undefined && !list.data.done && (
        <button
          type="button"
          className={styles.auditApply}
          onClick={() => setPages((n) => n + 1)}
          disabled={list.isFetching}
        >
          {list.isFetching ? "Loading…" : "Load older entries"}
        </button>
      )}
    </div>
  );
}

function actorLabel(e: AuditEntry): string {
  return e.actor_id === null || e.actor_id === "" ? e.actor_kind : `${e.actor_kind}:${e.actor_id}`;
}

function pretty(v: unknown): string {
  if (v === null || v === undefined) return "";
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

/** One entry: a dense row, expandable to the before/after line diff. */
function AuditRow({ entry }: { entry: AuditEntry }) {
  const [open, setOpen] = useState(false);
  const hasSnapshots = entry.before != null || entry.after != null;

  return (
    <li className={styles.auditRow}>
      <button
        type="button"
        className={styles.auditRowHead}
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
      >
        <span className={styles.auditWhen}>{new Date(entry.created_at).toLocaleString()}</span>
        <span className={styles.auditActor} data-kind={entry.actor_kind}>
          {actorLabel(entry)}
        </span>
        <span className={styles.auditAction}>{entry.action}</span>
        <span className={styles.auditTarget}>
          {entry.target_kind}:{entry.target_id}
        </span>
        {entry.note !== undefined && entry.note !== "" && (
          <span className={styles.auditNote}>{entry.note}</span>
        )}
      </button>
      {open && (
        <div className={styles.auditDetail}>
          {hasSnapshots ? (
            <DiffView before={pretty(entry.before)} after={pretty(entry.after)} />
          ) : (
            <p className={styles.quiet}>No before/after snapshots on this entry.</p>
          )}
        </div>
      )}
    </li>
  );
}

/**
 * The before/after view: one unified line diff over the pretty-printed JSON snapshots —
 * the same diffLines the directive history renders with.
 */
function DiffView({ before, after }: { before: string; after: string }) {
  const lines = diffLines(before, after);
  return (
    <pre className={styles.auditDiff}>
      {lines.map((l, i) => (
        <div key={i} className={styles.auditDiffLine} data-op={l.op}>
          <span className={styles.auditDiffMark} aria-hidden="true">
            {l.op === "add" ? "+" : l.op === "del" ? "−" : " "}
          </span>
          {l.text}
        </div>
      ))}
    </pre>
  );
}
