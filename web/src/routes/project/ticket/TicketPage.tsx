/*
 * Ticket detail (S12, UI spec §5.4). Main column in spec order: title (inline, `R`),
 * description (the shared Editor), first-class acceptance criteria, sub-tickets, the
 * UNIFIED stream — comments and system rows interleaved chronologically, one feed, no
 * Comments/Activity tabs anywhere — and the composer at the bottom. Properties sidebar
 * (280px) toggles with ⌘I; assignee (human) and delegate (agent) render as visibly
 * distinct rows with different iconography, never one polymorphic field (D1).
 *
 * Selection in the description + ⌘⇧O opens the sub-ticket preview: N non-empty selected
 * lines → N titles shown before anything is created; confirming calls POST subtickets.
 *
 * Known S12 seams, closed by later stories: the wiki mention source renders its empty
 * state until the wiki API exists (the delegate picker and agent mentions are live as of
 * S16); run cards appear in the stream when S23 writes kind='run' entries; linked PR and
 * branch populate with the forge (S14+) and runs (S23+).
 */
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useCallback, useMemo, useRef, useState } from "react";

import type { EditorHandle } from "../../../components/Editor/Editor";
import { MarkdownView } from "../../../components/Editor/MarkdownView";
import type { MentionSources } from "../../../components/Editor/engine";
import { collapseToSingleLine } from "../../../components/Editor/engine";
import { useEligibleAgents } from "../../../lib/api/agentQueries";
import {
  ApiProblem,
  type Agent,
  type Column,
  type CommentRunRequest,
  type Criterion,
  type Label,
  type Member,
  type TicketDetail,
  type TicketPriority,
  type TicketStreamEntry,
} from "../../../lib/api/client";
import { useAutosave } from "../../../lib/autosave";
import { formatRelativeTime } from "../../../lib/format/format";
import { useKeyBindings, useKeyScope } from "../../../lib/keyboard/hooks";
import { useStreamTopics } from "../../../lib/sse/useStreamTopics";
import { Composer } from "./Composer";
import { DescriptionSection } from "./DescriptionSection";
import { selectionToTitles } from "./subticketSelection";
import { systemLine } from "./streamFormat";
import styles from "./ticket.module.css";
import {
  useAddCriterion,
  useColumnsQuery,
  useCreateSubtickets,
  useDeleteCriterion,
  useMembers,
  useMoveTicket,
  usePatchTicket,
  usePostComment,
  useProjectLabels,
  useSetLabel,
  useTicketDetail,
  useTicketId,
  useTicketList,
  useTicketStream,
  useUpdateCriterion,
} from "./ticketData";

const ROUTE_ID = "/shell/p/$key/t/$ticket" as const;

const PRIORITIES: TicketPriority[] = ["none", "low", "medium", "high", "urgent"];

export function TicketPage() {
  const { key, ticket: num } = useParams({ from: ROUTE_ID });
  useStreamTopics([`project:${key}`]);

  const resolved = useTicketId(key, num);
  const detail = useTicketDetail(key, resolved.id);
  const stream = useTicketStream(key, resolved.id);

  if (resolved.isPending || (resolved.id !== undefined && detail.isPending)) {
    return <div className={styles.root} aria-busy="true" />;
  }
  if (resolved.isError || detail.isError) {
    const err = resolved.error ?? detail.error;
    return (
      <p role="alert" className={styles.error}>
        The ticket could not load: {err instanceof Error ? err.message : "unknown error"}
      </p>
    );
  }
  if (resolved.id === undefined || detail.data === undefined) {
    return (
      <p role="alert" className={styles.error}>
        No ticket {key}-{num} in this project.
      </p>
    );
  }
  return (
    // Keyed by ticket id: the description draft and dialogs reset when navigating between
    // tickets (e.g. into a sub-ticket) rather than leaking across.
    <LoadedTicket
      key={resolved.id}
      projectKey={key}
      detail={detail.data}
      stream={stream.data?.entries ?? []}
    />
  );
}

