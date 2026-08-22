/*
 * Editor — THE markdown editor (UI spec §7): one component behind the ticket description,
 * the comment composer, and (later stories) the wiki and directive editors. Never forked;
 * a behavior added here appears in every placement, and the shared placement test suite
 * (Editor.placements.test.tsx) holds that true.
 *
 * It is a plain <textarea> over markdown text (decision documented in engine.ts) with:
 * - slash commands ("/" at line start → the minimal menu: /h1 /h2 /bullet /code /criteria),
 * - `@` mentions (users, agents, wiki pages, tickets — sources are props; empty sources
 *   render their section's honest empty state until their APIs exist),
 * - plain-text paste (the clipboard's text/plain, inserted at the selection),
 * - ⌘Enter submit for composer placements.
 */
import {
  forwardRef,
  useCallback,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
} from "react";

import styles from "./Editor.module.css";
import {
  applyMention,
  applyPaste,
  applySlashCommand,
  detectMentionTrigger,
  detectSlashTrigger,
  filterMentions,
  filterSlashCommands,
  type MentionItem,
  type MentionKind,
  type MentionSources,
  type SlashCommand,
  type Trigger,
} from "./engine";

export interface EditorHandle {
  focus: () => void;
  /** The current selection's text — the ⌘⇧O selection→sub-tickets source. */
  getSelectedText: () => string;
  /** Escape hatch for placement-specific needs (selection ranges, scrolling). */
  textarea: () => HTMLTextAreaElement | null;
}

export interface EditorProps {
  value: string;
  onChange: (value: string) => void;
  mentions: MentionSources;
  ariaLabel: string;
  placeholder?: string;
  autoFocus?: boolean;
  minRows?: number;
  /** ⌘Enter / Ctrl+Enter — the composer's post action. Absent = the chord does nothing. */
  onSubmit?: () => void;
  onBlur?: () => void;
}

type Menu =
  | { type: "none" }
  | { type: "slash"; trigger: Trigger; items: SlashCommand[]; cursor: number }
  | { type: "mention"; trigger: Trigger; items: MenuRow[]; cursor: number };

/** One row of the mention menu: a real item, or a section's empty-state line. */
type MenuRow =
  | { row: "item"; item: MentionItem }
  | { row: "empty"; kind: MentionKind; text: string };

const KIND_BADGES: Record<MentionKind, string> = {
  user: "user",
  agent: "agent",
  wiki: "wiki",
  ticket: "ticket",
};

/** Sections in menu order, with the §8-style empty copy for sources that have no API yet. */
function mentionRows(sources: MentionSources, query: string): MenuRow[] {
  const rows: MenuRow[] = [];
  const sections: Array<[MentionKind, MentionItem[], string | null]> = [
    ["user", sources.users, null],
    ["agent", sources.agents, "No agents yet"],
    ["wiki", sources.wiki, "No wiki pages yet"],
    ["ticket", sources.tickets, null],
  ];
  for (const [kind, items, emptyText] of sections) {
    const hits = filterMentions(query, items);
    for (const item of hits) rows.push({ row: "item", item });
    // The honest empty state renders only while the source itself is empty (API not landed
    // or nothing exists) — not when a query merely filtered everything out.
    if (items.length === 0 && emptyText !== null && query === "") {
      rows.push({ row: "empty", kind, text: emptyText });
    }
  }
  return rows;
}

