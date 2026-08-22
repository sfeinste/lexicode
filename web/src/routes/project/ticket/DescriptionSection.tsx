/*
 * The ticket description placement of the shared Editor (S12). Always the editor — the
 * description IS a markdown editor per §5.4, not a rendered view with an edit mode — so
 * text selection for ⌘⇧O works directly on the textarea. Router-free by design: the shared
 * placement test suite mounts this exactly as the page does.
 */
import type { RefObject } from "react";

import { Editor, type EditorHandle, type MentionSources } from "../../../components/Editor/Editor";
import styles from "./ticket.module.css";

export interface DescriptionSectionProps {
  value: string;
  onChange: (value: string) => void;
  onBlur?: () => void;
  mentions: MentionSources;
  editorRef?: RefObject<EditorHandle | null>;
  /** The autosave indicator line ("Saving…" / "Saved"). */
  status?: string;
}

export function DescriptionSection({
  value,
  onChange,
  onBlur,
  mentions,
  editorRef,
  status,
}: DescriptionSectionProps) {
  return (
    <section className={styles.section} aria-label="Description">
      <div className={styles.sectionHead}>
        <h2 className={styles.sectionTitle}>Description</h2>
        {status !== undefined && <span className={styles.saveStatus}>{status}</span>}
      </div>
      <Editor
        ref={editorRef}
        value={value}
        onChange={onChange}
        onBlur={onBlur}
        mentions={mentions}
        ariaLabel="Description"
        placeholder="Describe the work. / for blocks, @ to mention. Select lines and press ⌘⇧O to convert them into sub-tickets."
        minRows={4}
      />
    </section>
  );
}
