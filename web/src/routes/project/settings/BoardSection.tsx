/*
 * Project settings → Board (S09, UI spec §5.11): the column editor. Each row shows the
 * column's name (rename inline — commit on blur or Enter), its category badge and picker,
 * WIP limit, the auto-start toggle, reorder controls and delete.
 *
 * Reordering is up/down buttons rather than drag: deterministic, keyboard-accessible, and no
 * drag dependency for a settings list this short. The button sends the server an `after_id`
 * anchor; positions are server-assigned, so the list re-renders from the response ordering.
 *
 * The category badge renders from `category`, never from the name — the name is a display
 * string a user may change at will (plan rule 3); the badge is what automation will read.
 *
 * Enabling auto-start requires an explicit confirm dialog because it spends money silently
 * (brief: silent agent spend is unacceptable). Disabling needs no confirm.
 *
 * Deleting a column that holds tickets opens a destination picker — the server refuses the
 * delete without one, and moves the tickets in the same transaction.
 */
import { useState } from "react";

import { ApiProblem, type Column, type ColumnCategory } from "../../../lib/api/client";
import {
  useColumnsQuery,
  useCreateColumn,
  useDeleteColumn,
  useUpdateColumn,
} from "../../../lib/api/columnQueries";
import styles from "./settings.module.css";

const CATEGORIES: ColumnCategory[] = [
  "backlog",
  "ready",
  "running",
  "review",
  "done",
  "canceled",
];

/** The §5.3 auto-start warning: enabling this column spends money without a human click. */
export const AUTO_START_WARNING =
  "Tickets moved into this column will automatically start their delegate agent. " +
  "Agent runs cost money.";

function problemMessage(err: unknown): string {
  if (err instanceof ApiProblem) {
    if (err.errors?.length) return err.errors.map((e) => e.message).join(" ");
    return err.detail || err.title;
  }
  return err instanceof Error ? err.message : "Something went wrong.";
}

export function BoardSection({ projectKey }: { projectKey: string }) {
  const columns = useColumnsQuery(projectKey);
  const update = useUpdateColumn(projectKey);
  const create = useCreateColumn(projectKey);
  const remove = useDeleteColumn(projectKey);

  const [error, setError] = useState<string | null>(null);
  const [confirmAutoStart, setConfirmAutoStart] = useState<Column | null>(null);
  const [deleting, setDeleting] = useState<Column | null>(null);

  if (columns.isPending) {
    return <section aria-label="Board" aria-busy="true" />;
  }
  if (columns.isError) {
    return (
      <section aria-label="Board">
        <p role="alert" className={styles.quiet}>
          Columns could not load: {columns.error.message}
        </p>
      </section>
    );
  }
  const list = columns.data.columns;

  const patch = (id: string, body: Parameters<typeof update.mutate>[0]["body"]) => {
    setError(null);
    update.mutate({ id, body }, { onError: (err) => setError(problemMessage(err)) });
  };

  /** Move the column at index i one slot up or down: anchor after its new predecessor. */
  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir;
    if (j < 0 || j >= list.length) return;
    // Moving up one slot: land after the column two above (or first, after_id null).
    // Moving down one slot: land after the column currently below.
    const anchor = dir === -1 ? (i >= 2 ? list[i - 2].id : null) : list[j].id;
    patch(list[i].id, { after_id: anchor });
  };

  return (
    <section aria-label="Board">
      <header className={styles.paneHeader}>
        <h1 className={styles.paneTitle}>Board</h1>
      </header>
      <p className={styles.sectionIntro}>
        Columns are yours to name; automation only ever reads the <em>category</em>. A project
        always keeps at least one backlog, running and done column.
      </p>
      {error && (
        <p role="alert" className={styles.boardError}>
          {error}
        </p>
      )}

      <ul className={styles.columnList}>
        {list.map((col, i) => (
          <li key={col.id} className={styles.columnRow}>
            <span className={styles.reorder}>
              <button
                aria-label={`Move ${col.name} up`}
                className={styles.iconButton}
                disabled={i === 0}
                onClick={() => move(i, -1)}
              >
                ↑
              </button>
              <button
                aria-label={`Move ${col.name} down`}
                className={styles.iconButton}
                disabled={i === list.length - 1}
                onClick={() => move(i, 1)}
              >
                ↓
              </button>
            </span>

            <input
              key={`${col.id}-${col.name}`}
              className={styles.columnName}
              defaultValue={col.name}
              aria-label={`Column name`}
              onBlur={(e) => {
                const name = e.target.value.trim();
                if (name && name !== col.name) patch(col.id, { name });
                else e.target.value = col.name;
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") e.currentTarget.blur();
                if (e.key === "Escape") {
                  e.currentTarget.value = col.name;
                  e.currentTarget.blur();
                }
              }}
            />

            <span className={styles.catBadge} data-cat={col.category}>
              {col.category}
            </span>
            <select
              aria-label={`Category of ${col.name}`}
              value={col.category}
              className={styles.catSelect}
              onChange={(e) =>
                patch(col.id, { category: e.target.value as ColumnCategory })
              }
            >
              {CATEGORIES.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>

            <label className={styles.wipField}>
              WIP
              <input
                key={`${col.id}-wip-${col.wip_limit ?? ""}`}
                type="number"
                min={1}
                placeholder="—"
                defaultValue={col.wip_limit ?? ""}
                aria-label={`WIP limit of ${col.name}`}
                onBlur={(e) => {
                  const v = e.target.value === "" ? null : e.target.valueAsNumber;
                  if (v !== null && (Number.isNaN(v) || v < 1)) {
                    e.target.value = col.wip_limit == null ? "" : String(col.wip_limit);
                    return;
                  }
                  if (v !== (col.wip_limit ?? null)) patch(col.id, { wip_limit: v });
                }}
                onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
              />
            </label>

            <label className={styles.autoStart}>
              <input
                type="checkbox"
                checked={col.auto_start_delegate}
                aria-label={`Auto-start delegate on ${col.name}`}
                onChange={(e) => {
                  if (e.target.checked) setConfirmAutoStart(col);
                  else patch(col.id, { auto_start_delegate: false });
                }}
              />
              ⚡ auto-start
            </label>

            <span className={styles.quiet}>
              {col.ticket_count === 1 ? "1 ticket" : `${col.ticket_count} tickets`}
            </span>

            <button
              className={styles.iconButton}
              aria-label={`Delete ${col.name}`}
              title="Delete column"
              onClick={() => {
                setError(null);
                setDeleting(col);
              }}
            >
              ✕
            </button>
          </li>
        ))}
      </ul>

      <AddColumnForm
        onAdd={(name, category) => {
          setError(null);
          create.mutate({ name, category }, { onError: (err) => setError(problemMessage(err)) });
        }}
      />

      {confirmAutoStart && (
        <ConfirmDialog
          title={`Enable auto-start on “${confirmAutoStart.name}”?`}
          body={AUTO_START_WARNING}
          confirmLabel="Enable auto-start"
          onCancel={() => setConfirmAutoStart(null)}
          onConfirm={() => {
            patch(confirmAutoStart.id, { auto_start_delegate: true });
            setConfirmAutoStart(null);
          }}
        />
      )}

      {deleting && (
        <DeleteColumnDialog
          column={deleting}
          others={list.filter((c) => c.id !== deleting.id)}
          onCancel={() => setDeleting(null)}
          onDelete={(destinationColumnId) => {
            remove.mutate(
              { id: deleting.id, destinationColumnId },
              { onError: (err) => setError(problemMessage(err)) },
            );
            setDeleting(null);
          }}
        />
      )}
    </section>
  );
}

