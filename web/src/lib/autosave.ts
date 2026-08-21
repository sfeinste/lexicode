/*
 * useAutosave — the §5.11 autosave contract: no global Save button, every edit saves itself,
 * an inline indicator says "Saved". Edits are queued per field, merged shallowly, debounced,
 * and flushed on unmount so navigating away never loses a pending edit ("survives navigation",
 * S08 acceptance).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export type AutosaveStatus = "idle" | "saving" | "saved" | "error";

export interface Autosave<T extends object> {
  /** Merge a partial patch into the pending edit and (re)start the debounce timer. */
  queue: (patch: Partial<T>) => void;
  /** Save the pending edit now (also called automatically on unmount). */
  flush: () => void;
  status: AutosaveStatus;
  /** The problem's message when status is "error". */
  error: string | null;
}

export const AUTOSAVE_DEBOUNCE_MS = 600;

export function useAutosave<T extends object>(
  save: (patch: Partial<T>) => Promise<unknown>,
  debounceMs: number = AUTOSAVE_DEBOUNCE_MS,
): Autosave<T> {
  const [status, setStatus] = useState<AutosaveStatus>("idle");
  const [error, setError] = useState<string | null>(null);

  // Refs, not state: the pending patch and timer must be readable from unmount cleanup.
  const pending = useRef<Partial<T> | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const saveRef = useRef(save);
  saveRef.current = save;
  const mounted = useRef(true);

  const flush = useCallback(() => {
    if (timer.current !== null) {
      clearTimeout(timer.current);
      timer.current = null;
    }
    const patch = pending.current;
    if (patch === null) return;
    pending.current = null;
    if (mounted.current) {
      setStatus("saving");
      setError(null);
    }
    void saveRef.current(patch).then(
      () => {
        // A newer edit queued while this save was in flight owns the indicator.
        if (mounted.current && pending.current === null) setStatus("saved");
      },
      (err: unknown) => {
        if (!mounted.current) return;
        setStatus("error");
        setError(err instanceof Error ? err.message : "Save failed");
      },
    );
  }, []);

  const queue = useCallback(
    (patch: Partial<T>) => {
      pending.current = { ...pending.current, ...patch };
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = setTimeout(flush, debounceMs);
    },
    [flush, debounceMs],
  );

  // Flush on unmount: the request outlives the component; the server-side result is what
  // the next mount reads back.
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      flush();
    };
  }, [flush]);

  return useMemo(
    () => ({ queue, flush, status, error }),
    [queue, flush, status, error],
  );
}
