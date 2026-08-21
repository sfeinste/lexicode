/*
 * Shared frame for the three auth screens (setup, login, invite redemption): a centered
 * card, the wordmark, one form. These render outside the app chrome — there is no rail to
 * show someone who is not signed in.
 */
import type { FormEvent, ReactNode } from "react";

import { ApiProblem } from "../../lib/api/client";
import styles from "./auth.module.css";

export function AuthScreen({
  title,
  lead,
  children,
  onSubmit,
  submitLabel,
  busy,
  error,
  footer,
}: {
  title: string;
  lead?: string;
  children: ReactNode;
  onSubmit: (e: FormEvent<HTMLFormElement>) => void;
  submitLabel: string;
  busy: boolean;
  error: unknown;
  footer?: ReactNode;
}) {
  return (
    <main className={styles.screen}>
      <div className={styles.card}>
        <div className={styles.wordmark} aria-hidden="true">
          ◈ Lexicode
        </div>
        <h1 className={styles.title}>{title}</h1>
        {lead && <p className={styles.lead}>{lead}</p>}
        <form className={styles.form} onSubmit={onSubmit}>
          {children}
          <ProblemNote error={error} />
          <button type="submit" className={styles.submit} disabled={busy}>
            {busy ? "…" : submitLabel}
          </button>
        </form>
        {footer && <div className={styles.footer}>{footer}</div>}
      </div>
    </main>
  );
}

export function Field({
  label,
  name,
  type = "text",
  autoComplete,
  error,
}: {
  label: string;
  name: string;
  type?: string;
  autoComplete?: string;
  error?: unknown;
}) {
  const fieldError =
    error instanceof ApiProblem
      ? error.errors?.find((e) => e.field === name)?.message
      : undefined;
  return (
    <label className={styles.field}>
      <span className={styles.fieldLabel}>{label}</span>
      <input name={name} type={type} autoComplete={autoComplete} required />
      {fieldError && <span className={styles.fieldError}>{fieldError}</span>}
    </label>
  );
}

/** The problem's human message. Field-level messages render next to their inputs instead. */
function ProblemNote({ error }: { error: unknown }) {
  if (!error) return null;
  const message =
    error instanceof ApiProblem
      ? error.errors?.length
        ? null // every message is next to its field already
        : error.detail || error.title
      : "The server could not be reached.";
  if (!message) return null;
  return (
    <p role="alert" className={styles.problem}>
      {message}
    </p>
  );
}
