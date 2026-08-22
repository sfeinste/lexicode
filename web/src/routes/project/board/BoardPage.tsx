/*
 * The board (S11, UI spec §5.3): ONE view, two layouts. `⌘B` flips board ⇄ list; `group_by`
 * picks the property; the board is that grouping rendered horizontally — groups come from
 * grouping.ts for both layouts, never computed twice. All selection state — layout,
 * grouping, filters, search, keyboard selection, display properties — lives in the URL
 * (interaction rule 12), so a filtered view is shareable by copying the address bar.
 *
 * Drag and drop is hand-rolled on the native HTML5 drag events (draggable / dragover /
 * drop) — no dnd library: the interaction is "move a card between flat vertical lists",
 * which native DnD covers in ~60 lines, keeps the bundle flat, and leaves the browser to
 * do the cross-window ergonomics. Dropping writes the grouped property (a move for
 * status, a PATCH for priority/assignee/delegate, attach+detach for labels) with an
 * optimistic cache update and rollback (boardData.ts). Drag NEVER starts a run (rule 2).
 *
 * Live updates: the page subscribes to the project SSE topic; ticket.* / label.* /
 * board.updated frames funnel through applyEvent's single reducer and invalidate the
 * ["board", key] queries, so a move in one tab appears in a second tab without a reload.
 *
 * Known S11 seams, closed by later stories:
 * - The delegate picker (D) lists the delegate-eligible agents (S16); the "No agents yet"
 *   empty state renders only while the project truly has none.
 * - The assign picker (A) offers Me / Unassigned — there is no user-list endpoint yet, so
 *   other members cannot be picked (and assignee groups render shortened ids).
 * - The needs-you lane query 404s into an empty lane until S21/S22 implement the runs view.
 */
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { Link } from "@tanstack/react-router";

import type { BoardSearch } from "../../../app/router";
import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { useAgentsQuery } from "../../../lib/api/agentQueries";
import { useBootstrapPreviewQuery, useRepoStatusQuery } from "../../../lib/api/repoQueries";
import { EMPTY_STATES, boardImportLabel } from "../../emptyStates";
import {
  ApiProblem,
  authApi,
  type Agent,
  type Label,
  type Ticket,
  type TicketPriority,
} from "../../../lib/api/client";
import { useColumnsQuery } from "../../../lib/api/columnQueries";
import { useKeyBindings, useKeyScope } from "../../../lib/keyboard/hooks";
import { useStreamTopics } from "../../../lib/sse/useStreamTopics";
import { NeedsYouLane } from "./NeedsYouLane";
import { TicketCard, type DisplayProps, type NeedsYouFlavor } from "./TicketCard";
import styles from "./board.module.css";
import {
  useBoardLabels,
  useBoardTickets,
  useCreateTicket,
  useMoveTicket,
  useNeedsYouRuns,
  usePatchTicket,
  useSetTicketLabel,
} from "./boardData";
import {
  NONE_GROUP,
  PRIORITY_LABELS,
  PRIORITY_ORDER,
  dropPosition,
  filterTickets,
  flattenGroups,
  groupTickets,
  type BoardGroup,
  type GroupBy,
} from "./grouping";
import { buildBoardBindings, type BoardPickerKind } from "./keymap";
import { wipDisplay } from "./wip";

const ROUTE_ID = "/shell/p/$key/board" as const;

// ---- ⇧V display properties --------------------------------------------------------------

const DISPLAY_KEYS = ["priority", "labels", "criteria", "people", "pr"] as const;
type DisplayKey = (typeof DISPLAY_KEYS)[number];

const DISPLAY_TITLES: Record<DisplayKey, string> = {
  priority: "Priority",
  labels: "Labels",
  criteria: "Acceptance criteria",
  people: "Delegate / assignee",
  pr: "Pull request",
};

/** `?show=` absent means everything on; otherwise it is the comma list of enabled props. */
function parseShow(show: string | undefined): DisplayProps {
  if (show === undefined) {
    return { priority: true, labels: true, criteria: true, people: true, pr: true };
  }
  const on = new Set(show.split(","));
  return {
    priority: on.has("priority"),
    labels: on.has("labels"),
    criteria: on.has("criteria"),
    people: on.has("people"),
    pr: on.has("pr"),
  };
}

function serializeShow(props: DisplayProps): string | undefined {
  const on = DISPLAY_KEYS.filter((k) => props[k]);
  return on.length === DISPLAY_KEYS.length ? undefined : on.join(",");
}

// ---- drag state -------------------------------------------------------------------------

interface DragState {
  ticketId: string;
  fromGroupId: string;
}

interface DropTarget {
  groupId: string;
  index: number;
}

