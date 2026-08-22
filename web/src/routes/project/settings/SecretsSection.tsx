/*
 * Project settings → Secrets (S13, UI spec §5.11): env-var-named secrets for this project's
 * run containers. The list shows name and `set · 4 days ago`; values are write-only (D-16),
 * so the only actions are Add, Replace and Delete — there is nothing to view.
 */
import { SecretsPanel } from "../../../components/SecretsPanel/SecretsPanel";
import { secretsApi } from "../../../lib/api/client";
import styles from "./settings.module.css";

export function SecretsSection({ projectKey }: { projectKey: string }) {
  return (
    <section aria-label="Secrets">
      <header className={styles.paneHeader}>
        <h1 className={styles.paneTitle}>Secrets</h1>
      </header>
      <p className={styles.sectionIntro}>
        Injected into this project&apos;s run containers as environment variables, named
        exactly as listed. Values are encrypted at rest and can never be viewed again after
        saving — only replaced or deleted.
      </p>
      <SecretsPanel
        queryKey={["secrets", "project", projectKey]}
        api={{
          list: (signal) => secretsApi.list(projectKey, signal),
          set: (body) => secretsApi.set(projectKey, body),
          remove: (id) => secretsApi.remove(projectKey, id),
        }}
      />
    </section>
  );
}
