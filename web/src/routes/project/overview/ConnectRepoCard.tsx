/*
 * The single-gate connect card (S15, UI spec §8): a project without a repo shows one primary
 * action — connect owner/name + PAT — and everything derives from it. On success the empty
 * state becomes a loading state: straight to the bootstrap checklist.
 */
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { ApiProblem } from "../../../lib/api/client";
import { useConnectRepo } from "../../../lib/api/repoQueries";
import styles from "./overview.module.css";

export function ConnectRepoCard({ projectKey }: { projectKey: string }) {
  const navigate = useNavigate();
  const connect = useConnectRepo(projectKey);
  const [owner, setOwner] = useState("");
  const [name, setName] = useState("");
  const [token, setToken] = useState("");

  const fieldErrors: Record<string, string> = {};
  let formError: string | null = null;
  if (connect.isError) {
    const err = connect.error;
    if (err instanceof ApiProblem && err.errors?.length) {
      for (const fe of err.errors) fieldErrors[fe.field] = fe.message;
    } else {
      formError = err.message;
    }
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    connect.mutate(
      { owner, name, token },
      {
        onSuccess: () => {
          void navigate({ to: "/p/$key/bootstrap", params: { key: projectKey } });
        },
      },
    );
  };

  return (
    <section className={styles.connectCard} aria-label="Connect a repository">
      <h2 className={styles.connectHeadline}>Connect a repository to get started</h2>
      <p className={styles.connectBody}>
        We&apos;ll import your issues, docs, and agent instructions automatically — with a
        preview and checkboxes, never silently.
      </p>
      <form onSubmit={submit} className={styles.connectForm}>
        <div className={styles.connectFields}>
          <label className={styles.connectField}>
            Owner
            <input
              value={owner}
              onChange={(e) => setOwner(e.target.value)}
              placeholder="acme"
              autoComplete="off"
            />
            {fieldErrors.owner && (
              <span role="alert" className={styles.fieldError}>
                {fieldErrors.owner}
              </span>
            )}
          </label>
          <label className={styles.connectField}>
            Repository
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="payments"
              autoComplete="off"
            />
            {fieldErrors.name && (
              <span role="alert" className={styles.fieldError}>
                {fieldErrors.name}
              </span>
            )}
          </label>
          <label className={styles.connectField}>
            Personal access token
            <input
              type="password"
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="ghp_…"
              autoComplete="off"
            />
            {fieldErrors.token && (
              <span role="alert" className={styles.fieldError}>
                {fieldErrors.token}
              </span>
            )}
            <span className={styles.fieldHint}>
              Stored encrypted as the project secret GITHUB_TOKEN. It is never shown again.
            </span>
          </label>
        </div>
        {formError && (
          <p role="alert" className={styles.fieldError}>
            {formError}
          </p>
        )}
        <button type="submit" className={styles.connectButton} disabled={connect.isPending}>
          {connect.isPending ? "Verifying…" : "Connect GitHub repo"}
        </button>
      </form>
    </section>
  );
}
