/*
 * The theme pays for colour BY REFERENCE, and this suite is what keeps it honest.
 *
 * D-1 amendment A-1 makes `styles/tokens.css` the single source of truth for colour and lets
 * the MUI theme consume it as `var(--token)`. The failure mode that arrangement has is silent:
 * MUI's `createPalette` FILLS IN any slot the theme leaves unset, with a literal light-mode
 * value — `action.active` defaults to `rgba(0, 0, 0, 0.54)`, `action.hover` to
 * `rgba(0, 0, 0, 0.04)`, and so on. A component that reads such a slot then renders a
 * hard-coded near-black in BOTH themes, which is fine in light and invisible in dark.
 *
 * That is not hypothetical. `ToggleButton` colours its unselected label from
 * `palette.action.active` (ToggleButton.js), so before this file existed the run detail's
 * Summary / Verbose options were near-black on `--surface` in dark mode. jsdom renders no
 * colours and axe's contrast rule needs real layout, so neither `axe.test.tsx` nor
 * `tokens.contrast.test.ts` could see it — it was found by screenshotting the real bundle
 * (`web/scripts/screenshot.mjs`).
 *
 * So the assertion here is structural rather than perceptual: every palette value the theme
 * resolves must be expressed in terms of the tokens, never as a literal colour. A future slot
 * left unset fails by name, before it reaches a screen.
 */
import { describe, expect, it } from "vitest";

import { lexicodeTheme } from "./theme";

/** A literal colour: what a Material default looks like and a token reference never does. */
const LITERAL = /^(#|rgb|rgba|hsl|hsla)\(?/i;

/** Token-derived: either the custom property itself or a color-mix() over one. */
function isTokenDerived(value: string): boolean {
  return value.includes("var(--");
}

/**
 * The palette slots this app's components actually read that MUI would otherwise default.
 * `action` is the whole object because every one of its entries has a literal default.
 */
function flatten(obj: unknown, prefix: string): Array<[string, string]> {
  if (typeof obj === "string") return [[prefix, obj]];
  if (obj === null || typeof obj !== "object") return [];
  return Object.entries(obj as Record<string, unknown>).flatMap(([k, v]) =>
    flatten(v, prefix === "" ? k : `${prefix}.${k}`),
  );
}

describe("the MUI theme pays for colour by reference (D-1 amendment A-1)", () => {
  it("leaves no palette.action slot on a Material literal", () => {
    // The regression that motivated this file. `active` is the one ToggleButton reads.
    for (const [path, value] of flatten(lexicodeTheme.palette.action, "action")) {
      if (!LITERAL.test(value)) continue;
      expect(
        isTokenDerived(value),
        `palette.${path} is the literal "${value}" — a Material default that does not follow ` +
          "data-theme, so it renders the same colour in light and dark. Give it a var(--token) " +
          "or a color-mix() over one in web/src/theme/theme.ts.",
      ).toBe(true);
    }
  });

  it("keeps action.active on the tokens, so ToggleButton's unselected label is legible in both themes", () => {
    expect(lexicodeTheme.palette.action.active).toContain("var(--text-2)");
  });

  it("maps every §3.2 semantic hue onto a token, not a copy of its hex", () => {
    const slots: Array<[string, string]> = [
      ["primary", lexicodeTheme.palette.primary.main],
      ["error", lexicodeTheme.palette.error.main],
      ["success", lexicodeTheme.palette.success.main],
      ["warning", lexicodeTheme.palette.warning.main],
      ["info", lexicodeTheme.palette.info.main],
      ["background.default", lexicodeTheme.palette.background.default],
      ["background.paper", lexicodeTheme.palette.background.paper],
      ["text.primary", lexicodeTheme.palette.text.primary],
      ["text.secondary", lexicodeTheme.palette.text.secondary],
      ["divider", lexicodeTheme.palette.divider],
    ];
    for (const [name, value] of slots) {
      expect(
        isTokenDerived(value),
        `palette.${name} is "${value}" — it must reference tokens.css, or a token edit stops ` +
          "reaching the library and tokens.contrast.test.ts stops covering it.",
      ).toBe(true);
    }
  });

  it("carries the three §3.2 meanings Material has no slot for", () => {
    // running / needs-you / halt. Collapsing `halt` into `error` would undo the clearest
    // distinction in the §4 vocabulary: "the system deliberately stopped this" is not a failure.
    for (const [name, value] of Object.entries(lexicodeTheme.palette.lexicode)) {
      expect(isTokenDerived(value), `palette.lexicode.${name} is "${value}"`).toBe(true);
    }
  });
});
