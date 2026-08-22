/*
 * The proposal diff block (S35): a labeled line diff, reusing the S16 LCS differ the
 * directive version view established. An edit-proposal renders as proposed-body vs the
 * page body it would change; the conflict view renders two of these side by side.
 */
import { diffLines } from "../agents/diff";
import styles from "./wiki.module.css";

export function ProposalDiff({
  label,
  from,
  to,
}: {
  label: string;
  from: string;
  to: string;
}) {
  const diff = diffLines(from, to);
  return (
    <section aria-label={label}>
      <span className={styles.sectionLabel}>{label}</span>
      <div className={styles.diffView}>
        <pre>
          {diff.map((l, i) => (
            <span
              key={i}
              data-op={l.op}
              className={
                l.op === "add" ? styles.diffAdd : l.op === "del" ? styles.diffDel : styles.diffCtx
              }
            >
              {(l.op === "add" ? "+ " : l.op === "del" ? "- " : "  ") + l.text}
            </span>
          ))}
        </pre>
      </div>
    </section>
  );
}
