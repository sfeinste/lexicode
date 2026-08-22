/*
 * MarkdownView — the minimal renderer for Editor-authored markdown (S12): headings, bullet
 * and task-list lines, fenced code blocks, and `@[label](kind:id)` mention tokens rendered
 * as chips. Deliberately no full markdown pipeline (no inline bold/italic/links yet — the
 * wiki story owns richer rendering); the stream needs comments to read cleanly, not a
 * document engine, and everything unrecognized renders as literal text — never HTML.
 */
import styles from "./MarkdownView.module.css";
import { mentionPattern } from "./mentionPattern";

function inline(text: string, keyPrefix: string): React.ReactNode[] {
  const out: React.ReactNode[] = [];
  let last = 0;
  let i = 0;
  for (const m of text.matchAll(mentionPattern)) {
    const idx = m.index;
    if (idx > last) out.push(text.slice(last, idx));
    out.push(
      <span key={`${keyPrefix}-m${i++}`} className={styles.mention} data-kind={m[2]}>
        @{m[1]}
      </span>,
    );
    last = idx + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function MarkdownView({ markdown }: { markdown: string }) {
  const lines = markdown.split("\n");
  const blocks: React.ReactNode[] = [];
  let i = 0;
  let key = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (line.startsWith("```")) {
      const code: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) {
        code.push(lines[i]);
        i++;
      }
      i++; // closing fence (or EOF)
      blocks.push(
        <pre key={key++} className={styles.code}>
          {code.join("\n")}
        </pre>,
      );
      continue;
    }
    if (line.startsWith("## ")) {
      blocks.push(
        <h4 key={key++} className={styles.h2}>
          {inline(line.slice(3), `l${i}`)}
        </h4>,
      );
    } else if (line.startsWith("# ")) {
      blocks.push(
        <h3 key={key++} className={styles.h1}>
          {inline(line.slice(2), `l${i}`)}
        </h3>,
      );
    } else if (/^- \[[ xX]\] /.test(line)) {
      const checked = line[3] !== " ";
      blocks.push(
        <div key={key++} className={styles.task}>
          <input type="checkbox" checked={checked} readOnly tabIndex={-1} />
          <span>{inline(line.slice(6), `l${i}`)}</span>
        </div>,
      );
    } else if (line.startsWith("- ")) {
      blocks.push(
        <div key={key++} className={styles.bullet}>
          <span aria-hidden="true">•</span>
          <span>{inline(line.slice(2), `l${i}`)}</span>
        </div>,
      );
    } else if (line.trim() === "") {
      blocks.push(<div key={key++} className={styles.gap} />);
    } else {
      blocks.push(
        <p key={key++} className={styles.p}>
          {inline(line, `l${i}`)}
        </p>,
      );
    }
    i++;
  }
  return <div className={styles.root}>{blocks}</div>;
}
