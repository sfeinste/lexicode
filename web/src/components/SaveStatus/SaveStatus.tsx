/*
 * SaveStatus — the §5.11 inline saved indicator. Settings screens autosave; there is no
 * global Save button, so this one line is the only save feedback.
 */
import type { AutosaveStatus } from "../../lib/autosave";

import styles from "./SaveStatus.module.css";

export function SaveStatus({ status, error }: { status: AutosaveStatus; error: string | null }) {
  if (status === "idle") return <span className={styles.root} aria-hidden="true" />;
  if (status === "saving") {
    return (
      <span className={styles.root} role="status">
        Saving…
      </span>
    );
  }
  if (status === "error") {
    return (
      <span className={`${styles.root} ${styles.error}`} role="alert">
        {error ?? "Save failed"}
      </span>
    );
  }
  return (
    <span className={`${styles.root} ${styles.saved}`} role="status">
      Saved
    </span>
  );
}
