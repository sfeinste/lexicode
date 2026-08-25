/*
 * The root route's component: the Material UI theme provider, and nothing else.
 *
 * It sits on the ROOT ROUTE rather than in App.tsx deliberately. The axe suite, the
 * reachability crawl and every future render test mount `routeTree` directly with their own
 * providers, so a ThemeProvider in App.tsx would be absent from exactly the tests that need
 * to see the real thing — light/dark, the §3.2 hues, the §10 focus ring. Here, anything
 * that renders a route renders it themed. (D-1 amendment, S39.)
 *
 * No `CssBaseline`. `styles/reset.css` already does that job for the whole app, and while
 * the migration is in flight the screens that have not converted yet are still styled by
 * their CSS modules — dropping Material's baseline on top of them would change every
 * unconverted screen at once, which is exactly what a staged migration is for avoiding.
 * CssBaseline lands in the stage that retires the last CSS module.
 */
import { Outlet } from "@tanstack/react-router";
import { ThemeProvider } from "@mui/material/styles";

import { lexicodeTheme } from "../../theme/theme";

export function RootLayout() {
  return (
    <ThemeProvider theme={lexicodeTheme}>
      <Outlet />
    </ThemeProvider>
  );
}