// ---- overlays ---------------------------------------------------------------------------

type Overlay =
  | { kind: "none" }
  | { kind: "create" }
  | { kind: "rename"; ticket: Ticket }
  | { kind: "peek"; ticket: Ticket }
  | { kind: "display" }
  | { kind: "picker"; picker: BoardPickerKind; targets: Ticket[] };

export function BoardPage() {
  const { key } = useParams({ from: ROUTE_ID });
  // Agents (S16): the D picker offers the delegate-eligible subset (enabled, non-archived);
  // card badges resolve any delegate id — including a since-disabled one — to its name.
  const agentsQuery = useAgentsQuery(key);
  const eligibleAgents = useMemo(
    () =>
      (agentsQuery.data?.agents ?? []).filter((a) => a.enabled && a.archived_at === null),
    [agentsQuery.data],
  );
  const agentNamesById = useMemo(() => {
    const m = new Map<string, string>();
    for (const a of agentsQuery.data?.agents ?? []) m.set(a.id, a.name);
    return m;
  }, [agentsQuery.data]);
  const search = useSearch({ from: ROUTE_ID });
  const navigate = useNavigate();

  // Live board: one SSE subscription to the project topic (architecture §13).
  useStreamTopics([`project:${key}`]);

  const columnsQuery = useColumnsQuery(key);
  const ticketsQuery = useBoardTickets(key);
  const labelsQuery = useBoardLabels(key);
  const needsYouQuery = useNeedsYouRuns(key);
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: ({ signal }) => authApi.me(signal),
    staleTime: 5 * 60_000,
  });

  const move = useMoveTicket(key);
  const patch = usePatchTicket(key);
  const setLabel = useSetTicketLabel(key);
  const create = useCreateTicket(key);

  const groupBy: GroupBy = search.group_by ?? "status";
  const layout: "board" | "list" = search.layout ?? "board";
  const show = useMemo(() => parseShow(search.show), [search.show]);

  const [multi, setMulti] = useState<ReadonlySet<string>>(new Set());
  const [overlay, setOverlay] = useState<Overlay>({ kind: "none" });
  const [drag, setDrag] = useState<DragState | null>(null);
  const [drop, setDrop] = useState<DropTarget | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const patchSearch = useCallback(
    (patchObj: Partial<BoardSearch>, replace = true) => {
      void navigate({
        to: "/p/$key/board",
        params: { key },
        // prev arrives as the router-wide search union (S23 added a runs-list `view` of a
        // different shape); this navigate targets the board route, so the cast is honest.
        search: (prev) => ({ ...(prev as BoardSearch), ...patchObj }),
        replace,
      });
    },
    [navigate, key],
  );

  const columns = useMemo(() => columnsQuery.data?.columns ?? [], [columnsQuery.data]);
  const labels = useMemo(() => labelsQuery.data?.labels ?? [], [labelsQuery.data]);
  const allTickets = useMemo(() => ticketsQuery.data?.tickets ?? [], [ticketsQuery.data]);

  const labelsById = useMemo(() => {
    const m = new Map<string, Label>();
    for (const l of labels) m.set(l.id, l);
    return m;
  }, [labels]);

  // No user-list API yet: the only human name available client-side is the session's own.
  const nameFor = useCallback(
    (id: string): string => {
      if (me.data && id === me.data.id) return me.data.display_name;
      return id.length > 8 ? `${id.slice(0, 8)}…` : id;
    },
    [me.data],
  );

  // `?view=backlog` (§1): the same view filtered to backlog-category columns.
  const backlogColumnIds = useMemo(
    () => new Set(columns.filter((c) => c.category === "backlog").map((c) => c.id)),
    [columns],
  );
  const scopedTickets = useMemo(
    () =>
      search.view === "backlog"
        ? allTickets.filter((t) => backlogColumnIds.has(t.column_id))
        : allTickets,
    [allTickets, backlogColumnIds, search.view],
  );

  const filtered = useMemo(
    () =>
      filterTickets(scopedTickets, {
        assignee: search.assignee,
        delegate: search.delegate,
        label: search.label,
        priority: search.priority,
        q: search.q,
      }),
    [scopedTickets, search.assignee, search.delegate, search.label, search.priority, search.q],
  );

  const groups = useMemo(() => {
    const scopedColumns =
      search.view === "backlog" ? columns.filter((c) => c.category === "backlog") : columns;
    const gs = groupTickets(filtered, groupBy, { columns: scopedColumns, labels });
    // Assignees are humans: the only display name available client-side is the session's
    // own (no user-list API yet). Delegate groups keep the agent id until S16 lands names.
    if (groupBy === "assignee") {
      for (const g of gs) if (g.id !== NONE_GROUP) g.name = nameFor(g.id);
    }
    return gs;
  }, [filtered, groupBy, columns, labels, search.view, nameFor]);

  const flat = useMemo(() => flattenGroups(groups), [groups]);
  const selected = useMemo(
    () => flat.find((t) => t.key === search.sel),
    [flat, search.sel],
  );

  // The ▲ corner badge earns itself from the needs-you query (populated from S22).
  const needsYouByTicket = useMemo(() => {
    const m = new Map<string, NeedsYouFlavor>();
    for (const r of needsYouQuery.data ?? []) {
      if (r.ticket_id !== null) m.set(r.ticket_id, r.flavor);
    }
    return m;
  }, [needsYouQuery.data]);

  // ---- actions the keyboard map and the mouse share -----------------------------------

  const onMutationError = useCallback((err: unknown) => {
    setActionError(
      err instanceof ApiProblem ? err.detail || err.title : "The change did not save.",
    );
  }, []);

  const selectKey = useCallback(
    (ticketKey: string | undefined) => patchSearch({ sel: ticketKey }),
    [patchSearch],
  );

  const openTicket = useCallback(
    (t: Ticket) => {
      void navigate({
        to: "/p/$key/t/$ticket",
        params: { key, ticket: String(t.seq) },
      });
    },
    [navigate, key],
  );

  /** The ticket(s) a mutation chord applies to: the X multi-selection, else the cursor. */
  const targets = useCallback((): Ticket[] => {
    if (multi.size > 0) return flat.filter((t) => multi.has(t.id));
    return selected ? [selected] : [];
  }, [multi, flat, selected]);

  // Latest-state ref so the key bindings register once and still see current data.
  const stateRef = useRef({ flat, selected, targets, layout, show });
  stateRef.current = { flat, selected, targets, layout, show };

  useKeyScope("route", true);
  useKeyBindings(
    () =>
      buildBoardBindings({
        createTicket: () => setOverlay({ kind: "create" }),
        moveSelection: (delta) => {
          const { flat: f, selected: sel } = stateRef.current;
          if (f.length === 0) return;
          const i = sel ? f.findIndex((t) => t.id === sel.id) : -1;
          const next = f[Math.min(f.length - 1, Math.max(0, i + delta))];
          selectKey(next.key);
        },
        peek: () => {
          const sel = stateRef.current.selected;
          if (sel) setOverlay({ kind: "peek", ticket: sel });
        },
        openSelected: () => {
          const sel = stateRef.current.selected;
          if (sel) openTicket(sel);
        },
        toggleMultiSelect: () => {
          const sel = stateRef.current.selected;
          if (!sel) return;
          setMulti((prev) => {
            const next = new Set(prev);
            if (next.has(sel.id)) next.delete(sel.id);
            else next.add(sel.id);
            return next;
          });
        },
        clearSelection: () => {
          setMulti(new Set());
          selectKey(undefined);
        },
        openPicker: (picker) => {
          const ts = stateRef.current.targets();
          if (ts.length > 0) setOverlay({ kind: "picker", picker, targets: ts });
        },
        // E edits the ticket body — that editor is the S12 detail screen; until it lands,
        // E and Enter both open the ticket route.
        edit: () => {
          const sel = stateRef.current.selected;
          if (sel) openTicket(sel);
        },
        rename: () => {
          const sel = stateRef.current.selected;
          if (sel) setOverlay({ kind: "rename", ticket: sel });
        },
        toggleLayout: () =>
          patchSearch({ layout: stateRef.current.layout === "board" ? "list" : undefined }),
        toggleDisplayMenu: () =>
          setOverlay((o) => (o.kind === "display" ? { kind: "none" } : { kind: "display" })),
        hasSelection: () => stateRef.current.selected !== undefined,
      }),
    [selectKey, openTicket, patchSearch],
  );

  // ---- drag and drop ------------------------------------------------------------------

  const ticketById = useCallback(
    (id: string) => allTickets.find((t) => t.id === id),
    [allTickets],
  );

  const handleDrop = useCallback(
    (groupId: string, index: number) => {
      setDrop(null);
      const d = drag;
      setDrag(null);
      if (d === null) return;
      const t = ticketById(d.ticketId);
      if (t === undefined) return;
      const err = { onError: onMutationError };
      switch (groupBy) {
        case "status": {
          const group = groups.find((g) => g.id === groupId);
          if (group === undefined) return;
          const rest = group.tickets.filter((x) => x.id !== t.id);
          // Index was computed over the rendered list (dragged card included); shift it
          // down when the card sat above the drop point in the same column.
          let i = index;
          if (group.id === d.fromGroupId) {
            const oldIndex = group.tickets.findIndex((x) => x.id === t.id);
            if (oldIndex !== -1 && oldIndex < index) i -= 1;
          }
          const position = dropPosition(rest, i);
          if (t.column_id === groupId && position === t.position) return;
          move.mutate({ id: t.id, body: { column_id: groupId, position } }, err);
          return;
        }
        case "priority":
          if (t.priority !== groupId) {
            patch.mutate({ id: t.id, body: { priority: groupId as TicketPriority } }, err);
          }
          return;
        case "assignee": {
          const next = groupId === NONE_GROUP ? null : groupId;
          if (t.assignee_id !== next) {
            patch.mutate({ id: t.id, body: { assignee_id: next } }, err);
          }
          return;
        }
        case "delegate": {
          const next = groupId === NONE_GROUP ? null : groupId;
          if (t.delegate_agent_id !== next) {
            patch.mutate({ id: t.id, body: { delegate_agent_id: next } }, err);
          }
          return;
        }
        case "label": {
          if (groupId === d.fromGroupId) return;
          const attach = groupId === NONE_GROUP ? undefined : groupId;
          const detach = d.fromGroupId === NONE_GROUP ? undefined : d.fromGroupId;
          setLabel.mutate({ id: t.id, attach, detach }, err);
          return;
        }
      }
    },
    [drag, ticketById, groupBy, groups, move, patch, setLabel, onMutationError],
  );

  // ---- header handlers ----------------------------------------------------------------

  const chips: Array<{ id: string; text: string; clear: Partial<BoardSearch> }> = [];
  if (search.assignee !== undefined) {
    chips.push({
      id: "assignee",
      text: `Assignee: ${search.assignee === NONE_GROUP ? "Unassigned" : nameFor(search.assignee)}`,
      clear: { assignee: undefined },
    });
  }
  if (search.delegate !== undefined) {
    chips.push({
      id: "delegate",
      text: `Delegate: ${search.delegate === NONE_GROUP ? "None" : search.delegate}`,
      clear: { delegate: undefined },
    });
  }
  if (search.label !== undefined) {
    chips.push({
      id: "label",
      text: `Label: ${labelsById.get(search.label)?.name ?? search.label}`,
      clear: { label: undefined },
    });
  }
  if (search.priority !== undefined) {
    chips.push({
      id: "priority",
      text: `Priority: ${PRIORITY_LABELS[search.priority]}`,
      clear: { priority: undefined },
    });
  }

  const assigneeOptions = useMemo(() => {
    const ids = new Set<string>();
    for (const t of allTickets) if (t.assignee_id !== null) ids.add(t.assignee_id);
    if (me.data) ids.add(me.data.id);
    return [...ids].sort();
  }, [allTickets, me.data]);

  const delegateOptions = useMemo(() => {
    const ids = new Set<string>();
    for (const t of allTickets) if (t.delegate_agent_id !== null) ids.add(t.delegate_agent_id);
    return [...ids].sort();
  }, [allTickets]);

  if (ticketsQuery.isPending || columnsQuery.isPending || labelsQuery.isPending) {
    return <div className={styles.root} aria-busy="true" />;
  }
  if (ticketsQuery.isError || columnsQuery.isError || labelsQuery.isError) {
    const err = ticketsQuery.error ?? columnsQuery.error ?? labelsQuery.error;
    return (
      <div className={styles.root}>
        <p role="alert" className={styles.error}>
          The board could not load: {err instanceof Error ? err.message : "unknown error"}
        </p>
      </div>
    );
  }

  const cardProps = (t: Ticket, variant: "card" | "row") => ({
    ticket: t,
    variant,
    labelsById,
    agentNamesById,
    show,
    selected: t.key === search.sel,
    multiSelected: multi.has(t.id),
    needsYou: needsYouByTicket.get(t.id),
    onClick: () => selectKey(t.key),
    onDoubleClick: () => openTicket(t),
    draggable: true,
    onDragStart: (e: React.DragEvent) => {
      e.dataTransfer.setData("text/plain", t.id);
      e.dataTransfer.effectAllowed = "move";
      // The group the drag started from (label grouping needs it to know what to detach).
      const from = groups.find((g) => g.tickets.some((x) => x.id === t.id));
      setDrag({ ticketId: t.id, fromGroupId: from?.id ?? "" });
    },
    onDragEnd: () => {
      setDrag(null);
      setDrop(null);
    },
  });

  const renderGroupBody = (g: BoardGroup, variant: "card" | "row") => (
    <div
      className={variant === "card" ? styles.columnBody : styles.sectionBody}
      data-drop-active={drag !== null && drop?.groupId === g.id ? "" : undefined}
      onDragOver={(e) => {
        if (drag === null) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "move";
        // Compute the insertion index from the card midpoints under the pointer.
        const cards = [...e.currentTarget.querySelectorAll("[data-ticket-key]")];
        let index = cards.length;
        for (let i = 0; i < cards.length; i++) {
          const r = cards[i].getBoundingClientRect();
          if (e.clientY < r.top + r.height / 2) {
            index = i;
            break;
          }
        }
        setDrop((prev) =>
          prev?.groupId === g.id && prev.index === index ? prev : { groupId: g.id, index },
        );
      }}
      onDrop={(e) => {
        e.preventDefault();
        handleDrop(g.id, drop?.groupId === g.id ? drop.index : g.tickets.length);
      }}
    >
      {g.tickets.map((t, i) => (
        <div key={`${t.id}:${i}`} className={styles.cardSlot}>
          {drop?.groupId === g.id && drop.index === i && drag !== null && (
            <div className={styles.dropLine} />
          )}
          <TicketCard {...cardProps(t, variant)} />
        </div>
      ))}
      {drop?.groupId === g.id && drop.index >= g.tickets.length && drag !== null && (
        <div className={styles.dropLine} />
      )}
    </div>
  );

  const groupHeader = (g: BoardGroup) => {
    const wip = g.column ? wipDisplay(g.tickets.length, g.column.wip_limit) : null;
    return (
      <header className={styles.columnHeader}>
        <span className={styles.columnName}>{g.name}</span>
        <span className={styles.columnCount}>{g.tickets.length}</span>
        {wip !== null && (
          <span className={styles.wip} data-wip={wip.level}>
            {wip.label}
          </span>
        )}
        {g.column?.auto_start_delegate && (
          <span className={styles.autoStart} title="Tickets moved here auto-start their delegate">
            ⚡ auto-runs delegate
          </span>
        )}
      </header>
    );
  };

  return (
    <div className={styles.root}>
      <div className={styles.header}>
        <div className={styles.headerRow}>
          <div className={styles.layoutToggle} role="group" aria-label="Layout (⌘B)">
            <button
              type="button"
              data-active={layout === "board" || undefined}
              onClick={() => patchSearch({ layout: undefined })}
            >
              Board
            </button>
            <button
              type="button"
              data-active={layout === "list" || undefined}
              onClick={() => patchSearch({ layout: "list" })}
            >
              List
            </button>
          </div>

          <label className={styles.groupBy}>
            group_by
            <select
              value={groupBy}
              onChange={(e) =>
                patchSearch({
                  group_by:
                    e.target.value === "status" ? undefined : (e.target.value as GroupBy),
                })
              }
            >
              <option value="status">status</option>
              <option value="assignee">assignee</option>
              <option value="delegate">delegate</option>
              <option value="priority">priority</option>
              <option value="label">label</option>
            </select>
          </label>

          <div className={styles.filters}>
            <select
              aria-label="Filter by assignee"
              value={search.assignee ?? ""}
              onChange={(e) => patchSearch({ assignee: e.target.value || undefined })}
            >
              <option value="">Assignee</option>
              <option value={NONE_GROUP}>Unassigned</option>
              {assigneeOptions.map((id) => (
                <option key={id} value={id}>
                  {nameFor(id)}
                </option>
              ))}
            </select>
            <select
              aria-label="Filter by delegate"
              value={search.delegate ?? ""}
              onChange={(e) => patchSearch({ delegate: e.target.value || undefined })}
            >
              <option value="">Delegate</option>
              <option value={NONE_GROUP}>No delegate</option>
              {delegateOptions.map((id) => (
                <option key={id} value={id}>
                  {id}
                </option>
              ))}
            </select>
            <select
              aria-label="Filter by label"
              value={search.label ?? ""}
              onChange={(e) => patchSearch({ label: e.target.value || undefined })}
            >
              <option value="">Label</option>
              <option value={NONE_GROUP}>No label</option>
              {labels.map((l) => (
                <option key={l.id} value={l.id}>
                  {l.name}
                </option>
              ))}
            </select>
            <select
              aria-label="Filter by priority"
              value={search.priority ?? ""}
              onChange={(e) =>
                patchSearch({
                  priority: (e.target.value || undefined) as BoardSearch["priority"],
                })
              }
            >
              <option value="">Priority</option>
              {PRIORITY_ORDER.map((p) => (
                <option key={p} value={p}>
                  {PRIORITY_LABELS[p]}
                </option>
              ))}
            </select>
          </div>

          <input
            type="search"
            className={styles.search}
            placeholder="Search"
            aria-label="Search tickets"
            value={search.q ?? ""}
            onChange={(e) => patchSearch({ q: e.target.value || undefined })}
          />

          <button
            type="button"
            className={styles.displayButton}
            aria-haspopup="menu"
            onClick={() =>
              setOverlay((o) => (o.kind === "display" ? { kind: "none" } : { kind: "display" }))
            }
          >
            Display ⇧V
          </button>

          <button
            type="button"
            className={styles.newTicket}
            onClick={() => setOverlay({ kind: "create" })}
          >
            + New ticket
          </button>
        </div>

        {chips.length > 0 && (
          <div className={styles.chips}>
            {chips.map((c) => (
              <button
                key={c.id}
                type="button"
                className={styles.chip}
                onClick={() => patchSearch(c.clear)}
              >
                {c.text} ✕
              </button>
            ))}
          </div>
        )}

        {actionError !== null && (
          <p role="alert" className={styles.error}>
            {actionError}{" "}
            <button type="button" onClick={() => setActionError(null)}>
              Dismiss
            </button>
          </p>
        )}
      </div>

      <NeedsYouLane projectKey={key} runs={needsYouQuery.data ?? []} />

      {allTickets.length === 0 ? (
        <BoardEmpty projectKey={key} onNewTicket={() => setOverlay({ kind: "create" })} />
      ) : layout === "board" ? (
        <div className={styles.board}>
          {groups.map((g) => (
            <section key={g.id} className={styles.column} aria-label={g.name}>
              {groupHeader(g)}
              {renderGroupBody(g, "card")}
            </section>
          ))}
        </div>
      ) : (
        <div className={styles.list}>
          {groups.map((g) => (
            <section key={g.id} className={styles.section} aria-label={g.name}>
              {groupHeader(g)}
              {renderGroupBody(g, "row")}
            </section>
          ))}
        </div>
      )}

      {overlay.kind === "create" && (
        <CreateTicketDialog
          onClose={() => setOverlay({ kind: "none" })}
          onCreate={(title) => {
            create.mutate(
              { title },
              {
                onSuccess: (t) => {
                  setOverlay({ kind: "none" });
                  selectKey(t.key);
                },
                onError: onMutationError,
              },
            );
          }}
        />
      )}

      {overlay.kind === "rename" && (
        <RenameDialog
          ticket={overlay.ticket}
          onClose={() => setOverlay({ kind: "none" })}
          onRename={(title) => {
            patch.mutate(
              { id: overlay.ticket.id, body: { title } },
              { onError: onMutationError },
            );
            setOverlay({ kind: "none" });
          }}
        />
      )}

      {overlay.kind === "peek" && (
        <PeekPanel
          ticket={overlay.ticket}
          labelsById={labelsById}
          columnName={columns.find((c) => c.id === overlay.ticket.column_id)?.name ?? ""}
          onClose={() => setOverlay({ kind: "none" })}
          onOpen={() => {
            setOverlay({ kind: "none" });
            openTicket(overlay.ticket);
          }}
        />
      )}

      {overlay.kind === "display" && (
        <DisplayMenu
          show={show}
          onToggle={(k) => {
            const next = { ...show, [k]: !show[k] };
            patchSearch({ show: serializeShow(next) });
          }}
          onClose={() => setOverlay({ kind: "none" })}
        />
      )}

      {overlay.kind === "picker" && (
        <BoardPicker
          picker={overlay.picker}
          targets={overlay.targets}
          columns={columns}
          labels={labels}
          agents={eligibleAgents}
          meId={me.data?.id}
          meName={me.data?.display_name}
          onClose={() => setOverlay({ kind: "none" })}
          onApply={(apply) => {
            setOverlay({ kind: "none" });
            for (const t of overlay.targets) {
              apply(t, {
                move: (body) => move.mutate({ id: t.id, body }, { onError: onMutationError }),
                patch: (body) => patch.mutate({ id: t.id, body }, { onError: onMutationError }),
                label: (attach, detach) =>
                  setLabel.mutate({ id: t.id, attach, detach }, { onError: onMutationError }),
              });
            }
          }}
        />
      )}
    </div>
  );
}

