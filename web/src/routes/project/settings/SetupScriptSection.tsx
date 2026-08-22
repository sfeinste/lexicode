/*
 * The setup script (repos.setup_script): the Repository pane's block for the shell a project
 * runs during provisioning, before the agent starts.
 *
 * The column existed in the schema and the sandbox has executed it since S17/S19, but there
 * was no API field and no control, so the only way to set it was to write the row by hand.
 * The cost of that gap was an agent improvising `apt-get install` mid-task — or a bare
 * `exit 127`. The helper text states the three things this screen is the only place to learn:
 * it runs on every run, its output is a checklist step, and a non-zero exit fails the run
 * before the agent starts.
 */
import { SaveStatus } from "../../../components/SaveStatus/SaveStatus";
import type { Repo, UpdateRepoSettingsRequest } from "../../../lib/api/client";
import { useUpdateRepoSettings } from "../../../lib/api/repoQueries";
import { useAutosave } from "../../../lib/autosave";
import styles from "./settings.module.css";

/** The wired block: autosave over PATCH /repo. */
export function SetupScriptSection({ projectKey, repo }: { projectKey: string; repo: Repo }) {
  const update = useUpdateRepoSettings(projectKey);
  const autosave = useAutosave<UpdateRepoSettingsRequest>((patch) => update.mutateAsync(patch));

  return (
    <SetupScriptField
      script={repo.setup_script}
      status={<SaveStatus status={autosave.status} error={autosave.error} />}
      onChange={(script) => autosave.queue({ setup_script: script })}
    />
  );
}

/**
 * The presentational field, so the wording is testable without a query client. The textarea is
 * uncontrolled (defaultValue): the autosave round-trip re-renders the pane, and a controlled
 * value would fight the caret on every keystroke — the same reason the allowlist editor is.
 */
export function SetupScriptField({
  script,
  status,
  onChange,
}: {
  script: string;
  status?: React.ReactNode;
  /** Called with the whole script; debounced by the caller's autosave. */
  onChange: (script: string) => void;
}) {
  return (
    <fieldset className={styles.networkBlock}>
      <legend className={styles.networkLegend}>
        Setup script
        {status}
      </legend>

      <textarea
        key="setup-script-editor"
        aria-label="Setup script"
        className={styles.codeArea}
        rows={6}
        defaultValue={script}
        placeholder={"apt-get update && apt-get install -y python3"}
        spellCheck={false}
        onChange={(e) => onChange(e.target.value)}
      />

      <p className={styles.quiet}>
        Runs as root in the cloned workspace during provisioning, on <strong>every run</strong>,
        after the clone and before the agent starts. It appears as a{" "}
        <strong>setup script</strong> step in that run&rsquo;s provisioning checklist, with its
        output in the run&rsquo;s verbose activities. A non-zero exit fails the run there, with
        the script&rsquo;s output in the failure message — the agent never starts. An empty
        script is skipped entirely.
      </p>
      <p className={styles.quiet}>
        Nothing it installs is cached: every run starts from the image again and runs the whole
        script, under the network policy above.
      </p>
    </fieldset>
  );
}
