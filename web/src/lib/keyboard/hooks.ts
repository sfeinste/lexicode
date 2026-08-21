import { useEffect, useSyncExternalStore } from "react";

import { keyboard, type KeyBinding, type KeyScope } from "./registry";

/** Register a binding for the lifetime of the component. */
export function useKeyBinding(binding: KeyBinding): void {
  useEffect(() => keyboard.register(binding), [binding]);
}

/**
 * Register a batch of bindings built once per mount (the shell's global map, a route's
 * chords). The factory runs on mount; everything unregisters on unmount.
 */
export function useKeyBindings(factory: () => KeyBinding[], deps: unknown[]): void {
  useEffect(() => {
    const offs = factory().map((b) => keyboard.register(b));
    return () => offs.forEach((off) => off());
    // eslint-disable-next-line react-hooks/exhaustive-deps -- caller-owned dep list
  }, deps);
}

/** Activate a stacking scope while `active` (a modal is open, a list has focus). */
export function useKeyScope(scope: KeyScope, active: boolean): void {
  useEffect(() => {
    if (!active) return;
    return keyboard.activateScope(scope);
  }, [scope, active]);
}

/** Subscribe to the registry's binding list (cheatsheet, palette). */
export function useRegisteredBindings(): KeyBinding[] {
  return useSyncExternalStore(
    (cb) => keyboard.subscribe(cb),
    () => keyboard.getBindings(),
  );
}

/** Install the single window keydown listener. Mount exactly once, in the app root. */
export function useKeyboardDispatch(): void {
  useEffect(() => {
    const onKeydown = (e: KeyboardEvent) => keyboard.handleKeydown(e);
    window.addEventListener("keydown", onKeydown);
    return () => window.removeEventListener("keydown", onKeydown);
  }, []);
}

const isMac = typeof navigator !== "undefined" && /Mac|iP(hone|ad|od)/.test(navigator.platform);

/** Render a chord for humans: "mod+k" → "⌘K" (mac) / "Ctrl+K"; "g b" → "G B". */
export function chordLabel(chord: string): string {
  return chord
    .split(/\s+/)
    .map((step) =>
      step
        .split("+")
        .map((key) => {
          switch (key) {
            case "mod":
              return isMac ? "⌘" : "Ctrl+";
            case "alt":
              return isMac ? "⌥" : "Alt+";
            case "shift":
              return "⇧";
            case "escape":
              return "Esc";
            case "space":
              return "Space";
            case "enter":
              return "Enter";
            default:
              return key.length === 1 ? key.toUpperCase() : key;
          }
        })
        .join(""),
    )
    .join(" ");
}
