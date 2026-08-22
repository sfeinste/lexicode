/*
 * Import from repository (S35; D-11 import-only): the bootstrap checklist's docs section,
 * re-runnable from the WIKI page. Opens on a fresh preview — the S15 detection with
 * already-imported flags (idempotent on imported_from) — then imports exactly the checked
 * subset with per-file scope selects. Importing twice never duplicates: previously imported
 * files come back unchecked and labeled, and the server skips them even if re-sent.
 */
import { useEffect, useState } from "react";

import { SCOPE_VALUES } from "../../../components/ScopeBadge/scopeValues";
import { ApiProblem, type AgentScope, type DocCandidate } from "../../../lib/api/client";
import { useWikiImport, useWikiImportPreview } from "../../../lib/api/wikiQueries";
import styles from "./wiki.module.css";

interface Row {
  doc: DocCandidate;
  checked: boolean;
  scope: AgentScope;
}

export function ImportDialog({
  projectKey,
  onClose,
}: {
  projectKey: string;
  onClose: () => void;
}) {
  const preview = useWikiImportPreview(projectKey, true);
  const doImport = useWikiImport(projectKey);
  const [rows, setRows] = useState<Row[] | null>(null);
  const [done, setDone] = useState<{ created: number; skipped: number } | null>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  // Seed the editable rows from the preview once it arrives.
  useEffect(() => {
    if (preview.data !== undefined && rows === null) {
      setRows(
        preview.data.docs.map((doc) => ({
          doc,
          checked: doc.checked,
          scope: doc.proposed_scope as AgentScope,
        })),
      );
    }
  }, [preview.data, rows]);

  const checkedRows = (rows ?? []).filter((r) => r.checked && !r.doc.already_imported);

  const submit = () => {
    doImport.mutate(
      checkedRows.map((r) => ({
        path: r.doc.path,
        scope: r.scope,
        paths: r.doc.scope_paths,
      })),
      {
        onSuccess: (res) =>
          setDone({ created: res.pages_created.length, skipped: res.docs_skipped.length }),
      },
    );
  };

  const previewError =
    preview.error instanceof ApiProblem
      ? preview.error.detail || preview.error.title
      : preview.isError
        ? "The repository could not be scanned."
        : null;
  const importError =
    doImport.error instanceof ApiProblem
      ? doImport.error.detail || doImport.error.title
      : doImport.isError
        ? "The import failed."
        : null;

  return (
    <div className={styles.overlay} role="presentation" onClick={onClose}>
      <div
        className={styles.dialog}
        role="dialog"
        aria-label="Import from repository"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.dialogTitle}>Import from repository</h2>
        {preview.isPending && <p className={styles.hint}>Scanning the repository…</p>}
        {previewError !== null && (
          <p className={styles.error} role="alert">
            {previewError}
          </p>
        )}
        {rows !== null && rows.length === 0 && (
          <p className={styles.hint}>
            No instruction files detected (AGENTS.md, CLAUDE.md, .cursor/rules, docs/**,
            README.md).
          </p>
        )}
        {rows !== null && rows.length > 0 && done === null && (
          <ul className={styles.importList}>
            {rows.map((r, i) => (
              <li key={r.doc.path} className={styles.importRow}>
                <label className={styles.importCheck}>
                  <input
                    type="checkbox"
                    checked={r.checked}
                    disabled={r.doc.already_imported}
                    aria-label={`Import ${r.doc.path}`}
                    onChange={(e) =>
                      setRows((prev) =>
                        (prev ?? []).map((row, j) =>
                          j === i ? { ...row, checked: e.target.checked } : row,
                        ),
                      )
                    }
                  />
                  <span className={styles.importPath}>{r.doc.path}</span>
                </label>
                {r.doc.already_imported ? (
                  <span className={styles.importedFlag}>already imported</span>
                ) : (
                  <select
                    className={styles.select}
                    aria-label={`Scope for ${r.doc.path}`}
                    value={r.scope}
                    onChange={(e) =>
                      setRows((prev) =>
                        (prev ?? []).map((row, j) =>
                          j === i ? { ...row, scope: e.target.value as AgentScope } : row,
                        ),
                      )
                    }
                  >
                    {SCOPE_VALUES.map((v) => (
                      <option key={v} value={v}>
                        {v}
                      </option>
                    ))}
                  </select>
                )}
              </li>
            ))}
          </ul>
        )}
        {done !== null && (
          <p className={styles.hint} data-testid="import-done">
            Imported {done.created} page{done.created === 1 ? "" : "s"}
            {done.skipped > 0 ? ` · ${done.skipped} already imported, skipped` : ""}.
          </p>
        )}
        {importError !== null && (
          <p className={styles.error} role="alert">
            {importError}
          </p>
        )}
        <div className={styles.dialogActions}>
          {done === null ? (
            <>
              <button type="button" className={styles.smallBtn} onClick={onClose}>
                Cancel
              </button>
              <button
                type="button"
                className={styles.primaryBtn}
                disabled={doImport.isPending || checkedRows.length === 0}
                onClick={submit}
              >
                Import {checkedRows.length} file{checkedRows.length === 1 ? "" : "s"}
              </button>
            </>
          ) : (
            <button type="button" className={styles.primaryBtn} onClick={onClose}>
              Done
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
