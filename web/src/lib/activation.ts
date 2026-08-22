/*
 * The two §8 activation moments (S38): "first completed run" and "first needs input".
 * Each is shown exactly once per project, then never again.
 *
 * Persistence decision: localStorage, same as the UI store's persisted slice (ui.ts) — an
 * activation moment is a per-browser teaching aid, not workspace data, and losing it on a
 * new machine (the moment shows once more) is harmless. When a server-side user-preferences
 * API lands, this module is the single place to swap.
 */

export type ActivationMoment = "first-completed-run" | "first-needs-input";

const keyFor = (moment: ActivationMoment, projectKey: string): string =>
  `lexicode-moment:${moment}:${projectKey}`;

function storage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null; // Storage denied (private mode policies): the moment simply never shows.
  }
}

/** True while the moment has not been shown for this project. */
export function momentPending(moment: ActivationMoment, projectKey: string): boolean {
  return storage()?.getItem(keyFor(moment, projectKey)) === null;
}

/** Record the moment as shown — momentPending is false from now on. */
export function markMomentSeen(moment: ActivationMoment, projectKey: string): void {
  storage()?.setItem(keyFor(moment, projectKey), new Date().toISOString());
}
