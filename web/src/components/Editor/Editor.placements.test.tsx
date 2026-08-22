/*
 * The shared Editor placement suite (S12 acceptance: "the editor behaves identically in all
 * four placements — a shared test suite runs against each mount"). Two placements exist
 * today — the ticket description (DescriptionSection) and the comment composer (Composer);
 * the wiki and directive editors join this PLACEMENTS list when their stories land. The
 * suite mounts each placement exactly as the ticket page does (real components, real
 * props), then asserts the same behaviors through the placement's own textarea:
 *
 *   1. `@` opens the mention menu (with the honest agent/wiki empty states) and picking
 *      an item inserts the canonical `@[label](kind:id)` token.
 *   2. `/` at line start opens the slash menu and /bullet inserts "- ".
 *   3. Paste inserts the clipboard's plain text (multi-line preserved) at the caret.
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { Composer } from "../../routes/project/ticket/Composer";
import { DescriptionSection } from "../../routes/project/ticket/DescriptionSection";
import type { MentionSources } from "./engine";

const MENTIONS: MentionSources = {
  users: [{ kind: "user", id: "u1", label: "Ada" }],
  agents: [],
  wiki: [],
  tickets: [{ kind: "ticket", id: "t2", label: "PAY-2", hint: "The other ticket" }],
};

interface Placement {
  name: string;
  /** Mount the real placement component; return how to reach its textarea. */
  mount: () => { textarea: () => HTMLTextAreaElement };
}

function DescriptionHarness() {
  const [value, setValue] = useState("");
  return <DescriptionSection value={value} onChange={setValue} mentions={MENTIONS} />;
}

const PLACEMENTS: Placement[] = [
  {
    name: "ticket description (DescriptionSection)",
    mount: () => {
      render(<DescriptionHarness />);
      return {
        textarea: () =>
          screen.getByRole("textbox", { name: "Description" }) as HTMLTextAreaElement,
      };
    },
  },
  {
    name: "comment composer (Composer)",
    mount: () => {
      render(
        <Composer mentions={MENTIONS} onPost={vi.fn(() => Promise.resolve())} />,
      );
      return {
        textarea: () =>
          screen.getByRole("textbox", { name: "Add a comment" }) as HTMLTextAreaElement,
      };
    },
  },
];

describe.each(PLACEMENTS)("Editor placement: $name", ({ mount }) => {
  it("opens the mention menu on @ and inserts the canonical token", () => {
    const { textarea } = mount();
    const ta = textarea();

    fireEvent.change(ta, { target: { value: "@Ad" } });
    const menu = screen.getByRole("listbox", { name: "Mentions" });
    const option = within(menu).getByRole("option", { name: /Ada/ });
    expect(option).toBeTruthy();

    fireEvent.keyDown(ta, { key: "Enter" });
    expect(textarea().value).toBe("@[Ada](user:u1) ");
  });

  it("renders the honest agent/wiki empty states in the mention menu", () => {
    const { textarea } = mount();
    fireEvent.change(textarea(), { target: { value: "@" } });
    const menu = screen.getByRole("listbox", { name: "Mentions" });
    expect(within(menu).getByText("No agents yet")).toBeTruthy();
    expect(within(menu).getByText("No wiki pages yet")).toBeTruthy();
    // Ticket sources appear alongside users — four kinds, one menu.
    expect(within(menu).getByRole("option", { name: /PAY-2/ })).toBeTruthy();
  });

  it("opens the slash menu on / at line start and applies /bullet", () => {
    const { textarea } = mount();
    const ta = textarea();

    fireEvent.change(ta, { target: { value: "/bu" } });
    const menu = screen.getByRole("listbox", { name: "Slash commands" });
    expect(within(menu).getByRole("option", { name: /\/bullet/ })).toBeTruthy();

    fireEvent.keyDown(ta, { key: "Enter" });
    expect(textarea().value).toBe("- ");
  });

  it("pastes plain text, multi-line preserved, at the caret", () => {
    const { textarea } = mount();
    const ta = textarea();

    fireEvent.paste(ta, {
      clipboardData: { getData: () => "line one\nline two" },
    });
    expect(textarea().value).toBe("line one\nline two");
  });

  it("does not treat a mid-word @ as a mention trigger", () => {
    const { textarea } = mount();
    const ta = textarea();
    fireEvent.change(ta, { target: { value: "mail me at spruce@exa" } });
    expect(screen.queryByRole("listbox", { name: "Mentions" })).toBeNull();
  });
});