function AddColumnForm({
  onAdd,
}: {
  onAdd: (name: string, category: ColumnCategory) => void;
}) {
  const [name, setName] = useState("");
  const [category, setCategory] = useState<ColumnCategory>("backlog");
  return (
    <form
      className={styles.addColumn}
      onSubmit={(e) => {
        e.preventDefault();
        if (!name.trim()) return;
        onAdd(name.trim(), category);
        setName("");
      }}
    >
      <input
        placeholder="New column name"
        aria-label="New column name"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
      <select
        aria-label="New column category"
        value={category}
        onChange={(e) => setCategory(e.target.value as ColumnCategory)}
      >
        {CATEGORIES.map((c) => (
          <option key={c} value={c}>
            {c}
          </option>
        ))}
      </select>
      <button type="submit" className={styles.secondaryButton} disabled={!name.trim()}>
        Add column
      </button>
    </form>
  );
}

function ConfirmDialog({
  title,
  body,
  confirmLabel,
  onCancel,
  onConfirm,
}: {
  title: string;
  body: string;
  confirmLabel: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <div className={styles.overlay} role="presentation" onClick={onCancel}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={styles.dialog}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.dialogTitle}>{title}</h2>
        <p className={styles.dialogBody}>{body}</p>
        <div className={styles.dialogActions}>
          <button className={styles.secondaryButton} onClick={onCancel}>
            Cancel
          </button>
          <button className={styles.primaryButton} onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

/**
 * Delete flow. A column with tickets needs somewhere for them to go, so the dialog carries a
 * destination picker; an empty column confirms plainly. The server enforces both the
 * destination requirement and the last-required-category guardrail either way.
 */
function DeleteColumnDialog({
  column,
  others,
  onCancel,
  onDelete,
}: {
  column: Column;
  others: Column[];
  onCancel: () => void;
  onDelete: (destinationColumnId?: string) => void;
}) {
  const needsDestination = column.ticket_count > 0;
  const [destination, setDestination] = useState(others[0]?.id ?? "");
  return (
    <div className={styles.overlay} role="presentation" onClick={onCancel}>
      <div
        role="dialog"
        aria-modal="true"
        aria-label={`Delete ${column.name}`}
        className={styles.dialog}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.dialogTitle}>Delete “{column.name}”?</h2>
        {needsDestination ? (
          <>
            <p className={styles.dialogBody}>
              This column holds {column.ticket_count === 1 ? "1 ticket" : `${column.ticket_count} tickets`}.
              Choose the column they move to:
            </p>
            <select
              aria-label="Destination column"
              className={styles.destinationSelect}
              value={destination}
              onChange={(e) => setDestination(e.target.value)}
            >
              {others.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name} ({c.category})
                </option>
              ))}
            </select>
          </>
        ) : (
          <p className={styles.dialogBody}>The column is empty; nothing else changes.</p>
        )}
        <div className={styles.dialogActions}>
          <button className={styles.secondaryButton} onClick={onCancel}>
            Cancel
          </button>
          <button
            className={styles.dangerButton}
            disabled={needsDestination && !destination}
            onClick={() => onDelete(needsDestination ? destination : undefined)}
          >
            Delete column
          </button>
        </div>
      </div>
    </div>
  );
}
