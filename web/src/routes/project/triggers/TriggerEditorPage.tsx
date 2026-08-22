/*
 * The trigger editor page: `new` creates, an id edits. The form itself (TriggerForm) is
 * catalog-generated and router-free; this page owns loading, the draft state, save with
 * per-section validation surfacing (the server's field errors map onto the WHEN/IF/THEN
 * sections), and — for existing rules — the firing history with inline loop chains.
 */
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useState } from "react";

import { ApiProblem } from "../../../lib/api/client";
import { useEligibleAgents } from "../../../lib/api/agentQueries";
import {
  useCreateTrigger,
  useTriggerCatalogQuery,
  useTriggerFiringsQuery,
  useTriggerQuery,
  useUpdateTrigger,
} from "../../../lib/api/triggerQueries";
import { draftFromTrigger, draftToInput, emptyDraft } from "./draft";
import { FiringHistory } from "./FiringHistory";
import { TriggerForm, type FieldErrors, type TriggerDraft } from "./TriggerForm";
import styles from "./triggers.module.css";

export function TriggerEditorPage() {
  const { key, id } = useParams({ from: "/shell/p/$key/triggers/$id" });
  const isNew = id === "new";
  const navigate = useNavigate();

  const catalog = useTriggerCatalogQuery(key);
  const trigger = useTriggerQuery(id, !isNew);
  const firings = useTriggerFiringsQuery(id, !isNew);
  const { agents } = useEligibleAgents(key);
  const create = useCreateTrigger(key);
  const update = useUpdateTrigger(key);

  const [draft, setDraft] = useState<TriggerDraft | null>(isNew ? emptyDraft() : null);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [saveError, setSaveError] = useState<string | null>(null);

  if (catalog.isPending || (!isNew && trigger.isPending)) {
    return <p className={styles.muted}>Loading…</p>;
  }
  if (catalog.isError || catalog.data === undefined || (!isNew && (trigger.isError || trigger.data === undefined))) {
    return <p className={styles.errorText}>The trigger editor failed to load.</p>;
  }

  const current = draft ?? draftFromTrigger(trigger.data as NonNullable<typeof trigger.data>);
  const saving = create.isPending || update.isPending;

  const save = () => {
    setErrors({});
    setSaveError(null);
    const body = draftToInput(current);
    const done = () => void navigate({ to: "/p/$key/triggers", params: { key } });
    const fail = (err: unknown) => {
      if (err instanceof ApiProblem && err.errors !== undefined) {
        const fieldErrors: FieldErrors = {};
        for (const fe of err.errors) {
          (fieldErrors[fe.field] ??= []).push(fe.message);
        }
        setErrors(fieldErrors);
      } else {
        setSaveError(err instanceof Error ? err.message : "Save failed.");
      }
    };
    if (isNew) {
      create.mutate(body, { onSuccess: done, onError: fail });
    } else {
      update.mutate({ id, body }, { onSuccess: done, onError: fail });
    }
  };

  return (
    <div className={styles.page}>
      <div className={styles.pageTitle}>
        <Link to="/p/$key/triggers" params={{ key }} className={styles.backLink}>
          ← Triggers
        </Link>
        <h1>{isNew ? "New trigger" : current.name || "Edit trigger"}</h1>
        <div className={styles.titleActions}>
          <button type="button" className={styles.saveButton} onClick={save} disabled={saving}>
            {saving ? "Saving…" : isNew ? "Create trigger" : "Save changes"}
          </button>
        </div>
      </div>
      {saveError !== null && <p className={styles.errorText}>{saveError}</p>}

      <TriggerForm
        catalog={catalog.data}
        agents={agents.map((a) => ({ id: a.id, name: a.name }))}
        draft={current}
        onChange={setDraft}
        errors={errors}
      />

      {!isNew && (
        <section className={styles.historySection} aria-label="Firing history">
          <h2 className={styles.sectionHead}>Firing history</h2>
          {firings.isPending ? (
            <p className={styles.muted}>Loading history…</p>
          ) : (
            <FiringHistory projectKey={key} firings={firings.data?.firings ?? []} />
          )}
        </section>
      )}
    </div>
  );
}
