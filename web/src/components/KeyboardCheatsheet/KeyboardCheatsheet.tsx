/*
 * The `?` cheatsheet (UI spec §6). It renders FROM the keyboard registry — every binding
 * registered anywhere appears here, grouped, with no hardcoded duplicate list. That is the
 * S07 acceptance criterion, and it is what keeps the cheatsheet honest as later stories add
 * chords.
 */
import { useMemo } from "react";

import { chordLabel, useKeyBindings, useKeyScope, useRegisteredBindings } from "../../lib/keyboard/hooks";
import type { KeyBinding } from "../../lib/keyboard/registry";
import { useUIStore } from "../../stores/ui";
import styles from "./KeyboardCheatsheet.module.css";

export function KeyboardCheatsheet() {
  const open = useUIStore((s) => s.cheatsheetOpen);
  const setOpen = useUIStore((s) => s.setCheatsheetOpen);
  useKeyScope("modal", open);
  useKeyBindings(
    () =>
      open
        ? [
            {
              id: "cheatsheet.close",
              scope: "modal",
              chord: "escape",
              title: "Close cheatsheet",
              group: "General",
              run: () => setOpen(false),
            },
          ]
        : [],
    [open, setOpen],
  );

  if (!open) return null;
  return <CheatsheetDialog onClose={() => setOpen(false)} />;
}

function CheatsheetDialog({ onClose }: { onClose: () => void }) {
  const bindings = useRegisteredBindings();

  const groups = useMemo(() => {
    const byGroup = new Map<string, KeyBinding[]>();
    for (const b of bindings) {
      // The close bindings of the open overlays are plumbing, not vocabulary.
      if (b.id.endsWith(".close")) continue;
      const list = byGroup.get(b.group) ?? [];
      list.push(b);
      byGroup.set(b.group, list);
    }
    return [...byGroup.entries()];
  }, [bindings]);

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div
        role="dialog"
        aria-label="Keyboard shortcuts"
        className={styles.dialog}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className={styles.title}>Keyboard shortcuts</h2>
        <div className={styles.groups}>
          {groups.map(([group, list]) => (
            <section key={group} className={styles.group}>
              <h3 className={styles.groupTitle}>{group}</h3>
              <dl className={styles.rows}>
                {list.map((b) => (
                  <div key={b.id} className={styles.row}>
                    <dt className={styles.rowTitle}>{b.title}</dt>
                    <dd className={styles.rowChord}>
                      <kbd>{chordLabel(b.chord)}</kbd>
                    </dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
