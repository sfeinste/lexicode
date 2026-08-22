/*
 * Project settings — `/p/:key/settings` (UI spec §5.11): a left rail of sections, one
 * scrollable pane each, autosave with an inline saved indicator, no global Save button.
 *
 * S08 landed the shell and the General section; S09 the Board section (columns, categories,
 * WIP, auto-start). The other §5.11 sections are rendered in the rail but disabled until
 * their owning stories arrive (Wiki → S17-ish, Repository → S14, and so on) — rendering them
 * disabled keeps the §5.11 geography visible from day one without dead links.
 *
 * The three inheritable settings (daily budget, context threshold, verification days) live
 * here under General for now; later stories may move budget/wiki fields into their §5.11
 * sections as those panes land. Each renders through <InheritedField>, backed by the API's
 * {value, inherited, workspace_value} triple.
 */
import { useParams } from "@tanstack/react-router";

import { InheritedField } from "../../../components/InheritedField/InheritedField";
import { SaveStatus } from "../../../components/SaveStatus/SaveStatus";
import type { InheritedInt, Project, UpdateProjectRequest } from "../../../lib/api/client";
import { useProjectQuery, useUpdateProject } from "../../../lib/api/projectQueries";
import { useAutosave } from "../../../lib/autosave";
import { BoardSection } from "./BoardSection";
import { RepositorySection } from "./RepositorySection";
import { SecretsSection } from "./SecretsSection";
import styles from "./settings.module.css";

/** The §5.11 rail. Sections without an owning story yet render disabled. */
const SECTIONS: Array<{ id: string; label: string; enabled: boolean }> = [
  { id: "general", label: "General", enabled: true },
  { id: "board", label: "Board", enabled: true },
  { id: "secrets", label: "Secrets", enabled: true },
  { id: "wiki", label: "Wiki", enabled: false },
  { id: "repository", label: "Repository", enabled: true },
  { id: "agents", label: "Agents", enabled: false },
  { id: "triggers", label: "Triggers", enabled: false },
  { id: "members", label: "Members & access", enabled: false },
  { id: "notifications", label: "Notifications", enabled: false },
  { id: "danger", label: "Danger zone", enabled: false },
];

export function ProjectSettingsPage() {
  const params = useParams({ strict: false });
  const key = params.key ?? "";
  const section = (params as { _splat?: string })._splat || "general";
  const project = useProjectQuery(key);

  if (project.isPending) {
    return <div className={styles.root} aria-busy="true" />;
  }
  if (project.isError) {
    return (
      <div className={styles.root}>
        <p role="alert" className={styles.quiet}>
          Settings could not load: {project.error.message}
        </p>
      </div>
    );
  }

  return (
    <div className={styles.root}>
      <nav className={styles.rail} aria-label="Settings sections">
        {SECTIONS.map((s) =>
          s.enabled ? (
            <a
              key={s.id}
              href={`/p/${key}/settings${s.id === "general" ? "" : `/${s.id}`}`}
              className={styles.railLink}
              data-active={(section === s.id || (s.id === "general" && !section)) || undefined}
            >
              {s.label}
            </a>
          ) : (
            <span key={s.id} className={styles.railDisabled} aria-disabled="true">
              {s.label}
            </span>
          ),
        )}
      </nav>
      <div className={styles.pane}>
        {section === "board" ? (
          <BoardSection projectKey={key} />
        ) : section === "secrets" ? (
          <SecretsSection projectKey={key} />
        ) : section === "repository" ? (
          <RepositorySection projectKey={key} />
        ) : (
          <GeneralSection project={project.data} />
        )}
      </div>
    </div>
  );
}

