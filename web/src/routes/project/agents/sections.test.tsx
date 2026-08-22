/*
 * S16 acceptance (brief D7): the permission checkboxes and the directive editor must be
 * unmistakably different controls. Asserted structurally — the permissions section carries
 * the enforcement panel class and data attributes plus a lock icon per row, while the
 * directive section is a plain section with a monospace textarea and neither enforcement
 * marker — and behaviorally for the version list, diff view and live token estimate.
 */
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { AgentPermissions, Directive } from "../../../lib/api/client";
import styles from "./agents.module.css";
import { diffLines } from "./diff";
import { estimateTokens } from "./constants";
import { AutonomySection, DirectiveSection, PermissionsSection } from "./sections";

const PERMS: AgentPermissions = {
  read_files: true,
  edit_files: false,
  run_commands: true,
  push_branches: false,
  open_prs: false,
  comment_prs: true,
  submit_reviews: true,
  create_wiki_pages: false,
};

const VERSIONS: Directive[] = [
  {
    id: "d2",
    agent_id: "a1",
    version: 2,
    body: "line one\nline two changed",
    token_estimate: 6,
    author_id: null,
    note: "tightened",
    created_at: "2026-08-21T10:00:00Z",
  },
  {
    id: "d1",
    agent_id: "a1",
    version: 1,
    body: "line one\nline two",
    token_estimate: 4,
    author_id: null,
    note: "",
    created_at: "2026-08-20T10:00:00Z",
  },
];

function renderDirective(value = "hello world directive") {
  return render(
    <DirectiveSection
      value={value}
      onChange={() => {}}
      onSave={() => {}}
      saving={false}
      versions={VERSIONS}
      currentVersionId="d2"
    />,
  );
}

describe("PermissionsSection vs DirectiveSection (D7: enforcement is not guidance)", () => {
  it("renders permissions as the enforcement panel with a lock icon on every checkbox", () => {
    const { container } = render(<PermissionsSection permissions={PERMS} onChange={() => {}} />);
    const panel = container.querySelector('[data-section="permissions"]')!;
    expect(panel).toBeTruthy();
    // The distinct-surface styling hook and the explicit enforcement marker.
    expect(panel.className).toContain(styles.permissionsPanel);
    expect(panel.getAttribute("data-enforcement")).toBe("true");
    // Eight checkboxes, one per §3.1 permission — checkboxes, never a textarea.
    const boxes = within(panel as HTMLElement).getAllByRole("checkbox");
    expect(boxes).toHaveLength(8);
    expect(panel.querySelector("textarea")).toBeNull();
    // A lock icon on every permission row (plus the heading's).
    const locks = panel.querySelectorAll('[data-icon="lock"]');
    expect(locks.length).toBeGreaterThanOrEqual(8);
    // The verbatim enforcement sentence from UI spec §5.8.
    expect(panel.textContent).toContain(
      "A reviewer with edit unchecked cannot write code; that is stronger than telling it not to.",
    );
  });

  it("renders the directive as a plain monospace editor with none of the enforcement styling", () => {
    const { container } = renderDirective();
    const section = container.querySelector('[data-section="directive"]')!;
    expect(section).toBeTruthy();
    // Different surface: the ordinary section class, not the enforcement panel; no
    // enforcement marker, no lock icons, no checkboxes.
    expect(section.className).toContain(styles.section);
    expect(section.className).not.toContain(styles.permissionsPanel);
    expect(section.getAttribute("data-enforcement")).toBeNull();
    expect(section.querySelectorAll('[data-icon="lock"]')).toHaveLength(0);
    expect(within(section as HTMLElement).queryAllByRole("checkbox")).toHaveLength(0);
    // A monospace textarea is the control.
    const ta = section.querySelector("textarea")!;
    expect(ta).toBeTruthy();
    expect(ta.className).toContain(styles.directiveEditor);
  });

  it("reports a toggled permission through onChange", () => {
    const onChange = vi.fn();
    render(<PermissionsSection permissions={PERMS} onChange={onChange} />);
    fireEvent.click(screen.getByLabelText("edit files"));
    expect(onChange).toHaveBeenCalledWith({ ...PERMS, edit_files: true });
  });
});

describe("DirectiveSection versions, diff and token estimate", () => {
  it("lists versions newest first and marks the current one", () => {
    renderDirective();
    const list = screen.getByLabelText("Directive versions");
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("v2 (current)");
    expect(rows[1].textContent).toContain("v1");
  });

  it("shows a line diff against the previous version on demand", () => {
    renderDirective();
    const list = screen.getByLabelText("Directive versions");
    fireEvent.click(within(list).getAllByRole("button", { name: "diff" })[0]);
    const diff = screen.getByLabelText("Diff for version 2");
    expect(diff.textContent).toContain("- line two");
    expect(diff.textContent).toContain("+ line two changed");
    expect(diff.textContent).toContain("  line one");
  });

  it("shows the live chars/4 token estimate", () => {
    renderDirective("x".repeat(400));
    expect(screen.getByLabelText("Token estimate").textContent).toBe("~100 tokens");
    expect(estimateTokens("")).toBe(0);
    expect(estimateTokens("ab")).toBe(1);
  });
});

describe("AutonomySection", () => {
  it("orders the four stops by increasing risk and confirms the top rung", () => {
    const onChange = vi.fn();
    render(<AutonomySection autonomy="approve_each" onChange={onChange} />);
    const radios = screen.getAllByRole("radio");
    expect(radios).toHaveLength(4);

    // Picking Auto does NOT apply immediately — the confirm gate appears.
    fireEvent.click(radios[3]);
    expect(onChange).not.toHaveBeenCalled();
    const confirm = screen.getByRole("alertdialog", { name: "Confirm Auto" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Switch to Auto" }));
    expect(onChange).toHaveBeenCalledWith("auto");

    // A lower rung applies without confirmation.
    fireEvent.click(radios[0]);
    expect(onChange).toHaveBeenCalledWith("suggest");
  });
});

describe("diffLines", () => {
  it("computes adds, deletes and context", () => {
    expect(diffLines("a\nb\nc", "a\nx\nc")).toEqual([
      { op: "same", text: "a" },
      { op: "del", text: "b" },
      { op: "add", text: "x" },
      { op: "same", text: "c" },
    ]);
  });
});
