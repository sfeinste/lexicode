/*
 * The §5.8 agent-detail sections, in spec order: Identity · Directive · Model & effort ·
 * Permissions · Autonomy · Limits · Context preview. Presentational — the page wires the
 * mutations — so the D7 styling contract (permissions must be unmistakably distinct from the
 * directive editor) is testable in isolation.
 *
 * Two deliberate decisions, documented here:
 *
 * - The directive is a plain monospace <textarea>, not the shared Editor component. The
 *   Editor's behaviors are ticket-comment semantics — @ mentions that stage runs and slash
 *   commands that insert criteria — which are wrong inside a system prompt, and its mention
 *   menu would render misleading empty states. A raw monospace surface is what "the system
 *   prompt" should feel like.
 * - The live token count uses the same documented chars/4 heuristic the server stamps on
 *   saved versions (agents.EstimateTokens), computed locally so it updates on every
 *   keystroke with zero latency and can never disagree with the saved value.
 */
import { useMemo, useState } from "react";

import type { Agent, AgentAutonomy, AgentPermissions, Directive } from "../../../lib/api/client";
import styles from "./agents.module.css";
import {
  AUTONOMY_STOPS,
  EFFORTS,
  IDENTITY_LINE,
  MODELS,
  estimateTokens,
} from "./constants";
import { diffLines } from "./diff";

// ---- Identity ---------------------------------------------------------------------------

export function IdentitySection({
  agent,
  onPatch,
}: {
  agent: Agent;
  onPatch: (patch: {
    name?: string;
    role?: string;
    color?: string;
    git_author_name?: string;
    git_author_email?: string;
  }) => void;
}) {
  return (
    <section className={styles.section} aria-label="Identity" data-section="identity">
      <h2>Identity</h2>
      <div className={styles.formGrid}>
        <label className={styles.field}>
          Name
          <input
            defaultValue={agent.name}
            onBlur={(e) => {
              if (e.target.value !== agent.name) onPatch({ name: e.target.value });
            }}
          />
        </label>
        <label className={styles.field}>
          Role
          <input
            defaultValue={agent.role}
            onBlur={(e) => {
              if (e.target.value !== agent.role) onPatch({ role: e.target.value });
            }}
          />
        </label>
        <label className={styles.field}>
          Color
          <input
            type="color"
            defaultValue={agent.color}
            onBlur={(e) => {
              if (e.target.value !== agent.color) onPatch({ color: e.target.value });
            }}
          />
        </label>
        <label className={styles.field}>
          Git author name
          <input
            defaultValue={agent.git_author_name}
            onBlur={(e) => {
              if (e.target.value !== agent.git_author_name)
                onPatch({ git_author_name: e.target.value });
            }}
          />
        </label>
        <label className={styles.field}>
          Git author email
          <input
            defaultValue={agent.git_author_email}
            onBlur={(e) => {
              if (e.target.value !== agent.git_author_email)
                onPatch({ git_author_email: e.target.value });
            }}
          />
        </label>
      </div>
      <p className={styles.identityLine}>{IDENTITY_LINE}</p>
    </section>
  );
}

// ---- Directive --------------------------------------------------------------------------

