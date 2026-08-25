/*
 * §3.2's seven meanings, as paths into the Material UI theme palette.
 *
 * Material names four of them. `running`, `needs-you` (a distinct amber from `warning`'s use
 * elsewhere), `halt` and `muted` live in the theme's `lexicode` slot because Material has no
 * equivalent — `halt` most of all, since UI spec §3.2 makes the case that "the system
 * deliberately stopped this" is neither success nor error and collapsing it into either is
 * the single most common source of confusion in automation products.
 *
 * Its own module so that StatusDot.tsx stays a component file (the react-refresh lint rule
 * wants component modules to export components) and so the mapping can be asserted directly
 * in StatusDot.test.tsx.
 */
import type { StatusMeta } from "./StatusDot";

export const PALETTE_PATH: Record<StatusMeta["color"], string> = {
  accent: "primary.main",
  running: "lexicode.running",
  "needs-you": "lexicode.needsYou",
  ok: "success.main",
  fail: "error.main",
  halt: "lexicode.halt",
  muted: "lexicode.muted",
};
