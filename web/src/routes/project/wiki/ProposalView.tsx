/*
 * The agent-proposal review view (S35, UI spec §5.6): a proposed page renders as a DIFF for
 * edit-proposals (proposed body vs the current live body) and as the full body for new-page
 * proposals, with the agent's reason, a link to the proposing run, and Accept / Edit /
 * Dismiss. Never auto-written (interaction rule 10) — every path out of here is a human
 * decision.
 *
 * The three-way check surfaces up front: when the target has advanced past the version the
 * proposal was written against (base ≠ current), the view shows BOTH diffs — what the
 * proposal wanted against its base, and what the live page did in the meantime — and Accept
 * is the server-checked 409 path; the human resolves by Edit.
 */
import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";

import { Editor } from "../../../components/Editor/Editor";
import { MarkdownView } from "../../../components/Editor/MarkdownView";
import { ScopeBadge } from "../../../components/ScopeBadge/ScopeBadge";
import { ApiProblem, type WikiPageDetail } from "../../../lib/api/client";
import {
  useAcceptProposal,
  useDismissProposal,
  useUpdateWikiPage,
} from "../../../lib/api/wikiQueries";
import { ProposalDiff } from "./ProposalDiff";
import styles from "./wiki.module.css";

export function ProposalView({
  projectKey,
  detail,
}: {
  projectKey: string;
  detail: WikiPageDetail;
}) {
  const page = detail.page;
  const info = detail.proposal;
  const navigate = useNavigate();
  const accept = useAcceptProposal(projectKey);
  const dismiss = useDismissProposal(projectKey);
  const update = useUpdateWikiPage(projectKey);

  const [actionError, setActionError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [bodyDraft, setBodyDraft] = useState(page.body);

  const isEdit = info?.target_id != null;
  const conflict =
    isEdit && (info?.base_version ?? 0) !== (info?.current_version ?? 0);

  const onError = (err: unknown) =>
    setActionError(err instanceof ApiProblem ? err.detail || err.title : "That did not work.");

  const onAccept = () => {
    setActionError(null);
    accept.mutate(page.id, {
      onSuccess: (res) =>
        void navigate({
          to: "/p/$key/wiki/$slug",
          params: { key: projectKey, slug: res.page.slug },
        }),
      onError,
    });
  };

  const onDismiss = () => {
    setActionError(null);
    dismiss.mutate(page.id, {
      onSuccess: () => void navigate({ to: "/p/$key/wiki", params: { key: projectKey } }),
      onError,
    });
  };

  const saveEdit = () => {
    setEditing(false);
    if (bodyDraft === page.body) return;
    setActionError(null);
    update.mutate({ id: page.id, body: { body: bodyDraft } }, { onError });
  };

  const busy = accept.isPending || dismiss.isPending || update.isPending;

  return (
    <main className={styles.main} aria-label={`Wiki proposal ${page.title}`}>
      <div className={styles.proposedBanner} data-testid="proposal-banner">
        <strong>PROPOSED</strong>
        {" — "}
        {isEdit
          ? `an agent proposed changes to ${info?.target_title ?? "a page"}.`
          : "an agent proposed this new page."}{" "}
        Nothing is written until you accept.
        {info !== undefined && info.reason !== "" && (
          <div className={styles.proposalReason}>Reason: {info.reason}</div>
        )}
        {info?.run_id != null && (
          <div>
            <Link
              to="/p/$key/runs/$id"
              params={{ key: projectKey, id: info.run_id }}
              className={styles.proposalRunLink}
            >
              View the proposing run
            </Link>
          </div>
        )}
      </div>

      <header className={styles.pageHeader}>
        <div className={styles.titleRow}>
          <h1 className={styles.pageTitle}>{page.title}</h1>
          <ScopeBadge scope={page.agent_scope} />
        </div>
        {actionError !== null && (
          <div className={styles.error} role="alert">
            {actionError}
          </div>
        )}
      </header>

      {conflict && info !== undefined && (
        <div className={styles.conflictBanner} role="alert" data-testid="proposal-conflict">
          The page has changed since this was proposed: {info.target_title} is now at version{" "}
          {info.current_version}, but the proposal was written against version{" "}
          {info.base_version}. Accepting as-is would clobber the newer edits, so it is
          refused — review both diffs below and use Edit to bring the proposal up to date.
        </div>
      )}

      {editing ? (
        <>
          <Editor
            value={bodyDraft}
            onChange={setBodyDraft}
            mentions={{ users: [], agents: [], wiki: [], tickets: [] }}
            ariaLabel="Proposal body"
            autoFocus
            minRows={12}
          />
          <div className={styles.editActions}>
            <button type="button" className={styles.primaryBtn} onClick={saveEdit}>
              Save
            </button>
            <button type="button" className={styles.smallBtn} onClick={() => setEditing(false)}>
              Cancel
            </button>
          </div>
        </>
      ) : (
        <>
          {!isEdit && <MarkdownView markdown={page.body} />}
          {isEdit && !conflict && info !== undefined && (
            <ProposalDiff
              label={`Proposed changes to ${info.target_title}`}
              from={info.target_body ?? ""}
              to={page.body}
            />
          )}
          {isEdit && conflict && info !== undefined && (
            <>
              <ProposalDiff
                label={`What the proposal changes (against version ${info.base_version})`}
                from={info.base_body ?? ""}
                to={page.body}
              />
              <ProposalDiff
                label={`What the live page changed since (version ${info.base_version} → ${info.current_version})`}
                from={info.base_body ?? ""}
                to={info.target_body ?? ""}
              />
            </>
          )}
          <div className={styles.proposalActions}>
            <button
              type="button"
              className={styles.primaryBtn}
              disabled={busy}
              onClick={onAccept}
            >
              Accept
            </button>
            <button
              type="button"
              className={styles.smallBtn}
              disabled={busy}
              onClick={() => {
                setBodyDraft(page.body);
                setEditing(true);
              }}
            >
              Edit
            </button>
            <button type="button" className={styles.smallBtn} disabled={busy} onClick={onDismiss}>
              Dismiss
            </button>
          </div>
        </>
      )}
    </main>
  );
}