// ---- overlays ---------------------------------------------------------------------------

/** Shared overlay chrome: backdrop click and Escape close; keys never leak to the board
 * bindings (stopPropagation before the window listener sees them). */
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

function CreateTicketDialog({
  onClose,
  onCreate,
}: {
  onClose: () => void;
  onCreate: (title: string) => void;
}) {
  const [title, setTitle] = useState("");
  return (
    <OverlayShell label="New ticket" onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (title.trim() !== "") onCreate(title.trim());
        }}
      >
        <h2 className={styles.overlayTitle}>New ticket</h2>
        <input
          autoFocus
          className={styles.overlayInput}
          placeholder="Ticket title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
        <div className={styles.overlayActions}>
          <button type="submit" disabled={title.trim() === ""}>
            Create
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </OverlayShell>
  );
}

function RenameDialog({
  ticket,
  onClose,
  onRename,
}: {
  ticket: Ticket;
  onClose: () => void;
  onRename: (title: string) => void;
}) {
  const [title, setTitle] = useState(ticket.title);
  return (
    <OverlayShell label={`Rename ${ticket.key}`} onClose={onClose}>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (title.trim() !== "") onRename(title.trim());
        }}
      >
        <h2 className={styles.overlayTitle}>Rename {ticket.key}</h2>
        <input
          autoFocus
          className={styles.overlayInput}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
        <div className={styles.overlayActions}>
          <button type="submit" disabled={title.trim() === ""}>
            Rename
          </button>
          <button type="button" onClick={onClose}>
            Cancel
          </button>
        </div>
      </form>
    </OverlayShell>
  );
}

