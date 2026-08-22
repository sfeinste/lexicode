/*
 * Project settings → Repository (S15 slice of UI spec §5.11): the connection facts, the
 * "Re-scan repository" entry back into the bootstrap checklist, reconnect (rotate the token
 * by reconnecting with a new PAT) and disconnect. Branch template, setup script, secrets and
 * network policy join this pane with their owning stories.
 */
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { useDisconnectRepo, useRepoStatusQuery } from "../../../lib/api/repoQueries";
import { ConnectRepoCard } from "../overview/ConnectRepoCard";
import { NetworkSection } from "./NetworkSection";
import styles from "./settings.module.css";

export function RepositorySection({ projectKey }: { projectKey: string }) {
  const status = useRepoStatusQuery(projectKey);
  const disconnect = useDisconnectRepo(projectKey);
  const [confirming, setConfirming] = useState(false);

  if (status.isPending) {
    return <section aria-label="Repository" aria-busy="true" />;
  }
  if (status.isError) {
    return (
      <section aria-label="Repository">
        <p role="alert" className={styles.quiet}>
          Repository status could not load: {status.error.message}
        </p>
      </section>
    );
  }

  const repo = status.data.connected ? status.data.repo : undefined;

  return (
    <section aria-label="Repository">
      <header className={styles.paneHeader}>
        <h1 className={styles.paneTitle}>Repository</h1>
      </header>

      {!repo ? (
        <ConnectRepoCard projectKey={projectKey} />
      ) : (
        <div className={styles.fields}>
          <dl className={styles.repoFacts}>
            <div>
              <dt>Repository</dt>
              <dd>
                {repo.owner}/{repo.name}
              </dd>
            </div>
            <div>
              <dt>Default branch</dt>
              <dd>{repo.default_branch ?? "inherited from workspace"}</dd>
            </div>
            <div>
              <dt>Head commit</dt>
              <dd>
                {repo.head_sha ? (
                  <>
                    <code>{repo.head_sha.slice(0, 7)}</code> {repo.head_message}
                  </>
                ) : (
                  "unknown"
                )}
              </dd>
            </div>
            <div>
              <dt>Token</dt>
              <dd>
                {repo.has_token
                  ? "Stored as project secret GITHUB_TOKEN (write-only)"
                  : "Missing — reconnect to store one"}
              </dd>
            </div>
            <div>
              <dt>Last scanned</dt>
              <dd>{repo.last_synced_at ?? "never"}</dd>
            </div>
          </dl>

          <div className={styles.repoActions}>
            <Link
              to="/p/$key/bootstrap"
              params={{ key: projectKey }}
              className={styles.primaryButton}
            >
              Re-scan repository
            </Link>
            {!confirming ? (
              <button
                type="button"
                className={styles.dangerButton}
                onClick={() => setConfirming(true)}
              >
                Disconnect…
              </button>
            ) : (
              <span className={styles.repoActions}>
                <span className={styles.quiet}>
                  Disconnect {repo.owner}/{repo.name} and delete the stored token? Imported
                  tickets, wiki pages, triggers and agents stay.
                </span>
                <button
                  type="button"
                  className={styles.dangerButton}
                  onClick={() => disconnect.mutate()}
                  disabled={disconnect.isPending}
                >
                  {disconnect.isPending ? "Disconnecting…" : "Disconnect"}
                </button>
                <button
                  type="button"
                  className={styles.quietButton}
                  onClick={() => setConfirming(false)}
                >
                  Cancel
                </button>
              </span>
            )}
          </div>
          {disconnect.isError && (
            <p role="alert" className={styles.fieldError}>
              Disconnect failed: {disconnect.error.message}
            </p>
          )}

          <NetworkSection projectKey={projectKey} repo={repo} />
        </div>
      )}
    </section>
  );
}