export function DirectiveSection({
  value,
  onChange,
  onSave,
  saving,
  versions,
  currentVersionId,
}: {
  value: string;
  onChange: (v: string) => void;
  onSave: (note: string) => void;
  saving: boolean;
  versions: Directive[];
  currentVersionId: string | null;
}) {
  const [note, setNote] = useState("");
  // The diff target: show what version N changed relative to N-1 (v1 diffs against empty).
  const [diffVersion, setDiffVersion] = useState<number | null>(null);

  const diff = useMemo(() => {
    if (diffVersion === null) return null;
    const to = versions.find((d) => d.version === diffVersion);
    if (!to) return null;
    const from = versions.find((d) => d.version === diffVersion - 1);
    return diffLines(from?.body ?? "", to.body);
  }, [diffVersion, versions]);

  return (
    <section className={styles.section} aria-label="Directive" data-section="directive">
      <h2>Directive</h2>
      <p className={styles.sectionHint}>
        The system prompt. Saved versions are append-only; saving an unchanged directive
        creates no new version.
      </p>
      <textarea
        className={styles.directiveEditor}
        aria-label="Directive body"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        spellCheck={false}
      />
      <div className={styles.directiveFoot}>
        <input
          className={styles.saveNote}
          aria-label="Version note"
          placeholder="Version note (optional)"
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
        <button
          type="button"
          disabled={saving}
          onClick={() => {
            onSave(note);
            setNote("");
          }}
        >
          {saving ? "Saving…" : "Save version"}
        </button>
        <span className={styles.tokenCount} aria-label="Token estimate">
          ~{estimateTokens(value)} tokens
        </span>
      </div>

      {versions.length > 0 && (
        <ul className={styles.versionList} aria-label="Directive versions">
          {versions.map((d) => (
            <li key={d.id} className={styles.versionRow}>
              <span className={d.id === currentVersionId ? styles.versionCurrent : undefined}>
                v{d.version}
                {d.id === currentVersionId ? " (current)" : ""}
              </span>
              <span>~{d.token_estimate} tokens</span>
              {d.note !== "" && <span>{d.note}</span>}
              <span>{d.created_at.slice(0, 10)}</span>
              <button
                type="button"
                onClick={() => setDiffVersion(diffVersion === d.version ? null : d.version)}
              >
                {diffVersion === d.version ? "hide diff" : "diff"}
              </button>
            </li>
          ))}
        </ul>
      )}

      {diff !== null && (
        <div className={styles.diffView} aria-label={`Diff for version ${diffVersion}`}>
          <pre>
            {diff.map((l, i) => (
              <span
                key={i}
                className={
                  l.op === "add" ? styles.diffAdd : l.op === "del" ? styles.diffDel : styles.diffCtx
                }
              >
                {(l.op === "add" ? "+ " : l.op === "del" ? "- " : "  ") + l.text}
              </span>
            ))}
          </pre>
        </div>
      )}
    </section>
  );
}

// ---- Model & effort ---------------------------------------------------------------------

