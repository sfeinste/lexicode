/*
 * Home — `/` (UI spec §5.1): the cross-project "Needs you" strip on top, the projects table
 * below. S07 ships the frame with both regions in their empty states; S08 fills the projects
 * table, S28 the needs-you cards.
 */
import { EmptyState } from "../../components/EmptyState/EmptyState";
import styles from "./home.module.css";

export function HomePage() {
  return (
    <div className={styles.root}>
      <section aria-label="Needs you">
        <h2 className={styles.sectionTitle}>Needs you</h2>
        <p className={styles.quiet}>Nothing is waiting on you.</p>
      </section>
      <section aria-label="Projects">
        <h2 className={styles.sectionTitle}>Projects</h2>
        <EmptyState
          headline="Nothing here yet"
          body="A project connects a repo, a board, and a roster of agents."
          primary={<button className={styles.cta}>Create project</button>}
        />
      </section>
    </div>
  );
}
