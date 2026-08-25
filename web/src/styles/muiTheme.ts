/*
 * The MUI theme, derived FROM tokens.css rather than duplicating it.
 *
 * D-1's amendment (plan/00-decisions.md) reverses "no component library" but keeps the §3
 * token ladder as the single source of colour: tokens.css stays the one file that defines
 * a hue, `styles/tokens.contrast.test.ts` keeps asserting the §10 contrast floors against
 * it, and this module parses that same stylesheet at import time and hands the values to
 * `createTheme`. Editing a token therefore still moves the whole app — including every MUI
 * component — and still fails the contrast test if it drops below 4.5:1 / 3:1.
 *
 * Why parse instead of `palette: { primary: { main: "var(--accent)" } }`: MUI runs real
 * colour maths on palette entries (`alpha()`, `lighten()`, `getContrastRatio()`), and those
 * throw or silently misbehave on a `var()` string. Parsed hex keeps every MUI feature
 * working; the stylesheet stays authoritative.
 *
 * Density: UI spec §2.2's 32px / 28px rows arrive as MUI's `spacing` and the shared row
 * height token, so the existing user-menu density switch drives MUI components too.
 */
import { createTheme, type Theme } from "@mui/material/styles";

import tokensCss from "./tokens.css?raw";

/** The token names this module reads. Kept narrow on purpose — the rest are layout/type. */
export interface Palette {
  bg: string;
  surface: string;
  "surface-2": string;
  "surface-3": string;
  border: string;
  "border-strong": string;
  text: string;
  "text-2": string;
  "text-3": string;
  accent: string;
  running: string;
  "needs-you": string;
  ok: string;
  fail: string;
  halt: string;
  muted: string;
}

/**
 * Pull the `--name: #hex;` declarations out of one top-level block of tokens.css. The same
 * shape tokens.contrast.test.ts uses, for the same reason: the stylesheet is the source.
 */
function readPalette(selectorStart: string): Palette {
  const start = tokensCss.indexOf(selectorStart);
  if (start === -1) throw new Error(`tokens.css lost its "${selectorStart}" block`);
  const open = tokensCss.indexOf("{", start);
  const close = tokensCss.indexOf("}", open);
  const body = tokensCss.slice(open + 1, close);
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})\s*;/g)) {
    out[m[1]] = m[2];
  }
  return out as unknown as Palette;
}

export const LIGHT_TOKENS = readPalette(":root {");
export const DARK_TOKENS = readPalette(':root[data-theme="dark"] {');

export type ThemeMode = "light" | "dark";

/** UI spec §2.2: base row 32px, compact 28px. */
export type Density = "comfortable" | "compact";

const FONT_UI = 'ui-sans-serif, -apple-system, "Segoe UI", Inter, sans-serif';
const FONT_MONO = 'ui-monospace, "SF Mono", "JetBrains Mono", Menlo, monospace';

/**
 * Build the MUI theme for a mode and density.
 *
 * The component overrides below are the "dense dev tool, not Material" adjustment the
 * evaluation calls out as MUI's main weakness for this product (design/ui-library-
 * evaluation.md §5). They are ordinary `components.styleOverrides` — MUI's own documented
 * theming mechanism — not a parallel component set.
 */
export function buildTheme(mode: ThemeMode, density: Density = "comfortable"): Theme {
  const t = mode === "dark" ? DARK_TOKENS : LIGHT_TOKENS;
  const rowHeight = density === "compact" ? 28 : 32;

  return createTheme({
    palette: {
      mode,
      primary: { main: t.accent },
      error: { main: t.fail },
      success: { main: t.ok },
      warning: { main: t["needs-you"] },
      info: { main: t.running },
      background: { default: t.bg, paper: t.surface },
      text: { primary: t.text, secondary: t["text-2"], disabled: t["text-3"] },
      divider: t.border,
      action: { hover: t["surface-2"], selected: t["surface-3"], focus: t["border-strong"] },
    },
    // §3.3's scale in full. Nothing larger than 20px exists anywhere in the app, so the
    // Material type ramp is replaced rather than extended.
    typography: {
      fontFamily: FONT_UI,
      fontSize: 13,
      htmlFontSize: 16,
      h1: { fontSize: 20, fontWeight: 600, lineHeight: 1.3 },
      h2: { fontSize: 15, fontWeight: 600, lineHeight: 1.35 },
      h3: { fontSize: 13, fontWeight: 600, lineHeight: 1.4 },
      body1: { fontSize: 13, lineHeight: 1.5 },
      body2: { fontSize: 12, lineHeight: 1.45 },
      caption: { fontSize: 11, lineHeight: 1.4 },
      button: { fontSize: 13, textTransform: "none", fontWeight: 500 },
    },
    shape: { borderRadius: 4 },
    // §3.4: 120ms ease-out for state changes; nothing slides, nothing bounces.
    transitions: { duration: { shortest: 120, shorter: 120, short: 120, standard: 120 } },
    components: {
      // §2.2: "whitespace is a cost, not a virtue" — Material's 36px min-height and
      // uppercase labels are the single biggest mismatch with this product.
      MuiButton: {
        defaultProps: { disableElevation: true, size: "small" },
        styleOverrides: {
          root: { minHeight: rowHeight, paddingBlock: 2, borderRadius: 4 },
        },
      },
      MuiIconButton: { defaultProps: { size: "small" } },
      MuiChip: {
        defaultProps: { size: "small" },
        styleOverrides: { root: { borderRadius: 4, height: 20, fontSize: 11 } },
      },
      MuiToggleButton: {
        defaultProps: { size: "small" },
        styleOverrides: {
          root: { textTransform: "none", paddingBlock: 2, minHeight: 24, fontSize: 12 },
        },
      },
      MuiTextField: { defaultProps: { size: "small", variant: "outlined" } },
      MuiSelect: { defaultProps: { size: "small" } },
      MuiTooltip: { defaultProps: { arrow: false, enterDelay: 300 } },
      MuiTab: {
        styleOverrides: { root: { textTransform: "none", minHeight: 34, fontSize: 13 } },
      },
      MuiTableCell: {
        styleOverrides: {
          root: { paddingBlock: 4, paddingInline: 8, fontSize: 12, borderColor: t.border },
        },
      },
      MuiPaper: {
        defaultProps: { elevation: 0 },
        styleOverrides: { root: { backgroundImage: "none", border: `1px solid ${t.border}` } },
      },
      MuiListItemButton: {
        styleOverrides: { root: { minHeight: rowHeight, paddingBlock: 0 } },
      },
      // §10: "Focus rings on everything interactive, --border-strong, never removed."
      MuiButtonBase: {
        styleOverrides: {
          root: {
            "&.Mui-focusVisible": { outline: `2px solid ${t["border-strong"]}`, outlineOffset: 1 },
          },
        },
      },
      MuiCssBaseline: {
        styleOverrides: { code: { fontFamily: FONT_MONO }, pre: { fontFamily: FONT_MONO } },
      },
    },
  });
}

export const MONO_FONT = FONT_MONO;
