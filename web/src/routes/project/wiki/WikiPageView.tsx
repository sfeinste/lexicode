/*
 * One wiki page (UI spec §5.6). Header: title (R or click renames), owner avatar,
 * `verified until …` (red past due — VerifiedChip), scope badge with an edit affordance
 * (popover, five values), tags (chips link to the tag index; inline add/remove). Body:
 * MarkdownView; Edit swaps in THE Editor from S12, unchanged, with live mention sources
 * (users, agents, wiki pages, tickets). Agent proposals render through ProposalView (S35):
 * diff + reason + Accept / Edit / Dismiss.
 */
import { useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";

import { Editor, type EditorHandle } from "../../../components/Editor/Editor";
import type { MentionSources } from "../../../components/Editor/engine";
import { MarkdownView } from "../../../components/Editor/MarkdownView";
import { ScopeBadge } from "../../../components/ScopeBadge/ScopeBadge";
import { SCOPE_VALUES } from "../../../components/ScopeBadge/scopeValues";
import {
  ApiProblem,
  type AgentScope,
  type Member,
  type WikiPageDetail,
} from "../../../lib/api/client";
import { useEligibleAgents } from "../../../lib/api/agentQueries";
import { useUpdateWikiPage, useWikiListQuery } from "../../../lib/api/wikiQueries";
import { useMembers, useTicketList } from "../ticket/ticketData";
import { ProposalView } from "./ProposalView";
import { VerifiedChip } from "./VerifiedChip";
import styles from "./wiki.module.css";

export function WikiPageView({
  projectKey,
  detail,
}: {
  projectKey: string;
  detail: WikiPageDetail;
}) {
  if (detail.page.state === "proposed") {
    return <ProposalView projectKey={projectKey} detail={detail} />;
  }
  return <LivePageView projectKey={projectKey} detail={detail} />;
}

function LivePageView({
  projectKey,
  detail,
}: {
  projectKey: string;
  detail: WikiPageDetail;
}) {
  const page = detail.page;
  const update = useUpdateWikiPage(projectKey);
  const [actionError, setActionError] = useState<string | null>(null);

  const onError = (err: unknown) =>
    setActionError(err instanceof ApiProblem ? err.detail || err.title : "The change did not save.");
  const patch = (body: Parameters<typeof update.mutate>[0]["body"]) => {
    setActionError(null);
    update.mutate({ id: page.id, body }, { onError });
  };

  // ---- mention sources: every source is live now (the wiki closes the S12 seam) --------
  const membersQuery = useMembers();
  const members = useMemo(() => membersQuery.data?.users ?? [], [membersQuery.data]);
  const { agents } = useEligibleAgents(projectKey);
  const wikiList = useWikiListQuery(projectKey);
  const tickets = useTicketList(projectKey);
  const mentions: MentionSources = useMemo(
    () => ({
      users: members.map((m: Member) => ({ kind: "user" as const, id: m.id, label: m.display_name })),
      agents: agents.map((a) => ({ kind: "agent" as const, id: a.id, label: a.name })),
      wiki: (wikiList.data?.pages ?? [])
        .filter((p) => p.id !== page.id && p.state === "live")
        .map((p) => ({ kind: "wiki" as const, id: p.id, label: p.title })),
      tickets: (tickets.data?.tickets ?? [])
        .filter((t) => t.archived_at === null)
        .map((t) => ({ kind: "ticket" as const, id: t.id, label: t.key, hint: t.title })),
    }),
    [members, agents, wikiList.data, tickets.data, page.id],
  );

  // ---- title rename --------------------------------------------------------------------
  const navigate = useNavigate();
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState(page.title);
  const commitTitle = () => {
    setEditingTitle(false);
    const t = titleDraft.trim();
    if (t === "" || t === page.title) return;
    setActionError(null);
    // Slugs follow titles — land on the renamed page's new URL, not a dead slug.
    update.mutate(
      { id: page.id, body: { title: t } },
      {
        onError,
        onSuccess: (updated) =>
          void navigate({
            to: "/p/$key/wiki/$slug",
            params: { key: projectKey, slug: updated.slug },
            replace: true,
          }),
      },
    );
  };

  // ---- scope popover -------------------------------------------------------------------
  const [scopeOpen, setScopeOpen] = useState(false);

  // ---- verified until ------------------------------------------------------------------
  const [editingVerified, setEditingVerified] = useState(false);

  // ---- tags ----------------------------------------------------------------------------
  const [tagDraft, setTagDraft] = useState("");
  const addTag = () => {
    const t = tagDraft.trim();
    if (t === "") return;
    setTagDraft("");
    patch({ tags: [...page.tags, t] });
  };

  // ---- body edit mode ------------------------------------------------------------------
  const [editing, setEditing] = useState(false);
  const [bodyDraft, setBodyDraft] = useState(page.body);
  const editorRef = useRef<EditorHandle>(null);
  const startEdit = () => {
    setBodyDraft(page.body);
    setEditing(true);
  };
  const saveBody = () => {
    setEditing(false);
    if (bodyDraft !== page.body) patch({ body: bodyDraft });
  };

  const owner = members.find((m: Member) => m.id === page.owner_id);
  // LivePageView never renders proposals (ProposalView does) — the gates stay as defense.
  const proposed = page.state === "proposed";

  return (
    <main className={styles.main} aria-label={`Wiki page ${page.title}`}>
      <header className={styles.pageHeader}>
        <div className={styles.titleRow}>
          {editingTitle ? (
            <input
              className={styles.titleInput}
              aria-label="Page title"
              autoFocus
              value={titleDraft}
              onChange={(e) => setTitleDraft(e.target.value)}
              onBlur={commitTitle}
              onKeyDown={(e) => {
                if (e.key === "Enter") commitTitle();
                if (e.key === "Escape") {
                  setTitleDraft(page.title);
                  setEditingTitle(false);
                }
              }}
            />
          ) : (
            <>
              <h1 className={styles.pageTitle}>{page.title}</h1>
              {!proposed && (
                <button
                  type="button"
                  className={styles.smallBtn}
                  onClick={() => {
                    setTitleDraft(page.title);
                    setEditingTitle(true);
                  }}
                >
                  Rename
                </button>
              )}
              {!proposed && !editing && (
                <button type="button" className={styles.smallBtn} onClick={startEdit}>
                  Edit
                </button>
              )}
            </>
          )}
        </div>
        <div className={styles.metaRow}>
          {owner !== undefined && (
            <span title={`Owner: ${owner.display_name}`}>
              <span
                className={styles.avatar}
                style={{ background: owner.avatar_color }}
                aria-label={`Owner ${owner.display_name}`}
              >
                {owner.display_name.slice(0, 1).toUpperCase()}
              </span>
            </span>
          )}
          {editingVerified ? (
            <input
              className={styles.dateInput}
              type="date"
              aria-label="Verified until"
              autoFocus
              defaultValue={page.verified_until ?? ""}
              onBlur={(e) => {
                setEditingVerified(false);
                const v = e.target.value;
                if (v !== (page.verified_until ?? "")) {
                  patch({ verified_until: v === "" ? null : v });
                }
              }}
            />
          ) : (
            <button
              type="button"
              className={styles.scopeEdit}
              onClick={() => setEditingVerified(true)}
              title="Set the verification date"
            >
              {page.verified_until !== null ? (
                <VerifiedChip verifiedUntil={page.verified_until} />
              ) : (
                <span className={styles.hint}>not verified</span>
              )}
            </button>
          )}
          <span style={{ position: "relative" }}>
            <button
              type="button"
              className={styles.scopeEdit}
              aria-label="Edit agent scope"
              aria-expanded={scopeOpen}
              onClick={() => setScopeOpen((o) => !o)}
            >
              <ScopeBadge scope={page.agent_scope} />
            </button>
            {scopeOpen && (
              <div className={styles.scopePopover} role="menu" aria-label="Agent scope">
                {SCOPE_VALUES.map((v: AgentScope) => (
                  <button
                    key={v}
                    type="button"
                    role="menuitem"
                    className={styles.scopeOption}
                    onClick={() => {
                      setScopeOpen(false);
                      if (v !== page.agent_scope) patch({ agent_scope: v });
                    }}
                  >
                    <ScopeBadge scope={v} />
                    <span>{SCOPE_HINTS[v]}</span>
                  </button>
                ))}
              </div>
            )}
          </span>
          {page.tags.map((t) => (
            <span key={t} className={styles.tagChip}>
              <Link
                to="/p/$key/wiki"
                params={{ key: projectKey }}
                search={{ tag: t }}
                className={styles.tagChip}
                style={{ background: "none", padding: 0 }}
              >
                {t}
              </Link>
              {!proposed && (
                <button
                  type="button"
                  className={styles.tagRemove}
                  aria-label={`Remove tag ${t}`}
                  onClick={() => patch({ tags: page.tags.filter((x) => x !== t) })}
                >
                  ×
                </button>
              )}
            </span>
          ))}
          {!proposed && (
            <input
              className={styles.tagInput}
              placeholder="+ tag"
              aria-label="Add tag"
              value={tagDraft}
              onChange={(e) => setTagDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") addTag();
              }}
              onBlur={addTag}
            />
          )}
        </div>
        {actionError !== null && <div className={styles.error}>{actionError}</div>}
      </header>

      {editing ? (
        <>
          <Editor
            ref={editorRef}
            value={bodyDraft}
            onChange={setBodyDraft}
            mentions={mentions}
            ariaLabel="Page body"
            placeholder="Write the page…"
            autoFocus
            minRows={12}
          />
          <div className={styles.editActions}>
            <button type="button" className={styles.primaryBtn} onClick={saveBody}>
              Save
            </button>
            <button
              type="button"
              className={styles.smallBtn}
              onClick={() => setEditing(false)}
            >
              Cancel
            </button>
          </div>
        </>
      ) : (
        <MarkdownView markdown={page.body} />
      )}
    </main>
  );
}

const SCOPE_HINTS: Record<AgentScope, string> = {
  always: "every run — costs context each time",
  auto: "matched to the task by title and tags",
  paths: "when changed paths match the globs",
  manual: "only when a human attaches it",
  never: "never injected",
};
