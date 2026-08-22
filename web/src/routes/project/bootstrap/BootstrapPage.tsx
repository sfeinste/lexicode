/*
 * Bootstrap checklist — `/p/:key/bootstrap` (S15, brief §6.3). One scan, five sections
 * (Issues · Docs · CI triggers · Agents · Overview), per-section select-all, checkboxes
 * everywhere: nothing is created silently. Already-imported items render disabled with an
 * "Already imported" label — the idempotent re-scan. Apply creates exactly the checked
 * subset, then a result view names what happened and where to go next.
 */
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";

import type {
  AgentScope,
  BootstrapApplyResult,
  BootstrapPreview,
} from "../../../lib/api/client";
import {
  useBootstrapApply,
  useBootstrapPreviewQuery,
  useRepoStatusQuery,
} from "../../../lib/api/repoQueries";
import styles from "./bootstrap.module.css";

const SCOPES: AgentScope[] = ["always", "auto", "paths", "manual", "never"];

interface Selection {
  issues: Set<number>;
  docs: Set<string>;
  docScopes: Record<string, AgentScope>;
  triggers: Set<string>;
  agents: Set<string>;
  overview: boolean;
}

function initialSelection(pv: BootstrapPreview): Selection {
  return {
    issues: new Set(pv.issues.filter((i) => i.checked).map((i) => i.number)),
    docs: new Set(pv.docs.filter((d) => d.checked).map((d) => d.path)),
    docScopes: Object.fromEntries(pv.docs.map((d) => [d.path, d.proposed_scope])),
    triggers: new Set(pv.triggers.filter((t) => t.checked).map((t) => t.id)),
    agents: new Set(pv.agents.filter((a) => a.checked).map((a) => a.name)),
    overview: pv.overview.checked,
  };
}

function toggled<T>(set: Set<T>, value: T, on: boolean): Set<T> {
  const next = new Set(set);
  if (on) next.add(value);
  else next.delete(value);
  return next;
}

