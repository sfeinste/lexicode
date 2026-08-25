/*
 * The MUI theme — the bridge between the design tokens and the component library.
 *
 * D-1 (amended) puts the UI on Material UI. That amendment does NOT throw away UI spec §3:
 * `styles/tokens.css` stays the single source of truth for colour, type and density, and
 * this theme consumes it by REFERENCE (`var(--accent)`, not `#6c7cff`). Three things follow
 * from that, all of them load-bearing:
 *
 *  1. Light / dark / system keep working through the mechanism the app already has — the
 *     `data-theme` attribute App.tsx writes on <html>, plus the `prefers-color-scheme`
 *     block in tokens.css. MUI's own colour-scheme machinery is deliberately NOT used;
 *     turning it on would create a second switch that has to be kept in sync with the
 *     first, and "system" (attribute absent) is exactly the case such a switch gets wrong.
 *  2. `tokens.contrast.test.ts` keeps its teeth. It asserts §10's contrast floors against
 *     tokens.css, and every MUI component now resolves to those same values, so the
 *     assertion covers the library's output too.
 *  3. A token edit needs no theme edit.
 *
 * The cost of paying by reference: MUI cannot do colour arithmetic on a `var()`. Every
 * palette entry therefore supplies `light`, `dark` and `contrastText` explicitly —
 * `augmentColor` only computes those when they are missing, so supplying them keeps
 * `getContrastRatio` away from a value it cannot parse. Derived colours (hover tints and
 * the like) come from `color-mix()`, which takes a `var()` happily.
 *
 * Typography, spacing and shape are §3.3 / §2.2 verbatim: nothing larger than 20px, 13px
 * body, monospace only for machine output.
 */
import { createTheme, type Theme } from "@mui/material/styles";

/** A token reference. Colour lives in tokens.css; this file only points at it. */
const token = (name: string): string => `var(--${name})`;

/**
 * A palette entry MUI will not try to compute from. `main` is the token; `light` and `dark`
 * are `color-mix()` derivations of it so hover and active states stay on the same hue
 * instead of falling back to a Material default that ignores §3.2's one-hue-per-meaning rule.
 */
function entry(name: string): {
  main: string;
  light: string;
  dark: string;
  contrastText: string;
} {
  const c = token(name);
  return {
    main: c,
    light: `color-mix(in srgb, ${c} 78%, white)`,
    dark: `color-mix(in srgb, ${c} 82%, black)`,
    contrastText: token("surface"),
  };
}

/**
 * The §3.2 semantic hues that Material's own palette has no slot for. Reached in `sx` as
 * `theme.vars`-style tokens would be, but typed: `theme.palette.lexicode.running`.
 *
 * `--running`, `--needs-you` and `--halt` are the three meanings Material does not model.
 * `--halt` in particular earns its own hue by §3.2's argument — "the system deliberately
 * stopped this" is neither success nor failure — and collapsing it into `error` would undo
 * the single clearest thing about the status vocabulary.
 */
export interface LexicodePalette {
  running: string;
  needsYou: string;
  halt: string;
  muted: string;
  surface2: string;
  surface3: string;
  borderStrong: string;
  text3: string;
}

declare module "@mui/material/styles" {
  interface Palette {
    lexicode: LexicodePalette;
  }
  interface PaletteOptions {
    lexicode?: LexicodePalette;
  }
}

