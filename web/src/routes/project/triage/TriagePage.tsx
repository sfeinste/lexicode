/*
 * The triage queue (UI spec §5.5): a single-column list of tickets created by triggers and
 * agents, keyboard-first. Every row shows what created it — the provenance line, verbatim
 * from the triage row, is the whole reason this screen exists. J/K move, Space peeks, Enter
 * opens the ticket; the verbs are `1` accept (the ticket appears in the board's default
 * backlog column), `2` mark duplicate (a ticket picker, then a merge), `3` decline (optional
 * reason, cancels), `H` snooze (1 day / 1 week / until new activity). Snoozed items render
 * below the pending list, muted, with their wake condition — and never count toward the tab
 * badge (actionable only).
 */
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import {
  ApiProblem,
  ticketsApi,
  type Ticket,
  type TriageItem,
} from "../../../lib/api/client";
import {
  useTriageAccept,
  useTriageDecline,
  useTriageDuplicate,
  useTriageQuery,
  useTriageSnooze,
} from "../../../lib/api/triageQueries";
import { formatRelativeTime } from "../../../lib/format/format";
import { useKeyBindings, useKeyScope } from "../../../lib/keyboard/hooks";
import { useStreamTopics } from "../../../lib/sse/useStreamTopics";
import { buildTriageBindings } from "./keymap";
import styles from "./triage.module.css";

const ROUTE_ID = "/shell/p/$key/triage";

type Overlay =
  | { kind: "none" }
  | { kind: "peek"; item: TriageItem }
  | { kind: "duplicate"; item: TriageItem }
  | { kind: "decline"; item: TriageItem }
  | { kind: "snooze"; item: TriageItem };

