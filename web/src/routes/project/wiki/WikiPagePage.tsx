/*
 * /p/:key/wiki/:slug — one wiki page inside the three-column screen (S33). The slug
 * resolves to an id through the cached tree payload; the detail fetch then brings the page
 * plus backlinks and unlinked mentions in one round trip.
 */
import { useParams } from "@tanstack/react-router";

import { EmptyState } from "../../../components/EmptyState/EmptyState";
import { useWikiListQuery, useWikiPageQuery } from "../../../lib/api/wikiQueries";
import { BacklinksPane } from "./BacklinksPane";
import { WikiPageView } from "./WikiPageView";
import { WikiScreen } from "./WikiScreen";
import styles from "./wiki.module.css";

export function WikiPagePage() {
  const { key, slug } = useParams({ from: "/shell/p/$key/wiki/$slug" });
  const list = useWikiListQuery(key);
  const id = list.data?.pages.find((p) => p.slug === slug)?.id;
  const detail = useWikiPageQuery(id);

  return (
    <WikiScreen projectKey={key} selectedSlug={slug} hasSide={detail.data !== undefined}>
      {detail.data !== undefined ? (
        <>
          <WikiPageView projectKey={key} detail={detail.data} />
          <BacklinksPane projectKey={key} detail={detail.data} />
        </>
      ) : (
        <main className={styles.main}>
          {!list.isPending && id === undefined ? (
            <EmptyState
              headline="Page not found"
              body="It may have been renamed (slugs follow titles) or archived. Search knows the current titles."
            />
          ) : (
            <p className={styles.hint}>Loading…</p>
          )}
        </main>
      )}
    </WikiScreen>
  );
}