export const Editor = forwardRef<EditorHandle, EditorProps>(function Editor(
  {
    value,
    onChange,
    mentions,
    ariaLabel,
    placeholder,
    autoFocus,
    minRows = 3,
    onSubmit,
    onBlur,
  }: EditorProps,
  ref,
) {
  const taRef = useRef<HTMLTextAreaElement>(null);
  const [menu, setMenu] = useState<Menu>({ type: "none" });
  // A caret position to restore after a programmatic edit (slash/mention/paste insert).
  const pendingCaret = useRef<number | null>(null);

  useImperativeHandle(ref, () => ({
    focus: () => taRef.current?.focus(),
    getSelectedText: () => {
      const ta = taRef.current;
      if (ta === null) return "";
      return ta.value.slice(ta.selectionStart, ta.selectionEnd);
    },
    textarea: () => taRef.current,
  }));

  const refreshMenu = useCallback(
    (nextValue: string, caret: number) => {
      const slash = detectSlashTrigger(nextValue, caret);
      if (slash !== null) {
        const items = filterSlashCommands(slash.query);
        if (items.length > 0) {
          setMenu({ type: "slash", trigger: slash, items, cursor: 0 });
          return;
        }
      }
      const mention = detectMentionTrigger(nextValue, caret);
      if (mention !== null) {
        const items = mentionRows(mentions, mention.query);
        if (items.length > 0) {
          setMenu({ type: "mention", trigger: mention, items, cursor: 0 });
          return;
        }
      }
      setMenu({ type: "none" });
    },
    [mentions],
  );

  const applyEdit = useCallback(
    (nextValue: string, caret: number) => {
      pendingCaret.current = caret;
      onChange(nextValue);
      // Restore the caret after React re-renders the controlled value. (setTimeout, not
      // rAF: test environments without a render loop must behave identically.)
      setTimeout(() => {
        const ta = taRef.current;
        if (ta !== null && pendingCaret.current !== null) {
          ta.setSelectionRange(pendingCaret.current, pendingCaret.current);
          pendingCaret.current = null;
        }
      }, 0);
      setMenu({ type: "none" });
    },
    [onChange],
  );

  const pick = useCallback(
    (m: Menu, index: number) => {
      const ta = taRef.current;
      const caret = ta?.selectionStart ?? value.length;
      if (m.type === "slash") {
        const cmd = m.items[index];
        if (cmd === undefined) return;
        const r = applySlashCommand(value, m.trigger, caret, cmd.id);
        applyEdit(r.value, r.caret);
      } else if (m.type === "mention") {
        const row = m.items[index];
        if (row === undefined || row.row !== "item") return;
        const r = applyMention(value, m.trigger, caret, row.item);
        applyEdit(r.value, r.caret);
      }
    },
    [value, applyEdit],
  );

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && onSubmit !== undefined) {
        e.preventDefault();
        onSubmit();
        return;
      }
      if (menu.type === "none") return;
      const count = menu.items.length;
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          setMenu({ ...menu, cursor: (menu.cursor + 1) % count });
          break;
        case "ArrowUp":
          e.preventDefault();
          setMenu({ ...menu, cursor: (menu.cursor - 1 + count) % count });
          break;
        case "Enter":
        case "Tab":
          e.preventDefault();
          pick(menu, menu.cursor);
          break;
        case "Escape":
          // Consume it: closing the menu must not bubble into a route/modal Escape.
          e.preventDefault();
          e.stopPropagation();
          setMenu({ type: "none" });
          break;
      }
    },
    [menu, pick, onSubmit],
  );

  const onPaste = useCallback(
    (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
      // Always plain text: whatever the clipboard holds, only text/plain lands.
      e.preventDefault();
      const ta = e.currentTarget;
      const pasted = e.clipboardData.getData("text/plain");
      const r = applyPaste(ta.value, ta.selectionStart, ta.selectionEnd, pasted);
      applyEdit(r.value, r.caret);
    },
    [applyEdit],
  );

  const rows = useMemo(
    () => Math.max(minRows, value.split("\n").length),
    [minRows, value],
  );

  return (
    <div className={styles.root}>
      <textarea
        ref={taRef}
        className={styles.textarea}
        aria-label={ariaLabel}
        placeholder={placeholder}
        autoFocus={autoFocus}
        rows={rows}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          refreshMenu(e.target.value, e.target.selectionStart);
        }}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        onBlur={onBlur}
        onClick={(e) => refreshMenu(e.currentTarget.value, e.currentTarget.selectionStart)}
      />
      {menu.type !== "none" && (
        <ul className={styles.menu} role="listbox" aria-label={menu.type === "slash" ? "Slash commands" : "Mentions"}>
          {menu.type === "slash"
            ? menu.items.map((c, i) => (
                <li key={c.id}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={i === menu.cursor}
                    data-cursor={i === menu.cursor || undefined}
                    className={styles.option}
                    // mousedown, not click: the textarea must keep focus.
                    onMouseDown={(e) => {
                      e.preventDefault();
                      pick(menu, i);
                    }}
                  >
                    <span className={styles.optionLabel}>{c.label}</span>
                    <span className={styles.optionHint}>{c.hint}</span>
                  </button>
                </li>
              ))
            : menu.items.map((row, i) =>
                row.row === "empty" ? (
                  <li key={`empty-${row.kind}`} className={styles.emptyRow}>
                    <span className={styles.kindBadge}>{KIND_BADGES[row.kind]}</span>
                    {row.text}
                  </li>
                ) : (
                  <li key={`${row.item.kind}:${row.item.id}`}>
                    <button
                      type="button"
                      role="option"
                      aria-selected={i === menu.cursor}
                      data-cursor={i === menu.cursor || undefined}
                      className={styles.option}
                      onMouseDown={(e) => {
                        e.preventDefault();
                        pick(menu, i);
                      }}
                    >
                      <span className={styles.kindBadge}>{KIND_BADGES[row.item.kind]}</span>
                      <span className={styles.optionLabel}>{row.item.label}</span>
                      {row.item.hint !== undefined && (
                        <span className={styles.optionHint}>{row.item.hint}</span>
                      )}
                    </button>
                  </li>
                ),
              )}
        </ul>
      )}
    </div>
  );
});

export type { MentionItem, MentionSources } from "./engine";