function LoadedTicket({
  projectKey,
  detail,
  stream,
}: {
  projectKey: string;
  detail: TicketDetail;
  stream: TicketStreamEntry[];
}) {
  const navigate = useNavigate();
  const columnsQuery = useColumnsQuery(projectKey);
  const labelsQuery = useProjectLabels(projectKey);
  const membersQuery = useMembers();
  const listQuery = useTicketList(projectKey);

  const patch = usePatchTicket(projectKey);
  const move = useMoveTicket(projectKey);
  const comment = usePostComment(projectKey);
  const addCriterion = useAddCriterion(projectKey);
  const updateCriterion = useUpdateCriterion(projectKey);
  const deleteCriterion = useDeleteCriterion(projectKey);
  const createSubtickets = useCreateSubtickets(projectKey);
  const setLabel = useSetLabel(projectKey);

  const columns = columnsQuery.data?.columns ?? [];
  const labels = labelsQuery.data?.labels ?? [];
  const members = useMemo(() => membersQuery.data?.users ?? [], [membersQuery.data]);
  // Delegate-eligible agents (S16): enabled, non-archived — the sidebar picker and the
  // mention autocomplete's agent source.
  const { agents: eligibleAgents } = useEligibleAgents(projectKey);

  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [editingTitle, setEditingTitle] = useState(false);
  const [subticketDraft, setSubticketDraft] = useState<string | null>(null);
  const [runNotices, setRunNotices] = useState<CommentRunRequest[]>([]);
  const [actionError, setActionError] = useState<string | null>(null);

  const editorRef = useRef<EditorHandle>(null);
  const titleRef = useRef<HTMLInputElement>(null);

  // Description autosaves (the §5.11 contract — no Save button anywhere).
  const [description, setDescription] = useState(detail.description);
  const autosave = useAutosave<{ description: string }>(
    useCallback(
      (p) => patch.mutateAsync({ id: detail.id, body: p }),
      [patch, detail.id],
    ),
  );

  const onMutationError = useCallback((err: unknown) => {
    setActionError(
      err instanceof ApiProblem ? err.detail || err.title : "The change did not save.",
    );
  }, []);

  // One mention source set for every Editor on the page. Wiki pages stay empty until the
  // wiki API exists — the menu renders their honest empty state rather than pretending.
  const mentions: MentionSources = useMemo(
    () => ({
      users: members.map((m: Member) => ({ kind: "user" as const, id: m.id, label: m.display_name })),
      agents: eligibleAgents.map((a) => ({ kind: "agent" as const, id: a.id, label: a.name })),
      wiki: [],
      tickets: (listQuery.data?.tickets ?? [])
        .filter((t) => t.id !== detail.id && t.archived_at === null)
        .map((t) => ({ kind: "ticket" as const, id: t.id, label: t.key, hint: t.title })),
    }),
    [members, eligibleAgents, listQuery.data, detail.id],
  );

  const membersById = useMemo(() => {
    const m = new Map<string, Member>();
    for (const u of members) m.set(u.id, u);
    return m;
  }, [members]);

  const openSubticketDialog = useCallback(() => {
    setSubticketDraft(editorRef.current?.getSelectedText() ?? "");
  }, []);

  // ---- keyboard (§6): R rename · ⌘I sidebar · ⌘⇧O sub-tickets · E edit description ------
  const stateRef = useRef({ editingTitle });
  stateRef.current = { editingTitle };
  useKeyScope("route", true);
  useKeyBindings(
    () => [
      {
        id: "ticket.rename",
        scope: "route",
        chord: "r",
        title: "Rename ticket",
        group: "Ticket",
        palette: true,
        run: () => setEditingTitle(true),
      },
      {
        id: "ticket.edit",
        scope: "route",
        chord: "e",
        title: "Edit description",
        group: "Ticket",
        run: () => editorRef.current?.focus(),
      },
      {
        id: "ticket.sidebar",
        scope: "route",
        chord: "mod+i",
        title: "Properties sidebar",
        group: "Ticket",
        palette: true,
        run: () => setSidebarOpen((o) => !o),
      },
      {
        id: "ticket.subtickets",
        scope: "route",
        chord: "mod+shift+o",
        title: "New sub-ticket / convert selection",
        group: "Ticket",
        palette: true,
        run: openSubticketDialog,
      },
      {
        id: "ticket.back",
        scope: "route",
        chord: "escape",
        title: "Back to board",
        group: "Ticket",
        run: () => {
          // Escape first leaves an editing surface; only from the page itself does it
          // navigate back.
          const el = document.activeElement;
          if (
            el instanceof HTMLElement &&
            (el.tagName === "TEXTAREA" || el.tagName === "INPUT" || el.tagName === "SELECT")
          ) {
            el.blur();
            return;
          }
          if (stateRef.current.editingTitle) {
            setEditingTitle(false);
            return;
          }
          void navigate({ to: "/p/$key/board", params: { key: projectKey } });
        },
      },
    ],
    [projectKey, navigate, openSubticketDialog],
  );

  const parent = useMemo(
    () =>
      detail.parent_id === null
        ? undefined
        : listQuery.data?.tickets.find((t) => t.id === detail.parent_id),
    [detail.parent_id, listQuery.data],
  );

  const column = columns.find((c) => c.id === detail.column_id);

  const saveTitle = (title: string) => {
    setEditingTitle(false);
    const clean = title.trim();
    if (clean !== "" && clean !== detail.title) {
      patch.mutate({ id: detail.id, body: { title: clean } }, { onError: onMutationError });
    }
  };

  const postComment = (body: string) =>
    comment.mutateAsync({ id: detail.id, body: { body } }).then((res) => {
      setRunNotices(res.run_requests);
    });

  return (
    <div className={styles.root}>
      <div className={styles.main}>
        <div className={styles.head}>
          <span className={styles.key}>{detail.key}</span>
          <span className={styles.categoryChip}>{column?.name ?? detail.category}</span>
          <span className={styles.headSpacer} />
          <button
            type="button"
            className={styles.iconButton}
            aria-pressed={sidebarOpen}
            onClick={() => setSidebarOpen((o) => !o)}
          >
            Sidebar ⌘I
          </button>
        </div>

        {editingTitle ? (
          <input
            ref={titleRef}
            className={styles.titleInput}
            aria-label="Ticket title"
            defaultValue={detail.title}
            autoFocus
            onFocus={(e) => e.target.select()}
            onBlur={(e) => saveTitle(e.target.value)}
            onPaste={(e) => {
              // A multi-line paste into the title stays a single line.
              e.preventDefault();
              const el = e.currentTarget;
              const text = collapseToSingleLine(e.clipboardData.getData("text/plain"));
              const next =
                el.value.slice(0, el.selectionStart ?? 0) +
                text +
                el.value.slice(el.selectionEnd ?? 0);
              el.value = next;
            }}
            onKeyDown={(e) => {
              e.stopPropagation();
              if (e.key === "Enter") saveTitle(e.currentTarget.value);
              if (e.key === "Escape") setEditingTitle(false);
            }}
          />
        ) : (
          <h1
            className={styles.title}
            title="Rename (R)"
            onClick={() => setEditingTitle(true)}
          >
            {detail.title}
          </h1>
        )}

        {parent !== undefined && (
          <p className={styles.parentLine}>
            Sub-ticket of{" "}
            <Link
              to="/p/$key/t/$ticket"
              params={{ key: projectKey, ticket: String(parent.seq) }}
            >
              {parent.key} — {parent.title}
            </Link>
          </p>
        )}

        {actionError !== null && (
          <p role="alert" className={styles.error}>
            {actionError}{" "}
            <button type="button" onClick={() => setActionError(null)}>
              Dismiss
            </button>
          </p>
        )}

        <DescriptionSection
          value={description}
          onChange={(v) => {
            setDescription(v);
            autosave.queue({ description: v });
          }}
          onBlur={autosave.flush}
          mentions={mentions}
          editorRef={editorRef}
          status={
            autosave.status === "saving"
              ? "Saving…"
              : autosave.status === "saved"
                ? "Saved"
                : autosave.status === "error"
                  ? (autosave.error ?? "Save failed")
                  : undefined
          }
        />

        <CriteriaSection
          criteria={detail.criteria}
          onAdd={(text) =>
            addCriterion.mutate({ id: detail.id, text }, { onError: onMutationError })
          }
          onToggle={(c) =>
            updateCriterion.mutate(
              { id: c.id, body: { checked: !c.checked } },
              { onError: onMutationError },
            )
          }
          onEdit={(c, text) =>
            updateCriterion.mutate({ id: c.id, body: { text } }, { onError: onMutationError })
          }
          onDelete={(c) => deleteCriterion.mutate(c.id, { onError: onMutationError })}
          onReorder={(c, afterId) =>
            updateCriterion.mutate(
              { id: c.id, body: { after_id: afterId } },
              { onError: onMutationError },
            )
          }
        />

        <SubticketsBlock
          projectKey={projectKey}
          detail={detail}
          onNew={openSubticketDialog}
        />

        <StreamSection
          entries={stream}
          columns={columns}
          membersById={membersById}
        />

        <Composer mentions={mentions} onPost={postComment} runNotices={runNotices} />
      </div>

      {sidebarOpen && (
        <Sidebar
          detail={detail}
          columns={columns}
          labels={labels}
          members={members}
          onStatus={(columnId) =>
            move.mutate({ id: detail.id, columnId }, { onError: onMutationError })
          }
          onPriority={(priority) =>
            patch.mutate({ id: detail.id, body: { priority } }, { onError: onMutationError })
          }
          onAssignee={(userId) =>
            patch.mutate(
              { id: detail.id, body: { assignee_id: userId } },
              { onError: onMutationError },
            )
          }
          agents={eligibleAgents}
          onDelegate={(agentId) =>
            patch.mutate(
              { id: detail.id, body: { delegate_agent_id: agentId } },
              { onError: onMutationError },
            )
          }
          onLabel={(labelId, attach) =>
            setLabel.mutate(
              attach
                ? { id: detail.id, attach: labelId }
                : { id: detail.id, detach: labelId },
              { onError: onMutationError },
            )
          }
        />
      )}

      {subticketDraft !== null && (
        <SubticketPreviewDialog
          initial={subticketDraft}
          onClose={() => setSubticketDraft(null)}
          onCreate={(titles) => {
            createSubtickets.mutate(
              { id: detail.id, titles },
              {
                onSuccess: () => setSubticketDraft(null),
                onError: (err) => {
                  setSubticketDraft(null);
                  onMutationError(err);
                },
              },
            );
          }}
        />
      )}
    </div>
  );
}

