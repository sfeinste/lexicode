/*
 * Ephemeral UI state (D-1: Zustand's whole job) — rail collapse, theme, density, the two
 * overlays. Server data never lives here; that is TanStack Query's cache.
 *
 * railCollapsed / theme / density persist across reloads. localStorage for now; when the
 * user-preferences API lands, the storage below swaps for it without touching callers.
 * paletteOpen / cheatsheetOpen are session state and deliberately not persisted — nobody
 * wants a command palette that reopens itself on reload.
 */
import { create } from "zustand";
import { persist } from "zustand/middleware";

export type ThemePreference = "light" | "dark" | "system";
export type Density = "comfortable" | "compact";

export interface UIState {
  railCollapsed: boolean;
  theme: ThemePreference;
  density: Density;
  paletteOpen: boolean;
  cheatsheetOpen: boolean;
  toggleRail: () => void;
  setRailCollapsed: (collapsed: boolean) => void;
  setTheme: (theme: ThemePreference) => void;
  setDensity: (density: Density) => void;
  setPaletteOpen: (open: boolean) => void;
  setCheatsheetOpen: (open: boolean) => void;
}

/** The localStorage key the persisted slice lives under. */
export const UI_STORAGE_KEY = "lexicode-ui";

/** Exported for the persistence test; the app uses the useUIStore singleton below. */
export function createUIStore() {
  return create<UIState>()(
    persist(
      (set) => ({
        railCollapsed: false,
        theme: "system",
        density: "comfortable",
        paletteOpen: false,
        cheatsheetOpen: false,
        toggleRail: () => set((s) => ({ railCollapsed: !s.railCollapsed })),
        setRailCollapsed: (railCollapsed) => set({ railCollapsed }),
        setTheme: (theme) => set({ theme }),
        setDensity: (density) => set({ density }),
        setPaletteOpen: (paletteOpen) => set({ paletteOpen }),
        setCheatsheetOpen: (cheatsheetOpen) => set({ cheatsheetOpen }),
      }),
      {
        name: UI_STORAGE_KEY,
        partialize: (s) => ({
          railCollapsed: s.railCollapsed,
          theme: s.theme,
          density: s.density,
        }),
      },
    ),
  );
}

export const useUIStore = createUIStore();