/** `Space` — peek without opening (§6). */
function PeekPanel({
  ticket: t,
  labelsById,
  columnName,
  onClose,
  onOpen,
}: {
  ticket: Ticket;
  labelsById: Map<string, Label>;
  columnName: string;
  onClose: () => void;
  onOpen: () => void;
}) {
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
        <div className={styles.peekHead}>
          <span className={styles.cardKey}>{t.key}</span>
          <span className={styles.peekColumn}>{columnName}</span>
        </div>
        <h2 className={styles.overlayTitle}>{t.title}</h2>
        {t.description !== "" && <p className={styles.peekDescription}>{t.description}</p>}
        {t.criteria_total > 0 && (
          <p className={styles.cardCriteria}>
            ▸ {t.criteria_checked}/{t.criteria_total} acceptance criteria
          </p>
        )}
        {t.label_ids.length > 0 && (
          <p className={styles.cardFooter}>
            {t.label_ids.map((id) => {
              const l = labelsById.get(id);
              return l === undefined ? null : (
                <span key={id} className={styles.cardLabel}>
                  <span className={styles.labelSwatch} style={{ background: l.color }} />
                  {l.name}
                </span>
              );
            })}
          </p>
        )}
        <div className={styles.overlayActions}>
          <button type="button" onClick={onOpen}>
            Open ticket ↵
          </button>
        </div>
      </div>
    </div>
  );
}