export function TriagePage() {
  const { key } = useParams({ from: ROUTE_ID });
  const navigate = useNavigate();
  useStreamTopics([`project:${key}`]);

  const queue = useTriageQuery(key);
  const accept = useTriageAccept(key);
  const duplicate = useTriageDuplicate(key);
  const decline = useTriageDecline(key);
  const snooze = useTriageSnooze(key);

  const items = useMemo(() => queue.data?.items ?? [], [queue.data]);
  const pending = useMemo(() => items.filter((i) => i.state === "pending"), [items]);
  const snoozed = useMemo(() => items.filter((i) => i.state === "snoozed"), [items]);

  // Selection is an item id over the flat pending→snoozed order; it survives refetches and
  // falls back to the first row when its item resolves away.
  const [selId, setSelId] = useState<string | undefined>(undefined);
  const [overlay, setOverlay] = useState<Overlay>({ kind: "none" });
  const [actionError, setActionError] = useState<string | null>(null);

  const flat = items; // the API orders pending first, then snoozed, each oldest first
  const selected = useMemo(
    () => flat.find((i) => i.id === selId) ?? flat[0],
    [flat, selId],
  );

  const onMutationError = useCallback((err: unknown) => {
    setActionError(
      err instanceof ApiProblem ? err.detail || err.title : "The change did not save.",
    );
  }, []);

  const openTicket = useCallback(
    (t: Ticket) => {
      void navigate({ to: "/p/$key/t/$ticket", params: { key, ticket: String(t.seq) } });
    },
    [navigate, key],
  );

  // Latest-state ref so the key bindings register once and still see current data.
  const stateRef = useRef({ flat, selected });
  stateRef.current = { flat, selected };

  const runAccept = useCallback(
    (item: TriageItem) => accept.mutate(item.id, { onError: onMutationError }),
    [accept, onMutationError],
  );

  useKeyScope("route", true);
  useKeyBindings(
    () =>
      buildTriageBindings({
        moveSelection: (delta) => {
          const { flat: f, selected: sel } = stateRef.current;
          if (f.length === 0) return;
          const i = sel ? f.findIndex((it) => it.id === sel.id) : -1;
          const next = f[Math.min(f.length - 1, Math.max(0, i + delta))];
          setSelId(next.id);
        },
        peek: () => {
          const sel = stateRef.current.selected;
          if (sel) setOverlay({ kind: "peek", item: sel });
        },
        openSelected: () => {
          const sel = stateRef.current.selected;
          if (sel) openTicket(sel.ticket);
        },
        clearSelection: () => setSelId(undefined),
        accept: () => {
          const sel = stateRef.current.selected;
          if (sel) runAccept(sel);
        },
        duplicate: () => {
          const sel = stateRef.current.selected;
          if (sel) setOverlay({ kind: "duplicate", item: sel });
        },
        decline: () => {
          const sel = stateRef.current.selected;
          if (sel) setOverlay({ kind: "decline", item: sel });
        },
        snooze: () => {
          const sel = stateRef.current.selected;
          if (sel) setOverlay({ kind: "snooze", item: sel });
        },
        hasSelection: () => stateRef.current.selected !== undefined,
      }),
    [openTicket, runAccept],
  );

  const close = useCallback(() => setOverlay({ kind: "none" }), []);

  if (queue.isSuccess && items.length === 0) {
    return (
      <div className={styles.page}>
        <EmptyState
          headline="Nothing to triage"
          body="Tickets created by triggers and agents land here first."
        />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <div className={styles.pageHead}>
        <h1 className={styles.pageTitle}>Triage</h1>
        <span className={styles.pageCount}>
          {pending.length} pending
          {snoozed.length > 0 ? ` · ${snoozed.length} snoozed` : ""}
        </span>
        <span className={styles.hint}>1 accept · 2 duplicate · 3 decline · H snooze</span>
      </div>

      {actionError !== null && (
        <p role="alert" className={styles.error}>
          {actionError}{" "}
          <button type="button" onClick={() => setActionError(null)}>
            Dismiss
          </button>
        </p>
      )}

      <ul className={styles.list} aria-label="Pending triage">
        {pending.map((item) => (
          <TriageRow
            key={item.id}
            item={item}
            selected={selected?.id === item.id}
            onSelect={() => setSelId(item.id)}
            onOpen={() => openTicket(item.ticket)}
            onAccept={() => runAccept(item)}
            onDuplicate={() => setOverlay({ kind: "duplicate", item })}
            onDecline={() => setOverlay({ kind: "decline", item })}
            onSnooze={() => setOverlay({ kind: "snooze", item })}
          />
        ))}
      </ul>

      {snoozed.length > 0 && (
        <>
          <h2 className={styles.sectionHead}>Snoozed</h2>
          <ul className={styles.list} aria-label="Snoozed triage">
            {snoozed.map((item) => (
              <TriageRow
                key={item.id}
                item={item}
                muted
                selected={selected?.id === item.id}
                onSelect={() => setSelId(item.id)}
                onOpen={() => openTicket(item.ticket)}
                onAccept={() => runAccept(item)}
                onDuplicate={() => setOverlay({ kind: "duplicate", item })}
                onDecline={() => setOverlay({ kind: "decline", item })}
                onSnooze={() => setOverlay({ kind: "snooze", item })}
              />
            ))}
          </ul>
        </>
      )}

      {overlay.kind === "peek" && <PeekPanel item={overlay.item} onClose={close} onOpen={() => {
        close();
        openTicket(overlay.item.ticket);
      }} />}

      {overlay.kind === "duplicate" && (
        <DuplicatePicker
          projectKey={key}
          item={overlay.item}
          onClose={close}
          onPick={(ofTicketId) => {
            close();
            duplicate.mutate({ id: overlay.item.id, ofTicketId }, { onError: onMutationError });
          }}
        />
      )}

      {overlay.kind === "decline" && (
        <DeclineDialog
          item={overlay.item}
          onClose={close}
          onDecline={(reason) => {
            close();
            decline.mutate({ id: overlay.item.id, reason }, { onError: onMutationError });
          }}
        />
      )}

      {overlay.kind === "snooze" && (
        <SnoozeMenu
          item={overlay.item}
          onClose={close}
          onSnooze={(until) => {
            close();
            snooze.mutate({ id: overlay.item.id, until }, { onError: onMutationError });
          }}
        />
      )}
    </div>
  );
}

/** One queue row: title, the verbatim provenance line, age — and the wake condition when
 * snoozed. */
function TriageRow({
  item,
  muted,
  selected,
  onSelect,
  onOpen,
  onAccept,
  onDuplicate,
  onDecline,
  onSnooze,
}: {
  item: TriageItem;
  muted?: boolean;
  selected: boolean;
  onSelect: () => void;
  onOpen: () => void;
  onAccept: () => void;
  onDuplicate: () => void;
  onDecline: () => void;
  onSnooze: () => void;
}) {
  return (
    <li
      className={muted ? styles.snoozedRow : styles.row}
      data-selected={selected ? "" : undefined}
      onClick={onSelect}
      onDoubleClick={onOpen}
      aria-selected={selected}
    >
      <div className={styles.rowHead}>
        <span className={styles.rowKey}>{item.ticket.key}</span>
        <span className={styles.rowTitle}>{item.ticket.title}</span>
        <span className={styles.rowAge}>{formatRelativeTime(item.created_at)}</span>
      </div>
      <p className={styles.provenance}>{item.provenance}</p>
      {item.state === "snoozed" && (
        <p className={styles.wake}>
          {item.snooze_until === null
            ? "Wakes on new activity"
            : `Wakes ${new Date(item.snooze_until).toLocaleString()}`}
        </p>
      )}
      {selected && (
        <div className={styles.rowActions}>
          <button type="button" onClick={onAccept}>
            <kbd>1</kbd>Accept
          </button>
          <button type="button" onClick={onDuplicate}>
            <kbd>2</kbd>Duplicate
          </button>
          <button type="button" onClick={onDecline}>
            <kbd>3</kbd>Decline
          </button>
          <button type="button" onClick={onSnooze}>
            <kbd>H</kbd>Snooze
          </button>
        </div>
      )}
    </li>
  );
}

/** Shared overlay chrome: backdrop click and Escape close; keys never leak to the route
 * bindings. */
function OverlayShell({
  label,
  onClose,
  children,
}: {
  label: string;
  onClose: () => void;
  children: React.ReactNode;
}) {
  useKeyScope("modal", true);
  return (
    <div className={styles.backdrop} onClick={onClose}>
      <div
        role="dialog"
        aria-label={label}
        className={styles.overlay}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "Escape") onClose();
        }}
      >
        {children}
      </div>
    </div>
  );
}

