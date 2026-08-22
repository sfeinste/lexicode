/*
 * Workspace settings — `/settings` (UI spec §1). S08 lands the Defaults pane: the single
 * workspace_settings row every project inherits from. Owner-only — the API answers 403 for
 * members, and the screen says so instead of rendering dead controls. Members, integrations
 * and the audit log arrive with later stories.
 *
 * Autosave per §5.11: every control saves itself, inline "Saved" indicator, no Save button.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { SaveStatus } from "../../components/SaveStatus/SaveStatus";
import { SecretsPanel } from "../../components/SecretsPanel/SecretsPanel";
import {
  authApi,
  secretsApi,
  usersApi,
  type UpdateWorkspaceSettingsRequest,
  type WorkspaceSettings,
} from "../../lib/api/client";
import {
  useUpdateWorkspaceSettings,
  useWorkspaceSettingsQuery,
} from "../../lib/api/projectQueries";
import { useAutosave } from "../../lib/autosave";
import { CredentialsSection } from "./CredentialsSection";
import styles from "./workspace.module.css";

export function WorkspaceSettingsPage() {
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: ({ signal }) => authApi.me(signal),
    staleTime: 5 * 60_000,
  });
  const isOwner = me.data?.role === "owner";
  const settings = useWorkspaceSettingsQuery(isOwner);

  if (me.isPending) {
    return <div className={styles.root} aria-busy="true" />;
  }
  if (!isOwner) {
    return (
      <div className={styles.root}>
        <h1 className={styles.title}>Workspace settings</h1>
        <p className={styles.quiet}>Only the workspace owner can change these settings.</p>
      </div>
    );
  }
  if (settings.isPending) {
    return <div className={styles.root} aria-busy="true" />;
  }
  if (settings.isError) {
    return (
      <div className={styles.root}>
        <h1 className={styles.title}>Workspace settings</h1>
        <p role="alert" className={styles.quiet}>
          Settings could not load: {settings.error.message}
        </p>
      </div>
    );
  }

  return <SettingsForm settings={settings.data} />;
}

/**
 * Members (S37 slice): the directory plus the owner's "revoke sessions" action — the lost
 * laptop / departing teammate lever. Revocation is immediate and audited.
 */
function MembersSection() {
  const qc = useQueryClient();
  const members = useQuery({
    queryKey: ["users", "list"],
    queryFn: ({ signal }) => usersApi.list(signal),
  });
  const [done, setDone] = useState<string | null>(null);
  const revoke = useMutation({
    mutationFn: (id: string) => usersApi.revokeSessions(id),
    onSuccess: (res, id) => {
      setDone(`Revoked ${res.revoked} session${res.revoked === 1 ? "" : "s"}.`);
      void qc.invalidateQueries({ queryKey: ["users"] });
      void id;
    },
  });

  return (
    <section aria-label="Members" className={styles.secretsSection}>
      <h2 className={styles.sectionTitle}>Members</h2>
      <p className={styles.lede}>
        Revoking sessions signs a member out everywhere immediately; they can sign back in
        with their password. The revocation is audited.
      </p>
      {members.data?.users.map((u) => (
        <div key={u.id} className={styles.memberRow}>
          <span className={styles.memberAvatar} style={{ background: u.avatar_color }}>
            {u.display_name.slice(0, 1)}
          </span>
          <span className={styles.memberName}>{u.display_name}</span>
          <button
            type="button"
            className={styles.revokeButton}
            disabled={revoke.isPending}
            onClick={() => revoke.mutate(u.id)}
          >
            Revoke sessions
          </button>
        </div>
      ))}
      {done !== null && (
        <p className={styles.quiet} role="status">
          {done}
        </p>
      )}
      {revoke.isError && (
        <p className={styles.quiet} role="alert">
          Revocation failed: {revoke.error.message}
        </p>
      )}
    </section>
  );
}

