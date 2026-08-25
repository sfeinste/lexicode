/*
 * The MUI theme boundary.
 *
 * It resolves the mode the way the rest of the app already does — the persisted preference
 * from `stores/ui` when it is "light" or "dark", the OS preference when it is "system" —
 * so the existing user-menu Theme control drives MUI components with no second switch, and
 * `data-theme` on <html> and MUI's palette can never disagree.
 *
 * Scoped, not global: while the migration is mid-flight (design/ui-library-evaluation.md
 * §7, stage 2) only converted screens mount this, so an unconverted screen is untouched.
 * `ScopedCssBaseline` is deliberate for the same reason — MUI's global `CssBaseline` would
 * reset typography for screens that still rely on styles/reset.css.
 */
import ScopedCssBaseline from "@mui/material/ScopedCssBaseline";
import { ThemeProvider } from "@mui/material/styles";
import { useMemo, type ReactNode } from "react";

import { useMediaQuery } from "../lib/useMediaQuery";
import { useUIStore } from "../stores/ui";
import { buildTheme, type ThemeMode } from "./muiTheme";

export function MuiThemeProvider({ children }: { children: ReactNode }) {
  const preference = useUIStore((s) => s.theme);
  const density = useUIStore((s) => s.density);
  // The fallback matters in jsdom, which has no matchMedia: tests then see the light
  // palette, which is also what tokens.css's bare :root block gives them.
  const prefersDark = useMediaQuery("(prefers-color-scheme: dark)", false);
  const mode: ThemeMode =
    preference === "system" ? (prefersDark ? "dark" : "light") : preference;

  const theme = useMemo(() => buildTheme(mode, density), [mode, density]);

  return (
    <ThemeProvider theme={theme}>
      <ScopedCssBaseline sx={{ bgcolor: "transparent", height: "100%" }}>
        {children}
      </ScopedCssBaseline>
    </ThemeProvider>
  );
}
