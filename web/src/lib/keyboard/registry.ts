/*
 * The global keyboard registry (architecture §13). One map of (scope, chord) → command;
 * scopes stack (global < route < list < modal), so a modal's Escape shadows a list's Escape
 * without either knowing about the other. The command palette (⌘K) and the cheatsheet (?)
 * both read from this registry — UI spec §6 is data here, never fifteen scattered keydown
 * handlers.
 *
 * Chord syntax: steps separated by spaces, keys in a step joined by "+".
 *   "mod+k"   ⌘K (mac) / Ctrl+K
 *   "?"       the printable character, shift already folded in
 *   "g b"     a two-step sequence (G then B), 1s window between steps
 */

export type KeyScope = "global" | "route" | "list" | "modal";

const SCOPE_RANK: Record<KeyScope, number> = { global: 0, route: 1, list: 2, modal: 3 };

export interface KeyBinding {
  /** Unique id; also the palette command id. */
  id: string;
  scope: KeyScope;
  chord: string;
  /** Human title. The cheatsheet and the palette render exactly this. */
  title: string;
  /** Cheatsheet section, e.g. "Navigation", "General". */
  group: string;
  run: () => void;
  /** Show in the ⌘K palette. Defaults to false. */
  palette?: boolean;
  /** Extra gate, e.g. "a project is open". Checked at dispatch and by the palette. */
  enabled?: () => boolean;
}

/** How long a multi-step chord waits for its next step. */
export const SEQUENCE_WINDOW_MS = 1000;

/** Turn a KeyboardEvent into a step descriptor ("mod+k", "g", "?", "escape"). */
export function describeKey(e: KeyboardEvent): string | null {
  if (e.key === "Meta" || e.key === "Control" || e.key === "Alt" || e.key === "Shift") {
    return null;
  }
  const parts: string[] = [];
  if (e.metaKey || e.ctrlKey) parts.push("mod");
  if (e.altKey) parts.push("alt");
  let key = e.key;
  if (key === " ") key = "space";
  if (/^[a-zA-Z0-9]$/.test(key)) {
    if (e.shiftKey) parts.push("shift");
    key = key.toLowerCase();
  } else {
    // Punctuation ("?", "\") arrives with shift already applied; special keys by name.
    key = key.toLowerCase();
  }
  parts.push(key);
  return parts.join("+");
}

function isEditable(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

export class KeyboardRegistry {
  private bindings = new Map<string, KeyBinding>();
  /** Refcount per stacking scope. global is always active. */
  private scopeCounts: Record<KeyScope, number> = { global: 1, route: 0, list: 0, modal: 0 };
  private pending: string[] = [];
  private pendingTimer: ReturnType<typeof setTimeout> | null = null;
  private listeners = new Set<() => void>();
  private snapshot: KeyBinding[] = [];

  register(binding: KeyBinding): () => void {
    if (this.bindings.has(binding.id)) {
      throw new Error(`keyboard: duplicate binding id "${binding.id}"`);
    }
    this.bindings.set(binding.id, binding);
    this.notify();
    return () => {
      // Only remove if it is still this registration (an id may be re-registered on remount).
      if (this.bindings.get(binding.id) === binding) {
        this.bindings.delete(binding.id);
        this.notify();
      }
    };
  }

  /** Activate a stacking scope (a modal opened, a list took focus). Returns the deactivate. */
  activateScope(scope: KeyScope): () => void {
    this.scopeCounts[scope] += 1;
    let done = false;
    return () => {
      if (done) return;
      done = true;
      this.scopeCounts[scope] -= 1;
    };
  }

  scopeActive(scope: KeyScope): boolean {
    return this.scopeCounts[scope] > 0;
  }

  /** Every registered binding, for the cheatsheet and the palette. Stable between changes. */
  getBindings(): KeyBinding[] {
    return this.snapshot;
  }

  /** Re-render hook for React (useSyncExternalStore). */
  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  run(id: string): void {
    const b = this.bindings.get(id);
    if (b && (b.enabled?.() ?? true)) b.run();
  }

  /**
   * Feed one keydown through the registry. Returns true when the event was consumed
   * (matched a chord or extended a pending sequence).
   */
  handleKeydown(e: KeyboardEvent): boolean {
    const step = describeKey(e);
    if (step === null) return false;
    // Typing wins inside editable elements: only modified chords pass through.
    if (isEditable(e.target) && !step.includes("mod+") && step !== "escape") {
      this.clearPending();
      return false;
    }

    const attempt = [...this.pending, step];
    const exact = this.matchExact(attempt);
    if (exact) {
      this.clearPending();
      e.preventDefault();
      exact.run();
      return true;
    }
    if (this.matchesPrefix(attempt)) {
      this.pending = attempt;
      this.armPendingTimer();
      e.preventDefault();
      return true;
    }
    // A broken sequence: the final step may still start a new chord on its own.
    if (this.pending.length > 0) {
      this.clearPending();
      return this.handleKeydown(e);
    }
    return false;
  }

  /**
   * The active binding for a chord sequence: among matching bindings whose scope is active
   * and whose enabled() passes, the highest-ranked scope wins (modal shadows list shadows
   * route shadows global).
   */
  private matchExact(steps: string[]): KeyBinding | null {
    let best: KeyBinding | null = null;
    for (const b of this.bindings.values()) {
      if (!this.scopeActive(b.scope) || !(b.enabled?.() ?? true)) continue;
      if (!sameSteps(chordSteps(b.chord), steps)) continue;
      if (best === null || SCOPE_RANK[b.scope] > SCOPE_RANK[best.scope]) best = b;
    }
    return best;
  }

  private matchesPrefix(steps: string[]): boolean {
    for (const b of this.bindings.values()) {
      if (!this.scopeActive(b.scope) || !(b.enabled?.() ?? true)) continue;
      const chord = chordSteps(b.chord);
      if (chord.length > steps.length && sameSteps(chord.slice(0, steps.length), steps)) {
        return true;
      }
    }
    return false;
  }

  private armPendingTimer(): void {
    if (this.pendingTimer) clearTimeout(this.pendingTimer);
    this.pendingTimer = setTimeout(() => {
      this.pending = [];
      this.pendingTimer = null;
    }, SEQUENCE_WINDOW_MS);
  }

  private clearPending(): void {
    if (this.pendingTimer) clearTimeout(this.pendingTimer);
    this.pendingTimer = null;
    this.pending = [];
  }

  private notify(): void {
    this.snapshot = [...this.bindings.values()];
    for (const l of this.listeners) l();
  }
}

export function chordSteps(chord: string): string[] {
  return chord.trim().split(/\s+/);
}

function sameSteps(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((s, i) => s === b[i]);
}

/** The app-wide registry instance. Tests construct their own KeyboardRegistry. */
export const keyboard = new KeyboardRegistry();
