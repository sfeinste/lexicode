/*
 * The S07 placeholder page: a real, titled route target so every UI spec §1 URL resolves,
 * with the story that replaces it named. Later stories delete their placeholder as they
 * land the screen.
 */
import styles from "./placeholder.module.css";

export function Placeholder({ title, note }: { title: string; note?: string }) {
  return (
    <div className={styles.root}>
      <h1>{title}</h1>
      <p className={styles.note}>{note ?? "This screen arrives in a later story."}</p>
    </div>
  );
}
