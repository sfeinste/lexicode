/*
 * The comment composer placement of the shared Editor (S12): the bottom of the unified
 * stream. `@` mentions people, agents, wiki pages and tickets; mentioning an agent stages a
 * run — and until the S22 scheduler exists the API reports staged=false with a note, which
 * renders here verbatim rather than pretending a run started. Router-free by design: the
 * shared placement test suite mounts this exactly as the page does.
 */
import { useState } from "react";

import { Editor, type MentionSources } from "../../../components/Editor/Editor";
import type { CommentRunRequest } from "../../../lib/api/client";
import styles from "./ticket.module.css";

export interface ComposerProps {
  mentions: MentionSources;
  /** Posts the comment; resolves on success (the draft clears), rejects on failure. */
  onPost: (body: string) => Promise<unknown>;
  /** Honest agent-mention outcomes from the last post (empty = render nothing). */
  runNotices?: CommentRunRequest[];
}

export function Composer({ mentions, onPost, runNotices = [] }: ComposerProps) {
  const [draft, setDraft] = useState("");
  const [posting, setPosting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const post = () => {
    const body = draft;
    if (body.trim() === "" || posting) return;
    setPosting(true);
    setError(null);
    onPost(body).then(
      () => {
        setPosting(false);
        setDraft("");
      },
      (err: unknown) => {
        setPosting(false);
        setError(err instanceof Error ? err.message : "The comment did not post.");
      },
    );
  };

  return (
    <section className={styles.composer} aria-label="Add a comment">
      <Editor
        value={draft}
        onChange={setDraft}
        mentions={mentions}
        ariaLabel="Add a comment"
        placeholder="Comment — @ mentions people and agents; mentioning an agent starts a run on this ticket"
        minRows={2}
        onSubmit={post}
      />
      <div className={styles.composerActions}>
        <button
          type="button"
          className={styles.primaryButton}
          disabled={draft.trim() === "" || posting}
          onClick={post}
        >
          {posting ? "Posting…" : "Comment ⌘↵"}
        </button>
        {error !== null && (
          <span role="alert" className={styles.composerError}>
            {error}
          </span>
        )}
      </div>
      {runNotices.length > 0 && (
        <ul className={styles.runNotices}>
          {runNotices.map((r) => (
            <li key={r.agent_id} data-staged={r.staged || undefined}>
              @{r.agent_name}: {r.staged ? "run queued" : `not started — ${r.note}`}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