export function BootstrapPage() {
  const { key } = useParams({ from: "/shell/p/$key/bootstrap" });
  const navigate = useNavigate();
  const status = useRepoStatusQuery(key);
  const connected = status.data?.connected === true;
  const preview = useBootstrapPreviewQuery(key, connected);
  const apply = useBootstrapApply(key);

  const [sel, setSel] = useState<Selection | null>(null);
  const [result, setResult] = useState<BootstrapApplyResult | null>(null);
  const pv = preview.data;

  // A fresh scan re-seeds the selection. It must NOT clear a shown result: the apply
  // invalidates the preview, and that refetch would otherwise wipe the result view the user
  // is reading.
  useEffect(() => {
    if (pv) setSel(initialSelection(pv));
  }, [pv]);

  useEffect(() => {
    if (status.data && !status.data.connected) {
      void navigate({ to: "/p/$key", params: { key } });
    }
  }, [status.data, navigate, key]);

  const totalChecked = useMemo(() => {
    if (!sel) return 0;
    return (
      sel.issues.size + sel.docs.size + sel.triggers.size + sel.agents.size +
      (sel.overview ? 1 : 0)
    );
  }, [sel]);

  if (status.isPending || (connected && preview.isPending)) {
    return (
      <div className={styles.root} aria-busy="true">
        <p className={styles.quiet}>Scanning the repository…</p>
      </div>
    );
  }
  if (preview.isError) {
    return (
      <div className={styles.root}>
        <p role="alert" className={styles.quiet}>
          The scan failed: {preview.error.message}
        </p>
        <button
          type="button"
          className={styles.secondaryButton}
          onClick={() => void preview.refetch()}
        >
          Retry scan
        </button>
      </div>
    );
  }
  if (!pv || !sel) return <div className={styles.root} aria-busy="true" />;

  if (result) {
    return <ResultView projectKey={key} result={result} />;
  }

  const submit = () => {
    apply.mutate(
      {
        issues: [...sel.issues],
        docs: [...sel.docs].map((path) => ({
          path,
          scope: sel.docScopes[path] ?? "auto",
          paths: pv.docs.find((d) => d.path === path)?.scope_paths ?? [],
        })),
        triggers: [...sel.triggers],
        agents: [...sel.agents],
        overview: sel.overview ? pv.overview.draft : null,
      },
      { onSuccess: setResult },
    );
  };

  return (
    <div className={styles.root}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Set up this project from the repository</h1>
          <p className={styles.quiet}>
            Review what was detected. Only what you check is created — nothing happens
            silently.
          </p>
        </div>
        <div className={styles.headerActions}>
          <button
            type="button"
            className={styles.secondaryButton}
            onClick={() => void preview.refetch()}
            disabled={preview.isFetching}
          >
            {preview.isFetching ? "Scanning…" : "Re-scan repository"}
          </button>
          <button
            type="button"
            className={styles.primaryButton}
            onClick={submit}
            disabled={apply.isPending || totalChecked === 0}
          >
            {apply.isPending
              ? "Importing…"
              : `Import ${totalChecked} item${totalChecked === 1 ? "" : "s"}`}
          </button>
        </div>
      </header>
      {apply.isError && (
        <p role="alert" className={styles.error}>
          Import failed: {apply.error.message}
        </p>
      )}

      <Section
        title="Issues"
        hint="Open GitHub issues become tickets in the backlog."
        items={pv.issues.map((i) => ({ id: String(i.number), imported: i.already_imported }))}
        selected={new Set([...sel.issues].map(String))}
        onAll={(on) =>
          setSel({
            ...sel,
            issues: new Set(
              on ? pv.issues.filter((i) => !i.already_imported).map((i) => i.number) : [],
            ),
          })
        }
      >
        {pv.issues.length === 0 && <p className={styles.quiet}>No open issues.</p>}
        {pv.issues.map((issue) => (
          <label
            key={issue.number}
            className={styles.row}
            data-disabled={issue.already_imported || undefined}
          >
            <input
              type="checkbox"
              checked={sel.issues.has(issue.number)}
              disabled={issue.already_imported}
              onChange={(e) =>
                setSel({ ...sel, issues: toggled(sel.issues, issue.number, e.target.checked) })
              }
            />
            <span className={styles.rowMain}>
              <span className={styles.rowTitle}>
                <span className={styles.mono}>#{issue.number}</span> {issue.title}
              </span>
              <span className={styles.rowMeta}>
                @{issue.author_login}
                {issue.labels.map((l) => (
                  <span key={l} className={styles.chip}>
                    {l}
                  </span>
                ))}
              </span>
            </span>
            {issue.already_imported && (
              <span className={styles.importedBadge}>
                Already imported{issue.ticket_key ? ` · ${issue.ticket_key}` : ""}
              </span>
            )}
          </label>
        ))}
      </Section>

      <Section
        title="Docs"
        hint="Detected instruction files become wiki pages with the scope shown — the scope decides when agents see them."
        items={pv.docs.map((d) => ({ id: d.path, imported: d.already_imported }))}
        selected={sel.docs}
        onAll={(on) =>
          setSel({
            ...sel,
            docs: new Set(
              on ? pv.docs.filter((d) => !d.already_imported).map((d) => d.path) : [],
            ),
          })
        }
      >
        {pv.docs.length === 0 && (
          <p className={styles.quiet}>No instruction docs detected.</p>
        )}
        {pv.docs.map((doc) => (
          <label
            key={doc.path}
            className={styles.row}
            data-disabled={doc.already_imported || undefined}
          >
            <input
              type="checkbox"
              checked={sel.docs.has(doc.path)}
              disabled={doc.already_imported}
              onChange={(e) =>
                setSel({ ...sel, docs: toggled(sel.docs, doc.path, e.target.checked) })
              }
            />
            <span className={styles.rowMain}>
              <span className={styles.rowTitle}>{doc.title}</span>
              <span className={styles.rowMeta}>
                <span className={styles.mono}>{doc.path}</span>
                {doc.scope_paths.length > 0 && (
                  <span className={styles.mono}>{doc.scope_paths.join(", ")}</span>
                )}
              </span>
            </span>
            {doc.already_imported ? (
              <span className={styles.importedBadge}>Already imported</span>
            ) : (
              <select
                aria-label={`Agent scope for ${doc.path}`}
                className={styles.scopeSelect}
                value={sel.docScopes[doc.path] ?? doc.proposed_scope}
                onClick={(e) => e.preventDefault()}
                onChange={(e) =>
                  setSel({
                    ...sel,
                    docScopes: {
                      ...sel.docScopes,
                      [doc.path]: e.target.value as AgentScope,
                    },
                  })
                }
              >
                {SCOPES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            )}
          </label>
        ))}
      </Section>

      <Section
        title="CI triggers"
        hint="CI was detected, so two rules are pre-filled. They are created with their toggles OFF — nothing fires until you enable them."
        items={pv.triggers.map((t) => ({ id: t.id, imported: t.already_created }))}
        selected={sel.triggers}
        onAll={(on) =>
          setSel({
            ...sel,
            triggers: new Set(
              on ? pv.triggers.filter((t) => !t.already_created).map((t) => t.id) : [],
            ),
          })
        }
      >
        {pv.triggers.length === 0 && (
          <p className={styles.quiet}>No CI workflows detected, so no triggers to suggest.</p>
        )}
        {pv.triggers.map((tr) => (
          <label
            key={tr.id}
            className={styles.row}
            data-disabled={tr.already_created || undefined}
          >
            <input
              type="checkbox"
              checked={sel.triggers.has(tr.id)}
              disabled={tr.already_created}
              onChange={(e) =>
                setSel({ ...sel, triggers: toggled(sel.triggers, tr.id, e.target.checked) })
              }
            />
            <span className={styles.rowMain}>
              <span className={styles.rowTitle}>{tr.name}</span>
              <span className={styles.rowMeta}>{tr.description}</span>
            </span>
            {tr.already_created ? (
              <span className={styles.importedBadge}>Already created</span>
            ) : (
              <span className={styles.offBadge}>created off</span>
            )}
          </label>
        ))}
      </Section>

      <Section
        title="Agents"
        hint="A starter roster: Dev implements; Reviewer reviews and structurally cannot edit files."
        items={pv.agents.map((a) => ({ id: a.name, imported: a.already_created }))}
        selected={sel.agents}
        onAll={(on) =>
          setSel({
            ...sel,
            agents: new Set(
              on ? pv.agents.filter((a) => !a.already_created).map((a) => a.name) : [],
            ),
          })
        }
      >
        {pv.agents.map((agent) => (
          <label
            key={agent.name}
            className={styles.row}
            data-disabled={agent.already_created || undefined}
          >
            <input
              type="checkbox"
              checked={sel.agents.has(agent.name)}
              disabled={agent.already_created}
              onChange={(e) =>
                setSel({ ...sel, agents: toggled(sel.agents, agent.name, e.target.checked) })
              }
            />
            <span className={styles.rowMain}>
              <span className={styles.rowTitle}>
                {agent.name} <span className={styles.quiet}>· {agent.role}</span>
              </span>
              <span className={styles.rowMeta}>
                <span className={styles.mono}>{agent.model}</span>
              </span>
            </span>
            {agent.already_created && (
              <span className={styles.importedBadge}>Already created</span>
            )}
          </label>
        ))}
      </Section>

      <section className={styles.section} aria-label="Overview">
        <header className={styles.sectionHeader}>
          <h2 className={styles.sectionTitle}>Overview</h2>
        </header>
        {pv.overview.draft === "" ? (
          <p className={styles.quiet}>The README had nothing to draft a description from.</p>
        ) : (
          <label className={styles.row} data-disabled={undefined}>
            <input
              type="checkbox"
              checked={sel.overview}
              onChange={(e) => setSel({ ...sel, overview: e.target.checked })}
            />
            <span className={styles.rowMain}>
              <span className={styles.rowTitle}>
                Use the README's first section as the project description
                {pv.overview.already_set && (
                  <span className={styles.importedBadge}> replaces the current one</span>
                )}
              </span>
              <span className={styles.draft}>{pv.overview.draft}</span>
            </span>
          </label>
        )}
      </section>
    </div>
  );
}

function Section({
  title,
  hint,
  items,
  selected,
  onAll,
  children,
}: {
  title: string;
  hint: string;
  items: Array<{ id: string; imported: boolean }>;
  selected: Set<string>;
  onAll: (on: boolean) => void;
  children: React.ReactNode;
}) {
  const selectable = items.filter((i) => !i.imported);
  const allOn = selectable.length > 0 && selectable.every((i) => selected.has(i.id));
  return (
    <section className={styles.section} aria-label={title}>
      <header className={styles.sectionHeader}>
        <h2 className={styles.sectionTitle}>
          {title} <span className={styles.count}>{selected.size}/{items.length}</span>
        </h2>
        {selectable.length > 0 && (
          <label className={styles.selectAll}>
            <input
              type="checkbox"
              checked={allOn}
              onChange={(e) => onAll(e.target.checked)}
            />
            Select all
          </label>
        )}
      </header>
      <p className={styles.hint}>{hint}</p>
      <div className={styles.rows}>{children}</div>
    </section>
  );
}

function ResultView({
  projectKey,
  result,
}: {
  projectKey: string;
  result: BootstrapApplyResult;
}) {
  const skipped = result.issues_skipped.length + result.docs_skipped.length;
  return (
    <div className={styles.root}>
      <h1 className={styles.title}>Import complete</h1>
      <ul className={styles.resultList}>
        <li>
          <strong>{result.tickets_created.length}</strong> tickets created
          {result.tickets_created.length > 0 && (
            <>
              {" · "}
              <Link to="/p/$key/board" params={{ key: projectKey }}>
                open the board
              </Link>
            </>
          )}
        </li>
        <li>
          <strong>{result.pages_created.length}</strong> wiki pages created
          {result.pages_created.length > 0 && (
            <>
              {" · "}
              <Link to="/p/$key/wiki" params={{ key: projectKey }}>
                open the wiki
              </Link>
            </>
          )}
        </li>
        <li>
          <strong>{result.triggers_created.length}</strong> triggers created, all disabled
          {result.triggers_created.length > 0 && (
            <>
              {" · "}
              <Link to="/p/$key/triggers" params={{ key: projectKey }}>
                review and enable
              </Link>
            </>
          )}
        </li>
        <li>
          <strong>{result.agents_created.length}</strong> agents created
          {result.agents_created.length > 0 && (
            <>
              {" · "}
              <Link to="/p/$key/agents" params={{ key: projectKey }}>
                meet the roster
              </Link>
            </>
          )}
        </li>
        <li>
          Project description {result.overview_set ? "updated from the README" : "left unchanged"}
        </li>
        {skipped > 0 && (
          <li className={styles.quiet}>{skipped} already-imported items were skipped.</li>
        )}
      </ul>
      <Link to="/p/$key" params={{ key: projectKey }} className={styles.primaryButton}>
        Go to project overview
      </Link>
    </div>
  );
}