/** `Space` — peek without opening (§6). */
function PeekPanel({
  item,
  onClose,
  onOpen,
}: {
  item: TriageItem;
  onClose: () => void;
  onOpen: () => void;
}) {
  const t = item.ticket;
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => ref.current?.focus(), []);
  useKeyScope("modal", true);
  return (
    <div className={styles.backdrop} onClick={onClose}>
      <div
        ref={ref}
        role="dialog"
        aria-label={`Peek: ${t.key}`}
        tabIndex={-1}
        className={styles.overlay}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "Escape" || e.key === " ") onClose();
          if (e.key === "Enter") onOpen();
        }}
      >
        <div className={styles.rowHead}>
          <span className={styles.rowKey}>{t.key}</span>
          <span className={styles.rowAge}>{formatRelativeTime(item.created_at)}</span>
        </div>
        <h2 className={styles.overlayTitle}>{t.title}</h2>
        <p className={styles.provenance}>{item.provenance}</p>
        {t.description !== "" && <p className={styles.peekDescription}>{t.description}</p>}
        <div className={styles.overlayActions}>
          <button type="button" onClick={onOpen}>
            Open ticket ↵
          </button>
        </div>
      </div>
    </div>
  );
}

/** `2` — the duplicate-of picker: search the project's tickets, pick the survivor. */
function DuplicatePicker({
  projectKey,
  item,
  onClose,
  onPick,
}: {
  projectKey: string;
  item: TriageItem;
  onClose: () => void;
  onPick: (ofTicketId: string) => void;
}) {
  const [q, setQ] = useState("");
  const [active, setActive] = useState(0);
  const tickets = useQuery({
    queryKey: ["triage", projectKey, "duplicate-candidates"],
    queryFn: ({ signal }) => ticketsApi.list(projectKey, undefined, signal),
  });
  const candidates = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (tickets.data?.tickets ?? [])
      .filter((t) => t.id !== item.ticket_id)
      .filter(
        (t) =>
          needle === "" ||
          t.title.toLowerCase().includes(needle) ||
          t.key.toLowerCase().includes(needle),
      )
      .slice(0, 30);
  }, [tickets.data, q, item.ticket_id]);
  const clamped = Math.min(active, Math.max(0, candidates.length - 1));

  return (
    <OverlayShell label={`Mark ${item.ticket.key} as a duplicate`} onClose={onClose}>
      <h2 className={styles.overlayTitle}>
        {item.ticket.key} duplicates…
      </h2>
      <input
        autoFocus
        className={styles.overlayInput}
        placeholder="Search tickets by key or title"
        value={q}
        onChange={(e) => {
          setQ(e.target.value);
          setActive(0);
        }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setActive((i) => Math.min(candidates.length - 1, i + 1));
          }
          if (e.key === "ArrowUp") {
            e.preventDefault();
            setActive((i) => Math.max(0, i - 1));
          }
          if (e.key === "Enter" && candidates[clamped] !== undefined) {
            e.preventDefault();
            onPick(candidates[clamped].id);
          }
        }}
      />
      {candidates.length === 0 ? (
        <p className={styles.pickerEmpty}>
          {tickets.isPending ? "Loading tickets…" : "No matching tickets."}
        </p>
      ) : (
        <ul className={styles.pickerList} role="listbox" aria-label="Duplicate of">
          {candidates.map((t, i) => (
            <li key={t.id} data-active={i === clamped ? "" : undefined}>
              <button type="button" onClick={() => onPick(t.id)}>
                <span className={styles.rowKey}>{t.key}</span>
                <span>{t.title}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </OverlayShell>
  );
}

/** `3` — decline with an optional reason. */
function DeclineDialog({
  item,
  onClose,
  onDecline,
}: {
  item: TriageItem;
  onClose: () => void;
  onDecline: (reason: string) => void;
}) {
  const [reason, setReason] = useState("");
  return (
    <OverlayShell label={`Decline ${item.ticket.key}`} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          onDecline(reason.trim());
        }}
      >
        <h2 className={styles.overlayTitle}>Decline {item.ticket.key}</h2>
        <input
          autoFocus
          className={styles.overlayInput}
          placeholder="Reason (optional)"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
        />
        <div className={styles.overlayActions}>
          <button type="submit">Decline</button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </OverlayShell>
  );
}

/** `H` — the snooze menu: 1 day / 1 week / until new activity (§5.5). */
function SnoozeMenu({
  item,
  onClose,
  onSnooze,
}: {
  item: TriageItem;
  onClose: () => void;
  onSnooze: (until: string | null) => void;
}) {
  const inDays = (n: number) => new Date(Date.now() + n * 24 * 60 * 60 * 1000).toISOString();
  return (
    <OverlayShell label={`Snooze ${item.ticket.key}`} onClose={onClose}>
      <h2 className={styles.overlayTitle}>Snooze {item.ticket.key}</h2>
      <ul className={styles.menuList}>
        <li>
          <button type="button" autoFocus onClick={() => onSnooze(inDays(1))}>
            1 day
          </button>
        </li>
        <li>
          <button type="button" onClick={() => onSnooze(inDays(7))}>
            1 week
          </button>
        </li>
        <li>
          <button type="button" onClick={() => onSnooze(null)}>
            Until new activity
          </button>
        </li>
      </ul>
    </OverlayShell>
  );
}
