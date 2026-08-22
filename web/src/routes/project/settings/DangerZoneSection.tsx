/*
 * Project settings → Danger zone (S37, UI spec §5.11): archive, rotate the repository token,
 * and delete the project behind a typed confirmation that names exactly what will go —
 * counts of tickets, runs and wiki pages, fetched live. The server enforces the typed key
 * independently (DELETE /projects/{key} refuses a wrong `confirm`); the UI gate here is the
 * first fence, not the only one. Deletion is workspace-owner only — the API answers 403 for
 * members, so the button is honest about failing for them.
 */
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import {
  ApiProblem,
  repoApi,
  type Project,
  type ProjectCounts,
} from "../../../lib/api/client";
import {
  useDeleteProject,
  useProjectCountsQuery,
  useUpdateProject,
} from "../../../lib/api/projectQueries";
import { useRepoStatusQuery } from "../../../lib/api/repoQueries";
import styles from "./settings.module.css";

export function DangerZoneSection({ project }: { project: Project }) {
  return (
    <section aria-label="Danger zone">
      <header className={styles.paneHeader}>
        <h1 className={styles.paneTitle}>Danger zone</h1>
      </header>
      <div className={styles.fields}>
        <ArchiveBlock project={project} />
        <RotateTokenBlock projectKey={project.key} />
        <DeleteBlock project={project} />
      </div>
    </section>
  );
}

/** Archive: reversible, nothing is deleted. Same mutation the S08 flow used. */
function ArchiveBlock({ project }: { project: Project }) {
  const update = useUpdateProject(project.key);
  const archived = project.archived_at !== null;
  return (
    <div className={styles.dangerBlock}>
      <h2 className={styles.sectionTitle}>{archived ? "Unarchive" : "Archive"} project</h2>
      <p className={styles.quiet}>
        Archived projects disappear from Home and the rail; nothing is deleted.
      </p>
      <button
        type="button"
        className={archived ? styles.secondaryButton : styles.dangerButton}
        disabled={update.isPending}
        onClick={() => update.mutate({ archived: !archived })}
      >
        {archived ? "Unarchive project" : "Archive project"}
      </button>
    </div>
  );
}

/**
 * Rotate the stored repository token. Verify-then-replace on the server: a bad token is a
 * field error and the old token keeps working.
 */
function RotateTokenBlock({ projectKey }: { projectKey: string }) {
  const status = useRepoStatusQuery(projectKey);
  const [token, setToken] = useState("");
  const [state, setState] = useState<
    { kind: "idle" } | { kind: "busy" } | { kind: "done" } | { kind: "error"; message: string }
  >({ kind: "idle" });

  const repo = status.data?.connected === true ? status.data.repo : undefined;
  if (repo === undefined) {
    return null; // no repo, nothing to rotate
  }

  const rotate = async () => {
    setState({ kind: "busy" });
    try {
      await repoApi.rotateToken(projectKey, token);
      setToken("");
      setState({ kind: "done" });
    } catch (e) {
      const p = e as ApiProblem;
      setState({
        kind: "error",
        message: p.errors?.[0]?.message ?? p.message,
      });
    }
  };

  return (
    <div className={styles.dangerBlock}>
      <h2 className={styles.sectionTitle}>Rotate repository token</h2>
      <p className={styles.quiet}>
        Replace the stored token for {repo.owner}/{repo.name}. The new token is verified
        against the repository before the old one is replaced — a bad token changes nothing.
      </p>
      <div className={styles.dangerRow}>
        <input
          type="password"
          placeholder="New personal access token"
          value={token}
          autoComplete="off"
          onChange={(e) => setToken(e.target.value)}
          aria-label="New personal access token"
        />
        <button
          type="button"
          className={styles.secondaryButton}
          disabled={token.trim() === "" || state.kind === "busy"}
          onClick={() => void rotate()}
        >
          {state.kind === "busy" ? "Verifying…" : "Verify & rotate"}
        </button>
      </div>
      {state.kind === "done" && (
        <p className={styles.quiet} role="status">
          Token rotated. The old token is no longer stored.
        </p>
      )}
      {state.kind === "error" && (
        <p className={styles.dangerText} role="alert">
          {state.message}
        </p>
      )}
    </div>
  );
}

function DeleteBlock({ project }: { project: Project }) {
  const counts = useProjectCountsQuery(project.key);
  const del = useDeleteProject(project.key);
  const navigate = useNavigate();

  return (
    <DeleteProjectConfirm
      projectKey={project.key}
      projectName={project.name}
      counts={counts.data}
      busy={del.isPending}
      error={del.isError ? del.error.message : undefined}
      onDelete={(confirm) =>
        del.mutate(confirm, {
          onSuccess: () => void navigate({ to: "/" }),
        })
      }
    />
  );
}

/**
 * The typed-confirmation gate, exported for the unit test: the delete button stays disabled
 * until the typed value equals the project key exactly (case-sensitive — the key IS
 * uppercase), and the copy names the live counts of what will go.
 */
export function DeleteProjectConfirm({
  projectKey,
  projectName,
  counts,
  busy = false,
  error,
  onDelete,
}: {
  projectKey: string;
  projectName: string;
  counts: ProjectCounts | undefined;
  busy?: boolean;
  error?: string;
  onDelete: (confirm: string) => void;
}) {
  const [typed, setTyped] = useState("");
  const armed = typed === projectKey;

  return (
    <div className={styles.dangerBlock}>
      <h2 className={styles.sectionTitle}>Delete project</h2>
      <p className={styles.quiet}>
        Permanently delete {projectName}
        {counts !== undefined ? (
          <>
            {" "}
            — including {counts.tickets} {counts.tickets === 1 ? "ticket" : "tickets"},{" "}
            {counts.runs} {counts.runs === 1 ? "run" : "runs"} and {counts.wiki_pages} wiki{" "}
            {counts.wiki_pages === 1 ? "page" : "pages"}.
          </>
        ) : (
          "."
        )}{" "}
        This cannot be undone. The audit log keeps the project&apos;s history at workspace
        level.
      </p>
      <label className={styles.field}>
        Type <code>{projectKey}</code> to confirm
        <input
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          aria-label={`Type ${projectKey} to confirm deletion`}
        />
      </label>
      <div className={styles.dangerRow}>
        <button
          type="button"
          className={styles.dangerButton}
          disabled={!armed || busy}
          onClick={() => onDelete(typed)}
        >
          {busy ? "Deleting…" : "Delete this project forever"}
        </button>
      </div>
      {error !== undefined && (
        <p className={styles.dangerText} role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