// ---- acceptance criteria (first-class, not part of the description) ---------------------

function CriteriaSection({
  criteria,
  onAdd,
  onToggle,
  onEdit,
  onDelete,
  onReorder,
}: {
  criteria: Criterion[];
  onAdd: (text: string) => void;
  onToggle: (c: Criterion) => void;
  onEdit: (c: Criterion, text: string) => void;
  onDelete: (c: Criterion) => void;
  onReorder: (c: Criterion, afterId: string | null) => void;
}) {
  const [draft, setDraft] = useState("");
  const [editingId, setEditingId] = useState<string | null>(null);
  const checked = criteria.filter((c) => c.checked).length;

  const add = () => {
    if (draft.trim() === "") return;
    onAdd(draft.trim());
    setDraft("");
  };

  return (
    <section className={styles.section} aria-label="Acceptance criteria">
      <div className={styles.sectionHead}>
        <h2 className={styles.sectionTitle}>Acceptance criteria</h2>
        {criteria.length > 0 && (
          <span className={styles.sectionMeta}>
            {checked}/{criteria.length}
          </span>
        )}
      </div>
      <ul className={styles.criteria}>
        {criteria.map((c, i) => (
          <li key={c.id} className={styles.criterion}>
            <input
              type="checkbox"
              checked={c.checked}
              aria-label={`Criterion: ${c.text}`}
              onChange={() => onToggle(c)}
            />
            {editingId === c.id ? (
              <input
                className={styles.criterionEdit}
                defaultValue={c.text}
                autoFocus
                onBlur={(e) => {
                  setEditingId(null);
                  const t = e.target.value.trim();
                  if (t !== "" && t !== c.text) onEdit(c, t);
                }}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === "Enter") e.currentTarget.blur();
                  if (e.key === "Escape") setEditingId(null);
                }}
              />
            ) : (
              <button
                type="button"
                className={styles.criterionText}
                data-checked={c.checked || undefined}
                onClick={() => setEditingId(c.id)}
              >
                {c.text}
              </button>
            )}
            <span className={styles.criterionTools}>
              <button
                type="button"
                className={styles.criterionTool}
                aria-label="Move up"
                disabled={i === 0}
                onClick={() => onReorder(c, i <= 1 ? null : criteria[i - 2].id)}
              >
                ↑
              </button>
              <button
                type="button"
                className={styles.criterionTool}
                aria-label="Move down"
                disabled={i === criteria.length - 1}
                onClick={() => onReorder(c, criteria[i + 1]?.id ?? null)}
              >
                ↓
              </button>
              <button
                type="button"
                className={styles.criterionTool}
                aria-label="Delete criterion"
                onClick={() => onDelete(c)}
              >
                ✕
              </button>
            </span>
          </li>
        ))}
      </ul>
      <div className={styles.criterionAdd}>
        <input
          className={styles.criterionAddInput}
          placeholder="Add a criterion — Enter to save"
          aria-label="Add a criterion"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            e.stopPropagation();
            if (e.key === "Enter") add();
          }}
        />
      </div>
    </section>
  );
}

