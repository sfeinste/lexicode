/*
 * App root: providers + router. The order matters — the QueryClient must exist before the
 * router renders anything that queries, and the auth navigator must point at the router so
 * a 401 redirect is a client-side navigation, not a full reload.
 */
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useEffect } from "react";

import { installAuthNavigator } from "../lib/api/client";
import { queryClient } from "../lib/api/queryClient";
import { useKeyboardDispatch } from "../lib/keyboard/hooks";
import { useUIStore } from "../stores/ui";
import { router } from "./router";

installAuthNavigator((path) => {
  void router.navigate({ to: path });
});

/**
 * Reflect the persisted theme and density preferences onto <html>. tokens.css keys off
 * data-theme (explicit light/dark; absent means "system" and the media query decides) and
 * data-density (compact switches --row-height to 28px).
 */
function usePreferenceAttributes(): void {
  const theme = useUIStore((s) => s.theme);
  const density = useUIStore((s) => s.density);
  useEffect(() => {
    const el = document.documentElement;
    if (theme === "system") el.removeAttribute("data-theme");
    else el.setAttribute("data-theme", theme);
  }, [theme]);
  useEffect(() => {
    const el = document.documentElement;
    if (density === "compact") el.setAttribute("data-density", "compact");
    else el.removeAttribute("data-density");
  }, [density]);
}

export function App() {
  usePreferenceAttributes();
  useKeyboardDispatch();
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
}