export function ModelSection({
  agent,
  onPatch,
}: {
  agent: Agent;
  onPatch: (patch: { model?: string; effort?: string }) => void;
}) {
  // A stored model outside the picker list (older data) still renders rather than silently
  // snapping to another model.
  const models = MODELS.includes(agent.model) ? MODELS : [agent.model, ...MODELS];
  return (
    <section className={styles.section} aria-label="Model and effort" data-section="model">
      <h2>Model &amp; effort</h2>
      <div className={styles.formGrid}>
        <label className={styles.field}>
          Model
          <select value={agent.model} onChange={(e) => onPatch({ model: e.target.value })}>
            {models.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <label className={styles.field}>
          Thinking effort
          <select value={agent.effort} onChange={(e) => onPatch({ effort: e.target.value })}>
            {EFFORTS.map((e) => (
              <option key={e} value={e}>
                {e}
              </option>
            ))}
          </select>
        </label>
      </div>
    </section>
  );
}

// ---- Permissions ------------------------------------------------------------------------

const PERMISSION_LABELS: Array<[keyof AgentPermissions, string]> = [
  ["read_files", "read files"],
  ["edit_files", "edit files"],
  ["run_commands", "run commands"],
  ["push_branches", "push branches"],
  ["open_prs", "open PRs"],
  ["comment_prs", "comment on PRs"],
  ["submit_reviews", "submit reviews"],
  ["create_wiki_pages", "create wiki pages"],
];

/** The lock glyph on every permission row — enforcement iconography (D7). */
function LockIcon() {
  return (
    <svg
      className={styles.lockIcon}
      data-icon="lock"
      aria-hidden="true"
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
    >
      <rect x="4" y="10" width="16" height="11" rx="2" fill="currentColor" stroke="none" />
      <path d="M8 10V7a4 4 0 0 1 8 0v3" />
    </svg>
  );
}

export function PermissionsSection({
  permissions,
  onChange,
}: {
  permissions: AgentPermissions;
  onChange: (next: AgentPermissions) => void;
}) {
  return (
    <section
      className={styles.permissionsPanel}
      aria-label="Permissions"
      data-section="permissions"
      data-enforcement="true"
    >
      <h2>
        <LockIcon />
        Permissions
      </h2>
      <p className={styles.sectionHint}>
        Enforced in the runtime and the forge adapter — not prompt text.
      </p>
      <div className={styles.permissionsGrid}>
        {PERMISSION_LABELS.map(([key, label]) => (
          <label key={key} className={styles.permissionRow}>
            <LockIcon />
            <input
              type="checkbox"
              checked={permissions[key]}
              onChange={(e) => onChange({ ...permissions, [key]: e.target.checked })}
            />
            {label}
          </label>
        ))}
      </div>
      <p className={styles.enforcementNote}>
        A reviewer with edit unchecked <em>cannot</em> write code; that is stronger than
        telling it not to.
      </p>
    </section>
  );
}

// ---- Autonomy ---------------------------------------------------------------------------

export function AutonomySection({
  autonomy,
  onChange,
}: {
  autonomy: AgentAutonomy;
  onChange: (next: AgentAutonomy) => void;
}) {
  // The top rung sits behind a confirmation (§5.8: dangerous rungs confirm).
  const [pendingAuto, setPendingAuto] = useState(false);

  const pick = (value: AgentAutonomy) => {
    if (value === autonomy) return;
    if (value === "auto") {
      setPendingAuto(true);
      return;
    }
    setPendingAuto(false);
    onChange(value);
  };

  return (
    <section className={styles.section} aria-label="Autonomy" data-section="autonomy">
      <h2>Autonomy</h2>
      <p className={styles.sectionHint}>
        Ordered by increasing risk. The current level is echoed on every run header.
      </p>
      <div className={styles.autonomyList} role="radiogroup" aria-label="Autonomy level">
        {AUTONOMY_STOPS.map((stop) => (
          <label key={stop.value} className={styles.autonomyRow} data-active={stop.value === autonomy}>
            <input
              type="radio"
              name="autonomy"
              checked={stop.value === autonomy}
              onChange={() => pick(stop.value)}
            />
            <span className={styles.autonomyName}>{stop.name}</span>
            <span className={styles.autonomyDesc}>{stop.desc}</span>
          </label>
        ))}
      </div>
      {pendingAuto && (
        <div className={styles.confirmBox} role="alertdialog" aria-label="Confirm Auto">
          <span>
            Auto runs unattended: no plan gate, no destructive-action gate. Permissions and
            limits still apply.
          </span>
          <button
            type="button"
            onClick={() => {
              setPendingAuto(false);
              onChange("auto");
            }}
          >
            Switch to Auto
          </button>
          <button type="button" onClick={() => setPendingAuto(false)}>
            Cancel
          </button>
        </div>
      )}
    </section>
  );
}

// ---- Limits -----------------------------------------------------------------------------

export function LimitsSection({
  agent,
  onPatch,
}: {
  agent: Agent;
  onPatch: (patch: {
    concurrency_cap?: number;
    daily_cap_cents?: number | null;
    max_wall_clock_seconds?: number;
    max_steps?: number;
  }) => void;
}) {
  return (
    <section className={styles.section} aria-label="Limits" data-section="limits">
      <h2>Limits</h2>
      <div className={styles.formGrid}>
        <label className={styles.field}>
          Max concurrent runs
          <input
            type="number"
            min={1}
            defaultValue={agent.concurrency_cap}
            onBlur={(e) => {
              const v = Number(e.target.value);
              if (v >= 1 && v !== agent.concurrency_cap) onPatch({ concurrency_cap: v });
            }}
          />
        </label>
        <label className={styles.field}>
          Daily spend cap (cents; empty inherits the project default)
          <input
            type="number"
            min={0}
            defaultValue={agent.daily_cap_cents ?? ""}
            onBlur={(e) => {
              const raw = e.target.value.trim();
              const next = raw === "" ? null : Number(raw);
              if (next !== (agent.daily_cap_cents ?? null)) onPatch({ daily_cap_cents: next });
            }}
          />
        </label>
        <label className={styles.field}>
          Max wall clock (seconds)
          <input
            type="number"
            min={60}
            defaultValue={agent.max_wall_clock_seconds}
            onBlur={(e) => {
              const v = Number(e.target.value);
              if (v >= 60 && v !== agent.max_wall_clock_seconds)
                onPatch({ max_wall_clock_seconds: v });
            }}
          />
        </label>
        <label className={styles.field}>
          Max steps
          <input
            type="number"
            min={1}
            defaultValue={agent.max_steps}
            onBlur={(e) => {
              const v = Number(e.target.value);
              if (v >= 1 && v !== agent.max_steps) onPatch({ max_steps: v });
            }}
          />
        </label>
      </div>
    </section>
  );
}

// ---- Context preview --------------------------------------------------------------------

export function ContextPreviewSection() {
  return (
    <section className={styles.section} aria-label="Context preview" data-section="context-preview">
      <h2>Context preview</h2>
      <p className={styles.placeholderCard}>
        &ldquo;What every run of this agent sees.&rdquo; Context preview arrives with the
        context resolver (S34).
      </p>
    </section>
  );
}
