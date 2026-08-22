/*
 * The Editor's pure text mechanics (S12) — trigger detection, slash commands, mention
 * insertion, paste shaping — kept free of React so behavior is testable as functions and
 * therefore identical in every placement (ticket description, comment composer, and later
 * the wiki and directive editors — one component, four placements, never forked).
 *
 * Deliberate scope (documented decisions):
 * - The editor is a plain <textarea> over markdown text. No contenteditable, no heavyweight
 *   editor dependency — markdown conventions ARE the formatting UI, which matches the
 *   dense dev-tool direction and keeps the bundle flat.
 * - The slash menu is the minimal S12 set: /h1 /h2 /bullet /code /criteria. It grows by
 *   adding entries to SLASH_COMMANDS, nowhere else.
 * - A mention is the explicit token `@[label](kind:id)` — the same wire format the backend
 *   parses into mentions rows. Bare `@name` text is never a mention.
 */

export type MentionKind = "user" | "agent" | "wiki" | "ticket";

export interface MentionItem {
  kind: MentionKind;
  id: string;
  label: string;
  /** Secondary text in the menu row (a ticket title, a page path). */
  hint?: string;
}

/** What the caller wires into the `@` menu. Empty arrays render the section's empty state. */
export interface MentionSources {
  users: MentionItem[];
  agents: MentionItem[];
  wiki: MentionItem[];
  tickets: MentionItem[];
}

/** The canonical mention token the Editor inserts and the backend parses. */
export function mentionToken(item: MentionItem): string {
  return `@[${item.label}](${item.kind}:${item.id})`;
}

// ---- triggers ---------------------------------------------------------------------------

export interface Trigger {
  /** Index of the trigger character ("@" or "/"). */
  start: number;
  /** The text typed after it, up to the caret. */
  query: string;
}

/**
 * An open `@` mention trigger at the caret: an "@" with no whitespace between it and the
 * caret, starting a word (preceded by start-of-text, whitespace or "(").
 */
export function detectMentionTrigger(value: string, caret: number): Trigger | null {
  const at = value.lastIndexOf("@", caret - 1);
  if (at === -1) return null;
  const query = value.slice(at + 1, caret);
  if (/[\s\]]/.test(query)) return null;
  // Already a completed token like "@[x](user:1)"? The "[" right after @ means the token
  // form is being written/present; keep the menu closed.
  if (query.startsWith("[")) return null;
  const before = at === 0 ? "" : value[at - 1];
  if (before !== "" && !/[\s(]/.test(before)) return null;
  return { start: at, query };
}

/** An open slash-command trigger: "/" as the first character of the current line. */
export function detectSlashTrigger(value: string, caret: number): Trigger | null {
  const lineStart = value.lastIndexOf("\n", caret - 1) + 1;
  if (value[lineStart] !== "/") return null;
  const query = value.slice(lineStart + 1, caret);
  if (!/^[a-z0-9]*$/i.test(query)) return null;
  return { start: lineStart, query };
}

// ---- slash commands ---------------------------------------------------------------------

export interface SlashCommand {
  id: "h1" | "h2" | "bullet" | "code" | "criteria";
  label: string;
  hint: string;
}

/** The minimal S12 set. /criteria inserts a markdown task item ("- [ ] ") — in the ticket
 * description that reads as a checklist line an agent can propose and ⌘⇧O can convert;
 * first-class acceptance criteria live in their own block, not in the description. */
export const SLASH_COMMANDS: readonly SlashCommand[] = [
  { id: "h1", label: "/h1", hint: "Heading" },
  { id: "h2", label: "/h2", hint: "Subheading" },
  { id: "bullet", label: "/bullet", hint: "Bulleted list item" },
  { id: "code", label: "/code", hint: "Code block" },
  { id: "criteria", label: "/criteria", hint: "Checklist item" },
];

export function filterSlashCommands(query: string): SlashCommand[] {
  const q = query.toLowerCase();
  return SLASH_COMMANDS.filter((c) => c.id.startsWith(q));
}

export interface EditResult {
  value: string;
  caret: number;
}

/**
 * Replace the trigger text ("/bu…", up to the caret) with the command's markdown. The
 * command applies at the trigger's line start — where detectSlashTrigger guarantees it is.
 */
export function applySlashCommand(
  value: string,
  trigger: Trigger,
  caret: number,
  id: SlashCommand["id"],
): EditResult {
  const before = value.slice(0, trigger.start);
  const after = value.slice(caret);
  switch (id) {
    case "h1":
      return { value: `${before}# ${after}`, caret: trigger.start + 2 };
    case "h2":
      return { value: `${before}## ${after}`, caret: trigger.start + 3 };
    case "bullet":
      return { value: `${before}- ${after}`, caret: trigger.start + 2 };
    case "criteria":
      return { value: `${before}- [ ] ${after}`, caret: trigger.start + 6 };
    case "code": {
      const insert = "```\n\n```";
      return { value: before + insert + after, caret: trigger.start + 4 };
    }
  }
}

// ---- mentions ---------------------------------------------------------------------------

/** Replace the open "@query" with the picked item's token plus a trailing space. */
export function applyMention(
  value: string,
  trigger: Trigger,
  caret: number,
  item: MentionItem,
): EditResult {
  const token = `${mentionToken(item)} `;
  const before = value.slice(0, trigger.start);
  return { value: before + token + value.slice(caret), caret: trigger.start + token.length };
}

/** Case-insensitive substring filter over one mention source. */
export function filterMentions(query: string, items: MentionItem[]): MentionItem[] {
  const q = query.toLowerCase();
  if (q === "") return items;
  return items.filter(
    (i) => i.label.toLowerCase().includes(q) || (i.hint ?? "").toLowerCase().includes(q),
  );
}

// ---- paste ------------------------------------------------------------------------------

/** Insert pasted plain text at the selection. The Editor always pastes as plain text —
 * whatever rich content the clipboard holds, only text/plain lands. */
export function applyPaste(
  value: string,
  selStart: number,
  selEnd: number,
  pasted: string,
): EditResult {
  const next = value.slice(0, selStart) + pasted + value.slice(selEnd);
  return { value: next, caret: selStart + pasted.length };
}

/** Single-line fields (the ticket title): a multi-line paste collapses to one line. */
export function collapseToSingleLine(text: string): string {
  return text
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter((l) => l !== "")
    .join(" ");
}
