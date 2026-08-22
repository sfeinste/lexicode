/*
 * /p/:key/wiki — the wiki index (S33): with `?tag=` the pages carrying that tag; without,
 * the tag index (flat tags with counts) plus the §8 empty state when the project has no
 * docs yet. Import-from-repo is the bootstrap flow (S15); its detected-file CTA joins this
 * empty state when the S34/S35 wiki-import endpoint lands.
 */
import { Link, useParams, useSearch } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { useWikiListQuery } from "../../../lib/api/wikiQueries";
import { pagesWithTag, tagIndex } from "./tree";
import { WikiScreen } from "./WikiScreen";
import styles from "./wiki.module.css";

export function WikiIndexPage() {
  const { key } = useParams({ from: "/shell/p/$key/wiki/" });
  const { tag } = useSearch({ from: "/shell/p/$key/wiki/" });
  const list = useWikiListQuery(key);
  const pages = list.data?.pages ?? [];
  const tags = tagIndex(pages);

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
          <EmptyState
            headline="Your project has no docs yet"
            body="Docs here steer your agents, not just your teammates."
            primary={<span className={styles.hint}>Create one with “New page” in the tree.</span>}
          />
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
