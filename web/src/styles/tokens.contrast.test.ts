/*
 * UI spec §10 contrast requirements, asserted against the real tokens.css:
 *
 *   - body text >= 4.5:1 against its surface, in both themes (--text and --text-2 against
 *     every surface in the ladder);
 *   - status colors >= 3:1 for the glyph, in both themes (every §3.2 semantic color against
 *     the backgrounds a StatusDot sits on).
 *
 * The test parses the stylesheet rather than duplicating values, so editing a token
 * recomputes the assertion. --text-3 is exempt by design: the spec labels it "tertiary,
 * timestamps, disabled", not body text.
 */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));
const css = readFileSync(join(here, "tokens.css"), "utf8");

/** Pull `--name: #hex;` declarations out of one top-level block of the stylesheet. */
function palette(selectorStart: string): Record<string, string> {
  const start = css.indexOf(selectorStart);
  if (start === -1) throw new Error(`tokens.css lost its "${selectorStart}" block`);
  const open = css.indexOf("{", start);
  const close = css.indexOf("}", open);
  const body = css.slice(open + 1, close);
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    out[m[1]] = m[2];
  }
  return out;
}

/** WCAG 2.x relative luminance of a #rrggbb color. */
function luminance(hex: string): number {
  const chan = (i: number) => {
    const c = parseInt(hex.slice(1 + 2 * i, 3 + 2 * i), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  };
  return 0.2126 * chan(0) + 0.7152 * chan(1) + 0.0722 * chan(2);
}

/** WCAG contrast ratio between two #rrggbb colors. */
export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

const SURFACES = ["bg", "surface", "surface-2", "surface-3"] as const;
const TEXT = ["text", "text-2"] as const;
const STATUS = ["accent", "running", "needs-you", "ok", "fail", "halt", "muted"] as const;
// StatusDot glyphs sit on the app background, on cards and on hover/selected rows.
const GLYPH_BACKGROUNDS = SURFACES;

const themes = {
  light: palette(":root {"),
  dark: palette(':root[data-theme="dark"] {'),
};

describe.each(Object.entries(themes))("tokens.css %s palette", (_name, tokens) => {
  it("defines every token the contrast rules cover", () => {
    for (const name of [...SURFACES, ...TEXT, ...STATUS]) {
      expect(tokens[name], `--${name}`).toMatch(/^#[0-9a-fA-F]{6}$/);
    }
  });

  it.each(TEXT.flatMap((t) => SURFACES.map((s) => [t, s] as const)))(
    "--%s on --%s is at least 4.5:1",
    (text, surface) => {
      expect(contrast(tokens[text], tokens[surface])).toBeGreaterThanOrEqual(4.5);
    },
  );

  it.each(STATUS.flatMap((c) => GLYPH_BACKGROUNDS.map((s) => [c, s] as const)))(
    "--%s on --%s is at least 3:1",
    (color, surface) => {
      expect(contrast(tokens[color], tokens[surface])).toBeGreaterThanOrEqual(3);
    },
  );
});

it("the explicit dark block and the prefers-color-scheme dark block agree", () => {
  const media = css.slice(css.indexOf("@media (prefers-color-scheme: dark)"));
  const explicit = themes.dark;
  for (const [name, value] of Object.entries(explicit)) {
    expect(media, `--${name} drifted between the two dark blocks`).toContain(
      `--${name}: ${value};`,
    );
  }
});
