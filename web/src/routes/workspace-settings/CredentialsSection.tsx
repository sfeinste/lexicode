/*
 * Claude credentials (S19, D-5): the workspace setting where the user pastes the output of
 * `claude setup-token`. The token is write-only — the API returns configuration state and
 * health, never a value — and the health line renders the server's message verbatim.
 *
 * The import button is the Linux-only fallback (macOS keeps the CLI login in the system
 * Keychain): the server checks the OS at click time, and the button only renders when the
 * server says it is available.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { StatusDot } from "../../components/StatusDot/StatusDot";
import { ApiProblem, credentialsApi, type CredentialsStatus } from "../../lib/api/client";
import styles from "./workspace.module.css";

/** The command the copy tells the user to run — asserted verbatim by the section's test. */
export const SETUP_TOKEN_COMMAND = "claude setup-token";

/** The instruction line — asserted verbatim by the section's test. */
export const SETUP_TOKEN_COPY_BEFORE = "Run ";
export const SETUP_TOKEN_COPY_AFTER =
  " in a terminal, then paste its output here. Agent runs sign in to Claude with this token; it is stored encrypted and injected into each run's container, never shown again.";

export function CredentialsSection() {
  const queryClient = useQueryClient();
  const status = useQuery({
    queryKey: ["workspace", "credentials"],
    queryFn: ({ signal }) => credentialsApi.get(signal),
  });

  const refresh = (next: CredentialsStatus) =>
    queryClient.setQueryData(["workspace", "credentials"], next);

  const save = useMutation({
    mutationFn: (token: string) => credentialsApi.setOauthToken(token),
    onSuccess: refresh,
  });
  const importToken = useMutation({
    mutationFn: () => credentialsApi.importOauthToken(),
    onSuccess: refresh,
  });
  const clear = useMutation({
    mutationFn: () => credentialsApi.clearOauthToken(),
    onSuccess: refresh,
  });

  if (status.isPending) {
    return <section aria-label="Claude credentials" aria-busy="true" />;
  }
  if (status.isError) {
    return (
      <section aria-label="Claude credentials" className={styles.secretsSection}>
        <h2 className={styles.sectionTitle}>Claude credentials</h2>
        <p role="alert" className={styles.quiet}>
          Credentials could not load: {status.error.message}
        </p>
      </section>
    );
  }

  return (
    <CredentialsSectionView
      status={status.data}
      busy={save.isPending || importToken.isPending || clear.isPending}
      error={firstError(save.error, importToken.error, clear.error)}
      onSave={(token) => save.mutate(token)}
      onImport={() => importToken.mutate()}
      onClear={() => clear.mutate()}
    />
  );
}

function firstError(...errors: (Error | null)[]): string | null {
  for (const e of errors) {
    if (e) return e instanceof ApiProblem ? (e.detail ?? e.title) : e.message;
  }
  return null;
}

export interface CredentialsSectionViewProps {
  status: CredentialsStatus;
  busy?: boolean;
  error?: string | null;
  onSave: (token: string) => void;
  onImport: () => void;
  onClear: () => void;
}

/** The presentational section, rendered with the loaded status; tested directly. */
export function CredentialsSectionView({
  status,
  busy,
  error,
  onSave,
  onImport,
  onClear,
}: CredentialsSectionViewProps) {
  const [token, setToken] = useState("");
  const oauth = status.oauth_token;

  const healthLabel = oauth.healthy
    ? "Token configured"
    : oauth.configured
      ? "Token needs attention"
      : "No token configured";

  return (
    <section aria-label="Claude credentials" className={styles.secretsSection}>
      <h2 className={styles.sectionTitle}>Claude credentials</h2>
      <p className={styles.lede}>
        {SETUP_TOKEN_COPY_BEFORE}
        <code className={styles.mono}>{SETUP_TOKEN_COMMAND}</code>
        {SETUP_TOKEN_COPY_AFTER}
      </p>

      <div className={styles.credentialHealth}>
        <StatusDot status={oauth.healthy ? "completed" : "failed"} label={healthLabel} />
        {!oauth.healthy && oauth.message ? (
          <span className={styles.hint}>{oauth.message}</span>
        ) : null}
      </div>
      {status.env.healthy && !oauth.healthy ? (
        <p className={styles.hint}>
          A fallback credential from the server&apos;s environment is currently in use.
        </p>
      ) : null}

      <form
        className={styles.credentialForm}
        onSubmit={(e) => {
          e.preventDefault();
          if (token.trim() !== "") {
            onSave(token.trim());
            setToken("");
          }
        }}
      >
        <label className={styles.field}>
          OAuth token
          <input
            type="password"
            autoComplete="off"
            placeholder="sk-ant-oat01-…"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            disabled={busy}
          />
        </label>
        <div className={styles.credentialActions}>
          <button type="submit" disabled={busy || token.trim() === ""}>
            Save token
          </button>
          {status.import.available ? (
            <button type="button" disabled={busy} onClick={onImport}>
              Import from {status.import.path}
            </button>
          ) : null}
          {oauth.configured ? (
            <button type="button" disabled={busy} onClick={onClear}>
              Forget token
            </button>
          ) : null}
        </div>
      </form>
      {error ? (
        <p role="alert" className={styles.credentialError}>
          {error}
        </p>
      ) : null}
    </section>
  );
}
