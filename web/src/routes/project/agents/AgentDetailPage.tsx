/*
 * Agent detail (UI spec §5.8), sections in exact spec order: Identity · Directive · Model &
 * effort · Permissions · Autonomy · Limits · Context preview (placeholder until S34). The
 * sections themselves are presentational (sections.tsx); this page wires the queries and the
 * PATCH / directive-save mutations.
 */
import { useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { ApiProblem, type UpdateAgentRequest } from "../../../lib/api/client";
import {
  useAgentQuery,
  useDirectivesQuery,
  useSaveDirective,
  useUpdateAgent,
} from "../../../lib/api/agentQueries";
import styles from "./agents.module.css";
import {
  AutonomySection,
  ContextPreviewSection,
  DirectiveSection,
  IdentitySection,
  LimitsSection,
  ModelSection,
  PermissionRulesSection,
  PermissionsSection,
} from "./sections";

export function AgentDetailPage() {
  const { key, id } = useParams({ from: "/shell/p/$key/agents/$id" });
  const agentQuery = useAgentQuery(id);
  const directivesQuery = useDirectivesQuery(id);
  const update = useUpdateAgent(key);
  const saveDirective = useSaveDirective(id);

  const [error, setError] = useState<string | null>(null);
  const [saveFlash, setSaveFlash] = useState<string | null>(null);

  // The editor's draft. Seeded from the current version once it loads; kept across saves.
  const [draft, setDraft] = useState<string | null>(null);
  const versions = directivesQuery.data?.directives ?? [];
  const current = versions.find((d) => d.id === agentQuery.data?.directive_version_id);
  useEffect(() => {
    if (draft === null && directivesQuery.data !== undefined) {
      setDraft(current?.body ?? "");
    }
  }, [draft, directivesQuery.data, current]);

  if (agentQuery.isPending || directivesQuery.isPending) {
    return <div className={styles.page}>Loading…</div>;
  }
  if (agentQuery.isError || directivesQuery.isError) {
    return <div className={styles.page}>The agent failed to load.</div>;
  }
  const agent = agentQuery.data;

  const patch = (body: UpdateAgentRequest) => {
    setError(null);
    update.mutate(
      { id, body },
      {
        onError: (err) => {
          setError(
            err instanceof ApiProblem ? err.detail || err.title : "The change did not save.",
          );
        },
      },
    );
  };

  return (
    <div className={styles.page}>
      <div className={styles.pageTitle}>
        <h1>
          {agent.name}
          {!agent.enabled && " (disabled)"}
        </h1>
      </div>
      {error !== null && <p className={styles.error}>{error}</p>}
      {saveFlash !== null && <p className={styles.sectionHint}>{saveFlash}</p>}
      <div className={styles.sections}>
        <IdentitySection agent={agent} onPatch={patch} />
        <DirectiveSection
          value={draft ?? ""}
          onChange={setDraft}
          saving={saveDirective.isPending}
          versions={versions}
          currentVersionId={agent.directive_version_id}
          onSave={(note) => {
            setError(null);
            saveDirective.mutate(
              { body: draft ?? "", note },
              {
                onSuccess: (res) => {
                  setSaveFlash(
                    res.created
                      ? `Saved v${res.version}.`
                      : "No changes — the current version stands.",
                  );
                },
                onError: (err) => {
                  setError(
                    err instanceof ApiProblem
                      ? err.detail || err.title
                      : "The directive did not save.",
                  );
                },
              },
            );
          }}
        />
        <ModelSection agent={agent} onPatch={patch} />
        <PermissionsSection
          permissions={agent.permissions}
          onChange={(permissions) => patch({ permissions })}
        />
        <PermissionRulesSection agentID={agent.id} />
        <AutonomySection autonomy={agent.autonomy} onChange={(autonomy) => patch({ autonomy })} />
        <LimitsSection agent={agent} onPatch={patch} />
        <ContextPreviewSection agentId={agent.id} />
      </div>
    </div>
  );
}
