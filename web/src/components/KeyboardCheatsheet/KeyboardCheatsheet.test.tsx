/*
 * S07 acceptance: the cheatsheet lists every registered binding with no hardcoded duplicate
 * list. Proven by registering bindings the component has never heard of and finding them
 * rendered.
 */
import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { keyboard } from "../../lib/keyboard/registry";
import { useUIStore } from "../../stores/ui";
import { KeyboardCheatsheet } from "./KeyboardCheatsheet";

const offs: Array<() => void> = [];

afterEach(() => {
  while (offs.length) offs.pop()!();
  useUIStore.setState({ cheatsheetOpen: false });
});

function reg(id: string, chord: string, title: string, group: string) {
  offs.push(
    keyboard.register({ id, scope: "global", chord, title, group, run: () => {} }),
  );
}

describe("KeyboardCheatsheet", () => {
  it("renders nothing while closed", () => {
    reg("t.a", "a", "Alpha action", "Test group");
    render(<KeyboardCheatsheet />);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders every registered binding from the registry, grouped", () => {
    reg("t.a", "a", "Alpha action", "Group one");
    reg("t.b", "mod+b", "Beta action", "Group one");
    reg("t.nav", "g z", "Zeta navigation", "Group two");
    useUIStore.setState({ cheatsheetOpen: true });

    render(<KeyboardCheatsheet />);
    expect(screen.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeTruthy();
    expect(screen.getByText("Alpha action")).toBeTruthy();
    expect(screen.getByText("Beta action")).toBeTruthy();
    expect(screen.getByText("Zeta navigation")).toBeTruthy();
    expect(screen.getByText("Group one")).toBeTruthy();
    expect(screen.getByText("Group two")).toBeTruthy();
    // The chord labels come from the same registrations.
    expect(screen.getByText("G Z")).toBeTruthy();
  });

  it("reflects bindings registered after mount (it reads, it does not copy)", () => {
    useUIStore.setState({ cheatsheetOpen: true });
    render(<KeyboardCheatsheet />);
    expect(screen.queryByText("Late arrival")).toBeNull();

    act(() => reg("t.late", "l", "Late arrival", "Group three"));
    expect(screen.getByText("Late arrival")).toBeTruthy();
  });
});
