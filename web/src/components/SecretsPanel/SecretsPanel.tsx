/*
 * The secrets list + editor (S13, D-16), shared by project settings and workspace settings.
 *
 * Values are write-only: the API never returns one, so this panel never has one to show.
 * Each row renders the name and "set · 4 days ago" (relative updated_at). Replace opens a
 * password field that clears after save; Add is a name + password pair that does the same.
 * Inputs are type="password" with autofill disabled — a secret should not end up in the
 * browser's saved-password store under the app's origin.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import {
  ApiProblem,
  type Secret,
  type SecretListResponse,
  type SetSecretRequest,
} from "../../lib/api/client";
import { formatRelativeTime } from "../../lib/format/format";
import styles from "./SecretsPanel.module.css";

/** The API half the panel needs — project or workspace scope, injected by the caller. */
export interface SecretsScopeApi {
  list: (signal?: AbortSignal) => Promise<SecretListResponse>;
  set: (body: SetSecretRequest) => Promise<Secret>;
  remove: (id: string) => Promise<void>;
}

function problemText(err: unknown): string {
  if (err instanceof ApiProblem) {
    const field = err.errors?.[0];
    return field ? field.message : err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

export function SecretsPanel({
  queryKey,
  api,
}: {
  queryKey: readonly unknown[];
  api: SecretsScopeApi;
}) {
  const qc = useQueryClient();
  const query = useQuery({
    queryKey,
    queryFn: ({ signal }) => api.list(signal),
  });
  const invalidate = () => void qc.invalidateQueries({ queryKey });

  const set = useMutation({
    mutationFn: (body: SetSecretRequest) => api.set(body),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (id: string) => api.remove(id),
    onSuccess: invalidate,
  });

  if (query.isPending) {
    return <div className={styles.panel} aria-busy="true" />;
  }
  if (query.isError) {
    return (
      <p role="alert" className={styles.error}>
        Secrets could not load: {query.error.message}
      </p>
    );
  }

  const secrets = query.data.secrets;
  return (
    <div className={styles.panel}>
      {secrets.length === 0 ? (
        <p className={styles.quiet}>No secrets yet.</p>
      ) : (
        <ul className={styles.list}>
          {secrets.map((s) => (
            <SecretRow
              key={s.id}
              secret={s}
              onReplace={(value) => set.mutateAsync({ name: s.name, value })}
              onDelete={() => remove.mutateAsync(s.id)}
            />
          ))}
        </ul>
      )}
      <AddSecret onAdd={(name, value) => set.mutateAsync({ name, value })} />
    </div>
  );
}

function SecretRow({
  secret,
  onReplace,
  onDelete,
}: {
  secret: Secret;
  onReplace: (value: string) => Promise<unknown>;
  onDelete: () => Promise<unknown>;
}) {
  const [replacing, setReplacing] = useState(false);
  const [value, setValue] = useState("");
  const [armed, setArmed] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const save = async () => {
    if (!value) return;
    try {
      await onReplace(value);
      // The write-only contract, visibly: the field clears and collapses back to the
      // set-date row. Nothing is retained client-side either.
      setValue("");
      setReplacing(false);
      setError(null);
    } catch (err) {
      setError(problemText(err));
    }
  };

  return (
    <li className={styles.row}>
      <div className={styles.rowMain}>
        <span className={styles.name}>{secret.name}</span>
        <span className={styles.quiet}>set · {formatRelativeTime(secret.updated_at)}</span>
        <span className={styles.rowActions}>
          {!replacing && (
            <button
              className={styles.secondaryButton}
              onClick={() => {
                setReplacing(true);
                setArmed(false);
              }}
            >
              Replace
            </button>
          )}
          {armed ? (
            <>
              <button
                className={styles.dangerButton}
                onClick={() => {
                  void onDelete().catch((err: unknown) => setError(problemText(err)));
                }}
              >
                Confirm delete
              </button>
              <button className={styles.secondaryButton} onClick={() => setArmed(false)}>
                Keep
              </button>
            </>
          ) : (
            <button className={styles.dangerButton} onClick={() => setArmed(true)}>
              Delete
            </button>
          )}
        </span>
      </div>
      {replacing && (
        <form
          className={styles.valueForm}
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <input
            type="password"
            autoComplete="new-password"
            data-1p-ignore
            placeholder="New value"
            aria-label={`New value for ${secret.name}`}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
          <button type="submit" className={styles.primaryButton} disabled={!value}>
            Save
          </button>
          <button
            type="button"
            className={styles.secondaryButton}
            onClick={() => {
              setReplacing(false);
              setValue("");
              setError(null);
            }}
          >
            Cancel
          </button>
        </form>
      )}
      {error && (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      )}
    </li>
  );
}

function AddSecret({ onAdd }: { onAdd: (name: string, value: string) => Promise<unknown> }) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const add = async () => {
    if (!name.trim() || !value) return;
    try {
      await onAdd(name.trim().toUpperCase(), value);
      setName("");
      setValue("");
      setError(null);
    } catch (err) {
      setError(problemText(err));
    }
  };

  return (
    <form
      className={styles.addForm}
      onSubmit={(e) => {
        e.preventDefault();
        void add();
      }}
    >
      <div className={styles.addRow}>
        <input
          className={styles.name}
          placeholder="NAME"
          aria-label="Secret name"
          autoComplete="off"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <input
          type="password"
          autoComplete="new-password"
          data-1p-ignore
          placeholder="Value"
          aria-label="Secret value"
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <button type="submit" className={styles.primaryButton} disabled={!name.trim() || !value}>
          Add
        </button>
      </div>
      <span className={styles.quiet}>
        Values are write-only: once saved they can be replaced or deleted, never viewed.
      </span>
      {error && (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      )}
    </form>
  );
}