function GeneralSection({ project }: { project: Project }) {
  const update = useUpdateProject(project.key);
  const autosave = useAutosave<UpdateProjectRequest>((patch) => update.mutateAsync(patch));

  const s = project.settings;

  return (
    <section aria-label="General">
      <header className={styles.paneHeader}>
        <h1 className={styles.paneTitle}>General</h1>
        <SaveStatus status={autosave.status} error={autosave.error} />
      </header>

      <div className={styles.fields}>
        <label className={styles.field}>
          Name
          <input
            key={`name-${project.id}`}
            defaultValue={project.name}
            onChange={(e) => autosave.queue({ name: e.target.value })}
          />
        </label>

        <label className={styles.field}>
          Description
          <textarea
            key={`description-${project.id}`}
            defaultValue={project.description}
            rows={3}
            onChange={(e) => autosave.queue({ description: e.target.value })}
          />
        </label>

        <label className={styles.field}>
          Color
          <input
            key={`color-${project.id}-${project.color}`}
            type="color"
            defaultValue={project.color}
            className={styles.colorInput}
            onChange={(e) => autosave.queue({ color: e.target.value })}
          />
        </label>

        <label className={styles.field}>
          Agent guidance
          <textarea
            key={`guidance-${project.id}`}
            defaultValue={project.agent_guidance}
            rows={4}
            placeholder="Project-wide prompt preamble for every agent."
            onChange={(e) => autosave.queue({ agent_guidance: e.target.value })}
          />
        </label>

        <InheritedNumberField
          label="Daily budget (USD)"
          field={s.daily_budget_cents}
          scale={100}
          step="0.5"
          format={(cents) => `$${(cents / 100).toFixed(2)}`}
          onChange={(v) => autosave.queue({ daily_budget_cents: v })}
          flush={autosave.flush}
        />
        <InheritedNumberField
          label="Context threshold (tokens)"
          field={s.context_threshold_tokens}
          format={(v) => String(v)}
          onChange={(v) => autosave.queue({ context_threshold_tokens: v })}
          flush={autosave.flush}
        />
        <InheritedNumberField
          label="Verification period (days)"
          field={s.verification_days}
          format={(v) => String(v)}
          onChange={(v) => autosave.queue({ verification_days: v })}
          flush={autosave.flush}
        />

        <div className={styles.archiveRow}>
          {project.archived_at ? (
            <button
              className={styles.secondaryButton}
              onClick={() => {
                autosave.queue({ archived: false });
                autosave.flush();
              }}
            >
              Unarchive project
            </button>
          ) : (
            <button
              className={styles.dangerButton}
              onClick={() => {
                autosave.queue({ archived: true });
                autosave.flush();
              }}
            >
              Archive project
            </button>
          )}
          <span className={styles.quiet}>
            Archived projects disappear from Home and the rail; nothing is deleted.
          </span>
        </div>
      </div>
    </section>
  );
}

/**
 * One inheritable numeric setting: the control plus the InheritedField line. While inherited
 * the input is disabled and mirrors the live workspace value; "Override." writes the current
 * effective value as an override, "Reset to workspace default." clears it back to null.
 */
function InheritedNumberField({
  label,
  field,
  onChange,
  flush,
  format,
  scale = 1,
  step,
}: {
  label: string;
  field: InheritedInt;
  /** Called with the raw (unscaled) value to store, or null to revert to inherit. */
  onChange: (value: number | null) => void;
  /** Saves immediately — Override / Reset are discrete actions, not typing. */
  flush: () => void;
  /** Renders the workspace default for the spec's wording line. */
  format: (raw: number) => string;
  /** Display divisor: cents render as dollars with scale 100. */
  scale?: number;
  step?: string;
}) {
  return (
    <InheritedField
      label={label}
      inherited={field.inherited}
      workspaceValue={format(field.workspace_value)}
      onOverride={() => {
        onChange(field.workspace_value);
        flush();
      }}
      onReset={() => {
        onChange(null);
        flush();
      }}
    >
      {field.inherited ? (
        // Disabled mirror of the live workspace value; editing starts with "Override.".
        <input type="number" disabled readOnly value={field.value / scale} />
      ) : (
        <input
          key={`${label}-override`}
          type="number"
          step={step}
          min={0}
          defaultValue={field.value / scale}
          onChange={(e) => {
            const v = e.target.valueAsNumber;
            if (!Number.isNaN(v)) onChange(Math.round(v * scale));
          }}
        />
      )}
    </InheritedField>
  );
}