/** `⇧V` — the display-properties menu. */
function DisplayMenu({
  show,
  onToggle,
  onClose,
}: {
  show: DisplayProps;
  onToggle: (k: DisplayKey) => void;
  onClose: () => void;
}) {
  return (
    <OverlayShell label="Display properties" onClose={onClose}>
      <h2 className={styles.overlayTitle}>Display properties</h2>
      {DISPLAY_KEYS.map((k) => (
        <label key={k} className={styles.displayRow}>
          <input type="checkbox" checked={show[k]} onChange={() => onToggle(k)} />
          {DISPLAY_TITLES[k]}
        </label>
      ))}
      <div className={styles.overlayActions}>
        <button type="button" onClick={onClose}>
          Done
        </button>
      </div>
    </OverlayShell>
  );
}

// ---- the S/P/A/D/L pickers --------------------------------------------------------------

interface PickerMutators {
  move: (body: { column_id: string; after_ticket_id?: string | null }) => void;
  patch: (body: {
    priority?: TicketPriority;
    assignee_id?: string | null;
    delegate_agent_id?: string | null;
  }) => void;
  label: (attach?: string, detach?: string) => void;
}

function BoardPicker({
  picker,
  targets,
  columns,
  labels,
  agents,
  meId,
  meName,
  onClose,
  onApply,
}: {
  picker: BoardPickerKind;
  targets: Ticket[];
  columns: Array<{ id: string; name: string; category: string }>;
  labels: Label[];
  agents: Agent[];
  meId: string | undefined;
  meName: string | undefined;
  onClose: () => void;
  onApply: (apply: (t: Ticket, m: PickerMutators) => void) => void;
}) {
  const one = targets.length === 1 ? targets[0] : undefined;

  let title: string;
  let options: Array<{ id: string; text: string; active?: boolean }>;
  let empty: string | null = null;
  let apply: (id: string) => (t: Ticket, m: PickerMutators) => void;

  switch (picker) {
    case "status":
      title = "Status";
      // Moving via S appends to the destination column (server-side placement).
      options = columns.map((c) => ({
        id: c.id,
        text: c.name,
        active: one?.column_id === c.id,
      }));
      apply = (id) => (t, m) => {
        if (t.column_id !== id) m.move({ column_id: id });
      };
      break;
    case "priority":
      title = "Priority";
      options = PRIORITY_ORDER.map((p) => ({
        id: p,
        text: PRIORITY_LABELS[p],
        active: one?.priority === p,
      }));
      apply = (id) => (t, m) => {
        if (t.priority !== id) m.patch({ priority: id as TicketPriority });
      };
      break;
    case "assign":
      title = "Assign (human)";
      options = [
        ...(meId !== undefined ? [{ id: meId, text: meName ?? "Me" }] : []),
        { id: NONE_GROUP, text: "Unassigned" },
      ];
      apply = (id) => (_t, m) => m.patch({ assignee_id: id === NONE_GROUP ? null : id });
      break;
    case "delegate":
      title = "Delegate (agent)";
      // Enabled, non-archived agents only (S16); the §8 empty state until any exist.
      options = [
        ...agents.map((a) => ({
          id: a.id,
          text: a.name,
          active: one?.delegate_agent_id === a.id,
        })),
        ...(agents.length > 0 ? [{ id: NONE_GROUP, text: "No delegate" }] : []),
      ];
      empty = agents.length === 0 ? "No agents yet" : null;
      apply = (id) => (t, m) => {
        const next = id === NONE_GROUP ? null : id;
        if (t.delegate_agent_id !== next) m.patch({ delegate_agent_id: next });
      };
      break;
    case "label":
      title = "Labels";
      options = labels.map((l) => ({
        id: l.id,
        text: l.name,
        active: one?.label_ids.includes(l.id),
      }));
      empty = options.length === 0 ? "No labels in this project yet" : null;
      apply = (id) => (t, m) => {
        if (t.label_ids.includes(id)) m.label(undefined, id);
        else m.label(id, undefined);
      };
      break;
  }

  const [cursor, setCursor] = useState(0);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => ref.current?.focus(), []);
  useKeyScope("modal", true);

  const pick = (id: string) => onApply(apply(id));

  return (
    <div className={styles.backdrop} onClick={onClose}>
      <div
        ref={ref}
        role="dialog"
        aria-label={title}
        tabIndex={-1}
        className={styles.overlay}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "Escape") onClose();
          if (e.key === "ArrowDown") setCursor((c) => Math.min(options.length - 1, c + 1));
          if (e.key === "ArrowUp") setCursor((c) => Math.max(0, c - 1));
          if (e.key === "Enter" && options[cursor] !== undefined) pick(options[cursor].id);
        }}
      >
        <h2 className={styles.overlayTitle}>
          {title}
          {targets.length > 1 ? ` — ${targets.length} tickets` : one ? ` — ${one.key}` : ""}
        </h2>
        {empty !== null ? (
          <p className={styles.pickerEmpty}>{empty}</p>
        ) : (
          <ul className={styles.pickerList} role="listbox">
            {options.map((o, i) => (
              <li key={o.id}>
                <button
                  type="button"
                  role="option"
                  aria-selected={o.active ?? false}
                  data-cursor={i === cursor || undefined}
                  className={styles.pickerOption}
                  onClick={() => pick(o.id)}
                >
                  {o.active === true ? "✓ " : ""}
                  {o.text}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

/**
 * The §8 board empty state, with the detected-content variant: while the board is empty
 * and a repo is connected, the bootstrap scan runs and the primary CTA becomes
 * *"Import 12 open issues"*, leading to the S15 bootstrap checklist (import with a preview
 * and checkboxes, never silently). Without a repo — or when the scan finds nothing new —
 * the one working CTA, "New ticket", is promoted to primary.
 */
function BoardEmpty({
  projectKey,
  onNewTicket,
}: {
  projectKey: string;
  onNewTicket: () => void;
}) {
  const repo = useRepoStatusQuery(projectKey);
  const connected = repo.data?.connected === true;
  const preview = useBootstrapPreviewQuery(projectKey, connected);
  const openIssues = (preview.data?.issues ?? []).filter((i) => !i.already_imported).length;

  const newTicket = (
    <button type="button" className={styles.newTicket} onClick={onNewTicket}>
      {EMPTY_STATES.board.secondary}
    </button>
  );
  return (
    <EmptyState
      headline={EMPTY_STATES.board.headline}
      body={EMPTY_STATES.board.body}
      primary={
        openIssues > 0 ? (
          <Link to="/p/$key/bootstrap" params={{ key: projectKey }} className={styles.newTicket}>
            {boardImportLabel(openIssues)}
          </Link>
        ) : (
          newTicket
        )
      }
      secondary={openIssues > 0 ? newTicket : undefined}
    />
  );
}
