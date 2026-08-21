import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { KeyboardRegistry, SEQUENCE_WINDOW_MS, describeKey } from "./registry";

function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

describe("describeKey", () => {
  it.each([
    [{ key: "k", metaKey: true }, "mod+k"],
    [{ key: "k", ctrlKey: true }, "mod+k"],
    [{ key: "\\", metaKey: true }, "mod+\\"],
    [{ key: "?" }, "?"],
    [{ key: "g" }, "g"],
    [{ key: "V", shiftKey: true }, "shift+v"],
    [{ key: "Escape" }, "escape"],
  ])("%o → %s", (init, want) => {
    expect(describeKey(keydown(init))).toBe(want);
  });

  it("ignores bare modifier presses", () => {
    expect(describeKey(keydown({ key: "Meta", metaKey: true }))).toBeNull();
  });
});

describe("KeyboardRegistry scope stacking", () => {
  let reg: KeyboardRegistry;
  let fired: string[];

  const bind = (id: string, scope: "global" | "route" | "list" | "modal", chord: string) =>
    reg.register({ id, scope, chord, title: id, group: "Test", run: () => fired.push(id) });

  beforeEach(() => {
    reg = new KeyboardRegistry();
    fired = [];
  });

  it("global bindings fire with no other scope active", () => {
    bind("g.x", "global", "x");
    expect(reg.handleKeydown(keydown({ key: "x" }))).toBe(true);
    expect(fired).toEqual(["g.x"]);
  });

  it("a higher scope shadows a lower one for the same chord, and unwinds", () => {
    bind("global.esc", "global", "escape");
    bind("modal.esc", "modal", "escape");

    reg.handleKeydown(keydown({ key: "Escape" }));
    expect(fired).toEqual(["global.esc"]); // modal not active yet

    const deactivate = reg.activateScope("modal");
    reg.handleKeydown(keydown({ key: "Escape" }));
    expect(fired).toEqual(["global.esc", "modal.esc"]);

    deactivate();
    reg.handleKeydown(keydown({ key: "Escape" }));
    expect(fired).toEqual(["global.esc", "modal.esc", "global.esc"]);
  });

  it("stacks all four scopes in order: global < route < list < modal", () => {
    bind("s.global", "global", "j");
    bind("s.route", "route", "j");
    bind("s.list", "list", "j");
    bind("s.modal", "modal", "j");

    const offRoute = reg.activateScope("route");
    reg.handleKeydown(keydown({ key: "j" }));
    const offList = reg.activateScope("list");
    reg.handleKeydown(keydown({ key: "j" }));
    const offModal = reg.activateScope("modal");
    reg.handleKeydown(keydown({ key: "j" }));
    offModal();
    reg.handleKeydown(keydown({ key: "j" }));
    offList();
    offRoute();
    reg.handleKeydown(keydown({ key: "j" }));

    expect(fired).toEqual(["s.route", "s.list", "s.modal", "s.list", "s.global"]);
  });

  it("a binding in an inactive scope never fires", () => {
    bind("l.only", "list", "k");
    expect(reg.handleKeydown(keydown({ key: "k" }))).toBe(false);
    expect(fired).toEqual([]);
  });

  it("unregistering removes the binding", () => {
    const off = bind("g.y", "global", "y");
    off();
    expect(reg.handleKeydown(keydown({ key: "y" }))).toBe(false);
  });

  it("disabled bindings do not fire and do not shadow", () => {
    bind("g.z", "global", "z");
    reg.register({
      id: "modal.z",
      scope: "modal",
      chord: "z",
      title: "off",
      group: "Test",
      enabled: () => false,
      run: () => fired.push("modal.z"),
    });
    reg.activateScope("modal");
    reg.handleKeydown(keydown({ key: "z" }));
    expect(fired).toEqual(["g.z"]);
  });
});

describe("KeyboardRegistry sequences", () => {
  let reg: KeyboardRegistry;
  let fired: string[];

  beforeEach(() => {
    vi.useFakeTimers();
    reg = new KeyboardRegistry();
    fired = [];
    reg.register({
      id: "go.board",
      scope: "global",
      chord: "g b",
      title: "Go to Board",
      group: "Navigation",
      run: () => fired.push("go.board"),
    });
    reg.register({
      id: "single.b",
      scope: "global",
      chord: "b",
      title: "B alone",
      group: "Test",
      run: () => fired.push("single.b"),
    });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("fires the chord when both steps land inside the window", () => {
    reg.handleKeydown(keydown({ key: "g" }));
    reg.handleKeydown(keydown({ key: "b" }));
    expect(fired).toEqual(["go.board"]);
  });

  it("the pending prefix expires after the sequence window", () => {
    reg.handleKeydown(keydown({ key: "g" }));
    vi.advanceTimersByTime(SEQUENCE_WINDOW_MS + 1);
    reg.handleKeydown(keydown({ key: "b" }));
    expect(fired).toEqual(["single.b"]); // g expired; b matched on its own
  });

  it("a broken sequence lets the final key start over", () => {
    reg.handleKeydown(keydown({ key: "g" }));
    reg.handleKeydown(keydown({ key: "x" })); // not a continuation
    reg.handleKeydown(keydown({ key: "b" }));
    expect(fired).toEqual(["single.b"]);
  });
});

describe("KeyboardRegistry and editable targets", () => {
  it("plain keys pass through an input; mod chords still fire", () => {
    const reg = new KeyboardRegistry();
    const fired: string[] = [];
    reg.register({
      id: "t.c",
      scope: "global",
      chord: "c",
      title: "New ticket",
      group: "Test",
      run: () => fired.push("t.c"),
    });
    reg.register({
      id: "t.palette",
      scope: "global",
      chord: "mod+k",
      title: "Palette",
      group: "Test",
      run: () => fired.push("t.palette"),
    });

    const input = document.createElement("input");
    document.body.appendChild(input);
    const listener = (e: KeyboardEvent) => reg.handleKeydown(e);
    window.addEventListener("keydown", listener);
    try {
      input.dispatchEvent(keydown({ key: "c" }));
      input.dispatchEvent(keydown({ key: "k", metaKey: true }));
      expect(fired).toEqual(["t.palette"]);
    } finally {
      window.removeEventListener("keydown", listener);
      input.remove();
    }
  });
});
