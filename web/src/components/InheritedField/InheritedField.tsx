/*
 * InheritedField — the settings-inheritance control (UI spec §5.11, S08). Every project-level
 * setting with a workspace default renders as its control plus this component's one line:
 *
 *   inherited:  Inherited from workspace: `<value>`. Override.
 *   overridden: Reset to workspace default.
 *
 * The wording is the spec's, verbatim. The data comes straight off the API's
 * {value, inherited, workspace_value} triple (nullable project columns; null = inherit —
 * data model §1), so this component never recomputes inheritance.
 */
import type { ReactNode } from "react";

import styles from "./InheritedField.module.css";

export interface InheritedFieldProps {
  label: string;
  /** True when the project column is null and the workspace default applies. */
  inherited: boolean;
  /** The live workspace default, already formatted for display. */
  workspaceValue: string;
  /** Called when the user chooses to override the workspace default. */
  onOverride: () => void;
  /** Called when the user reverts to inheriting the workspace default. */
  onReset: () => void;
  /** The control itself (disabled by the caller while inherited). */
  children: ReactNode;
  hint?: string;
}

export function InheritedField({
  label,
  inherited,
  workspaceValue,
  onOverride,
  onReset,
  children,
  hint,
}: InheritedFieldProps) {
  return (
    <div className={styles.root}>
      <label className={styles.label}>
        {label}
        {children}
      </label>
      {hint && <p className={styles.hint}>{hint}</p>}
      <p className={styles.inheritance}>
        {inherited ? (
          <>
            Inherited from workspace: <code className={styles.value}>{workspaceValue}</code>.{" "}
            <button type="button" className={styles.action} onClick={onOverride}>
              Override.
            </button>
          </>
        ) : (
          <button type="button" className={styles.action} onClick={onReset}>
            Reset to workspace default.
          </button>
        )}
      </p>
    </div>
  );
}
