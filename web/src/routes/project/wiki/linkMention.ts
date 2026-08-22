/*
 * One-click linking of an unlinked mention (S33). The click edits the SOURCE page: replace
 * the first plain occurrence of the target's title in that page's body with the mention
 * token and PATCH the source — which appends a version and re-derives its mentions like any
 * body edit. Mirrors the server's detector: occurrences inside existing mention tokens are
 * masked out first, so a token's label never gets double-linked.
 */
import { mentionPattern } from "../../../components/Editor/mentionPattern";

/** Mask every mention token with spaces (same length — indices map 1:1 to the original). */
function maskTokens(body: string): string {
  return body.replace(mentionPattern, (tok) => " ".repeat(tok.length));
}

/**
 * Replace the first plain (outside-any-token, case-insensitive) occurrence of `title` in
 * `body` with the `@[title](wiki:id)` token. Returns null when no plain occurrence exists —
 * the caller refetches rather than guessing.
 */
export function linkFirstPlainOccurrence(
  body: string,
  title: string,
  targetId: string,
): string | null {
  if (title === "") return null;
  const masked = maskTokens(body).toLowerCase();
  const idx = masked.indexOf(title.toLowerCase());
  if (idx === -1) return null;
  const token = `@[${title}](wiki:${targetId})`;
  return body.slice(0, idx) + token + body.slice(idx + title.length);
}
