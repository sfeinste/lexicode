/*
 * /p/:key/wiki — the wiki index (S33): with `?tag=` the pages carrying that tag; without,
 * the tag index (flat tags with counts) plus the §8 empty state when the project has no
 * docs yet — whose CTA opens the S35 import-from-repository dialog (re-runnable S15
 * detection; also on the tree column's toolbar).
 */
import { useState } from "react";
import { Link, useParams, useSearch } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { useWikiImportPreview, useWikiListQuery } from "../../../lib/api/wikiQueries";
import { EMPTY_STATES, wikiImportLabel } from "../../emptyStates";
import { ImportDialog } from "./ImportDialog";
import { pagesWithTag, tagIndex } from "./tree";
import { WikiScreen } from "./WikiScreen";
import styles from "./wiki.module.css";

export function WikiIndexPage() {
  const { key } = useParams({ from: "/shell/p/$key/wiki/" });
  const { tag } = useSearch({ from: "/shell/p/$key/wiki/" });
  const list = useWikiListQuery(key);
  const pages = list.data?.pages ?? [];
  const tags = tagIndex(pages);
  const [importing, setImporting] = useState(false);
  // §8's detected-content variant: while the wiki is empty, run the S35 detection so the
  // CTA can say "Import AGENTS.md (detected)" when the scan finds one.
  const empty = pages.length === 0 && !list.isPending;
  const detect = useWikiImportPreview(key, empty && !importing);
  const agentsMdDetected = (detect.data?.docs ?? []).some(
    (d) => !d.already_imported && d.path.split("/").pop()?.toLowerCase() === "agents.md",
  );

  return (
    <WikiScreen projectKey={key} hasSide={false}>
      <main className={styles.main} aria-label="Wiki index">
        {tag !== undefined ? (
          <>
            <span className={styles.sectionLabel}>Tagged “{tag}”</span>
            <ul className={styles.pageList}>
              {pagesWithTag(pages, tag).map((p) => (
                <li key={p.id}>
                  <Link
                    to="/p/$key/wiki/$slug"
                    params={{ key, slug: p.slug }}
                    className={styles.treeRow}
                  >
                    <span className={styles.treeTitle}>{p.title}</span>
                  </Link>
                </li>
              ))}
            </ul>
            <p>
              <Link to="/p/$key/wiki" params={{ key }} className={styles.hint}>
                ← all tags
              </Link>
            </p>
          </>
        ) : pages.length === 0 && !list.isPending ? (
          <>
            <EmptyState
              headline={EMPTY_STATES.wiki.headline}
              body={EMPTY_STATES.wiki.body}
              primary={
                <button
                  type="button"
                  className={styles.primaryBtn}
                  onClick={() => setImporting(true)}
                >
                  {wikiImportLabel(agentsMdDetected)}
                </button>
              }
              secondary={
                <span className={styles.hint}>
                  …or use “{EMPTY_STATES.wiki.secondary}” in the tree.
                </span>
              }
            />
            {importing && <ImportDialog projectKey={key} onClose={() => setImporting(false)} />}
          </>
        ) : (
          <>
            <span className={styles.sectionLabel}>Tags</span>
            {tags.length === 0 ? (
              <p className={styles.hint}>
                No tags yet — add them on a page’s header; they index here.
              </p>
            ) : (
              <div className={styles.tagIndex}>
                {tags.map((t) => (
                  <Link
                    key={t.tag}
                    to="/p/$key/wiki"
                    params={{ key }}
                    search={{ tag: t.tag }}
                    className={styles.tagIndexChip}
                  >
                    {t.tag}
                    <span className={styles.tagCount}>{t.count}</span>
                  </Link>
                ))}
              </div>
            )}
            <p className={styles.hint} style={{ marginTop: 16 }}>
              Press / to search — search outranks the tree.
            </p>
          </>
        )}
      </main>
    </WikiScreen>
  );
}
