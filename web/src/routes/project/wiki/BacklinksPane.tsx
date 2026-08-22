/*
 * The right column of a wiki page (UI spec §5.6): outline (headings from the body), then
 * linked mentions with their full containing paragraph as context, then the collapsed
 * `Unlinked mentions (3)` disclosure with one-click linking. Linking edits the OTHER page:
 * the source's body gets its first plain occurrence of this page's title replaced with the
 * mention token, then a normal PATCH — versioning and mention re-derivation come for free.
 */
import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";

import { mentionPattern } from "../../../components/Editor/mentionPattern";
import { wikiApi, type WikiPageDetail, type WikiUnlinkedMention } from "../../../lib/api/client";
import { wikiKeys } from "../../../lib/api/wikiQueries";
import { linkFirstPlainOccurrence } from "./linkMention";
import styles from "./wiki.module.css";

/** Render a backlink paragraph with mention tokens as `@label` runs — the stored token
 * syntax is wire format, not reading material. */
function renderParagraph(text: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let last = 0;
  let i = 0;
  for (const m of text.matchAll(mentionPattern)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    out.push(
      <strong key={i++} data-kind={m[2]}>
        @{m[1]}
      </strong>,
    );
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function BacklinksPane({
  projectKey,
  detail,
}: {
  projectKey: string;
  detail: WikiPageDetail;
}) {
  const page = detail.page;
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [linkError, setLinkError] = useState<string | null>(null);

  const linkUnlinked = async (u: WikiUnlinkedMention) => {
    setLinkError(null);
    try {
      // Fetch the source fresh — the list cache may be stale — then rewrite and PATCH it.
      const source = await wikiApi.get(u.page_id);
      const next = linkFirstPlainOccurrence(source.page.body, page.title, page.id);
      if (next === null) {
        // The plain occurrence is gone (someone edited meanwhile); refresh the disclosure.
        await qc.invalidateQueries({ queryKey: wikiKeys.detail(page.id) });
        return;
      }
      await wikiApi.update(u.page_id, { body: next });
      await qc.invalidateQueries({ queryKey: ["wikiPage"] });
      await qc.invalidateQueries({ queryKey: wikiKeys.list(projectKey) });
    } catch {
      setLinkError("Linking failed — the source page may have changed.");
    }
  };

  const outline = page.body
    .split("\n")
    .filter((l) => l.startsWith("# ") || l.startsWith("## "))
    .map((l, i) => ({
      key: i,
      level: l.startsWith("## ") ? 2 : 1,
      text: l.replace(/^#+ /, ""),
    }));

  return (
    <aside className={styles.sideCol} aria-label="Outline and backlinks">
      {outline.length > 0 && (
        <section>
          <span className={styles.sectionLabel}>Outline</span>
          <ul className={styles.outlineList}>
            {outline.map((h) => (
              <li key={h.key} data-level={h.level}>
                {h.text}
              </li>
            ))}
          </ul>
        </section>
      )}

      <section aria-label="Backlinks">
        <span className={styles.sectionLabel}>Backlinks</span>
        {detail.backlinks.length === 0 && (
          <p className={styles.hint}>No pages link here yet.</p>
        )}
        {detail.backlinks.map((g) => (
          <div key={`${g.source_kind}:${g.source_id}`} className={styles.backlinkGroup}>
            {g.source_kind === "wiki" && g.source_slug !== undefined ? (
              <Link
                to="/p/$key/wiki/$slug"
                params={{ key: projectKey, slug: g.source_slug }}
                className={styles.backlinkSource}
              >
                {g.title}
              </Link>
            ) : (
              <span className={styles.backlinkSource}>
                {g.source_key !== undefined ? `${g.source_key} · ` : ""}
                {g.title}
              </span>
            )}
            {g.paragraphs.map((p, i) => (
              <p key={i} className={styles.backlinkPara}>
                {renderParagraph(p)}
              </p>
            ))}
          </div>
        ))}
      </section>

      {detail.unlinked_mentions.length > 0 && (
        <section className={styles.unlinked} aria-label="Unlinked mentions">
          <button
            type="button"
            className={styles.unlinkedToggle}
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
          >
            {open ? "▾" : "▸"} Unlinked mentions ({detail.unlinked_mentions.length})
          </button>
          {open &&
            detail.unlinked_mentions.map((u) => (
              <div key={u.page_id} className={styles.unlinkedRow}>
                <div className={styles.unlinkedHead}>
                  <Link
                    to="/p/$key/wiki/$slug"
                    params={{ key: projectKey, slug: u.slug }}
                    className={styles.backlinkSource}
                  >
                    {u.title}
                  </Link>
                  <button
                    type="button"
                    className={styles.linkBtn}
                    onClick={() => void linkUnlinked(u)}
                  >
                    Link
                  </button>
                </div>
                <p className={styles.backlinkPara}>{renderParagraph(u.paragraph)}</p>
              </div>
            ))}
          {linkError !== null && <div className={styles.error}>{linkError}</div>}
        </section>
      )}
    </aside>
  );
}
