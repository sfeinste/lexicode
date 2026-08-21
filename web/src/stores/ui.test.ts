/*
 * S07 acceptance: rail collapse persists across reload. Simulated by writing through one
 * store instance and hydrating a fresh one from the same localStorage (what a reload does).
 */
import { beforeEach, describe, expect, it } from "vitest";

import { UI_STORAGE_KEY, createUIStore } from "./ui";

describe("UI store persistence", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("persists rail collapse and restores it in a fresh store", () => {
    const first = createUIStore();
    expect(first.getState().railCollapsed).toBe(false);

    first.getState().toggleRail();
    expect(first.getState().railCollapsed).toBe(true);

    const second = createUIStore(); // a reload: new store, same storage
    expect(second.getState().railCollapsed).toBe(true);
  });

  it("persists theme and density the same way", () => {
    const first = createUIStore();
    first.getState().setTheme("dark");
    first.getState().setDensity("compact");

    const second = createUIStore();
    expect(second.getState().theme).toBe("dark");
    expect(second.getState().density).toBe("compact");
  });

  it("does not persist the ephemeral overlay state", () => {
    const first = createUIStore();
    first.getState().setPaletteOpen(true);
    first.getState().setCheatsheetOpen(true);
    first.getState().toggleRail(); // force a persist write

    const raw = localStorage.getItem(UI_STORAGE_KEY);
    expect(raw).toBeTruthy();
    expect(raw).not.toContain("paletteOpen");
    expect(raw).not.toContain("cheatsheetOpen");

    const second = createUIStore();
    expect(second.getState().paletteOpen).toBe(false);
    expect(second.getState().cheatsheetOpen).toBe(false);
  });

  it("toggling back persists the collapsed=false state too", () => {
    const first = createUIStore();
    first.getState().toggleRail();
    first.getState().toggleRail();

    const second = createUIStore();
    expect(second.getState().railCollapsed).toBe(false);
  });
});