export const lexicodeTheme: Theme = createTheme({
  // Load-bearing, not a preference. With `cssVariables` off, MUI components call `alpha()`
  // on `palette.*.main` at render time and `alpha()` cannot parse a `var()` — Button throws
  // "Unsupported `var(--accent)` color" on first render. With it on, the same derivations
  // go through `color-mix()`, which takes a custom property. This flag is the entire reason
  // paying by reference is possible at all.
  cssVariables: true,
  palette: {
    // Material's slots, mapped onto the §3.2 meanings that fit them.
    primary: entry("accent"),
    error: entry("fail"),
    success: entry("ok"),
    warning: entry("needs-you"),
    info: entry("running"),
    // The three §3.2 hues Material has no slot for, plus the surface ladder steps and the
    // tertiary text tone that §3.1 names but Material's `text` object does not carry.
    lexicode: {
      running: token("running"),
      needsYou: token("needs-you"),
      halt: token("halt"),
      muted: token("muted"),
      surface2: token("surface-2"),
      surface3: token("surface-3"),
      borderStrong: token("border-strong"),
      text3: token("text-3"),
    },
    background: {
      default: token("bg"),
      paper: token("surface"),
    },
    text: {
      primary: token("text"),
      secondary: token("text-2"),
      disabled: token("text-3"),
    },
    divider: token("border"),
    action: {
      // color-mix keeps these on the app's own text hue in both themes; Material's defaults
      // are fixed rgba() values that only look right on one of the two.
      //
      // `active` is the one that bites hardest, so it is first: ToggleButton colours its
      // UNSELECTED label from `palette.action.active` (ToggleButton.js), and Material's
      // default for it is a literal `rgba(0, 0, 0, 0.54)`. Leave it unset and the verbosity
      // switch's Summary/Verbose options render near-black on `--surface` in dark mode —
      // legible in light, effectively invisible in dark, and indistinguishable from
      // `disabled` in both. Found by screenshotting the converted screen in both themes;
      // jsdom renders no colour, which is why theme.tokens.test.ts asserts the shape instead.
      active: token("text-2"),
      hover: `color-mix(in srgb, ${token("text")} 6%, transparent)`,
      selected: `color-mix(in srgb, ${token("text")} 10%, transparent)`,
      disabled: token("text-3"),
      disabledBackground: `color-mix(in srgb, ${token("text")} 8%, transparent)`,
      focus: `color-mix(in srgb, ${token("accent")} 18%, transparent)`,
    },
  },

  // §3.3: the whole scale, and nothing larger than 20px anywhere in the app.
  typography: {
    fontFamily: token("font-ui"),
    fontSize: 13,
    htmlFontSize: 16,
    h1: { fontSize: token("fs-title"), fontWeight: 600, lineHeight: 1.3 },
    h2: { fontSize: token("fs-section"), fontWeight: 600, lineHeight: 1.3 },
    h3: { fontSize: token("fs-section"), fontWeight: 600, lineHeight: 1.3 },
    body1: { fontSize: token("fs-body"), lineHeight: 1.5 },
    body2: { fontSize: token("fs-mono"), lineHeight: 1.5 },
    caption: { fontSize: token("fs-micro"), lineHeight: 1.4 },
    button: { fontSize: token("fs-body"), textTransform: "none", fontWeight: 500 },
  },

  shape: { borderRadius: 4 },

  // §3.4: 120ms ease-out for hover/press, and that is the whole vocabulary for state change.
  transitions: {
    duration: { shortest: 120, shorter: 120, short: 120, standard: 120 },
    easing: { easeOut: "ease-out" },
  },

  components: {
    // §10: "Focus rings on everything interactive, --border-strong, never removed."
    // reset.css states the rule globally; these two restate it for the components that
    // ship their own focus treatment, so a library default can never quietly win.
    MuiButtonBase: {
      defaultProps: { disableRipple: true },
      styleOverrides: {
        root: {
          "&:focus-visible": {
            outline: `2px solid ${token("border-strong")}`,
            outlineOffset: 2,
          },
        },
      },
    },
    // §2.2: "each region gets clear separation via a 1px border and a background step,
    // never via margin alone." Material's default separation device is a shadow, so the
    // default variant here is `outlined` and elevation is off — a Paper that wants a shadow
    // has to ask for one, which under this spec it never should.
    MuiPaper: {
      defaultProps: { elevation: 0, variant: "outlined" },
      styleOverrides: {
        // Material tints elevated surfaces in dark mode with a gradient overlay. The §3.1
        // surface ladder already encodes that step, so the overlay is double-counting.
        root: { backgroundImage: "none" },
      },
    },
    MuiTooltip: {
      defaultProps: { enterDelay: 200 },
    },
    MuiButton: {
      defaultProps: { disableElevation: true },
    },
  },
});

export default lexicodeTheme;
