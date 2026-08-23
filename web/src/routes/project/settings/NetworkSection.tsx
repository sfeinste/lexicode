/*
 * Network policy (story S18, decision D-10): the Repository pane's network block. Three
 * radios with the honest wording — `none` is "nothing beyond what the agent itself needs",
 * never "no network", because the container still reaches the Anthropic API and the repo's
 * git host through the egress proxy; a container with zero egress could not run the agent at
 * all and the setting would be a trap. Plus the allowlist domain editor.
 *
 * The policy column is nullable (null = inherit the workspace default). The inheritance line
 * reuses InheritedField's exact spec wording but not the component itself: InheritedField
 * wraps its child control in one <label>, which is correct for a single input and wrong for a
 * radiogroup (a label may hold only one labelable element — clicking option two would
 * activate option one).
 */
import { useId } from "react";

import inheritStyles from "../../../components/InheritedField/InheritedField.module.css";
import { SaveStatus } from "../../../components/SaveStatus/SaveStatus";
import type { Repo, UpdateRepoNetworkRequest } from "../../../lib/api/client";
import { useUpdateRepoNetwork } from "../../../lib/api/repoQueries";
import { useAutosave } from "../../../lib/autosave";
import styles from "./settings.module.css";

type PolicyValue = "none" | "allowlist" | "open";

/** The D-10 wording, verbatim where the decision spells it. */
const POLICY_OPTIONS: Array<{ value: PolicyValue; label: string; hint: string }> = [
  {
    value: "none",
    label: "None — nothing beyond what the agent itself needs",
    hint:
      "The Anthropic API and this repository's git host stay reachable through the egress " +
      "proxy; everything else is blocked. A run that could reach nothing could not run the " +
      "agent at all.",
  },
  {
    value: "allowlist",
    label: "Allowlist — approved domains only",
    hint:
      "Everything None allows, plus the domains listed below. *.example.com matches the " +
      "domain and every subdomain.",
  },
  {
    value: "open",
    label: "Open — unrestricted",
    hint:
      "The container joins the default Docker network with no proxy. Every host is " +
      "reachable and no network decisions are logged.",
  },
];

/** The wired block: autosave over PATCH /repo/network. */
export function NetworkSection({ projectKey, repo }: { projectKey: string; repo: Repo }) {
  const update = useUpdateRepoNetwork(projectKey);
  const autosave = useAutosave<UpdateRepoNetworkRequest>((patch) => update.mutateAsync(patch));

  const setPolicy = (value: PolicyValue | null) => {
    autosave.queue({ network_policy: value });
    autosave.flush();
  };

  return (
    <NetworkPolicyField
      policy={repo.network_policy}
      workspaceDefault={repo.workspace_network_policy}
      allowlist={repo.network_allowlist}
      status={<SaveStatus status={autosave.status} error={autosave.error} />}
      onPolicyChange={setPolicy}
      onAllowlistChange={(domains) => autosave.queue({ network_allowlist: domains })}
    />
  );
}

/**
 * The presentational field: everything about the network block except the transport, so the
 * wording and the inherit/override mechanics are testable without a query client.
 */
export function NetworkPolicyField({
  policy,
  workspaceDefault,
  allowlist,
  status,
  onPolicyChange,
  onAllowlistChange,
}: {
  /** The stored override; null means inherit the workspace default. */
  policy: PolicyValue | null;
  workspaceDefault: PolicyValue;
  allowlist: string[];
  status?: React.ReactNode;
  /** Called with the chosen policy, or null to revert to inherit. Discrete — saves at once. */
  onPolicyChange: (value: PolicyValue | null) => void;
  /** Called with the parsed domain list; debounced by the caller's autosave. */
  onAllowlistChange: (domains: string[]) => void;
}) {
  const groupId = useId();
  const inherited = policy === null;
  const effective: PolicyValue = policy ?? workspaceDefault;
  const workspaceLabel =
    POLICY_OPTIONS.find((o) => o.value === workspaceDefault)?.label ?? workspaceDefault;

  return (
    <fieldset className={styles.networkBlock} aria-label="Network policy">
      <legend className={styles.networkLegend}>
        Network policy
        {status}
      </legend>

      <div role="radiogroup" aria-label="Network policy options" className={styles.networkOptions}>
        {POLICY_OPTIONS.map((opt) => (
          <label key={opt.value} className={styles.networkOption}>
            <input
              type="radio"
              name={`network-policy-${groupId}`}
              value={opt.value}
              checked={effective === opt.value}
              disabled={inherited}
              onChange={() => onPolicyChange(opt.value)}
            />
            <span>
              {opt.label}
              <span className={styles.networkHint}>{opt.hint}</span>
            </span>
          </label>
        ))}
      </div>

      <p className={inheritStyles.inheritance}>
        {inherited ? (
          <>
            Inherited from workspace: <code className={inheritStyles.value}>{workspaceLabel}</code>
            .{" "}
            <button
              type="button"
              className={inheritStyles.action}
              onClick={() => onPolicyChange(workspaceDefault)}
            >
              Override.
            </button>
          </>
        ) : (
          <button
            type="button"
            className={inheritStyles.action}
            onClick={() => onPolicyChange(null)}
          >
            Reset to workspace default.
          </button>
        )}
      </p>

      <p className={styles.quiet}>
        Reachable right now:{" "}
        {effective === "open"
          ? "every host — the container joins the default Docker network with no proxy."
          : effective === "allowlist" && allowlist.length > 0
            ? `the Anthropic API, this repository's git host, and ${allowlist.length} listed ${
                allowlist.length === 1 ? "domain" : "domains"
              }.`
            : "the Anthropic API and this repository's git host, and nothing else."}
      </p>

      {effective === "allowlist" && allowlist.length === 0 && (
        <p className={styles.warning} role="status">
          <strong>This allowlist is empty, so it currently behaves exactly like None.</strong>{" "}
          Only the Anthropic API and this repository's git host are reachable. Package installs
          (<code>npm</code>, <code>pip</code>, Go modules) will fail with a proxy denial until a
          domain is added below. The denials are recorded in each run's verbose activities.
        </p>
      )}

      {effective === "allowlist" && (
        <label className={styles.field}>
          Allowed domains (one per line)
          <textarea
            key="allowlist-editor"
            rows={5}
            defaultValue={allowlist.join("\n")}
            placeholder={"registry.npmjs.org\n*.githubusercontent.com"}
            spellCheck={false}
            onChange={(e) => onAllowlistChange(parseDomains(e.target.value))}
          />
          <span className={styles.quiet}>
            Bare domains only — no scheme, no path. The Anthropic API and the repository's git
            host are always allowed; every other allow and deny is logged in the run's verbose
            activities.
          </span>
        </label>
      )}
    </fieldset>
  );
}

/** One domain per line; blanks dropped. Validation happens server-side, per entry. */
function parseDomains(raw: string): string[] {
  return raw
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");
}