function SettingsForm({ settings }: { settings: WorkspaceSettings }) {
  const update = useUpdateWorkspaceSettings();
  const autosave = useAutosave<UpdateWorkspaceSettingsRequest>((patch) =>
    update.mutateAsync(patch),
  );

  const num =
    (field: keyof UpdateWorkspaceSettingsRequest) =>
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const v = e.target.valueAsNumber;
      if (!Number.isNaN(v)) autosave.queue({ [field]: Math.round(v) });
    };

  return (
    <div className={styles.root}>
      <header className={styles.header}>
        <h1 className={styles.title}>Workspace settings</h1>
        <SaveStatus status={autosave.status} error={autosave.error} />
      </header>
      <nav className={styles.settingsLinks} aria-label="Workspace settings pages">
        <Link to="/settings/audit">Audit log →</Link>
      </nav>
      <p className={styles.lede}>
        Defaults every project inherits. A project that has not overridden a value follows
        changes made here immediately.
      </p>

      <div className={styles.fields}>
        <label className={styles.field}>
          Default branch
          <input
            defaultValue={settings.default_branch}
            onChange={(e) => autosave.queue({ default_branch: e.target.value })}
          />
        </label>
        <label className={styles.field}>
          Branch naming template
          <input
            className={styles.mono}
            defaultValue={settings.default_branch_template}
            onChange={(e) => autosave.queue({ default_branch_template: e.target.value })}
          />
          <span className={styles.hint}>
            Placeholders: {"{agent}"}, {"{ticket-key}"}, {"{slug}"}
          </span>
        </label>
        <label className={styles.field}>
          Default network policy
          <select
            defaultValue={settings.default_network_policy}
            onChange={(e) => {
              autosave.queue({
                default_network_policy: e.target
                  .value as UpdateWorkspaceSettingsRequest["default_network_policy"],
              });
              autosave.flush();
            }}
          >
            <option value="none">none — no network</option>
            <option value="allowlist">allowlist — approved hosts only</option>
            <option value="open">open — unrestricted</option>
          </select>
        </label>
        <label className={styles.field}>
          Default daily budget (USD, per project)
          <input
            type="number"
            min={0}
            step="0.5"
            defaultValue={settings.default_daily_budget_cents / 100}
            onChange={(e) => {
              const v = e.target.valueAsNumber;
              if (!Number.isNaN(v))
                autosave.queue({ default_daily_budget_cents: Math.round(v * 100) });
            }}
          />
        </label>
        <label className={styles.field}>
          Default context threshold (tokens)
          <input
            type="number"
            min={0}
            defaultValue={settings.default_context_threshold_tokens}
            onChange={num("default_context_threshold_tokens")}
          />
        </label>
        <label className={styles.field}>
          Default verification period (days)
          <input
            type="number"
            min={1}
            defaultValue={settings.default_verification_days}
            onChange={num("default_verification_days")}
          />
        </label>
        <label className={styles.field}>
          Max concurrent containers
          <input
            type="number"
            min={1}
            defaultValue={settings.max_concurrent_containers}
            onChange={num("max_concurrent_containers")}
          />
        </label>
        <label className={styles.field}>
          Poll interval (seconds)
          <input
            type="number"
            min={1}
            defaultValue={settings.poll_interval_seconds}
            onChange={num("poll_interval_seconds")}
          />
        </label>
        <label className={styles.field}>
          PR size warning (changed lines)
          <input
            type="number"
            min={0}
            defaultValue={settings.pr_size_warning_lines}
            onChange={num("pr_size_warning_lines")}
          />
          <span className={styles.hint}>
            Agent PRs above this many changed lines get a large-diff warning chip. 0 disables.
            Projects may override.
          </span>
        </label>
      </div>

      <MembersSection />

      {/*
        Claude credentials (S19, D-5): the pasted `claude setup-token` output, with health.
        Owner-only like the rest of this screen.
      */}
      <CredentialsSection />

      {/*
        Workspace-scope secrets (S13, D-16): the schema's other scope. Owner-only like the
        rest of this screen; the API refuses members regardless. Same write-only contract as
        project secrets — names and set-dates are all that ever renders.
      */}
      <section aria-label="Workspace secrets" className={styles.secretsSection}>
        <h2 className={styles.sectionTitle}>Secrets</h2>
        <p className={styles.lede}>
          Available to every project. Values are encrypted at rest and can never be viewed
          again after saving — only replaced or deleted.
        </p>
        <SecretsPanel
          queryKey={["secrets", "workspace"]}
          api={{
            list: (signal) => secretsApi.workspaceList(signal),
            set: (body) => secretsApi.workspaceSet(body),
            remove: (id) => secretsApi.workspaceRemove(id),
          }}
        />
      </section>
    </div>
  );
}
