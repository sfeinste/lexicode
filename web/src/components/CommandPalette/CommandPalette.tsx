/*
 * The ⌘K command palette shell (UI spec §6: deterministic actions; ⌘J — talking to an
 * agent — is a different, later surface). It reads its commands FROM the keyboard registry:
 * anything registered with palette: true appears, filtered fuzzily, with its chord shown.
 * Later stories add commands by registering bindings, never by touching this file.
 */
import { useEffect, useMemo, useRef, useState } from "react";

import { fuzzyFilter } from "../../lib/fuzzy";
import { chordLabel, useKeyBindings, useKeyScope, useRegisteredBindings } from "../../lib/keyboard/hooks";
import { keyboard } from "../../lib/keyboard/registry";
import { useUIStore } from "../../stores/ui";
import styles from "./CommandPalette.module.css";

export function CommandPalette() {
  const open = useUIStore((s) => s.paletteOpen);
  const setOpen = useUIStore((s) => s.setPaletteOpen);
  useKeyScope("modal", open);
  useKeyBindings(
    () =>
      open
        ? [
            {
              id: "palette.close",
              scope: "modal",
              chord: "escape",
              title: "Close palette",
              group: "General",
              run: () => setOpen(false),
            },
          ]
        : [],
    [open, setOpen],
  );

  if (!open) return null;
  return <PaletteDialog onClose={() => setOpen(false)} />;
}

function PaletteDialog({ onClose }: { onClose: () => void }) {
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const bindings = useRegisteredBindings();

  const commands = useMemo(() => {
    const visible = bindings.filter((b) => b.palette && (b.enabled?.() ?? true));
    return fuzzyFilter(query, visible, (b) => b.title);
  }, [bindings, query]);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);
  useEffect(() => {
    setCursor(0);
  }, [query]);

  const run = (id: string) => {
    onClose();
    keyboard.run(id);
  };

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div
        role="dialog"
        aria-label="Command palette"
        className={styles.dialog}
        onClick={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className={styles.input}
          placeholder="Type a command…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setCursor((c) => Math.min(c + 1, commands.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setCursor((c) => Math.max(c - 1, 0));
            } else if (e.key === "Enter" && commands[cursor]) {
              e.preventDefault();
              run(commands[cursor].id);
            }
          }}
        />
        <ul className={styles.list} role="listbox">
          {commands.map((cmd, i) => (
            <li key={cmd.id}>
              <button
                type="button"
                role="option"
                aria-selected={i === cursor}
                className={styles.item}
                data-selected={i === cursor || undefined}
                onMouseEnter={() => setCursor(i)}
                onClick={() => run(cmd.id)}
              >
                <span>{cmd.title}</span>
                <kbd className={styles.chord}>{chordLabel(cmd.chord)}</kbd>
              </button>
            </li>
          ))}
          {commands.length === 0 && <li className={styles.none}>No matching commands</li>}
        </ul>
      </div>
    </div>
  );
}