// ---- sub-tickets (one level) ------------------------------------------------------------

function SubticketsBlock({
  projectKey,
  detail,
  onNew,
}: {
  projectKey: string;
  detail: TicketDetail;
  onNew: () => void;
}) {
  // A sub-ticket can never have children (one level, data model §10.1) — no block on one.
  if (detail.parent_id !== null) return null;
  return (
    <section className={styles.section} aria-label="Sub-tickets">
      <div className={styles.sectionHead}>
        <h2 className={styles.sectionTitle}>Sub-tickets</h2>
        {detail.children.length > 0 && (
          <span className={styles.sectionMeta}>{detail.children.length}</span>
        )}
      </div>
      {detail.children.length > 0 && (
        <ul className={styles.subtickets}>
          {detail.children.map((c) => (
            <li key={c.id}>
              <Link
                to="/p/$key/t/$ticket"
                params={{ key: projectKey, ticket: String(c.seq) }}
                className={styles.subticketRow}
              >
                <span className={styles.subticketKey}>{c.key}</span>
                <span className={styles.subticketTitle}>{c.title}</span>
                <span className={styles.subticketCategory}>{c.category}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      <button type="button" className={styles.smallButton} onClick={onNew}>
        + Sub-ticket ⌘⇧O
      </button>
    </section>
  );
}

// ---- the unified stream (no tabs — the §5.4 hill) ---------------------------------------

function StreamSection({
  entries,
  columns,
  membersById,
}: {
  entries: TicketStreamEntry[];
  columns: Column[];
  membersById: Map<string, Member>;
}) {
  const columnName = (id: string) => columns.find((c) => c.id === id)?.name ?? "…";
  const userName = (id: string) => membersById.get(id)?.display_name ?? "someone";
  const actorName = (e: TicketStreamEntry): string => {
    switch (e.actor_kind) {
      case "human":
        return e.actor_id === null ? "someone" : userName(e.actor_id);
      case "agent":
        return "agent";
      case "trigger":
        return "trigger";
      default:
        return "system";
    }
  };

  return (
    <section className={styles.stream} aria-label="Activity and comments">
      {entries.map((e) => {
        if (e.kind === "comment") {
          const author = actorName(e);
          const color =
            e.actor_id !== null ? membersById.get(e.actor_id)?.avatar_color : undefined;
          return (
            <article key={e.id} className={styles.commentCard} aria-label={`Comment by ${author}`}>
              <div className={styles.commentHead}>
                <span
                  aria-hidden="true"
                  className={styles.commentAvatar}
                  style={{ background: color ?? "var(--muted)" }}
                />
                <span className={styles.commentAuthor}>{author}</span>
                <span className={styles.streamTime}>{formatRelativeTime(e.created_at)}</span>
              </div>
              <MarkdownView markdown={e.body} />
            </article>
          );
        }
        if (e.kind === "run") {
          // S23 writes these rows and renders RunSessionCard from live run data. Until
          // then the row cannot exist; a defensive compact line keeps an early row visible.
          return (
            <div key={e.id} className={styles.systemRow}>
              <span aria-hidden="true" className={styles.systemGlyph}>
                ●
              </span>
              <span className={styles.systemText}>agent run</span>
              <span className={styles.streamTime}>{formatRelativeTime(e.created_at)}</span>
            </div>
          );
        }
        return (
          <div key={e.id} className={styles.systemRow}>
            <span aria-hidden="true" className={styles.systemGlyph}>
              ·
            </span>
            <span className={styles.systemActor}>{actorName(e)}</span>
            <span className={styles.systemText}>
              {systemLine(e, { columnName, userName, labelName: () => "" })}
            </span>
            <span className={styles.streamTime}>{formatRelativeTime(e.created_at)}</span>
          </div>
        );
      })}
    </section>
  );
}

// ---- sidebar (⌘I) -----------------------------------------------------------------------

function Sidebar({
  detail,
  columns,
  labels,
  members,
  agents,
  onStatus,
  onPriority,
  onAssignee,
  onDelegate,
  onLabel,
}: {
  detail: TicketDetail;
  columns: Column[];
  labels: Label[];
  members: Member[];
  agents: Agent[];
  onStatus: (columnId: string) => void;
  onPriority: (p: TicketPriority) => void;
  onAssignee: (userId: string | null) => void;
  onDelegate: (agentId: string | null) => void;
  onLabel: (labelId: string, attach: boolean) => void;
}) {
  const copyBranch = () => {
    if (detail.branch !== null) {
      void navigator.clipboard.writeText(
        `git fetch origin && git checkout ${detail.branch}`,
      );
    }
  };

  return (
    <aside className={styles.sidebar} aria-label="Properties">
      <div className={styles.sideRow}>
        <span className={styles.sideLabel}>Status</span>
        <select
          className={styles.sideSelect}
          aria-label="Status"
          value={detail.column_id}
          onChange={(e) => onStatus(e.target.value)}
        >
          {columns.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </div>

      <div className={styles.sideRow}>
        <span className={styles.sideLabel}>Priority</span>
        <select
          className={styles.sideSelect}
          aria-label="Priority"
          value={detail.priority}
          onChange={(e) => onPriority(e.target.value as TicketPriority)}
        >
          {PRIORITIES.map((p) => (
            <option key={p} value={p}>
              {p === "none" ? "No priority" : p[0].toUpperCase() + p.slice(1)}
            </option>
          ))}
        </select>
      </div>

      {/* Assignee (human) and delegate (agent): two rows, two icons, two colors (D1). */}
      <div className={`${styles.sideRow} ${styles.personRow}`} data-kind="human">
        <span className={styles.sideLabel}>
          <span aria-hidden="true" className={styles.personIcon}>
            ◉
          </span>
          Assignee
          <span className={styles.kindTag}>human</span>
        </span>
        <select
          className={styles.sideSelect}
          aria-label="Assignee (human)"
          value={detail.assignee_id ?? ""}
          onChange={(e) => onAssignee(e.target.value === "" ? null : e.target.value)}
        >
          <option value="">Unassigned</option>
          {members.map((m) => (
            <option key={m.id} value={m.id}>
              {m.display_name}
            </option>
          ))}
        </select>
      </div>

      <div className={`${styles.sideRow} ${styles.personRow}`} data-kind="agent">
        <span className={styles.sideLabel}>
          <span aria-hidden="true" className={styles.personIcon}>
            ▣
          </span>
          Delegate
          <span className={styles.kindTag}>agent</span>
        </span>
        {agents.length === 0 && detail.delegate_agent_id === null ? (
          <span className={styles.sideEmpty}>No agents yet</span>
        ) : (
          <select
            className={styles.sideSelect}
            aria-label="Delegate (agent)"
            value={detail.delegate_agent_id ?? ""}
            onChange={(e) => onDelegate(e.target.value === "" ? null : e.target.value)}
          >
            <option value="">No delegate</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
            {/* A disabled/archived delegate stays visible (history intact), just not pickable
                fresh. */}
            {detail.delegate_agent_id !== null &&
              !agents.some((a) => a.id === detail.delegate_agent_id) && (
                <option value={detail.delegate_agent_id}>(disabled agent)</option>
              )}
          </select>
        )}
      </div>

      <div className={styles.sideRow}>
        <span className={styles.sideLabel}>Labels</span>
        {labels.length === 0 ? (
          <span className={styles.sideEmpty}>No labels in this project yet</span>
        ) : (
          <div className={styles.labelList}>
            {labels.map((l) => (
              <label key={l.id} className={styles.labelRow}>
                <input
                  type="checkbox"
                  checked={detail.label_ids.includes(l.id)}
                  onChange={(e) => onLabel(l.id, e.target.checked)}
                />
                <span className={styles.labelSwatch} style={{ background: l.color }} />
                {l.name}
              </label>
            ))}
          </div>
        )}
      </div>

      <div className={styles.sideRow}>
        <span className={styles.sideLabel}>Linked PR</span>
        {detail.pr_number === null ? (
          <span className={styles.sideEmpty}>None — appears when a run opens one</span>
        ) : (
          <span className={styles.branchLine}>
            #{detail.pr_number} {detail.pr_state ?? ""}
          </span>
        )}
      </div>

      <div className={styles.sideRow}>
        <span className={styles.sideLabel}>Branch</span>
        {detail.branch === null ? (
          <span className={styles.sideEmpty}>None — assigned when a run pushes</span>
        ) : (
          <span className={styles.branchLine}>
            {detail.branch}
            <button
              type="button"
              className={styles.iconButton}
              title="Copy checkout command"
              onClick={copyBranch}
            >
              ⧉
            </button>
          </span>
        )}
      </div>

      <div className={styles.sideRow}>
        <span className={styles.sideLabel}>Created</span>
        <span className={styles.timestamp} title={detail.created_at}>
          {formatRelativeTime(detail.created_at)}
        </span>
      </div>
      <div className={styles.sideRow}>
        <span className={styles.sideLabel}>Updated</span>
        <span className={styles.timestamp} title={detail.updated_at}>
          {formatRelativeTime(detail.updated_at)}
        </span>
      </div>
    </aside>
  );
}

// ---- ⌘⇧O preview dialog -----------------------------------------------------------------

/**
 * The selection→sub-tickets preview (§5.4): N non-empty lines → N titles, every title
 * visible before anything is created. Opened with an empty selection it doubles as the
 * plain "new sub-ticket" dialog — type one line per sub-ticket.
 */
function SubticketPreviewDialog({
  initial,
  onClose,
  onCreate,
}: {
  initial: string;
  onClose: () => void;
  onCreate: (titles: string[]) => void;
}) {
  const [text, setText] = useState(initial);
  const titles = selectionToTitles(text);
  useKeyScope("modal", true);
  return (
    <div className={styles.backdrop} onClick={onClose}>
      <div
        role="dialog"
        aria-label="Create sub-tickets"
        className={styles.dialog}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "Escape") onClose();
        }}
      >
        <h2 className={styles.dialogTitle}>Create sub-tickets</h2>
        <textarea
          className={styles.dialogTextarea}
          aria-label="Sub-ticket titles, one per line"
          placeholder="One sub-ticket per line"
          rows={Math.max(3, titles.length + 1)}
          value={text}
          autoFocus
          onChange={(e) => setText(e.target.value)}
        />
        <ul className={styles.previewList} aria-label="Preview">
          {titles.map((t, i) => (
            <li key={`${i}:${t}`} className={styles.previewItem}>
              <span className={styles.previewMarker}>{i + 1}.</span>
              {t}
            </li>
          ))}
        </ul>
        <div className={styles.dialogActions}>
          <button type="button" className={styles.smallButton} onClick={onClose}>
            Cancel
          </button>
          <button
            type="button"
            className={styles.primaryButton}
            disabled={titles.length === 0}
            onClick={() => onCreate(titles)}
          >
            Create {titles.length} sub-ticket{titles.length === 1 ? "" : "s"}
          </button>
        </div>
      </div>
    </div>
  );
}
