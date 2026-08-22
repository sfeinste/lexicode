/*
 * The wiki screen frame (UI spec §5.6): tree (220px) · main · outline + backlinks (240px).
 * Search outranks the tree — the box is pinned at the top of the tree column, `/` focuses
 * it (keyboard registry, route scope), and a non-empty query swaps the main column to
 * ranked results with snippets. The tree is two levels, drag-reorders within a parent
 * (fractional positions — the drop writes a midpoint), and shows a ScopeBadge per node;
 * agent proposals render dashed with a PROPOSED chip (review lands in S35).
 *
 * The context budget meter is the shared ContextMeter (S34): always-on tokens against the
 * project's effective threshold from GET /wiki/context-budget, amber when over. The page
 * count comes from the tree payload already in hand.
 */
import { Fragment, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";

import { ContextMeter } from "../../../components/ContextMeter/ContextMeter";
import { ScopeBadge } from "../../../components/ScopeBadge/ScopeBadge";
import { ApiProblem, type WikiPage } from "../../../lib/api/client";
import { useContextBudgetQuery } from "../../../lib/api/contextQueries";
import {
  useCreateWikiPage,
  useUpdateWikiPage,
  useWikiListQuery,
  useWikiSearchQuery,
} from "../../../lib/api/wikiQueries";
import { useKeyBindings, useKeyScope } from "../../../lib/keyboard/hooks";
import { ImportDialog } from "./ImportDialog";
import { renderSnippet } from "./snippet";
import { buildTree, dropPosition } from "./tree";
import styles from "./wiki.module.css";

export function WikiScreen({
  projectKey,
  selectedSlug,
  children,
  hasSide,
}: {
  projectKey: string;
  selectedSlug?: string;
  children: React.ReactNode;
  /** Whether the right (outline + backlinks) column is present — page views only. */
  hasSide: boolean;
}) {
  const navigate = useNavigate();
  const list = useWikiListQuery(projectKey);
  const pages = useMemo(() => list.data?.pages ?? [], [list.data]);
  const tree = useMemo(() => buildTree(pages), [pages]);

  const [query, setQuery] = useState("");
  const search = useWikiSearchQuery(projectKey, query);
  const searchRef = useRef<HTMLInputElement>(null);

  useKeyScope("route", true);
  useKeyBindings(
    () => [
      {
        id: "wiki.search",
        scope: "route",
        chord: "/",
        title: "Search wiki",
        group: "Wiki",
        palette: true,
        run: () => searchRef.current?.focus(),
      },
    ],
    [],
  );

  // ---- context budget meter (S34): server numbers; the tree supplies the page count ----
  const budget = useContextBudgetQuery(projectKey);
  const alwaysPages = pages.filter((p) => p.agent_scope === "always" && p.state === "live");

  // ---- drag reorder (within one parent) -----------------------------------------------
  const update = useUpdateWikiPage(projectKey);
  const [dragId, setDragId] = useState<string | null>(null);
  const [dropKey, setDropKey] = useState<string | null>(null);
  const byId = useMemo(() => new Map(pages.map((p) => [p.id, p])), [pages]);

  const siblingsOf = (parentId: string | null): WikiPage[] => {
    if (parentId === null) return tree.map((n) => n.page);
    return tree.find((n) => n.page.id === parentId)?.children ?? [];
  };

  /** Drop before `index` in `parentId`'s sibling list (index === length appends). */
  const onDrop = (parentId: string | null, index: number) => {
    setDropKey(null);
    if (dragId === null) return;
    const dragged = byId.get(dragId);
    setDragId(null);
    if (dragged === undefined || dragged.parent_id !== parentId) return; // reorder only
    const rest = siblingsOf(parentId).filter((p) => p.id !== dragged.id);
    const position = dropPosition(rest, index);
    if (position === dragged.position) return;
    update.mutate({ id: dragged.id, body: { position } });
  };

  const rowDragProps = (page: WikiPage, index: number) => ({
    draggable: true,
    onDragStart: () => setDragId(page.id),
    onDragEnd: () => {
      setDragId(null);
      setDropKey(null);
    },
    onDragOver: (e: React.DragEvent) => {
      if (dragId === null || dragId === page.id) return;
      if (byId.get(dragId)?.parent_id !== page.parent_id) return;
      e.preventDefault();
      setDropKey(page.id);
    },
    onDragLeave: () => setDropKey((k) => (k === page.id ? null : k)),
    onDrop: (e: React.DragEvent) => {
      e.preventDefault();
      // Adjust: dropping before a row that sits after the dragged one shifts by the removal.
      const sibs = siblingsOf(page.parent_id);
      const dragIndex = sibs.findIndex((p) => p.id === dragId);
      const target = dragIndex !== -1 && dragIndex < index ? index - 1 : index;
      onDrop(page.parent_id, target);
    },
  });

  // ---- new page flow: title → created under the selected parent -----------------------
  const create = useCreateWikiPage(projectKey);
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
  const [newTitle, setNewTitle] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  // The selected page's root: a selected root page hosts the new page as a child; a
  // selected child page means its parent hosts it (two levels — never deeper).
  const selected = pages.find((p) => p.slug === selectedSlug);
  const parentCandidate =
    selected === undefined
      ? undefined
      : selected.parent_id === null
        ? selected
        : byId.get(selected.parent_id);
  const [underParent, setUnderParent] = useState(true);

  const submitNewPage = () => {
    const title = newTitle.trim();
    if (title === "") return;
    const parent_id = underParent && parentCandidate !== undefined ? parentCandidate.id : null;
    create.mutate(
      { title, parent_id, body: "" },
      {
        onSuccess: (page) => {
          setCreating(false);
          setNewTitle("");
          setCreateError(null);
          void navigate({
            to: "/p/$key/wiki/$slug",
            params: { key: projectKey, slug: page.slug },
          });
        },
        onError: (err) =>
          setCreateError(
            err instanceof ApiProblem ? err.detail || err.title : "The page was not created.",
          ),
      },
    );
  };

  const searching = query.trim() !== "";
  const results = search.data?.results ?? [];

  const renderRow = (page: WikiPage, depth: 0 | 1, index: number) => (
    <li key={page.id}>
      <Link
        to="/p/$key/wiki/$slug"
        params={{ key: projectKey, slug: page.slug }}
        className={styles.treeRow}
        data-depth={depth}
        data-selected={page.slug === selectedSlug || undefined}
        data-proposed={page.state === "proposed" || undefined}
        data-drop={dropKey === page.id || undefined}
        {...rowDragProps(page, index)}
      >
        <span className={styles.treeTitle}>{page.title}</span>
        {page.state === "proposed" && <span className={styles.proposedChip}>PROPOSED</span>}
        <ScopeBadge scope={page.agent_scope} />
      </Link>
    </li>
  );

  return (
    <div className={hasSide ? styles.screen : `${styles.screen} ${styles.screenNoSide}`}>
      <nav className={styles.treeCol} aria-label="Wiki tree">
        <input
          ref={searchRef}
          className={styles.search}
          type="search"
          placeholder="Search wiki…  /"
          aria-label="Search wiki"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              setQuery("");
              e.currentTarget.blur();
            }
          }}
        />
        <div className={styles.budgetStrip}>
          <ContextMeter
            alwaysTokens={budget.data?.always_tokens ?? alwaysPages.reduce((sum, p) => sum + p.token_estimate, 0)}
            thresholdTokens={budget.data?.threshold_tokens ?? 0}
            pageCount={alwaysPages.length}
          />
        </div>
        <ul className={styles.tree}>
          {tree.map((node, i) => (
            <Fragment key={node.page.id}>
              {renderRow(node.page, 0, i)}
              {node.children.map((c, j) => renderRow(c, 1, j))}
            </Fragment>
          ))}
        </ul>
        <div className={styles.newPage}>
          {creating ? (
            <>
              <input
                className={styles.newPageInput}
                autoFocus
                placeholder="Page title"
                aria-label="New page title"
                value={newTitle}
                onChange={(e) => setNewTitle(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") submitNewPage();
                  if (e.key === "Escape") setCreating(false);
                }}
              />
              {parentCandidate !== undefined && (
                <select
                  className={styles.select}
                  aria-label="Parent"
                  value={underParent ? "parent" : "root"}
                  onChange={(e) => setUnderParent(e.target.value === "parent")}
                >
                  <option value="parent">Under {parentCandidate.title}</option>
                  <option value="root">Top level</option>
                </select>
              )}
              <button type="button" className={styles.smallBtn} onClick={submitNewPage}>
                Create
              </button>
              {createError !== null && <div className={styles.error}>{createError}</div>}
            </>
          ) : (
            <>
              <button
                type="button"
                className={styles.newPageBtn}
                onClick={() => {
                  setCreating(true);
                  setCreateError(null);
                }}
              >
                New page
              </button>
              <button
                type="button"
                className={styles.newPageBtn}
                onClick={() => setImporting(true)}
              >
                Import from repository
              </button>
            </>
          )}
        </div>
      </nav>
      {importing && (
        <ImportDialog projectKey={projectKey} onClose={() => setImporting(false)} />
      )}

      {searching ? (
        <main className={styles.main} aria-label="Search results">
          <span className={styles.sectionLabel}>
            {search.isPending ? "Searching…" : `${results.length} results`}
          </span>
          <ul className={styles.resultList}>
            {results.map((r) => (
              <li key={r.id}>
                <Link
                  to="/p/$key/wiki/$slug"
                  params={{ key: projectKey, slug: r.slug }}
                  className={styles.resultRow}
                  onClick={() => setQuery("")}
                >
                  <div className={styles.resultTitle}>{renderSnippet(r.title_snippet)}</div>
                  <div className={styles.resultSnippet}>{renderSnippet(r.body_snippet)}</div>
                </Link>
              </li>
            ))}
          </ul>
        </main>
      ) : (
        children
      )}
    </div>
  );
}
